package cmd

// autoStartDashboard documents the platform policy for which side brings up
// the HTTP dashboard, and is the single tested contract the build-tag files
// (core_autostart_linux.go / core_autostart_windows.go /
// core_autostart_other.go) must stay in sync with:
//   - linux: no tray build exists — the Core must self-host (systemd path).
//   - windows: the companion sends start_http once per process lifetime, so a
//     Core-only restart would otherwise leave the dashboard down forever.
//   - darwin (and any other host): TrayApp hosts the dashboard in-process; a
//     Core-side server would race it for the port.
func autoStartDashboard(goos string) bool {
	return goos == "linux" || goos == "windows"
}
