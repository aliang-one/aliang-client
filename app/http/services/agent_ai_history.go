package services

import (
	"encoding/json"
	"fmt"
	"strings"
)

func serializeGoalContext(raw interface{}) string {
	if raw == nil {
		return ""
	}
	gc, ok := raw.(map[string]interface{})
	if !ok {
		return ""
	}
	var b strings.Builder
	b.WriteString("=== Goal Context (authoritative) ===")

	// Objective + constraints
	if goal, ok := gc["goal"].(map[string]interface{}); ok {
		if obj, ok := goal["objective"].(string); ok && strings.TrimSpace(obj) != "" {
			b.WriteString("\nObjective: ")
			b.WriteString(obj)
		}
		if rev, ok := goal["revisionNumber"]; ok {
			b.WriteString(fmt.Sprintf("\nRevision: %v", rev))
		}
		if attempt, ok := goal["taskAttempt"]; ok {
			b.WriteString(fmt.Sprintf("\nAttempt: %v", attempt))
		}
		// Constraints / non-goals are the approved contract the executor must
		// honor. The server ships them in the envelope; rendering them here stops
		// the executor from drifting past the agreed scope.
		if cons, ok := goal["constraints"].([]interface{}); ok && len(cons) > 0 {
			b.WriteString("\nConstraints:")
			for _, c := range cons {
				if s, ok := c.(string); ok && strings.TrimSpace(s) != "" {
					b.WriteString("\n  - ")
					b.WriteString(s)
				}
			}
		}
		if ngs, ok := goal["nonGoals"].([]interface{}); ok && len(ngs) > 0 {
			b.WriteString("\nNon-goals (do NOT do these):")
			for _, c := range ngs {
				if s, ok := c.(string); ok && strings.TrimSpace(s) != "" {
					b.WriteString("\n  - ")
					b.WriteString(s)
				}
			}
		}
	}

	// Task details — THE critical part
	if task, ok := gc["task"].(map[string]interface{}); ok {
		b.WriteString("\n\n--- Current Task ---")
		if key, ok := task["key"].(string); ok {
			b.WriteString("\nKey: ")
			b.WriteString(key)
		}
		if title, ok := task["title"].(string); ok {
			b.WriteString("\nTitle: ")
			b.WriteString(title)
		}
		if desc, ok := task["description"].(string); ok && strings.TrimSpace(desc) != "" {
			b.WriteString("\nDescription: ")
			b.WriteString(desc)
		}
		if roots, ok := task["allowedRoots"].([]interface{}); ok && len(roots) > 0 {
			b.WriteString("\nAllowed roots: ")
			for i, r := range roots {
				if i > 0 {
					b.WriteString(", ")
				}
				b.WriteString(fmt.Sprintf("%v", r))
			}
		}
		if cmds, ok := task["allowedCommands"].([]interface{}); ok && len(cmds) > 0 {
			b.WriteString("\nAllowed commands: ")
			for i, c := range cmds {
				if i > 0 {
					b.WriteString(", ")
				}
				b.WriteString(fmt.Sprintf("%v", c))
			}
		}
		// Required checks — tells the AI what must pass AND what to produce.
		// Render the check definition (command/path/contains), not just the
		// name, so the executor knows exactly what condition to satisfy.
		if checks, ok := task["requiredChecks"].([]interface{}); ok && len(checks) > 0 {
			b.WriteString("\n\nRequired verification checks (your work must satisfy these):")
			for _, c := range checks {
				check, ok := c.(map[string]interface{})
				if !ok {
					continue
				}
				b.WriteString("\n  - ")
				if ck, ok := check["key"].(string); ok && ck != "" {
					b.WriteString(ck)
				}
				if ct, ok := check["type"].(string); ok && ct != "" {
					b.WriteString(" (" + ct + ")")
				}
				if cmd, ok := check["command"].(string); ok && strings.TrimSpace(cmd) != "" {
					b.WriteString("\n      command: " + strings.TrimSpace(cmd))
				}
				if p, ok := check["path"].(string); ok && strings.TrimSpace(p) != "" {
					b.WriteString("\n      path: " + strings.TrimSpace(p))
				}
				if con, ok := check["contains"].(string); ok && strings.TrimSpace(con) != "" {
					b.WriteString("\n      must contain: " + strings.TrimSpace(con))
				}
			}
		}
	}

	// Queued inputs (user messages that clarify the task)
	if inputs, ok := gc["queuedInputs"].([]interface{}); ok && len(inputs) > 0 {
		b.WriteString("\n\n--- Queued Inputs ---")
		for _, inp := range inputs {
			if input, ok := inp.(map[string]interface{}); ok {
				if content, ok := input["content"].(string); ok && strings.TrimSpace(content) != "" {
					b.WriteString("\n  ")
					b.WriteString(strings.TrimSpace(content))
				}
			}
		}
	}

	// Plan overview: every task with state + deps so the executor sees the big
	// picture (what's done, what's next, what interfaces downstream tasks need).
	if plan, ok := gc["plan"].([]interface{}); ok && len(plan) > 0 {
		b.WriteString("\n\n--- Plan (big picture) ---")
		for _, p := range plan {
			pt, ok := p.(map[string]interface{})
			if !ok {
				continue
			}
			key, _ := pt["key"].(string)
			title, _ := pt["title"].(string)
			b.WriteString(fmt.Sprintf("\n  - %s %s", key, title))
			if state, _ := pt["state"].(string); state != "" {
				b.WriteString(" [" + state + "]")
			}
			if deps, ok := pt["dependsOn"].([]interface{}); ok && len(deps) > 0 {
				b.WriteString(" after:")
				for i, d := range deps {
					if i > 0 {
						b.WriteString(",")
					}
					b.WriteString(fmt.Sprintf(" %v", d))
				}
			}
			if n, ok := pt["requiredChecks"]; ok {
				b.WriteString(fmt.Sprintf(" (%v checks)", n))
			}
		}
	}
	// Goal progress: distance-to-goal + recent failures. Breaks the repeated
	// blind-failure loop (failedTaskOutputs reach the executor only here).
	if gp, ok := gc["goalProgress"].(map[string]interface{}); ok {
		b.WriteString("\n\n--- Goal Progress ---")
		b.WriteString(fmt.Sprintf("\nCompleted: %v / %v", gp["completedTasks"], gp["totalTasks"]))
		if rem, ok := gp["remainingTasks"].([]interface{}); ok && len(rem) > 0 {
			b.WriteString("\nRemaining:")
			for _, r := range rem {
				if rm, ok := r.(map[string]interface{}); ok {
					k, _ := rm["key"].(string)
					t, _ := rm["title"].(string)
					b.WriteString(fmt.Sprintf("\n  - %s %s", k, t))
				}
			}
		}
		if fails, ok := gp["recentFailures"].([]interface{}); ok && len(fails) > 0 {
			b.WriteString("\nRecent failures (do not repeat these):")
			for _, f := range fails {
				if fm, ok := f.(map[string]interface{}); ok {
					tk, _ := fm["taskKey"].(string)
					summary, _ := fm["summary"].(string)
					b.WriteString(fmt.Sprintf("\n  - %s: %s", tk, summary))
				}
			}
		}
	}
	// Budget: rendered generically (shape is opaque to the agent). Lets the
	// executor see the remaining token/time envelope and self-throttle.
	if budget, ok := gc["budget"]; ok && budget != nil {
		if bj, err := json.Marshal(budget); err == nil {
			if s := string(bj); s != "null" && s != "{}" {
				b.WriteString("\n\n--- Budget ---")
				b.WriteString("\n" + s)
			}
		}
	}

	b.WriteString("\n=== End Goal Context ===")
	return b.String()
}

func buildAgentAIPrompt(session *agentAISession, latestContent string) string {
	history := trimAgentAIHistoryForSession(session, append([]agentAIMessage(nil), session.history...))
	var builder strings.Builder
	builder.WriteString("You are continuing an Aliang remote agent AI chat session.\n")
	builder.WriteString("Use the existing conversation as context and answer the latest user message.\n")
	builder.WriteString("Project path: ")
	builder.WriteString(session.projectPath)
	builder.WriteString("\nMode: ")
	builder.WriteString(session.mode)
	builder.WriteString("\n\nConversation:\n")
	for _, item := range history {
		builder.WriteString(agentAIRoleLabel(item.Role))
		builder.WriteString(": ")
		builder.WriteString(strings.TrimSpace(item.Content))
		builder.WriteString("\n\n")
	}
	if len(history) == 0 {
		builder.WriteString("User: ")
		builder.WriteString(strings.TrimSpace(latestContent))
		builder.WriteString("\n\n")
	}
	builder.WriteString("Assistant:")
	return builder.String()
}

func agentAIRoleLabel(role string) string {
	role = strings.ToLower(strings.TrimSpace(role))
	switch role {
	case "user":
		return "User"
	case "assistant":
		return "Assistant"
	case "system":
		return "System"
	case "":
		return "Message"
	default:
		return strings.ToUpper(role[:1]) + role[1:]
	}
}

// trimAgentAIHistory 是默认档：最多 16 条 / 64000 字符。
//
// 这档上限服务的是「宿主历史只用来拼初始 prompt 或生成会话摘要」的 provider
// ——claude/codex 的上下文在 CLI 自己的会话里，宿主历史短一点无所谓。

func trimAgentAIHistory(history []agentAIMessage) []agentAIMessage {
	return trimAgentAIHistoryWithLimits(history, agentAIHistoryDefaultMessages, agentAIHistoryDefaultChars)
}

const (
	agentAIHistoryDefaultMessages = 16
	agentAIHistoryDefaultChars    = 64000
)

// agentAIHistoryLimits 返回某个会话应保留的历史上限。
//
// 内置内核（agentcore）直接消费这份历史：固定 16 条 / 64k 字符会把「手机上
// 选了大窗口」变成一句空话——16 条消息远到不了 100k token，长对话在宿主这
// 一层就被截断，内核的自动压缩永远不会触发。所以内核会话按选定窗口换算，
// 其余 provider 维持原值（它们的上下文由 CLI 自己管理）。

func agentAIHistoryLimits(provider string, contextWindow int) (maxMessages, maxChars int) {
	if !agentAIUseAgentCore(provider) {
		return agentAIHistoryDefaultMessages, agentAIHistoryDefaultChars
	}
	contextWindow = clampAgentAIContextWindow(contextWindow)
	if contextWindow <= 0 {
		// 与内核 DefaultContextWindow 对齐：手机没传窗口时内核按 200k 压缩，
		// 宿主先把历史截到 64k 字符会让这个 200k 永远用不满。
		contextWindow = agentAIContextWindowDefault
	}
	// 1 token ≈ 2 字符是保守换算：CJK 约 1 字符/token，ASCII 约 4 字符/token。
	// 宁可多留一点让内核按真实估算去压，也不要在宿主层过早截断。
	maxChars = contextWindow * 2
	maxMessages = contextWindow / 100
	if maxMessages < agentAIHistoryDefaultMessages {
		maxMessages = agentAIHistoryDefaultMessages
	}
	return maxMessages, maxChars
}

// trimAgentAIHistoryForSession 按会话选定的 provider/窗口裁剪历史。

func trimAgentAIHistoryForSession(session *agentAISession, history []agentAIMessage) []agentAIMessage {
	if session == nil {
		return trimAgentAIHistory(history)
	}
	maxMessages, maxChars := agentAIHistoryLimits(session.provider, session.contextWindow)
	return trimAgentAIHistoryWithLimits(history, maxMessages, maxChars)
}

func trimAgentAIHistoryWithLimits(history []agentAIMessage, maxMessages, maxChars int) []agentAIMessage {
	if len(history) > maxMessages {
		history = history[len(history)-maxMessages:]
	}
	total := 0
	start := len(history)
	for i := len(history) - 1; i >= 0; i-- {
		total += len(history[i].Content)
		if total > maxChars {
			break
		}
		start = i
	}
	if start > 0 && start < len(history) {
		history = history[start:]
	}
	return history
}
