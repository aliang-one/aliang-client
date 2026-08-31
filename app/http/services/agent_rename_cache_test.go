package services

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func renameCacheTestPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "rename_cache.json")
}

// TestAgentRenameCacheRoundTrip verifies entries survive a save/load cycle with
// origin and timestamp intact — the fields the phone/local timestamp
// competition (and PhoneServer's latestOf guard) depend on.
func TestAgentRenameCacheRoundTrip(t *testing.T) {
	path := renameCacheTestPath(t)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	entries := map[string]agentRenameCacheEntry{
		"sid-local": {Name: "本地改名", Origin: "local", UpdatedAt: now, ProjectPath: "/tmp/proj"},
		"sid-phone": {Name: "手机改名", Origin: "phone", UpdatedAt: now},
	}
	require.NoError(t, saveAgentRenameCacheFile(path, entries))

	loaded, err := loadAgentRenameCacheFile(path)
	require.NoError(t, err)
	require.Len(t, loaded, 2)
	assert.Equal(t, "本地改名", loaded["sid-local"].Name)
	assert.Equal(t, "local", loaded["sid-local"].Origin)
	assert.Equal(t, now, loaded["sid-local"].UpdatedAt)
	assert.Equal(t, "/tmp/proj", loaded["sid-local"].ProjectPath)
	assert.Equal(t, "phone", loaded["sid-phone"].Origin)
}

// TestAgentRenameCacheMissingFileIsEmpty verifies a missing (first run) or
// corrupt cache degrades to an empty map, never an error up the stack.
func TestAgentRenameCacheMissingOrCorruptIsEmpty(t *testing.T) {
	loaded, err := loadAgentRenameCacheFile(filepath.Join(t.TempDir(), "absent.json"))
	require.NoError(t, err)
	assert.Empty(t, loaded)

	corrupt := filepath.Join(t.TempDir(), "corrupt.json")
	require.NoError(t, os.WriteFile(corrupt, []byte("{not json"), 0o600))
	loaded, err = loadAgentRenameCacheFile(corrupt)
	require.NoError(t, err)
	assert.Empty(t, loaded)
}

// TestMergeAgentRenameEntryKeepsNewer verifies the competition rule: the newer
// updatedAt wins regardless of origin, so a fresh local rename can retake a
// phone rename and vice versa (last-writer-wins on the agent clock).
func TestMergeAgentRenameEntryKeepsNewer(t *testing.T) {
	old := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano)
	newTS := time.Now().UTC().Format(time.RFC3339Nano)

	entries := map[string]agentRenameCacheEntry{}
	mergeAgentRenameEntry(entries, "sid", "手机新名", "phone", newTS, "")
	mergeAgentRenameEntry(entries, "sid", "本地旧名", "local", old, "")
	assert.Equal(t, "手机新名", entries["sid"].Name, "newer phone rename must beat older local rename")

	entries = map[string]agentRenameCacheEntry{}
	mergeAgentRenameEntry(entries, "sid", "本地新名", "local", newTS, "")
	mergeAgentRenameEntry(entries, "sid", "手机旧名", "phone", old, "")
	assert.Equal(t, "本地新名", entries["sid"].Name, "newer local rename must beat older phone rename")

	// Same timestamp: keep the existing entry (no churn on equal ts).
	entries = map[string]agentRenameCacheEntry{}
	mergeAgentRenameEntry(entries, "sid", "first", "local", newTS, "")
	mergeAgentRenameEntry(entries, "sid", "second", "phone", newTS, "")
	assert.Equal(t, "first", entries["sid"].Name)
}

// TestAgentRenameCacheCapDropsOldest verifies the LRU cap keeps the cache from
// growing without bound across months of sessions.
func TestAgentRenameCacheCapDropsOldest(t *testing.T) {
	path := renameCacheTestPath(t)
	entries := map[string]agentRenameCacheEntry{}
	base := time.Now().UTC().Add(-24 * time.Hour)
	for i := 0; i < agentRenameCacheMaxEntries+50; i++ {
		mergeAgentRenameEntry(entries, sidKey(i), "name", "local",
			base.Add(time.Duration(i)*time.Second).Format(time.RFC3339Nano), "")
	}
	require.NoError(t, saveAgentRenameCacheFile(path, entries))

	loaded, err := loadAgentRenameCacheFile(path)
	require.NoError(t, err)
	assert.LessOrEqual(t, len(loaded), agentRenameCacheMaxEntries)
	// The oldest entries must be the ones dropped, newest retained.
	assert.NotContains(t, loaded, sidKey(0))
	assert.Contains(t, loaded, sidKey(agentRenameCacheMaxEntries+49))
}

func sidKey(i int) string {
	return "sid-" + time.Unix(int64(i), 0).Format("150405") + "-" + time.Duration(i).String()
}
