//go:build windows

package cmd

import (
	"fmt"

	httpServer "aliang.one/nursorgate/app/http"
	"aliang.one/nursorgate/common/logger"
)

// startCoreDashboardIfHeadless brings up the HTTP dashboard from the Core
// daemon on platforms where no in-process host will request it reliably.
// Windows: the tray companion sends ActionStartHTTP only once per companion
// process lifetime, so a Core-only restart (SCM `Restart-Service`, crash
// recovery, upgrade cycle) previously left the dashboard down forever — the
// 2026-11-16 production incident. StartHttpServer is idempotent
// (app/http/server.go guards on isRunning), so a later companion request is a
// harmless no-op that simply echoes the already-running port.
func startCoreDashboardIfHeadless() {
	if err := httpServer.StartHttpServer(); err != nil {
		logger.Warn(fmt.Sprintf("Failed to auto-start HTTP dashboard on Windows: %v", err))
	}
}
