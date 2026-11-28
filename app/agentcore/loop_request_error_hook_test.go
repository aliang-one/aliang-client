package agentcore

import (
	"context"
	"fmt"
	"net/http"
	"testing"
)

// 失败上报缝的宿主接线（spec 二期 §5.3a）：子 run 形态 Emit=nil，OnRetry 的
// 帧通道全空，OnRequestError 也必须照样从 Loop 传到宿主。这里用不可重试的
// 402 走完 Loop，断言 Hooks.OnRequestError 恰好一次且带状态码。
//
// 变异守卫：删掉 loop.go 里 WithRequestError(r.cfg.Hooks.OnRequestError)，
// 本条即红。
func TestLoopOnRequestErrorHookFires(t *testing.T) {
	srv := sseServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(http.StatusPaymentRequired)
		fmt.Fprint(w, `{"error":{"message":"insufficient balance","type":"payment_required"}}`)
	})

	type observed struct {
		status  int
		headers http.Header
	}
	var calls []observed
	res, err := Run(context.Background(), &Config{
		Client: NewClient(srv.URL),
		Model:  "x",
		// Emit 刻意不设（零值 nil）——锁死「钩子不依赖帧通道」。
		Hooks: LoopHooks{
			OnRequestError: func(status int, headers http.Header) {
				calls = append(calls, observed{status: status, headers: headers})
			},
		},
	}, []Message{TextMessage("user", "hi")})
	// 终端上游错误经 finishWithErr 返回 error（不是 result.IsError）。
	if err == nil {
		t.Fatalf("402 应最终失败, got result=%+v", res)
	}
	if len(calls) != 1 {
		t.Fatalf("OnRequestError 次数 = %d, want 1", len(calls))
	}
	if calls[0].status != http.StatusPaymentRequired {
		t.Errorf("status = %d, want 402", calls[0].status)
	}
}
