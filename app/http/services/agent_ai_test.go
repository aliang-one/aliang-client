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
	"testing"

	"aliang.one/nursorgate/app/http/models"
)

// captureCreatePayload 捕获 create() 单帧回显(agentTerminalWriter 参数是
// interface{},先例 captureAIWriter)。
func captureCreatePayload(t *testing.T, got *map[string]interface{}) agentTerminalWriter {
	t.Helper()
	return func(p interface{}) error {
		m, ok := p.(map[string]interface{})
		if !ok {
			t.Fatalf("payload type = %T, want map[string]interface{}", p)
		}
		*got = m
		return nil
	}
}

func TestCreateTruncateRebuildsHistoryFromJournal(t *testing.T) {
	m := newAgentAIManager()
	m.coreJournal = newAgentCoreJournal(t.TempDir()) // 隔离:绝不碰真机 journal
	const sid = "s-create-trunc"
	m.coreJournal.ensureSessionHeader(sid, "/proj", "glm-5.3", "2026-10-31T00:00:00Z", "dashboard")
	m.coreJournal.appendUser(sid, "u1", "第一问", "ts")
	m.coreJournal.appendAssistant(sid, "a1", "第一答", "ts")
	m.coreJournal.appendUser(sid, "u2", "第二问", "ts")

	var created map[string]interface{}
	m.create(map[string]interface{}{
		"session_id":                 sid,
		"provider":                   "agentcore",
		"model":                      "glm-5.3",
		"project_path":               t.TempDir(),
		"truncate_before_message_id": "u2",
	}, captureCreatePayload(t, &created))

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
	m.coreJournal.ensureSessionHeader(sid, "/proj", "glm-5.3", "ts", "dashboard")
	m.coreJournal.appendUser(sid, "u1", "第一问", "ts")
	// cancel != nil 即活跃回合（clearRunning 清位的是同一字段，agent_ai.go:315/:4234）。
	m.mu.Lock()
	m.sessions[sid] = &agentAISession{id: sid, provider: "agentcore", cancel: func() {}}
	m.mu.Unlock()

	var resp map[string]interface{}
	m.create(map[string]interface{}{
		"session_id":                 sid,
		"provider":                   "agentcore",
		"project_path":               t.TempDir(),
		"truncate_before_message_id": "u1",
	}, captureCreatePayload(t, &resp))

	// 时序铁律:拒绝必须发生在任何 journal 写之前——被拒时 journal 必须原封不动。
	if resp["type"] != models.AgentEventAIError {
		t.Fatalf("expect ai.error, got %+v", resp)
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
	m.coreJournal.ensureSessionHeader(sid, "/proj", "glm-5.3", "2026-10-31T00:00:00Z", "dashboard")
	m.coreJournal.appendUser(sid, "u1", "第一问", "ts")

	var created map[string]interface{}
	m.create(map[string]interface{}{
		"session_id":                 sid,
		"provider":                   "agentcore",
		"project_path":               t.TempDir(),
		"truncate_before_message_id": "u1",
	}, captureCreatePayload(t, &created))

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
	m.coreJournal.ensureSessionHeader(sid, "/proj", "glm-5.3", "2026-10-31T00:00:00Z", "dashboard")
	m.coreJournal.appendUser(sid, "u1", "第一问", "ts")
	m.coreJournal.appendUser(sid, "u2", "第二问", "ts")

	var created map[string]interface{}
	m.create(map[string]interface{}{
		"session_id":                 sid,
		"provider":                   "claudecode",
		"project_path":               t.TempDir(),
		"truncate_before_message_id": "u2",
	}, captureCreatePayload(t, &created))

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

func TestCreateTruncateJournalUnavailableRejects(t *testing.T) {
	m := newAgentAIManager()
	m.coreJournal = nil // journal 禁用:撤回锚点不存在,拒绝而非静默空 history
	const sid = "s-create-trunc-nojournal"

	var resp map[string]interface{}
	m.create(map[string]interface{}{
		"session_id":                 sid,
		"provider":                   "agentcore",
		"project_path":               t.TempDir(),
		"truncate_before_message_id": "u1",
	}, captureCreatePayload(t, &resp))

	if resp["type"] != models.AgentEventAIError {
		t.Fatalf("expect ai.error, got %+v", resp)
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

	var resp map[string]interface{}
	m.create(map[string]interface{}{
		"session_id":                 sid,
		"provider":                   "agentcore",
		"project_path":               t.TempDir(),
		"truncate_before_message_id": "u1",
	}, captureCreatePayload(t, &resp))

	// 读回失败绝不静默空 history:拒绝 + 不注册会话(幂等重试即收敛)。
	if resp["type"] != models.AgentEventAIError {
		t.Fatalf("expect ai.error, got %+v", resp)
	}
	m.mu.Lock()
	sess := m.sessions[sid]
	m.mu.Unlock()
	if sess != nil {
		t.Fatalf("IO-failed create must not register session: %+v", sess)
	}
}
