package agentcore

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
