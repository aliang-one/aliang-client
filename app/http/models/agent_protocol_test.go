package models

// tool_ask bridge 契约(server 侧 src/modules/ai/toolAsk.ts 的镜像,2026-10-16
// 方案定稿):锁定三帧的 JSON 形状与精确白名单。白名单拒绝未知 mcp__aliang__*
// —— full 档原生加载项目 .mcp.json,恶意仓库可声明同名 server "aliang" 注入
// 工具制造命名空间碰撞,前缀自动批不得吃进未知工具名。

import (
	"encoding/json"
	"testing"
)

func TestAgentToolAskFrameTypesLocked(t *testing.T) {
	if AgentEventToolAskRequest != "tool_ask.request" {
		t.Fatalf("request type = %q", AgentEventToolAskRequest)
	}
	if AgentEventToolAskResponse != "tool_ask.response" {
		t.Fatalf("response type = %q", AgentEventToolAskResponse)
	}
	if AgentEventToolAskCancel != "tool_ask.cancel" {
		t.Fatalf("cancel type = %q", AgentEventToolAskCancel)
	}
}

func TestAgentToolAskToolWhitelistExactMatch(t *testing.T) {
	for _, name := range AgentToolAskTools {
		if !IsAgentToolAskTool(name) {
			t.Fatalf("whitelisted tool %q must match", name)
		}
	}
	// 命名空间碰撞:未知工具名一律不命中。
	for _, name := range []string{"mcp__aliang__shell", "mcp__aliang__ask_user_extra", "mcp__other__ask_user", "Bash"} {
		if IsAgentToolAskTool(name) {
			t.Fatalf("tool %q must NOT match whitelist", name)
		}
	}
}

func TestAgentToolAskRequestJSONRoundTrip(t *testing.T) {
	req := AgentToolAskRequest{
		Type:          AgentEventToolAskRequest,
		RunID:         "run_123",
		EventSeq:      42,
		InteractionID: "toolu_01ABC",
		SessionID:     "ais_456",
		DeviceID:      "dev_789",
		ToolName:      "mcp__aliang__ask_user",
		ToolInput:     json.RawMessage(`{"title":"选个方案"}`),
		ExpiresAt:     1792000000000,
	}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back AgentToolAskRequest
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.Type != req.Type || back.RunID != req.RunID || back.EventSeq != req.EventSeq ||
		back.InteractionID != req.InteractionID || back.SessionID != req.SessionID ||
		back.DeviceID != req.DeviceID || back.ToolName != req.ToolName || back.ExpiresAt != req.ExpiresAt {
		t.Fatalf("roundtrip mismatch:\n got %+v\nwant %+v", back, req)
	}
	if string(back.ToolInput) != `{"title":"选个方案"}` {
		t.Fatalf("tool_input = %s", back.ToolInput)
	}
}

func TestAgentToolAskResponseAnsweredRequiresAnswer(t *testing.T) {
	answered := AgentToolAskResponse{
		Type:          AgentEventToolAskResponse,
		InteractionID: "toolu_01ABC",
		DeliveryID:    "dv_1",
		Attempt:       1,
		Status:        AgentToolAskStatusAnswered,
		Answer:        &AgentToolAskAnswer{SelectedOptionID: "a", Message: "就这个"},
	}
	data, err := json.Marshal(answered)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back AgentToolAskResponse
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.Answer == nil || back.Answer.SelectedOptionID != "a" || back.Answer.Message != "就这个" {
		t.Fatalf("answer roundtrip mismatch: %+v", back.Answer)
	}
}

func TestAgentToolAskResponseExpiredOmitsAnswer(t *testing.T) {
	expired := AgentToolAskResponse{
		Type:          AgentEventToolAskResponse,
		InteractionID: "toolu_01ABC",
		DeliveryID:    "dv_1",
		Attempt:       1,
		Status:        AgentToolAskStatusExpired,
	}
	data, err := json.Marshal(expired)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// expired 不得携带 answer 字段(server 侧 schema 同样拒绝)。
	if _, ok := raw["answer"]; ok {
		t.Fatalf("expired response must omit answer, got %v", raw["answer"])
	}
}

func TestAgentToolAskCancelReasons(t *testing.T) {
	for reason, want := range map[string]string{
		AgentToolAskReasonTimeout:   "timeout",
		AgentToolAskReasonRunEnded:  "run_ended",
		AgentToolAskReasonClaudeErr: "claude_error",
	} {
		if reason != want {
			t.Fatalf("cancel reason = %q, want %q", reason, want)
		}
	}
	cancel := AgentToolAskCancel{Type: AgentEventToolAskCancel, InteractionID: "x", Reason: AgentToolAskReasonTimeout}
	data, err := json.Marshal(cancel)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(data) != `{"type":"tool_ask.cancel","interaction_id":"x","reason":"timeout"}` {
		t.Fatalf("cancel json = %s", data)
	}
}
