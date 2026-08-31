package services

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"aliang.one/nursorgate/common/logger"
)

const claudeProjectCapabilityLimit = 500

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

// withClaudeRemotePolicy applies the remote trust tier to a claude tool:
// per-tier --setting-sources, the sanitized capability plugin for the
// sanitized tier, and the explicit MCP merge when the server marked project
// MCP trusted. On plugin/MCP preparation failure it degrades the run to the
// isolated tier (fail-closed) and returns a structured policy notice instead
// of failing the run (spec §3/§4.1).
func withClaudeRemotePolicy(tool *agentAITool, run agentAIRun) (*agentAITool, func(), map[string]interface{}) {
	cleanup := func() {}
	if tool == nil || !run.claudePolicy.enabled {
		return tool, cleanup, nil
	}
	copied := *tool
	copied.args = append([]string(nil), tool.args...)
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

// claudeTierMCPArgs builds --strict-mcp-config/--mcp-config flags for the
// sanitized tier when the server marked the project MCP trusted (spec §5).
// Placeholder implementation in this task; replaced in the MCP merge task.
func claudeTierMCPArgs(policy agentAIClaudeRemotePolicy, projectPath string) ([]string, func(), error) {
	return nil, func() {}, nil
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
	manifest := []byte(`{"name":"aliang-project","version":"1.0.0","description":"Sanitized remote project capabilities"}`)
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
		if count >= claudeProjectCapabilityLimit || !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
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
		markdown, err := sanitizedClaudeMarkdown(sourceMarkdown, true)
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
		if count >= claudeProjectCapabilityLimit || !strings.HasSuffix(strings.ToLower(entry.Name()), ".md") {
			return nil
		}
		rel, err := filepath.Rel(sourceRoot, path)
		if err != nil || strings.HasPrefix(rel, "..") {
			return fmt.Errorf("invalid Claude command path %q", path)
		}
		markdown, err := sanitizedClaudeMarkdown(path, false)
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

// sanitizedClaudeMarkdown preserves the prompt body and safe discovery fields,
// but deliberately drops hooks, allowed-tools, context, agent, and arbitrary
// frontmatter that could alter the remote execution boundary.
func sanitizedClaudeMarkdown(path string, skill bool) ([]byte, error) {
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
	if skill {
		writeYAMLString("name", fm.name)
	}
	writeYAMLString("description", fm.description)
	writeYAMLString("argument-hint", fm.argumentHint)
	if !fm.isUserInvocable() {
		out.WriteString("user-invocable: false\n")
	}
	if !fm.isModelInvocable() {
		out.WriteString("disable-model-invocation: true\n")
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
