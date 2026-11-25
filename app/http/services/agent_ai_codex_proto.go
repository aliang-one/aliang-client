package services

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/common/logger"
)

func agentAIUseCodexAppServer(provider string) bool {
	provider = strings.ToLower(strings.TrimSpace(provider))
	return (provider == "codex" || provider == "auto") && codexAppServerAvailable()
}

// agentAIUseAgentCore 判断这一轮是否走内置内核。
//
// 只认显式的 "agentcore"，**不匹配 "auto"** —— 与 agentAIUseCodexAppServer 的
// 判断刻意不同：codex 是「装了就用」，内置内核一旦纳入 auto 就会顶掉用户装了
// 的 claude/codex，那是行为倒退。用户明确选了自有入口才走这里。

func agentAIUseAgentCore(provider string) bool {
	return strings.ToLower(strings.TrimSpace(provider)) == "agentcore"
}

// agentAIUseAgentCoreFallback 是 auto 的兜底：一个外部 CLI 都没有时走内置内核。
//
// 为什么需要它：产品的核心承诺是「装好 alianggate 就能用 AI」。auto 的候选集
// 只有 codex/claude/claudecode，三者都不在 PATH 时 resolveAgentAITool 直接报
// "no approval-capable AI CLI found in PATH"，用户什么都没得用——这条兜底正是
// 那个承诺的落点。
//
// 为什么不放进 resolveAgentAITool 的候选集：那里返回的是 *agentAITool（外部
// 进程句柄：path/args/outputFormat），内核根本不消费它。让 runCLI 在解析之前
// 分流，比伪造一个假 tool 干净。
//
// 三条刻意排除：
//   - 只认 "auto"：显式选了 claude/codex 就照旧报错，不静默换 provider。
//   - goal 相关 run（readOnly / goalIdentity）不走：内核首期不注入 goal 的
//     ALIANG_GOAL_REPORT 强制段（withGoalTaskReportSystemPrompt 只能改
//     agentAITool），跑起来会让 goal 永远拿不到报告行。这类 run 继续用 CLI。
//   - codex app-server 可用时不会走到这里（runCLI 里那个分支在前）。

func agentAIUseAgentCoreFallback(run agentAIRun) bool {
	if strings.ToLower(strings.TrimSpace(run.provider)) != "auto" {
		return false
	}
	if run.readOnly || len(run.goalIdentity) > 0 {
		return false
	}
	return !agentAIExternalCLIAvailable()
}

// agentAIUseAgentCoreForRun 是 runCLI 分流用的总闸:显式 agentcore 或 auto
// 兜底任一命中即走内核,但 **goal 回合一律除外**。
//
// 为什么要单独这道闸:goal 的排除逻辑此前只写在 auto 兜底分支里,
// 「显式 provider=agentcore」的分流没有这层判据——服务端一旦显式下发
// agentcore(目前被服务端 GoalProvider 白名单挡着,白名单一旦放宽就会漏),
// goal 回合就会进内核,而内核不注入 ALIANG_GOAL_REPORT 强制段,goal 会永远
// 拿不到报告行,被 attachGoalReport 记成 goal_report_missing。防御纵深要求
// 判据长在离分流最近的地方,而不是只指望上游白名单。

func agentAIUseAgentCoreForRun(run agentAIRun) bool {
	if len(run.goalIdentity) > 0 {
		return false
	}
	return agentAIUseAgentCore(run.provider) || agentAIUseAgentCoreFallback(run)
}

// agentAIExternalCLIProbe 报告 auto 的候选集里是否有可用的外部 CLI。
//
// var 是单测注入点：hermetic，不依赖测试机上真的装没装 claude/codex（与
// platformProviderEnvProbe 同一手法）。
//
// 口径必须与 resolveAgentAITool 的 auto 分支一致：codex 需 app-server 才算
// 可用；claudecode 在 resolveNamedAgentAITool 里会回退去找 claude，所以任一
// 命中即算。

var agentAIExternalCLIProbe = func() bool {
	if codexAppServerAvailable() {
		return true
	}
	if _, err := lookPathCLI("claude"); err == nil {
		return true
	}
	if _, err := lookPathCLI("claudecode"); err == nil {
		return true
	}
	return false
}

// agentAIExternalCLIAvailable 是 probe 的读取入口，保留函数形状便于将来
// 加缓存而不改调用方。

func agentAIExternalCLIAvailable() bool {
	return agentAIExternalCLIProbe()
}

var codexAppServerProbeCache sync.Map

func executableProbeCacheKey(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	key := path
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		key += ":" + resolved
	}
	if stat, err := os.Stat(path); err == nil {
		key += fmt.Sprintf(":%d:%d", stat.ModTime().UnixNano(), stat.Size())
	}
	return key
}

func codexAppServerAvailable() bool {
	path, err := lookPathCLI("codex")
	if err != nil {
		return false
	}
	cacheKey := executableProbeCacheKey(path)
	if cached, ok := codexAppServerProbeCache.Load(cacheKey); ok {
		return cached.(bool)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	output, probeErr := newBackgroundCommandContext(ctx, path, "app-server", "--help").CombinedOutput()
	available := probeErr == nil && strings.Contains(strings.ToLower(string(output)), "app-server")
	codexAppServerProbeCache.Store(cacheKey, available)
	return available
}

func codexNativeGoalAvailable() bool {
	path, err := lookPathCLI("codex")
	if err != nil || !codexAppServerAvailable() {
		return false
	}
	cacheKey := "goal:" + executableProbeCacheKey(path)
	if cached, ok := codexAppServerProbeCache.Load(cacheKey); ok {
		return cached.(bool)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	output, probeErr := newBackgroundCommandContext(ctx, path, "features", "list").CombinedOutput()
	available := probeErr == nil && codexFeatureEnabled(string(output), "goals")
	codexAppServerProbeCache.Store(cacheKey, available)
	return available
}

func codexFeatureEnabled(output, feature string) bool {
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 3 && fields[0] == feature && fields[len(fields)-1] == "true" {
			return true
		}
	}
	return false
}

// codexTurnResult inspects a codex `turn/completed` notification params payload
// and reports the terminal status the agent should surface, plus any error
// message carried by a failed turn. Status is "completed", "interrupted",
// "failed", or "" when the payload is missing/unknown. Per the app-server
// protocol, turn/completed carries { turn: { status, error?: { message,
// codexErrorInfo?, additionalDetails? } } }; a failed or interrupted turn must
// NOT be treated as a successful completion, so the caller gates ai.done on it.

func codexTurnResult(params map[string]interface{}) (status string, errMsg string) {
	if params == nil {
		return "", ""
	}
	turn, ok := params["turn"].(map[string]interface{})
	if !ok {
		return "", ""
	}
	status = strings.ToLower(strings.TrimSpace(remoteString(turn, "status")))
	if errObj, ok := turn["error"].(map[string]interface{}); ok {
		errMsg = strings.TrimSpace(firstNonEmpty(remoteString(errObj, "message"), remoteString(errObj, "additionalDetails")))
	}
	return status, errMsg
}

// extractCodexFileChangeItem reads an item/started or item/completed
// notification and, when the carried item is a fileChange, returns its itemId
// and the marshalled `changes` array. Returns ("", nil) for non-fileChange
// items. The fileChange approval request itself carries no diff (per the
// app-server protocol); the proposed edits arrive in this prior item event, so
// the caller keys them by itemId and attaches them to the approval request.

func extractCodexFileChangeItem(msg map[string]interface{}) (string, json.RawMessage) {
	params, _ := msg["params"].(map[string]interface{})
	item, _ := params["item"].(map[string]interface{})
	if remoteString(item, "type") != "fileChange" {
		return "", nil
	}
	itemID := remoteString(item, "id")
	if itemID == "" {
		return "", nil
	}
	if changes, ok := item["changes"]; ok && changes != nil {
		return itemID, marshalAgentAIRaw(changes)
	}
	return itemID, nil
}

// summarizeFileDiff counts added/removed lines in a unified diff for the
// activity feed ("Update(file) +N -M"). File headers (+++/---), hunk headers
// (@@) and the "\ No newline at end of file" marker are not counted.

func summarizeFileDiff(diff string) (added, removed int) {
	if diff == "" {
		return 0, 0
	}
	for _, line := range strings.Split(diff, "\n") {
		if line == "" {
			continue
		}
		switch line[0] {
		case '@', '\\', ' ':
			continue
		case '+':
			if !strings.HasPrefix(line, "+++") {
				added++
			}
		case '-':
			if !strings.HasPrefix(line, "---") {
				removed++
			}
		}
	}
	return added, removed
}

func codexAppServerResponseID(msg map[string]interface{}) string {
	if _, ok := msg["id"]; !ok {
		return ""
	}
	return fmt.Sprint(msg["id"])
}

func codexAppServerResponseError(msg map[string]interface{}) (string, bool) {
	errObj, ok := msg["error"].(map[string]interface{})
	if !ok || errObj == nil {
		return "", false
	}
	message := firstNonEmpty(remoteString(errObj, "message"), remoteString(errObj, "error"))
	if info, ok := errObj["codexErrorInfo"].(map[string]interface{}); ok {
		message = firstNonEmpty(message, remoteString(info, "message"), remoteString(info, "code"))
	}
	code := remoteString(errObj, "code")
	if message == "" {
		message = "unknown error"
	}
	if code != "" {
		return fmt.Sprintf("%s (%s)", message, code), true
	}
	return message, true
}

// extractCodexCommandItem pulls the lifecycle fields of a codex
// commandExecution item (item/started carries command+cwd; item/completed
// carries exitCode+stdout+stderr). ok is true only for commandExecution items;
// exitCode is non-nil only when the completed item carried one.

func extractCodexCommandItem(msg map[string]interface{}) (itemID, command, cwd string, exitCode *int, stdout, stderr string, ok bool) {
	params, _ := msg["params"].(map[string]interface{})
	item, _ := params["item"].(map[string]interface{})
	if remoteString(item, "type") != "commandExecution" {
		return "", "", "", nil, "", "", false
	}
	itemID = remoteString(item, "id")
	command = codexJoinCommand(item["command"])
	cwd = remoteString(item, "cwd")
	exitCode = remoteIntPointer(item, "exitCode", "exit_code")
	stdout = remoteString(item, "stdout")
	stderr = remoteString(item, "stderr")
	return itemID, command, cwd, exitCode, stdout, stderr, true
}

// codexJoinCommand renders a codex command value — either a shell string or an
// argv array — as a single display string.

func codexJoinCommand(raw interface{}) string {
	switch v := raw.(type) {
	case string:
		return strings.TrimSpace(v)
	case []interface{}:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if part := strings.TrimSpace(fmt.Sprint(item)); part != "" {
				out = append(out, part)
			}
		}
		return strings.Join(out, " ")
	}
	return ""
}

// remoteIntPointer returns the first present integer-valued field among keys,
// or nil when none of them hold a number. JSON numbers arrive as float64.

func remoteIntPointer(fields map[string]interface{}, keys ...string) *int {
	for _, key := range keys {
		raw, present := fields[key]
		if !present || raw == nil {
			continue
		}
		if v, ok := toIntValue(raw); ok {
			return &v
		}
	}
	return nil
}

func toIntValue(raw interface{}) (int, bool) {
	switch v := raw.(type) {
	case float64:
		return int(v), true
	case int:
		return v, true
	case int64:
		return int(v), true
	}
	return 0, false
}

// remoteIntOK / remoteFloatOK / remoteBoolValue：claudeRunDiagnostics 读
// result 事件数值/布尔字段的取值器（encoding/json 解出的数字是 float64），
// 形态对齐 remoteString(event, key)。

func usageFieldInt(usage map[string]interface{}, key string) int {
	if v, ok := toIntValue(usage[key]); ok {
		return v
	}
	return 0
}

func remoteIntOK(event map[string]interface{}, key string) (int, bool) {
	return toIntValue(event[key])
}

func remoteFloatOK(event map[string]interface{}, key string) (float64, bool) {
	if v, ok := event[key].(float64); ok {
		return v, true
	}
	return 0, false
}

func remoteBoolValue(event map[string]interface{}, key string) bool {
	b, _ := event[key].(bool)
	return b
}

// agentAICloudFieldCap bounds a single structured-event text field (command
// output, diff) so one verbose command/edit cannot blow up a websocket frame.

const agentAICloudFieldCap = 8 * 1024

func truncateForCloud(value string) string {
	if len(value) <= agentAICloudFieldCap {
		return value
	}
	// rune 安全截断（2026-11-17 审计 P2）：字节下标裸切会把多字节 rune 劈成
	// 非法 UTF-8 尾巴。json.Marshal 会把非法字节消毒成 U+FFFD——线协议不破，
	// 但截点处字符花屏；按 rune 边界回退至多 3 字节，截点干净。
	cut := value[:agentAICloudFieldCap]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut + "…<truncated>"
}

// emitCodexFileChangeEvents fans a codex fileChange item's changes out as one
// ai.file_change event per changed path, each carrying ±line counts (from the
// diff) and the raw diff for the cloud to render.

func emitCodexFileChangeEvents(run agentAIRun, writeJSON agentTerminalWriter, itemID string, changes json.RawMessage) {
	var parsed []map[string]interface{}
	if err := json.Unmarshal(changes, &parsed); err != nil || len(parsed) == 0 {
		return
	}
	for _, ch := range parsed {
		diff := firstNonEmpty(remoteString(ch, "diff"), remoteString(ch, "change"))
		added, removed := summarizeFileDiff(diff)
		payload := map[string]interface{}{
			"type":       models.AgentEventAIFileChange,
			"session_id": run.sessionID,
			"message_id": agentAssistantMessageID(run.messageID),
			"item_id":    itemID,
			"path":       remoteString(ch, "path"),
			"kind":       remoteString(ch, "kind"),
			"added":      added,
			"removed":    removed,
		}
		if diff != "" {
			payload["diff"] = truncateForCloud(diff)
		}
		_ = writeJSON(payload)
	}
}

// emitCodexUsageIfPresent is a best-effort hook: codex surfaces per-turn token
// usage on turn/completed, but the exact field shape varies across codex
// versions. It probes the most likely locations and emits a single ai.usage
// event when one is found; otherwise it is a no-op. Verify/adjust the shape
// against a real codex transcript at runtime.

func emitCodexUsageIfPresent(run agentAIRun, writeJSON agentTerminalWriter, params map[string]interface{}) {
	if params == nil {
		return
	}
	sources := []map[string]interface{}{
		mapIf(params["usage"]),
		mapIf(params["turn"]),
		mapIf(params["tokenUsage"]),
	}
	for _, src := range sources {
		if src == nil {
			continue
		}
		usage := src
		if nested := mapIf(src["usage"]); nested != nil {
			usage = nested
		}
		payload := map[string]interface{}{
			"type":       models.AgentEventAIUsage,
			"session_id": run.sessionID,
			"message_id": agentAssistantMessageID(run.messageID),
		}
		hit := false
		for _, key := range []string{"input_tokens", "inputTokens"} {
			if v := remoteIntValue(usage[key]); v >= 0 {
				payload["input_tokens"] = v
				hit = true
				break
			}
		}
		for _, key := range []string{"output_tokens", "outputTokens"} {
			if v := remoteIntValue(usage[key]); v >= 0 {
				payload["output_tokens"] = v
				hit = true
				break
			}
		}
		for _, key := range []string{"cache_read_tokens", "cacheReadInputTokens"} {
			if v := remoteIntValue(usage[key]); v >= 0 {
				payload["cache_read_tokens"] = v
				hit = true
				break
			}
		}
		if hit {
			_ = writeJSON(payload)
			return
		}
	}
}

func emitCodexThreadUsage(run agentAIRun, writeJSON agentTerminalWriter, params map[string]interface{}) {
	tokenUsage := mapIf(params["tokenUsage"])
	if tokenUsage == nil {
		return
	}
	usage := mapIf(tokenUsage["last"])
	if usage == nil {
		usage = mapIf(tokenUsage["total"])
	}
	if usage == nil {
		return
	}
	payload := map[string]interface{}{
		"type":       models.AgentEventAIUsage,
		"session_id": run.sessionID,
		"message_id": agentAssistantMessageID(run.messageID),
	}
	hit := false
	for source, target := range map[string]string{
		"inputTokens":           "input_tokens",
		"outputTokens":          "output_tokens",
		"cachedInputTokens":     "cache_read_tokens",
		"reasoningOutputTokens": "reasoning_output_tokens",
		"totalTokens":           "total_tokens",
	} {
		if value := remoteIntValue(usage[source]); value >= 0 {
			payload[target] = value
			hit = true
		}
	}
	if contextWindow := remoteIntValue(tokenUsage["modelContextWindow"]); contextWindow >= 0 {
		payload["model_context_window"] = contextWindow
		hit = true
	}
	if hit {
		_ = writeJSON(payload)
	}
}

func emitCodexPlan(run agentAIRun, writeJSON agentTerminalWriter, params map[string]interface{}) {
	rawPlan, _ := params["plan"].([]interface{})
	if len(rawPlan) == 0 {
		return
	}
	tasks := make([]map[string]interface{}, 0, len(rawPlan))
	for _, rawStep := range rawPlan {
		step := mapIf(rawStep)
		if step == nil {
			continue
		}
		subject := strings.TrimSpace(remoteString(step, "step"))
		if subject == "" {
			continue
		}
		status := remoteString(step, "status")
		if status == "inProgress" {
			status = "in_progress"
		}
		tasks = append(tasks, map[string]interface{}{"subject": subject, "status": status})
	}
	if len(tasks) == 0 {
		return
	}
	payload := map[string]interface{}{
		"type":       models.AgentEventAITask,
		"session_id": run.sessionID,
		"message_id": agentAssistantMessageID(run.messageID),
		"tasks":      tasks,
	}
	if explanation := strings.TrimSpace(remoteString(params, "explanation")); explanation != "" {
		payload["explanation"] = explanation
	}
	_ = writeJSON(payload)
}

func mapIf(raw interface{}) map[string]interface{} {
	if m, ok := raw.(map[string]interface{}); ok {
		return m
	}
	return nil
}

func remoteIntValue(raw interface{}) int {
	if v, ok := toIntValue(raw); ok {
		return v
	}
	return -1
}

// emitCodexThinkingIfPresent is a best-effort hook for codex reasoning: when an
// item carries reasoning text (type reasoning/agentReasoning/reasoningSummary
// with a text/summary field), it is streamed as ai.thinking. Codex's reasoning
// event shape is version-dependent; verify against a real transcript.

func emitCodexThinkingIfPresent(run agentAIRun, writeJSON agentTerminalWriter, item map[string]interface{}) {
	if item == nil {
		return
	}
	switch remoteString(item, "type") {
	case "reasoning", "agentReasoning", "reasoningSummary":
	default:
		return
	}
	text := firstNonEmpty(remoteString(item, "text"), remoteString(item, "summary"), remoteString(item, "delta"))
	if strings.TrimSpace(text) == "" {
		return
	}
	_ = writeJSON(map[string]interface{}{
		"type":       models.AgentEventAIThinking,
		"session_id": run.sessionID,
		"message_id": agentAssistantMessageID(run.messageID),
		"delta":      text,
	})
}

// codexAgentMessageDedup suppresses exact replays of the previous completed
// agent message within a single codex turn. The upstream model/proxy has been
// observed re-streaming an identical generation (same content, sometimes the
// same message id); without dedup the backend renders the reply twice. The
// first message of a turn streams through verbatim. For each subsequent
// message, deltas that reproduce the previous message's text are held back;
// once the new message completes that exact replay (dropped) or diverges / adds
// a tail, only the genuinely new portion is emitted, so no real content is
// lost. It assumes codex replays under a fresh item id (the protocol's item
// lifecycle is item/started → deltas → item/completed); when item ids are
// absent it degrades to passthrough rather than risk dropping content.

type codexAgentMessageDedup struct {
	lastFull string // raw text of the previous completed message
	curID    string // item id of the message currently streaming
	buf      string // accumulated raw text of the current message
	emitted  string // text emitted for the current message so far
	live     bool   // once true, deltas pass through verbatim
}

func newCodexAgentMessageDedup() *codexAgentMessageDedup {
	return &codexAgentMessageDedup{}
}

func (d *codexAgentMessageDedup) start(itemID string) {
	d.lastFull = d.buf
	d.curID = itemID
	d.buf = ""
	d.emitted = ""
	d.live = false
}

func (d *codexAgentMessageDedup) process(itemID, delta string) string {
	if itemID != d.curID {
		d.start(itemID)
	}
	d.buf += delta
	if d.live || d.lastFull == "" {
		// First message of the turn, or already past the replay window: stream
		// verbatim.
		d.emitted += delta
		return delta
	}
	var want string
	switch {
	case isStringPrefix(d.buf, d.lastFull):
		// Still reproducing the previous message; hold everything back.
		want = ""
	case isStringPrefix(d.lastFull, d.buf):
		// Replay plus a new tail: emit only the tail. Everything after this
		// lands beyond lastFull, so go live.
		d.live = true
		want = d.buf[len(d.lastFull):]
	default:
		// Diverged from the previous message: genuinely new content. Flush the
		// whole message and stream the rest live.
		d.live = true
		want = d.buf
	}
	if want == d.emitted {
		return ""
	}
	out := want[len(d.emitted):]
	d.emitted = want
	return out
}

// isStringPrefix reports whether s is a prefix of full (s == full[:len(s)]),
// including the case s == full.

func isStringPrefix(s, full string) bool {
	return len(s) <= len(full) && full[:len(s)] == s
}

func (m *agentAIManager) runCodexAppServer(ctx context.Context, run agentAIRun, writeJSON agentTerminalWriter) agentAIRunOutcome {
	path, err := lookPathCLI("codex")
	if err != nil {
		_ = writeJSON(agentAIErrorPayload(run.sessionID, run.messageID, fmt.Errorf("AI CLI %q was not found in PATH", "codex")))
		return agentAIRunDone
	}
	cmd := newBackgroundCommandContext(ctx, path, "app-server", "--stdio")
	cmd.Dir = run.projectPath
	cmd.Env = agentChildProcessEnv()
	stdin, err := cmd.StdinPipe()
	if err != nil {
		_ = writeJSON(agentAIErrorPayload(run.sessionID, run.messageID, err))
		return agentAIRunDone
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = writeJSON(agentAIErrorPayload(run.sessionID, run.messageID, err))
		return agentAIRunDone
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = writeJSON(agentAIErrorPayload(run.sessionID, run.messageID, err))
		return agentAIRunDone
	}
	if err := cmd.Start(); err != nil {
		_ = writeJSON(agentAIErrorPayload(run.sessionID, run.messageID, err))
		return agentAIRunDone
	}

	var stderrBuf strings.Builder
	var stderrWG sync.WaitGroup
	stderrWG.Add(1)
	go func() {
		defer stderrWG.Done()
		captureAgentAIStderr(stderr, &stderrBuf)
	}()

	var writeMu sync.Mutex
	send := func(payload map[string]interface{}) error {
		raw, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		writeMu.Lock()
		defer writeMu.Unlock()
		if _, err := stdin.Write(append(raw, '\n')); err != nil {
			return err
		}
		return nil
	}
	steerControl := newAgentAICodexSteerControl(run, send, writeJSON)
	m.setCodexSteerControl(run.sessionID, run.runSeq, steerControl)

	model := normalizeAgentAIModel(run.model)
	effort := strings.TrimSpace(run.effort)
	codexPhase := "initializing"
	terminalErr := ""
	runStartedEmitted := false
	emitRunStarted := func() {
		if runStartedEmitted {
			return
		}
		runStartedEmitted = true
		_ = writeJSON(map[string]interface{}{
			"type":         models.AgentEventAIRunStarted,
			"session_id":   run.sessionID,
			"message_id":   agentAssistantMessageID(run.messageID),
			"provider":     "codex",
			"mode":         run.mode,
			"project_path": run.projectPath,
			"state":        "running",
		})
	}

	_ = send(map[string]interface{}{
		"method": "initialize",
		"id":     0,
		"params": map[string]interface{}{
			"clientInfo": map[string]interface{}{
				"name":    "alianggate",
				"title":   "Aliang Agent",
				"version": "0.1.0",
			},
			"capabilities": nil,
		},
	})
	_ = send(map[string]interface{}{"method": "initialized", "params": map[string]interface{}{}})
	// #5: fork (re-planning exploration) sessions run in a READ-ONLY codex sandbox
	// so exploration can't mutate the workspace. v1 always used workspace-write,
	// bypassing the runCLIPass readOnly enforcement entirely on the app-server path.
	threadParams := map[string]interface{}{
		"cwd":               run.projectPath,
		"approvalPolicy":    "on-request",
		"approvalsReviewer": "user",
		"sandbox":           codexSandboxMode(run.readOnly),
	}
	// thread/fork（2026-11-25 线程 busy 询问）：run.forkFromSessionID 非空 =
	// 用户在 busy 卡上选了「复制会话继续」。fork 把源线程历史复制成全新线程
	// （daemon 实测：params {"threadId": 源id}，返回体 thread.forkedFromId 指
	// 回源），源 rollout/写锁零接触。与 resume 互斥，fork 优先。
	forkedFromID := ""
	threadMethod := "thread/start"
	if fork := strings.TrimSpace(run.forkFromSessionID); fork != "" {
		threadMethod = "thread/fork"
		threadParams["threadId"] = fork
		forkedFromID = fork
	} else if strings.TrimSpace(run.resumeSessionID) != "" {
		threadMethod = "thread/resume"
		threadParams["threadId"] = strings.TrimSpace(run.resumeSessionID)
	}
	if model != "" {
		threadParams["model"] = model
	}
	codexPhase = threadMethod
	_ = send(map[string]interface{}{"method": threadMethod, "id": 1, "params": threadParams})

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	limiter := &agentAIOutputLimiter{meter: newOutputMeter(agentAIOutputRateWindow, agentAIOutputRateBytes, int64(agentAIOutputCapBytes))}
	var outMu sync.Mutex
	var output strings.Builder
	capture := func(text string) {
		outMu.Lock()
		appendAgentAIHistoryCapture(&output, text)
		outMu.Unlock()
	}
	threadID := ""
	turnID := ""
	threadBound := false
	// codex mismatch 自愈的单次封顶闸：reserved 占位释放后 confirmBinding 的
	// expected 为空，结构上不可能二次 mismatch，闸只是纵深防御。
	bindingRetried := false
	bindThread := func() error {
		if threadBound || threadID == "" {
			return nil
		}
		// fork 二趟：本地锚若仍指向 fork 源线程，confirm 会报 mismatch——
		// forkedFromID 传入做 fork 容忍（expected==fork 源才放行换绑），与
		// 服务端 bindings 的 fork 放行语义镜像。
		record, err := m.confirmBindingFork(run.sessionID, "codex", threadID, run.bindingVersion, forkedFromID)
		if err != nil {
			return err
		}
		threadBound = true
		return writeJSON(map[string]interface{}{
			"type":              models.AgentEventAISessionBound,
			"session_id":        run.sessionID,
			"message_id":        run.messageID,
			"run_id":            run.runID,
			"provider":          "codex",
			"native_session_id": record.NativeSessionID,
			"source_session_id": record.NativeSessionID,
			"binding_version":   record.BindingVersion,
			"binding_state":     record.State,
		})
	}
	turnStarted := false
	completed := false
	resumeMissing := false
	threadBusy := false
	turnStatus := ""
	fileChangesByID := map[string]json.RawMessage{}
	commandsByID := map[string]string{}
	dedup := newCodexAgentMessageDedup()
	var nativeGoalSnapshot map[string]interface{}
	nativeGoalTerminalDesired := ""
	nativeGoalSyncErr := ""
	startTurn := func() {
		m.setAgentAIResumeSessionIDIfEmpty(run.sessionID, run.runSeq, threadID)
		turnStarted = true
		emitRunStarted()
		turnParams := map[string]interface{}{
			"threadId":            threadID,
			"clientUserMessageId": run.messageID,
			"input":               codexTurnInput(run),
			"approvalPolicy":      "on-request",
			"approvalsReviewer":   "user",
		}
		if model != "" {
			turnParams["model"] = model
		}
		if effort != "" {
			turnParams["effort"] = effort
		}
		codexPhase = "turn/start"
		_ = send(map[string]interface{}{
			"method": "turn/start",
			"id":     2,
			"params": turnParams,
		})
	}
	// Progress heartbeat. Within a codex turn the app-server emits nothing
	// during quiet gaps (long tool runs, model thinking, upstream retries), and
	// unlike the Claude path there is no per-token stream — so without a
	// heartbeat the phone receives no "still alive" signal and can demote a
	// still-running turn mid-run. Tick ai.run.progress on the same interval as
	// runCLIPass so the run stays running across silent gaps. progressStop is
	// closed when runCodexAppServer returns (the scan loop below has exited).
	progressStop := make(chan struct{})
	defer close(progressStop)
	go func() {
		ticker := time.NewTicker(agentAIRunProgressInterval)
		defer ticker.Stop()
		for {
			select {
			case <-progressStop:
				return
			case <-ticker.C:
				_ = writeJSON(map[string]interface{}{
					"type":              models.AgentEventAIRunProgress,
					"session_id":        run.sessionID,
					"message_id":        agentAssistantMessageID(run.messageID),
					"git_changed_count": countGitChanged(run.projectPath),
				})
			}
		}
	}()
	for scanner.Scan() {
		run.activity.bump()
		var msg map[string]interface{}
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			continue
		}
		if steerControl.handleResponse(msg) {
			continue
		}
		if responseID := codexAppServerResponseID(msg); responseID != "" {
			if detail, ok := codexAppServerResponseError(msg); ok {
				switch responseID {
				case "0":
					terminalErr = "Codex app-server initialize failed: " + detail
					goto done
				case "1":
					if threadMethod == "thread/resume" && isAgentAIResumeMissing(detail) {
						resumeMissing = true
						goto done
					}
					if isAgentAICodexWriterConflict(detail) {
						// 线程单写者冲突（2026-11-25 手机 resume VS Code 活会话事故）：
						// 不在此处发 ai.error——驱动层拿 agentAIRunThreadBusy 先问
						// 用户是否 fork（默认不 fork），询问不可用时才落
						// codex_thread_busy 终态。terminalErr 仅留作降级文案底稿。
						threadBusy = true
						terminalErr = fmt.Sprintf("Codex app-server %s failed: %s", threadMethod, detail)
						goto done
					}
					terminalErr = fmt.Sprintf("Codex app-server %s failed: %s", threadMethod, detail)
					goto done
				case "2":
					terminalErr = "Codex app-server turn/start failed: " + detail
					goto done
				case "3", "4", "5":
					nativeGoalSyncErr = "Codex app-server native Goal synchronization failed: " + detail
					terminalErr = nativeGoalSyncErr
					goto done
				}
			}
		}
		if method := remoteString(msg, "method"); method != "" {
			if _, hasID := msg["id"]; hasID {
				id := msg["id"]
				params, _ := msg["params"].(map[string]interface{})
				var fileChanges json.RawMessage
				if method == "item/fileChange/requestApproval" {
					fileChanges = fileChangesByID[remoteString(params, "itemId")]
				}
				result, errorCode, err := m.handleCodexAppServerRequest(ctx, run, writeJSON, method, params, fileChanges)
				if err != nil {
					if errorCode == 0 {
						errorCode = -32000
					}
					_ = send(map[string]interface{}{
						"id":    id,
						"error": map[string]interface{}{"code": errorCode, "message": err.Error()},
					})
				} else {
					_ = send(map[string]interface{}{"id": id, "result": result})
				}
				continue
			}
			switch method {
			case "turn/started":
				if params, ok := msg["params"].(map[string]interface{}); ok {
					if value := remoteString(params, "threadId"); value != "" {
						threadID = value
					}
					if turn, ok := params["turn"].(map[string]interface{}); ok {
						turnID = remoteString(turn, "id")
					}
					if turnID == "" {
						turnID = remoteString(params, "turnId")
					}
					if turnID != "" {
						codexPhase = "running"
						steerControl.markReady(threadID, turnID)
					}
				}
			case "item/agentMessage/delta":
				if params, ok := msg["params"].(map[string]interface{}); ok {
					out := dedup.process(remoteString(params, "itemId"), remoteString(params, "delta"))
					// Suppressed replays are neither emitted nor captured, so they
					// never reach the backend or the stored assistant history.
					if out != "" && !emitAIDelta(out, run, "assistant", writeJSON, limiter, capture) {
						if run.cancel != nil {
							run.cancel()
						}
					}
				}
			case "item/reasoning/textDelta", "item/reasoning/summaryTextDelta":
				if params, ok := msg["params"].(map[string]interface{}); ok {
					if delta := remoteString(params, "delta"); strings.TrimSpace(delta) != "" {
						_ = writeJSON(map[string]interface{}{
							"type":       models.AgentEventAIThinking,
							"session_id": run.sessionID,
							"message_id": agentAssistantMessageID(run.messageID),
							"item_id":    remoteString(params, "itemId"),
							"delta":      delta,
						})
					}
				}
			case "thread/tokenUsage/updated":
				if params, ok := msg["params"].(map[string]interface{}); ok {
					emitCodexThreadUsage(run, writeJSON, params)
				}
			case "thread/goal/updated":
				if params, ok := msg["params"].(map[string]interface{}); ok {
					nativeGoalSnapshot = codexNativeGoalSnapshot(params["goal"])
					if nativeGoalSnapshot != nil {
						_ = writeJSON(map[string]interface{}{
							"type":        models.AgentEventAIRunProgress,
							"session_id":  run.sessionID,
							"message_id":  agentAssistantMessageID(run.messageID),
							"native_goal": nativeGoalSnapshot,
						})
					}
				}
			case "turn/plan/updated":
				if params, ok := msg["params"].(map[string]interface{}); ok {
					emitCodexPlan(run, writeJSON, params)
				}
			case "item/plan/delta":
				if params, ok := msg["params"].(map[string]interface{}); ok {
					if delta := strings.TrimSpace(remoteString(params, "delta")); delta != "" {
						_ = writeJSON(map[string]interface{}{
							"type":       models.AgentEventAITask,
							"session_id": run.sessionID,
							"message_id": agentAssistantMessageID(run.messageID),
							"item_id":    remoteString(params, "itemId"),
							"tasks": []map[string]interface{}{
								{"subject": delta, "status": "in_progress"},
							},
						})
					}
				}
			case "item/commandExecution/outputDelta":
				if params, ok := msg["params"].(map[string]interface{}); ok {
					itemID := remoteString(params, "itemId")
					_ = writeJSON(map[string]interface{}{
						"type":         models.AgentEventAICommand,
						"session_id":   run.sessionID,
						"message_id":   agentAssistantMessageID(run.messageID),
						"item_id":      itemID,
						"status":       "running",
						"command":      commandsByID[itemID],
						"output_delta": truncateForCloud(remoteString(params, "delta")),
					})
				}
			case "turn/diff/updated":
				if params, ok := msg["params"].(map[string]interface{}); ok {
					if diff := remoteString(params, "diff"); diff != "" {
						_ = writeJSON(map[string]interface{}{
							"type":       models.AgentEventAIFileChange,
							"session_id": run.sessionID,
							"message_id": agentAssistantMessageID(run.messageID),
							"item_id":    "turn_diff",
							"kind":       "diff",
							"diff":       truncateForCloud(diff),
						})
					}
				}
			case "item/started", "item/completed":
				isCompleted := method == "item/completed"
				// commandExecution lifecycle → ai.command (started: command+cwd;
				// completed: exit_code + truncated output). The completed item
				// often omits the command, so remember it from started by item id.
				if cmdID, cmd, cmdCwd, exitCode, stdout, stderr, ok := extractCodexCommandItem(msg); ok && cmdID != "" {
					if cmd != "" {
						commandsByID[cmdID] = cmd
					}
					command := cmd
					if command == "" {
						command = commandsByID[cmdID]
					}
					payload := map[string]interface{}{
						"type":       models.AgentEventAICommand,
						"session_id": run.sessionID,
						"message_id": agentAssistantMessageID(run.messageID),
						"item_id":    cmdID,
						"status":     "started",
						"command":    command,
					}
					if cmdCwd != "" {
						payload["cwd"] = cmdCwd
					}
					if isCompleted {
						payload["status"] = "completed"
						// A finished Codex work item (command/file change) is
						// real progress for the no-progress watchdog.
						run.activity.markProgress()
						if exitCode != nil {
							payload["exit_code"] = *exitCode
						}
						if output := firstNonEmpty(stdout, stderr); output != "" {
							payload["output"] = truncateForCloud(output)
						}
					}
					_ = writeJSON(payload)
				}
				// Best-effort reasoning streaming (codex reasoning items), kept off
				// the prose channel.
				if params, ok := msg["params"].(map[string]interface{}); ok {
					if item, ok := params["item"].(map[string]interface{}); ok {
						emitCodexThinkingIfPresent(run, writeJSON, item)
					}
				}
				// fileChange: seed the approval map (started) and emit ai.file_change
				// once the change is actually applied (completed).
				if fcItemID, fcChanges := extractCodexFileChangeItem(msg); fcItemID != "" && fcChanges != nil {
					fileChangesByID[fcItemID] = fcChanges
					if isCompleted {
						emitCodexFileChangeEvents(run, writeJSON, fcItemID, fcChanges)
					}
				}
			case "turn/completed":
				completedParams, _ := msg["params"].(map[string]interface{})
				var turnErr string
				turnStatus, turnErr = codexTurnResult(completedParams)
				emitCodexUsageIfPresent(run, writeJSON, completedParams)
				if turnStatus == "failed" && turnErr != "" {
					terminalErr = firstNonEmpty(turnErr, "codex turn failed")
				}
				if len(run.nativeGoal) > 0 {
					if turnStatus == "failed" || turnStatus == "interrupted" {
						nativeGoalTerminalDesired = "blocked"
					} else {
						outMu.Lock()
						nativeGoalTerminalDesired = codexNativeGoalTerminalStatus(output.String())
						outMu.Unlock()
					}
					codexPhase = "thread/goal/get(final)"
					_ = send(map[string]interface{}{
						"method": "thread/goal/get",
						"id":     4,
						"params": map[string]interface{}{"threadId": threadID},
					})
					continue
				}
				completed = true
				goto done
			case "error":
				if params, ok := msg["params"].(map[string]interface{}); ok {
					terminalErr = "Codex app-server error: " + firstNonEmpty(remoteString(params, "message"), remoteString(params, "error"), "unknown error")
					goto done
				}
			}
			continue
		}
		if fmt.Sprint(msg["id"]) == "1" && threadID == "" {
			if result, ok := msg["result"].(map[string]interface{}); ok {
				if thread, ok := result["thread"].(map[string]interface{}); ok {
					threadID = remoteString(thread, "id")
				}
			}
			if threadID != "" {
				if err := bindThread(); err != nil {
					// codex mismatch 自愈（2026-10-29 already-in-use 事故的 codex
					// 侧）：本地还是 reserved 占位时，mismatch 说明占位 id 已不可
					// 兑现——claude 族撞 jsonl 的同族问题在 codex 表现为撞绑定锚。
					// 释放绑定、丢掉 resume 锚，就地以全新 thread/start 重试一次。
					// 释放后 confirmBinding 的 expected 为空，不会二次 mismatch。
					if !bindingRetried && strings.Contains(err.Error(), "mismatch") && m.reservedBindingID(run.sessionID) != "" {
						bindingRetried = true
						m.releaseReservedBinding(run.sessionID, "")
						run.resumeSessionID = ""
						delete(threadParams, "threadId")
						threadMethod = "thread/start"
						codexPhase = threadMethod
						threadID = ""
						logger.Warn(fmt.Sprintf("ai.codex.binding.revive: session=%s runSeq=%d reserved 绑定 mismatch 已释放，就地重跑 thread/start", run.sessionID, run.runSeq))
						_ = send(map[string]interface{}{"method": threadMethod, "id": 1, "params": threadParams})
						continue
					}
					terminalErr = "Codex provider binding failed: " + err.Error()
					goto done
				}
				if len(run.nativeGoal) > 0 {
					params := map[string]interface{}{
						"threadId":  threadID,
						"objective": remoteString(run.nativeGoal, "objective"),
						"status":    firstNonEmpty(remoteString(run.nativeGoal, "status"), "active"),
					}
					if budget := remoteInt(run.nativeGoal, "token_budget", 0); budget > 0 {
						params["tokenBudget"] = budget
					}
					codexPhase = "thread/goal/set"
					_ = send(map[string]interface{}{"method": "thread/goal/set", "id": 3, "params": params})
				} else {
					startTurn()
				}
			} else if _, hasResult := msg["result"]; hasResult {
				terminalErr = fmt.Sprintf("Codex app-server %s returned no thread id", threadMethod)
				goto done
			}
		}
		if fmt.Sprint(msg["id"]) == "3" {
			if result, ok := msg["result"].(map[string]interface{}); ok {
				nativeGoalSnapshot = codexNativeGoalSnapshot(result["goal"])
			}
			if nativeGoalSnapshot == nil {
				nativeGoalSyncErr = "Codex app-server thread/goal/set returned no Goal snapshot"
				terminalErr = nativeGoalSyncErr
				goto done
			}
			_ = writeJSON(map[string]interface{}{
				"type":        models.AgentEventAIRunProgress,
				"session_id":  run.sessionID,
				"message_id":  agentAssistantMessageID(run.messageID),
				"native_goal": nativeGoalSnapshot,
			})
			startTurn()
		}
		if fmt.Sprint(msg["id"]) == "4" {
			if result, ok := msg["result"].(map[string]interface{}); ok {
				nativeGoalSnapshot = codexNativeGoalSnapshot(result["goal"])
			}
			if nativeGoalSnapshot == nil {
				nativeGoalSyncErr = "Codex app-server final native Goal read returned no Goal snapshot"
				terminalErr = nativeGoalSyncErr
				goto done
			}
			currentNativeStatus := remoteString(nativeGoalSnapshot, "status")
			if currentNativeStatus == "active" ||
				((turnStatus == "failed" || turnStatus == "interrupted") && currentNativeStatus != "blocked") {
				codexPhase = "thread/goal/set(final)"
				_ = send(map[string]interface{}{
					"method": "thread/goal/set",
					"id":     5,
					"params": map[string]interface{}{"threadId": threadID, "status": nativeGoalTerminalDesired},
				})
				continue
			}
			completed = true
			goto done
		}
		if fmt.Sprint(msg["id"]) == "5" {
			if result, ok := msg["result"].(map[string]interface{}); ok {
				nativeGoalSnapshot = codexNativeGoalSnapshot(result["goal"])
			}
			if nativeGoalSnapshot == nil {
				nativeGoalSyncErr = "Codex app-server final native Goal update returned no Goal snapshot"
				terminalErr = nativeGoalSyncErr
				goto done
			}
			completed = true
			goto done
		}
		if fmt.Sprint(msg["id"]) == "2" && turnID == "" {
			if result, ok := msg["result"].(map[string]interface{}); ok {
				if turn, ok := result["turn"].(map[string]interface{}); ok {
					turnID = remoteString(turn, "id")
				}
			}
			if turnID != "" {
				codexPhase = "running"
				steerControl.markReady(threadID, turnID)
			}
		}
	}

done:
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	stderrWG.Wait()
	if resumeMissing {
		return agentAIRunResumeMissing
	}
	if threadBusy {
		// 与 resumeMissing 同形：不在 pass 内发终态帧，驱动层决定询问 fork
		// （默认否）还是直接落 codex_thread_busy。
		return agentAIRunThreadBusy
	}
	if ctx.Err() != nil {
		status, errMsg := agentAIRunStoppedStatus(run.activity, limiter)
		if len(run.goalIdentity) > 0 {
			if errMsg == "" {
				errMsg = "Codex Goal run stopped with status " + status
			}
			if codexPhase != "" {
				errMsg = fmt.Sprintf("%s while %s", errMsg, codexPhase)
			}
			if stderrText := strings.TrimSpace(stderrBuf.String()); stderrText != "" {
				errMsg = fmt.Sprintf("%s; codex stderr: %s", errMsg, truncateForCloud(stderrText))
			}
			payload := agentAIErrorPayload(run.sessionID, run.messageID, errors.New(errMsg))
			payload["native_goal"] = codexNativeGoalStaleSnapshot(nativeGoalSnapshot, errMsg)
			_ = writeJSON(payload)
			return agentAIRunDone
		}
		statusPayload := map[string]interface{}{
			"type":       models.AgentEventAIStatus,
			"session_id": run.sessionID,
			"status":     status,
		}
		if errMsg != "" {
			if codexPhase != "" {
				errMsg = fmt.Sprintf("%s while %s", errMsg, codexPhase)
			}
			if stderrText := strings.TrimSpace(stderrBuf.String()); stderrText != "" {
				errMsg = fmt.Sprintf("%s; codex stderr: %s", errMsg, truncateForCloud(stderrText))
			}
			statusPayload["error"] = errMsg
		}
		_ = writeJSON(statusPayload)
		return agentAIRunDone
	}
	if terminalErr != "" {
		if stderrText := strings.TrimSpace(stderrBuf.String()); stderrText != "" {
			terminalErr = fmt.Sprintf("%s; codex stderr: %s", terminalErr, truncateForCloud(stderrText))
		}
		payload := agentAIErrorPayload(run.sessionID, run.messageID, errors.New(terminalErr))
		if nativeGoalSyncErr != "" {
			payload["native_goal"] = codexNativeGoalStaleSnapshot(nativeGoalSnapshot, nativeGoalSyncErr)
		} else {
			payload["native_goal"] = nativeGoalSnapshot
		}
		_ = writeJSON(payload)
		return agentAIRunDone
	}
	if !completed {
		if err := scanner.Err(); err != nil {
			payload := agentAIErrorPayload(run.sessionID, run.messageID, err)
			payload["native_goal"] = codexNativeGoalStaleSnapshot(nativeGoalSnapshot, err.Error())
			_ = writeJSON(payload)
			return agentAIRunDone
		}
		if waitErr != nil {
			message := fmt.Sprintf("Codex app-server exited before turn completed while %s: %v", codexPhase, waitErr)
			if stderrText := strings.TrimSpace(stderrBuf.String()); stderrText != "" {
				message = fmt.Sprintf("%s; codex stderr: %s", message, truncateForCloud(stderrText))
			}
			payload := agentAIErrorPayload(run.sessionID, run.messageID, errors.New(message))
			payload["native_goal"] = codexNativeGoalStaleSnapshot(nativeGoalSnapshot, message)
			_ = writeJSON(payload)
			return agentAIRunDone
		}
		if turnStarted {
			message := fmt.Sprintf("Codex app-server exited before turn completed while %s", codexPhase)
			payload := agentAIErrorPayload(run.sessionID, run.messageID, errors.New(message))
			payload["native_goal"] = codexNativeGoalStaleSnapshot(nativeGoalSnapshot, message)
			_ = writeJSON(payload)
			return agentAIRunDone
		}
	}
	if turnStatus == "failed" || turnStatus == "interrupted" {
		if turnStatus == "failed" {
			payload := agentAIErrorPayload(run.sessionID, run.messageID, errors.New("codex turn failed"))
			payload["native_goal"] = nativeGoalSnapshot
			_ = writeJSON(payload)
		} else if len(run.goalIdentity) > 0 {
			payload := agentAIErrorPayload(run.sessionID, run.messageID, errors.New("codex turn interrupted"))
			payload["native_goal"] = nativeGoalSnapshot
			_ = writeJSON(payload)
		} else {
			_ = writeJSON(map[string]interface{}{
				"type":       models.AgentEventAIStatus,
				"session_id": run.sessionID,
				"status":     "interrupted",
			})
		}
		return agentAIRunDone
	}
	outMu.Lock()
	assistantOutput := output.String()
	outMu.Unlock()
	m.appendAssistantHistory(run.sessionID, run.runSeq, run.messageID, assistantOutput)
	donePayload := map[string]interface{}{
		"type":              models.AgentEventAIDone,
		"session_id":        run.sessionID,
		"message_id":        agentAssistantMessageID(run.messageID),
		"codex_thread_id":   threadID,
		"source_session_id": threadID,
		"native_goal":       nativeGoalSnapshot,
	}
	if forkedFromID != "" {
		// fork 二趟成功：终态带 fork 证据，服务端据此把（可能已 confirmed 的）
		// 绑定换绑到新线程——不换绑则后续手机回合永远 resume 回被占用的源线程。
		donePayload["forked_from"] = forkedFromID
	}
	_ = writeJSON(donePayload)
	return agentAIRunDone
}

// codexSteerInput 是 turn/steer 的 input 数组：文本 + 随插话一起发的图片。
// Codex app-server 接受与 turn 相同的 input 形状，所以图片直接用 localImage
// block（steer 路径的图片在入口就落成本地临时文件，见 steer()）。

func codexSteerInput(msg agentAISteerMessage) []map[string]interface{} {
	input := []map[string]interface{}{
		{"type": "text", "text": msg.Content, "text_elements": []interface{}{}},
	}
	for _, block := range msg.ImageBlocks {
		// ImageBlocks 在 steer 入口按 Claude 形状编码（base64 source）。Codex
		// app-server 的 input 认 data:image URL，所以这里转成 data URL——两边
		// 都不需要临时文件，也就没有「排队期间临时文件被清理」的生命周期问题。
		source, _ := block["source"].(map[string]interface{})
		mediaType, _ := source["media_type"].(string)
		data, _ := source["data"].(string)
		if mediaType == "" || data == "" {
			continue
		}
		input = append(input, map[string]interface{}{
			"type": "image",
			"url":  "data:" + mediaType + ";base64," + data,
		})
	}
	return input
}

func codexTurnInput(run agentAIRun) []map[string]interface{} {
	text := run.prompt + agentAIAttachmentPromptSuffix(run.attachments, false)
	input := []map[string]interface{}{{"type": "text", "text": text, "text_elements": []interface{}{}}}
	for _, attachment := range run.attachments {
		if attachment.Type != "image" {
			continue
		}
		if attachment.URL != "" {
			input = append(input, map[string]interface{}{"type": "image", "url": attachment.URL})
		} else if attachment.Path != "" {
			input = append(input, map[string]interface{}{"type": "localImage", "path": attachment.Path})
		}
	}
	return input
}

func (m *agentAIManager) handleCodexAppServerRequest(ctx context.Context, run agentAIRun, writeJSON agentTerminalWriter, method string, params map[string]interface{}, fileChanges json.RawMessage) (map[string]interface{}, int, error) {
	if isCodexApprovalMethod(method) {
		result, err := m.codexAppServerApprovalResult(ctx, run, writeJSON, method, params, fileChanges)
		return result, -32000, err
	}
	if method == "item/tool/requestUserInput" {
		result, err := m.codexAppServerUserInputResult(ctx, run, writeJSON, params)
		return result, -32000, err
	}
	return nil, -32601, fmt.Errorf("unsupported Codex app-server request: %s", method)
}

func isCodexApprovalMethod(method string) bool {
	switch method {
	case "item/commandExecution/requestApproval", "item/fileChange/requestApproval", "item/permissions/requestApproval", "execCommandApproval", "applyPatchApproval":
		return true
	default:
		return false
	}
}

func (m *agentAIManager) codexAppServerUserInputResult(ctx context.Context, run agentAIRun, writeJSON agentTerminalWriter, params map[string]interface{}) (map[string]interface{}, error) {
	rawQuestions, _ := params["questions"].([]interface{})
	if len(rawQuestions) == 0 {
		return nil, errors.New("Codex requestUserInput contained no questions")
	}
	answers := make(map[string]interface{}, len(rawQuestions))
	for index, rawQuestion := range rawQuestions {
		question := mapIf(rawQuestion)
		if question == nil {
			continue
		}
		questionID := firstNonEmpty(remoteString(question, "id"), fmt.Sprintf("question_%d", index+1))
		optionID := newAgentAIApprovalID(run.sessionID, run.runSeq)
		choices, labelByID := codexUserInputChoices(question["options"])
		allowCustom := len(choices) == 0 || remoteBool(question, "isOther", false)
		if len(choices) == 0 {
			choices = []agentAIOptionChoice{{ID: "custom", Label: "自定义回答"}}
		}
		waiter := &agentAICodexInputWaiter{
			sessionID: run.sessionID,
			runSeq:    run.runSeq,
			optionID:  optionID,
			respond:   make(chan agentAICodexInputAnswer, 1),
		}
		key := agentAICodexInputMapKey(run.sessionID, optionID)
		m.mu.Lock()
		m.codexInputs[key] = waiter
		m.mu.Unlock()
		cleanup := func() {
			m.mu.Lock()
			if current := m.codexInputs[key]; current == waiter {
				delete(m.codexInputs, key)
			}
			m.mu.Unlock()
		}
		if run.activity != nil {
			run.activity.setAwaitingApproval(true)
		}
		err := writeJSON(map[string]interface{}{
			"type":         models.AgentEventAIOptionRequest,
			"session_id":   run.sessionID,
			"message_id":   agentAssistantMessageID(run.messageID),
			"option_id":    optionID,
			"title":        firstNonEmpty(remoteString(question, "header"), remoteString(question, "question")),
			"question":     remoteString(question, "question"),
			"options":      choices,
			"allow_custom": allowCustom,
			"multi":        false,
			"provider":     "codex",
			"question_id":  questionID,
		})
		if err != nil {
			cleanup()
			if run.activity != nil {
				run.activity.setAwaitingApproval(false)
			}
			return nil, err
		}
		var answer agentAICodexInputAnswer
		select {
		case <-ctx.Done():
			cleanup()
			if run.activity != nil {
				run.activity.setAwaitingApproval(false)
			}
			return nil, ctx.Err()
		case answer = <-waiter.respond:
			cleanup()
		case <-time.After(agentAIApprovalTimeout):
			// 交互输入唯一 deadline(2026-11-01 审计 P0):codex requestUserInput
			// 此前无任何上限——断连即永挂,ai.run.progress 心跳还让服务端永远
			// 视为活跑。超时与审批同源(默认 24h,可 env 调),到期按取消收口。
			// 帧型修正(2026-11-08):超时的是 option 交互,借发
			// ai.approval.cancelled 会让审批取消处理器对 option id 做 no-op、
			// 选项卡无从作废——改发 ai.option.cancelled(审批族白名单,同广播)。
			cleanup()
			if run.activity != nil {
				run.activity.setAwaitingApproval(false)
			}
			m.emitOptionCancelled(writeJSON, run.sessionID, []string{optionID}, "approval_timeout")
			return nil, fmt.Errorf("Codex requestUserInput timed out after %s", agentAIApprovalTimeout)
		}
		if run.activity != nil {
			run.activity.setAwaitingApproval(false)
		}
		values := make([]string, 0, len(answer.selected)+1)
		for _, selected := range answer.selected {
			if label := labelByID[selected]; label != "" {
				values = append(values, label)
			} else if selected != "custom" && strings.TrimSpace(selected) != "" {
				values = append(values, selected)
			}
		}
		if custom := strings.TrimSpace(answer.custom); custom != "" {
			values = append(values, custom)
		}
		if len(values) == 0 {
			return nil, fmt.Errorf("Codex requestUserInput question %s received no answer", questionID)
		}
		answers[questionID] = map[string]interface{}{"answers": values}
	}
	if len(answers) == 0 {
		return nil, errors.New("Codex requestUserInput contained no valid questions")
	}
	return map[string]interface{}{"answers": answers}, nil
}

func codexUserInputChoices(raw interface{}) ([]agentAIOptionChoice, map[string]string) {
	items, _ := raw.([]interface{})
	choices := make([]agentAIOptionChoice, 0, len(items))
	labels := make(map[string]string, len(items))
	for index, rawItem := range items {
		item := mapIf(rawItem)
		if item == nil {
			continue
		}
		label := strings.TrimSpace(remoteString(item, "label"))
		if label == "" {
			continue
		}
		id := fmt.Sprintf("option_%d", index+1)
		choices = append(choices, agentAIOptionChoice{ID: id, Label: label, Description: remoteString(item, "description")})
		labels[id] = label
	}
	return choices, labels
}

func (m *agentAIManager) codexAppServerApprovalResult(ctx context.Context, run agentAIRun, writeJSON agentTerminalWriter, method string, params map[string]interface{}, fileChanges json.RawMessage) (map[string]interface{}, error) {
	// Policy short-circuit (mirrors the Claude hook): auto-approve/deny locally
	// when the device policy says so, with no cloud round-trip. Only
	// require_approval falls through to requestApproval.
	// bypass_all 快路径(仅策略服务缺失,同 agentcore/claude hook)。
	codexSvc := m.approvalService()
	if run.approvalMode == agentAIApprovalModeBypassAll && codexSvc == nil {
		logger.Info(fmt.Sprintf("approval-hook: BYPASS-ALL fast path (policy service unavailable) method=%s session=%s (codex)", method, run.sessionID))
		return codexApprovalResponseResult(method, params, agentAIApprovalResponse{Decision: models.AgentAIApprovalDecisionAccept})
	}
	if codexSvc != nil {
		toolName, toolInput := codexPolicyToolHint(method, params, fileChanges)
		rawDecision, rawMatchedID := codexSvc.evaluateApprovalDecision(toolName, toolInput, run.projectPath)
		overridden := applyApprovalModeOverride(run.approvalMode, rawDecision, toolName)
		if overridden != rawDecision {
			logger.Info(fmt.Sprintf("approval-hook: MODE-OVERRIDE mode=%s %s→%s tool=%s session=%s (codex)", run.approvalMode, rawDecision, overridden, toolName, run.sessionID))
		}
		switch decision, matchedID := overridden, rawMatchedID; decision {
		case decisionAutoApprove:
			logger.Info(fmt.Sprintf("approval-hook: AUTO-APPROVE by policy rule=%s method=%s session=%s (codex, no cloud round-trip)", matchedID, method, run.sessionID))
			return codexApprovalResponseResult(method, params, agentAIApprovalResponse{Decision: models.AgentAIApprovalDecisionAccept})
		case decisionAutoDeny:
			logger.Info(fmt.Sprintf("approval-hook: AUTO-DENY by policy rule=%s method=%s session=%s (codex)", matchedID, method, run.sessionID))
			return codexApprovalResponseResult(method, params, agentAIApprovalResponse{Decision: models.AgentAIApprovalDecisionDecline})
		case decisionRequireApproval:
			run.matchedRuleID = matchedID
			run.policyVersion = codexSvc.effectiveApprovalPolicyForPath(run.projectPath).Version
		}
	}
	// 会话级批准记忆检查点（policy 之后、云端往返之前）：本会话用户对同键选过
	// accept_for_session 的直接放行（agent_ai_session_approvals.go）。codex 原生
	// scope=session 行为保留不动，这里只是 agent 侧叠加记忆免往返。provider 取
	// 请求对象自身（buildCodexApprovalRequest 硬编码 "codex"，而 run.provider
	// 可能是 "auto"——与 approval() 记录点存的 waiter.request.Provider 对齐）。
	// ask_all/bypass_all 下跳过会话批准记忆(同 claude hook)。read_only 只对
	// 写/执行族跳过——记忆免问不能复活被压制的写权限。
	// double build intentional (同上游 claude hook):hit path stays branch-free
	if pre := buildCodexApprovalRequest(run, method, params, fileChanges); approvalModeSkipsSessionMemoryForTool(run.approvalMode, pre.ToolName) {
		logger.Info(fmt.Sprintf("approval-hook: SESSION-MEMORY-SKIPPED mode=%s method=%s session=%s (codex)", run.approvalMode, method, run.sessionID))
	} else if m.sessionApprovalAllows(run.sessionID, pre.Provider, pre.Kind, pre.ToolName, pre.Command) {
		logger.Info(fmt.Sprintf("approval-hook: SESSION-APPROVED (memory) tool=%s kind=%s method=%s session=%s (codex)", pre.ToolName, pre.Kind, method, run.sessionID))
		m.journalSessionApprovalHit(run.sessionID, run.runSeq, pre.Kind, pre.ToolName, pre.Command)
		return codexApprovalResponseResult(method, params, agentAIApprovalResponse{Decision: models.AgentAIApprovalDecisionAccept})
	}
	req := buildCodexApprovalRequest(run, method, params, fileChanges)
	// Use the env-configurable approval timeout (ALIANG_AI_APPROVAL_TIMEOUT,
	// default 24h) — same source as the Claude hook path (see handleClaudeApprovalHook).
	// Previously this was a hardcoded 30m that clipped long approval waits even when
	// the operator had raised the configured ceiling.
	approvalCtx, cancel := context.WithTimeout(ctx, agentAIApprovalTimeout)
	defer cancel()
	response, err := m.requestApproval(approvalCtx, run, writeJSON, req)
	if err != nil {
		return nil, err
	}
	return codexApprovalResponseResult(method, params, response)
}

func buildCodexApprovalRequest(run agentAIRun, method string, params map[string]interface{}, fileChanges json.RawMessage) agentAIApprovalRequest {
	rawJSON := marshalAgentAIRaw(params)
	req := agentAIApprovalRequest{
		ID:       firstNonEmpty(remoteString(params, "approvalId"), remoteString(params, "itemId"), remoteString(params, "callId"), newAgentAIApprovalID(run.sessionID, run.runSeq)),
		Provider: "codex",
		Kind:     models.AgentAIApprovalKindTool,
		Title:    "Approve Codex action",
		Reason:   firstNonEmpty(boundaryApprovalReason(run.matchedRuleID), remoteString(params, "reason")),
		CWD:      firstNonEmpty(remoteString(params, "cwd"), run.projectPath),
		Raw:      rawJSON,
		AvailableDecisions: []string{
			models.AgentAIApprovalDecisionAccept,
			models.AgentAIApprovalDecisionAcceptForSession,
			models.AgentAIApprovalDecisionDecline,
			models.AgentAIApprovalDecisionCancel,
		},
	}
	switch method {
	case "item/commandExecution/requestApproval", "execCommandApproval":
		req.Kind = models.AgentAIApprovalKindCommand
		req.Title = "Approve Codex command"
		req.Command = codexApprovalCommand(params)
		req.ToolName = "command"
	case "item/fileChange/requestApproval", "applyPatchApproval":
		req.Kind = models.AgentAIApprovalKindFileChange
		req.Title = "Approve Codex file change"
		// The fileChange approval request carries no diff; the proposed edits
		// arrive in the prior item/started fileChange item, tracked by the caller
		// and passed in as fileChanges. Fall back to the legacy params shape for
		// older codex that inlined them.
		if fileChanges != nil {
			req.FileChanges = fileChanges
		} else {
			req.FileChanges = marshalAgentAIRaw(firstNonNil(params["fileChanges"], params["file_changes"]))
		}
	case "item/permissions/requestApproval":
		req.Kind = models.AgentAIApprovalKindPermissions
		req.Title = "Approve Codex permissions"
		req.ToolName = "permissions"
		req.ToolInput = marshalAgentAIRaw(params["permissions"])
	default:
		req.ToolName = method
	}
	if decisions := codexAvailableDecisions(params["availableDecisions"]); len(decisions) > 0 {
		req.AvailableDecisions = decisions
	}
	req.MatchedRuleID = run.matchedRuleID
	req.PolicyVersion = run.policyVersion
	return req
}

func codexApprovalCommand(params map[string]interface{}) string {
	if value := strings.TrimSpace(remoteString(params, "command")); value != "" {
		return value
	}
	if raw, ok := params["command"].([]interface{}); ok {
		parts := make([]string, 0, len(raw))
		for _, item := range raw {
			part := strings.TrimSpace(fmt.Sprint(item))
			if part != "" {
				parts = append(parts, part)
			}
		}
		return strings.Join(parts, " ")
	}
	return ""
}

// codexPolicyToolHint maps a Codex approval method+params to a (toolName,
// toolInput) hint for device-policy evaluation. Command methods map to Bash +
// the command string; file-change methods map to a file-mutation tool name;
// permissions and anything unknown map to "" so the policy's default applies
// (fail-safe under balanced, approve-all under allow_all).

func codexPolicyToolHint(method string, params map[string]interface{}, fileChanges json.RawMessage) (string, json.RawMessage) {
	switch method {
	case "item/commandExecution/requestApproval", "execCommandApproval":
		return "Bash", marshalAgentAIRaw(map[string]interface{}{"command": codexApprovalCommand(params)})
	case "item/fileChange/requestApproval", "applyPatchApproval":
		if len(fileChanges) == 0 {
			fileChanges = marshalAgentAIRaw(firstNonNil(params["fileChanges"], params["file_changes"]))
		}
		var changes []map[string]interface{}
		_ = json.Unmarshal(fileChanges, &changes)
		paths := make([]string, 0, len(changes))
		for _, change := range changes {
			if path := strings.TrimSpace(remoteString(change, "path")); path != "" {
				paths = append(paths, path)
			}
		}
		return "Edit", marshalAgentAIRaw(map[string]interface{}{"paths": paths})
	default:
		return "", nil
	}
}

func codexAvailableDecisions(raw interface{}) []string {
	items, ok := raw.([]interface{})
	if !ok {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		switch value := item.(type) {
		case string:
			if decision := normalizeAgentAIApprovalDecision(value); decision != "" {
				out = append(out, decision)
			}
		case map[string]interface{}:
			for key := range value {
				if decision := normalizeAgentAIApprovalDecision(key); decision != "" {
					out = append(out, decision)
				}
			}
		}
	}
	return out
}

func codexApprovalResponseResult(method string, params map[string]interface{}, response agentAIApprovalResponse) (map[string]interface{}, error) {
	switch method {
	case "execCommandApproval", "applyPatchApproval":
		return map[string]interface{}{"decision": codexLegacyReviewDecision(response.Decision)}, nil
	case "item/commandExecution/requestApproval":
		return map[string]interface{}{"decision": codexCommandDecision(response.Decision)}, nil
	case "item/fileChange/requestApproval":
		return map[string]interface{}{"decision": codexFileChangeDecision(response.Decision)}, nil
	case "item/permissions/requestApproval":
		switch response.Decision {
		case models.AgentAIApprovalDecisionAccept, models.AgentAIApprovalDecisionAcceptForSession:
			permissions, _ := params["permissions"].(map[string]interface{})
			if permissions == nil {
				permissions = map[string]interface{}{}
			}
			scope := normalizeAgentAIApprovalScope(response.Scope)
			if response.Decision == models.AgentAIApprovalDecisionAcceptForSession {
				scope = "session"
			}
			return map[string]interface{}{"permissions": permissions, "scope": scope}, nil
		default:
			return nil, errors.New("permission request denied by user")
		}
	default:
		if response.Decision == models.AgentAIApprovalDecisionAccept || response.Decision == models.AgentAIApprovalDecisionAcceptForSession {
			return map[string]interface{}{}, nil
		}
		return nil, errors.New("approval denied by user")
	}
}

func codexLegacyReviewDecision(decision string) interface{} {
	switch decision {
	case models.AgentAIApprovalDecisionAccept:
		return "approved"
	case models.AgentAIApprovalDecisionAcceptForSession:
		return "approved_for_session"
	case models.AgentAIApprovalDecisionCancel:
		return "abort"
	default:
		return "denied"
	}
}

func codexCommandDecision(decision string) interface{} {
	switch decision {
	case models.AgentAIApprovalDecisionAccept:
		return "accept"
	case models.AgentAIApprovalDecisionAcceptForSession:
		return "acceptForSession"
	case models.AgentAIApprovalDecisionCancel:
		return "cancel"
	default:
		return "decline"
	}
}

func codexFileChangeDecision(decision string) string {
	switch decision {
	case models.AgentAIApprovalDecisionAccept:
		return "accept"
	case models.AgentAIApprovalDecisionAcceptForSession:
		return "acceptForSession"
	case models.AgentAIApprovalDecisionCancel:
		return "cancel"
	default:
		return "decline"
	}
}

func firstNonNil(values ...interface{}) interface{} {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

// runCLIPass 跑一遍 CLI。第二个返回值仅在 agentAIRunEffortRejected 时携带
// CLI 的 effort 拒绝分类(flag 本身不受支持 / 合法清单),供调用方自愈重试。
