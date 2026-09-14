//go:build !windows

package tray

// enableWindowsDPIAwareness 在非 Windows 平台是无操作。
// macOS NSMenu / Linux appindicator 由系统自行处理 DPI，无需声明。
func enableWindowsDPIAwareness() {}
