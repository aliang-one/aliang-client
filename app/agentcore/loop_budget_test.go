package agentcore

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// 这一组锁死「回合预算家族」的语义（见 loop.go 顶部注释）：三个无进展守卫
// 必须先于 maxIterations 触发，且**每个出口都走 stopWithSummary**——用户拿到
// 的是「干到哪了 + 为什么停」，不是一句裸错误。

// toolTurn 是一个完整的工具调用回合 SSE（参数合法，会真实执行）。
// 帧形态沿用 turnScript 的约定：writeSSE 给首帧补 data: 前缀，后续帧自带。
// input 按调用方便利传入裸 JSON（如 {"x":"a"}），嵌入时转义引号——partial_json
// 是字符串字段，裸嵌会造出畸形帧。
func toolTurn(id, name, input string) string {
	escaped := strings.ReplaceAll(input, `"`, `\"`)
	return `{"type":"message_start","message":{"id":"msg_b1","model":"claude-x"}}` + "\n\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"` + id + `","name":"` + name + `"}}` + "\n\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"` + escaped + `"}}` + "\n\n" +
		`data: {"type":"content_block_stop","index":0}` + "\n\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"}}`
}

// textTurn 是一个纯文本收尾回合 SSE。
func textTurn(text string) string {
	return `{"type":"message_start","message":{"id":"msg_b2","model":"claude-x"}}` + "\n\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"` + text + `"}}` + "\n\n" +
		`data: {"type":"content_block_stop","index":0}` + "\n\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"}}`
}

// scriptServer 按请求次序回放 SSE 脚本。
func scriptServer(t *testing.T, turns []string) *httptest.Server {
	t.Helper()
	return sseServer(t, (&turnScript{turns: turns}).handler(t))
}

// TestLoopStopsOnRepeatedIdenticalToolCalls：连续 3 轮一模一样的工具调用
// 必须停。模型自己不会承认在打转，等 maxIterations 兜底要再烧 47 轮。
func TestLoopStopsOnRepeatedIdenticalToolCalls(t *testing.T) {
	turns := make([]string, 0, maxIdenticalToolTurns+2)
	for i := 0; i < maxIdenticalToolTurns+2; i++ {
		turns = append(turns, toolTurn("toolu_dup", "Stub", `{"x":"always-the-same"}`))
	}
	srv := scriptServer(t, turns)
	defer srv.Close()

	logs := []string{}
	stub := &stubTool{name: "Stub", result: ToolResult{Content: "ok"}}
	registry := NewRegistry()
	registry.Register(stub)

	rec := &frameRecorder{}
	res, err := Run(context.Background(), &Config{
		Client: NewClient(srv.URL),
		Model:  "claude-x",
		Tools:  registry,
		Emit:   rec.emit,
		Logf: func(format string, args ...interface{}) {
			logs = append(logs, fmt.Sprintf(format, args...))
		},
	}, []Message{TextMessage("user", "重复干活")})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.IsError {
		t.Fatalf("无进展必须以终止帧收场，实际文本 %q", res.Text)
	}
	if res.Turns != maxIdenticalToolTurns {
		t.Errorf("Turns = %d, want %d（第 3 轮就停，不烧到上限）", res.Turns, maxIdenticalToolTurns)
	}
	// 终止原因写在 result 帧里（res.Text 是整轮文本累计，模型全程没说过话
	// 时它是空的）——手机错误卡片渲染的正是帧里的这个字段。
	resultFrame := lastResultFrame(t, rec)
	if got, _ := resultFrame["result"].(string); !strings.Contains(got, "无进展") {
		t.Errorf("终止原因应点名无进展，实际 %q", got)
	}
	// 守卫在第 3 轮执行工具**之前**触发：只执行前 2 轮的调用。
	if got := atomic.LoadInt32(&stub.calls); got != int32(maxIdenticalToolTurns-1) {
		t.Errorf("工具执行了 %d 次, want %d", got, maxIdenticalToolTurns-1)
	}
	if joined := strings.Join(logs, "\n"); !strings.Contains(joined, "完全相同的工具调用") {
		t.Errorf("应有观测日志，实际 %s", joined)
	}
}

// lastResultFrame 取 Emit 流里的最后一个 result 帧（测试辅助）。
func lastResultFrame(t *testing.T, rec *frameRecorder) map[string]interface{} {
	t.Helper()
	frames := rec.frames(t)
	for i := len(frames) - 1; i >= 0; i-- {
		if frames[i]["type"] == "result" {
			return frames[i]
		}
	}
	t.Fatal("Emit 流里没有 result 帧")
	return nil
}

// TestLoopContinuesWhenToolCallsDiffer：签名不同（参数在变）不算打转。
// 参数每轮变化的同工具调用是正常工作形态，守卫不许误伤。
func TestLoopContinuesWhenToolCallsDiffer(t *testing.T) {
	turns := make([]string, 0, maxIdenticalToolTurns+1)
	for i := 0; i < maxIdenticalToolTurns+1; i++ {
		turns = append(turns, toolTurn("toolu_var", "Stub", fmt.Sprintf(`{"x":"round-%d"}`, i)))
	}
	turns = append(turns, textTurn("干完了"))
	srv := scriptServer(t, turns)
	defer srv.Close()

	registry := NewRegistry()
	registry.Register(&stubTool{name: "Stub", result: ToolResult{Content: "ok"}})

	res, err := Run(context.Background(), &Config{
		Client: NewClient(srv.URL),
		Model:  "claude-x",
		Tools:  registry,
	}, []Message{TextMessage("user", "每轮换个参数")})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.IsError {
		t.Fatalf("参数变化的调用不该被判打转: %q", res.Text)
	}
	if !strings.Contains(res.Text, "干完了") {
		t.Errorf("应正常收尾，实际 %q", res.Text)
	}
}

// TestLoopStopsOnConsecutiveToolErrors：连续 5 个工具结果都是错误必须停。
// 参数每轮变化（不触发无进展守卫），但结果永远是错误。
func TestLoopStopsOnConsecutiveToolErrors(t *testing.T) {
	turns := make([]string, 0, maxConsecutiveToolErrors+2)
	for i := 0; i < maxConsecutiveToolErrors+2; i++ {
		turns = append(turns, toolTurn("toolu_err", "Stub", fmt.Sprintf(`{"x":"try-%d"}`, i)))
	}
	srv := scriptServer(t, turns)
	defer srv.Close()

	registry := NewRegistry()
	registry.Register(&stubTool{name: "Stub", result: ToolResult{
		Content: "文件不存在", IsError: true,
	}})

	rec := &frameRecorder{}
	res, err := Run(context.Background(), &Config{
		Client: NewClient(srv.URL),
		Model:  "claude-x",
		Tools:  registry,
		Emit:   rec.emit,
	}, []Message{TextMessage("user", "一直失败")})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.IsError {
		t.Fatal("连续错误必须以终止帧收场")
	}
	resultFrame := lastResultFrame(t, rec)
	if got, _ := resultFrame["result"].(string); !strings.Contains(got, "连续") || !strings.Contains(got, "失败") {
		t.Errorf("终止原因应点名连续失败，实际 %q", got)
	}
	if res.Turns != maxConsecutiveToolErrors {
		t.Errorf("Turns = %d, want %d", res.Turns, maxConsecutiveToolErrors)
	}
}

// TestLoopErrStreakResetsOnSuccess：中间有成功就把连续错误清零——守卫防的是
// 螺旋，不是累计失败次数（真实任务里失败几次再成功太常见了）。
func TestLoopErrStreakResetsOnSuccess(t *testing.T) {
	// 错×4 → 成×1 交替，连续计数永远到不了阈值，必须能跑满 12 个工具轮后
	// 正常收尾。
	turns := make([]string, 0, 13)
	for i := 0; i < 12; i++ {
		turns = append(turns, toolTurn("toolu_mix", "Stub", fmt.Sprintf(`{"x":"mix-%d"}`, i)))
	}
	turns = append(turns, textTurn("收工"))
	srv := scriptServer(t, turns)
	defer srv.Close()

	registry := NewRegistry()
	registry.Register(&stubTool{name: "Stub", result: ToolResult{Content: "ok"}})

	res, err := Run(context.Background(), &Config{
		Client: NewClient(srv.URL),
		Model:  "claude-x",
		Tools:  registry,
	}, []Message{TextMessage("user", "混合成败")})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.IsError {
		t.Fatalf("交错成败不该触发守卫: %q", res.Text)
	}
	if res.Turns != 13 {
		t.Errorf("Turns = %d, want 13（12 个工具轮 + 1 个收尾轮）", res.Turns)
	}
}

// TestLoopStopsOnWallClockBudget：墙钟预算。直构造 runner（startedAt 倒填、
// wallBudget 缩短）让第一轮就超预算。
func TestLoopStopsOnWallClockBudget(t *testing.T) {
	srv := scriptServer(t, []string{toolTurn("toolu_wall", "Stub", `{"x":"late"}`), textTurn("来不及了")})
	defer srv.Close()

	logs := []string{}
	registry := NewRegistry()
	registry.Register(&stubTool{name: "Stub", result: ToolResult{Content: "ok"}})

	rec := &frameRecorder{}
	// Emit 必须在构造 writer 之前就位——newStreamWriter 在构造时捕获它。
	r := &runner{
		cfg: &Config{
			Client: NewClient(srv.URL),
			Model:  "claude-x",
			Tools:  registry,
			Emit:   rec.emit,
			Logf: func(format string, args ...interface{}) {
				logs = append(logs, fmt.Sprintf(format, args...))
			},
		},
		history:    []Message{TextMessage("user", "跑很久")},
		writer:     newStreamWriter(rec.emit, "sess-wall"),
		startedAt:  time.Now().Add(-time.Hour), // 一小时前开始
		wallBudget: time.Minute,                // 预算 1 分钟 → 第一轮就超
	}
	res, err := r.loop(context.Background())
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	if !res.IsError {
		t.Fatal("超墙钟预算必须以终止帧收场")
	}
	resultFrame := lastResultFrame(t, rec)
	if got, _ := resultFrame["result"].(string); !strings.Contains(got, "时间预算") {
		t.Errorf("终止原因应点名时间预算，实际 %q", got)
	}
	if joined := strings.Join(logs, "\n"); !strings.Contains(joined, "时间预算") {
		t.Errorf("应有观测日志，实际 %s", joined)
	}
}

// TestToolCallSignature：签名的可比性——同参数同签名（id 不参与），参数或
// 工具不同即不同。map 序列化按键排序，这是稳定性的来源。
func TestToolCallSignature(t *testing.T) {
	a := []ContentBlock{toolUseBlock("1", "read", map[string]interface{}{"path": "a.go"})}
	b := []ContentBlock{toolUseBlock("2", "read", map[string]interface{}{"path": "a.go"})}
	c := []ContentBlock{toolUseBlock("3", "read", map[string]interface{}{"path": "b.go"})}
	d := []ContentBlock{toolUseBlock("4", "bash", map[string]interface{}{"cmd": "ls"})}

	if toolCallSignature(a) != toolCallSignature(b) {
		t.Error("同工具同参数（id 不同）应同签名")
	}
	if toolCallSignature(a) == toolCallSignature(c) {
		t.Error("参数不同应不同签名")
	}
	if toolCallSignature(a) == toolCallSignature(d) {
		t.Error("工具不同应不同签名")
	}
}

// toolUseBlock 造一个 tool_use 内容块（测试辅助）。
func toolUseBlock(id, name string, input map[string]interface{}) ContentBlock {
	return ContentBlock{Type: BlockTypeToolUse, ID: id, Name: name, Input: input}
}
