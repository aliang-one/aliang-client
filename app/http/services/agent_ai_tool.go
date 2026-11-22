package services

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

func resolveAgentAITool(prompt string, preferred string, model string, effort string, resumeSessionID string, newSessionID ...string) (*agentAITool, error) {
	reservedID := ""
	if len(newSessionID) > 0 {
		reservedID = strings.TrimSpace(newSessionID[0])
	}
	preferred, err := normalizeAgentAIProvider(preferred)
	if err != nil {
		return nil, err
	}
	if preferred != "auto" {
		if err := validateApprovalCapableProvider(preferred, codexAppServerAvailable()); err != nil {
			return nil, err
		}
		return resolveNamedAgentAITool(preferred, prompt, model, effort, resumeSessionID, reservedID)
	}
	candidates := []string{"claude", "claudecode"}
	if codexAppServerAvailable() {
		candidates = append([]string{"codex"}, candidates...)
	}
	for _, candidate := range candidates {
		if tool, err := resolveNamedAgentAITool(candidate, prompt, model, effort, resumeSessionID, reservedID); err == nil {
			return tool, nil
		}
	}
	return nil, errors.New("no approval-capable AI CLI found in PATH: Codex app-server, Claude, or Claude Code")
}

func resolveGoalAgentAITool(prompt string, preferred string, model string, effort string, resumeSessionID string, newSessionID ...string) (*agentAITool, error) {
	preferred, err := normalizeAgentAIProvider(preferred)
	if err != nil {
		return nil, err
	}
	if preferred == "auto" {
		return nil, errors.New("Goal execution requires an explicit AI provider")
	}
	return resolveNamedAgentAITool(preferred, prompt, model, effort, resumeSessionID, newSessionID...)
}

func validateApprovalCapableProvider(provider string, hasCodexAppServer bool) error {
	switch provider {
	case "codex":
		if !hasCodexAppServer {
			return errors.New("Codex app-server is required for remote approval support; update Codex before using it from Aliang")
		}
	case "opencode":
		return errors.New("OpenCode is unavailable for remote execution because it does not provide an Aliang approval bridge")
	}
	return nil
}

func resolveNamedAgentAITool(name string, prompt string, model string, effort string, resumeSessionID string, newSessionID ...string) (*agentAITool, error) {
	reservedID := ""
	if len(newSessionID) > 0 {
		reservedID = strings.TrimSpace(newSessionID[0])
	}
	model = normalizeAgentAIModel(model)
	effort = strings.TrimSpace(effort)
	resumeSessionID = strings.TrimSpace(resumeSessionID)
	switch name {
	case "codex":
		if path, err := lookPathCLI("codex"); err == nil {
			// codex/OpenAI path: reasoning effort is conveyed as a `<base>-<effort>`
			// model-name suffix — the downstream gateway derives reasoning_effort
			// from it (gpt-5.4-xhigh → reasoning_effort=xhigh). Applied AFTER model
			// normalization so the normalizer doesn't strip it. Skipped when no base
			// model is set (nothing to suffix) or the model already carries a suffix.
			codexModel := applyCodexEffortSuffix(model, effort)
			args := []string{"exec"}
			if resumeSessionID != "" {
				args = append(args, "resume")
			}
			args = append(args, "--json", "--skip-git-repo-check")
			if codexModel != "" {
				args = append(args, "--model", codexModel)
			}
			if resumeSessionID != "" {
				args = append(args, resumeSessionID)
			}
			args = append(args, prompt)
			return &agentAITool{
				id:           "codex",
				path:         path,
				args:         args,
				outputFormat: agentAIOutputCodexJSON,
			}, nil
		}
	case "claude":
		if path, err := lookPathCLI("claude"); err == nil {
			return newClaudeCodeAITool("claude", path, prompt, model, effort, resumeSessionID, reservedID), nil
		}
	case "claudecode":
		if path, err := lookPathCLI("claudecode"); err == nil {
			return newClaudeCodeAITool("claudecode", path, prompt, model, effort, resumeSessionID, reservedID), nil
		}
		if path, err := lookPathCLI("claude"); err == nil {
			return newClaudeCodeAITool("claudecode", path, prompt, model, effort, resumeSessionID, reservedID), nil
		}
	case "opencode":
		if path, err := lookPathCLI("opencode"); err == nil {
			return newOpenCodeAITool(path, prompt, model, effort, resumeSessionID), nil
		}
	default:
		return nil, fmt.Errorf("unsupported AI provider: %s", name)
	}
	return nil, fmt.Errorf("AI CLI %q was not found in PATH", name)
}

// applyCodexEffortSuffix appends the reasoning effort as a model-name suffix for
// the codex/OpenAI path: the downstream gateway derives reasoning_effort from a
// `<base>-<effort>` model name (e.g. gpt-5.4-xhigh → reasoning_effort=xhigh).
// Skipped when model is empty (no base to suffix), effort is empty, or the model
// already carries a recognized effort suffix (avoids gpt-5.4-xhigh-xhigh).
//
// Claude Code path does NOT use a model-name suffix: the gateway does not parse
// a suffixed claude model name and the upstream call would break. Claude effort
// is instead conveyed via the CLI's `--effort <level>` flag (see
// newClaudeCodeAITool), supported since Claude Code 2.1.x. The two providers
// therefore carry effort through DIFFERENT channels: codex via model-name
// suffix, claude via --effort. Both read the same session.effort field.

func applyCodexEffortSuffix(model, effort string) string {
	model = strings.TrimSpace(model)
	effort = strings.TrimSpace(effort)
	if model == "" || effort == "" {
		return model
	}
	if endsWithKnownAgentEffortSuffix(model) {
		return model
	}
	return model + "-" + effort
}

// endsWithKnownAgentEffortSuffix reports whether the model name already ends with
// a reasoning-effort suffix (none/minimal/low/medium/high/xhigh/extrahigh/max),
// so we don't double-append.

func endsWithKnownAgentEffortSuffix(model string) bool {
	lower := strings.ToLower(strings.TrimSpace(model))
	for _, suffix := range []string{"-none", "-minimal", "-low", "-medium", "-high", "-xhigh", "-extrahigh", "-max"} {
		if strings.HasSuffix(lower, suffix) {
			return true
		}
	}
	return false
}

func newClaudeCodeAITool(id string, path string, prompt string, model string, effort string, resumeSessionID string, newSessionID ...string) *agentAITool {
	return newClaudeCodeAIToolForOS(runtime.GOOS, id, path, prompt, model, effort, resumeSessionID, newSessionID...)
}

func newClaudeCodeAIToolForOS(goos string, id string, path string, prompt string, model string, effort string, resumeSessionID string, newSessionID ...string) *agentAITool {
	return newClaudeCodeAIToolForOSWithInput(goos, id, path, prompt, model, effort, resumeSessionID,
		claudeStreamJSONInputAvailable(path), newSessionID...)
}

// newClaudeCodeAIToolForOSWithInput 是 newClaudeCodeAIToolForOS 的可注入版本：
// streamInput 为 true 时启用 `--input-format stream-json`，prompt 改由
// runCLIPass 写入常驻 stdin 管道（这是 Claude steer 的物理前提）；为 false
// 时保持历史行为（prompt 走 argv，或 Windows .cmd shim 走一次性 stdin）。

func newClaudeCodeAIToolForOSWithInput(goos string, id string, path string, prompt string, model string, effort string, resumeSessionID string, streamInput bool, newSessionID ...string) *agentAITool {
	args := claudeCodeHeadlessSlimArgs()
	args = append(args, "--print", "--verbose", "--output-format", "stream-json", "--include-partial-messages")
	if streamInput {
		// 双向 stream-json：stdin 保持打开，后续 steer 以 JSON 行追加。
		args = append(args, "--input-format", "stream-json")
	}
	args = append(args, "--append-system-prompt", agentAIOptionSystemPrompt)
	if resumeSessionID != "" {
		args = append(args, "--resume", resumeSessionID)
	} else if len(newSessionID) > 0 && strings.TrimSpace(newSessionID[0]) != "" {
		args = append(args, "--session-id", strings.TrimSpace(newSessionID[0]))
	}
	if model != "" {
		args = append(args, "--model", model)
	}
	if effort = strings.TrimSpace(effort); effort != "" {
		args = append(args, "--effort", effort)
	}
	promptViaStdin := claudePromptViaStdinForOS(goos, path)
	if !streamInput && !promptViaStdin {
		args = append(args, prompt)
	}
	tool := &agentAITool{
		id:           id,
		path:         path,
		args:         args,
		outputFormat: agentAIOutputClaudeStreamJSON,
	}
	// stdinPrompt 在两条 stdin 路径上都保存原始 prompt：附件/只读等包装器
	// 靠它判断「末位不是位置参数」，并把附件清单拼进同一份 stdin 文本。
	if streamInput || promptViaStdin {
		tool.stdinPrompt = prompt
	}
	if streamInput {
		tool.stdinStreamJSON = true
	}
	return tool
}

var claudeAutoCompactProbeCache sync.Map

// claudeAutoCompactAvailable reports whether this claude binary understands
// `--autocompact`. Probe failure counts as unsupported: an unknown flag would
// make every turn fail to start, so an older CLI keeps its built-in window.
// The cache key includes symlink target + mtime + size
// (executableProbeCacheKey), so an upgrade invalidates it automatically.

func claudeAutoCompactAvailable(path string) bool {
	path = strings.TrimSpace(path)
	if path == "" {
		return false
	}
	cacheKey := executableProbeCacheKey(path)
	if cached, ok := claudeAutoCompactProbeCache.Load(cacheKey); ok {
		return cached.(bool)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	output, err := newBackgroundCommandContext(ctx, path, "--help").CombinedOutput()
	available := err == nil && strings.Contains(string(output), "--autocompact")
	claudeAutoCompactProbeCache.Store(cacheKey, available)
	return available
}

// withClaudeAutoCompact pins the CLI's auto-compaction window to the context
// size chosen on the phone.
//
// Without it the CLI derives the window from the model name (a "[1m]" tag
// claims 1M tokens even when the gateway window is ~262k), and compaction then
// fires too late — the request dies with "Prompt is too long" instead. The flag
// takes tokens directly (100k–1M). Old binaries silently skip it.

func withClaudeAutoCompact(tool *agentAITool, contextWindow int) *agentAITool {
	if tool == nil || len(tool.args) == 0 {
		return tool
	}
	if tool.outputFormat != agentAIOutputClaudeStreamJSON {
		return tool
	}
	contextWindow = clampAgentAIContextWindow(contextWindow)
	if contextWindow <= 0 || !claudeAutoCompactAvailable(tool.path) {
		return tool
	}
	copied := *tool
	copied.args = append([]string(nil), tool.args...)
	flags := []string{"--autocompact", strconv.Itoa(contextWindow)}
	// prompt 走 stdin 时 args 末位不是位置参数：flags 直接追加尾部。
	if copied.stdinPrompt != "" {
		copied.args = append(copied.args, flags...)
		return &copied
	}
	promptIndex := len(copied.args) - 1
	copied.args = append(copied.args[:promptIndex], append(flags, copied.args[promptIndex])...)
	return &copied
}

// claudePromptViaStdinForOS 报告该平台/路径下 prompt 是否必须走 stdin 而非
// argv。Windows 批处理 shim(.cmd/.bat)经 cmd.exe /c 调起：即使 Go 已按 cmd.exe
// 规则转义，%VAR% 展开与引号/脱字符的边缘序列仍会重切或污染自由文本参数——
// 用户 prompt 不可信，不允许进 argv。claude --print 原生支持从 stdin 读提示词，
// 语义与位置参数完全一致。

func claudePromptViaStdinForOS(goos, path string) bool {
	if goos != "windows" {
		return false
	}
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".cmd" || ext == ".bat"
}

func newOpenCodeAITool(path string, prompt string, model string, effort string, resumeSessionID string) *agentAITool {
	args := []string{"run", "--format", "json"}
	if resumeSessionID != "" {
		args = append(args, "--session", resumeSessionID)
	}
	if model != "" {
		args = append(args, "--model", model)
	}
	if effort = strings.TrimSpace(effort); effort != "" {
		args = append(args, "--variant", effort)
	}
	args = append(args, prompt)
	return &agentAITool{
		id:           "opencode",
		path:         path,
		args:         args,
		outputFormat: agentAIOutputOpenCodeJSON,
	}
}

func withAgentAIAttachments(tool *agentAITool, attachments []agentAIAttachment) *agentAITool {
	if tool == nil || len(attachments) == 0 || len(tool.args) == 0 {
		return tool
	}
	copied := *tool
	copied.args = append([]string(nil), tool.args...)
	promptIndex := len(copied.args) - 1
	prompt := copied.args[promptIndex]
	extraArgs := make([]string, 0, len(attachments)*2)
	attachedPaths := make(map[string]struct{})
	for _, attachment := range attachments {
		switch tool.outputFormat {
		case agentAIOutputCodexJSON:
			if attachment.Type == "image" && attachment.Path != "" {
				extraArgs = append(extraArgs, "--image", attachment.Path)
				attachedPaths[attachment.Path] = struct{}{}
			}
		case agentAIOutputOpenCodeJSON:
			if attachment.Path != "" {
				extraArgs = append(extraArgs, "--file", attachment.Path)
				attachedPaths[attachment.Path] = struct{}{}
			}
		case agentAIOutputClaudeStreamJSON:
			// 图片走 stdin 的 image block（buildClaudeImageBlocks），不再列进
			// prompt 的附件清单——同一张图既给 block 又给路径会让模型重复解读。
			if attachment.Type == "image" && attachment.Path != "" && tool.stdinStreamJSON {
				attachedPaths[attachment.Path] = struct{}{}
			}
		}
	}
	remaining := make([]agentAIAttachment, 0, len(attachments))
	for _, attachment := range attachments {
		if _, attached := attachedPaths[attachment.Path]; !attached {
			remaining = append(remaining, attachment)
		}
	}
	// prompt 走 stdin 时附件清单必须拼进 stdinPrompt——拼到 args 末位会把
	// 附件文本叠到最后一个 flag 值上（--resume id / --effort 等）。
	if tool.stdinPrompt != "" {
		copied.stdinPrompt = tool.stdinPrompt + agentAIAttachmentPromptSuffix(remaining, true)
		copied.args = append(copied.args, extraArgs...)
		return &copied
	}
	prompt += agentAIAttachmentPromptSuffix(remaining, true)
	copied.args = append(copied.args[:promptIndex], append(extraArgs, prompt)...)
	return &copied
}

func agentAIAttachmentPromptSuffix(attachments []agentAIAttachment, includeImages bool) string {
	if len(attachments) == 0 {
		return ""
	}
	var lines []string
	var texts []agentAIAttachment
	for _, attachment := range attachments {
		// 内联文本素材没有位置可列，正文直接内联（见下）。带 Path 的
		// Type=="text" 是「文本文件」，仍然走位置清单。
		if attachment.Text != "" {
			if strings.TrimSpace(attachment.Text) != "" {
				texts = append(texts, attachment)
			}
			continue
		}
		if !includeImages && attachment.Type == "image" {
			continue
		}
		location := firstNonEmpty(attachment.Path, attachment.URL)
		if location == "" {
			continue
		}
		name := firstNonEmpty(attachment.Name, filepath.Base(attachment.Path), "attachment")
		lines = append(lines, fmt.Sprintf("- %s: %s", name, location))
	}
	var b strings.Builder
	if len(lines) > 0 {
		b.WriteString("\n\nAttachments available for this request:\n")
		b.WriteString(strings.Join(lines, "\n"))
	}
	for _, attachment := range texts {
		name := firstNonEmpty(attachment.Name, "attachment.txt")
		// 用围栏包住正文：模型按等宽读，ASCII 图的列对齐才不会被当成普通段落
		// 重排。围栏长度取正文里最长反引号串 +1，避免正文自带 ``` 提前闭合。
		fence := strings.Repeat("`", longestBacktickRun(attachment.Text)+1)
		b.WriteString("\n\nAttachment ")
		b.WriteString(strconv.Quote(name))
		b.WriteString(" (text):\n")
		b.WriteString(fence)
		b.WriteString("\n")
		b.WriteString(attachment.Text)
		b.WriteString("\n")
		b.WriteString(fence)
	}
	return b.String()
}

// longestBacktickRun 返回 s 中最长的连续反引号长度（用于挑一个不会提前闭合的
// Markdown 围栏）。至少为 2，保证围栏永远是 ``` 起。

func longestBacktickRun(s string) int {
	longest, current := 0, 0
	for _, r := range s {
		if r == '`' {
			current++
			if current > longest {
				longest = current
			}
			continue
		}
		current = 0
	}
	if longest < 2 {
		return 2
	}
	return longest
}

const (
	claudeCodeHeadlessDefaultTools = "Bash,Edit,Glob,Grep,Read,Write"
	claudeCodeHeadlessEmptyMCP     = `{"mcpServers":{}}`
)

func claudeCodeHeadlessSlimArgs() []string {
	if !claudeCodeHeadlessSlimEnabled() {
		return nil
	}
	tools := strings.TrimSpace(os.Getenv("ALIANG_CLAUDE_HEADLESS_TOOLS"))
	if tools == "" {
		tools = claudeCodeHeadlessDefaultTools
	}
	args := []string{"--tools", tools}
	if truthyEnv("ALIANG_CLAUDE_HEADLESS_ENABLE_MCP") {
		return args
	}
	return append(args, "--strict-mcp-config", "--mcp-config", claudeCodeHeadlessEmptyMCP)
}

// claudeCodeHeadlessSlimEnabled reports whether headless "slim" mode is enabled.
//
// 永久停用: slim 会用 --tools 把 built-in tools 砍到 6 个 + 用
// --strict-mcp-config --mcp-config {} 清空 MCP, 导致 headless 启动的 tools
// 集合远少于交互式 TUI(实测 6 vs 24+), 被下游网关判定为"非标准 Claude
// Code"(openclaw)。已验证停用后 tools 恢复到完整 built-in 集。故无视
// ALIANG_CLAUDE_HEADLESS_SLIM, 一律不启用。若将来确需精简模式, 恢复下方
// 读 env 的 switch 即可。

func claudeCodeHeadlessSlimEnabled() bool {
	return false
}

func truthyEnv(key string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "1", "true", "yes", "on", "enable", "enabled":
		return true
	default:
		return false
	}
}

type claudeCodeVersion struct {
	major int
	minor int
	patch int
}

func (v claudeCodeVersion) atLeast(major, minor, patch int) bool {
	if v.major != major {
		return v.major > major
	}
	if v.minor != minor {
		return v.minor > minor
	}
	return v.patch >= patch
}

func parseClaudeCodeVersion(raw string) (claudeCodeVersion, bool) {
	matches := claudeCodeVersionRe.FindStringSubmatch(raw)
	if len(matches) != 4 {
		return claudeCodeVersion{}, false
	}
	major, errMajor := strconv.Atoi(matches[1])
	minor, errMinor := strconv.Atoi(matches[2])
	patch, errPatch := strconv.Atoi(matches[3])
	if errMajor != nil || errMinor != nil || errPatch != nil {
		return claudeCodeVersion{}, false
	}
	return claudeCodeVersion{major: major, minor: minor, patch: patch}, true
}

// 信任档位(sanitized/full)放行的最低 claude 版本:官方在 2.1.268 修复
// headless --print 不触发 PermissionRequest 的缺陷(CHANGELOG 2.1.268)。
// 档位守卫(applyClaudeTierVersionGuard)、审批钩子策略选择(本文件)与
// slash 列表门(agent_slash_commands.go)三处必须共用此常量——2026-10-16
// 评审曾发现守卫放宽而钩子策略仍卡旧阈值的三门漂移(full 档加载用户 ask
// 规则却接 PreToolUse 桥,手机批准后仍被拒)。旧值 2.2.0 从未发布,是永假门。

const (
	claudeTierMinMajor = 2
	claudeTierMinMinor = 1
	claudeTierMinPatch = 268
	// claudeKnownGood* 及以上修复了 <2.1.289 的审批绕过缺口(复合命令嵌套/
	// env 前缀展开等),更早版本放行信任档时告警建议升级。
	claudeKnownGoodMajor = 2
	claudeKnownGoodMinor = 1
	claudeKnownGoodPatch = 289
)

func claudeApprovalHookStrategyForVersion(raw string) claudeApprovalHookStrategy {
	version, ok := parseClaudeCodeVersion(raw)
	// 2.1.268 前的构建不触发 PermissionRequest(2.1.17 验证),只响应
	// PreToolUse 命令钩子;2.1.268 起遵循现行 PermissionRequest/http 契约。
	if ok && version.atLeast(claudeTierMinMajor, claudeTierMinMinor, claudeTierMinPatch) {
		return claudeApprovalHookPermissionRequestHTTP
	}
	return claudeApprovalHookPreToolUseCommand
}

func claudeApprovalHookStrategyOverride(raw string) (claudeApprovalHookStrategy, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "pretool", "pretooluse", "pretool_command", "pre_tool_use_command", "legacy":
		return claudeApprovalHookPreToolUseCommand, true
	case "permissionrequest", "permission_request", "permission_request_http", "http", "modern":
		return claudeApprovalHookPermissionRequestHTTP, true
	default:
		return "", false
	}
}

func detectClaudeApprovalHookStrategy(toolPath string) claudeApprovalHookStrategy {
	if strategy, ok := claudeApprovalHookStrategyOverride(os.Getenv("ALIANG_CLAUDE_APPROVAL_HOOK")); ok {
		return strategy
	}
	raw, ok := probeClaudeRawVersion(toolPath)
	if !ok {
		return claudeApprovalHookPreToolUseCommand
	}
	return claudeApprovalHookStrategyForVersion(raw)
}

func claudeApprovalHookTimeoutSeconds(approvalTimeout time.Duration) int64 {
	if approvalTimeout <= 0 {
		approvalTimeout = time.Second
	}
	seconds := int64(approvalTimeout / time.Second)
	if approvalTimeout%time.Second != 0 {
		seconds++
	}
	return seconds + int64(claudeApprovalHookTimeoutGrace/time.Second)
}

// claudeAuthEnvOverlay returns the gateway auth keys headless claude runs need
// but did not inherit, sourced from the user's <agentHome>/.claude/settings.json
// env block (the canonical token location since quick_setup fa84f42). Only keys
// missing (or empty) in the effective child-process environment are returned —
// an env-provided value always wins. Isolated-tier runs pass --setting-sources ""
// so claude never loads that settings file itself; a GUI-spawned agent carries no
// shell-profile exports, so without this overlay its headless children have zero
// credentials and claude fails locally with "Please run /login". Missing or
// unparseable settings yield no overlay (tolerated, matching claudeEnabledPlugins'
// fail-closed posture); only the two auth keys are ever copied.
