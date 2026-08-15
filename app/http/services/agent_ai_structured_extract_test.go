package services

import (
	"testing"
)

func TestSummarizeFileDiff(t *testing.T) {
	tests := []struct {
		name    string
		diff    string
		added   int
		removed int
	}{
		{
			name:  "counts simple add and remove",
			diff:  "@@ -1,2 +1,2 @@\n-old line\n+new line\n context\n",
			added: 1, removed: 1,
		},
		{
			name:  "ignores file headers",
			diff:  "--- a/foo.go\n+++ b/foo.go\n@@ -1,1 +1,2 @@\n+added\n",
			added: 1, removed: 0,
		},
		{
			name:  "ignores no-newline marker",
			diff:  "@@ -1,1 +1,1 @@\n-old\n\\ No newline at end of file\n+new\n",
			added: 1, removed: 1,
		},
		{name: "empty diff", diff: "", added: 0, removed: 0},
		{name: "pure additions", diff: "@@ -0,0 +1,3 @@\n+a\n+b\n+c\n", added: 3, removed: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			added, removed := summarizeFileDiff(tt.diff)
			if added != tt.added || removed != tt.removed {
				t.Fatalf("summarizeFileDiff(%q) = (+%d,-%d), want (+%d,-%d)", tt.diff, added, removed, tt.added, tt.removed)
			}
		})
	}
}

func TestExtractCodexCommandItem(t *testing.T) {
	t.Run("started captures command and cwd", func(t *testing.T) {
		msg := map[string]interface{}{
			"method": "item/started",
			"params": map[string]interface{}{
				"item": map[string]interface{}{
					"type":    "commandExecution",
					"id":      "cmd_42",
					"command": []interface{}{"git", "pull"},
					"cwd":     "/repo",
				},
			},
		}
		itemID, command, cwd, exitCode, stdout, stderr, ok := extractCodexCommandItem(msg)
		if !ok || itemID != "cmd_42" {
			t.Fatalf("got (%q,%v), want (cmd_42,true)", itemID, ok)
		}
		if command != "git pull" {
			t.Fatalf("command = %q, want %q", command, "git pull")
		}
		if cwd != "/repo" {
			t.Fatalf("cwd = %q, want /repo", cwd)
		}
		if exitCode != nil {
			t.Fatalf("started item should have nil exitCode, got %v", *exitCode)
		}
		if stdout != "" || stderr != "" {
			t.Fatalf("started item stdout/stderr should be empty, got %q/%q", stdout, stderr)
		}
	})

	t.Run("command may be a single string", func(t *testing.T) {
		msg := map[string]interface{}{
			"params": map[string]interface{}{
				"item": map[string]interface{}{
					"type":    "commandExecution",
					"id":      "cmd_1",
					"command": "echo hi",
				},
			},
		}
		_, command, _, _, _, _, ok := extractCodexCommandItem(msg)
		if !ok || command != "echo hi" {
			t.Fatalf("got (%q,%v), want (echo hi,true)", command, ok)
		}
	})

	t.Run("completed captures exit code and output", func(t *testing.T) {
		msg := map[string]interface{}{
			"method": "item/completed",
			"params": map[string]interface{}{
				"item": map[string]interface{}{
					"type":     "commandExecution",
					"id":       "cmd_42",
					"exitCode": 0,
					"stdout":   "done\n",
					"stderr":   "",
				},
			},
		}
		itemID, _, _, exitCode, stdout, stderr, ok := extractCodexCommandItem(msg)
		if !ok || itemID != "cmd_42" {
			t.Fatalf("got (%q,%v), want (cmd_42,true)", itemID, ok)
		}
		if exitCode == nil || *exitCode != 0 {
			t.Fatalf("exitCode = %v, want pointer to 0", exitCode)
		}
		if stdout != "done\n" || stderr != "" {
			t.Fatalf("stdout/stderr = %q/%q, want done\\n/\"\"", stdout, stderr)
		}
	})

	t.Run("completed with nonzero exit code", func(t *testing.T) {
		msg := map[string]interface{}{
			"params": map[string]interface{}{
				"item": map[string]interface{}{
					"type":     "commandExecution",
					"id":       "cmd_x",
					"exitCode": 2,
				},
			},
		}
		_, _, _, exitCode, _, _, ok := extractCodexCommandItem(msg)
		if !ok || exitCode == nil || *exitCode != 2 {
			t.Fatalf("got exitCode=%v ok=%v, want pointer to 2", exitCode, ok)
		}
	})

	t.Run("non-command item ignored", func(t *testing.T) {
		msg := map[string]interface{}{
			"params": map[string]interface{}{
				"item": map[string]interface{}{"type": "fileChange", "id": "fc_1"},
			},
		}
		_, _, _, _, _, _, ok := extractCodexCommandItem(msg)
		if ok {
			t.Fatal("fileChange item should not be recognized as a command")
		}
	})
}
