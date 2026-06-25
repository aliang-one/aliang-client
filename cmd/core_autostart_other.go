//go:build !linux

package cmd

// startCoreDashboardIfHeadless is a no-op on platforms that have a tray client
// (macOS/Windows): the tray requests the dashboard over IPC on demand, so the
// Core daemon must not start a competing HTTP server. See
// core_autostart_linux.go for the Linux implementation.
func startCoreDashboardIfHeadless() {}
