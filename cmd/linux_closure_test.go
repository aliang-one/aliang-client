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
	// 模块路径形式(CWD 无关):go test 二进制的 CWD 是包目录 cmd/,
	// 相对路径 ./cmd/aliang 会解析成 cmd/cmd/aliang 而恒失败被 Skip。
	out, err := exec.Command("go", "list", "-deps", "aliang.one/nursorgate/cmd/aliang").CombinedOutput()
	if err != nil {
		t.Skipf("go list failed (environment): %v", err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, "wailsapp") || strings.Contains(line, "go-webview2") || strings.Contains(line, "panelwin") {
			t.Fatalf("Linux 依赖闭包出现 GUI 依赖: %s", line)
		}
	}
}
