package cmd

import (
	"fmt"

	"aliang.one/nursorgate/common/logger"
	"aliang.one/nursorgate/common/version"
	"github.com/spf13/cobra"
)

// 添加其他有用的命令

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the version number",
	Long:  `Print the version number of aliang`,
	Run: func(cmd *cobra.Command, args []string) {
		if version.Version == "" {
			fmt.Println("aliang version: unknown")
		} else {
			fmt.Printf("aliang %s\n", version.Version)
		}
		if version.GitCommit != "" {
			fmt.Printf("commit: %s\n", version.GitCommit)
		}
		fmt.Printf("build: %s\n", version.BuildString())
	},
}

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Configuration management commands",
	Long:  `Manage configuration: load, save, validate, etc.`,
}

var configLoadCmd = &cobra.Command{
	Use:   "load [config-file]",
	Short: "Load configuration from file",
	Long:  `Load configuration from a JSON file and apply it to the system`,
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		configPath := args[0]
		logger.Info(fmt.Sprintf("Loading configuration from: %s", configPath))
		return LoadAndApplyConfig(configPath)
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)
	rootCmd.AddCommand(configCmd)

	configCmd.AddCommand(configLoadCmd)
}
