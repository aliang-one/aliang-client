package services

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"aliang.one/nursorgate/common/cache"
)

// sidecarPathFor returns Claude Code's per-session sidecar location for a
// transcript path: <transcript dir>/<sessionId>/custom-title.json.
func sidecarPathFor(transcriptPath, nativeSessionID string) string {
	return filepath.Join(filepath.Dir(transcriptPath), nativeSessionID, "custom-title.json")
}

// sidecarCollectFixture builds a collectable single-session home: one Claude
// transcript with the given raw lines (already joined with newlines) plus an
// optional pid record, mirroring renameCollectFixture.
func sidecarCollectFixture(t *testing.T, sid string, transcriptLines []string, pidRecord string) (string, string) {
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

	lines := make([]string, 0, len(transcriptLines)+1)
	lines = append(lines, `{"timestamp":"`+freshFixtureTimestamp()+`","type":"user","cwd":"`+projectPath+
		`","sessionId":"`+sid+`","gitBranch":"main","message":{"role":"user","content":[{"type":"text","text":"Fix the login bug"}]}}`)
	lines = append(lines, transcriptLines...)
	transcriptPath := filepath.Join(claudeDir, sid+".jsonl")
	require.NoError(t, os.WriteFile(transcriptPath, []byte(strings.Join(lines, "\n")+"\n"), 0o600))

	if pidRecord != "" {
		require.NoError(t, os.MkdirAll(filepath.Join(home, ".claude", "sessions"), 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(home, ".claude", "sessions", "12345.json"), []byte(pidRecord), 0o600))
	}
	return home, transcriptPath
}

func seedPhoneRenameCacheEntry(t *testing.T, sid, name string) {
	t.Helper()
	path, err := agentRenameCachePath()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, saveAgentRenameCacheFile(path, map[string]agentRenameCacheEntry{
		sid: {
			Name:      name,
			Origin:    agentRenameOriginPhone,
			Source:    agentRenameSourcePhone,
			UpdatedAt: time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano),
		},
	}))
}

func countLines(t *testing.T, path string) int {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	trimmed := strings.TrimSuffix(string(raw), "\n")
	if trimmed == "" {
		return 0
	}
	return len(strings.Split(trimmed, "\n"))
}

// TestHandleRemoteAIRenameWritesTitleSidecar pins the sidecar half of the
// mirror: Claude Code's /resume title resolution is tail-64KB → sidecar →
// head-64KB, so without the sidecar a long turn that evicts the custom-title
// line from the tail window reverts the desktop list to the old title.
func TestHandleRemoteAIRenameWritesTitleSidecar(t *testing.T) {
	service := newEnabledRenameService(t)
	home := os.Getenv("HOME")
	seedRenameTranscript(t, home, "-home-liang-demo", renameTestNativeID,
		`{"type":"user","message":{"role":"user","content":"hi"}}`)

	acks := dispatchPhoneRename(t, service, "conv-123", "手机端新标题")
	require.Len(t, acks, 1)
	assert.Equal(t, true, acks[0]["accepted"])

	sidecar := sidecarPathFor(renameTranscriptPath(home, "-home-liang-demo", renameTestNativeID), renameTestNativeID)
	raw, err := os.ReadFile(sidecar)
	require.NoError(t, err, "the mirror must write Claude Code's custom-title.json sidecar")
	assert.Equal(t, `{"customTitle":"手机端新标题"}`, string(raw),
		"sidecar content must match CC's own writer shape (no trailing newline)")

	info, err := os.Stat(sidecar)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "sidecar file permissions mirror CC's own writer")
	dirInfo, err := os.Stat(filepath.Dir(sidecar))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), dirInfo.Mode().Perm(), "sidecar directory permissions mirror CC's own writer")
}

// TestConvergePhoneRenameReappendsAfterTuiRevertedTitle is the R2 worst-case
// regression: a live TUI re-appends its in-memory old title while the phone
// rename's line gets evicted from the tail window, and on exit re-appends the
// old title again — the desktop list then shows the old title indefinitely.
// Once no live process owns the session, the periodic inventory scan must
// re-append the phone title (and refresh the sidecar) so the desktop
// converges, and must not append again on the next scan.
func TestConvergePhoneRenameReappendsAfterTuiRevertedTitle(t *testing.T) {
	const sid = renameTestNativeID
	_, transcriptPath := sidecarCollectFixture(t, sid, []string{
		`{"type":"custom-title","customTitle":"旧自动标题","sessionId":"` + sid + `"}`,
	}, "")
	seedPhoneRenameCacheEntry(t, sid, "手机最终名")

	first := collectClaudeVibeSessions(nil)
	found := findCollectedSession(first, sid)
	require.NotNil(t, found)
	assert.Equal(t, "手机最终名", found.Title, "cache phone entry still wins the overlay")

	raw, err := os.ReadFile(transcriptPath)
	require.NoError(t, err)
	assert.True(t, strings.HasSuffix(strings.TrimRight(string(raw), "\n"),
		`"customTitle":"手机最终名","sessionId":"`+sid+`"}`),
		"convergence must re-append the phone title as the last custom-title line")

	sidecar := sidecarPathFor(transcriptPath, sid)
	sidecarRaw, err := os.ReadFile(sidecar)
	require.NoError(t, err, "convergence must also write/refresh the sidecar")
	assert.Equal(t, `{"customTitle":"手机最终名"}`, string(sidecarRaw))

	// Idempotence: the next scan must find the tail already resolving to the
	// phone title and append nothing.
	before := countLines(t, transcriptPath)
	_ = collectClaudeVibeSessions(nil)
	assert.Equal(t, before, countLines(t, transcriptPath),
		"a converged tail must not be re-appended on every scan")
}

// TestConvergePhoneRenameSkipsLivePid pins the safety valve: while a Claude
// Code process is alive it owns the transcript tail (it re-appends its
// in-memory title every ~32KB), so re-appending under it would ping-pong
// alternating title lines forever.
func TestConvergePhoneRenameSkipsLivePid(t *testing.T) {
	const sid = renameTestNativeID
	pidRecord := `{"pid":` + itoaTest(os.Getpid()) + `,"sessionId":"` + sid +
		`","name":"旧自动标题","nameSource":"auto","updatedAt":"` + time.Now().UTC().Format(time.RFC3339Nano) + `","status":"idle"}`
	_, transcriptPath := sidecarCollectFixture(t, sid, []string{
		`{"type":"custom-title","customTitle":"旧自动标题","sessionId":"` + sid + `"}`,
	}, pidRecord)
	seedPhoneRenameCacheEntry(t, sid, "手机最终名")

	before := countLines(t, transcriptPath)
	_ = collectClaudeVibeSessions(nil)
	assert.Equal(t, before, countLines(t, transcriptPath),
		"a live pid owns the tail; convergence must wait for it to exit")
	_, err := os.Stat(sidecarPathFor(transcriptPath, sid))
	assert.True(t, os.IsNotExist(err), "no sidecar may be written while the session is live")
}

// TestConvergePhoneRenameHandlesEvictedTail covers the pure-eviction shape:
// the phone custom-title line (and any older title) sits beyond the last 64KB,
// so the tail window resolves to NO title at all — Claude Code's list then
// falls back to the sidecar, which convergence must have written.
func TestConvergePhoneRenameHandlesEvictedTail(t *testing.T) {
	const sid = renameTestNativeID
	// ~80KB of filler so the seeded custom-title line lands outside the last
	// 64KB window, exactly like a long turn after the rename.
	filler := strings.Repeat("x", 80*1024)
	_, transcriptPath := sidecarCollectFixture(t, sid, []string{
		`{"type":"custom-title","customTitle":"早期标题","sessionId":"` + sid + `"}`,
		fmt.Sprintf(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"%s"}]}}`, filler),
	}, "")
	seedPhoneRenameCacheEntry(t, sid, "手机最终名")

	_ = collectClaudeVibeSessions(nil)

	raw, err := os.ReadFile(transcriptPath)
	require.NoError(t, err)
	assert.True(t, strings.HasSuffix(strings.TrimRight(string(raw), "\n"),
		`"customTitle":"手机最终名","sessionId":"`+sid+`"}`),
		"an evicted tail must be repaired by re-appending the phone title")

	sidecarRaw, err := os.ReadFile(sidecarPathFor(transcriptPath, sid))
	require.NoError(t, err)
	assert.Equal(t, `{"customTitle":"手机最终名"}`, string(sidecarRaw))
}

// TestLastClaudeCustomTitleInTailUnit pins the window semantics: only the last
// 64KB is considered, the LAST custom-title line inside it wins, and a window
// without any custom-title line reports ok=false.
func TestLastClaudeCustomTitleInTailUnit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	mkLine := func(title string) string {
		return `{"type":"custom-title","customTitle":"` + title + `","sessionId":"x"}`
	}
	require.NoError(t, os.WriteFile(path,
		[]byte(mkLine("头内标题")+"\n"+strings.Repeat("y\n", 40*1024)+mkLine("尾内标题")+"\n"), 0o600))
	got, ok := lastClaudeCustomTitleInTail(path)
	require.True(t, ok)
	assert.Equal(t, "尾内标题", got, "the last custom-title inside the window wins")

	require.NoError(t, os.WriteFile(path,
		[]byte(mkLine("头内标题")+"\n"+strings.Repeat("y\n", 40*1024)), 0o600))
	got, ok = lastClaudeCustomTitleInTail(path)
	assert.False(t, ok, "a custom-title line outside the 64KB window is invisible")
	assert.Empty(t, got)
}
