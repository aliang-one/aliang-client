package agentruntime

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"aliang.one/nursorgate/app/http/common"
	"aliang.one/nursorgate/app/http/handlers"
	"aliang.one/nursorgate/app/http/services"
	"aliang.one/nursorgate/common/logger"
	"aliang.one/nursorgate/processor/config"
)

var (
	serverMu sync.Mutex
	server   *http.Server
	running  bool
)

func RunForeground(ctx context.Context) error {
	// 进程级独占锁：保证全局只有一个 user-agent。拿不到锁说明已有实例在跑
	// （如 EnsureStarted 在本进程启动前刚 spawn 了一份，或用户手动双开）。
	// 干净退出、不 listen、不报错 —— EnsureStarted 探 56433 会看到先到的实例。
	lockFile, err := acquireAgentLock()
	if err != nil {
		logger.Info(fmt.Sprintf("[AGENT-BOOT] foreground_runtime another_instance_running exiting: %v", err))
		return nil
	}
	defer lockFile.Close()

	logger.Info("[AGENT-BOOT] foreground_runtime starting")
	if err := StartLocalServer(); err != nil {
		return err
	}
	<-ctx.Done()
	logger.Info("[AGENT-BOOT] foreground_runtime stopping")
	return StopLocalServer()
}

func StartLocalServer() error {
	serverMu.Lock()
	defer serverMu.Unlock()

	if running {
		logger.Info(fmt.Sprintf("[AGENT-BOOT] local_server already_running addr=%s", config.DefaultUserAgentAddr))
		return nil
	}

	mux := http.NewServeMux()
	registerAgentRoutes(mux)

	listener, err := net.Listen("tcp", config.DefaultUserAgentAddr)
	if err != nil {
		return fmt.Errorf("failed to listen on user agent address %s: %w", config.DefaultUserAgentAddr, err)
	}
	services.SetAgentAIApprovalHookBaseURL("http://" + config.DefaultUserAgentAddr)

	srv := &http.Server{
		Handler:           loopbackOnly(mux),
		ReadHeaderTimeout: 5 * time.Second,
	}
	server = srv
	running = true

	// 用量采集循环不受本地 HTTP 服务器影响，此处刻意不联动停止（远程 WS 独立拨号）。
	go func() {
		err := srv.Serve(listener)
		if err != nil && err != http.ErrServerClosed {
			logger.Error(fmt.Sprintf("User agent HTTP server failed: %v", err))
		}

		serverMu.Lock()
		defer serverMu.Unlock()
		if server == srv {
			server = nil
			running = false
		}
	}()

	logger.Info(fmt.Sprintf("User agent runtime listening on http://%s", config.DefaultUserAgentAddr))
	logger.Info(fmt.Sprintf("[AGENT-BOOT] local_server listening url=http://%s routes_registered=true", config.DefaultUserAgentAddr))
	services.StartUsageTrackerRuntime()
	services.StartAgentRegistrationKeepalive()
	services.WarmAgentAuthorizedExecutionDirectories()
	logger.Info("[AGENT-BOOT] local_server awaiting session owner sync")
	return nil
}

func StopLocalServer() error {
	serverMu.Lock()
	srv := server
	serverMu.Unlock()

	if srv == nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		return err
	}
	services.StopUsageTrackerRuntime()
	services.StopAgentRegistrationKeepalive()
	return nil
}

func registerAgentRoutes(mux *http.ServeMux) {
	logger.Info("[AGENT-BOOT] local_server registering_routes routes=/api/agent/status,/api/agent/sync,/api/agent/enable,/api/agent/auth-recover,/api/agent/disable,/api/agent/tools,/api/agent/protocol,/api/agent/tools/launch,/api/agent/ai/approval-hook,/api/agent/ai/stream,/api/agent/peer/*")
	agentHandler := handlers.NewAgentHandler(services.GetSharedAgentService())
	mux.HandleFunc("/api/agent/status", agentHandler.HandleStatus)
	mux.HandleFunc("/api/agent/sync", agentHandler.HandleSync)
	mux.HandleFunc("/api/agent/enable", agentHandler.HandleEnable)
	// auth_expired 自愈链（2026-10-29 修复）：core 在 token 刷新成功后经
	// requestLocalUserAgentRecoverAfterAuthExpired 直连 POST 本端点，仪表盘侧
	// HandleAuthRecover 的 proxyIfNeeded 也转发到本 runtime。缺注册=404 透传，
	// auth_expired 终态在 core+user agent 双进程拓扑下永不自愈（双注册教训，
	// 2026-10-21 liang-dev 同族）。
	mux.HandleFunc("/api/agent/auth-recover", agentHandler.HandleAuthRecover)
	mux.HandleFunc("/api/agent/disable", agentHandler.HandleDisable)
	mux.HandleFunc("/api/agent/reconnect", agentHandler.HandleReconnect)
	mux.HandleFunc("/api/agent/session-event", agentHandler.HandleSessionEvent)
	mux.HandleFunc("/api/agent/tools", agentHandler.HandleTools)
	mux.HandleFunc("/api/agent/protocol", agentHandler.HandleProtocol)
	mux.HandleFunc("/api/agent/tools/launch", agentHandler.HandleLaunch)
	// 设备互传本地触发（D13）：核心侧 routes.go 已有同名注册，缺这里会 404
	//（双注册教训，2026-10-21 liang-dev 实测）。
	mux.HandleFunc("/api/agent/transfers", agentHandler.HandleTransferCreate)
	// 设备互访本地面（peer-link / `aliang mcp` stdio 面的 HTTP 后端）：核心侧
	// routes.go 同名注册透传（双注册教训同上，缺任一侧=404）。
	mux.HandleFunc("/api/agent/peer/exec", agentHandler.HandlePeerExec)
	mux.HandleFunc("/api/agent/peer/fetch-file", agentHandler.HandlePeerFetchFile)
	mux.HandleFunc("/api/agent/peer/devices", agentHandler.HandlePeerDevices)
	mux.HandleFunc("/api/agent/peer/device-info", agentHandler.HandlePeerDeviceInfo)
	mux.HandleFunc("/api/agent/peer/note", agentHandler.HandlePeerNote)
	mux.HandleFunc("/api/agent/ai/approval-hook", agentHandler.HandleAIApprovalHook)
	// 仪表盘聊天 WS（2026-10-28 统一）：core 把 /api/agent/ai/stream 透传到本
	// runtime，由本地 AI 管理器跑（journal/清单与手机同源）。缺注册=404 透传
	// （2026-10-21 liang-dev 双注册教训）。
	mux.HandleFunc("/api/agent/ai/stream", agentHandler.HandleAIStream)
	mux.HandleFunc("/mcp/aliang/", agentHandler.HandleToolAskMCP)
	mux.HandleFunc("/api/agent/sessions", agentHandler.HandleVibeSessions)
	mux.HandleFunc("/api/agent/session", agentHandler.HandleVibeSession)
	mux.HandleFunc("/api/agent/scan-directories", agentHandler.HandleScanDirectories)
	// 仪表盘数据面端点：核心进程会把这两个请求代理到本 runtime，缺注册会让
	// 仪表盘 Agent 页的「远程终端」列表和「进行中」徽章永远拿不到数据
	// （2026-10-21 liang-dev 实测 404 透传）。
	mux.HandleFunc("/api/agent/runtime-sessions", agentHandler.HandleRuntimeSessions)
	mux.HandleFunc("/api/agent/terminal/{sessionId}/scrollback", agentHandler.HandleTerminalScrollback)
}

func loopbackOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host = r.RemoteAddr
		}
		ip := net.ParseIP(strings.TrimSpace(host))
		if ip == nil || !ip.IsLoopback() {
			common.ErrorForbidden(w, "user agent only accepts loopback requests")
			return
		}
		next.ServeHTTP(w, r)
	})
}
