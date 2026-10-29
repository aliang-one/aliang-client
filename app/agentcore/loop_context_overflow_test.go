package agentcore

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// 上游的 4xx 文本五花八门，只有明确指向「上下文/输入过长」的才算超限；
// max_tokens 之类的 400 不能被误判（否则会白白压缩一次历史）。
func TestIsContextOverflowError(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{
			name:   "Anthropic 措辞",
			status: http.StatusBadRequest,
			body:   `{"type":"error","error":{"message":"prompt is too long: 250000 tokens > 200000 maximum"}}`,
			want:   true,
		},
		{
			name:   "OpenAI 兼容措辞",
			status: http.StatusBadRequest,
			body:   `{"error":{"message":"This model's maximum context length is 128000 tokens"}}`,
			want:   true,
		},
		{
			name:   "中文措辞",
			status: http.StatusBadRequest,
			body:   `{"error":{"message":"输入上下文超出模型窗口"}}`,
			want:   true,
		},
		{
			name:   "max_tokens 超限不是上下文问题",
			status: http.StatusBadRequest,
			body:   `{"error":{"message":"max_tokens: 32000 > 16000, which is the maximum allowed number of output tokens"}}`,
			want:   false,
		},
		{
			name:   "429 限流不是上下文问题",
			status: http.StatusTooManyRequests,
			body:   `{"error":{"message":"rate limit exceeded"}}`,
			want:   false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isContextOverflowError(&httpStatusError{status: tc.status, body: tc.body})
			if got != tc.want {
				t.Fatalf("isContextOverflowError = %v, want %v (body=%s)", got, tc.want, tc.body)
			}
		})
	}
}

// 用户把上下文滑杆拉到 1M、模型只有 200k 时，压缩阈值按 1M 算会一路拖到
// 750k 才动手，请求直接被上游拒。这里锁「被拒一次 → 降有效窗口 → 压缩 →
// 重试成功」这条自愈路径。
func TestRunRecoversFromContextOverflow(t *testing.T) {
	var requests int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&requests, 1)
		switch n {
		case 1:
			// 第一次正常回合：上游说装不下。
			http.Error(w, `{"type":"error","error":{"message":"prompt is too long: 250000 tokens > 200000 maximum"}}`, http.StatusBadRequest)
		case 2:
			// 压缩摘要请求。
			writeSimpleText(w, "早期上下文摘要")
		default:
			// 压缩后的重试：正常收尾。
			writeSimpleText(w, "ok")
		}
	}))
	defer srv.Close()

	// 历史要足够大，压缩才有「早期消息」可折叠：5 条各约 4k tokens。
	big := strings.Repeat("a", 16000)
	history := []Message{
		TextMessage("user", big),
		TextMessage("assistant", big),
		TextMessage("user", big),
		TextMessage("assistant", big),
		TextMessage("user", big),
	}

	var frames []string
	cfg := &Config{
		Client:        NewClient(srv.URL),
		Model:         "claude-x",
		ContextWindow: 1_000_000, // 故意远大于模型真实窗口
		Emit: func(frame []byte) error {
			frames = append(frames, string(frame))
			return nil
		},
	}

	res, err := Run(t.Context(), cfg, history)
	if err != nil {
		t.Fatalf("Run 应自愈成功，实际: %v", err)
	}
	if res == nil || res.Text != "ok" {
		t.Fatalf("Run 结果异常: %+v", res)
	}
	if got := atomic.LoadInt32(&requests); got != 3 {
		t.Fatalf("请求 %d 次，期望 3 次（超限 → 摘要 → 重试）", got)
	}
	found := false
	for _, f := range frames {
		if strings.Contains(f, "compact_boundary") {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("降级压缩应发出 compact_boundary 帧，让手机显示压缩提示")
	}
}

// TestContextOverflowCompactEmitsProgressFrames 锁降级压缩的进度接线：上下文
// 超限自愈的压缩与 maybeAutoCompact 同型（零 delta 长静默摘要请求），压缩期间
// 必须经 streamWriter 发 system/compact_progress 活性帧（带正的 summary_tokens），
// 且与既有 compact_boundary 帧并存。
func TestContextOverflowCompactEmitsProgressFrames(t *testing.T) {
	var requests int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&requests, 1)
		switch n {
		case 1:
			// 第一次回合：上游说装不下（响应检查阶段的 4xx，天然零帧）。
			http.Error(w, `{"type":"error","error":{"message":"prompt is too long: 250000 tokens > 200000 maximum"}}`, http.StatusBadRequest)
		case 2:
			// 压缩摘要请求：一段 3000 字符的摘要，超过 2k 节流线，至少触发
			// 一次进度回调。
			writeSimpleText(w, strings.Repeat("甲", 3000))
		default:
			// 压缩后的重试：正常收尾。
			writeSimpleText(w, "ok")
		}
	}))
	defer srv.Close()

	big := strings.Repeat("a", 16000)
	history := []Message{
		TextMessage("user", big),
		TextMessage("assistant", big),
		TextMessage("user", big),
		TextMessage("assistant", big),
		TextMessage("user", big),
	}

	rec := &frameRecorder{}
	cfg := &Config{
		Client:        NewClient(srv.URL),
		Model:         "claude-x",
		ContextWindow: 1_000_000,
		Emit:          rec.emit,
	}
	res, err := Run(t.Context(), cfg, history)
	if err != nil {
		t.Fatalf("Run 应自愈成功，实际: %v", err)
	}
	if res == nil || res.Text != "ok" {
		t.Fatalf("Run 结果异常: %+v", res)
	}
	if got := atomic.LoadInt32(&requests); got != 3 {
		t.Fatalf("请求 %d 次，期望 3 次（超限 → 摘要 → 重试）", got)
	}
	progressFrames := 0
	foundBoundary := false
	for _, f := range rec.frames(t) {
		if typ, _ := f["type"].(string); typ != "system" {
			continue
		}
		switch sub, _ := f["subtype"].(string); sub {
		case "compact_progress":
			progressFrames++
			if n, ok := f["summary_tokens"].(float64); !ok || n <= 0 {
				t.Fatalf("compact_progress 帧缺 summary_tokens: %v", f)
			}
		case "compact_boundary":
			foundBoundary = true
		}
	}
	if progressFrames == 0 {
		t.Fatal("降级压缩未发出任何 system/compact_progress 活性帧")
	}
	if !foundBoundary {
		t.Fatal("降级压缩应发出 compact_boundary 帧，让手机显示压缩提示")
	}
}

// TestContextOverflowCompactProgressWriteFailureKeepsRecovery 锁旧语义：进度
// 帧写失败只记日志，不拖累降级压缩与自愈——压缩照常完成、重试照常成功。
func TestContextOverflowCompactProgressWriteFailureKeepsRecovery(t *testing.T) {
	var requests int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&requests, 1)
		switch n {
		case 1:
			http.Error(w, `{"type":"error","error":{"message":"prompt is too long: 250000 tokens > 200000 maximum"}}`, http.StatusBadRequest)
		case 2:
			writeSimpleText(w, strings.Repeat("甲", 3000))
		default:
			writeSimpleText(w, "ok")
		}
	}))
	defer srv.Close()

	big := strings.Repeat("a", 16000)
	history := []Message{
		TextMessage("user", big),
		TextMessage("assistant", big),
		TextMessage("user", big),
		TextMessage("assistant", big),
		TextMessage("user", big),
	}

	cfg := &Config{
		Client:        NewClient(srv.URL),
		Model:         "claude-x",
		ContextWindow: 1_000_000,
		// emit 只对 compact_progress 帧报错：TurnStart/正文帧照常通过，隔离
		// 「进度帧写失败」这一个变量。
		Emit: func(frame []byte) error {
			if strings.Contains(string(frame), "compact_progress") {
				return errors.New("emit: 宿主写端已关")
			}
			return nil
		},
	}
	res, err := Run(t.Context(), cfg, history)
	if err != nil {
		t.Fatalf("进度帧写失败不得拖累自愈，实际: %v", err)
	}
	if res == nil || res.Text != "ok" {
		t.Fatalf("Run 结果异常: %+v", res)
	}
	if got := atomic.LoadInt32(&requests); got != 3 {
		t.Fatalf("请求 %d 次，期望 3 次（超限 → 摘要 → 重试）", got)
	}
}

// 有效窗口取配置值与降级值的较小者；没有降级时就是配置值。
func TestEffectiveContextWindow(t *testing.T) {
	r := &runner{cfg: &Config{ContextWindow: 1_000_000}}
	if got := r.effectiveContextWindow(); got != 1_000_000 {
		t.Fatalf("未降级时 = %d, want 1000000", got)
	}
	r.ctxOverflowWindow = 200_000
	if got := r.effectiveContextWindow(); got != 200_000 {
		t.Fatalf("降级后 = %d, want 200000", got)
	}
	// 降级值更大时不覆盖用户配置（理论上不会发生，防御一遍）。
	r.cfg.ContextWindow = 150_000
	if got := r.effectiveContextWindow(); got != 150_000 {
		t.Fatalf("取较小者 = %d, want 150000", got)
	}
}

func writeSimpleText(w http.ResponseWriter, text string) {
	w.Header().Set("content-type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(
		"data: {\"type\":\"message_start\",\"message\":{\"id\":\"m1\",\"model\":\"claude-x\"}}\n\n" +
			"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
			"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"" + text + "\"}}\n\n" +
			"data: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
			"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n" +
			"data: [DONE]\n\n"))
}
