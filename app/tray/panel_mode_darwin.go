//go:build darwin

package tray

import "github.com/getlantern/systray"

// setTrayPanelMode macOS 面板模式 shim:true=状态项摘掉原生菜单、挂上点击
// 回调(systray_darwin.m setPanelMode:,经 runInMainThread 封送主线程同步
// 执行);false=摘掉回调、接回原生菜单。菜单对象在面板期间仍被刷新循环照常
// 更新,切换无损。前提:SetPanelClickHook 已先挂好(systray.h 注释约定),
// installPanelBridge 的调用顺序保证这一点。
func setTrayPanelMode(on bool) {
	systray.SetPanelMode(on)
}
