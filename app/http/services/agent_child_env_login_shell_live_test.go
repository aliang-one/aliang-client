//go:build !windows

package services

import (
	"runtime"
	"strings"
	"testing"
	"time"
)

// 真探针：必须能拿到含冒号的非空 PATH。linux 极简容器可能没有 zsh/bash，
// 探针失败降级为 Skip（fail-open 是产品语义）；darwin 必须成功。
func TestLoginShellPathSnapshotLive(t *testing.T) {
	path, ok := loginShellPathSnapshot(5 * time.Second)
	if !ok || path == "" || !strings.Contains(path, ":") {
		if runtime.GOOS == "darwin" {
			t.Fatalf("登录 shell PATH 探针失败: ok=%v path=%q", ok, path)
		}
		t.Skipf("无可用登录 shell，fail-open: ok=%v", ok)
	}
}
