//go:build !windows

package tray

import _ "embed"

//go:embed icon-active.png
var iconDataActive []byte

//go:embed icon-inactive.png
var iconDataInActive []byte

//go:embed tray_icon/active_vscode.png
var iconDataActiveVSCode []byte

//go:embed tray_icon/inactive_vscode.png
var iconDataInactiveVSCode []byte

//go:embed tray_icon/active_cursor.png
var iconDataActiveCursor []byte

//go:embed tray_icon/inactive_cursor.png
var iconDataInactiveCursor []byte

//go:embed tray_icon/active_openai.png
var iconDataActiveOpenAI []byte

//go:embed tray_icon/inactive_openai.png
var iconDataInactiveOpenAI []byte

//go:embed tray_icon/active_anthropic.png
var iconDataActiveAnthropic []byte

//go:embed tray_icon/inactive_anthropic.png
var iconDataInactiveAnthropic []byte

// GetIcon returns the application icon bytes for the active state.
func GetIcon() []byte {
	return iconDataActive
}

// GetIconDisabled returns the application icon bytes for the inactive state.
func GetIconDisabled() []byte {
	return iconDataInActive
}

// GetProviderIcon returns provider-specific tray icon bytes for the current
// platform. The key must already be normalized by trayIconProviderKey.
func GetProviderIcon(key string, active bool) []byte {
	if active {
		switch key {
		case "vscode":
			return iconDataActiveVSCode
		case "cursor":
			return iconDataActiveCursor
		case "openai":
			return iconDataActiveOpenAI
		case "anthropic":
			return iconDataActiveAnthropic
		}
		return nil
	}

	switch key {
	case "vscode":
		return iconDataInactiveVSCode
	case "cursor":
		return iconDataInactiveCursor
	case "openai":
		return iconDataInactiveOpenAI
	case "anthropic":
		return iconDataInactiveAnthropic
	default:
		return nil
	}
}
