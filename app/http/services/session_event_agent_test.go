package services

import (
	"testing"

	"aliang.one/nursorgate/common/cache"
)

func TestSessionEventAgentAction(t *testing.T) {
	cases := []struct{ to, reason, want string }{
		{"active", "refreshed", "reconnect"},
		{"active", "login", "reconnect"},
		{"hard_invalid", "logout", "disable"},
		{"hard_invalid", "refresh_invalid", "none"},
		{"hard_invalid", "soft_expiry_timeout", "none"},
		{"hard_invalid", "revoked", "none"},
		{"soft_expired", "access_rejected", "none"},
		{"unauthenticated", "", "none"},
		{"ACTIVE", "X", "reconnect"}, // case-insensitive
		{"hard_invalid", "LOGOUT", "disable"},
		{"  hard_invalid  ", " logout ", "disable"}, // whitespace-tolerant
	}
	for _, c := range cases {
		if got := sessionEventAgentAction(c.to, c.reason); got != c.want {
			t.Errorf("sessionEventAgentAction(%q,%q)=%q want %q", c.to, c.reason, got, c.want)
		}
	}
}

func TestApplyLogoutSessionEventClearsForwardedIdentityAndAgentState(t *testing.T) {
	t.Setenv(AgentRuntimeEnv, "1")
	t.Setenv("ALIANG_DATA_DIR", t.TempDir())
	t.Setenv("ALIANG_CACHE_DIR", t.TempDir())
	cache.ResetCacheDirForTest()
	t.Cleanup(cache.ResetCacheDirForTest)

	service := NewAgentService()
	service.mu.Lock()
	service.forwardedUserAuthorization = "Bearer stale-access"
	service.forwardedUserKey = "id:42"
	service.state.Enabled = true
	service.state.Registered = true
	service.state.DeviceToken = "device-token"
	service.mu.Unlock()

	service.ApplySessionEvent("hard_invalid", "logout")

	service.mu.Lock()
	defer service.mu.Unlock()
	if service.forwardedUserAuthorization != "" || service.forwardedUserKey != "" {
		t.Fatal("logout session event retained forwarded user credentials")
	}
	if service.state.Enabled || service.state.Registered || service.state.DeviceToken != "" {
		t.Fatalf("logout session event retained agent state: %+v", service.state)
	}
	if service.state.LastSyncStatus != "logout" {
		t.Fatalf("logout status = %q, want logout", service.state.LastSyncStatus)
	}
}
