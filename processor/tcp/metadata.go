package tcp

import (
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
)

// MetadataBuilder helps construct Metadata objects
type MetadataBuilder struct {
	metadata *Metadata
}

// NewMetadataBuilder creates a new metadata builder
func NewMetadataBuilder() *MetadataBuilder {
	return &MetadataBuilder{
		metadata: &Metadata{
			Network: "tcp",
		},
	}
}

// WithSource sets the source IP and port
func (b *MetadataBuilder) WithSource(ip netip.Addr, port uint16) *MetadataBuilder {
	b.metadata.SrcIP = ip
	b.metadata.SrcPort = port
	return b
}

// WithDestination sets the destination IP and port
func (b *MetadataBuilder) WithDestination(ip netip.Addr, port uint16) *MetadataBuilder {
	b.metadata.DstIP = ip
	b.metadata.DstPort = port
	return b
}

// WithMidpoint sets the middle IP and port (our local address)
func (b *MetadataBuilder) WithMidpoint(ip netip.Addr, port uint16) *MetadataBuilder {
	b.metadata.MidIP = ip
	b.metadata.MidPort = port
	return b
}

// WithHostName sets the hostname
func (b *MetadataBuilder) WithHostName(hostname string) *MetadataBuilder {
	b.metadata.HostName = hostname
	return b
}

// Build returns the constructed Metadata
func (b *MetadataBuilder) Build() *Metadata {
	return b.metadata
}

// ParseAddrPort parses a string in the form "host:port" and returns IP and port.
// Supports both IPv4 and IPv6 addresses.
func ParseAddrPort(addr string) (netip.Addr, uint16, error) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return netip.Addr{}, 0, fmt.Errorf("invalid address format: %w", err)
	}

	ip, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, 0, fmt.Errorf("invalid IP address: %w", err)
	}

	port, err := strconv.ParseUint(portStr, 10, 16)
	if err != nil {
		return netip.Addr{}, 0, fmt.Errorf("invalid port: %w", err)
	}

	return ip, uint16(port), nil
}

// ParseAddr parses just an IP address without port
func ParseAddr(addr string) (netip.Addr, error) {
	return netip.ParseAddr(addr)
}

// ExtractHostPort extracts host and port from an address string
func ExtractHostPort(addr string) (string, uint16, error) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return "", 0, fmt.Errorf("invalid address format: %w", err)
	}

	port, err := strconv.ParseUint(portStr, 10, 16)
	if err != nil {
		return "", 0, fmt.Errorf("invalid port: %w", err)
	}

	return host, uint16(port), nil
}

// GetMetadataFromConn extracts source and destination addresses from net.Conn
func GetMetadataFromConn(conn net.Conn) (*Metadata, error) {
	builder := NewMetadataBuilder()

	// Extract source address
	if remoteAddr, ok := conn.RemoteAddr().(*net.TCPAddr); ok {
		ip, err := netip.ParseAddr(remoteAddr.IP.String())
		if err == nil {
			builder.WithSource(ip, uint16(remoteAddr.Port))
		}
	}

	// Extract destination address
	if localAddr, ok := conn.LocalAddr().(*net.TCPAddr); ok {
		ip, err := netip.ParseAddr(localAddr.IP.String())
		if err == nil {
			builder.WithMidpoint(ip, uint16(localAddr.Port))
		}
	}

	return builder.Build(), nil
}

// IsLoopbackIP checks if IP is a loopback address (127.0.0.1 or ::1)
func IsLoopbackIP(ip netip.Addr) bool {
	return ip.IsLoopback()
}

// IsPrivateIP checks if IP is in a private range
func IsPrivateIP(ip netip.Addr) bool {
	return ip.IsPrivate()
}

// IsMulticastIP checks if IP is a multicast address
func IsMulticastIP(ip netip.Addr) bool {
	return ip.IsMulticast()
}

// NormalizeHostname normalizes a hostname by lowercasing it
func NormalizeHostname(hostname string) string {
	return strings.ToLower(strings.TrimSpace(hostname))
}

// IsValidHostname checks if a string is a valid hostname
func IsValidHostname(hostname string) bool {
	if len(hostname) == 0 || len(hostname) > 253 {
		return false
	}

	// Hostnames can contain alphanumerics, hyphens, and dots
	// But cannot start/end with hyphen or dot
	labels := strings.Split(hostname, ".")
	for _, label := range labels {
		if len(label) == 0 || len(label) > 63 {
			return false
		}
		if label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
	}

	return true
}

// DebugString returns a debug-friendly representation of Metadata
func (m *Metadata) DebugString() string {
	return fmt.Sprintf(
		"TCP[%s:%d -> %s:%d via %s:%d] hostname=%s",
		m.SrcIP, m.SrcPort,
		m.DstIP, m.DstPort,
		m.MidIP, m.MidPort,
		m.HostName,
	)
}
