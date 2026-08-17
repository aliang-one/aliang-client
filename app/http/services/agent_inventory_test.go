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
// title the user set via Claude Code's /rename — the per-process record in
// ~/.claude/sessions/<pid>.json — overrides the transcript-derived title for a
// detected local Claude Code conversation (this fixture has no
// sessions-index.json, so the pid record is the only rename source).
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

	// No sessions-index.json in this fixture — the pid record is the rename source.
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

// TestCollectClaudeVibeSessionsPrefersIndexCustomTitle verifies that the
// durable "customTitle" Claude Code writes into sessions-index.json (the
// /rename title) wins over the auto-derived summary and firstPrompt, while
// entries without customTitle keep the derived title.
func TestCollectClaudeVibeSessionsPrefersIndexCustomTitle(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	cache.ResetCacheDirForTest()

	projectPath := filepath.Join(home, "work", "indexed-project")
	require.NoError(t, os.MkdirAll(projectPath, 0o700))

	// Claude Code encodes the cwd into the projects dir name: "/" -> "-", leading "-".
	encodedCwd := "-" + strings.ReplaceAll(strings.Trim(projectPath, string(filepath.Separator)), string(filepath.Separator), "-")
	claudeDir := filepath.Join(home, ".claude", "projects", encodedCwd)
	require.NoError(t, os.MkdirAll(claudeDir, 0o700))

	const renamedSID = "claude-index-renamed-sid"
	const derivedSID = "claude-index-derived-sid"
	indexJSON := `{"version":1,"originalPath":"` + projectPath + `","entries":[` +
		`{"sessionId":"` + renamedSID + `","fullPath":"` + filepath.Join(claudeDir, renamedSID+".jsonl") +
		`","firstPrompt":"command preamble junk","summary":"Auto summary text","customTitle":"索引里的重命名",` +
		`"messageCount":5,"created":"2026-06-18T00:00:00Z","modified":"2026-06-19T00:00:00Z","gitBranch":"main",` +
		`"projectPath":"` + projectPath + `","isSidechain":false},` +
		`{"sessionId":"` + derivedSID + `","fullPath":"` + filepath.Join(claudeDir, derivedSID+".jsonl") +
		`","firstPrompt":"Fallback first prompt","summary":"",` +
		`"messageCount":2,"created":"2026-06-18T00:00:00Z","modified":"2026-06-19T00:00:00Z","gitBranch":"main",` +
		`"projectPath":"` + projectPath + `","isSidechain":false}]}`

	require.NoError(t, os.WriteFile(filepath.Join(claudeDir, "sessions-index.json"), []byte(indexJSON), 0o600))

	sessions := collectClaudeVibeSessions(nil)
	byID := make(map[string]models.AgentVibeSession, len(sessions))
	for _, session := range sessions {
		byID[session.ID] = session
	}

	renamed, ok := byID["claude_"+renamedSID]
	require.True(t, ok, "renamed claude session %q was not collected", "claude_"+renamedSID)
	assert.Equal(t, "索引里的重命名", renamed.Title, "customTitle must beat summary/firstPrompt")
	assert.Equal(t, "Auto summary text", renamed.Summary, "auto summary is preserved when present")

	derived, ok := byID["claude_"+derivedSID]
	require.True(t, ok, "derived claude session %q was not collected", "claude_"+derivedSID)
	assert.Equal(t, "Fallback first prompt", derived.Title, "entries without customTitle keep the derived title")
}

// TestCollectClaudeVibeSessionsRenamePidRecordBeatsIndexCustomTitle verifies
// precedence between the two /rename storage sites: the per-process record in
// ~/.claude/sessions/<pid>.json reflects the newest rename of a live process
// and must override the (possibly stale) customTitle from sessions-index.json.
func TestCollectClaudeVibeSessionsRenamePidRecordBeatsIndexCustomTitle(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	cache.ResetCacheDirForTest()

	projectPath := filepath.Join(home, "work", "stale-index-project")
	require.NoError(t, os.MkdirAll(projectPath, 0o700))

	encodedCwd := "-" + strings.ReplaceAll(strings.Trim(projectPath, string(filepath.Separator)), string(filepath.Separator), "-")
	claudeDir := filepath.Join(home, ".claude", "projects", encodedCwd)
	require.NoError(t, os.MkdirAll(claudeDir, 0o700))

	const sid = "claude-stale-index-sid"
	indexJSON := `{"version":1,"originalPath":"` + projectPath + `","entries":[` +
		`{"sessionId":"` + sid + `","fullPath":"` + filepath.Join(claudeDir, sid+".jsonl") +
		`","firstPrompt":"command preamble junk","summary":"","customTitle":"索引里的旧名字",` +
		`"messageCount":5,"created":"2026-06-18T00:00:00Z","modified":"2026-06-19T00:00:00Z","gitBranch":"main",` +
		`"projectPath":"` + projectPath + `","isSidechain":false}]}`
	require.NoError(t, os.WriteFile(filepath.Join(claudeDir, "sessions-index.json"), []byte(indexJSON), 0o600))

	require.NoError(t, os.MkdirAll(filepath.Join(home, ".claude", "sessions"), 0o700))
	renameRecord := `{"pid":23456,"sessionId":"` + sid + `","cwd":"` + projectPath +
		`","name":"进程里的新名字","status":"idle"}`
	require.NoError(t, os.WriteFile(filepath.Join(home, ".claude", "sessions", "23456.json"), []byte(renameRecord), 0o600))

	sessions := collectClaudeVibeSessions(nil)

	var found *models.AgentVibeSession
	for i := range sessions {
		if sessions[i].ID == "claude_"+sid {
			found = &sessions[i]
			break
		}
	}
	require.NotNil(t, found, "claude session %q was not collected", "claude_"+sid)
	assert.Equal(t, "进程里的新名字", found.Title, "live pid rename must beat the index customTitle")
}
