# 远程 Claude 会话三档信任 实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 按 spec（`docs/superpowers/specs/2026-08-31-remote-claude-trust-tiers-design.md`）实现服务端可下发的三档信任（`isolated` / `sanitized` / `full`），使远程 claude headless 会话能力对齐本地交互式，同时保留审批桥与项目设置隔离。

**Architecture:** 扩展 `claude_remote_policy` 解析出 effective trust tier → `runCLIPass` 中版本守门（<2.2 整档降级 isolated + `policy_notice`）→ `withClaudeRemotePolicy` 按档位拼 `--setting-sources` / 清洗插件 / MCP 显式合并 → `withClaudeApprovalHook` 携带档位 sources（不再无条件清空）→ slash 列表按档位加 `aliang-project:` 前缀并重定门控。

**Tech Stack:** Go（现有 `app/http/services` 包），标准库 json/os/filepath；测试用现有 `testing` 风格；真实 CLI 冒烟用临时项目。

**Spec:** `docs/superpowers/specs/2026-08-31-remote-claude-trust-tiers-design.md`（实现中与 spec 冲突时以 spec 为准，改 spec 需说明）

**Git 约定（项目规则）:** 提交标题全中文「新增：/修复：」，HEREDOC 多行，footer 🤖 + Co-Authored-By（见 CLAUDE.md）；**执行前须获得用户对提交的明确同意**。

---

## File Structure

全部改动集中在既有三个文件（不新建源文件）：

| 文件 | 职责变化 |
|---|---|
| `app/http/services/agent_ai.go` | `parseAgentAIClaudeRemotePolicy` 档位映射；新增 `probeClaudeCodeVersion` / `applyClaudeTierVersionGuard`；`withClaudeApprovalHook` 携带档位 sources；`claudeApprovalHookSettings` 档位门控；`runCLIPass` 接线 + `ai.run.started` 增 `policy_notice` |
| `app/http/services/agent_ai_claude_policy.go` | 新增 `normalizeClaudeTrustTier` / `claudeTierSettingSources`（T1 已落地）；`withClaudeRemotePolicy` 按档位（签名改返回 notice）；`sanitizedClaudeMarkdown` 按 kind 白名单；新增 `copySanitizedClaudeAgents`；`prepareClaudeProjectCapabilityPlugin` 加 agents/；新增 `claudeTierMCPArgs`；超限日志 |
| `app/http/services/agent_slash_commands.go` | `parseSlashFrontmatter` 增 `context`/`agent`/`tools` 字段；`collectProjectSlashCommands` 加 namePrefix 参数；`agentSlashCommandsListPayloadWithManager` 前缀 + `includeProjectClaude` 按档位 |
| `app/http/services/agent_slash_commands_test.go` | Task 7：`collectProjectSlashCommands` 签名变更的 3 处既有调用点适配（约 209/236/404 行） |
| `app/http/services/agent_ai_claude_policy_test.go` | 上述全部单测 + 3 处既有测试更新 |

import 提示：`agent_ai_claude_policy.go` 现无 `logger` import，Task 2 起需补 `"aliang.one/nursorgate/common/logger"`。

注意：`parseSlashFrontmatter`/`slashFrontmatter` 定义在 **agent_slash_commands.go**（508-577 行），`sanitizedClaudeMarkdown` 在 agent_ai_claude_policy.go 使用它——字段扩展改前者，白名单改后者。

---

### Task 1: 信任档模型与解析映射

**Files:**
- Modify: `app/http/services/agent_ai.go`（`agentAIClaudeRemotePolicy` 结构 584-595 行、`parseAgentAIClaudeRemotePolicy` 602-630 行）
- Modify: `app/http/services/agent_ai_claude_policy.go`（新增 `normalizeClaudeTrustTier`、`claudeTierSettingSources`）
- Test: `app/http/services/agent_ai_claude_policy_test.go`

- [ ] **Step 1: 写失败测试（档位解析矩阵）**

在 `agent_ai_claude_policy_test.go` 末尾（`argumentValue` helper 之前）追加：

```go
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
	if p := parse(map[string]interface{}{"trust_level": "full", "project_skill_trusted": false, "project_capability_mode": "disabled"}); p.trustTier != "full" {
		t.Fatalf("trust_level must supersede legacy fields, got %q", p.trustTier)
	}
	if p := parse(map[string]interface{}{"project_skill_trusted": true, "project_capability_mode": "disabled"}); p.trustTier != "isolated" {
		t.Fatalf("legacy trusted+bad mode = %q, want isolated", p.trustTier)
	}
}
```

```go
func TestClaudeTierSettingSources(t *testing.T) {
	cases := map[string]string{"isolated": "", "sanitized": "user", "full": "user,project", "": "", "bogus": ""}
	for tier, want := range cases {
		if got := claudeTierSettingSources(tier); got != want {
			t.Fatalf("claudeTierSettingSources(%q) = %q, want %q", tier, got, want)
		}
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./app/http/services/ -run 'TestParseAgentAIClaudeRemotePolicyTrustLevels|TestClaudeTierSettingSources' -count=1`
Expected: FAIL（`trustTier` 字段不存在 / `claudeTierSettingSources` undefined，编译错误）

- [ ] **Step 3: 实现**

3a. `agent_ai.go` 结构体追加三个字段（放在 `permissionAsk` 之后）：

```go
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
```

3b. `agent_ai_claude_policy.go` 顶部新增两个函数：

```go
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
```

（`agent_ai_claude_policy.go` 现有 import 需补 `strings`——已有。）

3c. `parseAgentAIClaudeRemotePolicy` 整体替换为（602-630 行）：

```go
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
	// Tier resolution — spec §3 compat mapping, evaluated in order:
	// 1) valid trust_level wins and supersedes the legacy fields;
	// 2) present-but-invalid trust_level → isolated (fail-closed);
	// 3) no trust_level → legacy trusted+sanitized_plugin maps to sanitized,
	//    everything else (incl. legacy untrusted) maps to isolated so old
	//    servers keep byte-identical behavior;
	// 4) absent claude_remote_policy → mechanism off (early return above).
	if level := strings.TrimSpace(remoteString(raw, "trust_level")); level != "" {
		if tier, valid := normalizeClaudeTrustTier(level); valid {
			policy.trustTier = tier
		} else {
			policy.trustTier = "isolated"
		}
	} else if policy.projectSkillTrusted && policy.projectCapabilityMode == "sanitized_plugin" {
		policy.trustTier = "sanitized"
	} else {
		policy.trustTier = "isolated"
	}
	policy.projectMCPTrusted = remoteBool(raw, "project_mcp_trusted", false)
	// The tier table owns --setting-sources (see claudeTierSettingSources);
	// the message-level setting_sources field is legacy and dropped.
	policy.settingSources = nil
	if settings, ok := raw["settings"].(map[string]interface{}); ok {
		policy.disableSkillShell = remoteBool(settings, "disableSkillShellExecution", true)
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
```

（保留原注释中 permission 数组合并语义的要点，已折叠进 tier 注释。`agent_ai.go` 已 import `strings`。）

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./app/http/services/ -run 'TestParseAgentAIClaudeRemotePolicy' -count=1`
Expected: PASS（含既有 `TestParseAgentAIClaudeRemotePolicyDisablesFilesystemSettings`——`settingSources` 仍被置 nil，断言不变）

- [ ] **Step 5: 提交**

```bash
git add app/http/services/agent_ai.go app/http/services/agent_ai_claude_policy.go app/http/services/agent_ai_claude_policy_test.go
git commit -m "$(cat <<'EOF'
新增：远程 Claude 信任档模型与解析映射（trust_level → isolated/sanitized/full）

🤖 Generated with [Claude Code](https://claude.com/claude-code)

Co-Authored-By: Claude Sonnet 4.5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 2: withClaudeRemotePolicy 按档位出旗子（签名改返回 notice）

**Files:**
- Modify: `app/http/services/agent_ai_claude_policy.go:17-36`（`withClaudeRemotePolicy`）
- Modify: `app/http/services/agent_ai.go:4610-4620`（`runCLIPass` 调用点）
- Test: `app/http/services/agent_ai_claude_policy_test.go`

- [ ] **Step 1: 写失败测试**

追加到 `agent_ai_claude_policy_test.go`：

```go
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
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./app/http/services/ -run 'TestClaudeRemotePolicyTierFlags|TestClaudeRemotePolicySanitizeFailure' -count=1`
Expected: FAIL（`withClaudeRemotePolicy` 仍返回 error，编译错误；sanitized sources 仍为 `""`）

- [ ] **Step 3: 实现**

3a. `withClaudeRemotePolicy` 整体替换（签名 `error` → `map[string]interface{}`）：

```go
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
```

3b. 同文件末尾先放一个 **Task 6 才实现主体** 的占位（保证本任务可编译，Task 6 替换实现）：

```go
// claudeTierMCPArgs builds --strict-mcp-config/--mcp-config flags for the
// sanitized tier when the server marked the project MCP trusted (spec §5).
// Placeholder implementation in this task; replaced in the MCP merge task.
func claudeTierMCPArgs(policy agentAIClaudeRemotePolicy, projectPath string) ([]string, func(), error) {
	return nil, func() {}, nil
}
```

3c. `runCLIPass` 调用点（agent_ai.go 4610-4620）替换为（**本任务起 `agent_ai_claude_policy.go` 需补 import `"aliang.one/nursorgate/common/logger"`**）：

```go
	tool = withAgentAIAttachments(tool, run.attachments)
	cleanupClaudePolicy := func() {}
	effectiveRun := run
	if tool.outputFormat == agentAIOutputClaudeStreamJSON && !run.readOnly {
		effectiveRun.claudePolicy = applyClaudeTierVersionGuard(tool, effectiveRun.claudePolicy)
		var policyNotice map[string]interface{}
		tool, cleanupClaudePolicy, policyNotice = withClaudeRemotePolicy(tool, effectiveRun)
		if policyNotice != nil {
			effectiveRun.claudePolicy.policyNotice = policyNotice
		}
		defer cleanupClaudePolicy()
		tool = withClaudeApprovalHook(tool, effectiveRun)
	}
```

（`applyClaudeTierVersionGuard` 在 Task 4 实现——本任务先放占位，保证编译：）

```go
// applyClaudeTierVersionGuard downgrades non-isolated tiers on claude < 2.2
// (spec §3 guard). Placeholder in this task; replaced in the guard task.
func applyClaudeTierVersionGuard(tool *agentAITool, policy agentAIClaudeRemotePolicy) agentAIClaudeRemotePolicy {
	return policy
}
```

- [ ] **Step 4: 跑测试确认通过 + 既有测试适配**

Run: `go test ./app/http/services/ -run 'TestClaudeRemotePolicy' -count=1`
Expected: `TestClaudeRemotePolicyBuildsSanitizedProjectPlugin` 可能 FAIL（旧 helper `testClaudeRemotePolicy(true)` 现映射 sanitized → sources 期望从 `""` 变 `"user"`）。改该测试：`tool, cleanup, err := withClaudeRemotePolicy(...)` 的第三个返回值改名为 `notice`（签名已从 error 变 map），`if err != nil` 判空改为 `if notice != nil { t.Fatalf(...) }`；`--setting-sources` 断言改为：

```go
	if got := argumentValue(tool.args, "--setting-sources"); got != "user" {
		t.Fatalf("setting sources = %q, want user", got)
	}
```

再跑至全部 PASS：`go test ./app/http/services/ -run 'TestClaudeRemotePolicy|TestParseAgentAIClaudeRemotePolicy' -count=1`

- [ ] **Step 5: 提交**

```bash
git add app/http/services/agent_ai.go app/http/services/agent_ai_claude_policy.go app/http/services/agent_ai_claude_policy_test.go
git commit -m "$(cat <<'EOF'
新增：withClaudeRemotePolicy 按信任档位出旗子，sanitize 失败降级 isolated 并回报 notice

🤖 Generated with [Claude Code](https://claude.com/claude-code)

Co-Authored-By: Claude Sonnet 4.5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 3: 清洗白名单扩展（context/agent 保留、agents/ 拷贝、超限日志）

**Files:**
- Modify: `app/http/services/agent_slash_commands.go:508-514`（`slashFrontmatter` 结构）与 546-563（解析 switch）
- Modify: `app/http/services/agent_ai_claude_policy.go`（`sanitizedClaudeMarkdown`、`prepareClaudeProjectCapabilityPlugin`、两个 copy 函数超限日志、新增 `copySanitizedClaudeAgents`）
- Test: `app/http/services/agent_ai_claude_policy_test.go`

- [ ] **Step 1: 写失败测试**

追加：

```go
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
	agent := "---\nname: reviewer\ndescription: Reviews code\ntools: Bash, Read\nhooks:\n  Stop: []\nmodel: opus\n---\nReview carefully.\n"
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
	for _, forbidden := range []string{"hooks:", "model:"} {
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
```

并更新既有 `TestClaudeRemotePolicyBuildsSanitizedProjectPlugin`：
- skill forbidden 列表 `[]string{"allowed-tools", "hooks:", "context:"}` → `[]string{"allowed-tools", "hooks:"}`（context 不再是违禁品）
- command 断言 `if strings.Contains(string(command), "allowed-tools") || strings.Contains(string(command), "context:")` → 只保留 `allowed-tools` 检查，另加 `if !strings.Contains(string(command), "context: \"fork\"") { t.Fatalf(...) }`（ship.md 带 `context: fork`，现在应保留）

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./app/http/services/ -run 'TestSanitizedClaudeMarkdownPreserves|TestCopySanitizedClaudeAgents|TestClaudeRemotePolicyBuildsSanitizedProjectPlugin' -count=1`
Expected: FAIL（`claudeSanitizeSkill`/`copySanitizedClaudeAgents` undefined；context 仍被剥）

- [ ] **Step 3: 实现**

3a. `agent_slash_commands.go` `slashFrontmatter` 加三字段，解析 switch 加三个 case：

```go
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
```

switch 增：

```go
		case "context":
			fm.context = value
		case "agent":
			fm.agent = value
		case "tools":
			fm.tools = value
```

（注意：`tools` 仅支持单行值 `tools: Bash, Read`；YAML 块列表形态解析不到即丢弃，fail-closed，可接受——与 spec §4.3 一致。）

3b. `agent_ai_claude_policy.go`：kind 常量 + `sanitizedClaudeMarkdown` 重写：

```go
// claudeSanitizeKind selects the frontmatter whitelist for a capability file.
type claudeSanitizeKind string

const (
	claudeSanitizeSkill   claudeSanitizeKind = "skill"
	claudeSanitizeCommand claudeSanitizeKind = "command"
	claudeSanitizeAgent   claudeSanitizeKind = "agent"
)
```

```go
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
	default:
		if kind == claudeSanitizeSkill {
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
		writeYAMLString("context", fm.context)
		writeYAMLString("agent", fm.agent)
	}
	out.WriteString("---\n")
	out.WriteString(body)
	return []byte(out.String()), nil
}
```

调用点替换：`copySanitizedClaudeSkills` 内 `sanitizedClaudeMarkdown(sourceMarkdown, true)` → `sanitizedClaudeMarkdown(sourceMarkdown, claudeSanitizeSkill)`；`copySanitizedClaudeCommands` 内 `sanitizedClaudeMarkdown(path, false)` → `sanitizedClaudeMarkdown(path, claudeSanitizeCommand)`。

3c. 新增 `copySanitizedClaudeAgents` + `prepareClaudeProjectCapabilityPlugin` 接线：

```go
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
	count := 0
	for _, entry := range entries {
		if count >= claudeProjectCapabilityLimit {
			logger.Warn(fmt.Sprintf("claude-policy: agent cap %d reached at %s; further agents skipped", claudeProjectCapabilityLimit, sourceRoot))
			break
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
```

`prepareClaudeProjectCapabilityPlugin` 在 commands 拷贝之后追加：

```go
	agentsDir := filepath.Join(root, "agents")
	if err := os.MkdirAll(agentsDir, 0o700); err != nil {
		return fail(err)
	}
	if err := copySanitizedClaudeAgents(filepath.Join(dotClaude, "agents"), agentsDir); err != nil {
		return fail(err)
	}
```

3d. 超限日志（spec §4.1）：`copySanitizedClaudeSkills` 循环首行条件 `if count >= claudeProjectCapabilityLimit || ...` 拆开，cap 分支加 `logger.Warn`（同 agents 模式）；`copySanitizedClaudeCommands` WalkDir 内 `if count >= claudeProjectCapabilityLimit || ...` 同样拆开加一次性 Warn（用局部 `warned bool` 防刷屏）。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./app/http/services/ -run 'TestSanitizedClaudeMarkdownPreserves|TestCopySanitizedClaude|TestClaudeRemotePolicyBuildsSanitizedProjectPlugin' -count=1`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add app/http/services/agent_slash_commands.go app/http/services/agent_ai_claude_policy.go app/http/services/agent_ai_claude_policy_test.go
git commit -m "$(cat <<'EOF'
新增：清洗白名单保留 context/agent、新增项目 agents/ 清洗拷贝与超限日志

🤖 Generated with [Claude Code](https://claude.com/claude-code)

Co-Authored-By: Claude Sonnet 4.5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 4: 2.1.x 版本守门 + policy_notice 上报

**Files:**
- Modify: `app/http/services/agent_ai.go`（`applyClaudeTierVersionGuard` 占位替换；`runCLIPass` 的 `ai.run.started` payload）
- Test: `app/http/services/agent_ai_claude_policy_test.go`

- [ ] **Step 1: 写失败测试**

```go
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
```

（脚本可执行探测依赖内容寻址缓存 `executableProbeCacheKey`——临时路径内容各不相同，互不污染。）

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./app/http/services/ -run TestApplyClaudeTierVersionGuard -count=1`
Expected: FAIL（仍为占位，full 被 2.1.x 保留）

- [ ] **Step 3: 实现**

3a. 替换 `applyClaudeTierVersionGuard` 占位，并新增版本探测：

```go
type claudeCodeVersionProbe struct {
	version claudeCodeVersion
	ok      bool
}

var claudeCodeVersionProbeCache sync.Map

// probeClaudeCodeVersion returns the parsed version of the claude executable,
// cached per executable content. ok=false when the probe fails (missing or
// non-claude binary) — callers treat that as fail-closed.
func probeClaudeCodeVersion(toolPath string) (claudeCodeVersion, bool) {
	toolPath = strings.TrimSpace(toolPath)
	if toolPath == "" {
		return claudeCodeVersion{}, false
	}
	cacheKey := executableProbeCacheKey(toolPath)
	if cached, ok := claudeCodeVersionProbeCache.Load(cacheKey); ok {
		probe, _ := cached.(claudeCodeVersionProbe)
		return probe.version, probe.ok
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := newBackgroundCommandContext(ctx, toolPath, "--version").CombinedOutput()
	version, ok := parseClaudeCodeVersion(string(out))
	if err != nil || !ok {
		claudeCodeVersionProbeCache.Store(cacheKey, claudeCodeVersionProbe{})
		return claudeCodeVersion{}, false
	}
	claudeCodeVersionProbeCache.Store(cacheKey, claudeCodeVersionProbe{version: version, ok: true})
	return version, true
}

// applyClaudeTierVersionGuard downgrades non-isolated tiers to isolated when
// the installed claude predates 2.2.0: 2.1.x never fires PermissionRequest in
// headless --print mode and user ask rules suppress PreToolUse approvals, so
// loading user/project settings there would break the approval bridge
// (spec §3 guard). The downgrade is tier-wide: sources back to "", no plugin.
// Note: a full-tier policy downgraded here keeps its (intentionally empty)
// ask list — the <2.2 PreToolUse strategy never injects permissions.ask, so
// behavior matches legacy isolated runs without refilling the default list.
func applyClaudeTierVersionGuard(tool *agentAITool, policy agentAIClaudeRemotePolicy) agentAIClaudeRemotePolicy {
	if !policy.enabled || policy.trustTier == "" || policy.trustTier == "isolated" {
		return policy
	}
	version, ok := probeClaudeCodeVersion(tool.path)
	if ok && version.atLeast(2, 2, 0) {
		return policy
	}
	logger.Warn(fmt.Sprintf("claude-policy: claude %s predates 2.2.0, downgrading trust tier %s to isolated", tool.path, policy.trustTier))
	downgraded := policy
	downgraded.trustTier = "isolated"
	downgraded.policyNotice = map[string]interface{}{
		"effective": "isolated",
		"requested": policy.trustTier,
		"reason":    "claude_version_below_2_2",
	}
	return downgraded
}
```

3b. `runCLIPass` 的 `ai.run.started` 写出（原为字面 map 直传 `writeJSON`）改为：

```go
	started := map[string]interface{}{
		"type":         models.AgentEventAIRunStarted,
		"session_id":   run.sessionID,
		"message_id":   agentAssistantMessageID(run.messageID),
		"provider":     tool.id,
		"mode":         run.mode,
		"project_path": run.projectPath,
		"state":        "running",
	}
	if effectiveRun.claudePolicy.policyNotice != nil {
		started["policy_notice"] = effectiveRun.claudePolicy.policyNotice
	}
	_ = writeJSON(started)
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./app/http/services/ -run 'TestApplyClaudeTierVersionGuard|TestClaudeRemotePolicy' -count=1`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add app/http/services/agent_ai.go app/http/services/agent_ai_claude_policy_test.go
git commit -m "$(cat <<'EOF'
新增：claude<2.2 版本守门整档降级 isolated，ai.run.started 上报 policy_notice

🤖 Generated with [Claude Code](https://claude.com/claude-code)

Co-Authored-By: Claude Sonnet 4.5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 5: withClaudeApprovalHook 携带档位 sources + 审批设置档位门控

**Files:**
- Modify: `app/http/services/agent_ai.go:6772-6795`（`withClaudeApprovalHook`）与 6749-6757（`claudeApprovalHookSettings` 条件）
- Test: `app/http/services/agent_ai_claude_policy_test.go`

- [ ] **Step 1: 写失败测试**

```go
func TestClaudeApprovalHookCarriesTierSettingSources(t *testing.T) {
	mk := func(raw map[string]interface{}) agentAIRun {
		return agentAIRun{sessionID: "s-tier", messageID: "m-tier", runSeq: 1, approvalToken: "token",
			claudePolicy: parseAgentAIClaudeRemotePolicy(map[string]interface{}{"claude_remote_policy": raw})}
	}
	cases := []struct {
		name   string
		raw    map[string]interface{}
		wantSS string
	}{
		{"isolated", map[string]interface{}{"trust_level": "isolated"}, ""},
		{"sanitized", map[string]interface{}{"trust_level": "sanitized"}, "user"},
		{"full", map[string]interface{}{"trust_level": "full"}, "user,project"},
	}
	for _, tc := range cases {
		tool := withClaudeApprovalHook(&agentAITool{path: "/bin/claude", args: []string{"--setting-sources", "stale", "--print", "prompt"}}, mk(tc.raw))
		if got := argumentValue(tool.args, "--setting-sources"); got != tc.wantSS {
			t.Fatalf("%s: final setting sources = %q, want %q", tc.name, got, tc.wantSS)
		}
		if n := strings.Count(strings.Join(tool.args, "\x00"), "--setting-sources"); n != 1 {
			t.Fatalf("%s: flag not normalized (%d): %v", tc.name, n, tool.args)
		}
	}
}

func TestClaudeApprovalSettingsFullTierOmitsShellDisableAndAsk(t *testing.T) {
	run := agentAIRun{sessionID: "s-full", messageID: "m-full", approvalToken: "token",
		claudePolicy: parseAgentAIClaudeRemotePolicy(map[string]interface{}{"claude_remote_policy": map[string]interface{}{"trust_level": "full"}})}
	settings, err := claudeApprovalHookSettings(claudeApprovalHookPermissionRequestHTTP, run)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := settings["disableSkillShellExecution"]; exists {
		t.Fatal("full tier must not disable skill shell execution")
	}
	if _, exists := settings["permissions"]; exists {
		t.Fatal("full tier must not inject ask rules")
	}
	if _, exists := settings["hooks"]; !exists {
		t.Fatal("approval bridge hooks must stay injected")
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./app/http/services/ -run 'TestClaudeApprovalHookCarriesTierSettingSources|TestClaudeApprovalSettingsFullTier' -count=1`
Expected: FAIL（sanitized/full 最终 sources 仍为 `""`；full 仍注入 shell 禁用）

- [ ] **Step 3: 实现**

3a. `withClaudeApprovalHook` 尾部（从现有 `copied := *tool` 行到 `return &copied`，共 4 行）整体替换为：

```go
	copied := *tool
	sources := ""
	if run.claudePolicy.enabled {
		// Carry the effective tier's sources instead of unconditionally
		// clobbering to "" — the hook layer previously erased whatever tier
		// withClaudeRemotePolicy had configured (spec §7). Policy-disabled
		// runs keep the historical "" (isolated) behavior.
		sources = claudeTierSettingSources(run.claudePolicy.trustTier)
	}
	copied.args = withoutCLIArgumentValue(tool.args, "--setting-sources")
	copied.args = append([]string{"--setting-sources", sources, "--permission-mode", "default", "--settings", string(settingsRaw)}, copied.args...)
	return &copied
```

3b. `claudeApprovalHookSettings` 条件 `if run.claudePolicy.enabled {` 改为：

```go
	if run.claudePolicy.enabled && run.claudePolicy.trustTier != "full" {
```

（full 档不注入 `disableSkillShellExecution` 与 `permissions.ask`；hooks 恒注入。注释保留原 2.1.x/2.2 策略说明。）

- [ ] **Step 4: 跑测试确认通过（含既有用例）**

Run: `go test ./app/http/services/ -run 'TestClaudeApproval' -count=1`
Expected: PASS。注意既有 `TestClaudeApprovalHookDisablesFilesystemSettingsWithoutRemotePolicy`（无 policy）行为未变（sources 仍 `""`），应原样通过；`TestClaudeApprovalSettingsPreserveRemoteAskRules`/`LegacyOmits...`（legacy isolated）不变通过。

- [ ] **Step 5: 提交**

```bash
git add app/http/services/agent_ai.go app/http/services/agent_ai_claude_policy_test.go
git commit -m "$(cat <<'EOF'
修复：审批 hook 不再清空档位 setting-sources；full 档不再注入 shell 禁用与 ask 规则

🤖 Generated with [Claude Code](https://claude.com/claude-code)

Co-Authored-By: Claude Sonnet 4.5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 6: sanitized 档 MCP 显式合并（project_mcp_trusted）

**Files:**
- Modify: `app/http/services/agent_ai_claude_policy.go`（`claudeTierMCPArgs` 占位替换）
- Test: `app/http/services/agent_ai_claude_policy_test.go`

- [ ] **Step 1: 写失败测试**

```go
func TestClaudeTierMCPArgsMergesUserAndProjectServers(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	userConfig := map[string]interface{}{
		"mcpServers": map[string]interface{}{
			"shared": map[string]interface{}{"command": "user-cmd"},
			"u-only": map[string]interface{}{"command": "user-only"},
		},
	}
	raw, _ := json.Marshal(userConfig)
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	project := t.TempDir()
	projectMCP := `{"mcpServers":{"shared":{"command":"project-cmd"},"p-only":{"command":"project-only"}}}`
	if err := os.WriteFile(filepath.Join(project, ".mcp.json"), []byte(projectMCP), 0o644); err != nil {
		t.Fatal(err)
	}
	policy := parseAgentAIClaudeRemotePolicy(map[string]interface{}{"claude_remote_policy": map[string]interface{}{"trust_level": "sanitized", "project_mcp_trusted": true}})

	flags, cleanup, err := claudeTierMCPArgs(policy, project)
	if err != nil {
		t.Fatal(err)
	}
	if len(flags) != 3 || flags[0] != "--strict-mcp-config" || flags[1] != "--mcp-config" {
		t.Fatalf("flags = %v", flags)
	}
	configRaw, err := os.ReadFile(flags[2])
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		MCPServers map[string]interface{} `json:"mcpServers"`
	}
	if err := json.Unmarshal(configRaw, &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.MCPServers) != 3 {
		t.Fatalf("servers = %v", parsed.MCPServers)
	}
	if shared, _ := parsed.MCPServers["shared"].(map[string]interface{}); shared["command"] != "project-cmd" {
		t.Fatalf("project must override user on collision: %v", shared)
	}
	if _, ok := parsed.MCPServers["u-only"]; !ok {
		t.Fatal("user-only server missing")
	}
	// Explicit cleanup (not defer) so the removal assertion below sees the
	// file already gone; os.Remove on a missing file is an ignored no-op.
	cleanup()
	if _, err := os.Stat(flags[2]); !os.IsNotExist(err) {
		t.Fatalf("temp config not removed: %v", err)
	}
}

func TestClaudeTierMCPArgsInactive(t *testing.T) {
	policy := parseAgentAIClaudeRemotePolicy(map[string]interface{}{"claude_remote_policy": map[string]interface{}{"trust_level": "sanitized"}})
	project := t.TempDir()
	flags, cleanup, err := claudeTierMCPArgs(policy, project)
	defer cleanup()
	if err != nil || flags != nil {
		t.Fatalf("untrusted project MCP must be a no-op: flags=%v err=%v", flags, err)
	}
	// Trusted but empty project .mcp.json → no flags either (user scope loads natively).
	policy2 := parseAgentAIClaudeRemotePolicy(map[string]interface{}{"claude_remote_policy": map[string]interface{}{"trust_level": "sanitized", "project_mcp_trusted": true}})
	flags2, cleanup2, err2 := claudeTierMCPArgs(policy2, project)
	defer cleanup2()
	if err2 != nil || flags2 != nil {
		t.Fatalf("empty project mcp must be a no-op: flags=%v err=%v", flags2, err2)
	}
}
```

（测试文件需补 `"encoding/json"` import。`t.Setenv("HOME")` 生效前提：`runtimepath.EffectiveAgentHome()` 在测试环境读 `HOME`/`USERPROFILE`——既有 `TestSlashCommandsRequireTrustAndSystemInitForProjectSkills` 已用同法，若实现后发现 `agentHome()` 不走 env，则在实现中改用可注入的 home resolver 并在本测试中注入。）

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./app/http/services/ -run TestClaudeTierMCPArgs -count=1`
Expected: FAIL（占位返回 nil flags）

- [ ] **Step 3: 实现（替换占位）**

```go
// claudeTierMCPArgs builds --strict-mcp-config/--mcp-config flags for the
// sanitized tier when the server marked the project MCP trusted (spec §5):
// user-scope servers (top-level "mcpServers" in <agentHome>/.claude.json —
// NOT the per-project entries) merged with the project's .mcp.json, project
// entries winning name collisions (mirrors the CLI's local > project > user
// precedence at merge time). Returns nil flags — native user-scope discovery
// — when the project contributes no servers.
func claudeTierMCPArgs(policy agentAIClaudeRemotePolicy, projectPath string) ([]string, func(), error) {
	if !policy.projectMCPTrusted {
		return nil, func() {}, nil
	}
	merged := map[string]interface{}{}
	projectCount := 0
	if home := agentHome(); home != "" {
		if raw, err := os.ReadFile(filepath.Join(home, ".claude.json")); err == nil {
			var parsed struct {
				MCPServers map[string]interface{} `json:"mcpServers"`
			}
			if json.Unmarshal(raw, &parsed) == nil {
				for name, server := range parsed.MCPServers {
					merged[name] = server
				}
			}
		}
	}
	if raw, err := os.ReadFile(filepath.Join(projectPath, ".mcp.json")); err == nil {
		var parsed struct {
			MCPServers map[string]interface{} `json:"mcpServers"`
		}
		if json.Unmarshal(raw, &parsed) == nil {
			for name, server := range parsed.MCPServers {
				if _, exists := merged[name]; !exists {
					projectCount++
				}
				merged[name] = server
			}
		}
	}
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
```

（`encoding/json` 已在 `agent_ai_claude_policy.go` import 中（第 3 行）；`agentHome` 已在包内。读文件失败一律容忍——合并层尽量可用，不因 user 配置缺失而降级。）

- [ ] **Step 4: 跑测试确认通过 + 全包回归**

Run: `go test ./app/http/services/ -run 'TestClaudeTierMCPArgs|TestClaudeRemotePolicy' -count=1` 然后 `go test ./app/http/services/ -count=1`
Expected: 全部 PASS

- [ ] **Step 5: 提交**

```bash
git add app/http/services/agent_ai_claude_policy.go app/http/services/agent_ai_claude_policy_test.go
git commit -m "$(cat <<'EOF'
新增：sanitized 档项目 MCP 受信时的 user+project 显式合并注入（--strict-mcp-config）

🤖 Generated with [Claude Code](https://claude.com/claude-code)

Co-Authored-By: Claude Sonnet 4.5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 7: slash 列表命名空间对齐 + 门控按档位

**Files:**
- Modify: `app/http/services/agent_slash_commands.go:50-62`（payload 组装）、`155-161`（`collectProjectSlashCommands`）
- Modify: `app/http/services/agent_slash_commands_test.go:209,236,404`（`collectProjectSlashCommands` 既有调用点加 `""` 前缀实参）
- Test: `app/http/services/agent_ai_claude_policy_test.go`

- [ ] **Step 1: 写失败测试**

更新既有 `TestSlashCommandsRequireTrustAndSystemInitForProjectSkills` 的 trusted 断言：

```go
	commands := trusted["commands"].([]map[string]interface{})
	if len(commands) != 1 || commands[0]["name"] != "aliang-project:deploy" || commands[0]["kind"] != "skill" {
		t.Fatalf("trusted commands = %+v", commands)
	}
```

（legacy helper `testClaudeRemotePolicy(true)` → sanitized 档 → 前缀生效；且 `recordClaudeCapabilities` 已记录 `aliang-project:deploy`，精确匹配通过。）

追加 full 档用例：

```go
func TestSlashCommandsFullTierListsProjectBareNames(t *testing.T) {
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
	manager.recordClaudeCapabilities("s1", project, []string{"deploy"}, "2.2.5")

	msg := map[string]interface{}{
		"request_id":           "r1",
		"session_id":           "s1",
		"project_path":         project,
		"provider":             "claude",
		"include_user_level":   false,
		"include_plugins":      false,
		"claude_remote_policy": map[string]interface{}{"trust_level": "full"},
	}
	payload := agentSlashCommandsListPayloadWithManager(msg, manager)
	commands := payload["commands"].([]map[string]interface{})
	if len(commands) != 1 || commands[0]["name"] != "deploy" {
		t.Fatalf("full tier commands = %+v, want bare deploy", commands)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./app/http/services/ -run 'TestSlashCommands' -count=1`
Expected: FAIL（full 档项目条目被旧门控排除；sanitized 无前缀）

- [ ] **Step 3: 实现**

3a. `agentSlashCommandsListPayloadWithManager`：在现有 `remotePolicy := parseAgentAIClaudeRemotePolicy(msg)` 之后，把原 `includeProjectClaude := ...` 一行替换为：

```go
	projectPrefix := ""
	if remotePolicy.enabled && remotePolicy.trustTier == "sanitized" {
		projectPrefix = "aliang-project:"
	}
	includeProjectClaude := !remotePolicy.enabled || remotePolicy.trustTier == "sanitized" || remotePolicy.trustTier == "full"
```

及调用处 `collectProjectSlashCommands(projectPath)` → `collectProjectSlashCommands(projectPath, projectPrefix)`。

3b. `collectProjectSlashCommands` 加参并透传（scan 函数已有 `namePrefix` 机制）；**同文件测试 `agent_slash_commands_test.go` 中 3 处既有调用 `collectProjectSlashCommands(projectPath)`（约 209/236/404 行）改为 `collectProjectSlashCommands(projectPath, "")`**，否则测试包编译失败：

```go
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
```

- [ ] **Step 4: 跑测试确认通过 + 全包回归**

Run: `go test ./app/http/services/ -run 'TestSlashCommands' -count=1` 然后 `go test ./app/http/services/ -count=1`
Expected: 全部 PASS

- [ ] **Step 5: 提交**

```bash
git add app/http/services/agent_slash_commands.go app/http/services/agent_slash_commands_test.go app/http/services/agent_ai_claude_policy_test.go
git commit -m "$(cat <<'EOF'
修复：sanitized 档 slash 列表加 aliang-project: 前缀对齐调用名；门控改按信任档位判定

🤖 Generated with [Claude Code](https://claude.com/claude-code)

Co-Authored-By: Claude Sonnet 4.5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 8: 全量验证与冒烟

**Files:** 无新增改动（验证任务）

- [ ] **Step 1: 全量构建与静态检查**

Run: `go build ./... && go vet ./app/http/services/`
Expected: 无输出（成功）

- [ ] **Step 2: 全量测试**

Run: `go test ./app/http/services/ -count=1`
Expected: 全部 PASS（0 FAIL）

- [ ] **Step 3: 真实 CLI 冒烟（spec §10，需本机 claude ≥ 2.2；临时项目含 `.claude/skills/demo-skill/SKILL.md`、`.claude/commands/hello.md`、`.mcp.json`、`CLAUDE.md` 暗号）**

| # | 命令要点 | 期望 |
|---|---|---|
| 1 | sanitized 等效：`claude --setting-sources user --permission-mode default --plugin-dir <清洗目录> --print --output-format stream-json ...` | init `slash_commands` 含 `aliang-project:demo-skill`；提示模型调用该 skill 成功输出；项目 CLAUDE.md 暗号**不可见** |
| 2 | full 等效：`claude --setting-sources user,project --permission-mode default --print ...` | 暗号回复成功（CLAUDE.md 加载）；裸名 `demo-skill`；`mcp_servers` = 用户级 + 项目 |
| 3 | isolated 等效（守门降级产物）：`claude --setting-sources "" --permission-mode default --print ...` | 暗号不可见；`mcp_servers: []` |
| 4 | full 档 `permissions.allow` 行为实测（承重墙）：settings 注入 allow + 触发该工具 | 放行不弹桥；skill `allowed-tools` 在 sanitized 档发起仍弹桥（已剥） |
| 5 | env 优先级：settings `env` 设 `ANTHROPIC_BASE_URL` + 进程 env 同名 | 记录实际生效方，回写 spec §9.6 结论 |
| 6 | strict 注入与插件 MCP 相容性 | 若插件 MCP 被 `--strict-mcp-config` 抑制 → 实现计划追加条件任务：插件 MCP 并入显式合并 config |

- [ ] **Step 4: 汇报**

向用户汇报：单测/构建结果、6 项冒烟结果、spec §9.6/冒烟 6 的结论回写，等待用户决定是否推送/继续服务端配套。

---

## 风险与回滚

- 每任务独立提交，任一任务失败可 `git revert` 单提交回滚。
- 行为开关：全部由服务端 `trust_level` 驱动，服务端不下发（或下发 `isolated`）即回到旧行为；`claude_remote_policy` 缺失时机制整体旁路。
- 高风险点回顾（spec §9）：full 档项目 hooks 生效（手机端显式确认兜底，属服务端 UX）；2.1.x 由守门自动降级。
