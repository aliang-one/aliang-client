package tun

import (
	"sync"
	"time"

	"aliang.one/nursorgate/inbound/tun/adapter"
	"aliang.one/nursorgate/inbound/tun/option"

	glog "github.com/sagernet/gvisor/pkg/log"
	"github.com/sagernet/gvisor/pkg/tcpip"
	"github.com/sagernet/gvisor/pkg/tcpip/adapters/gonet"
	"github.com/sagernet/gvisor/pkg/tcpip/header"
	"github.com/sagernet/gvisor/pkg/tcpip/stack"
	"github.com/sagernet/gvisor/pkg/tcpip/transport/tcp"
	"github.com/sagernet/gvisor/pkg/waiter"
)

const (
	// defaultWndSize if set to zero, the default
	// receive window buffer size is used instead.
	defaultWndSize = 0

	// maxConnAttempts specifies the maximum number
	// of in-flight tcp connection attempts.
	maxConnAttempts = 2 << 10

	// tcpKeepaliveCount is the maximum number of
	// TCP keep-alive probes to send before giving up
	// and killing the connection if no response is
	// obtained from the other end.
	// ✅ Increased from 3 to 5 for better stability
	tcpKeepaliveCount = 5

	// tcpKeepaliveIdle specifies the time a connection
	// must remain idle before the first TCP keepalive
	// packet is sent. Once this time is reached,
	// tcpKeepaliveInterval option is used instead.
	// ✅ Increased to 60s (was 15s) to better match NAT gateway timeouts (60-300s)
	// This prevents false positives on slow connections
	tcpKeepaliveIdle = 60 * time.Second

	// tcpKeepaliveInterval specifies the interval
	// time between sending TCP keepalive packets.
	// ✅ Increased to 15s (was 5s) for less aggressive probing
	// Total timeout: 60s idle + 5×15s = 135s (more forgiving than 30s)
	tcpKeepaliveInterval = 15 * time.Second
)

func withTCPHandler(handle func(adapter.TCPConn)) option.Option {
	return func(s *stack.Stack) error {
		tcpForwarder := tcp.NewForwarder(s, defaultWndSize, maxConnAttempts, func(r *tcp.ForwarderRequest) {
			var (
				wq  = &waiter.Queue{}
				ep  tcpip.Endpoint
				err tcpip.Error
				id  = r.ID()
			)

			defer func() {
				if err != nil {
					glog.Debugf("forward tcp request: %s:%d->%s:%d: %s",
						id.RemoteAddress, id.RemotePort, id.LocalAddress, id.LocalPort, err)
				}
			}()

			// Perform a TCP three-way handshake.
			ep, err = r.CreateEndpoint(wq)
			if err != nil {
				// RST: prevent potential half-open TCP connection leak.
				r.Complete(true)
				return
			}
			defer r.Complete(false)

			err = setSocketOptions(s, ep)

			conn := newTCPConn(gonet.NewTCPConn(wq, ep), id)
			handle(conn)
		})
		s.SetTransportProtocolHandler(tcp.ProtocolNumber, tcpForwarder.HandlePacket)
		return nil
	}
}

func setSocketOptions(s *stack.Stack, ep tcpip.Endpoint) tcpip.Error {
	{ /* TCP keepalive options */
		ep.SocketOptions().SetKeepAlive(true)

		idle := tcpip.KeepaliveIdleOption(tcpKeepaliveIdle)
		if err := ep.SetSockOpt(&idle); err != nil {
			return err
		}

		interval := tcpip.KeepaliveIntervalOption(tcpKeepaliveInterval)
		if err := ep.SetSockOpt(&interval); err != nil {
			return err
		}

		if err := ep.SetSockOptInt(tcpip.KeepaliveCountOption, tcpKeepaliveCount); err != nil {
			return err
		}
	}
	{ /* TCP recv/send buffer size */
		var ss tcpip.TCPSendBufferSizeRangeOption
		if err := s.TransportProtocolOption(header.TCPProtocolNumber, &ss); err == nil {
			ep.SocketOptions().SetSendBufferSize(int64(ss.Default), false)
		}

		var rs tcpip.TCPReceiveBufferSizeRangeOption
		if err := s.TransportProtocolOption(header.TCPProtocolNumber, &rs); err == nil {
			ep.SocketOptions().SetReceiveBufferSize(int64(rs.Default), false)
		}
	}
	return nil
}

type tcpConnControl interface {
	Close() error
	CloseRead() error
	CloseWrite() error
}

type tcpConn struct {
	*gonet.TCPConn

	control tcpConnControl
	id      stack.TransportEndpointID

	mu          sync.Mutex
	closed      bool
	readClosed  bool
	writeClosed bool
}

func newTCPConn(conn *gonet.TCPConn, id stack.TransportEndpointID) *tcpConn {
	return &tcpConn{
		TCPConn:     conn,
		control:     conn,
		id:          id,
		closed:      false,
		readClosed:  false,
		writeClosed: false,
	}
}

func (c *tcpConn) controller() tcpConnControl {
	if c == nil {
		return nil
	}
	if c.control != nil {
		return c.control
	}
	return c.TCPConn
}

func (c *tcpConn) ID() *stack.TransportEndpointID {
	return &c.id
}

func (c *tcpConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return nil
	}

	controller := c.controller()
	if controller == nil {
		c.closed = true
		c.readClosed = true
		c.writeClosed = true
		return nil
	}

	err := controller.Close()
	if err == nil {
		c.closed = true
		c.readClosed = true
		c.writeClosed = true
	}
	return err
}

func (c *tcpConn) CloseRead() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed || c.readClosed {
		return nil
	}

	controller := c.controller()
	if controller == nil {
		c.readClosed = true
		return nil
	}

	err := controller.CloseRead()
	if err == nil {
		c.readClosed = true
	}
	return err
}

func (c *tcpConn) CloseWrite() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed || c.writeClosed {
		return nil
	}

	controller := c.controller()
	if controller == nil {
		c.writeClosed = true
		return nil
	}

	err := controller.CloseWrite()
	if err == nil {
		c.writeClosed = true
	}
	return err
}
