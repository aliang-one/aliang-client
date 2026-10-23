package services

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/common/logger"
)

const agentAISubagentRegistryLimit = 64

type agentAISubagentProgress struct {
	CurrentStep  string
	StatusDetail string
	LastTool     string
	ToolUses     int
	TotalTokens  int64
	DurationMs   int64
	Summary      string
	OutputFile   string
}

type agentAISubagent struct {
	TaskID    string
	ToolUseID string
	// MessageID 是 start 时刻的父会话 assistant message id。一个 task 的所有
	// phase 事件都锚定它，保证服务端 structuredEventId(subagent,session,msg,task)
	// 稳定 → 单行 upsert，不会因父消息推进而裂成两行。
	MessageID      string
	Description    string
	SubagentType   string
	SpawnDepth     int
	TaskType       string
	IsBackgrounded bool
	Status         string
	CurrentStep    string
	StatusDetail   string
	LastTool       string
	ToolUses       int
	TotalTokens    int64
	DurationMs     int64
	Summary        string
	OutputFile     string
	StartedAt      time.Time
	LastProgressAt time.Time
	EndedAt        time.Time
	// Legacy 标记老版本 CC（无 task_* 帧）：注册表由父 Agent tool_use 兜底登记
	// （task_id 暂用 tool_use_id 顶替），等无父标记的 tool_result 到达再发终态。
	// 真帧到达后 start() 覆盖登记时清零。
	Legacy bool
}

func (s agentAISubagent) active() bool {
	return s.Status == "started" || s.Status == "progress"
}

type agentAISubagentRegistry struct {
	mu          sync.Mutex
	byToolUseID map[string]agentAISubagent
	taskAliases map[string]string
	order       []string
	activeSince time.Time
}

func newAgentAISubagentRegistry() *agentAISubagentRegistry {
	return &agentAISubagentRegistry{
		byToolUseID: make(map[string]agentAISubagent),
		taskAliases: make(map[string]string),
	}
}

func (r *agentAISubagentRegistry) start(taskID, toolUseID, messageID, description, subagentType string, spawnDepth int, taskType string, background bool, now time.Time) (agentAISubagent, bool) {
	if r == nil {
		return agentAISubagent{}, false
	}
	taskID = strings.TrimSpace(taskID)
	toolUseID = strings.TrimSpace(toolUseID)
	if taskID == "" || toolUseID == "" {
		return agentAISubagent{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if existing, ok := r.byToolUseID[toolUseID]; ok {
		if !existing.active() {
			return existing, false
		}
		if existing.TaskID != taskID {
			r.taskAliases[existing.TaskID] = toolUseID
			existing.TaskID = taskID
			// 真 task_started 帧到达，覆盖兜底登记 → 清 legacy 标记。
			existing.Legacy = false
		}
		r.taskAliases[taskID] = toolUseID
		if messageID != "" {
			existing.MessageID = messageID
		}
		if description != "" {
			existing.Description = description
		}
		if subagentType != "" {
			existing.SubagentType = subagentType
		}
		if spawnDepth > 0 {
			existing.SpawnDepth = spawnDepth
		}
		if taskType != "" {
			existing.TaskType = taskType
		}
		existing.IsBackgrounded = background
		r.byToolUseID[toolUseID] = existing
		return existing, true
	}
	if len(r.byToolUseID) >= agentAISubagentRegistryLimit && !r.evictOldestTerminalLocked() {
		logger.Warn(fmt.Sprintf("ai.subagent: registry at cap %d with all entries active; dropping task_id=%s tool_use_id=%s (its lifecycle will be invisible)", agentAISubagentRegistryLimit, taskID, toolUseID))
		return agentAISubagent{}, false
	}
	if now.IsZero() {
		now = time.Now()
	}
	record := agentAISubagent{
		TaskID: taskID, ToolUseID: toolUseID, MessageID: messageID, Description: description,
		SubagentType: subagentType, SpawnDepth: spawnDepth, TaskType: taskType,
		IsBackgrounded: background, Status: "started", StartedAt: now,
		LastProgressAt: now,
	}
	r.byToolUseID[toolUseID] = record
	r.taskAliases[taskID] = toolUseID
	r.order = append(r.order, toolUseID)
	if r.activeCountLocked() == 1 {
		r.activeSince = now
	}
	return record, true
}

// startLegacy 兜底登记老版本 CC 的 Agent tool_use（无 task_* 帧）：task_id 用
// tool_use_id 顶替、打 Legacy 标记。不发射事件——真帧可能紧随其后（实测顺序
// assistant→task_started），此刻发射必然与真 started 撞出两行。终态在无父标记
// 的 tool_result 到达时补发（claudeToolResultEvents），拆除时 finalize 兜底。
func (r *agentAISubagentRegistry) startLegacy(toolUseID, messageID, description, subagentType string, now time.Time) (agentAISubagent, bool) {
	if r == nil {
		return agentAISubagent{}, false
	}
	record, ok := r.start(toolUseID, toolUseID, messageID, description, subagentType, 1, "local_agent", false, now)
	if !ok {
		return record, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	record.Legacy = true
	r.byToolUseID[record.ToolUseID] = record
	return record, true
}

func (r *agentAISubagentRegistry) progress(id string, now time.Time, patch agentAISubagentProgress) (agentAISubagent, bool) {
	if r == nil {
		return agentAISubagent{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	toolUseID, ok := r.resolveLocked(id)
	if !ok {
		return agentAISubagent{}, false
	}
	record := r.byToolUseID[toolUseID]
	if !record.active() {
		return record, false
	}
	if now.IsZero() {
		now = time.Now()
	}
	record.Status = "progress"
	record.LastProgressAt = now
	if patch.CurrentStep != "" {
		record.CurrentStep = patch.CurrentStep
	}
	if patch.StatusDetail != "" {
		record.StatusDetail = patch.StatusDetail
	}
	if patch.LastTool != "" {
		record.LastTool = patch.LastTool
	}
	if patch.ToolUses > 0 {
		record.ToolUses = patch.ToolUses
	}
	if patch.TotalTokens > 0 {
		record.TotalTokens = patch.TotalTokens
	}
	if patch.DurationMs > 0 {
		record.DurationMs = patch.DurationMs
	}
	if patch.Summary != "" {
		record.Summary = truncateForCloud(patch.Summary)
	}
	if patch.OutputFile != "" {
		record.OutputFile = patch.OutputFile
	}
	r.byToolUseID[toolUseID] = record
	return record, true
}

func (r *agentAISubagentRegistry) finish(id, phase, summary string, now time.Time) (agentAISubagent, bool) {
	if r == nil || (phase != "completed" && phase != "failed" && phase != "interrupted") {
		return agentAISubagent{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	toolUseID, ok := r.resolveLocked(id)
	if !ok {
		return agentAISubagent{}, false
	}
	record := r.byToolUseID[toolUseID]
	if !record.active() {
		if phase != record.Status {
			return record, false
		}
		if summary != "" {
			record.Summary = truncateForCloud(summary)
			r.byToolUseID[toolUseID] = record
		}
		return record, true
	}
	if now.IsZero() {
		now = time.Now()
	}
	record.Status = phase
	record.EndedAt = now
	if summary != "" {
		record.Summary = truncateForCloud(summary)
	}
	r.byToolUseID[toolUseID] = record
	if r.activeCountLocked() == 0 {
		r.activeSince = time.Time{}
	}
	return record, true
}

func (r *agentAISubagentRegistry) finalize(now time.Time) []agentAISubagent {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if now.IsZero() {
		now = time.Now()
	}
	var out []agentAISubagent
	for _, id := range r.order {
		record, ok := r.byToolUseID[id]
		if !ok || !record.active() {
			continue
		}
		record.Status = "interrupted"
		record.EndedAt = now
		r.byToolUseID[id] = record
		out = append(out, record)
	}
	r.activeSince = time.Time{}
	return out
}

func (r *agentAISubagentRegistry) stalled(now time.Time, window time.Duration) []agentAISubagent {
	if r == nil || window <= 0 {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []agentAISubagent
	for _, id := range r.order {
		record, ok := r.byToolUseID[id]
		if ok && record.active() && !record.LastProgressAt.IsZero() && now.Sub(record.LastProgressAt) > window {
			out = append(out, record)
		}
	}
	return out
}

func (r *agentAISubagentRegistry) activeCount() int {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.activeCountLocked()
}

func (r *agentAISubagentRegistry) count() int {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.byToolUseID)
}

func (r *agentAISubagentRegistry) waitFor(now time.Time) time.Duration {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.activeCountLocked() == 0 || r.activeSince.IsZero() {
		return 0
	}
	return now.Sub(r.activeSince)
}

func (r *agentAISubagentRegistry) resolveLocked(id string) (string, bool) {
	if _, ok := r.byToolUseID[id]; ok {
		return id, true
	}
	toolUseID, ok := r.taskAliases[id]
	if !ok {
		return "", false
	}
	_, ok = r.byToolUseID[toolUseID]
	return toolUseID, ok
}

func (r *agentAISubagentRegistry) activeCountLocked() int {
	count := 0
	for _, record := range r.byToolUseID {
		if record.active() {
			count++
		}
	}
	return count
}

func (r *agentAISubagentRegistry) evictOldestTerminalLocked() bool {
	var candidates []agentAISubagent
	for _, record := range r.byToolUseID {
		if !record.active() {
			candidates = append(candidates, record)
		}
	}
	if len(candidates) == 0 {
		return false
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].StartedAt.Before(candidates[j].StartedAt) })
	victim := candidates[0].ToolUseID
	delete(r.byToolUseID, victim)
	for alias, toolUseID := range r.taskAliases {
		if toolUseID == victim {
			delete(r.taskAliases, alias)
		}
	}
	// order 同步修剪，否则 finalize/stalled 永远拖着死 id 扫（长 run 数百个
	// 顺序子 agent 时退化为 O(n) 空 walk）。
	for i, id := range r.order {
		if id == victim {
			r.order = append(r.order[:i], r.order[i+1:]...)
			break
		}
	}
	return true
}

// legacyStarted reports whether id resolves to a legacy-registered record
// (registered from the parent Agent tool_use because no task_started frame has
// arrived) that is still awaiting its terminal event.
func (r *agentAISubagentRegistry) legacyStarted(id string) bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	toolUseID, ok := r.resolveLocked(id)
	if !ok {
		return false
	}
	record, ok := r.byToolUseID[toolUseID]
	return ok && record.Legacy && record.active()
}

// claudeTaskPhase maps a CC task_updated/task_notification status string onto
// the wire phase vocabulary. Only "completed" is a recognized success; any
// other non-empty status fails closed (the raw string rides status_detail so
// the phone can still show what CC actually said). Empty → "" (caller keeps
// the record's current phase).
func claudeTaskPhase(status string) string {
	status = strings.TrimSpace(status)
	if status == "" {
		return ""
	}
	if status == "completed" {
		return "completed"
	}
	return "failed"
}

// agentAISubagentPayload builds the ai.subagent wire payload for one record.
// task_id is REQUIRED by the server parser (empty ⇒ the whole event is
// rejected), and every record in the registry carries one by construction —
// legacy records substitute the tool_use_id until real frames re-anchor them.
func agentAISubagentPayload(sessionID string, record agentAISubagent) map[string]interface{} {
	phase := record.Status
	if phase == "" {
		phase = "started"
	}
	payload := map[string]interface{}{
		"type":       models.AgentEventAISubagent,
		"session_id": sessionID,
		"message_id": record.MessageID,
		"task_id":    record.TaskID,
		"phase":      phase,
	}
	if record.ToolUseID != "" {
		payload["tool_use_id"] = record.ToolUseID
	}
	if record.Description != "" {
		payload["description"] = record.Description
	}
	if record.SubagentType != "" {
		payload["subagent_type"] = record.SubagentType
	}
	if record.SpawnDepth > 0 {
		payload["spawn_depth"] = record.SpawnDepth
	}
	if record.TaskType != "" {
		payload["task_type"] = record.TaskType
	}
	payload["is_backgrounded"] = record.IsBackgrounded
	if record.CurrentStep != "" {
		payload["current_step"] = record.CurrentStep
	}
	if record.StatusDetail != "" {
		payload["status_detail"] = record.StatusDetail
	}
	if record.LastTool != "" {
		payload["last_tool"] = record.LastTool
	}
	if record.ToolUses > 0 {
		payload["tool_uses"] = record.ToolUses
	}
	if record.TotalTokens > 0 {
		payload["total_tokens"] = record.TotalTokens
	}
	if record.DurationMs > 0 {
		payload["duration_ms"] = record.DurationMs
	}
	if record.Summary != "" {
		payload["summary"] = record.Summary
	}
	if record.OutputFile != "" {
		payload["output_file"] = record.OutputFile
	}
	if !record.StartedAt.IsZero() {
		payload["started_at"] = record.StartedAt.UTC().Format(time.RFC3339)
	}
	if !record.EndedAt.IsZero() {
		payload["ended_at"] = record.EndedAt.UTC().Format(time.RFC3339)
	}
	return payload
}

// handleClaudeTaskEvents consumes CC's purpose-built subagent lifecycle frames
// (system/task_started|task_progress|task_updated|task_notification, verified
// live on claude 2.1.286 --print stream-json) and mirrors them as ai.subagent
// protocol events. Each phase change also feeds the watchdog: task_progress is
// MEANINGFUL progress (it resets the contiguous tool-wait clock and the
// no-progress window), and the active-subagent count pauses the idle guard.
// Non-task events fall through untouched. Safe to call for every decoded
// event; registry and activity are nil-safe.
func handleClaudeTaskEvents(event map[string]interface{}, run agentAIRun, writeJSON agentTerminalWriter) {
	if remoteString(event, "type") != "system" {
		return
	}
	subtype := remoteString(event, "subtype")
	switch subtype {
	case "task_started":
		handleClaudeTaskStarted(event, run, writeJSON)
	case "task_progress":
		handleClaudeTaskProgress(event, run, writeJSON)
	case "task_updated":
		handleClaudeTaskUpdated(event, run, writeJSON)
	case "task_notification":
		handleClaudeTaskNotification(event, run, writeJSON)
	}
}

func handleClaudeTaskStarted(event map[string]interface{}, run agentAIRun, writeJSON agentTerminalWriter) {
	taskID := remoteString(event, "task_id")
	toolUseID := remoteString(event, "tool_use_id")
	// task_started must carry tool_use_id (verified in capture) — it is the
	// canonical key and the join to the parent's Agent tool_use. Without both
	// ids the record cannot be keyed or later joined; drop with a trace.
	if taskID == "" || toolUseID == "" {
		logger.Info(fmt.Sprintf("ai.subagent: task_started missing ids (task=%q tool_use=%q); ignored", taskID, toolUseID))
		return
	}
	depth := 1
	if n, ok := event["spawn_depth"].(float64); ok && n >= 1 {
		depth = int(n)
	}
	record, ok := run.subagents.start(
		taskID, toolUseID, streamingMessageID(run),
		remoteString(event, "description"), remoteString(event, "subagent_type"),
		depth, remoteString(event, "task_type"),
		event["is_backgrounded"] == true, time.Now(),
	)
	if !ok {
		return
	}
	run.activity.beginSubagent()
	_ = writeJSON(agentAISubagentPayload(run.sessionID, record))
}

func handleClaudeTaskProgress(event map[string]interface{}, run agentAIRun, writeJSON agentTerminalWriter) {
	taskID := remoteString(event, "task_id")
	// task_progress 的用量挂在嵌套 usage 对象下（capture 实测:
	// usage:{total_tokens, tool_uses, duration_ms}），description 才在顶层。
	patch := agentAISubagentProgress{
		CurrentStep:  remoteString(event, "description"),
		StatusDetail: remoteString(event, "status_detail"),
		LastTool:     remoteString(event, "last_tool_name"),
	}
	if usage := mapIf(event["usage"]); usage != nil {
		patch.ToolUses = remoteIntValue(usage["tool_uses"])
		patch.DurationMs = int64(remoteIntValue(usage["duration_ms"]))
		patch.TotalTokens = int64(remoteIntValue(usage["total_tokens"]))
	}
	record, ok := run.subagents.progress(taskID, time.Now(), patch)
	if !ok {
		return
	}
	// Progress = the subagent is demonstrably alive: feed every watchdog clock.
	run.activity.subagentProgress()
	_ = writeJSON(agentAISubagentPayload(run.sessionID, record))
}

func handleClaudeTaskUpdated(event map[string]interface{}, run agentAIRun, writeJSON agentTerminalWriter) {
	patch := mapIf(event["patch"])
	if patch == nil {
		return
	}
	phase := claudeTaskPhase(remoteString(patch, "status"))
	if phase == "" {
		return
	}
	taskID := remoteString(event, "task_id")
	record, ok := run.subagents.finish(taskID, phase, "", time.Now())
	if !ok {
		// Orphan terminal evidence (record never started here — resume across
		// runs, older CC, dropped frame): synthesize a terminal-only record so
		// the phone still learns the task ended. Never feeds the watchdog.
		if record, ok = synthesizeTerminalSubagent(run, taskID, remoteString(event, "tool_use_id"), phase); ok {
			_ = writeJSON(agentAISubagentPayload(run.sessionID, record))
		}
		return
	}
	run.activity.endSubagent()
	_ = writeJSON(agentAISubagentPayload(run.sessionID, record))
}

func handleClaudeTaskNotification(event map[string]interface{}, run agentAIRun, writeJSON agentTerminalWriter) {
	phase := claudeTaskPhase(remoteString(event, "status"))
	if phase == "" {
		return
	}
	taskID := remoteString(event, "task_id")
	usage := mapIf(event["usage"])
	patch := agentAISubagentProgress{Summary: remoteString(event, "summary"), OutputFile: remoteString(event, "output_file")}
	if usage != nil {
		patch.ToolUses = remoteIntValue(usage["tool_uses"])
		patch.DurationMs = int64(remoteIntValue(usage["duration_ms"]))
		patch.TotalTokens = int64(remoteIntValue(usage["total_tokens"]))
	}
	record, ok := run.subagents.finish(taskID, phase, patch.Summary, time.Now())
	if !ok {
		if record, ok = synthesizeTerminalSubagent(run, taskID, remoteString(event, "tool_use_id"), phase); ok {
			_ = writeJSON(agentAISubagentPayload(run.sessionID, record))
		}
		return
	}
	// Enrich the terminal record with the notification's summary/usage before
	// emitting: finish() already stored the summary; patch the rest via
	// progress()'s overlay (no-op on terminal records) is NOT possible, so
	// carry them on the emitted payload directly.
	if patch.ToolUses > 0 && record.ToolUses == 0 {
		record.ToolUses = patch.ToolUses
	}
	if patch.DurationMs > 0 && record.DurationMs == 0 {
		record.DurationMs = patch.DurationMs
	}
	if patch.TotalTokens > 0 && record.TotalTokens == 0 {
		record.TotalTokens = patch.TotalTokens
	}
	if patch.OutputFile != "" && record.OutputFile == "" {
		record.OutputFile = patch.OutputFile
	}
	run.activity.endSubagent()
	_ = writeJSON(agentAISubagentPayload(run.sessionID, record))
}

// synthesizeTerminalSubagent creates a terminal-only record for orphan
// completion evidence (task_updated/task_notification with no prior
// task_started). The record never feeds the watchdog (already terminal at
// creation) and renders on the phone as a completed row instead of vanishing.
func synthesizeTerminalSubagent(run agentAIRun, taskID, toolUseID, phase string) (agentAISubagent, bool) {
	if taskID == "" {
		return agentAISubagent{}, false
	}
	if toolUseID == "" {
		toolUseID = "task:" + taskID
	}
	now := time.Now()
	run.subagents.start(taskID, toolUseID, streamingMessageID(run), "", "", 1, "", false, now)
	record, ok := run.subagents.finish(taskID, phase, "", now)
	return record, ok
}

// finalizeRunSubagents flips every still-active registry entry to
// phase=interrupted and emits its terminal event. MUST run before the run's
// terminal payload (ai.done / ai.error / terminal ai.status) goes through the
// emitter: emit() is a terminal barrier — once the terminal event is written,
// every later emit is silently dropped, so an interrupted finalized after
// ai.done would never reach the server. finalize() is idempotent (only active
// records flip), so calling it on paths that may not have subagents at all is
// free.
func finalizeRunSubagents(run agentAIRun, writeJSON agentTerminalWriter) {
	for _, record := range run.subagents.finalize(time.Now()) {
		run.activity.endSubagent()
		_ = writeJSON(agentAISubagentPayload(run.sessionID, record))
	}
}

// subagentTailSummary extracts a display summary from the tail of a subagent
// transcript (last assistant text block), used by the TUI-path watcher when a
// sidechain file goes quiet. Best-effort: any read/parse failure returns "".
func subagentTailSummary(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	const tailCap = 16 * 1024
	info, err := f.Stat()
	if err != nil {
		return ""
	}
	offset := int64(0)
	if size := info.Size(); size > tailCap {
		offset = size - tailCap
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return ""
	}
	buf, err := io.ReadAll(f)
	if err != nil {
		return ""
	}
	summary := ""
	for _, line := range strings.Split(string(buf), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.HasPrefix(line, "{") {
			continue
		}
		var event map[string]interface{}
		if json.Unmarshal([]byte(line), &event) != nil {
			continue
		}
		if remoteString(event, "type") != "assistant" || event["isSidechain"] != true {
			continue
		}
		message := mapIf(event["message"])
		if message == nil {
			continue
		}
		if text := claudeMessageContentText(message["content"]); strings.TrimSpace(text) != "" {
			summary = text
		}
	}
	return strings.TrimSpace(truncateForCloud(summary))
}

// tuiSubagentWatcher 让 TUI 注入回合感知子 agent：CC 把 sidechain 转录写在
// <会话jsonl 同目录>/subagents/agent-<agentId>.jsonl（+ .meta.json，增量追加，
// 实测可 tail）。每轮 200ms poll 扫一次：新 meta.json→started，文件增长→
// progress（顺带喂看门狗），连续 2s 无增长→completed（摘要取 sidechain 末条
// assistant 文本）。inventory/digest 的 subagents/ 跳过逻辑与此无关，保持不动。
type tuiSubagentWatcher struct {
	dir   string
	known map[string]*tuiSubagentFile
}

type tuiSubagentFile struct {
	jsonlPath string
	size      int64
	quiet     int
	done      bool
}

// tuiSubagentQuietRounds = 完成判定的安静轮数。10 × 200ms = 2s 无增长。
const tuiSubagentQuietRounds = 10

func newTUISubagentWatcher(jsonlPath string) *tuiSubagentWatcher {
	return &tuiSubagentWatcher{
		dir:   filepath.Join(filepath.Dir(jsonlPath), "subagents"),
		known: map[string]*tuiSubagentFile{},
	}
}

func (w *tuiSubagentWatcher) poll(registry *agentAISubagentRegistry, activity *agentAIActivity, sessionID, messageID string, write agentTerminalWriter) {
	if w == nil || registry == nil {
		return
	}
	entries, err := os.ReadDir(w.dir)
	if err != nil {
		return // 目录不存在 = 本回合没有子 agent，常态
	}
	now := time.Now()
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, "agent-") || !strings.HasSuffix(name, ".meta.json") {
			continue
		}
		agentID := strings.TrimSuffix(strings.TrimPrefix(name, "agent-"), ".meta.json")
		if agentID == "" {
			continue
		}
		state, known := w.known[agentID]
		if known && state.done {
			continue
		}
		jsonlPath := filepath.Join(w.dir, "agent-"+agentID+".jsonl")
		if !known {
			meta := mapIf(readTUIJSONFile(filepath.Join(w.dir, name)))
			size := int64(0)
			if info, err := os.Stat(jsonlPath); err == nil {
				size = info.Size()
			}
			state = &tuiSubagentFile{jsonlPath: jsonlPath, size: size}
			w.known[agentID] = state
			// task_id == agentId、meta.toolUseId == 父 Agent tool_use_id（双侧实证）。
			if record, ok := registry.start(
				agentID, firstNonEmpty(remoteString(meta, "toolUseId"), agentID), messageID,
				remoteString(meta, "description"), remoteString(meta, "agentType"),
				tuiMetaSpawnDepth(meta), "local_agent", false, now,
			); ok {
				activity.beginSubagent()
				_ = write(agentAISubagentPayload(sessionID, record))
			}
			continue
		}
		info, err := os.Stat(state.jsonlPath)
		if err != nil {
			continue
		}
		if info.Size() > state.size {
			state.size = info.Size()
			state.quiet = 0
			if record, ok := registry.progress(agentID, now, agentAISubagentProgress{CurrentStep: "运行中"}); ok {
				activity.subagentProgress()
				_ = write(agentAISubagentPayload(sessionID, record))
			}
			continue
		}
		state.quiet++
		if state.quiet < tuiSubagentQuietRounds {
			continue
		}
		if record, ok := registry.finish(agentID, "completed", subagentTailSummary(state.jsonlPath), now); ok {
			activity.endSubagent()
			_ = write(agentAISubagentPayload(sessionID, record))
		}
		state.done = true
	}
}

func readTUIJSONFile(path string) map[string]interface{} {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out map[string]interface{}
	if json.Unmarshal(raw, &out) != nil {
		return nil
	}
	return out
}

func tuiMetaSpawnDepth(meta map[string]interface{}) int {
	if meta == nil {
		return 1
	}
	if n, ok := meta["spawnDepth"].(float64); ok && n >= 1 {
		return int(n)
	}
	return 1
}
