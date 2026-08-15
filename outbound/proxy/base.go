package proxy

import (
	"context"
	"errors"
	"net"

	"aliang.one/nursorgate/outbound/proxy/proto"

	M "aliang.one/nursorgate/inbound/tun/metadata"
)

var _ Proxy = (*Base)(nil)

type Base struct {
	Address  string
	Protocol proto.Proto
}

func (b *Base) Addr() string {
	return b.Address
}

func (b *Base) Proto() proto.Proto {
	return b.Protocol
}

func (b *Base) DialContext(context.Context, *M.Metadata) (net.Conn, error) {
	return nil, errors.ErrUnsupported
}

func (b *Base) DialUDP(*M.Metadata) (net.PacketConn, error) {
	return nil, errors.ErrUnsupported
}
