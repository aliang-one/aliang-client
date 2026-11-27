//go:build windows

package cmd

import (
	"os"
	"strings"

	"aliang.one/nursorgate/app/tray"
)

// companionAutostartArg 由 MSI 的 HKCU Run 键传入(2026-11-27 P0 登录自启)，
// 区分「Run 键自启」与「手动双击快捷方式」：自启形态不自动弹仪表盘浏览器、
// 启动失败不弹模态错误框（无人值守场景只落 trace 日志）。菜单与手动路径
// 完全一致，用户随时可从托盘打开面板。
const companionAutostartArg = "--autostart"

func MaybeRunWindowsCompanionFromArgs() bool {
	if len(os.Args) < 2 {
		return false
	}

	if !strings.EqualFold(strings.TrimSpace(os.Args[1]), "companion") {
		return false
	}

	autostart := false
	for _, arg := range os.Args[2:] {
		if strings.EqualFold(strings.TrimSpace(arg), companionAutostartArg) {
			autostart = true
			break
		}
	}

	if autostart {
		tray.RunCompanionAutostart()
	} else {
		tray.RunCompanion()
	}
	return true
}
