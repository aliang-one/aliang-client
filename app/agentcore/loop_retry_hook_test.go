package agentcore

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
)

// 重试活性旁路（vm129 审计 2026-11-10）：子 run 形态下 Emit 为零值（nil），
// streamWriter 全部写帧变空操作——Hooks.OnRetry 必须照样触发，它是宿主
// （agent_ai_agentcore_subagent.go 的 mirror.progress）观测子 run 上游重试链
// 的唯一通道。没有它，429/慢上游重试链（最坏 6×120s ≈ 12min）在面板与
// journal 上纯静默，「派完就卡死」。
//
// 变异守卫：删掉 loop.go 里 WithRetryNotice 回调中的 r.cfg.Hooks.OnRetry 分支，
// 本条即红（帧通道在这套测试里全程不可见，没有别的信号源）。
func TestLoopOnRetryHookFiresDespiteNilEmit(t *testing.T) {
	var attempts int32
	srv := sseServer(t, func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&attempts, 1) == 1 {
			w.Header().Set("content-type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"message":"upstream overloaded","type":"overloaded_error"},"type":"error"}`))
			return
		}
		writeSSE(w,
			`{"type":"message_start","message":{"id":"msg_retry_hook","model":"x"}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"重试后成功"}}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`,
		)
	})

	var notices []RetryNotice
	res, err := Run(context.Background(), &Config{
		Client: NewClient(srv.URL),
		Model:  "x",
		// Emit 刻意不设（零值 nil）——锁死「钩子不依赖帧通道」这一核心语义。
		Hooks: LoopHooks{
			OnRetry: func(n RetryNotice) { notices = append(notices, n) },
		},
	}, []Message{TextMessage("user", "hi")})
	if err != nil {
		t.Fatalf("503 后重试应成功: %v", err)
	}
	if res.IsError {
		t.Fatalf("不应失败: %q", res.Text)
	}
	if n := atomic.LoadInt32(&attempts); n != 2 {
		t.Fatalf("attempts = %d, want 2（503 + 重试成功）", n)
	}
	if len(notices) != 1 {
		t.Fatalf("OnRetry 触发次数 = %d, want 1", len(notices))
	}
	n := notices[0]
	if n.Attempt != 1 {
		t.Errorf("attempt = %d, want 1", n.Attempt)
	}
	if n.ErrorStatus != http.StatusServiceUnavailable {
		t.Errorf("error_status = %d, want 503", n.ErrorStatus)
	}
	if n.ErrorKind != "server_error" {
		t.Errorf("error_kind = %q, want server_error", n.ErrorKind)
	}
}
