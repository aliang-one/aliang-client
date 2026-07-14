package services

import (
	"os"
	"strings"
	"testing"

	"aliang.one/nursorgate/app/http/models"
)

func TestClaudeCodeBindingArgsAreMutuallyExclusive(t *testing.T) {
	tool := newClaudeCodeAITool("claudecode", "/bin/claude", "hello", "", "", "", "new-native-id")
	args := strings.Join(tool.args, " ")
	if !strings.Contains(args, "--session-id new-native-id") {
		t.Fatalf("new-session args = %q, want --session-id", args)
	}
	if strings.Contains(args, "--resume") {
		t.Fatalf("new-session args = %q, must not contain --resume", args)
	}

	tool = newClaudeCodeAITool("claudecode", "/bin/claude", "hello", "", "", "existing-native-id", "ignored-new-id")
	args = strings.Join(tool.args, " ")
	if !strings.Contains(args, "--resume existing-native-id") {
		t.Fatalf("resume args = %q, want --resume", args)
	}
	if strings.Contains(args, "--session-id") {
		t.Fatalf("resume args = %q, must not contain --session-id", args)
	}
}

func TestRunStartRedeliveryLaunchesProviderOnce(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	projectPath := setupAgentExecutionProjectForTest(t)
	setAgentAuthorizedExecutionDirectoriesCache([]string{projectPath})
	binDir := t.TempDir()
	counterPath := t.TempDir() + "/launches"
	t.Setenv("COUNTER_FILE", counterPath)
	writeFakeExecutable(t, binDir, "opencode", `#!/bin/sh
printf x >> "$COUNTER_FILE"
printf '%s\n' '{"type":"session.updated","properties":{"sessionID":"native-opencode-1"}}'
printf '%s\n' '{"type":"message.part.delta","properties":{"sessionID":"native-opencode-1","delta":"ok"}}'
`)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	manager := newAgentAIManager()
	defer manager.closeAll()
	mu, events, writer := captureAIWriter(t)
	start := map[string]interface{}{
		"type":         "ai.run.start",
		"session_id":   "managed-conversation-1",
		"run_id":       "run-idempotent-1",
		"message_id":   "message-idempotent-1",
		"provider":     "opencode",
		"project_path": projectPath,
		"content":      "hello",
	}
	manager.runStart(start, writer)
	waitForAgentEvent(t, mu, events, "ai.done", func(event map[string]interface{}) bool {
		return remoteString(event, "run_id") == "run-idempotent-1"
	})

	manager.runStart(start, writer)
	raw, err := os.ReadFile(counterPath)
	if err != nil {
		t.Fatalf("read provider launch counter: %v", err)
	}
	if got := string(raw); got != "x" {
		t.Fatalf("provider launches = %d, want 1", len(got))
	}
}

func TestManagedInventoryUsesBindingAlias(t *testing.T) {
	manager := newAgentAIManager()
	manager.bindings["ai-canonical"] = agentAIBindingRecord{
		ConversationID:  "ai-canonical",
		Provider:        "claudecode",
		NativeSessionID: "native-claude-1",
		State:           "reserved",
		BindingVersion:  1,
	}
	sessions := manager.annotateManagedVibeSessions([]models.AgentVibeSession{
		{ID: "claude_native-claude-1", Provider: "claude"},
		{ID: "claude_external-1", Provider: "claude"},
	})
	if sessions[0].Origin != "managed" || sessions[0].ManagedConversationID != "ai-canonical" {
		t.Fatalf("managed inventory = %+v", sessions[0])
	}
	if sessions[1].Origin != "external" || sessions[1].ManagedConversationID != "" {
		t.Fatalf("external inventory = %+v", sessions[1])
	}
}
