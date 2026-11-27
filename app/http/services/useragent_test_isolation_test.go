package services

import (
	"testing"

	"aliang.one/nursorgate/processor/config"
)

func TestRunningUnderGoTestDetectsTestBinary(t *testing.T) {
	if !runningUnderGoTest() {
		t.Fatal("runningUnderGoTest() = false inside a test binary — 生产 agent 只差一次 POST 的距离")
	}
}

func TestResolveLocalUserAgentBaseURLIsolatesTestProcess(t *testing.T) {
	if got := resolveLocalUserAgentBaseURL(true); got != testIsolatedUserAgentBaseURL {
		t.Fatalf("resolveLocalUserAgentBaseURL(underTest=true) = %q, want %q", got, testIsolatedUserAgentBaseURL)
	}
	if testIsolatedUserAgentBaseURL == "http://"+config.DefaultUserAgentAddr {
		t.Fatal("隔离地址不得等于生产 user-agent 地址")
	}
}

func TestResolveLocalUserAgentBaseURLKeepsProductionAddress(t *testing.T) {
	if got, want := resolveLocalUserAgentBaseURL(false), UserAgentBaseURL(); got != want {
		t.Fatalf("resolveLocalUserAgentBaseURL(underTest=false) = %q, want %q", got, want)
	}
}

// TestDefaultLocalUserAgentBaseURLUnderTestIsIsolated 是 2026-10-27 事故的回归
// 哨兵：包内不改写 localUserAgentBaseURL 时，默认目标必须是隔离地址。
// 若有人把默认值改回 UserAgentBaseURL，本用例立刻失败。
func TestDefaultLocalUserAgentBaseURLUnderTestIsIsolated(t *testing.T) {
	if got := localUserAgentBaseURL(); got == UserAgentBaseURL() {
		t.Fatalf("默认 user-agent 目标 = %q：测试进程会打到真实 agent，应为隔离地址", got)
	}
}

// TestIsCoreProcessContract 钉死 core 守护形态标记契约(2026-11-27 客户案):
// 仅显式 "1" 视为 core,空/其他值/空白一律不是——ensureAgentAfterAuth 的
// Windows 守卫(原 Geteuid==0 恒失效)依赖此判据决定是否 spawn user agent。
func TestIsCoreProcessContract(t *testing.T) {
	cases := []struct{ name, val, want string }{
		{"unset", "", "false"},
		{"explicit_one", "1", "true"},
		{"padded_one", " 1 ", "true"},
		{"zero", "0", "false"},
		{"junk", "yes", "false"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(CoreProcessEnv, tc.val)
			if got := IsCoreProcess(); (got && tc.want != "true") || (!got && tc.want != "false") {
				t.Fatalf("IsCoreProcess() with %s=%q = %t, want %s", CoreProcessEnv, tc.val, got, tc.want)
			}
		})
	}
}
