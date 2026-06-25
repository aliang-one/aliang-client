//go:build linux

package cmd

import (
	"fmt"

	httpServer "aliang.one/nursorgate/app/http"
	"aliang.one/nursorgate/common/logger"
)

// startCoreDashboardIfHeadless brings up the HTTP dashboard from the Core daemon
// on platforms that have no tray client to request it over IPC. Linux has no
// system-tray build, so without this the systemd Core service would run but
// serve nothing. On macOS/Windows the tray starts the dashboard on demand, so
// the stub (no-op) variant is compiled there.
func startCoreDashboardIfHeadless() {
	if err := httpServer.StartHttpServer(); err != nil {
		logger.Warn(fmt.Sprintf("Failed to auto-start HTTP dashboard on Linux: %v", err))
	}
}
