package services

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// 平台通道(env 注入的 ANTHROPIC_BASE_URL/AUTH_TOKEN)与用户 settings.json 的
// 优先级语义(2026-10-13 产品决策 A「仅兜底式」):
//   - 平台 env 齐全 → 注入 CLAUDE_CODE_PROVIDER_MANAGED_BY_HOST=1,钉死平台通道
//     (settings env 块会覆盖进程 env,不钉死则 sanitized/full 档被个人通道反杀);
//   - 平台 env 缺失 + isolated 档 → claude 无任何 provider 会 "Not logged in"
//     秒败,自动升为 sanitized 让用户 settings.json 提供上游,并以
//     policy_notice(effective=sanitized, reason=platform_provider_missing)告知。

func overridePlatformProbe(t *testing.T, present bool) {
	t.Helper()
	orig := platformProviderEnvProbe
	platformProviderEnvProbe = func() (string, string, bool) {
		if present {
			return "http://127.0.0.1:56432", "platform-token", true
		}
		return "", "", false
	}
	t.Cleanup(func() { platformProviderEnvProbe = orig })
}

func policyRun(tier string) agentAIRun {
	return agentAIRun{
		sessionID: "sess-1", runSeq: 1, approvalToken: "tok",
		claudePolicy: agentAIClaudeRemotePolicy{
			enabled:   true,
			trustTier: tier,
			permissionAsk: []string{"Bash", "Edit", "Write", "NotebookEdit", "mcp__*"},
		},
	}
}

func TestPlatformProviderProbeReadsInjectedEnv(t *testing.T) {
	orig := platformProviderEnvProbe
	t.Cleanup(func() { platformProviderEnvProbe = orig })
	t.Setenv("ANTHROPIC_BASE_URL", "http://127.0.0.1:56432")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "tok")
	if _, _, present := platformProviderEnvProbe(); !present {
		t.Error("env 齐全应判定平台通道可用")
	}
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	if _, _, present := platformProviderEnvProbe(); present {
		t.Error("token 为空应判定平台通道不可用")
	}
}

func TestPromoteIsolatedTierForPlatformFallback(t *testing.T) {
	overridePlatformProbe(t, false)
	promoted := promoteIsolatedTierForPlatformFallback(policyRun("isolated").claudePolicy, "s1")
	if promoted.trustTier != "sanitized" {
		t.Errorf("平台缺失 isolated 应升为 sanitized, got %q", promoted.trustTier)
	}
	if promoted.policyNotice == nil || promoted.policyNotice["effective"] != "sanitized" ||
		promoted.policyNotice["requested"] != "isolated" || promoted.policyNotice["reason"] != "platform_provider_missing" {
		t.Errorf("应携带升级 notice: %v", promoted.policyNotice)
	}

	// 非 isolated 档不参与（sanitized/full 本就加载 user settings）
	untouched := promoteIsolatedTierForPlatformFallback(policyRun("sanitized").claudePolicy, "s1")
	if untouched.trustTier != "sanitized" || untouched.policyNotice != nil {
		t.Errorf("sanitized 档不应被改动: %+v", untouched)
	}
	// 策略未启用（机制关闭）不参与
	disabled := policyRun("isolated")
	disabled.claudePolicy.enabled = false
	if got := promoteIsolatedTierForPlatformFallback(disabled.claudePolicy, "s1"); got.trustTier != "isolated" || got.policyNotice != nil {
		t.Errorf("策略未启用不应提升: %+v", got)
	}

	// 平台在位不提升
	overridePlatformProbe(t, true)
	kept := promoteIsolatedTierForPlatformFallback(policyRun("isolated").claudePolicy, "s1")
	if kept.trustTier != "isolated" || kept.policyNotice != nil {
		t.Errorf("平台在位不应提升: %+v", kept)
	}
}

func TestWithClaudeRemotePolicyPlatformMissingNoManagedByHost(t *testing.T) {
	overridePlatformProbe(t, false)
	tool := newClaudeCodeAIToolForOS("darwin", "claude", "/bin/claude", "hi", "", "", "")
	got, _, notice := withClaudeRemotePolicy(tool, policyRun("isolated"))
	if got == nil {
		t.Fatal("tool 不应为 nil")
	}
	for _, kv := range got.env {
		if strings.HasPrefix(kv, "CLAUDE_CODE_PROVIDER_MANAGED_BY_HOST=") {
			t.Errorf("平台缺失时不得注入 MANAGED_BY_HOST(否则 settings 兜底失效): %v", got.env)
		}
	}
	if notice != nil {
		t.Errorf("回退已上移至 promoteIsolatedTierForPlatformFallback, 此处不应有 notice: %v", notice)
	}
}

// 提升必须发生在版本守卫之前：claude<2.2.0 的 fail-closed 降级要压过提升
// （否则 2.1.x 上用户 ask 规则压死审批桥），≥2.2.0 则提升存活。
func TestPlatformPromotionRespectsVersionGuard(t *testing.T) {
	overridePlatformProbe(t, false)
	dir := t.TempDir()
	writeFake := func(name, version string) *agentAITool {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("#!/bin/sh\necho \""+version+" (Claude Code)\"\n"), 0o700); err != nil {
			t.Fatal(err)
		}
		return &agentAITool{path: path}
	}

	old := promoteIsolatedTierForPlatformFallback(policyRun("isolated").claudePolicy, "s1")
	if old.trustTier != "sanitized" {
		t.Fatalf("前置: 平台缺失应先提升, got %+v", old)
	}
	guarded := applyClaudeTierVersionGuard(writeFake("claude-old", "2.1.17"), old, "s1")
	if guarded.trustTier != "isolated" || guarded.policyNotice == nil || guarded.policyNotice["reason"] != "claude_version_below_2_2" {
		t.Errorf("2.1.x 上版本守卫必须压回 isolated: %+v", guarded)
	}

	fresh := promoteIsolatedTierForPlatformFallback(policyRun("isolated").claudePolicy, "s2")
	kept := applyClaudeTierVersionGuard(writeFake("claude-new", "2.2.5"), fresh, "s2")
	if kept.trustTier != "sanitized" || kept.policyNotice == nil || kept.policyNotice["reason"] != "platform_provider_missing" {
		t.Errorf("2.2+ 上提升应存活且保留升级 notice: %+v", kept)
	}
}

func TestWithClaudeRemotePolicyPlatformPresentPinsManagedByHost(t *testing.T) {
	overridePlatformProbe(t, true)
	for _, tier := range []string{"isolated", "sanitized", "full"} {
		tool := newClaudeCodeAIToolForOS("darwin", "claude", "/bin/claude", "hi", "", "", "")
		got, _, notice := withClaudeRemotePolicy(tool, policyRun(tier))
		found := false
		for _, kv := range got.env {
			if kv == "CLAUDE_CODE_PROVIDER_MANAGED_BY_HOST=1" {
				found = true
			}
		}
		if !found {
			t.Errorf("tier=%s 平台通道在位应注入 MANAGED_BY_HOST=1: %v", tier, got.env)
		}
		if notice != nil {
			t.Errorf("tier=%s 平台在位不应产生 notice: %v", tier, notice)
		}
		if tier == "sanitized" && !containsArgPair(got.args, "--setting-sources", "user") {
			t.Errorf("sanitized 档 sources 不应变: %v", got.args)
		}
		if tier == "isolated" && containsArgPair(got.args, "--setting-sources", "user") {
			t.Errorf("isolated 档不应被升级: %v", got.args)
		}
	}
}

func containsArgPair(args []string, flag, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}

// 通用化 notice 折叠:notice.effective 是权威终态(现有降级 effective=isolated;
// 新增升级 effective=sanitized)。缺省保持旧行为(按 isolated 处理)。
func TestApplyClaudePolicyNoticeHonorsEffectiveTier(t *testing.T) {
	p := agentAIClaudeRemotePolicy{enabled: true, trustTier: "isolated"}
	got := applyClaudePolicyNotice(p, map[string]interface{}{"effective": "sanitized", "requested": "isolated", "reason": "platform_provider_missing"})
	if got.trustTier != "sanitized" {
		t.Errorf("notice.effective=sanitized 应折叠为 sanitized, got %q", got.trustTier)
	}
	if got.policyNotice == nil || got.policyNotice["reason"] != "platform_provider_missing" {
		t.Errorf("policyNotice 应保留: %v", got.policyNotice)
	}
	got2 := applyClaudePolicyNotice(p, map[string]interface{}{"effective": "isolated", "requested": "sanitized", "reason": "sanitize_failed"})
	if got2.trustTier != "isolated" {
		t.Errorf("既有降级路径行为不变, got %q", got2.trustTier)
	}
	got3 := applyClaudePolicyNotice(p, map[string]interface{}{"reason": "legacy_shape"})
	if got3.trustTier != "isolated" {
		t.Errorf("无 effective 时保持旧行为(isolated), got %q", got3.trustTier)
	}
	if !reflect.DeepEqual(got3.policyNotice, map[string]interface{}{"reason": "legacy_shape"}) {
		t.Errorf("policyNotice 应保留: %v", got3.policyNotice)
	}
}
