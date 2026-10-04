//go:build darwin

package cmd

import "testing"

// .app 双击启动此前完全绕过单实例锁(aliang tray / aliang start 都有 guard):
// 双开 .app 会得到两个 companion、两套 watchdog。判定逻辑提取成纯函数后,
// guard 接线照抄 runTray 的既有模式(单实例语义由 internal/singleinstance 的
// 重复端口测试覆盖)。
func TestIsAppBundleExecPath(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"/Applications/Aliang.app/Contents/MacOS/aliang", true},
		{"/Users/mac/Apps/Aliang.app/Contents/MacOS/aliang", true},
		{"./aliang", false},
		{"/usr/local/bin/aliang", false},
		{"/Library/Application Support/one.aliang.aliang/aliang core", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := isAppBundleExecPath(tc.path); got != tc.want {
			t.Fatalf("isAppBundleExecPath(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}
