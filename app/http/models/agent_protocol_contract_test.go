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

// 2026-10-31 撤回重发：create 侧 truncate_before_message_id 与回显
// truncate_applied 必须双双登记进契约表（表对外下发，漏登=前端拿不到契约）。
func TestContractTruncateRecallFields(t *testing.T) {
	c := DefaultAgentProtocolContract()
	create := findProtocolEvent(c.WebSocket.ServerSends, AgentEventAISessionCreate)
	if create == nil {
		t.Fatalf("契约表缺 ai.session.create")
	}
	found := false
	for _, f := range create.Optional {
		if f == "truncate_before_message_id" {
			found = true
		}
	}
	if !found {
		t.Fatalf("ai.session.create Optional 缺 truncate_before_message_id")
	}
	created := findProtocolEvent(c.WebSocket.ClientSends, AgentEventAISessionCreated)
	if created == nil {
		t.Fatalf("契约表缺 ai.session.created")
	}
	found = false
	for _, f := range created.Optional {
		if f == "truncate_applied" {
			found = true
		}
	}
	if !found {
		t.Fatalf("ai.session.created Optional 缺 truncate_applied")
	}
}

// 2026-11-08 决策跨端同步：ai.option.request 的 resolved 同型帧字段
// （status/selected/custom_text）必须登记进契约 Optional 表（表对外下发，
// 漏登=前端拿不到契约提示）。
func TestContractOptionResolvedFields(t *testing.T) {
	c := DefaultAgentProtocolContract()
	schema := findProtocolEvent(c.WebSocket.ServerSends, AgentEventAIOptionRequest)
	if schema == nil {
		schema = findProtocolEvent(c.WebSocket.ClientSends, AgentEventAIOptionRequest)
	}
	if schema == nil {
		t.Fatalf("契约表缺 ai.option.request")
	}
	want := map[string]bool{"status": false, "selected": false, "custom_text": false}
	for _, f := range schema.Optional {
		if _, ok := want[f]; ok {
			want[f] = true
		}
	}
	for f, found := range want {
		if !found {
			t.Fatalf("ai.option.request Optional 缺 %s（resolved 同型帧字段）", f)
		}
	}
}

// 2026-11-12 mesh UDP 打洞（plan 2026-11-12-mesh-udp-punch Task 1）：peer.punch
// 三帧契约锁。登记位置照 peer.lan 家族先例——整族（含 B→server 的 result）
// 就近落在 ServerSends 块；candidates 为数组字段，表内写法照 entries/messages
// 惯例用裸名（无 [] 后缀）。
func TestContractPeerPunchFrames(t *testing.T) {
	if AgentEventPeerPunchSession != "peer.punch.session" {
		t.Fatalf("AgentEventPeerPunchSession=%q, want peer.punch.session", AgentEventPeerPunchSession)
	}
	if AgentEventPeerPunchSessionResult != "peer.punch.session.result" {
		t.Fatalf("AgentEventPeerPunchSessionResult=%q, want peer.punch.session.result", AgentEventPeerPunchSessionResult)
	}
	if AgentEventPeerPunchCandidates != "peer.punch.candidates" {
		t.Fatalf("AgentEventPeerPunchCandidates=%q, want peer.punch.candidates", AgentEventPeerPunchCandidates)
	}
	c := DefaultAgentProtocolContract()
	want := map[string]struct {
		required []string
		optional []string
		emits    []string
	}{
		AgentEventPeerPunchSession: {
			required: []string{"type", "request_id", "session_id", "token", "ttl_seconds"},
			optional: []string{"advertise_ip"},
			emits:    []string{AgentEventPeerPunchSessionResult},
		},
		AgentEventPeerPunchSessionResult: {
			required: []string{"type", "request_id", "ok"},
			optional: []string{"port", "fingerprint", "fail_reason"},
		},
		AgentEventPeerPunchCandidates: {
			required: []string{"type", "session_id", "candidates"},
		},
	}
	for typ, w := range want {
		schema := findProtocolEvent(c.WebSocket.ServerSends, typ)
		if schema == nil {
			t.Fatalf("契约表 ServerSends 缺 %s", typ)
		}
		if !sameStringSet(schema.Required, w.required) {
			t.Fatalf("contract %s Required=%v, want %v", typ, schema.Required, w.required)
		}
		if !sameStringSet(schema.Optional, w.optional) {
			t.Fatalf("contract %s Optional=%v, want %v", typ, schema.Optional, w.optional)
		}
		if !sameStringSet(schema.Emits, w.emits) {
			t.Fatalf("contract %s Emits=%v, want %v", typ, schema.Emits, w.emits)
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
