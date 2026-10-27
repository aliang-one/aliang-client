package services

import (
	"context"
	"io"
	"testing"
	"time"
)

func TestAgentCoreEmitUnblocksOnContextCancel(t *testing.T) {
	pr, pw := io.Pipe()
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
		if err == nil {
			t.Fatal("emit returned nil after cancel, want closed-pipe error")
		}
	case <-time.After(time.Second):
		t.Fatal("emit stayed blocked after ctx cancel")
	}
	_ = pr.Close()
}
