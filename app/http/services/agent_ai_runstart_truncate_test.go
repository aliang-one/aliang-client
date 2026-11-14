package services

// 撤回重发 v3 通道（spec §A.2 的 run.start 面）：ai.run.start 消费
// truncate_before_message_id。手机通道的回合入口是 v3 原子帧（不再先发
// ai.session.create），撤回必须搭本帧才能生效。
//
// 隔离铁律：所有用例必须把 m.coreJournal 覆盖到 t.TempDir()（先例
// agent_ai_slash_journal_test.go），绝不触真机 ~/.aliang/ai-sessions/。

import (
	"testing"

	"aliang.one/nursorgate/app/http/models"
)

// runStartJournalSeed 造一个带三回合历史的 journal：u1/a1/u2/a2/u3，
// 供各用例按锚点截断。
func runStartJournalSeed(t *testing.T, j *agentCoreJournal, sid string) {
	t.Helper()
	j.ensureSessionHeader(sid, "/proj", "glm-5.3", "", "2026-11-14T00:00:00Z", "phone", nil, nil, "")
	j.appendUser(sid, "u1", "第一问", "ts")
	j.appendAssistant(sid, "a1", "第一答", "ts")
	j.appendUser(sid, "u2", "第二问", "ts")
	j.appendAssistant(sid, "a2", "第二答", "ts")
	j.appendUser(sid, "u3", "第三问", "ts")
}

func TestRunStartTruncateRebuildsHistory(t *testing.T) {
	m := newAgentAIManager()
	m.coreJournal = newAgentCoreJournal(t.TempDir()) // 隔离:绝不碰真机 journal
	const sid = "s-runstart-trunc"
	runStartJournalSeed(t, m.coreJournal, sid)

	// 存量会话（内存 history 含锚点后的旧行），截断必须连内存一起覆盖。
	m.mu.Lock()
	m.sessions[sid] = &agentAISession{
		id: sid, provider: "agentcore", projectPath: t.TempDir(),
		history: []agentAIMessage{
			{Role: "user", MessageID: "u2", Content: "第二问"},
			{Role: "assistant", MessageID: "a2", Content: "第二答"},
			{Role: "user", MessageID: "u3", Content: "第三问"},
		},
	}
	m.mu.Unlock()

	// runStart 会把回合真正开跑（spawn provider），单测里不可行。截断在
	// message() 之前完成；回合本身会在 runUserMessage 处失败终态（单测环境
	// 无 CLI），不影响对截断效果的断言。content 必须非空——message() 对空
	// content 在 history 覆盖门之前就提前 return，那会测不到内存覆盖。
	_, _, writer := captureAIWriter(t)
	m.runStart(map[string]interface{}{
		"type":                       models.AgentEventAIRunStart,
		"session_id":                 sid,
		"run_id":                     "run-trunc-1",
		"message_id":                 "m1",
		"content":                    "重发的第一问",
		"provider":                   "agentcore",
		"project_path":               t.TempDir(),
		"truncate_before_message_id": "u2",
	}, writer)

	m.mu.Lock()
	sess := m.sessions[sid]
	m.mu.Unlock()
	if sess == nil {
		t.Fatalf("session must exist after runStart")
	}
	// 期望终态：截断后的 journal 历史（u1/a1）+ 本回合新消息（m1）。
	// 锚点后的旧行（u2/a2/u3）必须消失——无论 journal 还是内存 history。
	if len(sess.history) != 3 ||
		sess.history[0].MessageID != "u1" || sess.history[1].MessageID != "a1" ||
		sess.history[2].MessageID != "m1" {
		t.Fatalf("history not rebuilt from truncated journal: %+v", sess.history)
	}
	// journal 本体必须真的截短到锚点前（不含本回合新行——新行在 run 落盘时
	// 才追加，单测环境回合失败即无写点）。
	hist, _, err := readAgentCoreJournalHistoryFrom(m.coreJournal.dir, sid)
	if err != nil {
		t.Fatalf("journal read failed: %v", err)
	}
	if len(hist) != 2 || hist[0].MessageID != "u1" || hist[1].MessageID != "a1" {
		t.Fatalf("journal not truncated to anchor: %+v", hist)
	}
}

func TestRunStartTruncateRejectsWhileRunActive(t *testing.T) {
	m := newAgentAIManager()
	m.coreJournal = newAgentCoreJournal(t.TempDir())
	const sid = "s-runstart-trunc-busy"
	runStartJournalSeed(t, m.coreJournal, sid)
	// cancel != nil 即活跃回合（clearRunning 清位的是同一字段）。
	m.mu.Lock()
	m.sessions[sid] = &agentAISession{id: sid, provider: "agentcore", cancel: func() {}}
	m.mu.Unlock()

	mu, events, writer := captureAIWriter(t)
	m.runStart(map[string]interface{}{
		"type":                       models.AgentEventAIRunStart,
		"session_id":                 sid,
		"run_id":                     "run-trunc-busy",
		"message_id":                 "m1",
		"provider":                   "agentcore",
		"project_path":               t.TempDir(),
		"truncate_before_message_id": "u2",
	}, writer)

	mu.Lock()
	defer mu.Unlock()
	if len(*events) != 1 {
		t.Fatalf("expect exactly one error frame, got %d: %+v", len(*events), *events)
	}
	resp := (*events)[0]
	// 时序铁律:拒绝必须发生在任何 journal 写之前——被拒时 journal 必须原封不动。
	if resp["type"] != models.AgentEventAIError {
		t.Fatalf("expect ai.error, got %+v", resp)
	}
	if resp["error_code"] != "truncate_run_active" {
		t.Fatalf("error_code = %v, want truncate_run_active: %+v", resp["error_code"], resp)
	}
	hist, _, _ := readAgentCoreJournalHistoryFrom(m.coreJournal.dir, sid)
	if len(hist) != 5 {
		t.Fatalf("journal must be untouched on reject: %+v", hist)
	}
}

func TestRunStartTruncateAllowsEmptyHistory(t *testing.T) {
	m := newAgentAIManager()
	m.coreJournal = newAgentCoreJournal(t.TempDir())
	const sid = "s-runstart-trunc-empty"
	m.coreJournal.ensureSessionHeader(sid, "/proj", "glm-5.3", "", "2026-11-14T00:00:00Z", "phone", nil, nil, "")
	m.coreJournal.appendUser(sid, "u1", "第一问", "ts")

	m.mu.Lock()
	m.sessions[sid] = &agentAISession{
		id: sid, provider: "agentcore", projectPath: t.TempDir(),
		history: []agentAIMessage{{Role: "user", MessageID: "u1", Content: "第一问"}},
	}
	m.mu.Unlock()

	_, _, writer := captureAIWriter(t)
	m.runStart(map[string]interface{}{
		"type":                       models.AgentEventAIRunStart,
		"session_id":                 sid,
		"run_id":                     "run-trunc-empty",
		"message_id":                 "m1",
		"content":                    "重发的新问题",
		"provider":                   "agentcore",
		"project_path":               t.TempDir(),
		"truncate_before_message_id": "u1",
	}, writer)

	m.mu.Lock()
	sess := m.sessions[sid]
	m.mu.Unlock()
	// 「撤回到空」期望终态：旧历史（u1）必须消失，只剩本回合新消息。
	if sess == nil || len(sess.history) != 1 || sess.history[0].MessageID != "m1" {
		t.Fatalf("history must drop pre-anchor rows after truncate-to-empty: %+v", sess)
	}
}

func TestRunStartTruncateIdempotentOnRedelivery(t *testing.T) {
	// outbox 重投同 run：replayProcessedRun 命中已处理 run 直接回放终态，
	// 不会二次截断；但即便绕过 replay（如不同 run_id 重发），truncateBefore
	// 按位置幂等——第二次截断结果与第一次一致。本用例锁后者。
	m := newAgentAIManager()
	m.coreJournal = newAgentCoreJournal(t.TempDir())
	const sid = "s-runstart-trunc-idem"
	runStartJournalSeed(t, m.coreJournal, sid)

	applied1, err1 := m.coreJournal.truncateBefore(sid, "u2")
	hist1, _, _ := readAgentCoreJournalHistoryFrom(m.coreJournal.dir, sid)
	_, err2 := m.coreJournal.truncateBefore(sid, "u2")
	hist2, _, _ := readAgentCoreJournalHistoryFrom(m.coreJournal.dir, sid)

	if err1 != nil || err2 != nil {
		t.Fatalf("truncate failed: %v / %v", err1, err2)
	}
	if !applied1 {
		t.Fatalf("first truncate must apply")
	}
	if len(hist1) != 2 || len(hist2) != 2 {
		t.Fatalf("idempotent truncate drifted: %+v / %+v", hist1, hist2)
	}
}

func TestRunStartTruncateIgnoredForNonAgentcore(t *testing.T) {
	// provider != agentcore 时锚点静默忽略（v2 语义）：journal 不截、
	// history 键不注入。
	m := newAgentAIManager()
	m.coreJournal = newAgentCoreJournal(t.TempDir())
	const sid = "s-runstart-trunc-claude"
	runStartJournalSeed(t, m.coreJournal, sid)
	_, _, writer := captureAIWriter(t)

	m.runStart(map[string]interface{}{
		"type":                       models.AgentEventAIRunStart,
		"session_id":                 sid,
		"run_id":                     "run-trunc-claude",
		"message_id":                 "m1",
		"provider":                   "claude_code",
		"project_path":               t.TempDir(),
		"truncate_before_message_id": "u2",
	}, writer)

	hist, _, _ := readAgentCoreJournalHistoryFrom(m.coreJournal.dir, sid)
	if len(hist) != 5 {
		t.Fatalf("journal must be untouched for non-agentcore: %+v", hist)
	}
}
