package services

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func TestParseAgentAIClaudeRemotePolicyDisablesFilesystemSettings(t *testing.T) {
	policy := parseAgentAIClaudeRemotePolicy(map[string]interface{}{
		"claude_remote_policy": testClaudeRemotePolicy(true),
	})
	if !policy.enabled || !policy.projectSkillTrusted || policy.projectCapabilityMode != "sanitized_plugin" {
		t.Fatalf("unexpected policy: %+v", policy)
	}
	if len(policy.settingSources) != 0 {
		t.Fatalf("setting sources = %v, want none", policy.settingSources)
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
	tool, cleanup, notice := withClaudeRemotePolicy(&agentAITool{args: []string{"--print", "prompt"}}, run)
	if notice != nil {
		t.Fatalf("tier notice = %v", notice)
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
	for _, forbidden := range []string{"allowed-tools", "hooks:"} {
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
	if strings.Contains(string(command), "allowed-tools") {
		t.Fatalf("unsafe command frontmatter retained:\n%s", command)
	}
	if !strings.Contains(string(command), "context: \"fork\"") {
		t.Fatalf("sanitized command lost context frontmatter:\n%s", command)
	}
}

func TestSanitizedClaudeMarkdownPreservesContextAndAgent(t *testing.T) {
	dir := t.TempDir()
	skill := filepath.Join(dir, "SKILL.md")
	body := "---\nname: deploy\ndescription: Deploy safely\ncontext: fork\nagent: reviewer\nallowed-tools: Bash\nmodel: opus\nhooks:\n  PreToolUse: []\n---\nRun it.\n"
	if err := os.WriteFile(skill, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := sanitizedClaudeMarkdown(skill, claudeSanitizeSkill)
	if err != nil {
		t.Fatal(err)
	}
	text := string(out)
	for _, forbidden := range []string{"allowed-tools", "hooks:", "model:"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("sanitized skill retained %q:\n%s", forbidden, text)
		}
	}
	for _, wanted := range []string{"context: \"fork\"", "agent: \"reviewer\"", "description: \"Deploy safely\"", "Run it."} {
		if !strings.Contains(text, wanted) {
			t.Fatalf("sanitized skill missing %q:\n%s", wanted, text)
		}
	}
}

func TestCopySanitizedClaudeAgents(t *testing.T) {
	source := filepath.Join(t.TempDir(), "agents")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	agent := "---\nname: reviewer\ndescription: Reviews code\ntools: Bash, Read\nallowed-tools: Bash\nhooks:\n  Stop: []\nmodel: opus\n---\nReview carefully.\n"
	if err := os.WriteFile(filepath.Join(source, "reviewer.md"), []byte(agent), 0o644); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "out")
	if err := copySanitizedClaudeAgents(source, target); err != nil {
		t.Fatal(err)
	}
	out, err := os.ReadFile(filepath.Join(target, "reviewer.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(out)
	for _, forbidden := range []string{"allowed-tools", "hooks:", "model:"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("sanitized agent retained %q:\n%s", forbidden, text)
		}
	}
	for _, wanted := range []string{"name: \"reviewer\"", "tools: \"Bash, Read\"", "Review carefully."} {
		if !strings.Contains(text, wanted) {
			t.Fatalf("sanitized agent missing %q:\n%s", wanted, text)
		}
	}
}

func TestCopySanitizedClaudeAgentsSkipsSymlinkedAgentFiles(t *testing.T) {
	source := filepath.Join(t.TempDir(), "agents")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	agent := "---\nname: reviewer\ndescription: Reviews code\n---\nReview carefully.\n"
	if err := os.WriteFile(filepath.Join(source, "real.md"), []byte(agent), 0o644); err != nil {
		t.Fatal(err)
	}
	// The symlink target lives outside the agents source dir: a naive copy
	// that follows symlinks would pull this file's content into the plugin.
	escaped := filepath.Join(t.TempDir(), "escaped.md")
	if err := os.WriteFile(escaped, []byte("---\nname: leaked\n---\nescaped agent body\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(escaped, filepath.Join(source, "link.md")); err != nil {
		t.Fatalf("symlink setup failed: %v", err)
	}
	target := filepath.Join(t.TempDir(), "out")
	if err := copySanitizedClaudeAgents(source, target); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(target, "link.md")); !os.IsNotExist(err) {
		t.Fatalf("symlinked agent leaked into sanitized plugin: err=%v", err)
	}
	out, err := os.ReadFile(filepath.Join(target, "real.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "Review carefully.") {
		t.Fatalf("real agent missing body:\n%s", out)
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

func TestClaudeApprovalSettingsLegacyOmitsRemoteAskRules(t *testing.T) {
	run := agentAIRun{
		sessionID:     "s1",
		messageID:     "m1",
		approvalToken: "token",
		claudePolicy:  parseAgentAIClaudeRemotePolicy(map[string]interface{}{"claude_remote_policy": testClaudeRemotePolicy(false)}),
	}
	settings, err := claudeApprovalHookSettings(claudeApprovalHookPreToolUseCommand, run)
	if err != nil {
		t.Fatal(err)
	}
	if settings["disableSkillShellExecution"] != true {
		t.Fatalf("disableSkillShellExecution = %v", settings["disableSkillShellExecution"])
	}
	if _, exists := settings["permissions"]; exists {
		t.Fatalf("legacy settings must not combine explicit ask rules with PreToolUse: %v", settings["permissions"])
	}
}

func TestClaudeApprovalHookTimeoutCoversAgentApprovalWindow(t *testing.T) {
	if got, want := claudeApprovalHookTimeoutSeconds(1500*time.Millisecond), int64(32); got != want {
		t.Fatalf("hook timeout = %d, want %d", got, want)
	}
	settings, err := claudeApprovalHookSettings(claudeApprovalHookPermissionRequestHTTP, agentAIRun{
		sessionID: "s-timeout", messageID: "m-timeout", approvalToken: "token",
	})
	if err != nil {
		t.Fatal(err)
	}
	hooks := settings["hooks"].(map[string]interface{})["PermissionRequest"].([]interface{})
	handler := hooks[0].(map[string]interface{})["hooks"].([]interface{})[0].(map[string]interface{})
	if got, want := handler["timeout"], claudeApprovalHookTimeoutSeconds(agentAIApprovalTimeout); got != want {
		t.Fatalf("configured hook timeout = %v, want %v", got, want)
	}
}

func TestRequestApprovalCancellationEmitsTerminalEvent(t *testing.T) {
	manager := newAgentAIManager()
	defer manager.closeAll()
	mu, events, writer := captureAIWriter(t)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := manager.requestApproval(ctx, agentAIRun{
			sessionID: "s-cancel", messageID: "m-cancel", runSeq: 1, provider: "claude", activity: newAgentAIActivity(),
		}, writer, agentAIApprovalRequest{ID: "ap-cancel", Kind: "tool"})
		result <- err
	}()
	waitForAgentEvent(t, mu, events, "ai.approval.request", func(event map[string]interface{}) bool {
		return remoteString(event, "approval_id") == "ap-cancel" && remoteString(event, "status") == "pending"
	})
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("requestApproval error = %v, want context canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("requestApproval did not stop after cancellation")
	}
	cancelled := lastAIEvent(mu, events, "ai.approval.cancelled")
	if cancelled == nil || remoteString(cancelled, "reason") != "run_cancelled" {
		t.Fatalf("cancelled event = %#v, want run_cancelled", cancelled)
	}
	ids, _ := cancelled["approval_ids"].([]string)
	if len(ids) != 1 || ids[0] != "ap-cancel" {
		t.Fatalf("cancelled approval_ids = %#v", cancelled["approval_ids"])
	}
}

func TestExecutableProbeCacheKeyRefreshesAfterExecutableUpdate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(path, []byte("old executable"), 0o700); err != nil {
		t.Fatal(err)
	}
	before := executableProbeCacheKey(path)
	if err := os.WriteFile(path, []byte("new executable with different size"), 0o700); err != nil {
		t.Fatal(err)
	}
	after := executableProbeCacheKey(path)
	if before == after {
		t.Fatalf("executable cache key stayed stale: %q", before)
	}
}

func TestAgentAICapabilitiesOnlyAdvertiseApprovalCapableProviders(t *testing.T) {
	caps := agentAICapabilitiesForTools(true, false, false, true, false)
	for _, want := range []string{"ai_provider_claude", "ai_provider_claudecode", "ai_provider_opencode_basic"} {
		if !agentAIStringSliceContains(caps, want) {
			t.Fatalf("capabilities %v missing %s", caps, want)
		}
	}
	for _, forbidden := range []string{"ai_provider_codex", "ai_provider_codex_app_server", "ai_provider_opencode"} {
		if agentAIStringSliceContains(caps, forbidden) {
			t.Fatalf("capabilities %v unexpectedly contain %s", caps, forbidden)
		}
	}
	withCodex := agentAICapabilitiesForTools(false, false, true, false, true)
	for _, want := range []string{"ai_provider_codex", "ai_provider_codex_app_server"} {
		if !agentAIStringSliceContains(withCodex, want) {
			t.Fatalf("capabilities %v missing %s", withCodex, want)
		}
	}
	// goal_codex_native_v1 is intentionally NOT advertised: native goal support
	// for codex hasn't passed consistency verification.
	if agentAIStringSliceContains(withCodex, "goal_codex_native_v1") {
		t.Fatalf("capabilities %v unexpectedly contain goal_codex_native_v1", withCodex)
	}
}

func TestResolveAgentAIToolRejectsOpenCodeWithoutApprovalBridge(t *testing.T) {
	if _, err := resolveAgentAITool("edit a file", "opencode", "", "", ""); err == nil || !strings.Contains(err.Error(), "approval bridge") {
		t.Fatalf("resolveAgentAITool(opencode) error = %v", err)
	}
	if err := validateApprovalCapableProvider("codex", false); err == nil || !strings.Contains(err.Error(), "app-server") {
		t.Fatalf("validateApprovalCapableProvider(codex) error = %v", err)
	}
}

func TestClaudeApprovalHookDisablesFilesystemSettingsWithoutRemotePolicy(t *testing.T) {
	tool := withClaudeApprovalHook(&agentAITool{
		path: "/bin/claude",
		args: []string{"--setting-sources", "user", "--print", "prompt"},
	}, agentAIRun{sessionID: "s-isolated", messageID: "m-isolated", runSeq: 1, approvalToken: "token"})
	if got := argumentValue(tool.args, "--setting-sources"); got != "" {
		t.Fatalf("setting sources = %q, want none", got)
	}
	if strings.Count(strings.Join(tool.args, "\x00"), "--setting-sources") != 1 {
		t.Fatalf("setting sources flag was not normalized: %v", tool.args)
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

func TestParseAgentAIClaudeRemotePolicyTrustLevels(t *testing.T) {
	parse := func(raw map[string]interface{}) agentAIClaudeRemotePolicy {
		return parseAgentAIClaudeRemotePolicy(map[string]interface{}{"claude_remote_policy": raw})
	}
	if p := parse(map[string]interface{}{"trust_level": "full"}); p.trustTier != "full" {
		t.Fatalf("full tier = %q", p.trustTier)
	}
	if p := parse(map[string]interface{}{"trust_level": "full"}); len(p.permissionAsk) != 0 {
		t.Fatalf("full tier must not fill default ask list: %v", p.permissionAsk)
	}
	if p := parse(map[string]interface{}{"trust_level": "Sanitized"}); p.trustTier != "sanitized" {
		t.Fatalf("case-insensitive tier = %q", p.trustTier)
	}
	if p := parse(map[string]interface{}{"trust_level": "banana"}); p.trustTier != "isolated" {
		t.Fatalf("invalid tier must fail closed, got %q", p.trustTier)
	}
	if p := parse(map[string]interface{}{}); p.trustTier != "isolated" {
		t.Fatalf("absent tier must default isolated, got %q", p.trustTier)
	}
	if p := parse(map[string]interface{}{"trust_level": "sanitized", "project_mcp_trusted": true}); !p.projectMCPTrusted {
		t.Fatal("project_mcp_trusted not parsed")
	}
	if p := parse(testClaudeRemotePolicy(true)); p.trustTier != "sanitized" {
		t.Fatalf("legacy trusted mapping = %q, want sanitized", p.trustTier)
	}
	if p := parse(testClaudeRemotePolicy(false)); p.trustTier != "isolated" {
		t.Fatalf("legacy untrusted mapping = %q, want isolated", p.trustTier)
	}
	if p := parse(map[string]interface{}{"project_skill_trusted": true, "project_capability_mode": "disabled"}); p.trustTier != "isolated" {
		t.Fatalf("legacy trusted+bad mode = %q, want isolated", p.trustTier)
	}
	if p := parse(map[string]interface{}{"trust_level": "full", "project_skill_trusted": false, "project_capability_mode": "disabled"}); p.trustTier != "full" {
		t.Fatalf("trust_level must supersede legacy fields, got %q", p.trustTier)
	}
	if p := parse(map[string]interface{}{"project_skill_trusted": true, "project_capability_mode": "sanitized_plugin", "trust_level": "isolated"}); p.trustTier != "isolated" {
		t.Fatalf("trust_level must supersede legacy trusted mapping downward, got %q", p.trustTier)
	}
	if p := parse(map[string]interface{}{"trust_level": "   "}); p.trustTier != "isolated" {
		t.Fatalf("whitespace-only trust_level must take legacy route → isolated, got %q", p.trustTier)
	}
	if p := parse(map[string]interface{}{}); p.projectMCPTrusted {
		t.Fatal("project_mcp_trusted must default false")
	}
}

func TestClaudeTierSettingSources(t *testing.T) {
	cases := map[string]string{"isolated": "", "sanitized": "user", "full": "user,project", "": "", "bogus": ""}
	for tier, want := range cases {
		if got := claudeTierSettingSources(tier); got != want {
			t.Fatalf("claudeTierSettingSources(%q) = %q, want %q", tier, got, want)
		}
	}
}

func TestClaudeRemotePolicyTierFlags(t *testing.T) {
	project := t.TempDir()
	mkRun := func(raw map[string]interface{}) agentAIRun {
		return agentAIRun{projectPath: project, claudePolicy: parseAgentAIClaudeRemotePolicy(map[string]interface{}{"claude_remote_policy": raw})}
	}
	base := &agentAITool{args: []string{"--print", "prompt"}}

	tool, cleanup, notice := withClaudeRemotePolicy(base, mkRun(map[string]interface{}{"trust_level": "full"}))
	defer cleanup()
	if notice != nil {
		t.Fatalf("full tier returned notice: %v", notice)
	}
	if got := argumentValue(tool.args, "--setting-sources"); got != "user,project" {
		t.Fatalf("full setting sources = %q", got)
	}
	if argumentValue(tool.args, "--plugin-dir") != "" {
		t.Fatalf("full tier must not build plugin: %v", tool.args)
	}

	tool, cleanup, notice = withClaudeRemotePolicy(base, mkRun(map[string]interface{}{"trust_level": "sanitized"}))
	defer cleanup()
	if got := argumentValue(tool.args, "--setting-sources"); got != "user" {
		t.Fatalf("sanitized setting sources = %q", got)
	}
	if argumentValue(tool.args, "--plugin-dir") == "" {
		t.Fatalf("sanitized tier missing --plugin-dir: %v", tool.args)
	}

	tool, cleanup, _ = withClaudeRemotePolicy(base, mkRun(map[string]interface{}{}))
	defer cleanup()
	if got := argumentValue(tool.args, "--setting-sources"); got != "" {
		t.Fatalf("isolated setting sources = %q", got)
	}
	if argumentValue(tool.args, "--plugin-dir") != "" {
		t.Fatalf("isolated tier must not build plugin: %v", tool.args)
	}
}

func TestClaudeRemotePolicySanitizeFailureDegradesToIsolated(t *testing.T) {
	// projectPath points at a regular FILE so plugin preparation fails.
	file := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	run := agentAIRun{
		projectPath: file,
		claudePolicy: parseAgentAIClaudeRemotePolicy(map[string]interface{}{
			"claude_remote_policy": map[string]interface{}{"trust_level": "sanitized"},
		}),
	}
	tool, cleanup, notice := withClaudeRemotePolicy(&agentAITool{args: []string{"--print", "prompt"}}, run)
	defer cleanup()
	if notice == nil || notice["reason"] != "sanitize_failed" || notice["effective"] != "isolated" {
		t.Fatalf("notice = %v, want sanitize_failed/isolated", notice)
	}
	if got := argumentValue(tool.args, "--setting-sources"); got != "" {
		t.Fatalf("degraded setting sources = %q, want empty", got)
	}
	if argumentValue(tool.args, "--plugin-dir") != "" {
		t.Fatalf("degraded run must not carry plugin: %v", tool.args)
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

func TestApplyClaudeTierVersionGuard(t *testing.T) {
	newEnough := filepath.Join(t.TempDir(), "claude-new")
	if err := os.WriteFile(newEnough, []byte("#!/bin/sh\necho \"2.2.5 (Claude Code)\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	tooOld := filepath.Join(t.TempDir(), "claude-old")
	if err := os.WriteFile(tooOld, []byte("#!/bin/sh\necho \"2.1.17 (Claude Code)\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	policy := parseAgentAIClaudeRemotePolicy(map[string]interface{}{"claude_remote_policy": map[string]interface{}{"trust_level": "full"}})

	kept := applyClaudeTierVersionGuard(&agentAITool{path: newEnough}, policy)
	if kept.trustTier != "full" || kept.policyNotice != nil {
		t.Fatalf("2.2+ must keep tier: %+v", kept)
	}
	downgraded := applyClaudeTierVersionGuard(&agentAITool{path: tooOld}, policy)
	if downgraded.trustTier != "isolated" || downgraded.policyNotice == nil ||
		downgraded.policyNotice["reason"] != "claude_version_below_2_2" ||
		downgraded.policyNotice["requested"] != "full" {
		t.Fatalf("2.1.x must downgrade: %+v", downgraded)
	}
	// Unprobeable binary → fail-closed downgrade.
	unknown := applyClaudeTierVersionGuard(&agentAITool{path: filepath.Join(t.TempDir(), "missing")}, policy)
	if unknown.trustTier != "isolated" {
		t.Fatalf("unprobeable binary must fail closed: %+v", unknown)
	}
	// Isolated tiers are never touched.
	isolated := parseAgentAIClaudeRemotePolicy(map[string]interface{}{"claude_remote_policy": map[string]interface{}{"trust_level": "isolated"}})
	if got := applyClaudeTierVersionGuard(&agentAITool{path: tooOld}, isolated); got.trustTier != "isolated" || got.policyNotice != nil {
		t.Fatalf("isolated must stay untouched: %+v", got)
	}
}
