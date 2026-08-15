package singleinstance

import (
	"errors"
	"fmt"
	"net"
	"syscall"
)

const GuardAddr = "127.0.0.1:56430"

// Acquire reserves the shared local TCP port used to prevent duplicate app launches.
func Acquire() (net.Listener, bool, error) {
	return AcquireAddr(GuardAddr)
}

// AcquireAddr reserves the provided local TCP port and reports whether the caller
// acquired the single-instance guard.
func AcquireAddr(addr string) (net.Listener, bool, error) {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		if isAddressInUseError(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("failed to acquire single-instance guard on %s: %w", addr, err)
	}

	return listener, true, nil
}

func isAddressInUseError(err error) bool {
	if err == nil {
		return false
	}

	if errors.Is(err, syscall.EADDRINUSE) {
		return true
	}

	var errno syscall.Errno
	if errors.As(err, &errno) {
		return errno == syscall.Errno(10048)
	}

	return false
}
