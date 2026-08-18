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
