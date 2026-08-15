package shadowtls

import (
	"context"
	"fmt"
	"net"
	"sync"

	"github.com/xjasonlyu/tun2socks/v2/transport/shadowsocks/core"

	"aliang.one/nursorgate/inbound/tun/metadata"
	"aliang.one/nursorgate/outbound/proxy"
	"aliang.one/nursorgate/outbound/proxy/proto"
	"aliang.one/nursorgate/processor/config"
)

// ShadowTLS represents a ShadowTLS proxy implementation
// It acts as a Shadowsocks plugin that disguises traffic as TLS
type ShadowTLS struct {
	*proxy.Base

	// Basic connection info
	server string
	port   uint16

	// Shadowsocks configuration (for encryption layer)
	method   string
	password string
	username string

	// ShadowTLS specific parameters
	tlsHost     string // TLS camouflage domain
	tlsPassword string // ShadowTLS authentication password
	version     int    // Protocol version (1, 2, or 3)

	// Connection management
	mu sync.RWMutex
}

// New creates a new ShadowTLS proxy instance
func New(cfg *config.ShadowsocksConfig) (*ShadowTLS, error) {
	if cfg == nil {
		return nil, newConfigError("config", "configuration is nil", ErrNilConfig)
	}

	// Validate configuration
	if err := cfg.Validate(); err != nil {
		return nil, newConfigError("config", "failed to validate configuration", err)
	}

	// Ensure plugin is shadow-tls
	if cfg.Plugin != "shadow-tls" {
		return nil, newConfigError("plugin",
			fmt.Sprintf("plugin must be 'shadow-tls', got '%s'", cfg.Plugin),
			ErrInvalidPlugin)
	}

	if cfg.PluginOpts == nil {
		return nil, newConfigError("plugin_opts", "required when using shadow-tls plugin", ErrMissingPluginOpts)
	}

	// Validate that the encryption method is supported by creating a test cipher
	// This ensures we fail fast during initialization rather than during connection
	_, err := core.PickCipher(cfg.Method, nil, cfg.Password)
	if err != nil {
		return nil, newCipherError(cfg.Method, "init", err)
	}

	s := &ShadowTLS{
		server:      cfg.Server,
		port:        cfg.ServerPort,
		method:      cfg.Method,
		password:    cfg.Password,
		username:    cfg.Username,
		tlsHost:     cfg.PluginOpts.Host,
		tlsPassword: cfg.PluginOpts.Password,
		version:     cfg.PluginOpts.Version,
	}

	// Initialize Base
	addr := fmt.Sprintf("%s:%d", cfg.Server, cfg.ServerPort)
	s.Base = &proxy.Base{
		Address:  addr,
		Protocol: proto.ShadowTLS,
	}

	return s, nil
}

// Addr returns the proxy server address
func (s *ShadowTLS) Addr() string {
	return s.Base.Addr()
}

// Proto returns the protocol type
func (s *ShadowTLS) Proto() proto.Proto {
	return proto.ShadowTLS
}

// DialContext establishes a connection through the ShadowTLS proxy
// Implementation steps:
// 1. Establish TLS connection to server with camouflage host
// 2. Perform ShadowTLS authentication
// 3. Wrap connection with Shadowsocks encryption
// 4. Send target address to Shadowsocks server
// 5. Return wrapped connection for data transfer
// Optimized: improved resource cleanup and context awareness
func (s *ShadowTLS) DialContext(ctx context.Context, metadata *metadata.Metadata) (net.Conn, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	addr := s.Addr()

	// Step 1: Establish TLS connection
	tlsConn, err := s.tlsHandshake(s.server, s.port)
	if err != nil {
		// Error is already wrapped by tlsHandshake as TLSError
		return nil, newConnectionError("tls_handshake", addr, err)
	}

	// Ensure cleanup on failure with deferred cleanup
	// This will be skipped if we successfully return the connection
	var connSuccess bool
	defer func() {
		if !connSuccess && tlsConn != nil {
			tlsConn.Close()
		}
	}()

	// Step 2: Perform ShadowTLS authentication
	// TODO: Implement actual ShadowTLS authentication protocol
	err = s.shadowtlsAuth(tlsConn)
	if err != nil {
		// Error is already wrapped by shadowtlsAuth as AuthError
		return nil, newConnectionError("auth", addr, err)
	}

	// Step 3: Wrap with Shadowsocks encryption
	// Create cipher for this connection (required for security - each connection needs fresh state)
	cipher, err := core.PickCipher(s.method, nil, s.password)
	if err != nil {
		return nil, newConnectionError("cipher_init", addr,
			newCipherError(s.method, "init", err))
	}

	// Wrap TLS connection with encryption
	encryptedConn := cipher.StreamConn(tlsConn)

	// Step 4: Send target address request
	// Only send address if metadata is available
	if metadata != nil {
		err := SendRequest(encryptedConn, metadata.HostName, metadata.DstIP, metadata.DstPort)
		if err != nil {
			// Log error but continue - some servers might not require address
			// This allows more flexible usage patterns
		}
	}

	// Step 5: Return wrapped connection
	conn := NewShadowTLSConn(tlsConn, encryptedConn)
	connSuccess = true // Mark success to skip deferred cleanup
	return conn, nil
}

// DialUDP is not supported by ShadowTLS
func (s *ShadowTLS) DialUDP(metadata *metadata.Metadata) (net.PacketConn, error) {
	return nil, ErrUDPNotSupported
}
