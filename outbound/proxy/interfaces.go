// Package proxy provides implementations of proxy protocols.
package proxy

import (
	"context"
	"net"

	"aliang.one/nursorgate/inbound/tun/metadata"
	"aliang.one/nursorgate/outbound/proxy/proto"
)

// Dialer interface for dialing connections
type Dialer interface {
	DialContext(context.Context, *metadata.Metadata) (net.Conn, error)
	DialUDP(*metadata.Metadata) (net.PacketConn, error)
}

// Proxy interface represents a proxy protocol implementation
type Proxy interface {
	Dialer
	Addr() string
	Proto() proto.Proto
}
