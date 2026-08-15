package cmd

import (
	"fmt"
	"net"
	"os"
	"runtime/debug"

	"aliang.one/nursorgate/common/logger"
	"aliang.one/nursorgate/processor/config"
	"github.com/spf13/cobra"
)

// Global flags for root command (persistent across all subcommands)
var (
	configPath  string
	token       string
	serviceHost = config.DefaultServiceBindHost
)

var rootCmd = &cobra.Command{
	Use:   "aliang",
	Short: "Aliang is a tool for managing your aliang server",
	Long:  `Aliang is a tool for managing your aliang server`,
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		return configureServiceBindHost(serviceHost)
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		if maybeRunAppBundleCompanion() {
			return nil
		}
		return runDefaultRoot(cmd, args)
	},
}

func init() {
	// 配置文件路径
	rootCmd.PersistentFlags().StringVarP(&configPath, "config", "c", "", "Path to configuration file (e.g., ./config.json)")

	// Token（用于激活用户）
	rootCmd.PersistentFlags().StringVarP(&token, "token", "t", "", "Token for user activation")

	// 管理面板和 HTTP 代理默认只监听本机；只有显式指定才会对外暴露。
	rootCmd.PersistentFlags().StringVar(&serviceHost, "host", config.DefaultServiceBindHost, "Bind host for management (56431) and HTTP proxy (56432)")
}

func configureServiceBindHost(host string) error {
	if err := config.SetServiceBindHost(host); err != nil {
		return err
	}
	if ip := net.ParseIP(config.ServiceBindHost()); ip != nil && !ip.IsLoopback() {
		logger.Warn("Management and HTTP proxy services are exposed on a non-loopback interface", "host", ip.String())
	}
	return nil
}

func Execute() {
	defer func() {
		if recovered := recover(); recovered != nil {
			err := fmt.Errorf("panic: %v", recovered)
			logger.Error(fmt.Sprintf("Aliang panicked: %v\n%s", recovered, debug.Stack()))
			notifyExecuteError(err)
			os.Exit(1)
		}
	}()

	if err := rootCmd.Execute(); err != nil {
		logger.Error(fmt.Sprintf("Aliang command failed: %v", err))
		notifyExecuteError(err)
		os.Exit(1)
	}
}
