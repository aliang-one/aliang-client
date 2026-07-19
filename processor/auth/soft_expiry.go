package user

import (
	"fmt"
	"os"
	"sync"
	"time"

	"aliang.one/nursorgate/common/logger"
)

// SoftExpired recovery: when the cloud rejects the access_token but the refresh
// token may still be valid, the session enters SoftExpired and this coordinator
// drives a bounded recovery (immediate refresh + backoff). Permanent rejection
// escalates immediately (inside RefreshSession); only transient failures retry
// within softExpiryTimeout, after which the session escalates to HardInvalid.

const (
	defaultSoftExpiryTimeout = 30 * time.Second
	envSoftExpiryTimeout     = "ALIANG_SESSION_SOFT_EXPIRY_TIMEOUT"
)

var (
	softExpiryMu      sync.Mutex
	softExpiryRunning bool

	softExpiryTimeout = loadSoftExpiryTimeout()
	// softExpiryBackoff is the refresh retry schedule within the window.
	softExpiryBackoff = []time.Duration{0, 5 * time.Second, 15 * time.Second}
	// softExpiryRefresh is the recovery refresh (RefreshSession by default).
	// On success it MUST fire NotifyRefreshed; on permanent failure it MUST wipe
	// + fire NotifyRefreshFailed — exactly what RefreshSession does.
	softExpiryRefresh = func() (*UserInfo, error) { return RefreshSession("") }
	softExpirySleep   = func(d time.Duration) { time.Sleep(d) }
	softExpiryNow     = time.Now
)

func loadSoftExpiryTimeout() time.Duration {
	if v := os.Getenv(envSoftExpiryTimeout); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return defaultSoftExpiryTimeout
}

// StartSoftExpiryRecovery begins the SoftExpired recovery loop (single-flight:
// a second call while one is running is a no-op). It exits as soon as the
// authority leaves SoftExpired (refresh success → Active, permanent fail →
// HardInvalid), or escalates to HardInvalid (ReasonSoftExpiryTimeout) on timeout.
func StartSoftExpiryRecovery() {
	softExpiryMu.Lock()
	if softExpiryRunning {
		softExpiryMu.Unlock()
		return
	}
	softExpiryRunning = true
	softExpiryMu.Unlock()

	go runSoftExpiryRecovery()
}

func runSoftExpiryRecovery() {
	defer func() {
		softExpiryMu.Lock()
		softExpiryRunning = false
		softExpiryMu.Unlock()
	}()

	deadline := softExpiryNow().Add(softExpiryTimeout)
	for _, wait := range softExpiryBackoff {
		if wait > 0 {
			softExpirySleep(wait)
		}
		// Transitioned out elsewhere (e.g. a concurrent refresh succeeded).
		if GetSessionAuthority().State() != StateSoftExpired {
			return
		}
		if softExpiryNow().After(deadline) {
			break
		}
		if _, err := softExpiryRefresh(); err == nil {
			return // success → Active (NotifyRefreshed fired by the refresh)
		}
		if GetCurrentUserInfoOrLoad() == nil {
			return // permanent → HardInvalid (wipe + NotifyRefreshFailed fired by the refresh)
		}
		// transient failure → next backoff retry
	}

	if GetSessionAuthority().State() == StateSoftExpired {
		logger.Warn(fmt.Sprintf("SoftExpired recovery timed out after %s; escalating to HardInvalid", softExpiryTimeout))
		ExpireLocalSession("soft expiry timeout")
	}
}
