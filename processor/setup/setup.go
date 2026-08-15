package setup

import (
	"fmt"
	"os"
	"path/filepath"
)

// GetServiceManager 获取当前平台的服务管理器
func GetServiceManager(name string, systemWide bool) ServiceManager {
	opts := InstallOptions{
		Name:       name,
		SystemWide: systemWide,
	}
	return NewServiceManager(opts)
}

// InstallService 安装服务的便捷方法
func InstallService(options InstallOptions) error {
	manager := NewServiceManager(options)
	return manager.Install(options)
}

// UninstallService 卸载服务的便捷方法
func UninstallService(name string, systemWide bool) error {
	manager := GetServiceManager(name, systemWide)
	return manager.Uninstall()
}

// StartService 启动服务的便捷方法
func StartService(name string, systemWide bool) error {
	manager := GetServiceManager(name, systemWide)
	return manager.Start()
}

// StopService 停止服务的便捷方法
func StopService(name string, systemWide bool) error {
	manager := GetServiceManager(name, systemWide)
	return manager.Stop()
}

// RestartService 重启服务的便捷方法
func RestartService(name string, systemWide bool) error {
	manager := GetServiceManager(name, systemWide)
	return manager.Restart()
}

// GetServiceStatus 获取服务状态的便捷方法
func GetServiceStatus(name string, systemWide bool) (*ServiceStatus, error) {
	manager := GetServiceManager(name, systemWide)
	return manager.Status()
}

// IsServiceInstalled 检查服务是否已安装的便捷方法
func IsServiceInstalled(name string, systemWide bool) bool {
	manager := GetServiceManager(name, systemWide)
	return manager.IsInstalled()
}

// QuickInstall 快速安装服务（使用默认配置）
func QuickInstall(configPath string, systemWide bool) error {
	// 获取当前可执行文件路径
	execPath, err := GetCurrentExecutable()
	if err != nil {
		return fmt.Errorf("failed to get executable path: %w", err)
	}

	// 获取配置文件路径
	if configPath == "" {
		// 尝试默认配置文件位置
		defaultPaths := []string{
			"./config.json",
			"/etc/nursorgate/config.json",
		}

		if !systemWide {
			homeDir, _ := os.UserHomeDir()
			defaultPaths = append([]string{
				filepath.Join(homeDir, ".nursorgate/config.json"),
			}, defaultPaths...)
		}

		for _, path := range defaultPaths {
			if FileExists(path) {
				configPath = path
				break
			}
		}
	}

	options := InstallOptions{
		Name:           GetServiceName(),
		DisplayName:    "AliangGate Network Service",
		Description:    "AliangGate network proxy and routing service",
		ExecutablePath: execPath,
		ConfigPath:     configPath,
		SystemWide:     systemWide,
		StartType:      StartAutomatic,
	}

	return InstallService(options)
}

// PrintServiceStatus 打印服务状态信息
func PrintServiceStatus(status *ServiceStatus) {
	fmt.Println("Service Status:")
	fmt.Printf("  Installed: %v\n", status.IsInstalled)
	fmt.Printf("  Running:   %v\n", status.IsRunning)
	fmt.Printf("  Status:    %s\n", status.Status)
	if status.PID > 0 {
		fmt.Printf("  PID:       %d\n", status.PID)
	}
}
