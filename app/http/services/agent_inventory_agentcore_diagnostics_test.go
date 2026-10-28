package services

import (
	"strings"
	"testing"

	"aliang.one/nursorgate/app/agentcore"
	"aliang.one/nursorgate/app/http/models"
)

// 这一组锁死「诊断面板旧会话回放」链路的格式契约：内核 OnTurn 终态快照 →
// appendEndDiagnostics 落 journal end 行 → reader case "end" 还原成
// AgentVibeSession.Diagnostics。写读同仓同发布，键漂移会静默双端一致，与
// journal_test 同款取舍。

// TestAgentCoreJournalEndDiagnosticsRoundtrip：带诊断块的 end 行写读往返。
func TestAgentCoreJournalEndDiagnosticsRoundtrip(t *testing.T) {
	dir := t.TempDir()
	j := newAgentCoreJournal(dir)
	j.ensureSessionHeader("conv-diag", "/tmp/proj-diag", "glm-5.2", "2026-10-28T00:00:00Z")
	j.appendUser("conv-diag", "msg-1", "帮我把这个功能做完", "2026-10-28T00:00:01Z")

	diag := &agentCoreDiagnostics{}
	diag.set(agentcore.TurnSnapshot{
		Turns:         31,
		ContextTokens: 187000,
		ContextWindow: 200000,
		DupStreak:     3,
		ErrStreak:     2,
		TruncStreak:   1,
		StopReason:    "tool_use",
		Terminal:      true,
		Reason:        "达到单次运行的最大回合数 50，已停止",
		ElapsedMs:     654321,
	})
	j.appendEndDiagnostics("conv-diag", "error", "达到单次运行的最大回合数 50，已停止", diag)

	meta := readAgentCoreSessionMetaWithOptions(
		dir+"/"+agentCoreJournalFileName("conv-diag"), agentVibeSessionReadOptions{IncludePageMeta: false})
	if meta.ID != "conv-diag" {
		t.Fatalf("ID = %q", meta.ID)
	}
	if meta.Status != "closed" {
		t.Errorf("Status = %q, want closed（列表契约不动，终态只进 Diagnostics）", meta.Status)
	}
	d := meta.Diagnostics
	if d == nil {
		t.Fatal("Diagnostics 应非 nil（end 行带诊断块）")
	}
	if d.Status != "error" {
		t.Errorf("Diagnostics.Status = %q, want error", d.Status)
	}
	if d.Turns != 31 {
		t.Errorf("Turns = %d, want 31", d.Turns)
	}
	if d.ContextTokens != 187000 || d.ContextWindow != 200000 {
		t.Errorf("Context = %d/%d, want 187000/200000", d.ContextTokens, d.ContextWindow)
	}
	if d.DupStreak != 3 || d.ErrStreak != 2 || d.TruncStreak != 1 {
		t.Errorf("守卫计数 = %d/%d/%d, want 3/2/1", d.DupStreak, d.ErrStreak, d.TruncStreak)
	}
	if d.Reason != "达到单次运行的最大回合数 50，已停止" {
		t.Errorf("Reason = %q", d.Reason)
	}
	if d.Error != d.Reason {
		t.Errorf("Error = %q, 应与 end 行 error 字段一致", d.Error)
	}
	if !d.Terminal || d.ElapsedMs != 654321 || d.StopReason != "tool_use" {
		t.Errorf("终态字段不完整: %+v", d)
	}
}

// TestAgentCoreJournalEndLegacyRow：旧格式 end 行（无 diagnostics 键）——
// Status/Error 照样上浮，Diagnostics 不为 nil 但只有回退文案，面板据此降级。
func TestAgentCoreJournalEndLegacyRow(t *testing.T) {
	dir := t.TempDir()
	j := newAgentCoreJournal(dir)
	j.ensureSessionHeader("conv-legacy", "/tmp/proj-legacy", "glm-5.2", "2026-10-28T00:00:00Z")
	j.appendUser("conv-legacy", "msg-1", "你好", "2026-10-28T00:00:01Z")
	j.appendEnd("conv-legacy", "error", "agentcore run failed")

	meta := readAgentCoreSessionMetaWithOptions(
		dir+"/"+agentCoreJournalFileName("conv-legacy"), agentVibeSessionReadOptions{})
	if meta.Diagnostics == nil {
		t.Fatal("旧 end 行也应成诊断卡（回退文案）")
	}
	if meta.Diagnostics.Status != "error" {
		t.Errorf("Diagnostics.Status = %q, want error", meta.Diagnostics.Status)
	}
	if meta.Diagnostics.Error != "agentcore run failed" {
		t.Errorf("Error = %q", meta.Diagnostics.Error)
	}
	if meta.Diagnostics.Turns != 0 {
		t.Errorf("旧行无诊断块, Turns 应为 0, got %d", meta.Diagnostics.Turns)
	}
}

// TestAgentCoreDiagnosticsPayload：payload 的键集与 progress 通道契约对齐，
// 回合快照不带 terminal/reason，终态快照带。
func TestAgentCoreDiagnosticsPayload(t *testing.T) {
	// 尚无回合 → nil，不下发空壳。
	empty := &agentCoreDiagnostics{}
	if got := empty.payload(); got != nil {
		t.Errorf("空快照 payload = %v, want nil", got)
	}

	// 回合快照：无 terminal/reason 键。
	live := &agentCoreDiagnostics{}
	live.set(agentcore.TurnSnapshot{Turns: 2, ContextTokens: 90000, ContextWindow: 200000, DupStreak: 1})
	p := live.payload()
	if p["turns"] != 2 || p["context_tokens"] != 90000 || p["context_window"] != 200000 {
		t.Errorf("回合 payload = %v", p)
	}
	if _, has := p["terminal"]; has {
		t.Error("回合 payload 不应带 terminal 键")
	}
	if _, has := p["reason"]; has {
		t.Error("回合 payload 不应带 reason 键")
	}

	// 终态快照：terminal + reason 上浮。
	live.set(agentcore.TurnSnapshot{Turns: 3, Terminal: true, Reason: "工具连续 5 次执行失败，疑似陷入重试循环"})
	p = live.payload()
	if p["terminal"] != true {
		t.Errorf("终态 payload 应带 terminal=true, got %v", p)
	}
	if !strings.Contains(p["reason"].(string), "重试循环") {
		t.Errorf("终态 payload reason = %v", p["reason"])
	}
}

// TestAgentCoreJournalEndSuccessNoDiagnosticsNoise：成功且零诊断（理论形态，
// appendEnd 直调）→ end 行不带 diagnostics 键，reader 不造空诊断卡。
func TestAgentCoreJournalEndSuccessNoDiagnosticsNoise(t *testing.T) {
	dir := t.TempDir()
	j := newAgentCoreJournal(dir)
	j.ensureSessionHeader("conv-ok", "/tmp/proj-ok", "glm-5.2", "2026-10-28T00:00:00Z")
	j.appendUser("conv-ok", "msg-1", "你好", "2026-10-28T00:00:01Z")
	j.appendEnd("conv-ok", "success", "")

	meta := readAgentCoreSessionMetaWithOptions(
		dir+"/"+agentCoreJournalFileName("conv-ok"), agentVibeSessionReadOptions{})
	if meta.Diagnostics != nil {
		t.Errorf("成功且无诊断块不应造卡, got %+v", meta.Diagnostics)
	}
}

// 编译期锚点：models 侧类型确实存在且形状稳定（防止误删字段静默破坏契约）。
var _ = models.AgentVibeDiagnostics{Turns: 1}
