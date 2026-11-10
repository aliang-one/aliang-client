package services

// journal 历史读回测试：解析器单测（原 Task 11 计划交付，前置到订阅快照任务）
// + message() lazy-create 接线端到端（跨端/重启续聊，spec §4.3）。
// 格式契约由真实写侧 fixture + 读回断言守护：写读同仓同发布，键漂移会静默
// 双端一致（与 agent_ai_agentcore_journal_test.go 同一取舍）。

import (
	"fmt"
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
	j.ensureSessionHeader("s-hist", "/proj", "glm-5.3", "", "2026-10-28T00:00:00Z", "phone", nil, nil, "")
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
	j.ensureSessionHeader("s-ap", "/proj", "glm-5.3", "", "2026-10-28T00:00:00Z", "phone", nil, nil, "")
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
	m.coreJournal.ensureSessionHeader("s-restore", "/proj", "glm-5.3", "", now, "phone", nil, nil, "")
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

// TestMessageLazyCreateRestoresSessionSwitches 会话级开关的懒建恢复端到端
// （2026-10-31 验收缺陷③的持久化半边）：重启/驱逐后 run 不带开关键直达
// message() 懒建——必须从 journal 头行恢复「上次显式设置」，而不是静默
// 回滚成全启用。
func TestMessageLazyCreateRestoresSessionSwitches(t *testing.T) {
	setTestAgentHome(t)
	journalDir := t.TempDir()
	m := newAgentAIManager()
	m.coreJournal = newAgentCoreJournal(journalDir)
	now := time.Now().UTC().Format(time.RFC3339)
	m.coreJournal.ensureSessionHeader("s-sw-restore", "/proj", "glm-5.3", "", now, "phone", nil, nil, "")
	m.coreJournal.appendUser("s-sw-restore", "m1", "第一问", now)
	m.coreJournal.appendAssistant("s-sw-restore", "a1", "第一答", now)
	m.coreJournal.syncSessionSwitches("s-sw-restore", []string{"github"}, true, []string{"pdf"}, true)

	up, srv := newAgentCoreUpstream(t, agentCoreTextTurn("第二", "答"))
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
	// 载荷不带 disabled_mcp_servers/disabled_skills（重启后的 run.start 形态）
	svc.DispatchLocalAI(map[string]interface{}{
		"type":         models.AgentEventAIMessage,
		"session_id":   "s-sw-restore",
		"message_id":   "m2",
		"content":      "第二问",
		"provider":     "agentcore",
		"mode":         "vibe",
		"project_path": t.TempDir(),
		"model":        "glm-5.3",
	}, write)

	waitForAgentEvent(t, &mu, &frames, models.AgentEventAIDone, func(e map[string]interface{}) bool {
		return remoteString(e, "session_id") == "s-sw-restore"
	})

	m.mu.Lock()
	sess := m.sessions["s-sw-restore"]
	mcp := append([]string(nil), sess.disabledMCPServers...)
	skills := append([]string(nil), sess.disabledSkills...)
	m.mu.Unlock()
	if len(mcp) != 1 || mcp[0] != "github" {
		t.Fatalf("懒建应从头行恢复 disabledMCPServers=[github], got %v", mcp)
	}
	if len(skills) != 1 || skills[0] != "pdf" {
		t.Fatalf("懒建应从头行恢复 disabledSkills=[pdf], got %v", skills)
	}
	_ = up
}

// TestMessageLazyCreateSwitchesPayloadOverridesHead 载荷显式带键时以载荷为
// 权威（与 headModel 的「载荷权威」纪律同款）：头行 [github]、载荷 []（显式
// 全部启用）→ 会话落 []。
func TestMessageLazyCreateSwitchesPayloadOverridesHead(t *testing.T) {
	setTestAgentHome(t)
	journalDir := t.TempDir()
	m := newAgentAIManager()
	m.coreJournal = newAgentCoreJournal(journalDir)
	now := time.Now().UTC().Format(time.RFC3339)
	m.coreJournal.ensureSessionHeader("s-sw-payload", "/proj", "glm-5.3", "", now, "phone", nil, nil, "")
	m.coreJournal.appendUser("s-sw-payload", "m1", "第一问", now)
	m.coreJournal.appendAssistant("s-sw-payload", "a1", "第一答", now)
	m.coreJournal.syncSessionSwitches("s-sw-payload", []string{"github"}, true, nil, true)

	_, srv := newAgentCoreUpstream(t, agentCoreTextTurn("第二", "答"))
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
		"type":                 models.AgentEventAIMessage,
		"session_id":           "s-sw-payload",
		"message_id":           "m2",
		"content":              "第二问",
		"provider":             "agentcore",
		"mode":                 "vibe",
		"project_path":         t.TempDir(),
		"model":                "glm-5.3",
		"disabled_mcp_servers": []interface{}{},
	}, write)

	waitForAgentEvent(t, &mu, &frames, models.AgentEventAIDone, func(e map[string]interface{}) bool {
		return remoteString(e, "session_id") == "s-sw-payload"
	})

	m.mu.Lock()
	mcp := append([]string(nil), m.sessions["s-sw-payload"].disabledMCPServers...)
	m.mu.Unlock()
	// 断言的是优先级：载荷显式键整体替换，头行的 [github] 不得幸存。
	// （append(nil,...) 快照会把空切片变 nil，故只断长度不断 nil-ness——
	// 下游 nil 与 [] 行为等价。）
	if len(mcp) != 0 {
		t.Fatalf("载荷显式空数组应整体替换(不得保留头行 [github]), got %v", mcp)
	}
}

// TestMessageLazyCreateWithTranscriptStillHydratesSwitches 手机主路径的开关
// 水合 + 头行不可自毁（2026-10-31 评审 confirmed-0/2 的回归锚）：手机 v3 的
// ai.run.start 恒带 transcript、载荷不带开关键、服务端也不认识这两个键——
// ①懒建必须无视「载荷有历史」门、从 journal 头行恢复开关；
// ②run 进门绝不写头行（持久化写点只在显式改键处），头行名单必须原样幸存。
func TestMessageLazyCreateWithTranscriptStillHydratesSwitches(t *testing.T) {
	setTestAgentHome(t)
	journalDir := t.TempDir()
	m := newAgentAIManager()
	m.coreJournal = newAgentCoreJournal(journalDir)
	now := time.Now().UTC().Format(time.RFC3339)
	m.coreJournal.ensureSessionHeader("s-sw-tx", "/proj", "glm-5.3", "", now, "phone", nil, nil, "")
	m.coreJournal.appendUser("s-sw-tx", "m1", "第一问", now)
	m.coreJournal.appendAssistant("s-sw-tx", "a1", "第一答", now)
	m.coreJournal.syncSessionSwitches("s-sw-tx", []string{"github"}, true, []string{"pdf"}, true)

	up, srv := newAgentCoreUpstream(t, agentCoreTextTurn("第二", "答"))
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
		"type":       models.AgentEventAIMessage,
		"session_id": "s-sw-tx",
		"message_id": "m2",
		"content":    "第二问",
		"provider":   "agentcore",
		"mode":       "vibe",
		"model":      "glm-5.3",
		// 手机 v3 形态：transcript 恒带（历史以载荷为准），开关键缺席
		"transcript": []interface{}{
			map[string]interface{}{"role": "user", "id": "t1", "content": "第一问"},
			map[string]interface{}{"role": "assistant", "id": "t1a", "content": "第一答"},
		},
	}, write)

	waitForAgentEvent(t, &mu, &frames, models.AgentEventAIDone, func(e map[string]interface{}) bool {
		return remoteString(e, "session_id") == "s-sw-tx"
	})

	m.mu.Lock()
	mcp := append([]string(nil), m.sessions["s-sw-tx"].disabledMCPServers...)
	skills := append([]string(nil), m.sessions["s-sw-tx"].disabledSkills...)
	m.mu.Unlock()
	if len(mcp) != 1 || mcp[0] != "github" {
		t.Fatalf("transcript 载荷也必须从头行水合 disabledMCPServers=[github], got %v", mcp)
	}
	if len(skills) != 1 || skills[0] != "pdf" {
		t.Fatalf("transcript 载荷也必须从头行水合 disabledSkills=[pdf], got %v", skills)
	}

	// 头行不可被 run 自毁：跑完一轮后名单必须原样幸存
	_, head, err := readAgentCoreJournalHistoryFrom(journalDir, "s-sw-tx")
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	if len(head.disabledMCPServers) != 1 || head.disabledMCPServers[0] != "github" {
		t.Fatalf("头行名单被 run 抹掉了: %v", head.disabledMCPServers)
	}
	_ = up
}

// TestCreateBackfillsSiblingFromHeader 单键 create 的兄弟侧兜底（2026-10-31
// 二轮评审 F3）：冷会话 + create 只带 disabled_mcp_servers——skills 侧必须
// 从 journal 头行兜底恢复，而不是按 nil 注册后在整段进程生命周期里失效。
func TestCreateBackfillsSiblingFromHeader(t *testing.T) {
	setTestAgentHome(t)
	journalDir := t.TempDir()
	m := newAgentAIManager()
	m.coreJournal = newAgentCoreJournal(journalDir)
	now := time.Now().UTC().Format(time.RFC3339)
	m.coreJournal.ensureSessionHeader("s-f3", "/proj", "glm-5.3", "", now, "phone", nil, []string{"pdf"}, "")

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
		"type":                 models.AgentEventAISessionCreate,
		"session_id":           "s-f3",
		"provider":             "agentcore",
		"mode":                 "vibe",
		"project_path":         t.TempDir(),
		"model":                "glm-5.3",
		"disabled_mcp_servers": []interface{}{"a"},
		// 故意不带 disabled_skills —— 兄弟侧应从头行兜底
	}, write)

	m.mu.Lock()
	mcp := append([]string(nil), m.sessions["s-f3"].disabledMCPServers...)
	skills := append([]string(nil), m.sessions["s-f3"].disabledSkills...)
	m.mu.Unlock()
	if len(mcp) != 1 || mcp[0] != "a" {
		t.Fatalf("带键侧应以载荷为权威, got %v", mcp)
	}
	if len(skills) != 1 || skills[0] != "pdf" {
		t.Fatalf("单键 create 的兄弟侧应从头行兜底 [pdf], got %v", skills)
	}
}

// TestCreateRestoresJournalHistoryWithoutTranscript 重启失忆修复端到端
// （2026-11-05 G2）：仪表盘每条消息先发 ai.session.create（不带 transcript），
// agent 进程重启后 create 重建的内存会话历史为空——下回合模型只看到最新一条。
// create 无 transcript 且 journal 存在时必须回装历史，且与 message() 懒建
// 分支同一读侧（过滤/顺序/条数上限完全一致）。全链走 DispatchLocalAI：
// ①create 落内存历史（user/assistant/turn 三行，工具行不入）；②紧随的
// ai.message 把「回装历史 + 本问」作为独立消息发到内核上游——内核收到的
// messages 前文即回装证据；③若 message 再二次回装，前文会重复出现，
// 消息条数断言同时守住幂等。
func TestCreateRestoresJournalHistoryWithoutTranscript(t *testing.T) {
	setTestAgentHome(t)
	journalDir := t.TempDir()
	m := newAgentAIManager()
	m.coreJournal = newAgentCoreJournal(journalDir)
	now := time.Now().UTC().Format(time.RFC3339)
	m.coreJournal.ensureSessionHeader("s-g2", "/proj", "glm-5.3", "", now, "dashboard", nil, nil, "")
	m.coreJournal.appendUser("s-g2", "m1", "第一问", now)
	m.coreJournal.appendAssistant("s-g2", "a1", "第一答", now)
	m.coreJournal.appendTool("s-g2", "Bash", "ls", false) // 工具行:轨迹不是消息
	m.coreJournal.appendAssistantTurn("s-g2", "q-1", 1, "第一回合答复", "thinking 不进历史", "end_turn", nil)

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
	// 仪表盘形态：create 不带 transcript（历史只能来自 journal）。
	svc.DispatchLocalAI(map[string]interface{}{
		"type":         models.AgentEventAISessionCreate,
		"session_id":   "s-g2",
		"provider":     "agentcore",
		"mode":         "vibe",
		"project_path": t.TempDir(),
		"model":        "glm-5.3",
	}, write)

	// ①create 即回装：内存历史 = journal 的 user/assistant/turn 三行，顺序正确。
	m.mu.Lock()
	history := append([]agentAIMessage(nil), m.sessions["s-g2"].history...)
	m.mu.Unlock()
	if len(history) != 3 {
		t.Fatalf("create 后内存历史条数 = %d, want 3: %+v", len(history), history)
	}
	if history[0].Role != "user" || history[0].Content != "第一问" {
		t.Fatalf("create 后 history[0] = %+v, want user/第一问", history[0])
	}
	if history[1].Role != "assistant" || history[1].Content != "第一答" {
		t.Fatalf("create 后 history[1] = %+v, want assistant/第一答", history[1])
	}
	if history[2].Role != "assistant" || history[2].Content != "第一回合答复" {
		t.Fatalf("create 后 history[2] = %+v, want assistant/第一回合答复(turn 行)", history[2])
	}

	// ②仪表盘每条消息先 create 再 message 的序列：session 已在内存，走既有
	// 分支（不得二次回装）。
	svc.DispatchLocalAI(map[string]interface{}{
		"type":         models.AgentEventAIMessage,
		"session_id":   "s-g2",
		"message_id":   "m2",
		"content":      "第二问",
		"provider":     "agentcore",
		"mode":         "vibe",
		"project_path": t.TempDir(),
		"model":        "glm-5.3",
	}, write)
	waitForAgentEvent(t, &mu, &frames, models.AgentEventAIDone, func(e map[string]interface{}) bool {
		return remoteString(e, "session_id") == "s-g2"
	})

	// ③内核收到的 messages：回装历史（3 行）在前、本问排最后；重复回装会让
	// 前文翻倍，条数即幂等证据。
	reqs := up.requests()
	if len(reqs) != 1 {
		t.Fatalf("上游请求次数 = %d, want 1", len(reqs))
	}
	want := []struct{ role, text string }{
		{"user", "第一问"}, {"assistant", "第一答"},
		{"assistant", "第一回合答复"}, {"user", "第二问"},
	}
	if len(reqs[0].Messages) != len(want) {
		t.Fatalf("内核收到的消息条数 = %d, want %d: %+v", len(reqs[0].Messages), len(want), reqs[0].Messages)
	}
	for i, w := range want {
		msg := reqs[0].Messages[i]
		text := agentCoreMessageText(msg)
		if msg.Role != w.role || text != w.text {
			t.Errorf("内核 messages[%d] = %s/%q, want %s/%q", i, msg.Role, text, w.role, w.text)
		}
	}
}

// TestCreateWithTranscriptKeepsTranscriptOverJournal 核对项④：载荷带 transcript
// 的 create 仍以 transcript 为准（既有语义），journal 历史不得混入。
func TestCreateWithTranscriptKeepsTranscriptOverJournal(t *testing.T) {
	setTestAgentHome(t)
	journalDir := t.TempDir()
	m := newAgentAIManager()
	m.coreJournal = newAgentCoreJournal(journalDir)
	now := time.Now().UTC().Format(time.RFC3339)
	m.coreJournal.ensureSessionHeader("s-g2-tx", "/proj", "glm-5.3", "", now, "dashboard", nil, nil, "")
	m.coreJournal.appendUser("s-g2-tx", "m1", "journal旧问", now)
	m.coreJournal.appendAssistant("s-g2-tx", "a1", "journal旧答", now)

	up, srv := newAgentCoreUpstream(t, agentCoreTextTurn("第三", "答"))
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
		"type":         models.AgentEventAISessionCreate,
		"session_id":   "s-g2-tx",
		"provider":     "agentcore",
		"mode":         "vibe",
		"project_path": t.TempDir(),
		"model":        "glm-5.3",
		"transcript": []interface{}{
			map[string]interface{}{"role": "user", "id": "t1", "content": "载荷问"},
			map[string]interface{}{"role": "assistant", "id": "t1a", "content": "载荷答"},
		},
	}, write)

	svc.DispatchLocalAI(map[string]interface{}{
		"type":         models.AgentEventAIMessage,
		"session_id":   "s-g2-tx",
		"message_id":   "m2",
		"content":      "第三问",
		"provider":     "agentcore",
		"mode":         "vibe",
		"project_path": t.TempDir(),
		"model":        "glm-5.3",
	}, write)
	waitForAgentEvent(t, &mu, &frames, models.AgentEventAIDone, func(e map[string]interface{}) bool {
		return remoteString(e, "session_id") == "s-g2-tx"
	})

	reqs := up.requests()
	if len(reqs) != 1 {
		t.Fatalf("上游请求次数 = %d, want 1", len(reqs))
	}
	want := []struct{ role, text string }{
		{"user", "载荷问"}, {"assistant", "载荷答"}, {"user", "第三问"},
	}
	if len(reqs[0].Messages) != len(want) {
		t.Fatalf("内核收到的消息条数 = %d, want %d（journal 历史不得混入）: %+v",
			len(reqs[0].Messages), len(want), reqs[0].Messages)
	}
	for i, w := range want {
		msg := reqs[0].Messages[i]
		text := agentCoreMessageText(msg)
		if msg.Role != w.role || text != w.text {
			t.Errorf("内核 messages[%d] = %s/%q, want %s/%q", i, msg.Role, text, w.role, w.text)
		}
	}
	_ = up
}

// TestCreateRestoreKeepsInitialContext 回装不得冲掉 initial_context：create 的
// initial_context 拼接点在解析期（早于 message() 懒建的拼接点），无 transcript
// 回装时 system 条目必须保留在历史首部（内核经 System 字段承载）。
func TestCreateRestoreKeepsInitialContext(t *testing.T) {
	setTestAgentHome(t)
	journalDir := t.TempDir()
	m := newAgentAIManager()
	m.coreJournal = newAgentCoreJournal(journalDir)
	now := time.Now().UTC().Format(time.RFC3339)
	m.coreJournal.ensureSessionHeader("s-g2-ic", "/proj", "glm-5.3", "", now, "dashboard", nil, nil, "")
	m.coreJournal.appendUser("s-g2-ic", "m1", "journal旧问", now)
	m.coreJournal.appendAssistant("s-g2-ic", "a1", "journal旧答", now)

	svc := &AgentService{ai: m}
	write := func(p interface{}) error { return nil }
	svc.DispatchLocalAI(map[string]interface{}{
		"type":            models.AgentEventAISessionCreate,
		"session_id":      "s-g2-ic",
		"provider":        "agentcore",
		"mode":            "vibe",
		"project_path":    t.TempDir(),
		"model":           "glm-5.3",
		"initial_context": "这是初始上下文",
	}, write)

	m.mu.Lock()
	history := append([]agentAIMessage(nil), m.sessions["s-g2-ic"].history...)
	m.mu.Unlock()
	if len(history) != 3 {
		t.Fatalf("create 后内存历史条数 = %d, want 3(system+journal 两行): %+v", len(history), history)
	}
	if history[0].Role != "system" || history[0].Content != "这是初始上下文" {
		t.Fatalf("回装冲掉了 initial_context, history[0] = %+v", history[0])
	}
	if history[1].Content != "journal旧问" || history[2].Content != "journal旧答" {
		t.Fatalf("journal 回装行错: %+v", history[1:])
	}
}

// 2026-11-05 仪表盘刷新丢回答事故回归锚：agentcore 路径的回答走
// appendAssistantTurn(kind=turn, 无 message_id)——快照读侧此前只认
// kind=msg,转录里永远只有 user 行,重进页订阅快照/内存续聊恢复全部拿不到
// 回答。turn 行必须与 msg 行同权进历史。
func TestReadAgentCoreJournalHistoryIncludesTurnRows(t *testing.T) {
	dir := t.TempDir()
	j := newAgentCoreJournal(dir)
	j.ensureSessionHeader("s-turn", "/proj", "glm-5.3", "", "2026-11-05T00:00:00Z", "dashboard", nil, nil, "")
	j.appendUser("s-turn", "m1", "请只回复四个字：测试通过", "2026-11-05T00:01:00Z")
	j.appendAssistantTurn("s-turn", "q-turn", 1, "测试通过", "The user is asking me to reply with four characters.", "end_turn", nil)

	hist, _, err := readAgentCoreJournalHistoryFrom(dir, "s-turn")
	if err != nil {
		t.Fatalf("读回失败: %v", err)
	}
	if len(hist) != 2 {
		t.Fatalf("应回 2 条消息(user+turn),实际 %d: %+v", len(hist), hist)
	}
	if hist[0].Role != "user" || hist[0].Content != "请只回复四个字：测试通过" {
		t.Fatalf("user 行错: %+v", hist[0])
	}
	if hist[1].Role != "assistant" || hist[1].Content != "测试通过" {
		t.Fatalf("turn 行错(回答必须进历史): %+v", hist[1])
	}
	// turn 行的确定性 message_id 必须透传（2026-11-10 聊天记录身份化）：
	// assistant_<user_message_id>#<turn>。前端按它做回合分段与跨端合并键——
	// 旧口径「turn 行无 message_id」正是多答连体/分页锚点失效的共同根因。
	if hist[1].MessageID != "assistant_q-turn#1" {
		t.Fatalf("turn 行 message_id 应为确定性 id, got %q", hist[1].MessageID)
	}
	// thinking 不进历史(快照契约未消费)。
	if strings.Contains(hist[1].Content, "The user is asking") {
		t.Fatalf("thinking 不得混入正文: %+v", hist[1])
	}
}

// writeJournalRawLines 往指定 journal 目录手写原始 JSONL 行（绕过写侧 helper
// 才能造带 diagnostics 块的 end 行——appendEndDiagnostics 的 agentCoreDiagnostics
// 字段不导出，测试造不出带 reason 键的终态快照）。
func writeJournalRawLines(t *testing.T, dir, sessionID string, lines ...string) {
	t.Helper()
	path := filepath.Join(dir, agentCoreJournalFileName(sessionID))
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("写 journal fixture: %v", err)
	}
}

// TestReadAgentCoreJournalTranscriptEndErrorNote（G5 问题悬空）：快照订阅读侧
// 必须把 journal end(error) 行折成 role=system 的系统条——失败回合在转录里
// 悬空（有提问无任何反馈）是仪表盘用户「为什么没有回答」的直接来源。
// 核对项：①diagnostics.reason（内核真因）优先于 error 键；②笼统兜底文案
// 「agentcore run failed」不得进正文；③成功/取消（success/stopped）终态不产条；
// ④模型上下文回装口径（readAgentCoreJournalHistoryFrom 薄包装）保持纯对话
// 历史——系统条绝不能进模型 prompt。
func TestReadAgentCoreJournalTranscriptEndErrorNote(t *testing.T) {
	dir := t.TempDir()
	head := `{"kind":"session","session_id":"%s","project_path":"/proj","model":"glm-5.3","created_at":"2026-11-05T00:00:00Z","origin":"phone"}`
	user := `{"kind":"msg","role":"user","message_id":"m1","text":"第一问","ts":"2026-11-05T00:01:00Z"}`
	// 真因形态：宿主已把笼统文案升级成内核真因时，error 与 diagnostics.reason
	// 同文；reason 键是权威源。
	writeJournalRawLines(t, dir, "s-err-reason",
		fmt.Sprintf(head, "s-err-reason"), user,
		`{"kind":"end","status":"error","error":"模型连续 3 轮发起完全相同的工具调用，判定为无进展，已停止","diagnostics":{"turns":3,"reason":"模型连续 3 轮发起完全相同的工具调用，判定为无进展"},"ts":"2026-11-05T00:02:00Z"}`)
	// 笼统形态：真因丢失，只有占位文案。
	writeJournalRawLines(t, dir, "s-err-generic",
		fmt.Sprintf(head, "s-err-generic"), user,
		`{"kind":"end","status":"error","error":"agentcore run failed","ts":"2026-11-05T00:02:00Z"}`)
	// 纯宿主错误形态：无 diagnostics 块（上游探测失败等早期出口）。
	writeJournalRawLines(t, dir, "s-err-host",
		fmt.Sprintf(head, "s-err-host"), user,
		`{"kind":"end","status":"error","error":"平台 AI 代理未运行(127.0.0.1:56432 连接被拒,已尝试自动拉起)——请在设备仪表盘启动代理后重试","ts":"2026-11-05T00:02:00Z"}`)
	writeJournalRawLines(t, dir, "s-success",
		fmt.Sprintf(head, "s-success"), user,
		`{"kind":"end","status":"success","ts":"2026-11-05T00:02:00Z"}`)
	writeJournalRawLines(t, dir, "s-stopped",
		fmt.Sprintf(head, "s-stopped"), user,
		`{"kind":"end","status":"stopped","ts":"2026-11-05T00:02:00Z"}`)

	hist, _, err := readAgentCoreJournalTranscriptFrom(dir, "s-err-reason", true)
	if err != nil {
		t.Fatalf("读回失败: %v", err)
	}
	if len(hist) != 2 || hist[1].Role != "system" {
		t.Fatalf("end(error) 必须折出系统条: %+v", hist)
	}
	if hist[1].Content != "这一轮执行失败：模型连续 3 轮发起完全相同的工具调用，判定为无进展" {
		t.Fatalf("系统条文案错(reason 键优先): %q", hist[1].Content)
	}
	if hist[1].CreatedAt.IsZero() {
		t.Fatalf("系统条应带 end 行 ts: %+v", hist[1])
	}

	hist, _, err = readAgentCoreJournalTranscriptFrom(dir, "s-err-generic", true)
	if err != nil {
		t.Fatalf("读回失败: %v", err)
	}
	if len(hist) != 2 || hist[1].Content != "这一轮执行失败" {
		t.Fatalf("笼统兜底文案不得进正文: %+v", hist)
	}

	hist, _, err = readAgentCoreJournalTranscriptFrom(dir, "s-err-host", true)
	if err != nil {
		t.Fatalf("读回失败: %v", err)
	}
	if len(hist) != 2 || hist[1].Content != "这一轮执行失败：平台 AI 代理未运行(127.0.0.1:56432 连接被拒,已尝试自动拉起)——请在设备仪表盘启动代理后重试" {
		t.Fatalf("宿主错误原文必须保留兜底: %+v", hist)
	}

	for _, sessionID := range []string{"s-success", "s-stopped"} {
		hist, _, err = readAgentCoreJournalTranscriptFrom(dir, sessionID, true)
		if err != nil {
			t.Fatalf("读回失败: %v", err)
		}
		if len(hist) != 1 || hist[0].Role != "user" {
			t.Fatalf("%s: 成功/取消终态不得产系统条: %+v", sessionID, hist)
		}
	}

	// 模型上下文回装口径（false）：同文件系统条不出现——历史要进模型 prompt。
	hist, _, err = readAgentCoreJournalHistoryFrom(dir, "s-err-reason")
	if err != nil {
		t.Fatalf("读回失败: %v", err)
	}
	if len(hist) != 1 || hist[0].Role != "user" {
		t.Fatalf("回装口径不得带系统条(会污染模型 prompt): %+v", hist)
	}
}
