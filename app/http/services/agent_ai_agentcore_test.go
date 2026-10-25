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

// 手机每次建会话都会显式传 model，那是权威来源：本机 env / settings 一律不得
// 覆盖它。反过来，手机传空或传 claude CLI 自己造的 "<synthetic>" 时必须继续
// 往下兜底——网关不认识这两个值，直接发出去只会得到 "model is required" 或
// model_not_found，而用户看到的是一句莫名其妙的失败。
func TestAgentCoreModelResolution(t *testing.T) {
	// 隔离 ~/.claude/settings.json：agentCoreEnvOrSettings 会去读它。
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ANTHROPIC_MODEL", "glm-5.3-from-env")
	t.Setenv("ANTHROPIC_DEFAULT_SONNET_MODEL", "glm-5.3-from-settings")

	// 1) 手机传的模型恒胜出，且去掉首尾空白。
	for _, tc := range []struct{ phone, want string }{
		{"glm-5.3", "glm-5.3"},
		{"  glm-5.3-flash[1m]  ", "glm-5.3-flash[1m]"},
		{"glm-5.1", "glm-5.1"},
	} {
		if got := agentCoreModel(agentAIRun{model: tc.phone}); got != tc.want {
			t.Errorf("手机传 %q → %q, want %q（手机是权威来源）", tc.phone, got, tc.want)
		}
	}

	// 2) 手机没传（或传了不可用的占位名）→ 退回本机配置，不能把占位名发出去。
	for _, phone := range []string{"", "   ", "<synthetic>", "<SYNTHETIC>"} {
		if got := agentCoreModel(agentAIRun{model: phone}); got != "glm-5.3-from-env" {
			t.Errorf("手机传 %q → %q, want 本机 ANTHROPIC_MODEL", phone, got)
		}
	}

	// 3) 本机也没有显式覆盖 → 兜底值本身必须是网关认得的 id。
	t.Setenv("ANTHROPIC_MODEL", "")
	t.Setenv("ANTHROPIC_DEFAULT_SONNET_MODEL", "")
	if got := agentCoreModel(agentAIRun{}); got != agentCoreDefaultModel {
		t.Errorf("全空 → %q, want %q", got, agentCoreDefaultModel)
	}
}
