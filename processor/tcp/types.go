package tcp

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"time"

	"aliang.one/nursorgate/common/logger"
)

// Metadata contains connection metadata for TCP handlers.
// It flows through the entire connection lifecycle and is used for:
// - Routing decisions (domain-based, IP-based)
// - Statistics tracking
// - Logging and debugging
// - Context propagation
type Metadata struct {
	// Connection identification
	Network  string     // "tcp"
	SrcIP    netip.Addr // Source IP (client)
	SrcPort  uint16     // Source port (client)
	DstIP    netip.Addr // Destination IP (target server)
	DstPort  uint16     // Destination port (target server)
	MidIP    netip.Addr // Middle IP (our local address when connecting out)
	MidPort  uint16     // Middle port (our local port when connecting out)
	HostName string     // Extracted domain name (from SNI or HTTP headers)
}

// SourceAddress returns the source IP and port as a string
func (m *Metadata) SourceAddress() string {
	if m.SrcPort == 0 {
		return m.SrcIP.String()
	}
	return net.JoinHostPort(m.SrcIP.String(), string(rune(m.SrcPort)))
}

// DestinationAddress returns the destination IP and port as a string
func (m *Metadata) DestinationAddress() string {
	if m.DstPort == 0 {
		return m.DstIP.String()
	}
	return net.JoinHostPort(m.DstIP.String(), string(rune(m.DstPort)))
}

// DestinationAddrPort returns destination as netip.AddrPort
func (m *Metadata) DestinationAddrPort() netip.AddrPort {
	return netip.AddrPortFrom(m.DstIP, m.DstPort)
}

// SourceAddrPort returns source as netip.AddrPort
func (m *Metadata) SourceAddrPort() netip.AddrPort {
	return netip.AddrPortFrom(m.SrcIP, m.SrcPort)
}

// TCPAddr returns destination as *net.TCPAddr for net.Dialer compatibility
func (m *Metadata) TCPAddr() *net.TCPAddr {
	return &net.TCPAddr{
		IP:   m.DstIP.AsSlice(),
		Port: int(m.DstPort),
	}
}

// WrappedConn preserves the TLS ClientHello buffer when SNI extraction
// requires pre-reading data from the connection. This is necessary because
// SNI extraction reads the TLS ClientHello from the connection, but we need
// to provide that same data to the TLS server during handshake.
type WrappedConn struct {
	net.Conn
	Buf               []byte // Buffered data from initial read (TLS ClientHello)
	readOffset        int    // Current position in buffer
	passThroughLogged bool
}

// Read implements net.Conn.Read with buffer support.
// If there's buffered data from initial read, return that first.
// Once buffered data is exhausted, read from underlying connection.
func (w *WrappedConn) Read(p []byte) (int, error) {
	if w == nil || w.Conn == nil {
		return 0, net.ErrClosed
	}

	// If we have buffered data, serve that first
	if len(w.Buf) > w.readOffset {
		n := copy(p, w.Buf[w.readOffset:])
		w.readOffset += n
		return n, nil
	}
	if !w.passThroughLogged {
		w.passThroughLogged = true
		logger.Debug(fmt.Sprintf(
			"[WRAPPED CONN] passthrough begin underlying_type=%T buffered=%d consumed=%d local=%v remote=%v",
			w.Conn,
			len(w.Buf),
			w.readOffset,
			w.LocalAddr(),
			w.RemoteAddr(),
		))
	}
	// All buffered data consumed, read from underlying connection
	return w.Conn.Read(p)
}

func (w *WrappedConn) Write(p []byte) (int, error) {
	if w == nil || w.Conn == nil {
		return 0, net.ErrClosed
	}
	return w.Conn.Write(p)
}

func (w *WrappedConn) Close() error {
	if w == nil || w.Conn == nil {
		return nil
	}
	return w.Conn.Close()
}

func (w *WrappedConn) CloseRead() error {
	if w == nil || w.Conn == nil {
		return nil
	}
	if cr, ok := w.Conn.(interface{ CloseRead() error }); ok {
		return cr.CloseRead()
	}
	return errors.New("CloseRead is not implemented")
}

func (w *WrappedConn) CloseWrite() error {
	if w == nil || w.Conn == nil {
		return nil
	}
	if cw, ok := w.Conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return errors.New("CloseWrite is not implemented")
}

func (w *WrappedConn) LocalAddr() net.Addr {
	if w == nil || w.Conn == nil {
		return nil
	}
	return w.Conn.LocalAddr()
}

func (w *WrappedConn) RemoteAddr() net.Addr {
	if w == nil || w.Conn == nil {
		return nil
	}
	return w.Conn.RemoteAddr()
}

func (w *WrappedConn) SetDeadline(t time.Time) error {
	if w == nil || w.Conn == nil {
		return net.ErrClosed
	}
	return w.Conn.SetDeadline(t)
}

func (w *WrappedConn) SetReadDeadline(t time.Time) error {
	if w == nil || w.Conn == nil {
		return net.ErrClosed
	}
	return w.Conn.SetReadDeadline(t)
}

func (w *WrappedConn) SetWriteDeadline(t time.Time) error {
	if w == nil || w.Conn == nil {
		return net.ErrClosed
	}
	return w.Conn.SetWriteDeadline(t)
}

func (w *WrappedConn) ConnectionDiagnosticString() string {
	if w == nil {
		return "type=*tcp.WrappedConn nil"
	}

	_, canCloseRead := any(w).(interface{ CloseRead() error })
	_, canCloseWrite := any(w).(interface{ CloseWrite() error })

	return fmt.Sprintf(
		"type=%T wrapped=true underlying=%T buf=%d read_offset=%d can_close_read=%t can_close_write=%t",
		w,
		w.Conn,
		len(w.Buf),
		w.readOffset,
		canCloseRead,
		canCloseWrite,
	)
}

// String representation for logging
func (w *WrappedConn) String() string {
	if w == nil || w.Conn == nil || w.Conn.RemoteAddr() == nil {
		return ""
	}
	return w.Conn.RemoteAddr().String()
}

// Constants for error detection and handling
const (
	// DoH (DNS-over-HTTPS) provider domains
	DoHProviderGoogle      = "dns.google"
	DoHProviderCloudflare  = "cloudflare-dns.com"
	DoHProviderOpenDNS     = "doh.opendns.com"
	DoHProviderQuad9       = "doh.quad9.net"
	DoHProviderCleanBrowse = "doh.cleanbrowsing.org"
	DoHProviderGoogle8     = "8.8.8.8"
	DoHProviderGoogle9     = "8.8.4.4"
	DoHProviderCloudflare1 = "1.1.1.1"
	DoHProviderCloudflare2 = "1.0.0.1"
	DoHProviderQuad9ip     = "9.9.9.9"

	// Common ports
	PortHTTP  = 80
	PortHTTPS = 443
	PortSOCKS = 1080
)
