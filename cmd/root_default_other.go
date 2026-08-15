//go:build !windows && !darwin && !linux

package cmd

import "github.com/spf13/cobra"

func runDefaultRoot(cmd *cobra.Command, args []string) error {
	return runCommandLineDefaultRoot(cmd, args)
}

func runCommandLineDefaultRoot(cmd *cobra.Command, args []string) error {
	return runStart(cmd, args)
}
