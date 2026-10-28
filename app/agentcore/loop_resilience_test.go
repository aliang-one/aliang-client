package agentcore

import (
	"context"
	"errors"
	"testing"
)

// 上下文超限以 2xx 裸 JSON 信封回来（本网关实测形态）时，也必须被识别为
// 超限——否则会被当成不可重试的 invalid_request 直接判死整轮（2026-10-28
// ai_3p18eg1aYQvE 事故路径）。
func TestIsContextOverflowErrorEnvelope(t *testing.T) {
	cases := []struct {
		name string
		kind string
		msg  string
		want bool
	}{
		{
			name: "GLM input too long 信封",
			kind: "invalid_request_error",
			msg:  "input is too long: 260000 tokens exceeds the maximum context length",
			want: true,
		},
		{
			name: "OpenAI 兼容 maximum context 信封",
			kind: "invalid_request_error",
			msg:  "This model's maximum context length is 128000 tokens",
			want: true,
		},
		{
			name: "限流信封不是上下文问题",
			kind: "rate_limit_error",
			msg:  "Upstream rate limit exceeded, please retry later",
			want: false,
		},
		{
			name: "普通 invalid_request 不是上下文问题",
			kind: "invalid_request_error",
			msg:  "messages.0.role: field required",
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isContextOverflowError(&upstreamError{kind: tc.kind, message: tc.msg})
			if got != tc.want {
				t.Fatalf("isContextOverflowError(envelope kind=%s msg=%q) = %v, want %v", tc.kind, tc.msg, got, tc.want)
			}
		})
	}
}

// 手机没传 context_window（cfg.ContextWindow<=0）时，有效窗口必须回落到
// DefaultContextWindow，否则 maybeAutoCompact 因 window<=0 直接 return，
// 主动压缩永不触发。
func TestEffectiveContextWindowDefault(t *testing.T) {
	r := &runner{cfg: &Config{}}
	if got := r.effectiveContextWindow(); got != DefaultContextWindow {
		t.Fatalf("未配置窗口时 = %d, want %d(DefaultContextWindow)", got, DefaultContextWindow)
	}
	// 有降级值时，0 配置也要取降级值（更小者）。
	r.ctxOverflowWindow = 64_000
	if got := r.effectiveContextWindow(); got != 64_000 {
		t.Fatalf("降级后 = %d, want 64000", got)
	}
}

// 判死时必须把真因带进 RunResult.ErrMessage，供宿主落 journal + 日志。
func TestFinishWithErrSetsErrMessage(t *testing.T) {
	r := &runner{cfg: &Config{}}
	res, err := r.finishWithErr(context.Background(), "", errors.New("上游返回 HTTP 429: rate limited"))
	if err == nil {
		t.Fatal("finishWithErr 应透传 runErr")
	}
	if res == nil {
		t.Fatal("finishWithErr 应返回非 nil RunResult")
	}
	if !res.IsError {
		t.Fatal("IsError 应为 true")
	}
	if res.ErrMessage == "" {
		t.Fatal("ErrMessage 不应为空（真因必须可回溯）")
	}
	if res.ErrMessage != err.Error() {
		t.Fatalf("ErrMessage=%q, want %q", res.ErrMessage, err.Error())
	}
}

// 被用户取消（ctx.Err()!=nil）走 interrupted，不算错误，ErrMessage 应为空。
func TestFinishWithErrCancelledNoErrMessage(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := &runner{cfg: &Config{}}
	res, _ := r.finishWithErr(ctx, "", errors.New("whatever"))
	if res == nil {
		t.Fatal("应返回 RunResult")
	}
	if res.IsError {
		t.Fatal("取消不应标记 IsError")
	}
	if res.ErrMessage != "" {
		t.Fatalf("取消不应有 ErrMessage，实际 %q", res.ErrMessage)
	}
}
