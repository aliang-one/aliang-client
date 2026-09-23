package services

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"aliang.one/nursorgate/common/cache"
)

func writeProbeTestHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	cache.ResetCacheDirForTest()
	return home
}

func writeClaudeTranscript(t *testing.T, home, sid string, userPrompts []string) {
	t.Helper()
	projectPath := filepath.Join(home, "work", "myproject")
	require.NoError(t, os.MkdirAll(projectPath, 0o700))
	encodedCwd := "-" + strings.ReplaceAll(strings.Trim(projectPath, string(filepath.Separator)), string(filepath.Separator), "-")
	claudeDir := filepath.Join(home, ".claude", "projects", encodedCwd)
	require.NoError(t, os.MkdirAll(claudeDir, 0o700))

	var lines []string
	for _, prompt := range userPrompts {
		lines = append(lines, `{"timestamp":"2026-09-23T02:06:30Z","type":"user","cwd":"`+projectPath+
			`","sessionId":"`+sid+`","gitBranch":"main","message":{"role":"user","content":"`+prompt+`"}}`)
		lines = append(lines, `{"timestamp":"2026-09-23T02:06:40Z","type":"assistant","cwd":"`+projectPath+
			`","sessionId":"`+sid+`","gitBranch":"main","message":{"role":"assistant","content":[{"type":"text","text":"working on it"}]}}`)
	}
	require.NoError(t, os.WriteFile(filepath.Join(claudeDir, sid+".jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o600))
}

// The agent's own CLI capability probe (claudeEffortProbePrompt) runs
// `claude --print -p probe` headlessly. It must never surface as an imported
// phone conversation (2026-09-23 "vibe-on-phone-75" incident).
func TestCollectClaudeVibeSessionsSkipsProbeSession(t *testing.T) {
	home := writeProbeTestHome(t)
	writeClaudeTranscript(t, home, "probe-only-sid", []string{claudeEffortProbePrompt})

	sessions := collectClaudeVibeSessions(nil)
	for i := range sessions {
		assert.NotEqual(t, "claude_probe-only-sid", sessions[i].ID,
			"probe session must not be collected as an importable vibe session")
	}
}

// A real conversation that merely STARTS with the word "probe" keeps flowing:
// once a second user prompt exists it is a human session, not the headless probe.
func TestCollectClaudeVibeSessionsKeepsSessionWithFollowUpPrompt(t *testing.T) {
	home := writeProbeTestHome(t)
	writeClaudeTranscript(t, home, "probe-followup-sid", []string{claudeEffortProbePrompt, "now fix the login bug"})

	sessions := collectClaudeVibeSessions(nil)
	var found bool
	for i := range sessions {
		if sessions[i].ID == "claude_probe-followup-sid" {
			found = true
		}
	}
	assert.True(t, found, "session with a follow-up user prompt must still be collected")
}

// Defense in depth for the sessions-index pass: an index entry whose
// firstPrompt is the bare probe sentinel is skipped as well.
func TestCollectClaudeVibeSessionsSkipsProbeIndexEntry(t *testing.T) {
	home := writeProbeTestHome(t)
	projectPath := filepath.Join(home, "work", "myproject")
	require.NoError(t, os.MkdirAll(projectPath, 0o700))
	encodedCwd := "-" + strings.ReplaceAll(strings.Trim(projectPath, string(filepath.Separator)), string(filepath.Separator), "-")
	claudeDir := filepath.Join(home, ".claude", "projects", encodedCwd)
	require.NoError(t, os.MkdirAll(claudeDir, 0o700))

	index := `{"entries":[
		{"sessionId":"probe-index-sid","firstPrompt":"` + claudeEffortProbePrompt + `","messageCount":2,"projectPath":"` + projectPath + `"},
		{"sessionId":"real-index-sid","firstPrompt":"修复tui","messageCount":9,"projectPath":"` + projectPath + `"}
	]}`
	require.NoError(t, os.WriteFile(filepath.Join(claudeDir, "sessions-index.json"), []byte(index), 0o600))

	sessions := collectClaudeVibeSessions(nil)
	var ids []string
	for i := range sessions {
		ids = append(ids, sessions[i].ID)
	}
	assert.NotContains(t, ids, "claude_probe-index-sid", "probe index entry must be skipped")
	assert.Contains(t, ids, "claude_real-index-sid", "real index entry must survive")
}
