package services

// 斜杠命令 journal 留痕（spec §4.5 + amendment）：agentcore 无原生转录，本地
// 应答的斜杠命令（/clear /model /help /cost）与 /compact 必须各自落
// user+assistant 两行——否则 journal 读回（跨端续聊/快照）看不到这些回合。
// claude/codex 由各自原生转录承载，journal 必须零写入（防回归）。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"aliang.one/nursorgate/app/http/models"
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

// ---------------------------------------------------------------------------
// 内核内置工具 tool 行（Task 13，spec §4.5）：summarize 确定性/截断 + 桥接
// 工具不双记的不变量
// ---------------------------------------------------------------------------

// TestSummarizeAgentCoreKernelToolDeterministic：map 遍历随机，键序必须 sort
// 兜底——同一输入两次摘要必须逐字节一致，否则同一行输入每次落盘形状都不同。
func TestSummarizeAgentCoreKernelToolDeterministic(t *testing.T) {
	input := map[string]interface{}{"command": "ls -la", "timeout": 30, "z_last": true}
	a := summarizeAgentCoreKernelTool("Bash", input)
	for i := 0; i < 50; i++ {
		if got := summarizeAgentCoreKernelTool("Bash", input); got != a {
			t.Fatalf("摘要不确定: %q vs %q", got, a)
		}
	}
	if a != "Bash command=ls -la timeout=30 z_last=true" {
		t.Fatalf("摘要形状不对: %q", a)
	}
}

// TestSummarizeAgentCoreKernelToolRuneSafeTruncation：超限截断必须按 rune，
// 切点不得劈开多字节字符（36bcc43 教训）——截后仍是合法 UTF-8，且带截断尾缀。
func TestSummarizeAgentCoreKernelToolRuneSafeTruncation(t *testing.T) {
	pad := strings.Repeat("内", 5000) // 多字节，若按字节切必劈出 U+FFFD
	got := summarizeAgentCoreKernelTool("Read", map[string]interface{}{"path": pad})
	if !utf8.ValidString(got) {
		t.Fatal("截断后不是合法 UTF-8（多字节 rune 被劈开）")
	}
	runes := []rune(got)
	suffixRunes := utf8.RuneCountInString("…(truncated)")
	if len(runes) != agentCoreKernelToolSummaryCapRune+suffixRunes {
		t.Fatalf("截断长度 = %d, want %d", len(runes), agentCoreKernelToolSummaryCapRune+suffixRunes)
	}
	if !strings.HasSuffix(got, "…(truncated)") {
		t.Fatalf("缺截断尾缀: %q", runes[len(runes)-20:])
	}
	if strings.ContainsRune(got, utf8.RuneError) {
		t.Fatal("截断点出现 U+FFFD")
	}
	// 不超限的输入原样返回，不加尾缀。
	short := summarizeAgentCoreKernelTool("Bash", map[string]interface{}{"command": "ls"})
	if strings.HasSuffix(short, "(truncated)") {
		t.Fatalf("短输入不该截断: %q", short)
	}
}

// TestAgentCoreKernelBuiltinToolsExcludeBridges：双记防线的不变量——按内置名集
// 过滤后，内置工具记行、桥接工具不记（MCP 恒 mcp__ 前缀不在集内；子 agent 桥
// 名消解时避开 reserved 也不在集内）。防线破洞 = 同一次调用落两行 tool。
func TestAgentCoreKernelBuiltinToolsExcludeBridges(t *testing.T) {
	for _, name := range []string{"Bash", "Read", "Edit", "Write"} {
		if !agentCoreKernelBuiltinTools[name] {
			t.Errorf("内置工具 %s 应在集内（不记就丢了 spec §4.5 的缺口修复）", name)
		}
	}
	for _, name := range []string{"mcp__srv__lookup", "mcp__mcp__x", "my-subagent"} {
		if agentCoreKernelBuiltinTools[name] {
			t.Errorf("桥接工具 %s 不得在集内（各自 onCall 已落行，在集内=双记）", name)
		}
	}
}

// ---------------------------------------------------------------------------
// 审批审计行（Task 14，spec §4.5）：决策/取消/超时统一落 kind=approval
// ---------------------------------------------------------------------------

// TestApprovalDecisionJournaledAsAuditRow：审批终态必须落审计行。决策
// （accept/decline）、取消、超时全走 rememberCompletedApprovalLocked 这个
// 汇聚点，审计行自动覆盖全部终态；读侧（inventory/history）对未知 kind
// 忽略，不污染历史回放。
func TestApprovalDecisionJournaledAsAuditRow(t *testing.T) {
	dir := t.TempDir()
	m := newAgentAIManager()
	m.coreJournal = newAgentCoreJournal(dir)
	m.coreJournal.ensureSessionHeader("s-audit", "/p", "m", "", time.Now().UTC().Format(time.RFC3339), "phone", nil, nil, "")
	m.mu.Lock()
	m.rememberCompletedApprovalLocked("ap-1", "s-audit", 3, agentAIApprovalResponse{Decision: models.AgentAIApprovalDecisionDecline})
	m.mu.Unlock()
	data, err := os.ReadFile(filepath.Join(dir, agentCoreJournalFileName("s-audit")))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"kind":"approval"`) {
		t.Fatalf("缺审批审计行: %s", data)
	}
	// 逐字段：决策值与 runSeq 必须如实落行，回放端才能重建「哪一回合的哪个
	// 审批被怎么处理了」。
	var audit map[string]interface{}
	for _, row := range readJournalLines(t, dir, "s-audit") {
		if row["kind"] == "approval" {
			audit = row
		}
	}
	if audit == nil {
		t.Fatal("无 approval 行")
	}
	if audit["approval_id"] != "ap-1" {
		t.Errorf("approval_id = %v, want ap-1", audit["approval_id"])
	}
	if audit["decision"] != models.AgentAIApprovalDecisionDecline {
		t.Errorf("decision = %v, want %q", audit["decision"], models.AgentAIApprovalDecisionDecline)
	}
	if audit["run_seq"] != float64(3) {
		t.Errorf("run_seq = %v, want 3", audit["run_seq"])
	}
}

// ---------------------------------------------------------------------------
// agentcore /clear 真实清空（2026-11-15）：内存 + journal 磁盘两侧都清——
// 只清内存会被下一帧 create/message 的 journal 读回打穿，「清空」形同虚设。
// ---------------------------------------------------------------------------

// TestAgentCoreClearCommandJournaled：/clear 应被本地接管、journal 重写为
// 头行+命令行+应答行（配置保留、旧历史不复活）、内存历史清空。命令行重落
// 垫在应答之前是硬约束：终态若 assistant-first，读回装成首条历史后上游
// Messages 契约按角色序拒收，清空过的会话整轮 400。
func TestAgentCoreClearCommandJournaled(t *testing.T) {
	dir := t.TempDir()
	m := newSlashJournalTestManager(t, dir)
	m.mu.Lock()
	sess := &agentAISession{
		id: "s-clear", provider: "agentcore", mode: "vibe", model: "glm-5.3",
		surface: "phone", originSocket: "remote",
		history: []agentAIMessage{
			{Role: "user", MessageID: "u1", Content: "旧问题"},
			{Role: "assistant", MessageID: "a1", Content: "旧回答"},
		},
		initialContext: "立身上下文",
	}
	m.sessions["s-clear"] = sess
	m.mu.Unlock()

	// 先落几条真历史进 journal（模拟既有会话）。
	m.coreJournal.ensureSessionHeader("s-clear", "/tmp/proj", "glm-5.3", "", time.Now().UTC().Format(time.RFC3339), "phone", nil, nil, "")
	m.coreJournal.appendUser("s-clear", "u1", "旧问题", time.Now().UTC().Format(time.RFC3339))
	m.coreJournal.appendAssistant("s-clear", "a1", "旧回答", time.Now().UTC().Format(time.RFC3339))

	if !m.handleLocalSlashCommand(sess, "msg-clear", "/clear", "agentcore", func(interface{}) error { return nil }) {
		t.Fatal("/clear 应被本地接管")
	}

	// 内存侧：历史与立身上下文清空、runSeq 归零。
	m.mu.Lock()
	cleared := m.sessions["s-clear"]
	emptyHist := len(cleared.history) == 0 && cleared.initialContext == "" && cleared.runSeq == 0
	m.mu.Unlock()
	if !emptyHist {
		t.Fatalf("内存历史未清空: %+v", cleared.history)
	}

	// 磁盘侧：头行保留、旧内容行全清、/clear 命令行重落 + 应答行按 spec §4.5
	// 留痕；模型配置不丢。顺序必须 user→assistant（上游角色序契约）。
	lines := readJournalLines(t, dir, "s-clear")
	if len(lines) != 3 || lines[0]["kind"] != "session" {
		t.Fatalf("/clear 后 journal 应为 头行+命令行+应答行,实际 %d 行: %v", len(lines), lines)
	}
	if lines[0]["model"] != "glm-5.3" {
		t.Errorf("头行 model = %v, want glm-5.3（配置必须保留）", lines[0]["model"])
	}
	if lines[1]["kind"] != "msg" || lines[1]["role"] != "user" || lines[1]["text"] != "/clear" {
		t.Errorf("第二行应为重落的 /clear 命令行,实际 %v", lines[1])
	}
	mid, _ := lines[1]["message_id"].(string)
	if !strings.HasPrefix(mid, "msg-clear-clear-") {
		t.Errorf("重落命令行 message_id = %q, want msg-clear-clear- 前缀（同 id 重落会被 written 抑制,必须派生）", mid)
	}
	if lines[2]["kind"] != "msg" || lines[2]["message_id"] != "assistant_msg-clear" {
		t.Errorf("第三行应为 /clear 应答行,实际 %v", lines[2])
	}
	// 读回 = /clear 命令行 + 应答（旧历史不复活）；首条必须是 user。
	hist, _, err := readAgentCoreJournalHistoryFrom(dir, "s-clear")
	if err != nil {
		t.Fatalf("readback: %v", err)
	}
	if len(hist) != 2 || hist[0].Role != "user" || hist[1].Role != "assistant" {
		t.Fatalf("读回历史应 user→assistant 两行,实际 %+v", hist)
	}
}

// TestAgentCoreClearCommandReplayStaysUserFirst：同 messageID 重发 /clear（幂
// 等重试）会二次 reset——重落命令行的派生 id 必须不被 written 去重键抑制，
// 否则第二次重放后 journal 又回到 assistant-first。应答行同 id 会被抑制
// （已知且无害：重放是异常路径，历史仍 user-first 合法，仅缺一条应答留痕）。
func TestAgentCoreClearCommandReplayStaysUserFirst(t *testing.T) {
	dir := t.TempDir()
	m := newSlashJournalTestManager(t, dir)
	m.mu.Lock()
	sess := &agentAISession{id: "s-replay", provider: "agentcore", mode: "vibe", model: "glm-5.3"}
	m.sessions["s-replay"] = sess
	m.mu.Unlock()
	m.coreJournal.ensureSessionHeader("s-replay", "/tmp/proj", "glm-5.3", "", time.Now().UTC().Format(time.RFC3339), "phone", nil, nil, "")
	m.coreJournal.appendUser("s-replay", "u1", "旧问题", time.Now().UTC().Format(time.RFC3339))

	take := func() {
		if !m.handleLocalSlashCommand(sess, "msg-replay", "/clear", "agentcore", func(interface{}) error { return nil }) {
			t.Fatal("/clear 应被本地接管")
		}
	}
	take()
	take() // 同 id 重放:第二次 reset 后命令行仍必须真正落盘(派生 id 避让去重)

	lines := readJournalLines(t, dir, "s-replay")
	if len(lines) != 2 || lines[0]["kind"] != "session" || lines[1]["role"] != "user" {
		t.Fatalf("重放后 journal 应为 头行+重落命令行(应答行同 id 被抑制),实际 %d 行: %v", len(lines), lines)
	}
	hist, _, err := readAgentCoreJournalHistoryFrom(dir, "s-replay")
	if err != nil {
		t.Fatalf("readback: %v", err)
	}
	if len(hist) != 1 || hist[0].Role != "user" {
		t.Fatalf("重放后读回仍应 user-first,实际 %+v", hist)
	}
}

// TestAgentCoreClearCommandNoJournal：journal 不可用时降级为仅清内存，
// 不得 panic，且命令仍被接管（照常应答）。
func TestAgentCoreClearCommandNoJournal(t *testing.T) {
	m := newAgentAIManager()
	m.coreJournal = nil
	m.mu.Lock()
	sess := &agentAISession{id: "s-noj", provider: "agentcore", mode: "vibe"}
	sess.history = []agentAIMessage{{Role: "user", MessageID: "u1", Content: "x"}}
	m.sessions["s-noj"] = sess
	m.mu.Unlock()

	if !m.handleLocalSlashCommand(sess, "msg-x", "/clear", "agentcore", func(interface{}) error { return nil }) {
		t.Fatal("/clear 应被本地接管")
	}
	m.mu.Lock()
	empty := len(m.sessions["s-noj"].history) == 0
	m.mu.Unlock()
	if !empty {
		t.Fatal("journal 缺席时内存历史仍应清空")
	}
}

// TestResetSessionJournalHistoryNoop：文件缺失是成功 no-op（磁盘上没有东西
// 可复活），不报错。
func TestResetSessionJournalHistoryNoop(t *testing.T) {
	j := newAgentCoreJournal(t.TempDir())
	if err := j.resetSessionHistory("never-existed"); err != nil {
		t.Fatalf("缺文件应为 no-op 成功,实际 err=%v", err)
	}
}
