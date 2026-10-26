package agentcore

import (
	"context"
	"strings"
	"testing"
)

// 这组测试锁死 panic 兜底：内核跑在宿主进程内，审批桥、围栏、工具实现里任何
// 一个 panic 都等于整台设备下线（CLI provider 的同类崩溃只死子进程）。2026-10-26
// 线上实测：宿主审批策略对一条带注释尾段的 bash 命令 slice 越界 panic，容器整个
// 退出。契约：宿主回调 / 工具实现 panic 必须降级成失败的 tool_result，回合继续，
// 进程不死。

// panicTool 是 Execute 必炸的工具。
type panicTool struct{ stubTool }

func (p *panicTool) Execute(_ context.Context, _ map[string]interface{}, _ *ToolEnv) ToolResult {
	panic("boom: 工具实现炸了")
}

// panickingTurn1 / panickingTurn2 是「调一次工具 → 收尾」的两回合脚本。
func panickingTurn1() string {
	return `{"type":"message_start","message":{"id":"msg_p1","model":"claude-x"}}` + "\n\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_p1","name":"Boom"}}` + "\n\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"x\":\"hi\"}"}}` + "\n\n" +
		`data: {"type":"content_block_stop","index":0}` + "\n\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"}}`
}

func panickingTurn2() string {
	return `{"type":"message_start","message":{"id":"msg_p2","model":"claude-x"}}` + "\n\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"已处理错误"}}` + "\n\n" +
		`data: {"type":"content_block_stop","index":0}` + "\n\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"}}`
}

// TestExecuteOneRecoversPanickingTool：工具实现 panic → 失败 tool_result，回合照常走完。
func TestExecuteOneRecoversPanickingTool(t *testing.T) {
	rec := &requestRecorder{}
	srv := sseServer(t, rec.handler(t, []string{panickingTurn1(), panickingTurn2()}))

	registry := NewRegistry()
	registry.Register(&panicTool{stubTool{name: "Boom"}})

	cfg := &Config{
		Client:    NewClient(srv.URL),
		Model:     "claude-x",
		SessionID: "sess-panic-tool",
		Tools:     registry,
		Logf:      func(string, ...interface{}) {},
	}
	res, err := Run(context.Background(), cfg, []Message{TextMessage("user", "调一次工具")})
	if err != nil {
		t.Fatalf("Run 不应因工具 panic 而失败: %v", err)
	}
	if res.Turns != 2 {
		t.Fatalf("Turns = %d, want 2（panic 后模型应收到错误并继续）", res.Turns)
	}

	// 第二轮请求体里必须带着 panic 摘要的 tool_result——模型要能看到出错原因。
	reqs := rec.snapshot()
	if len(reqs) != 2 {
		t.Fatalf("请求次数 = %d, want 2", len(reqs))
	}
	text := allToolResultText(t, reqs[1].Messages)
	if !strings.Contains(text, "boom") {
		t.Errorf("第二轮请求应包含带 panic 摘要的 tool_result，实际 %q", text)
	}
}

// TestExecuteOneRecoversPanickingApprover：宿主审批回调 panic → 同样降级，
// 不带崩整个内核（这正是线上事故的形状：审批策略在工具执行前崩）。
func TestExecuteOneRecoversPanickingApprover(t *testing.T) {
	rec := &requestRecorder{}
	srv := sseServer(t, rec.handler(t, []string{panickingTurn1(), panickingTurn2()}))

	registry := NewRegistry()
	registry.Register(&stubTool{name: "Boom", result: ToolResult{Content: "ok"}})

	cfg := &Config{
		Client:    NewClient(srv.URL),
		Model:     "claude-x",
		SessionID: "sess-panic-approve",
		Tools:     registry,
		Logf:      func(string, ...interface{}) {},
		Approve: func(_ context.Context, _ ApprovalRequest) (ApprovalResponse, error) {
			panic("boom: 审批桥炸了")
		},
	}
	res, err := Run(context.Background(), cfg, []Message{TextMessage("user", "调一次工具")})
	if err != nil {
		t.Fatalf("Run 不应因审批 panic 而失败: %v", err)
	}
	if res.Turns != 2 {
		t.Fatalf("Turns = %d, want 2", res.Turns)
	}

	reqs := rec.snapshot()
	text := allToolResultText(t, reqs[1].Messages)
	if !strings.Contains(text, "boom") {
		t.Errorf("第二轮请求应包含带 panic 摘要的 tool_result，实际 %q", text)
	}
}

// allToolResultText 拼接整份消息列表里所有 tool_result 的文本，不假设位置。
func allToolResultText(t *testing.T, msgs []Message) string {
	t.Helper()
	var b strings.Builder
	for _, m := range msgs {
		b.WriteString(toolResultText(t, m))
	}
	return b.String()
}
