package tunnel

import "testing"

func TestNewTCPHandlerContext_HasNoDeadline(t *testing.T) {
	ctx := newTCPHandlerContext()
	if _, ok := ctx.Deadline(); ok {
		t.Fatal("newTCPHandlerContext unexpectedly has a deadline")
	}
}
