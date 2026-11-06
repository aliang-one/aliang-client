package services

// 撤回重发（spec §A.2）内核侧：ai.session.create 消费 truncate_before_message_id。
// 前端流程=running 会话先 ai.stop 等终态，再 create 帧带截断锚点（不带
// transcript），宿主截断 journal 后从截断后 journal 全文重建 history。
//
// 隔离铁律：所有用例必须把 m.coreJournal 覆盖到 t.TempDir()（先例
// agent_ai_slash_journal_test.go），绝不触真机 ~/.aliang/ai-sessions/。

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"aliang.one/nursorgate/app/http/models"
)

// soleCreateFrame 断言 create() 恰好回显一帧并返回它。事件捕获复用
// captureAIWriter 的互斥锁事件列表惯例（先例 agent_ai_approval_proto_test.go:12），
// 与其余 AI 用例共享同一套捕获工具，而不是另起单帧捕获器。
func soleCreateFrame(t *testing.T, mu *sync.Mutex, events *[]map[string]interface{}) map[string]interface{} {
	t.Helper()
	mu.Lock()
	defer mu.Unlock()
	if len(*events) != 1 {
		t.Fatalf("create must emit exactly one frame, got %d: %+v", len(*events), *events)
	}
	return (*events)[0]
}

func TestCreateTruncateRebuildsHistoryFromJournal(t *testing.T) {
	m := newAgentAIManager()
	m.coreJournal = newAgentCoreJournal(t.TempDir()) // 隔离:绝不碰真机 journal
	const sid = "s-create-trunc"
	m.coreJournal.ensureSessionHeader(sid, "/proj", "glm-5.3", "", "2026-10-31T00:00:00Z", "dashboard", nil, nil, "")
	m.coreJournal.appendUser(sid, "u1", "第一问", "ts")
	m.coreJournal.appendAssistant(sid, "a1", "第一答", "ts")
	m.coreJournal.appendUser(sid, "u2", "第二问", "ts")

	mu, events, writer := captureAIWriter(t)
	m.create(map[string]interface{}{
		"session_id":                 sid,
		"provider":                   "agentcore",
		"model":                      "glm-5.3",
		"project_path":               t.TempDir(),
		"truncate_before_message_id": "u2",
	}, writer)
	created := soleCreateFrame(t, mu, events)

	if created["type"] != models.AgentEventAISessionCreated {
		t.Fatalf("expect ai.session.created, got %+v", created)
	}
	if created["truncate_applied"] != true {
		t.Fatalf("truncate_applied echo missing: %+v", created)
	}
	m.mu.Lock()
	sess := m.sessions[sid]
	m.mu.Unlock()
	if sess == nil || len(sess.history) != 2 ||
		sess.history[0].MessageID != "u1" || sess.history[1].Content != "第一答" {
		t.Fatalf("history not rebuilt from truncated journal: %+v", sess)
	}
}

func TestCreateTruncateRejectsWhileRunActive(t *testing.T) {
	m := newAgentAIManager()
	m.coreJournal = newAgentCoreJournal(t.TempDir())
	const sid = "s-create-trunc-busy"
	m.coreJournal.ensureSessionHeader(sid, "/proj", "glm-5.3", "", "ts", "dashboard", nil, nil, "")
	m.coreJournal.appendUser(sid, "u1", "第一问", "ts")
	// cancel != nil 即活跃回合（clearRunning 清位的是同一字段，agent_ai.go:315/:4281）。
	m.mu.Lock()
	m.sessions[sid] = &agentAISession{id: sid, provider: "agentcore", cancel: func() {}}
	m.mu.Unlock()

	mu, events, writer := captureAIWriter(t)
	m.create(map[string]interface{}{
		"session_id":                 sid,
		"provider":                   "agentcore",
		"project_path":               t.TempDir(),
		"truncate_before_message_id": "u1",
	}, writer)
	resp := soleCreateFrame(t, mu, events)

	// 时序铁律:拒绝必须发生在任何 journal 写之前——被拒时 journal 必须原封不动。
	if resp["type"] != models.AgentEventAIError {
		t.Fatalf("expect ai.error, got %+v", resp)
	}
	if resp["error_code"] != "truncate_run_active" {
		t.Fatalf("error_code = %v, want truncate_run_active: %+v", resp["error_code"], resp)
	}
	hist, _, _ := readAgentCoreJournalHistoryFrom(m.coreJournal.dir, sid)
	if len(hist) != 1 {
		t.Fatalf("journal must be untouched on reject: %+v", hist)
	}
}

func TestCreateTruncateAllowsEmptyHistory(t *testing.T) {
	m := newAgentAIManager()
	m.coreJournal = newAgentCoreJournal(t.TempDir())
	const sid = "s-create-trunc-empty"
	m.coreJournal.ensureSessionHeader(sid, "/proj", "glm-5.3", "", "2026-10-31T00:00:00Z", "dashboard", nil, nil, "")
	m.coreJournal.appendUser(sid, "u1", "第一问", "ts")

	mu, events, writer := captureAIWriter(t)
	m.create(map[string]interface{}{
		"session_id":                 sid,
		"provider":                   "agentcore",
		"project_path":               t.TempDir(),
		"truncate_before_message_id": "u1",
	}, writer)
	created := soleCreateFrame(t, mu, events)

	if created["type"] != models.AgentEventAISessionCreated {
		t.Fatalf("expect ai.session.created, got %+v", created)
	}
	// 「撤回到空」允许空覆盖:截断生效即回显 true,history 重建为空。
	if created["truncate_applied"] != true {
		t.Fatalf("truncate_applied should be true on truncate-to-empty: %+v", created)
	}
	m.mu.Lock()
	sess := m.sessions[sid]
	m.mu.Unlock()
	if sess == nil || len(sess.history) != 0 {
		t.Fatalf("history must be empty after truncate-to-empty: %+v", sess)
	}
}

func TestCreateTruncateIgnoredForNonAgentcore(t *testing.T) {
	m := newAgentAIManager()
	m.coreJournal = newAgentCoreJournal(t.TempDir())
	const sid = "s-create-trunc-claude"
	m.coreJournal.ensureSessionHeader(sid, "/proj", "glm-5.3", "", "2026-10-31T00:00:00Z", "dashboard", nil, nil, "")
	m.coreJournal.appendUser(sid, "u1", "第一问", "ts")
	m.coreJournal.appendUser(sid, "u2", "第二问", "ts")

	mu, events, writer := captureAIWriter(t)
	m.create(map[string]interface{}{
		"session_id":                 sid,
		"provider":                   "claudecode",
		"project_path":               t.TempDir(),
		"truncate_before_message_id": "u2",
	}, writer)
	created := soleCreateFrame(t, mu, events)

	if created["type"] != models.AgentEventAISessionCreated {
		t.Fatalf("expect ai.session.created, got %+v", created)
	}
	// 非 agentcore:截断字段被忽略但必须显式回显 false(前端据此走分支)。
	if v, ok := created["truncate_applied"].(bool); !ok || v {
		t.Fatalf("non-agentcore must echo truncate_applied=false: %+v", created)
	}
	// journal 原封不动。
	hist, _, _ := readAgentCoreJournalHistoryFrom(m.coreJournal.dir, sid)
	if len(hist) != 2 {
		t.Fatalf("journal must be untouched for non-agentcore: %+v", hist)
	}
}

func TestCreateTruncateEmptyOverrideOnExistingSession(t *testing.T) {
	// 既有会话分支的空覆盖守护（d468b55 新增 if 分支的回归）:重建结果为空也
	// 必须覆盖既有非空 history——旧逻辑 len(history)>0 才覆盖,空结果会被
	// 陈旧 history 顶回,撤回到空就永远不生效。
	m := newAgentAIManager()
	m.coreJournal = newAgentCoreJournal(t.TempDir())
	const sid = "s-create-trunc-exist-empty"
	m.coreJournal.ensureSessionHeader(sid, "/proj", "glm-5.3", "", "ts", "dashboard", nil, nil, "")
	m.coreJournal.appendUser(sid, "u1", "第一问", "ts")
	// 既有会话（无 cancel:无活跃 run,前置检查须放行）带着非空 history。
	m.mu.Lock()
	m.sessions[sid] = &agentAISession{id: sid, provider: "agentcore", history: []agentAIMessage{
		{Role: "user", MessageID: "u1", Content: "第一问", CreatedAt: time.Now().UTC()},
	}}
	m.mu.Unlock()

	mu, events, writer := captureAIWriter(t)
	m.create(map[string]interface{}{
		"session_id":                 sid,
		"provider":                   "agentcore",
		"project_path":               t.TempDir(),
		"truncate_before_message_id": "u1",
	}, writer)
	created := soleCreateFrame(t, mu, events)

	if created["type"] != models.AgentEventAISessionCreated {
		t.Fatalf("expect ai.session.created, got %+v", created)
	}
	m.mu.Lock()
	sess := m.sessions[sid]
	m.mu.Unlock()
	if sess == nil || len(sess.history) != 0 {
		t.Fatalf("existing session history must be overridden to empty (撤回到空): %+v", sess)
	}
}

func TestCreateTruncateMiddleOverrideOnExistingSession(t *testing.T) {
	// 既有会话分支的区间替换守护:截断锚为中间消息时,既有 history 必须整体
	// 替换为保留区间（u3 之前的 u1/a1/u2/a2）,而不是被陈旧 history 顶回。
	m := newAgentAIManager()
	m.coreJournal = newAgentCoreJournal(t.TempDir())
	const sid = "s-create-trunc-exist-mid"
	m.coreJournal.ensureSessionHeader(sid, "/proj", "glm-5.3", "", "ts", "dashboard", nil, nil, "")
	m.coreJournal.appendUser(sid, "u1", "第一问", "ts")
	m.coreJournal.appendAssistant(sid, "a1", "第一答", "ts")
	m.coreJournal.appendUser(sid, "u2", "第二问", "ts")
	m.coreJournal.appendAssistant(sid, "a2", "第二答", "ts")
	m.coreJournal.appendUser(sid, "u3", "第三问", "ts")
	// 既有会话带着完整 5 条 history,撤回锚=u3 ⇒ 保留 u1/a1/u2/a2 共 4 条。
	m.mu.Lock()
	m.sessions[sid] = &agentAISession{id: sid, provider: "agentcore", history: []agentAIMessage{
		{Role: "user", MessageID: "u1", Content: "第一问", CreatedAt: time.Now().UTC()},
		{Role: "assistant", MessageID: "a1", Content: "第一答", CreatedAt: time.Now().UTC()},
		{Role: "user", MessageID: "u2", Content: "第二问", CreatedAt: time.Now().UTC()},
		{Role: "assistant", MessageID: "a2", Content: "第二答", CreatedAt: time.Now().UTC()},
		{Role: "user", MessageID: "u3", Content: "第三问", CreatedAt: time.Now().UTC()},
	}}
	m.mu.Unlock()

	mu, events, writer := captureAIWriter(t)
	m.create(map[string]interface{}{
		"session_id":                 sid,
		"provider":                   "agentcore",
		"project_path":               t.TempDir(),
		"truncate_before_message_id": "u3",
	}, writer)
	created := soleCreateFrame(t, mu, events)

	if created["type"] != models.AgentEventAISessionCreated {
		t.Fatalf("expect ai.session.created, got %+v", created)
	}
	m.mu.Lock()
	sess := m.sessions[sid]
	m.mu.Unlock()
	if sess == nil || len(sess.history) != 4 ||
		sess.history[0].MessageID != "u1" || sess.history[3].MessageID != "a2" {
		t.Fatalf("existing history must be replaced with retained interval: %+v", sess)
	}
}

func TestCreateTruncateJournalUnavailableRejects(t *testing.T) {
	m := newAgentAIManager()
	m.coreJournal = nil // journal 禁用:撤回锚点不存在,拒绝而非静默空 history
	const sid = "s-create-trunc-nojournal"

	mu, events, writer := captureAIWriter(t)
	m.create(map[string]interface{}{
		"session_id":                 sid,
		"provider":                   "agentcore",
		"project_path":               t.TempDir(),
		"truncate_before_message_id": "u1",
	}, writer)
	resp := soleCreateFrame(t, mu, events)

	if resp["type"] != models.AgentEventAIError {
		t.Fatalf("expect ai.error, got %+v", resp)
	}
	if resp["error_code"] != "truncate_journal_unavailable" {
		t.Fatalf("error_code = %v, want truncate_journal_unavailable: %+v", resp["error_code"], resp)
	}
	m.mu.Lock()
	sess := m.sessions[sid]
	m.mu.Unlock()
	if sess != nil {
		t.Fatalf("rejected create must not register session: %+v", sess)
	}
}

func TestCreateTruncateIOErrorRejects(t *testing.T) {
	// 文件冒充目录(先例 agent_ai_agentcore_run_test.go:865):journal 读写全失败。
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := newAgentAIManager()
	m.coreJournal = newAgentCoreJournal(blocker)
	const sid = "s-create-trunc-io"

	mu, events, writer := captureAIWriter(t)
	m.create(map[string]interface{}{
		"session_id":                 sid,
		"provider":                   "agentcore",
		"project_path":               t.TempDir(),
		"truncate_before_message_id": "u1",
	}, writer)
	resp := soleCreateFrame(t, mu, events)

	// 读回失败绝不静默空 history:拒绝 + 不注册会话(幂等重试即收敛)。
	if resp["type"] != models.AgentEventAIError {
		t.Fatalf("expect ai.error, got %+v", resp)
	}
	if resp["error_code"] != "truncate_failed" {
		t.Fatalf("error_code = %v, want truncate_failed: %+v", resp["error_code"], resp)
	}
	m.mu.Lock()
	sess := m.sessions[sid]
	m.mu.Unlock()
	if sess != nil {
		t.Fatalf("IO-failed create must not register session: %+v", sess)
	}
}

func TestCreateTruncateRebuildFailureRejects(t *testing.T) {
	// 第四类 code:截断 no-op（目标 journal 从未落盘,truncateBefore 对缺失
	// 文件幂等成功）后重建读失败——同样拒绝,绝不静默回退空 history。
	m := newAgentAIManager()
	m.coreJournal = newAgentCoreJournal(t.TempDir())
	const sid = "s-create-trunc-rebuild"

	mu, events, writer := captureAIWriter(t)
	m.create(map[string]interface{}{
		"session_id":                 sid,
		"provider":                   "agentcore",
		"project_path":               t.TempDir(),
		"truncate_before_message_id": "u1",
	}, writer)
	resp := soleCreateFrame(t, mu, events)

	if resp["type"] != models.AgentEventAIError {
		t.Fatalf("expect ai.error, got %+v", resp)
	}
	if resp["error_code"] != "truncate_rebuild_failed" {
		t.Fatalf("error_code = %v, want truncate_rebuild_failed: %+v", resp["error_code"], resp)
	}
	m.mu.Lock()
	sess := m.sessions[sid]
	m.mu.Unlock()
	if sess != nil {
		t.Fatalf("rebuild-failed create must not register session: %+v", sess)
	}
}
