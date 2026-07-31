package tunnel

import (
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"aliang.one/nursorgate/common/logger"
	"aliang.one/nursorgate/inbound/tun/adapter"
	"aliang.one/nursorgate/inbound/tun/buffer"
	"aliang.one/nursorgate/processor/statistic"
	"github.com/miekg/dns"

	M "aliang.one/nursorgate/inbound/tun/metadata"
)

// TODO: Port Restricted NAT support.
func (t *Tunnel) handleUDPConn(uc adapter.UDPConn, flow *udpFlowCloser) {
	defer uc.Close()

	id := uc.ID()
	metadata := &M.Metadata{
		Network: M.UDP,
		SrcIP:   parseTCPIPAddress(id.RemoteAddress),
		SrcPort: id.RemotePort,
		DstIP:   parseTCPIPAddress(id.LocalAddress),
		DstPort: id.LocalPort,
	}

	pc, err := t.Dialer().DialUDP(metadata)
	if err != nil {
		logger.Warn(fmt.Sprintf("[UDP] dial %s: %v", metadata.DestinationAddress(), err))
		return
	}
	flow.Add(pc)
	metadata.MidIP, metadata.MidPort = parseNetAddr(pc.LocalAddr())

	// UDP connections are always direct (no MitM/proxy support)
	metadata.Route = "RouteDirect"

	pc = statistic.NewUDPTracker(pc, metadata, t.manager)
	defer pc.Close()

	var remote net.Addr
	if udpAddr := metadata.UDPAddr(); udpAddr != nil {
		remote = udpAddr
	} else {
		remote = metadata.Addr()
	}
	pc = newSymmetricNATPacketConn(pc, metadata)

	// If this is a DNS request (UDP/53), wrap the outbound PacketConn to log queried domain names
	if metadata.DstPort == 53 {
		pc = &dnsLoggingPacketConn{PacketConn: pc}
	}

	logger.Debug(fmt.Sprintf("[UDP] %s <-> %s", metadata.SourceAddress(), metadata.DestinationAddress()))
	pipePacket(uc, pc, remote, t.udpTimeout.Load())
}

type udpFlowCloser struct {
	mu      sync.Mutex
	closed  bool
	closers []io.Closer
}

func newUDPFlowCloser(closers ...io.Closer) *udpFlowCloser {
	return &udpFlowCloser{closers: closers}
}

func (f *udpFlowCloser) Add(closer io.Closer) {
	if closer == nil {
		return
	}

	f.mu.Lock()
	if !f.closed {
		f.closers = append(f.closers, closer)
		f.mu.Unlock()
		return
	}
	f.mu.Unlock()
	_ = closer.Close()
}

func (f *udpFlowCloser) Close() {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return
	}
	f.closed = true
	closers := f.closers
	f.closers = nil
	f.mu.Unlock()

	for _, closer := range closers {
		if closer != nil {
			_ = closer.Close()
		}
	}
}

// dnsLoggingPacketConn logs DNS query names for UDP/53 traffic by decoding the
// DNS message payload on WriteTo (client -> upstream DNS server).
type dnsLoggingPacketConn struct {
	net.PacketConn
}

func (d *dnsLoggingPacketConn) WriteTo(b []byte, addr net.Addr) (int, error) {
	// Best-effort parse DNS query and log QNAMEs
	// Only attempt when payload looks like a DNS message (at least header length)
	if len(b) >= 12 {
		var m dns.Msg
		if err := m.Unpack(b); err == nil {
			// Log all questions (usually 1)
			for _, q := range m.Question {
				logger.Debug(fmt.Sprintf("[DNS] query: %s %s", q.Name, dns.TypeToString[q.Qtype]))
			}
		}
	}
	return d.PacketConn.WriteTo(b, addr)
}

func pipePacket(origin, remote net.PacketConn, to net.Addr, timeout time.Duration) {
	wg := sync.WaitGroup{}
	wg.Add(2)

	go unidirectionalPacketStream(remote, origin, to, "origin->remote", &wg, timeout)
	go unidirectionalPacketStream(origin, remote, nil, "remote->origin", &wg, timeout)

	wg.Wait()
}

func unidirectionalPacketStream(dst, src net.PacketConn, to net.Addr, dir string, wg *sync.WaitGroup, timeout time.Duration) {
	defer wg.Done()
	if err := copyPacketData(dst, src, to, timeout); err != nil {
		//log.Debugf("[UDP] copy data for %s: %v", dir, err)
	}
}

func copyPacketData(dst, src net.PacketConn, to net.Addr, timeout time.Duration) error {
	buf := buffer.Get(buffer.MaxSegmentSize)
	defer buffer.Put(buf)

	for {
		src.SetReadDeadline(time.Now().Add(timeout))
		n, _, err := src.ReadFrom(buf)
		if ne, ok := err.(net.Error); ok && ne.Timeout() {
			return nil /* ignore I/O timeout */
		} else if err == io.EOF {
			return nil /* ignore EOF */
		} else if err != nil {
			return err
		}

		if _, err = dst.WriteTo(buf[:n], to); err != nil {
			return err
		}
		dst.SetReadDeadline(time.Now().Add(timeout))
	}
}

type symmetricNATPacketConn struct {
	net.PacketConn
	src string
	dst string
}

func newSymmetricNATPacketConn(pc net.PacketConn, metadata *M.Metadata) *symmetricNATPacketConn {
	return &symmetricNATPacketConn{
		PacketConn: pc,
		src:        metadata.SourceAddress(),
		dst:        metadata.DestinationAddress(),
	}
}

func (pc *symmetricNATPacketConn) ReadFrom(p []byte) (int, net.Addr, error) {
	for {
		n, from, err := pc.PacketConn.ReadFrom(p)

		if from != nil && from.String() != pc.dst {
			//log.Warnf("[UDP] symmetric NAT %s->%s: drop packet from %s", pc.src, pc.dst, from)
			continue
		}

		return n, from, err
	}
}
