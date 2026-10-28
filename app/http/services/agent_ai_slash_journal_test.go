package services

// 斜杠命令 journal 留痕（spec §4.5 + amendment）：agentcore 无原生转录，本地
// 应答的斜杠命令（/clear /model /help /cost）与 /compact 必须各自落
// user+assistant 两行——否则 journal 读回（跨端续聊/快照）看不到这些回合。
// claude/codex 由各自原生转录承载，journal 必须零写入（防回归）。

import (
	"os"
	"testing"
)

// newSlashJournalTestManager 建管理器并把 coreJournal 指到临时目录——
// newAgentAIManager 默认接真实 agentCoreJournalDir()，测试绝不落真家目录。
func newSlashJournalTestManager(t *testing.T, dir string) *agentAIManager {
	t.Helper()
	m := newAgentAIManager()
	m.coreJournal = newAgentCoreJournal(dir)
	return m
}

func TestLocalSlashCommandJournaled(t *testing.T) {
	dir := t.TempDir()
	m := newSlashJournalTestManager(t, dir)
	m.mu.Lock()
	m.sessions["s-slash"] = &agentAISession{id: "s-slash", provider: "agentcore", mode: "vibe", surface: "phone", originSocket: "remote"}
	m.mu.Unlock()
	sess := func() *agentAISession { m.mu.Lock(); defer m.mu.Unlock(); return m.sessions["s-slash"] }()
	if !m.handleLocalSlashCommand(sess, "msg-1", "/help", "agentcore", func(interface{}) error { return nil }) {
		t.Fatal("/help 应被本地接管")
	}

	hist, _, err := readAgentCoreJournalHistoryFrom(dir, "s-slash")
	if err != nil {
		t.Fatalf("journal 未落: %v", err)
	}
	if len(hist) != 2 || hist[0].Role != "user" || hist[0].Content != "/help" || hist[1].Role != "assistant" {
		t.Fatalf("斜杠命令应落 user+assistant 两行,实际 %+v", hist)
	}
	if hist[1].MessageID != "assistant_msg-1" {
		t.Errorf("assistant 行 message_id = %q, want assistant_msg-1", hist[1].MessageID)
	}
	// 头行 origin 推导（Task 10 helper）：originSocket=remote → phone。
	lines := readJournalLines(t, dir, "s-slash")
	if lines[0]["kind"] != "session" {
		t.Fatalf("首行应为 session 头行,实际 %v", lines[0]["kind"])
	}
	if lines[0]["origin"] != "phone" {
		t.Fatalf("origin 推导错: %v", lines[0]["origin"])
	}
}

func TestAgentCoreCompactJournaled(t *testing.T) {
	dir := t.TempDir()
	m := newSlashJournalTestManager(t, dir)
	m.mu.Lock()
	m.sessions["s-c"] = &agentAISession{id: "s-c", provider: "agentcore", surface: "phone", originSocket: "remote"}
	m.mu.Unlock()
	sess := func() *agentAISession { m.mu.Lock(); defer m.mu.Unlock(); return m.sessions["s-c"] }()
	// 空历史：Compact 在 agentCoreEnsureUpstream 之前就收敛（无网络），走
	// 「没有可压缩的上下文」分支——恰好同时覆盖命令行落痕与失败分支的
	// assistant 行。
	m.handleAgentCoreCompactCommand(sess, "msg-2", func(interface{}) error { return nil })

	hist, _, err := readAgentCoreJournalHistoryFrom(dir, "s-c")
	if err != nil || len(hist) < 1 || hist[0].Content != "/compact" {
		t.Fatalf("/compact 命令行未落: err=%v hist=%+v", err, hist)
	}
	if len(hist) < 2 || hist[1].Role != "assistant" || hist[1].Content == "" {
		t.Fatalf("无上下文分支应落 assistant 应答行: %+v", hist)
	}
}

// claude/codex 通道不落 journal（spec amendment）：/compact 是真实模型回合、
// 历史由原生转录承载，journal 必须保持零写入。
func TestLocalSlashCommandClaudeNotJournaled(t *testing.T) {
	dir := t.TempDir()
	m := newSlashJournalTestManager(t, dir)
	sess := &agentAISession{id: "s-claude", provider: "claudecode", mode: "vibe"}
	if !m.handleLocalSlashCommand(sess, "msg-3", "/help", "claudecode", func(interface{}) error { return nil }) {
		t.Fatal("claudecode /help 应被本地接管")
	}
	if _, err := os.Stat(dir + "/" + agentCoreJournalFileName("s-claude")); !os.IsNotExist(err) {
		t.Fatalf("claude 通道不得写 journal,文件状态: %v", err)
	}
}
