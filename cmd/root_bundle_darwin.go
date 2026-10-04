//go:build darwin

package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"aliang.one/nursorgate/app/tray"
	"aliang.one/nursorgate/common/logger"
)

func maybeRunAppBundleCompanion() bool {
	execPath, _ := filepath.EvalSymlinks(os.Args[0])
	if !isAppBundleExecPath(execPath) {
		return false
	}

	// 单实例保护与 aliang tray(cmd/tray.go)对齐。此前 .app 双击启动完全
	// 绕过 guard:双开会得到两个 companion、两套 watchdog(同时启动两个
	// user-agent 的尝试会被 agent 侧 56433/flock 挡住,但托盘与看护循环会
	// 双份运行)。guard 拿不到=已有实例在跑,静默让位。
	guard, acquired, err := acquireSingleInstanceGuard()
	if err != nil {
		logger.Warn(fmt.Sprintf("Single-instance guard acquisition failed for .app launch (%v); continuing without guard", err))
	} else if !acquired {
		logger.Info("Aliang is already running, exiting duplicate .app launch request.")
		return true
	} else {
		// RunCompanion 经 systray.Quit → os.Exit(0) 结束,defer 不会执行;
		// listener 的最终释放由进程退出兜底。
		_ = guard
	}

	logger.Info("Detected .app bundle launch, starting tray mode...")
	tray.RunCompanion()
	return true
}

// isAppBundleExecPath 判定本进程是否由 .app bundle 启动(macOS companion 路径)。
func isAppBundleExecPath(execPath string) bool {
	return execPath != "" && strings.Contains(execPath, ".app/Contents/MacOS/")
}
