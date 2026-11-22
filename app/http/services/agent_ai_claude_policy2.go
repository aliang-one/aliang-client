package services

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"aliang.one/nursorgate/common/logger"
)

type claudeApprovalHookStrategy string

const (
	claudeApprovalHookPreToolUseCommand     claudeApprovalHookStrategy = "pretool_command"
	claudeApprovalHookPermissionRequestHTTP claudeApprovalHookStrategy = "permission_request_http"
)

type agentAIClaudeRemotePolicy struct {
	enabled                 bool
	requireInitVerification bool
	projectSkillTrusted     bool
	projectCapabilityMode   string
	settingSources          []string
	permissionAsk           []string
	// trustTier is the effective trust tier ("isolated"|"sanitized"|"full").
	// It supersedes projectSkillTrusted/projectCapabilityMode when the server
	// sends trust_level; legacy fields map onto it for old servers.
	trustTier string
	// projectMCPTrusted opts the project's .mcp.json into the sanitized-tier
	// explicit MCP merge (spec §5).
	projectMCPTrusted bool
	// policyNotice carries a structured downgrade/fallback reason surfaced on
	// ai.run.started as policy_notice. Nil when the tier applied as requested.
	policyNotice map[string]interface{}
}

type agentAIClaudeCapabilities struct {
	verified    bool
	projectPath string
	version     string
	generation  string
	commands    map[string]struct{}
}

func parseAgentAIClaudeRemotePolicy(msg map[string]interface{}) agentAIClaudeRemotePolicy {
	raw, ok := msg["claude_remote_policy"].(map[string]interface{})
	if !ok || raw == nil {
		return agentAIClaudeRemotePolicy{}
	}
	policy := agentAIClaudeRemotePolicy{
		enabled:                 true,
		requireInitVerification: remoteBool(raw, "require_system_init_verification", true),
		projectSkillTrusted:     remoteBool(raw, "project_skill_trusted", false),
		projectCapabilityMode:   remoteString(raw, "project_capability_mode"),
		settingSources:          remoteStringSlice(raw, "setting_sources"),
	}
	if policy.projectCapabilityMode != "sanitized_plugin" {
		policy.projectCapabilityMode = "disabled"
	}
	// Tier resolution (2026-10-16 默认档 sanitized——产品语义「settings.json
	// 优先,平台 env 兜底」的最严实现):
	// 1) valid trust_level wins(服务端显式下发仍可 isolated/full);
	// 2) present-but-invalid trust_level → isolated (fail-closed, 服务端明
	//    显发了无法识别的值属配置事故);
	// 3) no trust_level(含全部 legacy 字段形态) → sanitized:加载 user
	//    scope(用户 settings.json 的 provider/skills/ask 规则生效,与 TUI
	//    一致),但**不加载 project scope**——项目 .claude/settings.json 的
	//    hooks/env/allow 在 -p 模式零审批执行且可被远程克隆的仓库投毒
	//    (R1,实验复现),user scope 的同名内容是设备主人自己的文件,无此面;
	// 4) absent claude_remote_policy → 机制关闭:withClaudeApprovalHook 保
	//    持历史行为 --setting-sources "",settings 全不加载(旧服务端设备的
	//    provider 仍只能走平台 env 或 claudeAuthEnvOverlay 兜底)。
	if level := strings.TrimSpace(remoteString(raw, "trust_level")); level != "" {
		if tier, valid := normalizeClaudeTrustTier(level); valid {
			policy.trustTier = tier
		} else {
			policy.trustTier = "isolated"
		}
	} else {
		policy.trustTier = "sanitized"
	}
	policy.projectMCPTrusted = remoteBool(raw, "project_mcp_trusted", false)
	// The tier table owns --setting-sources (see claudeTierSettingSources);
	// the message-level setting_sources field is legacy and dropped.
	policy.settingSources = nil
	// settings.disableSkillShellExecution 为 write-only 遗留死字段:agent 侧
	// 从未消费(写入端恒 true),服务端继续下发以兼容旧版本,此处不再解析。
	if settings, ok := raw["settings"].(map[string]interface{}); ok {
		if permissions, ok := settings["permissions"].(map[string]interface{}); ok {
			policy.permissionAsk = remoteStringSlice(permissions, "ask")
		}
	}
	// Full tier must not add ask rules: blanket ask outranks allow and would
	// suppress the user's own allow rules (= local approval volume, spec §3).
	if len(policy.permissionAsk) == 0 && policy.trustTier != "full" {
		policy.permissionAsk = []string{"Bash", "Edit", "Write", "NotebookEdit", "mcp__*"}
	}
	return policy
}

func cloneAgentAIClaudeRemotePolicy(policy agentAIClaudeRemotePolicy) agentAIClaudeRemotePolicy {
	policy.settingSources = append([]string(nil), policy.settingSources...)
	policy.permissionAsk = append([]string(nil), policy.permissionAsk...)
	return policy
}

type claudeCodeVersionProbe struct {
	raw string
	ok  bool
}

var claudeCodeVersionProbeCache sync.Map

// probeClaudeRawVersion returns the raw `claude --version` output for the
// executable, cached per executable content. ok=false when the probe fails
// (missing or non-claude binary) — failures are NOT cached (retried each
// pass, bounded by the 2s timeout); successes are cached until the binary
// content changes. Both consumers (hook strategy + trust-tier guard) parse
// their own derivatives from this single source.

func probeClaudeRawVersion(toolPath string) (string, bool) {
	toolPath = strings.TrimSpace(toolPath)
	if toolPath == "" {
		return "", false
	}
	cacheKey := executableProbeCacheKey(toolPath)
	if cached, ok := claudeCodeVersionProbeCache.Load(cacheKey); ok {
		probe, _ := cached.(claudeCodeVersionProbe)
		return probe.raw, probe.ok
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := newBackgroundCommandContext(ctx, toolPath, "--version").CombinedOutput()
	if err != nil {
		return "", false
	}
	claudeCodeVersionProbeCache.Store(cacheKey, claudeCodeVersionProbe{raw: string(out), ok: true})
	return string(out), true
}

// probeClaudeCodeVersion returns the parsed version of the claude executable,
// cached per executable content on success only (via probeClaudeRawVersion).
// ok=false when the probe fails or the raw output is unparsable — callers
// treat that as fail-closed.

func probeClaudeCodeVersion(toolPath string) (claudeCodeVersion, bool) {
	raw, ok := probeClaudeRawVersion(toolPath)
	if !ok {
		return claudeCodeVersion{}, false
	}
	version, ok := parseClaudeCodeVersion(raw)
	if !ok {
		return claudeCodeVersion{}, false
	}
	return version, true
}

// applyClaudeTierVersionGuard downgrades non-isolated tiers to isolated when
// the installed claude predates 2.1.268: those builds never fire
// PermissionRequest hooks in headless --print mode (fixed upstream in
// 2.1.268, CHANGELOG), and user ask rules suppress PreToolUse approvals, so
// loading user/project settings there would break the approval bridge
// (spec §3 guard). The downgrade is tier-wide: sources back to "", no plugin.
// Note: a full-tier policy downgraded here keeps its (intentionally empty)
// ask list — the <2.1.268 PreToolUse strategy never injects permissions.ask, so
// behavior matches legacy isolated runs without refilling the default list.

func applyClaudeTierVersionGuard(tool *agentAITool, policy agentAIClaudeRemotePolicy, sessionID string) agentAIClaudeRemotePolicy {
	if !policy.enabled || policy.trustTier == "" || policy.trustTier == "isolated" {
		return policy
	}
	version, ok := probeClaudeCodeVersion(tool.path)
	if ok && version.atLeast(claudeTierMinMajor, claudeTierMinMinor, claudeTierMinPatch) {
		if !version.atLeast(claudeKnownGoodMajor, claudeKnownGoodMinor, claudeKnownGoodPatch) {
			logger.Warn(fmt.Sprintf("claude-policy: %s is in the 2.1.268-2.1.288 band with known approval-bypass defects fixed in 2.1.289; upgrading claude is recommended session=%s", tool.path, sessionID))
		}
		return policy
	}
	logger.Warn(fmt.Sprintf("claude-policy: %s predates the tier minimum 2.1.268, downgrading trust tier %s to isolated session=%s", tool.path, policy.trustTier, sessionID))
	downgraded := policy
	downgraded.trustTier = "isolated"
	// reason 字面量为历史契约(实际阈值已是 2.1.268),服务端存储透传不改名。
	downgraded.policyNotice = map[string]interface{}{
		"effective": "isolated",
		"requested": policy.trustTier,
		"reason":    "claude_version_below_2_2",
	}
	return downgraded
}

// applyClaudePolicyNotice folds a degrade notice back into the effective
// policy: a degraded run IS an isolated run, so the tier must match the
// notice — otherwise the approval hook would re-open the sources the
// degrade just closed (spec §4.1 fail-closed).

func applyClaudePolicyNotice(policy agentAIClaudeRemotePolicy, notice map[string]interface{}) agentAIClaudeRemotePolicy {
	if notice == nil {
		return policy
	}
	policy.policyNotice = notice
	// notice.effective 是权威终态：既有降级路径为 isolated（sanitize/mcp 失败），
	// 平台 env 缺失回退路径为 sanitized（见 withClaudeRemotePolicy）。缺省保持
	// 旧行为（按 isolated 处理），approval hook 会按折叠后的 tier 重写 sources。
	if tier, valid := normalizeClaudeTrustTier(remoteString(notice, "effective")); valid {
		policy.trustTier = tier
	} else {
		policy.trustTier = "isolated"
	}
	return policy
}

func normalizeClaudeCapabilityName(name string) string {
	return strings.TrimPrefix(strings.TrimSpace(name), "/")
}

func claudeCapabilityProjectPath(path string) string {
	path = cleanAgentProjectPath(path)
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return filepath.Clean(resolved)
	}
	return path
}

func (m *agentAIManager) recordClaudeCapabilities(sessionID, projectPath string, names []string, version string) {
	if m == nil || strings.TrimSpace(sessionID) == "" {
		return
	}
	commands := make(map[string]struct{}, len(names))
	ordered := make([]string, 0, len(names))
	for _, name := range names {
		name = normalizeClaudeCapabilityName(name)
		if name == "" {
			continue
		}
		if _, exists := commands[name]; exists {
			continue
		}
		commands[name] = struct{}{}
		ordered = append(ordered, name)
	}
	sort.Strings(ordered)
	digest := sha256.Sum256([]byte(strings.Join(append([]string{strings.TrimSpace(version)}, ordered...), "\x00")))
	caps := agentAIClaudeCapabilities{
		verified:    true,
		projectPath: claudeCapabilityProjectPath(projectPath),
		version:     strings.TrimSpace(version),
		generation:  hex.EncodeToString(digest[:8]),
		commands:    commands,
	}
	m.mu.Lock()
	if session := m.sessions[sessionID]; session != nil && claudeCapabilityProjectPath(session.projectPath) == caps.projectPath {
		session.claudeCaps = caps
	}
	m.mu.Unlock()
}

func (m *agentAIManager) claudeCapabilities(sessionID, projectPath string) (agentAIClaudeCapabilities, bool) {
	if m == nil || strings.TrimSpace(sessionID) == "" {
		return agentAIClaudeCapabilities{}, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	session := m.sessions[sessionID]
	if session == nil || !session.claudeCaps.verified ||
		claudeCapabilityProjectPath(projectPath) != session.claudeCaps.projectPath {
		return agentAIClaudeCapabilities{}, false
	}
	caps := session.claudeCaps
	caps.commands = make(map[string]struct{}, len(session.claudeCaps.commands))
	for name := range session.claudeCaps.commands {
		caps.commands[name] = struct{}{}
	}
	return caps, true
}

var claudeCodeVersionRe = regexp.MustCompile(`\b(\d+)\.(\d+)\.(\d+)\b`)
