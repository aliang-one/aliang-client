package agentcore

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// 这一组锁死 max_tokens 截断的**恢复**语义。
//
// 背景（真机复现）：手机让内核写一个 3000 行的文件，模型的 tool_use 参数写到
// 一半就撞上 8192 输出上限，stop_reason=max_tokens。截断守卫把这批工具调用
// 全判失败是对的（半个 JSON 执行下去会写坏文件），但接着就 return 终止整轮
// 是错的——回填给模型的那句「请拆小后重试」它根本没机会读到，用户只看到
// 「模型输出被 max_tokens 截断」，任务死在这里。
//
// pi 的行为是：失败这批 tool_use、把失败结果回填、**循环继续**，让模型自己
// 分块重写。这组测试钉住这一点，外加「连续打转要有上限」这个护栏。

// truncatedTurn 是一个被截断的工具回合：partial_json 故意不闭合。
func truncatedTurn(id string) string {
	return `data: {"type":"message_start","message":{"id":"msg_t1","model":"claude-x"}}` + "\n\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"` + id + `","name":"Stub"}}` + "\n\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"x\":\"unterminated"}}` + "\n\n" +
		`data: {"type":"content_block_stop","index":0}` + "\n\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"max_tokens"}}`
}

// recoveredTurn 是模型看到「请拆小后重试」之后正常收尾。
func recoveredTurn(text string) string {
	return `data: {"type":"message_start","message":{"id":"msg_t2","model":"claude-x"}}` + "\n\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"` + text + `"}}` + "\n\n" +
		`data: {"type":"content_block_stop","index":0}` + "\n\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"}}`
}

// TestRunRecoversFromTruncation 是主用例：截断之后 run 必须继续，模型能拿到
// 失败回填，整轮以成功收尾（而不是 error）。
func TestRunRecoversFromTruncation(t *testing.T) {
	rec := &requestRecorder{}
	srv := sseServer(t, rec.handler(t, []string{
		truncatedTurn("toolu_trunc"),
		recoveredTurn("已拆小重写"),
	}))

	stub := &stubTool{name: "Stub", result: ToolResult{Content: "ok"}}
	registry := NewRegistry()
	registry.Register(stub)

	cfg := &Config{
		Client:    NewClient(srv.URL),
		Model:     "claude-x",
		SessionID: "sess-trunc",
		Tools:     registry,
	}
	res, err := Run(context.Background(), cfg, []Message{TextMessage("user", "写个 3000 行的文件")})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.IsError {
		t.Fatalf("截断后应恢复成正常收尾，实际 IsError=true（错误=%q）", res.Text)
	}
	if res.Turns != 2 {
		t.Errorf("Turns = %d, want 2（第一轮截断 + 第二轮恢复）", res.Turns)
	}
	if got := atomic.LoadInt32(&stub.calls); got != 0 {
		t.Errorf("被截断的工具执行了 %d 次，应为 0（参数不完整，绝不能跑）", got)
	}
	if !strings.Contains(res.Text, "已拆小重写") {
		t.Errorf("最终文本 = %q, want 含恢复轮的回复", res.Text)
	}

	reqs := rec.snapshot()
	if len(reqs) != 2 {
		t.Fatalf("请求次数 = %d, want 2", len(reqs))
	}
	// 第二次请求必须带着失败回填，且排在最后一条 user 消息里——否则模型
	// 根本不知道上一轮为什么没执行。
	last := reqs[1].Messages[len(reqs[1].Messages)-1]
	if last.Role != "user" {
		t.Fatalf("最后一次消息角色 = %q, want user（tool_result 回填）", last.Role)
	}
	if got := toolResultText(t, last); !strings.Contains(got, "请拆小后重试") {
		t.Errorf("回填文本 = %q, want 含「请拆小后重试」", got)
	}
}

// TestRunGivesUpAfterRepeatedTruncation 是护栏：连续截断超过上限必须终止，
// 不能无限重试把 token 烧穿。
func TestRunGivesUpAfterRepeatedTruncation(t *testing.T) {
	rec := &requestRecorder{}
	turns := make([]string, 0, maxTruncationRetries+2)
	for i := 0; i < maxTruncationRetries+2; i++ {
		turns = append(turns, truncatedTurn("toolu_t"))
	}
	srv := sseServer(t, rec.handler(t, turns))

	registry := NewRegistry()
	registry.Register(&stubTool{name: "Stub", result: ToolResult{Content: "ok"}})

	recFrames := &frameRecorder{}
	cfg := &Config{
		Client:    NewClient(srv.URL),
		Model:     "claude-x",
		SessionID: "sess-trunc-loop",
		Tools:     registry,
		Emit:      recFrames.emit,
	}
	res, err := Run(context.Background(), cfg, []Message{TextMessage("user", "一直写大文件")})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.IsError {
		t.Fatal("连续截断到底应终止为错误，实际 IsError=false")
	}
	if res.Turns != maxTruncationRetries+1 {
		t.Errorf("Turns = %d, want %d（上限一到就停，不再多试）", res.Turns, maxTruncationRetries+1)
	}
	if got := len(rec.snapshot()); got != maxTruncationRetries+1 {
		t.Errorf("请求次数 = %d, want %d", got, maxTruncationRetries+1)
	}

	// 终止原因走 result 帧（res.Text 只装 assistant 文本，截断轮没有文本）。
	rf := resultFrame(t, recFrames.frames(t))
	if isErr, _ := rf["is_error"].(bool); !isErr {
		t.Error("连续截断终止时 is_error 必须为 true，否则宿主不会转成 ai.error")
	}
	if sub, _ := rf["subtype"].(string); !strings.HasPrefix(sub, "error_") {
		t.Errorf("失败 subtype 必须以 error_ 开头，实际 %q", sub)
	}
	if text, _ := rf["result"].(string); !strings.Contains(text, "连续") {
		t.Errorf("result 文本 = %q，应说明是连续截断", text)
	}
}

// TestRunTruncationLogsDecision 钉住截断这件事**可观测**：截断没有自己的
// 事件帧（回填只是一条 tool_result），不打日志的话事后只能靠模型回复反推。
// 真机排查过一次，代价很高。
func TestRunTruncationLogsDecision(t *testing.T) {
	rec := &requestRecorder{}
	srv := sseServer(t, rec.handler(t, []string{
		truncatedTurn("toolu_log"),
		recoveredTurn("ok"),
	}))

	registry := NewRegistry()
	registry.Register(&stubTool{name: "Stub", result: ToolResult{Content: "ok"}})

	var mu sync.Mutex
	var logs []string
	cfg := &Config{
		Client:    NewClient(srv.URL),
		Model:     "claude-x",
		SessionID: "sess-trunc-log",
		Tools:     registry,
		Logf: func(format string, args ...interface{}) {
			mu.Lock()
			logs = append(logs, fmt.Sprintf(format, args...))
			mu.Unlock()
		},
	}
	if _, err := Run(context.Background(), cfg, []Message{TextMessage("user", "写大文件")}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(logs) == 0 {
		t.Fatal("截断没有打日志：事后无法从日志还原发生过什么")
	}
	joined := strings.Join(logs, "\n")
	if !strings.Contains(joined, "max_tokens") || !strings.Contains(joined, "sess-trunc-log") {
		t.Errorf("日志缺少关键信息（max_tokens / session id），实际：%q", joined)
	}
}

// TestRunTruncationStreakResetsAfterSuccess 钉住「连续」的语义：中间只要成功
// 一轮，计数就清零。否则一个长任务里零星几次截断会累计到上限、误杀。
func TestRunTruncationStreakResetsAfterSuccess(t *testing.T) {
	rec := &requestRecorder{}
	// 截断 → 成功工具回合 → 截断 → 成功收尾。两处截断不连续，都该被容忍。
	turns := []string{
		truncatedTurn("toolu_a"),
		steerTurn1(),
		truncatedTurn("toolu_b"),
		recoveredTurn("done"),
	}
	srv := sseServer(t, rec.handler(t, turns))

	registry := NewRegistry()
	registry.Register(&stubTool{name: "Stub", result: ToolResult{Content: "ok"}})

	cfg := &Config{
		Client:    NewClient(srv.URL),
		Model:     "claude-x",
		SessionID: "sess-trunc-reset",
		Tools:     registry,
	}
	res, err := Run(context.Background(), cfg, []Message{TextMessage("user", "长任务")})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.IsError {
		t.Fatalf("不连续的两次截断不该终止，实际 IsError=true（错误=%q）", res.Text)
	}
	if res.Turns != 4 {
		t.Errorf("Turns = %d, want 4", res.Turns)
	}
}
