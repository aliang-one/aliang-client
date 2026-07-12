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
	auth.SetSessionOwnerProcess(false)

	if err := storage.InitializeSoftwareConfigStore(); err != nil {
		return fmt.Errorf("failed to initialize software config persistence: %w", err)
	}
	if err := ApplyStartupConfigForMode(setup.RuntimeModeInteractive, configPath); err != nil {
		return fmt.Errorf("failed to initialize agent configuration: %w", err)
	}
	logAgentStartupConfig("agent_config_loaded")
	// The user-agent is deliberately not an auth-session owner. The dashboard/core
	// process restores and refreshes the session, then forwards the current access
	// token through /api/agent/sync.

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
