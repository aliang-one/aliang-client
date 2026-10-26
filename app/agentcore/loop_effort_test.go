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

// 这一组锁死 effort 的**下发契约**：档位归一（NormalizeEffortLevel）与
// output_config.effort 进请求体（buildRequest）。2026-10-26 之前内核首期不
// 传达 effort，手机选了档也是装饰；现在经 output_config 下发——与 claude CLI
// 收到 --effort 后发的是同一个网关字段。契约钉两点：
//  1. 合法档位必须以 output_config.effort 形态出现在请求体（不是顶层字段）；
//  2. 无法识别的档位**不下发**——宁可不设，也不能赌上游宽容吃 400。

func TestNormalizeEffortLevel(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"low", "low"},
		{"medium", "medium"},
		{"high", "high"},
		{"xhigh", "xhigh"},
		{"max", "max"},
		// ultracode 是手机端阶梯的最高档，上游没有这个档位，映射到 max。
		{"ultracode", "max"},
		{"ULTRACODE", "max"},
		{" minimal ", "low"},
		{"none", "low"},
		{"", ""},
		{"bogus", ""},
		{"maximum", ""},
	}
	for _, tc := range cases {
		if got := NormalizeEffortLevel(tc.in); got != tc.want {
			t.Errorf("NormalizeEffortLevel(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestBuildRequestEffortWiring(t *testing.T) {
	r := &runner{cfg: &Config{Model: "glm-5.3", Effort: "ultracode"}}
	req := r.buildRequest()
	if req.OutputConfig == nil || req.OutputConfig.Effort != "max" {
		t.Fatalf("output_config.effort = %+v, want max", req.OutputConfig)
	}

	r = &runner{cfg: &Config{Model: "glm-5.3", Effort: "bogus"}}
	if req := r.buildRequest(); req.OutputConfig != nil {
		t.Fatalf("无法识别的档位不应下发，got %+v", req.OutputConfig)
	}

	r = &runner{cfg: &Config{Model: "glm-5.3"}}
	if req := r.buildRequest(); req.OutputConfig != nil {
		t.Fatalf("空 effort 不应下发，got %+v", req.OutputConfig)
	}
}

// TestStreamSendsOutputConfigEffort 锁线上的字节形状：effort 必须出现在
// 请求体的 output_config 对象里（Claude Messages API 语义），而不是顶层
// "effort" 字段——顶层字段上游不认。
func TestStreamSendsOutputConfigEffort(t *testing.T) {
	var captured map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dec := json.NewDecoder(r.Body)
		_ = dec.Decode(&captured)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"message_start\",\"message\":{\"id\":\"m1\",\"model\":\"glm-5.3\"}}\n\n" +
			"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
			"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\n" +
			"data: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
			"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n" +
			"data: [DONE]\n\n"))
	}))
	defer srv.Close()

	err := NewClient(srv.URL).Stream(t.Context(), &MessagesRequest{
		Model:        "glm-5.3",
		MaxTokens:    64,
		OutputConfig: &OutputConfig{Effort: "xhigh"},
	}, func(StreamEvent) error { return nil })
	if err != nil {
		t.Fatalf("stream: %v", err)
	}

	oc, ok := captured["output_config"].(map[string]interface{})
	if !ok {
		t.Fatalf("请求体缺 output_config: %v", captured)
	}
	if oc["effort"] != "xhigh" {
		t.Fatalf("output_config.effort = %v, want xhigh", oc["effort"])
	}
	if _, exists := captured["effort"]; exists {
		t.Fatalf("effort 不允许出现在顶层字段")
	}
}

// TestStreamEffortRejectionSelfHeal 锁死自愈语义(经 Run 全路径):上游按模型
// 拒绝 effort(400 且错误体点名 output_config)→ 锁存 → 去掉 output_config
// 重试本回合成功。不锁存的话该会话每轮 run 都带同一档重发、持续 ai.error
// (CLI 路径 effort-rejected 自愈的内核同款;锁存是 per-run 的——每 run 至多
// 付一次透明重试,不做跨 run 的宿主级缓存)。
func TestStreamEffortRejectionSelfHeal(t *testing.T) {
	var mu sync.Mutex
	attempts := 0
	var firstOutputConfig, secondOutputConfig map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dec := json.NewDecoder(r.Body)
		body := map[string]interface{}{}
		_ = dec.Decode(&body)
		mu.Lock()
		attempts++
		n := attempts
		if n == 1 {
			firstOutputConfig, _ = body["output_config"].(map[string]interface{})
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"output_config.effort is not supported by this model"}}`))
			mu.Unlock()
			return
		}
		secondOutputConfig, _ = body["output_config"].(map[string]interface{})
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"message_start\",\"message\":{\"id\":\"m2\",\"model\":\"glm-5.3\"}}\n\n" +
			"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"}}}\n\n" +
			"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\n" +
			"data: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
			"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n" +
			"data: [DONE]\n\n"))
	}))
	defer srv.Close()

	logs := []string{}
	result, err := Run(t.Context(), &Config{
		Model:  "glm-5.3",
		Effort: "xhigh",
		Client: NewClient(srv.URL),
		Logf: func(format string, args ...interface{}) {
			logs = append(logs, fmt.Sprintf(format, args...))
		},
	}, []Message{TextMessage("user", "hi")})
	if err != nil {
		t.Fatalf("自愈后 run 仍失败: %v", err)
	}
	if result == nil || result.Text != "ok" {
		t.Fatalf("result = %+v, want text ok", result)
	}
	mu.Lock()
	defer mu.Unlock()
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2(首发被拒+去 effort 重试)", attempts)
	}
	if firstOutputConfig == nil || firstOutputConfig["effort"] != "xhigh" {
		t.Fatalf("首次请求应带 output_config.effort=xhigh, got %v", firstOutputConfig)
	}
	if secondOutputConfig != nil {
		t.Fatalf("重试请求不应带 output_config, got %v", secondOutputConfig)
	}
	joined := strings.Join(logs, "\n")
	if !strings.Contains(joined, "拒绝 effort") {
		t.Fatalf("应有观测日志, got %v", logs)
	}
}

// TestStreamUnrelated400NotLatched 无关 400(错误体不点名 effort)不触发锁存,
// 原样上抛——别把普通参数错误误伤成 effort 问题。
func TestStreamUnrelated400NotLatched(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"model not found"}}`))
	}))
	defer srv.Close()

	r := &runner{
		cfg: &Config{
			Model:  "no-such-model",
			Effort: "high",
			Client: NewClient(srv.URL),
		},
	}
	_, _, err := r.streamTurn(t.Context(), r.buildRequest(), nil)
	if err == nil {
		t.Fatal("无关 400 应原样上抛")
	}
	if r.effortLatched {
		t.Fatal("无关 400 不应锁存 effort")
	}
}

// TestNormalizeEffortDropLogged 无法识别的档位被丢弃时留观测痕(只一次)。
func TestNormalizeEffortDropLogged(t *testing.T) {
	logs := []string{}
	r := &runner{
		cfg: &Config{
			Model:  "glm-5.3",
			Effort: "bogus-tier",
			Logf: func(format string, args ...interface{}) {
				logs = append(logs, fmt.Sprintf(format, args...))
			},
		},
	}
	_ = r.buildRequest()
	_ = r.buildRequest()
	if len(logs) != 1 {
		t.Fatalf("丢弃日志应只打一次, got %d: %v", len(logs), logs)
	}
}
