package services

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestNormalizeAgentAIProviderAcceptsOpenCodeAliases(t *testing.T) {
	for _, raw := range []string{"opencode", "open-code", "open_code"} {
		got, err := normalizeAgentAIProvider(raw)
		if err != nil {
			t.Fatalf("normalizeAgentAIProvider(%q) error = %v", raw, err)
		}
		if got != "opencode" {
			t.Fatalf("normalizeAgentAIProvider(%q) = %q, want opencode", raw, got)
		}
	}
}

func TestResolveNamedAgentAIToolOpenCodeArgs(t *testing.T) {
	binDir := t.TempDir()
	writeFakeExecutable(t, binDir, "opencode", "#!/bin/sh\nexit 0\n")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	tool, err := resolveNamedAgentAITool("opencode", "hello from phone", "anthropic/claude-sonnet-4-5", "high", "opencode-session-1")
	if err != nil {
		t.Fatalf("resolveNamedAgentAITool(opencode) error = %v", err)
	}
	if tool.id != "opencode" || tool.outputFormat != agentAIOutputOpenCodeJSON {
		t.Fatalf("tool = %+v", tool)
	}
	want := []string{"run", "--format", "json", "--session", "opencode-session-1", "--model", "anthropic/claude-sonnet-4-5", "--variant", "high", "hello from phone"}
	if strings.Join(tool.args, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("args = %#v, want %#v", tool.args, want)
	}
}

func TestExtractOpenCodeJSONTextsAndSessionID(t *testing.T) {
	delta := map[string]interface{}{
		"type": "message.part.delta",
		"properties": map[string]interface{}{
			"sessionID": "op-sid",
			"messageID": "msg-1",
			"partID":    "part-1",
			"delta":     " hello",
		},
	}
	if got := openCodeSessionID(delta); got != "op-sid" {
		t.Fatalf("openCodeSessionID(delta) = %q, want op-sid", got)
	}
	texts := extractOpenCodeJSONTexts(delta, false)
	if len(texts) != 1 || texts[0] != " hello" {
		t.Fatalf("delta texts = %#v, want [\" hello\"]", texts)
	}

	partUpdate := map[string]interface{}{
		"type": "message.part.updated",
		"properties": map[string]interface{}{
			"part": map[string]interface{}{
				"type": "text",
				"text": "final part",
			},
		},
	}
	texts = extractOpenCodeJSONTexts(partUpdate, true)
	if len(texts) != 1 || texts[0] != "final part" {
		t.Fatalf("part update texts = %#v, want [final part]", texts)
	}

	messageUpdate := map[string]interface{}{
		"type": "message.updated",
		"properties": map[string]interface{}{
			"message": map[string]interface{}{
				"role": "assistant",
				"parts": []interface{}{
					map[string]interface{}{"type": "text", "text": "done"},
					map[string]interface{}{"type": "tool", "text": "skip me"},
				},
			},
		},
	}
	texts = extractOpenCodeJSONTexts(messageUpdate, true)
	if len(texts) != 1 || texts[0] != "done" {
		t.Fatalf("message update texts = %#v, want [done]", texts)
	}
}

func TestOpenCodeStructuredEventsAndErrors(t *testing.T) {
	mu, events, writer := captureAIWriter(t)
	run := agentAIRun{sessionID: "s", messageID: "m", activity: newAgentAIActivity()}
	emitOpenCodeStructuredEvents(agentAIOutputOpenCodeJSON, map[string]interface{}{
		"type": "message.part.updated",
		"properties": map[string]interface{}{"part": map[string]interface{}{
			"type": "reasoning", "text": "分析中",
		}},
	}, run, writer)
	emitOpenCodeStructuredEvents(agentAIOutputOpenCodeJSON, map[string]interface{}{
		"type": "message.part.updated",
		"properties": map[string]interface{}{"part": map[string]interface{}{
			"type": "tool", "tool": "bash", "callID": "tool-1",
			"state": map[string]interface{}{"status": "completed", "input": map[string]interface{}{"command": "go test ./..."}, "output": "ok"},
		}},
	}, run, writer)
	emitOpenCodeStructuredEvents(agentAIOutputOpenCodeJSON, map[string]interface{}{
		"type": "message.updated",
		"properties": map[string]interface{}{"message": map[string]interface{}{
			"tokens": map[string]interface{}{"input": 12, "output": 7, "reasoning": 3, "cache": map[string]interface{}{"read": 2}},
		}},
	}, run, writer)
	if len(findAIEvents(mu, events, "ai.thinking")) != 1 || len(findAIEvents(mu, events, "ai.command")) != 1 || len(findAIEvents(mu, events, "ai.usage")) != 1 {
		t.Fatalf("structured events = %#v", *events)
	}

	reason, ok := openCodeEventError(map[string]interface{}{
		"type": "session.error", "properties": map[string]interface{}{"error": map[string]interface{}{"message": "provider failed"}},
	})
	if !ok || reason != "provider failed" {
		t.Fatalf("openCodeEventError = %q, %v", reason, ok)
	}
}

func TestAgentAIManagerRejectsOpenCodeWithoutApprovalBridge(t *testing.T) {
	projectPath := setupAgentExecutionProjectForTest(t)
	binDir := t.TempDir()
	writeFakeExecutable(t, binDir, "opencode", `#!/bin/sh
printf '%s\n' '{"type":"session.updated","properties":{"sessionID":"op-sid"}}'
printf '%s\n' '{"type":"message.part.delta","properties":{"sessionID":"op-sid","messageID":"msg-1","partID":"part-1","delta":"你"}}'
printf '%s\n' '{"type":"message.part.delta","properties":{"sessionID":"op-sid","messageID":"msg-1","partID":"part-1","delta":"好"}}'
`)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	manager := newAgentAIManager()
	defer manager.closeAll()
	mu, events, writer := captureAIWriter(t)

	manager.create(map[string]interface{}{
		"type":         "ai.session.create",
		"session_id":   "s-opencode",
		"message_id":   "m-create",
		"provider":     "opencode",
		"project_path": projectPath,
	}, writer)
	manager.message(map[string]interface{}{
		"type":         "ai.message",
		"session_id":   "s-opencode",
		"message_id":   "m-run",
		"provider":     "opencode",
		"project_path": projectPath,
		"content":      "say hi",
	}, writer)

	failed := waitForAgentEvent(t, mu, events, "ai.error", func(ev map[string]interface{}) bool {
		return remoteString(ev, "session_id") == "s-opencode"
	})
	if !strings.Contains(remoteString(failed, "error"), "approval bridge") {
		t.Fatalf("error event = %+v", failed)
	}

	manager.mu.Lock()
	session := manager.sessions["s-opencode"]
	manager.mu.Unlock()
	if session == nil {
		t.Fatal("rejected OpenCode session was unexpectedly removed")
	}
	if session.resumeSessionID != "" {
		t.Fatalf("rejected OpenCode run persisted resumeSessionID = %q", session.resumeSessionID)
	}
}

func writeFakeExecutable(t *testing.T, dir, name, content string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		name += ".bat"
		if !strings.HasPrefix(content, "@echo") {
			content = "@echo off\r\n" + content
		}
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o700); err != nil {
		t.Fatalf("write fake executable %s: %v", path, err)
	}
}

func joinAgentDeltas(mu interface {
	Lock()
	Unlock()
}, events *[]map[string]interface{}) string {
	mu.Lock()
	defer mu.Unlock()
	var b strings.Builder
	for _, ev := range *events {
		if remoteString(ev, "type") == "ai.delta" {
			b.WriteString(remoteString(ev, "content"))
			b.WriteString(remoteString(ev, "text"))
			b.WriteString(remoteString(ev, "delta"))
		}
	}
	return b.String()
}
