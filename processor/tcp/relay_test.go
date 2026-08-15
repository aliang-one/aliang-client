package tcp

import (
	"bytes"
	"context"
	"net"
	"sync"
	"testing"
	"time"

	M "aliang.one/nursorgate/inbound/tun/metadata"
)

type recordingRelayConn struct {
	reader bytes.Reader

	closeCount           int
	closeReadCount       int
	closeWriteCount      int
	setReadDeadlineCount int
}

func newRecordingRelayConn(payload []byte) *recordingRelayConn {
	conn := &recordingRelayConn{}
	conn.reader = *bytes.NewReader(payload)
	return conn
}

func (c *recordingRelayConn) Read(p []byte) (int, error)  { return c.reader.Read(p) }
func (c *recordingRelayConn) Write(p []byte) (int, error) { return len(p), nil }
func (c *recordingRelayConn) Close() error {
	c.closeCount++
	return nil
}
func (c *recordingRelayConn) LocalAddr() net.Addr {
	return &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 40000}
}
func (c *recordingRelayConn) RemoteAddr() net.Addr {
	return &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 50000}
}
func (c *recordingRelayConn) SetDeadline(time.Time) error      { return nil }
func (c *recordingRelayConn) SetWriteDeadline(time.Time) error { return nil }
func (c *recordingRelayConn) SetReadDeadline(time.Time) error {
	c.setReadDeadlineCount++
	return nil
}
func (c *recordingRelayConn) CloseRead() error {
	c.closeReadCount++
	return nil
}
func (c *recordingRelayConn) CloseWrite() error {
	c.closeWriteCount++
	return nil
}

func TestShouldUseTrackedTeardownForGOOS(t *testing.T) {
	metadata := &M.Metadata{ConnID: "tun-42"}
	if !shouldUseTrackedTeardownForGOOS("windows", metadata) {
		t.Fatal("expected windows tun connection to use tracked teardown")
	}
	if shouldUseTrackedTeardownForGOOS("linux", metadata) {
		t.Fatal("expected non-windows connection to skip tracked teardown")
	}
	if shouldUseTrackedTeardownForGOOS("windows", &M.Metadata{ConnID: "http-42"}) {
		t.Fatal("expected non-tun conn_id to skip tracked teardown")
	}
}

func TestRelayStreamTrackedTeardownSkipsHalfCloseAndSetsDeadline(t *testing.T) {
	manager := NewDefaultRelayManager()
	dst := newRecordingRelayConn(nil)
	src := newRecordingRelayConn([]byte("hello"))
	tracker := newRelayCompletionTracker(src, dst)
	metadata := &M.Metadata{ConnID: "tun-100"}
	var wg sync.WaitGroup
	wg.Add(1)

	done := make(chan struct{})
	go func() {
		manager.relayStream(dst, src, "client->server", &wg, nil, nil, nil, context.Background(), metadata, tracker)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("relayStream did not complete")
	}

	if src.closeReadCount != 0 {
		t.Fatalf("expected tracked teardown to skip CloseRead, got %d", src.closeReadCount)
	}
	if dst.closeWriteCount != 0 {
		t.Fatalf("expected tracked teardown to skip CloseWrite, got %d", dst.closeWriteCount)
	}
	if dst.setReadDeadlineCount != 1 {
		t.Fatalf("expected tracked teardown to set opposite read deadline once, got %d", dst.setReadDeadlineCount)
	}
}

func TestRelayStreamTUNDirectConservativeTeardownSetsDeadline(t *testing.T) {
	manager := NewDefaultRelayManager()
	dst := newRecordingRelayConn(nil)
	src := newRecordingRelayConn([]byte("hello"))
	metadata := &M.Metadata{ConnID: "tun-101", Route: "RouteDirect"}
	var wg sync.WaitGroup
	wg.Add(1)

	done := make(chan struct{})
	go func() {
		manager.relayStream(dst, src, "client->server", &wg, nil, nil, nil, context.Background(), metadata, nil)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("relayStream did not complete")
	}

	if src.closeReadCount != 0 {
		t.Fatalf("expected TUN direct teardown to skip CloseRead, got %d", src.closeReadCount)
	}
	if dst.closeWriteCount != 0 {
		t.Fatalf("expected TUN direct teardown to skip CloseWrite, got %d", dst.closeWriteCount)
	}
	if dst.setReadDeadlineCount != 1 {
		t.Fatalf("expected TUN direct teardown to set opposite read deadline once, got %d", dst.setReadDeadlineCount)
	}
}

func TestRelayCompletionTrackerClosesBothAfterBothDirectionsComplete(t *testing.T) {
	origin := newRecordingRelayConn(nil)
	remote := newRecordingRelayConn(nil)
	tracker := newRelayCompletionTracker(origin, remote)

	tracker.markDone("client->server", "tun-1")
	if origin.closeCount != 0 || remote.closeCount != 0 {
		t.Fatalf("expected connections to remain open until both directions complete, got origin=%d remote=%d", origin.closeCount, remote.closeCount)
	}

	tracker.markDone("server->client", "tun-1")
	if origin.closeCount != 1 || remote.closeCount != 1 {
		t.Fatalf("expected both connections to close once, got origin=%d remote=%d", origin.closeCount, remote.closeCount)
	}

	tracker.markDone("server->client", "tun-1")
	if origin.closeCount != 1 || remote.closeCount != 1 {
		t.Fatalf("expected closeOnce semantics, got origin=%d remote=%d", origin.closeCount, remote.closeCount)
	}
}

func TestWrapCloseOnceConn_ClosesUnderlyingOnlyOnce(t *testing.T) {
	conn := newRecordingRelayConn(nil)
	wrapped := wrapCloseOnceConn(conn)

	if err := wrapped.Close(); err != nil {
		t.Fatalf("first Close() error = %v", err)
	}
	if err := wrapped.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	if conn.closeCount != 1 {
		t.Fatalf("underlying close count = %d, want 1", conn.closeCount)
	}
}

func TestWrapCloseOnceConn_ForwardsHalfClose(t *testing.T) {
	conn := newRecordingRelayConn(nil)
	wrapped := wrapCloseOnceConn(conn)

	cr, ok := wrapped.(interface{ CloseRead() error })
	if !ok {
		t.Fatal("wrapped conn does not implement CloseRead")
	}
	cw, ok := wrapped.(interface{ CloseWrite() error })
	if !ok {
		t.Fatal("wrapped conn does not implement CloseWrite")
	}

	if err := cr.CloseRead(); err != nil {
		t.Fatalf("CloseRead() error = %v", err)
	}
	if err := cw.CloseWrite(); err != nil {
		t.Fatalf("CloseWrite() error = %v", err)
	}
	if conn.closeReadCount != 1 {
		t.Fatalf("underlying CloseRead count = %d, want 1", conn.closeReadCount)
	}
	if conn.closeWriteCount != 1 {
		t.Fatalf("underlying CloseWrite count = %d, want 1", conn.closeWriteCount)
	}
}

func TestWrapCloseWithPeer_ClosesBothConnectionsOnce(t *testing.T) {
	conn := newRecordingRelayConn(nil)
	peer := newRecordingRelayConn(nil)
	wrapped := wrapCloseWithPeer(wrapCloseOnceConn(conn), peer)

	if err := wrapped.Close(); err != nil {
		t.Fatalf("first Close() error = %v", err)
	}
	if err := wrapped.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	if conn.closeCount != 1 {
		t.Fatalf("underlying close count = %d, want 1", conn.closeCount)
	}
	if peer.closeCount != 1 {
		t.Fatalf("peer close count = %d, want 1", peer.closeCount)
	}
}

func TestWrapCloseWithPeer_ForwardsHalfCloseWithoutClosingPeer(t *testing.T) {
	conn := newRecordingRelayConn(nil)
	peer := newRecordingRelayConn(nil)
	wrapped := wrapCloseWithPeer(wrapCloseOnceConn(conn), peer)

	cr, ok := wrapped.(interface{ CloseRead() error })
	if !ok {
		t.Fatal("wrapped conn does not implement CloseRead")
	}
	cw, ok := wrapped.(interface{ CloseWrite() error })
	if !ok {
		t.Fatal("wrapped conn does not implement CloseWrite")
	}

	if err := cr.CloseRead(); err != nil {
		t.Fatalf("CloseRead() error = %v", err)
	}
	if err := cw.CloseWrite(); err != nil {
		t.Fatalf("CloseWrite() error = %v", err)
	}
	if conn.closeReadCount != 1 {
		t.Fatalf("underlying CloseRead count = %d, want 1", conn.closeReadCount)
	}
	if conn.closeWriteCount != 1 {
		t.Fatalf("underlying CloseWrite count = %d, want 1", conn.closeWriteCount)
	}
	if peer.closeCount != 0 {
		t.Fatalf("peer close count = %d, want 0", peer.closeCount)
	}
}
