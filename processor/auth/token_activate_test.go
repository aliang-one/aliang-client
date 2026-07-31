package user

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"aliang.one/nursorgate/processor/config"
)

func TestRefreshSession_SerializesTokenRotation(t *testing.T) {
	baseDir, err := os.MkdirTemp("", "aliang-refresh-session-*")
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

	var refreshCalls int32
	var refreshTokensMu sync.Mutex
	refreshTokens := make([]string, 0, 4)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/refresh":
			var payload struct {
				RefreshToken string `json:"refresh_token"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode refresh payload failed: %v", err)
			}

			refreshTokensMu.Lock()
			refreshTokens = append(refreshTokens, payload.RefreshToken)
			refreshTokensMu.Unlock()

			callIndex := atomic.AddInt32(&refreshCalls, 1)
			if payload.RefreshToken == "st-1" && callIndex == 1 {
				time.Sleep(120 * time.Millisecond)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"data":{"access_token":"st-2","refresh_token":"st-2","expires_in":3600,"token_type":"Bearer"}}`))
				return
			}
			if payload.RefreshToken == "st-1" {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"code":401,"message":"invalid refresh token","reason":"REFRESH_TOKEN_INVALID"}`))
				return
			}
			if payload.RefreshToken == "st-2" {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"data":{"access_token":"st-3","refresh_token":"st-3","expires_in":3600,"token_type":"Bearer"}}`))
				return
			}

			t.Fatalf("unexpected refresh token: %q", payload.RefreshToken)

		case "/api/v1/user/profile":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":{"id":1,"email":"user@example.com","username":"user","role":"member","balance":12.5,"concurrency":2,"status":"active","allowed_groups":[1,2],"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-04-09T00:00:00Z"}}`))
			return
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	config.SetGlobalConfig(&config.Config{Core: &config.CoreConfig{APIServer: server.URL}})

	if err := SaveUserInfo(&UserInfo{
		AccessToken:  "st-1",
		RefreshToken: "st-1",
		TokenType:    "Bearer",
		Username:     "user",
		Email:        "user@example.com",
		UpdatedAt:    time.Now().Add(-30 * time.Minute),
		ExpiresIn:    3600,
	}); err != nil {
		t.Fatalf("SaveUserInfo() error = %v", err)
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	results := make([]*UserInfo, 2)
	errs := make([]error, 2)

	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			results[index], errs[index] = RefreshSession("st-1")
		}(i)
	}

	close(start)
	wg.Wait()

	for i, refreshErr := range errs {
		if refreshErr != nil {
			t.Fatalf("RefreshSession() #%d error = %v", i, refreshErr)
		}
		if results[i] == nil {
			t.Fatalf("RefreshSession() #%d returned nil user info", i)
		}
		if results[i].RefreshToken != "st-2" {
			t.Fatalf("RefreshSession() #%d refresh token = %q, want st-2", i, results[i].RefreshToken)
		}
	}

	if got := atomic.LoadInt32(&refreshCalls); got != 1 {
		t.Fatalf("expected exactly 1 refresh HTTP call, got %d (tokens=%v)", got, refreshTokens)
	}

	saved, err := LoadUserInfo()
	if err != nil {
		t.Fatalf("LoadUserInfo() error = %v", err)
	}
	if saved.RefreshToken != "st-2" {
		t.Fatalf("saved refresh token = %q, want st-2", saved.RefreshToken)
	}
}

func TestRefreshSession_PersistsRotatedTokenWhenProfileSyncFails(t *testing.T) {
	baseDir, err := os.MkdirTemp("", "aliang-refresh-profile-failure-*")
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

	var refreshCalls int32
	var profileCalls int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/refresh":
			var payload struct {
				RefreshToken string `json:"refresh_token"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode refresh payload failed: %v", err)
			}

			switch atomic.AddInt32(&refreshCalls, 1) {
			case 1:
				if payload.RefreshToken != "st-1" {
					t.Fatalf("first refresh token = %q, want st-1", payload.RefreshToken)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"data":{"access_token":"st-2","refresh_token":"st-2","expires_in":3600,"token_type":"Bearer"}}`))
			case 2:
				if payload.RefreshToken != "st-2" {
					t.Fatalf("second refresh token = %q, want st-2", payload.RefreshToken)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"data":{"access_token":"st-3","refresh_token":"st-3","expires_in":3600,"token_type":"Bearer"}}`))
			default:
				t.Fatalf("unexpected refresh call #%d with token %q", refreshCalls, payload.RefreshToken)
			}
			return

		case "/api/v1/user/profile":
			call := atomic.AddInt32(&profileCalls, 1)
			if call == 1 {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"message":"temporary failure"}`))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":{"id":1,"email":"user@example.com","username":"user","role":"member","balance":12.5,"concurrency":2,"status":"active","allowed_groups":[1,2],"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-04-09T00:00:00Z"}}`))
			return

		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	config.SetGlobalConfig(&config.Config{Core: &config.CoreConfig{APIServer: server.URL}})

	if err := SaveUserInfo(&UserInfo{
		AccessToken:  "st-1",
		RefreshToken: "st-1",
		TokenType:    "Bearer",
		Username:     "cached-user",
		Email:        "cached@example.com",
		UpdatedAt:    time.Now().Add(-30 * time.Minute),
		ExpiresIn:    3600,
	}); err != nil {
		t.Fatalf("SaveUserInfo() error = %v", err)
	}

	refreshed, err := RefreshSession("st-1")
	if err != nil {
		t.Fatalf("first RefreshSession() error = %v", err)
	}
	if refreshed.RefreshToken != "st-2" {
		t.Fatalf("first RefreshSession() refresh token = %q, want st-2", refreshed.RefreshToken)
	}
	if refreshed.Username != "cached-user" {
		t.Fatalf("first RefreshSession() username = %q, want cached-user fallback", refreshed.Username)
	}

	savedAfterFirstRefresh, err := LoadUserInfo()
	if err != nil {
		t.Fatalf("LoadUserInfo() after first refresh error = %v", err)
	}
	if savedAfterFirstRefresh.RefreshToken != "st-2" {
		t.Fatalf("saved refresh token after first refresh = %q, want st-2", savedAfterFirstRefresh.RefreshToken)
	}

	refreshedAgain, err := RefreshSession("")
	if err != nil {
		t.Fatalf("second RefreshSession() error = %v", err)
	}
	if refreshedAgain.RefreshToken != "st-3" {
		t.Fatalf("second RefreshSession() refresh token = %q, want st-3", refreshedAgain.RefreshToken)
	}
	if refreshedAgain.Username != "user" {
		t.Fatalf("second RefreshSession() username = %q, want user", refreshedAgain.Username)
	}
}

func TestRestoreSession_ProfileRejectsRefreshedTokenDoesNotResurrectSession(t *testing.T) {
	baseDir, err := os.MkdirTemp("", "aliang-refresh-profile-unauthorized-*")
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
			_, _ = w.Write([]byte(`{"data":{"access_token":"rejected-access","refresh_token":"rotated-refresh","expires_in":3600,"token_type":"Bearer"}}`))
		case "/api/v1/user/profile":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"code":"TOKEN_EXPIRED","message":"Token has expired"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	config.SetGlobalConfig(&config.Config{Core: &config.CoreConfig{APIServer: server.URL}})
	if err := SaveUserInfo(&UserInfo{
		AccessToken:  "stale-access",
		RefreshToken: "stale-refresh",
		TokenType:    "Bearer",
		Username:     "stale-user",
		UpdatedAt:    time.Now(),
	}); err != nil {
		t.Fatalf("SaveUserInfo() error = %v", err)
	}

	authority := ResetSessionAuthorityForTest()
	authority.NotifyLoggedIn(&UserInfo{Username: "stale-user"})

	_, err = RestoreSession()
	if !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("RestoreSession() error = %v, want ErrSessionExpired", err)
	}
	if authority.State() != StateHardInvalid {
		t.Fatalf("authority state = %v, want HardInvalid", authority.State())
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

func TestRestoreSessionTransientFailurePublishesRecoveringSnapshot(t *testing.T) {
	baseDir := t.TempDir()
	t.Setenv("HOME", filepath.Join(baseDir, "home"))
	t.Setenv("ALIANG_CACHE_DIR", filepath.Join(baseDir, "cache"))
	ResetAuthPersistenceForTest()
	StopTokenRefresh()
	config.ResetGlobalConfigForTest()
	t.Cleanup(func() {
		StopTokenRefresh()
		ResetAuthPersistenceForTest()
		config.ResetGlobalConfigForTest()
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "temporary upstream outage", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	config.SetGlobalConfig(&config.Config{Core: &config.CoreConfig{APIServer: server.URL}})

	want := &UserInfo{
		AccessToken:  "cached-access",
		RefreshToken: "cached-refresh",
		TokenType:    "Bearer",
		Username:     "cached-user",
		UpdatedAt:    time.Now(),
	}
	if err := SaveUserInfo(want); err != nil {
		t.Fatalf("SaveUserInfo() error=%v", err)
	}
	authority := ResetSessionAuthorityForTest()

	got, err := RestoreSession()
	if !errors.Is(err, ErrSessionRecovering) {
		t.Fatalf("RestoreSession() error=%v, want ErrSessionRecovering", err)
	}
	if got == nil || got.Username != "cached-user" {
		t.Fatalf("RestoreSession() user=%#v, want cached user", got)
	}
	snapshot := authority.Snapshot()
	if snapshot.State != StateSoftExpired || snapshot.Reason != ReasonRestoreUnavailable {
		t.Fatalf("authority snapshot=%+v, want SoftExpired/RestoreUnavailable", snapshot)
	}
	if snapshot.User == nil || snapshot.User.Username != "cached-user" || authority.CanProxy() {
		t.Fatalf("recovering authority user/gate mismatch: %+v", snapshot)
	}
	if persisted, loadErr := LoadUserInfo(); loadErr != nil || persisted.Username != "cached-user" {
		t.Fatalf("persisted session was lost: user=%#v err=%v", persisted, loadErr)
	}
}

func TestRefreshSession_RenewsAccessTokenAndRetainsRefreshTokenWhenOmitted(t *testing.T) {
	baseDir, err := os.MkdirTemp("", "aliang-refresh-missing-token-*")
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

	var profileCalls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/refresh":
			// Server returns a fresh access_token but omits a rotated refresh_token.
			// (Some refresh endpoints do not rotate refresh tokens on every call.)
			// The client MUST still renew the access_token and retain the existing
			// refresh_token as a fallback, otherwise the access_token is never
			// renewed and the session expires (auth_expired) on a fixed cadence.
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":{"access_token":"access-2","expires_in":3600,"token_type":"Bearer"}}`))
		case "/api/v1/user/profile":
			atomic.AddInt32(&profileCalls, 1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":{"id":1,"email":"user@example.com","username":"user","role":"member","balance":12.5,"concurrency":2,"status":"active","allowed_groups":[1,2],"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-04-09T00:00:00Z"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	config.SetGlobalConfig(&config.Config{Core: &config.CoreConfig{APIServer: server.URL}})

	if err := SaveUserInfo(&UserInfo{
		AccessToken:  "st-1",
		RefreshToken: "legacy-upstream-refresh",
		TokenType:    "Bearer",
		Username:     "cached-user",
		Email:        "cached@example.com",
		UpdatedAt:    time.Now().Add(-30 * time.Minute),
		ExpiresIn:    3600,
	}); err != nil {
		t.Fatalf("SaveUserInfo() error = %v", err)
	}

	refreshed, err := RefreshSession("legacy-upstream-refresh")
	if err != nil {
		t.Fatalf("RefreshSession() error = %v, want nil (access_token must be renewed even when no refresh_token is returned)", err)
	}
	if refreshed.AccessToken != "access-2" {
		t.Fatalf("RefreshSession() access token = %q, want access-2 (renewed)", refreshed.AccessToken)
	}
	if refreshed.RefreshToken != "access-2" {
		t.Fatalf("RefreshSession() refresh token = %q, want access-2 local fallback", refreshed.RefreshToken)
	}
	if got := atomic.LoadInt32(&profileCalls); got != 1 {
		t.Fatalf("profile calls = %d, want 1 (renewed access_token should be used to sync profile)", got)
	}

	saved, err := LoadUserInfo()
	if err != nil {
		t.Fatalf("LoadUserInfo() error = %v", err)
	}
	if saved.AccessToken != "access-2" {
		t.Fatalf("saved access token = %q, want access-2", saved.AccessToken)
	}
	if saved.RefreshToken != "access-2" {
		t.Fatalf("saved refresh token = %q, want access-2 local fallback", saved.RefreshToken)
	}
}

func TestActivateWithTokensRejectsMissingRefreshToken(t *testing.T) {
	userInfo, err := ActivateWithTokens("scan-access-token", " ")
	if err == nil {
		t.Fatal("ActivateWithTokens() error = nil, want refresh token error")
	}
	if err.Error() != "refresh token cannot be empty" {
		t.Fatalf("ActivateWithTokens() error = %v, want refresh token cannot be empty", err)
	}
	if userInfo != nil {
		t.Fatalf("ActivateWithTokens() user info = %#v, want nil", userInfo)
	}
}
