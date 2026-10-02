package services

// Codex Desktop 会把外部(Claude)历史批量导入为新 Codex thread:台账
// ~/.codex/external_agent_session_imports.json 逐条记录 source_path /
// imported_thread_id / imported_at / source_modified_at。副本 rollout 的
// session_meta 时间 = 迁移时刻,原样上报会把旧对话顶成"刚活跃"
// (2026-10-01 14:35Z 31 条风暴事故)。本文件读台账识别导入副本:
// 原文件仍在 → 跳过(claude 通道带真实时间上报);已丢 → 按台账恢复原始时间。
// 台账缺失/损坏一律静默降级为不过滤。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"aliang.one/nursorgate/app/http/models"
)

// 导入后的副本若仍被用户继续对话(mtime/updated_at 晚于导入+5min),视为
// 已分叉的独立活跃会话,不再跳过也不再改时间。
const codexExternalImportActivitySlack = 5 * time.Minute

type codexExternalImportRecord struct {
	SourcePath       string `json:"source_path"`
	ImportedThreadID string `json:"imported_thread_id"`
	ImportedAt       int64  `json:"imported_at"`        // unix 秒
	SourceModifiedAt int64  `json:"source_modified_at"` // unix 纳秒
	Title            string `json:"title"`
}

type codexExternalImportLedger struct {
	byThread map[string]codexExternalImportRecord
}

func loadCodexExternalImportLedger(home string) *codexExternalImportLedger {
	out := &codexExternalImportLedger{byThread: map[string]codexExternalImportRecord{}}
	if strings.TrimSpace(home) == "" {
		return out
	}
	raw, err := os.ReadFile(filepath.Join(home, ".codex", "external_agent_session_imports.json"))
	if err != nil {
		return out
	}
	var doc struct {
		Records []codexExternalImportRecord `json:"records"`
	}
	if json.Unmarshal(raw, &doc) != nil {
		return out
	}
	for _, rec := range doc.Records {
		if strings.TrimSpace(rec.ImportedThreadID) != "" {
			out.byThread[rec.ImportedThreadID] = rec
		}
	}
	return out
}

// resolve 返回 (skip, restoredAt)。skip=true → 不上报该副本;restoredAt 非空 →
// 用它覆盖 CreatedAt/UpdatedAt(原文件已丢,台账 source_modified_at 是唯一可信来源)。
func (l *codexExternalImportLedger) resolve(session models.AgentVibeSession) (bool, string) {
	rec, ok := l.byThread[strings.TrimPrefix(session.ID, "codex_")]
	if !ok {
		return false, ""
	}
	importedAt := time.Unix(rec.ImportedAt, 0)
	if parseAgentRFC3339(session.UpdatedAt).After(importedAt.Add(codexExternalImportActivitySlack)) {
		return false, ""
	}
	if rec.SourcePath != "" {
		if _, err := os.Stat(rec.SourcePath); err == nil {
			return true, ""
		}
	}
	if rec.SourceModifiedAt > 0 {
		return false, time.Unix(0, rec.SourceModifiedAt).UTC().Format(time.RFC3339)
	}
	return false, ""
}
