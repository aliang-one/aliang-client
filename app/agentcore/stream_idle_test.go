package agentcore

import (
	"errors"
	"io"
	"strings"
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
