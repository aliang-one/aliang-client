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
// drives recovery (immediate refresh + backoff, retrying until success or
// permanent rejection). Permanent rejection escalates immediately (inside
// RefreshSession → HardInvalid). Transient failures (e.g. cloud briefly
// unreachable) keep retrying on a capped backoff — they MUST NOT force a
// re-login, because a transient outage would otherwise log users out en masse.
// The ingress proxy stays paused while SoftExpired (no forwarding with a
// rejected token — closes 缺口 B) and resumes on → Active.

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
	// softExpiryMaxAttempts caps recovery attempts for tests (0 = retry transient
	// failures indefinitely in production). Transient failures never force a
	// re-login; only a permanent rejection (RefreshSession wiping the session) does.
	softExpiryMaxAttempts = 0
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
// authority leaves SoftExpired: refresh success → Active, or permanent failure
// → HardInvalid (wiped inside RefreshSession). Transient failures keep retrying
// without forcing a re-login.
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

// softExpiryBackoffFor returns the wait before the given 1-based recovery
// attempt, cycling through softExpiryBackoff and capping at its largest value
// once the schedule is exhausted, so open-ended retry settles to a steady cadence.
func softExpiryBackoffFor(attempt int) time.Duration {
	if len(softExpiryBackoff) == 0 {
		return 0
	}
	if attempt < 1 {
		attempt = 1
	}
	idx := attempt - 1
	if idx >= len(softExpiryBackoff) {
		idx = len(softExpiryBackoff) - 1
	}
	return softExpiryBackoff[idx]
}

func runSoftExpiryRecovery() {
	defer func() {
		softExpiryMu.Lock()
		softExpiryRunning = false
		softExpiryMu.Unlock()
	}()

	attempt := 0
	lastWarn := softExpiryNow()
	for {
		attempt++
		if wait := softExpiryBackoffFor(attempt); wait > 0 {
			softExpirySleep(wait)
		}
		// Transitioned out elsewhere (e.g. a concurrent refresh succeeded, or a
		// permanent failure wiped the session).
		if GetSessionAuthority().State() != StateSoftExpired {
			return
		}
		if _, err := softExpiryRefresh(); err == nil {
			return // success → Active (NotifyRefreshed fired by the refresh)
		}
		if GetCurrentUserInfoOrLoad() == nil {
			return // permanent → HardInvalid (wipe + NotifyRefreshFailed fired by the refresh)
		}
		// Transient failure (e.g. cloud briefly unreachable): keep the session in
		// SoftExpired and retry. The ingress proxy stays paused (no forwarding
		// with a rejected token — closes 缺口 B). A transient outage MUST NOT
		// force a re-login — only a permanent rejection (RefreshSession wiping
		// the session) does. softExpiryTimeout now only throttles this warning.
		if now := softExpiryNow(); now.Sub(lastWarn) >= softExpiryTimeout {
			logger.Warn(fmt.Sprintf("SoftExpired recovery still transient after %d attempts; ingress paused, retrying (no forced re-login)", attempt))
			lastWarn = now
		}
		if softExpiryMaxAttempts > 0 && attempt >= softExpiryMaxAttempts {
			return // test seam: bounded attempts; production (0) retries indefinitely
		}
	}
}
