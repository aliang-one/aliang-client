package user

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"aliang.one/nursorgate/processor/config"
)

func TestTokenRefresher_RefreshNowSkipsHealthyToken(t *testing.T) {
	baseDir, err := os.MkdirTemp("", "aliang-token-refresher-*")
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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/auth/refresh" {
			atomic.AddInt32(&refreshCalls, 1)
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	config.SetGlobalConfig(&config.Config{Core: &config.CoreConfig{APIServer: server.URL}})

	if err := SaveUserInfo(&UserInfo{
		AccessToken:  "access-healthy",
		RefreshToken: "refresh-healthy",
		TokenType:    "Bearer",
		Username:     "user",
		Email:        "user@example.com",
		UpdatedAt:    time.Now(),
		ExpiresIn:    3600,
	}); err != nil {
		t.Fatalf("SaveUserInfo() error = %v", err)
	}

	refresher := NewTokenRefresher()
	if err := refresher.RefreshNow(); err != nil {
		t.Fatalf("RefreshNow() error = %v", err)
	}

	if got := atomic.LoadInt32(&refreshCalls); got != 0 {
		t.Fatalf("refresh endpoint called %d times, want 0", got)
	}
}

func TestTokenRefresher_RefreshNowRefreshesWhenTokenExpiresWithinTenMinutes(t *testing.T) {
	baseDir, err := os.MkdirTemp("", "aliang-token-refresher-expiring-*")
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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/refresh":
			atomic.AddInt32(&refreshCalls, 1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":{"access_token":"access-fresh","refresh_token":"refresh-fresh","expires_in":3600,"token_type":"Bearer"}}`))
		case "/api/v1/user/profile":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":{"id":1,"email":"user@example.com","username":"user","role":"member","balance":12.5,"concurrency":2,"status":"active","allowed_groups":[1],"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-04-09T00:00:00Z"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	config.SetGlobalConfig(&config.Config{Core: &config.CoreConfig{APIServer: server.URL}})

	if err := SaveUserInfo(&UserInfo{
		AccessToken:  "access-soon",
		RefreshToken: "refresh-soon",
		TokenType:    "Bearer",
		Username:     "user",
		Email:        "user@example.com",
		ExpiresIn:    60 * 60,
	}); err != nil {
		t.Fatalf("SaveUserInfo() error = %v", err)
	}
	SetCurrentUserInfo(&UserInfo{
		AccessToken:  "access-soon",
		RefreshToken: "refresh-soon",
		TokenType:    "Bearer",
		Username:     "user",
		Email:        "user@example.com",
		UpdatedAt:    time.Now().Add(-51 * time.Minute),
		ExpiresIn:    60 * 60,
	})

	refresher := NewTokenRefresher()
	if err := refresher.RefreshNow(); err != nil {
		t.Fatalf("RefreshNow() error = %v", err)
	}

	if got := atomic.LoadInt32(&refreshCalls); got != 1 {
		t.Fatalf("refresh endpoint called %d times, want 1", got)
	}
}
