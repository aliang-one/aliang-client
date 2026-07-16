package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	httpServer "aliang.one/nursorgate/app/http"
	"aliang.one/nursorgate/app/http/services"
	"aliang.one/nursorgate/app/http/storage"
	"aliang.one/nursorgate/common/logger"
	"aliang.one/nursorgate/common/version"
	auth "aliang.one/nursorgate/processor/auth"
	"aliang.one/nursorgate/processor/config"
	"aliang.one/nursorgate/processor/rules"
	"aliang.one/nursorgate/processor/runtime"
	"aliang.one/nursorgate/processor/setup"
	"aliang.one/nursorgate/processor/statistic"
	"github.com/spf13/cobra"

	"aliang.one/nursorgate/internal/ipc"
)

var coreCmd = &cobra.Command{
	Use:    "core",
	Short:  "Run as core daemon (system service)",
	Hidden: true, // Users don't directly invoke this
	RunE:   runCore,
}

func init() {
	rootCmd.AddCommand(coreCmd)
}

func runCore(cmd *cobra.Command, args []string) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	return runCoreWithContext(ctx)
}

func runCoreWithContext(ctx context.Context) error {
	ensureCoreRuntimeEnvironment()
	auth.SetSessionOwnerProcess(true)

	logger.Info("========================================")
	logger.Info("Starting Aliang Core Daemon")
	logger.Info("========================================")

	// 1. Ensure system-level directories exist
	if err := setup.EnsureCoreDirs(); err != nil {
		writeStartupTrace("runCoreWithContext EnsureCoreDirs failed: %v", err)
		return fmt.Errorf("failed to ensure core directories: %w", err)
	}

	// 2. Initialize IPC Server
	ipcServer := ipc.NewServer()
	registerIPCHandlers(ipcServer)

	if err := ipcServer.Start(); err != nil {
		writeStartupTrace("runCoreWithContext ipcServer.Start failed: %v", err)
		return fmt.Errorf("failed to start IPC server: %w", err)
	}

	// 3. Initialize core subsystems (without starting HTTP)
	if err := initializeCoreSubsystems(); err != nil {
		writeStartupTrace("runCoreWithContext initializeCoreSubsystems failed: %v", err)
		ipcServer.Stop()
		return fmt.Errorf("failed to initialize core subsystems: %w", err)
	}

	logger.Info("Core daemon started successfully")
	logger.Info("IPC server listening, waiting for commands...")

	// On headless platforms (Linux) there is no tray client to request the
	// dashboard over IPC, so the Core daemon brings up the HTTP dashboard itself
	// to keep the deployment self-contained. macOS/Windows rely on the tray.
	startCoreDashboardIfHeadless()

	// 4. Wait for shutdown signal/context cancellation
	<-ctx.Done()
	logger.Info("Received shutdown signal, stopping core daemon...")

	// 5. Stop HTTP server if running
	httpServer.StopHttpServer()

	// 6. Stop proxy if running
	runService := services.GetSharedRunService()
	if runService.IsRunning() {
		runService.StopService()
	}

	// 7. Stop token refresh
	auth.StopTokenRefresh()

	// 8. Stop IPC server
	ipcServer.Stop()

	logger.Info("Core daemon stopped successfully")
	return nil
}

func ensureCoreRuntimeEnvironment() {
	if strings.TrimSpace(os.Getenv("ALIANG_DATA_DIR")) == "" {
		_ = os.Setenv("ALIANG_DATA_DIR", setup.CoreDataDir())
	}
	if strings.TrimSpace(os.Getenv("ALIANG_CACHE_DIR")) == "" {
		_ = os.Setenv("ALIANG_CACHE_DIR", setup.CoreDataDir())
	}
	if strings.TrimSpace(os.Getenv("ALIANG_LOG_DIR")) == "" {
		_ = os.Setenv("ALIANG_LOG_DIR", setup.CoreLogDir())
	}
	if socketPath := strings.TrimSpace(setup.CoreSocketPath()); socketPath != "" && strings.TrimSpace(os.Getenv("ALIANG_SOCKET_PATH")) == "" {
		_ = os.Setenv("ALIANG_SOCKET_PATH", socketPath)
	}
}

// initializeCoreSubsystems initializes core subsystems without starting HTTP server.
func initializeCoreSubsystems() error {
	logger.Info("Initializing core subsystems...")

	// Initialize auth persistence
	if err := auth.InitializeAuthPersistence(); err != nil {
		writeStartupTrace("initializeCoreSubsystems auth init failed: %v", err)
		return fmt.Errorf("failed to initialize auth persistence: %w", err)
	}

	// Initialize software config store
	if err := storage.InitializeSoftwareConfigStore(); err != nil {
		writeStartupTrace("initializeCoreSubsystems storage init failed: %v", err)
		return fmt.Errorf("failed to initialize software config store: %w", err)
	}

	// Load configuration (honor --config when provided by CLI/service args)
	if err := ApplyStartupConfigForMode(setup.RuntimeModeDaemon, configPath); err != nil {
		writeStartupTrace("initializeCoreSubsystems ApplyStartupConfig failed: %v", err)
		return fmt.Errorf("failed to initialize config: %w", err)
	}

	// Initialize startup state
	startupState := runtime.GetStartupState()
	initialStatus := determineCoreInitialStatus()
	startupState.SetStatus(initialStatus)
	logger.Info(fmt.Sprintf("Core initial status: %s", initialStatus))

	// Initialize user (try to load persisted info - InitializeUser handles this)
	if err := InitializeUser(""); err != nil {
		writeStartupTrace("initializeCoreSubsystems InitializeUser warning: %v", err)
		logger.Warn(fmt.Sprintf("Failed to initialize user: %v (continuing anyway)", err))
	}

	// Initialize global rule engine
	if err := initializeCoreRuleEngine(); err != nil {
		writeStartupTrace("initializeCoreSubsystems initializeCoreRuleEngine warning: %v", err)
		logger.Warn(fmt.Sprintf("Failed to initialize rule engine: %v (continuing anyway)", err))
	}
	logger.Info("Core subsystems initialized")
	return nil
}

func determineCoreInitialStatus() runtime.StartupStatus {
	hasLocalUserInfo, _ := auth.HasPersistedUserInfo()
	if hasLocalUserInfo {
		return runtime.READY
	}
	return runtime.UNCONFIGURED
}

func initializeCoreRuleEngine() error {
	ruleEngine := rules.GetEngine()
	if err := ruleEngine.Initialize(config.GetGlobalConfig()); err != nil {
		return fmt.Errorf("failed to initialize rule engine: %w", err)
	}
	logger.Info("Core rule engine initialized")
	return nil
}

// registerIPCHandlers registers all IPC handlers for Core commands.
func registerIPCHandlers(s *ipc.Server) {
	s.Register(ipc.ActionPing, handlePing)
	s.Register(ipc.ActionStartHTTP, handleStartHTTP)
	s.Register(ipc.ActionSyncAgentAuth, handleSyncAgentAuth)
	s.Register(ipc.ActionStopHTTP, handleStopHTTP)
	s.Register(ipc.ActionGetStatus, handleGetStatus)
	s.Register(ipc.ActionStartProxy, handleStartProxy)
	s.Register(ipc.ActionStopProxy, handleStopProxy)
	s.Register(ipc.ActionSwitchMode, handleSwitchMode)
	s.Register(ipc.ActionShutdown, handleShutdown)
}

var (
	currentAgentRegisterAuthorizationHeader = services.CurrentAgentRegisterAuthorizationHeader
	syncUserAgentAfterAuthWithRetry         = services.SyncUserAgentAfterAuthWithRetry
)

func handleSyncAgentAuth(raw json.RawMessage) (interface{}, error) {
	reason := "agent_started"
	if len(raw) > 0 {
		var args ipc.SyncAgentAuthArgs
		if err := json.Unmarshal(raw, &args); err != nil {
			return nil, fmt.Errorf("invalid sync_agent_auth args: %w", err)
		}
		if strings.TrimSpace(args.Reason) != "" {
			reason = strings.TrimSpace(args.Reason)
		}
	}
	if strings.TrimSpace(currentAgentRegisterAuthorizationHeader()) == "" {
		return map[string]interface{}{
			"status":  "skipped",
			"reason":  reason,
			"message": "no authenticated user session",
		}, nil
	}
	if err := syncUserAgentAfterAuthWithRetry(reason); err != nil {
		return nil, err
	}
	return map[string]interface{}{"status": "synced", "reason": reason}, nil
}

func handlePing(_ json.RawMessage) (interface{}, error) {
	return map[string]interface{}{
		"status":  "ok",
		"version": version.String(),
	}, nil
}

func handleStartHTTP(_ json.RawMessage) (interface{}, error) {
	logger.Info("[IPC] Starting HTTP dashboard...")

	// Check if already running
	if httpServer.IsServerRunning() {
		return map[string]interface{}{
			"port": httpServer.GetActualPort(),
			"url":  "http://127.0.0.1:" + httpServer.GetActualPort(),
		}, nil
	}

	// Start HTTP server
	if err := httpServer.StartHttpServer(); err != nil {
		return nil, fmt.Errorf("failed to start HTTP server: %w", err)
	}

	port := httpServer.GetActualPort()
	if port == "" {
		port = fmt.Sprintf("%d", config.DefaultManagementPort)
	}

	logger.Info(fmt.Sprintf("[IPC] HTTP dashboard started on port %s", port))
	return map[string]interface{}{
		"port": port,
		"url":  "http://127.0.0.1:" + port,
	}, nil
}

func handleStopHTTP(_ json.RawMessage) (interface{}, error) {
	logger.Info("[IPC] Stopping HTTP dashboard...")

	// Stop proxy first if running
	runService := services.GetSharedRunService()
	if runService.IsRunning() {
		logger.Info("[IPC] Stopping proxy before stopping HTTP...")
		runService.StopService()
	}

	// Stop HTTP server
	if err := httpServer.StopHttpServer(); err != nil {
		return nil, fmt.Errorf("failed to stop HTTP server: %w", err)
	}

	logger.Info("[IPC] HTTP dashboard stopped")
	return map[string]interface{}{
		"status": "stopped",
	}, nil
}

func handleGetStatus(_ json.RawMessage) (interface{}, error) {
	runService := services.GetSharedRunService()

	status := runService.GetStatus()
	status["http_enabled"] = httpServer.IsServerRunning()
	status["version"] = version.String()

	aiStatus := buildAIStatusPayload()
	if len(aiStatus) > 0 {
		status["ai_status"] = aiStatus
	}

	return status, nil
}

func buildAIStatusPayload() []map[string]interface{} {
	cfg := config.GetGlobalConfig()
	if cfg == nil || cfg.Customer == nil || len(cfg.Customer.AIRules) == 0 {
		return nil
	}

	aiSummary := statistic.GetDefaultAIActivityTracker().Summary()

	trafficSet := make(map[string]*statistic.AIActivityDetection, len(aiSummary.RecentProviderTraffic))
	for _, d := range aiSummary.RecentProviderTraffic {
		trafficSet[config.NormalizeAIProviderKey(d.ProviderKey)] = d
	}

	var result []map[string]interface{}
	for _, preset := range config.PresetAIRuleProviders {
		providerKey := config.NormalizeAIProviderKey(preset.Key)
		rule, exists := config.FindCustomerAIRule(cfg.Customer.AIRules, providerKey)
		if !exists || rule == nil || rule.Enble == nil || !*rule.Enble {
			continue
		}

		label := rule.Label
		if label == "" {
			label = preset.Label
		}

		detected := false
		active := false
		if d, ok := trafficSet[providerKey]; ok {
			detected = true
			active = d.Active
		}

		result = append(result, map[string]interface{}{
			"key":      providerKey,
			"label":    label,
			"enabled":  true,
			"detected": detected,
			"active":   active,
		})
	}
	return result
}

func handleStartProxy(_ json.RawMessage) (interface{}, error) {
	logger.Info("[IPC] Starting proxy...")

	runService := services.GetSharedRunService()
	result := runService.StartService()

	if status, ok := result["status"].(string); ok && status == "failed" {
		return nil, fmt.Errorf("start proxy failed: %v", result)
	}

	logger.Info("[IPC] Proxy started")
	return result, nil
}

func handleStopProxy(_ json.RawMessage) (interface{}, error) {
	logger.Info("[IPC] Stopping proxy...")

	runService := services.GetSharedRunService()
	result := runService.StopService()

	logger.Info("[IPC] Proxy stopped")
	return result, nil
}

func handleSwitchMode(args json.RawMessage) (interface{}, error) {
	var switchArgs ipc.SwitchModeArgs
	if err := json.Unmarshal(args, &switchArgs); err != nil {
		return nil, fmt.Errorf("invalid switch mode args: %w", err)
	}

	logger.Info(fmt.Sprintf("[IPC] Switching mode to %s...", switchArgs.Mode))

	runService := services.GetSharedRunService()
	result := runService.SwitchMode(switchArgs.Mode)

	if status, ok := result["status"].(string); ok && status == "failed" {
		return nil, fmt.Errorf("switch mode failed: %v", result)
	}

	logger.Info(fmt.Sprintf("[IPC] Mode switched to %s", switchArgs.Mode))
	return result, nil
}

func handleShutdown(_ json.RawMessage) (interface{}, error) {
	logger.Info("[IPC] Shutdown requested, stopping all services...")

	// Stop proxy
	runService := services.GetSharedRunService()
	if runService.IsRunning() {
		runService.StopService()
	}

	// Stop HTTP server
	httpServer.StopHttpServer()

	// Stop token refresh
	auth.StopTokenRefresh()

	logger.Info("[IPC] All services stopped, exiting...")
	os.Exit(0)
	return nil, nil
}
