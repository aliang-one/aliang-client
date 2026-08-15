package setup

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"aliang.one/nursorgate/common/logger"
)

// LinuxServiceManager Linux systemd 服务管理器
type LinuxServiceManager struct {
	name        string
	systemWide  bool
	servicePath string
}

// NewServiceManager 创建当前平台的服务管理器
func NewServiceManager(opts InstallOptions) ServiceManager {
	return &LinuxServiceManager{
		name:        opts.Name,
		systemWide:  opts.SystemWide,
		servicePath: getServicePath(opts.Name, opts.SystemWide),
	}
}

// getServicePath 获取 systemd unit 文件路径
func getServicePath(name string, systemWide bool) string {
	if systemWide {
		return filepath.Join("/etc/systemd/system", fmt.Sprintf("%s.service", name))
	}

	homeDir, _ := os.UserHomeDir()
	return filepath.Join(homeDir, ".config/systemd/user", fmt.Sprintf("%s.service", name))
}

// Install 安装 Linux 服务
func (l *LinuxServiceManager) Install(options InstallOptions) error {
	logger.Info("Installing Linux service...", "name", options.Name, "systemWide", options.SystemWide)

	// 检查权限
	if options.SystemWide && !IsRoot() {
		return ErrNotRoot
	}

	// 检查服务是否已存在
	if l.IsInstalled() {
		return ErrServiceExists
	}

	// 获取可执行文件路径
	execPath := options.ExecutablePath
	if execPath == "" {
		var err error
		execPath, err = GetCurrentExecutable()
		if err != nil {
			return fmt.Errorf("failed to get executable path: %w", err)
		}
	}

	// 准备 ExecStart 命令
	execStart := execPath
	if options.ConfigPath != "" {
		execStart += " --config " + options.ConfigPath
	}
	if len(options.Args) > 0 {
		execStart += " " + strings.Join(options.Args, " ")
	}

	// 准备用户和组（仅系统级服务）
	user := ""
	group := ""
	if options.SystemWide {
		user = "root"
		group = "root"
	}

	// 生成 systemd unit 内容
	unitData := SystemdUnitData{
		Description:     options.DisplayName,
		After:           "network.target",
		Wants:           "network.target",
		ExecStart:       execStart,
		RestartPolicy:   "on-failure",
		RestartSec:      "5s",
		User:            user,
		Group:           group,
		StandardOutput:  "journal",
		StandardError:   "journal",
		WantedBy:        "multi-user.target",
		EnvironmentVars: options.Env,
	}

	// 如果是自动启动
	if options.StartType == StartAutomatic {
		unitData.RestartPolicy = "always"
	}

	unitContent, err := RenderSystemdUnit(unitData)
	if err != nil {
		return fmt.Errorf("failed to render systemd unit: %w", err)
	}

	// 确保目录存在
	serviceDir := filepath.Dir(l.servicePath)
	if err := os.MkdirAll(serviceDir, 0755); err != nil {
		return fmt.Errorf("failed to create service directory: %w", err)
	}

	// 写入 unit 文件
	if err := os.WriteFile(l.servicePath, []byte(unitContent), 0644); err != nil {
		return fmt.Errorf("failed to write service file: %w", err)
	}

	logger.Info("Service unit file created", "path", l.servicePath)

	// 重新加载 systemd
	if err := l.systemctlReload(); err != nil {
		// 重载失败，清理文件
		os.Remove(l.servicePath)
		return fmt.Errorf("failed to reload systemd: %w", err)
	}

	// 如果设置为自动启动，则启用服务
	if options.StartType == StartAutomatic {
		if err := l.systemctlEnable(); err != nil {
			logger.Warn("Failed to enable service", "error", err)
		}
	}

	logger.Info("Service installed successfully")
	return nil
}

// Uninstall 卸载 Linux 服务
func (l *LinuxServiceManager) Uninstall() error {
	logger.Info("Uninstalling Linux service...", "name", l.name)

	if !l.IsInstalled() {
		return ErrServiceNotInstalled
	}

	status, err := l.Status()
	if err != nil {
		return fmt.Errorf("failed to inspect systemd service status before uninstall: %w", err)
	}

	// 停止服务
	if status.IsRunning {
		if err := l.systemctlStop(); err != nil {
			return fmt.Errorf("failed to stop service before uninstall: %w", err)
		}
	}

	// 禁用服务
	if err := l.systemctlDisable(); err != nil {
		return fmt.Errorf("failed to disable service before uninstall: %w", err)
	}

	// 删除 unit 文件
	if err := os.Remove(l.servicePath); err != nil {
		return fmt.Errorf("failed to remove service file: %w", err)
	}

	// 重新加载 systemd
	if err := l.systemctlReload(); err != nil {
		return fmt.Errorf("failed to reload systemd after uninstall: %w", err)
	}

	logger.Info("Service uninstalled successfully")
	return nil
}

// Start 启动 Linux 服务
func (l *LinuxServiceManager) Start() error {
	logger.Info("Starting Linux service...", "name", l.name)

	if !l.IsInstalled() {
		return ErrServiceNotInstalled
	}

	return l.systemctlStart()
}

// Stop 停止 Linux 服务
func (l *LinuxServiceManager) Stop() error {
	logger.Info("Stopping Linux service...", "name", l.name)

	if !l.IsInstalled() {
		return ErrServiceNotInstalled
	}

	return l.systemctlStop()
}

// Restart 重启 Linux 服务
func (l *LinuxServiceManager) Restart() error {
	if err := l.Stop(); err != nil {
		return err
	}
	return l.Start()
}

// Status 获取 Linux 服务状态
func (l *LinuxServiceManager) Status() (*ServiceStatus, error) {
	status := &ServiceStatus{
		IsInstalled: l.IsInstalled(),
		IsRunning:   false,
		PID:         0,
		Status:      "unknown",
	}

	if !status.IsInstalled {
		status.Status = "not_installed"
		return status, nil
	}

	// 使用 systemctl is-active 检查状态
	cmd := l.systemctlCommand("is-active")
	output, err := cmd.CombinedOutput()
	outputStr := strings.TrimSpace(string(output))

	if err == nil && outputStr == "active" {
		status.IsRunning = true
		status.Status = "running"

		// 尝试获取 PID
		pidOutput, pidErr := l.systemctlCommand("show", "--property=MainPID", "--value").CombinedOutput()
		if pidErr == nil {
			status.PID = parseInt(strings.TrimSpace(string(pidOutput)))
		}
	} else {
		status.Status = outputStr // "inactive", "failed", etc.
	}

	return status, nil
}

// IsInstalled 检查服务是否已安装
func (l *LinuxServiceManager) IsInstalled() bool {
	return FileExists(l.servicePath)
}

// GetName 获取服务名称
func (l *LinuxServiceManager) GetName() string {
	return l.name
}

// systemctlCommand 创建 systemctl 命令
func (l *LinuxServiceManager) systemctlCommand(action string, args ...string) *exec.Cmd {
	allArgs := []string{action}

	// 如果是用户级服务，添加 --user 标志
	if !l.systemWide {
		allArgs = append([]string{"--user"}, allArgs...)
	}

	// 添加服务名称
	allArgs = append(allArgs, l.name)

	// 添加额外参数
	allArgs = append(allArgs, args...)

	return exec.Command("systemctl", allArgs...)
}

// systemctlReload 重新加载 systemd 配置
func (l *LinuxServiceManager) systemctlReload() error {
	var cmd *exec.Cmd
	if l.systemWide {
		cmd = exec.Command("systemctl", "daemon-reload")
	} else {
		cmd = exec.Command("systemctl", "--user", "daemon-reload")
	}

	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl daemon-reload failed: %w, output: %s", err, string(output))
	}
	return nil
}

// systemctlEnable 启用服务
func (l *LinuxServiceManager) systemctlEnable() error {
	cmd := l.systemctlCommand("enable")
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl enable failed: %w, output: %s", err, string(output))
	}
	return nil
}

// systemctlDisable 禁用服务
func (l *LinuxServiceManager) systemctlDisable() error {
	cmd := l.systemctlCommand("disable")
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl disable failed: %w, output: %s", err, string(output))
	}
	return nil
}

// systemctlStart 启动服务
func (l *LinuxServiceManager) systemctlStart() error {
	cmd := l.systemctlCommand("start")
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl start failed: %w, output: %s", err, string(output))
	}
	return nil
}

// systemctlStop 停止服务
func (l *LinuxServiceManager) systemctlStop() error {
	cmd := l.systemctlCommand("stop")
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl stop failed: %w, output: %s", err, string(output))
	}
	return nil
}
