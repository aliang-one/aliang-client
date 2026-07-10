package user

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"aliang.one/nursorgate/processor/config"
)

func TestGetUserProfile_ClearsSessionWhenRefreshTokenAlsoInvalid(t *testing.T) {
	baseDir, err := os.MkdirTemp("", "aliang-user-center-auth-*")
	if err != nil {
		t.Fatalf("MkdirTemp() error = %v", err)
	}
	t.Setenv("HOME", filepath.Join(baseDir, "home"))
	t.Setenv("ALIANG_CACHE_DIR", filepath.Join(baseDir, "cache"))
	defer os.RemoveAll(baseDir)

	defer StopTokenRefresh()
	defer ResetAuthPersistenceForTest()
	defer config.ResetGlobalConfigForTest()

	ResetAuthPersistenceForTest()
	StopTokenRefresh()
	config.ResetGlobalConfigForTest()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/user/profile":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"code":"TOKEN_REVOKED","message":"Token has been revoked (password changed)"}`))
		case "/api/v1/auth/refresh":
			// Genuinely revoked session: the refresh token is also invalid, so the
			// liveness probe confirms the session is dead and clears it.
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"code":401,"message":"invalid refresh token","reason":"REFRESH_TOKEN_INVALID"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	config.SetGlobalConfig(&config.Config{Core: &config.CoreConfig{APIServer: server.URL}})

	if err := SaveUserInfo(&UserInfo{
		AccessToken:  "stale-access-token",
		RefreshToken: "stale-refresh-token",
		TokenType:    "Bearer",
		Username:     "stale-user",
		UpdatedAt:    time.Now(),
	}); err != nil {
		t.Fatalf("SaveUserInfo() error = %v", err)
	}

	_, err = GetUserProfile()
	if !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("GetUserProfile() error = %v, want ErrSessionExpired", err)
	}
	if got := GetCurrentUserInfo(); got != nil {
		t.Fatalf("current user info = %#v, want nil", got)
	}
	if hasUserInfo, err := HasPersistedUserInfo(); err != nil {
		t.Fatalf("HasPersistedUserInfo() error = %v", err)
	} else if hasUserInfo {
		t.Fatal("expected persisted user info to be cleared")
	}
}

// TestCallUserCenterAPI_RetainsSessionOnTransient401 locks the B fix: a
// spurious 401 from a user-center API (backend deploy, load-balancer hiccup,
// clock skew, momentary network blip surfacing as 401) must NOT wipe the local
// session. The session is probed via a refresh — when refresh succeeds the 401
// is treated as transient, the access_token is renewed, and the caller retries
// later instead of being forced to re-login.
func TestCallUserCenterAPI_RetainsSessionOnTransient401(t *testing.T) {
	baseDir, err := os.MkdirTemp("", "aliang-user-center-transient-*")
	if err != nil {
		t.Fatalf("MkdirTemp() error = %v", err)
	}
	t.Setenv("HOME", filepath.Join(baseDir, "home"))
	t.Setenv("ALIANG_CACHE_DIR", filepath.Join(baseDir, "cache"))
	defer os.RemoveAll(baseDir)

	defer StopTokenRefresh()
	defer ResetAuthPersistenceForTest()
	defer config.ResetGlobalConfigForTest()

	ResetAuthPersistenceForTest()
	StopTokenRefresh()
	config.ResetGlobalConfigForTest()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/user/profile":
			// The original call carries the stale token and gets a spurious 401;
			// the profile fetch performed after a successful refresh carries the
			// fresh token and must succeed.
			if strings.Contains(r.Header.Get("Authorization"), "fresh-access-token") {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"data":{"id":1,"email":"user@example.com","username":"user","role":"member","balance":1,"concurrency":1,"status":"active","allowed_groups":[1],"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-04-09T00:00:00Z"}}`))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"code":"TOKEN_EXPIRED","message":"token expired"}`))
		case "/api/v1/auth/refresh":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":{"access_token":"fresh-access-token","refresh_token":"fresh-refresh-token","expires_in":3600,"token_type":"Bearer"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	config.SetGlobalConfig(&config.Config{Core: &config.CoreConfig{APIServer: server.URL}})

	if err := SaveUserInfo(&UserInfo{
		AccessToken:  "stale-access-token",
		RefreshToken: "stale-refresh-token",
		TokenType:    "Bearer",
		Username:     "stale-user",
		UpdatedAt:    time.Now(),
	}); err != nil {
		t.Fatalf("SaveUserInfo() error = %v", err)
	}

	_, err = GetUserProfile()
	if err == nil {
		t.Fatal("GetUserProfile() error = nil, want the original 401 error (not a silent success)")
	}
	if errors.Is(err, ErrSessionExpired) {
		t.Fatalf("GetUserProfile() error = ErrSessionExpired; a transient 401 must NOT expire the session (got %v)", err)
	}

	// Session must be retained and the token renewed by the liveness refresh.
	if got := GetCurrentUserInfo(); got == nil {
		t.Fatal("current user info = nil, want retained session after transient 401")
	} else if got.AccessToken != "fresh-access-token" {
		t.Fatalf("access token = %q, want fresh-access-token (renewed by liveness refresh)", got.AccessToken)
	}
	if hasUserInfo, err := HasPersistedUserInfo(); err != nil {
		t.Fatalf("HasPersistedUserInfo() error = %v", err)
	} else if !hasUserInfo {
		t.Fatal("expected persisted user info to be retained after transient 401")
	}
}

// TestClearLocalSessionAfterExpiredAccessToken_RetainsSessionWhenRefreshSucceeds
// locks the D self-heal: an expired-access-token wipe must first attempt a
// recovery refresh. When the refresh token is still valid the access token is
// renewed and the session survives — no auth_expired, no forced re-login. The
// access token typically expires only because the background refresher missed
// its lead-time window (e.g. a transient network failure during refresh); that
// must not escalate to a permanent disable.
func TestClearLocalSessionAfterExpiredAccessToken_GoesSoftExpiredRetainsSession(t *testing.T) {
	baseDir, err := os.MkdirTemp("", "aliang-clear-expired-recover-*")
	if err != nil {
		t.Fatalf("MkdirTemp() error = %v", err)
	}
	t.Setenv("HOME", filepath.Join(baseDir, "home"))
	t.Setenv("ALIANG_CACHE_DIR", filepath.Join(baseDir, "cache"))
	defer os.RemoveAll(baseDir)

	defer StopTokenRefresh()
	defer ResetAuthPersistenceForTest()
	defer config.ResetGlobalConfigForTest()

	ResetAuthPersistenceForTest()
	StopTokenRefresh()
	config.ResetGlobalConfigForTest()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/refresh":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":{"access_token":"fresh-access-token","refresh_token":"fresh-refresh-token","expires_in":3600,"token_type":"Bearer"}}`))
		case "/api/v1/user/profile":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":{"id":1,"email":"user@example.com","username":"user","role":"member","balance":1,"concurrency":1,"status":"active","allowed_groups":[1],"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-04-09T00:00:00Z"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	config.SetGlobalConfig(&config.Config{Core: &config.CoreConfig{APIServer: server.URL}})

	if err := SaveUserInfo(&UserInfo{
		AccessToken:  "stale-access-token",
		RefreshToken: "stale-refresh-token",
		TokenType:    "Bearer",
		Username:     "stale-user",
		UpdatedAt:    time.Now(),
	}); err != nil {
		t.Fatalf("SaveUserInfo() error = %v", err)
	}

	authority := ResetSessionAuthorityForTest()
	hardInvalidFired := false
	authority.Subscribe(func(e SessionEvent) {
		if e.To == StateHardInvalid {
			hardInvalidFired = true
		}
	})
	authority.NotifyLoggedIn(&UserInfo{}) // session is Active when access is rejected

	clearLocalSessionAfterExpiredAccessToken()

	if hardInvalidFired {
		t.Fatal("HardInvalid fired; want SoftExpired (recovery is async via the coordinator)")
	}
	if authority.State() != StateSoftExpired {
		t.Fatalf("authority state=%v want SoftExpired", authority.State())
	}
	if got := GetCurrentUserInfo(); got == nil {
		t.Fatal("current user info = nil; want session retained in SoftExpired")
	}
	if hasUserInfo, err := HasPersistedUserInfo(); err != nil {
		t.Fatalf("HasPersistedUserInfo() error = %v", err)
	} else if !hasUserInfo {
		t.Fatal("expected persisted user info retained in SoftExpired")
	}
}
