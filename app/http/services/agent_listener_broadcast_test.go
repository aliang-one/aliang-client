package services

// agent_listener_broadcast_test.go listener.triggered 账号级广播单测
// (specs/005-event-listener §2 / 计划 Task 14)。覆盖:协议常量 wire 串、
// 双 socket 全量收到原样载荷、零订阅 socket 照收(账号级契约)、注册表内
// 已死 socket 混在广播目标里不 panic 且活 socket 照收、零 socket no-op、
// 经 handleRemoteAgentMessage 的整链派发(读循环同路径,fire-and-forget
// 无回帧,enabled-device 门控豁免)。
// fake-socket 构造复用 agent_ai_sockets_test.go 的 registerAISocket+
// channel 写闭包范式;pump goroutine 内只同步、不断言。

import (
	"context"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/common/cache"
)

// listenerTriggeredFrame 构造 server 契约形态的命中帧(§1.5/§2:
// {type, listener_id, title, summary},best-effort 展示数据)。
func listenerTriggeredFrame() map[string]interface{} {
	return map[string]interface{}{
		"type":        models.AgentEventListenerTriggered,
		"listener_id": "L-trig-1",
		"title":       "vm220 掉线提醒",
		"summary":     "vm220 连续 5 分钟无心跳,请检查",
	}
}

// newListenerBroadcastSocket 注册一条捕获式 fake socket(role=local),
// 返回 socket 与捕获通道。
func newListenerBroadcastSocket(t *testing.T, m *agentAIManager) (*agentAISocket, chan map[string]interface{}) {
	t.Helper()
	got := make(chan map[string]interface{}, 8)
	sock := m.registerAISocket("local", func(p interface{}) error {
		got <- p.(map[string]interface{})
		return nil
	})
	require.NotNil(t, sock)
	return sock, got
}

func TestListenerBroadcastProtocolConstant(t *testing.T) {
	assert.Equal(t, "listener.triggered", models.AgentEventListenerTriggered)
}

// TestListenerBroadcastReachesAllSockets 账号级广播主路径:两个注册 socket
// 都收到**原样**载荷(四字段逐字,不含订阅判定)。
func TestListenerBroadcastReachesAllSockets(t *testing.T) {
	m := newTestAISocketsManager(t)
	sock1, got1 := newListenerBroadcastSocket(t, m)
	sock2, got2 := newListenerBroadcastSocket(t, m)
	defer m.unregisterAISocket(sock1.id)
	defer m.unregisterAISocket(sock2.id)

	frame := listenerTriggeredFrame()
	m.BroadcastListenerEvent(frame)

	want := listenerTriggeredFrame()
	for name, got := range map[string]chan map[string]interface{}{"socket1": got1, "socket2": got2} {
		select {
		case p := <-got:
			if !reflect.DeepEqual(want, p) {
				t.Fatalf("%s 收到的载荷不是原样帧: want %v, got %v", name, want, p)
			}
		case <-time.After(time.Second):
			t.Fatalf("%s 未收到广播", name)
		}
	}
}

// TestListenerBroadcastNoSubscriptionRequired 账号级契约:零订阅会话的
// socket 也必须收到(监听器事件不属于任何会话,无订阅门槛);订阅了别的
// 会话的 socket 同收(与订阅集无关)。
func TestListenerBroadcastNoSubscriptionRequired(t *testing.T) {
	m := newTestAISocketsManager(t)
	zeroSub, zeroGot := newListenerBroadcastSocket(t, m)
	otherSub, otherGot := newListenerBroadcastSocket(t, m)
	defer m.unregisterAISocket(zeroSub.id)
	defer m.unregisterAISocket(otherSub.id)
	m.subscribeAISocket(otherSub.id, "some-unrelated-session")
	if zeroSub.subscribed("") {
		t.Fatal("前置失效:零订阅 socket 不应有任何订阅")
	}

	m.BroadcastListenerEvent(listenerTriggeredFrame())

	for name, got := range map[string]chan map[string]interface{}{"zero-sub": zeroGot, "other-sub": otherGot} {
		select {
		case p := <-got:
			if p["type"] != models.AgentEventListenerTriggered {
				t.Fatalf("%s 收到错误帧型 %v", name, p["type"])
			}
		case <-time.After(time.Second):
			t.Fatalf("%s 未收到账号级广播(无订阅门槛)", name)
		}
	}
}

// TestListenerBroadcastClosedSocketInMix 注册表内已死 socket 混在广播目标
// (close 与 onDead 摘除之间的固有窗口):广播不 panic,活 socket 照收,
// 死 socket 的写闭包不再被调。
func TestListenerBroadcastClosedSocketInMix(t *testing.T) {
	m := newTestAISocketsManager(t)
	var deadWrites int32
	deadSock := m.registerAISocket("local", func(interface{}) error {
		atomic.AddInt32(&deadWrites, 1)
		return nil
	})
	require.NotNil(t, deadSock)
	// 白盒模拟「closed 已置位、onDead 尚未摘除」窗口:直接关 closed、留在
	// 注册表(onDead 摘除是溢出/写失败路径的后续动作,两步之间有窗口)。
	deadSock.closeOnce.Do(func() { close(deadSock.closed) })

	live, liveGot := newListenerBroadcastSocket(t, m)
	defer m.unregisterAISocket(live.id)

	require.NotPanics(t, func() { m.BroadcastListenerEvent(listenerTriggeredFrame()) })

	select {
	case p := <-liveGot:
		if p["type"] != models.AgentEventListenerTriggered {
			t.Fatalf("活 socket 收到错误帧型 %v", p["type"])
		}
	case <-time.After(time.Second):
		t.Fatal("已死 socket 在册时活 socket 仍须收到广播")
	}
	time.Sleep(50 * time.Millisecond)
	if n := atomic.LoadInt32(&deadWrites); n != 0 {
		t.Fatalf("已死 socket 的写闭包不得再被调,实际 %d 次", n)
	}
}

// TestListenerBroadcastZeroSocketsNoop 空注册表:广播 no-op、不 panic;
// nil 载荷同样 no-op(防御,不留 panic 通路)。
func TestListenerBroadcastZeroSocketsNoop(t *testing.T) {
	m := newTestAISocketsManager(t)
	require.NotPanics(t, func() {
		m.BroadcastListenerEvent(listenerTriggeredFrame())
		m.BroadcastListenerEvent(nil)
	})
	m.mu.Lock()
	n := len(m.aiSockets)
	m.mu.Unlock()
	assert.Zero(t, n, "空注册表广播不得注册任何 socket")
}

// TestListenerTriggeredViaHandleRemoteAgentMessage 整链:server 命中帧经
// handleRemoteAgentMessage(读循环同路径)→ 两个注册 socket 都收到原样帧;
// fire-and-forget 不产生任何回帧(server 侧 best-effort,无回执语义);
// 不进 enabled-device 门控(状态同步类展示流量,与 upsert/remove 同族)。
func TestListenerTriggeredViaHandleRemoteAgentMessage(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	cache.ResetCacheDirForTest()
	service := NewAgentService()

	sock1, got1 := newListenerBroadcastSocket(t, service.ai)
	sock2, got2 := newListenerBroadcastSocket(t, service.ai)
	defer service.ai.unregisterAISocket(sock1.id)
	defer service.ai.unregisterAISocket(sock2.id)

	var written int32
	writeJSON := func(v interface{}) error {
		if _, ok := v.(map[string]interface{}); ok {
			atomic.AddInt32(&written, 1)
		}
		return nil
	}

	service.handleRemoteAgentMessage(context.Background(), listenerTriggeredFrame(), writeJSON)

	want := listenerTriggeredFrame()
	for name, got := range map[string]chan map[string]interface{}{"socket1": got1, "socket2": got2} {
		select {
		case p := <-got:
			if !reflect.DeepEqual(want, p) {
				t.Fatalf("%s 收到的载荷不是原样帧: want %v, got %v", name, want, p)
			}
		case <-time.After(time.Second):
			t.Fatalf("%s 未收到经 WS 派发的命中广播", name)
		}
	}
	time.Sleep(50 * time.Millisecond)
	assert.Zero(t, atomic.LoadInt32(&written), "listener.triggered 是 fire-and-forget 展示帧,不得产生回帧")

	// 门控豁免:被禁用设备也须能收展示广播(与 listener.upsert/remove 同族)。
	assert.False(t, remoteAgentMessageRequiresEnabledDevice(models.AgentEventListenerTriggered))
}
