package services

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// codexTurnResult inspects a codex `turn/completed` notification params payload
// and reports the terminal status the agent should surface, plus any error
// message carried by a failed turn. Status is one of "completed", "interrupted",
// "failed", or "" when the payload is missing/unknown.
func TestCodexTurnResult(t *testing.T) {
	tests := []struct {
		name   string
		params map[string]interface{}
		status string
		errMsg string
	}{
		{
			name:   "completed",
			params: map[string]interface{}{"turn": map[string]interface{}{"status": "completed"}},
			status: "completed",
		},
		{
			name: "failed surfaces error message",
			params: map[string]interface{}{"turn": map[string]interface{}{
				"status": "failed",
				"error":  map[string]interface{}{"message": "Context window exceeded"},
			}},
			status: "failed",
			errMsg: "Context window exceeded",
		},
		{
			name: "failed falls back to additionalDetails",
			params: map[string]interface{}{"turn": map[string]interface{}{
				"status": "failed",
				"error":  map[string]interface{}{"additionalDetails": "upstream 502"},
			}},
			status: "failed",
			errMsg: "upstream 502",
		},
		{
			name:   "interrupted",
			params: map[string]interface{}{"turn": map[string]interface{}{"status": "interrupted"}},
			status: "interrupted",
		},
		{
			name:   "missing turn payload",
			params: map[string]interface{}{},
			status: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, errMsg := codexTurnResult(tt.params)
			if status != tt.status {
				t.Fatalf("status = %q, want %q", status, tt.status)
			}
			if errMsg != tt.errMsg {
				t.Fatalf("errMsg = %q, want %q", errMsg, tt.errMsg)
			}
		})
	}
}

// The fileChange approval request itself carries no diff (per the app-server
// protocol); the proposed edits arrive in the prior item/started fileChange
// item. extractCodexFileChangeItem pulls them out keyed by itemId so the loop
// can attach them to the approval request.
func TestExtractCodexFileChangeItem(t *testing.T) {
	t.Run("fileChange started captures changes", func(t *testing.T) {
		msg := map[string]interface{}{
			"method": "item/started",
			"params": map[string]interface{}{
				"item": map[string]interface{}{
					"type": "fileChange",
					"id":   "fc_1",
					"changes": []interface{}{
						map[string]interface{}{"path": "/repo/a.go", "kind": "edit", "diff": "@@ ..."},
					},
				},
			},
		}
		itemID, changes := extractCodexFileChangeItem(msg)
		if itemID != "fc_1" {
			t.Fatalf("itemID = %q, want fc_1", itemID)
		}
		var parsed []map[string]interface{}
		if err := json.Unmarshal(changes, &parsed); err != nil {
			t.Fatalf("changes not valid JSON: %v", err)
		}
		if len(parsed) != 1 || parsed[0]["path"] != "/repo/a.go" {
			t.Fatalf("changes = %#v, want one entry for /repo/a.go", parsed)
		}
	})

	t.Run("non-fileChange item ignored", func(t *testing.T) {
		msg := map[string]interface{}{
			"method": "item/started",
			"params": map[string]interface{}{
				"item": map[string]interface{}{"type": "commandExecution", "id": "cmd_1"},
			},
		}
		itemID, changes := extractCodexFileChangeItem(msg)
		if itemID != "" || changes != nil {
			t.Fatalf("got (%q, %v), want (\"\", nil) for non-fileChange item", itemID, changes)
		}
	})
}

// buildCodexApprovalRequest must populate FileChanges from the tracked item
// payload (passed in), NOT from the requestApproval params — which the protocol
// never sends. The legacy params[fileChanges] fallback stays for old codex.
func TestBuildCodexApprovalRequestFileChange(t *testing.T) {
	run := agentAIRun{sessionID: "s", messageID: "m", runSeq: 1}
	trackedChanges, _ := json.Marshal([]map[string]interface{}{
		{"path": "/repo/a.go", "kind": "edit"},
	})

	t.Run("uses tracked changes from item/started", func(t *testing.T) {
		req := buildCodexApprovalRequest(run, "item/fileChange/requestApproval", map[string]interface{}{
			"itemId": "fc_1",
		}, trackedChanges)
		if req.Kind != "file_change" {
			t.Fatalf("Kind = %q, want file_change", req.Kind)
		}
		if string(req.FileChanges) != string(trackedChanges) {
			t.Fatalf("FileChanges = %s, want tracked %s", req.FileChanges, trackedChanges)
		}
	})

	t.Run("falls back to legacy params when no tracked changes", func(t *testing.T) {
		legacy := []interface{}{map[string]interface{}{"path": "/repo/b.go"}}
		req := buildCodexApprovalRequest(run, "item/fileChange/requestApproval", map[string]interface{}{
			"fileChanges": legacy,
		}, nil)
		if len(req.FileChanges) == 0 {
			t.Fatal("FileChanges empty; legacy params fallback should still populate it")
		}
	})
}

// streamDeltas feeds a sequence of (itemID, delta) pairs through the deduper and
// returns the concatenated emitted text.
func streamDeltas(d *codexAgentMessageDedup, deltas ...[2]string) string {
	var b strings.Builder
	for _, pair := range deltas {
		b.WriteString(d.process(pair[0], pair[1]))
	}
	return b.String()
}

func TestCodexAgentMessageDedup(t *testing.T) {
	t.Run("first message streams through verbatim", func(t *testing.T) {
		d := newCodexAgentMessageDedup()
		got := streamDeltas(d,
			[2]string{"i_1", "Hello"},
			[2]string{"i_1", " world"},
		)
		if got != "Hello world" {
			t.Fatalf("got %q, want %q", got, "Hello world")
		}
	})

	t.Run("exact replay of previous message is fully suppressed", func(t *testing.T) {
		d := newCodexAgentMessageDedup()
		// First, real message under i_1.
		if got := streamDeltas(d, [2]string{"i_1", "Hello"}, [2]string{"i_1", " world"}); got != "Hello world" {
			t.Fatalf("first message = %q, want %q", got, "Hello world")
		}
		// Replayed under a new item id with identical content -> nothing emitted.
		got := streamDeltas(d,
			[2]string{"i_2", "Hello"},
			[2]string{"i_2", " world"},
		)
		if got != "" {
			t.Fatalf("replay emitted %q, want empty (suppressed)", got)
		}
	})

	t.Run("genuinely different second message is emitted in full", func(t *testing.T) {
		d := newCodexAgentMessageDedup()
		streamDeltas(d, [2]string{"i_1", "Hello"}, [2]string{"i_1", " world"})
		// Second message diverges immediately.
		got := streamDeltas(d,
			[2]string{"i_2", "Goodbye"},
			[2]string{"i_2", " now"},
		)
		if got != "Goodbye now" {
			t.Fatalf("second message = %q, want %q", got, "Goodbye now")
		}
	})

	t.Run("replay plus extra tail emits only the tail", func(t *testing.T) {
		d := newCodexAgentMessageDedup()
		streamDeltas(d, [2]string{"i_1", "Hello"}, [2]string{"i_1", " world"})
		// Replays "Hello world" then continues with " again".
		got := streamDeltas(d,
			[2]string{"i_2", "Hello"},
			[2]string{"i_2", " world"},
			[2]string{"i_2", " again"},
		)
		if got != " again" {
			t.Fatalf("replay+tail = %q, want %q", got, " again")
		}
	})
}

func TestResolveAgentAIToolAutoSkipsCodexWithoutAppServer(t *testing.T) {
	binDir := t.TempDir()
	writeFakeExecutable(t, binDir, "codex", "#!/bin/sh\nexit 2\n")
	writeFakeExecutable(t, binDir, "claude", "#!/bin/sh\nexit 0\n")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	tool, err := resolveAgentAITool("hello", "auto", "", "", "")
	if err != nil {
		t.Fatalf("resolveAgentAITool(auto) error = %v", err)
	}
	if tool.id != "claude" {
		t.Fatalf("tool.id = %q, want claude fallback", tool.id)
	}
}
