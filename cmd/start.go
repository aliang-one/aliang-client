package cmd

import (
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	httpServer "aliang.one/nursorgate/app/http"
	"aliang.one/nursorgate/app/http/storage"
	"aliang.one/nursorgate/common/logger"
	auth "aliang.one/nursorgate/processor/auth"
	"aliang.one/nursorgate/processor/config"
	"aliang.one/nursorgate/processor/geoip"
	"aliang.one/nursorgate/processor/rules"
	"aliang.one/nursorgate/processor/runtime"
	"aliang.one/nursorgate/processor/setup"
	"github.com/spf13/cobra"
)

var startCmd = &cobra.Command{
	Use:   "start",
	Short: "Start the aliang server",
	Long: `Start the aliang server with configuration from file or default embedded config.

Examples:
  # Start with local config file and activate user with token
  aliang start --config ./config.json --token your-token-here

  # Start with local config file (use locally saved user info)
  aliang start --config ./config.json

  # Start with default embedded configuration and activate user with token
  aliang start --token your-token-here

  # Start with default embedded configuration (use locally saved user info)
  aliang start`,
	RunE: runStart,
}

func init() {
	rootCmd.AddCommand(startCmd)
	// Parameters are inherited from root command via PersistentFlags
}

func ApplyDefaultConfig() error {
	logger.Info("Loading default embedded configuration...")
	defaultConfigBytes := GetDefaultConfigBytes()
	config, err := LoadConfigFromBytes(defaultConfigBytes)
	if err != nil {
		return fmt.Errorf("failed to load default config: %w", err)
	}

	if err := ApplyConfig(config); err != nil {
		return fmt.Errorf("failed to apply default config: %w", err)
	}

	// Mark that we're using the default configuration
	setUseDefaultConfig(true)

	logger.Info("Default configuration applied successfully")
	return nil
}

func runStart(cmd *cobra.Command, args []string) error {
	guard, acquired, err := acquireSingleInstanceGuard()
	if err != nil {
		return err
	}
	if !acquired {
		logger.Info("Aliang is already running, exiting duplicate launch request.")
		return nil
	}
	defer guard.Close()

	// Get the global startup state
	startupState := runtime.GetStartupState()

	if err := auth.InitializeAuthPersistence(); err != nil {
		return fmt.Errorf("failed to initialize auth persistence: %w", err)
	}
	if err := storage.InitializeSoftwareConfigStore(); err != nil {
		return fmt.Errorf("failed to initialize software config persistence: %w", err)
	}

	if err := ApplyStartupConfigForMode(setup.RuntimeModeInteractive, configPath); err != nil {
		return fmt.Errorf("failed to initialize startup configuration: %w", err)
	}

	// Determine initial startup status based on login state
	// Status reflects whether the system is ready for proxy operations
	initialStatus := determineInitialStartupStatus(token)
	startupState.SetStatus(initialStatus)
	logger.Info(fmt.Sprintf("Initial startup status: %s", initialStatus))

	// Initialize user info (activate with token or load locally saved info)
	// This will transition the startup state based on success/failure
	// Note: Returns error only if token activation fails, causing startup to fail
	if err := InitializeUser(token); err != nil {
		return err
	}

	// ✅ GLOBAL Rule Engine Initialization
	// This should be done once at startup, NOT separately in HTTP and TUN modes
	// InitializeGlobalRuleEngine handles:
	// 1. Rule engine initialization (singleton)
	// 2. GeoIP database loading (if enabled in config)
	if err := InitializeGlobalRuleEngine(); err != nil {
		logger.Error(fmt.Sprintf("Failed to initialize global rule engine: %v", err))
		// Don't fail startup - system can run with default configuration
	}

	// 启动服务器
	logger.Info("Starting aliang server...")

	// 启动 HTTP 服务器（包含代理注册中心初始化）
	// HTTP server always starts, but API requests are gated by startup status middleware
	if err := httpServer.StartHttpServer(); err != nil {
		return fmt.Errorf("failed to start HTTP server: %w", err)
	}

	// 等待信号并优雅关闭
	logger.Info("Server started successfully. Press Ctrl+C to stop.")

	// 设置信号处理器
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// 等待信号
	<-sigChan
	logger.Info("Received shutdown signal, stopping server...")

	// 优雅关闭：停止Token定时刷新
	auth.StopTokenRefresh()

	logger.Info("Server stopped successfully.")
	return nil
}

// determineInitialStartupStatus determines the initial startup status based on:
// 1. Whether a token is provided via --token flag
// 2. Whether local user information exists
// 3. Whether using default configuration
//
// The startup status indicates system readiness:
// - UNCONFIGURED: No token, no local user info → system awaiting configuration
// - CONFIGURING: Token provided → system attempting activation and fetch
// - READY: Has local user info AND using default config → system ready (if fetch succeeds)
// - CONFIGURED: Has local user info BUT NOT using default config → needs fetch
func determineInitialStartupStatus(tokenFlag string) runtime.StartupStatus {
	hasLocalUserInfo := false
	hasLocalUserInfo, localUserErr := auth.HasPersistedUserInfo()
	if localUserErr != nil {
		logger.Warn(fmt.Sprintf("Failed to inspect persisted auth session: %v", localUserErr))
		hasLocalUserInfo = false
	}

	if strings.TrimSpace(tokenFlag) != "" {
		logger.Debug("Token provided via --token, status: CONFIGURING")
		return runtime.CONFIGURING
	}

	if hasLocalUserInfo {
		if IsUsingDefaultConfig() {
			logger.Debug("Local user info found with default config, status: READY")
			return runtime.READY
		}
		logger.Debug("Local user info found with custom config, status: CONFIGURED")
		return runtime.CONFIGURED
	}

	logger.Info("No token and no local user info found, status: UNCONFIGURED")
	return runtime.UNCONFIGURED
}

// ===== Test-Only Exports =====
// These functions are exported for testing purposes only

// ResetGlobalStartupStateForTest resets the startup state singleton for testing
// This allows tests to run in isolation without state pollution
func ResetGlobalStartupStateForTest() {
	runtime.ResetGlobalStartupStateForTest()
}

// DetermineInitialStartupStatusForTest exports the internal function for testing
func DetermineInitialStartupStatusForTest(tokenFlag string) runtime.StartupStatus {
	return determineInitialStartupStatus(tokenFlag)
}

// InitializeGlobalRuleEngine initializes the global rule engine once at startup
// This is the ONLY place where rule engine should be initialized
// Replaces duplicate initialization in:
// - app/http/server.go:initializeRuleEngine()
// - inbound/tun/runner/start.go:initializeRuleEngineForTUN()
func InitializeGlobalRuleEngine() error {
	logger.Info("========================================")
	logger.Info("Global Rule Engine Initialization")
	logger.Info("========================================")

	// Step 1: Initialize Rule Engine (singleton)
	logger.Info("Step 1: Initializing rule engine...")
	ruleEngine := rules.GetEngine()
	if err := ruleEngine.Initialize(config.GetGlobalConfig()); err != nil {
		return fmt.Errorf("failed to initialize rule engine: %w", err)
	}
	logger.Info("✓ Rule engine initialized")

	// Step 2: GeoIP routing is disabled in simplified mode
	logger.Info("Step 2: GeoIP routing disabled")
	geoip.GetService().Disable()

	logger.Info("========================================")
	logger.Info("✅ Global Rule Engine Initialization Complete")
	logger.Info("========================================")

	return nil
}

// initializeGeoIPDatabase loads the GeoIP database from default location
// Default path: <state-dir>/geoip/GeoLite2-Country.mmdb
func initializeGeoIPDatabase() error {
	geoipPath, err := geoip.DefaultDatabasePath()
	if err != nil {
		return fmt.Errorf("failed to resolve GeoIP database path: %w", err)
	}

	// Load database
	logger.Info(fmt.Sprintf("Loading GeoIP database from: %s", geoipPath))
	geoipService := geoip.GetService()
	if err := geoipService.LoadDatabase(geoipPath); err != nil {
		return fmt.Errorf("failed to load GeoIP database: %w", err)
	}

	logger.Info(fmt.Sprintf("✓ GeoIP database loaded from %s", geoipPath))
	return nil
}
