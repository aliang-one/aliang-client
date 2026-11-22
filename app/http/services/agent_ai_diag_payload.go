package services

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"aliang.one/nursorgate/app/http/models"
)

func normalizeAgentAIModel(model string) string {
	model = strings.TrimSpace(model)
	switch strings.ToLower(model) {
	case "", "auto", "codex", "claude", "claude code", "claudecode", "opencode", "open-code", "open_code", "provider default", "default":
		return ""
	default:
		return model
	}
}

func agentAICapabilities() []string {
	claudePath, hasClaude := lookPathCLI("claude")
	claudeCodePath, hasClaudeCode := lookPathCLI("claudecode")
	_, hasOpenCode := lookPathCLI("opencode")
	// Claude steer 依赖 `--input-format stream-json`；老世代 CLI 没有该 flag，
	// 探测失败即不宣称能力，服务端/手机会退回「运行中不可发消息」的旧行为。
	hasClaudeSteer := (hasClaude == nil && claudeStreamJSONInputAvailable(claudePath)) ||
		(hasClaudeCode == nil && claudeStreamJSONInputAvailable(claudeCodePath))
	// 外部 TUI 插话依赖 CC >= 2.1.224 写进 ~/.claude/sessions/<pid>.json 的
	// 跨会话 messaging UDS socket(MessagingSocketPath)。按 CLI 版本探测,与
	// stream-json 探测同模式(缓存随二进制内容失效)。
	hasClaudeExternalSteer := false
	if probePath := externalSteerProbePath(hasClaude == nil, claudePath, hasClaudeCode == nil, claudeCodePath); probePath != "" {
		if v, ok := probeClaudeCodeVersion(probePath); ok {
			hasClaudeExternalSteer = v.atLeast(2, 1, 224)
		}
	}
	return agentAICapabilitiesForTools(hasClaude == nil, hasClaudeCode == nil, codexAppServerAvailable(), hasOpenCode == nil, codexNativeGoalAvailable(), hasClaudeSteer, hasClaudeExternalSteer)
}

// externalSteerProbePath 挑做版本探测的 claude 二进制:优先 claude,退 claudecode。

func externalSteerProbePath(hasClaude bool, claudePath string, hasClaudeCode bool, claudeCodePath string) string {
	if hasClaude {
		return claudePath
	}
	if hasClaudeCode {
		return claudeCodePath
	}
	return ""
}

func agentAICapabilitiesForTools(hasClaude, hasClaudeCode, hasCodexAppServer, hasOpenCode, hasCodexNativeGoal, hasClaudeSteer, hasClaudeExternalSteer bool) []string {
	caps := []string{"ai_chat", "ai_chat_context", "ai_stream", "ai_approval", "ai_steer", "vibe_session"}
	if hasClaudeSteer {
		// 与 ai_steer（Codex app-server 通道）区分：老 agent 也会无条件宣称
		// ai_steer，但只有新 agent 才能真正对 Claude run 注入。
		caps = append(caps, "ai_steer_claude")
	}
	if hasClaudeExternalSteer {
		// 外部 TUI 会话(导入/桌面终端自发生长)的插话通道:经 CC 跨会话
		// messaging UDS 收件箱排队。与 ai_steer_claude(stream-json stdin,
		// 只覆盖 agent 自 spawn 的回合)互补。
		caps = append(caps, "ai_steer_external")
	}
	if hasClaude {
		caps = append(caps, "ai_provider_claude")
	}
	if hasClaudeCode || hasClaude {
		caps = append(caps, "ai_provider_claudecode")
	}
	if hasCodexAppServer {
		caps = append(caps, "ai_provider_codex", "ai_provider_codex_app_server")
	}
	// goal_codex_native_v1 intentionally NOT advertised: the native hybrid
	// driver lacks terminal-state validation (see docs/goal-master.md §五).
	// All goals (including Codex) use the Server-driven Phase 1 path until the
	// native conformance suite passes. hasCodexNativeGoal param retained for
	// the call signature; reuse it when re-enabling native.
	_ = hasCodexNativeGoal
	if hasOpenCode {
		caps = append(caps, "ai_provider_opencode_basic")
	}
	// 内置内核恒可用：它跑在本进程里，不依赖任何外部 CLI，也没有版本探测
	// 可言。所以不设 has* 参数——加一个恒为 true 的形参只会让 7 个调用点
	// 白改一遍。
	caps = append(caps, "ai_provider_agentcore")
	return caps
}

func agentAIStringSliceContains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

// resolveAgentAICWD resolves the vibecoding create/lazy-create project path.
// Operator policy: the discovery snapshot is a display cache, NOT an execution
// ACL — "discovered" and "authorized" are different concerns (2026-10-12 RCA:
// a project whose only transcript fell out of the agentVibeSessionFileScanLimit
// window could never start a session from the phone, and cold projects could
// never re-enter the list without manual CLI activity). Any existing directory
// the user names is creatable; existence + symlink resolution stay enforced
// via cleanExistingAgentDirectory, and an empty path falls back to the user's
// home (bc961e5 semantics). The terminal cwd keeps its own confinement
// (resolveAgentAuthorizedCWD) — that surface is a separate decision, which is
// why this function deliberately does NOT route through
// resolveAgentCWDFallbackHome: that helper degrades to an existence check only
// when zero projects are synchronized and otherwise enforces the authorized
// list — the exact ACL semantics this surface rejects (2026-10-16 merge fix:
// the terminal-cwd branch briefly routed this through the guard, breaking
// TestResolveAgentAICWDAcceptsExistingDirOutsideAuthorizedList).

func resolveAgentAICWD(raw string) (string, error) {
	cwd := strings.TrimSpace(raw)
	if cwd == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		cwd = home
	}
	// Same confinement as the other phone-driven surfaces (see
	// resolveAgentCWDWithConsent), but still without the authorized-list ACL:
	// existence + symlink resolution + TCC consent only.
	return resolveAgentCWDWithConsent(cwd)
}

// claudeRetryInfo captures the most-recent Claude stream-json
// {"type":"system","subtype":"api_retry", attempt, max_retries, retry_delay_ms,
// error_status, error} event for a run. It is threaded out of the stdout
// streaming goroutine so the run loop can (a) surface retries live as
// ai.run.progress and (b) enrich the terminal ai.error with a structured cause
// (error_status/error_type + retry counts) instead of a bare "exit status 1".

type claudeRunDiagnostics struct {
	mu            sync.Mutex
	turns         int
	contextTokens int
	contextWindow int
	stopReason    string
	startedAt     time.Time
	// terminal/reason/elapsedMs 由 result 事件（或收尾 finish）写入：终帧
	// 携带 run 的权威回合数与耗时，面板重开回放显示的就是这份终值。
	terminal  bool
	reason    string
	elapsedMs int64
}

func newClaudeRunDiagnostics(contextWindow int) *claudeRunDiagnostics {
	return &claudeRunDiagnostics{contextWindow: contextWindow, startedAt: time.Now()}
}

// observe 吃一条 claude stream-json 事件，维护回合数/最近用量/停止原因。
// assistant 帧的 usage 三项和直接覆盖——与 agentcore 同语义：缓存生效后
// input 骤降、自动压缩后回落都是正常形态，快照要的是「最近一次实测」。

func (d *claudeRunDiagnostics) observe(event map[string]interface{}) {
	if d == nil {
		return
	}
	switch remoteString(event, "type") {
	case "assistant":
		message := mapIf(event["message"])
		if message == nil {
			return
		}
		d.mu.Lock()
		d.turns++
		if usage := mapIf(message["usage"]); usage != nil {
			// 缺失键按 0 计（remoteIntValue 对缺失返回 -1 是「未知」语义，
			// 混进求和会把三项和拉低 1 的脏值）。
			in := usageFieldInt(usage, "input_tokens")
			read := usageFieldInt(usage, "cache_read_input_tokens")
			write := usageFieldInt(usage, "cache_creation_input_tokens")
			if total := in + read + write; total > 0 {
				d.contextTokens = total
			}
		}
		if sr := remoteString(message, "stop_reason"); sr != "" {
			d.stopReason = sr
		}
		d.mu.Unlock()
	case "result":
		d.mu.Lock()
		d.terminal = true
		if n, ok := remoteIntOK(event, "num_turns"); ok && n > 0 {
			d.turns = n
		}
		if ms, ok := remoteFloatOK(event, "duration_ms"); ok && ms > 0 {
			d.elapsedMs = int64(ms)
		}
		if remoteBoolValue(event, "is_error") {
			d.reason = "claude: " + firstNonEmpty(remoteString(event, "subtype"), "error")
		} else {
			d.reason = ""
		}
		d.mu.Unlock()
	}
}

// finish 在 runCLIPass 收尾调用：CLI 进程异常退出（无 result 事件）时补一个
// 终态标记，reason 取自既有的失败文案；正常路径 observe(result) 已标终态，
// finish 只兜真空。

func (d *claudeRunDiagnostics) finish(reason string) {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.elapsedMs <= 0 && !d.startedAt.IsZero() {
		d.elapsedMs = time.Since(d.startedAt).Milliseconds()
	}
	if !d.terminal {
		d.terminal = true
		d.reason = reason
	}
}

// payload 返回进 ai.run.progress 的扁平 diagnostics 字段，键与
// agentCoreDiagnostics.payload() 一一对应——server 归一化与手机面板共用
// 同一套字段，CLI 通道不发明新形状。尚无任何回合时返回 nil。

func (d *claudeRunDiagnostics) payload() map[string]interface{} {
	if d == nil {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.turns == 0 {
		return nil
	}
	out := map[string]interface{}{
		"turns":          d.turns,
		"context_tokens": d.contextTokens,
		"context_window": d.contextWindow,
		// CLI 通道没有内核级无进展守卫，三连击恒 0；键位保持与 agentcore
		// 对齐，面板按同一形状渲染。
		"dup_streak":   0,
		"err_streak":   0,
		"trunc_streak": 0,
		"elapsed_ms":   d.elapsedMs,
	}
	if d.elapsedMs <= 0 && !d.startedAt.IsZero() {
		out["elapsed_ms"] = time.Since(d.startedAt).Milliseconds()
	}
	if d.stopReason != "" {
		out["stop_reason"] = d.stopReason
	}
	if d.terminal {
		out["terminal"] = true
		if d.reason != "" {
			out["reason"] = d.reason
		}
	}
	return out
}

// claudeFailureCause builds a concise, phone-friendly cause string for a failed
// Claude turn. When retry info is present it describes the upstream error and
// how many retries were spent (e.g. "gateway 502 (server_error); retried
// 10/10"); otherwise it falls back to the CLI exit error. Verbose diagnostics
// (stderr, the raw result text) travel separately in the ai.error "detail"
// field, which the phone does not render by default.
// claudeFailureCause builds a concise, phone-friendly cause string for a failed
// Claude turn. When retry info is present it describes the upstream error and
// how many retries were spent (e.g. "gateway 502 (server_error); retried
// 10/10"); otherwise it falls back to the CLI exit error, enriched with the
// first meaningful stderr line when one exists (flag rejections, auth errors
// etc. would otherwise surface as a bare "exit status 1"). Verbose
// diagnostics (the raw stderr) travel separately in the ai.error "detail"
// field, which the phone does not render by default.

func claudeFailureCause(waitErr error, ri claudeRetryInfo, stderr string) string {
	if ri.has && ri.errorStatus > 0 {
		s := fmt.Sprintf("gateway %d", ri.errorStatus)
		if ri.errorType != "" {
			s = fmt.Sprintf("%s (%s)", s, ri.errorType)
		}
		if ri.max > 0 {
			s = fmt.Sprintf("%s; retried %d/%d", s, ri.attempt, ri.max)
		}
		return s
	}
	if waitErr != nil {
		if essence := conciseCLIStderrCause(stderr); essence != "" {
			return fmt.Sprintf("Claude CLI exited: %v — %s", waitErr, essence)
		}
		return fmt.Sprintf("Claude CLI exited: %v", waitErr)
	}
	return "claude run failed"
}

// conciseCLIStderrCause 提炼 stderr 首个非空行(截 200 字符),给手机端一个
// 可读的失败原因;空白 stderr 返回空串,由调用方退回裸格式。

func conciseCLIStderrCause(stderr string) string {
	for _, line := range strings.Split(stderr, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		runes := []rune(line)
		if len(runes) > 200 {
			runes = runes[:200]
		}
		return string(runes)
	}
	return ""
}

// agentAIErrorPayloadWithRetry is agentAIErrorPayload + structured retry/error
// fields so the phone can render "会话失败 · 网关 502 (重试 10/10)" instead of a
// bare exit message. Fields attach only when retry info is available, so
// non-retry failures (and codex, which emits no api_retry) are unaffected.

func agentAIErrorPayloadWithRetry(sessionID string, messageID string, err error, ri claudeRetryInfo) map[string]interface{} {
	payload := agentAIErrorPayload(sessionID, messageID, err)
	if !ri.has {
		return payload
	}
	if ri.errorStatus > 0 {
		payload["error_status"] = ri.errorStatus
	}
	if ri.errorType != "" {
		payload["error_type"] = ri.errorType
	}
	if ri.attempt > 0 {
		payload["retry_attempt"] = ri.attempt
	}
	if ri.max > 0 {
		payload["retry_max"] = ri.max
	}
	return payload
}

func agentAIErrorPayload(sessionID string, messageID string, err error) map[string]interface{} {
	message := "ai error"
	if err != nil {
		message = err.Error()
	}
	payload := map[string]interface{}{
		"type":       models.AgentEventAIError,
		"session_id": sessionID,
		"error":      message,
	}
	if messageID != "" {
		payload["message_id"] = agentAssistantMessageID(messageID)
	}
	// Typed refusals carry a machine-readable code (e.g. tui_busy for a spawn
	// refused by the external-TUI precheck) so the server can surface a
	// precise reason instead of string-matching the message.
	var coded interface{ ErrorCode() string }
	if err != nil && errors.As(err, &coded) {
		if code := coded.ErrorCode(); code != "" {
			payload["error_code"] = code
		}
	}
	return payload
}

func agentAIApprovalRequestPayload(req agentAIApprovalRequest) map[string]interface{} {
	payload := map[string]interface{}{
		"type":        models.AgentEventAIApprovalRequest,
		"session_id":  req.SessionID,
		"message_id":  req.MessageID,
		"approval_id": req.ID,
		"provider":    req.Provider,
		"kind":        req.Kind,
		"status":      "pending",
	}
	if req.Title != "" {
		payload["title"] = req.Title
	}
	if req.Reason != "" {
		payload["reason"] = req.Reason
	}
	if req.Command != "" {
		payload["command"] = req.Command
	}
	if req.CWD != "" {
		payload["cwd"] = req.CWD
	}
	if req.ToolName != "" {
		payload["tool_name"] = req.ToolName
	}
	if len(req.ToolInput) > 0 {
		payload["tool_input"] = json.RawMessage(req.ToolInput)
	}
	if len(req.FileChanges) > 0 {
		payload["file_changes"] = json.RawMessage(req.FileChanges)
	}
	if len(req.AvailableDecisions) > 0 {
		payload["available_decisions"] = req.AvailableDecisions
	}
	if len(req.Raw) > 0 {
		payload["raw"] = json.RawMessage(req.Raw)
	}
	if req.MatchedRuleID != "" {
		payload["matched_rule_id"] = req.MatchedRuleID
	}
	if req.PolicyVersion > 0 {
		payload["policy_version"] = req.PolicyVersion
	}
	return payload
}

func normalizeAgentAIApprovalDecision(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "accept", "accepted", "approve", "approved", "allow", "allowed", "yes":
		return models.AgentAIApprovalDecisionAccept
	case "accept_for_session", "acceptforsession", "approved_for_session", "approve_for_session", "allow_for_session":
		return models.AgentAIApprovalDecisionAcceptForSession
	case "decline", "declined", "deny", "denied", "reject", "rejected", "no":
		return models.AgentAIApprovalDecisionDecline
	case "cancel", "abort", "timed_out", "timeout":
		return models.AgentAIApprovalDecisionCancel
	default:
		return ""
	}
}

func normalizeAgentAIApprovalScope(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "session":
		return "session"
	default:
		return "turn"
	}
}

func newAgentAIApprovalID(sessionID string, runSeq int) string {
	token, err := newAgentAIApprovalToken()
	if err != nil {
		return fmt.Sprintf("%s-%d-%d", strings.TrimSpace(sessionID), runSeq, time.Now().UnixNano())
	}
	return token
}

func newAgentAIApprovalToken() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate approval token: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// newAgentAIMessageID 就地铸造一个用户发言的 message_id（2026-11-10 聊天记录
// 身份化）。只在客户端没带 message_id 时走这条兜底路径，正常客户端一律自带。
//
// 为什么必须有这个兜底而不是硬拒：旧客户端（以及部分只带 session_id 的老
// 调用方）一旦被硬拒就彻底发不出消息。而旧实现回落到 sessionID 更糟——journal
// 的去重键是 sessionID+role+messageID，同会话里所有缺 id 的发言共用同一个键，
// 于是第二条起全部命中去重被静默丢弃（用户侧表现就是「手机上问的，网页上看
// 不见」）。铸造唯一 id 同时满足两端：旧客户端继续可用，每条发言在 journal 里
// 都是独立记录。
//
// 取舍说清楚：铸造后同一帧若被重放（重连重发这类），会产生两条独立记录而
// 不再互相吞掉——丢失变成重复。这是有意的方向：重复是可见、可删的瑕疵，
// 静默丢失是数据事故。

func newAgentAIMessageID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate message id: %w", err)
	}
	return "msg_" + hex.EncodeToString(b[:]), nil
}

// resolveAgentAIUserMessageID 把「客户端可能没带的 message_id」解析成一个一定
// 非空的取值：带了就原样用（trim 后），没带就现场铸造。ai.message 与 ai.steer
// 共用这一条纪律，避免两处各自兜底再漂移。

func resolveAgentAIUserMessageID(messageID string) (string, bool, error) {
	if id := strings.TrimSpace(messageID); id != "" {
		return id, false, nil
	}
	id, err := newAgentAIMessageID()
	if err != nil {
		return "", false, err
	}
	return id, true, nil
}

func marshalAgentAIRaw(raw interface{}) json.RawMessage {
	if raw == nil {
		return nil
	}
	if msg, ok := raw.(json.RawMessage); ok {
		return msg
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	return encoded
}

func agentAssistantMessageID(messageID string) string {
	messageID = strings.TrimSpace(messageID)
	if strings.HasPrefix(messageID, "assistant_") {
		return messageID
	}
	return "assistant_" + messageID
}

// streamingMessageID returns the message_id that streaming events for the
// CURRENT provider assistant turn should carry: the provider's native msg id
// when known (one per turn → each turn is a distinct transcript message and
// distinct structured events, matching the post-refresh native-log view),
// falling back to the legacy run-derived id when the native id hasn't been
// captured yet (or for providers/paths that don't emit it).

func streamingMessageID(run agentAIRun) string {
	if id := strings.TrimSpace(run.providerMsgID); id != "" {
		return id
	}
	return agentAssistantMessageID(run.messageID)
}

// claudeStreamMessageID returns the provider native assistant message id from a
// claude stream-json event — the finalized "assistant" event's message.id, or
// the message_start stream_event's message.id (available before any
// content_block_delta, so streaming events of the turn can carry it). Empty
// when the event carries none.

func claudeStreamMessageID(event map[string]interface{}) string {
	switch remoteString(event, "type") {
	case "stream_event":
		if se := mapIf(event["event"]); se != nil && remoteString(se, "type") == "message_start" {
			if m := mapIf(se["message"]); m != nil {
				return strings.TrimSpace(remoteString(m, "id"))
			}
		}
	case "assistant":
		if m := mapIf(event["message"]); m != nil {
			return strings.TrimSpace(remoteString(m, "id"))
		}
	}
	return ""
}
