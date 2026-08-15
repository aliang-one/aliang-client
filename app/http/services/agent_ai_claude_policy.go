package services

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const claudeProjectCapabilityLimit = 500

// withClaudeRemotePolicy isolates remote Claude runs from project/local
// settings. Explicitly trusted project commands and Skills are re-exposed via a
// temporary plugin containing sanitized markdown and no project hooks.
func withClaudeRemotePolicy(tool *agentAITool, run agentAIRun) (*agentAITool, func(), error) {
	cleanup := func() {}
	if tool == nil || !run.claudePolicy.enabled {
		return tool, cleanup, nil
	}
	copied := *tool
	copied.args = append([]string(nil), tool.args...)
	sources := strings.Join(run.claudePolicy.settingSources, ",")
	flags := []string{"--setting-sources", sources}
	if run.claudePolicy.projectSkillTrusted && run.claudePolicy.projectCapabilityMode == "sanitized_plugin" {
		pluginDir, err := prepareClaudeProjectCapabilityPlugin(run.projectPath)
		if err != nil {
			return nil, cleanup, err
		}
		cleanup = func() { _ = os.RemoveAll(pluginDir) }
		flags = append(flags, "--plugin-dir", pluginDir)
	}
	copied.args = append(flags, copied.args...)
	return &copied, cleanup, nil
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
