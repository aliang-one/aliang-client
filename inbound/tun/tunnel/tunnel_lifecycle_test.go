package tunnel

import (
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/sagernet/gvisor/pkg/tcpip/stack"
)

type lifecycleTCPConn struct {
	mu         sync.Mutex
	closeCount int
}

func (c *lifecycleTCPConn) Read([]byte) (int, error)         { return 0, io.EOF }
func (c *lifecycleTCPConn) Write(p []byte) (int, error)      { return len(p), nil }
func (c *lifecycleTCPConn) LocalAddr() net.Addr              { return &net.TCPAddr{} }
func (c *lifecycleTCPConn) RemoteAddr() net.Addr             { return &net.TCPAddr{} }
func (c *lifecycleTCPConn) SetDeadline(time.Time) error      { return nil }
func (c *lifecycleTCPConn) SetReadDeadline(time.Time) error  { return nil }
func (c *lifecycleTCPConn) SetWriteDeadline(time.Time) error { return nil }
func (c *lifecycleTCPConn) ID() *stack.TransportEndpointID   { return &stack.TransportEndpointID{} }

func (c *lifecycleTCPConn) Close() error {
	c.mu.Lock()
	c.closeCount++
	c.mu.Unlock()
	return nil
}

func (c *lifecycleTCPConn) closes() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closeCount
}

func TestCloseActiveTCPConnectionsClosesAndReleasesTrackedConnections(t *testing.T) {
	tunnel := New(nil, nil)
	first := &lifecycleTCPConn{}
	second := &lifecycleTCPConn{}
	tunnel.trackTCPConn(first)
	tunnel.trackTCPConn(second)

	if got := tunnel.CloseActiveTCPConnections(); got != 2 {
		t.Fatalf("CloseActiveTCPConnections() = %d, want 2", got)
	}
	if first.closes() != 1 || second.closes() != 1 {
		t.Fatalf("close counts = (%d, %d), want (1, 1)", first.closes(), second.closes())
	}
	if got := tunnel.CloseActiveTCPConnections(); got != 0 {
		t.Fatalf("second CloseActiveTCPConnections() = %d, want 0", got)
	}
}

func TestNewBoundsTCPConcurrency(t *testing.T) {
	tunnel := New(nil, nil)
	if got := cap(tunnel.tcpLimiter); got != tcpMaxConcurrentConn {
		t.Fatalf("tcp limiter capacity = %d, want %d", got, tcpMaxConcurrentConn)
	}
	if tcpMaxConcurrentConn > 512 {
		t.Fatalf("tcp concurrency limit = %d, want at most 512", tcpMaxConcurrentConn)
	}
}
