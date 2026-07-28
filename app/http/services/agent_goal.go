package services

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"aliang.one/nursorgate/app/http/models"
	"github.com/google/shlex"
)

const (
	goalPlannerTimeout       = 2 * time.Minute
	goalEvidenceOutputLimit  = 16 * 1024
	// goalPlanOutputLimit caps how much of the provider's planning stream we
	// capture (to then extract the trailing ALIANG_GOAL_PLAN marker). Deep-effort
	// turns (e.g. glm-5.2[1m] @ xhigh) can emit 500KB-1MB+ of reasoning before the
	// marker; the old 256KB cap truncated the marker off the end -> "did not
	// converge" -> server re-dispatched -> goal stuck in planning. 2MB leaves
	// headroom for thorough planning while bounding memory.
	goalPlanOutputLimit       = 2 * 1024 * 1024
	goalFingerprintFileLimit  = 50_000
	goalFingerprintByteLimit  = 256 * 1024 * 1024
	goalReportMarker          = "ALIANG_GOAL_REPORT:"
	goalPlanMarker            = "ALIANG_GOAL_PLAN:"
	goalReportOutputByteLimit = 16 * 1024
)

// goalTaskReportSystemPrompt 是注入到 Goal task 执行回合的强化指令（拼到
// Claude --append-system-prompt 末尾）。作用：强制 provider 无论成败都在回复末尾
// 输出 ALIANG_GOAL_REPORT 行，并给最小示例。glm-5.2 经代理时经常不输出该行 ->
// agent 解析不到 -> goal 无故失败；此 prompt 是第一道防线。
//
// 注意：这是 agent 端的最后一道保障；server 侧 task prompt 也应含类似指令，但
// 即使 server prompt 缺失/被裁剪，这里仍生效。
const goalTaskReportSystemPrompt = `

[Aliang Goal Task — MANDATORY final report]
You are executing ONE Goal task. Regardless of success OR failure, your response MUST end with exactly one line beginning "ALIANG_GOAL_REPORT:" followed by compact single-line JSON. If you omit this line the task is treated as FAILED. Emit the line even on error, even if you only did partial work, even if you were blocked.
Schema: {"schema_version":1,"outcome":"task_completed|failed|blocked|no_progress","summary":string,"blocker_code":string,"evidence_refs":[string],"completion_proposed":bool}
- outcome="task_completed" IFF the task fully succeeded -> completion_proposed MUST be true, blocker_code "".
- outcome="failed" when the task could not be completed (command error, missing tool, missing file, etc.) -> completion_proposed MUST be false; put the root cause in summary (e.g. "npm install failed: command not found").
- outcome="blocked" when you need a human decision; outcome="no_progress" only as a last resort.
Minimal example (copy the shape, fill your own fields):
ALIANG_GOAL_REPORT: {"schema_version":1,"outcome":"task_completed","summary":"installed deps and ran tests","blocker_code":"","evidence_refs":[],"completion_proposed":true}
Do NOT wrap the report line in a code fence. Do NOT put any text after the report line. The report line MUST be plain text starting exactly with "ALIANG_GOAL_REPORT:".`

var goalIdentityFields = []string{
	"goal_id",
	"goal_run_id",
	"plan_revision_id",
	"task_id",
	"dispatch_token",
	"goal_context_digest",
}

var goalAllowedCheckCommands = map[string]struct{}{
	"git": {}, "npm": {}, "npx": {}, "pnpm": {}, "yarn": {}, "node": {},
	"tsc": {}, "vitest": {}, "jest": {}, "go": {}, "cargo": {}, "rustc": {},
	"python": {}, "python3": {}, "pytest": {}, "make": {}, "cmake": {},
	"gradle": {}, "mvn": {}, "dotnet": {}, "swift": {},
}

func goalRunIdentityFromMessage(msg map[string]interface{}) map[string]interface{} {
	identity := make(map[string]interface{}, len(goalIdentityFields))
	for _, key := range goalIdentityFields {
		value := strings.TrimSpace(remoteString(msg, key))
		if value == "" {
			return nil
		}
		identity[key] = value
	}
	return identity
}

func cloneGoalIdentity(identity map[string]interface{}) map[string]interface{} {
	if len(identity) == 0 {
		return nil
	}
	cloned := make(map[string]interface{}, len(identity))
	for key, value := range identity {
		cloned[key] = value
	}
	return cloned
}

func cloneAgentAIMap(value map[string]interface{}) map[string]interface{} {
	if len(value) == 0 {
		return nil
	}
	cloned := make(map[string]interface{}, len(value))
	for key, item := range value {
		cloned[key] = item
	}
	return cloned
}

func codexNativeGoalSnapshot(value interface{}) map[string]interface{} {
	row := mapIf(value)
	if row == nil {
		return nil
	}
	threadID := strings.TrimSpace(remoteString(row, "threadId"))
	status := strings.TrimSpace(remoteString(row, "status"))
	if threadID == "" || status == "" {
		return nil
	}
	snapshot := map[string]interface{}{
		"provider":          "codex",
		"thread_id":         threadID,
		"objective":         remoteString(row, "objective"),
		"status":            status,
		"tokens_used":       remoteInt(row, "tokensUsed", 0),
		"time_used_seconds": remoteInt(row, "timeUsedSeconds", 0),
		"created_at":        remoteInt(row, "createdAt", 0),
		"updated_at":        remoteInt(row, "updatedAt", 0),
		"sync_state":        "confirmed",
	}
	if budget, ok := row["tokenBudget"].(float64); ok && budget >= 0 {
		snapshot["token_budget"] = int(budget)
	}
	return snapshot
}

func codexNativeGoalStaleSnapshot(snapshot map[string]interface{}, syncErr string) map[string]interface{} {
	if len(snapshot) == 0 {
		return nil
	}
	stale := cloneAgentAIMap(snapshot)
	stale["sync_state"] = "stale"
	if syncErr = strings.TrimSpace(syncErr); syncErr != "" {
		stale["sync_error"] = syncErr
	}
	return stale
}

func codexNativeGoalTerminalStatus(output string) string {
	report, ok := parseMarkedJSONObject(output, goalReportMarker)
	if !ok || !validGoalReport(report) {
		return "blocked"
	}
	if remoteString(report, "outcome") == "task_completed" {
		return "complete"
	}
	return "blocked"
}

func (e *agentAIRunEmitter) appendGoalOutput(delta string) {
	if e == nil || len(e.goalIdentity) == 0 || delta == "" {
		return
	}
	remaining := agentAIMessageLimitBytes - e.goalOutput.Len()
	if remaining <= 0 {
		return
	}
	if len(delta) > remaining {
		delta = delta[:remaining]
	}
	e.goalOutput.WriteString(delta)
}

func (e *agentAIRunEmitter) captureGoalWorkspace(projectPath string) error {
	if e == nil || len(e.goalIdentity) == 0 {
		return nil
	}
	fingerprint, err := goalWorkspaceFingerprint(projectPath)
	if err != nil {
		return err
	}
	e.goalProjectPath = projectPath
	e.goalWorkspaceBefore = fingerprint
	return nil
}

// attachGoalReport 在 goal task 的 terminal 事件上附加结构化 goal_report。
// 三层策略，从最可信到最兜底：
//  1. 严格/容错解析 ALIANG_GOAL_REPORT —— provider 守格式时直接采用。
//  2. fallback 推断：provider 没输出 report 时（glm-5.2 经代理常见），从
//     claude 的累计输出（ai.delta 收集到 goalOutput）推断 outcome/summary，
//     不再直接报 goal_report_missing 让 goal 无故死掉。
//  3. 兜底：推断不出（如完全没输出）才报 goal_report_missing。
//
// 同时无论成败都填 `output`（截断 16KB）—— server schema 已支持，用于失败时
// 显示真实命令输出/stderr，闭环失败原因（之前的根因：goal 失败但 admin 看不到为何）。
func (e *agentAIRunEmitter) attachGoalReport(payload map[string]interface{}) {
	if e == nil || len(e.goalIdentity) == 0 || payload["goal_report"] != nil {
		return
	}
	if e.goalWorkspaceBefore != "" {
		payload["workspace_fingerprint_before"] = e.goalWorkspaceBefore
		if after, err := goalWorkspaceFingerprint(e.goalProjectPath); err == nil {
			payload["workspace_fingerprint_after"] = after
		} else {
			payload["workspace_fingerprint_error"] = err.Error()
		}
	}

	// 提取并填 output（无论成败，server schema 已支持，用于失败诊断）。
	capturedOutput := e.goalOutput.String()
	payload["output"] = truncateGoalReportOutput(capturedOutput)

	if report, ok := parseMarkedJSONObject(capturedOutput, goalReportMarker); ok && validGoalReport(report) {
		// provider 守格式：补全 output（若 report 自带 output 则保留其值）。
		if _, hasOutput := report["output"]; !hasOutput {
			report["output"] = truncateGoalReportOutput(stripGoalReportMarkerFromOutput(capturedOutput))
		}
		payload["goal_report"] = report
		return
	}

	// provider 未守格式：从输出推断 outcome/summary（fallback）。
	report := inferGoalReportFromOutput(capturedOutput, remoteString(payload, "type"))
	payload["goal_report"] = report
}

// inferGoalReportFromOutput 在 provider 没输出 ALIANG_GOAL_REPORT 时推断一份。
// 推断规则（乐观优先，避免误杀成功的任务）：
//   - AIError 事件 → failed（provider 进程级失败）。
//   - 输出明显含错误信号（"error:"、"failed:"、"command not found"、"ENOENT"、
//     "No such file"）→ failed，summary 含错误片段。
//   - 输出非空且无明显错误信号 → task_completed（乐观：claude 干完活只是没补 report）。
//   - 输出为空 → no_progress + goal_report_missing（真兜底）。
func inferGoalReportFromOutput(capturedOutput, terminalEventType string) map[string]interface{} {
	trimmed := strings.TrimSpace(capturedOutput)
	tail := trimmed
	if len(tail) > 600 {
		tail = tail[len(tail)-600:]
	}

	if terminalEventType == models.AgentEventAIError {
		return map[string]interface{}{
			"schema_version":      1,
			"outcome":             "failed",
			"summary":             "Goal task failed before a structured report was produced; provider output tail: " + clipSummary(tail),
			"blocker_code":        "provider_error",
			"evidence_refs":       []interface{}{},
			"completion_proposed": false,
		}
	}

	if trimmed == "" {
		return map[string]interface{}{
			"schema_version":      1,
			"outcome":             "no_progress",
			"summary":             "Provider did not return a structured Goal report and produced no usable output.",
			"blocker_code":        "goal_report_missing",
			"evidence_refs":       []interface{}{},
			"completion_proposed": false,
		}
	}

	lower := strings.ToLower(trimmed)
	if errorSignal, hit := detectGoalErrorSignal(lower); hit {
		return map[string]interface{}{
			"schema_version":      1,
			"outcome":             "failed",
			"summary":             "Goal task appears to have failed (" + errorSignal + "); provider output tail: " + clipSummary(tail),
			"blocker_code":        "task_command_failed",
			"evidence_refs":       []interface{}{},
			"completion_proposed": false,
		}
	}

	// 乐观：有输出且无明显错误 → task_completed。这是关键修复 —— 之前直接
	// goal_report_missing 让很多成功的 task 失败。
	return map[string]interface{}{
		"schema_version":      1,
		"outcome":             "task_completed",
		"summary":             "Goal task completed; provider did not emit a structured report (inferred from output). Output tail: " + clipSummary(tail),
		"blocker_code":        "",
		"evidence_refs":       []interface{}{},
		"completion_proposed": true,
	}
}

// detectGoalErrorSignal 在（已小写化的）输出里寻找常见错误信号，命中则返回
// 信号描述 + true。覆盖：命令找不到、文件缺失、退出码、显式 error/failed 前缀。
func detectGoalErrorSignal(lowered string) (string, bool) {
	signals := []struct {
		label   string
		pattern string
	}{
		{"command not found", "command not found"},
		{"command not found", "not found in path"},
		{"file not found", "no such file or directory"},
		{"file not found", "enoent"},
		{"permission denied", "permission denied"},
		{"nonzero exit", "nonzero exit"},
		{"exit code", "exit code"},
		{"fatal", "fatal"},
		{"timeout", "timed out"},
	}
	for _, sig := range signals {
		if strings.Contains(lowered, sig.pattern) {
			return sig.label, true
		}
	}
	// "error:" / "failed:" 词边界检查（避免 hit "error" 变量名）。
	if hasGoalErrorPrefixLine(lowered, "error") || hasGoalErrorPrefixLine(lowered, "failed") {
		return "error/failed line", true
	}
	return "", false
}

// hasGoalErrorPrefixLine 检查输出里是否存在 "word:" 或 "word " 开头的行。
func hasGoalErrorPrefixLine(lowered, word string) bool {
	for _, line := range strings.Split(lowered, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, word+":") || strings.HasPrefix(line+" ", word+" ") {
			return true
		}
	}
	return false
}

// clipSummary 把 summary 文本约束在 ~500 字符（远小于 server schema 的 8000），
// 因为 tail 已截到 600 字符，这里再做防御，避免极端长行把 server 字段撑爆。
func clipSummary(value string) string {
	const limit = 500
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "…"
}

// truncateGoalReportOutput 截断 goal_report.output 字段到 16KB（尾部保留）。
// server schema 限制 output 16KB；尾部最有诊断价值（错误信息通常在末尾）。
func truncateGoalReportOutput(value string) string {
	if len(value) <= goalReportOutputByteLimit {
		return value
	}
	return value[len(value)-goalReportOutputByteLimit:]
}

// stripGoalReportMarkerFromOutput 从累计输出里删掉 ALIANG_GOAL_REPORT 行，
// 给 report.output 用（report 本身的 JSON 不该出现在 output 里）。
func stripGoalReportMarkerFromOutput(value string) string {
	idx := strings.Index(value, goalReportMarker)
	if idx < 0 {
		return value
	}
	// 删掉 marker 所在的整行（到下一个换行）。
	end := strings.IndexByte(value[idx:], '\n')
	if end < 0 {
		return strings.TrimSpace(value[:idx])
	}
	return strings.TrimSpace(value[:idx] + value[idx+end+1:])
}

func validGoalReport(report map[string]interface{}) bool {
	if remoteInt(report, "schema_version", 0) != 1 {
		return false
	}
	switch remoteString(report, "outcome") {
	case "task_completed", "blocked", "failed", "no_progress":
	default:
		return false
	}
	summary, ok := report["summary"].(string)
	if !ok || len(summary) > 8_000 {
		return false
	}
	if blocker, exists := report["blocker_code"]; exists && blocker != nil {
		value, valid := blocker.(string)
		if !valid || len(value) > 160 {
			return false
		}
	}
	evidence, ok := report["evidence_refs"].([]interface{})
	if !ok || len(evidence) > 100 {
		return false
	}
	for _, item := range evidence {
		value, valid := item.(string)
		if !valid || len(value) > 500 {
			return false
		}
	}
	completionProposed, ok := report["completion_proposed"].(bool)
	return ok && completionProposed == (remoteString(report, "outcome") == "task_completed")
}

func validGoalPlan(plan map[string]interface{}) bool {
	if remoteInt(plan, "schema_version", 0) != 1 || strings.TrimSpace(remoteString(plan, "objective")) == "" {
		return false
	}
	tasks, ok := plan["tasks"].([]interface{})
	if !ok || len(tasks) == 0 || len(tasks) > 100 {
		return false
	}
	for _, rawTask := range tasks {
		task, valid := rawTask.(map[string]interface{})
		if !valid || strings.TrimSpace(remoteString(task, "key")) == "" || strings.TrimSpace(remoteString(task, "title")) == "" {
			return false
		}
		roots, rootsOK := task["allowed_roots"].([]interface{})
		checks, checksOK := task["checks"].([]interface{})
		if !rootsOK || len(roots) == 0 || !checksOK || len(checks) == 0 {
			return false
		}
		for _, rawCheck := range checks {
			check, checkOK := rawCheck.(map[string]interface{})
			if !checkOK || strings.TrimSpace(remoteString(check, "key")) == "" {
				return false
			}
			if _, requiredOK := check["required"].(bool); !requiredOK {
				return false
			}
			switch remoteString(check, "type") {
			case "command":
				if strings.TrimSpace(remoteString(check, "command")) == "" {
					return false
				}
			case "file_exists", "file_contains":
				if strings.TrimSpace(remoteString(check, "path")) == "" {
					return false
				}
			default:
				return false
			}
		}
	}
	return true
}

// parseMarkedJSONObject 在 output 里寻找 "MARKER" 之后的 JSON 对象。
// 容错策略（关键：provider 经代理时可能把 report 放在中间、用 ```json 包裹、
// 或在 marker 后混入空白/换行/多余文本）：
//  1. 取 marker 的第一个出现（不是最后一个）—— provider 偶尔在 thinking 里
//     复述指令会导致末尾反而没有真正的 report，但第一处真实 report 通常有效。
//  2. 容忍 marker 与 JSON 之间的空白/换行/可选的 ```json fence 开头。
//  3. 用 json.Decoder 流式解析，允许 JSON 后有尾随空白（但非空白尾随 → 失败）。
//
// 注意：原来用 strings.LastIndex 严格匹配末行；改成 Index（首个）+ 容错后，
// 对 plan/report 两种 marker 都更鲁棒。valid* 仍把关 schema，所以误命中会被拒。
func parseMarkedJSONObject(output, marker string) (map[string]interface{}, bool) {
	// 1. 首选：marker 后紧跟完整 JSON 到结尾（最常见、最严格）。
	if parsed, ok := parseMarkedJSONObjectStrict(output, marker); ok {
		return parsed, true
	}
	// 2. 容错：全文搜首个 marker 出现，剥掉 ```json/``` 围栏与多余空白后再解。
	index := strings.Index(output, marker)
	if index < 0 {
		return nil, false
	}
	tail := output[index+len(marker):]
	tail = strings.TrimSpace(stripCodeFence(tail))
	if tail == "" {
		return nil, false
	}
	var parsed map[string]interface{}
	decoder := json.NewDecoder(strings.NewReader(tail))
	if decoder.Decode(&parsed) != nil {
		return nil, false
	}
	// 允许 JSON 后有尾随空白（但不允许有意义的额外 token —— 那多半是解析串了）。
	var trailing interface{}
	if err := decoder.Decode(&trailing); err != nil && err != io.EOF {
		return nil, false
	}
	return parsed, true
}

// parseMarkedJSONObjectStrict 是原始的"最后一个 marker + 严格无尾随"解析。
// 先试它，命中则最可信（provider 严格遵守指令的常见路径）。
func parseMarkedJSONObjectStrict(output, marker string) (map[string]interface{}, bool) {
	index := strings.LastIndex(output, marker)
	if index < 0 {
		return nil, false
	}
	raw := strings.TrimSpace(output[index+len(marker):])
	var parsed map[string]interface{}
	decoder := json.NewDecoder(strings.NewReader(raw))
	if raw == "" || decoder.Decode(&parsed) != nil {
		return nil, false
	}
	var trailing interface{}
	if decoder.Decode(&trailing) != io.EOF {
		return nil, false
	}
	return parsed, true
}

// stripCodeFence 去掉字符串开头的 ```json / ``` 围栏开头与结尾的 ```。
// 用于容错 provider 把 marker JSON 包在 code fence 里输出。
func stripCodeFence(value string) string {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, "```") {
		return value
	}
	// 去掉开头的 ```json 或 ``` 行。
	if nl := strings.IndexByte(value, '\n'); nl >= 0 {
		value = strings.TrimSpace(value[nl+1:])
	} else {
		value = strings.TrimSpace(strings.TrimPrefix(value, "```"))
	}
	value = strings.TrimSuffix(strings.TrimSpace(value), "```")
	return strings.TrimSpace(value)
}

func handleAgentGoalMessage(msg map[string]interface{}, writeJSON func(interface{}) error, manager *agentAIManager) {
	switch remoteString(msg, "type") {
	case models.AgentEventGoalPlan:
		handleAgentGoalPlan(msg, writeJSON, manager)
	case models.AgentEventGoalVerify:
		handleAgentGoalVerify(msg, writeJSON)
	}
}

func agentGoalErrorPayload(msg map[string]interface{}, err error) map[string]interface{} {
	typeName := models.AgentEventGoalPlanError
	if remoteString(msg, "type") == models.AgentEventGoalVerify {
		typeName = models.AgentEventGoalVerifyError
	}
	message := "Goal request failed"
	if err != nil {
		message = err.Error()
	}
	return map[string]interface{}{
		"type":       typeName,
		"request_id": remoteString(msg, "request_id"),
		"error":      message,
	}
}

const goalPlanMaxEmitAttempts = 3

func handleAgentGoalPlan(msg map[string]interface{}, writeJSON func(interface{}) error, manager *agentAIManager) {
	requestID := strings.TrimSpace(remoteString(msg, "request_id"))
	objective := strings.TrimSpace(remoteString(msg, "objective"))
	constraints := remoteStringSlice(msg, "constraints")
	nonGoals := remoteStringSlice(msg, "non_goals")
	if requestID == "" || objective == "" || manager == nil {
		_ = writeJSON(agentGoalErrorPayload(msg, errors.New("goal.plan missing request_id, objective, or AI runtime")))
		return
	}
	projectPath, err := resolveAgentProjectPath(remoteString(msg, "project_path"))
	if err != nil {
		_ = writeJSON(agentGoalErrorPayload(msg, err))
		return
	}
	before, err := goalWorkspaceFingerprint(projectPath)
	if err != nil {
		_ = writeJSON(agentGoalErrorPayload(msg, err))
		return
	}
	goalID := remoteString(msg, "goal_id")
	goalSessionID := "goal-plan:" + goalID
	provider := firstNonEmpty(remoteString(msg, "provider"), "auto")
	model := remoteString(msg, "model")
	effort := remoteString(msg, "effort")

	// Safety net: a goal must never be left dangling. A blocking provider run or
	// an unexpected panic still yields a typed error so the server settles it.
	defer func() {
		if r := recover(); r != nil {
			_ = writeJSON(agentGoalErrorPayload(msg, fmt.Errorf("goal planner crashed: %v", r)))
		}
	}()

	// Phase A — read-only exploration on the primary goal-plan session.
	emitGoalPlanPhase(writeJSON, requestID, "exploring", 0)
	if plan, ok := extractGoalPlan(runGoalPlanCLIPass(manager, goalSessionID, requestID, projectPath, provider, model, effort, goalPlanExplorePrompt(objective, constraints, nonGoals, projectPath), false, "", makeGoalPlanThinkingEmitter(writeJSON, requestID, "exploring", 0))); ok {
		normalizeGoalPlanPaths(plan, projectPath)
		finalizeGoalPlan(writeJSON, msg, requestID, before, projectPath, plan)
		return
	}
	nativeSID := manager.resumeSessionIDFor(goalSessionID)

	// Phase B — forced emission. Each attempt runs on its OWN manager session
	// (goal-plan-emit-<attempt>:<id>) so runCLIPass is never called twice on the
	// same session (that blocked the earlier implementation). We pass the
	// exploration's native claude session id so the emission turn resumes it.
	emitPrompt := goalPlanEmitPrompt(objective, constraints, nonGoals, projectPath)
	for attempt := 1; attempt <= goalPlanMaxEmitAttempts; attempt++ {
		emitGoalPlanPhase(writeJSON, requestID, "emitting", attempt)
		emitSession := fmt.Sprintf("goal-plan-emit-%d:%s", attempt, goalID)
		if plan, ok := extractGoalPlan(runGoalPlanCLIPass(manager, emitSession, requestID, projectPath, provider, model, effort, emitPrompt, true, nativeSID, makeGoalPlanThinkingEmitter(writeJSON, requestID, "emitting", attempt))); ok {
			normalizeGoalPlanPaths(plan, projectPath)
			finalizeGoalPlan(writeJSON, msg, requestID, before, projectPath, plan)
			return
		}
	}

	// Convergence guarantee: typed failure instead of a vague planner_failed.
	_ = writeJSON(agentGoalErrorPayload(msg, errors.New("planner_did_not_converge: provider did not emit valid ALIANG_GOAL_PLAN after exploration and bounded emission attempts")))
}

// runGoalPlanCLIPass runs one goal-planning CLI turn: read-only exploration when
// emission is false, tool-locked emission when true. It returns the captured
// provider text output. Emission turns resume the exploration session when the
// native session id was pinned on the goal session.
func runGoalPlanCLIPass(manager *agentAIManager, sessionID, messageID, projectPath, provider, model, effort, prompt string, emission bool, resumeSID string, emitThinking func(totalChars int, preview string)) string {
	allowResume := emission && resumeSID != ""
	var captureMu sync.Mutex
	var output strings.Builder
	var lastEmit time.Time
	var emittedChars int
	const goalPlanThinkingThrottle = 1200 * time.Millisecond
	const goalPlanThinkingPreview = 600
	capture := func(value interface{}) error {
		payload, ok := value.(map[string]interface{})
		if !ok {
			return nil
		}
		captureMu.Lock()
		defer captureMu.Unlock()
		if remoteString(payload, "type") == models.AgentEventAIDelta && output.Len() < goalPlanOutputLimit {
			delta := remoteString(payload, "delta")
			remaining := goalPlanOutputLimit - output.Len()
			if len(delta) > remaining {
				delta = delta[:remaining]
			}
			output.WriteString(delta)
		}
		// Throttled live-thinking push so the phone sees the planner reasoning
		// (turns "stuck" into "visibly working"). One snapshot at most every
		// ~1.2s, only when fresh content arrived since the last push.
		if emitThinking != nil {
			now := time.Now()
			total := output.Len()
			if total > emittedChars && (lastEmit.IsZero() || now.Sub(lastEmit) >= goalPlanThinkingThrottle) {
				preview := output.String()
				if len(preview) > goalPlanThinkingPreview {
					preview = preview[len(preview)-goalPlanThinkingPreview:]
				}
				emitThinking(total, preview)
				lastEmit = now
				emittedChars = total
			}
		}
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), goalPlannerTimeout)
	defer cancel()
	manager.runCLIPass(ctx, agentAIRun{
		sessionID:       sessionID,
		messageID:       messageID,
		mode:            "agent",
		projectPath:     projectPath,
		provider:        provider,
		model:           model,
		effort:          effort,
		prompt:          prompt,
		freshPrompt:     prompt,
		activity:        newAgentAIActivity(),
		readOnly:        true,
		emissionOnly:    emission,
		resumeSessionID: resumeSID,
	}, capture, allowResume)
	// Final flush so the UI sees the last chunk written after the throttle window.
	captureMu.Lock()
	if emitThinking != nil && output.Len() > emittedChars {
		preview := output.String()
		if len(preview) > goalPlanThinkingPreview {
			preview = preview[len(preview)-goalPlanThinkingPreview:]
		}
		emitThinking(output.Len(), preview)
	}
	out := output.String()
	captureMu.Unlock()
	return out
}

func extractGoalPlan(output string) (map[string]interface{}, bool) {
	proposal, ok := parseMarkedJSONObject(output, goalPlanMarker)
	if !ok || !validGoalPlan(proposal) {
		return nil, false
	}
	return proposal, true
}

// goalAllowedCommandNames mirrors the server's DEFAULT_ALLOWED_COMMANDS
// (build/test/vcs command families). Providers often propose read-only shell
// utilities (ls/cat/grep) as task allowed_commands; the server rejects any
// command whose first token is not in this set (task_command_out_of_scope), so
// we filter those out before sending. Keep in sync with the server list.
var goalAllowedCommandNames = map[string]bool{
	"git": true, "npm": true, "npx": true, "pnpm": true, "yarn": true, "node": true,
	"tsc": true, "vitest": true, "jest": true, "go": true, "cargo": true, "rustc": true,
	"python": true, "python3": true, "pytest": true, "make": true, "cmake": true,
	"gradle": true, "mvn": true, "dotnet": true, "swift": true,
}

func goalCommandName(cmd string) string {
	cmd = strings.TrimSpace(cmd)
	if i := strings.IndexByte(cmd, ' '); i >= 0 {
		return cmd[:i]
	}
	return cmd
}

// normalizeGoalPlanPaths (misnomer retained for call-site stability) is the
// agent-side goal plan sanitizer: it rewrites the provider's raw plan into one
// the server's strict manifest compiler will accept, so an unreliable model
// (glm-5.2) does not fail the whole goal on scope nits. It:
//   - filters task allowed_commands to the server's allowed command families;
//   - drops check commands whose first token is not in the allowlist;
//   - resolves relative check paths against the project root;
//   - drops check paths that are absolute but OUTSIDE the project root (models
//     sometimes point checks at their own ~/.claude cache or other system dirs);
//   - drops any task that loses all its checks (server requires >=1 per task).
// A plan with zero surviving tasks is left empty, which extractGoalPlan then
// rejects (-> did_not_converge) rather than sending an empty proposal.
// Mutates the proposal in place.
func normalizeGoalPlanPaths(proposal map[string]interface{}, projectRoot string) {
	root := strings.TrimRight(strings.TrimSpace(projectRoot), "/")
	rootSlash := root + "/"
	insideRoot := func(p string) bool {
		if p == "" || !strings.HasPrefix(p, "/") || root == "" {
			return false
		}
		return p == root || strings.HasPrefix(p, rootSlash)
	}
	tasks, _ := proposal["tasks"].([]interface{})
	cleanedTasks := make([]interface{}, 0, len(tasks))
	for _, rawTask := range tasks {
		task, _ := rawTask.(map[string]interface{})
		if task == nil {
			continue
		}
		if rawCmds, ok := task["allowed_commands"].([]interface{}); ok {
			kept := make([]interface{}, 0, len(rawCmds))
			for _, raw := range rawCmds {
				cmd, _ := raw.(string)
				cmd = strings.TrimSpace(cmd)
				if cmd == "" || !goalAllowedCommandNames[goalCommandName(cmd)] {
					continue
				}
				kept = append(kept, cmd)
			}
			task["allowed_commands"] = kept
		}
		if rawChecks, ok := task["checks"].([]interface{}); ok {
			keptChecks := make([]interface{}, 0, len(rawChecks))
			for _, rawCheck := range rawChecks {
				check, _ := rawCheck.(map[string]interface{})
				if check == nil {
					continue
				}
				switch strings.TrimSpace(remoteString(check, "type")) {
				case "command":
					cmd := strings.TrimSpace(remoteString(check, "command"))
					if cmd == "" || !goalAllowedCommandNames[goalCommandName(cmd)] {
						continue // drop out-of-scope / empty check command
					}
				case "file_exists", "file_contains":
					path := strings.TrimSpace(remoteString(check, "path"))
					if path == "" {
						continue
					}
					if !strings.HasPrefix(path, "/") && root != "" {
						path = root + "/" + path // resolve relative → absolute
						check["path"] = path
					}
					if !insideRoot(path) {
						continue // drop absolute path outside project root
					}
				default:
					// keep unknown check types; server validates them
				}
				keptChecks = append(keptChecks, check)
			}
			task["checks"] = keptChecks
		}
		checks, _ := task["checks"].([]interface{})
		if len(checks) == 0 {
			continue // drop task with no surviving checks
		}
		cleanedTasks = append(cleanedTasks, task)
	}
	proposal["tasks"] = cleanedTasks
}

func finalizeGoalPlan(writeJSON func(interface{}) error, msg map[string]interface{}, requestID, before, projectPath string, proposal map[string]interface{}) {
	after, err := goalWorkspaceFingerprint(projectPath)
	if err != nil {
		_ = writeJSON(agentGoalErrorPayload(msg, err))
		return
	}
	_ = writeJSON(map[string]interface{}{
		"type":                         models.AgentEventGoalPlanResult,
		"request_id":                   requestID,
		"workspace_fingerprint_before": before,
		"workspace_fingerprint_after":  after,
		"provider_run_id":              requestID,
		"proposal":                     proposal,
	})
}

// emitGoalPlanPhase surfaces the current planning phase (exploring / emitting)
// so the phone can show whether the provider is still exploring or producing the
// plan. It is a progress hint; it does not mutate goal state.
func emitGoalPlanPhase(writeJSON func(interface{}) error, requestID, phase string, attempt int) {
	payload := map[string]interface{}{
		"type":            models.AgentEventAIRunProgress,
		"message_id":      requestID,
		"goal_plan_phase": phase,
	}
	if attempt > 0 {
		payload["goal_plan_attempt"] = attempt
	}
	_ = writeJSON(payload)
}

// makeGoalPlanThinkingEmitter returns a sink that pushes a throttled snapshot
// of the planner's live reasoning to the server (same ai.run.progress channel
// as the phase events) so the phone can render "exploring/emitting · attempt N
// · thinking…" instead of a silent "planning…". totalChars is the cumulative
// captured output length (a progress proxy); preview is the trailing slice.
func makeGoalPlanThinkingEmitter(writeJSON func(interface{}) error, requestID, phase string, attempt int) func(totalChars int, preview string) {
	return func(totalChars int, preview string) {
		payload := map[string]interface{}{
			"type":                   models.AgentEventAIRunProgress,
			"message_id":             requestID,
			"goal_plan_phase":        phase,
			"goal_plan_thinking_chars": totalChars,
			"goal_plan_thinking_preview": preview,
		}
		if attempt > 0 {
			payload["goal_plan_attempt"] = attempt
		}
		_ = writeJSON(payload)
	}
}

func goalPlanExplorePrompt(objective string, constraints, nonGoals []string, projectPath string) string {
	return fmt.Sprintf(`You are planning one software-development Goal in read-only mode.
Objective: %s
User constraints (authoritative): %s
Non-goals (authoritative): %s
Workspace root: %s
Inspect the workspace as needed to ground a concrete plan — read the key files relevant to the objective. Do not edit files, install dependencies, or run mutating commands.
When you have enough context, produce a small dependency-aware plan and emit the ALIANG_GOAL_PLAN line. Every task must stay under the workspace root. Checks must be deterministic command, file_exists, or file_contains checks. HARD CONSTRAINTS (the server rejects any plan that violates these, so follow exactly):
- Commands (both task allowed_commands AND check commands): the FIRST word MUST be one of exactly these executables: git, npm, npx, pnpm, yarn, node, tsc, vitest, jest, go, cargo, rustc, python, python3, pytest, make, cmake, gradle, mvn, dotnet, swift. Do NOT propose any other executable — never ls, cat, grep, find, sed, awk, curl, wget, sh, bash, docker, or similar.
- file_exists/file_contains check paths MUST be absolute paths INSIDE the workspace root shown above (e.g. %s/src/foo.ts). NEVER relative paths (src/foo.ts) and NEVER paths outside the workspace root — no ~/.claude/, /etc/, /tmp/, or any path that does not begin with the workspace root.
- Command checks must be verification-only (test/lint/typecheck/build). Never install, add, remove, publish, deploy, push, commit, checkout, reset, clean, or shell-composed commands. npx checks must use --no-install.
End with exactly one line beginning %s followed by compact single-line JSON shaped as:
{"schema_version":1,"objective":string,"constraints":[string],"non_goals":[string],"tasks":[{"key":string,"title":string,"description":string,"depends_on":[string],"allowed_roots":[%q],"allowed_commands":[string],"checks":[{"key":string,"type":"command|file_exists|file_contains","command":string,"path":string,"contains":string,"required":true,"timeout_ms":number}],"retry_safety":"safe|idempotent_with_key|unsafe","idempotency_key_template":"required when retry_safety is idempotent_with_key"}],"budget":{"max_attempts_per_task":number,"max_turns":number,"command_timeout_ms":number}}`, objective, goalPromptList(constraints), goalPromptList(nonGoals), projectPath, projectPath, goalPlanMarker, projectPath)
}

// goalPlanEmitPrompt is the forced-emission instruction used in Phase B. The
// provider has no tools available there, so this text is the only thing it can
// act on; it must emit the ALIANG_GOAL_PLAN line.
func goalPlanEmitPrompt(objective string, constraints, nonGoals []string, projectPath string) string {
	return fmt.Sprintf(`You have finished exploring the workspace. Do NOT call any tool and do NOT explore further.
Your only output must be a single line beginning %s followed by compact single-line JSON shaped as:
{"schema_version":1,"objective":string,"constraints":[string],"non_goals":[string],"tasks":[{"key":string,"title":string,"description":string,"depends_on":[string],"allowed_roots":[%q],"allowed_commands":[string],"checks":[{"key":string,"type":"command|file_exists|file_contains","command":string,"path":string,"contains":string,"required":true,"timeout_ms":number}],"retry_safety":"safe|idempotent_with_key|unsafe","idempotency_key_template":"required when retry_safety is idempotent_with_key"}],"budget":{"max_attempts_per_task":number,"max_turns":number,"command_timeout_ms":number}}
Objective: %s
User constraints (authoritative): %s
Non-goals (authoritative): %s
Workspace root: %s
Hard constraints (server rejects violations): commands (allowed_commands AND check commands) first word MUST be one of git, npm, npx, pnpm, yarn, node, tsc, vitest, jest, go, cargo, rustc, python, python3, pytest, make, cmake, gradle, mvn, dotnet, swift only — no ls/cat/grep/find/curl/sh/etc. file_exists/file_contains paths MUST be absolute paths INSIDE the workspace root above — never relative (src/foo.ts), never outside it (~/.claude/, /etc/, /tmp/, or any path not beginning with that root).
Emit the plan now. No prose before or after the %s line.`, goalPlanMarker, projectPath, objective, goalPromptList(constraints), goalPromptList(nonGoals), projectPath, goalPlanMarker)
}

// resumeSessionIDFor returns the native provider session id pinned on the goal
// session after the exploration turn, so the emission turn can resume it.
func (m *agentAIManager) resumeSessionIDFor(sessionID string) string {
	if m == nil {
		return ""
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	session := m.sessions[sessionID]
	if session == nil {
		return ""
	}
	return strings.TrimSpace(session.resumeSessionID)
}

func withGoalPlanningReadOnly(tool *agentAITool) *agentAITool {
	if tool == nil || len(tool.args) == 0 {
		return tool
	}
	copied := *tool
	copied.args = append([]string(nil), tool.args...)
	copied.env = append([]string(nil), tool.env...)
	promptIndex := len(copied.args) - 1
	flags := []string{}
	switch copied.id {
	case "codex":
		// Keep CODEX_HOME/config.toml loaded: Aliang uses it for the selected
		// model provider and gateway base URL. Isolate planning from rules and
		// persisted sessions without disabling the provider configuration.
		flags = []string{"--sandbox", "read-only", "--ignore-rules", "--ephemeral"}
	case "claude", "claudecode":
		flags = []string{
			"--permission-mode", "plan",
			"--disallowedTools", "Bash,Edit,Write,NotebookEdit",
			"--strict-mcp-config", "--mcp-config", claudeCodeHeadlessEmptyMCP,
			"--",
		}
	case "opencode":
		flags = []string{"--pure", "--agent", "plan"}
		copied.env = append(copied.env,
			`OPENCODE_CONFIG_CONTENT={"permission":{"edit":"deny","bash":"deny","task":"deny","external_directory":"deny"}}`,
		)
	default:
		return nil
	}
	copied.args = append(copied.args[:promptIndex], append(flags, copied.args[promptIndex])...)
	return &copied
}

// withGoalEmissionOnly forces a follow-up planning turn in which the provider
// cannot use any tool, so its only possible output is the ALIANG_GOAL_PLAN text.
// Used by the goal planner convergence loop after the read-only exploration.
// --tools "" + a broad --disallowedTools (subagent/exploration/mutation tools)
// + "--" so no variadic flag eats the prompt.
func withGoalEmissionOnly(tool *agentAITool) *agentAITool {
	if tool == nil || len(tool.args) == 0 {
		return tool
	}
	if tool.id != "claude" && tool.id != "claudecode" {
		return nil
	}
	copied := *tool
	args := append([]string(nil), tool.args...)
	args = withoutCLIArgumentValue(args, "--disallowedTools")
	args = withoutCLIArgumentValue(args, "--allowedTools")
	args = withoutCLIArgumentValue(args, "--tools")
	promptIndex := len(args) - 1
	flags := []string{
		"--permission-mode", "plan",
		"--tools", "",
		"--disallowedTools", "Agent,Task,Bash,Edit,Write,NotebookEdit,Read,Read_file,Grep,Glob,Ls,WebSearch,WebFetch,TodoWrite",
		"--strict-mcp-config", "--mcp-config", claudeCodeHeadlessEmptyMCP,
		"--",
	}
	copied.args = append(args[:promptIndex], append(flags, args[promptIndex])...)
	return &copied
}

func withGoalExecutionPolicy(tool *agentAITool) *agentAITool {
	if tool == nil || tool.id != "opencode" || len(tool.args) == 0 {
		return tool
	}
	copied := *tool
	copied.args = append([]string(nil), tool.args...)
	copied.env = append([]string(nil), tool.env...)
	promptIndex := len(copied.args) - 1
	copied.args = append(copied.args[:promptIndex], append([]string{"--pure", "--auto"}, copied.args[promptIndex])...)
	copied.env = append(copied.env,
		`OPENCODE_CONFIG_CONTENT={"share":"disabled","permission":{"external_directory":"deny","task":"deny"}}`,
	)
	return &copied
}

// withGoalTaskReportSystemPrompt 把 goal report 强制段拼到 Claude 路径的
// --append-system-prompt 末尾。非 Claude 路径（codex/opencode）原样返回（它们
// 不通过该 flag 注入；codex 走 native goal，opencode 当前不走 goal task）。
//
// 为什么放 agent 端：server 下发的 task prompt 也可能含类似指令，但 server
// prompt 在 provider 侧可能被裁剪/覆盖（claude --append-system-prompt 是显式
// 顶层指令，优先级高于用户消息里的相同指令），agent 端再补一道最稳。
func withGoalTaskReportSystemPrompt(tool *agentAITool) *agentAITool {
	if tool == nil || len(tool.args) == 0 {
		return tool
	}
	if tool.id != "claude" && tool.id != "claudecode" {
		return tool
	}
	copied := *tool
	copied.args = append([]string(nil), tool.args...)
	for i := 0; i+1 < len(copied.args); i++ {
		if copied.args[i] == "--append-system-prompt" {
			original := copied.args[i+1]
			copied.args[i+1] = original + goalTaskReportSystemPrompt
			break
		}
	}
	return &copied
}

func goalPromptList(values []string) string {
	raw, err := json.Marshal(values)
	if err != nil {
		return "[]"
	}
	return string(raw)
}

func handleAgentGoalVerify(msg map[string]interface{}, writeJSON func(interface{}) error) {
	requestID := strings.TrimSpace(remoteString(msg, "request_id"))
	batchID := strings.TrimSpace(remoteString(msg, "verification_batch_id"))
	if requestID == "" || batchID == "" {
		_ = writeJSON(agentGoalErrorPayload(msg, errors.New("goal.verify missing request_id or verification_batch_id")))
		return
	}
	projectPath, err := resolveAgentProjectPath(remoteString(msg, "project_path"))
	if err != nil {
		_ = writeJSON(agentGoalErrorPayload(msg, err))
		return
	}
	before, err := goalWorkspaceFingerprint(projectPath)
	if err != nil {
		_ = writeJSON(agentGoalErrorPayload(msg, err))
		return
	}
	rawChecks, ok := msg["checks"].([]interface{})
	if !ok || len(rawChecks) == 0 || len(rawChecks) > 100 {
		_ = writeJSON(agentGoalErrorPayload(msg, errors.New("goal.verify checks are missing or out of bounds")))
		return
	}
	results := make([]map[string]interface{}, 0, len(rawChecks))
	for _, raw := range rawChecks {
		definition, ok := raw.(map[string]interface{})
		if !ok {
			_ = writeJSON(agentGoalErrorPayload(msg, errors.New("goal.verify check definition is invalid")))
			return
		}
		results = append(results, executeGoalCheck(projectPath, definition, before))
	}
	after, err := goalWorkspaceFingerprint(projectPath)
	if err != nil {
		_ = writeJSON(agentGoalErrorPayload(msg, err))
		return
	}
	for _, result := range results {
		result["workspace_fingerprint_after"] = after
	}
	_ = writeJSON(map[string]interface{}{
		"type":                         models.AgentEventGoalVerifyResult,
		"request_id":                   requestID,
		"batch_id":                     batchID,
		"workspace_fingerprint_before": before,
		"workspace_fingerprint_after":  after,
		"results":                      results,
	})
}

func goalSafeCheckScript(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	for _, prefix := range []string{"test", "lint", "check", "typecheck", "verify", "build"} {
		if value == prefix || strings.HasPrefix(value, prefix+":") || strings.HasPrefix(value, prefix+".") || strings.HasPrefix(value, prefix+"-") || strings.HasPrefix(value, prefix+"_") {
			return true
		}
	}
	return false
}

func goalSafeCheckTool(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "tsc", "vitest", "jest", "pytest", "eslint":
		return true
	default:
		return false
	}
}

func goalCheckCommandSafe(argv []string) bool {
	if len(argv) == 0 {
		return false
	}
	executable := strings.ToLower(argv[0])
	plain := make([]string, 0, len(argv)-1)
	for _, value := range argv[1:] {
		if !strings.HasPrefix(value, "-") {
			plain = append(plain, value)
		}
	}
	first := ""
	if len(plain) > 0 {
		first = strings.ToLower(plain[0])
	}
	switch executable {
	case "git":
		for _, allowed := range []string{"status", "diff", "rev-parse", "log", "show", "ls-files", "grep", "check-ignore"} {
			if first == allowed {
				return true
			}
		}
	case "npm", "pnpm", "yarn":
		if first == "test" {
			return true
		}
		if first == "run" && len(plain) > 1 {
			return goalSafeCheckScript(plain[1])
		}
		if first == "exec" && len(plain) > 1 {
			return goalSafeCheckTool(plain[1])
		}
	case "npx":
		hasNoInstall := false
		for _, value := range argv[1:] {
			if value == "--no-install" {
				hasNoInstall = true
			}
		}
		return hasNoInstall && len(plain) > 0 && goalSafeCheckTool(plain[0])
	case "tsc", "vitest", "jest", "pytest":
		return true
	case "node":
		return len(argv) > 1 && argv[1] == "--check"
	case "python", "python3":
		return len(argv) > 2 && argv[1] == "-m" && (argv[2] == "pytest" || argv[2] == "compileall")
	case "go":
		return first == "test" || first == "vet"
	case "cargo":
		return first == "test" || first == "check" || first == "clippy"
	case "make", "gradle":
		if len(plain) == 0 {
			return false
		}
		for _, value := range plain {
			if !goalSafeCheckScript(value) {
				return false
			}
		}
		return true
	case "mvn":
		if len(plain) == 0 {
			return false
		}
		for _, value := range plain {
			if value != "test" && value != "verify" && value != "package" {
				return false
			}
		}
		return true
	case "dotnet", "swift":
		return first == "test" || first == "build"
	}
	return false
}

func executeGoalCheck(projectPath string, definition map[string]interface{}, fingerprint string) map[string]interface{} {
	started := time.Now().UTC()
	status := "error"
	output := ""
	var exitCode interface{}
	checkType := remoteString(definition, "type")
	switch checkType {
	case "command":
		command := strings.TrimSpace(remoteString(definition, "command"))
		argv, parseErr := shlex.Split(command)
		if parseErr != nil || len(argv) == 0 {
			output = "invalid check command"
			break
		}
		if filepath.Base(argv[0]) != argv[0] {
			output = "check executable must be resolved from PATH"
			break
		}
		commandName := strings.ToLower(filepath.Base(argv[0]))
		commandName = strings.TrimSuffix(commandName, ".exe")
		commandName = strings.TrimSuffix(commandName, ".cmd")
		if _, allowed := goalAllowedCheckCommands[commandName]; !allowed {
			output = "check command is not allowed"
			break
		}
		argv[0] = commandName
		if !goalCheckCommandSafe(argv) {
			output = "check command is not verification-safe"
			break
		}
		timeout := time.Duration(remoteInt(definition, "timeout_ms", 30_000)) * time.Millisecond
		if timeout < time.Second {
			timeout = time.Second
		}
		if timeout > 15*time.Minute {
			timeout = 15 * time.Minute
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
		cmd.Dir = projectPath
		cmd.Env = agentChildProcessEnv()
		combined, err := cmd.CombinedOutput()
		cancel()
		output = truncateGoalEvidence(string(combined))
		if ctx.Err() != nil {
			status = "error"
			output = truncateGoalEvidence(output + "\ncheck timed out")
		} else if err == nil {
			status = "passed"
			exitCode = 0
		} else if exitError, ok := err.(*exec.ExitError); ok {
			status = "failed"
			exitCode = exitError.ExitCode()
		} else {
			status = "error"
			output = truncateGoalEvidence(output + "\n" + err.Error())
		}
	case "file_exists", "file_contains":
		path, err := resolveGoalCheckPath(projectPath, remoteString(definition, "path"))
		if err != nil {
			output = err.Error()
			break
		}
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			if os.IsNotExist(readErr) {
				status = "failed"
				output = "file does not exist"
			} else {
				output = readErr.Error()
			}
			break
		}
		if checkType == "file_exists" {
			status = "passed"
			output = "file exists"
		} else if strings.Contains(string(content), remoteString(definition, "contains")) {
			status = "passed"
			output = "file contains expected text"
		} else {
			status = "failed"
			output = "file does not contain expected text"
		}
	default:
		output = "unsupported check type"
	}
	result := map[string]interface{}{
		"check_id":                     remoteString(definition, "check_id"),
		"checker":                      "aliang-agent",
		"checker_version":              1,
		"definition_digest":            remoteString(definition, "definition_digest"),
		"status":                       status,
		"output":                       truncateGoalEvidence(output),
		"started_at":                   started.Format(time.RFC3339Nano),
		"completed_at":                 time.Now().UTC().Format(time.RFC3339Nano),
		"workspace_fingerprint_before": fingerprint,
		"workspace_fingerprint_after":  fingerprint,
	}
	if exitCode != nil {
		result["exit_code"] = exitCode
	}
	return result
}

func resolveGoalCheckPath(root, raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", errors.New("check path is empty")
	}
	path := raw
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	path = filepath.Clean(path)
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("check path escapes the authorized project")
	}
	return path, nil
}

func truncateGoalEvidence(value string) string {
	if len(value) <= goalEvidenceOutputLimit {
		return value
	}
	return value[:goalEvidenceOutputLimit] + "\n[truncated]"
}

func goalWorkspaceFingerprint(root string) (string, error) {
	files := make([]string, 0, 1024)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path != root && shouldSkipGoalFingerprintDir(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if len(files) >= goalFingerprintFileLimit {
			return errors.New("workspace fingerprint file limit exceeded")
		}
		files = append(files, path)
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(files)
	hash := sha256.New()
	var total int64
	for _, path := range files {
		info, err := os.Lstat(path)
		if err != nil {
			return "", err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return "", err
		}
		_, _ = hash.Write([]byte(filepath.ToSlash(relative)))
		_, _ = hash.Write([]byte{0})
		if info.Mode()&os.ModeSymlink != 0 {
			target, readErr := os.Readlink(path)
			if readErr != nil {
				return "", readErr
			}
			_, _ = hash.Write([]byte("symlink:" + target))
			continue
		}
		total += info.Size()
		if total > goalFingerprintByteLimit {
			return "", errors.New("workspace fingerprint byte limit exceeded")
		}
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			return "", readErr
		}
		_, _ = hash.Write(content)
		_, _ = hash.Write([]byte{0})
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func shouldSkipGoalFingerprintDir(name string) bool {
	switch name {
	case ".git", "node_modules", ".next", ".turbo", ".cache", "dist", "build", "target", "vendor":
		return true
	default:
		return false
	}
}
