package services

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/common/cache"
)

// Claude Code's pid records carry a nameSource enum
// (user|peer|hook|derived|collision|auto). Only user/peer/hook are deliberate;
// auto/derived/collision are machine labels whose updatedAt is bumped on every
// ordinary status write. These tests pin BOTH properties of the fix:
//   - a deliberate title (phone rename, /rename, peer, hook) always outranks a
//     machine label regardless of timestamps (parallel fix 780431f);
//   - a machine label may be a display fallback but never stamps
//     title_updated_at, never fills Summary, and is ordered by the stable
//     nameSince — otherwise it permanently disables PhoneServer's Auto
//     Session Title.

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
// renamed keeps following Claude Code's own title — but still without a
// title_updated_at stamp.
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
	assert.Empty(t, found.TitleUpdatedAt,
		"an automatic label must never stamp title_updated_at even when it wins")

	cached, err := loadAgentRenameCacheFile(path)
	require.NoError(t, err)
	assert.Equal(t, "新自动摘要", cached[sid].Name)
	assert.Equal(t, "auto", cached[sid].Source)
}

// ─── Auto Session Title 解阻（派生名不打戳/不写 summary/存量去污）─────────

func namesourceFixture(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	cache.ResetCacheDirForTest()
}

func msOf(ts time.Time) string {
	return strconv.FormatInt(ts.UnixMilli(), 10)
}

func liveRecord(sid, name, source string, nameSince, updatedAt time.Time) agentRenamePidRecord {
	return agentRenamePidRecord{
		Name:       name,
		NameSource: source,
		NameSince:  nameSince,
		PID:        os.Getpid(),
		Status:     "busy",
		UpdatedAt:  updatedAt,
	}
}

func TestApplyClaudeRenameNamesDerivedIsFallbackWithoutStamp(t *testing.T) {
	namesourceFixture(t)
	const sid = "derived-fallback"
	now := time.Now().UTC()
	sessions := []models.AgentVibeSession{{ID: "claude_" + sid, Status: "idle"}}

	applyClaudeRenameNames(sessions, map[string]agentRenamePidRecord{
		sid: liveRecord(sid, "vibe-on-phone-a8", "derived", now.Add(-time.Hour), now),
	}, nil)

	assert.Equal(t, "vibe-on-phone-a8", sessions[0].Title, "derived name may fill an empty display title")
	assert.Empty(t, sessions[0].TitleUpdatedAt, "derived names must never stamp title_updated_at")
	assert.Equal(t, "derived", sessions[0].TitleSource)
	assert.Empty(t, sessions[0].Summary, "derived names must not leak into summary/objective")

	path, err := agentRenameCachePath()
	require.NoError(t, err)
	cached, err := loadAgentRenameCacheFile(path)
	require.NoError(t, err)
	require.Contains(t, cached, sid)
	assert.Equal(t, "derived", cached[sid].Source,
		"the cached label keeps its provenance so it can never be mistaken for a user rename")
}

func TestApplyClaudeRenameNamesDerivedDoesNotClobberIndexedTitle(t *testing.T) {
	namesourceFixture(t)
	const sid = "derived-indexed-title"
	now := time.Now().UTC()
	sessions := []models.AgentVibeSession{{ID: "claude_" + sid, Title: "index 摘要", Status: "idle"}}

	applyClaudeRenameNames(sessions, map[string]agentRenamePidRecord{
		sid: liveRecord(sid, "vibe-on-phone-a8", "derived", now.Add(-time.Hour), now),
	}, nil)

	assert.Equal(t, "index 摘要", sessions[0].Title, "a slug must not replace a better indexed title")
	assert.Empty(t, sessions[0].TitleUpdatedAt)
}

func TestApplyClaudeRenameNamesUserUsesNameSinceNotChurningUpdatedAt(t *testing.T) {
	namesourceFixture(t)
	const sid = "user-namesince"
	renameAt := time.UnixMilli(1792300000123).UTC()
	churnAt := renameAt.Add(4 * time.Hour)
	sessions := []models.AgentVibeSession{{ID: "claude_" + sid, Status: "idle"}}

	applyClaudeRenameNames(sessions, map[string]agentRenamePidRecord{
		sid: liveRecord(sid, "用户改名", "user", renameAt, churnAt),
	}, nil)

	assert.Equal(t, "用户改名", sessions[0].Title)
	assert.Equal(t, renameAt.Format(time.RFC3339Nano), sessions[0].TitleUpdatedAt,
		"user renames must stamp the stable nameSince, not churning updatedAt")
	assert.Equal(t, "user", sessions[0].TitleSource)

	path, err := agentRenameCachePath()
	require.NoError(t, err)
	cached, err := loadAgentRenameCacheFile(path)
	require.NoError(t, err)
	require.Contains(t, cached, sid)
	assert.Equal(t, "user", cached[sid].Source)
	assert.Equal(t, renameAt.Format(time.RFC3339Nano), cached[sid].UpdatedAt)
}

func TestApplyClaudeRenameNamesPeerAndHookAreUserIntent(t *testing.T) {
	for _, source := range []string{"peer", "hook"} {
		t.Run(source, func(t *testing.T) {
			namesourceFixture(t)
			sid := "source-" + source
			now := time.Now().UTC()
			sessions := []models.AgentVibeSession{{ID: "claude_" + sid, Status: "idle"}}
			applyClaudeRenameNames(sessions, map[string]agentRenamePidRecord{
				sid: liveRecord(sid, "跨会话改名", source, now, now),
			}, nil)
			assert.Equal(t, "跨会话改名", sessions[0].Title)
			assert.NotEmpty(t, sessions[0].TitleUpdatedAt)
			assert.Equal(t, source, sessions[0].TitleSource)
		})
	}
}

func TestApplyClaudeRenameNamesLegacyMissingSourceStaysUser(t *testing.T) {
	namesourceFixture(t)
	const sid = "legacy-no-source"
	updatedAt := time.Now().UTC()
	sessions := []models.AgentVibeSession{{ID: "claude_" + sid, Status: "idle"}}

	applyClaudeRenameNames(sessions, map[string]agentRenamePidRecord{
		sid: liveRecord(sid, "老版本改名", "", time.Time{}, updatedAt),
	}, nil)

	assert.Equal(t, "老版本改名", sessions[0].Title)
	assert.Equal(t, updatedAt.Format(time.RFC3339Nano), sessions[0].TitleUpdatedAt,
		"older CC builds omit nameSource; keep the pre-fix user-rename behavior")
}

func TestApplyClaudeRenameNamesDerivedDoesNotOverrideCachedPhoneRename(t *testing.T) {
	namesourceFixture(t)
	const sid = "derived-vs-phone"
	phoneTS := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)
	path, err := agentRenameCachePath()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, saveAgentRenameCacheFile(path, map[string]agentRenameCacheEntry{
		sid: {Name: "手机改名", Origin: agentRenameOriginPhone, Source: agentRenameSourcePhone, UpdatedAt: phoneTS},
	}))

	sessions := []models.AgentVibeSession{{ID: "claude_" + sid, Status: "idle"}}
	applyClaudeRenameNames(sessions, map[string]agentRenamePidRecord{
		sid: liveRecord(sid, "vibe-on-phone-a8", "derived", time.Now().Add(-time.Hour), time.Now().UTC()),
	}, nil)

	assert.Equal(t, "手机改名", sessions[0].Title, "a newer derived record must not retake a phone rename")
	assert.Equal(t, phoneTS, sessions[0].TitleUpdatedAt)
	assert.Equal(t, "user", sessions[0].TitleSource, "phone renames are user intent on the wire")

	cached, err := loadAgentRenameCacheFile(path)
	require.NoError(t, err)
	assert.Equal(t, "手机改名", cached[sid].Name)
	assert.Equal(t, "phone", cached[sid].Origin)
}

func TestApplyClaudeRenameNamesReconcilesLegacyDerivedCacheEntry(t *testing.T) {
	namesourceFixture(t)
	const sid = "legacy-derived-cache"
	legacyTS := time.Now().Add(-2 * time.Hour).UTC()
	recordTS := time.Now().Add(-3 * time.Hour).UTC()
	path, err := agentRenameCachePath()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	// Old agent versions cached the derived slug as an explicit local name
	// (Source did not exist yet).
	require.NoError(t, saveAgentRenameCacheFile(path, map[string]agentRenameCacheEntry{
		sid: {Name: "vibe-on-phone-a8", Origin: agentRenameOriginLocal, UpdatedAt: legacyTS.Format(time.RFC3339Nano)},
	}))

	sessions := []models.AgentVibeSession{{ID: "claude_" + sid, Status: "idle"}}
	applyClaudeRenameNames(sessions, map[string]agentRenamePidRecord{
		sid: liveRecord(sid, "vibe-on-phone-a8", "derived", recordTS, time.Now().UTC()),
	}, nil)

	assert.Empty(t, sessions[0].TitleUpdatedAt, "the reconciled legacy entry must stop stamping")
	cached, err := loadAgentRenameCacheFile(path)
	require.NoError(t, err)
	require.Contains(t, cached, sid)
	assert.Equal(t, "derived", cached[sid].Source,
		"the poisoned legacy entry is replaced by a provenance-tagged automatic label")
}

// TestApplyClaudeRenameNamesPurgeDoesNotClobberObservedRename pins the
// read-modify-write order: a purge and a freshly observed user rename in the
// SAME scan must both land. Saving the snapshot loaded at scan start would
// drop the observed rename forever (pid files are pruned).
func TestApplyClaudeRenameNamesPurgeDoesNotClobberObservedRename(t *testing.T) {
	namesourceFixture(t)
	const observedSID = "observed-user-rename"
	const purgedSID = "legacy-derived-entry"
	path, err := agentRenameCachePath()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, saveAgentRenameCacheFile(path, map[string]agentRenameCacheEntry{
		purgedSID: {
			Name:      "vibe-on-phone-a8",
			Origin:    agentRenameOriginLocal,
			UpdatedAt: time.Now().Add(-2 * time.Hour).Format(time.RFC3339Nano),
		},
	}))

	now := time.Now().UTC()
	sessions := []models.AgentVibeSession{
		{ID: "claude_" + observedSID, Status: "idle"},
		{ID: "claude_" + purgedSID, Status: "idle"},
	}
	applyClaudeRenameNames(sessions, map[string]agentRenamePidRecord{
		observedSID: liveRecord(observedSID, "用户新名", "user", now, now),
		purgedSID:   liveRecord(purgedSID, "vibe-on-phone-a8", "derived", now.Add(-3*time.Hour), now),
	}, nil)

	cached, err := loadAgentRenameCacheFile(path)
	require.NoError(t, err)
	require.Contains(t, cached, observedSID)
	assert.Equal(t, "用户新名", cached[observedSID].Name, "an observed user rename must survive the same-scan purge save")
	require.Contains(t, cached, purgedSID)
	assert.Equal(t, "derived", cached[purgedSID].Source,
		"the proven-derived legacy entry is re-seeded with provenance, not left explicit")
}

// TestCollectClaudeVibeSessionsDerivedPidNameDoesNotStamp is the end-to-end
// scan path: a real-shaped pid record with nameSource=derived must not stamp
// the wire title. This is the exact shape a phone-dispatched
// `claude --print --session-id` process writes.
func TestCollectClaudeVibeSessionsDerivedPidNameDoesNotStamp(t *testing.T) {
	const sid = "derived-e2e-scan"
	now := time.Now().UTC()
	renameCollectFixture(t, sid,
		`{"pid":`+itoaTest(os.Getpid())+`,"sessionId":"`+sid+`","name":"myproject-ab","nameSource":"derived","nameSince":`+msOf(now.Add(-time.Hour))+`,"updatedAt":`+msOf(now)+`,"status":"busy"}`)

	sessions := collectClaudeVibeSessions(nil)
	found := findCollectedSession(sessions, sid)
	require.NotNil(t, found)
	assert.Empty(t, found.TitleUpdatedAt, "a derived pid name must never reach the wire as a rename")
	assert.NotEmpty(t, found.Title, "the session still gets a display title")

	path, err := agentRenameCachePath()
	require.NoError(t, err)
	cached, err := loadAgentRenameCacheFile(path)
	require.NoError(t, err)
	require.Contains(t, cached, sid)
	assert.Equal(t, "derived", cached[sid].Source)
}

func TestLoadClaudeRenameRecordsParsesNameSourceAndNameSince(t *testing.T) {
	home := t.TempDir()
	sid := "parse-namesource"
	dir := filepath.Join(home, ".claude", "sessions")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	now := time.Now().UTC()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "42.json"), []byte(
		`{"pid":42,"sessionId":"`+sid+`","name":"proj-ab","nameSource":"derived","nameSince":`+msOf(now)+`,"updatedAt":`+msOf(now)+`,"status":"busy"}`,
	), 0o600))

	records := loadClaudeRenameRecords(home)
	require.Contains(t, records, sid)
	assert.Equal(t, "derived", records[sid].NameSource)
	assert.Equal(t, now.UnixMilli(), records[sid].NameSince.UnixMilli())
}
