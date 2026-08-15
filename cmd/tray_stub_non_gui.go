//go:build !darwin && !windows

package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var trayCmd = &cobra.Command{
	Use:   "tray",
	Short: "Start system tray application",
	Long: `Start the aliang system tray application.

This build does not include desktop tray support on this platform.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return fmt.Errorf("tray mode is not available in this Linux build; use `aliang start` for the CLI/server mode")
	},
}

func init() {
	rootCmd.AddCommand(trayCmd)
}
