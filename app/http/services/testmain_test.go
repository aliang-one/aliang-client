package services

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"aliang.one/nursorgate/internal/testisolate"
	auth "aliang.one/nursorgate/processor/auth"
	"aliang.one/nursorgate/processor/config"
)

func TestMain(m *testing.M) {
	baseDir, err := os.MkdirTemp("", "aliang-services-tests-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	_ = os.Setenv("ALIANG_AUTH_SESSION_DB", filepath.Join(baseDir, "auth.data"))
	// Package tests must never control a real desktop Agent that happens to be
	// listening on the developer machine's default port.
	config.DefaultUserAgentAddr = "127.0.0.1:0"
	// Keep logger/cache/state writes out of the real ~/.aliang.
	cleanupState := testisolate.RedirectUserStateDir()
	// 关掉常驻 stale 扫描：用例直接调 scanStaleRuns 覆盖扫描逻辑，常驻 ticker
	// 只会泄漏 goroutine，并与用例对包级 var 的写构成 -race 竞争。
	agentAIStaleScannerEnabled = false
	// agentcore 记忆存储隔离(N9):全程把记忆 dirFn 指向本测试临时目录——
	// agentHome() 解析的是真实 $HOME,testisolate 重定向覆盖不到;开发者本机
	// ~/.aliang/memory 恰有同名项目记忆时,prompt 回归的「逐字节一致」断言
	// 会被 <memory> 块打破。(services 包已有 TestMain,故按计划附注改在其
	// 内补充,不另设第二个 TestMain。)
	memDir, err := os.MkdirTemp("", "agentcore-memory-test")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	prevMemDirFn := agentCoreMemory.dirFn
	agentCoreMemory.dirFn = func() string { return memDir }

	code := m.Run()
	auth.StopTokenRefresh()
	auth.ResetAuthPersistenceForTest()
	agentCoreMemory.dirFn = prevMemDirFn
	cleanupState()
	_ = os.RemoveAll(baseDir)
	_ = os.RemoveAll(memDir)
	os.Exit(code)
}
