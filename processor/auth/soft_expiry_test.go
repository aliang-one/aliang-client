package user

import (
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"aliang.one/nursorgate/processor/config"
)

func resetSoftExpiryForTest() {
	softExpiryRunning = false
	softExpiryTimeout = defaultSoftExpiryTimeout
	softExpiryBackoff = []time.Duration{0, 5 * time.Second, 15 * time.Second}
	softExpiryRefresh = func() (*UserInfo, error) { return RefreshSession("") }
	softExpirySleep = func(time.Duration) {}
	softExpiryNow = time.Now
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

func TestSoftExpiryRecoveryTimeoutEscalates(t *testing.T) {
	baseDir := t.TempDir()
	t.Setenv("HOME", filepath.Join(baseDir, "home"))
	t.Setenv("ALIANG_CACHE_DIR", filepath.Join(baseDir, "cache"))
	defer StopTokenRefresh()
	defer ResetAuthPersistenceForTest()
	defer config.ResetGlobalConfigForTest()
	ResetAuthPersistenceForTest()
	config.ResetGlobalConfigForTest()
	if err := os.MkdirAll(filepath.Join(baseDir, "home"), 0o700); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	if err := SaveUserInfo(&UserInfo{AccessToken: "stale", RefreshToken: "stale", UpdatedAt: time.Now()}); err != nil {
		t.Fatalf("SaveUserInfo() error = %v", err)
	}

	resetSoftExpiryForTest()
	softExpiryBackoff = []time.Duration{0}
	softExpiryTimeout = 1 * time.Second
	var ticks int64
	softExpiryNow = func() time.Time {
		n := atomic.AddInt64(&ticks, 1)
		return time.Unix(n*100, 0) // each call advances +100s, so deadline passes immediately
	}
	softExpiryRefresh = func() (*UserInfo, error) {
		return nil, errors.New("transient")
	}

	a := ResetSessionAuthorityForTest()
	a.NotifyLoggedIn(&UserInfo{})
	a.NotifyAccessRejected("agent rejected")

	StartSoftExpiryRecovery()
	waitNotSoftExpired(t, a)

	if a.State() != StateHardInvalid {
		t.Fatalf("state=%v want HardInvalid after timeout", a.State())
	}
	if got := GetCurrentUserInfo(); got != nil {
		t.Fatalf("current user after timeout = %#v, want nil", got)
	}
	if hasUserInfo, err := HasPersistedUserInfo(); err != nil {
		t.Fatalf("HasPersistedUserInfo() error = %v", err)
	} else if hasUserInfo {
		t.Fatal("persisted user survived soft-expiry timeout")
	}
}
