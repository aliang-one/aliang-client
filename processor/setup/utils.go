package setup

import (
	"errors"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
)

var (
	// ErrNotRoot 权限不足错误
	ErrNotRoot = errors.New("this operation requires root/administrator privileges")
	// ErrServiceExists 服务已存在错误
	ErrServiceExists = errors.New("service already exists")
	// ErrServiceNotInstalled 服务未安装错误
	ErrServiceNotInstalled = errors.New("service is not installed")
)

// GetCurrentExecutable 获取当前可执行文件的路径
func GetCurrentExecutable() (string, error) {
	execPath, err := os.Executable()
	if err != nil {
		return "", err
	}
	// 获取真实路径（解析符号链接）
	realPath, err := filepath.EvalSymlinks(execPath)
	if err != nil {
		return execPath, nil // 如果解析失败，返回原始路径
	}
	return realPath, nil
}

// IsRoot 检查是否有 root/管理员权限
func IsRoot() bool {
	switch runtime.GOOS {
	case "windows":
		// Windows: 检查是否以管理员身份运行
		_, err := os.Open("\\\\.\\PHYSICALDRIVE0")
		return err == nil
	case "darwin", "linux":
		// Unix-like: 检查 UID 是否为 0
		return os.Geteuid() == 0
	default:
		return false
	}
}

// CheckSystemPrivileges 检查系统级权限，如果没有则返回错误
func CheckSystemPrivileges() error {
	if !IsRoot() {
		return ErrNotRoot
	}
	return nil
}

// GetServiceName 获取默认服务名称
func GetServiceName() string {
	return "alianggate"
}

// ExpandPath 展开路径中的 ~ 和环境变量
func ExpandPath(path string) (string, error) {
	// 展开环境变量
	expanded := os.ExpandEnv(path)

	// 展开用户目录
	if strings.HasPrefix(expanded, "~/") {
		usr, err := user.Current()
		if err != nil {
			return "", err
		}
		expanded = filepath.Join(usr.HomeDir, expanded[2:])
	}

	return filepath.Clean(expanded), nil
}

// FileExists 检查文件是否存在
func FileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// RunCommand 执行系统命令
func RunCommand(name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	return cmd.CombinedOutput()
}

// RunCommandWithSudo 使用 sudo 执行命令（Unix-like）
func RunCommandWithSudo(name string, args ...string) ([]byte, error) {
	if IsRoot() {
		return RunCommand(name, args...)
	}
	// 如果不是 root，尝试使用 sudo
	allArgs := append([]string{name}, args...)
	return RunCommand("sudo", allArgs...)
}
