package aliang

import (
	"fmt"
	"time"
)

const (
	// defaultLinkDialMaxAttempts bounds the total mTLS dial attempts per
	// connection/probe. 3 = one initial try + two retries, enough to absorb a
	// brief relay restart without adding noticeable latency on a real outage.
	defaultLinkDialMaxAttempts = 3
	// defaultLinkDialRetryBaseDelay is the first retry backoff; subsequent
	// retries double it (200ms, 400ms).
	defaultLinkDialRetryBaseDelay = 200 * time.Millisecond
	// defaultHealthMonitorInterval is the probe cadence while healthy.
	defaultHealthMonitorInterval = 30 * time.Second
	// defaultHealthRecoveryInterval is the probe cadence while disconnected,
	// i.e. how fast we retry to auto-heal the link.
	defaultHealthRecoveryInterval = 5 * time.Second
)

// AliangConfig contains configuration for the cursor_h2 proxy
type AliangConfig struct {
	// Addr is the cursor server address (host:port)
	Addr string

	// DialTimeout is the timeout for establishing connections
	DialTimeout time.Duration

	// ReadTimeout is the timeout for read operations
	ReadTimeout time.Duration

	// WriteTimeout is the timeout for write operations
	WriteTimeout time.Duration

	// MaxConcurrentStreams is the maximum number of concurrent HTTP/2 streams
	MaxConcurrentStreams uint32

	// ConnectionPoolConfig contains connection pool settings
	ConnectionPool *ConnectionPoolConfig

	// TLSConfig contains TLS/mTLS settings
	TLSConfig *TLSConfigOptions

	// LinkDialMaxAttempts is the total number of mTLS dial attempts (initial +
	// retries) per outbound connection or link probe before surfacing a
	// failure. A sub-second relay blip (handshake EOF during a restart) is
	// absorbed here instead of failing the connection and flipping the link
	// offline. <=0 falls back to the default.
	LinkDialMaxAttempts int

	// LinkDialRetryBaseDelay is the base backoff between retry attempts; the
	// wait doubles each attempt. <=0 falls back to the default.
	LinkDialRetryBaseDelay time.Duration

	// LinkFailureThreshold is how many consecutive failures must pile up
	// before the link status flips to "disconnected". A single transient
	// failure is tolerated. <=0 falls back to the default.
	LinkFailureThreshold int

	// LinkHealthInterval is how often the background health monitor probes
	// while the link is healthy (connected/degraded). It is a backstop for
	// keeping status fresh when there is no proxied traffic and no dashboard
	// open. <=0 falls back to the default.
	LinkHealthInterval time.Duration

	// LinkHealthRecoveryInterval is how often the monitor re-probes while the
	// link is disconnected/unknown, auto-healing the state the moment the
	// relay is reachable again. <=0 falls back to the default.
	LinkHealthRecoveryInterval time.Duration
}

// ConnectionPoolConfig contains settings for the connection pool
type ConnectionPoolConfig struct {
	// MaxConnPerHost is the maximum number of connections per host
	MaxConnPerHost int

	// MaxIdleTime is the maximum idle time for connections
	MaxIdleTime time.Duration

	// CleanupInterval is the interval for cleanup of idle connections
	CleanupInterval time.Duration
}

// TLSConfigOptions contains TLS/mTLS settings
type TLSConfigOptions struct {
	// InsecureSkipVerify disables certificate verification
	InsecureSkipVerify bool

	// ServerName is the server name for SNI
	ServerName string

	// CAFile is the path to CA certificate file
	CAFile string

	// CertFile is the path to client certificate file
	CertFile string

	// KeyFile is the path to client key file
	KeyFile string
}

// DefaultConfig creates a default configuration for the given server address
func DefaultConfig(addr string) *AliangConfig {
	return &AliangConfig{
		Addr:                 addr,
		DialTimeout:          10 * time.Second,
		ReadTimeout:          30 * time.Second,
		WriteTimeout:         30 * time.Second,
		MaxConcurrentStreams: 250,
		LinkDialMaxAttempts:   defaultLinkDialMaxAttempts,
		LinkDialRetryBaseDelay: defaultLinkDialRetryBaseDelay,
		LinkFailureThreshold:  defaultFailureThreshold,
		LinkHealthInterval:        defaultHealthMonitorInterval,
		LinkHealthRecoveryInterval: defaultHealthRecoveryInterval,
		ConnectionPool: &ConnectionPoolConfig{
			MaxConnPerHost:  4,
			MaxIdleTime:     5 * time.Minute,
			CleanupInterval: 1 * time.Minute,
		},
		TLSConfig: &TLSConfigOptions{
			InsecureSkipVerify: false,
			ServerName:         "",
		},
	}
}

// Validate validates the configuration
func (c *AliangConfig) Validate() error {
	if c == nil {
		return NewErrorf(ErrInvalidConfig, "config is nil")
	}

	if c.Addr == "" {
		return NewErrorf(ErrInvalidConfig, "addr is required")
	}

	if c.DialTimeout == 0 {
		return NewErrorf(ErrInvalidConfig, "dial timeout must be > 0")
	}

	if c.ReadTimeout == 0 {
		return NewErrorf(ErrInvalidConfig, "read timeout must be > 0")
	}

	if c.WriteTimeout == 0 {
		return NewErrorf(ErrInvalidConfig, "write timeout must be > 0")
	}

	if c.MaxConcurrentStreams == 0 {
		c.MaxConcurrentStreams = 250 // Set default
	}

	if c.ConnectionPool == nil {
		c.ConnectionPool = &ConnectionPoolConfig{
			MaxConnPerHost:  4,
			MaxIdleTime:     5 * time.Minute,
			CleanupInterval: 1 * time.Minute,
		}
	}

	if c.ConnectionPool.MaxConnPerHost <= 0 {
		return NewErrorf(ErrInvalidConfig, "connection pool max conn per host must be > 0")
	}

	if c.ConnectionPool.MaxIdleTime <= 0 {
		return NewErrorf(ErrInvalidConfig, "connection pool max idle time must be > 0")
	}

	if c.TLSConfig == nil {
		c.TLSConfig = &TLSConfigOptions{
			InsecureSkipVerify: false,
		}
	}

	return nil
}

// String returns a string representation of the configuration
func (c *AliangConfig) String() string {
	return fmt.Sprintf(
		"CursorH2Config{Addr: %s, DialTimeout: %v, MaxConcurrentStreams: %d}",
		c.Addr,
		c.DialTimeout,
		c.MaxConcurrentStreams,
	)
}
