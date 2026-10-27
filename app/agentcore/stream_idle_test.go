package agentcore

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestIdleTimeoutReaderReturnsStreamIdleError(t *testing.T) {
	pr, pw := io.Pipe()
	r := newIdleTimeoutReader(pr, 40*time.Millisecond)
	defer r.Close()

	// 先给一个字节，再永久静默（半开流形态）。
	go func() { _, _ = pw.Write([]byte("a")) }()

	buf := make([]byte, 8)
	if n, err := r.Read(buf); err != nil || n != 1 {
		t.Fatalf("first read = (%d, %v), want (1, nil)", n, err)
	}

	start := time.Now()
	_, err := r.Read(buf)
	if err == nil {
		t.Fatal("second read returned nil error, want streamIdleError")
	}
	var idleErr *streamIdleError
	if !errors.As(err, &idleErr) {
		t.Fatalf("error = %v (%T), want *streamIdleError", err, err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("idle detection took %s, want < 1s", elapsed)
	}
}

func TestIdleTimeoutReaderDisabledWhenWindowZero(t *testing.T) {
	pr, pw := io.Pipe()
	r := newIdleTimeoutReader(pr, 0)
	defer r.Close()

	go func() {
		time.Sleep(30 * time.Millisecond)
		_, _ = pw.Write([]byte("ok"))
	}()

	buf := make([]byte, 8)
	n, err := r.Read(buf)
	if err != nil || string(buf[:n]) != "ok" {
		t.Fatalf("read = (%q, %v), want (\"ok\", nil)", buf[:n], err)
	}
}

func TestStreamIdleErrorIsNotRetryable(t *testing.T) {
	err := &streamIdleError{window: 90 * time.Second}
	if isRetryable(err) {
		t.Fatal("streamIdleError must NOT be retryable: a retry would re-emit start/text frames")
	}
	if !strings.Contains(err.Error(), "停滞") {
		t.Fatalf("error message = %q, want it to mention 停滞", err.Error())
	}
}

func TestStreamDoesNotRetryOnStreamStall(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("content-type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		// 头部与一帧之后永久静默：半开流。
		_, _ = w.Write([]byte("data: {\"type\":\"message_start\",\"message\":{\"id\":\"m1\",\"model\":\"x\"}}\n\n"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-r.Context().Done()
	}))
	defer srv.Close()

	c := NewClient(srv.URL).WithStreamIdleTimeout(60 * time.Millisecond)

	done := make(chan error, 1)
	go func() {
		done <- c.Stream(context.Background(), &MessagesRequest{
			Model:     "x",
			MaxTokens: 16,
			Messages:  []Message{{Role: "user", Content: "hi"}},
		}, func(StreamEvent) error { return nil })
	}()

	var err error
	select {
	case err = <-done:
	case <-time.After(5 * time.Second):
		srv.CloseClientConnections()
		t.Fatal("Stream did not return on a stalled upstream; idle guard missing or ineffective")
	}

	var idleErr *streamIdleError
	if !errors.As(err, &idleErr) {
		t.Fatalf("Stream error = %v (%T), want *streamIdleError", err, err)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("upstream hits = %d, want 1 (stream stall must not retry)", got)
	}
}

func TestStreamCompletesWhenConnectionStaysOpenAfterMessageStop(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		f, _ := w.(http.Flusher)
		_, _ = w.Write([]byte("data: {\"type\":\"message_start\",\"message\":{\"id\":\"m1\",\"model\":\"x\"}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"message_stop\"}\n\n"))
		if f != nil {
			f.Flush()
		}
		// 数据发完但连接不关：半开。
		<-r.Context().Done()
	}))
	defer srv.Close()

	c := NewClient(srv.URL).WithStreamIdleTimeout(60 * time.Millisecond)

	done := make(chan error, 1)
	go func() {
		done <- c.Stream(context.Background(), &MessagesRequest{
			Model:     "x",
			MaxTokens: 16,
			Messages:  []Message{{Role: "user", Content: "hi"}},
		}, func(StreamEvent) error { return nil })
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Stream error = %v, want nil (turn already complete at message_stop)", err)
		}
	case <-time.After(5 * time.Second):
		srv.CloseClientConnections()
		t.Fatal("Stream did not return after message_stop; idle guard must not be the exit path")
	}
}
