package singleinstance

import (
	"net"
	"syscall"
	"testing"
)

func TestAcquireAddrAcquiresFreePort(t *testing.T) {
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve test port: %v", err)
	}
	addr := probe.Addr().String()
	_ = probe.Close()

	listener, acquired, err := AcquireAddr(addr)
	if err != nil {
		t.Fatalf("AcquireAddr returned error: %v", err)
	}
	if !acquired {
		t.Fatalf("AcquireAddr(%q) = acquired false, want true", addr)
	}
	if listener == nil {
		t.Fatalf("AcquireAddr(%q) returned nil listener", addr)
	}
	_ = listener.Close()
}

func TestAcquireAddrReturnsDuplicateWhenPortIsOccupied(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve occupied port: %v", err)
	}
	defer occupied.Close()

	listener, acquired, err := AcquireAddr(occupied.Addr().String())
	if err != nil {
		t.Fatalf("AcquireAddr returned error: %v", err)
	}
	if acquired {
		t.Fatalf("AcquireAddr(%q) = acquired true, want false", occupied.Addr().String())
	}
	if listener != nil {
		t.Fatalf("AcquireAddr(%q) returned listener %+v, want nil", occupied.Addr().String(), listener)
	}
}

func TestIsAddressInUseError(t *testing.T) {
	testCases := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "unix eaddrinuse",
			err:  syscall.EADDRINUSE,
			want: true,
		},
		{
			name: "windows wsaeaddrinuse",
			err:  syscall.Errno(10048),
			want: true,
		},
		{
			name: "other errno",
			err:  syscall.Errno(5),
			want: false,
		},
		{
			name: "nil",
			err:  nil,
			want: false,
		},
	}

	for _, tc := range testCases {
		if got := isAddressInUseError(tc.err); got != tc.want {
			t.Fatalf("%s: isAddressInUseError(%v) = %v, want %v", tc.name, tc.err, got, tc.want)
		}
	}
}
