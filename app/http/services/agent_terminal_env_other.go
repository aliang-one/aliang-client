//go:build !windows

package services

// augmentEnvWithWindowsUserPath 非 Windows 平台没有注册表用户 PATH 可合并，
// 快照环境原样返回。
func augmentEnvWithWindowsUserPath(env []string) []string {
	return env
}
