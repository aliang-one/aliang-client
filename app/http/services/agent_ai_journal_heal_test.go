package services

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// 2026-11-07 存量错位治愈:手机续聊曾把回合写进 ai_import_<hash>.jsonl(壳 id
// 冒充原生 id 的历史产物),桌面清单因此多出「新对话」。heal 把这类拆分 journal
// 并回 canonical 会话文件并删除源文件。存量绑定拓扑=链式(壳绑定的 native 本身
// 是另一绑定的 ConversationID,评审实锤);同 native 形态兼容保留。
func journalLines(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var lines []string
	for _, line := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func writeJournalFile(t *testing.T, dir, sessionID string, rows []string) {
	t.Helper()
	var b strings.Builder
	b.WriteString(`{"created_at":"2026-11-07T00:00:00Z","kind":"session","model":"glm-5.3","origin":"phone","project_path":"/tmp/proj","session_id":"` + sessionID + `"}` + "\n")
	for _, row := range rows {
		b.WriteString(row + "\n")
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, agentCoreJournalFileName(sessionID)), []byte(b.String()), 0o600))
}

func newHealTestManager(t *testing.T) (*agentAIManager, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	manager := newAgentAIManager()
	journalDir := t.TempDir()
	manager.coreJournal = newAgentCoreJournal(journalDir)
	return manager, journalDir
}

const healMsgRow = `{"kind":"msg","message_id":"m-x","role":"user","text":"手机追问","ts":"2026-11-07T01:00:00Z"}`

func TestHealSplitAgentCoreJournalsChainedTopology(t *testing.T) {
	manager, dir := newHealTestManager(t)
	writeJournalFile(t, dir, "local-a", []string{healMsgRow, healMsgRow})
	writeJournalFile(t, dir, "ai_import_h", []string{healMsgRow, healMsgRow, healMsgRow})
	manager.mu.Lock()
	manager.bindings["local-a"] = agentAIBindingRecord{
		ConversationID: "local-a", Provider: "agentcore", NativeSessionID: "agentcore-1-1", State: "confirmed", BindingVersion: 1,
	}
	manager.bindings["ai_import_h"] = agentAIBindingRecord{
		// 存量主形态(链式):壳绑定的 native=壳 id 冒充,即另一绑定的 ConversationID。
		ConversationID: "ai_import_h", Provider: "agentcore", NativeSessionID: "local-a", State: "confirmed", BindingVersion: 1,
	}
	manager.mu.Unlock()

	manager.healSplitAgentCoreJournals()

	_, err := os.Stat(filepath.Join(dir, agentCoreJournalFileName("ai_import_h")))
	require.True(t, os.IsNotExist(err), "source split journal must be removed")
	merged := journalLines(t, filepath.Join(dir, agentCoreJournalFileName("local-a")))
	require.Len(t, merged, 6, "header + 2 desktop rows + 3 phone rows")
	require.NotContains(t, merged[0], "ai_import_h")
	manager.mu.Lock()
	_, orphanLeft := manager.bindings["ai_import_h"]
	manager.mu.Unlock()
	require.False(t, orphanLeft, "orphan shell binding must be dropped")
}

func TestHealSplitAgentCoreJournalsSameNativeTopology(t *testing.T) {
	manager, dir := newHealTestManager(t)
	writeJournalFile(t, dir, "local-b", []string{healMsgRow})
	writeJournalFile(t, dir, "ai_import_h2", []string{healMsgRow})
	manager.mu.Lock()
	manager.bindings["local-b"] = agentAIBindingRecord{
		ConversationID: "local-b", Provider: "agentcore", NativeSessionID: "agentcore-9-9", State: "confirmed", BindingVersion: 1,
	}
	manager.bindings["ai_import_h2"] = agentAIBindingRecord{
		ConversationID: "ai_import_h2", Provider: "agentcore", NativeSessionID: "agentcore-9-9", State: "confirmed", BindingVersion: 1,
	}
	manager.mu.Unlock()

	manager.healSplitAgentCoreJournals()

	_, err := os.Stat(filepath.Join(dir, agentCoreJournalFileName("ai_import_h2")))
	require.True(t, os.IsNotExist(err))
	merged := journalLines(t, filepath.Join(dir, agentCoreJournalFileName("local-b")))
	require.Len(t, merged, 3, "header + 1 + 1")
}

func TestHealSplitAgentCoreJournalsSkipsUnmappedRunningOrMissing(t *testing.T) {
	manager, dir := newHealTestManager(t)
	writeJournalFile(t, dir, "local-c", []string{healMsgRow})
	writeJournalFile(t, dir, "ai_import_h3", []string{healMsgRow})
	writeJournalFile(t, dir, "ai_import_h4", []string{healMsgRow})
	manager.mu.Lock()
	// h3:无伙伴绑定(无法定位 canonical)→ 不动。
	manager.bindings["ai_import_h3"] = agentAIBindingRecord{
		ConversationID: "ai_import_h3", Provider: "agentcore", NativeSessionID: "agentcore-no-partner", State: "confirmed", BindingVersion: 1,
	}
	// h4:目标会话运行中 → 不动。
	manager.bindings["local-c"] = agentAIBindingRecord{
		ConversationID: "local-c", Provider: "agentcore", NativeSessionID: "local-c-target", State: "confirmed", BindingVersion: 1,
	}
	manager.bindings["ai_import_h4"] = agentAIBindingRecord{
		ConversationID: "ai_import_h4", Provider: "agentcore", NativeSessionID: "local-c-target", State: "confirmed", BindingVersion: 1,
	}
	manager.sessions["local-c"] = &agentAISession{id: "local-c", provider: "agentcore", cancel: func() {}}
	manager.mu.Unlock()

	manager.healSplitAgentCoreJournals()

	for _, id := range []string{"ai_import_h3", "ai_import_h4"} {
		_, err := os.Stat(filepath.Join(dir, agentCoreJournalFileName(id)))
		require.NoError(t, err, "unmapped/running-target source must stay untouched: "+id)
	}
}
