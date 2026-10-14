package services

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"aliang.one/nursorgate/common/logger"
)

const claudeProjectCapabilityLimit = 500

// claudeProjectPluginNamespace is the plugin name used for the sanitized-tier
// temporary capability plugin. Slash-list entries (agent_slash_commands.go)
// and the plugin manifest must stay in lockstep: the init event reports
// "<namespace>:<name>" and the phone list must match (spec §6).
const claudeProjectPluginNamespace = "aliang-project"

// normalizeClaudeTrustTier maps a server-sent trust_level to the internal tier
// constant. ok is false for empty/unknown values so callers can distinguish
// "absent" (legacy field fallback) from "present but invalid" (fail-closed).
func normalizeClaudeTrustTier(value string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "isolated":
		return "isolated", true
	case "sanitized":
		return "sanitized", true
	case "full":
		return "full", true
	default:
		return "", false
	}
}

// claudeTierSettingSources returns the --setting-sources value for a tier:
// isolated loads no settings files; sanitized loads user scope (user settings,
// user/plugin skills and commands, user-scope MCP); full loads user+project
// (project scope additionally brings project settings, .mcp.json, project
// capabilities and CLAUDE.md — i.e. local parity).
func claudeTierSettingSources(tier string) string {
	switch tier {
	case "full":
		return "user,project"
	case "sanitized":
		return "user"
	default:
		return ""
	}
}

// platformProviderEnvProbe 返回平台通道注入 env 的 (baseURL, token, 齐全)。
// var 是单测注入点：hermetic，不依赖测试机的真实环境。
var platformProviderEnvProbe = func() (string, string, bool) {
	base, _ := agentChildProcessEnvValue("ANTHROPIC_BASE_URL")
	token, _ := agentChildProcessEnvValue("ANTHROPIC_AUTH_TOKEN")
	base, token = strings.TrimSpace(base), strings.TrimSpace(token)
	return base, token, base != "" && token != ""
}

// withClaudeRemotePolicy applies the remote trust tier to a claude tool:
// per-tier --setting-sources, the sanitized capability plugin for the
// sanitized tier, and the explicit MCP merge when the server marked project
// MCP trusted. On plugin/MCP preparation failure it degrades the run to the
// isolated tier (fail-closed) and returns a structured policy notice instead
// of failing the run (spec §3/§4.1).
//
// 平台 env 在位时注入 CLAUDE_CODE_PROVIDER_MANAGED_BY_HOST=1：settings env
// 块会覆盖进程 env（官方语义，code.claude.com/docs/en/env-vars#precedence），
// 不钉死则 sanitized/full 档被用户 settings.json 个人通道反杀。平台缺失的
// isolated→sanitized 回退不在此处（会撤销版本守卫的降级），见
// promoteIsolatedTierForPlatformFallback——必须发生在版本守卫之前。
func withClaudeRemotePolicy(tool *agentAITool, run agentAIRun) (*agentAITool, func(), map[string]interface{}) {
	cleanup := func() {}
	if tool == nil || !run.claudePolicy.enabled {
		return tool, cleanup, nil
	}
	copied := *tool
	copied.args = append([]string(nil), tool.args...)
	if _, _, present := platformProviderEnvProbe(); present {
		copied.env = append(copied.env, "CLAUDE_CODE_PROVIDER_MANAGED_BY_HOST=1")
	}
	flags := []string{"--setting-sources", claudeTierSettingSources(run.claudePolicy.trustTier)}
	if run.claudePolicy.trustTier == "sanitized" {
		pluginDir, err := prepareClaudeProjectCapabilityPlugin(run.projectPath)
		if err != nil {
			logger.Warn(fmt.Sprintf("claude-policy: sanitize failed, degrading run to isolated tier session=%s project=%q err=%v", run.sessionID, run.projectPath, err))
			copied.args = append([]string{"--setting-sources", ""}, copied.args...)
			return &copied, cleanup, map[string]interface{}{
				"effective": "isolated",
				"requested": "sanitized",
				"reason":    "sanitize_failed",
			}
		}
		cleanup = func() { _ = os.RemoveAll(pluginDir) }
		flags = append(flags, "--plugin-dir", pluginDir)
		mcpFlags, mcpCleanup, mcpErr := claudeTierMCPArgs(run.claudePolicy, run.projectPath)
		if mcpErr != nil {
			logger.Warn(fmt.Sprintf("claude-policy: mcp merge failed, degrading run to isolated tier session=%s project=%q err=%v", run.sessionID, run.projectPath, mcpErr))
			_ = os.RemoveAll(pluginDir)
			copied.args = append([]string{"--setting-sources", ""}, copied.args...)
			return &copied, cleanup, map[string]interface{}{
				"effective": "isolated",
				"requested": "sanitized",
				"reason":    "mcp_merge_failed",
			}
		}
		if len(mcpFlags) > 0 {
			inner := cleanup
			cleanup = func() { mcpCleanup(); inner() }
			flags = append(flags, mcpFlags...)
		}
	}
	copied.args = append(flags, copied.args...)
	return &copied, cleanup, nil
}

// promoteIsolatedTierForPlatformFallback 平台通道（env 注入的
// ANTHROPIC_BASE_URL/AUTH_TOKEN）缺失且档位为 isolated 时，自动升为
// sanitized 让用户 settings.json 提供上游——否则 claude 没有任何 provider，
// 直接 "Not logged in" 秒败（2026-10-13 产品决策 A「仅兜底式」）。
//
// 必须在 applyClaudeTierVersionGuard 之前调用：claude<2.2.0 的 fail-closed
// 版本守卫要对提升后的档位继续行使职权（提升不得撤销守卫的降级——否则
// 2.1.x 上用户 ask 规则会压死审批桥，评审 2026-10-13 实验复现）。notice 里的
// requested 保留原档位，升级事实经 policy_notice 对服务端/手机可见。
func promoteIsolatedTierForPlatformFallback(policy agentAIClaudeRemotePolicy, sessionID string) agentAIClaudeRemotePolicy {
	if !policy.enabled || policy.trustTier != "isolated" {
		return policy
	}
	if _, _, present := platformProviderEnvProbe(); present {
		return policy
	}
	promoted := policy
	promoted.trustTier = "sanitized"
	promoted.policyNotice = map[string]interface{}{
		"effective": "sanitized",
		"requested": policy.trustTier,
		"reason":    "platform_provider_missing",
	}
	logger.Info(fmt.Sprintf("claude-policy: platform provider env missing, promoting isolated->sanitized (user settings.json provides upstream) session=%s", sessionID))
	return promoted
}

// readMCPServersFile 读一个 claude 形态 {"mcpServers":{...}} JSON 文件并入
// dst。文件缺失/不可读静默容忍;解析失败记日志且仅跳过该源(容忍,不降级)。
// 返回非 nil error 时 dst 保持不变(错误路径在合并前返回);文件缺失是常态,
// 调用方应忽略此 error(参见 claudeTierMCPArgs 两处调用)。
// 原 claudeTierMCPArgs 内闭包,收编为包级以便 MCP 清单采集复用同一数据源。
func readMCPServersFile(path string, dst map[string]interface{}) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err // absent/unreadable source is tolerated silently
	}
	var parsed struct {
		MCPServers map[string]interface{} `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		logger.Warn(fmt.Sprintf("claude-policy: unparseable MCP source %s: %v", path, err))
		return err // tolerate, don't degrade: skip this source only
	}
	for name, server := range parsed.MCPServers {
		dst[name] = server
	}
	return nil
}

// claudeTierMCPArgs builds --strict-mcp-config/--mcp-config flags for the
// sanitized tier when the server marked the project MCP trusted (spec §5):
// user-scope servers (top-level "mcpServers" in <agentHome>/.claude.json —
// NOT the per-project entries) plus user-scope plugin servers
// ("plugin:<name>:<server>", see readPluginMCPServers) merged with the
// project's .mcp.json, project entries winning name collisions (mirrors the
// CLI's local > project > user precedence at merge time). Returns nil flags —
// native user-scope discovery — when the project contributes no servers.
// Callers invoke the returned cleanup only on success; on error the
// implementation has already released everything it created.
// Note: a project .mcp.json that only redefines names already present in
// user scope contributes no new names, so no merge happens and the
// user-scope definitions win (direction: load fewer, never more).
func claudeTierMCPArgs(policy agentAIClaudeRemotePolicy, projectPath string) ([]string, func(), error) {
	if !policy.projectMCPTrusted {
		return nil, func() {}, nil
	}
	merged := map[string]interface{}{}
	if home := agentHome(); home != "" {
		readMCPServersFile(filepath.Join(home, ".claude.json"), merged)
	}
	readPluginMCPServers(merged)
	beforeProject := len(merged)
	readMCPServersFile(filepath.Join(projectPath, ".mcp.json"), merged)
	projectCount := len(merged) - beforeProject
	if projectCount == 0 {
		return nil, func() {}, nil
	}
	payload, err := json.Marshal(map[string]interface{}{"mcpServers": merged})
	if err != nil {
		return nil, func() {}, err
	}
	tmp, err := os.CreateTemp("", "aliang-claude-mcp-*.json")
	if err != nil {
		return nil, func() {}, err
	}
	if _, err := tmp.Write(payload); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return nil, func() {}, err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return nil, func() {}, err
	}
	cleanup := func() { _ = os.Remove(tmp.Name()) }
	return []string{"--strict-mcp-config", "--mcp-config", tmp.Name()}, cleanup, nil
}

// claudeEnabledPluginsIn 同 claudeEnabledPlugins,但 home 由调用方给定
// (MCP 清单采集的可测试入口);所有读取以传入 home 为根,不回落 agentHome()。
// A missing file, a missing key, a non-object value, or a parse error all
// yield an empty map — every plugin disabled (fail-closed, matching
// readPluginMCPServers's tolerance posture: load fewer, never more). Only an
// absent file stays silent; a file that exists but cannot be parsed is logged
// like other tolerated sources.
func claudeEnabledPluginsIn(home string) map[string]interface{} {
	if home == "" {
		return nil
	}
	settingsPath := filepath.Join(home, ".claude", "settings.json")
	raw, err := os.ReadFile(settingsPath)
	if err != nil {
		return nil // absent/unreadable settings: all plugins disabled (silent)
	}
	var settings map[string]interface{}
	if err := json.Unmarshal(raw, &settings); err != nil {
		logger.Warn(fmt.Sprintf("claude-policy: unparseable settings %s: %v", settingsPath, err))
		return nil
	}
	enabled, _ := settings["enabledPlugins"].(map[string]interface{})
	return enabled
}

// claudeEnabledPlugins reads the "enabledPlugins" map from
// <agentHome>/.claude/settings.json.
func claudeEnabledPlugins() map[string]interface{} {
	return claudeEnabledPluginsIn(agentHome())
}

// claudePluginEnabled reports whether enabledPlugins marks a plugin key
// enabled: only an explicit true counts (spec §9.9 — load fewer, never more;
// installed-but-unlisted plugins are treated as disabled).
func claudePluginEnabled(enabledPlugins map[string]interface{}, key string) bool {
	v, ok := enabledPlugins[key]
	if !ok {
		return false
	}
	enabled, ok := v.(bool)
	return ok && enabled
}

// claudePluginInstall 是一个已启用 user scope 插件安装项。
type claudePluginInstall struct {
	key         string // "<plugin>@<market>"
	pluginName  string
	installPath string
}

// enabledUserScopePluginInstallsIn 按 <home> 下 installed_plugins.json(键
// 排序)× settings.json enabledPlugins 显式启用,枚举 user scope 安装项。
// 所有读取以传入 home 为根(可测试,不回落 agentHome());容忍姿态与
// readPluginMCPServers 一致:注册表缺失/畸形返回 nil(无事发生)。逐项门槛:
// 仅 enabledPlugins 显式 true 的插件入选,非 user scope/缺 installPath 的
// 安装项跳过(fail-closed:load fewer, never more)。
func enabledUserScopePluginInstallsIn(home string) []claudePluginInstall {
	if home == "" {
		return nil
	}
	registryPath := filepath.Join(home, ".claude", "plugins", "installed_plugins.json")
	raw, err := os.ReadFile(registryPath)
	if err != nil {
		return nil // no plugin registry: nothing installed (silent no-op)
	}
	var registry struct {
		Plugins map[string][]map[string]interface{} `json:"plugins"`
	}
	if err := json.Unmarshal(raw, &registry); err != nil {
		logger.Warn(fmt.Sprintf("claude-policy: unparseable plugin registry %s: %v", registryPath, err))
		return nil
	}
	enabledPlugins := claudeEnabledPluginsIn(home)
	pluginKeys := make([]string, 0, len(registry.Plugins))
	for key := range registry.Plugins {
		pluginKeys = append(pluginKeys, key)
	}
	sort.Strings(pluginKeys) // deterministic merged output
	var installs []claudePluginInstall
	for _, key := range pluginKeys {
		if !claudePluginEnabled(enabledPlugins, key) {
			continue
		}
		pluginName, _, _ := strings.Cut(key, "@")
		if pluginName == "" {
			continue
		}
		for _, install := range registry.Plugins[key] {
			// Per-install field tolerance: a non-string/missing scope or
			// installPath skips that install only, never the whole registry.
			scope, _ := install["scope"].(string)
			installPath, _ := install["installPath"].(string)
			if scope != "user" || installPath == "" {
				continue
			}
			installs = append(installs, claudePluginInstall{key: key, pluginName: pluginName, installPath: installPath})
		}
	}
	return installs
}

// enabledUserScopePluginInstalls 是无参包装(readPluginMCPServers 消费)。
func enabledUserScopePluginInstalls() []claudePluginInstall {
	return enabledUserScopePluginInstallsIn(agentHome())
}

// readPluginMCPServers adds MCP servers declared by user-scope plugins
// (installed_plugins.json → <installPath>/.mcp.json) into dst under claude's
// native composite names ("plugin:<plugin>:<server>"), so strict-mode merged
// configs keep plugin servers that --strict-mcp-config would otherwise
// suppress (spec §9.9). Only plugins explicitly enabled in
// ~/.claude/settings.json "enabledPlugins" ("<plugin>@<market>": true) are
// merged — a missing key, false, or any other value means DISABLED, so an
// installed-but-unlisted plugin is treated as off (fail-closed: load fewer,
// never more, mirroring native claude's own gate). Plugin declarations come
// in two shapes — the wrapped {"mcpServers":{...}} and the flat
// {"<server>":{...}} — both are handled. Read/parse failures are logged and
// skipped (tolerate, never degrade).
// Returns the number of servers added.
func readPluginMCPServers(dst map[string]interface{}) int {
	added := 0
	for _, inst := range enabledUserScopePluginInstalls() {
		added += readPluginMCPDeclaration(inst.installPath, inst.pluginName, dst)
	}
	return added
}

// readPluginMCPDeclaration decodes one plugin's .mcp.json into dst under the
// composite "plugin:<pluginName>:<server>" names. The wrapped
// {"mcpServers":{...}} shape wins when present; otherwise every top-level key
// with an object value is treated as a server (the flat/legacy shape skips
// non-object entries such as "$schema"). Existing keys in dst are never
// overwritten (first writer wins; call order gives user < plugin < project).
func readPluginMCPDeclaration(installPath, pluginName string, dst map[string]interface{}) int {
	raw, err := os.ReadFile(filepath.Join(installPath, ".mcp.json"))
	if err != nil {
		if !os.IsNotExist(err) {
			logger.Warn(fmt.Sprintf("claude-policy: unreadable plugin MCP declaration %s: %v", installPath, err))
		}
		return 0 // absent declaration: this plugin provides no MCP servers
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		logger.Warn(fmt.Sprintf("claude-policy: unparseable plugin MCP declaration %s: %v", installPath, err))
		return 0
	}
	var servers map[string]interface{}
	// Note: a flat server literally named "mcpServers" is interpreted as the
	// wrapped shape here (mirrors native precedence).
	if wrapped, ok := decoded["mcpServers"].(map[string]interface{}); ok {
		servers = wrapped
	} else {
		servers = map[string]interface{}{}
		for name, value := range decoded {
			if cfg, ok := value.(map[string]interface{}); ok {
				servers[name] = cfg
			}
		}
	}
	names := make([]string, 0, len(servers))
	for name := range servers {
		names = append(names, name)
	}
	sort.Strings(names)
	added := 0
	for _, name := range names {
		key := "plugin:" + pluginName + ":" + name
		if _, exists := dst[key]; exists {
			continue
		}
		dst[key] = servers[name]
		added++
	}
	return added
}

func prepareClaudeProjectCapabilityPlugin(projectPath string) (string, error) {
	root, err := os.MkdirTemp("", "aliang-claude-project-")
	if err != nil {
		return "", err
	}
	fail := func(err error) (string, error) {
		_ = os.RemoveAll(root)
		return "", err
	}
	manifestDir := filepath.Join(root, ".claude-plugin")
	if err := os.MkdirAll(manifestDir, 0o700); err != nil {
		return fail(err)
	}
	manifest := []byte(fmt.Sprintf(`{"name":%q,"version":"1.0.0","description":"Sanitized remote project capabilities"}`, claudeProjectPluginNamespace))
	if err := os.WriteFile(filepath.Join(manifestDir, "plugin.json"), manifest, 0o600); err != nil {
		return fail(err)
	}
	dotClaude := filepath.Join(projectPath, ".claude")
	if err := copySanitizedClaudeSkills(filepath.Join(dotClaude, "skills"), filepath.Join(root, "skills")); err != nil {
		return fail(err)
	}
	if err := copySanitizedClaudeCommands(filepath.Join(dotClaude, "commands"), filepath.Join(root, "commands")); err != nil {
		return fail(err)
	}
	agentsDir := filepath.Join(root, "agents")
	if err := copySanitizedClaudeAgents(filepath.Join(dotClaude, "agents"), agentsDir); err != nil {
		return fail(err)
	}
	return root, nil
}

func copySanitizedClaudeSkills(sourceRoot, targetRoot string) error {
	entries, err := os.ReadDir(sourceRoot)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	count := 0
	for _, entry := range entries {
		if count >= claudeProjectCapabilityLimit {
			logger.Warn(fmt.Sprintf("claude-policy: skill cap %d reached at %s; further skills skipped", claudeProjectCapabilityLimit, sourceRoot))
			break
		}
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		sourceDir := filepath.Join(sourceRoot, entry.Name())
		sourceMarkdown := filepath.Join(sourceDir, "SKILL.md")
		if _, err := os.Stat(sourceMarkdown); err != nil {
			continue
		}
		targetDir := filepath.Join(targetRoot, entry.Name())
		if err := os.MkdirAll(targetDir, 0o700); err != nil {
			return err
		}
		markdown, err := sanitizedClaudeMarkdown(sourceMarkdown, claudeSanitizeSkill)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(targetDir, "SKILL.md"), markdown, 0o600); err != nil {
			return err
		}
		resources, err := os.ReadDir(sourceDir)
		if err != nil {
			return err
		}
		linkedResources := 0
		for _, resource := range resources {
			if linkedResources >= claudeProjectCapabilityLimit {
				break
			}
			if resource.Name() == "SKILL.md" || strings.HasPrefix(resource.Name(), ".") {
				continue
			}
			if err := os.Symlink(filepath.Join(sourceDir, resource.Name()), filepath.Join(targetDir, resource.Name())); err != nil {
				return err
			}
			linkedResources++
		}
		count++
	}
	return nil
}

func copySanitizedClaudeCommands(sourceRoot, targetRoot string) error {
	count := 0
	err := filepath.WalkDir(sourceRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) {
				return nil
			}
			return walkErr
		}
		if entry.IsDir() {
			if path != sourceRoot && strings.HasPrefix(entry.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if count >= claudeProjectCapabilityLimit {
			logger.Warn(fmt.Sprintf("claude-policy: command cap %d reached at %s; further commands skipped", claudeProjectCapabilityLimit, sourceRoot))
			return filepath.SkipAll
		}
		if !strings.HasSuffix(strings.ToLower(entry.Name()), ".md") {
			return nil
		}
		rel, err := filepath.Rel(sourceRoot, path)
		if err != nil || strings.HasPrefix(rel, "..") {
			return fmt.Errorf("invalid Claude command path %q", path)
		}
		markdown, err := sanitizedClaudeMarkdown(path, claudeSanitizeCommand)
		if err != nil {
			return err
		}
		target := filepath.Join(targetRoot, rel)
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(target, markdown, 0o600); err != nil {
			return err
		}
		count++
		return nil
	})
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// claudeSanitizeKind selects the frontmatter whitelist for a capability file.
type claudeSanitizeKind string

const (
	claudeSanitizeSkill   claudeSanitizeKind = "skill"
	claudeSanitizeCommand claudeSanitizeKind = "command"
	claudeSanitizeAgent   claudeSanitizeKind = "agent"
)

// copySanitizedClaudeAgents copies project .claude/agents/*.md through the
// agent whitelist so sanitized-tier `agent:` references resolve (spec §4.3).
func copySanitizedClaudeAgents(sourceRoot, targetRoot string) error {
	entries, err := os.ReadDir(sourceRoot)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := os.MkdirAll(targetRoot, 0o700); err != nil {
		return err
	}
	count := 0
	for _, entry := range entries {
		if count >= claudeProjectCapabilityLimit {
			logger.Warn(fmt.Sprintf("claude-policy: agent cap %d reached at %s; further agents skipped", claudeProjectCapabilityLimit, sourceRoot))
			break
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			continue
		}
		if entry.IsDir() || strings.HasPrefix(entry.Name(), ".") || !strings.HasSuffix(strings.ToLower(entry.Name()), ".md") {
			continue
		}
		markdown, err := sanitizedClaudeMarkdown(filepath.Join(sourceRoot, entry.Name()), claudeSanitizeAgent)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(targetRoot, entry.Name()), markdown, 0o600); err != nil {
			return err
		}
		count++
	}
	return nil
}

// sanitizedClaudeMarkdown preserves the prompt body and safe discovery fields
// but deliberately drops hooks, allowed-tools, model, and arbitrary frontmatter
// that could alter the remote execution boundary (spec §4.2). context/agent
// carry no permission meaning and are preserved; agent files additionally keep
// tools, which RESTRICTS the agent's tool set rather than granting permissions.
func sanitizedClaudeMarkdown(path string, kind claudeSanitizeKind) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	fm, _ := parseSlashFrontmatter(path)
	body := claudeMarkdownBody(string(raw))
	var out strings.Builder
	out.WriteString("---\n")
	writeYAMLString := func(key, value string) {
		if strings.TrimSpace(value) == "" {
			return
		}
		quoted, _ := json.Marshal(value)
		fmt.Fprintf(&out, "%s: %s\n", key, quoted)
	}
	switch kind {
	case claudeSanitizeAgent:
		writeYAMLString("name", fm.name)
		writeYAMLString("description", fm.description)
		writeYAMLString("tools", fm.tools)
	case claudeSanitizeSkill:
		writeYAMLString("name", fm.name)
		writeYAMLString("description", fm.description)
		writeYAMLString("argument-hint", fm.argumentHint)
		if !fm.isUserInvocable() {
			out.WriteString("user-invocable: false\n")
		}
		if !fm.isModelInvocable() {
			out.WriteString("disable-model-invocation: true\n")
		}
		writeYAMLString("context", fm.context)
		writeYAMLString("agent", fm.agent)
	case claudeSanitizeCommand:
		writeYAMLString("description", fm.description)
		writeYAMLString("argument-hint", fm.argumentHint)
		if !fm.isUserInvocable() {
			out.WriteString("user-invocable: false\n")
		}
		if !fm.isModelInvocable() {
			out.WriteString("disable-model-invocation: true\n")
		}
		writeYAMLString("context", fm.context)
		writeYAMLString("agent", fm.agent)
	default:
		return nil, fmt.Errorf("unknown claude sanitize kind %q", kind)
	}
	out.WriteString("---\n")
	out.WriteString(body)
	return []byte(out.String()), nil
}

func claudeMarkdownBody(raw string) string {
	raw = strings.TrimPrefix(raw, "\uFEFF")
	lines := strings.Split(raw, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return raw
	}
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			return strings.Join(lines[i+1:], "\n")
		}
	}
	return raw
}
