package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"aliang.one/nursorgate/app/agentruntime"
	"aliang.one/nursorgate/app/http/services"
	"aliang.one/nursorgate/app/http/storage"
	"aliang.one/nursorgate/common/logger"
	auth "aliang.one/nursorgate/processor/auth"
	"aliang.one/nursorgate/processor/setup"
	"github.com/spf13/cobra"
)

var agentCmd = &cobra.Command{
	Use:    "agent",
	Short:  "Run the user-mode command agent",
	Hidden: true,
	RunE:   runAgent,
}

func init() {
	rootCmd.AddCommand(agentCmd)
}

func runAgent(cmd *cobra.Command, args []string) error {
	ensureUserAgentEnvironment()

	if err := auth.InitializeAuthPersistence(); err != nil {
		return fmt.Errorf("failed to initialize auth persistence: %w", err)
	}
	if err := storage.InitializeSoftwareConfigStore(); err != nil {
		return fmt.Errorf("failed to initialize software config persistence: %w", err)
	}
	if err := ApplyStartupConfigForMode(setup.RuntimeModeInteractive, configPath); err != nil {
		return fmt.Errorf("failed to initialize agent configuration: %w", err)
	}
	logAgentStartupConfig("agent_config_loaded")
	if err := InitializeUser(""); err != nil {
		logger.Warn(fmt.Sprintf("Failed to initialize user session for agent: %v", err))
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	logger.Info("Starting Aliang user-mode agent")
	return agentruntime.RunForeground(ctx)
}

func ensureUserAgentEnvironment() {
	_ = os.Setenv(services.AgentRuntimeEnv, "1")
	_ = os.Unsetenv("ALIANG_DATA_DIR")
	_ = os.Unsetenv("ALIANG_CACHE_DIR")
	_ = os.Unsetenv("ALIANG_LOG_DIR")
	_ = os.Unsetenv("ALIANG_SOCKET_PATH")
}
