package services

import (
	"fmt"
	"os"
	"strings"

	"aliang.one/nursorgate/processor/config"
)

func agentAIDiagnosticArgs(args []string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case "--settings":
			out = append(out, "--settings", "<json>")
			i++
		case "--append-system-prompt":
			out = append(out, "--append-system-prompt", "<prompt>")
			i++
		default:
			out = append(out, arg)
		}
	}
	return out
}

// agentAIToolDiagnosticArgs 渲染 ai.run.cli 日志的 argv 诊断串。prompt 走
// stdin 时 args 末位是普通 flag 值，不得把它伪装成 prompt——改为在尾部追加
// 一个 stdin prompt 长度标注；prompt 在 argv 时保持原掩码语义。

func agentAIToolDiagnosticArgs(tool *agentAITool) []string {
	if tool == nil {
		return nil
	}
	out := agentAIDiagnosticArgs(tool.args)
	if tool.stdinPrompt != "" {
		return append(out, fmt.Sprintf("<prompt:%d chars via stdin>", len(tool.stdinPrompt)))
	}
	if len(out) > 0 && len(tool.args) > 0 {
		out[len(out)-1] = fmt.Sprintf("<prompt:%d chars>", len(tool.args[len(tool.args)-1]))
	}
	return out
}

func agentAIEnvDiagnostic() string {
	keys := []string{
		"ANTHROPIC_BASE_URL",
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC",
		"CLAUDE_CODE_SSE_PORT",
		"HTTP_PROXY",
		"HTTPS_PROXY",
		"ALL_PROXY",
		"NO_PROXY",
		"ALIANG_USER_AGENT_ADDR",
		"ALIANG_CLAUDE_APPROVAL_HOOK",
		"ALIANG_CLAUDE_HEADLESS_SLIM",
		"ALIANG_CLAUDE_HEADLESS_TOOLS",
		"ALIANG_CLAUDE_HEADLESS_ENABLE_MCP",
	}
	parts := make([]string, 0, len(keys)+1)
	for _, key := range keys {
		value, ok := os.LookupEnv(key)
		if !ok {
			parts = append(parts, key+"=<unset>")
			continue
		}
		parts = append(parts, fmt.Sprintf("%s=%q", key, value))
	}
	if token := strings.TrimSpace(os.Getenv("ANTHROPIC_AUTH_TOKEN")); token != "" {
		parts = append(parts, fmt.Sprintf("ANTHROPIC_AUTH_TOKEN=<set:%d chars>", len(token)))
	} else {
		parts = append(parts, "ANTHROPIC_AUTH_TOKEN=<unset>")
	}
	if cfg := config.GetGlobalConfig(); cfg != nil {
		if applied := len(cfg.EffectiveCustomEnvVars()); applied > 0 {
			parts = append(parts, fmt.Sprintf("CUSTOM_ENV_OVERRIDES=<applied:%d>", applied))
		}
	}
	return strings.Join(parts, " ")
}

// mergeEnvOverriding returns base with any entry whose key matches an overrides
// key removed, then appends "KEY=VALUE" for each override. The result has at most
// one entry per key, with overrides winning over the inherited values — this is
// what makes a custom env var reliably replace a same-named system variable for the
// child process (a plain append would leave both present and the winner would then
// depend on the child runtime's envp parsing).

func mergeEnvOverriding(base []string, overrides map[string]string) []string {
	if len(overrides) == 0 {
		return base
	}
	out := make([]string, 0, len(base)+len(overrides))
	for _, kv := range base {
		key := kv
		if i := strings.IndexByte(kv, '='); i >= 0 {
			key = kv[:i]
		}
		if _, drop := overrides[key]; drop {
			continue
		}
		out = append(out, kv)
	}
	for key, value := range overrides {
		out = append(out, key+"="+value)
	}
	return out
}

// agentChildProcessEnv returns the environment to pass to an AI CLI child process
// (claude/codex): the agent's inherited environment with custom env vars from the
// customer config overriding any same-named entries. It does NOT mutate the agent
// process environment (no os.Setenv), so goroutines spawning children concurrently
// stay isolated. Callers may still append tool-specific vars afterwards.

func agentChildProcessEnv() []string {
	// Windows 上合并注册表最新用户 PATH：agent 快照 PATH 缺后装 CLI 的目录，
	// npm 的 claude.cmd shim 还要靠 PATH 解析 node——不合并则面板探测判定可用
	// 而 spawn 必败（"node 不是内部或外部命令"）。非 Windows 原样返回。
	base := augmentEnvWithWindowsUserPath(os.Environ())
	// macOS/Linux：注入登录 shell PATH 快照，让 AI 子进程看到用户终端同款
	// 工具链（node/npm 等）。Windows 探针恒失败，走上面的注册表合并通道。
	base = augmentEnvWithLoginShellPath(base)
	cfg := config.GetGlobalConfig()
	if cfg == nil {
		return base
	}
	overrides := cfg.EffectiveCustomEnvVars()
	if len(overrides) == 0 {
		return base
	}
	return mergeEnvOverriding(base, overrides)
}

func agentChildProcessEnvValue(key string) (string, bool) {
	for _, entry := range agentChildProcessEnv() {
		if index := strings.IndexByte(entry, '='); index >= 0 && entry[:index] == key {
			return entry[index+1:], true
		}
	}
	return "", false
}

// agentAIContextWindowMin/Max bound the auto-compact window the phone may set.
// The claude CLI accepts 100k–1M for `--autocompact`, and the server enforces
// the same range (AliangPhoneServer server/src/modelConfig.ts), so an
// out-of-range value can only come from a stale/misbehaving client. Clamp
// instead of failing the turn.
