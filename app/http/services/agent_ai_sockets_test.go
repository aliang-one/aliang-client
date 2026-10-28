package services

import (
	"sync"
	"testing"
	"time"
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
