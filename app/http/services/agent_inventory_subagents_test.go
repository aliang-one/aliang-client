package services

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 2026-10-12 liang-dev RCA: the jsonl walk kept the top-N files by mtime across
// the whole ~/.claude/projects tree and filtered "subagents" transcripts only
// AFTER selection, so churn from subagent runs crowded real project transcripts
// out of the window (1263 of 1333 jsonl were subagents on the incident box; a
// project whose only transcript ranked #542 could never re-enter discovery and
// its directory was then rejected at session create). The walk must skip
// subagents dirs BEFORE selection — subagent transcripts are not project
// sessions and must not compete for the window.
func TestClaudeWalkSkipsSubagentTranscriptsBeforeSelection(t *testing.T) {
	root := t.TempDir()
	projectDir := filepath.Join(root, "-home-liang-cold-project")
	subDir := filepath.Join(projectDir, "subagents")
	if err := os.MkdirAll(subDir, 0o700); err != nil {
		t.Fatalf("mkdir subagents: %v", err)
	}

	for i := 0; i < agentVibeSessionFileScanLimit; i++ {
		sub := filepath.Join(subDir, fmt.Sprintf("sub-%04d.jsonl", i))
		if err := os.WriteFile(sub, []byte("{}\n"), 0o600); err != nil {
			t.Fatalf("write subagent transcript: %v", err)
		}
	}
	real := filepath.Join(projectDir, "1f96e26d-12c0-4a87-a2bc-94233d3c052c.jsonl")
	if err := os.WriteFile(real, []byte("{\"type\":\"user\"}\n"), 0o600); err != nil {
		t.Fatalf("write real transcript: %v", err)
	}
	old := time.Now().Add(-30 * 24 * time.Hour)
	if err := os.Chtimes(real, old, old); err != nil {
		t.Fatalf("backdate real transcript: %v", err)
	}

	files, _, truncated := findRecentAgentFilesWithStats(root, "*.jsonl", agentVibeSessionFileScanLimit)

	for _, path := range files {
		if strings.Contains(path, string(filepath.Separator)+"subagents"+string(filepath.Separator)) {
			t.Fatalf("subagent transcript selected into the window: %s", path)
		}
	}
	found := false
	for _, path := range files {
		if path == real {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("real transcript crowded out of the %d-slot window by subagent transcripts (files=%d, truncated=%t)",
			agentVibeSessionFileScanLimit, len(files), truncated)
	}
}
