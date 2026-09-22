package cmd

import (
	"os"
	"strings"
	"testing"

	"aliang.one/nursorgate/app/http/services"
)

// TestEnsureUserAgentEnvironmentInjectsDefaultOwnerAddr 锁定 Linux headless
// 兜底：手动运行 `aliang agent`（无 manager spawn 注入环境）时，
// ALIANG_SESSION_OWNER_ADDR 未设置则注入默认 owner 地址——否则凭据被拒
// 通知链（services.NotifyOwnerAuthRejected）因无地址而整体静默缺失，
// agent 只能自禁等待 owner 自查。
func TestEnsureUserAgentEnvironmentInjectsDefaultOwnerAddr(t *testing.T) {
	t.Setenv("ALIANG_DATA_DIR", "")
	t.Setenv("ALIANG_CACHE_DIR", "")
	t.Setenv("ALIANG_LOG_DIR", "")
	t.Setenv("ALIANG_SOCKET_PATH", "")
	t.Setenv(services.AgentRuntimeEnv, "")
	t.Setenv(services.SessionOwnerAddrEnv, "")

	ensureUserAgentEnvironment()

	if got := strings.TrimSpace(os.Getenv(services.AgentRuntimeEnv)); got != "1" {
		t.Fatalf("%s = %q, want %q", services.AgentRuntimeEnv, got, "1")
	}
	want := services.DefaultSessionOwnerAddr()
	if got := strings.TrimSpace(os.Getenv(services.SessionOwnerAddrEnv)); got != want {
		t.Fatalf("%s = %q, want injected default %q", services.SessionOwnerAddrEnv, got, want)
	}
}

// TestEnsureUserAgentEnvironmentPreservesExplicitOwnerAddr：显式设置的
// owner 地址（manager spawn 注入或运维手工指定）不被兜底覆盖。
func TestEnsureUserAgentEnvironmentPreservesExplicitOwnerAddr(t *testing.T) {
	t.Setenv("ALIANG_DATA_DIR", "")
	t.Setenv("ALIANG_CACHE_DIR", "")
	t.Setenv("ALIANG_LOG_DIR", "")
	t.Setenv("ALIANG_SOCKET_PATH", "")
	t.Setenv(services.AgentRuntimeEnv, "")
	t.Setenv(services.SessionOwnerAddrEnv, "http://127.0.0.1:60000")

	ensureUserAgentEnvironment()

	if got := strings.TrimSpace(os.Getenv(services.SessionOwnerAddrEnv)); got != "http://127.0.0.1:60000" {
		t.Fatalf("%s = %q, want explicit value preserved", services.SessionOwnerAddrEnv, got)
	}
}
