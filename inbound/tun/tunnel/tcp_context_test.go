package tunnel

import (
	"testing"
	"time"
)

func TestTeardownContext_NoDeadlineInitially(t *testing.T) {
	tr := New(nil, nil)
	ctx := tr.TeardownContext()
	if _, ok := ctx.Deadline(); ok {
		t.Fatal("teardown context unexpectedly has a deadline")
	}
}

func TestTeardownContext_CancelThenLazyRebuild(t *testing.T) {
	tr := New(nil, nil)
	first := tr.TeardownContext()
	tr.CancelTeardown()
	select {
	case <-first.Done():
	case <-time.After(1 * time.Second):
		t.Fatal("CancelTeardown did not cancel the live teardown context")
	}

	// 拆完重置:下一批连接拿到全新未取消 ctx(引擎重启代次)。
	second := tr.TeardownContext()
	select {
	case <-second.Done():
		t.Fatal("fresh teardown context must not be cancelled")
	default:
	}

	// CancelTeardown 幂等:重复取消不得误伤新代次。
	tr.CancelTeardown()
	tr.CancelTeardown()
	third := tr.TeardownContext()
	select {
	case <-third.Done():
		t.Fatal("repeated CancelTeardown must keep new context alive")
	default:
	}
}
