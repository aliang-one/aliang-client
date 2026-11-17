package ipc

import (
	"os"
	"testing"

	"aliang.one/nursorgate/internal/testisolate"
)

func TestMain(m *testing.M) {
	// server.Start() 会 EnsureCoreDirs 触碰真实用户状态目录;按本仓惯例把
	// 状态目录重定向进临时目录,避免测试污染生产 ~/.aliang。
	cleanupState := testisolate.RedirectUserStateDir()

	code := m.Run()
	cleanupState()
	os.Exit(code)
}
