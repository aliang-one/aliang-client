package tcp

import (
	"strings"
	"testing"
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
