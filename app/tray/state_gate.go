package tray

import "strings"

// 托盘可见面变更检测闸(2026-11-20 0.196 事故止血)。
//
// companion 的 syncStateLoop 每 2s 轮询一次 IPC 状态,旧实现每轮无条件
// SetIcon/SetTooltip/刷新 AI 菜单。而 systray v1.2.2 的 MenuItem.SetIcon
// 走 iconToBitmap:每次调用新建 HBITMAP,旧句柄被 menuItemIcons[id] 覆盖
// 后从不销毁——4 个 provider 每 tick 泄漏 4 个 GDI 对象,~83 分钟耗尽
// Windows 进程 10k GDI 配额(0.196 WER GDIObjectLeak ×3 实锤),托盘图标
// 随即消失。签名不变即整轮跳过,重绘只发生在可见面真翻转时,泄漏速率
// 随之从「每 tick × N」降到「状态翻转才发生」。
//
// 签名输入必须是渲染的完整决定集:mode、running、启动拦截码、AI provider
// 可见面。core 的 description 字段不参与托盘渲染(trayProxyStatusTitle
// 未使用该参数),刻意不进签名,避免易变输入让门控永不收敛。

// aiProvidersDigest 把 provider 可见面折成稳定摘要:只纳入 FormatAIStatusTitle
// 非空(会真正显示)的条目,标题本身已编码 Enabled/Active|Detected 的翻转
// (✅/⏳),而菜单图标也由 Key+Active|Detected 决定,故摘要同时覆盖文字与图标。
// 空摘要 = 全部隐藏,与 hideAIStatus 的渲染语义一致。
func aiProvidersDigest(providers []AIProviderStatus) string {
	var sb strings.Builder
	for _, p := range providers {
		title := FormatAIStatusTitle(p)
		if title == "" {
			continue
		}
		sb.WriteString(p.Key)
		sb.WriteString("=")
		sb.WriteString(title)
		sb.WriteString(";")
	}
	return sb.String()
}

// buildTrayViewKey 由 IPC 状态推导托盘可见面签名;输入相同则签名逐字节相同。
func buildTrayViewKey(mode string, running bool, startBlockCode string, providers []AIProviderStatus) string {
	state := "stopped"
	if running {
		state = "running"
	}
	return strings.Join([]string{
		"view",
		mode,
		state,
		startBlockCode,
		aiProvidersDigest(providers),
	}, "|")
}

// buildTrayUnavailableViewKey 是 IPC 不可达态的签名;与可达态签名前缀相异,
// 两者不可能碰撞。
func buildTrayUnavailableViewKey(reason string) string {
	return "unavailable|" + reason
}
