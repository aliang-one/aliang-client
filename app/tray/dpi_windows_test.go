//go:build windows

package tray

import "testing"

// TestEnableWindowsDPIAwarenessIdempotent 验证重复调用安全：
// 第二次调用时 API 返回 ERROR_ACCESS_DENIED，函数必须静默容忍而非 panic。
// 仅在 Windows 上运行（CI/用户机器），用于回归托盘菜单空白的 DPI 修复。
func TestEnableWindowsDPIAwarenessIdempotent(t *testing.T) {
	enableWindowsDPIAwareness()
	enableWindowsDPIAwareness() // 第二次应静默失败不 panic
}
