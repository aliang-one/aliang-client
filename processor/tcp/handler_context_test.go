package tcp

import (
	"context"
	"testing"
	"time"
)

func TestPrepareTCPConnectionContexts_ScopesConnectTimeoutSeparately(t *testing.T) {
	sessionCtx, connectCtx, cancel := prepareTCPConnectionContexts(context.Background(), "tun-test")
	defer cancel()

	if got := sessionCtx.Value(tcpContextConnIDKey{}); got != "tun-test" {
		t.Fatalf("session conn_id = %v, want tun-test", got)
	}
	if got := connectCtx.Value(tcpContextConnIDKey{}); got != "tun-test" {
		t.Fatalf("connect conn_id = %v, want tun-test", got)
	}

	if _, ok := sessionCtx.Deadline(); ok {
		t.Fatal("session context unexpectedly has a deadline")
	}

	deadline, ok := connectCtx.Deadline()
	if !ok {
		t.Fatal("connect context is missing a deadline")
	}

	remaining := time.Until(deadline)
	want := time.Duration(DefaultTCPConnectTimeout) * time.Second
	if remaining > want || remaining < want-2*time.Second {
		t.Fatalf("connect deadline remaining = %v, want around %v", remaining, want)
	}
}

func TestPrepareTCPConnectionContexts_RespectsParentDeadline(t *testing.T) {
	parentCtx, cancelParent := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelParent()

	sessionCtx, connectCtx, cancel := prepareTCPConnectionContexts(parentCtx, "tun-test")
	defer cancel()

	sessionDeadline, ok := sessionCtx.Deadline()
	if !ok {
		t.Fatal("session context should preserve parent deadline")
	}
	connectDeadline, ok := connectCtx.Deadline()
	if !ok {
		t.Fatal("connect context should preserve parent deadline")
	}

	drift := sessionDeadline.Sub(connectDeadline)
	if drift < -200*time.Millisecond || drift > 200*time.Millisecond {
		t.Fatalf("session/connect deadlines diverged: session=%v connect=%v", sessionDeadline, connectDeadline)
	}
}
