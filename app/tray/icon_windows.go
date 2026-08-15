//go:build windows

package tray

import _ "embed"

// Windows systray icons must be valid ICO bytes because systray uses
// LoadImageW with IMAGE_ICON under the hood.

//go:embed icon-active.ico
var iconDataActive []byte

//go:embed icon-inactive.ico
var iconDataInActive []byte

//go:embed tray_icon/active_vscode.ico
var iconDataActiveVSCode []byte

//go:embed tray_icon/inactive_vscode.ico
var iconDataInactiveVSCode []byte

//go:embed tray_icon/active_cursor.ico
var iconDataActiveCursor []byte

//go:embed tray_icon/inactive_cursor.ico
var iconDataInactiveCursor []byte

//go:embed tray_icon/active_openai.ico
var iconDataActiveOpenAI []byte

//go:embed tray_icon/inactive_openai.ico
var iconDataInactiveOpenAI []byte

//go:embed tray_icon/active_anthropic.ico
var iconDataActiveAnthropic []byte

//go:embed tray_icon/inactive_anthropic.ico
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
