//go:build windows

package services

import "time"

// Windows 不做登录 shell 探针：用户级 PATH 已由 augmentEnvWithWindowsUserPath
// 从注册表合并（见 agent_terminal_env_windows.go），那是 Windows 上的等价
// 通道。这里恒失败 → augmentEnvWithLoginShellPath 天然 no-op。
func loginShellPathSnapshot(timeout time.Duration) (string, bool) {
	return "", false
}
