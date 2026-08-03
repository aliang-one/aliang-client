package services

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"aliang.one/nursorgate/app/http/models"
)

func testGoalIdentity() map[string]interface{} {
	return map[string]interface{}{
		"goal_id":             "goal-1",
		"goal_run_id":         "run-1",
		"plan_revision_id":    "revision-1",
		"task_id":             "task-1",
		"dispatch_token":      "dispatch-1",
		"goal_context_digest": "sha256:context",
	}
}

func TestGoalRunEmitterDecoratesEventsAndParsesTerminalReport(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "main.txt")
	if err := os.WriteFile(path, []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	var events []map[string]interface{}
	emitter := newAgentAIRunEmitter(agentAIRun{
		runID: "run-1", goalIdentity: testGoalIdentity(),
	}, func(value interface{}) error {
		events = append(events, value.(map[string]interface{}))
		return nil
	})
	if err := emitter.captureGoalWorkspace(workspace); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("after"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := emitter.emit(map[string]interface{}{
		"type":  models.AgentEventAIDelta,
		"delta": `work complete` + "\n" + goalReportMarker + ` {"schema_version":1,"outcome":"task_completed","summary":"done","evidence_refs":[],"completion_proposed":true}`,
	}); err != nil {
		t.Fatal(err)
	}
	if err := emitter.emit(map[string]interface{}{"type": models.AgentEventAIDone}); err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("events = %d, want 2", len(events))
	}
	for index, event := range events {
		for key, want := range testGoalIdentity() {
			if event[key] != want {
				t.Fatalf("event %d %s = %v, want %v", index, key, event[key], want)
			}
		}
		if event["event_seq"] != int64(index+1) {
			t.Fatalf("event %d sequence = %v", index, event["event_seq"])
		}
	}
	report, ok := events[1]["goal_report"].(map[string]interface{})
	if !ok || report["outcome"] != "task_completed" {
		t.Fatalf("goal_report = %#v", events[1]["goal_report"])
	}
	if events[1]["workspace_fingerprint_before"] == events[1]["workspace_fingerprint_after"] {
		t.Fatalf("workspace fingerprints did not capture the run change: %#v", events[1])
	}
}

func TestGoalPlannerUsesDedicatedServerServiceWithoutLocalProviderSession(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "package.json"), []byte(`{"scripts":{"test":"vitest"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	request := map[string]interface{}{
		"type": models.AgentEventGoalPlan, "request_id": "req-plan-1", "protocol_version": 3,
		"goal_id": "goal-1", "planning_attempt_id": "attempt-1", "ai_session_id": "session-1",
		"project_path": workspace, "objective": "Ship the feature", "plan_skill": "gstack",
		"planner_timeout_ms": 120_000,
	}
	var events []map[string]interface{}
	writeJSON := func(value interface{}) error {
		event := value.(map[string]interface{})
		events = append(events, event)
		// Simulate the dedicated planner service: on each goal.plan.ai.request
		// reply with an assistant message whose content is a valid plan JSON.
		// The loop's content-fallback path converges in a single turn.
		if event["type"] == models.AgentEventGoalPlanAIRequest {
			content := fmt.Sprintf(`{"schema_version":1,"objective":"Ship the feature","tasks":[{"key":"implement","title":"Implement","allowed_roots":[%q],"checks":[{"key":"package","type":"file_exists","path":"package.json","required":true}]}]}`, workspace)
			deliverGoalPlanAIResponse(map[string]interface{}{
				"type": models.AgentEventGoalPlanAIResponse, "request_id": "req-plan-1",
				"goal_id": "goal-1", "planning_attempt_id": "attempt-1", "ai_session_id": "session-1",
				"response": map[string]interface{}{
					"id": "chatcmpl-plan-1",
					"choices": []interface{}{map[string]interface{}{
						"message": map[string]interface{}{"role": "assistant", "content": content},
					}},
				},
			})
		}
		return nil
	}

	handleAgentGoalPlan(request, writeJSON)

	var requestSeen, resultEvent map[string]interface{}
	for _, event := range events {
		switch event["type"] {
		case models.AgentEventGoalPlanAIRequest:
			requestSeen = event
		case models.AgentEventGoalPlanResult:
			resultEvent = event
		}
	}
	if requestSeen == nil || resultEvent == nil {
		t.Fatalf("events = %#v, want ai.request and result", events)
	}
	if resultEvent["provider_run_id"] != "chatcmpl-plan-1" {
		t.Fatalf("provider run id = %v", resultEvent["provider_run_id"])
	}
	proposal, _ := resultEvent["proposal"].(map[string]interface{})
	if proposal == nil || proposal["objective"] != "Ship the feature" {
		t.Fatalf("proposal = %#v", resultEvent["proposal"])
	}
}

func TestGoalPlannerWaitTimeoutAddsTransportMargin(t *testing.T) {
	got, err := goalPlannerWaitTimeout(map[string]interface{}{"planner_timeout_ms": 120_000})
	if err != nil {
		t.Fatal(err)
	}
	if want := 150 * time.Second; got != want {
		t.Fatalf("planner wait = %s, want %s", got, want)
	}
	if _, err := goalPlannerWaitTimeout(map[string]interface{}{}); err == nil {
		t.Fatal("missing planner_timeout_ms was accepted")
	}
}

func TestGoalPlanProjectContextStaysWithinAgentBudget(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "README.md"), []byte(strings.Repeat("r", 200*1024)), 0o600); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 20; index++ {
		dir := filepath.Join(workspace, fmt.Sprintf("pkg-%02d", index))
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(strings.Repeat("\"", 32*1024)), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	contextPayload, err := buildGoalPlanProjectContext(workspace)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(contextPayload)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > goalPlanContextMaxBytes {
		t.Fatalf("context bytes = %d, max = %d", len(raw), goalPlanContextMaxBytes)
	}
	if readme := contextPayload["readme"].(string); len(readme) > agentProjectReadmeMaxBytes {
		t.Fatalf("README bytes = %d, max = %d", len(readme), agentProjectReadmeMaxBytes)
	}
}

func TestGoalForkUsesProviderNativeReadOnlyPolicies(t *testing.T) {
	tests := []struct {
		id       string
		wantArgs []string
		wantEnv  string
	}{
		{id: "codex", wantArgs: []string{"--sandbox", "read-only", "--ignore-rules", "--ephemeral"}},
		{id: "claudecode", wantArgs: []string{"--permission-mode", "plan", "--disallowedTools", "Bash,Edit,Write,NotebookEdit"}},
		{id: "opencode", wantArgs: []string{"--pure", "--agent", "plan"}, wantEnv: "OPENCODE_CONFIG_CONTENT="},
	}
	for _, test := range tests {
		t.Run(test.id, func(t *testing.T) {
			original := &agentAITool{id: test.id, args: []string{"run", "planner prompt"}}
			readOnly := withAgentReadOnlyPolicy(original)
			if readOnly == nil {
				t.Fatal("read-only adapter missing")
			}
			joined := strings.Join(readOnly.args, " ")
			for _, want := range test.wantArgs {
				if !strings.Contains(joined, want) {
					t.Fatalf("args %q missing %q", joined, want)
				}
			}
			if readOnly.args[len(readOnly.args)-1] != "planner prompt" {
				t.Fatalf("prompt is not the final argument: %#v", readOnly.args)
			}
			if test.wantEnv != "" && !strings.Contains(strings.Join(readOnly.env, "\n"), test.wantEnv) {
				t.Fatalf("env %#v missing %q", readOnly.env, test.wantEnv)
			}
			if len(original.args) != 2 {
				t.Fatalf("read-only adapter mutated original tool: %#v", original.args)
			}
		})
	}
}

func TestAgentAIModelNormalizerDropsDisplayLabels(t *testing.T) {
	for _, displayLabel := range []string{"Claude Code", "provider default", "default"} {
		if got := normalizeAgentAIModel(displayLabel); got != "" {
			t.Fatalf("normalizeAgentAIModel(%q) = %q, want empty", displayLabel, got)
		}
	}
}

func TestOpenCodeGoalExecutionUsesBoundedAutoPolicy(t *testing.T) {
	original := &agentAITool{id: "opencode", args: []string{"run", "goal prompt"}}
	goalTool := withGoalExecutionPolicy(original)
	joinedArgs := strings.Join(goalTool.args, " ")
	if !strings.Contains(joinedArgs, "--pure --auto") {
		t.Fatalf("args %q do not enable bounded headless execution", joinedArgs)
	}
	joinedEnv := strings.Join(goalTool.env, "\n")
	for _, want := range []string{`"share":"disabled"`, `"external_directory":"deny"`, `"task":"deny"`} {
		if !strings.Contains(joinedEnv, want) {
			t.Fatalf("env %q missing %q", joinedEnv, want)
		}
	}
	if len(original.args) != 2 {
		t.Fatalf("Goal policy mutated original tool: %#v", original.args)
	}
}

func TestNonOpenCodeGoalExecutionPolicyIsUnchanged(t *testing.T) {
	for _, provider := range []string{"codex", "claudecode"} {
		original := &agentAITool{id: provider, args: []string{"run", "goal prompt"}}
		if got := withGoalExecutionPolicy(original); got != original {
			t.Fatalf("%s tool was unexpectedly copied or changed", provider)
		}
	}
}

func TestGoalRunEmitterFailsClosedWithoutStructuredReport(t *testing.T) {
	var terminal map[string]interface{}
	emitter := newAgentAIRunEmitter(agentAIRun{
		runID: "run-1", goalIdentity: testGoalIdentity(),
	}, func(value interface{}) error {
		terminal = value.(map[string]interface{})
		return nil
	})
	if err := emitter.emit(map[string]interface{}{"type": models.AgentEventAIDone}); err != nil {
		t.Fatal(err)
	}
	report := terminal["goal_report"].(map[string]interface{})
	if report["outcome"] != "no_progress" || report["blocker_code"] != "goal_report_missing" {
		t.Fatalf("goal_report = %#v", report)
	}
}

func TestGoalCheckPathCannotEscapeAuthorizedProject(t *testing.T) {
	root := t.TempDir()
	if _, err := resolveGoalCheckPath(root, filepath.Join("..", "secret")); err == nil {
		t.Fatal("expected parent traversal to be rejected")
	}
	inside, err := resolveGoalCheckPath(root, filepath.Join("src", "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	if inside != filepath.Join(root, "src", "main.go") {
		t.Fatalf("inside = %q", inside)
	}
}

func TestGoalCommandCheckDoesNotInterpretShellOperators(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "should-not-exist")
	result := executeGoalCheck(root, map[string]interface{}{
		"check_id":          "check-1",
		"type":              "command",
		"command":           "go version; touch " + marker,
		"timeout_ms":        30_000,
		"definition_digest": "sha256:test",
	}, "sha256:workspace")
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("shell operator executed an extra command: stat error = %v", err)
	}
	if result["status"] == "passed" {
		t.Fatalf("compound command unexpectedly passed: %#v", result)
	}
}

func TestGoalCommandCheckRejectsUnapprovedExecutable(t *testing.T) {
	result := executeGoalCheck(t.TempDir(), map[string]interface{}{
		"check_id":          "check-1",
		"type":              "command",
		"command":           "sh -c 'exit 0'",
		"timeout_ms":        30_000,
		"definition_digest": "sha256:test",
	}, "sha256:workspace")
	if result["status"] != "error" || result["output"] != "check command is not allowed" {
		t.Fatalf("unapproved command result = %#v", result)
	}
}

func TestGoalCommandCheckRejectsSideEffectingAllowedExecutable(t *testing.T) {
	result := executeGoalCheck(t.TempDir(), map[string]interface{}{
		"check_id":          "check-1",
		"type":              "command",
		"command":           "npm publish",
		"timeout_ms":        30_000,
		"definition_digest": "sha256:test",
	}, "sha256:workspace")
	if result["status"] != "error" || result["output"] != "check command is not verification-safe" {
		t.Fatalf("side-effecting command result = %#v", result)
	}
}

func TestGoalReportValidationMatchesServerContract(t *testing.T) {
	valid := map[string]interface{}{
		"schema_version": 1, "outcome": "task_completed", "summary": "done",
		"evidence_refs": []interface{}{}, "completion_proposed": true,
	}
	if !validGoalReport(valid) {
		t.Fatal("valid report was rejected")
	}
	invalid := map[string]interface{}{
		"schema_version": 1, "outcome": "task_completed", "summary": 42,
		"evidence_refs": []interface{}{}, "completion_proposed": true,
	}
	if validGoalReport(invalid) {
		t.Fatal("server-invalid report was accepted")
	}
}

func TestGoalCommandCheckRejectsExecutablePath(t *testing.T) {
	root := t.TempDir()
	fakeNPM := filepath.Join(root, "npm")
	if err := os.WriteFile(fakeNPM, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	result := executeGoalCheck(root, map[string]interface{}{
		"check_id":          "check-1",
		"type":              "command",
		"command":           fakeNPM + " test",
		"timeout_ms":        30_000,
		"definition_digest": "sha256:test",
	}, "sha256:workspace")
	if result["status"] != "error" || result["output"] != "check executable must be resolved from PATH" {
		t.Fatalf("path executable result = %#v", result)
	}
}

func TestGoalWorkspaceFingerprintChangesWithFileContent(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "main.txt")
	if err := os.WriteFile(path, []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := goalWorkspaceFingerprint(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("two"), 0o600); err != nil {
		t.Fatal(err)
	}
	after, err := goalWorkspaceFingerprint(root)
	if err != nil {
		t.Fatal(err)
	}
	if before == after {
		t.Fatal("workspace fingerprint did not change")
	}
}

func TestAgentCapabilitiesAdvertiseGoalProtocol(t *testing.T) {
	caps := agentCapabilities()
	for _, want := range []string{
		"goal_server_v1",
		"goal_plan_service_v1",
		"goal_report_v1",
		"goal_verify_v1",
		"workspace_fingerprint_v1",
	} {
		if !agentAIStringSliceContains(caps, want) {
			t.Fatalf("capabilities %v missing %s", caps, want)
		}
	}
}

func TestCodexFeatureEnabledRecognizesGoalsOnlyWhenEnabled(t *testing.T) {
	output := "shell_tool stable true\ngoals stable true\nother experimental false\n"
	if !codexFeatureEnabled(output, "goals") {
		t.Fatal("enabled goals feature was not recognized")
	}
	if codexFeatureEnabled("goals stable false\n", "goals") {
		t.Fatal("disabled goals feature was recognized")
	}
}

func TestCodexNativeGoalSnapshotNormalizesAppServerShape(t *testing.T) {
	snapshot := codexNativeGoalSnapshot(map[string]interface{}{
		"threadId": "thread-1", "objective": "ship", "status": "active",
		"tokenBudget": float64(1000), "tokensUsed": float64(25),
		"timeUsedSeconds": float64(4), "createdAt": float64(10), "updatedAt": float64(11),
	})
	if snapshot["thread_id"] != "thread-1" || snapshot["token_budget"] != 1000 || snapshot["sync_state"] != "confirmed" {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	stale := codexNativeGoalStaleSnapshot(snapshot, "app-server unavailable")
	if stale["sync_state"] != "stale" || stale["sync_error"] != "app-server unavailable" {
		t.Fatalf("stale snapshot = %#v", stale)
	}
	if snapshot["sync_state"] != "confirmed" || snapshot["sync_error"] != nil {
		t.Fatalf("source snapshot mutated = %#v", snapshot)
	}
}

func TestRecoveredGoalRunReplaysFencedFailureTerminal(t *testing.T) {
	setupAgentIdentityTestEnv(t)
	m := newAgentAIManager()
	m.processedRuns["run-1"] = agentAIProcessedRun{
		RunID:          "run-1",
		ConversationID: "goal-1",
		MessageID:      "message-1",
		State:          "received",
		GoalIdentity:   testGoalIdentity(),
		Recovered:      true,
	}
	var replay map[string]interface{}
	if !m.replayProcessedRun("run-1", func(value interface{}) error {
		replay = value.(map[string]interface{})
		return nil
	}) {
		t.Fatal("expected recovered run to be replayed")
	}
	if replay["type"] != models.AgentEventAIError {
		t.Fatalf("type = %v, want %s", replay["type"], models.AgentEventAIError)
	}
	for key, want := range testGoalIdentity() {
		if replay[key] != want {
			t.Fatalf("%s = %v, want %v", key, replay[key], want)
		}
	}
	report, ok := replay["goal_report"].(map[string]interface{})
	if !ok || report["blocker_code"] != "agent_restarted_before_terminal" {
		t.Fatalf("goal_report = %#v", replay["goal_report"])
	}
	if stored := m.processedRuns["run-1"]; stored.State != "terminal" || stored.Terminal == nil {
		t.Fatalf("processed run was not completed: %#v", stored)
	}
}

// parseMarkedJSONObject 容错：provider 把 report JSON 包在 ```json 围栏里。
func TestParseMarkedJSONObjectHandlesCodeFence(t *testing.T) {
	output := "thinking...\n```json\n" + goalReportMarker + ` {"schema_version":1,"outcome":"task_completed","summary":"done","evidence_refs":[],"completion_proposed":true}` + "\n```"
	report, ok := parseMarkedJSONObject(output, goalReportMarker)
	if !ok {
		t.Fatal("expected fenced report to parse")
	}
	if report["outcome"] != "task_completed" {
		t.Fatalf("outcome = %v", report["outcome"])
	}
}

// parseMarkedJSONObject 容错：marker 出现在中间（thinking 复述指令后真 report）。
func TestParseMarkedJSONObjectHandlesMidTextMarker(t *testing.T) {
	output := goalReportMarker + ` {"schema_version":1,"outcome":"failed","summary":"cmd err","evidence_refs":[],"completion_proposed":false}` + "\ntrailing text"
	report, ok := parseMarkedJSONObject(output, goalReportMarker)
	if !ok {
		t.Fatal("expected mid-text report to parse")
	}
	if report["outcome"] != "failed" {
		t.Fatalf("outcome = %v", report["outcome"])
	}
}

// attachGoalReport fallback：provider 没输出 report，但有正常输出 → 乐观 task_completed。
func TestAttachGoalReportFallbackInfersCompletedFromOutput(t *testing.T) {
	var terminal map[string]interface{}
	emitter := newAgentAIRunEmitter(agentAIRun{
		runID: "run-1", goalIdentity: testGoalIdentity(),
	}, func(value interface{}) error {
		terminal = value.(map[string]interface{})
		return nil
	})
	_ = emitter.emit(map[string]interface{}{"type": models.AgentEventAIDelta, "delta": "Installed dependencies.\nRan tests: all passing."})
	_ = emitter.emit(map[string]interface{}{"type": models.AgentEventAIDone})
	report := terminal["goal_report"].(map[string]interface{})
	if report["outcome"] != "task_completed" {
		t.Fatalf("outcome = %v, want task_completed (fallback should be optimistic)", report["outcome"])
	}
	if report["completion_proposed"] != true {
		t.Fatalf("completion_proposed = %v", report["completion_proposed"])
	}
	if _, hasOutput := terminal["output"].(string); !hasOutput {
		t.Fatalf("output field not attached: %#v", terminal["output"])
	}
}

// attachGoalReport fallback：输出含 "command not found" → failed。
func TestAttachGoalReportFallbackDetectsErrorSignal(t *testing.T) {
	var terminal map[string]interface{}
	emitter := newAgentAIRunEmitter(agentAIRun{
		runID: "run-1", goalIdentity: testGoalIdentity(),
	}, func(value interface{}) error {
		terminal = value.(map[string]interface{})
		return nil
	})
	_ = emitter.emit(map[string]interface{}{"type": models.AgentEventAIDelta, "delta": "sh: npm: command not found"})
	_ = emitter.emit(map[string]interface{}{"type": models.AgentEventAIDone})
	report := terminal["goal_report"].(map[string]interface{})
	if report["outcome"] != "failed" {
		t.Fatalf("outcome = %v, want failed", report["outcome"])
	}
	if report["blocker_code"] != "task_command_failed" {
		t.Fatalf("blocker_code = %v", report["blocker_code"])
	}
}

// attachGoalReport fallback：AIError 事件 → failed。
func TestAttachGoalReportFallbackOnAIError(t *testing.T) {
	var terminal map[string]interface{}
	emitter := newAgentAIRunEmitter(agentAIRun{
		runID: "run-1", goalIdentity: testGoalIdentity(),
	}, func(value interface{}) error {
		terminal = value.(map[string]interface{})
		return nil
	})
	_ = emitter.emit(map[string]interface{}{"type": models.AgentEventAIDelta, "delta": "partial work"})
	_ = emitter.emit(map[string]interface{}{"type": models.AgentEventAIError, "error": "provider crashed"})
	report := terminal["goal_report"].(map[string]interface{})
	if report["outcome"] != "failed" || report["blocker_code"] != "provider_error" {
		t.Fatalf("goal_report = %#v", report)
	}
}

// output 字段截断到 16KB（尾部保留）。
func TestAttachGoalReportOutputIsTruncated(t *testing.T) {
	var terminal map[string]interface{}
	emitter := newAgentAIRunEmitter(agentAIRun{
		runID: "run-1", goalIdentity: testGoalIdentity(),
	}, func(value interface{}) error {
		terminal = value.(map[string]interface{})
		return nil
	})
	big := strings.Repeat("a", goalReportOutputByteLimit*2)
	_ = emitter.emit(map[string]interface{}{"type": models.AgentEventAIDelta, "delta": big})
	_ = emitter.emit(map[string]interface{}{"type": models.AgentEventAIDone})
	output := terminal["output"].(string)
	if len(output) != goalReportOutputByteLimit {
		t.Fatalf("output length = %d, want %d", len(output), goalReportOutputByteLimit)
	}
}

// 守格式的 report（严格末行）仍走最严格解析路径，且 output 被剥掉 marker 行。
func TestAttachGoalReportStrictReportAlsoGetsOutput(t *testing.T) {
	var terminal map[string]interface{}
	emitter := newAgentAIRunEmitter(agentAIRun{
		runID: "run-1", goalIdentity: testGoalIdentity(),
	}, func(value interface{}) error {
		terminal = value.(map[string]interface{})
		return nil
	})
	body := "Installing...\nRunning tests...\n"
	_ = emitter.emit(map[string]interface{}{"type": models.AgentEventAIDelta, "delta": body + goalReportMarker + ` {"schema_version":1,"outcome":"task_completed","summary":"done","evidence_refs":[],"completion_proposed":true}`})
	_ = emitter.emit(map[string]interface{}{"type": models.AgentEventAIDone})
	report := terminal["goal_report"].(map[string]interface{})
	if report["outcome"] != "task_completed" {
		t.Fatalf("outcome = %v", report["outcome"])
	}
	output := report["output"].(string)
	if strings.Contains(output, goalReportMarker) {
		t.Fatalf("output should not contain the report marker: %q", output)
	}
}

// goal task 的 Claude --append-system-prompt 被追加 report 强制段。
func TestWithGoalTaskReportSystemPromptAugmentsClaude(t *testing.T) {
	original := &agentAITool{
		id:   "claude",
		args: []string{"claude", "--append-system-prompt", "base", "--print", "task prompt"},
	}
	augmented := withGoalTaskReportSystemPrompt(original)
	if augmented.args[2] == "base" {
		t.Fatal("append-system-prompt value was not augmented")
	}
	if !strings.Contains(augmented.args[2], goalReportMarker) {
		t.Fatalf("augmented prompt missing ALIANG_GOAL_REPORT reference: %q", augmented.args[2])
	}
	if !strings.HasPrefix(augmented.args[2], "base") {
		t.Fatalf("augmented prompt should preserve original prefix: %q", augmented.args[2])
	}
	// non-claude 路径不被修改。
	codex := &agentAITool{id: "codex", args: []string{"codex", "exec", "task"}}
	if got := withGoalTaskReportSystemPrompt(codex); got != codex {
		t.Fatal("codex tool should pass through unchanged")
	}
	// 原对象未被 mutate。
	if original.args[2] != "base" {
		t.Fatalf("original tool mutated: %q", original.args[2])
	}
}

func TestCodexSandboxMode(t *testing.T) {
	if got := codexSandboxMode(true); got != "read-only" {
		t.Fatalf("readOnly=true -> %q, want read-only", got)
	}
	if got := codexSandboxMode(false); got != "workspace-write" {
		t.Fatalf("readOnly=false -> %q, want workspace-write", got)
	}
}

func TestGoalPlanReadOnlyTools(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "node_modules"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("alpha\nbeta\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "b.go"), []byte("package sub\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "node_modules", "c.txt"), []byte("alpha noise\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "big.txt"), bytes.Repeat([]byte("x"), goalPlanReadFileMaxBytes+1024), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Run("read_file reads within root", func(t *testing.T) {
		r := executeGoalPlanReadOnlyTool(root, "read_file", `{"path":"a.txt"}`)
		if r.isError {
			t.Fatalf("unexpected error: %s", r.content)
		}
		if !strings.Contains(r.content, "alpha") {
			t.Fatalf("missing content: %q", r.content)
		}
	})
	t.Run("read_file rejects escape", func(t *testing.T) {
		r := executeGoalPlanReadOnlyTool(root, "read_file", `{"path":"../secret"}`)
		if !r.isError {
			t.Fatalf("expected error, got %q", r.content)
		}
	})
	t.Run("read_file truncates over cap", func(t *testing.T) {
		r := executeGoalPlanReadOnlyTool(root, "read_file", `{"path":"big.txt"}`)
		if r.isError {
			t.Fatalf("unexpected error: %s", r.content)
		}
		if len(r.content) > goalPlanReadFileMaxBytes {
			t.Fatalf("content not truncated: %d bytes", len(r.content))
		}
	})
	t.Run("list_dir lists children", func(t *testing.T) {
		r := executeGoalPlanReadOnlyTool(root, "list_dir", `{"path":"."}`)
		if r.isError {
			t.Fatalf("unexpected error: %s", r.content)
		}
		if !strings.Contains(r.content, "sub") || !strings.Contains(r.content, "a.txt") {
			t.Fatalf("missing entries: %q", r.content)
		}
	})
	t.Run("list_dir rejects escape", func(t *testing.T) {
		r := executeGoalPlanReadOnlyTool(root, "list_dir", `{"path":".."}`)
		if !r.isError {
			t.Fatalf("expected error, got %q", r.content)
		}
	})
	t.Run("grep finds matches and skips node_modules", func(t *testing.T) {
		r := executeGoalPlanReadOnlyTool(root, "grep", `{"pattern":"alpha"}`)
		if r.isError {
			t.Fatalf("unexpected error: %s", r.content)
		}
		if !strings.Contains(r.content, "a.txt") {
			t.Fatalf("expected a.txt match: %q", r.content)
		}
		if strings.Contains(r.content, "node_modules") {
			t.Fatalf("node_modules not skipped: %q", r.content)
		}
	})
	t.Run("grep rejects invalid regex", func(t *testing.T) {
		r := executeGoalPlanReadOnlyTool(root, "grep", `{"pattern":"("}`)
		if !r.isError {
			t.Fatalf("expected error, got %q", r.content)
		}
	})
	t.Run("unknown tool errors", func(t *testing.T) {
		r := executeGoalPlanReadOnlyTool(root, "write_file", `{"path":"x"}`)
		if !r.isError {
			t.Fatalf("expected error, got %q", r.content)
		}
	})
	t.Run("read_file rejects a symlink escaping the project root", func(t *testing.T) {
		outside := t.TempDir()
		outsideFile := filepath.Join(outside, "secret.txt")
		if err := os.WriteFile(outsideFile, []byte("secret"), 0o600); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(root, "escape")
		if err := os.Symlink(outsideFile, link); err != nil {
			t.Fatal(err)
		}
		r := executeGoalPlanReadOnlyTool(root, "read_file", `{"path":"escape"}`)
		if !r.isError {
			t.Fatalf("expected symlink escape rejected, got %q", r.content)
		}
	})
}

func validGoalPlanProposal() map[string]interface{} {
	return map[string]interface{}{
		"schema_version": 1,
		"objective":      "ship it",
		"tasks": []map[string]interface{}{
			{
				"key":           "t1",
				"title":         "Task one",
				"allowed_roots": []string{"/workspace"},
				"checks": []map[string]interface{}{
					{"key": "c1", "type": "command", "command": "npm test", "required": true},
				},
			},
		},
	}
}

// assistantWithToolCalls builds an assistant message with tool_calls shaped as a
// JSON-unmarshalled []interface{} (the production shape the loop asserts).
func assistantWithToolCalls(calls ...map[string]interface{}) map[string]interface{} {
	arr := make([]interface{}, len(calls))
	for i, c := range calls {
		arr[i] = c
	}
	return map[string]interface{}{"role": "assistant", "content": "", "tool_calls": arr}
}

func toolCall(id, name, args string) map[string]interface{} {
	return map[string]interface{}{
		"id":   id,
		"type": "function",
		"function": map[string]interface{}{
			"name":      name,
			"arguments": args,
		},
	}
}

func assistantWithContent(content string) map[string]interface{} {
	return map[string]interface{}{"role": "assistant", "content": content}
}

func TestGoalPlanLoop(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("alpha\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	scan := map[string]interface{}{"files": []string{"a.txt"}}

	t.Run("returns proposal when model submits immediately", func(t *testing.T) {
		proposal := validGoalPlanProposal()
		args, _ := json.Marshal(map[string]interface{}{"proposal": proposal})
		var calls int
		caller := func(turn int, messages []map[string]interface{}) (map[string]interface{}, string, error) {
			calls++
			return assistantWithToolCalls(toolCall("c1", "submit_goal_plan", string(args))), "resp-1", nil
		}
		got, runID, err := runGoalPlanLoop(goalPlanLoopInput{
			projectRoot: root, evidence: scan,
			callServer: caller, maxTurns: 8,
		})
		if err != nil {
			t.Fatal(err)
		}
		if calls != 1 {
			t.Fatalf("calls=%d want 1", calls)
		}
		if runID != "resp-1" {
			t.Fatalf("runID=%q want resp-1", runID)
		}
		if fmt.Sprint(got["objective"]) != "ship it" {
			t.Fatalf("proposal=%v", got)
		}
	})

	t.Run("executes read_file then submits over two turns", func(t *testing.T) {
		proposal := validGoalPlanProposal()
		propArgs, _ := json.Marshal(map[string]interface{}{"proposal": proposal})
		var calls int
		caller := func(turn int, messages []map[string]interface{}) (map[string]interface{}, string, error) {
			calls++
			if turn == 1 {
				return assistantWithToolCalls(toolCall("c1", "read_file", `{"path":"a.txt"}`)), "resp-1", nil
			}
			return assistantWithToolCalls(toolCall("c2", "submit_goal_plan", string(propArgs))), "resp-2", nil
		}
		got, _, err := runGoalPlanLoop(goalPlanLoopInput{
			projectRoot: root, evidence: scan,
			callServer: caller, maxTurns: 8,
		})
		if err != nil {
			t.Fatal(err)
		}
		if calls != 2 {
			t.Fatalf("calls=%d want 2", calls)
		}
		if fmt.Sprint(got["objective"]) != "ship it" {
			t.Fatalf("proposal=%v", got)
		}
	})

	t.Run("appends tool result before the next turn", func(t *testing.T) {
		proposal := validGoalPlanProposal()
		propArgs, _ := json.Marshal(map[string]interface{}{"proposal": proposal})
		var turn2Messages []map[string]interface{}
		caller := func(turn int, messages []map[string]interface{}) (map[string]interface{}, string, error) {
			if turn == 2 {
				turn2Messages = messages
				return assistantWithToolCalls(toolCall("c2", "submit_goal_plan", string(propArgs))), "resp-2", nil
			}
			return assistantWithToolCalls(toolCall("c1", "read_file", `{"path":"a.txt"}`)), "resp-1", nil
		}
		if _, _, err := runGoalPlanLoop(goalPlanLoopInput{
			projectRoot: root, evidence: scan,
			callServer: caller, maxTurns: 8,
		}); err != nil {
			t.Fatal(err)
		}
		var hasToolResult bool
		for _, m := range turn2Messages {
			if fmt.Sprint(m["role"]) == "tool" && strings.Contains(fmt.Sprint(m["content"]), "alpha") {
				hasToolResult = true
			}
		}
		if !hasToolResult {
			t.Fatalf("tool result not appended before turn 2: %v", turn2Messages)
		}
	})

	t.Run("errors when max turns exhausted without a plan", func(t *testing.T) {
		caller := func(turn int, messages []map[string]interface{}) (map[string]interface{}, string, error) {
			return assistantWithToolCalls(toolCall("c1", "read_file", `{"path":"a.txt"}`)), "resp-x", nil
		}
		_, _, err := runGoalPlanLoop(goalPlanLoopInput{
			projectRoot: root, evidence: scan,
			callServer: caller, maxTurns: 2,
		})
		if err == nil || !strings.Contains(err.Error(), "planner_budget_exceeded") {
			t.Fatalf("want planner_budget_exceeded, got %v", err)
		}
	})

	t.Run("parses content fallback when no tool_calls", func(t *testing.T) {
		proposal := validGoalPlanProposal()
		propJSON, _ := json.Marshal(proposal)
		caller := func(turn int, messages []map[string]interface{}) (map[string]interface{}, string, error) {
			return assistantWithContent(string(propJSON)), "resp-1", nil
		}
		got, _, err := runGoalPlanLoop(goalPlanLoopInput{
			projectRoot: root, evidence: scan,
			callServer: caller, maxTurns: 8,
		})
		if err != nil {
			t.Fatal(err)
		}
		if fmt.Sprint(got["objective"]) != "ship it" {
			t.Fatalf("proposal=%v", got)
		}
	})

	t.Run("retries after an invalid submit_goal_plan", func(t *testing.T) {
		valid := validGoalPlanProposal()
		validArgs, _ := json.Marshal(map[string]interface{}{"proposal": valid})
		var calls int
		caller := func(turn int, messages []map[string]interface{}) (map[string]interface{}, string, error) {
			calls++
			if turn == 1 {
				return assistantWithToolCalls(toolCall("c1", "submit_goal_plan", `{"proposal":{"schema_version":1,"objective":"x"}}`)), "resp-1", nil
			}
			return assistantWithToolCalls(toolCall("c2", "submit_goal_plan", string(validArgs))), "resp-2", nil
		}
		got, _, err := runGoalPlanLoop(goalPlanLoopInput{
			projectRoot: root, evidence: scan,
			callServer: caller, maxTurns: 8,
		})
		if err != nil {
			t.Fatal(err)
		}
		if calls != 2 {
			t.Fatalf("calls=%d want 2 (retry after invalid submit)", calls)
		}
		if got["tasks"] == nil {
			t.Fatalf("expected valid proposal, got %v", got)
		}
	})

	t.Run("accepts a bare plan via fallback when not wrapped", func(t *testing.T) {
		proposal := validGoalPlanProposal()
		bareArgs, _ := json.Marshal(proposal)
		caller := func(turn int, messages []map[string]interface{}) (map[string]interface{}, string, error) {
			return assistantWithToolCalls(toolCall("c1", "submit_goal_plan", string(bareArgs))), "resp-1", nil
		}
		got, _, err := runGoalPlanLoop(goalPlanLoopInput{
			projectRoot: root, evidence: scan, callServer: caller, maxTurns: 8,
		})
		if err != nil {
			t.Fatal(err)
		}
		if fmt.Sprint(got["objective"]) != "ship it" {
			t.Fatalf("bare fallback proposal=%v", got)
		}
	})
}

func goalPlanGitInit(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{
		{"init", dir},
		{"-C", dir, "config", "user.email", "t@t.test"},
		{"-C", dir, "config", "user.name", "test"},
	} {
		if err := exec.Command("git", args...).Run(); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
}

func goalPlanGitCommit(t *testing.T, dir, msg string) {
	t.Helper()
	exec.Command("git", "-C", dir, "add", "-A").Run()
	c := exec.Command("git", "-C", dir, "commit", "-m", msg)
	c.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=t@t.test",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=t@t.test")
	if err := c.Run(); err != nil {
		t.Fatalf("git commit: %v", err)
	}
}

func goalPlanChangedPaths(ctx map[string]interface{}) []string {
	entries, _ := ctx["changed_files"].([]map[string]interface{})
	out := []string{}
	for _, e := range entries {
		out = append(out, fmt.Sprint(e["path"]))
	}
	return out
}

func anyContains(values []string, substr string) bool {
	for _, v := range values {
		if strings.Contains(v, substr) {
			return true
		}
	}
	return false
}

func TestBuildGoalPlanGitContext(t *testing.T) {
	t.Run("committed change plus untracked file", func(t *testing.T) {
		root := t.TempDir()
		goalPlanGitInit(t, root)
		if err := os.WriteFile(filepath.Join(root, "committed.txt"), []byte("v1"), 0o600); err != nil {
			t.Fatal(err)
		}
		goalPlanGitCommit(t, root, "init")
		if err := os.WriteFile(filepath.Join(root, "committed.txt"), []byte("v2"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "new.txt"), []byte("new"), 0o600); err != nil {
			t.Fatal(err)
		}
		ctx := buildGoalPlanGitContext(root)
		if ctx["available"] != true {
			t.Fatalf("available = %v, want true", ctx["available"])
		}
		if ctx["head_sha"] == "" {
			t.Fatalf("head_sha empty after a commit")
		}
		paths := goalPlanChangedPaths(ctx)
		if !anyContains(paths, "committed.txt") || !anyContains(paths, "new.txt") {
			t.Fatalf("changed_files missing entries: %v", paths)
		}
		patches, _ := ctx["patches"].([]map[string]interface{})
		if len(patches) != 1 || !anyContains([]string{fmt.Sprint(patches[0]["path"])}, "committed.txt") {
			t.Fatalf("patches = %v, want one for committed.txt", patches)
		}
		commits, _ := ctx["recent_commits"].([]map[string]interface{})
		if len(commits) != 1 {
			t.Fatalf("recent_commits = %v, want 1", commits)
		}
	})

	t.Run("unborn repo degrades per command", func(t *testing.T) {
		root := t.TempDir()
		goalPlanGitInit(t, root)
		if err := os.WriteFile(filepath.Join(root, "u.txt"), []byte("u"), 0o600); err != nil {
			t.Fatal(err)
		}
		ctx := buildGoalPlanGitContext(root)
		if ctx["available"] != true {
			t.Fatalf("available = %v, want true (status still works)", ctx["available"])
		}
		if ctx["head_sha"] != "" {
			t.Fatalf("head_sha = %v, want empty (unborn)", ctx["head_sha"])
		}
		paths := goalPlanChangedPaths(ctx)
		if !anyContains(paths, "u.txt") {
			t.Fatalf("untracked u.txt missing: %v", paths)
		}
		if patches, _ := ctx["patches"].([]map[string]interface{}); len(patches) != 0 {
			t.Fatalf("patches = %v, want empty (unborn)", patches)
		}
		if commits, _ := ctx["recent_commits"].([]map[string]interface{}); len(commits) != 0 {
			t.Fatalf("recent_commits = %v, want empty (unborn)", commits)
		}
	})

	t.Run("non-git dir unavailable", func(t *testing.T) {
		ctx := buildGoalPlanGitContext(t.TempDir())
		if ctx["available"] != false {
			t.Fatalf("available = %v, want false", ctx["available"])
		}
	})

	t.Run("monorepo sibling directory not leaked", func(t *testing.T) {
		repo := t.TempDir()
		goalPlanGitInit(t, repo)
		if err := os.MkdirAll(filepath.Join(repo, "a"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(repo, "b"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, "a", "x.txt"), []byte("a"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, "b", "y.txt"), []byte("b"), 0o600); err != nil {
			t.Fatal(err)
		}
		goalPlanGitCommit(t, repo, "init")
		if err := os.WriteFile(filepath.Join(repo, "a", "x.txt"), []byte("aa"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, "b", "y.txt"), []byte("bb"), 0o600); err != nil {
			t.Fatal(err)
		}
		// projectPath = repo/a; sibling repo/b's changes must NOT appear.
		projectPath := filepath.Join(repo, "a")
		ctx := buildGoalPlanGitContext(projectPath)
		paths := goalPlanChangedPaths(ctx)
		var changed string
		for _, p := range paths {
			if strings.Contains(p, "x.txt") {
				changed = p
			}
			if strings.Contains(p, "y.txt") {
				t.Fatalf("sibling y.txt leaked into projectPath context: %v", paths)
			}
		}
		if changed == "" {
			t.Fatalf("projectPath change x.txt missing: %v", paths)
		}
		// Path must be projectPath-relative (not repo-root "a/x.txt"), else the
		// model's read_file would resolve to repo/a/a/x.txt.
		if changed != "x.txt" {
			t.Fatalf("path not projectPath-relative: %q (would break read_file)", changed)
		}
		// Round-trip: the model can read the changed file via read_file using the
		// projectPath-relative path git reported.
		read := executeGoalPlanReadOnlyTool(projectPath, "read_file", fmt.Sprintf(`{"path":%q}`, changed))
		if read.isError {
			t.Fatalf("read_file(%q) failed: %s", changed, read.content)
		}
	})

	t.Run("rename parses path without leaking the v2 score field", func(t *testing.T) {
		root := t.TempDir()
		goalPlanGitInit(t, root)
		if err := os.WriteFile(filepath.Join(root, "old.txt"), []byte("v1"), 0o600); err != nil {
			t.Fatal(err)
		}
		goalPlanGitCommit(t, root, "init")
		if err := os.Rename(filepath.Join(root, "old.txt"), filepath.Join(root, "new.txt")); err != nil {
			t.Fatal(err)
		}
		exec.Command("git", "-C", root, "add", "-A").Run()
		ctx := buildGoalPlanGitContext(root)
		paths := goalPlanChangedPaths(ctx)
		if !anyContains(paths, "new.txt") {
			t.Fatalf("rename target new.txt missing: %v", paths)
		}
		for _, p := range paths {
			if strings.Contains(p, "R100") || strings.HasPrefix(p, "R") {
				t.Fatalf("rename score leaked into path: %v", paths)
			}
		}
	})
}
