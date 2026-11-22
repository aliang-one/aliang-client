package services

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/common/logger"
)

func streamStructuredAIDelta(reader io.Reader, format agentAIOutputFormat, run agentAIRun, writeJSON agentTerminalWriter, limiter *agentAIOutputLimiter, capture func(string), fileSink func(string), lastRetry *claudeRetryInfo, resumeSessionID *string, onSessionID ...func(string)) {
	decoder := json.NewDecoder(reader)
	// Per-message streamed-text buffer + last finalized text. Replaces the old
	// per-run `emitted` flag (which, once set by the first text_delta,永久抑制
	// 后续所有 finalized assistant 文本). Now: text_delta 积进 currentStreamed；
	// 每个 finalized assistant 事件只补发未流过的后缀并重置 currentStreamed
	// (按 message 边界去重)。result 事件对齐 lastFinalText 去重。这样工具型
	// 多段 assistant 回合里，即便某段文本只出现在 finalized 事件(未经
	// text_delta 流式)，也能实时作为 ai.delta 发出，不再被丢到结束才回拉。
	currentStreamed := ""
	lastFinalText := ""
	// pendingCommands tracks Bash tool_use_id → command so a later user-turn
	// tool_result can close it as ai.command(completed) with output + exit code.
	pendingCommands := map[string]string{}
	// claudeBlockTypes tracks content_block index → block type (thinking/text/
	// tool_use) so content_block_stop can emit an explicit ai.thinking
	// active:false when a thinking span ends — giving the thinking activity a
	// reliable stop boundary (not only the run-terminal ai.done).
	claudeBlockTypes := map[int]string{}
	// pendingClaudeToolUseIDs tracks every Claude tool_use whose tool_result has
	// not arrived yet. This keeps the idle watchdog from killing a quiet but
	// legitimate Task/subagent or long-running tool wait.
	pendingClaudeToolUseIDs := map[string]struct{}{}
	for {
		var event map[string]interface{}
		if err := decoder.Decode(&event); err != nil {
			return
		}
		// Capture the provider's resumable session id from whichever structured
		// event first carries it. Pinned after a successful run so the next turn
		// resumes one continuous local session instead of N fresh runs.
		if format == agentAIOutputClaudeStreamJSON && resumeSessionID != nil && *resumeSessionID == "" {
			if sid := strings.TrimSpace(remoteString(event, "session_id")); sid != "" {
				*resumeSessionID = sid
				logger.Info(fmt.Sprintf("claude session_id captured for run %s: %s (from %s event)", run.sessionID, sid, remoteString(event, "type")))
				if len(onSessionID) > 0 && onSessionID[0] != nil {
					onSessionID[0](sid)
				}
			}
		}
		if format == agentAIOutputClaudeStreamJSON &&
			remoteString(event, "type") == "system" &&
			remoteString(event, "subtype") == "init" &&
			run.onClaudeInit != nil {
			run.onClaudeInit(
				remoteStringSlice(event, "slash_commands"),
				firstNonEmpty(remoteString(event, "claude_code_version"), remoteString(event, "version")),
			)
		}
		// Capture the provider's native assistant message id for the CURRENT
		// turn (claude message_start / finalized assistant). Streaming events
		// then carry it so each turn is a distinct transcript message + distinct
		// structured events — matching the post-refresh native-log view, instead
		// of the whole run collapsing into one assistant bubble + one growing
		// thinking event.
		if format == agentAIOutputClaudeStreamJSON {
			if mid := claudeStreamMessageID(event); mid != "" && mid != run.providerMsgID {
				run.providerMsgID = mid
			}
		}
		if format == agentAIOutputOpenCodeJSON && resumeSessionID != nil && *resumeSessionID == "" {
			if sid := openCodeSessionID(event); sid != "" {
				*resumeSessionID = sid
				logger.Info(fmt.Sprintf("opencode session id captured for run %s: %s (from %s event)", run.sessionID, sid, remoteString(event, "type")))
				if len(onSessionID) > 0 && onSessionID[0] != nil {
					onSessionID[0](sid)
				}
			}
		}
		if fileSink != nil {
			for _, fp := range extractToolUseFilePaths(format, event) {
				fileSink(fp)
			}
		}
		if format == agentAIOutputClaudeStreamJSON {
			updateClaudeToolUseActivity(event, run.activity, pendingClaudeToolUseIDs)
			// 子 agent 生命周期帧（system/task_*）→ 注册表 + ai.subagent 事件
			// + 看门狗喂食。放在 activity 配对之后、结构化发射之前，保证同一
			// 帧上 activity 状态先于 wire 事件就绪。
			handleClaudeTaskEvents(event, run, writeJSON)
			// agentcore 子agent状态镜像帧（ai.subagent）直接透传：帧形已是
			// 协议形状（agentAISubagentPayload），registry/activity 看门狗喂食
			// 已在桥侧（agentCoreSubagentMirror）完成——这里只负责把帧送进
			// 下行：writeJSON = origin 写 + 本地 WS 扇出 + 云端转发，手机 App
			// （SubagentRows）与仪表盘面板各取所需。无此分支镜像帧死在解码器
			// 里（2026-11-07 实锤：已合并的镜像功能空转，两端全收不到）。
			// claude CLI 自身不产该 type，无双发。
			if remoteString(event, "type") == models.AgentEventAISubagent {
				_ = writeJSON(event)
				continue
			}
			// 诊断聚合：assistant 帧（回合数/最近用量/停止原因）与 result 帧
			// （终态）喂给 claudeRunDiagnostics，进度 ticker 与 runCLIPass 收尾
			// 读它发 ai.run.progress.diagnostics。nil 安全（无挂载即跳过）。
			run.claudeDiag.observe(event)
		}
		emitClaudeStructuredEvents(format, event, run, writeJSON, pendingCommands, claudeBlockTypes)
		emitOpenCodeStructuredEvents(format, event, run, writeJSON)
		if format == agentAIOutputOpenCodeJSON {
			if reason, isErr := openCodeEventError(event); isErr {
				_ = writeJSON(agentAIErrorPayload(run.sessionID, run.messageID, errors.New(reason)))
				if run.cancel != nil {
					run.cancel()
				}
				return
			}
		}
		// Claude surfaces each upstream retry as a structured system event:
		// {"type":"system","subtype":"api_retry", attempt, max_retries,
		// retry_delay_ms, error_status, error}. Forward it as ai.run.progress so
		// the phone can show "重试 2/10 · 网关 502" instead of a silent gap, and
		// remember the last one to enrich the terminal ai.error with a structured
		// cause. The CLI's own "Retrying..." text never appears in stream-json, so
		// this event is the only retry signal available.
		//
		// Liveness: an api_retry frame means the provider talked to us and is
		// retrying — real progress by the same standard as ai.thinking. The
		// agentcore kernel's retry chain can legally stay silent ~8min worst case
		// (4 attempts × ResponseHeaderTimeout 120s + backoff), which blows past
		// the 5min stale threshold; each retry frame must refresh the activity
		// clock or healthy retrying turns get killed as stalled. Same refresh
		// extends the steer liveness window, which reads the same idleFor().
		if format == agentAIOutputClaudeStreamJSON &&
			remoteString(event, "type") == "system" &&
			remoteString(event, "subtype") == "api_retry" {
			run.activity.markProgress()
			ri := claudeRetryInfo{has: true}
			if n, ok := event["attempt"].(float64); ok {
				ri.attempt = int(n)
			}
			if n, ok := event["max_retries"].(float64); ok {
				ri.max = int(n)
			}
			if n, ok := event["retry_delay_ms"].(float64); ok {
				ri.delayMs = n
			}
			if n, ok := event["error_status"].(float64); ok {
				ri.errorStatus = int(n)
			}
			ri.errorType = remoteString(event, "error")
			if lastRetry != nil {
				*lastRetry = ri
			}
			logger.Warn(fmt.Sprintf(
				"ai.run.claude_retry: session=%s runSeq=%d attempt=%d max=%d delay_ms=%.0f status=%d error=%q",
				run.sessionID,
				run.runSeq,
				ri.attempt,
				ri.max,
				ri.delayMs,
				ri.errorStatus,
				ri.errorType,
			))
			progress := map[string]interface{}{
				"type":         models.AgentEventAIRunProgress,
				"session_id":   run.sessionID,
				"retry_active": true,
			}
			if ri.attempt > 0 {
				progress["retry_attempt"] = ri.attempt
			}
			if ri.max > 0 {
				progress["retry_max"] = ri.max
			}
			if ri.delayMs > 0 {
				progress["retry_delay_ms"] = ri.delayMs
			}
			if ri.errorStatus > 0 {
				progress["error_status"] = ri.errorStatus
			}
			if ri.errorType != "" {
				progress["error_type"] = ri.errorType
			}
			_ = writeJSON(progress)
			continue
		}
		// agentcore 内核的压缩进度帧：{"type":"system","subtype":
		// "compact_progress","summary_tokens":N}。压缩期是全程零 delta 的长
		// 静默（8192 token 的慢摘要在 20-25 tok/s 下超过 5min stale 阈值），
		// 这帧是服务层唯一活性信号；markProgress 同时刷新 stale 扫描器与
		// steer 活性窗（同源 idleFor）。与 api_retry 同一套镜像模式。
		//
		// 刻意不转发给手机：CLI 路径的压缩提示是 compact_boundary 完成帧
		// （claudeCompactNotice），手机契约里没有「压缩中」态，转发需要新增
		// 手机契约字段——按最小改动只做 markProgress + 日志；压缩完成时用户
		// 仍会收到既有的 compact_boundary 可见提示。
		if format == agentAIOutputClaudeStreamJSON &&
			remoteString(event, "type") == "system" &&
			remoteString(event, "subtype") == "compact_progress" {
			run.activity.markProgress()
			logger.Info(fmt.Sprintf(
				"ai.run.compact_progress: session=%s runSeq=%d summary_tokens=%v",
				run.sessionID, run.runSeq, event["summary_tokens"]))
			continue
		}
		// Claude steer：第一个 result 事件意味着本 turn 的产出已定稿。
		// 关闭 stdin 让 CLI 自然退出（约 0.5s）；实测「已进入管道」的 steer
		// 仍会被 CLI 读完并执行，所以这里不必等待队列排空。放在错误分支之前，
		// 失败 result 同样要关闸，否则进程会一直等输入。
		if format == agentAIOutputClaudeStreamJSON && run.claudeSteer != nil &&
			remoteString(event, "type") == "result" {
			run.claudeSteer.closeInput()
		}
		// A Claude "result" event that failed (is_error / error_* subtype) must
		// surface as ai.error with a structured cause, not be masked as an
		// assistant delta (which would make the turn look successful). Stop the
		// stream. The raw result text is kept as debug "detail" only — the phone
		// renders the structured cause derived from the last api_retry.
		if format == agentAIOutputClaudeStreamJSON {
			if reason, isErr := claudeResultError(event); isErr {
				ri := claudeRetryInfo{}
				if lastRetry != nil {
					ri = *lastRetry
				}
				var cause, detail string
				if ri.has && ri.errorStatus > 0 {
					cause = claudeFailureCause(nil, ri, "")
					detail = reason
				} else if reason != "" {
					cause = reason
				} else {
					cause = "claude run failed"
				}
				payload := agentAIErrorPayloadWithRetry(run.sessionID, run.messageID, errors.New(cause), ri)
				if detail != "" {
					payload["detail"] = truncateForCloud(detail)
				}
				_ = writeJSON(payload)
				return
			}
			// (claude session_id capture moved to the top of the decode loop —
			// grabs the first non-empty session_id from any event, not just the
			// result event, since system/init always carries it.)
		}
		parts := extractStructuredAITexts(format, event)
		for _, d := range parts.deltas {
			// 空白 delta（独立的 "\n"、独立空格）也必须原样上线：丢掉它们会让
			// 落库文本缺字节——```aliang-options 围栏标签后的换行被吃（围栏
			// 残废）、token 边界随机吃空格（2026-10-26 RCA）。emitAIDelta 对
			// 真空串自行短路；currentStreamed 继续记账，suffix 去重不受影响。
			if !emitAIDelta(d, run, "assistant", writeJSON, limiter, capture) {
				return
			}
			currentStreamed += d
		}
		if parts.finalText != "" {
			// A finalized assistant message emits only the text not already
			// streamed for this message (suffix). The run result event is the
			// LAST assistant message's text — dedup against whatever was emitted
			// for it: the streamed deltas if it never finalized, else the
			// finalized text. Either way, never silently drop finalized text
			// (the old global-flag bug) nor double-emit already-streamed text.
			reference := currentStreamed
			if parts.isResult && currentStreamed == "" {
				reference = lastFinalText
			}
			if suffix := suffixNotStreamed(reference, parts.finalText); suffix != "" {
				if !emitAIDelta(suffix, run, "assistant", writeJSON, limiter, capture) {
					return
				}
			}
			if parts.isResult {
				lastFinalText = parts.finalText
			} else {
				currentStreamed = ""
				lastFinalText = parts.finalText
			}
		}
	}
}

func updateClaudeToolUseActivity(event map[string]interface{}, activity *agentAIActivity, pending map[string]struct{}) {
	if activity == nil || pending == nil {
		return
	}
	// 子 agent 内部的 tool_use/tool_result 带 parent_tool_use_id（实测 --print
	// stream-json 默认全量下发）。它们必须在这里止步：否则内部配对会双计
	// pendingToolUses、掩盖父 Agent 等待的真实边界。子 agent 活跃度由
	// handleClaudeTaskEvents 经 beginSubagent/subagentProgress 单独喂。
	if remoteString(event, "parent_tool_use_id") != "" {
		return
	}
	message := mapIf(event["message"])
	if message == nil {
		return
	}
	content, _ := message["content"].([]interface{})
	if len(content) == 0 {
		return
	}
	switch remoteString(event, "type") {
	case "assistant":
		for _, raw := range content {
			row := mapIf(raw)
			if row == nil || remoteString(row, "type") != "tool_use" {
				continue
			}
			id := remoteString(row, "id")
			if id == "" {
				continue
			}
			if _, exists := pending[id]; exists {
				continue
			}
			pending[id] = struct{}{}
			activity.beginToolUseWait()
		}
	case "user":
		for _, raw := range content {
			row := mapIf(raw)
			if row == nil || remoteString(row, "type") != "tool_result" {
				continue
			}
			id := remoteString(row, "tool_use_id")
			if id == "" {
				continue
			}
			if _, exists := pending[id]; !exists {
				continue
			}
			delete(pending, id)
			activity.endToolUseWait()
		}
	}
}

// aiStreamTexts describes the user-visible text carried by one structured
// stream event, split into two channels that the run loop dedups differently:
//
//   - deltas    — streaming text_delta fragments. Always emitted as they
//     arrive, and accumulated into the current message's streamed buffer.
//   - finalText — the finalized assistant message's full text (a message
//     boundary). Emitted only as the un-streamed suffix (see suffixNotStreamed)
//     so messages whose text arrived only in the finalized event — never as
//     text_delta partials — are still delivered in real time.
//
// This replaces the old global per-run `emitted`/`allowFinal` flag, which once
// the first text_delta set it permanently suppressed every later finalized
// assistant message, dropping its text on the wire (only recoverable later by
// the server's end-of-run session-detail pull). isResult marks the run's
// terminal result event (deduped against the last assistant message instead
// of resetting the per-message buffer).

type aiStreamTexts struct {
	deltas    []string
	finalText string
	isResult  bool
}

func extractStructuredAITexts(format agentAIOutputFormat, event map[string]interface{}) aiStreamTexts {
	switch format {
	case agentAIOutputClaudeStreamJSON:
		return extractClaudeStreamTexts(event)
	case agentAIOutputCodexJSON:
		return extractCodexJSONTexts(event)
	case agentAIOutputOpenCodeJSON:
		return extractOpenCodeJSONTexts(event)
	default:
		return aiStreamTexts{}
	}
}

// suffixNotStreamed returns the portion of final that was not already streamed.
//   - Empty final ⇒ nothing to emit.
//   - streamed already covers final (or more) ⇒ nothing.
//   - final extends streamed ⇒ the un-streamed tail.
//   - they diverge (neither is a prefix of the other) ⇒ final wholesale.
//   - Empty streamed ⇒ all of final (recovers finalized-only messages).
//
// 前缀比较失败时先做一次空白容忍对齐（逐 rune，双侧空白等价）：result 事件与
// finalized 文本的排版可能有空白差异——这些都不该触发整段重发。真分叉（出现
// 不同字符）仍保持旧行为整体重发，绝不静默丢字。

func suffixNotStreamed(streamed, final string) string {
	if final == "" {
		return ""
	}
	if strings.HasPrefix(streamed, final) {
		return ""
	}
	if strings.HasPrefix(final, streamed) {
		return final[len(streamed):]
	}
	streamedRunes := []rune(streamed)
	finalRunes := []rune(final)
	i, j := 0, 0
	for i < len(streamedRunes) && j < len(finalRunes) {
		switch {
		case unicode.IsSpace(streamedRunes[i]) && unicode.IsSpace(finalRunes[j]):
			for i < len(streamedRunes) && unicode.IsSpace(streamedRunes[i]) {
				i++
			}
			for j < len(finalRunes) && unicode.IsSpace(finalRunes[j]) {
				j++
			}
		case unicode.IsSpace(streamedRunes[i]):
			i++
		case unicode.IsSpace(finalRunes[j]):
			j++
		case streamedRunes[i] == finalRunes[j]:
			i++
			j++
		default:
			return final
		}
	}
	if i < len(streamedRunes) {
		// 流式侧还有最终文本没覆盖到的内容 → 真分叉，整体重发。
		return final
	}
	return string(finalRunes[j:])
}

func extractOpenCodeJSONTexts(event map[string]interface{}) aiStreamTexts {
	var out aiStreamTexts
	eventType := strings.ToLower(strings.TrimSpace(remoteString(event, "type")))
	props := openCodeEventProperties(event)
	if strings.Contains(eventType, "part.delta") {
		if text := firstNonBlankPreserveSpace(remoteString(props, "delta"), remoteString(event, "delta")); text != "" {
			out.deltas = []string{text}
			return out
		}
	}
	// Finalized (per-message) text — emitted by the loop as the un-streamed
	// suffix. Routing it here (instead of the old allowFinal-guarded return)
	// means a later opencode assistant message whose text arrives only in its
	// finalized event is no longer dropped by a global per-run flag.
	if part := firstOpenCodeMap(props["part"], event["part"]); part != nil {
		if text := openCodePartText(part); text != "" {
			out.finalText = text
			return out
		}
	}
	if message := firstOpenCodeMap(props["message"], event["message"]); message != nil {
		if role := strings.ToLower(strings.TrimSpace(remoteString(message, "role"))); role == "" || role == "assistant" {
			if texts := openCodeMessageTexts(message["parts"]); len(texts) > 0 {
				out.finalText = strings.Join(texts, "")
				return out
			}
			if text := firstNonBlankPreserveSpace(remoteString(message, "text"), remoteString(message, "content")); text != "" {
				out.finalText = text
				return out
			}
		}
	}
	if role := strings.ToLower(strings.TrimSpace(remoteString(event, "role"))); role == "assistant" {
		if texts := openCodeMessageTexts(event["parts"]); len(texts) > 0 {
			out.finalText = strings.Join(texts, "")
			return out
		}
		if text := firstNonBlankPreserveSpace(remoteString(event, "text"), remoteString(event, "content")); text != "" {
			out.finalText = text
			return out
		}
	}
	if strings.Contains(eventType, "message") || strings.Contains(eventType, "assistant") {
		if text := firstNonBlankPreserveSpace(remoteString(props, "text"), remoteString(props, "content"), remoteString(event, "text"), remoteString(event, "content")); text != "" {
			out.finalText = text
			return out
		}
	}
	return out
}

func openCodeEventProperties(event map[string]interface{}) map[string]interface{} {
	if props := mapIf(event["properties"]); props != nil {
		return props
	}
	if data := mapIf(event["data"]); data != nil {
		return data
	}
	return event
}

func openCodeSessionID(event map[string]interface{}) string {
	props := openCodeEventProperties(event)
	if sid := strings.TrimSpace(firstNonEmpty(
		remoteString(event, "sessionID"),
		remoteString(event, "sessionId"),
		remoteString(event, "session_id"),
		remoteString(props, "sessionID"),
		remoteString(props, "sessionId"),
		remoteString(props, "session_id"),
	)); sid != "" {
		return sid
	}
	for _, key := range []string{"session", "info"} {
		if nested := mapIf(props[key]); nested != nil {
			if sid := strings.TrimSpace(firstNonEmpty(remoteString(nested, "id"), remoteString(nested, "sessionID"), remoteString(nested, "session_id"))); sid != "" {
				return sid
			}
		}
	}
	return ""
}

func firstOpenCodeMap(values ...interface{}) map[string]interface{} {
	for _, value := range values {
		if row := mapIf(value); row != nil {
			return row
		}
	}
	return nil
}

func firstNonBlankPreserveSpace(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func openCodeMessageTexts(raw interface{}) []string {
	parts, ok := raw.([]interface{})
	if !ok {
		return nil
	}
	var out []string
	for _, rawPart := range parts {
		part := mapIf(rawPart)
		if part == nil {
			continue
		}
		if text := openCodePartText(part); text != "" {
			out = append(out, text)
		}
	}
	return out
}

func openCodePartText(part map[string]interface{}) string {
	partType := strings.ToLower(strings.TrimSpace(remoteString(part, "type")))
	if partType != "" {
		for _, blocked := range []string{"tool", "bash", "command", "permission", "reasoning", "thinking"} {
			if strings.Contains(partType, blocked) {
				return ""
			}
		}
	}
	if text := firstNonBlankPreserveSpace(remoteString(part, "text"), remoteString(part, "content"), remoteString(part, "delta")); text != "" {
		return text
	}
	if content := agentContentText(part["content"]); strings.TrimSpace(content) != "" {
		return strings.TrimSpace(content)
	}
	return ""
}

func openCodeEventError(event map[string]interface{}) (string, bool) {
	eventType := strings.ToLower(strings.TrimSpace(remoteString(event, "type")))
	if !strings.Contains(eventType, "error") && event["error"] == nil {
		return "", false
	}
	props := openCodeEventProperties(event)
	for _, raw := range []interface{}{props["error"], event["error"], props, event} {
		if row := mapIf(raw); row != nil {
			if message := strings.TrimSpace(firstNonEmpty(
				remoteString(row, "message"),
				remoteString(row, "detail"),
				remoteString(row, "name"),
			)); message != "" {
				return message, true
			}
		}
		if message, ok := raw.(string); ok && strings.TrimSpace(message) != "" {
			return strings.TrimSpace(message), true
		}
	}
	return firstNonEmpty(eventType, "opencode run failed"), true
}

func extractClaudeStreamTexts(event map[string]interface{}) aiStreamTexts {
	var out aiStreamTexts
	// 子 agent 的内部文本（parented assistant / stream_event delta）不得混进
	// 父会话的 ai.delta——它们是子 agent 的自言自语，混进来会让用户看到两路
	// 交织的半截回复。父会话自身的帧 parent_tool_use_id 为 null，不受影响。
	if remoteString(event, "parent_tool_use_id") != "" {
		return out
	}
	if remoteString(event, "type") == "stream_event" {
		if streamEvent, ok := event["event"].(map[string]interface{}); ok && remoteString(streamEvent, "type") == "content_block_delta" {
			if delta, ok := streamEvent["delta"].(map[string]interface{}); ok && remoteString(delta, "type") == "text_delta" {
				if text := remoteString(delta, "text"); text != "" {
					out.deltas = []string{text}
				}
			}
		}
		return out
	}
	switch remoteString(event, "type") {
	case "assistant":
		if message, ok := event["message"].(map[string]interface{}); ok {
			// Claude emits a synthetic assistant message (model "<synthetic>",
			// carrying the error text) on terminal failure. Streaming it as a
			// normal reply masks the failure — skip it; the result event (or the
			// CLI exit) surfaces the failure as ai.error.
			if remoteString(message, "model") == "<synthetic>" {
				return out
			}
			if text := claudeMessageContentText(message["content"]); text != "" {
				out.finalText = text
			}
		}
	case "result":
		// A failed result (is_error / error_* subtype) must NOT be emitted as an
		// assistant delta — that masks the failure as a successful reply. The
		// streaming loop surfaces it as ai.error via claudeResultError.
		if _, isErr := claudeResultError(event); isErr {
			return out
		}
		if text := remoteString(event, "result"); text != "" {
			// 斜杠命令（/compact 等）的 headless 结果会被 CLI 包在
			// <local-command-stdout> 等标记里；标记不是给用户看的内容。
			if cleaned := stripLocalCommandMarkup(text); cleaned != "" {
				out.finalText = cleaned
				out.isResult = true
			}
		}
	}
	return out
}

// stripLocalCommandMarkup 去掉 CLI 斜杠命令输出的成对标记，保留其中的正文。
//
// headless 下 /compact 之类命令的结果会被包成
// <local-command-stdout>…</local-command-stdout>；<local-command-caveat> 块
// 是给模型的提示语，整体丢弃。标记缺失/未闭合时按容错处理，不吞正文。

func stripLocalCommandMarkup(text string) string {
	text = stripLocalCommandTag(text, "<local-command-caveat>", "</local-command-caveat>", true)
	text = stripLocalCommandTag(text, "<local-command-stdout>", "</local-command-stdout>", false)
	text = stripLocalCommandTag(text, "<local-command-stderr>", "</local-command-stderr>", false)
	return strings.TrimSpace(text)
}

func stripLocalCommandTag(text, open, close string, dropInner bool) string {
	var b strings.Builder
	rest := text
	for {
		i := strings.Index(rest, open)
		if i < 0 {
			b.WriteString(rest)
			return b.String()
		}
		j := strings.Index(rest[i+len(open):], close)
		b.WriteString(rest[:i])
		if j < 0 {
			b.WriteString(rest[i+len(open):])
			return b.String()
		}
		j += i + len(open)
		if !dropInner {
			b.WriteString(rest[i+len(open) : j])
		}
		rest = rest[j+len(close):]
	}
}

// claudeResultError reports whether a Claude stream-json "result" event is a
// failure (is_error == true, or an error_* subtype such as
// error_during_execution / error_max_tokens) and, if so, returns a human
// reason — the result text, falling back to the subtype. Non-result / success
// events return ("", false). Used by the streaming loop to surface the failure
// as ai.error instead of letting it be masked as an assistant delta.

func claudeResultError(event map[string]interface{}) (string, bool) {
	if remoteString(event, "type") != "result" {
		return "", false
	}
	isErr, _ := event["is_error"].(bool)
	subtype := remoteString(event, "subtype")
	if !isErr && !strings.HasPrefix(subtype, "error_") {
		return "", false
	}
	reason := strings.TrimSpace(remoteString(event, "result"))
	if reason == "" {
		reason = subtype
	}
	if reason == "" {
		reason = "claude run failed"
	}
	return reason, true
}

func claudeMessageContentText(raw interface{}) string {
	items, ok := raw.([]interface{})
	if !ok || len(items) == 0 {
		return ""
	}
	var builder strings.Builder
	for _, item := range items {
		row, ok := item.(map[string]interface{})
		if !ok || remoteString(row, "type") != "text" {
			continue
		}
		builder.WriteString(remoteString(row, "text"))
	}
	return builder.String()
}

// claudeMessageToolUseFilePaths collects distinct target paths of file-mutating
// tool_use blocks (Write/Edit/MultiEdit) from a finalized Claude assistant
// message's content array. Read-only tools and notebook ops are ignored — these
// are the files the run actually changed, surfaced to the mobile dashboard.

func claudeMessageToolUseFilePaths(raw interface{}) []string {
	items, ok := raw.([]interface{})
	if !ok {
		return nil
	}
	var paths []string
	for _, item := range items {
		row, ok := item.(map[string]interface{})
		if !ok || remoteString(row, "type") != "tool_use" {
			continue
		}
		switch remoteString(row, "name") {
		case "Write", "Edit", "MultiEdit":
		default:
			continue
		}
		input, ok := row["input"].(map[string]interface{})
		if !ok {
			continue
		}
		if fp := strings.TrimSpace(remoteString(input, "file_path")); fp != "" {
			paths = append(paths, fp)
		}
	}
	return paths
}

// extractToolUseFilePaths pulls file-mutating tool_use target paths from a
// structured AI stream event. For Claude stream-json the finalized "assistant"
// message event carries complete tool_use blocks with their inputs (the
// per-token input_json_delta fragments are intentionally not reconstructed).
// Codex file changes are approval-routed and left as a follow-up.

func extractToolUseFilePaths(format agentAIOutputFormat, event map[string]interface{}) []string {
	if format == agentAIOutputClaudeStreamJSON && remoteString(event, "type") == "assistant" {
		if message, ok := event["message"].(map[string]interface{}); ok {
			return claudeMessageToolUseFilePaths(message["content"])
		}
	}
	return nil
}

// countTextLines reports the number of lines in s for ±line accounting: empty
// text is 0; otherwise newline count, bumped by one when there is no trailing
// newline (so "a\nb" and "a\nb\n" are both 2).

func countTextLines(s string) int {
	if s == "" {
		return 0
	}
	n := strings.Count(s, "\n")
	if !strings.HasSuffix(s, "\n") {
		n++
	}
	return n
}

// claudeEditLineDelta estimates added/removed lines for a file-mutating Claude
// tool_use. Write contributes its content as additions; Edit/MultiEdit compare
// new_string vs old_string. Exact removed counts would need the baseline file;
// this is a faithful approximation for the activity feed, and the raw edit is
// not what we forward (only the counts + path).

func claudeEditLineDelta(toolName string, input map[string]interface{}) (added, removed int) {
	switch toolName {
	case "Write":
		return countTextLines(remoteString(input, "content")), 0
	case "Edit":
		return countTextLines(remoteString(input, "new_string")), countTextLines(remoteString(input, "old_string"))
	case "MultiEdit":
		if edits, ok := input["edits"].([]interface{}); ok {
			for _, raw := range edits {
				edit, ok := raw.(map[string]interface{})
				if !ok {
					continue
				}
				added += countTextLines(remoteString(edit, "new_string"))
				removed += countTextLines(remoteString(edit, "old_string"))
			}
		}
	}
	return added, removed
}

// claudeToolUseEvents maps a finalized Claude assistant message's tool_use
// blocks to structured activity events. Bash → ai.command(started) and its
// tool_use_id→command mapping is recorded in pending so a later tool_result can
// close it as completed. Write/Edit/MultiEdit → ai.file_change; TodoWrite →
// ai.task. Read-only and other tools are ignored (write/exec surface only).

func claudeToolUseEvents(content []interface{}, run agentAIRun, pending map[string]string) []map[string]interface{} {
	items := content
	msgID := streamingMessageID(run)
	var out []map[string]interface{}
	for _, raw := range items {
		row, ok := raw.(map[string]interface{})
		if !ok || remoteString(row, "type") != "tool_use" {
			continue
		}
		name := remoteString(row, "name")
		id := remoteString(row, "id")
		input, _ := row["input"].(map[string]interface{})
		switch name {
		case "Bash":
			command := remoteString(input, "command")
			if id != "" && command != "" && pending != nil {
				pending[id] = command
			}
			payload := map[string]interface{}{
				"type":       models.AgentEventAICommand,
				"session_id": run.sessionID,
				"message_id": msgID,
				"item_id":    id,
				"status":     "started",
				"command":    command,
			}
			if cwd := remoteString(input, "cwd"); cwd != "" {
				payload["cwd"] = cwd
			}
			out = append(out, payload)
		case "Write", "Edit", "MultiEdit":
			added, removed := claudeEditLineDelta(name, input)
			kind := "edit"
			if name == "Write" {
				kind = "create"
			}
			out = append(out, map[string]interface{}{
				"type":       models.AgentEventAIFileChange,
				"session_id": run.sessionID,
				"message_id": msgID,
				"item_id":    id,
				"path":       remoteString(input, "file_path"),
				"kind":       kind,
				"added":      added,
				"removed":    removed,
			})
		case "TodoWrite":
			out = append(out, map[string]interface{}{
				"type":       models.AgentEventAITask,
				"session_id": run.sessionID,
				"message_id": msgID,
				"item_id":    id,
				"tasks":      claudeTodosToTasks(input["todos"]),
			})
		case "Agent", "Task":
			// 老 CC（无 task_started 帧）兜底：仅登记不发射——实测新 CC 的
			// task_started 紧随 assistant 帧到达，此刻发射必然与真 started
			// 撞出重复行。登记后看门狗照常知情；终态由 claudeToolResultEvents
			// 在无父标记 tool_result 到达时补发，run 拆除时 finalize 兜底。
			if run.subagents != nil && id != "" {
				run.subagents.startLegacy(id, msgID, remoteString(input, "description"), remoteString(input, "subagent_type"), time.Now())
			}
		}
	}
	return out
}

// claudeTodosToTasks maps a TodoWrite todos array to the ai.task tasks payload:
// content→subject, status passthrough, activeForm→active_form.

func claudeTodosToTasks(raw interface{}) []map[string]interface{} {
	items, ok := raw.([]interface{})
	if !ok {
		return nil
	}
	out := make([]map[string]interface{}, 0, len(items))
	for _, item := range items {
		todo, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		entry := map[string]interface{}{
			"subject": remoteString(todo, "content"),
			"status":  remoteString(todo, "status"),
		}
		if af := remoteString(todo, "activeForm"); af != "" {
			entry["active_form"] = af
		}
		out = append(out, entry)
	}
	return out
}

// claudeToolResultEvents closes Bash commands whose tool_result arrives in a
// later "user" turn. A matching tool_use_id emits ai.command(completed) with the
// result text (truncated) and exit_code 1 for is_error, else 0; the pending
// entry is consumed. Unknown ids are ignored.

func claudeToolResultEvents(content []interface{}, run agentAIRun, pending map[string]string) []map[string]interface{} {
	items := content
	// pendingCommands 为空也不能早退：老 CC 兜底登记的子 agent 终态走这里
	// （无父标记 tool_result），与 Bash pending 无关。
	if len(pending) == 0 && run.subagents == nil {
		return nil
	}
	msgID := streamingMessageID(run)
	var out []map[string]interface{}
	for _, raw := range items {
		row, ok := raw.(map[string]interface{})
		if !ok || remoteString(row, "type") != "tool_result" {
			continue
		}
		id := remoteString(row, "tool_use_id")
		// 老 CC 兜底登记的子 agent：无父标记的 tool_result（"[Subagent
		// hand-back]" 或 async_launched）就是它的终态信号——此刻补发
		// completed/failed，摘要用返回文本（截断）。
		if run.subagents != nil && run.subagents.legacyStarted(id) {
			phase := "completed"
			if isErr, ok := row["is_error"].(bool); ok && isErr {
				phase = "failed"
			}
			if record, ok := run.subagents.finish(id, phase, claudeToolResultText(row["content"]), time.Now()); ok {
				run.activity.endSubagent()
				out = append(out, agentAISubagentPayload(run.sessionID, record))
			}
			continue
		}
		command, matched := pending[id]
		if !matched {
			continue
		}
		exitCode := 0
		if isErr, ok := row["is_error"].(bool); ok && isErr {
			exitCode = 1
		}
		output := truncateForCloud(claudeToolResultText(row["content"]))
		payload := map[string]interface{}{
			"type":       models.AgentEventAICommand,
			"session_id": run.sessionID,
			"message_id": msgID,
			"item_id":    id,
			"status":     "completed",
			"command":    command,
		}
		if output != "" {
			payload["output"] = output
		}
		payload["exit_code"] = exitCode
		out = append(out, payload)
		delete(pending, id)
	}
	return out
}

// claudeToolResultText flattens a tool_result content value — a plain string or
// an array of {type:text,text:...} blocks — into a single display string.

func claudeToolResultText(raw interface{}) string {
	if s, ok := raw.(string); ok {
		return s
	}
	items, ok := raw.([]interface{})
	if !ok {
		return ""
	}
	var b strings.Builder
	for _, item := range items {
		row, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		if remoteString(row, "type") == "text" {
			b.WriteString(remoteString(row, "text"))
		}
	}
	return b.String()
}

// claudeUsageEvent builds an ai.usage payload from a finalized assistant
// message's usage block, or returns nil when no token counts are present. The
// caller stamps session_id/message_id (run context is not available here).

func claudeUsageEvent(message map[string]interface{}) map[string]interface{} {
	usage := mapIf(message["usage"])
	if usage == nil {
		return nil
	}
	payload := map[string]interface{}{"type": models.AgentEventAIUsage}
	hit := false
	for field, key := range map[string]string{
		"input_tokens":      "input_tokens",
		"output_tokens":     "output_tokens",
		"cache_read_tokens": "cache_read_input_tokens",
	} {
		if v := remoteIntValue(usage[key]); v >= 0 {
			payload[field] = v
			hit = true
		}
	}
	if !hit {
		return nil
	}
	return payload
}

// emitClaudeStructuredEvents turns Claude stream-json events into structured
// activity events: thinking_delta→ai.thinking (streamed), the finalized
// assistant message's tool_use blocks→ai.command/ai.file_change/ai.task plus
// ai.usage, and a later user-turn tool_result closes a pending Bash command as
// ai.command(completed). Only write/exec tools surface; reads are ignored.
// streamFloatIndex parses a claude stream-json content_block "index" (a JSON
// number, decoded by encoding/json as float64) into an int. Returns
// (0,false) if absent or not a number.

func streamFloatIndex(raw interface{}) (int, bool) {
	f, ok := raw.(float64)
	if !ok {
		return 0, false
	}
	return int(f), true
}

func emitClaudeStructuredEvents(format agentAIOutputFormat, event map[string]interface{}, run agentAIRun, writeJSON agentTerminalWriter, pending map[string]string, blockTypes map[int]string) {
	if format != agentAIOutputClaudeStreamJSON {
		return
	}
	// 子 agent 内部帧（parented）的全部结构化内容——thinking delta、tool_use、
	// usage、tool_result——都不得以父会话名义上 wire。子 agent 生命周期走
	// handleClaudeTaskEvents 的 ai.subagent 专用通道。父会话帧
	// parent_tool_use_id 为 null，不受影响。
	if remoteString(event, "parent_tool_use_id") != "" {
		return
	}
	msgID := streamingMessageID(run)

	if remoteString(event, "type") == "stream_event" {
		streamEvent := mapIf(event["event"])
		if streamEvent != nil {
			switch remoteString(streamEvent, "type") {
			case "content_block_start":
				// Record this block's type so its content_block_stop can tell
				// whether a thinking span ended.
				if blockTypes != nil {
					if idx, ok := streamFloatIndex(streamEvent["index"]); ok {
						if cb := mapIf(streamEvent["content_block"]); cb != nil {
							blockTypes[idx] = remoteString(cb, "type")
						}
					}
				}
			case "content_block_stop":
				// A thinking span ending emits an explicit active:false so the
				// activity headline can leave "思考中" the moment the model
				// moves on — not only at run-terminal ai.done. The next
				// thinking_delta re-activates it (server-side merge). item_id is
				// the block index → this thinking block's event_id
				// (type+session+message+item) is distinct per block, so multiple
				// thinking blocks (across turns or within one) don't collapse.
				if blockTypes != nil {
					if idx, ok := streamFloatIndex(streamEvent["index"]); ok {
						if blockTypes[idx] == "thinking" {
							_ = writeJSON(map[string]interface{}{
								"type":       models.AgentEventAIThinking,
								"session_id": run.sessionID,
								"message_id": msgID,
								"item_id":    fmt.Sprintf("tb%d", idx),
								"active":     false,
							})
						}
						delete(blockTypes, idx)
					}
				}
			case "content_block_delta":
				if delta := mapIf(streamEvent["delta"]); delta != nil && remoteString(delta, "type") == "thinking_delta" {
					if text := remoteString(delta, "thinking"); strings.TrimSpace(text) != "" {
						// 思考流也算实打实的进展（与 TUI 路径同一口径）。不算
						// 的话，一段超过 idle 窗口（10 分钟）的纯推理在宿主看来
						// 就是「没有任何动静」，会被当卡死杀掉——模型明明在
						// 正常思考，用户却收到中断。
						run.activity.markProgress()
						idx, _ := streamFloatIndex(streamEvent["index"])
						_ = writeJSON(map[string]interface{}{
							"type":       models.AgentEventAIThinking,
							"session_id": run.sessionID,
							"message_id": msgID,
							"item_id":    fmt.Sprintf("tb%d", idx),
							"delta":      text,
						})
					}
				}
			}
		}
		return
	}

	switch remoteString(event, "type") {
	case "system":
		// 上下文压缩完成帧：CLI 的 /compact 与自动压缩都会发这一帧。把结果翻译
		// 成一行可见提示，否则手机端 /compact 会安静地「什么都没发生」。
		if notice := claudeCompactNotice(event); notice != "" {
			_ = writeJSON(map[string]interface{}{
				"type":       models.AgentEventAIDelta,
				"session_id": run.sessionID,
				"message_id": msgID,
				"channel":    "stdout",
				"delta":      notice,
			})
		}
	case "assistant":
		message := mapIf(event["message"])
		if message == nil {
			return
		}
		content, _ := message["content"].([]interface{})
		for _, ev := range claudeToolUseEvents(content, run, pending) {
			_ = writeJSON(ev)
		}
		if usage := claudeUsageEvent(message); usage != nil {
			usage["session_id"] = run.sessionID
			usage["message_id"] = msgID
			_ = writeJSON(usage)
		}
	case "user":
		message := mapIf(event["message"])
		if message == nil {
			return
		}
		content, _ := message["content"].([]interface{})
		for _, ev := range claudeToolResultEvents(content, run, pending) {
			_ = writeJSON(ev)
		}
	}
}

// claudeCompactNotice 把 CLI 的 system/compact_boundary 帧翻译成一行可见提示。
//
// 帧形状（2.1.x 实测 + CLI 源码常量）：type=system、subtype=compact_boundary、
// compact_metadata={trigger:"manual"|"auto", pre_tokens, post_tokens}。注意
// pre/post_tokens 在不同版本里既可能是 JSON 数字也可能是字符串，两种都认。
// 非压缩帧返回空串。

func claudeCompactNotice(event map[string]interface{}) string {
	if remoteString(event, "type") != "system" || remoteString(event, "subtype") != "compact_boundary" {
		return ""
	}
	meta := mapIf(event["compact_metadata"])
	if meta == nil {
		meta = mapIf(event["compactMetadata"])
	}
	trigger := remoteString(meta, "trigger")
	pre := claudeCompactTokenValue(meta["pre_tokens"])
	if pre <= 0 {
		pre = claudeCompactTokenValue(meta["preTokens"])
	}
	post := claudeCompactTokenValue(meta["post_tokens"])
	if post <= 0 {
		post = claudeCompactTokenValue(meta["postTokens"])
	}
	switch {
	case pre > 0 && post > 0:
		return fmt.Sprintf("✓ 上下文已压缩：约 %s → %s tokens。\n",
			agentAITokenCountLabel(pre), agentAITokenCountLabel(post))
	case pre > 0:
		return fmt.Sprintf("✓ 上下文已压缩（压缩前约 %s tokens）。\n", agentAITokenCountLabel(pre))
	case trigger == "auto":
		return "✓ 上下文已自动压缩。\n"
	default:
		return "✓ 上下文已压缩。\n"
	}
}

// claudeCompactTokenValue 解析 compact_metadata 里的 token 数（数字或字符串）。

func claudeCompactTokenValue(raw interface{}) int {
	switch v := raw.(type) {
	case float64:
		return int(v)
	case int:
		return v
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return 0
		}
		return n
	default:
		return 0
	}
}

// emitOpenCodeStructuredEvents maps OpenCode's event-bus JSON into the same
// activity protocol used by Codex and Claude. OpenCode versions have moved
// fields between properties/data and the event root, so the mapper accepts all
// three layouts and only emits when a recognizable structured part exists.

func emitOpenCodeStructuredEvents(format agentAIOutputFormat, event map[string]interface{}, run agentAIRun, writeJSON agentTerminalWriter) {
	if format != agentAIOutputOpenCodeJSON {
		return
	}
	props := openCodeEventProperties(event)
	part := firstOpenCodeMap(props["part"], event["part"])
	msgID := agentAssistantMessageID(run.messageID)
	if part != nil {
		partType := strings.ToLower(strings.TrimSpace(remoteString(part, "type")))
		if partType == "reasoning" || partType == "thinking" {
			if delta := firstNonBlankPreserveSpace(remoteString(props, "delta"), remoteString(part, "delta"), remoteString(part, "text")); delta != "" {
				_ = writeJSON(map[string]interface{}{
					"type": models.AgentEventAIThinking, "session_id": run.sessionID,
					"message_id": msgID, "delta": delta,
				})
			}
		}
		if partType == "tool" || strings.Contains(partType, "command") || partType == "bash" {
			emitOpenCodeToolEvent(part, run, writeJSON)
		}
	}

	message := firstOpenCodeMap(props["message"], event["message"], props["info"], event["info"])
	if message == nil {
		return
	}
	usage := firstOpenCodeMap(message["tokens"], message["usage"])
	if usage == nil {
		return
	}
	payload := map[string]interface{}{
		"type": models.AgentEventAIUsage, "session_id": run.sessionID, "message_id": msgID,
	}
	hit := false
	for outputKey, inputKey := range map[string]string{
		"input_tokens": "input", "output_tokens": "output", "reasoning_tokens": "reasoning",
	} {
		if value := remoteIntValue(usage[inputKey]); value >= 0 {
			payload[outputKey] = value
			hit = true
		}
	}
	if cache := mapIf(usage["cache"]); cache != nil {
		if value := remoteIntValue(cache["read"]); value >= 0 {
			payload["cache_read_tokens"] = value
			hit = true
		}
	}
	if hit {
		_ = writeJSON(payload)
	}
}

func emitOpenCodeToolEvent(part map[string]interface{}, run agentAIRun, writeJSON agentTerminalWriter) {
	state := mapIf(part["state"])
	if state == nil {
		state = part
	}
	toolName := strings.ToLower(strings.TrimSpace(firstNonEmpty(remoteString(part, "tool"), remoteString(part, "name"))))
	itemID := firstNonEmpty(remoteString(part, "callID"), remoteString(part, "call_id"), remoteString(part, "id"))
	status := strings.ToLower(strings.TrimSpace(firstNonEmpty(remoteString(state, "status"), "started")))
	switch status {
	case "pending", "running":
		status = "started"
	case "success", "completed":
		status = "completed"
	case "failed", "error":
		status = "failed"
	}
	input := mapIf(state["input"])
	if input == nil {
		input = mapIf(part["input"])
	}
	if strings.Contains(toolName, "write") || strings.Contains(toolName, "edit") || strings.Contains(toolName, "patch") {
		path := firstNonEmpty(remoteString(input, "file_path"), remoteString(input, "path"))
		_ = writeJSON(map[string]interface{}{
			"type": models.AgentEventAIFileChange, "session_id": run.sessionID,
			"message_id": agentAssistantMessageID(run.messageID), "item_id": itemID,
			"path": path, "kind": "edit", "status": status,
		})
		return
	}
	if toolName == "bash" || toolName == "shell" || strings.Contains(toolName, "command") || strings.Contains(toolName, "terminal") {
		payload := map[string]interface{}{
			"type": models.AgentEventAICommand, "session_id": run.sessionID,
			"message_id": agentAssistantMessageID(run.messageID), "item_id": itemID,
			"status": status, "command": firstNonEmpty(remoteString(input, "command"), remoteString(input, "cmd")),
		}
		if output := firstNonEmpty(remoteString(state, "output"), remoteString(state, "error")); output != "" {
			payload["output"] = output
		}
		_ = writeJSON(payload)
	}
}

// countGitChanged returns the number of working-tree entries reported by
// `git -C dir status --porcelain` (modified/added/deleted/untracked). Returns 0
// for a non-git directory or any git error. Only called from the user-initiated
// project probe — never from a background tick (folder access must stay tied
// to a user action so macOS does not raise Files & Folders prompts on its own).

func countGitChanged(dir string) int {
	out, err := newBackgroundCommand("git", "-C", dir, "status", "--porcelain").Output()
	if err != nil {
		return 0
	}
	trimmed := strings.TrimSpace(string(out))
	if trimmed == "" {
		return 0
	}
	return strings.Count(trimmed, "\n") + 1
}

// countGitTrackedFiles returns the number of tracked files reported by
// `git -C dir ls-files` (committed + staged). A fast, stable proxy for "project
// file count" used by the mobile dashboard's Files metric; returns 0 for a
// non-git directory or any git error. Only called from the user-initiated
// project probe — never from a background tick.

func countGitTrackedFiles(dir string) int {
	out, err := newBackgroundCommand("git", "-C", dir, "ls-files").Output()
	if err != nil {
		return 0
	}
	trimmed := strings.TrimSpace(string(out))
	if trimmed == "" {
		return 0
	}
	return strings.Count(trimmed, "\n") + 1
}

func extractCodexJSONTexts(event map[string]interface{}) aiStreamTexts {
	var out aiStreamTexts
	eventType := remoteString(event, "type")
	if strings.Contains(eventType, "delta") {
		if text := firstNonEmpty(remoteString(event, "delta"), remoteString(event, "text")); text != "" {
			out.deltas = []string{text}
			return out
		}
		if item, ok := event["item"].(map[string]interface{}); ok && remoteString(item, "type") == "agent_message" {
			if text := firstNonEmpty(remoteString(item, "delta"), remoteString(item, "text")); text != "" {
				out.deltas = []string{text}
				return out
			}
		}
	}
	if item, ok := event["item"].(map[string]interface{}); ok && remoteString(item, "type") == "agent_message" {
		if text := remoteString(item, "text"); text != "" {
			out.finalText = text
		}
	}
	if eventType == "agent_message" {
		if text := remoteString(event, "text"); text != "" {
			out.finalText = text
		}
	}
	return out
}

func streamAIDelta(reader io.Reader, run agentAIRun, channel string, writeJSON agentTerminalWriter, limiter *agentAIOutputLimiter, capture func(string)) {
	buf := make([]byte, 4096)
	for {
		n, err := reader.Read(buf)
		if n > 0 {
			allowed := n
			if limiter != nil {
				allowed = limiter.Reserve(n)
			}
			if allowed <= 0 {
				if run.cancel != nil {
					run.cancel()
				}
				return
			}
			text := string(buf[:allowed])
			emitAIDelta(text, run, channel, writeJSON, nil, capture)
			if allowed < n {
				if run.cancel != nil {
					run.cancel()
				}
				return
			}
		}
		if err != nil {
			return
		}
	}
}

// captureAgentAIStderr (defined above runCLIPass) is used for AI runs so a
// stale --resume session ("No conversation found with session ID") can be
// detected and the run retried fresh.

func emitAIDelta(text string, run agentAIRun, channel string, writeJSON agentTerminalWriter, limiter *agentAIOutputLimiter, capture func(string)) bool {
	run.activity.bump()
	if text == "" {
		return true
	}
	// Assistant text/thinking is real progress, not just activity.
	run.activity.markProgress()
	if limiter != nil {
		allowed := limiter.Reserve(len(text))
		if allowed <= 0 {
			if run.cancel != nil {
				run.cancel()
			}
			return false
		}
		if allowed < len(text) {
			text = text[:allowed]
			if run.cancel != nil {
				run.cancel()
			}
		}
	}
	if capture != nil {
		capture(text)
	}
	_ = writeJSON(map[string]interface{}{
		"type":       models.AgentEventAIDelta,
		"session_id": run.sessionID,
		"message_id": streamingMessageID(run),
		"channel":    channel,
		"delta":      text,
	})
	if limiter != nil && limiter.Exceeded() {
		return false
	}
	return true
}

// serializeGoalContext converts the structured goal_context envelope (sent by the
// server in ai.run.start) into a readable text block for the AI prompt. Without
// this, Claude only sees the task title and has no idea what to produce or what
// checks will verify — causing repeated blind failures.
