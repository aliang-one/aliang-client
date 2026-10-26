package services

import (
	"strings"
	"testing"
	"time"

	"aliang.one/nursorgate/processor/config"
)

// agentChildProcessEnv 必须合并登录 shell PATH：这是 claude/codex/goal/
// agentcore 所有 AI 子进程的共同咽喉。直接喂缓存伪造值，保证确定性。
func TestAgentChildProcessEnvMergesLoginShellPath(t *testing.T) {
	testCacheLoginShellPath(t, "/fake/login/bin:/usr/bin")

	env := agentChildProcessEnv()
	paths := envPaths(env)
	if len(paths) != 1 {
		t.Fatalf("PATH 应恰一条: %v", paths)
	}
	if paths[0] != "/fake/login/bin:/usr/bin" {
		t.Fatalf("PATH 应为登录快照值, got %q", paths[0])
	}
}

// 客户自定义 env 覆盖必须仍然压过登录快照（显式配置 > 自动探测）。
func TestAgentChildProcessEnvCustomOverrideWins(t *testing.T) {
	t.Setenv("PATH", "/usr/bin:/bin")
	testCacheLoginShellPath(t, "/fake/login/bin")
	setTestGlobalConfigCustomEnv(t, map[string]string{"PATH": "/custom/bin"})

	env := agentChildProcessEnv()
	if got := envPaths(env); len(got) != 1 || got[0] != "/custom/bin" {
		t.Fatalf("自定义覆盖应获胜, got %v", got)
	}
}

func envPaths(env []string) []string {
	var out []string
	for _, kv := range env {
		if strings.HasPrefix(kv, "PATH=") {
			out = append(out, kv[5:])
		}
	}
	return out
}

// testCacheLoginShellPath 临时改写登录 PATH 缓存，用完还原。必须持锁写。
func testCacheLoginShellPath(t *testing.T, path string) {
	t.Helper()
	loginShellPathMu.Lock()
	oldValue, oldOK, oldAt := loginShellPathValue, loginShellPathOK, loginShellPathFetched
	t.Cleanup(func() {
		loginShellPathMu.Lock()
		loginShellPathValue, loginShellPathOK, loginShellPathFetched = oldValue, oldOK, oldAt
		loginShellPathMu.Unlock()
	})
	loginShellPathValue, loginShellPathOK = path, path != ""
	loginShellPathFetched = time.Now()
	loginShellPathMu.Unlock()
}

// setTestGlobalConfigCustomEnv 临时替换带 CustomEnvVars 的全局配置，还原只走
// 内部的 t.Cleanup。必须处理 nil：services 测试按文件名字母序执行，本文件
// 排在多数 SetGlobalConfig 测试之前，运行时全局配置很可能还是 nil，`*cfg`
// 解引用会 panic。
func setTestGlobalConfigCustomEnv(t *testing.T, vars map[string]string) {
	t.Helper()
	old := config.GetGlobalConfig()
	var next *config.Config
	if old == nil {
		next = &config.Config{}
	} else {
		clone := *old
		next = &clone
	}
	// EffectiveCustomEnvVars 读 Customer.CustomEnvVars，类型是
	// *CustomEnvVarsConfig{Enabled *bool; Vars map[string]string}，其中
	// Enabled 必须显式 true（opt-in）。克隆 Customer 后只替换该指针，
	// 原配置对象不被触碰。
	custClone := config.CustomerConfig{}
	if next.Customer != nil {
		custClone = *next.Customer
	}
	enabled := true
	custClone.CustomEnvVars = &config.CustomEnvVarsConfig{Enabled: &enabled, Vars: vars}
	next.Customer = &custClone
	t.Cleanup(func() { config.SetGlobalConfig(old) }) // old 可为 nil，agentChildProcessEnv 已容忍
	config.SetGlobalConfig(next)
}
