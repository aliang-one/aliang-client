package agentcore

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

// 这一组测试锁死内核 → 宿主的帧契约。
//
// 宿主 app/http/services 的 streamStructuredAIDelta 只认 Claude stream-json，
// 任何一帧的 type/subtype/字段形状变了，手机端的 delta、工具卡、终止收敛都会
// 静默坏掉，而且不会报错。所以这里断言的是**精确的帧序列**，不是「包含某帧」。

// frameRecorder 收集 Emit 写出的帧。
type frameRecorder struct {
	raw strings.Builder
}

func (r *frameRecorder) emit(frame []byte) error {
	r.raw.Write(frame)
	return nil
}

// frames 把收集到的原始字节按行解析成帧。
func (r *frameRecorder) frames(t *testing.T) []map[string]interface{} {
	t.Helper()
	return parseFrames(t, r.raw.String())
}

// parseFrames 按行解析 Emit 的输出。每行必须是一个完整的 JSON 对象。
func parseFrames(t *testing.T, raw string) []map[string]interface{} {
	t.Helper()
	var out []map[string]interface{}
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var f map[string]interface{}
		if err := json.Unmarshal([]byte(line), &f); err != nil {
			t.Fatalf("帧不是合法 JSON: %v\n原始行: %s", err, line)
		}
		out = append(out, f)
	}
	return out
}

// frameKinds 把帧压成 "type" 或 "type:subtype" / "stream_event:eventType" 的
// 紧凑标签，便于整体比对序列。
func frameKinds(frames []map[string]interface{}) []string {
	out := make([]string, 0, len(frames))
	for _, f := range frames {
		typ, _ := f["type"].(string)
		switch typ {
		case "stream_event":
			ev, _ := f["event"].(map[string]interface{})
			evType, _ := ev["type"].(string)
			out = append(out, "stream_event:"+evType)
		case "system":
			sub, _ := f["subtype"].(string)
			out = append(out, "system:"+sub)
		default:
			out = append(out, typ)
		}
	}
	return out
}

// assertSessionIDOnEveryFrame 校验每帧都带 session_id。
//
// 宿主从任意一帧捕获 session_id 回填 resume 句柄，漏一帧就可能让对应事件
// 落不进会话。
func assertSessionIDOnEveryFrame(t *testing.T, frames []map[string]interface{}, want string) {
	t.Helper()
	for i, f := range frames {
		got, _ := f["session_id"].(string)
		if got != want {
			t.Errorf("第 %d 帧 session_id = %q, want %q（帧: %v）", i, got, want, f)
		}
	}
}

// assistantFrame 取最后一个 assistant 帧。
func assistantFrame(t *testing.T, frames []map[string]interface{}) map[string]interface{} {
	t.Helper()
	for i := len(frames) - 1; i >= 0; i-- {
		if typ, _ := frames[i]["type"].(string); typ == "assistant" {
			return frames[i]
		}
	}
	t.Fatal("没有找到 assistant 帧")
	return nil
}

// userFrame 取最后一个 user 帧（tool_result 回填）。
func userFrame(t *testing.T, frames []map[string]interface{}) map[string]interface{} {
	t.Helper()
	for i := len(frames) - 1; i >= 0; i-- {
		if typ, _ := frames[i]["type"].(string); typ == "user" {
			return frames[i]
		}
	}
	t.Fatal("没有找到 user 帧")
	return nil
}

// resultFrame 取最后一个 result 帧。
func resultFrame(t *testing.T, frames []map[string]interface{}) map[string]interface{} {
	t.Helper()
	for i := len(frames) - 1; i >= 0; i-- {
		if typ, _ := frames[i]["type"].(string); typ == "result" {
			return frames[i]
		}
	}
	t.Fatal("没有找到 result 帧")
	return nil
}

// messageContent 取 assistant/user 帧里的 message.content 数组。
func messageContent(t *testing.T, frame map[string]interface{}) []interface{} {
	t.Helper()
	msg, ok := frame["message"].(map[string]interface{})
	if !ok {
		t.Fatalf("帧缺少 message 对象: %v", frame)
	}
	content, ok := msg["content"].([]interface{})
	if !ok {
		t.Fatalf("message.content 不是数组: %v", msg["content"])
	}
	return content
}

// stubTool 是测试用的可计数工具。
type stubTool struct {
	name     string
	result   ToolResult
	calls    int32
	lastArgs map[string]interface{}
}

func (s *stubTool) Name() string        { return s.name }
func (s *stubTool) Description() string { return "测试用工具" }
func (s *stubTool) InputSchema() map[string]interface{} {
	return objectSchema(map[string]interface{}{"x": stringProp("参数 x")})
}
func (s *stubTool) Execute(_ context.Context, input map[string]interface{}, _ *ToolEnv) ToolResult {
	atomic.AddInt32(&s.calls, 1)
	s.lastArgs = input
	return s.result
}

// turnScript 按第几次请求返回不同的 SSE 帧序列。
type turnScript struct {
	turns []string
	n     int32
}

func (s *turnScript) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		idx := int(atomic.AddInt32(&s.n, 1)) - 1
		if idx >= len(s.turns) {
			// 超出脚本范围时给一个干净的收尾，避免测试挂死。
			writeSSE(w, `{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`)
			return
		}
		writeSSE(w, s.turns[idx])
	}
}

// ---------------------------------------------------------------------------
// 纯文本回合
// ---------------------------------------------------------------------------

func TestRunTextOnlyFrameSequence(t *testing.T) {
	script := &turnScript{turns: []string{
		`{"type":"message_start","message":{"id":"msg_txt","model":"claude-x"}}` + "\n\n" +
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"你好"}}` + "\n\n" +
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"，世界"}}` + "\n\n" +
			`data: {"type":"content_block_stop","index":0}` + "\n\n" +
			`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":9}}`,
	}}
	srv := sseServer(t, script.handler(t))

	rec := &frameRecorder{}
	cfg := &Config{
		Client:    NewClient(srv.URL),
		Model:     "claude-x",
		Emit:      rec.emit,
		SessionID: "sess-text",
	}
	res, err := Run(context.Background(), cfg, []Message{TextMessage("user", "hi")})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Text != "你好，世界" {
		t.Errorf("RunResult.Text = %q, want 你好，世界", res.Text)
	}
	if res.Turns != 1 {
		t.Errorf("Turns = %d, want 1", res.Turns)
	}
	if res.StopReason != StopReasonEndTurn {
		t.Errorf("StopReason = %q, want end_turn", res.StopReason)
	}

	frames := rec.frames(t)
	got := frameKinds(frames)
	want := []string{
		"system:init",
		"stream_event:message_start",
		"stream_event:content_block_start",
		"stream_event:content_block_delta",
		"stream_event:content_block_delta",
		"stream_event:content_block_stop",
		"assistant",
		"result",
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("帧序列不符\n got: %v\nwant: %v", got, want)
	}

	assertSessionIDOnEveryFrame(t, frames, "sess-text")

	// system/init 的形状：宿主 onClaudeInit 靠它初始化会话状态。
	init := frames[0]
	if v, _ := init["claude_code_version"].(string); v != "agentcore/1" {
		t.Errorf("claude_code_version = %q, want agentcore/1", v)
	}
	if _, ok := init["slash_commands"].([]interface{}); !ok {
		t.Errorf("slash_commands 必须是数组（不能是 nil），实际 %v", init["slash_commands"])
	}

	// message_start 的 model 必须是真的模型名，不能是 <synthetic>——宿主
	// extractClaudeStreamTexts 会把 <synthetic> 当失败吞掉。
	startEv, _ := frames[1]["event"].(map[string]interface{})
	startMsg, _ := startEv["message"].(map[string]interface{})
	if m, _ := startMsg["model"].(string); m != "claude-x" {
		t.Errorf("message_start.model = %q, want claude-x", m)
	}
	if id, _ := startMsg["id"].(string); id != "msg_txt" {
		t.Errorf("message_start.id = %q, want msg_txt", id)
	}

	// assistant 帧的 content 必须是文本块，且文本完整。
	content := messageContent(t, assistantFrame(t, frames))
	if len(content) != 1 {
		t.Fatalf("assistant content 长度 = %d, want 1", len(content))
	}
	block, _ := content[0].(map[string]interface{})
	if bt, _ := block["type"].(string); bt != BlockTypeText {
		t.Errorf("content[0].type = %q, want text", bt)
	}
	if txt, _ := block["text"].(string); txt != "你好，世界" {
		t.Errorf("content[0].text = %q, want 你好，世界", txt)
	}

	// result 帧：成功终止，is_error 必须是 false。
	res1 := resultFrame(t, frames)
	if sub, _ := res1["subtype"].(string); sub != ResultSubtypeSuccess {
		t.Errorf("result.subtype = %q, want success", sub)
	}
	if isErr, _ := res1["is_error"].(bool); isErr {
		t.Error("成功终止时 is_error 必须为 false")
	}
	if txt, _ := res1["result"].(string); txt != "你好，世界" {
		t.Errorf("result.result = %q, want 你好，世界", txt)
	}
	if n, _ := res1["num_turns"].(float64); int(n) != 1 {
		t.Errorf("result.num_turns = %v, want 1", res1["num_turns"])
	}
}

// ---------------------------------------------------------------------------
// 思考块（thinking）
// ---------------------------------------------------------------------------

// TestRunThinkingFrameSequence 锁内核的思考流契约：上游 thinking 块逐段透传
// 成 content_block_start/delta/stop（宿主翻成 ai.thinking 并 markProgress，
// 纯推理期间这是服务层唯一活性信号）；signature_delta 丢弃不发帧；思考内容
// 不进 blocks——finalized assistant 帧与 result 正文只有 text。
func TestRunThinkingFrameSequence(t *testing.T) {
	script := &turnScript{turns: []string{
		`{"type":"message_start","message":{"id":"msg_think","model":"claude-x"}}` + "\n\n" +
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}` + "\n\n" +
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"先想"}}` + "\n\n" +
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig-bytes"}}` + "\n\n" +
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"再答"}}` + "\n\n" +
			`data: {"type":"content_block_stop","index":0}` + "\n\n" +
			`data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}` + "\n\n" +
			`data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"结论"}}` + "\n\n" +
			`data: {"type":"content_block_stop","index":1}` + "\n\n" +
			`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":7}}`,
	}}
	srv := sseServer(t, script.handler(t))

	rec := &frameRecorder{}
	cfg := &Config{
		Client:    NewClient(srv.URL),
		Model:     "claude-x",
		Emit:      rec.emit,
		SessionID: "sess-think",
	}
	res, err := Run(context.Background(), cfg, []Message{TextMessage("user", "hi")})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Text != "结论" {
		t.Errorf("RunResult.Text = %q, want 结论（思考内容不得进正文）", res.Text)
	}

	frames := rec.frames(t)
	got := frameKinds(frames)
	want := []string{
		"system:init",
		"stream_event:message_start",
		"stream_event:content_block_start", // thinking 块 start（第一段 delta 触发）
		"stream_event:content_block_delta", // thinking_delta
		"stream_event:content_block_delta", // thinking_delta；signature_delta 不产生帧
		"stream_event:content_block_stop",
		"stream_event:content_block_start", // text 块
		"stream_event:content_block_delta",
		"stream_event:content_block_stop",
		"assistant",
		"result",
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("帧序列不符\n got: %v\nwant: %v", got, want)
	}

	assertSessionIDOnEveryFrame(t, frames, "sess-think")

	// thinking start 帧的形状：type=thinking，宿主 emitClaudeStructuredEvents
	// 靠它登记块类型，stop 时才补得出 active:false。
	startEv, _ := frames[2]["event"].(map[string]interface{})
	if cb, _ := startEv["content_block"].(map[string]interface{}); cb["type"] != BlockTypeThinking {
		t.Errorf("content_block.type = %v, want thinking", cb["type"])
	}
	// thinking delta 帧的形状：delta.type=thinking_delta、字段名 thinking，
	// 宿主只认这一种（与 CLI 路径同帧，不造第二种 thinking 帧）。
	for i, wantText := range map[int]string{3: "先想", 4: "再答"} {
		ev, _ := frames[i]["event"].(map[string]interface{})
		delta, _ := ev["delta"].(map[string]interface{})
		if delta["type"] != "thinking_delta" {
			t.Errorf("第 %d 帧 delta.type = %v, want thinking_delta", i, delta["type"])
		}
		if delta["thinking"] != wantText {
			t.Errorf("第 %d 帧 delta.thinking = %v, want %q", i, delta["thinking"], wantText)
		}
	}

	// finalized assistant 帧：content 只有 text 块——宿主只持久化这一帧的
	// text，思考块混进去会污染 transcript。
	content := messageContent(t, assistantFrame(t, frames))
	if len(content) != 1 {
		t.Fatalf("assistant content 长度 = %d, want 1（只有 text 块，无 thinking）", len(content))
	}
	block, _ := content[0].(map[string]interface{})
	if bt, _ := block["type"].(string); bt != BlockTypeText {
		t.Errorf("content[0].type = %q, want text", bt)
	}
	if txt, _ := block["text"].(string); txt != "结论" {
		t.Errorf("content[0].text = %q, want 结论", txt)
	}
	if txt, _ := resultFrame(t, frames)["result"].(string); txt != "结论" {
		t.Errorf("result.result = %q, want 结论", txt)
	}
}

// ---------------------------------------------------------------------------
// 工具回合
// ---------------------------------------------------------------------------

func TestRunToolLoopFrameSequence(t *testing.T) {
	turn1 := `{"type":"message_start","message":{"id":"msg_t1","model":"claude-x"}}` + "\n\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"Stub"}}` + "\n\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"x\":"}}` + "\n\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"hi\"}"}}` + "\n\n" +
		`data: {"type":"content_block_stop","index":0}` + "\n\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"}}`
	turn2 := `{"type":"message_start","message":{"id":"msg_t2","model":"claude-x"}}` + "\n\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"完成"}}` + "\n\n" +
		`data: {"type":"content_block_stop","index":0}` + "\n\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"}}`

	srv := sseServer(t, (&turnScript{turns: []string{turn1, turn2}}).handler(t))

	tool := &stubTool{name: "Stub", result: ToolResult{Content: "工具执行结果"}}
	registry := NewRegistry()
	registry.Register(tool)

	var finishTexts []string
	rec := &frameRecorder{}
	cfg := &Config{
		Client:    NewClient(srv.URL),
		Model:     "claude-x",
		Tools:     registry,
		Emit:      rec.emit,
		SessionID: "sess-tool",
		Hooks: LoopHooks{
			FinishTurn: func(tr TurnResult) {
				finishTexts = append(finishTexts, tr.Text)
			},
		},
	}

	res, err := Run(context.Background(), cfg, []Message{TextMessage("user", "干活")})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if n := atomic.LoadInt32(&tool.calls); n != 1 {
		t.Fatalf("工具调用次数 = %d, want 1", n)
	}
	if got := tool.lastArgs["x"]; got != "hi" {
		t.Errorf("工具参数 x = %v, want hi", got)
	}
	if res.Turns != 2 {
		t.Errorf("Turns = %d, want 2", res.Turns)
	}

	frames := rec.frames(t)
	got := frameKinds(frames)
	want := []string{
		"system:init",
		// 回合 1：工具
		"stream_event:message_start",
		"stream_event:content_block_start",
		"stream_event:content_block_delta",
		"stream_event:content_block_delta",
		"stream_event:content_block_stop",
		"assistant",
		"user",
		// 回合 2：文本收尾
		"stream_event:message_start",
		"stream_event:content_block_start",
		"stream_event:content_block_delta",
		"stream_event:content_block_stop",
		"assistant",
		"result",
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("帧序列不符\n got: %v\nwant: %v", got, want)
	}
	assertSessionIDOnEveryFrame(t, frames, "sess-tool")

	// 回合 1 的 assistant 帧必须带完整 tool_use 块（id/name/input）——
	// 这是宿主 updateClaudeToolUseActivity 登记待办工具的唯一入口。
	firstAssistant := frames[6]
	content := messageContent(t, firstAssistant)
	if len(content) != 1 {
		t.Fatalf("回合 1 assistant content 长度 = %d, want 1", len(content))
	}
	tu, _ := content[0].(map[string]interface{})
	if bt, _ := tu["type"].(string); bt != BlockTypeToolUse {
		t.Fatalf("content[0].type = %q, want tool_use", bt)
	}
	if id, _ := tu["id"].(string); id != "toolu_1" {
		t.Errorf("tool_use.id = %q, want toolu_1", id)
	}
	if name, _ := tu["name"].(string); name != "Stub" {
		t.Errorf("tool_use.name = %q, want Stub", name)
	}
	input, ok := tu["input"].(map[string]interface{})
	if !ok {
		t.Fatalf("tool_use.input 必须是对象，实际 %T", tu["input"])
	}
	if input["x"] != "hi" {
		t.Errorf("tool_use.input.x = %v, want hi", input["x"])
	}

	// user 帧回填 tool_result，宿主靠它清掉 pending 工具。
	userContent := messageContent(t, frames[7])
	if len(userContent) != 1 {
		t.Fatalf("user content 长度 = %d, want 1", len(userContent))
	}
	tr, _ := userContent[0].(map[string]interface{})
	if bt, _ := tr["type"].(string); bt != BlockTypeToolResult {
		t.Errorf("user content[0].type = %q, want tool_result", bt)
	}
	if id, _ := tr["tool_use_id"].(string); id != "toolu_1" {
		t.Errorf("tool_result.tool_use_id = %q, want toolu_1", id)
	}
	if txt, _ := tr["content"].(string); txt != "工具执行结果" {
		t.Errorf("tool_result.content = %q", txt)
	}
	if isErr, _ := tr["is_error"].(bool); isErr {
		t.Error("成功的工具不应带 is_error")
	}

	// FinishTurn 只应在最终收尾时触发一次，且只带本回合文本（不是整轮累计）。
	if len(finishTexts) != 1 {
		t.Fatalf("FinishTurn 调用 %d 次，want 1", len(finishTexts))
	}
	if finishTexts[0] != "完成" {
		t.Errorf("FinishTurn.Text = %q, want 完成（只含本回合，不含前几轮）", finishTexts[0])
	}

	// 整轮累计文本仍应是全部回合的拼接。
	if res.Text != "完成" {
		t.Errorf("RunResult.Text = %q, want 完成", res.Text)
	}
}

// TestRunMultiTurnResultCarriesLastTurnOnly 锁死 result 帧只带**末回合**文本。
//
// 宿主 streamStructuredAIDelta 的收尾去重（agent_ai.go suffixNotStreamed）
// 把 result 当「最后一条 assistant 消息的文本」，与已流式发出的 delta 做
// 后缀比对。若 result 给整轮累计，多回合 run 会以第 1 回合文本开头，与
// 只含末回合的已流式文本必然分叉，宿主遂判定「整段没发过」并重发全文 ——
// 手机上表现为整轮回复再刷一遍、落库消息双份。
//
// 这个用例的两个回合**都产出文本**，才能暴露差异：只让第 1 回合调工具
// 不说话时，整轮累计恰好等于末回合，测不出问题。
func TestRunMultiTurnResultCarriesLastTurnOnly(t *testing.T) {
	turn1 := `{"type":"message_start","message":{"id":"msg_a","model":"claude-x"}}` + "\n\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"我先看一下"}}` + "\n\n" +
		`data: {"type":"content_block_stop","index":0}` + "\n\n" +
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_m","name":"Stub"}}` + "\n\n" +
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"x\":\"hi\"}"}}` + "\n\n" +
		`data: {"type":"content_block_stop","index":1}` + "\n\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"}}`
	turn2 := `{"type":"message_start","message":{"id":"msg_b","model":"claude-x"}}` + "\n\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"改好了"}}` + "\n\n" +
		`data: {"type":"content_block_stop","index":0}` + "\n\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"}}`

	srv := sseServer(t, (&turnScript{turns: []string{turn1, turn2}}).handler(t))

	registry := NewRegistry()
	registry.Register(&stubTool{name: "Stub", result: ToolResult{Content: "ok"}})

	rec := &frameRecorder{}
	cfg := &Config{
		Client:    NewClient(srv.URL),
		Model:     "claude-x",
		Tools:     registry,
		Emit:      rec.emit,
		SessionID: "sess-multi",
	}
	res, err := Run(context.Background(), cfg, []Message{TextMessage("user", "干活")})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// 整轮累计：两回合文本都在。
	if res.Text != "我先看一下改好了" {
		t.Errorf("RunResult.Text = %q, want 我先看一下改好了（整轮累计）", res.Text)
	}

	// result 帧：只带末回合。给整轮会让宿主 suffixNotStreamed 判分叉并重发全文。
	res1 := resultFrame(t, rec.frames(t))
	if txt, _ := res1["result"].(string); txt != "改好了" {
		t.Errorf("result.result = %q, want 改好了（只含末回合，不含第 1 回合）", txt)
	}
	if n, _ := res1["num_turns"].(float64); int(n) != 2 {
		t.Errorf("result.num_turns = %v, want 2", res1["num_turns"])
	}
}

// ---------------------------------------------------------------------------
// 截断守卫
// ---------------------------------------------------------------------------

func TestRunTruncatedToolCallsNotExecuted(t *testing.T) {
	// stop_reason == max_tokens 时，tool_use 的 JSON 参数是不完整的，
	// 执行会写坏文件。pi 的做法是整批失败、一个都不执行。
	//
	// 注意这里只钉「这批工具不执行 + 回填失败结果」——**整轮是否终止**是另一条
	// 语义（见 loop_truncation_test.go）：截断后循环继续，模型拿到「请拆小后
	// 重试」再收尾，所以这里给一个恢复回合，避免误把「继续」测成「终止」。
	turn := `{"type":"message_start","message":{"id":"msg_trunc","model":"claude-x"}}` + "\n\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_x","name":"Stub"}}` + "\n\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"x\":\"unfin"}}` + "\n\n" +
		`data: {"type":"content_block_stop","index":0}` + "\n\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"max_tokens"}}`
	recover := `data: {"type":"message_start","message":{"id":"msg_rec","model":"claude-x"}}` + "\n\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"已拆小"}}` + "\n\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"}}`

	srv := sseServer(t, (&turnScript{turns: []string{turn, recover}}).handler(t))

	tool := &stubTool{name: "Stub", result: ToolResult{Content: "不该被调用"}}
	registry := NewRegistry()
	registry.Register(tool)

	rec := &frameRecorder{}
	cfg := &Config{
		Client:    NewClient(srv.URL),
		Model:     "claude-x",
		Tools:     registry,
		Emit:      rec.emit,
		SessionID: "sess-trunc",
	}
	res, err := Run(context.Background(), cfg, []Message{TextMessage("user", "写文件")})
	if err != nil {
		t.Fatalf("Run 不应返回错误（失败通过 result 帧表达）: %v", err)
	}
	if n := atomic.LoadInt32(&tool.calls); n != 0 {
		t.Fatalf("截断时工具不应执行，实际调用 %d 次", n)
	}
	if res.Turns != 2 {
		t.Errorf("Turns = %d, want 2（截断轮 + 恢复轮）", res.Turns)
	}

	frames := rec.frames(t)

	// 必须给每个 tool_use 回填一条失败 tool_result，否则模型下一轮会认为
	// 工具还在执行。
	userContent := messageContent(t, userFrame(t, frames))
	if len(userContent) != 1 {
		t.Fatalf("user content 长度 = %d, want 1", len(userContent))
	}
	tr, _ := userContent[0].(map[string]interface{})
	if isErr, _ := tr["is_error"].(bool); !isErr {
		t.Error("截断回填的 tool_result 必须带 is_error")
	}
	if id, _ := tr["tool_use_id"].(string); id != "toolu_x" {
		t.Errorf("tool_result.tool_use_id = %q, want toolu_x", id)
	}

	// 恢复轮正常收尾：最终 result 帧不该是错误，否则宿主会把「模型已自行
	// 拆小重写成功」的一轮报成失败。
	rf := resultFrame(t, frames)
	if isErr, _ := rf["is_error"].(bool); isErr {
		t.Error("恢复轮收尾时 is_error 必须为 false")
	}
	if sub, _ := rf["subtype"].(string); strings.HasPrefix(sub, "error_") {
		t.Errorf("恢复轮 subtype = %q，不应以 error_ 开头", sub)
	}
}

// ---------------------------------------------------------------------------
// 审批 / 围栏
// ---------------------------------------------------------------------------

func TestRunApprovalDenied(t *testing.T) {
	turn1 := `{"type":"message_start","message":{"id":"msg_a1","model":"claude-x"}}` + "\n\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_a","name":"Stub"}}` + "\n\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"x\":\"v\"}"}}` + "\n\n" +
		`data: {"type":"content_block_stop","index":0}` + "\n\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"}}`
	turn2 := `{"type":"message_start","message":{"id":"msg_a2","model":"claude-x"}}` + "\n\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"好的，我换个方式"}}` + "\n\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"}}`

	srv := sseServer(t, (&turnScript{turns: []string{turn1, turn2}}).handler(t))

	tool := &stubTool{name: "Stub", result: ToolResult{Content: "不该被调用"}}
	registry := NewRegistry()
	registry.Register(tool)

	var sawRequest ApprovalRequest
	rec := &frameRecorder{}
	cfg := &Config{
		Client:    NewClient(srv.URL),
		Model:     "claude-x",
		Tools:     registry,
		Emit:      rec.emit,
		SessionID: "sess-deny",
		Approve: func(_ context.Context, req ApprovalRequest) (ApprovalResponse, error) {
			sawRequest = req
			return ApprovalResponse{Approved: false, Reason: "用户拒绝"}, nil
		},
	}
	if _, err := Run(context.Background(), cfg, []Message{TextMessage("user", "执行")}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if n := atomic.LoadInt32(&tool.calls); n != 0 {
		t.Fatalf("审批被拒后工具不应执行，实际调用 %d 次", n)
	}
	if sawRequest.ToolName != "Stub" || sawRequest.ToolUseID != "toolu_a" {
		t.Errorf("审批请求 = %+v, want Stub/toolu_a", sawRequest)
	}
	if sawRequest.Input["x"] != "v" {
		t.Errorf("审批请求未带完整参数: %v", sawRequest.Input)
	}

	// 拒绝理由必须回填给模型，让它知道发生了什么。
	tr, _ := messageContent(t, userFrame(t, rec.frames(t)))[0].(map[string]interface{})
	if txt, _ := tr["content"].(string); !strings.Contains(txt, "用户拒绝") {
		t.Errorf("tool_result 应回填拒绝理由，实际 %q", txt)
	}
	if isErr, _ := tr["is_error"].(bool); !isErr {
		t.Error("被拒的 tool_result 必须带 is_error")
	}
}

func TestRunScopeCheckBlocks(t *testing.T) {
	turn1 := `{"type":"message_start","message":{"id":"msg_s1","model":"claude-x"}}` + "\n\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_s","name":"Stub"}}` + "\n\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{}"}}` + "\n\n" +
		`data: {"type":"content_block_stop","index":0}` + "\n\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"}}`
	turn2 := `{"type":"message_start","message":{"id":"msg_s2","model":"claude-x"}}` + "\n\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"只读模式不能写"}}` + "\n\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"}}`

	srv := sseServer(t, (&turnScript{turns: []string{turn1, turn2}}).handler(t))

	tool := &stubTool{name: "Stub", result: ToolResult{Content: "不该被调用"}}
	registry := NewRegistry()
	registry.Register(tool)

	rec := &frameRecorder{}
	cfg := &Config{
		Client:    NewClient(srv.URL),
		Model:     "claude-x",
		Tools:     registry,
		Emit:      rec.emit,
		SessionID: "sess-scope",
		Env: &ToolEnv{
			ScopeCheck: func(toolName string, _ map[string]interface{}) (bool, string) {
				if toolName == "Stub" {
					return false, "goal 只读模式禁止写操作"
				}
				return true, ""
			},
		},
	}
	if _, err := Run(context.Background(), cfg, []Message{TextMessage("user", "写")}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if n := atomic.LoadInt32(&tool.calls); n != 0 {
		t.Fatalf("围栏拒绝后工具不应执行，实际调用 %d 次", n)
	}
	tr, _ := messageContent(t, userFrame(t, rec.frames(t)))[0].(map[string]interface{})
	if txt, _ := tr["content"].(string); !strings.Contains(txt, "只读模式") {
		t.Errorf("tool_result 应回填围栏理由，实际 %q", txt)
	}
}

// ---------------------------------------------------------------------------
// 参数解析失败
// ---------------------------------------------------------------------------

func TestRunMalformedToolInputNotExecuted(t *testing.T) {
	turn1 := `{"type":"message_start","message":{"id":"msg_bad","model":"claude-x"}}` + "\n\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_bad","name":"Stub"}}` + "\n\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{不是 JSON"}}` + "\n\n" +
		`data: {"type":"content_block_stop","index":0}` + "\n\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"}}`
	turn2 := `{"type":"message_start","message":{"id":"msg_bad2","model":"claude-x"}}` + "\n\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"我修正参数"}}` + "\n\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"}}`

	srv := sseServer(t, (&turnScript{turns: []string{turn1, turn2}}).handler(t))

	tool := &stubTool{name: "Stub", result: ToolResult{Content: "不该被调用"}}
	registry := NewRegistry()
	registry.Register(tool)

	rec := &frameRecorder{}
	cfg := &Config{
		Client:    NewClient(srv.URL),
		Model:     "claude-x",
		Tools:     registry,
		Emit:      rec.emit,
		SessionID: "sess-bad",
	}
	if _, err := Run(context.Background(), cfg, []Message{TextMessage("user", "x")}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if n := atomic.LoadInt32(&tool.calls); n != 0 {
		t.Fatalf("参数不是合法 JSON 时工具不应执行，实际调用 %d 次", n)
	}
	tr, _ := messageContent(t, userFrame(t, rec.frames(t)))[0].(map[string]interface{})
	if isErr, _ := tr["is_error"].(bool); !isErr {
		t.Error("参数解析失败必须带 is_error")
	}
}

// ---------------------------------------------------------------------------
// 取消
// ---------------------------------------------------------------------------

func TestRunContextCancelledBeforeStart(t *testing.T) {
	// 进程内内核没有子进程可杀，取消只能靠自己轮询。取消后必须仍然发出
	// result 帧，否则手机会永远停在「进行中」。
	//
	// 终止语义是 interrupted 而不是 error_during_execution：用户在手机上点
	// 「停止」不是故障，宿主的 claudeResultError 只把 is_error / error_* 转成
	// ai.error，走错误分支会让正常停止变成一条红色错误气泡。
	rec := &frameRecorder{}
	cfg := &Config{
		Client:    NewClient("http://127.0.0.1:1"), // 不会被访问
		Model:     "claude-x",
		Emit:      rec.emit,
		SessionID: "sess-cancel",
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	res, err := Run(ctx, cfg, []Message{TextMessage("user", "x")})
	if err == nil {
		t.Fatal("取消后 Run 应返回错误")
	}
	if res == nil {
		t.Fatal("取消后 Run 仍应返回结果，宿主据此收敛状态")
	}
	if res.IsError {
		t.Error("取消不应被标记为 IsError —— 否则宿主会再补一条 ai.error")
	}

	frames := rec.frames(t)
	rf := resultFrame(t, frames)
	if sub, _ := rf["subtype"].(string); sub != ResultSubtypeInterrupted {
		t.Errorf("result.subtype = %q, want %s", sub, ResultSubtypeInterrupted)
	}
	if isErr, _ := rf["is_error"].(bool); isErr {
		t.Error("取消时 is_error 必须为 false —— 用户停止不是错误")
	}
	assertSessionIDOnEveryFrame(t, frames, "sess-cancel")
}

// ---------------------------------------------------------------------------
// session_id 默认值
// ---------------------------------------------------------------------------

func TestRunGeneratesSessionIDWhenEmpty(t *testing.T) {
	srv := sseServer(t, (&turnScript{turns: []string{
		`{"type":"message_start","message":{"id":"m","model":"claude-x"}}` + "\n\n" +
			`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"}}`,
	}}).handler(t))

	rec := &frameRecorder{}
	cfg := &Config{Client: NewClient(srv.URL), Model: "claude-x", Emit: rec.emit}
	if _, err := Run(context.Background(), cfg, []Message{TextMessage("user", "x")}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	frames := rec.frames(t)
	if len(frames) == 0 {
		t.Fatal("应至少产出一帧")
	}
	sid, _ := frames[0]["session_id"].(string)
	if strings.TrimSpace(sid) == "" {
		t.Fatal("SessionID 为空时必须自动生成，不能是空串")
	}
	assertSessionIDOnEveryFrame(t, frames, sid)
}

// ---------------------------------------------------------------------------
// 纯逻辑模式（Emit == nil）
// ---------------------------------------------------------------------------

func TestRunWithoutEmit(t *testing.T) {
	// Emit 为 nil 时全部帧写入是空操作，内核逻辑仍要跑通（单测/内部调用用）。
	srv := sseServer(t, (&turnScript{turns: []string{
		`{"type":"message_start","message":{"id":"m","model":"claude-x"}}` + "\n\n" +
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}` + "\n\n" +
			`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"}}`,
	}}).handler(t))

	res, err := Run(context.Background(), &Config{
		Client: NewClient(srv.URL),
		Model:  "claude-x",
	}, []Message{TextMessage("user", "x")})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Text != "ok" {
		t.Errorf("Text = %q, want ok", res.Text)
	}
}
