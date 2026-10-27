package services

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"
)

func TestAgentCoreEmitUnblocksOnContextCancel(t *testing.T) {
	pr, pw := io.Pipe()
	defer pr.Close()
	ctx, cancel := context.WithCancel(context.Background())
	emit, stop := newAgentCoreEmit(ctx, pw)
	defer stop()

	// 不消费 pipeReader：写侧必然阻塞。
	done := make(chan error, 1)
	go func() { done <- emit([]byte("frame")) }()

	select {
	case <-done:
		t.Fatal("emit returned before cancel; write should be blocked")
	case <-time.After(50 * time.Millisecond):
	}

	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, io.ErrClosedPipe) {
			t.Fatalf("emit error = %v, want io.ErrClosedPipe", err)
		}
	case <-time.After(time.Second):
		t.Fatal("emit stayed blocked after ctx cancel")
	}
}

func TestAgentCoreEmitStopKeepsPipeWritable(t *testing.T) {
	pr, pw := io.Pipe()
	defer pr.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	emit, stop := newAgentCoreEmit(ctx, pw)
	stop()

	done := make(chan error, 1)
	go func() { done <- emit([]byte("frame")) }()

	buf := make([]byte, 5)
	if _, err := io.ReadFull(pr, buf); err != nil {
		t.Fatalf("read after stop: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("emit after stop returned %v, want nil", err)
		}
	case <-time.After(time.Second):
		t.Fatal("emit stayed blocked after stop")
	}
	if string(buf) != "frame" {
		t.Fatalf("read %q, want %q", buf, "frame")
	}
}
