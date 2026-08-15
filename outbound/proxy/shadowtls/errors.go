package shadowtls

import (
	"errors"
	"fmt"
)

// Error types for different layers of the ShadowTLS protocol stack
// This provides clear, categorized error messages for debugging

var (
	// Configuration errors
	ErrNilConfig         = errors.New("shadowtls: configuration is nil")
	ErrInvalidPlugin     = errors.New("shadowtls: plugin field must be 'shadow-tls'")
	ErrMissingPluginOpts = errors.New("shadowtls: plugin_opts is required when using shadow-tls plugin")
	ErrInvalidCipher     = errors.New("shadowtls: unsupported or invalid cipher method")

	// Connection errors
	ErrConnectionFailed = errors.New("shadowtls: failed to establish connection")
	ErrConnectionClosed = errors.New("shadowtls: connection is closed")

	// TLS errors
	ErrTLSHandshakeFailed = errors.New("shadowtls: TLS handshake failed")
	ErrTLSCertInvalid     = errors.New("shadowtls: TLS certificate validation failed")

	// Authentication errors
	ErrAuthFailed         = errors.New("shadowtls: authentication failed")
	ErrAuthNotImplemented = errors.New("shadowtls: authentication not yet implemented")

	// Encryption errors
	ErrCipherInitFailed = errors.New("shadowtls: failed to initialize cipher")
	ErrEncryptionFailed = errors.New("shadowtls: data encryption failed")
	ErrDecryptionFailed = errors.New("shadowtls: data decryption failed")

	// Protocol errors
	ErrUDPNotSupported = errors.New("shadowtls: UDP protocol is not supported")
	ErrInvalidAddress  = errors.New("shadowtls: invalid target address")
)

// ConfigError represents a configuration validation error
type ConfigError struct {
	Field   string
	Message string
	Err     error
}

func (e *ConfigError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("shadowtls config error [%s]: %s: %v", e.Field, e.Message, e.Err)
	}
	return fmt.Sprintf("shadowtls config error [%s]: %s", e.Field, e.Message)
}

func (e *ConfigError) Unwrap() error {
	return e.Err
}

// ConnectionError represents a network connection error
type ConnectionError struct {
	Stage   string // "tcp_dial", "tls_handshake", "auth", "cipher_init"
	Address string
	Err     error
}

func (e *ConnectionError) Error() string {
	return fmt.Sprintf("shadowtls connection error [stage=%s, addr=%s]: %v", e.Stage, e.Address, e.Err)
}

func (e *ConnectionError) Unwrap() error {
	return e.Err
}

// TLSError represents a TLS layer error
type TLSError struct {
	Stage string // "handshake", "cert_verify", "read", "write"
	Host  string
	Err   error
}

func (e *TLSError) Error() string {
	return fmt.Sprintf("shadowtls TLS error [stage=%s, host=%s]: %v", e.Stage, e.Host, e.Err)
}

func (e *TLSError) Unwrap() error {
	return e.Err
}

// AuthError represents a ShadowTLS authentication error
type AuthError struct {
	Version int
	Err     error
}

func (e *AuthError) Error() string {
	return fmt.Sprintf("shadowtls auth error [version=%d]: %v", e.Version, e.Err)
}

func (e *AuthError) Unwrap() error {
	return e.Err
}

// CipherError represents an encryption/decryption error
type CipherError struct {
	Method    string
	Operation string // "init", "encrypt", "decrypt"
	Err       error
}

func (e *CipherError) Error() string {
	return fmt.Sprintf("shadowtls cipher error [method=%s, op=%s]: %v", e.Method, e.Operation, e.Err)
}

func (e *CipherError) Unwrap() error {
	return e.Err
}

// Helper functions for creating structured errors

func newConfigError(field, message string, err error) error {
	return &ConfigError{
		Field:   field,
		Message: message,
		Err:     err,
	}
}

func newConnectionError(stage, address string, err error) error {
	return &ConnectionError{
		Stage:   stage,
		Address: address,
		Err:     err,
	}
}

func newTLSError(stage, host string, err error) error {
	return &TLSError{
		Stage: stage,
		Host:  host,
		Err:   err,
	}
}

func newAuthError(version int, err error) error {
	return &AuthError{
		Version: version,
		Err:     err,
	}
}

func newCipherError(method, operation string, err error) error {
	return &CipherError{
		Method:    method,
		Operation: operation,
		Err:       err,
	}
}
