package services

import (
	"testing"

	auth "aliang.one/nursorgate/processor/auth"
)

// TestProxyStartBlockedReason verifies the login gate the tray consults before
// enabling its Start item: an active session permits a start; restoring and
// logged-out sessions report session_recovering / session_invalid with the
// user-facing message the tray surfaces as tooltip and notification.
func TestProxyStartBlockedReason(t *testing.T) {
	authority := auth.ResetSessionAuthorityForTest()
	t.Cleanup(func() { auth.ResetSessionAuthorityForTest() })

	if code, msg := ProxyStartBlockedReason(); code != "session_recovering" || msg == "" {
		t.Fatalf("restoring: got (%q, %q), want session_recovering with message", code, msg)
	}

	authority.NotifyLoggedIn(&auth.UserInfo{ID: 1, Username: "tray-start-gate"})
	if code, msg := ProxyStartBlockedReason(); code != "" || msg != "" {
		t.Fatalf("active: got (%q, %q), want empty (start permitted)", code, msg)
	}

	authority.NotifyLoggedOut()
	if code, msg := ProxyStartBlockedReason(); code != "session_invalid" || msg == "" {
		t.Fatalf("logged out: got (%q, %q), want session_invalid with message", code, msg)
	}
}
