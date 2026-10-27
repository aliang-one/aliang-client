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
