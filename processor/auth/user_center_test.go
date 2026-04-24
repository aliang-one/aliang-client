package user

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"aliang.one/nursorgate/processor/config"
)

func TestGetUserProfile_ClearsSessionWhenProfileAndAuthMeConfirmRevokedToken(t *testing.T) {
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
		case "/api/v1/auth/me":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"code":"TOKEN_REVOKED","message":"Token has been revoked (password changed)"}`))
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
