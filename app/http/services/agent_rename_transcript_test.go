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

// seedRenameTranscript writes a minimal but realistic Claude Code transcript
// for nativeSessionID under <home>/.claude/projects/<sanitized-project>/ and
// returns its exact bytes so tests can prove the append never rewrote them.
func seedRenameTranscript(t *testing.T, home, sanitizedProject, nativeSessionID string, lines ...string) string {
	t.Helper()
	dir := filepath.Join(home, ".claude", "projects", sanitizedProject)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	content := strings.Join(lines, "\n") + "\n"
	path := filepath.Join(dir, nativeSessionID+".jsonl")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return content
}

func dispatchPhoneRename(t *testing.T, service *AgentService, conversationID, title string) []map[string]interface{} {
	t.Helper()
	var acks []map[string]interface{}
	writeJSON := func(v interface{}) error {
		if m, ok := v.(map[string]interface{}); ok && m["type"] == "ai.session.rename.ack" {
			acks = append(acks, m)
		}
		return nil
	}
	service.handleRemoteAgentMessage(map[string]interface{}{
		"type":              "ai.session.rename",
		"session_id":        conversationID,
		"title":             title,
		"source_session_id": "native-sid-abc",
		"provider":          "claude",
		"project_path":      "/home/liang/demo",
	}, writeJSON)
	return acks
}

func newEnabledRenameService(t *testing.T) *AgentService {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	cache.ResetCacheDirForTest()
	service := NewAgentService()
	service.mu.Lock()
	service.state.Enabled = true
	service.state.Registered = true
	service.mu.Unlock()
	return service
}

// TestHandleRemoteAIRenameAppendsCustomTitleToTranscript extends the rename
// persistence to Claude Code's own transcript: the desktop TUI /resume list
// reads its titles from the session JSONL (last custom-title line wins), so a
// phone rename must land there too — as ONE appended line, never a rewrite.
// Existing transcript bytes (including the previous custom-title line) must
// survive byte-for-byte.
func TestHandleRemoteAIRenameAppendsCustomTitleToTranscript(t *testing.T) {
	service := newEnabledRenameService(t)
	home := os.Getenv("HOME")
	seed := seedRenameTranscript(t, home, "-home-liang-demo", "native-sid-abc",
		`{"type":"user","message":{"role":"user","content":"hi"}}`,
		`{"type":"custom-title","customTitle":"旧名字","sessionId":"native-sid-abc"}`,
		`{"type":"assistant","message":{"role":"assistant","content":"yo"}}`,
	)
	transcriptPath := filepath.Join(home, ".claude", "projects", "-home-liang-demo", "native-sid-abc.jsonl")

	acks := dispatchPhoneRename(t, service, "conv-123", "手机端新标题")

	require.Len(t, acks, 1, "rename must still be acknowledged")
	assert.Equal(t, true, acks[0]["accepted"])

	raw, err := os.ReadFile(transcriptPath)
	require.NoError(t, err)
	content := string(raw)

	// Safety contract: pure append — every pre-existing byte intact, in place.
	require.True(t, strings.HasPrefix(content, seed),
		"transcript must be append-only; existing bytes were modified")

	// The appended record is the LAST line and is a valid custom-title.
	trimmed := strings.TrimSuffix(content, "\n")
	lines := strings.Split(trimmed, "\n")
	require.GreaterOrEqual(t, len(lines), 2)
	require.Contains(t, lines[len(lines)-1], `"type":"custom-title"`,
		"rename must append a custom-title record as the transcript's last line")
	var appended map[string]string
	require.NoError(t, json.Unmarshal([]byte(lines[len(lines)-1]), &appended),
		"appended line must be a standalone JSON object")
	assert.Equal(t, "手机端新标题", appended["customTitle"])
	assert.Equal(t, "native-sid-abc", appended["sessionId"])

	// Claude's parser takes the LAST custom-title line per session, so the
	// old one stays on disk (data preservation) but the new one must come
	// after it.
	oldIndex, newIndex := -1, -1
	for i, line := range lines {
		if strings.Contains(line, `"customTitle":"旧名字"`) {
			oldIndex = i
		}
		if strings.Contains(line, `"customTitle":"手机端新标题"`) {
			newIndex = i
		}
	}
	require.NotEqual(t, -1, oldIndex, "previous custom-title line must be preserved")
	require.NotEqual(t, -1, newIndex)
	assert.Greater(t, newIndex, oldIndex, "new custom-title must be the later line (last-wins)")
}

// TestHandleRemoteAIRenameWithoutTranscriptDoesNotCreateFile keeps the append
// strictly opportunistic: when no transcript exists on this machine (e.g. an
// imported conversation from another device), the rename still persists to
// the agent cache and acks, but must NOT fabricate a transcript file — a bare
// custom-title JSONL would show up as an empty ghost session in /resume.
func TestHandleRemoteAIRenameWithoutTranscriptDoesNotCreateFile(t *testing.T) {
	service := newEnabledRenameService(t)
	home := os.Getenv("HOME")
	projectsRoot := filepath.Join(home, ".claude", "projects")
	require.NoError(t, os.MkdirAll(projectsRoot, 0o755))

	acks := dispatchPhoneRename(t, service, "conv-123", "手机端新标题")

	require.Len(t, acks, 1)
	assert.Equal(t, true, acks[0]["accepted"])

	var found []string
	require.NoError(t, filepath.Walk(projectsRoot, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && strings.HasSuffix(path, ".jsonl") {
			found = append(found, path)
		}
		return nil
	}))
	assert.Empty(t, found, "no transcript file may be created for a missing session")
}

// TestHandleRemoteAIRenameTitleWithNewlineStaysSingleLine guards the data
// format: a JSONL transcript is line-delimited, so the appended record must
// remain exactly ONE physical line even when the title contains a newline
// (JSON escaping), otherwise every subsequent line of the transcript would
// be corrupted for line-oriented parsers.
func TestHandleRemoteAIRenameTitleWithNewlineStaysSingleLine(t *testing.T) {
	service := newEnabledRenameService(t)
	home := os.Getenv("HOME")
	seed := seedRenameTranscript(t, home, "-home-liang-demo", "native-sid-abc",
		`{"type":"user","message":{"role":"user","content":"hi"}}`,
	)
	transcriptPath := filepath.Join(home, ".claude", "projects", "-home-liang-demo", "native-sid-abc.jsonl")

	acks := dispatchPhoneRename(t, service, "conv-123", "第一行\n第二行")

	require.Len(t, acks, 1)
	raw, err := os.ReadFile(transcriptPath)
	require.NoError(t, err)
	content := string(raw)
	require.True(t, strings.HasPrefix(content, seed), "transcript must stay append-only")

	trimmed := strings.TrimSuffix(content, "\n")
	lines := strings.Split(trimmed, "\n")
	require.Len(t, lines, 2, "appended title must occupy exactly one physical line")
	var appended map[string]string
	require.NoError(t, json.Unmarshal([]byte(lines[1]), &appended))
	assert.Equal(t, "第一行\n第二行", appended["customTitle"])
}
