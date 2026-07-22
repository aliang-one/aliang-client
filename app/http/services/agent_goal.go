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
	goalPlanOutputLimit      = 256 * 1024
	goalFingerprintFileLimit = 50_000
	goalFingerprintByteLimit = 256 * 1024 * 1024
	goalReportMarker         = "ALIANG_GOAL_REPORT:"
	goalPlanMarker           = "ALIANG_GOAL_PLAN:"
)

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
	if report, ok := parseMarkedJSONObject(e.goalOutput.String(), goalReportMarker); ok && validGoalReport(report) {
		payload["goal_report"] = report
		return
	}
	outcome := "no_progress"
	blocker := "goal_report_missing"
	if remoteString(payload, "type") == models.AgentEventAIError {
		outcome = "failed"
		blocker = "provider_error"
	}
	payload["goal_report"] = map[string]interface{}{
		"schema_version":      1,
		"outcome":             outcome,
		"summary":             "Provider did not return a valid structured Goal report.",
		"blocker_code":        blocker,
		"evidence_refs":       []interface{}{},
		"completion_proposed": false,
	}
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

func parseMarkedJSONObject(output, marker string) (map[string]interface{}, bool) {
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

	prompt := fmt.Sprintf(`You are planning one software-development Goal in read-only mode.
Objective: %s
User constraints (authoritative): %s
Non-goals (authoritative): %s
Workspace root: %s
Inspect the current workspace. Do not edit files, install dependencies, or run mutating commands.
Produce a small dependency-aware plan. Every task must stay under the workspace root. Checks must be deterministic command, file_exists, or file_contains checks. Command checks must be verification-only: use test/lint/check/typecheck/verify/build scripts; read-only git subcommands; direct tsc/vitest/jest/pytest; go test/vet; cargo test/check/clippy; or dotnet/swift test/build. Never propose install, add, remove, publish, deploy, push, commit, checkout, reset, clean, or shell-composed commands. npx checks must use --no-install.
End with exactly one line beginning %s followed by compact single-line JSON shaped as:
{"schema_version":1,"objective":string,"constraints":[string],"non_goals":[string],"tasks":[{"key":string,"title":string,"description":string,"depends_on":[string],"allowed_roots":[%q],"allowed_commands":[string],"checks":[{"key":string,"type":"command|file_exists|file_contains","command":string,"path":string,"contains":string,"required":true,"timeout_ms":number}],"retry_safety":"safe|idempotent_with_key|unsafe","idempotency_key_template":"required when retry_safety is idempotent_with_key"}],"budget":{"max_attempts_per_task":number,"max_turns":number,"command_timeout_ms":number}}`, objective, goalPromptList(constraints), goalPromptList(nonGoals), projectPath, goalPlanMarker, projectPath)

	var captureMu sync.Mutex
	var output strings.Builder
	var runError string
	capture := func(value interface{}) error {
		payload, ok := value.(map[string]interface{})
		if !ok {
			return nil
		}
		captureMu.Lock()
		defer captureMu.Unlock()
		switch remoteString(payload, "type") {
		case models.AgentEventAIDelta:
			if output.Len() < goalPlanOutputLimit {
				delta := remoteString(payload, "delta")
				remaining := goalPlanOutputLimit - output.Len()
				if len(delta) > remaining {
					delta = delta[:remaining]
				}
				output.WriteString(delta)
			}
		case models.AgentEventAIError:
			runError = firstNonEmpty(remoteString(payload, "error"), remoteString(payload, "detail"), "planner provider failed")
		}
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), goalPlannerTimeout)
	defer cancel()
	manager.runCLIPass(ctx, agentAIRun{
		sessionID:   "goal-plan:" + remoteString(msg, "goal_id"),
		messageID:   requestID,
		mode:        "agent",
		projectPath: projectPath,
		provider:    firstNonEmpty(remoteString(msg, "provider"), "auto"),
		model:       remoteString(msg, "model"),
		effort:      remoteString(msg, "effort"),
		prompt:      prompt,
		freshPrompt: prompt,
		activity:    newAgentAIActivity(),
		readOnly:    true,
	}, capture, false)

	captureMu.Lock()
	providerOutput := output.String()
	providerError := runError
	captureMu.Unlock()
	if ctx.Err() != nil {
		_ = writeJSON(agentGoalErrorPayload(msg, fmt.Errorf("goal planner timed out: %w", ctx.Err())))
		return
	}
	if providerError != "" {
		_ = writeJSON(agentGoalErrorPayload(msg, errors.New(providerError)))
		return
	}
	proposal, ok := parseMarkedJSONObject(providerOutput, goalPlanMarker)
	if !ok || !validGoalPlan(proposal) {
		_ = writeJSON(agentGoalErrorPayload(msg, errors.New("planner did not return valid ALIANG_GOAL_PLAN v1 JSON")))
		return
	}
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
		flags = []string{"--sandbox", "read-only", "--ignore-user-config"}
	case "claude", "claudecode":
		flags = []string{
			"--permission-mode", "plan",
			"--disallowedTools", "Bash,Edit,Write,NotebookEdit",
			"--setting-sources", "",
			"--strict-mcp-config", "--mcp-config", claudeCodeHeadlessEmptyMCP,
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
