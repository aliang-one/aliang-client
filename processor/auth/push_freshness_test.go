package user

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// stubPushFreshness isolates each test: a known session (or nil), a refresh
// stub, a fixed clock, and a known owner flag — all restored on cleanup.
func stubPushFreshness(t *testing.T, info *UserInfo, owner bool, refresh func() (*UserInfo, error), now time.Time) {
	t.Helper()
	origOwner := IsSessionOwnerProcess()
	origRefresh := pushRefreshNow
	origNow := pushNow
	SetCurrentUserInfo(info)
	SetSessionOwnerProcess(owner)
	pushRefreshNow = refresh
	pushNow = func() time.Time { return now }
	t.Cleanup(func() {
		pushRefreshNow = origRefresh
		pushNow = origNow
		SetSessionOwnerProcess(origOwner)
		SetCurrentUserInfo(nil)
	})
}

func TestEnsureFreshAccessTokenForPushRefreshesNearExpiryToken(t *testing.T) {
	// ExpiresIn 15min, lead 10min, now = 8min after update → inside lead window.
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	updated := now.Add(-8 * time.Minute)
	info := &UserInfo{AccessToken: "stale-token", TokenType: "Bearer", ExpiresIn: 15 * 60, UpdatedAt: updated}
	refreshed := 0
	stubPushFreshness(t, info, true, func() (*UserInfo, error) {
		refreshed++
		fresh := &UserInfo{AccessToken: "fresh-token", TokenType: "Bearer", ExpiresIn: 15 * 60, UpdatedAt: now}
		SetCurrentUserInfo(fresh)
		return fresh, nil
	}, now)

	header, err := EnsureFreshAccessTokenForPush()
	if err != nil {
		t.Fatalf("EnsureFreshAccessTokenForPush() error = %v", err)
	}
	if refreshed != 1 {
		t.Fatalf("refresh calls = %d, want 1", refreshed)
	}
	if header != "Bearer fresh-token" {
		t.Fatalf("header = %q, want refreshed bearer header", header)
	}
}

func TestEnsureFreshAccessTokenForPushSkipsFreshToken(t *testing.T) {
	// ExpiresIn 60min, now = 1min after update → far from expiry.
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	info := &UserInfo{AccessToken: "tok", TokenType: "Bearer", ExpiresIn: 60 * 60, UpdatedAt: now.Add(-time.Minute)}
	refreshed := 0
	stubPushFreshness(t, info, true, func() (*UserInfo, error) {
		refreshed++
		return info, nil
	}, now)

	header, err := EnsureFreshAccessTokenForPush()
	if err != nil {
		t.Fatalf("EnsureFreshAccessTokenForPush() error = %v", err)
	}
	if refreshed != 0 {
		t.Fatalf("refresh calls = %d, want 0", refreshed)
	}
	if !strings.Contains(header, "tok") {
		t.Fatalf("header = %q, want original token passthrough", header)
	}
}

func TestEnsureFreshAccessTokenForPushUnknownExpiryDoesNotRefresh(t *testing.T) {
	// Missing ExpiresIn → unknown expiry: passthrough, no refresh (avoids a
	// refresh storm on sessions without an expiry anchor).
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	info := &UserInfo{AccessToken: "tok", TokenType: "Bearer"}
	refreshed := 0
	stubPushFreshness(t, info, true, func() (*UserInfo, error) {
		refreshed++
		return info, nil
	}, now)

	if _, err := EnsureFreshAccessTokenForPush(); err != nil {
		t.Fatalf("EnsureFreshAccessTokenForPush() error = %v", err)
	}
	if refreshed != 0 {
		t.Fatalf("refresh calls = %d, want 0", refreshed)
	}
}

func TestEnsureFreshAccessTokenForPushRefreshFailureAbortsPush(t *testing.T) {
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	info := &UserInfo{AccessToken: "stale-token", TokenType: "Bearer", ExpiresIn: 15 * 60, UpdatedAt: now.Add(-8 * time.Minute)}
	stubPushFreshness(t, info, true, func() (*UserInfo, error) {
		return nil, errors.New("dial tcp: connection refused")
	}, now)

	header, err := EnsureFreshAccessTokenForPush()
	if err == nil {
		t.Fatalf("EnsureFreshAccessTokenForPush() error = nil, want pre-push refresh failure")
	}
	if header != "" {
		t.Fatalf("header = %q, want empty so the caller aborts the push", header)
	}
}

func TestEnsureFreshAccessTokenForPushNonOwnerAndEmptyHeaderPassThrough(t *testing.T) {
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	info := &UserInfo{AccessToken: "tok", TokenType: "Bearer", ExpiresIn: 15 * 60, UpdatedAt: now.Add(-8 * time.Minute)}
	refreshed := 0
	refresh := func() (*UserInfo, error) {
		refreshed++
		return info, nil
	}

	// Non-owner: never refreshes, header passes through.
	stubPushFreshness(t, info, false, refresh, now)
	header, err := EnsureFreshAccessTokenForPush()
	if err != nil || refreshed != 0 {
		t.Fatalf("non-owner: err=%v refreshCalls=%d, want nil/0", err, refreshed)
	}
	if !strings.Contains(header, "tok") {
		t.Fatalf("non-owner header = %q", header)
	}

	// Empty header: nothing to refresh, passthrough (caller reports unavailable).
	stubPushFreshness(t, nil, true, refresh, now)
	header, err = EnsureFreshAccessTokenForPush()
	if err != nil || header != "" {
		t.Fatalf("empty-header: err=%v header=%q, want nil/empty", err, header)
	}
	if refreshed != 0 {
		t.Fatalf("empty-header refresh calls = %d, want 0", refreshed)
	}
}
