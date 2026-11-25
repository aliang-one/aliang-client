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
		// 2026-11-25:去掉菜单项 SetIcon——每 2s syncState 刷新时 MenuItem.SetIcon
		// 在 Windows 上每次新建 HBITMAP(上游 v1.2.2 不回收),是 GDI 泄漏的唯一点火
		// 路径(83min 耗尽 10k 配额)。FormatAIStatusTitle 已带 ✅/⏳ 状态前缀,
		// 视觉信息量不丢;主托盘图标走 SetIcon(带 md5 缓存)不受影响。
		item.Show()
	}

	return true
}

// AIStatusMenuIcon was removed 2026-11-25: menu-item icons eliminated the GDI
// leak hot path on Windows (MenuItem.SetIcon → iconToBitmap → HBITMAP never freed).
// Status is conveyed by the ✅/⏳ prefix in FormatAIStatusTitle instead.
