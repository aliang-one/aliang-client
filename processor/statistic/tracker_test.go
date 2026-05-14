package statistic

import (
	"io"
	"net"
	"testing"
	"time"

	M "aliang.one/nursorgate/inbound/tun/metadata"
	"go.uber.org/atomic"
)

type closeCountingConn struct {
	closeCount int
	readData   []byte
	written    int
}

func (c *closeCountingConn) Read(p []byte) (int, error) {
	if len(c.readData) == 0 {
		return 0, io.EOF
	}
	n := copy(p, c.readData)
	c.readData = c.readData[n:]
	return n, nil
}
func (c *closeCountingConn) Write(p []byte) (int, error)      { c.written += len(p); return len(p), nil }
func (c *closeCountingConn) Close() error                     { c.closeCount++; return nil }
func (c *closeCountingConn) LocalAddr() net.Addr              { return &net.TCPAddr{} }
func (c *closeCountingConn) RemoteAddr() net.Addr             { return &net.TCPAddr{} }
func (c *closeCountingConn) SetDeadline(time.Time) error      { return nil }
func (c *closeCountingConn) SetReadDeadline(time.Time) error  { return nil }
func (c *closeCountingConn) SetWriteDeadline(time.Time) error { return nil }

type packetConnStub struct {
	closeCount int
	readData   []byte
	written    int
}

func (c *packetConnStub) ReadFrom(p []byte) (int, net.Addr, error) {
	if len(c.readData) == 0 {
		return 0, &net.UDPAddr{}, io.EOF
	}
	n := copy(p, c.readData)
	c.readData = c.readData[n:]
	return n, &net.UDPAddr{}, nil
}
func (c *packetConnStub) WriteTo(p []byte, _ net.Addr) (int, error) {
	c.written += len(p)
	return len(p), nil
}
func (c *packetConnStub) Close() error                     { c.closeCount++; return nil }
func (c *packetConnStub) LocalAddr() net.Addr              { return &net.UDPAddr{} }
func (c *packetConnStub) SetDeadline(time.Time) error      { return nil }
func (c *packetConnStub) SetReadDeadline(time.Time) error  { return nil }
func (c *packetConnStub) SetWriteDeadline(time.Time) error { return nil }

func newTestManager() *Manager {
	return &Manager{
		uploadTemp:    atomic.NewInt64(0),
		downloadTemp:  atomic.NewInt64(0),
		uploadBlip:    atomic.NewInt64(0),
		downloadBlip:  atomic.NewInt64(0),
		uploadTotal:   atomic.NewInt64(0),
		downloadTotal: atomic.NewInt64(0),
	}
}

func TestTCPTrackerClose_IsIdempotent(t *testing.T) {
	manager := newTestManager()
	conn := &closeCountingConn{}
	tracked := NewTCPTracker(conn, &M.Metadata{}, manager)

	if err := tracked.Close(); err != nil {
		t.Fatalf("first Close() error = %v", err)
	}
	if err := tracked.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	if conn.closeCount != 1 {
		t.Fatalf("underlying close count = %d, want 1", conn.closeCount)
	}
}

func TestTCPTracker_DirectRouteDoesNotContributeStatistics(t *testing.T) {
	manager := newTestManager()
	conn := &closeCountingConn{readData: []byte("hello")}
	tracked := NewTCPTracker(conn, &M.Metadata{Route: "RouteDirect"}, manager)

	buf := make([]byte, 8)
	if _, err := tracked.Read(buf); err != nil && err != io.EOF {
		t.Fatalf("Read() error = %v", err)
	}
	if _, err := tracked.Write([]byte("world")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	snapshot := manager.Snapshot()
	if got := snapshot.UploadTotal; got != 0 {
		t.Fatalf("UploadTotal = %d, want 0", got)
	}
	if got := snapshot.DownloadTotal; got != 0 {
		t.Fatalf("DownloadTotal = %d, want 0", got)
	}
	if got := len(snapshot.Connections); got != 0 {
		t.Fatalf("tracked connections = %d, want 0", got)
	}
}

func TestTCPTracker_NonDirectRouteContributesStatistics(t *testing.T) {
	manager := newTestManager()
	conn := &closeCountingConn{readData: []byte("hello")}
	tracked := NewTCPTracker(conn, &M.Metadata{Route: "RouteToALiang"}, manager)

	buf := make([]byte, 8)
	if _, err := tracked.Read(buf); err != nil && err != io.EOF {
		t.Fatalf("Read() error = %v", err)
	}
	if _, err := tracked.Write([]byte("world")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	snapshot := manager.Snapshot()
	if got := snapshot.UploadTotal; got != 5 {
		t.Fatalf("UploadTotal = %d, want 5", got)
	}
	if got := snapshot.DownloadTotal; got != 5 {
		t.Fatalf("DownloadTotal = %d, want 5", got)
	}
	if got := len(snapshot.Connections); got != 1 {
		t.Fatalf("tracked connections = %d, want 1", got)
	}
	if _, ok := snapshot.ByRoute["RouteToALiang"]; !ok {
		t.Fatalf("expected RouteToALiang stats to exist")
	}
}

func TestManager_CloseTrackedConnectionsByMetadata_ClosesMatchingConnections(t *testing.T) {
	manager := newTestManager()
	conn := &closeCountingConn{}
	_ = NewTCPTracker(conn, &M.Metadata{
		Route:    "RouteToALiang",
		HostName: "api.cursor.sh",
		ConnID:   "conn-1",
	}, manager)

	closed := manager.CloseTrackedConnectionsByMetadata(func(metadata *M.Metadata) bool {
		return metadata != nil && metadata.Route == "RouteToALiang" && metadata.HostName == "api.cursor.sh"
	})
	if closed != 1 {
		t.Fatalf("closed connections = %d, want 1", closed)
	}
	if conn.closeCount != 1 {
		t.Fatalf("underlying close count = %d, want 1", conn.closeCount)
	}
}

func TestManager_CloseTrackedConnectionsByMetadata_IgnoresNonMatchingConnections(t *testing.T) {
	manager := newTestManager()
	conn := &closeCountingConn{}
	_ = NewTCPTracker(conn, &M.Metadata{
		Route:    "RouteToALiang",
		HostName: "api.openai.com",
		ConnID:   "conn-2",
	}, manager)

	closed := manager.CloseTrackedConnectionsByMetadata(func(metadata *M.Metadata) bool {
		return metadata != nil && metadata.HostName == "api.cursor.sh"
	})
	if closed != 0 {
		t.Fatalf("closed connections = %d, want 0", closed)
	}
	if conn.closeCount != 0 {
		t.Fatalf("underlying close count = %d, want 0", conn.closeCount)
	}
}

func TestUDPTracker_DirectRouteDoesNotContributeStatistics(t *testing.T) {
	manager := newTestManager()
	conn := &packetConnStub{readData: []byte("ping")}
	tracked := NewUDPTracker(conn, &M.Metadata{Route: "RouteDirect"}, manager)

	buf := make([]byte, 8)
	if _, _, err := tracked.ReadFrom(buf); err != nil && err != io.EOF {
		t.Fatalf("ReadFrom() error = %v", err)
	}
	if _, err := tracked.WriteTo([]byte("pong"), &net.UDPAddr{}); err != nil {
		t.Fatalf("WriteTo() error = %v", err)
	}

	snapshot := manager.Snapshot()
	if got := snapshot.UploadTotal; got != 0 {
		t.Fatalf("UploadTotal = %d, want 0", got)
	}
	if got := snapshot.DownloadTotal; got != 0 {
		t.Fatalf("DownloadTotal = %d, want 0", got)
	}
	if got := len(snapshot.Connections); got != 0 {
		t.Fatalf("tracked connections = %d, want 0", got)
	}
}
