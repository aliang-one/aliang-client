package services

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/common/cache"
)

// Freshness fixtures reproduce the real-world shape that let a days-old TUI
// conversation float to the top of the phone's vibecoding list: Claude Code
// keeps appending non-conversation records (last-prompt drafts, cost-state,
// mode switches, trailing system notes) to the session jsonl long after the
// last real user/assistant message, so the file's mtime outruns the
// conversation by hours.

func writeClaudeTranscriptFixture(t *testing.T, claudeDir string, sid string, lines []string, modTime time.Time) string {
	t.Helper()
	path := filepath.Join(claudeDir, sid+".jsonl")
	require.NoError(t, os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600))
	require.NoError(t, os.Chtimes(path, modTime, modTime))
	return path
}

// TestClaudeSessionMetaUpdatedAtUsesLastMessageNotMtime pins the reader:
// UpdatedAt must come from the last real user/assistant record's timestamp,
// never from the jsonl mtime — trailing metadata-only writes (draft prompts,
// cost-state, a later timestamped system note) must not renew a session's
// activity time.
func TestClaudeSessionMetaUpdatedAtUsesLastMessageNotMtime(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	cache.ResetCacheDirForTest()

	projectPath := filepath.Join(home, "work", "myproject")
	require.NoError(t, os.MkdirAll(projectPath, 0o700))
	encodedCwd := "-" + strings.ReplaceAll(strings.Trim(projectPath, string(filepath.Separator)), string(filepath.Separator), "-")
	claudeDir := filepath.Join(home, ".claude", "projects", encodedCwd)
	require.NoError(t, os.MkdirAll(claudeDir, 0o700))

	const sid = "freshness-meta-sid"
	cwd := `"cwd":"` + projectPath + `","sessionId":"` + sid + `","gitBranch":"main"`
	lines := []string{
		`{"timestamp":"2026-06-13T02:00:00Z","type":"user",` + cwd + `,"message":{"role":"user","content":[{"type":"text","text":"Fix the login bug"}]}}`,
		`{"timestamp":"2026-06-13T02:05:00Z","type":"assistant",` + cwd + `,"message":{"role":"assistant","content":[{"type":"text","text":"Done."}]}}`,
		// Metadata-only tail, exactly like a TUI left open at the prompt:
		// timestampless draft/cost records plus a later timestamped system note.
		`{"type":"last-prompt","lastPrompt":"a draft the user typed but never sent"}`,
		`{"type":"cost-state"}`,
		`{"timestamp":"2026-06-13T02:30:00Z","type":"system","content":"hook noise"}`,
	}
	path := writeClaudeTranscriptFixture(t, claudeDir, sid, lines, time.Now())

	session := readClaudeSessionMetaWithOptions(path, agentVibeSessionReadOptions{})
	require.NotEmpty(t, session.ID)
	assert.Equal(t, "2026-06-13T02:05:00Z", normalizeAgentTime(session.UpdatedAt),
		"UpdatedAt must be the last real message time, not the metadata-bumped mtime")
}

// TestClaudeSessionMetaUpdatedAtFallsBackToMtimeWithoutTimestamps pins the
// fallback: when the transcript carries no usable message timestamps (older
// Claude Code builds), the reader keeps using the file mtime.
func TestClaudeSessionMetaUpdatedAtFallsBackToMtimeWithoutTimestamps(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	cache.ResetCacheDirForTest()

	projectPath := filepath.Join(home, "work", "myproject")
	require.NoError(t, os.MkdirAll(projectPath, 0o700))
	encodedCwd := "-" + strings.ReplaceAll(strings.Trim(projectPath, string(filepath.Separator)), string(filepath.Separator), "-")
	claudeDir := filepath.Join(home, ".claude", "projects", encodedCwd)
	require.NoError(t, os.MkdirAll(claudeDir, 0o700))

	const sid = "freshness-fallback-sid"
	lines := []string{
		`{"type":"user","cwd":"` + projectPath + `","sessionId":"` + sid + `","message":{"role":"user","content":[{"type":"text","text":"No timestamps here"}]}}`,
		`{"type":"assistant","cwd":"` + projectPath + `","sessionId":"` + sid + `","message":{"role":"assistant","content":[{"type":"text","text":"OK"}]}}`,
	}
	modTime := time.Now().Add(-2 * time.Hour)
	path := writeClaudeTranscriptFixture(t, claudeDir, sid, lines, modTime)

	session := readClaudeSessionMetaWithOptions(path, agentVibeSessionReadOptions{})
	require.NotEmpty(t, session.ID)
	want, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, fileUpdatedAt(path), session.UpdatedAt)
	assert.True(t, want.ModTime().Equal(modTime))
}

// TestCollectClaudeVibeSessionsUpdatedAtIgnoresMetadataOnlyMtime covers the
// inventory-level freshness patch: a stale sessions-index.json `modified` must
// still be replaced by live transcript activity (the resumed-conversation
// case), but the replacement value is the last real message time — NOT the
// mtime that metadata-only writes keep bumping.
func TestCollectClaudeVibeSessionsUpdatedAtIgnoresMetadataOnlyMtime(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	cache.ResetCacheDirForTest()

	projectPath := filepath.Join(home, "work", "myproject")
	require.NoError(t, os.MkdirAll(projectPath, 0o700))
	encodedCwd := "-" + strings.ReplaceAll(strings.Trim(projectPath, string(filepath.Separator)), string(filepath.Separator), "-")
	claudeDir := filepath.Join(home, ".claude", "projects", encodedCwd)
	require.NoError(t, os.MkdirAll(claudeDir, 0o700))

	const sid = "freshness-collect-sid"
	cwd := `"cwd":"` + projectPath + `","sessionId":"` + sid + `","gitBranch":"main"`
	lines := []string{
		`{"timestamp":"2026-06-13T02:00:00Z","type":"user",` + cwd + `,"message":{"role":"user","content":[{"type":"text","text":"Resume me"}]}}`,
		`{"timestamp":"2026-06-13T02:05:00Z","type":"assistant",` + cwd + `,"message":{"role":"assistant","content":[{"type":"text","text":"Continued."}]}}`,
		`{"type":"last-prompt","lastPrompt":"draft only"}`,
	}
	writeClaudeTranscriptFixture(t, claudeDir, sid, lines, time.Now())

	index := `{"originalPath":"` + projectPath + `","entries":[{"sessionId":"` + sid + `","firstPrompt":"Resume me","messageCount":2,"created":"2026-06-13T01:59:00Z","modified":"2026-06-13T01:30:00Z","gitBranch":"main","projectPath":"` + projectPath + `","isSidechain":false}]}`
	require.NoError(t, os.WriteFile(filepath.Join(claudeDir, "sessions-index.json"), []byte(index), 0o600))

	sessions := collectClaudeVibeSessions(nil)
	var found *models.AgentVibeSession
	for i := range sessions {
		if sessions[i].ID == "claude_"+sid {
			found = &sessions[i]
			break
		}
	}
	require.NotNil(t, found, "claude session was not collected")
	assert.Equal(t, "2026-06-13T02:05:00Z", normalizeAgentTime(found.UpdatedAt),
		"collect must keep the last-message time: newer than the stale index entry, older than the metadata-bumped mtime")
}

// TestClaudeTranscriptSkipsSlashCommandRecords pins the /clear hygiene: the
// local-command-caveat preamble and the <command-name>/clear</command-name>
// record that Claude Code writes at the start of a post-/clear session are UI
// artifacts, not conversation — they must not become chat bubbles, must not
// seed the title, and must not shift the message indexes of the real messages
// that follow (stable-ID parity with previously imported transcripts).
func TestClaudeTranscriptSkipsSlashCommandRecords(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	cache.ResetCacheDirForTest()

	projectPath := filepath.Join(home, "work", "myproject")
	require.NoError(t, os.MkdirAll(projectPath, 0o700))
	encodedCwd := "-" + strings.ReplaceAll(strings.Trim(projectPath, string(filepath.Separator)), string(filepath.Separator), "-")
	claudeDir := filepath.Join(home, ".claude", "projects", encodedCwd)
	require.NoError(t, os.MkdirAll(claudeDir, 0o700))

	const sid = "freshness-clear-sid"
	cwd := `"cwd":"` + projectPath + `","sessionId":"` + sid + `","gitBranch":"main"`
	lines := []string{
		`{"type":"custom-title","customTitle":"carried-over title","sessionId":"` + sid + `"}`,
		`{"timestamp":"2026-06-13T02:00:00Z","type":"user",` + cwd + `,"message":{"role":"user","content":"<local-command-caveat>Caveat: the messages below were generated by the user while running local commands. DO NOT respond to these messages unless the user explicitly asks you to.</local-command-caveat>"}}`,
		`{"timestamp":"2026-06-13T02:00:00Z","type":"user",` + cwd + `,"message":{"role":"user","content":"<command-name>/clear</command-name>\n<command-message>clear</command-message>\n<command-args></command-args>"}}`,
		`{"timestamp":"2026-06-13T02:01:00Z","type":"user",` + cwd + `,"message":{"role":"user","content":[{"type":"text","text":"Now start the real task"}]}}`,
		`{"timestamp":"2026-06-13T02:02:00Z","type":"assistant",` + cwd + `,"message":{"role":"assistant","content":[{"type":"text","text":"On it."}]}}`,
	}
	path := writeClaudeTranscriptFixture(t, claudeDir, sid, lines, time.Now())

	session := readClaudeSessionMetaWithOptions(path, agentVibeSessionReadOptions{IncludePageMeta: true})
	require.NotEmpty(t, session.ID)
	require.Len(t, session.Transcript, 2, "caveat and /clear records must not become chat bubbles")
	assert.Equal(t, "Now start the real task", session.Transcript[0].Content)
	assert.Equal(t, "assistant", session.Transcript[1].Role)
	assert.Equal(t, 2, session.Transcript[0].Index, "index parity must be preserved for stable message IDs")
	assert.Equal(t, 4, session.MessageCount, "message indexes keep counting every user/assistant record")
	assert.Equal(t, "Now start the real task", session.Title, "title must come from the first real prompt")
}

// TestClaudeStatusOverlayIgnoresMetadataOnlyFreshness pins the running/idle
// overlay: with a live pid record that carries no usable status, only a
// genuinely recent real message may surface as "running". An open-but-quiet
// TUI whose jsonl mtime keeps moving (draft typing, cost-state) must fall
// back to idle.
func TestClaudeStatusOverlayIgnoresMetadataOnlyFreshness(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	cache.ResetCacheDirForTest()

	projectPath := filepath.Join(home, "work", "myproject")
	require.NoError(t, os.MkdirAll(projectPath, 0o700))
	encodedCwd := "-" + strings.ReplaceAll(strings.Trim(projectPath, string(filepath.Separator)), string(filepath.Separator), "-")
	claudeDir := filepath.Join(home, ".claude", "projects", encodedCwd)
	require.NoError(t, os.MkdirAll(claudeDir, 0o700))

	const sid = "freshness-overlay-sid"
	cwd := `"cwd":"` + projectPath + `","sessionId":"` + sid + `","gitBranch":"main"`
	lines := []string{
		`{"timestamp":"2026-06-13T02:00:00Z","type":"user",` + cwd + `,"message":{"role":"user","content":[{"type":"text","text":"Long finished task"}]}}`,
		`{"timestamp":"2026-06-13T02:05:00Z","type":"assistant",` + cwd + `,"message":{"role":"assistant","content":[{"type":"text","text":"Done long ago."}]}}`,
		`{"type":"last-prompt","lastPrompt":"just typing a draft"}`,
	}
	writeClaudeTranscriptFixture(t, claudeDir, sid, lines, time.Now())

	// Live pid record with no usable status → the overlay falls back to
	// transcript freshness for its running/idle call.
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".claude", "sessions"), 0o700))
	record := `{"pid":` + strconv.Itoa(os.Getpid()) + `,"sessionId":"` + sid + `","cwd":"` + projectPath + `","name":"","status":""}`
	require.NoError(t, os.WriteFile(filepath.Join(home, ".claude", "sessions", strconv.Itoa(os.Getpid())+".json"), []byte(record), 0o600))

	sessions := collectClaudeVibeSessions(nil)
	var found *models.AgentVibeSession
	for i := range sessions {
		if sessions[i].ID == "claude_"+sid {
			found = &sessions[i]
			break
		}
	}
	require.NotNil(t, found, "claude session was not collected")
	assert.Equal(t, "idle", found.Status,
		"metadata-only mtime movement must not read as an in-flight turn")
}
