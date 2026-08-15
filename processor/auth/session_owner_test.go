package user

import (
	"errors"
	"testing"
	"time"
)

func TestNonOwnerCannotRefreshOrDeletePersistedSession(t *testing.T) {
	StopTokenRefresh()
	SetSessionOwnerProcess(true)
	if err := SaveUserInfo(&UserInfo{
		AccessToken:  "owner-access",
		RefreshToken: "owner-refresh",
		ExpiresIn:    3600,
		UpdatedAt:    time.Now(),
	}); err != nil {
		t.Fatalf("SaveUserInfo() error = %v", err)
	}
	t.Cleanup(func() {
		SetSessionOwnerProcess(true)
		_ = DeleteUserInfo()
	})

	SetSessionOwnerProcess(false)
	if _, err := RefreshSession("owner-refresh"); !errors.Is(err, ErrSessionOwnerRequired) {
		t.Fatalf("RefreshSession() error = %v, want ErrSessionOwnerRequired", err)
	}

	startTokenRefresh()
	if refresher := GetTokenRefresher(); refresher != nil && refresher.IsRunning() {
		t.Fatal("non-owner process started a token refresher")
	}

	clearLocalSessionAfterExpiration("invalid refresh token")
	loaded, err := LoadUserInfo()
	if err != nil {
		t.Fatalf("LoadUserInfo() after non-owner cleanup error = %v", err)
	}
	if loaded.RefreshToken != "owner-refresh" {
		t.Fatalf("persisted refresh token = %q, want owner-refresh", loaded.RefreshToken)
	}
}
