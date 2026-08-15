package aliang

import (
	"net"
	"time"
)

// PooledConn represents a pooled TLS connection
type PooledConn struct {
	Conn     net.Conn
	LastUsed time.Time
}

// ConnectionKey represents a connection pool key
type ConnectionKey struct {
	Host string
	Port string
}

// String returns the string representation of the connection key
func (ck ConnectionKey) String() string {
	return ck.Host + ":" + ck.Port
}
