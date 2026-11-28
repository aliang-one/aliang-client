// +build !windows
// go:build !windows

package systray

// #include "systray.h"
import "C"

import (
	"unsafe"
)

func registerSystray() {
	C.registerSystray()
}

func nativeLoop() {
	C.nativeLoop()
}

func quit() {
	C.quit()
}

// SetIcon sets the systray icon.
// iconBytes should be the content of .ico for windows and .ico/.jpg/.png
// for other platforms.
func SetIcon(iconBytes []byte) {
	cstr := (*C.char)(unsafe.Pointer(&iconBytes[0]))
	C.setIcon(cstr, (C.int)(len(iconBytes)), false)
}

// SetTitle sets the systray title, only available on Mac and Linux.
func SetTitle(title string) {
	C.setTitle(C.CString(title))
}

// SetTooltip sets the systray tooltip to display on mouse hover of the tray icon,
// only available on Mac and Windows.
func SetTooltip(tooltip string) {
	C.setTooltip(C.CString(tooltip))
}

func addOrUpdateMenuItem(item *MenuItem) {
	var disabled C.short
	if item.disabled {
		disabled = 1
	}
	var checked C.short
	if item.checked {
		checked = 1
	}
	var isCheckable C.short
	if item.isCheckable {
		isCheckable = 1
	}
	var parentID uint32 = 0
	if item.parent != nil {
		parentID = item.parent.id
	}
	C.add_or_update_menu_item(
		C.int(item.id),
		C.int(parentID),
		C.CString(item.title),
		C.CString(item.tooltip),
		disabled,
		checked,
		isCheckable,
	)
}

func addSeparator(id uint32) {
	C.add_separator(C.int(id))
}

func hideMenuItem(item *MenuItem) {
	C.hide_menu_item(
		C.int(item.id),
	)
}

func showMenuItem(item *MenuItem) {
	C.show_menu_item(
		C.int(item.id),
	)
}

//export systray_ready
func systray_ready() {
	systrayReady()
}

//export systray_on_exit
func systray_on_exit() {
	systrayExit()
}

//export systray_menu_item_selected
func systray_menu_item_selected(cID C.int) {
	systrayMenuItemSelected(uint32(cID))
}

// 本仓补丁:面板模式托盘点击出口——从 AppKit 主线程(systray_darwin.m 的
// trayClicked:)进来,与 Windows 侧 wndProc 的点击分支对齐:非阻塞地把
// panelClickFn 丢 goroutine 执行(回调耗时/并发/panic 契约见 SetPanelClickHook
// 注释),绝不在 AppKit 主线程做耗时工作;hook 为 nil(未进面板模式)则忽略。
//
//export systray_tray_clicked
func systray_tray_clicked() {
	if fn := loadPanelClickHook(); fn != nil {
		go fn()
	}
}
