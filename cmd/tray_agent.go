//go:build darwin

package cmd

import (
	"aliang.one/nursorgate/app/tray"
	"aliang.one/nursorgate/common/logger"
	"github.com/spf13/cobra"
)

var trayAgentCmd = &cobra.Command{
	Use:    "tray-agent",
	Short:  "Start the menu bar companion for a background service",
	Hidden: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		logger.Info("Starting Aliang menu bar companion...")
		tray.RunCompanion()
		return nil
	},
}

func init() {
	rootCmd.AddCommand(trayAgentCmd)
}
