package services

import (
	"strings"
	"testing"

	"aliang.one/nursorgate/app/http/models"
)

// 背景(2026-10-31 agentcore 手机无历史 8 路审计定谳):journal 逐回合内容
// 全在 kind=turn 行(OnTurnEnd 落盘,text+thinking+stop_reason+tools),
// 但 inventory 读侧 readAgentCoreSessionMetaWithOptions 的 switch 无 turn
// case,逐回合内容永远进不了 detail/扫盘上报——19 回合的内容手机端最多
// 看到每 run 一条 msg。本组测试锁死:有正文的 turn 行回放为 assistant
// 转录行;纯工具回合(text 空)无可渲染正文,Phase 2(工具/思考历史门控)
// 前跳过;thinking 随新字段携带供下游消费(server zod 剥未知字段,安全)。

// TestAgentCoreInventoryReaderTurnRowsReplay:有正文 turn 行 → assistant
// 转录行;空正文 turn 行跳过;计数与 UpdatedAt 语义与 msg 行一致。
func TestAgentCoreInventoryReaderTurnRowsReplay(t *testing.T) {
	dir := t.TempDir()
	j := newAgentCoreJournal(dir)
	j.ensureSessionHeader("conv-turn", "/tmp/proj-turn", "glm-5.2", "2026-10-30T08:27:27Z", "phone")
	j.appendUser("conv-turn", "msg-1", "我们不是已经有了每一个设备上边的基础信息", "2026-10-30T08:27:27Z")
	// 回合1:有正文+思考(应回放)
	j.appendAssistantTurn("conv-turn", 1, "我先看一下设备信息的现有实现。", "用户想展示设备基础信息,先找相关代码。", "tool_use", []string{"Bash", "peer_device_info"})
	// 回合2:纯工具回合,无正文(应跳过,Phase 2 前无可渲染内容)
	j.appendAssistantTurn("conv-turn", 2, "", "只调了工具没说话。", "tool_use", []string{"Bash"})
	// 回合3:有正文(应回放,且 id 与回合1不同)
	j.appendAssistantTurn("conv-turn", 3, "设备卡片的结构已经理清了。", "", "end_turn", nil)

	meta := readAgentCoreSessionMetaWithOptions(
		dir+"/"+agentCoreJournalFileName("conv-turn"), agentVibeSessionReadOptions{IncludePageMeta: false})
	if meta.ID != "conv-turn" {
		t.Fatalf("ID = %q", meta.ID)
	}
	if meta.MessageCount != 3 {
		t.Errorf("MessageCount = %d, want 3(user+两个有正文回合;纯工具回合不计)", meta.MessageCount)
	}
	if meta.DisplayMessageCount == nil || *meta.DisplayMessageCount != 3 {
		t.Errorf("DisplayMessageCount = %v, want 3", meta.DisplayMessageCount)
	}
	var assistantRows []models.AgentVibeMessage
	for _, m := range meta.Transcript {
		if m.Role == "assistant" {
			assistantRows = append(assistantRows, models.AgentVibeMessage{
				ID:       m.ID,
				Content:  m.Content,
				Thinking: m.Thinking,
			})
		}
	}
	if len(assistantRows) != 2 {
		t.Fatalf("assistant 转录行 = %d, want 2(空正文回合跳过); rows=%+v", len(assistantRows), meta.Transcript)
	}
	if assistantRows[0].Content != "我先看一下设备信息的现有实现。" {
		t.Errorf("回合1正文 = %q", assistantRows[0].Content)
	}
	if assistantRows[0].Thinking != "用户想展示设备基础信息,先找相关代码。" {
		t.Errorf("回合1 thinking 未随行携带 = %q", assistantRows[0].Thinking)
	}
	if assistantRows[1].Content != "设备卡片的结构已经理清了。" {
		t.Errorf("回合3正文 = %q", assistantRows[1].Content)
	}
	if assistantRows[0].ID == assistantRows[1].ID {
		t.Fatalf("turn 行 id 必须互异(同回合号跨 run 也不得相撞): %q", assistantRows[0].ID)
	}
	if !strings.HasPrefix(assistantRows[0].ID, "turn-") {
		t.Errorf("turn 行 id 应有稳定前缀, got %q", assistantRows[0].ID)
	}
}

// TestAgentCoreInventoryReaderTurnNoGhost:只有 turn 行、头行丢失的边缘
// 形态不造幽灵卡(parsedAny 语义与 end 行一致——turn 行只 enrich 已解析会话)。
// 注意:turn 行与 msg 行不同,它不携带 message_id,无头行时没有文件名兜底的
// 归属依据,与 tool 行同规跳过。
func TestAgentCoreInventoryReaderTurnHeadlessSkipped(t *testing.T) {
	dir := t.TempDir()
	j := newAgentCoreJournal(dir)
	// 故意不写头行:直接 appendAssistantTurn(无头行,session.ID 为空)
	j.appendAssistantTurn("conv-headless", 1, "无头行回合正文", "", "end_turn", nil)

	meta := readAgentCoreSessionMetaWithOptions(
		dir+"/"+agentCoreJournalFileName("conv-headless"), agentVibeSessionReadOptions{IncludePageMeta: false})
	if meta.ID != "" {
		t.Fatalf("无头行+仅 turn 行不应造出会话卡, got ID=%q", meta.ID)
	}
}
