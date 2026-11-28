package agentcore

import (
	"context"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
)

// 失败上报缝（spec 二期 §5.3a）：不可重试的 401/402 必须走 OnRequestError——
// 它们永远不触发 OnRetry（不是瞬态），一期 ReportFailure 从未被内核流量唤醒。
// 断言：恰好一次、状态码正确、响应头（Retry-After）随错误带出。
func TestStreamOnRequestErrorNonRetryable(t *testing.T) {
	srv := sseServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":{"message":"invalid api key","type":"authentication_error"}}`)
	})

	type observed struct {
		status  int
		headers http.Header
	}
	var calls []observed
	err := NewClient(srv.URL).Stream(context.Background(), &MessagesRequest{Model: "x"}, func(StreamEvent) error {
		return nil
	}, WithRequestError(func(status int, headers http.Header) {
		calls = append(calls, observed{status: status, headers: headers})
	}))
	if err == nil {
		t.Fatal("401 应最终失败")
	}
	if len(calls) != 1 {
		t.Fatalf("OnRequestError 次数 = %d, want 1（终端失败恰好一次）", len(calls))
	}
	if calls[0].status != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", calls[0].status)
	}
	if got := calls[0].headers.Get("Retry-After"); got != "120" {
		t.Errorf("headers Retry-After = %q, want 120（响应头必须随错误带出）", got)
	}
}

// 重试耗尽也属于「最终失败」：最后一次失败的 status/headers 必须通知一次，
// 且只通知一次（不是每次重试都通知）。用 Retry-After: 1 把退避压到 1s/次，
// 让 5 次重试的耗时可控。
func TestStreamOnRequestErrorRetryExhaustion(t *testing.T) {
	var attempts int32
	srv := sseServer(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.Header().Set("content-type", "application/json")
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprint(w, `{"error":{"message":"bad gateway","type":"api_error"}}`)
	})

	var calls int32
	var lastStatus int32
	err := NewClient(srv.URL).Stream(context.Background(), &MessagesRequest{Model: "x"}, func(StreamEvent) error {
		return nil
	}, WithRequestError(func(status int, headers http.Header) {
		atomic.AddInt32(&calls, 1)
		atomic.StoreInt32(&lastStatus, int32(status))
	}))
	if err == nil {
		t.Fatal("重试耗尽后应返回错误")
	}
	if n := atomic.LoadInt32(&attempts); n != int32(maxRetries)+1 {
		t.Fatalf("attempts = %d, want %d", n, maxRetries+1)
	}
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Fatalf("OnRequestError 次数 = %d, want 1（只在重试耗尽时通知）", n)
	}
	if got := int(atomic.LoadInt32(&lastStatus)); got != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", got)
	}
}

// 成功不应触发失败上报（防误报把健康行打成失败）。
func TestStreamOnRequestErrorNotFiredOnSuccess(t *testing.T) {
	srv := sseServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeSSE(w,
			`{"type":"message_start","message":{"id":"msg_ok","model":"x"}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`,
		)
	})

	var calls int32
	err := NewClient(srv.URL).Stream(context.Background(), &MessagesRequest{Model: "x"}, func(StreamEvent) error {
		return nil
	}, WithRequestError(func(status int, headers http.Header) {
		atomic.AddInt32(&calls, 1)
	}))
	if err != nil {
		t.Fatalf("成功流不应报错: %v", err)
	}
	if n := atomic.LoadInt32(&calls); n != 0 {
		t.Fatalf("OnRequestError 次数 = %d, want 0", n)
	}
}

// 已下发事件后的 ctx 取消（用户停止/watchdog 超时）不算 endpoint 失败：错误
// 没有（0, nil）以外的上游状态可携带，触发 OnRequestError 会把一次用户取消
// 记进 ReportFailure（4a 评审 Finding 1，根治在 Stream 的 delivered 分支）。
// 变异守卫：删掉 delivered 分支里的 ctx.Err() 守卫，本条即红。
func TestStreamOnRequestErrorNotFiredOnDeliveredCtxCancel(t *testing.T) {
	// 服务端先发两帧（保证 delivered=true），再挂住直到客户端取消断连。
	srv := sseServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeSSE(w,
			`{"type":"message_start","message":{"id":"msg_cancel","model":"x"}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`,
		)
		<-r.Context().Done()
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls int32
	err := NewClient(srv.URL).Stream(ctx, &MessagesRequest{Model: "x"}, func(ev StreamEvent) error {
		if ev.Kind == EventText {
			// 收到首个 delta 即取消：对应用户在流式中途点停止。
			cancel()
		}
		return nil
	}, WithRequestError(func(status int, headers http.Header) {
		atomic.AddInt32(&calls, 1)
	}))
	if err == nil {
		t.Fatal("取消后的流应返回错误（ctx.Err() 语义不变）")
	}
	if n := atomic.LoadInt32(&calls); n != 0 {
		t.Fatalf("OnRequestError 次数 = %d, want 0（用户取消不算 endpoint 失败）", n)
	}
}
