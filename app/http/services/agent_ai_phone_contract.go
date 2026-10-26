package services

import "strings"

// 手机驱动回合的行为契约（2026-10-26 手机行为契约设计）。
//
// server 随 ai.run.start / ai.message / ai.steer 下发 surface:"phone"（字段
// 缺省 = TUI 通道，零变化）。phone 回合在最后一公里把契约包进 prompt/插话：
//
//   - 新回合：runUserMessage 里包 freshPrompt / resume content / TUI 路由
//     的原始 content（三路 provider 共用这一个闸口）；
//   - 插话：steer() 里包 steer.Content（claude/codex/agentcore 三个 control
//     共用）；外部 TUI 活进程插话（externalSteerAttempt）同样包——那是唯一
//     能影响 TUI 内 CC 行为的通道；
//   - 硬拦：claudeApprovalHookSettings 给 settings.permissions.deny 追加
//     AskUserQuestion，CC 直接拒绝并回执原因，模型自纠，不再静默悬死。
//
// 契约只走文本不落盘：TUI 自己打字的回合不带 surface，提示永远不会出现。

// agentAISurfacePhone 是目前唯一的远端驱动面标记。normalizeAgentAISurface
// 白名单式收敛：未来新增 surface 值时同步扩这里。
const agentAISurfacePhone = "phone"

// agentAIPhoneContractMarker 是包裹文本的幂等锚点：追加型注入（history 重放、
// optionResponse 续接）可能让同一内容被包第二次，命中锚点原样返回。
const agentAIPhoneContractMarker = "surface=phone：本回合由手机聊天驱动"

// normalizeAgentAISurface 只认白名单值；空串/未知值一律归 ""（= TUI 通道，
// 行为零变化），老 server 不发该字段也自然落在这里。
func normalizeAgentAISurface(value string) string {
	if value == agentAISurfacePhone {
		return agentAISurfacePhone
	}
	return ""
}

// agentAIPhoneContractReminder 返回手机驱动回合的行为契约文本。保持紧凑：
// 每条手机回合的用户消息/插话都会带上。
func agentAIPhoneContractReminder() string {
	return "<system-reminder>\n" +
		agentAIPhoneContractMarker + "。\n" +
		"- 用户看不到终端；结论要自包含、手机可读，不要让用户去看命令输出或文件行号。\n" +
		"- 需要用户选择/回答时：优先调用 mcp__aliang__ask_user 工具；不可用时输出 ```aliang-options JSON 围栏（title + options[{id,label,description}]，用三反引号围住）；再退纯文本编号列表等用户打字回复。\n" +
		"- 不要调用 AskUserQuestion 等终端交互工具（本环境已禁用，调用会直接报错）。\n" +
		"- 工具需要授权时正常发起，审批会以卡片形式推送到手机，无需终端确认。\n" +
		"</system-reminder>\n\n"
}

// wrapAgentAIPhoneContract 把契约包在内容前面；已含锚点则原样返回。
func wrapAgentAIPhoneContract(content string) string {
	if content == "" || strings.Contains(content, agentAIPhoneContractMarker) {
		return content
	}
	return agentAIPhoneContractReminder() + content
}
