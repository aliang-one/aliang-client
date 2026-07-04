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

	go func() {
		logger.Info(fmt.Sprintf("[AGENT-BOOT] startup_sync begin agent_server=%s runtime=user_agent", services.UserAgentOfflineStatus(nil).AgentServer))
		if err := services.GetSharedAgentService().SyncNow(); err != nil {
			logger.Warn(fmt.Sprintf("User agent startup sync failed: %v", err))
			logger.Warn(fmt.Sprintf("[AGENT-BOOT] startup_sync failed error=%v", err))
			return
		}
		logger.Info("[AGENT-BOOT] startup_sync success")
	}()

	logger.Info(fmt.Sprintf("User agent runtime listening on http://%s", config.DefaultUserAgentAddr))
	logger.Info(fmt.Sprintf("[AGENT-BOOT] local_server listening url=http://%s routes_registered=true", config.DefaultUserAgentAddr))
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
	return nil
}

func registerAgentRoutes(mux *http.ServeMux) {
	logger.Info("[AGENT-BOOT] local_server registering_routes routes=/api/agent/status,/api/agent/sync,/api/agent/enable,/api/agent/disable,/api/agent/tools,/api/agent/protocol,/api/agent/tools/launch,/api/agent/ai/approval-hook")
	agentHandler := handlers.NewAgentHandler(services.GetSharedAgentService())
	mux.HandleFunc("/api/agent/status", agentHandler.HandleStatus)
	mux.HandleFunc("/api/agent/sync", agentHandler.HandleSync)
	mux.HandleFunc("/api/agent/enable", agentHandler.HandleEnable)
	mux.HandleFunc("/api/agent/disable", agentHandler.HandleDisable)
	mux.HandleFunc("/api/agent/reconnect", agentHandler.HandleReconnect)
	mux.HandleFunc("/api/agent/tools", agentHandler.HandleTools)
	mux.HandleFunc("/api/agent/protocol", agentHandler.HandleProtocol)
	mux.HandleFunc("/api/agent/tools/launch", agentHandler.HandleLaunch)
	mux.HandleFunc("/api/agent/ai/approval-hook", agentHandler.HandleAIApprovalHook)
	mux.HandleFunc("/api/agent/sessions", agentHandler.HandleVibeSessions)
	mux.HandleFunc("/api/agent/session", agentHandler.HandleVibeSession)
	mux.HandleFunc("/api/agent/scan-directories", agentHandler.HandleScanDirectories)
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
