package cmd

import "testing"

// 与三个 core_autostart_* 构建标签文件保持同步的策略契约测试(darwin CI 可真跑)。
func TestAutoStartDashboardPolicy(t *testing.T) {
	cases := map[string]bool{
		"linux":   true,
		"windows": true,
		"darwin":  false,
		"js":      false,
	}
	for goos, want := range cases {
		if got := autoStartDashboard(goos); got != want {
			t.Errorf("autoStartDashboard(%q) = %v, want %v", goos, got, want)
		}
	}
}
