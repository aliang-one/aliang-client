package services

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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

func TestGoalPlannerUsesProviderNativeReadOnlyPolicies(t *testing.T) {
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
			readOnly := withGoalPlanningReadOnly(original)
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

func TestGoalPlannerPreflightRejectsMissingClaudeProviderConfiguration(t *testing.T) {
	t.Setenv("ANTHROPIC_BASE_URL", "")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	err := preflightGoalPlannerProvider("claudecode", "", "")
	if err == nil || !strings.HasPrefix(err.Error(), "planner_provider_unconfigured:") {
		t.Fatalf("preflight error = %v, want planner_provider_unconfigured", err)
	}
}

func TestGoalPlanningSessionPinsNativeResumeID(t *testing.T) {
	m := newAgentAIManager()
	m.ensureGoalPlanningSession("goal-plan:test", t.TempDir(), "claudecode", "", "")
	m.bindings["goal-plan:test"] = agentAIBindingRecord{
		ConversationID:  "goal-plan:test",
		Provider:        "claudecode",
		NativeSessionID: "stale-planner-session",
		State:           "confirmed",
	}
	m.setAgentAIResumeSessionIDIfEmpty("goal-plan:test", 0, "native-session-1")
	if got := m.resumeSessionIDFor("goal-plan:test"); got != "native-session-1" {
		t.Fatalf("resume session id = %q, want native-session-1", got)
	}
	m.removeGoalPlanningSession("goal-plan:test")
	if got := m.resumeSessionIDFor("goal-plan:test"); got != "" {
		t.Fatalf("removed planner session still has resume id %q", got)
	}
	if _, ok := m.bindings["goal-plan:test"]; ok {
		t.Fatal("removed planner session retained a persistent binding")
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
		"goal_plan_readonly_v1",
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
