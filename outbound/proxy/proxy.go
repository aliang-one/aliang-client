// Package proxy provides implementations of proxy protocols.
package proxy

import (
	"context"
	"net"
	"time"

	M "aliang.one/nursorgate/inbound/tun/metadata"
)

const (
	tcpConnectTimeout = 5 * time.Second
)

var _defaultDialer Dialer = &Base{}

// SetDialer sets default Dialer.
func SetDialer(d Dialer) {
	_defaultDialer = d
}

// Dial uses default Dialer to dial TCP.
func Dial(metadata *M.Metadata) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), tcpConnectTimeout)
	defer cancel()
	return _defaultDialer.DialContext(ctx, metadata)
}

// DialContext uses default Dialer to dial TCP with context.
func DialContext(ctx context.Context, metadata *M.Metadata) (net.Conn, error) {
	return _defaultDialer.DialContext(ctx, metadata)
}

// DialUDP uses default Dialer to dial UDP.
func DialUDP(metadata *M.Metadata) (net.PacketConn, error) {
	return _defaultDialer.DialUDP(metadata)
}
