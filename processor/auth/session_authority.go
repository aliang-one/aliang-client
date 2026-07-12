package user

import (
	"fmt"
	"strings"
	"sync"

	"aliang.one/nursorgate/common/logger"
)

// SessionAuthority is the single source of truth for the local user-identity
// state machine. Producers (login / refresh / access-rejected / refresh-failed
// / logout) call Notify*; consumers Subscribe to SessionEvent to fan out
// per-transition side effects (clear local data, UI state, proxy, agent).
//
// Ordinary transitions are idempotent. Explicit teardown commands (permanent
// refresh failure and logout) force-fire listeners so repeated cleanup remains
// safe. A panicking listener is recovered + logged so one bad consumer cannot
// break auth-critical paths (login/refresh).

type SessionState int

const (
	StateUnauthenticated SessionState = iota
	StateActive
	StateSoftExpired
	StateHardInvalid
)

func (s SessionState) String() string {
	switch s {
	case StateActive:
		return "active"
	case StateSoftExpired:
		return "soft_expired"
	case StateHardInvalid:
		return "hard_invalid"
	default:
		return "unauthenticated"
	}
}

type SessionReason string

const (
	ReasonLogin             SessionReason = "login"
	ReasonRefreshed         SessionReason = "refreshed"
	ReasonAccessRejected    SessionReason = "access_rejected"
	ReasonRefreshInvalid    SessionReason = "refresh_invalid"
	ReasonSoftExpiryTimeout SessionReason = "soft_expiry_timeout"
	ReasonRevoked           SessionReason = "revoked"
	ReasonLogout            SessionReason = "logout"
)

type SessionEvent struct {
	From   SessionState
	To     SessionState
	Reason SessionReason
	User   *UserInfo
}

type SessionListener func(SessionEvent)

type SessionAuthority struct {
	mu        sync.RWMutex
	state     SessionState
	listeners []SessionListener
}

func (a *SessionAuthority) State() SessionState {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.state
}

func (a *SessionAuthority) Subscribe(l SessionListener) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.listeners = append(a.listeners, l)
}

// transition moves to `to`; fires listeners ONLY on a real state change.
// Returns true iff a transition occurred.
func (a *SessionAuthority) transition(to SessionState, reason SessionReason, user *UserInfo) bool {
	a.mu.Lock()
	from := a.state
	if from == to {
		a.mu.Unlock()
		return false
	}
	a.state = to
	listeners := append([]SessionListener(nil), a.listeners...)
	a.mu.Unlock()

	ev := SessionEvent{From: from, To: to, Reason: reason, User: user}
	for _, l := range listeners {
		func(l SessionListener) {
			defer func() {
				if r := recover(); r != nil {
					logger.Warn(fmt.Sprintf("session authority listener panicked (event %s->%s reason=%s): %v", ev.From, ev.To, ev.Reason, r))
				}
			}()
			l(ev)
		}(l)
	}
	return true
}

func (a *SessionAuthority) NotifyLoggedIn(user *UserInfo) bool {
	return a.transition(StateActive, ReasonLogin, user)
}

func (a *SessionAuthority) NotifyRefreshed(user *UserInfo) bool {
	return a.transition(StateActive, ReasonRefreshed, user)
}

func (a *SessionAuthority) NotifyAccessRejected(_ string) bool {
	return a.softExpire(ReasonAccessRejected)
}

// NotifyRefreshFailed escalates: permanent -> HardInvalid, transient -> SoftExpired.
// reason defaults to ReasonRefreshInvalid (permanent) / ReasonAccessRejected (transient).
//
// A permanent failure is an explicit teardown request (a wipe), so it ALWAYS
// fires the HardInvalid event — even if already HardInvalid — so idempotent
// cleanup (StopIngressIfActive etc.) re-runs on every wipe. Duplicate
// notifications are avoided because the cleanup itself is idempotent (e.g.
// StopIngressIfActive returns false, and thus skips the desktop notify, when the
// proxy is already stopped).
func (a *SessionAuthority) NotifyRefreshFailed(permanent bool, reason SessionReason) bool {
	if permanent {
		if reason == "" {
			reason = ReasonRefreshInvalid
		}
		return a.forceTransition(StateHardInvalid, reason, nil)
	}
	if reason == "" {
		reason = ReasonAccessRejected
	}
	return a.softExpire(reason)
}

// forceTransition sets the state to `to` and ALWAYS fires listeners, even when
// already in `to` — for explicit teardown signals that must re-run idempotent
// cleanup on every occurrence. Returns whether the state actually changed.
func (a *SessionAuthority) forceTransition(to SessionState, reason SessionReason, user *UserInfo) bool {
	a.mu.Lock()
	from := a.state
	changed := from != to
	a.state = to
	listeners := append([]SessionListener(nil), a.listeners...)
	a.mu.Unlock()

	ev := SessionEvent{From: from, To: to, Reason: reason, User: user}
	for _, l := range listeners {
		func(l SessionListener) {
			defer func() {
				if r := recover(); r != nil {
					logger.Warn(fmt.Sprintf("session authority listener panicked (event %s->%s reason=%s): %v", ev.From, ev.To, ev.Reason, r))
				}
			}()
			l(ev)
		}(l)
	}
	return changed
}

// softExpire transitions to SoftExpired only from a recoverable state (Active or
// already SoftExpired). From HardInvalid/Unauthenticated it is a no-op — a dead
// or absent session cannot become "transiently rejected".
func (a *SessionAuthority) softExpire(reason SessionReason) bool {
	a.mu.Lock()
	from := a.state
	a.mu.Unlock()
	if from != StateActive && from != StateSoftExpired {
		return false
	}
	return a.transition(StateSoftExpired, reason, nil)
}

func (a *SessionAuthority) NotifyLoggedOut() bool {
	// Logout is an explicit teardown command. Force-fire listeners even when the
	// session is already HardInvalid so proxy/agent cleanup is never skipped.
	return a.forceTransition(StateHardInvalid, ReasonLogout, nil)
}

// sessionReasonFromWipeReason maps the free-form reason string carried by
// clearLocalSessionAfterExpiration to a structured SessionReason. Every wipe is
// a hard (permanent) failure, so callers pass permanent=true; this only labels it.
func sessionReasonFromWipeReason(reason string) SessionReason {
	switch {
	case strings.Contains(strings.ToLower(reason), "invalid refresh"):
		return ReasonRefreshInvalid
	case strings.Contains(strings.ToLower(reason), "timeout"):
		return ReasonSoftExpiryTimeout
	case strings.Contains(strings.ToLower(reason), "revoke") || strings.Contains(reason, "吊销"):
		return ReasonRevoked
	default:
		return ReasonRefreshInvalid
	}
}

var (
	sessionAuthorityOnce sync.Once
	sessionAuthorityInst *SessionAuthority
)

// GetSessionAuthority returns the process-wide authority singleton. Initial
// state is Unauthenticated; the boot sequence (RestoreSession / login) raises
// it to Active.
func GetSessionAuthority() *SessionAuthority {
	sessionAuthorityOnce.Do(func() {
		sessionAuthorityInst = &SessionAuthority{state: StateUnauthenticated}
	})
	return sessionAuthorityInst
}

// ResetSessionAuthorityForTest resets the singleton for isolated tests.
func ResetSessionAuthorityForTest() *SessionAuthority {
	sessionAuthorityOnce = sync.Once{}
	sessionAuthorityInst = nil
	return GetSessionAuthority()
}
