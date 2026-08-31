package services

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"aliang.one/nursorgate/common/cache"
)

// TestCollectClaudeVibeSessionsReadsEveryProjectIndex is the deterministic
// collection regression: the index pass used to reuse findRecentAgentFiles
// (recent-80 cap, 500ms/6000-entry walk budget), so on machines with more
// projects than the cap the older projects' renamed conversations silently
// vanished from the phone list. Reading every project's sessions-index.json
// directly is milliseconds-cheap and loss-free.
func TestCollectClaudeVibeSessionsReadsEveryProjectIndex(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	cache.ResetCacheDirForTest()

	projectsRoot := filepath.Join(home, ".claude", "projects")
	const projectCount = 90 // > agentVibeIndexFileScanLimit (80)
	for i := 0; i < projectCount; i++ {
		projectPath := filepath.Join(home, "work", fmt.Sprintf("proj-%03d", i))
		encodedCwd := "-" + strings.ReplaceAll(strings.Trim(projectPath, string(filepath.Separator)), string(filepath.Separator), "-")
		projectDir := filepath.Join(projectsRoot, encodedCwd)
		require.NoError(t, os.MkdirAll(projectDir, 0o700))

		sid := fmt.Sprintf("sid-%03d", i)
		index := map[string]interface{}{
			"originalPath": projectPath,
			"entries": []map[string]interface{}{{
				"sessionId":   sid,
				"customTitle": fmt.Sprintf("项目%d的标题", i),
				"messageCount": 3,
				"created":     "2026-07-01T00:00:00Z",
				"modified":    "2026-07-01T01:00:00Z",
			}},
		}
		raw, err := json.Marshal(index)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(projectDir, "sessions-index.json"), raw, 0o600))
	}

	sessions := collectClaudeVibeSessions(nil)
	assert.Len(t, sessions, projectCount,
		"every project's indexed session must be collected regardless of project mtime ordering")
	// Spot-check the alphabetically-last project — the one a recent-80 cap
	// would be most likely to drop.
	found := findCollectedSession(sessions, "sid-089")
	require.NotNil(t, found)
	assert.Equal(t, "项目89的标题", found.Title)
}

// TestCollectClaudeVibeSessionsJSONLScanReportsTruncation pins the
// observability contract: when the recency-capped jsonl pass drops files, the
// truncation is visible in logs instead of being silent.
func TestCollectClaudeVibeSessionsJSONLScanReportsTruncation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	cache.ResetCacheDirForTest()

	projectPath := filepath.Join(home, "work", "big")
	encodedCwd := "-" + strings.ReplaceAll(strings.Trim(projectPath, string(filepath.Separator)), string(filepath.Separator), "-")
	projectDir := filepath.Join(home, ".claude", "projects", encodedCwd)
	require.NoError(t, os.MkdirAll(projectDir, 0o700))
	transcript := `{"timestamp":"2026-06-13T02:00:00Z","type":"user","cwd":"` + projectPath +
		`","sessionId":"%s","gitBranch":"main","message":{"role":"user","content":[{"type":"text","text":"hello"}]}}` + "\n"
	for i := 0; i < agentVibeSessionFileScanLimit+10; i++ {
		require.NoError(t, os.WriteFile(
			filepath.Join(projectDir, fmt.Sprintf("s-%03d.jsonl", i)),
			[]byte(fmt.Sprintf(transcript, fmt.Sprintf("sid-%03d", i))), 0o600))
	}

	// The capped scan must still succeed and report how many files it skipped.
	collected, truncated := collectClaudeVibeSessionsWithStats(nil)
	assert.Len(t, collected, agentVibeSessionFileScanLimit)
	assert.Equal(t, 10, truncated, "130 transcripts against a 120 cap must report 10 dropped")
}
