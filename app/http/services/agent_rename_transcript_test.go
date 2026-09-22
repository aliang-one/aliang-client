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

// renameTestNativeID is a valid Claude session UUID — the only shape Claude
// Code itself accepts for a custom-title write, and therefore the only shape
// the mirror accepts too.
const renameTestNativeID = "3f2504e0-4f89-11d3-9a0c-0305e82c3301"

// seedRenameTranscript writes a minimal but realistic Claude Code transcript
// for nativeSessionID under <home>/.claude/projects/<sanitized-project>/ and
// returns its exact bytes so tests can prove the append never rewrote them.
func seedRenameTranscript(t *testing.T, home, sanitizedProject, nativeSessionID string, lines ...string) string {
	t.Helper()
	return seedRenameTranscriptRaw(t, home, sanitizedProject, nativeSessionID, strings.Join(lines, "\n")+"\n")
}

// seedRenameTranscriptRaw is seedRenameTranscript without a forced trailing
// newline, so torn-tail scenarios can be seeded verbatim.
func seedRenameTranscriptRaw(t *testing.T, home, sanitizedProject, nativeSessionID, raw string) string {
	t.Helper()
	dir := filepath.Join(home, ".claude", "projects", sanitizedProject)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	path := filepath.Join(dir, nativeSessionID+".jsonl")
	require.NoError(t, os.WriteFile(path, []byte(raw), 0o600))
	return raw
}

func dispatchRename(t *testing.T, service *AgentService, payload map[string]interface{}) []map[string]interface{} {
	t.Helper()
	if payload["type"] == nil {
		payload["type"] = "ai.session.rename"
	}
	var acks []map[string]interface{}
	writeJSON := func(v interface{}) error {
		if m, ok := v.(map[string]interface{}); ok && m["type"] == "ai.session.rename.ack" {
			acks = append(acks, m)
		}
		return nil
	}
	service.handleRemoteAgentMessage(payload, writeJSON)
	return acks
}

func dispatchPhoneRename(t *testing.T, service *AgentService, conversationID, title string) []map[string]interface{} {
	t.Helper()
	return dispatchRename(t, service, map[string]interface{}{
		"type":              "ai.session.rename",
		"session_id":        conversationID,
		"title":             title,
		"source_session_id": renameTestNativeID,
		"provider":          "claude",
		"project_path":      "/home/liang/demo",
	})
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

func renameTranscriptPath(home, sanitizedProject, nativeSessionID string) string {
	return filepath.Join(home, ".claude", "projects", sanitizedProject, nativeSessionID+".jsonl")
}

// TestHandleRemoteAIRenameAppendsCustomTitleToTranscript extends the rename
// persistence to Claude Code's own transcript: the desktop TUI /resume list
// reads its titles from the session JSONL (last custom-title line wins), so a
// phone rename must land there too — as EXACTLY ONE appended line, never a
// rewrite. Existing transcript bytes (including the previous custom-title
// line) must survive byte-for-byte.
func TestHandleRemoteAIRenameAppendsCustomTitleToTranscript(t *testing.T) {
	service := newEnabledRenameService(t)
	home := os.Getenv("HOME")
	seed := seedRenameTranscript(t, home, "-home-liang-demo", renameTestNativeID,
		`{"type":"user","message":{"role":"user","content":"hi"}}`,
		`{"type":"custom-title","customTitle":"旧名字","sessionId":"`+renameTestNativeID+`"}`,
		`{"type":"assistant","message":{"role":"assistant","content":"yo"}}`,
	)
	transcriptPath := renameTranscriptPath(home, "-home-liang-demo", renameTestNativeID)

	acks := dispatchPhoneRename(t, service, "conv-123", "手机端新标题")

	require.Len(t, acks, 1, "rename must still be acknowledged")
	assert.Equal(t, true, acks[0]["accepted"])

	raw, err := os.ReadFile(transcriptPath)
	require.NoError(t, err)
	content := string(raw)

	// Safety contract: pure append — every pre-existing byte intact, in place.
	require.True(t, strings.HasPrefix(content, seed),
		"transcript must be append-only; existing bytes were modified")

	// Exactly ONE line was appended: the seed's 3 lines plus the new record.
	trimmed := strings.TrimSuffix(content, "\n")
	lines := strings.Split(trimmed, "\n")
	require.Len(t, lines, 4, "the mirror must append exactly one physical line")
	require.Contains(t, lines[len(lines)-1], `"type":"custom-title"`,
		"rename must append a custom-title record as the transcript's last line")
	var appended map[string]string
	require.NoError(t, json.Unmarshal([]byte(lines[len(lines)-1]), &appended),
		"appended line must be a standalone JSON object")
	assert.Equal(t, "手机端新标题", appended["customTitle"])
	assert.Equal(t, renameTestNativeID, appended["sessionId"])

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

// TestHandleRemoteAIRenameTitleWithSpecialCharsStaysSingleLine guards the
// data format: a JSONL transcript is line-delimited, so the appended record
// must remain exactly ONE physical line with an exact round-trip value even
// when the title contains newlines, JSON structural characters, or emoji.
func TestHandleRemoteAIRenameTitleWithSpecialCharsStaysSingleLine(t *testing.T) {
	cases := []struct {
		name  string
		title string
	}{
		{name: "newline", title: "第一行\n第二行"},
		{name: "quote_and_backslash", title: `他说"你好"\结束`},
		{name: "emoji", title: "🚀 好名字 ✨"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			service := newEnabledRenameService(t)
			home := os.Getenv("HOME")
			seed := seedRenameTranscript(t, home, "-home-liang-demo", renameTestNativeID,
				`{"type":"user","message":{"role":"user","content":"hi"}}`,
			)
			transcriptPath := renameTranscriptPath(home, "-home-liang-demo", renameTestNativeID)

			acks := dispatchPhoneRename(t, service, "conv-123", tc.title)

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
			assert.Equal(t, tc.title, appended["customTitle"], "title must round-trip exactly")
		})
	}
}

// TestHandleRemoteAIRenameAppendsAfterTornTailLine pins the torn-tail guard:
// when the transcript does not end in a newline (an earlier process died
// mid-write), the mirror must first close that partial line with a newline
// and then write its own record as a separate, valid line — never glue the
// record onto the existing fragment. Original bytes stay untouched.
func TestHandleRemoteAIRenameAppendsAfterTornTailLine(t *testing.T) {
	service := newEnabledRenameService(t)
	home := os.Getenv("HOME")
	torn := `{"type":"user","message":{"role":"user","content":"hi"}}`
	seed := seedRenameTranscriptRaw(t, home, "-home-liang-demo", renameTestNativeID, torn)
	transcriptPath := renameTranscriptPath(home, "-home-liang-demo", renameTestNativeID)

	acks := dispatchPhoneRename(t, service, "conv-123", "手机端新标题")

	require.Len(t, acks, 1)
	assert.Equal(t, true, acks[0]["accepted"])

	raw, err := os.ReadFile(transcriptPath)
	require.NoError(t, err)
	content := string(raw)

	// The fragment stays byte-identical, then a newline closes it, then the
	// mirror's record sits on its own line: exactly torn + "\n" + record + "\n".
	require.True(t, strings.HasPrefix(content, seed),
		"torn-tail guard must not modify the existing fragment")
	trimmed := strings.TrimSuffix(content, "\n")
	lines := strings.Split(trimmed, "\n")
	require.Len(t, lines, 2, "fragment and record must be two separate lines")
	assert.Equal(t, torn, lines[0], "existing partial line must be preserved verbatim")
	var appended map[string]string
	require.NoError(t, json.Unmarshal([]byte(lines[1]), &appended),
		"the record must be valid JSON on its own line, not glued to the fragment")
	assert.Equal(t, "custom-title", appended["type"])
	assert.Equal(t, "手机端新标题", appended["customTitle"])
}

// TestHandleRemoteAIRenameResumesByResumeSessionIDOnly covers the second
// level of the native-id ladder: a rename that only carries
// resume_session_id (no source_session_id) must still resolve the transcript
// and mirror the title.
func TestHandleRemoteAIRenameResumesByResumeSessionIDOnly(t *testing.T) {
	service := newEnabledRenameService(t)
	home := os.Getenv("HOME")
	seed := seedRenameTranscript(t, home, "-home-liang-demo", renameTestNativeID,
		`{"type":"user","message":{"role":"user","content":"hi"}}`,
	)
	transcriptPath := renameTranscriptPath(home, "-home-liang-demo", renameTestNativeID)

	acks := dispatchRename(t, service, map[string]interface{}{
		"type":              "ai.session.rename",
		"session_id":        "conv-123",
		"title":             "续跑改名",
		"resume_session_id": renameTestNativeID,
		"provider":          "claude",
	})

	require.Len(t, acks, 1)
	assert.Equal(t, true, acks[0]["accepted"])
	raw, err := os.ReadFile(transcriptPath)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(string(raw), seed))
	assert.Contains(t, string(raw), `"customTitle":"续跑改名"`)
}

// TestHandleRemoteAIRenameNonUUIDSessionIDSkipsTranscript mirrors Claude
// Code's own guard: it refuses to write a custom title for a non-UUID session
// id. A non-UUID id could glob-match an unrelated transcript, so the mirror
// must skip it while the rename itself still succeeds.
func TestHandleRemoteAIRenameNonUUIDSessionIDSkipsTranscript(t *testing.T) {
	service := newEnabledRenameService(t)
	home := os.Getenv("HOME")
	seed := seedRenameTranscript(t, home, "-home-liang-demo", "native-sid-abc",
		`{"type":"user","message":{"role":"user","content":"hi"}}`,
	)
	transcriptPath := renameTranscriptPath(home, "-home-liang-demo", "native-sid-abc")

	acks := dispatchRename(t, service, map[string]interface{}{
		"type":              "ai.session.rename",
		"session_id":        "conv-123",
		"title":             "手机端新标题",
		"source_session_id": "native-sid-abc",
		"provider":          "claude",
	})

	require.Len(t, acks, 1)
	assert.Equal(t, true, acks[0]["accepted"], "rename must still succeed via the cache")
	raw, err := os.ReadFile(transcriptPath)
	require.NoError(t, err)
	assert.Equal(t, seed, string(raw), "non-UUID session id must not touch any transcript")
}

// TestHandleRemoteAIRenameCodexProviderSkipsClaudeTranscript keeps the mirror
// Claude-only: codex/opencode transcripts live outside ~/.claude/projects, so
// a codex rename must not touch anything there — no wasted walk, no
// misleading transcript_not_found warning. Even a pathologically
// same-named file must stay byte-identical.
func TestHandleRemoteAIRenameCodexProviderSkipsClaudeTranscript(t *testing.T) {
	for _, provider := range []string{"codex", "opencode", "claudecode", "auto", ""} {
		t.Run("provider_"+provider, func(t *testing.T) {
			service := newEnabledRenameService(t)
			home := os.Getenv("HOME")
			seed := seedRenameTranscript(t, home, "-home-liang-demo", renameTestNativeID,
				`{"type":"user","message":{"role":"user","content":"hi"}}`,
			)
			transcriptPath := renameTranscriptPath(home, "-home-liang-demo", renameTestNativeID)

			payload := map[string]interface{}{
				"type":              "ai.session.rename",
				"session_id":        "conv-123",
				"title":             "手机端新标题",
				"source_session_id": renameTestNativeID,
				"project_path":      "/home/liang/demo",
			}
			if provider != "" {
				payload["provider"] = provider
			}
			acks := dispatchRename(t, service, payload)

			require.Len(t, acks, 1)
			assert.Equal(t, true, acks[0]["accepted"])

			raw, err := os.ReadFile(transcriptPath)
			require.NoError(t, err)
			switch provider {
			case "codex", "opencode":
				assert.Equal(t, seed, string(raw),
					"non-Claude provider must not touch the claude transcript")
			default:
				// claude/claudecode/auto/absent must mirror (absent keeps
				// backward compatibility with servers that omit provider).
				assert.Contains(t, string(raw), `"customTitle":"手机端新标题"`)
			}
		})
	}
}
