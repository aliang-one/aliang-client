package services

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"aliang.one/nursorgate/common/cache"
)

// The phone shows "in progress" for any session whose Claude TUI process was
// merely OPEN — idle at the prompt counts as running. Claude Code's own pid
// record carries the truth in its `status` field (idle/busy); the reported
// session status must follow it: busy→running, open-but-idle→idle,
// process-gone→closed.
func statusSemanticsFixture(t *testing.T, sid, ccStatus string, pid int) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	cache.ResetCacheDirForTest()

	projectPath := filepath.Join(home, "work", "proj")
	encodedCwd := "-" + strings.ReplaceAll(strings.Trim(projectPath, string(filepath.Separator)), string(filepath.Separator), "-")
	claudeDir := filepath.Join(home, ".claude", "projects", encodedCwd)
	require.NoError(t, os.MkdirAll(claudeDir, 0o700))
	transcript := `{"timestamp":"2026-06-13T02:00:00Z","type":"user","cwd":"` + projectPath +
		`","sessionId":"` + sid + `","gitBranch":"main","message":{"role":"user","content":[{"type":"text","text":"标题来源"}]}}` + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(claudeDir, sid+".jsonl"), []byte(transcript), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".claude", "sessions"), 0o700))
	rec := `{"pid":` + itoaTest(pid) + `,"sessionId":"` + sid + `","name":"t","updatedAt":"` +
		time.Now().UTC().Format(time.RFC3339Nano) + `","status":"` + ccStatus + `"}`
	require.NoError(t, os.WriteFile(filepath.Join(home, ".claude", "sessions", "1.json"), []byte(rec), 0o600))
}

func TestSessionStatusFollowsPidRecordStatus(t *testing.T) {
	live := os.Getpid()

	statusSemanticsFixture(t, "st-busy", "busy", live)
	sessions := collectClaudeVibeSessions(nil)
	assert.Equal(t, "running", findCollectedSession(sessions, "st-busy").Status,
		"busy TUI = task in progress")

	statusSemanticsFixture(t, "st-idle", "idle", live)
	sessions = collectClaudeVibeSessions(nil)
	assert.Equal(t, "idle", findCollectedSession(sessions, "st-idle").Status,
		"open TUI sitting at the prompt must NOT be reported as in progress")

	statusSemanticsFixture(t, "st-dead", "busy", 99999999)
	sessions = collectClaudeVibeSessions(nil)
	assert.Equal(t, "closed", findCollectedSession(sessions, "st-dead").Status,
		"dead process = closed even if the record froze on busy")
}

// TestAgentVibeDigestFlipsOnBusyIdleTransition pins the push semantics: an
// idle→busy transition is wire-visible status, so the digest must flip and
// trigger a hello (the phone sees "in progress" within one tick).
func TestAgentVibeDigestFlipsOnBusyIdleTransition(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	cache.ResetCacheDirForTest()
	sessionsDir := filepath.Join(home, ".claude", "sessions")
	require.NoError(t, os.MkdirAll(sessionsDir, 0o700))
	write := func(status string) {
		require.NoError(t, os.WriteFile(filepath.Join(sessionsDir, "9.json"),
			[]byte(`{"pid":`+itoaTest(os.Getpid())+`,"sessionId":"tr-sid","name":"n","updatedAt":"2026-09-01T00:00:00Z","status":"`+status+`"}`), 0o600))
	}
	write("idle")
	before := agentVibeDigest(home)
	require.NotEmpty(t, before)
	write("busy")
	assert.NotEqual(t, before, agentVibeDigest(home), "idle→busy must flip the digest (status is wire-visible)")
	write("busy") // same status, fresh updatedAt — must NOT flip again
	same := agentVibeDigest(home)
	write("busy")
	assert.Equal(t, same, agentVibeDigest(home), "updatedAt churn with unchanged status must not flip the digest")
}
