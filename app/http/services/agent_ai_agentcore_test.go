package services

import "testing"

// 内置内核的路由判定。
//
// 这里只钉语义边界，不钉实现：显式 "agentcore" 恒走内核；"auto" 只在
// 「一个外部 CLI 都没有」时兜底；goal / 只读 run 一律不兜底（内核首期不注入
// ALIANG_GOAL_REPORT 强制段，兜底会让 goal 永远拿不到报告行）。

func overrideExternalCLIProbe(t *testing.T, available bool) {
	t.Helper()
	orig := agentAIExternalCLIProbe
	agentAIExternalCLIProbe = func() bool { return available }
	t.Cleanup(func() { agentAIExternalCLIProbe = orig })
}

func TestAgentAIUseAgentCoreMatchesOnlyExplicitProvider(t *testing.T) {
	for _, p := range []string{"agentcore", "AgentCore", "  AGENTCORE  "} {
		if !agentAIUseAgentCore(p) {
			t.Errorf("%q 应判定走内核", p)
		}
	}
	// auto 必须**不**命中：内置内核不得顶掉用户装了的 claude/codex。
	for _, p := range []string{"auto", "claude", "claudecode", "codex", "opencode", "", "agent-core"} {
		if agentAIUseAgentCore(p) {
			t.Errorf("%q 不应判定走内核", p)
		}
	}
}

func TestAgentAIUseAgentCoreFallback(t *testing.T) {
	// 无外部 CLI + auto → 兜底。
	overrideExternalCLIProbe(t, false)
	if !agentAIUseAgentCoreFallback(agentAIRun{provider: "auto"}) {
		t.Error("无外部 CLI 时 auto 应兜底到内核")
	}
	// 有外部 CLI → 不兜底（用户装了什么就用什么）。
	overrideExternalCLIProbe(t, true)
	if agentAIUseAgentCoreFallback(agentAIRun{provider: "auto"}) {
		t.Error("有外部 CLI 时不应兜底")
	}

	// 显式 provider 永不兜底——即使一个 CLI 都没有，也要按用户的选择报错，
	// 而不是静默换成内核。
	overrideExternalCLIProbe(t, false)
	for _, p := range []string{"claude", "claudecode", "codex", "opencode", "agentcore", ""} {
		if agentAIUseAgentCoreFallback(agentAIRun{provider: p}) {
			t.Errorf("显式 provider %q 不应兜底", p)
		}
	}

	// goal / 只读 run 不兜底：内核首期不注入 goal 报告强制段。
	overrideExternalCLIProbe(t, false)
	if agentAIUseAgentCoreFallback(agentAIRun{provider: "auto", readOnly: true}) {
		t.Error("只读 run 不应兜底")
	}
	if agentAIUseAgentCoreFallback(agentAIRun{provider: "auto", goalIdentity: map[string]interface{}{"goal_id": "g1"}}) {
		t.Error("goal run 不应兜底")
	}
}
