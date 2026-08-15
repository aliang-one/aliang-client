package user

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSessionAuthorityLoginTransitionsToActive(t *testing.T) {
	a := newSessionAuthority(StateRestoring)
	var got []SessionEvent
	a.Subscribe(func(e SessionEvent) { got = append(got, e) })

	a.NotifyLoggedIn(&UserInfo{Email: "x@y.z"})

	if a.State() != StateActive {
		t.Fatalf("state=%v want Active", a.State())
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 event, got %d", len(got))
	}
	if got[0].To != StateActive || got[0].Reason != ReasonLogin || got[0].User == nil || got[0].User.Email != "x@y.z" {
		t.Fatalf("bad event: %+v", got[0])
	}
}

func TestSessionAuthorityRejectsNilAuthenticatedUser(t *testing.T) {
	a := newSessionAuthority(StateUnauthenticated)

	if a.NotifyLoggedIn(nil) {
		t.Fatal("nil user must not produce an authenticated transition")
	}
	if got := a.State(); got != StateUnauthenticated {
		t.Fatalf("state = %s, want unauthenticated", got)
	}
	if a.CanProxy() {
		t.Fatal("nil authenticated user must not enable proxy admission")
	}
}

func TestSessionAuthorityRefreshPublishesNewRevisionInSameState(t *testing.T) {
	a := newSessionAuthority(StateRestoring)
	var fires int
	a.Subscribe(func(e SessionEvent) { fires++ })

	a.NotifyLoggedIn(&UserInfo{})
	first := a.Snapshot()
	a.NotifyRefreshed(&UserInfo{})
	second := a.Snapshot()

	if fires != 2 {
		t.Fatalf("expected login and refresh snapshots, got %d events", fires)
	}
	if second.Revision != first.Revision+1 || second.InstanceID != first.InstanceID {
		t.Fatalf("bad snapshot versions: first=%+v second=%+v", first, second)
	}
}

func TestSessionAuthorityAccessRejectedGoesSoftExpired(t *testing.T) {
	a := newSessionAuthority(StateActive)
	var got SessionEvent
	a.Subscribe(func(e SessionEvent) { got = e })

	a.NotifyAccessRejected("agent rejected")

	if a.State() != StateSoftExpired {
		t.Fatalf("state=%v want SoftExpired", a.State())
	}
	if got.To != StateSoftExpired || got.Reason != ReasonAccessRejected {
		t.Fatalf("bad event: %+v", got)
	}
}

func TestSessionAuthorityAccessRejectedNoOpFromHardInvalid(t *testing.T) {
	a := newSessionAuthority(StateHardInvalid)
	var fired bool
	a.Subscribe(func(e SessionEvent) { fired = true })

	if a.NotifyAccessRejected("late") {
		t.Fatal("expected no transition from HardInvalid")
	}
	if fired {
		t.Fatal("listener must not fire when transitioning from HardInvalid")
	}
	if a.State() != StateHardInvalid {
		t.Fatalf("state=%v want HardInvalid", a.State())
	}
}

func TestSessionAuthorityRefreshFailedPermanentIsHardInvalid(t *testing.T) {
	a := newSessionAuthority(StateActive)
	var got SessionEvent
	a.Subscribe(func(e SessionEvent) { got = e })

	a.NotifyRefreshFailed(true, ReasonRefreshInvalid)

	if a.State() != StateHardInvalid {
		t.Fatalf("state=%v want HardInvalid", a.State())
	}
	if got.Reason != ReasonRefreshInvalid {
		t.Fatalf("bad reason: %+v", got)
	}
}

func TestSessionAuthorityRefreshFailedTransientIsSoftExpired(t *testing.T) {
	a := newSessionAuthority(StateActive)
	a.NotifyRefreshFailed(false, "")
	if a.State() != StateSoftExpired {
		t.Fatalf("state=%v want SoftExpired", a.State())
	}
}

func TestSessionAuthoritySoftExpiredRecoversToActive(t *testing.T) {
	a := newSessionAuthority(StateSoftExpired)
	var got SessionEvent
	a.Subscribe(func(e SessionEvent) { got = e })

	a.NotifyRefreshed(&UserInfo{Email: "r@y.z"})

	if a.State() != StateActive {
		t.Fatalf("state=%v want Active", a.State())
	}
	if got.From != StateSoftExpired || got.To != StateActive || got.Reason != ReasonRefreshed {
		t.Fatalf("bad transition: %+v", got)
	}
}

func TestSessionAuthorityLoggedOutIsUnauthenticated(t *testing.T) {
	a := newSessionAuthority(StateActive)
	var fires int
	a.Subscribe(func(e SessionEvent) {
		if e.To == StateUnauthenticated && e.Reason == ReasonLogout {
			fires++
		}
	})
	a.NotifyLoggedOut()
	a.NotifyLoggedOut()
	if a.State() != StateUnauthenticated {
		t.Fatalf("state=%v want Unauthenticated", a.State())
	}
	if fires != 2 {
		t.Fatalf("logout listener fires=%d want 2", fires)
	}
}

// TestSessionAuthorityRefreshFailedPermanentForceFiresOnEveryWipe locks the
// robustness guarantee: a permanent failure is an explicit teardown (wipe), so
// it must re-fire HardInvalid listeners every time — even when already
// HardInvalid — so idempotent cleanup re-runs on every wipe.
func TestSessionAuthorityRefreshFailedPermanentForceFiresOnEveryWipe(t *testing.T) {
	a := newSessionAuthority(StateActive)
	var fires int
	a.Subscribe(func(e SessionEvent) {
		if e.To == StateHardInvalid {
			fires++
		}
	})
	a.NotifyRefreshFailed(true, ReasonRefreshInvalid) // Active → HardInvalid
	a.NotifyRefreshFailed(true, ReasonRefreshInvalid) // already HardInvalid — force-fire
	if fires != 2 {
		t.Fatalf("expected permanent failure to force-fire on every wipe (2), got %d", fires)
	}
}

func TestSessionAuthorityListenerPanicDoesNotBreakTransition(t *testing.T) {
	a := newSessionAuthority(StateRestoring)
	var got SessionEvent
	a.Subscribe(func(e SessionEvent) { panic("boom") })
	a.Subscribe(func(e SessionEvent) { got = e })

	a.NotifyLoggedIn(&UserInfo{})

	if a.State() != StateActive {
		t.Fatalf("panicking listener broke transition: state=%v", a.State())
	}
	if got.To != StateActive {
		t.Fatalf("second listener did not run after first panicked: %+v", got)
	}
}

func TestSessionAuthoritySnapshotOwnsUserData(t *testing.T) {
	a := newSessionAuthority(StateRestoring)
	user := &UserInfo{Email: "owner@example.com", AllowedGroups: []int64{1, 2}}
	a.NotifyLoggedIn(user)

	user.Email = "mutated@example.com"
	user.AllowedGroups[0] = 99
	first := a.Snapshot()
	if first.User == nil || first.User.Email != "owner@example.com" || first.User.AllowedGroups[0] != 1 {
		t.Fatalf("authority retained mutable input: %+v", first.User)
	}

	first.User.Email = "consumer-mutated@example.com"
	first.User.AllowedGroups[0] = 88
	second := a.Snapshot()
	if second.User.Email != "owner@example.com" || second.User.AllowedGroups[0] != 1 {
		t.Fatalf("authority exposed mutable snapshot: %+v", second.User)
	}
}

func TestSessionAuthorityStaleOperationCannotReactivateAfterLogout(t *testing.T) {
	a := newSessionAuthority(StateActive)
	operation := a.BeginOperation()
	defer operation.Close()

	a.NotifyLoggedOut()
	persisted := false
	err := a.CommitAuthenticated(operation, &UserInfo{Email: "late@example.com"}, ReasonRefreshed, func(*UserInfo) error {
		persisted = true
		return nil
	})
	if !errors.Is(err, ErrStaleSessionOperation) {
		t.Fatalf("CommitAuthenticated() error=%v want ErrStaleSessionOperation", err)
	}
	if persisted {
		t.Fatal("stale operation executed persistence callback")
	}
	if a.State() != StateUnauthenticated || a.CanProxy() {
		t.Fatalf("stale operation revived authority: %+v", a.Snapshot())
	}
	if stats := a.Stats(); stats.StaleOperationCommits != 1 {
		t.Fatalf("stale operation commits=%d, want 1", stats.StaleOperationCommits)
	}
}

func TestSessionAuthorityCommitAuthenticatedInvalidatesPeerOperations(t *testing.T) {
	a := newSessionAuthority(StateRestoring)
	winner := a.BeginOperation()
	loser := a.BeginOperation()
	defer winner.Close()
	defer loser.Close()

	if err := a.CommitAuthenticated(winner, &UserInfo{Email: "winner@example.com"}, ReasonLogin, nil); err != nil {
		t.Fatalf("winner commit: %v", err)
	}
	if err := a.CommitAuthenticated(loser, &UserInfo{Email: "loser@example.com"}, ReasonLogin, nil); !errors.Is(err, ErrStaleSessionOperation) {
		t.Fatalf("loser commit error=%v want stale", err)
	}
	if got := a.Snapshot().User.Email; got != "winner@example.com" {
		t.Fatalf("active user=%q want winner", got)
	}
}

func TestSessionAuthorityCanProxyOnlyWhenActive(t *testing.T) {
	for _, state := range []SessionState{StateRestoring, StateUnauthenticated, StateSoftExpired, StateHardInvalid} {
		if newSessionAuthority(state).CanProxy() {
			t.Fatalf("CanProxy()=true for %s", state)
		}
	}
	if !newSessionAuthority(StateActive).CanProxy() {
		t.Fatal("CanProxy()=false for Active")
	}
}

func TestSessionAuthorityProxyLeaseDeniedOutsideActive(t *testing.T) {
	a := newSessionAuthority(StateRestoring)
	if lease, err := a.AcquireProxyLease(func() {}); !errors.Is(err, ErrProxyAdmissionDenied) || lease != nil {
		t.Fatalf("AcquireProxyLease()=(%#v,%v), want nil/ErrProxyAdmissionDenied", lease, err)
	}
	rejected, forced := a.ProxyAdmissionStats()
	if rejected != 1 || forced != 0 {
		t.Fatalf("proxy admission stats=(%d,%d), want (1,0)", rejected, forced)
	}
}

func TestSessionAuthorityDemotionClosesAdmittedProxyLease(t *testing.T) {
	a := newSessionAuthority(StateActive)
	var closes atomic.Int32
	lease, err := a.AcquireProxyLease(func() { closes.Add(1) })
	if err != nil {
		t.Fatalf("AcquireProxyLease() error=%v", err)
	}

	a.NotifyAccessRejected("rejected")
	lease.Release()

	if closes.Load() != 1 {
		t.Fatalf("flow close callbacks=%d, want 1", closes.Load())
	}
	if lease, err := a.AcquireProxyLease(nil); !errors.Is(err, ErrProxyAdmissionDenied) || lease != nil {
		t.Fatalf("post-demotion admission=(%#v,%v), want denied", lease, err)
	}
	_, forced := a.ProxyAdmissionStats()
	if forced != 1 {
		t.Fatalf("forced flow closes=%d, want 1", forced)
	}
	if stats := a.Stats(); stats.ActiveProxyFlows != 0 {
		t.Fatalf("active proxy flows=%d, want 0", stats.ActiveProxyFlows)
	}
}

func TestSessionAuthorityProxyAdmissionLinearizesWithDemotion(t *testing.T) {
	a := newSessionAuthority(StateActive)
	const attempts = 128
	var admitted atomic.Int32
	var closed atomic.Int32
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			lease, err := a.AcquireProxyLease(func() { closed.Add(1) })
			if err != nil {
				return
			}
			admitted.Add(1)
			defer lease.Release()
		}()
	}
	close(start)
	a.NotifyLoggedOut()
	wg.Wait()

	if got, want := closed.Load(), admitted.Load(); got != want {
		t.Fatalf("closed admitted flows=%d, want %d", got, want)
	}
}

// TestClearLocalSessionAfterExpirationTransitionsAuthorityToHardInvalid locks
// the producer wiring: the existing wipe path must drive the authority into
// HardInvalid with a structured reason, so the services-layer teardown listener
// (onSessionEvent) fans out.
func TestClearLocalSessionAfterExpirationTransitionsAuthorityToHardInvalid(t *testing.T) {
	ResetAuthPersistenceForTest()
	authority := ResetSessionAuthorityForTest()
	if err := SaveUserInfo(&UserInfo{AccessToken: "a", RefreshToken: "r", UpdatedAt: time.Now()}); err != nil {
		t.Fatalf("SaveUserInfo: %v", err)
	}

	var got SessionEvent
	authority.Subscribe(func(e SessionEvent) { got = e })

	clearLocalSessionAfterExpiration("invalid refresh token")

	if authority.State() != StateHardInvalid {
		t.Fatalf("authority state=%v want HardInvalid", authority.State())
	}
	if got.To != StateHardInvalid || got.Reason != ReasonRefreshInvalid {
		t.Fatalf("bad transition event: %+v", got)
	}
}
