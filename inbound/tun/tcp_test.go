package tun

import (
	"errors"
	"testing"

	"github.com/sagernet/gvisor/pkg/tcpip/stack"
)

type fakeTCPConnControl struct {
	closeCount      int
	closeReadCount  int
	closeWriteCount int

	closeErr      error
	closeReadErr  error
	closeWriteErr error
}

func (f *fakeTCPConnControl) Close() error {
	f.closeCount++
	return f.closeErr
}

func (f *fakeTCPConnControl) CloseRead() error {
	f.closeReadCount++
	return f.closeReadErr
}

func (f *fakeTCPConnControl) CloseWrite() error {
	f.closeWriteCount++
	return f.closeWriteErr
}

func TestTCPConnCloseMethods_SuccessBecomesIdempotent(t *testing.T) {
	control := &fakeTCPConnControl{}
	conn := &tcpConn{
		control: control,
		id:      stack.TransportEndpointID{},
	}

	if err := conn.CloseRead(); err != nil {
		t.Fatalf("first CloseRead() error = %v", err)
	}
	if err := conn.CloseRead(); err != nil {
		t.Fatalf("second CloseRead() error = %v", err)
	}
	if control.closeReadCount != 1 {
		t.Fatalf("CloseRead count = %d, want 1", control.closeReadCount)
	}

	if err := conn.CloseWrite(); err != nil {
		t.Fatalf("first CloseWrite() error = %v", err)
	}
	if err := conn.CloseWrite(); err != nil {
		t.Fatalf("second CloseWrite() error = %v", err)
	}
	if control.closeWriteCount != 1 {
		t.Fatalf("CloseWrite count = %d, want 1", control.closeWriteCount)
	}

	if err := conn.Close(); err != nil {
		t.Fatalf("first Close() error = %v", err)
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	if control.closeCount != 1 {
		t.Fatalf("Close count = %d, want 1", control.closeCount)
	}
}

func TestTCPConnCloseMethods_ErrorDoesNotMarkClosed(t *testing.T) {
	control := &fakeTCPConnControl{
		closeReadErr: errors.New("read close failed"),
	}
	conn := &tcpConn{
		control: control,
		id:      stack.TransportEndpointID{},
	}

	if err := conn.CloseRead(); err == nil {
		t.Fatal("first CloseRead() error = nil, want non-nil")
	}
	if control.closeReadCount != 1 {
		t.Fatalf("CloseRead count after first error = %d, want 1", control.closeReadCount)
	}

	control.closeReadErr = nil
	if err := conn.CloseRead(); err != nil {
		t.Fatalf("second CloseRead() error = %v", err)
	}
	if control.closeReadCount != 2 {
		t.Fatalf("CloseRead count after retry = %d, want 2", control.closeReadCount)
	}

	if err := conn.CloseRead(); err != nil {
		t.Fatalf("third CloseRead() error = %v", err)
	}
	if control.closeReadCount != 2 {
		t.Fatalf("CloseRead count after success repeat = %d, want 2", control.closeReadCount)
	}
}
