package services

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// msTimestamp renders a Claude pid-record updatedAt as UNIX milliseconds — the
// real on-disk shape (the 437bc99 lesson: fixtures that diverge from the real
// record shape pass locally and are blind in production).
func msTimestamp(t *testing.T, offset time.Duration) string {
	t.Helper()
	return strconv.FormatInt(time.Now().Add(offset).UnixMilli(), 10)
}

// livePidRecord writes a pid record for the TEST process — alive by
// construction on every platform the tests run on.
func livePidRecord(t *testing.T, sid string, fields string) string {
	t.Helper()
	record := `{"pid":` + strconv.Itoa(os.Getpid()) + `,"sessionId":"` + sid + `","updatedAt":` + msTimestamp(t, 0)
	if fields != "" {
		record += "," + fields
	}
	return record + "}"
}

// ageTranscript backdates the fixture transcript's mtime to simulate a session
// whose jsonl stopped being appended (turn finished / CLI exited abnormally).
func ageTranscript(t *testing.T, sid string, age time.Duration) {
	t.Helper()
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	encoded := "-" + strings.ReplaceAll(strings.Trim(filepath.Join(home, "work", "myproject"), string(filepath.Separator)), string(filepath.Separator), "-")
	path := filepath.Join(home, ".claude", "projects", encoded, sid+".jsonl")
	past := time.Now().Add(-age)
	require.NoError(t, os.Chtimes(path, past, past))
}

// TestClaudeStatusBusyAliveIsRunning is the 437bc99 anchor: a live process
// whose pid record says busy is running (a task is executing).
func TestClaudeStatusBusyAliveIsRunning(t *testing.T) {
	const sid = "status-busy-alive"
	renameCollectFixture(t, sid, livePidRecord(t, sid, `"status":"busy","entrypoint":"cli"`))
	found := findCollectedSession(collectClaudeVibeSessions(nil), sid)
	require.NotNil(t, found)
	assert.Equal(t, "running", found.Status)
}

// TestClaudeStatusIdleAliveIsIdle is the other 437bc99 anchor: an open TUI
// waiting at the prompt is idle, NOT forever "in progress".
func TestClaudeStatusIdleAliveIsIdle(t *testing.T) {
	const sid = "status-idle-alive"
	renameCollectFixture(t, sid, livePidRecord(t, sid, `"status":"idle","entrypoint":"cli"`))
	found := findCollectedSession(collectClaudeVibeSessions(nil), sid)
	require.NotNil(t, found)
	assert.Equal(t, "idle", found.Status)
}

// TestClaudeStatusMissingUsableStatus follows the transcript-freshness branch:
// records without a usable status (missing / null / empty / unknown value —
// Go unmarshal collapses all of them to "") report running only while the
// transcript was appended within the freshness window. These are the real
// record shapes seen on 2.1.x: interactive builds before the status field
// existed and headless/sdk entrypoints ("claude -p" never writes a status).
func TestClaudeStatusMissingUsableStatus(t *testing.T) {
	cases := []struct {
		name   string
		fields string
	}{
		{"missing", `"entrypoint":"sdk-cli"`},
		{"null", `"status":null,"entrypoint":"sdk-cli"`},
		{"empty", `"status":"","entrypoint":"sdk-cli"`},
		{"garbage", `"status":"weird","entrypoint":"cli"`},
	}
	for _, tc := range cases {
		t.Run(tc.name+"/fresh→running", func(t *testing.T) {
			const sid = "status-missing-fresh"
			renameCollectFixture(t, sid, livePidRecord(t, sid, tc.fields))
			found := findCollectedSession(collectClaudeVibeSessions(nil), sid)
			require.NotNil(t, found)
			assert.Equal(t, "running", found.Status)
		})
		t.Run(tc.name+"/stale→idle", func(t *testing.T) {
			const sid = "status-missing-stale"
			renameCollectFixture(t, sid, livePidRecord(t, sid, tc.fields))
			ageTranscript(t, sid, claudeStatusFreshnessWindow+time.Minute)
			found := findCollectedSession(collectClaudeVibeSessions(nil), sid)
			require.NotNil(t, found)
			assert.Equal(t, "idle", found.Status)
		})
	}
}

// TestClaudeStatusNoPidRecordStaysClosed: without a pid record there is no
// liveness signal at all — the scan default (closed) stands.
func TestClaudeStatusNoPidRecordStaysClosed(t *testing.T) {
	const sid = "status-no-record"
	renameCollectFixture(t, sid, "")
	found := findCollectedSession(collectClaudeVibeSessions(nil), sid)
	require.NotNil(t, found)
	assert.Equal(t, "closed", found.Status)
}

// TestClaudeStatusDeadPidBusyStaysClosed is the zombie anchor: a dead
// process's busy record must never mark a session running.
func TestClaudeStatusDeadPidBusyStaysClosed(t *testing.T) {
	const sid = "status-dead-pid"
	renameCollectFixture(t, sid, `{"pid":99999999,"sessionId":"`+sid+`","status":"busy","updatedAt":`+msTimestamp(t, 0)+`}`)
	found := findCollectedSession(collectClaudeVibeSessions(nil), sid)
	require.NotNil(t, found)
	assert.Equal(t, "closed", found.Status)
}

// TestCollectClaudeUpdatedAtFreshnessPatch is the resumed-session fix: Claude
// Code writes sessions-index.json lazily, so a resumed conversation's index
// entry keeps the PREVIOUS turn's `modified` while the jsonl is appended live.
// The collector must report the fresher jsonl mtime as updated_at, otherwise
// PhoneServer's stale-run sweeper (10 min quiet) flaps the mid-turn session
// to "timed out" and back on every inventory push.
func TestCollectClaudeUpdatedAtFreshnessPatch(t *testing.T) {
	const sid = "status-resumed-freshness"
	home := renameCollectFixture(t, sid, "")
	staleModified := time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339)
	encoded := "-" + strings.ReplaceAll(strings.Trim(filepath.Join(home, "work", "myproject"), string(filepath.Separator)), string(filepath.Separator), "-")
	indexPath := filepath.Join(home, ".claude", "projects", encoded, "sessions-index.json")
	index := map[string]interface{}{
		"originalPath": filepath.Join(home, "work", "myproject"),
		"entries": []map[string]interface{}{
			{
				"sessionId":    sid,
				"firstPrompt":  "Fix the login bug",
				"messageCount": 2,
				"created":      staleModified,
				"modified":     staleModified,
			},
		},
	}
	rawIndex, err := json.Marshal(index)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(indexPath, rawIndex, 0o600))

	found := findCollectedSession(collectClaudeVibeSessions(nil), sid)
	require.NotNil(t, found)
	assert.NotEqual(t, staleModified, found.UpdatedAt, "resumed session's updated_at must not stay at the stale index modified")
	freshFloor := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	assert.Greater(t, compareRFC3339(found.UpdatedAt, freshFloor), 0, "updated_at must carry the fresh jsonl mtime")
}

// TestCollectClaudeScanDeterminism: the same on-disk state must produce the
// same status twice in a row (agent restart / repeated scans converge instead
// of oscillating).
func TestCollectClaudeScanDeterminism(t *testing.T) {
	const sid = "status-determinism"
	renameCollectFixture(t, sid, livePidRecord(t, sid, `"status":"busy","entrypoint":"cli"`))
	first := findCollectedSession(collectClaudeVibeSessions(nil), sid)
	second := findCollectedSession(collectClaudeVibeSessions(nil), sid)
	require.NotNil(t, first)
	require.NotNil(t, second)
	assert.Equal(t, first.Status, second.Status)
	assert.Equal(t, first.UpdatedAt, second.UpdatedAt)
}
