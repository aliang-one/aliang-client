//go:build darwin || windows

package cmd

import (
	"fmt"

	httpServer "aliang.one/nursorgate/app/http"
	"aliang.one/nursorgate/app/http/services"
	"aliang.one/nursorgate/app/tray"
	"aliang.one/nursorgate/common/logger"
	auth "aliang.one/nursorgate/processor/auth"
	"aliang.one/nursorgate/processor/config"
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

	// SSH 入口启动恢复:配置已加载,按期望态 reconcile——重启后自动恢复
	// 已启用入口(失败仅日志不阻断 boot)。与配置提交协调路径共用
	// services.ReconcileSSHIngressFromConfig 防漂移;start.go runStart 与
	// core.go initializeCoreSubsystems 同款接线(三条 core 启动路径齐)。
	services.ReconcileSSHIngressFromConfig(config.GetGlobalConfig())
	// 统一入口绑定防漂移(同 core.go/start.go):56432 绑定面随 mesh 总开关
	// 变化,代理已在跑则按当前配置校正,未跑则空操作。
	services.ReconcileUnifiedIngressBinding(config.GetGlobalConfig())

	if err := httpServer.StartHttpServer(); err != nil {
		return fmt.Errorf("failed to start dashboard server for tray mode: %w", err)
	}

	// Start the tray application
	// This will block until the user quits
	tray.Run()

	return nil
}
