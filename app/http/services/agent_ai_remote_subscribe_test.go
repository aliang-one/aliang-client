package services

import (
	"testing"
	"time"
)

// 远端订阅兜底（2026-11-12 视频会话事故）。run 协议帧（ai.run.started/delta/
// 终态）到云端的唯一通道是订阅扇出，而远端常驻 socket 的订阅由服务端代 App
// 维护、随 interest 30 分钟 TTL 过期发 unsubscribe。桌面/TUI-origin 的回合
// 开跑时云端往往已不在订阅名单里——帧全部蒸发，服务端自证认领（PhoneServer
// 8d8f790）拿不到触发帧，云端 run_state 永卡终态，手机「内容新、状态旧」。
// 契约：run 活着 ⇒ 远端 socket 必须被订阅（幂等，无远端 socket 时静默 no-op）。

func newLiveRunTestSession(m *agentAIManager, id string) {
	m.mu.Lock()
	m.sessions[id] = &agentAISession{id: id, activeRunID: "run-" + id}
	m.mu.Unlock()
}

// TestRunStartSubscribesRemoteSocket 开跑钩子把远端 socket 订上来之后，未被
// 订阅的远端 socket 能收到该会话的 run 帧扇出（事故复现：订阅缺失时帧蒸发）。
func TestRunStartSubscribesRemoteSocket(t *testing.T) {
	m := newTestAISocketsManager(t)
	originWritten := make(chan struct{}, 4)
	remoteGot := make(chan map[string]interface{}, 4)
	originWrite := func(interface{}) error {
		originWritten <- struct{}{}
		return nil
	}
	origin := m.registerAISocket("local", originWrite)
	m.registerAISocket("remote", func(p interface{}) error {
		remoteGot <- p.(map[string]interface{})
		return nil
	})
	defer m.unregisterAISocket(agentAIRemoteSocketID) // 收尾：让远端 pump 退出
	_ = origin
	// 刻意不手动订阅远端 socket——模拟 App interest 过期、云端未代发订阅。

	m.ensureRemoteAISessionSubscribed("s1")

	write := m.fanOutWrite(origin.id, "s1", originWrite)
	_ = write(map[string]interface{}{"type": "ai.run.started", "session_id": "s1"})
	<-originWritten
	select {
	case got := <-remoteGot:
		if got["type"] != "ai.run.started" {
			t.Fatalf("远端 socket 收到错误事件: %v", got["type"])
		}
	case <-time.After(time.Second):
		t.Fatal("远端 socket 未收到 run.started——开跑订阅兜底失效（2026-11-12 事故形态）")
	}
}

// TestResubscribeRemoteSessionsForActiveRuns 重连补订阅：只补活跃 run 会话；
// 远端 socket 重建（订阅集清空）后补订阅恢复活跃会话、不波及空闲会话。
func TestResubscribeRemoteSessionsForActiveRuns(t *testing.T) {
	m := newTestAISocketsManager(t)
	newLiveRunTestSession(m, "s-live")
	m.mu.Lock()
	m.sessions["s-idle"] = &agentAISession{id: "s-idle"}
	m.mu.Unlock()

	remoteSubs := func() map[string]struct{} {
		m.mu.Lock()
		sock := m.aiSockets[agentAIRemoteSocketID]
		m.mu.Unlock()
		if sock == nil {
			return nil
		}
		sock.mu.Lock()
		defer sock.mu.Unlock()
		out := make(map[string]struct{}, len(sock.subs))
		for id := range sock.subs {
			out[id] = struct{}{}
		}
		return out
	}

	// 第一次连接：注册 + 补订阅。
	m.registerAISocket("remote", func(interface{}) error { return nil })
	m.resubscribeRemoteAISessionsForActiveRuns()
	subs := remoteSubs()
	if _, ok := subs["s-live"]; !ok {
		t.Fatal("活跃 run 会话未被补订阅")
	}
	if _, ok := subs["s-idle"]; ok {
		t.Fatal("空闲会话不应被补订阅")
	}
	m.unregisterAISocket(agentAIRemoteSocketID)

	// 重连：远端 socket 重建、订阅集为空，补订阅恢复活跃会话。
	m.registerAISocket("remote", func(interface{}) error { return nil })
	m.resubscribeRemoteAISessionsForActiveRuns()
	subs = remoteSubs()
	if _, ok := subs["s-live"]; !ok {
		t.Fatal("重连后活跃 run 会话未恢复订阅")
	}
	if _, ok := subs["s-idle"]; ok {
		t.Fatal("重连后空闲会话不应被补订阅")
	}
	m.unregisterAISocket(agentAIRemoteSocketID)
}

// TestRemoteSubscribeWithoutSocketNoop 远端 socket 未注册（离线）时开跑订阅
// 与重连补订阅都必须静默 no-op，不 panic、不落任何状态。
func TestRemoteSubscribeWithoutSocketNoop(t *testing.T) {
	m := newTestAISocketsManager(t)
	newLiveRunTestSession(m, "s1")
	m.ensureRemoteAISessionSubscribed("s1")
	m.resubscribeRemoteAISessionsForActiveRuns()
	if got := len(m.aiSockets); got != 0 {
		t.Fatalf("无远端 socket 时不应产生 socket: %d", got)
	}
}
