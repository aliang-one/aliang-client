package services

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testClaudeRemotePolicy(trusted bool) map[string]interface{} {
	mode := "disabled"
	if trusted {
		mode = "sanitized_plugin"
	}
	return map[string]interface{}{
		"require_system_init_verification": true,
		"project_skill_trusted":            trusted,
		"project_capability_mode":          mode,
		"setting_sources":                  []interface{}{"user", "project", "local"},
		"settings": map[string]interface{}{
			"disableSkillShellExecution": true,
			"permissions": map[string]interface{}{
				"ask": []interface{}{"Bash", "Edit", "mcp__*"},
			},
		},
	}
}

func TestParseAgentAIClaudeRemotePolicyForcesUserSettings(t *testing.T) {
	policy := parseAgentAIClaudeRemotePolicy(map[string]interface{}{
		"claude_remote_policy": testClaudeRemotePolicy(true),
	})
	if !policy.enabled || !policy.projectSkillTrusted || policy.projectCapabilityMode != "sanitized_plugin" {
		t.Fatalf("unexpected policy: %+v", policy)
	}
	if len(policy.settingSources) != 1 || policy.settingSources[0] != "user" {
		t.Fatalf("setting sources = %v, want user only", policy.settingSources)
	}
}

func TestClaudeRemotePolicyBuildsSanitizedProjectPlugin(t *testing.T) {
	project := t.TempDir()
	skillDir := filepath.Join(project, ".claude", "skills", "deploy")
	if err := os.MkdirAll(filepath.Join(skillDir, "references"), 0o755); err != nil {
		t.Fatal(err)
	}
	skill := "---\nname: deploy\ndescription: Deploy safely\nargument-hint: <env>\nallowed-tools: Bash\nhooks:\n  PreToolUse: []\nuser-invocable: false\n---\nRun $ARGUMENTS.\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skill), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "references", "runbook.md"), []byte("runbook"), 0o644); err != nil {
		t.Fatal(err)
	}
	commandDir := filepath.Join(project, ".claude", "commands")
	if err := os.MkdirAll(commandDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(commandDir, "ship.md"), []byte("---\ndescription: Ship\nallowed-tools: Bash\ncontext: fork\n---\nShip $ARGUMENTS.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	run := agentAIRun{
		projectPath:  project,
		claudePolicy: parseAgentAIClaudeRemotePolicy(map[string]interface{}{"claude_remote_policy": testClaudeRemotePolicy(true)}),
	}
	tool, cleanup, err := withClaudeRemotePolicy(&agentAITool{args: []string{"--print", "prompt"}}, run)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	pluginDir := argumentValue(tool.args, "--plugin-dir")
	if pluginDir == "" {
		t.Fatalf("missing --plugin-dir in %v", tool.args)
	}
	if got := argumentValue(tool.args, "--setting-sources"); got != "user" {
		t.Fatalf("setting sources = %q, want user", got)
	}
	sanitized, err := os.ReadFile(filepath.Join(pluginDir, "skills", "deploy", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(sanitized)
	for _, forbidden := range []string{"allowed-tools", "hooks:", "context:"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("sanitized skill retained %q:\n%s", forbidden, text)
		}
	}
	for _, wanted := range []string{"description: \"Deploy safely\"", "user-invocable: false", "Run $ARGUMENTS."} {
		if !strings.Contains(text, wanted) {
			t.Fatalf("sanitized skill missing %q:\n%s", wanted, text)
		}
	}
	if info, err := os.Lstat(filepath.Join(pluginDir, "skills", "deploy", "references")); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("skill resource should be a symlink: info=%v err=%v", info, err)
	}
	command, err := os.ReadFile(filepath.Join(pluginDir, "commands", "ship.md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(command), "allowed-tools") || strings.Contains(string(command), "context:") {
		t.Fatalf("unsafe command frontmatter retained:\n%s", command)
	}
}

func TestClaudeApprovalSettingsPreserveRemoteAskRules(t *testing.T) {
	run := agentAIRun{
		sessionID:     "s1",
		messageID:     "m1",
		approvalToken: "token",
		claudePolicy:  parseAgentAIClaudeRemotePolicy(map[string]interface{}{"claude_remote_policy": testClaudeRemotePolicy(false)}),
	}
	settings, err := claudeApprovalHookSettings(claudeApprovalHookPermissionRequestHTTP, run)
	if err != nil {
		t.Fatal(err)
	}
	if settings["disableSkillShellExecution"] != true {
		t.Fatalf("disableSkillShellExecution = %v", settings["disableSkillShellExecution"])
	}
	if _, exists := settings["disableAllHooks"]; exists {
		t.Fatal("disableAllHooks would also disable the Agent approval hook")
	}
	permissions := settings["permissions"].(map[string]interface{})
	ask := permissions["ask"].([]string)
	if strings.Join(ask, ",") != "Bash,Edit,mcp__*" {
		t.Fatalf("ask = %v", ask)
	}
}

func TestClaudeSystemInitCreatesSessionCapabilitySnapshot(t *testing.T) {
	manager := newAgentAIManager()
	project := t.TempDir()
	manager.sessions["s1"] = &agentAISession{id: "s1", projectPath: project}
	run := agentAIRun{sessionID: "s1", projectPath: project}
	run.onClaudeInit = func(commands []string, version string) {
		manager.recordClaudeCapabilities(run.sessionID, run.projectPath, commands, version)
	}
	input := `{"type":"system","subtype":"init","slash_commands":["/review","deploy"],"claude_code_version":"2.1.17"}`
	streamStructuredAIDelta(strings.NewReader(input), agentAIOutputClaudeStreamJSON, run, func(interface{}) error {
		return nil
	}, &agentAIOutputLimiter{}, nil, nil, nil, nil)
	caps, ok := manager.claudeCapabilities("s1", project)
	if !ok || caps.version != "2.1.17" || caps.generation == "" {
		t.Fatalf("capabilities = %+v, ok=%v", caps, ok)
	}
	for _, name := range []string{"review", "deploy"} {
		if _, exists := caps.commands[name]; !exists {
			t.Fatalf("missing normalized capability %q in %v", name, caps.commands)
		}
	}
}

func TestSlashCommandsRequireTrustAndSystemInitForProjectSkills(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	project := t.TempDir()
	skillDir := filepath.Join(project, ".claude", "skills", "deploy")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: deploy\ndescription: Deploy\n---\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	manager := newAgentAIManager()
	manager.sessions["s1"] = &agentAISession{id: "s1", projectPath: project}
	manager.recordClaudeCapabilities("s1", project, []string{"aliang-project:deploy"}, "2.1.17")

	msg := map[string]interface{}{
		"request_id":           "r1",
		"session_id":           "s1",
		"project_path":         project,
		"provider":             "claude",
		"include_user_level":   false,
		"include_plugins":      false,
		"claude_remote_policy": testClaudeRemotePolicy(false),
	}
	untrusted := agentSlashCommandsListPayloadWithManager(msg, manager)
	if commands := untrusted["commands"].([]map[string]interface{}); len(commands) != 0 {
		t.Fatalf("untrusted project leaked capabilities: %+v", commands)
	}

	msg["claude_remote_policy"] = testClaudeRemotePolicy(true)
	trusted := agentSlashCommandsListPayloadWithManager(msg, manager)
	if trusted["verified"] != true || trusted["claude_version"] != "2.1.17" {
		t.Fatalf("verification metadata = %+v", trusted)
	}
	commands := trusted["commands"].([]map[string]interface{})
	if len(commands) != 1 || commands[0]["name"] != "deploy" || commands[0]["kind"] != "skill" {
		t.Fatalf("trusted commands = %+v", commands)
	}
}

func argumentValue(args []string, key string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == key {
			return args[i+1]
		}
	}
	return ""
}
