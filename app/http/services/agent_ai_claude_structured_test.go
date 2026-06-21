package services

import (
	"reflect"
	"testing"
)

func TestClaudeEditLineDelta(t *testing.T) {
	tests := []struct {
		name           string
		toolName       string
		input          map[string]interface{}
		added, removed int
	}{
		{
			name:     "write counts content lines as additions",
			toolName: "Write",
			input:    map[string]interface{}{"content": "alpha\nbeta\ngamma\n"},
			added:    3, removed: 0,
		},
		{
			name:     "edit compares old vs new strings",
			toolName: "Edit",
			input:    map[string]interface{}{"old_string": "x\ny", "new_string": "x\ny\nz\nw"},
			added:    4, removed: 2,
		},
		{
			name:     "multiedit sums across edits",
			toolName: "MultiEdit",
			input: map[string]interface{}{"edits": []interface{}{
				map[string]interface{}{"old_string": "a", "new_string": "a\nb"},
				map[string]interface{}{"old_string": "c\nd", "new_string": "c"},
			}},
			added: 3, removed: 3,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			added, removed := claudeEditLineDelta(tt.toolName, tt.input)
			if added != tt.added || removed != tt.removed {
				t.Fatalf("claudeEditLineDelta(%s) = (+%d,-%d), want (+%d,-%d)", tt.toolName, added, removed, tt.added, tt.removed)
			}
		})
	}
}

func TestClaudeToolUseEvents(t *testing.T) {
	run := agentAIRun{sessionID: "s", messageID: "m", runSeq: 1}
	pending := map[string]string{}

	content := []interface{}{
		map[string]interface{}{"type": "text", "text": "thinking aloud"},
		map[string]interface{}{
			"type":  "tool_use",
			"id":    "call_bash",
			"name":  "Bash",
			"input": map[string]interface{}{"command": "ls -la", "cwd": "/repo"},
		},
		map[string]interface{}{
			"type":  "tool_use",
			"id":    "call_write",
			"name":  "Write",
			"input": map[string]interface{}{"file_path": "/repo/a.txt", "content": "one\ntwo\n"},
		},
		map[string]interface{}{
			"type":  "tool_use",
			"id":    "call_read",
			"name":  "Read",
			"input": map[string]interface{}{"file_path": "/repo/b.txt"},
		},
		map[string]interface{}{
			"type": "tool_use",
			"id":   "call_todo",
			"name": "TodoWrite",
			"input": map[string]interface{}{"todos": []interface{}{
				map[string]interface{}{"content": "do x", "status": "in_progress", "activeForm": "doing x"},
				map[string]interface{}{"content": "do y", "status": "pending", "activeForm": ""},
			}},
		},
	}

	events := claudeToolUseEvents(content, run, pending)

	var cmd, fc, task map[string]interface{}
	for _, ev := range events {
		switch ev["type"] {
		case "ai.command":
			cmd = ev
		case "ai.file_change":
			fc = ev
		case "ai.task":
			task = ev
		}
	}

	if cmd == nil {
		t.Fatal("Bash tool_use should produce an ai.command event")
	}
	if remoteString(cmd, "status") != "started" || remoteString(cmd, "command") != "ls -la" || remoteString(cmd, "cwd") != "/repo" {
		t.Fatalf("ai.command = %#v, want started/ls -la//repo", cmd)
	}
	if pending["call_bash"] != "ls -la" {
		t.Fatalf("pending[called_bash] = %q, want ls -la (for tool_result correlation)", pending["call_bash"])
	}

	if fc == nil {
		t.Fatal("Write tool_use should produce an ai.file_change event")
	}
	if remoteString(fc, "path") != "/repo/a.txt" || remoteString(fc, "kind") != "create" {
		t.Fatalf("ai.file_change = %#v, want /repo/a.txt/create", fc)
	}
	if added, _ := eventInt(fc, "added"); added != 2 {
		t.Fatalf("file_change added = %v, want 2", fc["added"])
	}

	if task == nil {
		t.Fatal("TodoWrite tool_use should produce an ai.task event")
	}
	tasks, ok := task["tasks"].([]map[string]interface{})
	if !ok || len(tasks) != 2 {
		t.Fatalf("ai.task tasks = %#v, want 2 entries", task["tasks"])
	}
	if remoteString(tasks[0], "subject") != "do x" || remoteString(tasks[0], "status") != "in_progress" || remoteString(tasks[0], "active_form") != "doing x" {
		t.Fatalf("task[0] = %#v, want subject=do x status=in_progress active_form=doing x", tasks[0])
	}

	if len(events) != 3 {
		t.Fatalf("events count = %d, want 3 (Read must be skipped)", len(events))
	}
}

func TestClaudeToolResultEvents(t *testing.T) {
	run := agentAIRun{sessionID: "s", messageID: "m", runSeq: 1}
	pending := map[string]string{"call_bash": "ls -la"}

	t.Run("matching result closes command with output", func(t *testing.T) {
		content := []interface{}{
			map[string]interface{}{"type": "tool_result", "tool_use_id": "call_bash", "content": "total 0\n"},
		}
		events := claudeToolResultEvents(content, run, pending)
		if len(events) != 1 || remoteString(events[0], "type") != "ai.command" {
			t.Fatalf("events = %#v, want one ai.command", events)
		}
		ev := events[0]
		if remoteString(ev, "status") != "completed" {
			t.Fatalf("status = %q, want completed", remoteString(ev, "status"))
		}
		if remoteString(ev, "command") != "ls -la" {
			t.Fatalf("command = %q, want ls -la (recalled from pending)", remoteString(ev, "command"))
		}
		if remoteString(ev, "output") != "total 0\n" {
			t.Fatalf("output = %q, want total 0\\n", remoteString(ev, "output"))
		}
		if code, _ := eventInt(ev, "exit_code"); code != 0 {
			t.Fatalf("exit_code = %v, want 0", ev["exit_code"])
		}
		if _, stillPending := pending["call_bash"]; stillPending {
			t.Fatal("pending[called_bash] should be cleared after result")
		}
	})

	t.Run("error result surfaces nonzero exit code", func(t *testing.T) {
		pending := map[string]string{"call_err": "false"}
		content := []interface{}{
			map[string]interface{}{"type": "tool_result", "tool_use_id": "call_err", "content": "boom", "is_error": true},
		}
		events := claudeToolResultEvents(content, run, pending)
		if len(events) != 1 {
			t.Fatalf("events = %d, want 1", len(events))
		}
		if code, _ := eventInt(events[0], "exit_code"); code != 1 {
			t.Fatalf("exit_code = %v, want 1 for is_error result", events[0]["exit_code"])
		}
	})

	t.Run("unknown tool_use_id ignored", func(t *testing.T) {
		content := []interface{}{
			map[string]interface{}{"type": "tool_result", "tool_use_id": "call_orphan", "content": "?"},
		}
		if got := claudeToolResultEvents(content, run, pending); len(got) != 0 {
			t.Fatalf("events = %#v, want none for unknown id", got)
		}
	})
}

func TestClaudeUsageFromAssistant(t *testing.T) {
	t.Run("extracts usage fields", func(t *testing.T) {
		message := map[string]interface{}{
			"usage": map[string]interface{}{
				"input_tokens":  float64(100),
				"output_tokens": float64(20),
			},
		}
		ev := claudeUsageEvent(message)
		if ev == nil {
			t.Fatal("expected usage event, got nil")
		}
		if in, _ := eventInt(ev, "input_tokens"); in != 100 {
			t.Fatalf("input_tokens = %v, want 100", ev["input_tokens"])
		}
		if out, _ := eventInt(ev, "output_tokens"); out != 20 {
			t.Fatalf("output_tokens = %v, want 20", ev["output_tokens"])
		}
	})

	t.Run("nil when no usage", func(t *testing.T) {
		if ev := claudeUsageEvent(map[string]interface{}{}); !reflect.ValueOf(ev).IsNil() {
			t.Fatalf("expected nil, got %#v", ev)
		}
	})
}
