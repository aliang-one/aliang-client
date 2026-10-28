package models

import "testing"

// 2026-10-28 跨端会话统一：新帧型契约锁（spec §4.1/§4.3）。
// 方向基准=远端 /ws/agent 链路（client=agent 拨号方，server=云端）。
func TestContractCrossDeviceSessionUnifyFrames(t *testing.T) {
	c := DefaultAgentProtocolContract()
	wantTypes := map[string]struct {
		inClient bool
		inServer bool
		required []string
	}{
		AgentEventAISessionSubscribe:   {false, true, []string{"type", "session_id"}},
		AgentEventAISessionUnsubscribe: {false, true, []string{"type", "session_id"}},
		AgentEventAISessionSnapshot:    {true, false, []string{"type", "session_id"}},
		AgentEventAIRunNotice:          {true, false, []string{"type", "session_id", "code"}},
	}
	for typ, want := range wantTypes {
		foundClient, foundServer := false, false
		for _, e := range c.WebSocket.ClientSends {
			if e.Type == typ {
				foundClient = true
			}
		}
		for _, e := range c.WebSocket.ServerSends {
			if e.Type == typ {
				foundServer = true
			}
		}
		if foundClient != want.inClient || foundServer != want.inServer {
			t.Fatalf("contract %s: client=%t server=%t, want client=%t server=%t", typ, foundClient, foundServer, want.inClient, want.inServer)
		}
		// Required 集合锁（顺序无关）。
		var schema *AgentProtocolEvent
		if want.inClient {
			schema = findProtocolEvent(c.WebSocket.ClientSends, typ)
		} else {
			schema = findProtocolEvent(c.WebSocket.ServerSends, typ)
		}
		if schema == nil {
			t.Fatalf("contract %s: 帧已登记但 schema 未找到", typ)
		}
		if !sameStringSet(schema.Required, want.required) {
			t.Fatalf("contract %s Required=%v, want %v", typ, schema.Required, want.required)
		}
	}
	// 审批 request 的 expires_at 进 Optional 表
	for _, e := range c.WebSocket.ClientSends {
		if e.Type == AgentEventAIApprovalRequest {
			ok := false
			for _, f := range e.Optional {
				if f == "expires_at" {
					ok = true
				}
			}
			if !ok {
				t.Fatalf("ai.approval.request Optional 缺 expires_at")
			}
		}
	}
}

func findProtocolEvent(events []AgentProtocolEvent, typ string) *AgentProtocolEvent {
	for i := range events {
		if events[i].Type == typ {
			return &events[i]
		}
	}
	return nil
}

// sameStringSet 判断两个切片作为多重集是否相等（顺序无关、重复计数敏感）。
func sameStringSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	count := make(map[string]int, len(a))
	for _, s := range a {
		count[s]++
	}
	for _, s := range b {
		if count[s] == 0 {
			return false
		}
		count[s]--
	}
	return true
}
