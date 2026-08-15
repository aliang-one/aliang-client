package services

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
)

// captureAIWriter returns a writer that records emitted events and the mutex
// guarding them, mirroring the harness used across the AI service tests.
func captureAIWriter(t *testing.T) (*sync.Mutex, *[]map[string]interface{}, agentTerminalWriter) {
	t.Helper()
	var mu sync.Mutex
	events := make([]map[string]interface{}, 0)
	writer := func(payload interface{}) error {
		event, ok := payload.(map[string]interface{})
		if !ok {
			t.Fatalf("payload type = %T, want map[string]interface{}", payload)
		}
		mu.Lock()
		events = append(events, event)
		mu.Unlock()
		return nil
	}
	return &mu, &events, writer
}

func findAIEvents(mu *sync.Mutex, events *[]map[string]interface{}, eventType string) []map[string]interface{} {
	mu.Lock()
	defer mu.Unlock()
	out := make([]map[string]interface{}, 0)
	for _, ev := range *events {
		if ev["type"] == eventType {
			out = append(out, ev)
		}
	}
	return out
}

func lastAIEvent(mu *sync.Mutex, events *[]map[string]interface{}, eventType string) map[string]interface{} {
	found := findAIEvents(mu, events, eventType)
	if len(found) == 0 {
		return nil
	}
	return found[len(found)-1]
}

// registerPendingApproval inserts a waiter directly so approval()/sync can be
// exercised without spinning up a real CLI run.
func registerPendingApproval(m *agentAIManager, sessionID, approvalID string, runSeq int) chan agentAIApprovalResponse {
	respond := make(chan agentAIApprovalResponse, 1)
	req := agentAIApprovalRequest{
		ID:        approvalID,
		SessionID: sessionID,
		Kind:      "command",
		Provider:  "codex",
		Command:   "make test",
		respond:   respond,
	}
	m.mu.Lock()
	m.approvals[agentAIApprovalMapKey(sessionID, approvalID)] = &agentAIApprovalWaiter{
		sessionID: sessionID,
		runSeq:    runSeq,
		request:   req,
	}
	m.mu.Unlock()
	return respond
}

func TestClaudeApprovalHookStrategyByVersion(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want claudeApprovalHookStrategy
	}{
		{name: "old claude 2.1 uses pretool command bridge", raw: "2.1.17 (Claude Code)", want: claudeApprovalHookPreToolUseCommand},
		{name: "newer claude uses permission request http", raw: "2.2.0 (Claude Code)", want: claudeApprovalHookPermissionRequestHTTP},
		{name: "future claude stays on permission request http", raw: "3.0.1 (Claude Code)", want: claudeApprovalHookPermissionRequestHTTP},
		{name: "unknown version falls back to proven legacy bridge", raw: "Claude Code dev build", want: claudeApprovalHookPreToolUseCommand},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := claudeApprovalHookStrategyForVersion(tt.raw); got != tt.want {
				t.Fatalf("strategy = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestClaudeApprovalHookStrategyOverride(t *testing.T) {
	tests := []struct {
		raw  string
		want claudeApprovalHookStrategy
	}{
		{raw: "legacy", want: claudeApprovalHookPreToolUseCommand},
		{raw: "pre_tool_use_command", want: claudeApprovalHookPreToolUseCommand},
		{raw: "modern", want: claudeApprovalHookPermissionRequestHTTP},
		{raw: "permission_request_http", want: claudeApprovalHookPermissionRequestHTTP},
	}
	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			got, ok := claudeApprovalHookStrategyOverride(tt.raw)
			if !ok {
				t.Fatalf("override %q was not recognized", tt.raw)
			}
			if got != tt.want {
				t.Fatalf("override strategy = %s, want %s", got, tt.want)
			}
		})
	}
	if _, ok := claudeApprovalHookStrategyOverride("unknown"); ok {
		t.Fatal("unknown override should not be recognized")
	}
}

func TestClaudeApprovalHookSettingsLegacyPreToolUseCommand(t *testing.T) {
	t.Cleanup(func() {
		SetAgentAIApprovalHookBaseURL(UserAgentBaseURL())
	})
	SetAgentAIApprovalHookBaseURL("http://127.0.0.1:49111")
	settings, err := claudeApprovalHookSettings(claudeApprovalHookPreToolUseCommand, agentAIRun{
		sessionID:     "s_legacy",
		messageID:     "m_legacy",
		approvalToken: "tok_legacy",
	})
	if err != nil {
		t.Fatalf("claudeApprovalHookSettings() error = %v", err)
	}
	raw, _ := json.Marshal(settings)
	text := string(raw)
	for _, want := range []string{
		`"PreToolUse"`,
		`"type":"command"`,
		`curl -sS -X POST`,
		`session_id=s_legacy`,
		`message_id=assistant_m_legacy`,
		`token=tok_legacy`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("settings = %s, want substring %q", text, want)
		}
	}
	if strings.Contains(text, "PermissionRequest") || strings.Contains(text, `"type":"http"`) {
		t.Fatalf("legacy settings should not use PermissionRequest/http: %s", text)
	}
}

func TestClaudeApprovalHookSettingsModernPermissionRequestHTTP(t *testing.T) {
	t.Cleanup(func() {
		SetAgentAIApprovalHookBaseURL(UserAgentBaseURL())
	})
	SetAgentAIApprovalHookBaseURL("http://127.0.0.1:49112")
	settings, err := claudeApprovalHookSettings(claudeApprovalHookPermissionRequestHTTP, agentAIRun{
		sessionID:     "s_modern",
		messageID:     "m_modern",
		approvalToken: "tok_modern",
	})
	if err != nil {
		t.Fatalf("claudeApprovalHookSettings() error = %v", err)
	}
	raw, _ := json.Marshal(settings)
	text := string(raw)
	for _, want := range []string{
		`"PermissionRequest"`,
		`"type":"http"`,
		`session_id=s_modern`,
		`message_id=assistant_m_modern`,
		`token=tok_modern`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("settings = %s, want substring %q", text, want)
		}
	}
	if strings.Contains(text, "PreToolUse") || strings.Contains(text, `"type":"command"`) {
		t.Fatalf("modern settings should not use PreToolUse/command: %s", text)
	}
}

func TestClaudeApprovalHookDecisionSchemas(t *testing.T) {
	pre := claudeApprovalHookDecision("PreToolUse", true, "ok")
	preOutput, _ := pre["hookSpecificOutput"].(map[string]interface{})
	if preOutput["hookEventName"] != "PreToolUse" || preOutput["permissionDecision"] != "allow" {
		t.Fatalf("PreToolUse decision = %#v, want legacy allow schema", preOutput)
	}
	if _, exists := preOutput["decision"]; exists {
		t.Fatalf("PreToolUse decision unexpectedly used modern schema: %#v", preOutput)
	}

	modern := claudeApprovalHookDecision("PermissionRequest", false, "nope")
	modernOutput, _ := modern["hookSpecificOutput"].(map[string]interface{})
	decision, _ := modernOutput["decision"].(map[string]interface{})
	if modernOutput["hookEventName"] != "PermissionRequest" || decision["behavior"] != "deny" || decision["message"] != "nope" {
		t.Fatalf("PermissionRequest decision = %#v, want modern deny schema", modernOutput)
	}
	if _, exists := modernOutput["permissionDecision"]; exists {
		t.Fatalf("PermissionRequest decision unexpectedly used legacy schema: %#v", modernOutput)
	}
}

func TestAgentAIApprovalAckAppliedAndDelivers(t *testing.T) {
	manager := newAgentAIManager()
	defer manager.closeAll()
	mu, events, writer := captureAIWriter(t)

	respond := registerPendingApproval(manager, "s1", "ap_1", 1)

	manager.approval(map[string]interface{}{
		"type":        "ai.approval.response",
		"session_id":  "s1",
		"approval_id": "ap_1",
		"decision":    "accept",
		"delivery_id": "dv_7",
	}, writer)

	// Decision delivered to the live waiter.
	select {
	case got := <-respond:
		if got.Decision != "accept" {
			t.Fatalf("waiter decision = %q, want accept", got.Decision)
		}
	default:
		t.Fatal("waiter was not resolved")
	}

	ack := lastAIEvent(mu, events, "ai.approval.ack")
	if ack == nil {
		t.Fatal("expected ai.approval.ack(applied), got none")
	}
	if ack["result"] != "applied" {
		t.Fatalf("ack result = %v, want applied", ack["result"])
	}
	if ack["delivery_id"] != "dv_7" {
		t.Fatalf("ack delivery_id = %v, want dv_7", ack["delivery_id"])
	}
	if ack["approval_id"] != "ap_1" {
		t.Fatalf("ack approval_id = %v, want ap_1", ack["approval_id"])
	}
}

func TestAgentAIApprovalAckDuplicate(t *testing.T) {
	manager := newAgentAIManager()
	defer manager.closeAll()
	mu, events, writer := captureAIWriter(t)

	registerPendingApproval(manager, "s2", "ap_2", 1)

	// First delivery resolves + acks applied.
	manager.approval(map[string]interface{}{
		"type": "ai.approval.response", "session_id": "s2", "approval_id": "ap_2",
		"decision": "accept", "delivery_id": "dv_1",
	}, writer)
	// Re-delivery (e.g. ack lost) must be idempotent + ack duplicate, not re-resolve.
	manager.approval(map[string]interface{}{
		"type": "ai.approval.response", "session_id": "s2", "approval_id": "ap_2",
		"decision": "accept", "delivery_id": "dv_2",
	}, writer)

	acks := findAIEvents(mu, events, "ai.approval.ack")
	if len(acks) != 2 {
		t.Fatalf("expected 2 acks, got %d", len(acks))
	}
	if acks[0]["result"] != "applied" {
		t.Fatalf("first ack = %v, want applied", acks[0]["result"])
	}
	if acks[1]["result"] != "duplicate" {
		t.Fatalf("second ack = %v, want duplicate", acks[1]["result"])
	}
}

func TestAgentAIApprovalAckNoMatch(t *testing.T) {
	manager := newAgentAIManager()
	defer manager.closeAll()
	mu, events, writer := captureAIWriter(t)

	// No waiter registered for ap_unknown -> no_match (run already ended).
	manager.approval(map[string]interface{}{
		"type": "ai.approval.response", "session_id": "s3", "approval_id": "ap_unknown",
		"decision": "accept", "delivery_id": "dv_9",
	}, writer)

	ack := lastAIEvent(mu, events, "ai.approval.ack")
	if ack == nil || ack["result"] != "no_match" {
		t.Fatalf("expected ack(no_match), got %+v", ack)
	}
	if lastAIEvent(mu, events, "ai.status") == nil {
		t.Fatal("expected an approval_not_found status alongside the ack")
	}
}

func TestAgentAIApprovalSyncListsPending(t *testing.T) {
	manager := newAgentAIManager()
	defer manager.closeAll()
	mu, events, writer := captureAIWriter(t)

	// No pending -> no sync emitted.
	manager.emitApprovalSync(writer)
	if got := findAIEvents(mu, events, "ai.approval.sync"); len(got) != 0 {
		t.Fatalf("expected no sync when none pending, got %d", len(got))
	}

	registerPendingApproval(manager, "s4", "ap_a", 1)
	registerPendingApproval(manager, "s4", "ap_b", 1)

	// Pending approvals are listed.
	manager.emitApprovalSync(writer)
	syncs := findAIEvents(mu, events, "ai.approval.sync")
	if len(syncs) != 1 {
		t.Fatalf("expected 1 sync, got %d", len(syncs))
	}
	pending, _ := syncs[0]["pending"].([]map[string]interface{})
	if len(pending) != 2 {
		t.Fatalf("sync pending len = %d, want 2", len(pending))
	}
	ids := map[string]bool{}
	for _, entry := range pending {
		ids[entry["approval_id"].(string)] = true
	}
	if !ids["ap_a"] || !ids["ap_b"] {
		t.Fatalf("sync pending ids = %v, want ap_a+ap_b", ids)
	}
}

func TestAgentAIApprovalCancelledEmittedOnClose(t *testing.T) {
	manager := newAgentAIManager()
	defer manager.closeAll()
	mu, events, writer := captureAIWriter(t)

	// Seed a session + a pending approval, then close -> cancelled emitted.
	manager.mu.Lock()
	manager.sessions["s5"] = &agentAISession{id: "s5", runSeq: 1}
	manager.mu.Unlock()
	registerPendingApproval(manager, "s5", "ap_c", 1)

	manager.close(map[string]interface{}{"type": "ai.session.close", "session_id": "s5"}, writer)

	cancelled := lastAIEvent(mu, events, "ai.approval.cancelled")
	if cancelled == nil {
		t.Fatal("expected ai.approval.cancelled on session close")
	}
	if cancelled["session_id"] != "s5" {
		t.Fatalf("cancelled session_id = %v, want s5", cancelled["session_id"])
	}
	ids, _ := cancelled["approval_ids"].([]string)
	if len(ids) != 1 || ids[0] != "ap_c" {
		t.Fatalf("cancelled approval_ids = %v, want [ap_c]", ids)
	}

	// And the pending approval is gone from the manager.
	manager.mu.Lock()
	_, stillThere := manager.approvals[agentAIApprovalMapKey("s5", "ap_c")]
	manager.mu.Unlock()
	if stillThere {
		t.Fatal("pending approval was not removed on close")
	}
}
