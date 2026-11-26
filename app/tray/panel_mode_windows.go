//go:build windows

package tray

// setTrayPanelMode Windows 面板模式 shim:空实现。Windows 无「摘菜单」动作
// ——systray_windows.go 的 wndProc 分支看到 panelClickFn 非 nil 即把左右键
// 交给回调(原生菜单不弹),panelClickFn 置 nil 即恢复菜单,无需额外开关。
// darwin 侧对应的 systray.SetPanelMode 只在 darwin 编译,故按平台拆 shim,
// panel_bridge.go 保持纯 Go 双平台可编译。
func setTrayPanelMode(bool) {}
