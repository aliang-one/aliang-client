//go:build windows

package tray

import "golang.org/x/sys/windows"

// Win11 在非 100% 显示缩放下，会对 DPI-unaware 进程的 TrackPopupMenu 弹出菜单
// 做 DPI 虚拟化，菜单渲染为空白方块（可点击但文字不可见）。修复方式是在托盘
// 的任何窗口/菜单创建之前，让进程主动声明 DPI 感知。
var (
	user32DLL                         = windows.NewLazySystemDLL("user32.dll")
	procSetProcessDpiAwarenessContext = user32DLL.NewProc("SetProcessDpiAwarenessContext")
	procSetProcessDPIAware            = user32DLL.NewProc("SetProcessDPIAware")

	shcoreDLL                  = windows.NewLazySystemDLL("shcore.dll")
	procSetProcessDpiAwareness = shcoreDLL.NewProc("SetProcessDpiAwareness")
)

// dpiAwarenessContextPerMonitorV2 对应 DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2，
// 即 (DPI_AWARENESS_CONTEXT)-4。
const dpiAwarenessContextPerMonitorV2 = ^uintptr(3)

// processPerMonitorDpiAware 对应 PROCESS_PER_MONITOR_DPI_AWARE（shcore API 参数）。
const processPerMonitorDpiAware = 2

// enableWindowsDPIAwareness 声明当前进程的 DPI 感知，按能力从新到旧降级：
// SetProcessDpiAwarenessContext(PerMonitorV2)（Win10 1607+）
// → SetProcessDpiAwareness(2)（Win 8.1+）
// → SetProcessDPIAware()（Vista+）。
// 必须在 systray.Run 之前调用。若感知已被清单或先前调用设置，API 会返回失败
// （ERROR_ACCESS_DENIED / E_ACCESSDENIED），此时静默容忍，不视为错误。
func enableWindowsDPIAwareness() {
	if procSetProcessDpiAwarenessContext.Find() == nil {
		if r, _, _ := procSetProcessDpiAwarenessContext.Call(dpiAwarenessContextPerMonitorV2); r != 0 {
			return
		}
	}
	if procSetProcessDpiAwareness.Find() == nil {
		if r, _, _ := procSetProcessDpiAwareness.Call(processPerMonitorDpiAware); r == 0 {
			return
		}
	}
	if procSetProcessDPIAware.Find() == nil {
		procSetProcessDPIAware.Call()
	}
}
