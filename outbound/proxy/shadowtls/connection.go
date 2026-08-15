package shadowtls

import (
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/netip"
	"sync/atomic"
	"time"
)

// ShadowTLSConn wraps a TLS connection with Shadowsocks encryption
// This handles the data forwarding between client and server through the TLS tunnel
type ShadowTLSConn struct {
	tlsConn       *tls.Conn // Underlying TLS connection (for close/address operations)
	encryptedConn net.Conn  // Shadowsocks-encrypted wrapper (for read/write operations)
	closed        uint32    // Atomic flag: 0 = open, 1 = closed
}

// NewShadowTLSConn creates a new ShadowTLS connection wrapper
// tlsConn is the underlying TLS connection
// encryptedConn is the Shadowsocks-encrypted wrapper around tlsConn
func NewShadowTLSConn(tlsConn *tls.Conn, encryptedConn net.Conn) *ShadowTLSConn {
	return &ShadowTLSConn{
		tlsConn:       tlsConn,
		encryptedConn: encryptedConn,
		closed:        0, // Atomic: 0 = open
	}
}

// Read reads data from the encrypted connection
// Data flows through: server -> TLS -> Shadowsocks cipher (decryption) -> client
// Optimized: uses atomic load for closed check to reduce lock contention
func (c *ShadowTLSConn) Read(b []byte) (n int, err error) {
	// Fast path: check closed state without locking
	if atomic.LoadUint32(&c.closed) != 0 {
		return 0, io.EOF
	}

	// Read from encrypted connection, which automatically decrypts data
	return c.encryptedConn.Read(b)
}

// Write writes data to the encrypted connection
// Data flows through: client -> Shadowsocks cipher (encryption) -> TLS -> server
// Optimized: uses atomic load for closed check to reduce lock contention
func (c *ShadowTLSConn) Write(b []byte) (n int, err error) {
	// Fast path: check closed state without locking
	if atomic.LoadUint32(&c.closed) != 0 {
		return 0, io.ErrClosedPipe
	}

	// Write to encrypted connection, which automatically encrypts data
	return c.encryptedConn.Write(b)
}

// Close closes both the encrypted wrapper and underlying TLS connection
// Optimized: uses atomic compare-and-swap for idempotent close
func (c *ShadowTLSConn) Close() error {
	// Use atomic compare-and-swap to ensure only one goroutine closes the connection
	if !atomic.CompareAndSwapUint32(&c.closed, 0, 1) {
		// Already closed
		return nil
	}

	// Close encrypted connection first (which also closes the underlying TLS connection)
	// No need to close tlsConn separately as it's wrapped by encryptedConn
	return c.encryptedConn.Close()
}

// LocalAddr returns the local network address
func (c *ShadowTLSConn) LocalAddr() net.Addr {
	return c.tlsConn.LocalAddr()
}

// RemoteAddr returns the remote network address
func (c *ShadowTLSConn) RemoteAddr() net.Addr {
	return c.tlsConn.RemoteAddr()
}

// SetDeadline sets the read and write deadlines
func (c *ShadowTLSConn) SetDeadline(t time.Time) error {
	return c.tlsConn.SetDeadline(t)
}

// SetReadDeadline sets the read deadline
func (c *ShadowTLSConn) SetReadDeadline(t time.Time) error {
	return c.tlsConn.SetReadDeadline(t)
}

// SetWriteDeadline sets the write deadline
func (c *ShadowTLSConn) SetWriteDeadline(t time.Time) error {
	return c.tlsConn.SetWriteDeadline(t)
}

// Shadowsocks encryption/decryption is now implemented via the encryptedConn wrapper
// which is created by cipher.StreamConn() and automatically handles:
// 1. Wrapping the TLS connection with Shadowsocks cipher
// 2. Encrypting outgoing data before writing to TLS connection
// 3. Decrypting incoming data after reading from TLS connection
// 4. Handling Shadowsocks-specific framing

// SOCKS5 address type constants
const (
	AddrTypeIPv4   = 0x01 // IPv4 address (4 bytes)
	AddrTypeDomain = 0x03 // Domain name (1 byte length + domain)
	AddrTypeIPv6   = 0x04 // IPv6 address (16 bytes)
)

// SendRequest encodes and sends the target address to the Shadowsocks server
// Uses SOCKS5 address format: [AddrType][Address][Port]
// This should be called after connection establishment and authentication
func SendRequest(conn net.Conn, hostName string, ip netip.Addr, port uint16) error {
	if conn == nil {
		return fmt.Errorf("connection is nil")
	}

	// Encode address in SOCKS5 format
	addrBuf, err := encodeAddress(hostName, ip, port)
	if err != nil {
		return fmt.Errorf("failed to encode address: %w", err)
	}

	// Send encoded address to server (will be encrypted by the connection wrapper)
	_, err = conn.Write(addrBuf)
	if err != nil {
		return fmt.Errorf("failed to send address request: %w", err)
	}

	return nil
}

// encodeAddress encodes the target address in SOCKS5 format
// Format: [1 byte type][address][2 bytes port]
// - IPv4: 0x01 + 4 bytes IP + 2 bytes port
// - Domain: 0x03 + 1 byte length + domain + 2 bytes port
// - IPv6: 0x04 + 16 bytes IP + 2 bytes port
func encodeAddress(hostName string, ip netip.Addr, port uint16) ([]byte, error) {
	var buf []byte
	portBuf := make([]byte, 2)
	binary.BigEndian.PutUint16(portBuf, port)

	// Prefer domain name if available (allows server-side DNS resolution)
	if hostName != "" && hostName != ip.String() {
		// Domain name format
		domainLen := len(hostName)
		if domainLen > 255 {
			return nil, fmt.Errorf("domain name too long: %d bytes (max 255)", domainLen)
		}

		buf = make([]byte, 1+1+domainLen+2)
		buf[0] = AddrTypeDomain          // Address type
		buf[1] = byte(domainLen)         // Domain length
		copy(buf[2:], []byte(hostName))  // Domain name
		copy(buf[2+domainLen:], portBuf) // Port
		return buf, nil
	}

	// IP address format
	if !ip.IsValid() {
		return nil, fmt.Errorf("invalid IP address and no hostname provided")
	}

	if ip.Is4() {
		// IPv4 format: type(1) + ipv4(4) + port(2) = 7 bytes
		buf = make([]byte, 1+4+2)
		buf[0] = AddrTypeIPv4
		copy(buf[1:5], ip.AsSlice())
		copy(buf[5:7], portBuf)
		return buf, nil
	}

	// IPv6 format: type(1) + ipv6(16) + port(2) = 19 bytes
	buf = make([]byte, 1+16+2)
	buf[0] = AddrTypeIPv6
	copy(buf[1:17], ip.AsSlice())
	copy(buf[17:19], portBuf)
	return buf, nil
}
