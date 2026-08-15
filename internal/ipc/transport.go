package ipc

import "net"

// Transport defines the interface for IPC communication.
type Transport interface {
	// Listen creates a listener for incoming connections.
	Listen() (net.Listener, error)
	// Dial connects to the server.
	Dial() (net.Conn, error)
	// SocketPath returns the file system path for the socket.
	SocketPath() string
}
