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
