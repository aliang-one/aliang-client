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
		name       string
		registered bool
		sync       string
		msg        string
		want       string
	}{
		{"registered", true, "online", "", registrationRegistered},
		{"hard invalid overrides stale registered state", true, "auth_expired", "", registrationLoginRequired},
		{"login required overrides stale registered state", true, "login_required", "", registrationLoginRequired},
		{"unregistered", false, "", "", registrationUnregistered},
		{"login_required", false, "login_required", "", registrationLoginRequired},
		{"auth_expired maps to login_required", false, "auth_expired", "", registrationLoginRequired},
		{"refresh_invalid maps to login_required", false, "refresh_invalid", "", registrationLoginRequired},
		{"soft_expiry_timeout maps to login_required", false, "soft_expiry_timeout", "", registrationLoginRequired},
		{"revoked maps to login_required", false, "revoked", "", registrationLoginRequired},
		{"device_id_conflict is rejected", false, "device_id_conflict", "", registrationRejected},
		{"device_unbound is rejected", false, "device_unbound", "", registrationRejected},
		{"enable_failed with 401 is rejected", false, "enable_failed", "returned 401: authentication_required", registrationRejected},
		{"enable_failed network error stays unregistered", false, "enable_failed", "dial tcp: i/o timeout", registrationUnregistered},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			svc := &AgentService{}
			svc.state.Registered = c.registered
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
		{"refresh_invalid is idle not error", false, "refresh_invalid", connectionDisconnected},
		{"soft_expiry_timeout is idle not error", false, "soft_expiry_timeout", connectionDisconnected},
		{"revoked is idle not error", false, "revoked", connectionDisconnected},
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
// non-empty, informative registration_message for a server-side unbind.
func TestRegistrationRejectedCarriesMessage(t *testing.T) {
	withAgentServerConfig(t)
	svc := &AgentService{}
	svc.state.LastSyncStatus = "device_unbound"
	svc.state.LastSyncMessage = "Agent mode was disabled because the device was unbound."
	state, msg := svc.deriveRegistrationStateLocked()
	if state != registrationRejected {
		t.Fatalf("state = %q, want %q", state, registrationRejected)
	}
	if msg == "" {
		t.Error("expected a non-empty registration_message for a rejected device")
	}
}
