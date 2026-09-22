package user

import (
	"fmt"
	"strings"
	"time"

	"aliang.one/nursorgate/common/logger"
)

// pushRefreshNow / pushNow are test hooks mirroring the softExpiry* pattern.
var (
	pushRefreshNow = func() (*UserInfo, error) { return RefreshSession("") }
	pushNow        = time.Now
)

// TokenNearExpiry reports whether the cached access token is expired or inside
// the refresh lead window (tokenRefreshLeadTime). It intentionally differs from
// TokenRefresher.isTokenExpired on the unknown-expiry case: a session without
// ExpiresIn metadata reports NOT near expiry here, because the push path would
// otherwise refresh on every sync for sessions that carry no expiry anchor;
// those are protected by the 401 recovery chain (agent → owner notify) instead.
func TokenNearExpiry(info *UserInfo) bool {
	if info == nil || info.ExpiresIn <= 0 {
		return false
	}
	expireAt := info.UpdatedAt.Add(time.Duration(info.ExpiresIn) * time.Second).Add(-tokenRefreshLeadTime)
	return pushNow().After(expireAt)
}

// EnsureFreshAccessTokenForPush is the pre-push guard for session-owner pushes
// to the user agent (SyncUserAgentAfterAuthWithRetry). A dead access token
// pushed today comes back as a register 401 tomorrow and sticky-disables the
// agent, so the push path must never forward a token that is already at (or
// inside the lead window of) expiry: refresh first, then hand out the fresh
// header. When the refresh fails, the empty header is NOT returned — callers
// must abort the push and let the existing retry loop try again later; pushing
// the stale token would be strictly worse than not pushing.
//
// Non-owner processes and sessions without a usable header pass through
// unchanged: the caller's existing "authorization not available" handling
// covers them, and only the session owner may refresh (RefreshSession enforces
// this itself).
func EnsureFreshAccessTokenForPush() (string, error) {
	header := strings.TrimSpace(GetCurrentAuthorizationHeader())
	if header == "" || !IsSessionOwnerProcess() {
		return header, nil
	}
	if !TokenNearExpiry(GetCurrentUserInfoOrLoad()) {
		return header, nil
	}
	logger.Info("access token near expiry; refreshing before pushing session to agent")
	if _, err := pushRefreshNow(); err != nil {
		return "", fmt.Errorf("pre-push token refresh failed: %w", err)
	}
	if fresh := strings.TrimSpace(GetCurrentAuthorizationHeader()); fresh != "" {
		return fresh, nil
	}
	return header, nil
}
