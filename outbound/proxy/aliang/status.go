package aliang

import (
	"fmt"
	"sync"
	"time"
)

const (
	LinkStateUnknown      = "unknown"
	LinkStateConnecting   = "connecting"
	LinkStateConnected    = "connected"
	LinkStateDegraded     = "degraded"
	LinkStateDisconnected = "disconnected"

	defaultLatencyWarningThreshold = 800 * time.Millisecond
	// defaultFailureThreshold is how many consecutive probe/dial failures must
	// pile up before the link is declared "disconnected". A single transient
	// mTLS hiccup (EOF / brief relay restart) must not flip the whole link
	// offline — that is what forced users to manually reconnect even though the
	// relay was already reachable again.
	defaultFailureThreshold = 3
)

// LinkStatusSnapshot is the serialized view consumed by the HTTP API and UI.
type LinkStatusSnapshot struct {
	ServerAddr         string `json:"server_addr"`
	State              string `json:"state"`
	LatencyMS          int64  `json:"latency_ms"`
	TCPConnectMS       int64  `json:"tcp_connect_ms"`
	TLSHandshakeMS     int64  `json:"tls_handshake_ms"`
	ProbeTotalMS       int64  `json:"probe_total_ms"`
	LastError          string `json:"last_error"`
	LastCheckedAt      int64  `json:"last_checked_at"`
	LastConnectedAt    int64  `json:"last_connected_at"`
	ConsecutiveFailure int    `json:"consecutive_failures"`
}

type linkStatusTracker struct {
	mu               sync.RWMutex
	serverAddr       string
	latencyThreshold time.Duration
	failureThreshold int
	snapshot         LinkStatusSnapshot
}

func newLinkStatusTracker(serverAddr string, latencyThreshold time.Duration) *linkStatusTracker {
	return newLinkStatusTrackerWithFailureThreshold(serverAddr, latencyThreshold, defaultFailureThreshold)
}

// newLinkStatusTrackerWithFailureThreshold lets callers (and tests) override the
// consecutive-failure tolerance. failureThreshold <= 0 falls back to the default.
func newLinkStatusTrackerWithFailureThreshold(serverAddr string, latencyThreshold time.Duration, failureThreshold int) *linkStatusTracker {
	if latencyThreshold <= 0 {
		latencyThreshold = defaultLatencyWarningThreshold
	}
	if failureThreshold <= 0 {
		failureThreshold = defaultFailureThreshold
	}

	return &linkStatusTracker{
		serverAddr:       serverAddr,
		latencyThreshold: latencyThreshold,
		failureThreshold: failureThreshold,
		snapshot: LinkStatusSnapshot{
			ServerAddr: serverAddr,
			State:      LinkStateUnknown,
		},
	}
}

func (t *linkStatusTracker) markConnecting() {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.snapshot.ServerAddr = t.serverAddr
	t.snapshot.State = LinkStateConnecting
	t.snapshot.LastCheckedAt = time.Now().UnixMilli()
	t.snapshot.LastError = ""
}

// state returns the current link state under a read lock.
func (t *linkStatusTracker) state() string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.snapshot.State
}

func (t *linkStatusTracker) markSuccess(timing ProbeTimings) {
	t.mu.Lock()
	defer t.mu.Unlock()

	state := LinkStateConnected
	if timing.Total >= t.latencyThreshold {
		state = LinkStateDegraded
	}

	now := time.Now().UnixMilli()
	t.snapshot.ServerAddr = t.serverAddr
	t.snapshot.State = state
	t.snapshot.LatencyMS = timing.DisplayLatency().Milliseconds()
	t.snapshot.TCPConnectMS = timing.TCPConnect.Milliseconds()
	t.snapshot.TLSHandshakeMS = timing.TLSHandshake.Milliseconds()
	t.snapshot.ProbeTotalMS = timing.Total.Milliseconds()
	t.snapshot.LastError = ""
	t.snapshot.LastCheckedAt = now
	t.snapshot.LastConnectedAt = now
	t.snapshot.ConsecutiveFailure = 0
}

func (t *linkStatusTracker) markFailure(err error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.snapshot.ServerAddr = t.serverAddr
	t.snapshot.LastCheckedAt = time.Now().UnixMilli()
	t.snapshot.ConsecutiveFailure++
	if err != nil {
		t.snapshot.LastError = err.Error()
	} else {
		t.snapshot.LastError = "link probe failed"
	}

	// Tolerate transient failures: only declare the link "disconnected" once
	// failures stack up to the threshold. Below it, keep the last observed
	// state (and the last known latency) so a sub-second relay blip does not
	// show the link as offline. The frontend self-heals on the next probe; the
	// background health monitor also drives this state machine automatically.
	if t.failureThreshold > 0 && t.snapshot.ConsecutiveFailure < t.failureThreshold {
		return
	}

	t.snapshot.State = LinkStateDisconnected
	t.snapshot.LatencyMS = 0
	t.snapshot.TCPConnectMS = 0
	t.snapshot.TLSHandshakeMS = 0
	t.snapshot.ProbeTotalMS = 0
}

func (t *linkStatusTracker) markReused() {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.snapshot.State == LinkStateUnknown || t.snapshot.State == LinkStateDisconnected {
		t.snapshot.State = LinkStateConnected
	}
	t.snapshot.ServerAddr = t.serverAddr
	t.snapshot.LastCheckedAt = time.Now().UnixMilli()
	t.snapshot.LastError = ""
}

func (t *linkStatusTracker) snapshotMap() map[string]interface{} {
	t.mu.RLock()
	defer t.mu.RUnlock()

	snapshot := t.snapshot
	if snapshot.ServerAddr == "" {
		snapshot.ServerAddr = t.serverAddr
	}

	return map[string]interface{}{
		"server_addr":            snapshot.ServerAddr,
		"state":                  snapshot.State,
		"latency_ms":             snapshot.LatencyMS,
		"tcp_connect_ms":         snapshot.TCPConnectMS,
		"tls_handshake_ms":       snapshot.TLSHandshakeMS,
		"probe_total_ms":         snapshot.ProbeTotalMS,
		"last_error":             snapshot.LastError,
		"last_checked_at":        snapshot.LastCheckedAt,
		"last_connected_at":      snapshot.LastConnectedAt,
		"consecutive_failures":   snapshot.ConsecutiveFailure,
		"high_latency_threshold": t.latencyThreshold.Milliseconds(),
	}
}

func unavailableLinkStatus(serverAddr string, err error) map[string]interface{} {
	message := "aliang outbound is unavailable"
	if err != nil {
		message = err.Error()
	}

	return map[string]interface{}{
		"server_addr":            serverAddr,
		"state":                  LinkStateDisconnected,
		"latency_ms":             int64(0),
		"tcp_connect_ms":         int64(0),
		"tls_handshake_ms":       int64(0),
		"probe_total_ms":         int64(0),
		"last_error":             message,
		"last_checked_at":        time.Now().UnixMilli(),
		"last_connected_at":      int64(0),
		"consecutive_failures":   0,
		"high_latency_threshold": defaultLatencyWarningThreshold.Milliseconds(),
	}
}

func describeProbeFailure(serverAddr string, err error) error {
	if err == nil {
		return fmt.Errorf("mTLS link probe to %s failed", serverAddr)
	}
	return fmt.Errorf("mTLS link probe to %s failed: %w", serverAddr, err)
}
