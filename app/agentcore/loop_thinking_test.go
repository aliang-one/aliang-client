package agentcore

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// thinkingTurn 构造一个带 thinking 块的回合 SSE：先一段 thinking_delta，
// 再一段 text_delta，最后 message_delta(end_turn) + message_stop。
func thinkingTurn(thinking, text string) string {
	frames := []string{
		`{"type":"message_start","message":{"id":"msg_th","model":"glm-5.2"}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"` + thinking + `"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"` + text + `"}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`,
		`{"type":"message_stop"}`,
	}
	var b []byte
	for _, f := range frames {
		b = append(b, "data: "...)
		b = append(b, f...)
		b = append(b, '\n', '\n')
	}
	return string(b)
}

// 逐回合 transcript：OnTurnEnd 每回合都要拿到本回合 assistant 文本与 thinking，
// 且 thinking 不得混进 Text、不得进入历史（回传需 signature，会破坏请求）。
// 标准载荷键是 "thinking"（Anthropic 语义）；个别网关走 "text"，都要接得住。
func TestOnTurnEndCapturesThinkingAndText(t *testing.T) {
	tests := []struct {
		name     string
		streamFn func(thinking, text string) string
	}{
		{"thinking 键", thinkingTurn},
		{"text 键（网关变体）", func(thinking, text string) string {
			return strings.ReplaceAll(thinkingTurn(thinking, text),
				`"type":"thinking_delta","thinking":"`, `"type":"thinking_delta","text":"`)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte(tt.streamFn("先想一下再答", "这是答案")))
			}))
			defer srv.Close()

			var turns []TurnResult
			cfg := &Config{
				Client: NewClient(srv.URL),
				Model:  "glm-5.2",
				Emit:   func([]byte) error { return nil },
				Hooks: LoopHooks{
					OnTurnEnd: func(tr TurnResult) { turns = append(turns, tr) },
				},
			}

			res, err := Run(t.Context(), cfg, []Message{TextMessage("user", "问题")})
			if err != nil {
				t.Fatalf("Run 失败: %v", err)
			}
			if res == nil || res.Text != "这是答案" {
				t.Fatalf("Run 结果异常: %+v", res)
			}
			if len(turns) != 1 {
				t.Fatalf("OnTurnEnd 触发 %d 次，want 1", len(turns))
			}
			if turns[0].Text != "这是答案" {
				t.Errorf("turn.Text = %q, want 这是答案", turns[0].Text)
			}
			if turns[0].Thinking != "先想一下再答" {
				t.Errorf("turn.Thinking = %q, want 先想一下再答", turns[0].Thinking)
			}
		})
	}
}
