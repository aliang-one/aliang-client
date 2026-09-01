package services

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCollectClaudeVibeSessionsSameNamePidDoesNotRestampCache pins the churn
// fix: Claude Code keeps bumping a live pid record's updatedAt (and status)
// while the session is active — observed live as idle→busy rewrites every few
// seconds. When the rename NAME is unchanged that timestamp churn must not
// re-stamp the cache (file rewrite per scan) nor the wire title_updated_at
// (which would churn PhoneServer DB upserts and flip the digest into
// hello-spam every tick).
func TestCollectClaudeVibeSessionsSameNamePidDoesNotRestampCache(t *testing.T) {
	const sid = "churn-same-name"
	cacheTS := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano)
	renameCollectFixture(t, sid,
		`{"pid":`+itoaTest(os.Getpid())+`,"sessionId":"`+sid+`","name":"同名标题","updatedAt":"`+time.Now().UTC().Format(time.RFC3339Nano)+`","status":"busy"}`)

	path, err := agentRenameCachePath()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, saveAgentRenameCacheFile(path, map[string]agentRenameCacheEntry{
		sid: {Name: "同名标题", Origin: "local", UpdatedAt: cacheTS},
	}))

	sessions := collectClaudeVibeSessions(nil)
	found := findCollectedSession(sessions, sid)
	require.NotNil(t, found)
	assert.Equal(t, "同名标题", found.Title)
	assert.Equal(t, cacheTS, found.TitleUpdatedAt,
		"same-name timestamp churn must not re-stamp title_updated_at")
	assert.Equal(t, "running", found.Status, "liveness marking is independent of the churn fix")

	cached, err := loadAgentRenameCacheFile(path)
	require.NoError(t, err)
	assert.Equal(t, cacheTS, cached[sid].UpdatedAt,
		"cache file must not be rewritten on same-name timestamp churn")
}

// TestAgentVibeDigestIgnoresPidTimestampChurn pins the digest side: a live
// session's updatedAt bump with unchanged name and liveness is invisible on
// the wire, so it must not flip the digest (which would push a hello every
// 10s for the whole duration of any active TUI session).
func TestAgentVibeDigestIgnoresPidTimestampChurn(t *testing.T) {
	const sid = "digest-churn-sid"
	home := digestFixtureHome(t)
	sessionsDir := filepath.Join(home, ".claude", "sessions")
	require.NoError(t, os.MkdirAll(sessionsDir, 0o700))
	pidFile := filepath.Join(sessionsDir, "123.json")
	write := func(updatedAt string) {
		require.NoError(t, os.WriteFile(pidFile,
			[]byte(`{"pid":`+itoaTest(os.Getpid())+`,"sessionId":"`+sid+`","name":"稳定标题","updatedAt":"`+updatedAt+`","status":"busy"}`), 0o600))
	}
	write(time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano))
	before := agentVibeDigest(home)
	require.NotEmpty(t, before)

	write(time.Now().UTC().Format(time.RFC3339Nano))
	assert.Equal(t, before, agentVibeDigest(home),
		"updatedAt churn with unchanged name/liveness must not flip the digest")

	// But a real rename still flips it.
	require.NoError(t, os.WriteFile(pidFile,
		[]byte(`{"pid":`+itoaTest(os.Getpid())+`,"sessionId":"`+sid+`","name":"改名后的标题","updatedAt":"`+time.Now().UTC().Format(time.RFC3339Nano)+`","status":"busy"}`), 0o600))
	assert.NotEqual(t, before, agentVibeDigest(home), "an actual rename must still flip the digest")
}
