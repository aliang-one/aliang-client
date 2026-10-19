package services

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Claude Code's pid records carry a nameSource enum
// (user|peer|hook|derived|collision|auto). Only user/peer/hook are deliberate;
// auto/derived/collision are machine labels whose updatedAt is bumped on every
// ordinary status write. These tests pin the precedence rule: a deliberate
// title (phone rename, /rename, peer, hook) always outranks a machine label
// regardless of timestamps, while LWW still applies within a class.

func TestAgentRenameSourceExplicit(t *testing.T) {
	for _, source := range []string{"", "user", "peer", "hook", "phone", "User", " HOOK "} {
		assert.Truef(t, agentRenameSourceExplicit(source), "source %q must count as deliberate", source)
	}
	for _, source := range []string{"auto", "derived", "collision", "unknown-future-value"} {
		assert.Falsef(t, agentRenameSourceExplicit(source), "source %q must count as automatic", source)
	}
}

func TestMergeAgentRenameEntryExplicitBeatsNewerAuto(t *testing.T) {
	now := time.Now().UTC()
	entries := map[string]agentRenameCacheEntry{
		"sid": {
			Name:      "手机改名",
			Origin:    agentRenameOriginPhone,
			Source:    agentRenameSourcePhone,
			UpdatedAt: now.Add(-time.Hour).Format(time.RFC3339Nano),
		},
	}
	mergeAgentRenameEntry(entries, "sid", agentRenameCacheEntry{
		Name:      "自动标题",
		Origin:    agentRenameOriginLocal,
		Source:    "derived",
		UpdatedAt: now.Format(time.RFC3339Nano),
	})
	assert.Equal(t, "手机改名", entries["sid"].Name,
		"a machine label must never overwrite a phone rename, however fresh")
}

func TestMergeAgentRenameEntryExplicitWinsRegardlessOfStamp(t *testing.T) {
	now := time.Now().UTC()
	entries := map[string]agentRenameCacheEntry{
		"sid": {
			Name:      "自动标题",
			Origin:    agentRenameOriginLocal,
			Source:    "auto",
			UpdatedAt: now.Format(time.RFC3339Nano),
		},
	}
	mergeAgentRenameEntry(entries, "sid", agentRenameCacheEntry{
		Name:      "用户改名",
		Origin:    agentRenameOriginLocal,
		Source:    "user",
		UpdatedAt: now.Add(-time.Hour).Format(time.RFC3339Nano),
	})
	assert.Equal(t, "用户改名", entries["sid"].Name,
		"a deliberate rename always replaces a previously cached machine label")
	assert.Equal(t, "user", entries["sid"].Source)
}

func TestMergeAgentRenameEntryAutoLWWWithinClass(t *testing.T) {
	now := time.Now().UTC()
	entries := map[string]agentRenameCacheEntry{
		"sid": {
			Name:      "旧自动标题",
			Origin:    agentRenameOriginLocal,
			Source:    "auto",
			UpdatedAt: now.Add(-time.Minute).Format(time.RFC3339Nano),
		},
	}
	mergeAgentRenameEntry(entries, "sid", agentRenameCacheEntry{
		Name:      "新自动标题",
		Origin:    agentRenameOriginLocal,
		Source:    "auto",
		UpdatedAt: now.Format(time.RFC3339Nano),
	})
	assert.Equal(t, "新自动标题", entries["sid"].Name,
		"within the automatic class the strictly newer stamp still wins")
}

func TestMergeAgentRenameEntryEqualStampKeepsIncumbent(t *testing.T) {
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	entries := map[string]agentRenameCacheEntry{
		"sid": {Name: "先到标题", Origin: agentRenameOriginLocal, Source: "user", UpdatedAt: stamp},
	}
	mergeAgentRenameEntry(entries, "sid", agentRenameCacheEntry{
		Name: "后到标题", Origin: agentRenameOriginLocal, Source: "user", UpdatedAt: stamp,
	})
	assert.Equal(t, "先到标题", entries["sid"].Name,
		"equal timestamps keep the incumbent so repeated scans never churn")
}

// TestCollectClaudeVibeSessionsAutoPidNameDoesNotRevertPhoneRename is the R1
// regression: a live TUI's derived/auto label carries a newer updatedAt
// (Claude Code bumps it on ordinary status writes while keeping the old
// automatic name), and used to silently outrank the phone rename in the
// durable cache — the next hello then reverted the title on the phone.
func TestCollectClaudeVibeSessionsAutoPidNameDoesNotRevertPhoneRename(t *testing.T) {
	const sid = "auto-pid-does-not-revert-phone"
	phoneTS := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano)
	renameCollectFixture(t, sid,
		`{"pid":`+itoaTest(os.Getpid())+`,"sessionId":"`+sid+`","name":"自动摘要：修复登录","nameSource":"derived","updatedAt":"`+time.Now().UTC().Format(time.RFC3339Nano)+`","status":"idle"}`)

	path, err := agentRenameCachePath()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, saveAgentRenameCacheFile(path, map[string]agentRenameCacheEntry{
		sid: {Name: "手机改名", Origin: agentRenameOriginPhone, Source: agentRenameSourcePhone, UpdatedAt: phoneTS},
	}))

	sessions := collectClaudeVibeSessions(nil)
	found := findCollectedSession(sessions, sid)
	require.NotNil(t, found)
	assert.Equal(t, "手机改名", found.Title, "a newer derived label must not revert a phone rename")
	assert.Equal(t, phoneTS, found.TitleUpdatedAt,
		"title_updated_at must keep the phone stamp so PhoneServer's latestOf guard is not bypassed")

	cached, err := loadAgentRenameCacheFile(path)
	require.NoError(t, err)
	assert.Equal(t, "手机改名", cached[sid].Name, "the cache must keep the deliberate title")
}

// TestCollectClaudeVibeSessionsCollisionPidDoesNotRevertPhoneRename covers the
// collision suffix variant: Claude Code derives it from a user-typed name but
// assigns it automatically, so it belongs to the automatic class.
func TestCollectClaudeVibeSessionsCollisionPidDoesNotRevertPhoneRename(t *testing.T) {
	const sid = "collision-pid-does-not-revert-phone"
	phoneTS := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano)
	renameCollectFixture(t, sid,
		`{"pid":`+itoaTest(os.Getpid())+`,"sessionId":"`+sid+`","name":"重复会话 (2)","nameSource":"collision","updatedAt":"`+time.Now().UTC().Format(time.RFC3339Nano)+`","status":"idle"}`)

	path, err := agentRenameCachePath()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, saveAgentRenameCacheFile(path, map[string]agentRenameCacheEntry{
		sid: {Name: "手机改名", Origin: agentRenameOriginPhone, Source: agentRenameSourcePhone, UpdatedAt: phoneTS},
	}))

	sessions := collectClaudeVibeSessions(nil)
	found := findCollectedSession(sessions, sid)
	require.NotNil(t, found)
	assert.Equal(t, "手机改名", found.Title, "a collision label must not revert a phone rename")
}

// TestCollectClaudeVibeSessionsUserPidNameBeatsAutoCache is the reverse
// direction: a deliberate local rename (/rename, nameSource=user) retakes the
// title from a newer automatic label already sitting in the cache.
func TestCollectClaudeVibeSessionsUserPidNameBeatsAutoCache(t *testing.T) {
	const sid = "user-pid-beats-auto-cache"
	renameCollectFixture(t, sid,
		`{"pid":`+itoaTest(os.Getpid())+`,"sessionId":"`+sid+`","name":"我起的名","nameSource":"user","updatedAt":"`+time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano)+`","status":"idle"}`)

	path, err := agentRenameCachePath()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, saveAgentRenameCacheFile(path, map[string]agentRenameCacheEntry{
		sid: {Name: "自动摘要", Origin: agentRenameOriginLocal, Source: "auto", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)},
	}))

	sessions := collectClaudeVibeSessions(nil)
	found := findCollectedSession(sessions, sid)
	require.NotNil(t, found)
	assert.Equal(t, "我起的名", found.Title,
		"a deliberate local rename outranks a newer automatic cache label")

	cached, err := loadAgentRenameCacheFile(path)
	require.NoError(t, err)
	assert.Equal(t, "我起的名", cached[sid].Name)
	assert.Equal(t, "user", cached[sid].Source)
}

// TestCollectClaudeVibeSessionsAutoPidNameStillUpdatesAutoCache guards the
// opposite mistake: the fix must not freeze auto labels forever. Within the
// automatic class last-writer-wins still applies, so a session the user never
// renamed keeps following Claude Code's own title.
func TestCollectClaudeVibeSessionsAutoPidNameStillUpdatesAutoCache(t *testing.T) {
	const sid = "auto-pid-updates-auto-cache"
	renameCollectFixture(t, sid,
		`{"pid":`+itoaTest(os.Getpid())+`,"sessionId":"`+sid+`","name":"新自动摘要","nameSource":"auto","updatedAt":"`+time.Now().UTC().Format(time.RFC3339Nano)+`","status":"idle"}`)

	path, err := agentRenameCachePath()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, saveAgentRenameCacheFile(path, map[string]agentRenameCacheEntry{
		sid: {Name: "旧自动摘要", Origin: agentRenameOriginLocal, Source: "auto", UpdatedAt: time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano)},
	}))

	sessions := collectClaudeVibeSessions(nil)
	found := findCollectedSession(sessions, sid)
	require.NotNil(t, found)
	assert.Equal(t, "新自动摘要", found.Title, "auto labels still follow last-writer-wins within their class")

	cached, err := loadAgentRenameCacheFile(path)
	require.NoError(t, err)
	assert.Equal(t, "新自动摘要", cached[sid].Name)
	assert.Equal(t, "auto", cached[sid].Source)
}
