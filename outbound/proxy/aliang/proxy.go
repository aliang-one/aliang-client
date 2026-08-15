package aliang

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"aliang.one/nursorgate/common/logger"
	"aliang.one/nursorgate/inbound/tun/metadata"
	"aliang.one/nursorgate/outbound/proxy"
	"aliang.one/nursorgate/outbound/proxy/proto"
)

// aliangDialer is the seam over AliangServerConnector.DialWithTiming so that
// dial retry and the background health monitor can be exercised with a fake
// dialer instead of a real mTLS handshake.
type aliangDialer interface {
	DialWithTiming(ctx context.Context, network, address string, appProto string) (net.Conn, ProbeTimings, error)
}

// Aliang implements the Proxy interface for cursor H2 proxy
// Core responsibility: mTLS connection establishment and pooling
type Aliang struct {
	*proxy.Base
	config    *AliangConfig
	connector aliangDialer
	connPool  *ConnectionPool
	status    *linkStatusTracker
	mu        sync.RWMutex
	closed    bool

	monitorMu     sync.Mutex
	monitorCtx    context.Context
	monitorCancel context.CancelFunc
	monitorDone   chan struct{}
}

// New creates a new CursorH2 proxy instance
func NewAliang(config *AliangConfig) (*Aliang, error) {
	if config == nil {
		return nil, NewErrorf(ErrInvalidConfig, "config is required")
	}

	if err := config.Validate(); err != nil {
		return nil, err
	}

	aliang := &Aliang{
		Base: &proxy.Base{
			Address:  config.Addr,
			Protocol: proto.Aliang,
		},
		config:    config,
		connector: NewAliangServerConnector(config),
		connPool:  NewConnectionPool(config.ConnectionPool),
		status:    newLinkStatusTrackerWithFailureThreshold(config.Addr, 0, config.LinkFailureThreshold),
		closed:    false,
	}
	// Health monitor is started explicitly by the production refresh path
	// (registry.StartAliangHealthMonitor), not here — see StartHealthMonitor.
	return aliang, nil
}

func (c *Aliang) dialMaxAttempts() int {
	if c.config != nil && c.config.LinkDialMaxAttempts > 0 {
		return c.config.LinkDialMaxAttempts
	}
	return defaultLinkDialMaxAttempts
}

func (c *Aliang) dialRetryBaseDelay() time.Duration {
	if c.config != nil && c.config.LinkDialRetryBaseDelay > 0 {
		return c.config.LinkDialRetryBaseDelay
	}
	return defaultLinkDialRetryBaseDelay
}

// dialWithRetry dials the aliang server, retrying transient failures so a
// sub-second relay blip (mTLS handshake EOF during a server restart) is
// absorbed instead of surfacing to the caller and flipping the link offline.
// The loop stops as soon as ctx is cancelled; the per-attempt deadline comes
// from the connector (which applies DialTimeout when no deadline is set).
func (c *Aliang) dialWithRetry(ctx context.Context, network, address, appProto string) (net.Conn, ProbeTimings, error) {
	maxAttempts := c.dialMaxAttempts()
	baseDelay := c.dialRetryBaseDelay()

	var lastErr error
	for attempt := 1; ; attempt++ {
		conn, timing, err := c.connector.DialWithTiming(ctx, network, address, appProto)
		if err == nil {
			return conn, timing, nil
		}
		lastErr = err
		if attempt >= maxAttempts {
			break
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ProbeTimings{}, ctxErr
		}
		// Exponential backoff; cap the shift to avoid overflow on large counts.
		shift := attempt - 1
		if shift > 10 {
			shift = 10
		}
		wait := baseDelay * time.Duration(1<<shift)
		select {
		case <-ctx.Done():
			return nil, ProbeTimings{}, ctx.Err()
		case <-time.After(wait):
		}
	}
	return nil, ProbeTimings{}, lastErr
}

// DialContext implements the Proxy interface
func (c *Aliang) DialContext(ctx context.Context, metadata *metadata.Metadata) (net.Conn, error) {
	c.mu.RLock()
	if c.closed {
		c.mu.RUnlock()
		return nil, NewErrorf(ErrInvalidConfig, "proxy is closed")
	}
	c.mu.RUnlock()

	// Establish a dedicated mTLS connection for the current tunneled TCP session.
	// Raw byte relaying is not safe to reuse across independent sessions, even if
	// they target the same destination and protocol.
	c.status.markConnecting()
	if metadata != nil && metadata.ConnID != "" {
		ctx = context.WithValue(ctx, aliangContextConnIDKey{}, metadata.ConnID)
	}
	conn, timing, err := c.dialWithRetry(ctx, "tcp", c.config.Addr, metadata.AppProto)
	if err != nil {
		c.status.markFailure(describeProbeFailure(c.config.Addr, err))
		return nil, err
	}
	c.status.markSuccess(timing)

	if metadata != nil {
		appProto := metadata.AppProto
		if appProto == "" {
			appProto = "unknown"
		}
		logger.Debug(fmt.Sprintf("[AliangGate] conn_id=%s established dedicated mtls session app_proto=%s target=%s via=%s", metadata.ConnID, appProto, metadata.DestinationAddress(), c.config.Addr))
	}

	return conn, nil
}

// DialUDP implements the Proxy interface
// UDP is not supported for cursor_h2 proxy
func (c *Aliang) DialUDP(metadata *metadata.Metadata) (net.PacketConn, error) {
	return nil, NewErrorf(ErrInvalidConfig, "cursor_h2 does not support UDP")
}

// Close closes the proxy and releases resources
func (c *Aliang) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	pool := c.connPool
	c.mu.Unlock()

	// Stop the background health monitor outside c.mu so its lifecycle lock is
	// independent; it also signals in-flight probes (via monitor ctx) to abort.
	c.stopHealthMonitor()

	if pool != nil {
		pool.Close()
	}

	return nil
}

// GetStats returns statistics about the proxy
func (c *Aliang) GetStats() map[string]interface{} {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return map[string]interface{}{
		"addr":            c.config.Addr,
		"proto":           "cursor_h2",
		"closed":          c.closed,
		"connection_pool": c.connPool.Stats(),
		"link_status":     c.status.snapshotMap(),
	}
}

// LinkStatusSnapshot returns the latest observed mTLS link status without forcing a new probe.
func (c *Aliang) LinkStatusSnapshot() map[string]interface{} {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.closed {
		return unavailableLinkStatus(c.config.Addr, NewErrorf(ErrInvalidConfig, "proxy is closed"))
	}
	return c.status.snapshotMap()
}

// ProbeLink actively performs a new mTLS dial to measure reachability and latency.
func (c *Aliang) ProbeLink(ctx context.Context) map[string]interface{} {
	c.mu.RLock()
	if c.closed {
		c.mu.RUnlock()
		return unavailableLinkStatus(c.config.Addr, NewErrorf(ErrInvalidConfig, "proxy is closed"))
	}
	serverAddr := c.config.Addr
	c.mu.RUnlock()

	c.status.markConnecting()

	conn, timing, err := c.dialWithRetry(ctx, "tcp", serverAddr, "unknown")
	if err != nil {
		c.status.markFailure(describeProbeFailure(serverAddr, err))
		return c.status.snapshotMap()
	}
	_ = conn.Close()

	c.status.markSuccess(timing)
	return c.status.snapshotMap()
}
