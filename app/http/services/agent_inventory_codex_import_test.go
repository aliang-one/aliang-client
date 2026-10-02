package services

// 集成:collectCodexVibeSessions 结合台账应跳过"导入副本"(原 Claude 文件仍在、
// 无导入后活跃);原文件丢失时用台账时间恢复;无台账时行为不变。
// collectCodexVibeSessions 经 agentHome()→$HOME 解析,t.Setenv 注入临时 home。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"aliang.one/nursorgate/app/http/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupCodexImportFixture(t *testing.T) (home, sourcePath, threadID string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)

	threadID = "01a0f7e3-ed1c-7d22-84d5-6df93b74e2e0"
	codexDir := filepath.Join(home, ".codex")
	require.NoError(t, os.MkdirAll(filepath.Join(codexDir, "sessions"), 0o755))

	// session_index.jsonl: 一条导入副本,index 时间=导入前(无导入后活跃)
	indexLine, err := json.Marshal(map[string]string{
		"id":          threadID,
		"thread_name": "检查app审核",
		"updated_at":  "2026-10-01T14:00:00Z",
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(
		filepath.Join(codexDir, "session_index.jsonl"),
		[]byte(string(indexLine)+"\n"), 0o644))

	// rollout 副本文件(session_meta 时间=迁移时刻)
	meta, err := json.Marshal(map[string]any{
		"timestamp": "2026-10-01T14:35:04Z",
		"type":      "session_meta",
		"payload":   map[string]any{"id": threadID, "cwd": "/projects/foo", "model": "gpt-5"},
	})
	require.NoError(t, err)
	rollout := string(meta) + "\n" +
		`{"timestamp":"2026-10-01T14:35:05Z","type":"message","payload":{"role":"user","content":"检查，为什么我们的app审核没有通过"}}` + "\n"
	rolloutPath := filepath.Join(codexDir, "sessions", "rollout-2026-10-01T22-35-04-"+threadID+".jsonl")
	require.NoError(t, os.WriteFile(rolloutPath, []byte(rollout), 0o644))
	// 副本文件 mtime 压到导入前,排除 mtime 活跃误判
	stamp := time.Unix(1790861734, 0) // 2026-10-01T13:35:34Z < imported_at
	require.NoError(t, os.Chtimes(rolloutPath, stamp, stamp))

	// 台账:source_path 指向原 Claude 文件(存在与否由用例决定)
	sourcePath = filepath.Join(home, ".claude", "projects", "proj", "2d96defe.jsonl")
	ledger := "{\"records\":[{\"source_path\":\"" + sourcePath +
		"\",\"imported_thread_id\":\"" + threadID +
		"\",\"imported_at\":1790865305,\"source_modified_at\":1790851733581106525,\"title\":\"检查app审核\"}]}"
	require.NoError(t, os.WriteFile(
		filepath.Join(codexDir, "external_agent_session_imports.json"), []byte(ledger), 0o644))

	return home, sourcePath, threadID
}

func codexSessionByID(sessions []models.AgentVibeSession, threadID string) *models.AgentVibeSession {
	for i := range sessions {
		if sessions[i].ID == "codex_"+threadID {
			return &sessions[i]
		}
	}
	return nil
}

func TestCollectCodexVibeSessionsImportLedger(t *testing.T) {
	t.Run("source exists → copy skipped", func(t *testing.T) {
		_, source, threadID := setupCodexImportFixture(t)
		require.NoError(t, os.MkdirAll(filepath.Dir(source), 0o755))
		require.NoError(t, os.WriteFile(source, []byte("{}\n"), 0o644))

		sessions := collectCodexVibeSessions(nil)
		assert.Nil(t, codexSessionByID(sessions, threadID), "导入副本应被跳过")
	})

	t.Run("source missing → copy restored from ledger", func(t *testing.T) {
		_, _, threadID := setupCodexImportFixture(t) // 原文件不创建

		sessions := collectCodexVibeSessions(nil)
		copy := codexSessionByID(sessions, threadID)
		require.NotNil(t, copy, "原文件已丢时副本应保留")
		want := time.Unix(0, 1790851733581106525).UTC().Format(time.RFC3339)
		assert.Equal(t, want, copy.CreatedAt)
		assert.Equal(t, want, copy.UpdatedAt)
	})

	t.Run("no ledger → copy kept with native times", func(t *testing.T) {
		home, _, threadID := setupCodexImportFixture(t)
		require.NoError(t, os.Remove(filepath.Join(home, ".codex", "external_agent_session_imports.json")))

		sessions := collectCodexVibeSessions(nil)
		copy := codexSessionByID(sessions, threadID)
		require.NotNil(t, copy)
		assert.Equal(t, "2026-10-01T14:00:00Z", copy.UpdatedAt)
	})
}
