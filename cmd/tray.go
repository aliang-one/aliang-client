//go:build darwin || windows

package cmd

import (
	"fmt"

	httpServer "aliang.one/nursorgate/app/http"
	"aliang.one/nursorgate/app/tray"
	"aliang.one/nursorgate/common/logger"
	auth "aliang.one/nursorgate/processor/auth"
	"aliang.one/nursorgate/processor/runtime"
	"aliang.one/nursorgate/processor/setup"
	"github.com/spf13/cobra"
)

var trayCmd = &cobra.Command{
	Use:   "tray",
	Short: "Start system tray application",
	Long: `Start the aliang system tray application.

This will create a system tray icon with a menu to control the server.
Supports Linux, macOS, and Windows.

Examples:
  # Start with system tray
  aliang tray
  
  # Start with config file and tray
  aliang tray --config ./config.json`,
	RunE: runTray,
}

func init() {
	rootCmd.AddCommand(trayCmd)
}

func runTray(cmd *cobra.Command, args []string) error {
	logger.Info("Starting Aliang in system tray mode...")
	auth.SetSessionOwnerProcess(true)

	guard, acquired, err := acquireSingleInstanceGuard()
	if err != nil {
		return err
	}
	if !acquired {
		logger.Info("Aliang is already running, exiting duplicate launch request.")
		return nil
	}
	defer guard.Close()

	// Load startup configuration using the same precedence as start command:
	// --config > ./config.json > ~/.aliang/config.json > database snapshot > embedded default
	if err := ApplyStartupConfigForMode(setup.RuntimeModeInteractive, configPath); err != nil {
		return err
	}

	startupState := runtime.GetStartupState()
	initialStatus := determineInitialStartupStatus(token)
	startupState.SetStatus(initialStatus)
	logger.Info(fmt.Sprintf("Initial tray startup status: %s", initialStatus))

	if err := InitializeUser(token); err != nil {
		return err
	}

	if err := InitializeGlobalRuleEngine(); err != nil {
		logger.Error(fmt.Sprintf("Failed to initialize global rule engine for tray mode: %v", err))
	}

	if err := httpServer.StartHttpServer(); err != nil {
		return fmt.Errorf("failed to start dashboard server for tray mode: %w", err)
	}

	// Start the tray application
	// This will block until the user quits
	tray.Run()

	return nil
}
