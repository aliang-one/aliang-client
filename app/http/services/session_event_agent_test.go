package services

import "testing"

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
