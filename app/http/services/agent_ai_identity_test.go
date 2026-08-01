package services

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

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
	writeFakeExecutable(t, binDir, "claude", `#!/bin/sh
if [ "$1" = "--version" ]; then
  printf '%s\n' '2.1.17 (Claude Code)'
  exit 0
fi
printf x >> "$COUNTER_FILE"
printf '%s\n' '{"type":"system","subtype":"init","session_id":"native-claude-1"}'
printf '%s\n' '{"type":"assistant","message":{"content":[{"type":"text","text":"ok"}]}}'
printf '%s\n' '{"type":"result","subtype":"success","session_id":"native-claude-1","result":"ok"}'
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
		"provider":     "claude",
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

func TestCanonicalInternalInventoryID(t *testing.T) {
	cases := []struct{ provider, native, want string }{
		{"claude", "abc-123", "claude_abc-123"},
		{"claudecode", "abc-123", "claude_abc-123"}, // tool.id is claudecode, but scanner emits claude_
		{"CLAUDE", "x", "claude_x"},                 // case-insensitive
		{"codex", "thread-9", "codex_thread-9"},
		{"codex", "", ""},    // empty native -> empty key (not recorded)
		{"unknown", "y", ""}, // unknown provider -> empty key
	}
	for _, c := range cases {
		if got := canonicalInternalInventoryID(c.provider, c.native); got != c.want {
			t.Errorf("canonicalInternalInventoryID(%q,%q)=%q want %q", c.provider, c.native, got, c.want)
		}
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

func TestRememberPlannerNativeSession(t *testing.T) {
	m := newAgentAIManager()
	// First insert returns true (new); duplicate returns false (idempotent).
	if !m.rememberPlannerNativeSession("claude", "sid-1") {
		t.Fatal("首次插入应为新增")
	}
	if m.rememberPlannerNativeSession("claude", "sid-1") {
		t.Fatal("重复插入不应算新增")
	}
	// claudecode normalizes to the same key as claude.
	m.rememberPlannerNativeSession("claudecode", "sid-1")
	// Unknown provider / empty native not recorded, not new.
	if m.rememberPlannerNativeSession("foo", "x") {
		t.Fatal("未知 provider 不应记")
	}
	if m.rememberPlannerNativeSession("claude", "") {
		t.Fatal("空 native 不应记")
	}
	snap := m.internalNativeSessionSnapshot()
	if !snap["claude_sid-1"] {
		t.Errorf("snapshot 缺 claude_sid-1: %v", snap)
	}
}

func TestPruneInternalSessionsPerProviderBucket(t *testing.T) {
	m := newAgentAIManager()
	// Deterministic timestamps (avoid sub-microsecond collisions). Codex cap+10,
	// c-0 oldest; one Claude entry.
	base := time.Now().UTC()
	for i := 0; i < agentInternalSessionBucketCap+10; i++ {
		m.internalNativeSessions[fmt.Sprintf("codex_c-%d", i)] = rememberedInternalSession{
			Provider:        "codex",
			NativeSessionID: fmt.Sprintf("c-%d", i),
			CapturedAt:      base.Add(time.Duration(i) * time.Second),
		}
	}
	m.internalNativeSessions["claude_keep"] = rememberedInternalSession{
		Provider: "claude", NativeSessionID: "keep", CapturedAt: base,
	}
	m.mu.Lock()
	pruneInternalSessionsLocked(m.internalNativeSessions)
	m.mu.Unlock()
	snap := m.internalNativeSessionSnapshot()
	gotCodex := 0
	for k := range snap {
		if strings.HasPrefix(k, "codex_") {
			gotCodex++
		}
	}
	if gotCodex != agentInternalSessionBucketCap {
		t.Errorf("codex 桶 = %d, want %d", gotCodex, agentInternalSessionBucketCap)
	}
	if !snap["claude_keep"] {
		t.Error("Claude 不应被 Codex 挤掉（按 provider 分桶）")
	}
	if snap["codex_c-0"] {
		t.Error("最旧 codex_c-0 应被淘汰")
	}
	if !snap[fmt.Sprintf("codex_c-%d", agentInternalSessionBucketCap+9)] {
		t.Error("最新 codex 条目应保留")
	}
}

func TestInternalNativeSessionSnapshotIsolation(t *testing.T) {
	m := newAgentAIManager()
	m.rememberPlannerNativeSession("claude", "sid")
	snap := m.internalNativeSessionSnapshot()
	snap["claude_injected"] = true // mutating the returned snapshot must not affect the manager
	if m.internalNativeSessionSnapshot()["claude_injected"] {
		t.Error("snapshot 必须是拷贝，外部篡改不应泄漏回 manager")
	}
}
