package tcp

import (
	"context"
	"net"
	"time"

	M "aliang.one/nursorgate/inbound/tun/metadata"
)

// TCPConnHandler orchestrates the entire TCP connection lifecycle.
// It handles protocol detection, routing decisions, and data relay
// for connections from both TUN and HTTP proxy modules.
type TCPConnHandler interface {
	// Handle processes a single TCP connection from any proxy source.
	// It is responsible for:
	// - Protocol detection (TLS on port 443, direct for others)
	// - SNI extraction and certificate interception (for HTTPS)
	// - Routing decisions (cursor proxy, door proxy, or direct)
	// - Statistics tracking
	// - Bidirectional data relay
	//
	// The handler takes ownership of originConn and is responsible for closing it.
	// The context.Context is used for timeouts and cancellation.
	Handle(ctx context.Context, originConn net.Conn, metadata *M.Metadata) error
}

// RelayManager manages bidirectional data piping between two connections.
// It handles buffer pooling, statistics tracking, and graceful cleanup.
type RelayManager interface {
	// Relay establishes bidirectional data flow between originConn and remoteConn.
	// It:
	// - Copies data concurrently in both directions using io.CopyBuffer
	// - Uses buffer pooling for efficiency (from tun/buffer)
	// - Integrates with processor/statistic for connection tracking
	// - Performs TCP half-close handling (CloseRead/CloseWrite)
	// - Sets appropriate timeouts and cleanup handlers
	//
	// The function blocks until both directions complete or context is cancelled.
	Relay(ctx context.Context, originConn, remoteConn net.Conn, metadata *M.Metadata) (*RelayStats, error)
}

type RelayStats struct {
	StartedAt          time.Time
	FirstResponseAt    time.Time
	CompletedAt        time.Time
	ClientToServerByte int64
	ServerToClientByte int64
	RequestPayload     []byte
	ResponsePayload    []byte
}

// ProtocolDetector determines how to handle a connection based on destination port.
type ProtocolDetector interface {
	// Detect returns the protocol type for the given port.
	// Typical behavior:
	// - Port 443: ProtocolTLS (SNI extraction, MITM, domain routing)
	// - Port 80: ProtocolHTTP (may need special handling for CONNECT)
	// - Others: ProtocolDirect (pass-through connection)
	Detect(port uint16) Protocol
}

// TLSHandler manages TLS-specific operations:
// - SNI extraction from ClientHello
// - MITM certificate generation and TLS handshake
// - Domain-based routing decisions (both legacy and rule engine)
type TLSHandler interface {
	// ExtractSNI reads the Server Name Indication from a TLS ClientHello.
	// Returns:
	// - serverName: The extracted domain name (e.g., "example.com")
	// - buffer: The raw TLS ClientHello data (for re-reading in TLS handshake)
	// - error: If SNI extraction fails or connection is not TLS
	ExtractSNI(ctx context.Context, conn net.Conn) (serverName string, buffer []byte, err error)

	// PerformMITM creates a TLS server connection with intercepted certificate.
	// Steps:
	// 1. Generate certificate for serverName (cached if already generated)
	// 2. Create TLS config with the certificate
	// 3. Perform TLS handshake as server
	// 4. Return the established TLS connection
	PerformMITM(ctx context.Context, originConn net.Conn, serverName string) (net.Conn, error)

	// DetermineRoute checks if domain should be routed to aliang (MITM), socks, or direct.
	// Uses SNI allowlist from config.
	// This is the legacy method without rule engine context.
	DetermineRoute(serverName string) ProxyRoute

	// DetermineRouteWithContext makes routing decisions with metadata context.
	// It can leverage cached SNI bindings and the SNI allowlist.
	//
	DetermineRouteWithContext(metadata *M.Metadata) (ProxyRoute, bool)
}

// StatisticsTracker wraps connections to track upload/download statistics.
type StatisticsTracker interface {
	// WrapConnection adds statistics tracking to a connection.
	// Returns a wrapped connection that tracks bytes read/written.
	WrapConnection(conn net.Conn, metadata *M.Metadata) net.Conn

	// UpdateStatistics notifies the statistics manager of bytes transferred.
	UpdateStatistics(uploaded, downloaded int64)
}

// ProxyDialer establishes connections to proxy servers.
type ProxyDialer interface {
	// DialContext dials a connection to the target through a proxy.
	// Returns a net.Conn representing the connection to the target through the proxy.
	DialContext(ctx context.Context, metadata *M.Metadata) (net.Conn, error)

	// Addr returns the proxy server address.
	Addr() string
}

// ConnectionProvider supplies necessary components to TCPConnHandler.
// Used for dependency injection and testing.
type ConnectionProvider interface {
	// GetTLSHandler returns the TLS handler implementation
	GetTLSHandler() TLSHandler

	// GetRelayManager returns the relay manager implementation
	GetRelayManager() RelayManager

	// GetProtocolDetector returns the protocol detector implementation
	GetProtocolDetector() ProtocolDetector

	// GetStatisticsTracker returns the statistics tracker implementation
	GetStatisticsTracker() StatisticsTracker

	// GetDefaultDialer returns the default dialer for direct connections
	GetDefaultDialer() ProxyDialer

	// GetSocksProxy returns the SOCKS proxy for routing
	GetSocksProxy() (ProxyDialer, error)

	// IsDoHProvider checks if the domain is a DNS-over-HTTPS provider
	IsDoHProvider(domain string) bool

	// IsAllowedToCursor checks if domain should be routed to cursor proxy
	IsAllowedToCursor(domain string) bool

	// IsAllowedToAnySocks checks if domain should be routed to SOCKS proxy
	IsAllowedToAnySocks(domain string) bool
}

// Protocol represents the detected connection protocol
type Protocol int

const (
	ProtocolTLS Protocol = iota
	ProtocolHTTP
	ProtocolDirect
)

// ProxyRoute determines where to send the connection
type ProxyRoute int

const (
	RouteToALiang ProxyRoute = iota
	RouteToLocalProxy
	RouteDirect
)

func (r ProxyRoute) String() string {
	switch r {
	case RouteToALiang:
		return "RouteToALiang"
	case RouteToLocalProxy:
		return "RouteToLocalProxy"
	case RouteDirect:
		return "RouteDirect"
	default:
		return "RouteUnknown"
	}
}

// Timeouts for TCP operations
const (
	DefaultTCPConnectTimeout = 30     // seconds
	DefaultTCPWaitTimeout    = 5 * 60 // seconds
)
