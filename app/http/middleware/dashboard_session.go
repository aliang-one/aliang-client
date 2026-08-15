package middleware

import (
	"crypto/rand"
	"crypto/sha256"
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
	dashboardSessionMaxActive  = 64
)

var dashboardSessionState struct {
	sync.Mutex
	sessions map[[sha256.Size]byte]time.Time
}

// IssueDashboardSession rotates the request-bound local management credential.
// Loopback may bootstrap it before upstream authentication; a remote browser
// may only receive it after a successful upstream login.
func IssueDashboardSession(w http.ResponseWriter, r *http.Request) error {
	if !isLoopbackRequest(r) {
		if auth.GetSessionAuthority().State() != auth.StateActive {
			return errors.New("cannot issue remote dashboard session without an active user session")
		}
	}

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return fmt.Errorf("generate dashboard session: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	now := time.Now()
	dashboardSessionState.Lock()
	if dashboardSessionState.sessions == nil {
		dashboardSessionState.sessions = make(map[[sha256.Size]byte]time.Time)
	}
	pruneDashboardSessionsLocked(now)
	for len(dashboardSessionState.sessions) >= dashboardSessionMaxActive {
		evictOldestDashboardSessionLocked()
	}
	dashboardSessionState.sessions[sha256.Sum256([]byte(token))] = now.Add(dashboardSessionTTL)
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

// RevokeDashboardSession explicitly clears the local management credential.
// Upstream logout does not call this: the local dashboard must remain able to
// observe the Unauthenticated snapshot and initiate a new login.
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
	key := sha256.Sum256([]byte(strings.TrimSpace(cookie.Value)))
	now := time.Now()
	dashboardSessionState.Lock()
	expiresAt, ok := dashboardSessionState.sessions[key]
	if ok && !now.Before(expiresAt) {
		delete(dashboardSessionState.sessions, key)
		ok = false
	}
	dashboardSessionState.Unlock()
	if !ok {
		return false
	}
	return true
}

// CanBootstrapDashboardSession allows persisted-session restoration only from
// loopback. Remote browsers must prove identity through an explicit login.
func CanBootstrapDashboardSession(r *http.Request) bool {
	if ValidateDashboardSession(r) {
		return true
	}
	return isLoopbackRequest(r)
}

func isLoopbackRequest(r *http.Request) bool {
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

func clearDashboardSession() {
	dashboardSessionState.Lock()
	dashboardSessionState.sessions = nil
	dashboardSessionState.Unlock()
}

func pruneDashboardSessionsLocked(now time.Time) {
	for key, expiresAt := range dashboardSessionState.sessions {
		if !now.Before(expiresAt) {
			delete(dashboardSessionState.sessions, key)
		}
	}
}

func evictOldestDashboardSessionLocked() {
	var oldestKey [sha256.Size]byte
	var oldestExpiry time.Time
	found := false
	for key, expiresAt := range dashboardSessionState.sessions {
		if !found || expiresAt.Before(oldestExpiry) {
			oldestKey = key
			oldestExpiry = expiresAt
			found = true
		}
	}
	if found {
		delete(dashboardSessionState.sessions, oldestKey)
	}
}

func ResetDashboardSessionForTest() {
	clearDashboardSession()
}
