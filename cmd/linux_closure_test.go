//go:build linux

package cmd

import (
	"os/exec"
	"strings"
	"testing"
)

// 面板窗口壳与其 WebView 依赖只允许进入 windows/darwin 构建。本测试是
// Linux 服务器二进制的依赖闭包回归门（托盘调研 §6.3 不变量；里程碑 2
// 落地 internal/panelwin 后此门自动生效）。
func TestLinuxDepsExcludeGUI(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "./cmd/aliang").CombinedOutput()
	if err != nil {
		t.Skipf("go list failed (environment): %v", err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, "wailsapp") || strings.Contains(line, "go-webview2") || strings.Contains(line, "panelwin") {
			t.Fatalf("Linux 依赖闭包出现 GUI 依赖: %s", line)
		}
	}
}
