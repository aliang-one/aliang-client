package tray

import "testing"

// TestTrayStartBlockedStatusTitle verifies the Status menu line shown while the
// login gate disables the Start item — the visible hint on macOS, where tray
// menu items ignore per-item tooltips.
func TestTrayStartBlockedStatusTitle(t *testing.T) {
	testCases := []struct {
		code string
		want string
	}{
		{code: "session_invalid", want: "Status: 未登录，请先打开 Dashboard 登录"},
		{code: "session_recovering", want: "Status: 登录恢复中，暂不能启动代理"},
		{code: "", want: ""},
	}

	for _, tc := range testCases {
		if got := trayStartBlockedStatusTitle(tc.code); got != tc.want {
			t.Fatalf("trayStartBlockedStatusTitle(%q) = %q, want %q", tc.code, got, tc.want)
		}
	}
}

// TestStartMenuItemGate covers the companion tray's three-state Start decision:
// running disables the item; a login-gate blockMsg disables it with the reason
// as tooltip; otherwise it is enabled with the default tooltip.
func TestStartMenuItemGate(t *testing.T) {
	testCases := []struct {
		name         string
		running      bool
		blockMsg     string
		wantDisabled bool
		wantTooltip  string
	}{
		{name: "running keeps item disabled", running: true, blockMsg: "", wantDisabled: true, wantTooltip: ""},
		{name: "login gate disables with reason", running: false, blockMsg: "login required", wantDisabled: true, wantTooltip: "login required"},
		{name: "permitted enables with default tooltip", running: false, blockMsg: "", wantDisabled: false, wantTooltip: startProxyMenuTooltip},
		{name: "whitespace-only blockMsg counts as permitted", running: false, blockMsg: "   ", wantDisabled: false, wantTooltip: startProxyMenuTooltip},
	}

	for _, tc := range testCases {
		gotDisabled, gotTooltip := startMenuItemGate(tc.running, tc.blockMsg)
		if gotDisabled != tc.wantDisabled || gotTooltip != tc.wantTooltip {
			t.Fatalf("%s: startMenuItemGate(%v, %q) = (%v, %q), want (%v, %q)",
				tc.name, tc.running, tc.blockMsg, gotDisabled, gotTooltip, tc.wantDisabled, tc.wantTooltip)
		}
	}
}
