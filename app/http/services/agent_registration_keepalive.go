package services

// 会话外注册保活定时器：周期检查"enabled 且握有凭据且曾有 device_id，但注册
// 态丢失或实时链路未连"的设备，触发一次 SyncNow（不带显式 JWT——不得越权，
// 粘性禁用态由 shouldBlockBackgroundSyncLocked 在 SyncNow 内部拦截）。
//
// 这是 2026-10-08 事故（47 秒链路抖动 → 注册态翻转落盘 → 16 小时静默离线）
// 的兜底引擎：boot fallback（agentShouldBootFallbackReconnect）要求
// registered=true 才自连，事故残留态恰是 Registered=false——只有本循环能救。
//
// 不变量 1（未注册设备禁止自连）：生产代码 state.Enabled=true 唯一写点是
// 注册成功，因此 "Enabled && DeviceID 非空" 严格蕴含"曾注册过"，从未注册的
// 设备永远不会被本循环自连。
//
// 形态仿 usage tracker 的 stop-channel 模板：启停幂等、tick 与 sync 动作以
// 启动参数传入（goroutine 内不可变，测试可安全缩小而无 -race）；循环闭包不
// 捕获 receiver 做长命回写，规避 agent_remote_ws.go:442-509 的 SIGBUS 反模式。

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"aliang.one/nursorgate/common/logger"
)

// agentRegistrationKeepaliveTick 正常巡检周期。30s：恢复延迟可接受，远小于
// WS 会话重建窗口；仅坏态时才产生一次 register POST，负载可忽略。
const agentRegistrationKeepaliveTick = 30 * time.Second

var agentRegistrationKeepaliveRuntime struct {
	mu      sync.Mutex
	started bool
	stop    chan struct{}
	nudge   chan struct{} // buffered 1；非阻塞 nudge
}

// registrationKeepaliveNeeded 报告当前是否需要一次注册保活动作。凭据判定与
// remoteConnectionSnapshot（WS 循环 shouldRun 门禁）同源，避免两处口径分裂。
func (s *AgentService) registrationKeepaliveNeeded() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	authHeader := strings.TrimSpace(s.effectiveUserAuthorizationLocked(""))
	deviceID := s.state.DeviceID
	return s.state.Enabled &&
		authHeader != "" &&
		deviceID != "" &&
		(!s.state.Registered || !s.state.RemoteConnected)
}

// nudgeAgentRegistrationKeepalive 非阻塞唤醒保活循环；未启动时 no-op。
func nudgeAgentRegistrationKeepalive(reason string) {
	agentRegistrationKeepaliveRuntime.mu.Lock()
	defer agentRegistrationKeepaliveRuntime.mu.Unlock()
	if !agentRegistrationKeepaliveRuntime.started {
		return
	}
	logger.Debug(fmt.Sprintf("[AGENT-BOOT] registration_keepalive nudged reason=%s", reason))
	select {
	case agentRegistrationKeepaliveRuntime.nudge <- struct{}{}:
	default:
	}
}

// StartAgentRegistrationKeepalive 启动保活循环（幂等；仅 user-agent 运行时）。
func StartAgentRegistrationKeepalive() {
	if !IsUserAgentRuntime() {
		return
	}
	startAgentRegistrationKeepalive(GetSharedAgentService(), agentRegistrationKeepaliveTick,
		func(s *AgentService) error { return s.SyncNowWithUserContext("", "") })
}

// startAgentRegistrationKeepalive 内部启动入口，参数显式传入供测试缩小周期
// 与替换 sync 动作。
func startAgentRegistrationKeepalive(s *AgentService, tick time.Duration, syncFn func(*AgentService) error) {
	agentRegistrationKeepaliveRuntime.mu.Lock()
	if agentRegistrationKeepaliveRuntime.started {
		agentRegistrationKeepaliveRuntime.mu.Unlock()
		return
	}
	stop := make(chan struct{})
	nudge := make(chan struct{}, 1)
	agentRegistrationKeepaliveRuntime.started = true
	agentRegistrationKeepaliveRuntime.stop = stop
	agentRegistrationKeepaliveRuntime.nudge = nudge
	agentRegistrationKeepaliveRuntime.mu.Unlock()

	go runAgentRegistrationKeepalive(stop, nudge, tick, s, syncFn)
	logger.Info(fmt.Sprintf("[AGENT-BOOT] registration_keepalive started tick=%s", tick))
}

// runAgentRegistrationKeepalive 主循环。失败退避复用 handshakeRetryDelay 形状
// （2s→4s→…→60s 封顶，已被 agent_remote_ws_backoff_test.go 钉死）；成功或无需
// 动作即回到 tick 节奏。
func runAgentRegistrationKeepalive(stop <-chan struct{}, nudge <-chan struct{},
	tick time.Duration, s *AgentService, syncFn func(*AgentService) error) {
	timer := time.NewTimer(tick)
	defer timer.Stop()
	streak := 0
	for {
		select {
		case <-stop:
			return
		case <-nudge:
		case <-timer.C:
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		if s == nil || !s.registrationKeepaliveNeeded() {
			streak = 0
			timer.Reset(tick)
			continue
		}
		if err := syncFn(s); err != nil {
			streak++
			delay := handshakeRetryDelay(streak)
			logger.Warn(fmt.Sprintf("[AGENT-BOOT] registration_keepalive sync_failed streak=%d retry_in=%s error=%v", streak, delay, err))
			timer.Reset(delay)
			continue
		}
		streak = 0
		timer.Reset(tick)
	}
}

// StopAgentRegistrationKeepalive 停止保活循环（进程退出时调用）。
func StopAgentRegistrationKeepalive() {
	agentRegistrationKeepaliveRuntime.mu.Lock()
	defer agentRegistrationKeepaliveRuntime.mu.Unlock()
	if !agentRegistrationKeepaliveRuntime.started {
		return
	}
	close(agentRegistrationKeepaliveRuntime.stop)
	agentRegistrationKeepaliveRuntime.started = false
}

// ResetAgentRegistrationKeepaliveForTest 仅供测试：确保循环已停并清 started 位。
func ResetAgentRegistrationKeepaliveForTest() { StopAgentRegistrationKeepalive() }
