package services

import (
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
