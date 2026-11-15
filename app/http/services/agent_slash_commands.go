package services

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/processor/config"
)

const (
	// slashCommandMaxPerScope bounds a single discovery scope so a pathological
	// tree cannot produce an unbounded payload. Generous for real projects.
	slashCommandMaxPerScope = 500
	// slashCommandMaxPlugins bounds plugin enumeration (a handful in practice).
	slashCommandMaxPlugins = 100
)

// agentSlashCommandsListPayload handles `slash.commands.list`: it introspects
// the project's `.claude` tree (plus optional user-level `~/.claude` and enabled
// plugins) for the Claude provider, `~/.codex/prompts` plus a static builtin
// baseline for the Codex provider, and `.opencode`/`~/.config/opencode` command
// roots for the OpenCode provider. For the agentcore (AliangCode 内核) provider
// it surfaces the agent-local builtin set (the commands the dispatcher actually
// intercepts — /clear /model /help /cost /compact) plus live capability
// entries: one /skill:<name> per injected skill and one /mcp:<server> per
// configured MCP server, so the panel reflects what the kernel will really
// load. It returns the `/`-command surface as AgentCommandInfo-like entries,
// each tagged with the provider it belongs to.
// Claude builtins ARE part of the baseline here: the headless CLI's init event
// reports its own command surface (verified live on 2.1.286 — builtins like
// compact/clear/model/mcp are present), so filterVerifiedClaudeCommands keeps
// rather than drops them. For Codex the builtins are surfaced as a convenience
// baseline because Codex's headless app-server does not enumerate them itself.
//
// The optional `provider` field selects which provider's commands to return
// ("claude"/"claudecode", "codex", "opencode", or "agentcore"). When absent,
// all providers' commands are returned tagged, so the client can filter by the
// active conversation.
func agentSlashCommandsListPayload(msg map[string]interface{}) map[string]interface{} {
	return agentSlashCommandsListPayloadWithManager(msg, nil)
}

func agentSlashCommandsListPayloadWithManager(msg map[string]interface{}, manager *agentAIManager) map[string]interface{} {
	requestID := remoteString(msg, "request_id")
	projectPath, err := resolveAgentProjectPath(remoteString(msg, "project_path"))
	if err != nil {
		return agentSlashCommandsErrorPayload(requestID, err)
	}
	provider := normalizeSlashProvider(remoteString(msg, "provider"))
	includeUser := remoteBool(msg, "include_user_level", true)
	includePlugins := remoteBool(msg, "include_plugins", true)
	remotePolicy := parseAgentAIClaudeRemotePolicy(msg)
	// Hoisted init-snapshot lookup (policy-independent, same conditions as the
	// verified/version block below): spec §9.11 version guard — when the
	// session's recorded claude predates 2.1.268 (or is unparsable), the
	// EFFECTIVE tier for list gating is isolated even if the request asked for
	// sanitized/full, so aliang-project:* entries are not exposed before the
	// run-path guard downgrades the actual run. Residual window (spec §9.11):
	// with no snapshot yet (pre-first-run, or nil manager) the REQUESTED tier
	// gates the list (verified:false on the payload); the first run records
	// the snapshot and the list self-corrects.
	effectiveTier := remotePolicy.trustTier
	var claudeCaps agentAIClaudeCapabilities
	haveClaudeCaps := false
	if provider == "claude" && manager != nil {
		if caps, ok := manager.claudeCapabilities(remoteString(msg, "session_id"), projectPath); ok {
			claudeCaps, haveClaudeCaps = caps, true
			if remotePolicy.enabled {
				if v, pok := parseClaudeCodeVersion(caps.version); !pok || !v.atLeast(2, 1, 268) {
					effectiveTier = "isolated"
				}
			}
		}
	}
	projectPrefix := ""
	if remotePolicy.enabled && effectiveTier == "sanitized" {
		projectPrefix = claudeProjectPluginNamespace + ":"
	}
	includeProjectClaude := !remotePolicy.enabled || effectiveTier == "sanitized" || effectiveTier == "full"

	wantClaude := provider == "" || provider == "claude"
	wantCodex := provider == "" || provider == "codex"
	wantOpenCode := provider == "" || provider == "opencode"
	wantAgentCore := provider == "" || provider == "agentcore"

	var commands []map[string]interface{}
	if wantClaude {
		if includeProjectClaude {
			commands = append(commands, collectProjectSlashCommands(projectPath, projectPrefix)...)
		}
		if includeUser {
			commands = append(commands, collectUserSlashCommands()...)
		}
		if includePlugins {
			commands = append(commands, collectPluginSlashCommands()...)
		}
		commands = append(commands, claudeBuiltinCommands()...)
	}
	if wantCodex {
		commands = append(commands, collectCodexSlashCommands()...)
	}
	if wantOpenCode {
		commands = append(commands, collectOpenCodeSlashCommands(projectPath, includeUser)...)
	}
	if wantAgentCore {
		commands = append(commands, collectAgentCoreSlashCommands(manager)...)
	}
	sortSlashCommands(commands)
	verified := !wantClaude
	var claudeVersion, capabilityGeneration string
	if haveClaudeCaps {
		verified = true
		claudeVersion = claudeCaps.version
		capabilityGeneration = claudeCaps.generation
		commands = filterVerifiedClaudeCommands(commands, claudeCaps.commands)
	}

	payload := map[string]interface{}{
		"type":         models.AgentEventSlashCommandsListResult,
		"request_id":   requestID,
		"project_path": projectPath,
		"provider":     provider,
		"commands":     commands,
		"generated_at": time.Now().UTC().Format(time.RFC3339),
		"verified":     verified,
	}
	if claudeVersion != "" {
		payload["claude_version"] = claudeVersion
	}
	if capabilityGeneration != "" {
		payload["capability_generation"] = capabilityGeneration
	}
	return payload
}

func filterVerifiedClaudeCommands(commands []map[string]interface{}, available map[string]struct{}) []map[string]interface{} {
	filtered := make([]map[string]interface{}, 0, len(commands))
	for _, command := range commands {
		// Host-side builtin baselines are exempt from the init-snapshot filter:
		// the snapshot is the CLI's report of ITS command surface, and builtins
		// are not derived from it. (In practice the snapshot includes builtins —
		// verified live on 2.1.286 — but the exemption keeps the baseline
		// independent of that format detail.)
		if scope, _ := command["scope"].(string); scope == "builtin" {
			filtered = append(filtered, command)
			continue
		}
		name := normalizeClaudeCapabilityName(remoteString(command, "name"))
		if name == "" {
			continue
		}
		if _, ok := available[name]; ok {
			filtered = append(filtered, command)
			continue
		}
		for capability := range available {
			if strings.HasSuffix(capability, ":"+name) || strings.HasSuffix(name, ":"+capability) {
				filtered = append(filtered, command)
				break
			}
		}
	}
	return filtered
}

// normalizeSlashProvider maps a provider hint to the command-surface provider.
// An empty or unrecognized value returns "" meaning "unspecified" — callers
// collect all providers so the client can filter by the active conversation.
func normalizeSlashProvider(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "codex":
		return "codex"
	case "claude", "claudecode", "claude-code", "claude_code":
		return "claude"
	case "opencode", "open-code", "open_code":
		return "opencode"
	case "agentcore", "agent-core", "agent_core":
		return "agentcore"
	}
	return ""
}

func agentSlashCommandsErrorPayload(requestID string, err error) map[string]interface{} {
	message := "slash command discovery failed"
	if err != nil {
		message = err.Error()
	}
	return map[string]interface{}{
		"type":       models.AgentEventSlashCommandsListError,
		"request_id": requestID,
		"error":      message,
	}
}

// collectProjectSlashCommands scans <projectPath>/.claude/{commands,skills}.
// namePrefix is prepended to every entry name: sanitized-tier entries carry
// "aliang-project:" so list names match the plugin-namespace invocation names
// the claude init event reports (spec §6).
func collectProjectSlashCommands(projectPath string, namePrefix string) []map[string]interface{} {
	dotClaude := filepath.Join(projectPath, ".claude")
	var out []map[string]interface{}
	out = append(out, scanCommandMarkdowns(filepath.Join(dotClaude, "commands"), "project", "project", projectPath, namePrefix, "claude")...)
	out = append(out, scanSkillMarkdowns(filepath.Join(dotClaude, "skills"), "project", "project", projectPath, namePrefix, "claude")...)
	return out
}

// collectUserSlashCommands scans ~/.claude/{commands,skills}.
func collectUserSlashCommands() []map[string]interface{} {
	home := agentHome()
	if home == "" {
		return nil
	}
	homeClaude := filepath.Join(home, ".claude")
	if _, err := os.Stat(homeClaude); err != nil {
		return nil
	}
	var out []map[string]interface{}
	out = append(out, scanCommandMarkdowns(filepath.Join(homeClaude, "commands"), "user", "user", homeClaude, "", "claude")...)
	out = append(out, scanSkillMarkdowns(filepath.Join(homeClaude, "skills"), "user", "user", homeClaude, "", "claude")...)
	return out
}

// collectPluginSlashCommands enumerates enabled plugins from
// ~/.claude/plugins/installed_plugins.json and scans each install path.
// Only user-scoped plugins are included (they apply everywhere); project-scoped
// plugins require a projectPath match the agent cannot reliably evaluate here.
func collectPluginSlashCommands() []map[string]interface{} {
	home := agentHome()
	if home == "" {
		return nil
	}
	manifestPath := filepath.Join(home, ".claude", "plugins", "installed_plugins.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil
	}
	var manifest installedPluginsManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil
	}
	var out []map[string]interface{}
	handled := 0
	for key, records := range manifest.Plugins {
		pluginName := pluginShortName(key)
		if pluginName == "" {
			continue
		}
		prefix := pluginName + ":"
		for _, rec := range records {
			if !pluginRecordEnabled(rec) {
				continue
			}
			installPath := strings.TrimSpace(rec.InstallPath)
			if installPath == "" {
				continue
			}
			if _, err := os.Stat(installPath); err != nil {
				continue
			}
			out = append(out, scanCommandMarkdowns(filepath.Join(installPath, "commands"), "plugin", "plugin", installPath, prefix, "claude")...)
			out = append(out, scanSkillMarkdowns(filepath.Join(installPath, "skills"), "plugin", "plugin", installPath, prefix, "claude")...)
		}
		handled++
		if handled >= slashCommandMaxPlugins {
			break
		}
	}
	return capSlashCommands(out)
}

// collectCodexSlashCommands returns Codex's slash-command surface: the static
// builtin baseline plus any custom prompts the user placed in ~/.codex/prompts.
// (Custom prompts are legacy/deprecated in Codex, but are still scanned when
// present so existing setups keep working.) Codex's headless app-server does
// not enumerate builtins itself, so the baseline is sourced here.
func collectCodexSlashCommands() []map[string]interface{} {
	out := codexBuiltinCommands()
	if home := agentHome(); home != "" {
		out = append(out, collectCodexPromptCommands(filepath.Join(home, ".codex", "prompts"))...)
	}
	return out
}

// collectOpenCodeSlashCommands returns OpenCode's baseline slash-command surface
// plus project/user markdown commands when present. OpenCode conventionally uses
// command roots under `.opencode` and `~/.config/opencode`; both singular and
// plural directory names are accepted to preserve older/manual setups.
func collectOpenCodeSlashCommands(projectPath string, includeUser bool) []map[string]interface{} {
	out := openCodeBuiltinCommands()
	out = append(out, scanCommandMarkdowns(filepath.Join(projectPath, ".opencode", "commands"), "project", "project", projectPath, "", "opencode")...)
	out = append(out, scanCommandMarkdowns(filepath.Join(projectPath, ".opencode", "command"), "project", "project", projectPath, "", "opencode")...)
	if includeUser {
		for _, root := range openCodeUserCommandRoots() {
			out = append(out, scanCommandMarkdowns(root, "user", "user", filepath.Dir(root), "", "opencode")...)
		}
	}
	return capSlashCommands(out)
}

func openCodeUserCommandRoots() []string {
	home := agentHome()
	if home == "" {
		return nil
	}
	return []string{
		filepath.Join(home, ".config", "opencode", "commands"),
		filepath.Join(home, ".config", "opencode", "command"),
		filepath.Join(home, ".opencode", "commands"),
		filepath.Join(home, ".opencode", "command"),
	}
}

func openCodeBuiltinCommands() []map[string]interface{} {
	type builtin struct {
		name, description, argHint string
	}
	builtins := []builtin{
		{"init", "Create or refresh OpenCode project instructions", ""},
		{"help", "Show OpenCode help", ""},
		{"model", "Choose the active model", "<provider/model>"},
		{"undo", "Undo the previous change", ""},
		{"redo", "Redo the previous undone change", ""},
	}
	out := make([]map[string]interface{}, 0, len(builtins))
	for _, b := range builtins {
		out = append(out, slashCapabilityEntry(b.name, b.description, b.argHint, "builtin", "builtin", "", "opencode", "command", true, true))
	}
	return out
}

// collectCodexPromptCommands scans a `prompts/` root and emits one entry per
// .md file. The command name is the filename stem (Codex invokes these as
// `/name`); frontmatter supplies description and argument-hint when present.
func collectCodexPromptCommands(promptsDir string) []map[string]interface{} {
	entries, err := os.ReadDir(promptsDir)
	if err != nil {
		return nil
	}
	var out []map[string]interface{}
	for _, entry := range entries {
		if entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		if !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), ".md")
		if name == "" {
			continue
		}
		full := filepath.Join(promptsDir, entry.Name())
		fm, _ := parseSlashFrontmatter(full)
		out = append(out, slashCapabilityEntry(name, fm.description, fm.argumentHint, "user", "user", entry.Name(), "codex", "command", fm.isUserInvocable(), fm.isModelInvocable()))
	}
	return capSlashCommands(out)
}

// codexBuiltinCommands is a static baseline of Codex's built-in slash commands
// (the local TUI command surface). These are informational: Codex's headless
// app-server mode does not execute slash commands, so a selected command is
// forwarded as text. The list mirrors the official Codex CLI command set.
func codexBuiltinCommands() []map[string]interface{} {
	type builtin struct {
		name, description, argHint string
	}
	builtins := []builtin{
		{"model", "Choose the active model and reasoning effort", "<model>"},
		{"fast", "Toggle the Fast service tier", "[on|off|status]"},
		{"plan", "Switch to plan mode and optionally send a prompt", "[prompt]"},
		{"goal", "Set, view, pause, resume, or clear a task goal", "[pause|resume|clear|<objective>]"},
		{"review", "Ask Codex to review your working tree", ""},
		{"diff", "Show the Git diff including untracked files", ""},
		{"compact", "Summarize the visible conversation to free tokens", ""},
		{"status", "Display session configuration and token usage", ""},
		{"permissions", "Set what Codex can do without asking first", ""},
		{"approve", "Approve one retry of a recent auto-review denial", ""},
		{"clear", "Clear the terminal and start a fresh chat", ""},
		{"new", "Start a new conversation in the same CLI session", ""},
		{"fork", "Fork the current conversation into a new thread", ""},
		{"resume", "Resume a saved conversation from your session list", ""},
		{"mention", "Attach a file to the conversation", "<path>"},
		{"init", "Generate an AGENTS.md scaffold in the current directory", ""},
		{"mcp", "List configured MCP tools", "[verbose]"},
		{"personality", "Choose a communication style for responses", ""},
		{"skills", "Browse and use local skills", ""},
		{"memories", "Configure memory use and generation", ""},
		{"usage", "View account token usage", "[daily|weekly|cumulative]"},
		{"copy", "Copy the latest completed Codex output", ""},
		{"raw", "Toggle raw scrollback mode", "[on|off]"},
		{"side", "Start an ephemeral side conversation", "[prompt]"},
		{"agent", "Switch or inspect an agent thread", ""},
	}
	out := make([]map[string]interface{}, 0, len(builtins))
	for _, b := range builtins {
		out = append(out, slashCapabilityEntry(b.name, b.description, b.argHint, "builtin", "builtin", "", "codex", "command", true, true))
	}
	return out
}

// claudeBuiltinCommands is a static baseline of Claude Code's built-in slash
// commands. The headless CLI forwards slash tokens inside the prompt turn, so a
// selected builtin is sent as text and the CLI handles it in-turn (verified:
// /compact with --resume compacts the native session; /clear /model /help /cost
// are additionally intercepted agent-side by localSlashBuiltins before a model
// turn would spawn). The list mirrors the CLI's init-event command surface.
func claudeBuiltinCommands() []map[string]interface{} {
	type builtin struct {
		name, description, argHint string
	}
	builtins := []builtin{
		{"compact", "压缩会话上下文以释放 token（headless 下落成原生 CLI 回合）", "[instructions]"},
		{"clear", "清空当前会话上下文，下一轮开启全新会话", ""},
		{"model", "查看当前会话使用的模型（切换请在会话设置里进行）", ""},
		{"help", "列出本机命令与说明", ""},
		{"cost", "查看当前会话的 token 用量", ""},
		{"init", "让 Claude 分析项目并生成 CLAUDE.md", ""},
		{"review", "让 Claude 审查代码改动", "[target]"},
		{"mcp", "列出已连接的 MCP server 与工具", ""},
		{"context", "查看当前上下文窗口占用情况", ""},
		{"usage", "查看账号用量信息", ""},
	}
	out := make([]map[string]interface{}, 0, len(builtins))
	for _, b := range builtins {
		out = append(out, slashCapabilityEntry(b.name, b.description, b.argHint, "builtin", "builtin", "", "claude", "command", true, true))
	}
	return out
}

// collectAgentCoreSlashCommands surfaces the AliangCode (agentcore) kernel's
// slash-command surface. The kernel has no CLI of its own, so the baseline is:
//  1. the agent-local builtin set (localSlashBuiltins) — these are commands the
//     dispatcher actually intercepts without a model turn (/compact 特例：落成
//     内核 Compact() 真实压缩);
//  2. one /skill:<name> entry per skill the kernel will inject this session
//     (同一 agentCoreSkillsFor 数据源，含会话级开关语义——这里没有 session_id
//     上下文，不做会话级过滤，面板展示全量);
//  3. one /mcp:<server> entry per configured MCP server (agentCoreMCPRawServersIn
//     的合并结果：.claude.json + 插件 + 统一注册表，跳过 disabled 条目).
//
// 选中即插入 `/skill:<name> ` / `/mcp:<server> `，由模型回合解析（技能名进
// prompt，内核注入的技能清单里就有它；MCP server 名同理会引导模型用对应桥
// 工具）。全局总闸关闭时对应侧不出条目，面板与内核实际行为保持一致。
func collectAgentCoreSlashCommands(manager *agentAIManager) []map[string]interface{} {
	return collectAgentCoreSlashCommandsFor(agentHome(), manager)
}

func collectAgentCoreSlashCommandsFor(home string, manager *agentAIManager) []map[string]interface{} {
	type builtin struct {
		name, description, argHint string
	}
	builtins := []builtin{
		{"compact", "对当前会话历史做一次真实压缩（内核 Compact）", ""},
		{"clear", "清空当前会话上下文，下一轮从零开始", ""},
		{"model", "查看当前会话使用的模型（切换请在会话设置里进行）", ""},
		{"help", "列出本机命令与说明", ""},
		{"cost", "查看当前会话的 token 用量", ""},
	}
	out := make([]map[string]interface{}, 0, len(builtins)+8)
	for _, b := range builtins {
		out = append(out, slashCapabilityEntry(b.name, b.description, b.argHint, "builtin", "builtin", "", "agentcore", "command", true, true))
	}
	if manager != nil {
		if config.GetGlobalConfig().EffectiveAgentCoreSkillsEnabled() {
			for _, skill := range agentCoreSkillsFor(home, nil) {
				desc := strings.TrimSpace(skill.Description)
				if desc == "" {
					desc = "技能：" + skill.Name
				}
				out = append(out, slashCapabilityEntry("skill:"+skill.Name, desc, "", "builtin", "kernel", skill.Path, "agentcore", "skill", true, true))
			}
		}
		if config.GetGlobalConfig().EffectiveAgentCoreMCPEnabled() {
			for _, server := range agentCoreMCPRawServersIn(home) {
				desc := "MCP server：" + server.Name
				if len(server.IncludeTools) > 0 {
					desc += fmt.Sprintf("（tools: %s）", strings.Join(server.IncludeTools, ", "))
				}
				out = append(out, slashCapabilityEntry("mcp:"+server.Name, desc, "", "builtin", "kernel", server.Command, "agentcore", "mcp", true, true))
			}
		}
	}
	return out
}

// pluginRecordEnabled keeps user-scoped plugins and skips explicitly disabled
// ones. installed_plugins.json records installs, not enablement; the enabled
// flag (when present) comes from settings sync. Absent flag = treated enabled.
func pluginRecordEnabled(rec installedPluginRecord) bool {
	switch strings.ToLower(strings.TrimSpace(rec.Scope)) {
	case "user", "":
		return !rec.Disabled
	default:
		return false
	}
}

type installedPluginRecord struct {
	Scope       string `json:"scope"`
	ProjectPath string `json:"projectPath"`
	InstallPath string `json:"installPath"`
	Disabled    bool   `json:"disabled"`
}

type installedPluginsManifest struct {
	Plugins map[string][]installedPluginRecord `json:"plugins"`
}

// scanCommandMarkdowns walks a `commands/` root and emits one entry per .md.
// namePrefix namespaces plugin commands (e.g. "plugin-dev:"). provider tags the
// entry with the CLI whose command surface this belongs to ("claude"/"codex").
func scanCommandMarkdowns(root, scope, origin, sourceBase, namePrefix, provider string) []map[string]interface{} {
	var out []map[string]interface{}
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if path != root {
				name := d.Name()
				if strings.HasPrefix(name, ".") || name == "node_modules" {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if !strings.HasSuffix(path, ".md") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		name := commandNameFromRel(rel)
		if name == "" || strings.HasPrefix(name, ".") {
			return nil
		}
		fm, _ := parseSlashFrontmatter(path)
		out = append(out, slashCapabilityEntry(namePrefix+name, fm.description, fm.argumentHint, scope, origin, relSource(sourceBase, path), provider, "command", fm.isUserInvocable(), fm.isModelInvocable()))
		return nil
	})
	return capSlashCommands(out)
}

// scanSkillMarkdowns scans a `skills/` root for per-skill SKILL.md files.
func scanSkillMarkdowns(root, scope, origin, sourceBase, namePrefix, provider string) []map[string]interface{} {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var out []map[string]interface{}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		skillMd := filepath.Join(root, entry.Name(), "SKILL.md")
		if _, err := os.Stat(skillMd); err != nil {
			continue
		}
		fm, _ := parseSlashFrontmatter(skillMd)
		name := strings.TrimSpace(fm.name)
		if name == "" {
			name = entry.Name()
		}
		out = append(out, slashCapabilityEntry(namePrefix+name, fm.description, fm.argumentHint, scope, origin, relSource(sourceBase, skillMd), provider, "skill", fm.isUserInvocable(), fm.isModelInvocable()))
	}
	return capSlashCommands(out)
}

func slashCommandEntry(name, description, argHint, scope, origin, source, provider string) map[string]interface{} {
	return slashCapabilityEntry(name, description, argHint, scope, origin, source, provider, "command", true, true)
}

func slashCapabilityEntry(name, description, argHint, scope, origin, source, provider, kind string, userInvocable, modelInvocable bool) map[string]interface{} {
	entry := map[string]interface{}{
		"name":            name,
		"description":     description,
		"kind":            kind,
		"scope":           scope,
		"origin":          origin,
		"source":          source,
		"provider":        provider,
		"user_invocable":  userInvocable,
		"model_invocable": modelInvocable,
	}
	if strings.TrimSpace(argHint) != "" {
		entry["arg_hint"] = strings.TrimSpace(argHint)
	}
	return entry
}

// commandNameFromRel turns a path relative to a `commands/` root into the bare
// slash-command name: drop ".md", join segments with ".". Mirrors Claude Code's
// own derivation (commands/speckit.plan.md -> "speckit.plan",
// commands/foo/bar.md -> "foo.bar").
func commandNameFromRel(rel string) string {
	name := strings.TrimSuffix(rel, ".md")
	name = filepath.ToSlash(name)
	name = strings.ReplaceAll(name, "/", ".")
	return strings.TrimSpace(name)
}

func relSource(base, path string) string {
	rel, err := filepath.Rel(base, path)
	if err != nil || rel == "" {
		return filepath.ToSlash(path)
	}
	return filepath.ToSlash(rel)
}

func capSlashCommands(in []map[string]interface{}) []map[string]interface{} {
	if len(in) > slashCommandMaxPerScope {
		return in[:slashCommandMaxPerScope]
	}
	return in
}

// pluginShortName extracts the plugin name from a manifest key like
// "plugin-dev@claude-plugins-official" -> "plugin-dev".
func pluginShortName(key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return ""
	}
	idx := strings.Index(key, "@")
	if idx < 0 {
		return key
	}
	if idx == 0 {
		return ""
	}
	return key[:idx]
}

type slashFrontmatter struct {
	description            string
	argumentHint           string
	name                   string
	userInvocable          *bool
	disableModelInvocation bool
	context                string
	agent                  string
	tools                  string
}

func (fm slashFrontmatter) isUserInvocable() bool {
	return fm.userInvocable == nil || *fm.userInvocable
}

func (fm slashFrontmatter) isModelInvocable() bool {
	return !fm.disableModelInvocation
}

// parseSlashFrontmatter reads a markdown file and extracts the handful of
// frontmatter fields we care about (description, argument-hint, name). It is
// intentionally minimal: single-line `key: value` pairs between the leading
// "---" fences. ok is false only when the file cannot be read; a file without
// frontmatter yields a zero value with ok=true.
func parseSlashFrontmatter(path string) (slashFrontmatter, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return slashFrontmatter{}, false
	}
	text := strings.TrimPrefix(string(data), "\uFEFF")
	lines := strings.Split(text, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return slashFrontmatter{}, true
	}
	var fm slashFrontmatter
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			break
		}
		key, value, ok := splitFrontmatterField(lines[i])
		if !ok {
			continue
		}
		switch key {
		case "description":
			fm.description = value
		case "argument-hint", "argument_hint":
			fm.argumentHint = value
		case "name":
			fm.name = value
		case "user-invocable", "user_invocable":
			if parsed, ok := parseSlashFrontmatterBool(value); ok {
				fm.userInvocable = &parsed
			}
		case "disable-model-invocation", "disable_model_invocation":
			if parsed, ok := parseSlashFrontmatterBool(value); ok {
				fm.disableModelInvocation = parsed
			}
		case "context":
			fm.context = value
		case "agent":
			fm.agent = value
		case "tools":
			// Only single-line `tools: a, b` is captured; block-style YAML
			// lists (`tools:\n  - Bash`) are not, so they get dropped on the
			// agent sanitize path. Dropping tools WIDENS the agent's toolset
			// (absence inherits the full toolset); this is bounded downstream
			// by session permission/approval gating and deemed acceptable.
			fm.tools = value
		}
	}
	return fm, true
}

func parseSlashFrontmatterBool(value string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true", "yes", "1":
		return true, true
	case "false", "no", "0":
		return false, true
	default:
		return false, false
	}
}

func splitFrontmatterField(line string) (string, string, bool) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "-") {
		return "", "", false
	}
	idx := strings.Index(trimmed, ":")
	if idx <= 0 {
		return "", "", false
	}
	key := strings.ToLower(strings.TrimSpace(trimmed[:idx]))
	value := strings.TrimSpace(trimmed[idx+1:])
	if len(value) >= 2 {
		if (value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'') {
			value = value[1 : len(value)-1]
		}
	}
	return key, value, true
}

func sortSlashCommands(cmds []map[string]interface{}) {
	sort.SliceStable(cmds, func(i, j int) bool {
		oi, _ := cmds[i]["origin"].(string)
		oj, _ := cmds[j]["origin"].(string)
		if oi != oj {
			return slashOriginOrder(oi) < slashOriginOrder(oj)
		}
		ni, _ := cmds[i]["name"].(string)
		nj, _ := cmds[j]["name"].(string)
		return ni < nj
	})
}

func slashOriginOrder(origin string) int {
	switch origin {
	case "project":
		return 0
	case "user":
		return 1
	case "plugin":
		return 2
	case "builtin":
		return 3
	}
	return 9
}

// remoteBool reads a bool-ish field from a remote agent message, accepting JSON
// bool, "true"/"1"/"yes" strings, or numbers. Missing/empty -> fallback.
func remoteBool(msg map[string]interface{}, key string, fallback bool) bool {
	raw, ok := msg[key]
	if !ok || raw == nil {
		return fallback
	}
	switch v := raw.(type) {
	case bool:
		return v
	case string:
		s := strings.ToLower(strings.TrimSpace(v))
		if s == "" {
			return fallback
		}
		return s == "true" || s == "1" || s == "yes"
	case float64:
		return v != 0
	}
	return fallback
}
