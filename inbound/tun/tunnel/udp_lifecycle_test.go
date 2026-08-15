package tunnel

import (
	"sync/atomic"
	"testing"
)

type countingCloser struct {
	closed atomic.Int32
}

func (c *countingCloser) Close() error {
	c.closed.Add(1)
	return nil
}

func TestUDPFlowCloserClosesBothEndpointsOnce(t *testing.T) {
	origin := &countingCloser{}
	remote := &countingCloser{}
	flow := newUDPFlowCloser(origin)
	flow.Add(remote)

	flow.Close()
	flow.Close()

	if got := origin.closed.Load(); got != 1 {
		t.Fatalf("origin close count = %d, want 1", got)
	}
	if got := remote.closed.Load(); got != 1 {
		t.Fatalf("remote close count = %d, want 1", got)
	}
}

func TestUDPFlowCloserImmediatelyClosesLateEndpoint(t *testing.T) {
	flow := newUDPFlowCloser()
	flow.Close()

	lateRemote := &countingCloser{}
	flow.Add(lateRemote)

	if got := lateRemote.closed.Load(); got != 1 {
		t.Fatalf("late remote close count = %d, want 1", got)
	}
}
