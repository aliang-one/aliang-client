package agentcore

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// 这一组测试锁死 steering 的语义：插话由 LoopHooks.GetSteeringMessages 在
// **下一次 LLM 调用前**取走，直接 append 进 history。
//
// 为什么值得单独测：内核不做角色合并，插话会造出连续的 user 消息
// （user(tool_result) 紧跟 user(steer)）。这条形状曾被怀疑会被网关拒
// （Anthropic Messages API 传统上要求 user/assistant 交替），实测 glm-5.2
// 对 user,user 与 user,assistant,user(tool_result),user(steer) 都返回
// end_turn。这里把「插话确实进了下一轮请求体」钉住——内核侧唯一能保证的
// 就是这件事；网关是否接受由上面的实测结论兜底。

// requestRecorder 记录每次 /v1/messages 的请求体，供断言上下文形状。
type requestRecorder struct {
	mu   sync.Mutex
	reqs []MessagesRequest
}

func (r *requestRecorder) handler(t *testing.T, turns []string) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, req *http.Request) {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			t.Errorf("读取请求体失败: %v", err)
		}
		var parsed MessagesRequest
		if err := json.Unmarshal(body, &parsed); err != nil {
			t.Errorf("请求体不是合法 JSON: %v\n%s", err, body)
		}
		r.mu.Lock()
		r.reqs = append(r.reqs, parsed)
		idx := len(r.reqs) - 1
		r.mu.Unlock()
		if idx >= len(turns) {
			// 脚本用尽时给一个干净的收尾，避免测试挂死。
			writeSSE(w, `{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`)
			return
		}
		writeSSE(w, turns[idx])
	}
}

func (r *requestRecorder) snapshot() []MessagesRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]MessagesRequest(nil), r.reqs...)
}

// messageText 取出任意形状消息里的文本：纯字符串、或内容块数组里的 text 块。
func messageText(t *testing.T, m Message) string {
	t.Helper()
	switch v := m.Content.(type) {
	case string:
		return v
	case []interface{}:
		var b strings.Builder
		for _, raw := range v {
			block, _ := raw.(map[string]interface{})
			if block == nil {
				continue
			}
			if bt, _ := block["type"].(string); bt != BlockTypeText {
				continue
			}
			if s, _ := block["text"].(string); s != "" {
				b.WriteString(s)
			}
		}
		return b.String()
	}
	return ""
}

// toolResultText 取出 tool_result 块里的文本内容（工具结果是块消息，不是
// 纯文本消息，messageText 读不到它）。
func toolResultText(t *testing.T, m Message) string {
	t.Helper()
	switch blocks := m.Content.(type) {
	case []ContentBlock:
		for _, block := range blocks {
			if block.Type != BlockTypeToolResult {
				continue
			}
			if s, ok := block.Content.(string); ok {
				return s
			}
		}
	case []interface{}:
		// 请求体经过 JSON 往返，块是 map[string]interface{}。
		for _, raw := range blocks {
			block, _ := raw.(map[string]interface{})
			if block == nil {
				continue
			}
			if bt, _ := block["type"].(string); bt != BlockTypeToolResult {
				continue
			}
			if s, ok := block["content"].(string); ok {
				return s
			}
		}
	}
	return ""
}

func messageRoles(msgs []Message) string {
	roles := make([]string, len(msgs))
	for i, m := range msgs {
		roles[i] = m.Role
	}
	return strings.Join(roles, ",")
}

// steerTurn1 是一个工具回合：模型要求调用 Stub。必须走工具回合，否则
// 一轮就结束，根本不存在「下一次 LLM 调用」。
func steerTurn1() string {
	return `{"type":"message_start","message":{"id":"msg_s1","model":"claude-x"}}` + "\n\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_s1","name":"Stub"}}` + "\n\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"x\":\"hi\"}"}}` + "\n\n" +
		`data: {"type":"content_block_stop","index":0}` + "\n\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"}}`
}

func steerTurn2() string {
	return `{"type":"message_start","message":{"id":"msg_s2","model":"claude-x"}}` + "\n\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"完成"}}` + "\n\n" +
		`data: {"type":"content_block_stop","index":0}` + "\n\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"}}`
}

// TestRunInjectsSteeringBeforeNextRequest 是 steering 的主用例：回合中插话，
// 必须出现在**下一次**请求体的末尾，且排在 tool_result 之后。
func TestRunInjectsSteeringBeforeNextRequest(t *testing.T) {
	rec := &requestRecorder{}
	srv := sseServer(t, rec.handler(t, []string{steerTurn1(), steerTurn2()}))

	registry := NewRegistry()
	registry.Register(&stubTool{name: "Stub", result: ToolResult{Content: "ok"}})

	const steerText = "顺便把 README 也更新一下"
	var calls int32
	cfg := &Config{
		Client:    NewClient(srv.URL),
		Model:     "claude-x",
		SessionID: "sess-steer",
		Tools:     registry,
		Hooks: LoopHooks{
			GetSteeringMessages: func() []Message {
				// 第一轮前用户还没来得及插话；第二轮前给一条。
				if atomic.AddInt32(&calls, 1) == 1 {
					return nil
				}
				return []Message{TextMessage("user", steerText)}
			},
		},
	}
	res, err := Run(context.Background(), cfg, []Message{TextMessage("user", "改个 bug")})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Turns != 2 {
		t.Fatalf("Turns = %d, want 2", res.Turns)
	}

	reqs := rec.snapshot()
	if len(reqs) != 2 {
		t.Fatalf("请求次数 = %d, want 2", len(reqs))
	}

	// 第一轮不该有插话：它是在这一轮请求发出之后才排队的。
	for _, m := range reqs[0].Messages {
		if strings.Contains(messageText(t, m), "README") {
			t.Fatalf("第一轮请求不该带插话，实际 messages=%+v", reqs[0].Messages)
		}
	}

	// 第二轮：user / assistant(tool_use) / user(tool_result) / user(steer)。
	// 末尾连续两个 user 是内核不合并角色的直接结果，实测网关接受。
	second := reqs[1].Messages
	if got := messageRoles(second); got != "user,assistant,user,user" {
		t.Fatalf("第二轮 roles = %s, want user,assistant,user,user", got)
	}
	if got := messageText(t, second[len(second)-1]); got != steerText {
		t.Errorf("第二轮最后一条 = %q, want %q", got, steerText)
	}
	// tool_result 必须在插话之前——否则模型会先看到插话、后看到工具结果。
	if got := toolResultText(t, second[len(second)-2]); got != "ok" {
		t.Errorf("倒数第二条应是工具结果 %q，实际 %q", "ok", got)
	}
}

// TestRunInjectsSteeringBeforeFirstRequest 覆盖「内核还没发出第一次请求时
// 用户就插话」：插话必须进第一轮请求，且在后续轮次里只出现一次。
func TestRunInjectsSteeringBeforeFirstRequest(t *testing.T) {
	rec := &requestRecorder{}
	srv := sseServer(t, rec.handler(t, []string{steerTurn1(), steerTurn2()}))

	registry := NewRegistry()
	registry.Register(&stubTool{name: "Stub", result: ToolResult{Content: "ok"}})

	const steerText = "先看下 package.json"
	var calls int32
	cfg := &Config{
		Client:    NewClient(srv.URL),
		Model:     "claude-x",
		SessionID: "sess-steer-early",
		Tools:     registry,
		Hooks: LoopHooks{
			GetSteeringMessages: func() []Message {
				// 只给一次：drain 语义要求取走即清空，重复给会被内核
				// 原样 append，等于同一句插话进上下文两次。
				if atomic.AddInt32(&calls, 1) == 1 {
					return []Message{TextMessage("user", steerText)}
				}
				return nil
			},
		},
	}
	if _, err := Run(context.Background(), cfg, []Message{TextMessage("user", "改个 bug")}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	reqs := rec.snapshot()
	if len(reqs) != 2 {
		t.Fatalf("请求次数 = %d, want 2", len(reqs))
	}

	// 第一轮就是连续的 user,user——这正是需要实测兜底的形状。
	if got := messageRoles(reqs[0].Messages); got != "user,user" {
		t.Fatalf("第一轮 roles = %s, want user,user", got)
	}
	if got := messageText(t, reqs[0].Messages[1]); got != steerText {
		t.Errorf("第一轮第二条 = %q, want %q", got, steerText)
	}

	// 第二轮：插话仍在历史里，但只出现一次。
	second := reqs[1].Messages
	if got := messageRoles(second); got != "user,user,assistant,user" {
		t.Fatalf("第二轮 roles = %s, want user,user,assistant,user", got)
	}
	if n := strings.Count(strings.Join(messageTexts(t, second), "\x00"), steerText); n != 1 {
		t.Errorf("插话在第二轮出现 %d 次，want 1", n)
	}
}

func messageTexts(t *testing.T, msgs []Message) []string {
	t.Helper()
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i] = messageText(t, m)
	}
	return out
}
