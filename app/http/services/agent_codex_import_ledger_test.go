package services

// Codex Desktop 导入 Claude 历史会生成台账 ~/.codex/external_agent_session_imports.json,
// 副本 rollout 的时间 = 迁移时刻;原样上报会把旧对话顶成"刚活跃"(2026-10-01 事故)。
// 本文件覆盖台账加载与副本判定:跳过(原文件仍在)/保留(导入后仍活跃)/时间恢复(原文件已丢)。

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"aliang.one/nursorgate/app/http/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const codexImportThreadID = "01a0f7e3-ed1c-7d22-84d5-6df93b74e2e0"

func writeLedgerWithSource(t *testing.T, home string, sourcePath string, sourceExists bool) {
	t.Helper()
	dir := filepath.Join(home, ".codex")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	if sourceExists {
		require.NoError(t, os.MkdirAll(filepath.Dir(sourcePath), 0o755))
		require.NoError(t, os.WriteFile(sourcePath, []byte("{}\n"), 0o644))
	}
	body := "{\"records\":[{\"source_path\":\"" + sourcePath + "\",\"imported_thread_id\":\"" + codexImportThreadID + "\",\"imported_at\":1790865305,\"source_modified_at\":1790851733581106525,\"title\":\"检查app审核\"}]}"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "external_agent_session_imports.json"), []byte(body), 0o644))
}

func codexImportSession(updatedAt string) models.AgentVibeSession {
	return models.AgentVibeSession{ID: "codex_" + codexImportThreadID, UpdatedAt: updatedAt}
}

func TestCodexExternalImportLedger(t *testing.T) {
	t.Run("no ledger file → not filtered", func(t *testing.T) {
		ledger := loadCodexExternalImportLedger(t.TempDir())
		skip, restored := ledger.resolve(codexImportSession("2026-10-01T14:00:00Z"))
		assert.False(t, skip)
		assert.Empty(t, restored)
	})

	t.Run("empty home → not filtered", func(t *testing.T) {
		ledger := loadCodexExternalImportLedger("")
		skip, restored := ledger.resolve(codexImportSession("2026-10-01T14:00:00Z"))
		assert.False(t, skip)
		assert.Empty(t, restored)
	})

	t.Run("malformed ledger json → not filtered", func(t *testing.T) {
		home := t.TempDir()
		dir := filepath.Join(home, ".codex")
		require.NoError(t, os.MkdirAll(dir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "external_agent_session_imports.json"), []byte("{oops"), 0o644))
		ledger := loadCodexExternalImportLedger(home)
		skip, restored := ledger.resolve(codexImportSession("2026-10-01T14:00:00Z"))
		assert.False(t, skip)
		assert.Empty(t, restored)
	})

	t.Run("copy idle and source exists → skip", func(t *testing.T) {
		home := t.TempDir()
		source := filepath.Join(home, ".claude", "projects", "proj", "2d96defe.jsonl")
		writeLedgerWithSource(t, home, source, true)
		ledger := loadCodexExternalImportLedger(home)
		// 会话时间=导入前(2026-10-01T10:00Z < imported_at 14:35:05Z) → 无导入后活跃
		skip, restored := ledger.resolve(codexImportSession("2026-10-01T10:00:00Z"))
		assert.True(t, skip)
		assert.Empty(t, restored)
	})

	t.Run("copy active after import → keep native", func(t *testing.T) {
		home := t.TempDir()
		source := filepath.Join(home, ".claude", "projects", "proj", "2d96defe.jsonl")
		writeLedgerWithSource(t, home, source, true)
		ledger := loadCodexExternalImportLedger(home)
		// imported_at=14:35:05Z,slack 5min;16:00Z 晚于 → 用户继续在 Codex 里聊
		skip, restored := ledger.resolve(codexImportSession("2026-10-01T16:00:00Z"))
		assert.False(t, skip)
		assert.Empty(t, restored)
	})

	t.Run("source missing → restore ledger time", func(t *testing.T) {
		home := t.TempDir()
		source := filepath.Join(home, ".claude", "projects", "proj", "gone.jsonl")
		writeLedgerWithSource(t, home, source, false)
		ledger := loadCodexExternalImportLedger(home)
		skip, restored := ledger.resolve(codexImportSession("2026-10-01T10:00:00Z"))
		assert.False(t, skip)
		want := time.Unix(0, 1790851733581106525).UTC().Format(time.RFC3339)
		assert.Equal(t, want, restored)
	})

	t.Run("unknown thread → not filtered", func(t *testing.T) {
		home := t.TempDir()
		source := filepath.Join(home, ".claude", "projects", "proj", "2d96defe.jsonl")
		writeLedgerWithSource(t, home, source, true)
		ledger := loadCodexExternalImportLedger(home)
		other := models.AgentVibeSession{ID: "codex_ffffffff-1111-2222-3333-444444444444", UpdatedAt: "2026-10-01T10:00:00Z"}
		skip, restored := ledger.resolve(other)
		assert.False(t, skip)
		assert.Empty(t, restored)
	})
}
