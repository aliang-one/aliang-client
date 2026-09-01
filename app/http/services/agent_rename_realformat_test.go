package services

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCollectClaudeVibeSessionsPidEpochMillisUpdatedAt is the production
// fixture regression: real Claude Code pid records carry updatedAt as UNIX
// MILLISECONDS (a JSON number — verified in ~/.claude/sessions/11147.json),
// not an RFC3339 string. The first implementation declared `UpdatedAt string`,
// so json.Unmarshal failed on every real record and the entire pid rename
// path silently no-op'd in production (titles never synced) while unit tests
// stayed green on string fixtures.
func TestCollectClaudeVibeSessionsPidEpochMillisUpdatedAt(t *testing.T) {
	const sid = "epoch-millis-sid"
	updatedAtMillis := time.Now().UTC().Add(-time.Minute).UnixMilli()
	renameCollectFixture(t, sid,
		`{"pid":99999999,"sessionId":"`+sid+`","name":"手机端同步的标题","updatedAt":`+
			itoaTest(int(updatedAtMillis))+`,"status":"idle"}`)

	sessions := collectClaudeVibeSessions(nil)
	found := findCollectedSession(sessions, sid)
	require.NotNil(t, found)
	assert.Equal(t, "手机端同步的标题", found.Title,
		"epoch-millis updatedAt must not invalidate the whole pid record")

	// The winning timestamp must be the decoded millis as RFC3339 — this is
	// what PhoneServer's latestOf guard compares against.
	expected := time.UnixMilli(updatedAtMillis).UTC().Format(time.RFC3339Nano)
	assert.Equal(t, expected, found.TitleUpdatedAt)

	cached, err := loadAgentRenameCacheFile(func() string {
		path, err := agentRenameCachePath()
		require.NoError(t, err)
		return path
	}())
	require.NoError(t, err)
	require.Contains(t, cached, sid, "the observed rename must be durably cached")
	assert.Equal(t, expected, cached[sid].UpdatedAt)
}

// TestCollectClaudeVibeSessionsPidUpdatedAtTimestampSeconds covers the smaller
// variant some CC versions write (seconds instead of millis).
func TestCollectClaudeVibeSessionsPidUpdatedAtTimestampSeconds(t *testing.T) {
	const sid = "epoch-seconds-sid"
	secs := time.Now().UTC().Add(-2 * time.Minute).Unix()
	renameCollectFixture(t, sid,
		`{"pid":99999999,"sessionId":"`+sid+`","name":"秒级时间戳","updatedAt":`+itoaTest(int(secs))+`}`)

	sessions := collectClaudeVibeSessions(nil)
	found := findCollectedSession(sessions, sid)
	require.NotNil(t, found)
	assert.Equal(t, "秒级时间戳", found.Title)
	expected := time.Unix(secs, 0).UTC().Format(time.RFC3339Nano)
	assert.Equal(t, expected, found.TitleUpdatedAt)
}
