//go:build !linux && !windows

package cmd

// startCoreDashboardIfHeadless is a no-op on platforms that host the dashboard
// in-process (macOS TrayApp): the tray owns the HTTP server there, so the Core
// daemon must not start a competing one. Linux and Windows compile real
// implementations (see core_autostart_linux.go / core_autostart_windows.go) —
// autoStartDashboard documents the policy contract.
func startCoreDashboardIfHeadless() {}
