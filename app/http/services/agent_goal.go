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
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"aliang.one/nursorgate/app/http/models"
	"github.com/google/shlex"
)

const (
	goalPlannerTransportMargin  = 30 * time.Second
	goalPlannerProgressInterval = 15 * time.Second
	goalPlanContextMaxBytes     = 128 * 1024
	goalPlanFileListMaxBytes    = 16 * 1024
	goalPlanKeyFilesMaxBytes    = 32 * 1024
	goalPlanReadFileMaxBytes    = 32 * 1024
	goalPlanListDirMaxEntries   = 500
	goalPlanGrepMaxResults      = 50
	goalPlanGrepFileMaxBytes    = 256 * 1024
	goalPlanGitPatchMaxBytes    = 48 * 1024
	goalPlanGitPatchPerFile     = 16 * 1024
	goalPlanGitDiffStatMaxBytes = 8 * 1024
	goalEvidenceOutputLimit     = 16 * 1024
	goalFingerprintFileLimit    = 50_000
	goalFingerprintByteLimit    = 256 * 1024 * 1024
	goalReportMarker            = "ALIANG_GOAL_REPORT:"
	goalReportOutputByteLimit   = 16 * 1024
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
	report["output"] = truncateGoalReportOutput(capturedOutput)
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
//  3. 用 json.Decoder 流式解析，允许 JSON 后有 provider prose；如果尾随部分
//     仍是一个完整 JSON value，则拒绝，避免把两个候选结果拼在一起。
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
	// Provider 经常在 JSON 后补一小段解释文字。完整的第二个 JSON value
	// 仍然拒绝；语法错误则视为 prose，schema 校验会继续把 parsed 把关。
	var trailing interface{}
	if err := decoder.Decode(&trailing); err == nil {
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
	// A marker may appear inside a fenced block, so after the marker the tail
	// starts with JSON and ends with the closing fence. Remove that suffix before
	// attempting JSON decoding; the opening fence was already consumed.
	value = strings.TrimSpace(strings.TrimSuffix(value, "```"))
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

func handleAgentGoalMessage(msg map[string]interface{}, writeJSON func(interface{}) error) {
	switch remoteString(msg, "type") {
	case models.AgentEventGoalPlan:
		handleAgentGoalPlan(msg, writeJSON)
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

var pendingGoalPlanAIResponses sync.Map

func deliverGoalPlanAIResponse(msg map[string]interface{}) {
	requestID := strings.TrimSpace(remoteString(msg, "request_id"))
	value, ok := pendingGoalPlanAIResponses.Load(requestID)
	if !ok {
		return
	}
	select {
	case value.(chan map[string]interface{}) <- msg:
	default:
	}
}

func handleAgentGoalPlan(msg map[string]interface{}, writeJSON func(interface{}) error) {
	requestID := strings.TrimSpace(remoteString(msg, "request_id"))
	objective := strings.TrimSpace(remoteString(msg, "objective"))
	goalID := strings.TrimSpace(remoteString(msg, "goal_id"))
	attemptID := strings.TrimSpace(remoteString(msg, "planning_attempt_id"))
	aiSessionID := strings.TrimSpace(remoteString(msg, "ai_session_id"))
	if remoteInt(msg, "protocol_version", 0) != 3 {
		_ = writeJSON(agentGoalErrorPayload(msg, errors.New("goal.plan protocol_version unsupported")))
		return
	}
	if requestID == "" || objective == "" || goalID == "" || attemptID == "" || aiSessionID == "" {
		_ = writeJSON(agentGoalErrorPayload(msg, errors.New("goal.plan missing request, goal, attempt, session, or objective")))
		return
	}
	plannerWait, err := goalPlannerWaitTimeout(msg)
	if err != nil {
		_ = writeJSON(agentGoalErrorPayload(msg, err))
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
	conversationContext, _ := msg["conversation_context"].(map[string]interface{})
	evidence, err := buildGoalPlanContext(projectPath, conversationContext)
	if err != nil {
		_ = writeJSON(agentGoalErrorPayload(msg, err))
		return
	}
	emitGoalPlanPhase(writeJSON, requestID, "exploring", 0)
	responseCh := make(chan map[string]interface{}, 1)
	pendingGoalPlanAIResponses.Store(requestID, responseCh)
	defer pendingGoalPlanAIResponses.Delete(requestID)
	deadline := time.Now().Add(plannerWait)
	progress := time.NewTicker(goalPlannerProgressInterval)
	defer progress.Stop()
	// callServer drives one planning turn over the WS: send goal.plan.ai.request
	// with the conversation only (the server owns the system prompt, objective
	// contract, and canonical tools), then wait for the server's
	// goal.plan.ai.response (the OpenAI assistant message). The whole loop shares
	// one wall-clock deadline (planner_timeout_ms + margin); each turn waits at
	// most until that deadline.
	callServer := func(turn int, messages []map[string]interface{}) (map[string]interface{}, string, error) {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, "", errors.New("planner_service_transport_timeout")
		}
		if err := writeJSON(map[string]interface{}{
			"type":                models.AgentEventGoalPlanAIRequest,
			"request_id":          requestID,
			"goal_id":             goalID,
			"planning_attempt_id": attemptID,
			"ai_session_id":       aiSessionID,
			"turn":                turn,
			"messages":            messages,
		}); err != nil {
			return nil, "", err
		}
		emitGoalPlanPhase(writeJSON, requestID, "planning", turn)
		timer := time.NewTimer(remaining)
		defer timer.Stop()
		for {
			select {
			case response := <-responseCh:
				if remoteString(response, "goal_id") != goalID ||
					remoteString(response, "planning_attempt_id") != attemptID ||
					remoteString(response, "ai_session_id") != aiSessionID {
					return nil, "", errors.New("planner_service_identity_mismatch")
				}
				if remoteString(response, "type") == models.AgentEventGoalPlanAIError {
					return nil, "", errors.New(remoteString(response, "error"))
				}
				return extractGoalPlanAssistant(response)
			case <-progress.C:
				emitGoalPlanPhase(writeJSON, requestID, "planning", turn)
			case <-timer.C:
				return nil, "", errors.New("planner_service_transport_timeout")
			}
		}
	}
	emitGoalPlanPhase(writeJSON, requestID, "planning", 1)
	proposal, providerRunID, err := runGoalPlanLoop(goalPlanLoopInput{
		projectRoot: projectPath,
		evidence:    evidence,
		callServer:  callServer,
		maxTurns:    goalPlannerMaxTurns,
	})
	if err != nil {
		_ = writeJSON(agentGoalErrorPayload(msg, err))
		return
	}
	normalizeGoalPlanPaths(proposal, projectPath)
	finalizeGoalPlan(writeJSON, msg, requestID, before, projectPath, providerRunID, proposal)
}

// extractGoalPlanAssistant pulls the OpenAI assistant message (choices[0].message)
// and the planning response id out of a goal.plan.ai.response envelope. The
// assistant message may carry tool_calls (read-only exploration or
// submit_goal_plan) or plain content (JSON fallback); the loop interprets it.
func extractGoalPlanAssistant(response map[string]interface{}) (map[string]interface{}, string, error) {
	openai, ok := response["response"].(map[string]interface{})
	if !ok {
		return nil, "", errors.New("planner_service_response_invalid")
	}
	choices, _ := openai["choices"].([]interface{})
	if len(choices) == 0 {
		return nil, "", errors.New("planner_service_response_invalid")
	}
	choice, _ := choices[0].(map[string]interface{})
	message, _ := choice["message"].(map[string]interface{})
	if message == nil {
		return nil, "", errors.New("planner_service_response_invalid")
	}
	return message, remoteString(openai, "id"), nil
}

// goalPlannerWaitTimeout derives the Agent's SHARED wall-clock deadline for the
// whole planning loop from planner_timeout_ms. planner_timeout_ms is the
// whole-plan budget (NOT per-turn): the Agent spends it across all turns, so a
// slow first turn leaves less for the rest. The +margin covers WS round-trips so
// the Agent never times out before the server's per-turn call (which is bounded
// by the same planner_timeout_ms as a single-turn safety net).
func goalPlannerWaitTimeout(msg map[string]interface{}) (time.Duration, error) {
	plannerTimeoutMs := remoteInt(msg, "planner_timeout_ms", -1)
	if plannerTimeoutMs < 1_000 || plannerTimeoutMs > 600_000 {
		return 0, errors.New("goal.plan planner_timeout_ms invalid")
	}
	return time.Duration(plannerTimeoutMs)*time.Millisecond + goalPlannerTransportMargin, nil
}

func buildGoalPlanProjectContext(projectPath string) (map[string]interface{}, error) {
	files, fileCount, totalSize := summarizeAgentProjectFiles(projectPath, 1200)
	keyNames := map[string]struct{}{
		"package.json": {}, "tsconfig.json": {}, "go.mod": {}, "cargo.toml": {},
		"pyproject.toml": {}, "requirements.txt": {}, "makefile": {}, "dockerfile": {},
		"docker-compose.yml": {}, "docker-compose.yaml": {}, "agents.md": {}, "claude.md": {},
	}
	keyFiles := make([]map[string]interface{}, 0, 16)
	remaining := goalPlanKeyFilesMaxBytes
	for _, rel := range files {
		if _, ok := keyNames[strings.ToLower(filepath.Base(rel))]; !ok || remaining <= 0 {
			continue
		}
		path := filepath.Join(projectPath, filepath.FromSlash(rel))
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			continue
		}
		inside, err := filepath.Rel(projectPath, resolved)
		if err != nil || inside == ".." || strings.HasPrefix(inside, ".."+string(filepath.Separator)) {
			continue
		}
		info, err := os.Stat(resolved)
		if err != nil || !info.Mode().IsRegular() || info.Size() > 128*1024 {
			continue
		}
		raw, err := os.ReadFile(resolved)
		if err != nil || bytesContainNUL(raw) {
			continue
		}
		if len(raw) > 32*1024 {
			raw = raw[:32*1024]
		}
		if len(raw) > remaining {
			raw = raw[:remaining]
		}
		remaining -= len(raw)
		keyFiles = append(keyFiles, map[string]interface{}{"path": rel, "content": string(raw)})
	}
	contextPayload := map[string]interface{}{
		"schema_version": 1, "workspace_root": projectPath, "files": boundedGoalPlanFiles(files),
		"file_count": fileCount, "total_size_bytes": totalSize,
		"readme": readAgentProjectReadme(projectPath), "key_files": keyFiles,
	}
	if err := fitGoalPlanProjectContext(contextPayload); err != nil {
		return nil, err
	}
	return contextPayload, nil
}

func boundedGoalPlanFiles(files []string) []string {
	bounded := make([]string, 0, len(files))
	remaining := goalPlanFileListMaxBytes
	for _, path := range files {
		cost := len([]byte(path)) + 3
		if cost > remaining {
			break
		}
		bounded = append(bounded, path)
		remaining -= cost
	}
	return bounded
}

func fitGoalPlanProjectContext(contextPayload map[string]interface{}) error {
	encodedSize := func() (int, error) {
		raw, err := json.Marshal(contextPayload)
		return len(raw), err
	}
	for {
		size, err := encodedSize()
		if err != nil {
			return fmt.Errorf("goal planner context encoding failed: %w", err)
		}
		if size <= goalPlanContextMaxBytes {
			return nil
		}
		if keyFiles, ok := contextPayload["key_files"].([]map[string]interface{}); ok && len(keyFiles) > 0 {
			contextPayload["key_files"] = keyFiles[:len(keyFiles)/2]
			continue
		}
		if readme, ok := contextPayload["readme"].(string); ok && len(readme) > 0 {
			contextPayload["readme"] = readme[:len(readme)/2]
			continue
		}
		if files, ok := contextPayload["files"].([]string); ok && len(files) > 0 {
			contextPayload["files"] = files[:len(files)/2]
			continue
		}
		return errors.New("goal planner context exceeds agent limit")
	}
}

// buildGoalPlanContext assembles the evidence the planner receives in its first
// user turn: bounded recent conversation (server-supplied), a projectPath-scoped
// Git snapshot, and a workspace scan (key files + file list + README). The
// server injects the authoritative objective/constraints/non-goals + skill as
// the system message, so the evidence carries NO objective — it is purely
// grounding ("what was discussed / what changed / what files exist"). Trimmed to
// the planner budget (per-turn resend cost is bounded by this cap × max turns).
func buildGoalPlanContext(projectPath string, conversation map[string]interface{}) (map[string]interface{}, error) {
	workspace, err := buildGoalPlanProjectContext(projectPath)
	if err != nil {
		return nil, err
	}
	evidence := map[string]interface{}{
		"conversation_context": conversation,
		"git_context":          buildGoalPlanGitContext(projectPath),
		"workspace_context": map[string]interface{}{
			"readme":    workspace["readme"],
			"key_files": workspace["key_files"],
			"files":     workspace["files"],
		},
	}
	if err := fitGoalPlanContext(evidence); err != nil {
		return nil, err
	}
	return evidence, nil
}

// fitGoalPlanContext bounds the evidence to goalPlanContextMaxBytes by halving
// fields in priority order: static workspace first (key_files → file list →
// README), then git patches + changed_files, keeping conversation + git metadata
// (small, high-value for "continue from latest changes") as long as possible.
func fitGoalPlanContext(evidence map[string]interface{}) error {
	for {
		raw, err := json.Marshal(evidence)
		if err != nil {
			return fmt.Errorf("goal planner evidence encoding failed: %w", err)
		}
		if len(raw) <= goalPlanContextMaxBytes {
			return nil
		}
		if halveGoalPlanSliceField(evidence, "workspace_context", "key_files") {
			continue
		}
		if halveGoalPlanSliceField(evidence, "workspace_context", "files") {
			continue
		}
		if halveGoalPlanStringField(evidence, "workspace_context", "readme") {
			continue
		}
		if halveGoalPlanSliceField(evidence, "git_context", "patches") {
			continue
		}
		if halveGoalPlanSliceField(evidence, "git_context", "changed_files") {
			continue
		}
		return errors.New("goal planner evidence exceeds agent limit")
	}
}

func halveGoalPlanSliceField(evidence map[string]interface{}, parent, field string) bool {
	m, ok := evidence[parent].(map[string]interface{})
	if !ok {
		return false
	}
	switch slice := m[field].(type) {
	case []map[string]interface{}:
		if len(slice) <= 1 {
			return false
		}
		m[field] = slice[:len(slice)/2]
		return true
	case []string:
		if len(slice) <= 1 {
			return false
		}
		m[field] = slice[:len(slice)/2]
		return true
	case []interface{}:
		if len(slice) <= 1 {
			return false
		}
		m[field] = slice[:len(slice)/2]
		return true
	}
	return false
}

func halveGoalPlanStringField(evidence map[string]interface{}, parent, field string) bool {
	m, ok := evidence[parent].(map[string]interface{})
	if !ok {
		return false
	}
	value, ok := m[field].(string)
	if !ok || len(value) <= 1 {
		return false
	}
	m[field] = value[:len(value)/2]
	return true
}

func bytesContainNUL(value []byte) bool {
	for _, b := range value {
		if b == 0 {
			return true
		}
	}
	return false
}

// buildGoalPlanGitContext gathers a bounded, projectPath-scoped Git snapshot for
// planning. Every command carries a fixed `-- .` pathspec (so a monorepo's
// sibling directories are never leaked) plus --no-ext-diff/--no-textconv (so
// repo config can't trigger external programs). Each command degrades
// INDEPENDENTLY: an unborn repo fails `log`/`diff HEAD`/`rev-parse` but still
// yields status + untracked files. Only a `status` failure (not a git repo /
// git missing) marks the whole context unavailable — it never fails planning.
func buildGoalPlanGitContext(projectPath string) map[string]interface{} {
	statusOut, err := agentRunGit(projectPath, "status", "--porcelain=v2", "-z", "--branch",
		"--untracked-files=all", "--", ".")
	if err != nil {
		return map[string]interface{}{"available": false, "reason": "git status unavailable"}
	}
	// prefix = repo-root → projectPath relative path (e.g. "a/"); stripped from
	// git paths so they are projectPath-relative and round-trip with read_file.
	prefix, _ := agentRunGit(projectPath, "rev-parse", "--show-prefix")
	prefix = strings.TrimRight(prefix, "\n")
	branch, changedFiles := parseGoalPlanGitStatusV2(statusOut, prefix)
	ctx := map[string]interface{}{
		"available":     true,
		"branch":        branch,
		"changed_files": changedFiles,
	}
	if sha, err := agentRunGit(projectPath, "rev-parse", "--verify", "HEAD"); err == nil {
		ctx["head_sha"] = strings.TrimSpace(sha)
	} else {
		ctx["head_sha"] = ""
	}
	if stat, err := agentRunGit(projectPath, "diff", "--no-ext-diff", "--no-textconv",
		"--stat", "HEAD", "--", "."); err == nil {
		ctx["diff_stat"] = clipBytes(stat, goalPlanGitDiffStatMaxBytes)
	}
	ctx["patches"] = goalPlanGitPatches(projectPath, prefix)
	if logOut, err := agentRunGit(projectPath, "log", "-10", "--format=%H%x09%s", "--", "."); err == nil {
		ctx["recent_commits"] = parseGoalPlanGitLog(logOut)
	} else {
		ctx["recent_commits"] = []interface{}{}
	}
	return ctx
}

// goalPlanGitPatches returns per-file diff chunks (capped) for tracked changes
// under projectPath. Truncation is per-file + total-bounded so a hunk is never
// hard-cut by a single global cap. Reuses parseGitDiffFiles for robust path
// extraction (handles renames, spaces, non-ASCII via the +++/--- headers).
func goalPlanGitPatches(projectPath, prefix string) []map[string]interface{} {
	diffOut, err := agentRunGit(projectPath, "diff", "--no-ext-diff", "--no-textconv",
		"--no-color", "--unified=3", "HEAD", "--", ".")
	if err != nil {
		return []map[string]interface{}{}
	}
	patches := []map[string]interface{}{}
	total := 0
	for _, file := range parseGitDiffFiles(diffOut) {
		if total >= goalPlanGitPatchMaxBytes {
			break
		}
		original := len(file.diff)
		capped := file.diff
		if len(capped) > goalPlanGitPatchPerFile {
			capped = capped[:goalPlanGitPatchPerFile]
		}
		if remaining := goalPlanGitPatchMaxBytes - total; len(capped) > remaining {
			capped = capped[:remaining]
		}
		total += len(capped)
		patches = append(patches, map[string]interface{}{
			"path": stripGoalPlanPathPrefix(file.relPath, prefix), "diff": capped, "truncated": len(capped) < original,
		})
	}
	return patches
}

// parseGoalPlanGitStatusV2 parses `git status --porcelain=v2 -z --branch` output
// (NUL-delimited, so paths with spaces / non-ASCII / renames decode verbatim —
// unlike the v1 octal-escaped path quirk). Returns the branch head + a list of
// {path, status} for changed/untracked files.
// parseGoalPlanGitStatusV2 parses `git status --porcelain=v2 -z --branch` output
// (NUL-delimited, so paths with spaces / non-ASCII / renames decode verbatim —
// unlike the v1 octal-escaped path quirk). prefix is `git rev-parse
// --show-prefix` (the repo-root→projectPath relative path, e.g. "a/"); it is
// stripped from each path so changed_files entries are projectPath-relative and
// the model can feed them straight back to read_file. Returns the branch head +
// a list of {path, status} for changed/untracked files.
func parseGoalPlanGitStatusV2(out, prefix string) (branch string, entries []map[string]interface{}) {
	entries = []map[string]interface{}{}
	for _, field := range strings.Split(out, "\x00") {
		if field == "" {
			continue
		}
		if strings.HasPrefix(field, "# branch.head ") {
			branch = strings.TrimSpace(strings.TrimPrefix(field, "# branch.head "))
			continue
		}
		if strings.HasPrefix(field, "#") {
			continue
		}
		switch field[0] {
		case '1':
			// "1 <XY> <sub> <mH> <mI> <mW> <hH> <hI> <path>" → 9 fields, path=parts[8].
			parts := strings.SplitN(field, " ", 9)
			if len(parts) >= 9 {
				entries = append(entries, map[string]interface{}{
					"path": stripGoalPlanPathPrefix(parts[8], prefix),
					"status": classifyGitStatusCode(parts[1]),
				})
			}
		case '2':
			// "2 <XY> <sub> <mH> <mI> <mW> <hH> <hI> <Rscore> <path>" → 10 fields,
			// path=parts[9] (the rename/copy score occupies parts[8]). The original
			// path follows in the next NUL field and is a bare path (no case match).
			parts := strings.SplitN(field, " ", 10)
			if len(parts) >= 10 {
				entries = append(entries, map[string]interface{}{
					"path": stripGoalPlanPathPrefix(parts[9], prefix),
					"status": classifyGitStatusCode(parts[1]),
				})
			}
		case '?':
			if rel := strings.TrimPrefix(field, "? "); rel != "" {
				entries = append(entries, map[string]interface{}{
					"path": stripGoalPlanPathPrefix(rel, prefix), "status": "added",
				})
			}
		case '!':
			// ignored — skip
		}
	}
	if branch == "" {
		branch = "(unknown)"
	}
	return
}

// stripGoalPlanPathPrefix removes the repo-root→projectPath prefix (e.g. "a/")
// that git emits, so paths become projectPath-relative and the model can pass
// them to read_file without a doubled "a/a/..." segment.
func stripGoalPlanPathPrefix(path, prefix string) string {
	if prefix == "" {
		return path
	}
	if strings.HasPrefix(path, prefix) {
		return path[len(prefix):]
	}
	return path
}

func parseGoalPlanGitLog(out string) []map[string]interface{} {
	commits := []map[string]interface{}{}
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if line == "" {
			continue
		}
		tab := strings.IndexByte(line, '\t')
		if tab < 0 {
			continue
		}
		commits = append(commits, map[string]interface{}{"sha": line[:tab], "subject": line[tab+1:]})
	}
	return commits
}

func clipBytes(value string, max int) string {
	if len(value) <= max {
		return value
	}
	return value[:max]
}

// goalPlanToolResult is the content of the {role:"tool"} message the bounded
// planning loop appends after running one read-only exploration tool locally.
// isError marks a failed tool call (bad path / bad regex / unknown tool) so the
// model can react instead of the loop stalling on an empty success.
type goalPlanToolResult struct {
	content string
	isError bool
}

// resolveGoalPlanReadPath resolves a planner-tool path under projectRoot with
// symlink awareness. resolveGoalCheckPath only does a LEXICAL boundary check, so
// a symlink inside the root that points outside would let the model read beyond
// the authorized project. EvalSymlinks + a canonical-root re-check closes that.
func resolveGoalPlanReadPath(projectRoot, raw string) (string, error) {
	lexical, err := resolveGoalCheckPath(projectRoot, raw)
	if err != nil {
		return "", err
	}
	// Canonicalize both sides: on macOS /var → /private/var, so EvalSymlinks of
	// the file lands in /private/... while a raw projectRoot stays in /var/...,
	// making a legitimate file look like an escape. Resolve the root too.
	rootResolved, err := filepath.EvalSymlinks(projectRoot)
	if err != nil {
		rootResolved = projectRoot
	}
	resolved, err := filepath.EvalSymlinks(lexical)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(rootResolved, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("path escapes the authorized project")
	}
	return resolved, nil
}

// executeGoalPlanReadOnlyTool dispatches one read-only planning tool call from
// the LLM against the agent's local filesystem, sandboxed to projectRoot. The
// terminal submit_goal_plan tool is handled by the loop driver, not here.
func executeGoalPlanReadOnlyTool(projectRoot, toolName, argumentsJSON string) goalPlanToolResult {
	switch toolName {
	case "read_file":
		return goalPlanReadFileTool(projectRoot, argumentsJSON)
	case "list_dir":
		return goalPlanListDirTool(projectRoot, argumentsJSON)
	case "grep":
		return goalPlanGrepTool(projectRoot, argumentsJSON)
	default:
		return goalPlanToolResult{content: "unknown planning tool: " + toolName, isError: true}
	}
}

func goalPlanReadFileTool(projectRoot, argumentsJSON string) goalPlanToolResult {
	var args struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(argumentsJSON), &args); err != nil {
		return goalPlanToolResult{content: "invalid arguments: " + err.Error(), isError: true}
	}
	path, err := resolveGoalPlanReadPath(projectRoot, args.Path)
	if err != nil {
		return goalPlanToolResult{content: "path rejected: " + err.Error(), isError: true}
	}
	// io.LimitReader: read at most cap+1 bytes so a multi-GB file can't be
	// fully allocated before truncation (the +1 detects truncation).
	f, err := os.Open(path)
	if err != nil {
		return goalPlanToolResult{content: "read failed: " + err.Error(), isError: true}
	}
	raw, readErr := io.ReadAll(io.LimitReader(f, goalPlanReadFileMaxBytes+1))
	f.Close()
	if readErr != nil {
		return goalPlanToolResult{content: "read failed: " + readErr.Error(), isError: true}
	}
	if len(raw) > goalPlanReadFileMaxBytes {
		raw = raw[:goalPlanReadFileMaxBytes]
	}
	return goalPlanToolResult{content: string(raw)}
}

func goalPlanListDirTool(projectRoot, argumentsJSON string) goalPlanToolResult {
	var args struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(argumentsJSON), &args); err != nil {
		return goalPlanToolResult{content: "invalid arguments: " + err.Error(), isError: true}
	}
	target := strings.TrimSpace(args.Path)
	if target == "" {
		target = "."
	}
	path, err := resolveGoalPlanReadPath(projectRoot, target)
	if err != nil {
		return goalPlanToolResult{content: "path rejected: " + err.Error(), isError: true}
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return goalPlanToolResult{content: "list failed: " + err.Error(), isError: true}
	}
	type listEntry struct {
		Name  string `json:"name"`
		IsDir bool   `json:"is_dir"`
	}
	out := make([]listEntry, 0, len(entries))
	for i, entry := range entries {
		if i >= goalPlanListDirMaxEntries {
			break
		}
		out = append(out, listEntry{Name: entry.Name(), IsDir: entry.IsDir()})
	}
	encoded, _ := json.Marshal(out)
	return goalPlanToolResult{content: string(encoded)}
}

var errGoalPlanGrepEnough = errors.New("goal plan grep result cap reached")

func goalPlanGrepTool(projectRoot, argumentsJSON string) goalPlanToolResult {
	var args struct {
		Pattern string `json:"pattern"`
		Path    string `json:"path"`
	}
	if err := json.Unmarshal([]byte(argumentsJSON), &args); err != nil {
		return goalPlanToolResult{content: "invalid arguments: " + err.Error(), isError: true}
	}
	if strings.TrimSpace(args.Pattern) == "" {
		return goalPlanToolResult{content: "pattern is empty", isError: true}
	}
	re, err := regexp.Compile(args.Pattern)
	if err != nil {
		return goalPlanToolResult{content: "invalid regex: " + err.Error(), isError: true}
	}
	scope := projectRoot
	if strings.TrimSpace(args.Path) != "" {
		resolved, err := resolveGoalPlanReadPath(projectRoot, args.Path)
		if err != nil {
			return goalPlanToolResult{content: "path rejected: " + err.Error(), isError: true}
		}
		scope = resolved
	}
	type grepMatch struct {
		Path string `json:"path"`
		Line int    `json:"line"`
		Text string `json:"text"`
	}
	matches := make([]grepMatch, 0, goalPlanGrepMaxResults)
	walkErr := filepath.WalkDir(scope, func(p string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if d.IsDir() {
			if p != scope && shouldSkipGoalFingerprintDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil || info.Size() > goalPlanGrepFileMaxBytes {
			return nil
		}
		rel, err := filepath.Rel(projectRoot, p)
		if err != nil {
			return nil
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		for lineNo, line := range strings.Split(string(raw), "\n") {
			if re.MatchString(line) {
				matches = append(matches, grepMatch{
					Path: filepath.ToSlash(rel), Line: lineNo + 1, Text: clipGoalPlanLine(line),
				})
				if len(matches) >= goalPlanGrepMaxResults {
					return errGoalPlanGrepEnough
				}
			}
		}
		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, errGoalPlanGrepEnough) {
		return goalPlanToolResult{content: "grep failed: " + walkErr.Error(), isError: true}
	}
	encoded, _ := json.Marshal(matches)
	return goalPlanToolResult{content: string(encoded)}
}

func clipGoalPlanLine(line string) string {
	if len(line) <= 500 {
		return line
	}
	return line[:500] + "…"
}

// goalPlannerMaxTurns is the default soft cap for the bounded planning loop. It
// bounds cost on the free planner channel while leaving ample room to explore a
// real repo (the LLM rarely needs more than a handful of file reads before
// submitting). Reached only when the model loops without converging.
const goalPlannerMaxTurns = 8

// goalPlanTurnCaller sends one planning turn to the server and returns the
// assistant message + the planning response id. Injected so the loop logic is
// unit-testable without a live WebSocket; the production caller in
// handleAgentGoalPlan performs the real goal.plan.ai.request round-trip.
type goalPlanTurnCaller func(turn int, messages []map[string]interface{}) (assistant map[string]interface{}, responseID string, err error)

type goalPlanLoopInput struct {
	projectRoot string
	evidence    map[string]interface{}
	callServer  goalPlanTurnCaller
	maxTurns    int
}

// goalPlannerTools was removed: the server now owns the canonical tool
// definitions (read_file/list_dir/grep/submit_goal_plan); the agent never
// supplies tool schemas, only implements the matching handlers below.

// extractSubmitGoalPlanProposal pulls the plan out of a submit_goal_plan tool
// call's arguments. The server's canonical tool schema wraps the plan as
// {"proposal": {...}}, so a schema-compliant model sends the plan under
// "proposal"; accept that shape and fall back to a bare plan object for
// robustness. Returns nil if the arguments are not a JSON object.
func extractSubmitGoalPlanProposal(arguments string) map[string]interface{} {
	var wrapper struct {
		Proposal map[string]interface{} `json:"proposal"`
	}
	if json.Unmarshal([]byte(arguments), &wrapper) == nil && wrapper.Proposal != nil {
		return wrapper.Proposal
	}
	var bare map[string]interface{}
	if json.Unmarshal([]byte(arguments), &bare) == nil {
		return bare
	}
	return nil
}

// runGoalPlanLoop drives the bounded, read-only exploration loop: the agent
// holds the conversation, calls the server once per turn (the server injects
// the skill system prompt and forwards a single OpenAI call), executes any
// read-only tool calls locally against projectRoot, and ends when the model
// calls submit_goal_plan with a valid plan. A content-only assistant turn is
// parsed as a JSON fallback. Returns planner_budget_exceeded if maxTurns is
// reached without convergence.
func runGoalPlanLoop(input goalPlanLoopInput) (map[string]interface{}, string, error) {
	if input.maxTurns <= 0 {
		input.maxTurns = goalPlannerMaxTurns
	}
	firstUser, _ := json.Marshal(input.evidence)
	messages := []map[string]interface{}{
		{"role": "user", "content": string(firstUser)},
	}
	for turn := 1; turn <= input.maxTurns; turn++ {
		assistant, responseID, err := input.callServer(turn, messages)
		if err != nil {
			return nil, "", err
		}
		toolCalls, _ := assistant["tool_calls"].([]interface{})
		if len(toolCalls) == 0 {
			content := strings.TrimSpace(remoteString(assistant, "content"))
			var proposal map[string]interface{}
			if content != "" && json.Unmarshal([]byte(content), &proposal) == nil && validGoalPlan(proposal) {
				return proposal, responseID, nil
			}
			messages = append(messages, assistant, map[string]interface{}{
				"role":    "user",
				"content": "You did not call submit_goal_plan and your content was not a valid plan. Call submit_goal_plan with the full proposal now.",
			})
			continue
		}
		messages = append(messages, assistant)
		for _, raw := range toolCalls {
			call, ok := raw.(map[string]interface{})
			if !ok {
				continue
			}
			fn, _ := call["function"].(map[string]interface{})
			name := strings.TrimSpace(remoteString(fn, "name"))
			arguments := remoteString(fn, "arguments")
			callID := strings.TrimSpace(remoteString(call, "id"))
			if name == "submit_goal_plan" {
				proposal := extractSubmitGoalPlanProposal(arguments)
				if proposal != nil && validGoalPlan(proposal) {
					return proposal, responseID, nil
				}
				messages = append(messages, map[string]interface{}{
					"role": "tool", "tool_call_id": callID,
					"content": "invalid plan: the proposal failed validation (schema_version/objective/tasks/allowed_roots/checks). Re-emit submit_goal_plan with a complete valid plan.",
				})
				continue
			}
			result := executeGoalPlanReadOnlyTool(input.projectRoot, name, arguments)
			messages = append(messages, map[string]interface{}{
				"role": "tool", "tool_call_id": callID, "content": result.content,
			})
		}
	}
	return nil, "", errors.New("planner_budget_exceeded: max planning turns reached without a valid plan")
}

// codexSandboxMode selects the Codex app-server sandbox based on readOnly.
// Extracted from runCodexAppServer for testability (#5 fork read-only).
func codexSandboxMode(readOnly bool) string {
	if readOnly {
		return "read-only"
	}
	return "workspace-write"
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
//
// A plan with zero surviving tasks is left empty so the server rejects it as an
// invalid proposal rather than accepting an unverifiable plan.
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

func finalizeGoalPlan(writeJSON func(interface{}) error, msg map[string]interface{}, requestID, before, projectPath, providerRunID string, proposal map[string]interface{}) {
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
		"provider_run_id":              firstNonEmpty(providerRunID, requestID),
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

func withAgentReadOnlyPolicy(tool *agentAITool) *agentAITool {
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
		// model provider and gateway base URL. Isolate the read-only session from rules and
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
