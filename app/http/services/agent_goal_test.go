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
		{id: "codex", wantArgs: []string{"--sandbox", "read-only", "--ignore-user-config"}},
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
