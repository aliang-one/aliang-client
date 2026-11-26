package systray

/*
#cgo darwin CFLAGS: -DDARWIN -x objective-c -fobjc-arc
#cgo darwin LDFLAGS: -framework Cocoa -framework WebKit

#include "systray.h"
*/
import "C"

import (
	"unsafe"
)

// 本仓补丁:面板模式开关——true=托盘图标摘掉原生菜单、挂上点击回调(左/右键
// 按下即触发 systray_tray_clicked → panelClickFn,先 SetPanelClickHook 再开);
// false=摘掉回调、接回原生菜单(原生菜单兜底)。菜单对象始终被刷新循环正常
// 更新,面板期间不可见而已,切换无损。主线程执行经 runInMainThread 封送,
// 本调用同步等待(与 setIcon 等既有入口同款)。
func SetPanelMode(panel bool) {
	C.set_panel_mode(C.bool(panel))
}

// 本仓补丁:托盘图标的屏幕矩形(全局左上原点、物理像素,与 Windows 侧 TrayRect
// 同基准),面板窗口锚定用。ok=false = 托盘尚未初始化或拿不到窗口矩形(溢出区
// 之类的两态判废归接线层,同 Windows 侧约定)。darwin 侧是整数四元组、Windows
// 侧是导出 Rect 类型——两种形态并存是既定设计(见 systray_windows.go 的 TrayRect)。
func TrayRect() (x, y, w, h int, ok bool) {
	var cx, cy, cw, ch C.int
	var cok C.bool
	C.tray_rect(&cx, &cy, &cw, &ch, &cok)
	return int(cx), int(cy), int(cw), int(ch), bool(cok)
}

// SetTemplateIcon sets the systray icon as a template icon (on Mac), falling back
// to a regular icon on other platforms.
// templateIconBytes and regularIconBytes should be the content of .ico for windows and
// .ico/.jpg/.png for other platforms.
func SetTemplateIcon(templateIconBytes []byte, regularIconBytes []byte) {
	cstr := (*C.char)(unsafe.Pointer(&templateIconBytes[0]))
	C.setIcon(cstr, (C.int)(len(templateIconBytes)), true)
}

// SetIcon sets the icon of a menu item. Only works on macOS and Windows.
// iconBytes should be the content of .ico/.jpg/.png
func (item *MenuItem) SetIcon(iconBytes []byte) {
	cstr := (*C.char)(unsafe.Pointer(&iconBytes[0]))
	C.setMenuItemIcon(cstr, (C.int)(len(iconBytes)), C.int(item.id), false)
}

// SetTemplateIcon sets the icon of a menu item as a template icon (on macOS). On Windows, it
// falls back to the regular icon bytes and on Linux it does nothing.
// templateIconBytes and regularIconBytes should be the content of .ico for windows and
// .ico/.jpg/.png for other platforms.
func (item *MenuItem) SetTemplateIcon(templateIconBytes []byte, regularIconBytes []byte) {
	cstr := (*C.char)(unsafe.Pointer(&templateIconBytes[0]))
	C.setMenuItemIcon(cstr, (C.int)(len(templateIconBytes)), C.int(item.id), true)
}
