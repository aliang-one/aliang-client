package models

import "testing"

// 2026-10-28 跨端会话统一：新帧型契约锁（spec §4.1/§4.3）。
func TestContractCrossDeviceSessionUnifyFrames(t *testing.T) {
	c := DefaultAgentProtocolContract()
	wantTypes := map[string]struct {
		inClient bool
		inServer bool
	}{
		AgentEventAISessionSubscribe:   {true, false},
		AgentEventAISessionUnsubscribe: {true, false},
		AgentEventAISessionSnapshot:    {false, true},
		AgentEventAIRunNotice:          {false, true},
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
