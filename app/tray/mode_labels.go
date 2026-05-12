package tray

import "fmt"

func trayModeDisplayName(mode string) string {
	switch mode {
	case "http":
		return "Regular Mode"
	case "tun":
		return "Deep Mode"
	default:
		return "Unknown Mode"
	}
}

func trayModeShortName(mode string) string {
	switch mode {
	case "http":
		return "regular"
	case "tun":
		return "deep"
	default:
		return "unknown"
	}
}

func trayProxyStatusTitle(mode string, running bool, description string) string {
	state := "stopped"
	if running {
		state = "running"
	}
	return fmt.Sprintf("Proxy: %s-%s", trayModeShortName(mode), state)
}

func traySelectedNotRunningStatus(mode string) string {
	return fmt.Sprintf("%s is selected, service not running", trayModeDisplayName(mode))
}

func trayProxyTooltip(mode string, running bool) string {
	state := "Stopped"
	if running {
		state = "Running"
	}
	return fmt.Sprintf("Aliang - %s Proxy %s", trayModeDisplayName(mode), state)
}
