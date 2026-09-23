package services

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/common/cache"
)

// freshFixtureTimestamp is the "just now" real-message timestamp the shared
// fixture encodes. Activity freshness is MESSAGE-derived (last real
// user/assistant record), not file mtime — a fixture session only counts as
// fresh when its last real message is recent.
func freshFixtureTimestamp() string {
	return time.Now().Add(-30 * time.Second).UTC().Format(time.RFC3339)
}

// renameCollectFixture writes a Claude transcript plus an optional pid session
// record, mirroring the fixture style of the existing inventory tests.
func renameCollectFixture(t *testing.T, sid string, pidRecord string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	cache.ResetCacheDirForTest()

	projectPath := filepath.Join(home, "work", "myproject")
	require.NoError(t, os.MkdirAll(projectPath, 0o700))
	encodedCwd := "-" + strings.ReplaceAll(strings.Trim(projectPath, string(filepath.Separator)), string(filepath.Separator), "-")
	claudeDir := filepath.Join(home, ".claude", "projects", encodedCwd)
	require.NoError(t, os.MkdirAll(claudeDir, 0o700))
	transcript := `{"timestamp":"` + freshFixtureTimestamp() + `","type":"user","cwd":"` + projectPath +
		`","sessionId":"` + sid + `","gitBranch":"main","message":{"role":"user","content":[{"type":"text","text":"Fix the login bug"}]}}` + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(claudeDir, sid+".jsonl"), []byte(transcript), 0o600))
	if pidRecord != "" {
		require.NoError(t, os.MkdirAll(filepath.Join(home, ".claude", "sessions"), 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(home, ".claude", "sessions", "12345.json"), []byte(pidRecord), 0o600))
	}
	return home
}

func findCollectedSession(sessions []models.AgentVibeSession, sid string) *models.AgentVibeSession {
	for i := range sessions {
		if sessions[i].ID == "claude_"+sid {
			return &sessions[i]
		}
	}
	return nil
}

// TestCollectClaudeVibeSessionsCacheNameBeatsStalePidName is the zombie-pid
// regression: a dead process's old pid-file rename must NOT shadow a newer
// rename held in the durable cache (previously both were applied in glob order
// — whichever pid number sorted last won, at random).
func TestCollectClaudeVibeSessionsCacheNameBeatsStalePidName(t *testing.T) {
	const sid = "cache-beats-stale-pid"
	old := time.Now().UTC().Add(-48 * time.Hour).Format(time.RFC3339Nano)
	renameCollectFixture(t, sid,
		`{"pid":99999999,"sessionId":"`+sid+`","name":"僵尸旧名","updatedAt":"`+old+`","status":"exited"}`)

	// Seed the durable cache with the newer phone rename.
	path, err := agentRenameCachePath()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	newTS := time.Now().UTC().Format(time.RFC3339Nano)
	require.NoError(t, saveAgentRenameCacheFile(path, map[string]agentRenameCacheEntry{
		sid: {Name: "手机新名", Origin: "phone", UpdatedAt: newTS},
	}))

	sessions := collectClaudeVibeSessions(nil)
	found := findCollectedSession(sessions, sid)
	require.NotNil(t, found)
	assert.Equal(t, "手机新名", found.Title, "newer cache rename must beat stale zombie pid rename")
	assert.Equal(t, newTS, found.TitleUpdatedAt, "title_updated_at must carry the winning timestamp for PhoneServer latestOf")
}

// TestCollectClaudeVibeSessionsPidNameSurvivesPidCleanup is the durability
// regression: Claude Code deletes ~/.claude/sessions/<pid>.json when it prunes
// old processes; once the agent has observed a rename it must persist in the
// cache so the title survives the cleanup (root cause of "renames vanish").
func TestCollectClaudeVibeSessionsPidNameSurvivesPidCleanup(t *testing.T) {
	const sid = "rename-survives-cleanup"
	now := time.Now().UTC().Format(time.RFC3339Nano)
	home := renameCollectFixture(t, sid,
		`{"pid":99999999,"sessionId":"`+sid+`","name":"修复一些bug","updatedAt":"`+now+`","status":"idle"}`)

	first := collectClaudeVibeSessions(nil)
	found := findCollectedSession(first, sid)
	require.NotNil(t, found)
	assert.Equal(t, "修复一些bug", found.Title)

	// The observation must have been persisted to the durable cache…
	path, err := agentRenameCachePath()
	require.NoError(t, err)
	cached, err := loadAgentRenameCacheFile(path)
	require.NoError(t, err)
	require.Contains(t, cached, sid, "observed pid rename must be persisted to the cache")
	assert.Equal(t, "修复一些bug", cached[sid].Name)
	assert.Equal(t, "local", cached[sid].Origin)

	// …so that after Claude Code prunes the pid file the title still resolves.
	require.NoError(t, os.Remove(filepath.Join(home, ".claude", "sessions", "12345.json")))
	second := collectClaudeVibeSessions(nil)
	found = findCollectedSession(second, sid)
	require.NotNil(t, found)
	assert.Equal(t, "修复一些bug", found.Title, "rename must survive pid-file cleanup via the durable cache")
}

// TestCollectClaudeVibeSessionsLivePidMarksRunning verifies liveness: a session
// whose pid record references a live process busy at work is reported as
// running instead of closed, so the phone can see TUI work in progress. An
// open-but-idle TUI reports "idle" (see agent_status_semantics_test.go).
func TestCollectClaudeVibeSessionsLivePidMarksRunning(t *testing.T) {
	const sid = "live-pid-running"
	now := time.Now().UTC().Format(time.RFC3339Nano)
	renameCollectFixture(t, sid,
		`{"pid":`+itoaTest(os.Getpid())+`,"sessionId":"`+sid+`","name":"运行中会话","updatedAt":"`+now+`","status":"busy"}`)

	sessions := collectClaudeVibeSessions(nil)
	found := findCollectedSession(sessions, sid)
	require.NotNil(t, found)
	assert.Equal(t, "running", found.Status, "a busy live pid must be reported as running")
}

// TestCollectClaudeVibeSessionsNewerPidNameRetakesCache verifies the reverse
// direction: a fresh local rename (newer updatedAt on a live pid record)
// retakes the title from an older cache entry.
func TestCollectClaudeVibeSessionsNewerPidNameRetakesCache(t *testing.T) {
	const sid = "local-retakes-cache"
	old := time.Now().UTC().Add(-24 * time.Hour).Format(time.RFC3339Nano)
	renameCollectFixture(t, sid,
		`{"pid":`+itoaTest(os.Getpid())+`,"sessionId":"`+sid+`","name":"本地最新名","updatedAt":"`+time.Now().UTC().Format(time.RFC3339Nano)+`","status":"idle"}`)

	path, err := agentRenameCachePath()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, saveAgentRenameCacheFile(path, map[string]agentRenameCacheEntry{
		sid: {Name: "手机旧名", Origin: "phone", UpdatedAt: old},
	}))

	sessions := collectClaudeVibeSessions(nil)
	found := findCollectedSession(sessions, sid)
	require.NotNil(t, found)
	assert.Equal(t, "本地最新名", found.Title, "newer live-pid rename must retake the title from an older cache entry")
}

func itoaTest(n int) string {
	raw, _ := json.Marshal(n)
	return string(raw)
}
