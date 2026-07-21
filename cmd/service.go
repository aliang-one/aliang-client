package cmd

import (
	"fmt"
	"runtime"

	"aliang.one/nursorgate/app/http/services"
	"aliang.one/nursorgate/common/logger"
	"aliang.one/nursorgate/processor/config"
	"aliang.one/nursorgate/processor/setup"
	"github.com/spf13/cobra"
)

var (
	// 服务安装标志
	serviceSystemWide bool
	serviceStartNow   bool
)

var serviceCmd = &cobra.Command{
	Use:   "service",
	Short: "Manage system service",
	Long: `Install, uninstall, start, stop, and manage the application as a system service.

This command supports multiple platforms:
  - macOS: LaunchDaemon (system) or LaunchAgent (user)
  - Linux: systemd (system or user)
  - Windows: Windows Service (requires Administrator)

Examples:
  # Install as system service (requires root/admin)
  sudo aliang service install --system-wide --config /etc/aliang/config.json

  # Explicitly expose management and HTTP proxy ports on all IPv4 interfaces
  sudo aliang service install --system-wide --host 0.0.0.0

  # Install as user service
  aliang service install --config ~/.aliang/config.json

  # Start the service
  sudo aliang service start

  # Check service status
  aliang service status

  # Stop the service
  sudo aliang service stop

  # Uninstall the service
  sudo aliang service uninstall`,
}

var serviceInstallCmd = &cobra.Command{
	Use:   "install",
	Short: "Install as system service",
	Long: `Install aliang as a system service.

System-wide installation (requires root/admin):
  sudo aliang service install --system-wide --config /etc/aliang/config.json

User-level installation:
  aliang service install --config ~/.aliang/config.json`,
	RunE: runServiceInstall,
}

var serviceUninstallCmd = &cobra.Command{
	Use:   "uninstall",
	Short: "Uninstall system service",
	Long:  `Remove aliang from system services.`,
	RunE:  runServiceUninstall,
}

var serviceStartCmd = &cobra.Command{
	Use:   "start",
	Short: "Start the service",
	Long:  `Start the aliang service.`,
	RunE:  runServiceStart,
}

var serviceStopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop the service",
	Long:  `Stop the aliang service.`,
	RunE:  runServiceStop,
}

var serviceRestartCmd = &cobra.Command{
	Use:   "restart",
	Short: "Restart the service",
	Long:  `Restart the aliang service.`,
	RunE:  runServiceRestart,
}

var serviceStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Check service status",
	Long:  `Show the current status of aliang service.`,
	RunE:  runServiceStatus,
}

func init() {
	rootCmd.AddCommand(serviceCmd)

	// 添加子命令
	serviceCmd.AddCommand(serviceInstallCmd)
	serviceCmd.AddCommand(serviceUninstallCmd)
	serviceCmd.AddCommand(serviceStartCmd)
	serviceCmd.AddCommand(serviceStopCmd)
	serviceCmd.AddCommand(serviceRestartCmd)
	serviceCmd.AddCommand(serviceStatusCmd)

	// install 命令的标志
	serviceInstallCmd.Flags().BoolVar(&serviceSystemWide, "system-wide", false, "Install as system-wide service (requires root/admin)")
	serviceInstallCmd.Flags().BoolVar(&serviceStartNow, "start", false, "Start the service immediately after installation")
	serviceInstallCmd.Flags().StringVarP(&configPath, "config", "c", "", "Path to configuration file")

	// uninstall 命令的标志
	serviceUninstallCmd.Flags().BoolVar(&serviceSystemWide, "system-wide", false, "Uninstall system-wide service")

	// 其他命令的标志
	serviceStartCmd.Flags().BoolVar(&serviceSystemWide, "system-wide", false, "System-wide service")
	serviceStopCmd.Flags().BoolVar(&serviceSystemWide, "system-wide", false, "System-wide service")
	serviceRestartCmd.Flags().BoolVar(&serviceSystemWide, "system-wide", false, "System-wide service")
	serviceStatusCmd.Flags().BoolVar(&serviceSystemWide, "system-wide", false, "System-wide service")
}

func runServiceInstall(cmd *cobra.Command, args []string) error {
	logger.Info("Installing service...", "systemWide", serviceSystemWide, "config", configPath)

	// 检查权限（系统级安装需要 root）
	if serviceSystemWide && !setup.IsRoot() {
		fmt.Println("Error: System-wide installation requires root/administrator privileges.")
		fmt.Println("Please run with sudo (macOS/Linux) or as Administrator (Windows).")
		return setup.ErrNotRoot
	}

	// 检查服务是否已安装
	if setup.IsServiceInstalled(setup.GetServiceName(), serviceSystemWide) {
		fmt.Println("Service already exists, reinstalling...")
		if err := setup.UninstallService(setup.GetServiceName(), serviceSystemWide); err != nil {
			logger.Error("Failed to uninstall existing service before reinstall", "error", err)
			return fmt.Errorf("failed to uninstall existing service before reinstall: %w", err)
		}
		if serviceSystemWide {
			if err := services.RemoveManagedSystemServiceExecutable(); err != nil {
				logger.Warn(fmt.Sprintf("Failed to remove previous managed executable during reinstall: %v", err))
			}
		}
	}

	options, err := services.BuildCLIServiceInstallOptions(configPath, serviceSystemWide)
	if err != nil {
		return err
	}
	hostExplicit := cmd.Flags().Changed("host")
	options.Args = serviceArgsWithHost(options.Args, hostExplicit)

	// 安装服务
	if err := setup.InstallService(options); err != nil {
		logger.Error("Failed to install service", "error", err)
		return fmt.Errorf("failed to install service: %w", err)
	}

	fmt.Println("✓ Service installed successfully")
	if runtime.GOOS == "darwin" {
		// Install core service LaunchAgent (for PKG scenario or standalone development)
		coreArgs := serviceArgsWithHost([]string{"core"}, hostExplicit)
		if err := setup.InstallMacOSCoreService(options.ExecutablePath, coreArgs); err != nil {
			fmt.Printf("Warning: Service installed, but failed to install macOS core service: %v\n", err)
		} else {
			fmt.Println("✓ macOS core service installed successfully")
		}
		// Also clean up old tray agent if it exists (for migration from old architecture)
		if err := setup.UninstallMacOSTrayAgent(); err != nil {
			// Not a failure — old agent may not exist
			logger.Info("Old tray agent cleanup", "error", err)
		}
	}

	// 如果指定了 --start 标志，立即启动服务
	if serviceStartNow {
		fmt.Println("Starting service...")
		if err := setup.StartService(setup.GetServiceName(), serviceSystemWide); err != nil {
			fmt.Printf("Warning: Service installed but failed to start: %v\n", err)
		} else {
			fmt.Println("✓ Service started successfully")
		}
	}

	return nil
}

func serviceArgsWithHost(args []string, explicit bool) []string {
	if !explicit {
		return args
	}
	return append(args, "--host", config.ServiceBindHost())
}

func runServiceUninstall(cmd *cobra.Command, args []string) error {
	logger.Info("Uninstalling service...", "systemWide", serviceSystemWide)

	// 检查权限（系统级卸载需要 root）
	if serviceSystemWide && !setup.IsRoot() {
		fmt.Println("Error: System-wide uninstallation requires root/administrator privileges.")
		fmt.Println("Please run with sudo (macOS/Linux) or as Administrator (Windows).")
		return setup.ErrNotRoot
	}

	// 检查服务是否已安装
	if !setup.IsServiceInstalled(setup.GetServiceName(), serviceSystemWide) {
		fmt.Println("Error: Service is not installed.")
		return setup.ErrServiceNotInstalled
	}

	// 卸载服务
	if err := setup.UninstallService(setup.GetServiceName(), serviceSystemWide); err != nil {
		logger.Error("Failed to uninstall service", "error", err)
		return fmt.Errorf("failed to uninstall service: %w", err)
	}
	if serviceSystemWide {
		if err := services.RemoveManagedSystemServiceExecutable(); err != nil {
			return fmt.Errorf("service uninstalled, but failed to remove managed executable: %w", err)
		}
	}

	fmt.Println("✓ Service uninstalled successfully")
	if runtime.GOOS == "darwin" {
		// Uninstall core service LaunchAgent
		if err := setup.UninstallMacOSCoreService(); err != nil {
			fmt.Printf("Warning: Service uninstalled, but failed to remove macOS core service: %v\n", err)
		} else {
			fmt.Println("✓ macOS core service removed successfully")
		}
		// Also clean up old tray agent if it exists
		if err := setup.UninstallMacOSTrayAgent(); err != nil {
			logger.Info("Old tray agent cleanup during uninstall", "error", err)
		}
	}
	return nil
}

func runServiceStart(cmd *cobra.Command, args []string) error {
	logger.Info("Starting service...", "systemWide", serviceSystemWide)

	// 检查服务是否已安装
	if !setup.IsServiceInstalled(setup.GetServiceName(), serviceSystemWide) {
		fmt.Println("Error: Service is not installed.")
		fmt.Println("Please install it first: aliang service install")
		return setup.ErrServiceNotInstalled
	}

	// 启动服务
	if err := setup.StartService(setup.GetServiceName(), serviceSystemWide); err != nil {
		logger.Error("Failed to start service", "error", err)
		return fmt.Errorf("failed to start service: %w", err)
	}

	fmt.Println("✓ Service started successfully")
	return nil
}

func runServiceStop(cmd *cobra.Command, args []string) error {
	logger.Info("Stopping service...", "systemWide", serviceSystemWide)

	// 检查服务是否已安装
	if !setup.IsServiceInstalled(setup.GetServiceName(), serviceSystemWide) {
		fmt.Println("Error: Service is not installed.")
		return setup.ErrServiceNotInstalled
	}

	// 停止服务
	if err := setup.StopService(setup.GetServiceName(), serviceSystemWide); err != nil {
		logger.Error("Failed to stop service", "error", err)
		return fmt.Errorf("failed to stop service: %w", err)
	}

	fmt.Println("✓ Service stopped successfully")
	return nil
}

func runServiceRestart(cmd *cobra.Command, args []string) error {
	logger.Info("Restarting service...", "systemWide", serviceSystemWide)

	// 检查服务是否已安装
	if !setup.IsServiceInstalled(setup.GetServiceName(), serviceSystemWide) {
		fmt.Println("Error: Service is not installed.")
		fmt.Println("Please install it first: aliang service install")
		return setup.ErrServiceNotInstalled
	}

	// 重启服务
	if err := setup.RestartService(setup.GetServiceName(), serviceSystemWide); err != nil {
		logger.Error("Failed to restart service", "error", err)
		return fmt.Errorf("failed to restart service: %w", err)
	}

	fmt.Println("✓ Service restarted successfully")
	return nil
}

func runServiceStatus(cmd *cobra.Command, args []string) error {
	logger.Info("Checking service status...", "systemWide", serviceSystemWide)

	// 获取服务状态
	status, err := setup.GetServiceStatus(setup.GetServiceName(), serviceSystemWide)
	if err != nil {
		logger.Error("Failed to get service status", "error", err)
		return fmt.Errorf("failed to get service status: %w", err)
	}

	// 打印状态
	setup.PrintServiceStatus(status)

	// 如果服务未安装，返回错误
	if !status.IsInstalled {
		return setup.ErrServiceNotInstalled
	}

	return nil
}
