package agentcore

import (
	"context"
	"strings"
	"sync"
	"testing"
)

// 这组测试锁死 LoopHooks.OnToolCall 的语义：每个工具结果产出处触发一次
// （内置与桥接工具统一口径），宿主用它落 journal 工具轨迹（2026-10-28 spec
// §4.5——内置内核工具此前无任何宿主回调，MCP 桥/子 agent 桥各自有 onCall，
// 内置工具没有）。契约两条：
//  1. 一次带工具调用的 run → 钩子恰好一次，name/input/result 与调用一致；
//  2. 宿主回调 panic 不得影响工具结果（executeOne 已有全量 recover，这里的
//     独立 recover 是纵深防御——回调挂在锁外路径上时不许搅了已定的结果）。

// hookProbeTool 是最小工具桩：Execute 签名无 error（tools.go Tool 接口）。
type hookProbeTool struct{ name string }

func (t *hookProbeTool) Name() string        { return t.name }
func (t *hookProbeTool) Description() string { return "probe" }
func (t *hookProbeTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{"type": "object"}
}
func (t *hookProbeTool) Execute(_ context.Context, _ map[string]interface{}, _ *ToolEnv) ToolResult {
	return ToolResult{Content: "ok"}
}

// toolHookTurn1 / toolHookTurn2 是「调一次 probe-tool → 收尾」的两回合脚本
// （形状照抄 loop_steer_test.go 的 steerTurn1/steerTurn2）。
func toolHookTurn1() string {
	return `{"type":"message_start","message":{"id":"msg_th1","model":"claude-x"}}` + "\n\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_th1","name":"probe-tool"}}` + "\n\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"x\":\"hi\"}"}}` + "\n\n" +
		`data: {"type":"content_block_stop","index":0}` + "\n\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"}}`
}

func toolHookTurn2() string {
	return `{"type":"message_start","message":{"id":"msg_th2","model":"claude-x"}}` + "\n\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"完成"}}` + "\n\n" +
		`data: {"type":"content_block_stop","index":0}` + "\n\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"}}`
}

// TestOnToolCallHookFiresPerToolResult：一次带工具调用的 run，钩子恰好一次，
// name/input/result 与桩一致。
//
// Run 是同步的：钩子在 executeTools 里同步触发，Run 返回时必然已发生，
// 所以这里不需要 goroutine/chan——mutex 只为 race detector 的卫生（回调
// 理论上可能并发，锁住写入侧读侧就都不会有数据竞争）。
func TestOnToolCallHookFiresPerToolResult(t *testing.T) {
	rec := &requestRecorder{}
	srv := sseServer(t, rec.handler(t, []string{toolHookTurn1(), toolHookTurn2()}))

	registry := NewRegistry()
	registry.Register(&hookProbeTool{name: "probe-tool"})

	var mu sync.Mutex
	var calls int
	var gotName string
	var gotInput map[string]interface{}
	var gotResult ToolResult

	cfg := &Config{
		Client:    NewClient(srv.URL),
		Model:     "claude-x",
		SessionID: "sess-tool-hook",
		Tools:     registry,
		Logf:      func(string, ...interface{}) {},
		Hooks: LoopHooks{OnToolCall: func(name string, input map[string]interface{}, res ToolResult) {
			mu.Lock()
			calls++
			gotName = name
			gotInput = input
			gotResult = res
			mu.Unlock()
		}},
	}
	res, err := Run(context.Background(), cfg, []Message{TextMessage("user", "调一次工具")})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Turns != 2 {
		t.Fatalf("Turns = %d, want 2", res.Turns)
	}

	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Fatalf("OnToolCall 触发次数 = %d, want 1", calls)
	}
	if gotName != "probe-tool" {
		t.Errorf("OnToolCall name = %q, want %q", gotName, "probe-tool")
	}
	if v, _ := gotInput["x"].(string); v != "hi" {
		t.Errorf("OnToolCall input = %v, want x=hi", gotInput)
	}
	if gotResult.Content != "ok" || gotResult.IsError {
		t.Errorf("OnToolCall result = %+v, want Content=ok IsError=false", gotResult)
	}
}

// TestOnToolCallHookPanicDoesNotAffectToolResult：宿主回调 panic → 结果不受
// 搅动（与 MCP 桥 safeOnCall 同语义）。工具照常产出 "ok" 的 tool_result，
// 回合照常走完，进程不死。
func TestOnToolCallHookPanicDoesNotAffectToolResult(t *testing.T) {
	rec := &requestRecorder{}
	srv := sseServer(t, rec.handler(t, []string{toolHookTurn1(), toolHookTurn2()}))

	registry := NewRegistry()
	registry.Register(&hookProbeTool{name: "probe-tool"})

	cfg := &Config{
		Client:    NewClient(srv.URL),
		Model:     "claude-x",
		SessionID: "sess-tool-hook-panic",
		Tools:     registry,
		Logf:      func(string, ...interface{}) {},
		Hooks: LoopHooks{OnToolCall: func(string, map[string]interface{}, ToolResult) {
			panic("boom: 宿主 journal 回调炸了")
		}},
	}
	res, err := Run(context.Background(), cfg, []Message{TextMessage("user", "调一次工具")})
	if err != nil {
		t.Fatalf("Run 不应因钩子 panic 而失败: %v", err)
	}
	if res.Turns != 2 {
		t.Fatalf("Turns = %d, want 2", res.Turns)
	}

	// 第二轮请求体里的 tool_result 必须还是工具的正常结果，不是错误兜底。
	reqs := rec.snapshot()
	if len(reqs) != 2 {
		t.Fatalf("请求次数 = %d, want 2", len(reqs))
	}
	text := allToolResultText(t, reqs[1].Messages)
	if !strings.Contains(text, "ok") || strings.Contains(text, "panic") || strings.Contains(text, "内部错误") {
		t.Errorf("第二轮 tool_result 应为工具正常输出 %q，不应是错误兜底", text)
	}
}
