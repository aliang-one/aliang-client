package services

import (
	"strings"
	"sync"
	"testing"

	"aliang.one/nursorgate/app/http/models"
)

// 背景(2026-10-31 健康会话重复气泡防御):直播链路每条 assistant 消息
// 已按原生 id 逐条落库(server 实测 ai_5LJvi97hfkrf=69 行),journal 若再
// 留「run 级累计 assistant 行」+「逐回合 turn 行」双表示,detail 回放会把
// 两种表示都交给 server——累计行内容=各回合拼接,与任何单行都不相等,
// dropContentDuplicates 接不住 → 手机出现重复大气泡。
// 收口为单一真源:带正文回合完成后,-partial 与 run 级累计行都不再写
// (turn 行已全覆盖);零带正文回合的在途半截文本仍由 -partial 保留。

// TestJournalPartialAssistantSkippedWhenTextTurnCompleted:run 已有带正文
// 回合完成时,-partial 不落行(重叠内容,turn 行已保)。
func TestJournalPartialAssistantSkippedWhenTextTurnCompleted(t *testing.T) {
	dir := t.TempDir()
	j := newAgentCoreJournal(dir)
	j.ensureSessionHeader("conv-gate", "/tmp/proj-gate", "glm-5.2", "", "2026-10-31T00:00:00Z", "phone")
	diag := &agentCoreDiagnostics{}
	diag.noteTextTurn() // 回合1 已带正文完成

	var out strings.Builder
	out.WriteString("回合2流到一半的文本")
	outMu := &sync.Mutex{}
	run := agentAIRun{sessionID: "conv-gate", messageID: "msg_create_x"}
	m := &agentAIManager{}
	m.journalPartialAssistant(j, diag, run, &out, outMu)

	meta := readAgentCoreSessionMetaWithOptions(
		dir+"/"+agentCoreJournalFileName("conv-gate"), agentVibeSessionReadOptions{IncludePageMeta: false})
	for _, row := range meta.Transcript {
		if row.Role == "assistant" {
			t.Fatalf("带正文回合已完成时 -partial 不应落行, got %+v", row)
		}
	}
}

// TestJournalPartialAssistantKeptWithoutTextTurn:零带正文回合(回合1 在途
// 死亡)时,-partial 照旧落行——在途半截文本的唯一载体,不能丢。
func TestJournalPartialAssistantKeptWithoutTextTurn(t *testing.T) {
	dir := t.TempDir()
	j := newAgentCoreJournal(dir)
	j.ensureSessionHeader("conv-gate2", "/tmp/proj-gate2", "glm-5.2", "", "2026-10-31T00:00:00Z", "phone")
	diag := &agentCoreDiagnostics{} // 无带正文回合

	var out strings.Builder
	out.WriteString("回合1流到一半的文本")
	outMu := &sync.Mutex{}
	run := agentAIRun{sessionID: "conv-gate2", messageID: "msg_create_y"}
	m := &agentAIManager{}
	m.journalPartialAssistant(j, diag, run, &out, outMu)

	meta := readAgentCoreSessionMetaWithOptions(
		dir+"/"+agentCoreJournalFileName("conv-gate2"), agentVibeSessionReadOptions{IncludePageMeta: false})
	found := false
	for _, row := range meta.Transcript {
		if row.Role == "assistant" && strings.Contains(row.Content, "回合1流到一半") {
			found = true
		}
	}
	if !found {
		t.Fatalf("零带正文回合时 -partial 必须保留, transcript=%+v", meta.Transcript)
	}
}

// TestAgentCoreDiagnosticsNoteTextTurn:note/has 原语的并发安全语义。
func TestAgentCoreDiagnosticsNoteTextTurn(t *testing.T) {
	d := &agentCoreDiagnostics{}
	if d.hasTextTurns() {
		t.Fatal("初始必须为 false")
	}
	d.noteTextTurn()
	if !d.hasTextTurns() {
		t.Fatal("noteTextTurn 后必须为 true")
	}
	var _ models.AgentVibeMessage // 保持 models 引用(与包内其他测试一致)
}
