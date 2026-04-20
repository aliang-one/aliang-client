package tcp

import (
	"net"
	"strings"
	"testing"
	"time"
)

func TestWrappedConn_ForwardsHalfClose(t *testing.T) {
	conn := newRecordingRelayConn(nil)
	wrapped := &WrappedConn{Conn: conn}

	if err := wrapped.CloseRead(); err != nil {
		t.Fatalf("CloseRead() error = %v", err)
	}
	if err := wrapped.CloseWrite(); err != nil {
		t.Fatalf("CloseWrite() error = %v", err)
	}
	if conn.closeReadCount != 1 {
		t.Fatalf("underlying CloseRead count = %d, want 1", conn.closeReadCount)
	}
	if conn.closeWriteCount != 1 {
		t.Fatalf("underlying CloseWrite count = %d, want 1", conn.closeWriteCount)
	}
}

func TestWrappedConnConnectionDiagnosticString(t *testing.T) {
	conn := newRecordingRelayConn(nil)
	wrapped := &WrappedConn{Conn: conn, Buf: []byte("hello")}
	wrapped.readOffset = 2

	got := wrapped.ConnectionDiagnosticString()
	checks := []string{
		"type=*tcp.WrappedConn",
		"wrapped=true",
		"buf=5",
		"read_offset=2",
		"can_close_read=true",
		"can_close_write=true",
	}
	for _, check := range checks {
		if !strings.Contains(got, check) {
			t.Fatalf("diagnostic %q missing %q", got, check)
		}
	}
}

func TestWrappedConnWithoutUnderlyingDoesNotPanic(t *testing.T) {
	wrapped := &WrappedConn{}

	if _, err := wrapped.Read(make([]byte, 1)); err != net.ErrClosed {
		t.Fatalf("Read() error = %v, want %v", err, net.ErrClosed)
	}
	if _, err := wrapped.Write([]byte("x")); err != net.ErrClosed {
		t.Fatalf("Write() error = %v, want %v", err, net.ErrClosed)
	}
	if err := wrapped.SetReadDeadline(time.Now()); err != net.ErrClosed {
		t.Fatalf("SetReadDeadline() error = %v, want %v", err, net.ErrClosed)
	}
	if err := wrapped.SetWriteDeadline(time.Now()); err != net.ErrClosed {
		t.Fatalf("SetWriteDeadline() error = %v, want %v", err, net.ErrClosed)
	}
	if err := wrapped.SetDeadline(time.Now()); err != net.ErrClosed {
		t.Fatalf("SetDeadline() error = %v, want %v", err, net.ErrClosed)
	}
	if got := wrapped.String(); got != "" {
		t.Fatalf("String() = %q, want empty string", got)
	}
}
