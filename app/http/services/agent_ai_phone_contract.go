package services

import "strings"

// 手机驱动回合的行为契约（2026-10-26 手机行为契约设计；2026-10-27 收窄为
// TUI 注入专用）。
//
// server 随 ai.run.start / ai.message / ai.steer 下发 surface:"phone"（字段
// 缺省 = TUI 通道，零变化）。契约只在「手机消息跨进程注入正在运行的 TUI」
// 这一条通道上随文本下发，共三处：
//
//   - 新回合注入 TUI：runUserMessage 的 TUI 路由 → runTUITurn；
//   - 回合中插话注册表内 TUI：steer() 的 session.tuiTurn 分支 → steerTUITurn；
//   - 外部活 TUI 插话：externalSteerAttempt（会话不在注册表）。
//
// 这三处的共性是 TUI 里的 CC 是另一个进程，看不见宿主 agent 的权限配置，
// 注入文本是唯一能影响它行为的通道。
//
// headless 通道（claude -p / codex / agentcore）一律不注入本契约：它们由宿主
// 自己拉起，行为约束走 settings.permissions.deny（硬拦 AskUserQuestion）与
// --append-system-prompt 的 agentAIOptionSystemPrompt。往用户内容前面塞
// system-reminder 会把提示写进原生 transcript（--resume 后还翻得出来），
// 且 2026-10-27 明确这不是期望行为。
//
// 契约只走注入帧不落盘：TUI 自己打字的回合不带 surface，提示永远不会出现。
// 回程有闸：上游回传的 transcript 若带回这份契约（注入行会被 TUI 记进原生
// 转录），摄入历史时由 stripAgentAIPhoneContract 剥掉，不让它变成下一轮的
// 对话内容。

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
		"- 需要用户选择/回答时：优先调用 mcp__aliang__ask_user 工具；不可用时输出 ```aliang-options JSON 围栏——首行只有 ```aliang-options，下一行是独占一行的单个 JSON 对象，收尾三反引号独占一行（{\"title\",\"options\":[{\"id\",\"label\",\"description\",\"response\",\"previews\"}],\"multi\",\"allow_custom\"}；id 用 ≤48 字符短英文 slug、label 纯文本禁 markdown、其余字段按需省略，围栏后停下等用户选）；再退纯文本编号列表等用户打字回复。\n" +
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

// wrapAgentAIPhoneContractForSurface 是「注入正在运行的 TUI」这条通道的统一
// 闸口：surface=phone 才包，其余原样返回。headless 通道不得调用它（见文件头
// 注释：-p / codex / agentcore 的约束不走文本注入）。
func wrapAgentAIPhoneContractForSurface(surface, content string) string {
	if surface != agentAISurfacePhone {
		return content
	}
	return wrapAgentAIPhoneContract(content)
}

// stripAgentAIPhoneContract 从文本里剥掉本宿主注入的手机契约块，还原用户原文。
//
// 为什么需要：契约是给「正在运行的 TUI」的一次性提示，而那条通道的 tran-
// script 会被上游回传（ai.message / ai.run.start 的 transcript 字段），宿主
// 再把它当历史吃进来 —— 契约于是变成对话内容的一部分，随 --resume 的扁平化
// 提示与内核 messages 一轮轮回灌，等于自己给自己重复下指令。
//
// 只剥自己的块：以 agentAIPhoneContractMarker 为锚点，且该锚点必须落在
// <system-reminder> … </system-reminder> 之内。CC 自己写进转录的其它
// system-reminder 是合法上下文，删了会丢信息 —— 所以形状认不出时宁可原样返回。
func stripAgentAIPhoneContract(content string) string {
	if !strings.Contains(content, agentAIPhoneContractMarker) {
		return content
	}
	for strings.Contains(content, agentAIPhoneContractMarker) {
		start := strings.Index(content, "<system-reminder>")
		if start < 0 {
			break
		}
		// 连同包装时补的分隔空行一起切：agentAIPhoneContractReminder 以
		// "</system-reminder>\n\n" 收尾，只切标签会留下多余空行（重复注入时
		// 会越积越多）。兼容上游可能做过的换行归一化。
		rest, endLen := content[start:], 0
		for _, tail := range []string{"</system-reminder>\n\n", "</system-reminder>\r\n\r\n", "</system-reminder>"} {
			if idx := strings.Index(rest, tail); idx >= 0 {
				endLen = idx + len(tail)
				break
			}
		}
		if endLen == 0 {
			break
		}
		if !strings.Contains(content[start:start+endLen], agentAIPhoneContractMarker) {
			// 第一个提醒块不是我们的：不猜位置，原样保留。
			break
		}
		content = content[:start] + content[start+endLen:]
	}
	return content
}
