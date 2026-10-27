package agentcore

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// 这一组锁死 prompt cache 的**线上契约**与自愈语义：
//  1. 请求体里真的带了 cache_control 断点（不是只在内核内部有个字段）；
//  2. 上游不认时自动降级——摘掉断点重试一次，整轮照常跑完；
//  3. 无关的 400 不许误伤成「不支持缓存」；
//  4. 缓存命中量必须能被观测到（cache_read），否则永远不知道有没有生效。

func sseTextDone(text string) string {
	return "data: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"model\":\"glm-5.3\"}}\n\n" +
		"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"" + text + "\"}}\n\n" +
		"data: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n" +
		"data: [DONE]\n\n"
}

// hasCacheBreakpoint 检查一个请求体里是否任何一个断点。
func hasCacheBreakpoint(body map[string]interface{}) bool {
	raw, err := json.Marshal(body)
	if err != nil {
		return false
	}
	return strings.Contains(string(raw), "cache_control")
}

// TestRunSendsCacheBreakpoints：正常路径下断点必须出现在线上字节里。
func TestRunSendsCacheBreakpoints(t *testing.T) {
	var mu sync.Mutex
	var bodies []map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := map[string]interface{}{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		bodies = append(bodies, body)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(sseTextDone("ok")))
	}))
	defer srv.Close()

	registry := DefaultRegistry()
	_, err := Run(t.Context(), &Config{
		Model:  "glm-5.3",
		System: "你是 AliangCode。",
		Client: NewClient(srv.URL),
		Tools:  registry,
	}, []Message{TextMessage("user", "hi")})
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 1 {
		t.Fatalf("请求数 = %d, want 1", len(bodies))
	}
	body := bodies[0]

	sys, ok := body["system"].([]interface{})
	if !ok || len(sys) == 0 {
		t.Fatalf("system 应升级成块数组，实际 %T", body["system"])
	}
	first, _ := sys[0].(map[string]interface{})
	if _, ok := first["cache_control"]; !ok {
		t.Errorf("system 块缺 cache_control: %v", first)
	}

	tools, _ := body["tools"].([]interface{})
	if len(tools) == 0 {
		t.Fatal("应带工具定义")
	}
	lastTool, _ := tools[len(tools)-1].(map[string]interface{})
	if _, ok := lastTool["cache_control"]; !ok {
		t.Errorf("最后一个工具缺 cache_control: %v", lastTool)
	}

	msgs, _ := body["messages"].([]interface{})
	lastMsg, _ := msgs[len(msgs)-1].(map[string]interface{})
	content, _ := lastMsg["content"].([]interface{})
	if len(content) == 0 {
		t.Fatalf("末条消息应是块数组: %v", lastMsg)
	}
	lastBlock, _ := content[len(content)-1].(map[string]interface{})
	if _, ok := lastBlock["cache_control"]; !ok {
		t.Errorf("末条消息的末块缺 cache_control: %v", lastBlock)
	}
}

// TestRunCacheRejectionSelfHeal：上游 400 点名 cache_control → 锁存 → 摘掉
// 断点重试成功。缓存是优化项，不能因为它不被支持就整轮失败。
func TestRunCacheRejectionSelfHeal(t *testing.T) {
	var mu sync.Mutex
	attempts := 0
	var first, second map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := map[string]interface{}{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		attempts++
		n := attempts
		mu.Unlock()
		if n == 1 {
			mu.Lock()
			first = body
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"messages.0.content.0: Extra inputs are not permitted: cache_control"}}`))
			return
		}
		mu.Lock()
		second = body
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(sseTextDone("ok")))
	}))
	defer srv.Close()

	logs := []string{}
	result, err := Run(t.Context(), &Config{
		Model:  "glm-5.3",
		System: "sys",
		Client: NewClient(srv.URL),
		Logf: func(format string, args ...interface{}) {
			logs = append(logs, fmt.Sprintf(format, args...))
		},
	}, []Message{TextMessage("user", "hi")})
	if err != nil {
		t.Fatalf("降级后 run 仍失败: %v", err)
	}
	if result == nil || result.Text != "ok" {
		t.Fatalf("result = %+v, want text ok", result)
	}

	mu.Lock()
	defer mu.Unlock()
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2（首发被拒 + 无断点重试）", attempts)
	}
	if !hasCacheBreakpoint(first) {
		t.Error("首次请求应带断点")
	}
	if hasCacheBreakpoint(second) {
		t.Errorf("重试请求不应再带断点: %v", second)
	}
	if !strings.Contains(strings.Join(logs, "\n"), "拒绝 cache_control") {
		t.Errorf("应有观测日志，实际 %v", logs)
	}
}

// TestRunUnrelated400DoesNotLatchCache：与缓存无关的 400 不许被误判。
func TestRunUnrelated400DoesNotLatchCache(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"model not found"}}`))
	}))
	defer srv.Close()

	r := &runner{
		cfg: &Config{
			Model:  "no-such-model",
			System: "sys",
			Client: NewClient(srv.URL),
		},
	}
	req := r.buildRequest()
	applyCacheBreakpoints(req)
	_, _, err := r.streamTurn(t.Context(), req, nil)
	if err == nil {
		t.Fatal("无关 400 应原样上抛")
	}
	if r.cacheLatched {
		t.Fatal("无关 400 不该锁存 cache")
	}
}

// TestRunUsageRecordsCacheRead：缓存命中量必须进 Usage 与日志——这是判断
// 「上游到底有没有做缓存」的唯一地面真值，也是本次优化的验收指标。
func TestRunUsageRecordsCacheRead(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(
			"data: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"model\":\"glm-5.3\",\"usage\":{\"input_tokens\":12,\"cache_creation_input_tokens\":30,\"cache_read_input_tokens\":900}}}\n\n" +
				"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
				"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\n" +
				"data: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
				"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":7}}\n\n" +
				"data: [DONE]\n\n"))
	}))
	defer srv.Close()

	logs := []string{}
	result, err := Run(t.Context(), &Config{
		Model:  "glm-5.3",
		Client: NewClient(srv.URL),
		Logf: func(format string, args ...interface{}) {
			logs = append(logs, fmt.Sprintf(format, args...))
		},
	}, []Message{TextMessage("user", "hi")})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Usage.CacheReadInputTokens != 900 {
		t.Errorf("CacheReadInputTokens = %d, want 900", result.Usage.CacheReadInputTokens)
	}
	if result.Usage.CacheCreationInputTokens != 30 {
		t.Errorf("CacheCreationInputTokens = %d, want 30", result.Usage.CacheCreationInputTokens)
	}
	if result.Usage.InputTokens != 12 || result.Usage.OutputTokens != 7 {
		t.Errorf("基础用量 = %+v", result.Usage)
	}
	// ctx 必须是三项之和（12+30+900）：只用 input_tokens 会把上下文报成 12，
	// 自动压缩就永远不触发。
	joined := strings.Join(logs, "\n")
	if !strings.Contains(joined, "ctx=942") {
		t.Errorf("上下文应为 942（含缓存命中），日志: %s", joined)
	}
}

// TestWrapUpRequestsToolslessSummary：撞回合上限时补一次无工具收尾。
//
// 关键断言是「请求里 tools 为空」——模型没有工具可调就只能输出文字，这才是
// 收尾能拿到总结的前提。同时确认收尾文本进了 turnText（finish 会把它拼进
// 结果），否则用户还是只看到一句「达到最大回合数」。
func TestWrapUpRequestsToolslessSummary(t *testing.T) {
	var mu sync.Mutex
	var body map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := map[string]interface{}{}
		_ = json.NewDecoder(r.Body).Decode(&b)
		mu.Lock()
		body = b
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(sseTextDone("已经改完解析器，还差测试没补")))
	}))
	defer srv.Close()

	r := &runner{
		cfg: &Config{
			Model:  "glm-5.3",
			System: "sys",
			Client: NewClient(srv.URL),
			Tools:  DefaultRegistry(),
		},
		history: []Message{TextMessage("user", "把解析器重写完")},
		writer:  newStreamWriter(nil, "sess"),
	}
	r.wrapUp(t.Context(), "工具使用预算（50 轮）已用完")

	mu.Lock()
	defer mu.Unlock()
	if body == nil {
		t.Fatal("收尾请求没发出去")
	}
	if tools, ok := body["tools"].([]interface{}); ok && len(tools) > 0 {
		t.Errorf("收尾请求不该带工具，实际 %d 个", len(tools))
	}
	msgs, _ := body["messages"].([]interface{})
	if len(msgs) < 2 {
		t.Fatalf("收尾请求应追加一条收尾指令，消息数 = %d", len(msgs))
	}
	last, _ := msgs[len(msgs)-1].(map[string]interface{})
	if got, _ := last["content"].(string); !strings.Contains(got, "预算") {
		t.Errorf("末条消息应是收尾指令，实际 %v", last["content"])
	}
	if !strings.Contains(r.turnText.String(), "已经改完解析器") {
		t.Errorf("收尾文本应进 turnText，实际 %q", r.turnText.String())
	}
}
