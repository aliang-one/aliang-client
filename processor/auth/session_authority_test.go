package user

import (
	"testing"
	"time"
)

func TestSessionAuthorityLoginTransitionsToActive(t *testing.T) {
	a := &SessionAuthority{}
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

func TestSessionAuthorityIdempotentNoRefireOnSameState(t *testing.T) {
	a := &SessionAuthority{}
	var fires int
	a.Subscribe(func(e SessionEvent) { fires++ })

	a.NotifyLoggedIn(&UserInfo{})  // →Active, fires once
	a.NotifyRefreshed(&UserInfo{}) // Active→Active: must NOT refire

	if fires != 1 {
		t.Fatalf("expected 1 fire (idempotent), got %d", fires)
	}
	if a.State() != StateActive {
		t.Fatalf("state=%v want Active", a.State())
	}
}

func TestSessionAuthorityAccessRejectedGoesSoftExpired(t *testing.T) {
	a := &SessionAuthority{state: StateActive}
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
	a := &SessionAuthority{state: StateHardInvalid}
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
	a := &SessionAuthority{state: StateActive}
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
	a := &SessionAuthority{state: StateActive}
	a.NotifyRefreshFailed(false, "")
	if a.State() != StateSoftExpired {
		t.Fatalf("state=%v want SoftExpired", a.State())
	}
}

func TestSessionAuthoritySoftExpiredRecoversToActive(t *testing.T) {
	a := &SessionAuthority{state: StateSoftExpired}
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

func TestSessionAuthorityLoggedOutIsHardInvalid(t *testing.T) {
	a := &SessionAuthority{state: StateActive}
	var fires int
	a.Subscribe(func(e SessionEvent) {
		if e.To == StateHardInvalid && e.Reason == ReasonLogout {
			fires++
		}
	})
	a.NotifyLoggedOut()
	a.NotifyLoggedOut()
	if a.State() != StateHardInvalid {
		t.Fatalf("state=%v want HardInvalid", a.State())
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
	a := &SessionAuthority{state: StateActive}
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
	a := &SessionAuthority{}
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
