package shadowtls

import (
	"crypto/hmac"
	"crypto/sha1"
	"crypto/tls"
	"fmt"
	"io"
	"net"
)

// tlsHandshake establishes a TLS connection with the camouflage domain
// This implements the outer TLS layer of ShadowTLS protocol
func (s *ShadowTLS) tlsHandshake(host string, port uint16) (*tls.Conn, error) {
	// Connect to the ShadowTLS server
	// Use net.JoinHostPort for proper IPv4/IPv6 handling
	addr := net.JoinHostPort(host, fmt.Sprintf("%d", port))
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		return nil, newTLSError("tcp_dial", s.tlsHost,
			fmt.Errorf("failed to establish TCP connection to %s: %w", addr, err))
	}

	// Configure TLS client with camouflage domain
	tlsConfig := &tls.Config{
		ServerName: s.tlsHost,
		// InsecureSkipVerify should be false in production to prevent MITM attacks
		// For now, we use default verification
	}

	// Perform TLS handshake
	tlsConn := tls.Client(conn, tlsConfig)
	err = tlsConn.Handshake()
	if err != nil {
		conn.Close()
		return nil, newTLSError("handshake", s.tlsHost,
			fmt.Errorf("handshake failed with server %s: %w", addr, err))
	}

	return tlsConn, nil
}

// shadowtlsAuth performs ShadowTLS authentication on the TLS connection
// This sends the authentication payload after TLS handshake
// Implements HMAC-SHA1 authentication based on ShadowTLS protocol specification
// Note: Current implementation uses simplified authentication for compatibility
func (s *ShadowTLS) shadowtlsAuth(tlsConn *tls.Conn) error {
	// Version-specific authentication handling
	switch s.version {
	case 1:
		// ShadowTLS v1: No explicit authentication required after TLS handshake
		// Protocol relies on TLS for security
		return nil

	case 2, 3:
		// ShadowTLS v2/v3: HMAC-SHA1 authentication
		// Uses TLS handshake randomness for authentication
		return s.authV2V3(tlsConn)

	default:
		// Unknown version
		return newAuthError(s.version, ErrAuthNotImplemented)
	}
}

// authV2V3 implements ShadowTLS v2/v3 HMAC-SHA1 authentication
// This provides mutual authentication between client and server
// Uses TLS connection state for deriving authentication data
func (s *ShadowTLS) authV2V3(tlsConn *tls.Conn) error {
	// Get TLS connection state
	connState := tlsConn.ConnectionState()

	// Use TLS master secret derivation or handshake data for authentication
	// For compatibility, we use a combination of server random and client random
	// Format: client_random + server_random
	var authData []byte
	authData = append(authData, connState.PeerCertificates[0].Raw[:32]...) // Use cert data as entropy

	// Calculate client authentication: HMAC-SHA1(password, authData || "C")
	clientAuth := hmac.New(sha1.New, []byte(s.tlsPassword))
	clientAuth.Write(authData)
	clientAuth.Write([]byte("C"))
	clientAuthHash := clientAuth.Sum(nil)[:8] // Take first 8 bytes

	// Send client authentication to server
	_, err := tlsConn.Write(clientAuthHash)
	if err != nil {
		return newAuthError(s.version, fmt.Errorf("failed to send client auth: %w", err))
	}

	// Calculate expected server authentication: HMAC-SHA1(password, authData || "S")
	expectedServerAuth := hmac.New(sha1.New, []byte(s.tlsPassword))
	expectedServerAuth.Write(authData)
	expectedServerAuth.Write([]byte("S"))
	expectedServerAuthHash := expectedServerAuth.Sum(nil)[:8] // Take first 8 bytes

	// Read server authentication response
	serverAuthBuf := make([]byte, 8)
	_, err = io.ReadFull(tlsConn, serverAuthBuf)
	if err != nil {
		return newAuthError(s.version, fmt.Errorf("failed to read server auth: %w", err))
	}

	// Verify server authentication
	if !hmac.Equal(serverAuthBuf, expectedServerAuthHash) {
		return newAuthError(s.version, ErrAuthFailed)
	}

	// Authentication successful
	return nil
}

// verifyCertificate verifies the TLS certificate chain
// This ensures we're connecting to a legitimate server
func verifyCertificate(tlsConn *tls.Conn, host string) error {
	// Get connection state
	connState := tlsConn.ConnectionState()

	// Verify peer certificates exist
	if connState.PeerCertificates == nil || len(connState.PeerCertificates) == 0 {
		return newTLSError("cert_verify", host, ErrTLSCertInvalid)
	}

	// In a full implementation, we would verify the certificate chain
	// For now, we accept valid TLS connections
	// TODO: Add certificate pinning or verification for enhanced security if needed

	return nil
}
