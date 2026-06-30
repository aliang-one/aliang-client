package services

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"aliang.one/nursorgate/app/http/models"
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
// roots for the OpenCode provider. It returns the `/`-command surface as
// AgentCommandInfo-like entries, each tagged with the provider it belongs to.
// Builtins are intentionally NOT reported for Claude — the cloud owns that
// static builtin baseline; the agent is the source of truth only for custom
// commands. For Codex the builtins are surfaced as a convenience baseline
// because Codex's headless app-server does not enumerate them itself.
//
// The optional `provider` field selects which provider's commands to return
// ("claude"/"claudecode", "codex", or "opencode"). When absent, all providers'
// commands are returned tagged, so the client can filter by the active
// conversation.
func agentSlashCommandsListPayload(msg map[string]interface{}) map[string]interface{} {
	requestID := remoteString(msg, "request_id")
	projectPath, err := resolveAgentProjectPath(remoteString(msg, "project_path"))
	if err != nil {
		return agentSlashCommandsErrorPayload(requestID, err)
	}
	provider := normalizeSlashProvider(remoteString(msg, "provider"))
	includeUser := remoteBool(msg, "include_user_level", true)
	includePlugins := remoteBool(msg, "include_plugins", true)

	wantClaude := provider == "" || provider == "claude"
	wantCodex := provider == "" || provider == "codex"
	wantOpenCode := provider == "" || provider == "opencode"

	var commands []map[string]interface{}
	if wantClaude {
		commands = append(commands, collectProjectSlashCommands(projectPath)...)
		if includeUser {
			commands = append(commands, collectUserSlashCommands()...)
		}
		if includePlugins {
			commands = append(commands, collectPluginSlashCommands()...)
		}
	}
	if wantCodex {
		commands = append(commands, collectCodexSlashCommands()...)
	}
	if wantOpenCode {
		commands = append(commands, collectOpenCodeSlashCommands(projectPath, includeUser)...)
	}
	sortSlashCommands(commands)

	return map[string]interface{}{
		"type":         models.AgentEventSlashCommandsListResult,
		"request_id":   requestID,
		"project_path": projectPath,
		"provider":     provider,
		"commands":     commands,
		"generated_at": time.Now().UTC().Format(time.RFC3339),
	}
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
func collectProjectSlashCommands(projectPath string) []map[string]interface{} {
	dotClaude := filepath.Join(projectPath, ".claude")
	var out []map[string]interface{}
	out = append(out, scanCommandMarkdowns(filepath.Join(dotClaude, "commands"), "project", "project", projectPath, "", "claude")...)
	out = append(out, scanSkillMarkdowns(filepath.Join(dotClaude, "skills"), "project", "project", projectPath, "", "claude")...)
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
			out = append(out, scanCommandMarkdowns(filepath.Join(installPath, "commands"), "user", "plugin", installPath, prefix, "claude")...)
			out = append(out, scanSkillMarkdowns(filepath.Join(installPath, "skills"), "user", "plugin", installPath, prefix, "claude")...)
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
		out = append(out, slashCommandEntry(b.name, b.description, b.argHint, "builtin", "builtin", "", "opencode"))
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
		out = append(out, slashCommandEntry(name, fm.description, fm.argumentHint, "user", "user", entry.Name(), "codex"))
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
		out = append(out, slashCommandEntry(b.name, b.description, b.argHint, "builtin", "builtin", "", "codex"))
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
		out = append(out, slashCommandEntry(namePrefix+name, fm.description, fm.argumentHint, scope, origin, relSource(sourceBase, path), provider))
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
		out = append(out, slashCommandEntry(namePrefix+name, fm.description, "", scope, origin, relSource(sourceBase, skillMd), provider))
	}
	return capSlashCommands(out)
}

func slashCommandEntry(name, description, argHint, scope, origin, source, provider string) map[string]interface{} {
	entry := map[string]interface{}{
		"name":        name,
		"description": description,
		"scope":       scope,
		"origin":      origin,
		"source":      source,
		"provider":    provider,
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
	description  string
	argumentHint string
	name         string
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
		}
	}
	return fm, true
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
