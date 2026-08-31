package services

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"aliang.one/nursorgate/common/cache"
)

func digestFixtureHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	cache.ResetCacheDirForTest()

	projectPath := filepath.Join(home, "work", "proj")
	encodedCwd := "-" + strings.ReplaceAll(strings.Trim(projectPath, string(filepath.Separator)), string(filepath.Separator), "-")
	projectDir := filepath.Join(home, ".claude", "projects", encodedCwd)
	require.NoError(t, os.MkdirAll(projectDir, 0o700))
	index := map[string]interface{}{
		"originalPath": projectPath,
		"entries": []map[string]interface{}{{
			"sessionId":   "digest-sid-1",
			"customTitle": "索引标题",
			"modified":    "2026-08-01T00:00:00Z",
		}},
	}
	raw, err := json.Marshal(index)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(projectDir, "sessions-index.json"), raw, 0o600))
	return home
}

// TestAgentVibeDigestIsDeterministic verifies identical inputs produce an
// identical digest — the change ticker compares consecutive digests, so any
// nondeterminism would either spam hellos or swallow real changes.
func TestAgentVibeDigestIsDeterministic(t *testing.T) {
	home := digestFixtureHome(t)
	first := agentVibeDigest(home)
	require.NotEmpty(t, first)
	assert.Equal(t, first, agentVibeDigest(home))
}

func TestAgentVibeDigestDetectsChanges(t *testing.T) {
	base := digestFixtureHome(t)
	require.NotEmpty(t, agentVibeDigest(base))

	t.Run("pid rename flips the digest", func(t *testing.T) {
		home := digestFixtureHome(t)
		before := agentVibeDigest(home)
		sessionsDir := filepath.Join(home, ".claude", "sessions")
		require.NoError(t, os.MkdirAll(sessionsDir, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(sessionsDir, "123.json"),
			[]byte(`{"pid":99999999,"sessionId":"digest-sid-1","name":"pid新名","updatedAt":"2026-08-02T00:00:00Z"}`), 0o600))
		assert.NotEqual(t, before, agentVibeDigest(home), "a new pid rename must change the digest")
	})

	t.Run("index customTitle change flips the digest", func(t *testing.T) {
		home := digestFixtureHome(t)
		before := agentVibeDigest(home)
		encodedCwd := "-" + strings.ReplaceAll(strings.Trim(filepath.Join(home, "work", "proj"), string(filepath.Separator)), string(filepath.Separator), "-")
		indexPath := filepath.Join(home, ".claude", "projects", encodedCwd, "sessions-index.json")
		raw, err := os.ReadFile(indexPath)
		require.NoError(t, err)
		updated := strings.Replace(string(raw), "索引标题", "改名后的标题", 1)
		require.NoError(t, os.WriteFile(indexPath, []byte(updated), 0o600))
		assert.NotEqual(t, before, agentVibeDigest(home), "an index customTitle change must flip the digest")
	})

	t.Run("rename cache change flips the digest", func(t *testing.T) {
		home := digestFixtureHome(t)
		before := agentVibeDigest(home)
		path, err := agentRenameCachePath()
		require.NoError(t, err)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
		require.NoError(t, saveAgentRenameCacheFile(path, map[string]agentRenameCacheEntry{
			"digest-sid-1": {Name: "缓存名", Origin: "phone", UpdatedAt: "2026-08-03T00:00:00Z"},
		}))
		assert.NotEqual(t, before, agentVibeDigest(home), "a rename-cache mutation must flip the digest")
	})

	t.Run("empty home yields empty digest", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		t.Setenv("USERPROFILE", t.TempDir())
		cache.ResetCacheDirForTest()
		assert.Empty(t, agentVibeDigest(""))
	})
}
