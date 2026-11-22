package services

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"aliang.one/nursorgate/common/logger"
)

const claudeApprovalHookTimeoutGrace = 30 * time.Second

func claudeAuthEnvOverlay() map[string]string {
	authKeys := []string{"ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL"}
	missing := make(map[string]bool, len(authKeys))
	for _, key := range authKeys {
		if value, ok := agentChildProcessEnvValue(key); !ok || value == "" {
			missing[key] = true
		}
	}
	if len(missing) == 0 {
		return nil
	}
	home := agentHome()
	if home == "" {
		return nil
	}
	settingsPath := filepath.Join(home, ".claude", "settings.json")
	raw, err := os.ReadFile(settingsPath)
	if err != nil {
		return nil // absent/unreadable settings: nothing to fall back to (silent)
	}
	var settings struct {
		Env map[string]string `json:"env"`
	}
	if err := json.Unmarshal(raw, &settings); err != nil {
		logger.Warn(fmt.Sprintf("claude-auth: unparseable settings %s: %v", settingsPath, err))
		return nil
	}
	overlay := make(map[string]string, len(missing))
	for _, key := range authKeys {
		if !missing[key] {
			continue
		}
		if value := strings.TrimSpace(settings.Env[key]); value != "" {
			overlay[key] = value
		}
	}
	if len(overlay) == 0 {
		return nil
	}
	return overlay
}

func claudeApprovalHookSettings(strategy claudeApprovalHookStrategy, run agentAIRun) (map[string]interface{}, error) {
	hookURL := agentAIApprovalHookURL(run.sessionID, agentAssistantMessageID(run.messageID), run.approvalToken)
	hookTimeout := claudeApprovalHookTimeoutSeconds(agentAIApprovalTimeout)
	var settings map[string]interface{}
	switch strategy {
	case claudeApprovalHookPermissionRequestHTTP:
		settings = map[string]interface{}{
			"hooks": map[string]interface{}{
				"PermissionRequest": []interface{}{
					map[string]interface{}{
						"matcher": "*",
						"hooks": []interface{}{
							map[string]interface{}{
								"type":    "http",
								"url":     hookURL,
								"timeout": hookTimeout,
							},
						},
					},
				},
			},
		}
	default:
		settings = map[string]interface{}{
			"hooks": map[string]interface{}{
				"PreToolUse": []interface{}{
					map[string]interface{}{
						"matcher": "*",
						"hooks": []interface{}{
							map[string]interface{}{
								"type":    "command",
								"command": claudeApprovalHookCurlCommand(hookURL),
								"timeout": hookTimeout,
							},
						},
					},
				},
			},
		}
	}
	// Full tier runs at local parity: it keeps its own settings sources and
	// must not carry the fail-closed shell disable or ask overlay (hooks stay).
	if run.claudePolicy.enabled && run.claudePolicy.trustTier != "full" {
		settings["disableSkillShellExecution"] = true
		// Claude Code 2.1.x never invokes PermissionRequest hooks in headless
		// --print mode. Its explicit ask rules also take precedence over an
		// allowing PreToolUse result, so combining the two makes tools such as
		// Edit fail even after our policy hook auto-approves them. Legacy runs
		// enforce the same policy entirely in PreToolUse; modern runs keep the
		// fail-closed ask layer and resolve it through PermissionRequest.
		if strategy == claudeApprovalHookPermissionRequestHTTP {
			settings["permissions"] = map[string]interface{}{
				"ask": append([]string(nil), run.claudePolicy.permissionAsk...),
			}
		}
	}
	// 手机驱动回合(surface=phone):硬禁终端交互构件,与档位无关——本地 full
	// 档的"不打扰"约束针对本地用户;手机回合不存在本地旁观者。deny 让 CC
	// 直接拒绝 AskUserQuestion 并把原因回执给模型,模型自纠(配合 surface
	// 契约文本),无头模式不再静默悬死。
	if run.surface == agentAISurfacePhone {
		permissions, _ := settings["permissions"].(map[string]interface{})
		if permissions == nil {
			permissions = map[string]interface{}{}
			settings["permissions"] = permissions
		}
		deny, _ := permissions["deny"].([]string)
		permissions["deny"] = append(deny, "AskUserQuestion")
	}
	return settings, nil
}

func claudeApprovalHookCurlCommand(hookURL string) string {
	curl := claudeHookCurlWordForOS(runtime.GOOS, exec.LookPath)
	return fmt.Sprintf("%s -sS -X POST -H %q --data-binary @- %q", curl, "Content-Type: application/json", hookURL)
}

// claudeHookCurlWordForOS 把 curl 解析成 shell 安全的命令词。Windows 上钩子经
// Git Bash 执行（无则回退 PowerShell），而 PowerShell 5.1 的裸 curl 是
// Invoke-WebRequest 别名，-H/--data-binary 非法、钩子必败——System32 自带的
// curl.exe（Win10 1803+）是稳出口，解析绝对路径绕开别名。含空格的路径
// （Program Files 下的 Git Bash curl）在 bash 与 PowerShell 的引用/调用语义
// 无法用单一字符串两全，保持裸 curl——Git Bash 场景本就正常。非 Windows 零变化。

func claudeHookCurlWordForOS(goos string, lookPath func(string) (string, error)) string {
	if goos != "windows" {
		return "curl"
	}
	p, err := lookPath("curl")
	if err != nil {
		return "curl"
	}
	p = strings.TrimSpace(p)
	if p == "" || strings.ContainsAny(p, " \t") {
		return "curl"
	}
	// 正斜杠在 Git Bash 与 PowerShell 下都可执行；反斜杠在 bash 未引用时会
	// 被当转义符吞掉。不用 filepath.ToSlash——它按构建平台的分隔符处理，
	// darwin 上对反斜杠是 no-op，而此函数单测要在任意平台跑。
	return strings.ReplaceAll(p, `\`, `/`)
}

// marshalClaudeApprovalHookSettings 是 --settings JSON 的唯一序列化出口
// （withClaudeApprovalHook 与护栏测试共用）。必须保持 encoding/json 默认
// escapeHTML=true——它把 & < > 转成 \uXXXX，是 Windows .cmd 链路上挡住
// cmd.exe 元字符的关键防线之一（% ^ 无转义，靠内容自身约束，见
// agent_approval_hook_guard_test.go 的护栏）。改成 Encoder+SetEscapeHTML(false)
// 或手拼 JSON 会让字面 & 直达 cmd.exe；guard 测试会红，红时必须显式评审。

func marshalClaudeApprovalHookSettings(settings map[string]interface{}) ([]byte, error) {
	return json.Marshal(settings)
}

func withClaudeApprovalHook(tool *agentAITool, run agentAIRun) *agentAITool {
	if tool == nil {
		logger.Warn(fmt.Sprintf("approval-hook: withClaudeApprovalHook skipped (nil tool) session=%s", run.sessionID))
		return tool
	}
	if strings.TrimSpace(run.approvalToken) == "" {
		logger.Warn(fmt.Sprintf("approval-hook: withClaudeApprovalHook SKIPPED (empty approvalToken) session=%s runSeq=%d — claude runs with NO permission hook, commands needing approval auto-deny", run.sessionID, run.runSeq))
		return tool
	}
	strategy := detectClaudeApprovalHookStrategy(tool.path)
	logger.Info(fmt.Sprintf("approval-hook: wiring Claude hook strategy=%s session=%s runSeq=%d", strategy, run.sessionID, run.runSeq))
	settings, err := claudeApprovalHookSettings(strategy, run)
	if err != nil {
		return tool
	}
	settingsRaw, err := marshalClaudeApprovalHookSettings(settings)
	if err != nil {
		return tool
	}
	copied := *tool
	// Auth fallback rides the child environment, never argv: claudeAuthEnvOverlay
	// sources the gateway keys from <agentHome>/.claude/settings.json only for
	// keys the effective child env is missing, so cmd.Env gains no duplicates and
	// the token never appears in `ps`-visible command lines or the --settings
	// blob (which stays hooks-only and log-masked). Isolation is untouched —
	// --setting-sources still blocks all settings-file loading below.
	if overlay := claudeAuthEnvOverlay(); len(overlay) > 0 {
		keys := make([]string, 0, len(overlay))
		for key, value := range overlay {
			copied.env = append(copied.env, key+"="+value)
			keys = append(keys, key)
		}
		logger.Info(fmt.Sprintf("claude-auth: env overlay applied keys=%v session=%s runSeq=%d (values stay out of argv and logs)", keys, run.sessionID, run.runSeq))
	}
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
}

func withoutCLIArgumentValue(args []string, flag string) []string {
	out := make([]string, 0, len(args))
	for index := 0; index < len(args); index++ {
		if args[index] == flag {
			if index+1 < len(args) {
				index++
			}
			continue
		}
		out = append(out, args[index])
	}
	return out
}
