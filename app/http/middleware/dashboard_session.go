package middleware

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"aliang.one/nursorgate/app/http/common"
	auth "aliang.one/nursorgate/processor/auth"
)

const (
	DashboardSessionCookieName = "aliang_dashboard_session"
	dashboardSessionTTL        = 24 * time.Hour
)

type dashboardSession struct {
	token       string
	identity    string
	expiresAt   time.Time
	authority   *auth.SessionAuthority
	initialized bool
}

var dashboardSessionState struct {
	sync.Mutex
	dashboardSession
}

// IssueDashboardSession rotates the browser credential after a successful
// login or local session restore. Only one dashboard browser session is active
// at a time, which keeps revocation deterministic for this desktop service.
func IssueDashboardSession(w http.ResponseWriter, r *http.Request) error {
	identity, ok := dashboardIdentity(false)
	if !ok {
		return errors.New("cannot issue dashboard session without an active user session")
	}

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return fmt.Errorf("generate dashboard session: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	authority := auth.GetSessionAuthority()
	ensureDashboardSessionSubscription(authority)

	dashboardSessionState.Lock()
	dashboardSessionState.dashboardSession = dashboardSession{
		token:       token,
		identity:    identity,
		expiresAt:   time.Now().Add(dashboardSessionTTL),
		authority:   authority,
		initialized: true,
	}
	dashboardSessionState.Unlock()

	http.SetCookie(w, &http.Cookie{
		Name:     DashboardSessionCookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   int(dashboardSessionTTL.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   r != nil && r.TLS != nil,
	})
	return nil
}

// RevokeDashboardSession clears both the server-side credential and browser
// cookie. SessionAuthority hard-invalid transitions also clear server state.
func RevokeDashboardSession(w http.ResponseWriter) {
	clearDashboardSession()
	if w == nil {
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     DashboardSessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
}

func RequireDashboardSession(w http.ResponseWriter, r *http.Request) bool {
	if ValidateDashboardSession(r) {
		return true
	}
	common.ErrorUnauthorized(w, "Authenticated dashboard session is required")
	return false
}

func ValidateDashboardSession(r *http.Request) bool {
	if r == nil {
		return false
	}
	cookie, err := r.Cookie(DashboardSessionCookieName)
	if err != nil || strings.TrimSpace(cookie.Value) == "" {
		return false
	}
	identity, ok := dashboardIdentity(true)
	if !ok {
		return false
	}
	authority := auth.GetSessionAuthority()
	ensureDashboardSessionSubscription(authority)

	dashboardSessionState.Lock()
	session := dashboardSessionState.dashboardSession
	dashboardSessionState.Unlock()
	if !session.initialized || session.authority != authority || time.Now().After(session.expiresAt) || session.identity != identity {
		return false
	}
	provided := []byte(cookie.Value)
	expected := []byte(session.token)
	return len(provided) == len(expected) && subtle.ConstantTimeCompare(provided, expected) == 1
}

// CanBootstrapDashboardSession allows persisted-session restoration only from
// loopback. Remote browsers must prove identity through an explicit login.
func CanBootstrapDashboardSession(r *http.Request) bool {
	if ValidateDashboardSession(r) {
		return true
	}
	if r == nil {
		return false
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func dashboardIdentity(allowSoftExpired bool) (string, bool) {
	state := auth.GetSessionAuthority().State()
	if state != auth.StateActive && !(allowSoftExpired && state == auth.StateSoftExpired) {
		return "", false
	}
	current := auth.GetCurrentUserInfoOrLoad()
	if current == nil {
		return "", false
	}
	return fmt.Sprintf("%d\x00%s\x00%s", current.ID, current.Username, current.Email), true
}

func ensureDashboardSessionSubscription(authority *auth.SessionAuthority) {
	if authority == nil {
		return
	}
	dashboardSessionState.Lock()
	alreadySubscribed := dashboardSessionState.authority == authority
	if !alreadySubscribed {
		dashboardSessionState.authority = authority
	}
	dashboardSessionState.Unlock()
	if alreadySubscribed {
		return
	}
	authority.Subscribe(func(event auth.SessionEvent) {
		if event.To == auth.StateHardInvalid || event.To == auth.StateUnauthenticated {
			clearDashboardSession()
		}
	})
}

func clearDashboardSession() {
	dashboardSessionState.Lock()
	authority := dashboardSessionState.authority
	dashboardSessionState.dashboardSession = dashboardSession{authority: authority}
	dashboardSessionState.Unlock()
}

func ResetDashboardSessionForTest() {
	dashboardSessionState.Lock()
	dashboardSessionState.dashboardSession = dashboardSession{}
	dashboardSessionState.Unlock()
}
