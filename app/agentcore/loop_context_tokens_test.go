package agentcore

import (
	"context"
	"net/http/httptest"
	"strconv"
	"testing"
)

// 这一组锁死诊断快照 ContextTokens 的三个来源优先级：流首 message_start.usage
// 为主，流末 message_delta.usage 兜底（deepseek 等 OpenAI 风格 anthropic 兼容
// 端点不在流首报量），全程无用量时按 EstimateTokens 估算。诊断面板的
// 「上下文用量」卡直接消费这份数据——恒 0 时整卡观感为空（2026-10-29 线上
// deepseek-flash 实测）。

// tailUsageTurn：流首不带 usage（deepseek 形态），流末 message_delta.usage
// 给全量 input 侧计数。
func tailUsageTurn(text string, in, cacheRead, cacheWrite int) string {
	return `{"type":"message_start","message":{"id":"msg_t1","model":"claude-x"}}` + "\n\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"` + text + `"}}` + "\n\n" +
		`data: {"type":"content_block_stop","index":0}` + "\n\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":` + itoa(in) +
		`,"cache_read_input_tokens":` + itoa(cacheRead) + `,"cache_creation_input_tokens":` + itoa(cacheWrite) + `,"output_tokens":42}}`
}

func itoa(n int) string {
	return strconv.Itoa(n)
}

// TestLoopContextTokensFromStreamTailUsage：流首无用量、流末带全量 → 快照取
// 流末三项和（input + cache_read + cache_write）。
func TestLoopContextTokensFromStreamTailUsage(t *testing.T) {
	srv := scriptServer(t, []string{tailUsageTurn("好的", 3000, 5000, 200)})
	defer srv.Close()

	snapshots := []TurnSnapshot{}
	_, err := Run(context.Background(), &Config{
		Client: NewClient(srv.URL),
		Model:  "claude-x",
		Hooks:  LoopHooks{OnTurn: func(snap TurnSnapshot) { snapshots = append(snapshots, snap) }},
	}, []Message{TextMessage("user", "tail usage")})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(snapshots) == 0 {
		t.Fatal("应有快照")
	}
	// 回合快照（index 0）在 OnTurn 触发时点流末 usage 尚未到达（快照在发起
	// 请求前发）；终态快照（最后一个）时流末 usage 已收全，必须是三项和。
	tail := snapshots[len(snapshots)-1]
	if got := tail.ContextTokens; got != 8200 {
		t.Fatalf("终态快照 ContextTokens = %d, want 8200（3000+5000+200）", got)
	}
}

// TestLoopContextTokensPrefersStartWhenTailLacksInput：anthropic 正常语义里
// 流末 message_delta.usage 只补 output（input 侧为 0）——三项和为 0 不得把
// 流首已有的实测值覆盖掉。
func TestLoopContextTokensPrefersStartWhenTailLacksInput(t *testing.T) {
	startUsage := `{"type":"message_start","message":{"id":"msg_s1","model":"claude-x","usage":{"input_tokens":100,"cache_read_input_tokens":400}}}` + "\n\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}` + "\n\n" +
		`data: {"type":"content_block_stop","index":0}` + "\n\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":7}}`
	srv := scriptServer(t, []string{startUsage})
	defer srv.Close()

	snapshots := []TurnSnapshot{}
	_, err := Run(context.Background(), &Config{
		Client: NewClient(srv.URL),
		Model:  "claude-x",
		Hooks:  LoopHooks{OnTurn: func(snap TurnSnapshot) { snapshots = append(snapshots, snap) }},
	}, []Message{TextMessage("user", "start usage")})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	tail := snapshots[len(snapshots)-1]
	if got := tail.ContextTokens; got != 500 {
		t.Fatalf("终态快照 ContextTokens = %d, want 500（流首 100+400 不被流末 output-only 覆盖）", got)
	}
}

// TestLoopContextTokensEstimatedWhenNoUsage：整轮没有任何上游用量数据时，
// 快照按 EstimateTokens 估算兜底——面板有数好过恒 0。
func TestLoopContextTokensEstimatedWhenNoUsage(t *testing.T) {
	srv := scriptServer(t, []string{textTurn("估算兜底")})
	defer srv.Close()

	snapshots := []TurnSnapshot{}
	_, err := Run(context.Background(), &Config{
		Client: NewClient(srv.URL),
		Model:  "claude-x",
		Hooks:  LoopHooks{OnTurn: func(snap TurnSnapshot) { snapshots = append(snapshots, snap) }},
	}, []Message{TextMessage("user", "这是一条足够长的用户消息，用来让估算值明显大于零而不是恰好等于零。")})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(snapshots) == 0 {
		t.Fatal("应有快照")
	}
	tail := snapshots[len(snapshots)-1]
	if tail.ContextTokens <= 0 {
		t.Fatalf("终态快照 ContextTokens = %d, 应为估算值 > 0", tail.ContextTokens)
	}
}

// 编译期引用 httptest 保持 import 稳定（scriptServer 内部已用）。
var _ = httptest.NewServer
