package services

// 设备互访阶段 1（spec §5.1/§5.2）协议层契约测试：常量、契约表登记、能力位、
// WS 单帧读限、peer.exec.request 分发可达（四点齐，照互传契约测试先例）。

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/common/cache"
	"github.com/gorilla/websocket"
)

func TestPeerExecProtocolConstants(t *testing.T) {
	cases := map[string]string{
		models.AgentEventPeerExecRequest:  "peer.exec.request",
		models.AgentEventPeerExecResult:   "peer.exec.result",
		models.AgentEventPeerExecRejected: "peer.exec.rejected",
	}
	for got, want := range cases {
		if got != want {
			t.Fatalf("constant drift: got %q want %q", got, want)
		}
	}
}

func TestPeerExecContractEntriesRegistered(t *testing.T) {
	contract := models.DefaultAgentProtocolContract()

	find := func(events []models.AgentProtocolEvent, typ string) *models.AgentProtocolEvent {
		for i := range events {
			if events[i].Type == typ {
				return &events[i]
			}
		}
		return nil
	}
	requireField := func(t *testing.T, ev *models.AgentProtocolEvent, field string) {
		t.Helper()
		for _, f := range ev.Required {
			if f == field {
				return
			}
		}
		t.Fatalf("%s contract missing required field %q (Required=%v)", ev.Type, field, ev.Required)
	}

	req := find(contract.WebSocket.ServerSends, models.AgentEventPeerExecRequest)
	if req == nil {
		t.Fatal("peer.exec.request missing from ServerSends")
	}
	for _, field := range []string{"request_id", "source_device_id", "command"} {
		requireField(t, req, field)
	}
	for _, field := range []string{"timeout_ms", "workdir"} {
		found := false
		for _, f := range req.Optional {
			if f == field {
				found = true
			}
		}
		if !found {
			t.Fatalf("peer.exec.request Optional missing %q", field)
		}
	}

	res := find(contract.WebSocket.ClientSends, models.AgentEventPeerExecResult)
	if res == nil {
		t.Fatal("peer.exec.result missing from ClientSends")
	}
	for _, field := range []string{"request_id", "target_device_id", "exit_code"} {
		requireField(t, res, field)
	}

	rejected := find(contract.WebSocket.ServerSends, models.AgentEventPeerExecRejected)
	if rejected == nil {
		t.Fatal("peer.exec.rejected missing from ServerSends")
	}
	requireField(t, rejected, "request_id")
	requireField(t, rejected, "reason") // 裁决语义的载体，必填（对抗审查 blocker 3）
}

func TestAgentCapabilitiesAdvertisePeerV1(t *testing.T) {
	found := false
	for _, cap := range agentCapabilities() {
		if cap == "peer_v1" {
			found = true
		}
	}
	if !found {
		t.Fatal("agentCapabilities() must advertise peer_v1 (spec §5.1)")
	}
	// hello 载荷带 capabilities → 间接锁 hello 带 peer_v1。
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ALIANG_DATA_DIR", t.TempDir())
	cache.ResetCacheDirForTest()
	t.Cleanup(cache.ResetCacheDirForTest)
	svc := &AgentService{}
	hello := svc.agentHelloPayload()
	caps, ok := hello["capabilities"].([]string)
	if !ok {
		t.Fatalf("hello capabilities wrong shape: %T", hello["capabilities"])
	}
	hasPeer := false
	for _, c := range caps {
		if c == "peer_v1" {
			hasPeer = true
		}
	}
	if !hasPeer {
		t.Fatal("hello payload capabilities missing peer_v1")
	}
}

func TestRemoteAgentMessageRequiresEnabledDevice_PeerExecRequest(t *testing.T) {
	if !remoteAgentMessageRequiresEnabledDevice(models.AgentEventPeerExecRequest) {
		t.Fatal("peer.exec.request must be gated on enabled device (control-plane family)")
	}
	// rejected 是服务端裁决回包的旁证，不得进门禁（禁用设备也要能收尾消费）。
	if remoteAgentMessageRequiresEnabledDevice(models.AgentEventPeerExecRejected) {
		t.Fatal("peer.exec.rejected must NOT be gated")
	}
}

func TestHandleRemoteAgentMessage_PeerExecRequestDispatch(t *testing.T) {
	// 端到端分发：经 default 显式臂（新增 case）收到 peer.exec.request 帧、
	// 回 peer.exec.result。占住串行锁走 busy 分支——不真执行命令，测试快且稳。
	peerExecTestEnv(t)
	agentPeerExecMu.Lock()
	defer agentPeerExecMu.Unlock()

	svc := &AgentService{}
	svc.mu.Lock()
	svc.state.Enabled = true
	svc.state.Registered = true
	svc.mu.Unlock()

	done := make(chan map[string]interface{}, 1)
	writeJSON := func(p interface{}) error {
		if m, ok := p.(map[string]interface{}); ok {
			done <- m
		}
		return nil
	}
	svc.handleRemoteAgentMessage(map[string]interface{}{
		"type":             models.AgentEventPeerExecRequest,
		"request_id":       "req-dispatch-1",
		"source_device_id": "device-a",
		"command":          "echo hi",
	}, writeJSON)

	select {
	case m := <-done:
		if m["type"] != models.AgentEventPeerExecResult {
			t.Fatalf("type = %v, want peer.exec.result", m["type"])
		}
		if m["request_id"] != "req-dispatch-1" {
			t.Fatalf("request_id = %v", m["request_id"])
		}
		if m["fail_reason"] != peerExecFailBusy {
			t.Fatalf("fail_reason = %v, want busy", m["fail_reason"])
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no peer.exec.result written for peer.exec.request")
	}
}

func TestHandleRemoteAgentMessage_PeerExecRejectedSilent(t *testing.T) {
	peerExecTestEnv(t)
	svc := &AgentService{}
	svc.mu.Lock()
	svc.state.Enabled = true
	svc.state.Registered = true
	svc.mu.Unlock()

	called := false
	writeJSON := func(p interface{}) error {
		called = true
		return nil
	}
	svc.handleRemoteAgentMessage(map[string]interface{}{
		"type":       models.AgentEventPeerExecRejected,
		"request_id": "req-rej-1",
		"reason":     "not_allowed",
	}, writeJSON)
	if called {
		t.Fatal("peer.exec.rejected must be consumed silently (no unsupported noise)")
	}
}

func TestRunRemoteAgentSession_ReadLimitEnforced(t *testing.T) {
	// 端到端锁 SetReadLimit：服务端发 >8MiB 单帧，客户端 ReadJSON 必须报
	// read limit exceeded 断连（而不是把对端任意大小帧读进内存）。
	peerExecTestEnv(t)

	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		// 读掉 hello，再发一帧超过 8MiB 读限的文本帧。
		var discard map[string]interface{}
		_ = conn.ReadJSON(&discard)
		_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		_ = conn.WriteMessage(websocket.TextMessage, make([]byte, (8<<20)+1024))
		time.Sleep(300 * time.Millisecond)
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	svc := &AgentService{}
	svc.ai = newAgentAIManager()
	svc.terminal = newAgentTerminalManager()
	err = svc.runRemoteAgentSession(conn)
	if err == nil {
		t.Fatal("oversized frame must fail the read loop")
	}
	if !strings.Contains(err.Error(), "read limit exceeded") {
		t.Fatalf("error = %v, want read limit exceeded", err)
	}
}
