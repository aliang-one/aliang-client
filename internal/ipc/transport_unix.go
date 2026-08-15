//go:build darwin || linux

package ipc

import (
	"fmt"
	"net"
	"os"
	"runtime"
)

// unixTransport implements Transport using Unix Domain Socket.
type unixTransport struct {
	path string
}

// NewTransport creates a new Unix Domain Socket transport.
func NewTransport() Transport {
	return &unixTransport{path: SocketPath()}
}

func (t *unixTransport) Listen() (net.Listener, error) {
	// Remove existing socket file
	os.Remove(t.path)

	// Create socket
	listener, err := net.Listen("unix", t.path)
	if err != nil {
		return nil, fmt.Errorf("failed to listen on unix socket %s: %w", t.path, err)
	}

	// Set socket file permissions to 0666 (all users can read/write)
	if err := os.Chmod(t.path, 0666); err != nil {
		listener.Close()
		return nil, fmt.Errorf("failed to set socket permissions: %w", err)
	}

	return listener, nil
}

func (t *unixTransport) Dial() (net.Conn, error) {
	conn, err := net.Dial("unix", t.path)
	if err != nil {
		return nil, fmt.Errorf("failed to dial unix socket %s: %w", t.path, err)
	}
	return conn, nil
}

func (t *unixTransport) SocketPath() string {
	return t.path
}

// EnsureRunDir ensures the parent directory of the socket exists.
func EnsureRunDir() error {
	runDir := "/var/run"
	if runtime.GOOS == "linux" {
		runDir = "/run"
	}
	return os.MkdirAll(runDir, 0755)
}
