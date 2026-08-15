//go:build darwin

package cmd

import (
	"os"
	"path/filepath"
	"strings"

	"aliang.one/nursorgate/app/tray"
	"aliang.one/nursorgate/common/logger"
)

func maybeRunAppBundleCompanion() bool {
	execPath, _ := filepath.EvalSymlinks(os.Args[0])
	if execPath == "" || !strings.Contains(execPath, ".app/Contents/MacOS/") {
		return false
	}

	logger.Info("Detected .app bundle launch, starting tray mode...")
	tray.RunCompanion()
	return true
}
