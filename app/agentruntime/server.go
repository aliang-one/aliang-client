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
	logger.Info("[AGENT-BOOT] local_server registering_routes routes=/api/agent/health,/api/agent/status,/api/agent/bind/start,/api/agent/bind/status,/api/agent/disable,/api/agent/tools,/api/agent/tools/launch")
	agentHandler := handlers.NewAgentHandler(services.GetSharedAgentService())
	mux.HandleFunc("/api/agent/health", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			common.Error(w, http.StatusMethodNotAllowed, "Method not allowed", nil)
			return
		}
		common.Success(w, map[string]interface{}{
			"status": "ok",
			"url":    "http://" + config.DefaultUserAgentAddr,
		})
	})
	mux.HandleFunc("/api/agent/status", agentHandler.HandleStatus)
	mux.HandleFunc("/api/agent/bind/start", agentHandler.HandleBindStart)
	mux.HandleFunc("/api/agent/bind/status", agentHandler.HandleBindStatus)
	mux.HandleFunc("/api/agent/disable", agentHandler.HandleDisable)
	mux.HandleFunc("/api/agent/tools", agentHandler.HandleTools)
	mux.HandleFunc("/api/agent/tools/launch", agentHandler.HandleLaunch)
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
