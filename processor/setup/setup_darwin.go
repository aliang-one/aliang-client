package setup

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"aliang.one/nursorgate/common/logger"
)

// DarwinServiceManager macOS 服务管理器
type DarwinServiceManager struct {
	name       string
	systemWide bool
	plistPath  string
}

// NewServiceManager 创建当前平台的服务管理器
func NewServiceManager(opts InstallOptions) ServiceManager {
	return &DarwinServiceManager{
		name:       opts.Name,
		systemWide: opts.SystemWide,
		plistPath:  getPlistPath(opts.Name, opts.SystemWide),
	}
}

// getPlistPath 获取 plist 文件路径
func getPlistPath(name string, systemWide bool) string {
	if systemWide {
		return "/Library/LaunchDaemons/" + macOSCoreServiceLabel + ".plist"
	}

	homeDir, _ := os.UserHomeDir()
	return filepath.Join(homeDir, "Library/LaunchAgents", fmt.Sprintf("org.nursor.%s.plist", name))
}

func getLaunchdLabel(name string, systemWide bool) string {
	if systemWide {
		return macOSCoreServiceLabel
	}
	return fmt.Sprintf("org.nursor.%s", name)
}

// Install 安装 macOS 服务
func (d *DarwinServiceManager) Install(options InstallOptions) error {
	logger.Info("Installing macOS service...", "name", options.Name, "systemWide", options.SystemWide)

	// 检查权限
	if options.SystemWide && !IsRoot() {
		return ErrNotRoot
	}

	// 检查服务是否已存在
	if d.IsInstalled() {
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

	// 准备参数
	args := []string{}
	if options.ConfigPath != "" {
		args = append(args, "--config", options.ConfigPath)
	}
	if len(options.Args) > 0 {
		args = append(args, options.Args...)
	}

	// 准备日志路径
	logDir := "/var/log"
	if !options.SystemWide {
		homeDir, _ := os.UserHomeDir()
		logDir = filepath.Join(homeDir, "Library/Logs")
	}

	// 生成 plist 内容
	plistData := LaunchdPlistData{
		Label:             getLaunchdLabel(options.Name, options.SystemWide),
		ProgramPath:       execPath,
		Args:              args,
		RunAtLoad:         options.StartType == StartAutomatic,
		KeepAlive:         options.StartType == StartAutomatic,
		WorkingDirectory:  options.WorkingDirectory,
		StandardOutPath:   filepath.Join(logDir, fmt.Sprintf("%s.log", options.Name)),
		StandardErrorPath: filepath.Join(logDir, fmt.Sprintf("%s.error.log", options.Name)),
		EnvironmentVars:   options.Env,
	}

	plistContent, err := RenderLaunchdPlist(plistData)
	if err != nil {
		return fmt.Errorf("failed to render plist: %w", err)
	}

	// 写入 plist 文件
	if err := os.WriteFile(d.plistPath, []byte(plistContent), 0644); err != nil {
		return fmt.Errorf("failed to write plist file: %w", err)
	}

	logger.Info("Service plist created", "path", d.plistPath)

	// 加载服务
	if err := d.load(); err != nil {
		// 加载失败，清理文件
		os.Remove(d.plistPath)
		return fmt.Errorf("failed to load service: %w", err)
	}

	logger.Info("Service installed successfully")
	return nil
}

// Uninstall 卸载 macOS 服务
func (d *DarwinServiceManager) Uninstall() error {
	logger.Info("Uninstalling macOS service...", "name", d.name)

	if !d.IsInstalled() {
		return ErrServiceNotInstalled
	}

	status, err := d.Status()
	if err != nil {
		return fmt.Errorf("failed to inspect launchd service status before uninstall: %w", err)
	}

	// 仅当服务已加载/运行时卸载，避免未加载时误报
	if status.Status == "running" || status.Status == "loaded" {
		if err := d.unload(); err != nil {
			return fmt.Errorf("failed to unload service before uninstall: %w", err)
		}
	}

	// 删除 plist 文件
	if err := os.Remove(d.plistPath); err != nil {
		return fmt.Errorf("failed to remove plist file: %w", err)
	}

	logger.Info("Service uninstalled successfully")
	return nil
}

// Start 启动 macOS 服务
func (d *DarwinServiceManager) Start() error {
	logger.Info("Starting macOS service...", "name", d.name)

	if !d.IsInstalled() {
		return ErrServiceNotInstalled
	}

	return d.load()
}

// Stop 停止 macOS 服务
func (d *DarwinServiceManager) Stop() error {
	logger.Info("Stopping macOS service...", "name", d.name)

	if !d.IsInstalled() {
		return ErrServiceNotInstalled
	}

	return d.unload()
}

// Restart 重启 macOS 服务
func (d *DarwinServiceManager) Restart() error {
	if err := d.Stop(); err != nil {
		return err
	}
	return d.Start()
}

// Status 获取 macOS 服务状态
func (d *DarwinServiceManager) Status() (*ServiceStatus, error) {
	status := &ServiceStatus{
		IsInstalled: d.IsInstalled(),
		IsRunning:   false,
		PID:         0,
		Status:      "unknown",
	}

	if !status.IsInstalled {
		status.Status = "not_installed"
		return status, nil
	}

	// 使用 launchctl list 查看服务状态
	label := getLaunchdLabel(d.name, d.systemWide)
	cmd := exec.Command("launchctl", "list", label)
	output, err := cmd.CombinedOutput()
	if err != nil {
		// 服务未运行
		status.Status = "stopped"
		return status, nil
	}

	// 解析输出获取 PID 和状态
	outputStr := string(output)
	re := regexp.MustCompile(`(?m)^\s*(\d+)\s+\d+\s+` + label + `$`)
	matches := re.FindStringSubmatch(outputStr)
	if len(matches) > 1 {
		status.IsRunning = true
		status.PID = parseInt(matches[1])
		status.Status = "running"
	} else if strings.Contains(outputStr, label) {
		status.Status = "loaded"
	}

	return status, nil
}

// IsInstalled 检查服务是否已安装
func (d *DarwinServiceManager) IsInstalled() bool {
	return FileExists(d.plistPath)
}

// GetName 获取服务名称
func (d *DarwinServiceManager) GetName() string {
	return d.name
}

// load 加载（启动）服务
func (d *DarwinServiceManager) load() error {
	cmd := exec.Command("launchctl", "load", d.plistPath)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("launchctl load failed: %w, output: %s", err, string(output))
	}
	return nil
}

// unload 卸载（停止）服务
func (d *DarwinServiceManager) unload() error {
	cmd := exec.Command("launchctl", "unload", d.plistPath)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("launchctl unload failed: %w, output: %s", err, string(output))
	}
	return nil
}
