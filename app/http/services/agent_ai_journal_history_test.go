package services

// journal 历史读回测试：解析器单测（原 Task 11 计划交付，前置到订阅快照任务）
// + message() lazy-create 接线端到端（跨端/重启续聊，spec §4.3）。
// 格式契约由真实写侧 fixture + 读回断言守护：写读同仓同发布，键漂移会静默
// 双端一致（与 agent_ai_agentcore_journal_test.go 同一取舍）。

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"aliang.one/nursorgate/app/agentcore"
	"aliang.one/nursorgate/app/http/models"
)

// agentCoreMessageText 取内核消息的纯文本：agentCoreUpstream 记录的是 JSON
// 解码后的请求体，Content 因此是字符串或 []interface{}（text 块数组——
// applyCacheBreakpoints 会把最后一条消息升级成块挂断点），两种形态都摊平。
func agentCoreMessageText(msg agentcore.Message) string {
	switch content := msg.Content.(type) {
	case string:
		return content
	case []interface{}:
		var b strings.Builder
		for _, item := range content {
			if block, ok := item.(map[string]interface{}); ok {
				text, _ := block["text"].(string)
				b.WriteString(text)
			}
		}
		return b.String()
	default:
		return ""
	}
}

func TestReadAgentCoreJournalHistory(t *testing.T) {
	dir := t.TempDir()
	j := newAgentCoreJournal(dir)
	j.ensureSessionHeader("s-hist", "/proj", "glm-5.3", "", "2026-10-28T00:00:00Z", "phone")
	j.appendUser("s-hist", "m1", "第一问", "2026-10-28T00:01:00Z")
	j.appendAssistant("s-hist", "a1", "第一答", "2026-10-28T00:02:00Z")
	j.appendTool("s-hist", "Bash", "ls", false) // 工具行：轨迹不是消息，解析器必须跳过
	j.appendUser("s-hist", "m2", "第二问", "2026-10-28T00:03:00Z")
	j.appendEnd("s-hist", "success", "")
	// 残行容忍：上一进程可能死在写一半（写侧只在新写入时补换行封口，残行
	// 本身留在文件里）。模拟手工追加一个无换行的半行 JSON。
	f, err := os.OpenFile(filepath.Join(dir, agentCoreJournalFileName("s-hist")), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("打开 journal: %v", err)
	}
	if _, err := f.WriteString(`{"kind":"msg","role":"user"`); err != nil {
		t.Fatalf("写残行: %v", err)
	}
	f.Close()

	hist, head, err := readAgentCoreJournalHistoryFrom(dir, "s-hist")
	if err != nil {
		t.Fatalf("读回失败: %v", err)
	}
	if len(hist) != 3 {
		t.Fatalf("应回 3 条消息(user/a/user),实际 %d: %+v", len(hist), hist)
	}
	if hist[0].Role != "user" || hist[0].Content != "第一问" {
		t.Fatalf("首条错: %+v", hist[0])
	}
	if hist[0].MessageID != "m1" || hist[0].CreatedAt.IsZero() {
		t.Fatalf("首条 message_id/ts 解析错: %+v", hist[0])
	}
	if hist[1].Role != "assistant" || hist[1].Content != "第一答" {
		t.Fatalf("第二条错: %+v", hist[1])
	}
	if hist[2].Content != "第二问" {
		t.Fatalf("第三条错: %+v", hist[2])
	}
	if head.projectPath != "/proj" || head.model != "glm-5.3" {
		t.Fatalf("头信息错: %+v", head)
	}
}

func TestReadAgentCoreJournalHistoryMissing(t *testing.T) {
	// 便捷包装扫默认 journal 根：不存在的会话必须报错（调用方回退
	// lazy-create / 快照降级）。只读探测，会话名 "no-such" 不可能与真机
	// ~/.aliang/ai-sessions 里的真实会话撞名。
	if _, _, err := readAgentCoreJournalHistory("no-such"); err == nil {
		t.Fatal("不存在应报错(调用方回退 lazy-create)")
	}
}

func TestReadAgentCoreJournalHistoryFromMissing(t *testing.T) {
	// From 变体（供快照按 manager 自己的 journal 目录读）：目录为空或文件
	// 缺失一律报错，绝不静默回空历史——空历史与「读不到」语义不同。
	if _, _, err := readAgentCoreJournalHistoryFrom("", "s-x"); err == nil {
		t.Fatal("空目录应报错")
	}
	if _, _, err := readAgentCoreJournalHistoryFrom(t.TempDir(), "s-x"); err == nil {
		t.Fatal("文件缺失应报错")
	}
}

// TestReadAgentCoreJournalHistoryIgnoresApprovalRows（Task 14）：kind=approval
// 审计行必须被历史读回忽略——审计是轨迹不是消息，混进回放会把「审批被怎么
// 处理了」当成对话内容端到另一端。
func TestReadAgentCoreJournalHistoryIgnoresApprovalRows(t *testing.T) {
	dir := t.TempDir()
	j := newAgentCoreJournal(dir)
	j.ensureSessionHeader("s-ap", "/proj", "glm-5.3", "", "2026-10-28T00:00:00Z", "phone")
	j.appendUser("s-ap", "m1", "第一问", "2026-10-28T00:01:00Z")
	j.appendApprovalAudit("s-ap", "ap-1", 1, "decline")
	j.appendAssistant("s-ap", "a1", "第一答", "2026-10-28T00:02:00Z")

	hist, _, err := readAgentCoreJournalHistoryFrom(dir, "s-ap")
	if err != nil {
		t.Fatalf("读回失败: %v", err)
	}
	if len(hist) != 2 {
		t.Fatalf("history 行数 = %d, want 2（approval 行不得混入）: %+v", len(hist), hist)
	}
	if hist[0].Content != "第一问" || hist[1].Content != "第一答" {
		t.Fatalf("history 内容 = %+v", hist)
	}
}

// TestMessageLazyCreateRestoresJournalHistory 跨端/重启续聊端到端（spec §4.3）：
// agentcore 会话不在内存（进程重启/内存驱逐），journal 里有完整历史——ai.message
// 的 lazy-create 必须把历史装回内存再跑。全链走 DispatchLocalAI，续聊的最终
// 可见形态在 fake 上游的请求体里：装回的历史必须原样作为独立 user/assistant
// 消息发到上游、本问排最后，且头行模型在载荷 model 为空时兜底。
func TestMessageLazyCreateRestoresJournalHistory(t *testing.T) {
	setTestAgentHome(t)
	journalDir := t.TempDir()
	m := newAgentAIManager()
	m.coreJournal = newAgentCoreJournal(journalDir)
	now := time.Now().UTC().Format(time.RFC3339)
	m.coreJournal.ensureSessionHeader("s-restore", "/proj", "glm-5.3", "", now, "phone")
	m.coreJournal.appendUser("s-restore", "m1", "第一问", now)
	m.coreJournal.appendAssistant("s-restore", "a1", "第一答", now)

	up, srv := newAgentCoreUpstream(t, agentCoreTextTurn("第二", "答"))
	// agentCoreBaseURL 只认回环；httptest 正是 127.0.0.1:PORT——守卫探测必过，
	// 绝不触发自愈 POST（真实本机管理端口不能被测试触碰，2026-10-27 事故口径）。
	t.Setenv("ANTHROPIC_BASE_URL", srv.URL)

	svc := &AgentService{ai: m}
	var mu sync.Mutex
	var frames []map[string]interface{}
	write := func(p interface{}) error {
		if row, ok := p.(map[string]interface{}); ok {
			mu.Lock()
			frames = append(frames, row)
			mu.Unlock()
		}
		return nil
	}
	// 不注册内存会话——模拟重启/驱逐后的续聊。载荷不带货载历史（服务端重启
	// 后本来就没有），历史只能来自 journal。
	svc.DispatchLocalAI(map[string]interface{}{
		"type":         models.AgentEventAIMessage,
		"session_id":   "s-restore",
		"message_id":   "m2",
		"content":      "第二问",
		"provider":     "agentcore",
		"mode":         "vibe",
		"project_path": t.TempDir(),
		"model":        "", // 显式载荷为空 → 头行模型兜底
	}, write)

	// 等回合收尾再断言：run 在独立 goroutine 里，提前返回会与 t.Setenv 清理
	// 竞争（race 检测下必炸）。
	waitForAgentEvent(t, &mu, &frames, models.AgentEventAIDone, func(e map[string]interface{}) bool {
		return remoteString(e, "session_id") == "s-restore"
	})

	m.mu.Lock()
	history := append([]agentAIMessage(nil), m.sessions["s-restore"].history...)
	m.mu.Unlock()
	// 装回两行 + 本回合 user + 本回合 assistant = 4。
	if len(history) != 4 {
		t.Fatalf("历史条数 = %d, want 4: %+v", len(history), history)
	}
	if history[0].Role != "user" || history[0].Content != "第一问" {
		t.Fatalf("history[0] = %+v, want user/第一问", history[0])
	}
	if history[1].Role != "assistant" || history[1].Content != "第一答" {
		t.Fatalf("history[1] = %+v, want assistant/第一答", history[1])
	}
	if history[2].Role != "user" || history[2].Content != "第二问" {
		t.Fatalf("history[2] = %+v, want 本回合 user/第二问", history[2])
	}

	// 上游请求体：装回历史 + 本问，model 用头行兜底值。
	reqs := up.requests()
	if len(reqs) != 1 {
		t.Fatalf("上游请求次数 = %d, want 1", len(reqs))
	}
	if reqs[0].Model != "glm-5.3" {
		t.Errorf("上游 model = %q, want glm-5.3（载荷 model 为空时头行兜底）", reqs[0].Model)
	}
	want := []struct{ role, text string }{
		{"user", "第一问"}, {"assistant", "第一答"}, {"user", "第二问"},
	}
	if len(reqs[0].Messages) != len(want) {
		t.Fatalf("上游消息条数 = %d, want %d: %+v", len(reqs[0].Messages), len(want), reqs[0].Messages)
	}
	for i, w := range want {
		msg := reqs[0].Messages[i]
		text := agentCoreMessageText(msg)
		if msg.Role != w.role || text != w.text {
			t.Errorf("上游 messages[%d] = %s/%q, want %s/%q", i, msg.Role, text, w.role, w.text)
		}
	}

	// 装回成功不发 history_restore_failed。
	for _, f := range filterEvents(&mu, &frames, models.AgentEventAIRunNotice) {
		if remoteString(f, "code") == "history_restore_failed" {
			t.Fatalf("装回成功不应发 notice: %+v", f)
		}
	}
}

// TestMessageLazyCreateRestoreFailureNotice 读回 IO 失败（非 ErrNotExist）必须
// 发 ai.run.notice{code:history_restore_failed}，且会话照常空起——历史损坏
// 不能把整轮 run 顶死。读失败的构造：journal 目录指到一个普通文件，
// ReadFile(<file>/<session>.jsonl) 得 ENOTDIR（实测 ≠ErrNotExist，见
// readAgentCoreJournalHistoryFrom 的分支约定）。
func TestMessageLazyCreateRestoreFailureNotice(t *testing.T) {
	setTestAgentHome(t)
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocker, []byte("regular file, not a directory"), 0o600); err != nil {
		t.Fatalf("构造普通文件: %v", err)
	}
	m := newAgentAIManager()
	m.coreJournal = newAgentCoreJournal(blocker)

	// 同上：回环 fake 上游保探测必过、自愈 POST 不可达。载荷 model 为空，
	// 回合在内核模型检查处以 ai.error 收场——正好证明「空起照常进行」。
	_, srv := newAgentCoreUpstream(t)
	t.Setenv("ANTHROPIC_BASE_URL", srv.URL)

	svc := &AgentService{ai: m}
	var mu sync.Mutex
	var frames []map[string]interface{}
	write := func(p interface{}) error {
		if row, ok := p.(map[string]interface{}); ok {
			mu.Lock()
			frames = append(frames, row)
			mu.Unlock()
		}
		return nil
	}
	svc.DispatchLocalAI(map[string]interface{}{
		"type":         models.AgentEventAIMessage,
		"session_id":   "s-broken",
		"message_id":   "m1",
		"content":      "看看还在吗",
		"provider":     "agentcore",
		"mode":         "vibe",
		"project_path": t.TempDir(),
		"model":        "",
	}, write)

	waitForAgentEvent(t, &mu, &frames, models.AgentEventAIRunNotice, func(e map[string]interface{}) bool {
		return remoteString(e, "session_id") == "s-broken" &&
			remoteString(e, "code") == "history_restore_failed" &&
			remoteString(e, "message_id") == "m1" &&
			strings.TrimSpace(remoteString(e, "detail")) != ""
	})

	// 必须等终态帧再返回：ai.error 在 runAgentCore 的上游守卫（agentCoreEnsureUpstream）
	// 之后发出——等到它，才能保证 run goroutine 已在测试 env（ANTHROPIC_BASE_URL=
	// httptest）下完成上游解析；否则测试先返回、t.Setenv 撤销 env，goroutine 里
	// 晚到的解析会落回真实 127.0.0.1:56432，dial 失败后 heal() POST 真实管理
	// 端口拉起本机 AI 代理（10-13/10-27 事故族）。
	waitForAgentEvent(t, &mu, &frames, models.AgentEventAIError, func(e map[string]interface{}) bool {
		return remoteString(e, "session_id") == "s-broken"
	})

	// 会话照常空起：注册成功，历史只有本条新 user 消息（run goroutine 里同步
	// 追加，轮询等它落地；快照锁内取，直接复用）。
	var hist []agentAIMessage
	waitUntilFor(t, 3*time.Second, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		s := m.sessions["s-broken"]
		if s == nil {
			return false
		}
		hist = append(hist[:0], s.history...)
		return len(hist) == 1
	})
	if len(hist) != 1 || hist[0].Role != "user" || hist[0].Content != "看看还在吗" {
		t.Fatalf("restore 失败后未照常空起（未注册或历史异常）: %+v", hist)
	}
	// 测试收尾后 run goroutine 的 journal defer（appendEnd）可能撞上
	// t.TempDir 清理，日志里的「agentcore-journal: mkdir failed ...
	// invalid argument」告警属预期、行为无害，不作断言。
}
