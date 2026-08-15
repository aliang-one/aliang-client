package services

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/common/cache"
)

func TestIsSafeAgentProjectPath(t *testing.T) {
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	home = filepath.Clean(home)

	valid := []string{
		filepath.Join(home, "code", "myproject"),
		filepath.Join(home, "MyProgram", "GoProgram", "nursor", "alianggate"),
		"/tmp/some-project",
	}
	for _, p := range valid {
		assert.True(t, isSafeAgentProjectPath(p), "expected valid: %s", p)
	}

	invalid := []string{
		"",
		"/",
		home,
		// ~/Library holds per-app data containers (design/IDE/storage), not code
		// projects — e.g. Open Design drives Claude Code with cwd pointing here.
		filepath.Join(home, "Library"),
		filepath.Join(home, "Library", "Application Support", "Open Design",
			"namespaces", "release-stable", "data", "projects",
			"05bfc135-d5c5-46ee-9ac6-13826187935d"),
		// Anything inside a macOS app bundle is the app's own resource tree.
		"/Applications/Open Design.app",
		filepath.Join("/Applications", "Open Design.app", "Contents", "Resources", "app", "prebundled"),
	}
	for _, p := range invalid {
		assert.False(t, isSafeAgentProjectPath(p), "expected invalid: %s", p)
	}
}

// TestCollectClaudeVibeSessionsAppliesRenameName verifies that the conversation
// title the user set via Claude Code's /rename — stored only in
// ~/.claude/sessions/<pid>.json — overrides the transcript-derived title for a
// detected local Claude Code conversation.
func TestCollectClaudeVibeSessionsAppliesRenameName(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	cache.ResetCacheDirForTest()

	projectPath := filepath.Join(home, "work", "myproject")
	require.NoError(t, os.MkdirAll(projectPath, 0o700))

	// Claude Code encodes the cwd into the projects dir name: "/" -> "-", leading "-".
	encodedCwd := "-" + strings.ReplaceAll(strings.Trim(projectPath, string(filepath.Separator)), string(filepath.Separator), "-")
	claudeDir := filepath.Join(home, ".claude", "projects", encodedCwd)
	require.NoError(t, os.MkdirAll(claudeDir, 0o700))

	const sid = "claude-rename-sid"
	// Transcript whose first user message is a command preamble — the kind of
	// junk title the previous derivation would surface.
	transcript := `{"timestamp":"2026-06-13T02:00:00Z","type":"user","cwd":"` + projectPath +
		`","sessionId":"` + sid + `","gitBranch":"main","message":{"role":"user","content":[{"type":"text","text":"<local-command-caveat>junk preamble that should not be the title</local-command-caveat>"}]}}` + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(claudeDir, sid+".jsonl"), []byte(transcript), 0o600))

	// The /rename title lives ONLY in ~/.claude/sessions/<pid>.json.
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".claude", "sessions"), 0o700))
	renameRecord := `{"pid":12345,"sessionId":"` + sid + `","cwd":"` + projectPath +
		`","name":"我的重命名标题","status":"idle"}`
	require.NoError(t, os.WriteFile(filepath.Join(home, ".claude", "sessions", "12345.json"), []byte(renameRecord), 0o600))

	sessions := collectClaudeVibeSessions(nil)

	var found *models.AgentVibeSession
	for i := range sessions {
		if sessions[i].ID == "claude_"+sid {
			found = &sessions[i]
			break
		}
	}
	require.NotNil(t, found, "claude session %q was not collected", "claude_"+sid)
	assert.Equal(t, "我的重命名标题", found.Title, "rename name must override transcript-derived title")
	assert.Equal(t, "我的重命名标题", found.Summary, "rename name fills empty summary")
}

// TestCollectClaudeVibeSessionsKeepsDerivedTitleWithoutRename verifies the
// overlay is a no-op when no /rename name exists, so existing behaviour is
// preserved (title derived from summary / first user message).
func TestCollectClaudeVibeSessionsKeepsDerivedTitleWithoutRename(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	cache.ResetCacheDirForTest()

	projectPath := filepath.Join(home, "work", "myproject")
	require.NoError(t, os.MkdirAll(projectPath, 0o700))

	encodedCwd := "-" + strings.ReplaceAll(strings.Trim(projectPath, string(filepath.Separator)), string(filepath.Separator), "-")
	claudeDir := filepath.Join(home, ".claude", "projects", encodedCwd)
	require.NoError(t, os.MkdirAll(claudeDir, 0o700))

	const sid = "claude-no-rename-sid"
	transcript := `{"timestamp":"2026-06-13T02:00:00Z","type":"user","cwd":"` + projectPath +
		`","sessionId":"` + sid + `","gitBranch":"main","message":{"role":"user","content":[{"type":"text","text":"Fix the login bug"}]}}` + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(claudeDir, sid+".jsonl"), []byte(transcript), 0o600))

	// No ~/.claude/sessions/<pid>.json at all.
	sessions := collectClaudeVibeSessions(nil)

	var found *models.AgentVibeSession
	for i := range sessions {
		if sessions[i].ID == "claude_"+sid {
			found = &sessions[i]
			break
		}
	}
	require.NotNil(t, found, "claude session %q was not collected", "claude_"+sid)
	assert.Equal(t, "Fix the login bug", found.Title, "title should fall back to first user message when no rename name exists")
}
