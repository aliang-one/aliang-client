package user

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func resetSoftExpiryForTest() {
	deadline := time.Now().Add(2 * time.Second)
	for {
		softExpiryMu.Lock()
		if !softExpiryRunning {
			softExpiryTimeout = defaultSoftExpiryTimeout
			softExpiryBackoff = []time.Duration{0, 5 * time.Second, 15 * time.Second}
			softExpiryRefresh = func() (*UserInfo, error) { return RefreshSession("") }
			softExpirySleep = func(time.Duration) {}
			softExpiryNow = time.Now
			softExpiryMaxAttempts = 0
			softExpiryMu.Unlock()
			return
		}
		softExpiryMu.Unlock()
		if time.Now().After(deadline) {
			panic("previous soft-expiry recovery did not stop")
		}
		time.Sleep(time.Millisecond)
	}
}

// waitNotSoftExpired polls the authority until it leaves SoftExpired (or times out).
func waitNotSoftExpired(t *testing.T, a *SessionAuthority) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if a.State() != StateSoftExpired {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("authority still SoftExpired after 2s; state=%v", a.State())
}

// Stubs mimic real RefreshSession: on success it fires NotifyRefreshed (→Active);
// on permanent failure it wipes user info + fires NotifyRefreshFailed (→HardInvalid).

func TestSoftExpiryRecoverySucceedsOnFirstAttempt(t *testing.T) {
	resetSoftExpiryForTest()
	softExpiryBackoff = []time.Duration{0}
	var refreshCalls int32
	softExpiryRefresh = func() (*UserInfo, error) {
		atomic.AddInt32(&refreshCalls, 1)
		GetSessionAuthority().NotifyRefreshed(&UserInfo{Email: "ok"})
		return &UserInfo{Email: "ok"}, nil
	}

	a := ResetSessionAuthorityForTest()
	a.NotifyLoggedIn(&UserInfo{})
	a.NotifyAccessRejected("agent rejected")
	if a.State() != StateSoftExpired {
		t.Fatalf("precondition: state=%v want SoftExpired", a.State())
	}

	StartSoftExpiryRecovery()
	waitNotSoftExpired(t, a)

	if a.State() != StateActive {
		t.Fatalf("state=%v want Active after successful recovery", a.State())
	}
	if got := atomic.LoadInt32(&refreshCalls); got != 1 {
		t.Fatalf("expected 1 refresh call, got %d", got)
	}
}

func TestSoftExpiryRecoveryRetriesThenSucceeds(t *testing.T) {
	resetSoftExpiryForTest()
	ResetAuthPersistenceForTest()
	SetCurrentUserInfo(&UserInfo{AccessToken: "x"}) // retained across transient failures
	softExpiryBackoff = []time.Duration{0, 0, 0}
	var refreshCalls int32
	softExpiryRefresh = func() (*UserInfo, error) {
		n := atomic.AddInt32(&refreshCalls, 1)
		if n < 3 {
			return nil, errors.New("transient network") // session retained → retry
		}
		GetSessionAuthority().NotifyRefreshed(&UserInfo{})
		return &UserInfo{}, nil
	}

	a := ResetSessionAuthorityForTest()
	a.NotifyLoggedIn(&UserInfo{})
	a.NotifyAccessRejected("agent rejected")

	StartSoftExpiryRecovery()
	waitNotSoftExpired(t, a)

	if a.State() != StateActive {
		t.Fatalf("state=%v want Active", a.State())
	}
	if got := atomic.LoadInt32(&refreshCalls); got != 3 {
		t.Fatalf("expected 3 refresh calls (2 transient + 1 success), got %d", got)
	}
}

func TestSoftExpiryRecoveryPermanentFailureEscalates(t *testing.T) {
	resetSoftExpiryForTest()
	ResetAuthPersistenceForTest()
	softExpiryBackoff = []time.Duration{0}
	var refreshCalls int32
	softExpiryRefresh = func() (*UserInfo, error) {
		atomic.AddInt32(&refreshCalls, 1)
		SetCurrentUserInfo(nil) // mimic RefreshSession wiping the session
		GetSessionAuthority().NotifyRefreshFailed(true, ReasonRefreshInvalid)
		return nil, ErrRefreshTokenInvalid
	}

	a := ResetSessionAuthorityForTest()
	a.NotifyLoggedIn(&UserInfo{})
	a.NotifyAccessRejected("agent rejected")

	StartSoftExpiryRecovery()
	waitNotSoftExpired(t, a)

	if a.State() != StateHardInvalid {
		t.Fatalf("state=%v want HardInvalid on permanent failure", a.State())
	}
	if got := atomic.LoadInt32(&refreshCalls); got != 1 {
		t.Fatalf("expected no retry after permanent failure, got %d calls", got)
	}
}

// TestSoftExpiryRecoveryTransientDoesNotForceRelogin asserts fix A: a transient
// refresh failure (cloud briefly unreachable, local session retained) MUST NOT
// escalate to HardInvalid or force a re-login. The session stays SoftExpired,
// the recovery loop keeps retrying, and local user info is preserved so a later
// refresh can succeed. Only a permanent rejection forces re-login (covered by
// TestSoftExpiryRecoveryPermanentFailureEscalates).
//
// OLD behavior (the bug this replaces): after the soft-expiry deadline the loop
// called ExpireLocalSession("soft expiry timeout") -> HardInvalid -> proxy
// stopped + "认证已过期，请重新登录". A ~30s network blip forced a full re-login.
// Recorded as a Bug-class divergence for the Rust port (REWRITE_DESIGN.md §4 现场 3).
func TestSoftExpiryRecoveryTransientDoesNotForceRelogin(t *testing.T) {
	resetSoftExpiryForTest()
	ResetAuthPersistenceForTest()
	SetCurrentUserInfo(&UserInfo{AccessToken: "x"}) // retained across transient failures
	softExpiryBackoff = []time.Duration{0}
	softExpiryMaxAttempts = 3
	var refreshCalls int32
	softExpiryRefresh = func() (*UserInfo, error) {
		atomic.AddInt32(&refreshCalls, 1)
		return nil, errors.New("transient network") // session retained -> retry, never wipes
	}

	a := ResetSessionAuthorityForTest()
	a.NotifyLoggedIn(&UserInfo{})
	a.NotifyAccessRejected("agent rejected")
	if a.State() != StateSoftExpired {
		t.Fatalf("precondition: state=%v want SoftExpired", a.State())
	}

	StartSoftExpiryRecovery()
	// The loop now terminates after softExpiryMaxAttempts transient failures
	// WITHOUT leaving SoftExpired, so waitNotSoftExpired does not apply. Poll
	// until the recovery goroutine has made all its attempts, then let it return.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(&refreshCalls) >= 3 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond) // allow the goroutine to return after its final attempt

	if a.State() != StateSoftExpired {
		t.Fatalf("state=%v want SoftExpired (transient failure must NOT force re-login)", a.State())
	}
	if got := atomic.LoadInt32(&refreshCalls); got != 3 {
		t.Fatalf("expected 3 transient refresh attempts then stop, got %d", got)
	}
	if GetCurrentUserInfo() == nil {
		t.Fatal("local user info was wiped on transient failure; must be retained for recovery")
	}
}
