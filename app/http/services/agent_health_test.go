package services

import (
	"testing"

	"aliang.one/nursorgate/processor/config"
)

// withAgentServerConfig installs a non-empty agent server URL for the duration
// of the test so deriveRegistrationStateLocked can progress past the
// not_configured branch.
func withAgentServerConfig(t *testing.T) {
	t.Helper()
	config.SetGlobalConfig(&config.Config{Core: &config.CoreConfig{AgentServer: "https://agent.example.com"}})
	t.Cleanup(config.ResetGlobalConfigForTest)
}

func TestDeriveRegistrationState(t *testing.T) {
	withAgentServerConfig(t)

	cases := []struct {
		name  string
		token string
		sync  string
		msg   string
		want  string
	}{
		// A cached device_token is the live authority for "registered": the
		// server issued it and clears it on rejection, so while it is present the
		// device is in good standing. The user-session status (auth_expired /
		// login_required) is a connection concern and must NOT downgrade a
		// registered device. This is the regression guard for the "logged in but
		// agent offline" inconsistency.
		{"registered with token", "dev-token", "online", "", registrationRegistered},
		{"token present survives auth_expired", "dev-token", "auth_expired", "", registrationRegistered},
		{"token present survives login_required", "dev-token", "login_required", "", registrationRegistered},
		{"token present overrides stale rejection status", "dev-token", "device_token_invalid", "", registrationRegistered},

		// No device_token: the reason comes from the last sync outcome. Note the
		// rejection cases carry NO token — a real server rejection clears it, so
		// "rejected with a token still present" is contradictory state that the
		// derive logic resolves in favor of the live token (registered) above.
		{"unregistered no token", "", "", "", registrationUnregistered},
		{"login_required", "", "login_required", "", registrationLoginRequired},
		{"auth_expired maps to login_required", "", "auth_expired", "", registrationLoginRequired},
		{"device_token_invalid is rejected", "", "device_token_invalid", "agent server returned 401: authentication_required", registrationRejected},
		{"device_id_conflict is rejected", "", "device_id_conflict", "", registrationRejected},
		{"device_unbound is rejected", "", "device_unbound", "", registrationRejected},
		{"enable_failed with 401 is rejected", "", "enable_failed", "returned 401: authentication_required", registrationRejected},
		{"enable_failed network error stays unregistered", "", "enable_failed", "dial tcp: i/o timeout", registrationUnregistered},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			svc := &AgentService{}
			svc.state.DeviceToken = c.token
			svc.state.LastSyncStatus = c.sync
			svc.state.LastSyncMessage = c.msg
			got, _ := svc.deriveRegistrationStateLocked()
			if got != c.want {
				t.Errorf("deriveRegistrationStateLocked() = %q, want %q", got, c.want)
			}
		})
	}
}

func TestDeriveRegistrationState_NotConfigured(t *testing.T) {
	config.ResetGlobalConfigForTest() // currentAgentServerURL() → ""
	svc := &AgentService{}
	svc.state.DeviceToken = "whatever"
	got, _ := svc.deriveRegistrationStateLocked()
	if got != registrationNotConfigured {
		t.Errorf("without agent server config = %q, want %q", got, registrationNotConfigured)
	}
}

func TestDeriveConnectionState(t *testing.T) {
	cases := []struct {
		name   string
		remote bool
		sync   string
		want   string
	}{
		{"connected", true, "online", connectionConnected},
		{"connecting", false, "connecting", connectionConnecting},
		// A session expiry reads as "idle, waiting for sign-in", NOT an error:
		// the link itself is healthy, it just has no valid user_token until the
		// session is restored. This keeps the UI from crying "connection error"
		// during an ordinary login blip.
		{"auth_expired is idle not error", false, "auth_expired", connectionDisconnected},
		{"login_required is idle not error", false, "login_required", connectionDisconnected},
		{"connect_failed is error", false, "connect_failed", connectionError},
		{"disconnected is error", false, "disconnected", connectionError},
		{"server_unavailable is error", false, "server_unavailable", connectionError},
		{"offline is idle", false, "offline", connectionDisconnected},
		{"empty is idle", false, "", connectionDisconnected},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			svc := &AgentService{}
			svc.state.RemoteConnected = c.remote
			svc.state.LastSyncStatus = c.sync
			got, _ := svc.deriveConnectionStateLocked()
			if got != c.want {
				t.Errorf("deriveConnectionStateLocked() = %q, want %q", got, c.want)
			}
		})
	}
}

// TestRegistrationRejectedCarriesMessage ensures the rejected state surfaces a
// non-empty, informative registration_message (the persisted 401 body). The token
// is empty here because a real server rejection clears it — that is what lets
// the derive logic take the rejected branch.
func TestRegistrationRejectedCarriesMessage(t *testing.T) {
	withAgentServerConfig(t)
	svc := &AgentService{}
	svc.state.DeviceToken = ""
	svc.state.LastSyncStatus = "device_token_invalid"
	svc.state.LastSyncMessage = "Agent mode was disabled because the device token was rejected. Server response: {\"error\":\"authentication_required\"}"
	state, msg := svc.deriveRegistrationStateLocked()
	if state != registrationRejected {
		t.Fatalf("state = %q, want %q", state, registrationRejected)
	}
	if msg == "" {
		t.Error("expected a non-empty registration_message for a rejected device")
	}
}
