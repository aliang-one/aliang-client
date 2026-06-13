package tray

import "github.com/getlantern/systray"

func applyAIStatusMenu(header *systray.MenuItem, items []*systray.MenuItem, providers []AIProviderStatus) bool {
	if len(providers) == 0 {
		return false
	}

	if header != nil {
		header.SetTitle("AI Acceleration")
		header.Show()
	}

	for i, item := range items {
		if item == nil {
			continue
		}
		if i >= len(providers) {
			item.Hide()
			continue
		}

		provider := providers[i]
		title := FormatAIStatusTitle(provider)
		if title == "" {
			item.Hide()
			continue
		}

		item.SetTitle(title)
		if icon := AIStatusMenuIcon(provider); len(icon) > 0 {
			item.SetIcon(icon)
		}
		item.Show()
	}

	return true
}

func AIStatusMenuIcon(provider AIProviderStatus) []byte {
	key := trayIconProviderKey(provider.Key)
	if key == "" {
		return nil
	}
	return GetProviderIcon(key, provider.Active || provider.Detected)
}
