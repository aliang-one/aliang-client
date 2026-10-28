package services

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"aliang.one/nursorgate/app/http/models"
)

func newTestAISocketsManager(t *testing.T) *agentAIManager {
	t.Helper()
	return newAgentAIManager()
}

func TestAISocketRegisterUnsubscribeLifecycle(t *testing.T) {
	m := newTestAISocketsManager(t)
	var mu sync.Mutex
	written := 0
	sock := m.registerAISocket("local", func(interface{}) error {
		mu.Lock()
		written++
		mu.Unlock()
		return nil
	})
	if sock == nil || sock.id == "" {
		t.Fatal("socket 未注册")
	}
	m.subscribeAISocket(sock.id, "s1")
	sock.enqueue(map[string]interface{}{"type": "probe"})
	waitUntilFor(t, time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return written == 1
	})
	m.unregisterAISocket(sock.id)
	if got := len(m.aiSockets); got != 0 {
		t.Fatalf("注销后残留 socket: %d", got)
	}
}

func TestAISocketSlowConsumerDropped(t *testing.T) {
	m := newTestAISocketsManager(t)
	block := make(chan struct{})
	entered := make(chan struct{})
	var enteredOnce sync.Once
	sock := m.registerAISocket("local", func(interface{}) error {
		// pump goroutine 内：只同步，不做断言（禁止 t.Fatal）。once 防御：
		// 测试收尾 close(block) 后 pump 可能再排空缓冲残留（select 对
		// closed/out 两就绪分支随机取舍），write 会被再次进入。
		enteredOnce.Do(func() { close(entered) })
		<-block // 故意不消费
		return nil
	})
	defer close(block)
	// 先投 1 条让 pump 取走并卡死在 write 里：此后 pump 不可能再消费任何
	// 事件，再灌 cap+1 条必然恰好溢出 1 条。（若直接灌 cap+1，pump 可能
	// 抢走其中 1 条使全部投递成功，closed 永不关闭——race 下必现。）
	sock.enqueue(map[string]interface{}{"type": "probe"})
	<-entered
	// 灌满队列 +1 溢出
	for i := 0; i < agentAISocketQueueCap+1; i++ {
		sock.enqueue(map[string]interface{}{"type": "flood", "i": i})
	}
	waitUntilFor(t, time.Second, func() bool {
		select {
		case <-sock.closed:
			return true
		default:
			return false
		}
	})
	m.mu.Lock()
	_, alive := m.aiSockets[sock.id]
	m.mu.Unlock()
	if alive {
		t.Fatal("溢出 socket 应从注册表摘除")
	}
}

func TestAISocketSubscribeUnknownSessionOK(t *testing.T) {
	m := newTestAISocketsManager(t)
	sock := m.registerAISocket("local", func(interface{}) error { return nil })
	m.subscribeAISocket(sock.id, "no-such-session") // 不存在会话允许订阅（列表刷新场景）
	m.unsubscribeAISocket(sock.id, "no-such-session")
	m.unregisterAISocket(sock.id)
}

// TestAISocketSubscribeCapClearsOldestSet 订阅数上限（评审 M5）：第 cap+1 个
// 订阅生效，之前的整组被清（与 completedApprovals 封顶同风格）；无错误、无
// panic——「socket 突然收不到事件」的排查入口是 subscribeAISocket 的 Warn。
func TestAISocketSubscribeCapClearsOldestSet(t *testing.T) {
	m := newTestAISocketsManager(t)
	sock := m.registerAISocket("local", func(interface{}) error { return nil })
	defer m.unregisterAISocket(sock.id)
	for i := 0; i < agentAISocketSubCap+1; i++ {
		m.subscribeAISocket(sock.id, fmt.Sprintf("s-%d", i))
	}
	if sock.subscribed("s-0") {
		t.Fatal("超限全清后首个订阅应消失")
	}
	if !sock.subscribed(fmt.Sprintf("s-%d", agentAISocketSubCap)) {
		t.Fatal("第 cap+1 个订阅应生效")
	}
}

// TestRemoteSocketReRegistration 远端重注册契约：同键换新、旧 socket 关闭、
// 注册表恒只有一条 remote（回归 2026-10-28 AB-BA 死锁：驱逐曾持 m.mu 进
// closeOnce.Do，与慢消费者摘除路径互锁）。
func TestRemoteSocketReRegistration(t *testing.T) {
	m := newTestAISocketsManager(t)
	block := make(chan struct{})
	defer close(block)
	write := func(interface{}) error {
		<-block
		return nil
	}
	sock1 := m.registerAISocket("remote", write)
	if sock1 == nil || sock1.id != agentAIRemoteSocketID {
		t.Fatalf("首次注册远端 id = %v, 期望 %q", sock1, agentAIRemoteSocketID)
	}
	sock2 := m.registerAISocket("remote", write)
	if sock2 == nil || sock2.id != agentAIRemoteSocketID {
		t.Fatalf("重注册远端 id = %v, 期望 %q", sock2, agentAIRemoteSocketID)
	}
	if sock2 == sock1 {
		t.Fatal("重注册应产生新 socket 对象")
	}
	defer m.unregisterAISocket(agentAIRemoteSocketID) // 收尾：让新 pump 退出
	// 旧 socket 的 closed 必须触发（解锁后驱逐关闭）。
	waitUntilFor(t, time.Second, func() bool {
		select {
		case <-sock1.closed:
			return true
		default:
			return false
		}
	})
	// 新 socket 活跃且独占注册表唯一 remote 条目。
	m.mu.Lock()
	live := m.aiSockets[agentAIRemoteSocketID]
	remoteCount := 0
	for _, s := range m.aiSockets {
		if s.role == "remote" {
			remoteCount++
		}
	}
	m.mu.Unlock()
	if live != sock2 {
		t.Fatal("注册表 remote 键应指向新 socket")
	}
	if !sock2.alive() {
		t.Fatal("新 socket 应活跃")
	}
	if remoteCount != 1 {
		t.Fatalf("注册表 remote 条目数 = %d, 期望 1", remoteCount)
	}
}

// TestRunEventFanOutToSubscriber run 事件扇出：origin 内联写出 + 订阅者收到
// 副本；未订阅 socket 不收 run 流事件。originWrite 与 origin socket 的写函数
// 是同一闭包（生产等价：HandleAIStream 注册与 origin 内联走同一 writeJSON）。
func TestRunEventFanOutToSubscriber(t *testing.T) {
	m := newTestAISocketsManager(t)
	originWritten := make(chan struct{}, 8)
	subWritten := make(chan map[string]interface{}, 8)
	bystanderWritten := make(chan map[string]interface{}, 8)
	originWrite := func(interface{}) error {
		originWritten <- struct{}{}
		return nil
	}
	origin := m.registerAISocket("local", originWrite)
	sub := m.registerAISocket("local", func(p interface{}) error {
		subWritten <- p.(map[string]interface{})
		return nil
	})
	m.registerAISocket("local", func(p interface{}) error {
		bystanderWritten <- p.(map[string]interface{})
		return nil
	})
	m.subscribeAISocket(sub.id, "s1")

	write := m.fanOutWrite(origin.id, "s1", originWrite)
	_ = write(map[string]interface{}{"type": "ai.delta", "delta": "x"})

	<-originWritten // origin 已内联写出
	select {
	case got := <-subWritten:
		if got["type"] != "ai.delta" {
			t.Fatalf("订阅者收到错误事件: %v", got["type"])
		}
	case <-time.After(time.Second):
		t.Fatal("订阅者未收到 run 事件")
	}
	// 未订阅 socket 不收 run 流事件（非审批族只按订阅扇出）。
	time.Sleep(50 * time.Millisecond)
	select {
	case got := <-bystanderWritten:
		t.Fatalf("未订阅 socket 不应收到 run 事件,收到 %v", got["type"])
	default:
	}
}

// TestApprovalBroadcastReachesNonSubscriber 审批族事件广播到全部活跃 socket
// （不看订阅，spec §4.1 双端审核语义）；origin（此处=未注册的常驻 remote id）
// 被跳过不影响他人收到。
func TestApprovalBroadcastReachesNonSubscriber(t *testing.T) {
	m := newTestAISocketsManager(t)
	got := make(chan map[string]interface{}, 8)
	m.registerAISocket("local", func(p interface{}) error {
		got <- p.(map[string]interface{})
		return nil
	})
	write := m.fanOutWrite(agentAIRemoteSocketID, "s1", func(interface{}) error { return nil })
	_ = write(map[string]interface{}{"type": models.AgentEventAIApprovalRequest, "session_id": "s1"})
	select {
	case p := <-got:
		if p["type"] != models.AgentEventAIApprovalRequest {
			t.Fatalf("期望审批广播,收到 %v", p["type"])
		}
	case <-time.After(time.Second):
		t.Fatal("未订阅 socket 未收到审批广播")
	}
}

// TestOriginSocketNotDuplicated origin 去重：包装内层的 originWrite 恰好执行
// 1 次（内联）；fan-out 跳过 origin socket，sock 自己的写函数不再被调——总数
// 必须是 1，出现 2 即去重失效。socket 必须订阅该会话（评审 I1）：非审批族
// 事件只按订阅扇出，不订阅时 fan-out 本来就无目标，origin 传空也照样绿
// （守卫形同虚设）。
func TestOriginSocketNotDuplicated(t *testing.T) {
	m := newTestAISocketsManager(t)
	count := make(chan struct{}, 32)
	sock := m.registerAISocket("local", func(interface{}) error { count <- struct{}{}; return nil })
	m.subscribeAISocket(sock.id, "s1")
	write := m.fanOutWrite(sock.id, "s1", func(interface{}) error { count <- struct{}{}; return nil })
	_ = write(map[string]interface{}{"type": "ai.delta"})
	waitUntilFor(t, time.Second, func() bool { return len(count) == 1 })
	time.Sleep(50 * time.Millisecond)
	if n := len(count); n != 1 {
		t.Fatalf("origin 事件应恰好写一次,实际 %d", n)
	}
}

// TestOriginSocketStampedOnSessionViaCreate create 路径落标：入站帧盖
// __origin_socket 后，DispatchLocalAI 走 create()，session.originSocket 应等于
// 发起 socket 的注册表 id（Task 5 标记链的第一环）。
//
// provider 用 "auto" 而非 "agentcore"（评审 M3）：create() 在注册前就校验
// provider，非法值直接报错返回、会话根本不落标——非法 provider 在这里只会
// 造出空转用例；create() 今日不 spawn，选 auto 是纵深防御：即使未来 create
// 引入 spawn，auto 也不会显式命中 agentcore（agentAIUseAgentCore 只认显式值，
// auto 唯一的 agentcore 通路是 CLI 全缺席时的 fallback 改写）。不可拨本机
// 56432 是硬约束。
func TestOriginSocketStampedOnSessionViaCreate(t *testing.T) {
	m := newTestAISocketsManager(t)
	svc := &AgentService{ai: m}
	sock := m.registerAISocket("local", func(interface{}) error { return nil })
	defer m.unregisterAISocket(sock.id)
	svc.DispatchLocalAI(map[string]interface{}{
		"type":                   models.AgentEventAISessionCreate,
		"session_id":             "origin-stamp-c1",
		"provider":               "auto",
		"mode":                   "vibe",
		"project_path":           t.TempDir(),
		agentAIOriginSocketField: sock.id,
	}, func(interface{}) error { return nil })
	m.mu.Lock()
	s := m.sessions["origin-stamp-c1"]
	var got string
	if s != nil {
		got = s.originSocket
	}
	m.mu.Unlock()
	if s == nil {
		t.Fatal("create 未注册会话")
	}
	if got != sock.id {
		t.Fatalf("session.originSocket = %q, 期望 %q", got, sock.id)
	}
}

// TestOriginSocketStampedOnSessionViaMessage message 路径落标：会话已存在时，
// 第二临界区（lastActiveAt 同点）应把入站标记落到 session.originSocket。
// provider 用非法值让回合在落标点之后立刻以 ai.error 收场——不真正 spawn
// 任何 provider（agentcore 会拨本机 56432，测试环境绝不能碰）。
func TestOriginSocketStampedOnSessionViaMessage(t *testing.T) {
	m := newTestAISocketsManager(t)
	svc := &AgentService{ai: m}
	sock := m.registerAISocket("local", func(interface{}) error { return nil })
	defer m.unregisterAISocket(sock.id)
	written := make(chan map[string]interface{}, 8)
	dispense := func(p interface{}) error {
		if row, ok := p.(map[string]interface{}); ok {
			written <- row
		}
		return nil
	}
	svc.DispatchLocalAI(map[string]interface{}{
		"type":       models.AgentEventAISessionCreate,
		"session_id": "origin-stamp-m1",
		// "auto" 而非 "agentcore"：同 TestOriginSocketStampedOnSessionViaCreate
		// 的理由（M3），create 铺底也不给未来 spawn 留 56432 通路。
		"provider":               "auto",
		"mode":                   "vibe",
		"project_path":           t.TempDir(),
		agentAIOriginSocketField: sock.id,
	}, dispense)
	svc.DispatchLocalAI(map[string]interface{}{
		"type":                   models.AgentEventAIMessage,
		"session_id":             "origin-stamp-m1",
		"message_id":             "m1",
		"content":                "hi",
		"provider":               "no-such-provider",
		agentAIOriginSocketField: sock.id,
	}, dispense)
	m.mu.Lock()
	s := m.sessions["origin-stamp-m1"]
	var got string
	if s != nil {
		got = s.originSocket
	}
	m.mu.Unlock()
	if got != sock.id {
		t.Fatalf("session.originSocket = %q, 期望 %q", got, sock.id)
	}
	// 回合应被非法 provider 拦下（ai.error），证明失败发生在落标之后、spawn 之前。
	// 注意排空写法：len(chan) 随接收收缩，`i < len(ch)` 的计数循环只会读一半。
	sawError := false
drain:
	for {
		select {
		case row := <-written:
			if row["type"] == models.AgentEventAIError {
				sawError = true
				break drain
			}
		default:
			break drain
		}
	}
	if !sawError {
		t.Fatal("message 未以 ai.error 收场（非法 provider 应在落标后被拒）")
	}
}

// TestRunEmitterCarriesRunOriginSocket runEmitter 把 run.originSocketID 接进
// fan-out 去重：Task 4 阶段恒空（无去重），Task 5 填充后 origin socket 不得
// 再收到副本。socket 必须订阅该会话（评审 I1）：ai.delta 非审批族、只按订阅
// 扇出——不订阅时丢失 origin 的退化路径（originSocketID=""）同样全绿，断言
// 抓不到回归；订阅后丢 origin 必然 count==2 而失败。
func TestRunEmitterCarriesRunOriginSocket(t *testing.T) {
	m := newTestAISocketsManager(t)
	count := make(chan struct{}, 16)
	sock := m.registerAISocket("local", func(interface{}) error { count <- struct{}{}; return nil })
	defer m.unregisterAISocket(sock.id)
	m.subscribeAISocket(sock.id, "s1")
	emitter := m.runEmitter(agentAIRun{sessionID: "s1", originSocketID: sock.id},
		func(interface{}) error { count <- struct{}{}; return nil })
	_ = emitter.emit(map[string]interface{}{"type": "ai.delta", "delta": "x"})
	waitUntilFor(t, time.Second, func() bool { return len(count) == 1 })
	time.Sleep(50 * time.Millisecond)
	if n := len(count); n != 1 {
		t.Fatalf("origin 事件应恰好写一次(内联),实际 %d", n)
	}
}

// TestDispatchLocalAISubscribeRepliesSnapshot 订阅握手（spec §4.1）：本机聊天
// 连接经 DispatchLocalAI 订阅任意会话，必须在同连接回一个 ai.session.snapshot
// ——未知会话也回（status=idle，列表刷新场景友好）。socket 透传是裸泵，此
// 端点只存在于 agent 进程，订阅落地全靠 DispatchLocalAI 这条 case。
func TestDispatchLocalAISubscribeRepliesSnapshot(t *testing.T) {
	m := newTestAISocketsManager(t)
	svc := &AgentService{ai: m}
	sock := m.registerAISocket("local", func(interface{}) error { return nil })
	defer m.unregisterAISocket(sock.id)
	replies := make(chan map[string]interface{}, 8)
	svc.DispatchLocalAI(map[string]interface{}{
		"type":                   models.AgentEventAISessionSubscribe,
		"session_id":             "any-session",
		agentAIOriginSocketField: sock.id,
	}, func(p interface{}) error {
		replies <- p.(map[string]interface{})
		return nil
	})
	select {
	case p := <-replies:
		if p["type"] != models.AgentEventAISessionSnapshot {
			t.Fatalf("期望快照回包,收到 %v", p["type"])
		}
	case <-time.After(time.Second):
		t.Fatal("订阅未回快照")
	}
}

// TestDispatchLocalAISubscribeCarriesTranscriptAndPending 快照内容：转录来自
// journal 读侧解析（manager 自己的 coreJournal.dir，不读真机 ~/.aliang）、
// 待审批来自会话订阅表、状态按 cancel 非空=running。订阅帧不带 origin 标记
// 也要能回快照（订阅≠发起，socketID 为空合法）。
func TestDispatchLocalAISubscribeCarriesTranscriptAndPending(t *testing.T) {
	m := newTestAISocketsManager(t)
	svc := &AgentService{ai: m}
	// 准备一个有 journal 历史与会话的会话。
	dir := t.TempDir()
	m.coreJournal = newAgentCoreJournal(dir)
	m.coreJournal.ensureSessionHeader("s-snap", "/proj", "glm-5.3", time.Now().UTC().Format(time.RFC3339))
	m.coreJournal.appendUser("s-snap", "m1", "第一问", time.Now().UTC().Format(time.RFC3339))
	m.coreJournal.appendAssistant("s-snap", "a1", "第一答", time.Now().UTC().Format(time.RFC3339))
	m.mu.Lock()
	m.sessions["s-snap"] = &agentAISession{id: "s-snap", cancel: func() {}} // cancel 非空=running
	m.approvals[agentAIApprovalMapKey("s-snap", "ap-1")] = &agentAIApprovalWaiter{
		sessionID: "s-snap",
		runSeq:    1,
		request:   agentAIApprovalRequest{ID: "ap-1", Kind: "tool", Provider: "agentcore"},
	}
	m.mu.Unlock()

	replies := make(chan map[string]interface{}, 8)
	svc.DispatchLocalAI(map[string]interface{}{
		"type":       models.AgentEventAISessionSubscribe,
		"session_id": "s-snap",
	}, func(p interface{}) error { replies <- p.(map[string]interface{}); return nil })
	select {
	case p := <-replies:
		if p["status"] != "running" {
			t.Fatalf("快照状态应 running,实际 %v", p["status"])
		}
		tr, _ := p["transcript"].([]map[string]interface{})
		if len(tr) != 2 {
			t.Fatalf("快照转录应 2 条,实际 %d: %v", len(tr), p["transcript"])
		}
		pend, _ := p["pending_approvals"].([]map[string]interface{})
		if len(pend) != 1 || pend[0]["approval_id"] != "ap-1" {
			t.Fatalf("快照待审批应 1 项 ap-1,实际 %v", p["pending_approvals"])
		}
	case <-time.After(2 * time.Second):
		t.Fatal("未收到快照")
	}
}

// TestDispatchLocalAISnapshotTranscriptBounded 快照转录传输层限幅（评审 I1）：
// 单条文本按清单口径截断（agentVibeTranscriptMaxContentRunes，与
// agent_inventory_agentcore.go 同一 helper），总条数只保最近
// agentAISnapshotTranscriptCap 条。解析器 readAgentCoreJournalHistoryFrom
// 必须仍返回全文——Task 11 内存续聊恢复靠它，截断只发生在快照传输层。
func TestDispatchLocalAISnapshotTranscriptBounded(t *testing.T) {
	m := newTestAISocketsManager(t)
	svc := &AgentService{ai: m}
	dir := t.TempDir()
	m.coreJournal = newAgentCoreJournal(dir)
	ts := time.Now().UTC().Format(time.RFC3339)
	m.coreJournal.ensureSessionHeader("s-bound", "/proj", "glm-5.3", ts)
	for i := 0; i < 210; i++ {
		m.coreJournal.appendUser("s-bound", fmt.Sprintf("m-%d", i), fmt.Sprintf("问%d", i), ts)
	}
	// 末条超长（5000 rune）：验证单条截断到恰好 agentVibeTranscriptMaxContentRunes。
	big := strings.Repeat("长", 5000)
	m.coreJournal.appendUser("s-bound", "m-big", big, ts)

	replies := make(chan map[string]interface{}, 8)
	svc.DispatchLocalAI(map[string]interface{}{
		"type":       models.AgentEventAISessionSubscribe,
		"session_id": "s-bound",
	}, func(p interface{}) error { replies <- p.(map[string]interface{}); return nil })
	var p map[string]interface{}
	select {
	case p = <-replies:
	case <-time.After(2 * time.Second):
		t.Fatal("未收到快照")
	}
	tr, _ := p["transcript"].([]map[string]interface{})
	if len(tr) != agentAISnapshotTranscriptCap {
		t.Fatalf("快照转录应限 %d 条,实际 %d", agentAISnapshotTranscriptCap, len(tr))
	}
	// 共 211 条，丢最旧 11 条 → 首条应是问11（保最近 200 条）。
	if got, _ := tr[0]["text"].(string); got != "问11" {
		t.Fatalf("应丢弃最旧、保留最近 %d 条: 首条=%v", agentAISnapshotTranscriptCap, tr[0]["text"])
	}
	last, _ := tr[len(tr)-1]["text"].(string)
	if n := len([]rune(last)); n != agentVibeTranscriptMaxContentRunes {
		t.Fatalf("末条超长文本应截断到 %d rune,实际 %d", agentVibeTranscriptMaxContentRunes, n)
	}
	// 解析器本身仍返回全文（Task 11 内存恢复依赖，I1 只许动传输层）。
	hist, _, err := readAgentCoreJournalHistoryFrom(dir, "s-bound")
	if err != nil {
		t.Fatalf("读回失败: %v", err)
	}
	if len(hist) != 211 {
		t.Fatalf("解析器应返回全部 211 条,实际 %d", len(hist))
	}
	if hist[len(hist)-1].Content != big {
		t.Fatal("解析器必须保留全文（截断只在快照传输层）")
	}
}

// TestDispatchLocalAIOptionResponseRouted option.response 本机作答入口：从
// default unsupported 分支捞出来，路由进 manager 既有 optionResponse（不改它）。
// 用缺字段的帧触发 optionResponse 自身守卫错误——合法帧遇未知会话在
// optionResponse 内静默 no-op（无回包可断言），缺字段错误则是可断言的路由
// 证据：若仍走 default 会回 "unsupported local AI event type: ai.option.response"。
func TestDispatchLocalAIOptionResponseRouted(t *testing.T) {
	m := newTestAISocketsManager(t)
	svc := &AgentService{ai: m}
	replies := make(chan map[string]interface{}, 8)
	svc.DispatchLocalAI(map[string]interface{}{
		"type": models.AgentEventAIOptionResponse,
	}, func(p interface{}) error { replies <- p.(map[string]interface{}); return nil })
	waitUntilFor(t, time.Second, func() bool { return len(replies) > 0 })
	p := <-replies
	if p["type"] == models.AgentEventAIError && strings.Contains(fmt.Sprint(p["error"]), "unsupported") {
		t.Fatalf("option.response 仍走 unsupported 分支: %v", p)
	}
	if !strings.Contains(fmt.Sprint(p["error"]), "missing session_id or option_id") {
		t.Fatalf("应回 optionResponse 自身守卫错误,实际 %v", p)
	}
}

// TestDispatchLocalAIToolAskResponseRouted tool_ask.response 本机作答入口：
// 与远端同语义（run 阻塞在 ask_user 上时必须能答，不受本地开关门）。路由
// 证据=toolAskResponse 懒初始化 tool ask 桥（default 分支不会建桥；作答投递
// 进桥不写 writeJSON，无回包可断言）。
func TestDispatchLocalAIToolAskResponseRouted(t *testing.T) {
	m := newTestAISocketsManager(t)
	svc := &AgentService{ai: m}
	svc.DispatchLocalAI(map[string]interface{}{
		"type":           models.AgentEventToolAskResponse,
		"interaction_id": "ta-route-1",
		"status":         "answered",
	}, func(p interface{}) error { return nil })
	m.mu.Lock()
	hasBridge := m.toolAsk != nil
	m.mu.Unlock()
	if !hasBridge {
		t.Fatal("tool_ask.response 未路由进 toolAskResponse（桥未初始化）")
	}
}

// waitUntilFor 轮询等待（注意：包内 agent_tui_turn_test.go 已有两个参数的
// waitFor(t, cond)，本 helper 用不同名字避免重名冲突）。
func waitUntilFor(t *testing.T, d time.Duration, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("waitUntilFor 超时")
}
