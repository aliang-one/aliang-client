//go:build windows

package cmd

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"aliang.one/nursorgate/common/logger"
	"aliang.one/nursorgate/processor/config"
	"aliang.one/nursorgate/processor/setup"
	"golang.org/x/sys/windows/svc"
)

// serviceStopDeadline 是 SCM 停止请求后等待 core 优雅退出的兜底上界。
const serviceStopDeadline = 15 * time.Second

// MaybeRunAsWindowsService detects Windows service context and runs service dispatcher when needed.
// Returns handled=true when running as a Windows service.
func MaybeRunAsWindowsService() (handled bool, err error) {
	isService, err := svc.IsWindowsService()
	if err != nil {
		writeStartupTrace("MaybeRunAsWindowsService detection failed: %v", err)
		return false, fmt.Errorf("failed to detect windows service context: %w", err)
	}
	if !isService {
		return false, nil
	}

	logger.Info("Detected Windows service context, starting service dispatcher...")
	if err := svc.Run(setup.GetServiceName(), &aliangWindowsService{}); err != nil {
		writeStartupTrace("svc.Run failed: %v", err)
		return true, fmt.Errorf("windows service runtime failed: %w", err)
	}
	return true, nil
}

type aliangWindowsService struct{}

func (s *aliangWindowsService) Execute(args []string, req <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	const accepted = svc.AcceptStop | svc.AcceptShutdown

	if err := applyWindowsServiceRuntimeArgs(args); err != nil {
		logger.Error("Invalid Windows service runtime arguments", "error", err)
		return false, 1
	}

	status <- svc.Status{State: svc.StartPending}

	// 服务态 stderr 是黑洞(SCM 不给句柄),panic/runtime fatal 的崩溃栈全
	// 蒸发——2026-11-23 win196 158 次静默崩溃只能靠控制台复现取证。一切
	// 之前先落 crash 文件,以后每次死亡都自带栈。
	redirectCrashOutput()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 存活心跳:随 Execute 生命周期起停(deferred cancel 兜全部返回路径),
	// 文件停在最后一次心跳 = 死亡时刻上界。取证协议见 service_heartbeat.go。
	go runHeartbeat(ctx, filepath.Join(setup.CoreDataDir(), "core.heartbeat"), serviceHeartbeatInterval)

	done := make(chan error, 1)
	go func() {
		done <- runCoreWithContext(ctx)
	}()

	status <- svc.Status{State: svc.Running, Accepts: accepted}

	for {
		select {
		case c := <-req:
			switch c.Cmd {
			case svc.Interrogate:
				status <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				status <- svc.Status{State: svc.StopPending, Accepts: accepted}
				cancel()
				// 停止链封顶:下游任何一环挂死(TUN teardown、IPC 在途
				// 连接、半起 dashboard 抢锁)都不能让 SCM 永远停在
				// StopPending——2026-11-16 生产 StopPending>2min 最终靠
				// taskkill /F 收场。超时 fail-loud 后返回非零,服务管理器
				// 随即结束进程,与强杀等价但变为有界+有日志;下次启动会
				// 重新配置路由,wintun 适配器按 GUID 复用可容忍残留。
				select {
				case err := <-done:
					if err != nil {
						logger.Error("Windows service core loop exited with error", "error", err)
						return false, 1
					}
					status <- svc.Status{State: svc.Stopped}
					return false, 0
				case <-time.After(serviceStopDeadline):
					writeStartupTrace("windows service stop deadline (%s) exceeded; giving up, SCM will terminate the process", serviceStopDeadline)
					logger.Error(fmt.Sprintf("Windows service stop deadline (%s) exceeded; giving up, SCM will terminate the process", serviceStopDeadline))
					return false, 1
				}
			default:
			}
		case err := <-done:
			writeStartupTrace("windows service core goroutine exited err=%v", err)
			if err != nil {
				logger.Error("Windows service core loop exited unexpectedly", "error", err)
				return false, 1
			}
			status <- svc.Status{State: svc.Stopped}
			return false, 0
		}
	}
}

func applyWindowsServiceRuntimeArgs(args []string) error {
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--config", "-c":
			if i+1 < len(args) {
				configPath = args[i+1]
				i++
			}
		case "--host":
			if i+1 >= len(args) {
				return fmt.Errorf("--host requires a value")
			}
			if err := config.SetServiceBindHost(args[i+1]); err != nil {
				return err
			}
			i++
		}
	}
	return nil
}
