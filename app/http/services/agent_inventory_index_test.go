package services

import (
	"encoding/json"
	"fmt"
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

func TestFindRecentCodexSessionFilesUsesRolloutTimestamp(t *testing.T) {
	root := t.TempDir()
	old := filepath.Join(root, "rollout-2026-10-04T23-00-00-old.jsonl")
	new := filepath.Join(root, "rollout-2026-10-05T01-00-00-new.jsonl")
	require.NoError(t, os.WriteFile(old, []byte("{}\n"), 0o600))
	require.NoError(t, os.WriteFile(new, []byte("{}\n"), 0o600))
	importedAt := time.Date(2026, 10, 5, 11, 4, 0, 0, time.UTC)
	require.NoError(t, os.Chtimes(old, importedAt, importedAt))
	require.NoError(t, os.Chtimes(new, importedAt, importedAt))

	files := findRecentCodexSessionFiles(root, 1)
	require.Len(t, files, 1)
	assert.Equal(t, new, files[0])
}

func TestCollectCodexVibeSessionsPreservesCumulativeMessageCount(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	cache.ResetCacheDirForTest()

	sessionsDir := filepath.Join(home, ".codex", "sessions", "2026", "10", "18")
	require.NoError(t, os.MkdirAll(sessionsDir, 0o700))
	path := filepath.Join(sessionsDir, "rollout-2026-10-18T00-00-00-sid-count.jsonl")
	lines := []string{`{"timestamp":"2026-10-18T00:00:00Z","type":"session_meta","payload":{"id":"sid-count","cwd":"/tmp/project"}}`}
	for i := 0; i < agentVibeTranscriptMaxMessages+6; i++ {
		lines = append(lines, fmt.Sprintf(`{"timestamp":"2026-10-18T00:00:%02dZ","type":"message","payload":{"role":"user","content":"message-%d"}}`, i+1, i))
	}
	require.NoError(t, os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600))

	sessions := collectCodexVibeSessions(nil)
	var found *models.AgentVibeSession
	for i := range sessions {
		if sessions[i].ID == "codex_sid-count" {
			found = &sessions[i]
			break
		}
	}
	require.NotNil(t, found)
	assert.Equal(t, agentVibeTranscriptMaxMessages+6, found.MessageCount)
}

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
				"sessionId":    sid,
				"customTitle":  fmt.Sprintf("项目%d的标题", i),
				"messageCount": 3,
				"created":      "2026-07-01T00:00:00Z",
				"modified":     "2026-07-01T01:00:00Z",
			}},
		}
		raw, err := json.Marshal(index)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(projectDir, "sessions-index.json"), raw, 0o600))
		// 索引通道存在性校验契约(2026-10-18):条目须有转录文件支撑。
		writeIndexSupportTranscript(t, projectDir, sid, projectPath)
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
