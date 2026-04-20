package tcp

import (
	"context"
	"net"
	"testing"
	"time"
)

type deadlineRecorderConn struct {
	setReadDeadlines []time.Time
}

func (c *deadlineRecorderConn) Read([]byte) (int, error)         { return 0, nil }
func (c *deadlineRecorderConn) Write(b []byte) (int, error)      { return len(b), nil }
func (c *deadlineRecorderConn) Close() error                     { return nil }
func (c *deadlineRecorderConn) LocalAddr() net.Addr              { return &net.TCPAddr{} }
func (c *deadlineRecorderConn) RemoteAddr() net.Addr             { return &net.TCPAddr{} }
func (c *deadlineRecorderConn) SetDeadline(time.Time) error      { return nil }
func (c *deadlineRecorderConn) SetWriteDeadline(time.Time) error { return nil }

func (c *deadlineRecorderConn) SetReadDeadline(t time.Time) error {
	c.setReadDeadlines = append(c.setReadDeadlines, t)
	return nil
}

func TestApplyContextReadDeadline_ClearsDeadlineAfterUse(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	conn := &deadlineRecorderConn{}
	clearDeadline := applyContextReadDeadline(ctx, conn)
	clearDeadline()

	if len(conn.setReadDeadlines) != 2 {
		t.Fatalf("SetReadDeadline count = %d, want 2", len(conn.setReadDeadlines))
	}
	if conn.setReadDeadlines[0].IsZero() {
		t.Fatal("first deadline should be non-zero")
	}
	if !conn.setReadDeadlines[1].IsZero() {
		t.Fatalf("second deadline = %v, want zero time", conn.setReadDeadlines[1])
	}
}

func TestApplyContextReadDeadline_NoDeadlineIsNoop(t *testing.T) {
	conn := &deadlineRecorderConn{}
	clearDeadline := applyContextReadDeadline(context.Background(), conn)
	clearDeadline()

	if len(conn.setReadDeadlines) != 0 {
		t.Fatalf("SetReadDeadline count = %d, want 0", len(conn.setReadDeadlines))
	}
}

func TestApplyContextReadDeadline_WrappedConnWithoutUnderlyingIsNoop(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	clearDeadline := applyContextReadDeadline(ctx, &WrappedConn{})
	clearDeadline()
}

func TestIsUsableConn_TypedNilWrappedConnIsFalse(t *testing.T) {
	var wrapped *WrappedConn
	var conn net.Conn = wrapped

	if isUsableConn(conn) {
		t.Fatal("isUsableConn() = true, want false for typed nil wrapped conn")
	}
}
