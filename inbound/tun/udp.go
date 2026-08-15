package tun

import (
	glog "github.com/sagernet/gvisor/pkg/log"
	"github.com/sagernet/gvisor/pkg/tcpip/adapters/gonet"
	"github.com/sagernet/gvisor/pkg/tcpip/stack"
	"github.com/sagernet/gvisor/pkg/tcpip/transport/udp"
	"github.com/sagernet/gvisor/pkg/waiter"

	"aliang.one/nursorgate/inbound/tun/adapter"
	"aliang.one/nursorgate/inbound/tun/option"
)

func withUDPHandler(handle func(adapter.UDPConn)) option.Option {
	return func(s *stack.Stack) error {
		udpForwarder := udp.NewForwarder(s, func(r *udp.ForwarderRequest) {
			var (
				wq = &waiter.Queue{}
				id = r.ID()
			)
			ep, err := r.CreateEndpoint(wq)
			if err != nil {
				glog.Debugf("forward udp request: %s:%d->%s:%d: %s",
					id.RemoteAddress, id.RemotePort, id.LocalAddress, id.LocalPort, err)
				return
			}

			conn := &udpConn{
				UDPConn: gonet.NewUDPConn(wq, ep),
				id:      id,
			}
			handle(conn)
		})
		s.SetTransportProtocolHandler(udp.ProtocolNumber, udpForwarder.HandlePacket)
		return nil
	}
}

type udpConn struct {
	*gonet.UDPConn
	id stack.TransportEndpointID
}

func (c *udpConn) ID() *stack.TransportEndpointID {
	return &c.id
}
