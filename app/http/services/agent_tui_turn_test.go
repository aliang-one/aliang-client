package services

import "testing"

func TestTUITurnErrorCodes(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{newAgentTUITurnError(agentTUIErrUnavailable, "gone"), "tui_unavailable"},
		{newAgentTUITurnError(agentTUIErrInjectFailed, "dial"), "tui_inject_failed"},
		{newAgentTUITurnError(agentTUIErrHeld, "held"), "tui_held"},
		{newAgentTUITurnError(agentTUIErrTranscriptLost, "gone"), "tui_transcript_lost"},
	}
	for _, c := range cases {
		coder, ok := c.err.(interface{ ErrorCode() string })
		if !ok {
			t.Fatalf("%T 未实现 ErrorCode()", c.err)
		}
		if got := coder.ErrorCode(); got != c.want {
			t.Fatalf("ErrorCode() = %q, want %q", got, c.want)
		}
		if c.err.Error() == "" {
			t.Fatalf("Error() 不应为空")
		}
	}
}

// TestAgentAIErrorPayloadCarriesTUITurnCode 锁死 payload 契约：错误码必须经
// errors.As 被 agentAIErrorPayload 拾取并落到 error_code 字段上——否则服务端
// 收不到机器可读原因，只能对错误字符串做脆弱匹配。
func TestAgentAIErrorPayloadCarriesTUITurnCode(t *testing.T) {
	payload := agentAIErrorPayload("sess-1", "m-1", newAgentTUITurnError(agentTUIErrInjectFailed, "无法把消息送进终端会话"))
	if payload["type"] != "ai.error" {
		t.Fatalf("type = %v, want ai.error", payload["type"])
	}
	if payload["error_code"] != agentTUIErrInjectFailed {
		t.Fatalf("error_code = %v, want %q", payload["error_code"], agentTUIErrInjectFailed)
	}
}
