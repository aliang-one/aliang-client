package agentcore

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// 这一组锁死上下文压缩的**安全契约**——压错一次就是 400（孤儿 tool_result）
// 或者用户目标丢失（摘要丢关键信息）：
//  1. 切分点只能落在 assistant 或人类 user 消息上，tool_use/tool_result 永远
//     成对保留在尾部原文里；
//  2. token 估算对中文不能低估到触发过晚（中文按 1 rune/token，不是字节/4）；
//  3. 自动压缩在历史超过窗口 3/4 时触发，压缩后历史 = 摘要 + 原文尾部，
//     OnCompact 钩子与 compact_boundary 帧都要发出（宿主据此落库/提示）。

func TestEstimateTokensCJKAware(t *testing.T) {
	ascii := EstimateTokens([]Message{TextMessage("user", strings.Repeat("a", 400))})
	if ascii < 100 || ascii > 115 {
		t.Fatalf("400 个 ASCII 字符应约 100 tokens（+消息开销），got %d", ascii)
	}
	cjk := EstimateTokens([]Message{TextMessage("user", strings.Repeat("中", 400))})
	if cjk < 400 || cjk > 415 {
		t.Fatalf("400 个汉字应约 400 tokens（1 rune/token），got %d", cjk)
	}
	if cjk < ascii*3 {
		t.Fatalf("中文估算不得按字节/4 低估：ascii=%d cjk=%d", ascii, cjk)
	}
}

// compactionFixture 构造一段含 tool_use/tool_result 的历史：
//
//	0 user(text) 1 assistant(text) 2 user(text)
//	3 assistant(tool_use) 4 user(tool_result) 5 assistant(text) 6 user(text)
func compactionFixture() []Message {
	return []Message{
		TextMessage("user", strings.Repeat("甲", 80)),
		TextMessage("assistant", strings.Repeat("乙", 80)),
		TextMessage("user", strings.Repeat("丙", 80)),
		BlocksMessage("assistant", []ContentBlock{{
			Type:  BlockTypeToolUse,
			ID:    "tool-1",
			Name:  "Read",
			Input: map[string]interface{}{"file_path": strings.Repeat("x", 80)},
		}}),
		BlocksMessage("user", []ContentBlock{{
			Type:      BlockTypeToolResult,
			ToolUseID: "tool-1",
			Content:   strings.Repeat("丁", 80),
		}}),
		TextMessage("assistant", strings.Repeat("戊", 80)),
		TextMessage("user", strings.Repeat("己", 80)),
	}
}

func TestCompactSplitIndexKeepsToolPairs(t *testing.T) {
	messages := compactionFixture()
	tailBudget := estimateMessageTokens(messages[6]) + estimateMessageTokens(messages[5])
	window := tailBudget * compactTailRatioDen

	split := compactSplitIndex(messages, window)
	if split <= 0 || split >= len(messages) {
		t.Fatalf("split = %d, 期望落在 (0,%d) 区间", split, len(messages))
	}
	// 尾部预算刚好覆盖 5、6 两条时切分点应在 m5（assistant），完整保留尾部。
	if split != 5 {
		t.Fatalf("split = %d, want 5", split)
	}
}

// TestCompactSplitIndexWalksBackFromToolResult 让尾部预算恰好停在 tool_result
// 上：切分点必须回退到它的 assistant tool_use，否则上游收到孤儿 tool_result。
func TestCompactSplitIndexWalksBackFromToolResult(t *testing.T) {
	messages := compactionFixture()
	tailBudget := estimateMessageTokens(messages[6]) +
		estimateMessageTokens(messages[5]) +
		estimateMessageTokens(messages[4])
	window := tailBudget * compactTailRatioDen

	split := compactSplitIndex(messages, window)
	if split != 3 {
		t.Fatalf("split = %d, want 3（回退到 tool_use 的 assistant 消息）", split)
	}
	if !messageHasBlockType(messages[split], BlockTypeToolUse) {
		t.Fatalf("切分点必须落在带 tool_use 的 assistant 消息上")
	}
	// 尾部必须同时包含 tool_use 与它的 tool_result。
	hasUse, hasResult := false, false
	for _, m := range messages[split:] {
		hasUse = hasUse || messageHasBlockType(m, BlockTypeToolUse)
		hasResult = hasResult || messageHasBlockType(m, BlockTypeToolResult)
	}
	if !hasUse || !hasResult {
		t.Fatalf("尾部 tool_use/tool_result 不完整 use=%v result=%v", hasUse, hasResult)
	}
}

func messageHasBlockType(m Message, blockType string) bool {
	blocks, ok := m.Content.([]ContentBlock)
	if !ok {
		return false
	}
	for _, b := range blocks {
		if b.Type == blockType {
			return true
		}
	}
	return false
}

func newSummarySSEServer(t *testing.T, summary string, requests *int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests != nil {
			*requests++
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"message_start\",\"message\":{\"id\":\"m1\",\"model\":\"glm-5.3\"}}\n\n" +
			"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
			"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"" + summary + "\"}}\n\n" +
			"data: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
			"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n" +
			"data: [DONE]\n\n"))
	}))
}

func TestCompactReplacesPrefixWithSummary(t *testing.T) {
	messages := compactionFixture()
	// 尾部预算覆盖 m4..m6：切分点会从 tool_result 回退到 m3(tool_use)，
	// 这样断言到的尾部原文同时含配对的 tool_use / tool_result。
	tailBudget := estimateMessageTokens(messages[6]) +
		estimateMessageTokens(messages[5]) +
		estimateMessageTokens(messages[4])
	window := tailBudget * compactTailRatioDen

	srv := newSummarySSEServer(t, "摘要：读完了 foo.go。", nil)
	defer srv.Close()

	cfg := &Config{Client: NewClient(srv.URL), Model: "glm-5.3", ContextWindow: window}
	compacted, info, err := Compact(t.Context(), cfg, messages, "manual")
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if info.Trigger != "manual" {
		t.Fatalf("Trigger = %q", info.Trigger)
	}
	if info.Removed != 3 || len(compacted) != 5 {
		t.Fatalf("Removed=%d len=%d, want 3/5", info.Removed, len(compacted))
	}
	head, ok := compacted[0].Content.(string)
	if !ok || !strings.Contains(head, compactSummaryHeader) || !strings.Contains(head, "读完了 foo.go") {
		t.Fatalf("首条消息应是带 header 的摘要，got %#v", compacted[0].Content)
	}
	// 尾部原文逐条保留（含配对的 tool_use / tool_result）。
	if !messageHasBlockType(compacted[1], BlockTypeToolUse) {
		t.Fatalf("尾部首条应是 assistant tool_use")
	}
	if !messageHasBlockType(compacted[2], BlockTypeToolResult) {
		t.Fatalf("尾部第二条应是 tool_result")
	}
	if info.PostTokens >= info.PreTokens {
		t.Fatalf("压缩后 token 应下降：pre=%d post=%d", info.PreTokens, info.PostTokens)
	}
}

func TestCompactNothingToCompact(t *testing.T) {
	messages := []Message{
		TextMessage("user", "你好"),
		TextMessage("assistant", "你好，有什么可以帮你？"),
	}
	cfg := &Config{Client: NewClient("http://127.0.0.1:1"), Model: "glm-5.3", ContextWindow: DefaultContextWindow}
	_, _, err := Compact(t.Context(), cfg, messages, "manual")
	if !errors.Is(err, ErrNothingToCompact) {
		t.Fatalf("err = %v, want ErrNothingToCompact（历史短时不得发摘要请求）", err)
	}
}

// TestRunAutoCompactsBeforeRequest 全链路锁自动压缩：历史估算超过窗口 3/4 →
// 先发摘要请求压掉前缀 → 正常回合的请求里首条是摘要 → OnCompact 钩子与
// compact_boundary 帧都发出。
func TestRunAutoCompactsBeforeRequest(t *testing.T) {
	var mu sync.Mutex
	summaryRequests := 0
	chatRequests := 0
	var chatBody map[string]interface{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		isSummary := false
		if sys, _ := body["system"].(string); strings.Contains(sys, "上下文压缩器") {
			isSummary = true
		}
		mu.Lock()
		if isSummary {
			summaryRequests++
		} else {
			chatRequests++
			chatBody = body
		}
		mu.Unlock()
		text := "ok"
		if isSummary {
			text = "摘要正文"
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"message_start\",\"message\":{\"id\":\"m1\",\"model\":\"glm-5.3\"}}\n\n" +
			"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
			"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"" + text + "\"}}\n\n" +
			"data: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
			"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n" +
			"data: [DONE]\n\n"))
	}))
	defer srv.Close()

	// 6 条消息 × 约 125 tokens ≈ 750，窗口 800 → 阈值 600 必触发；
	// 尾部预算 200 只够保 1 条，前缀有得压。
	var history []Message
	for i := 0; i < 3; i++ {
		history = append(history,
			TextMessage("user", strings.Repeat("甲", 120)),
			TextMessage("assistant", strings.Repeat("乙", 120)))
	}

	var frames [][]byte
	var compacted []Message
	var info CompactInfo
	cfg := &Config{
		Client:        NewClient(srv.URL),
		Model:         "glm-5.3",
		ContextWindow: 800,
		Emit: func(frame []byte) error {
			frames = append(frames, append([]byte(nil), frame...))
			return nil
		},
		Hooks: LoopHooks{OnCompact: func(msgs []Message, ci CompactInfo) {
			compacted = append([]Message(nil), msgs...)
			info = ci
		}},
	}
	res, err := Run(t.Context(), cfg, history)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res == nil || res.Text != "ok" {
		t.Fatalf("Run 结果异常: %+v", res)
	}
	mu.Lock()
	defer mu.Unlock()
	if summaryRequests != 1 {
		t.Fatalf("摘要请求 %d 次, want 1", summaryRequests)
	}
	if chatRequests != 1 {
		t.Fatalf("正常回合请求 %d 次, want 1", chatRequests)
	}
	// 正常回合的首条消息必须是摘要（压缩生效的线上证据）。
	msgs, _ := chatBody["messages"].([]interface{})
	if len(msgs) == 0 {
		t.Fatalf("正常回合请求缺 messages")
	}
	first, _ := msgs[0].(map[string]interface{})
	if content, _ := first["content"].(string); !strings.Contains(content, compactSummaryHeader) {
		t.Fatalf("首条消息应是压缩摘要，got %v", first["content"])
	}
	if info.Trigger != "auto" || info.PreTokens < 600 || info.PostTokens >= info.PreTokens {
		t.Fatalf("OnCompact info 异常: %+v", info)
	}
	if len(compacted) == 0 {
		t.Fatalf("OnCompact 未回传压缩后的历史")
	}
	foundBoundary := false
	for _, frame := range frames {
		if strings.Contains(string(frame), "\"compact_boundary\"") {
			foundBoundary = true
			break
		}
	}
	if !foundBoundary {
		t.Fatalf("缺少 system/compact_boundary 帧")
	}
}

// TestRunDoesNotCompactBelowThreshold 锁不误压：历史远小于窗口时不发摘要请求。
func TestRunDoesNotCompactBelowThreshold(t *testing.T) {
	var mu sync.Mutex
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests++
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"message_start\",\"message\":{\"id\":\"m1\",\"model\":\"glm-5.3\"}}\n\n" +
			"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
			"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\n" +
			"data: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
			"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n" +
			"data: [DONE]\n\n"))
	}))
	defer srv.Close()

	cfg := &Config{Client: NewClient(srv.URL), Model: "glm-5.3", ContextWindow: 1_000_000}
	if _, err := Run(t.Context(), cfg, []Message{TextMessage("user", "你好")}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if requests != 1 {
		t.Fatalf("请求 %d 次, want 1（不应有摘要请求）", requests)
	}
}
