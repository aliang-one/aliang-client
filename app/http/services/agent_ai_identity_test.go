package services

import (
	"os"
	"strings"
	"testing"

	"aliang.one/nursorgate/app/http/models"
)

func TestClaudeCodeBindingArgsAreMutuallyExclusive(t *testing.T) {
	tool := newClaudeCodeAITool("claudecode", "/bin/claude", "hello", "", "", "", "new-native-id")
	args := strings.Join(tool.args, " ")
	if !strings.Contains(args, "--session-id new-native-id") {
		t.Fatalf("new-session args = %q, want --session-id", args)
	}
	if strings.Contains(args, "--resume") {
		t.Fatalf("new-session args = %q, must not contain --resume", args)
	}

	tool = newClaudeCodeAITool("claudecode", "/bin/claude", "hello", "", "", "existing-native-id", "ignored-new-id")
	args = strings.Join(tool.args, " ")
	if !strings.Contains(args, "--resume existing-native-id") {
		t.Fatalf("resume args = %q, want --resume", args)
	}
	if strings.Contains(args, "--session-id") {
		t.Fatalf("resume args = %q, must not contain --session-id", args)
	}
}

func TestRunStartRedeliveryLaunchesProviderOnce(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	projectPath := setupAgentExecutionProjectForTest(t)
	setAgentAuthorizedExecutionDirectoriesCache([]string{projectPath})
	binDir := t.TempDir()
	counterPath := t.TempDir() + "/launches"
	t.Setenv("COUNTER_FILE", counterPath)
	writeFakeExecutable(t, binDir, "claude", `#!/bin/sh
if [ "$1" = "--version" ]; then
  printf '%s\n' '2.1.17 (Claude Code)'
  exit 0
fi
if [ "$1" = "--help" ]; then
  # 2.1.17 一代没有 --input-format：能力探测据此判定不支持 Claude steer。
  printf '%s\n' 'Usage: claude [options] [prompt]'
  printf '%s\n' '  --print                        Print response and exit'
  printf '%s\n' '  --output-format <format>       text | json | stream-json'
  exit 0
fi
printf x >> "$COUNTER_FILE"
printf '%s\n' '{"type":"system","subtype":"init","session_id":"native-claude-1"}'
printf '%s\n' '{"type":"assistant","message":{"content":[{"type":"text","text":"ok"}]}}'
printf '%s\n' '{"type":"result","subtype":"success","session_id":"native-claude-1","result":"ok"}'
`)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	manager := newAgentAIManager()
	defer manager.closeAll()
	mu, events, writer := captureAIWriter(t)
	start := map[string]interface{}{
		"type":         "ai.run.start",
		"session_id":   "managed-conversation-1",
		"run_id":       "run-idempotent-1",
		"message_id":   "message-idempotent-1",
		"provider":     "claude",
		"project_path": projectPath,
		"content":      "hello",
	}
	manager.runStart(start, writer)
	waitForAgentEvent(t, mu, events, "ai.done", func(event map[string]interface{}) bool {
		return remoteString(event, "run_id") == "run-idempotent-1"
	})

	manager.runStart(start, writer)
	raw, err := os.ReadFile(counterPath)
	if err != nil {
		t.Fatalf("read provider launch counter: %v", err)
	}
	if got := string(raw); got != "x" {
		t.Fatalf("provider launches = %d, want 1", len(got))
	}
}

// 绑定守卫回归（2026-10-27 native session id mismatch 事故）：绑定已 confirmed
// 为 claudecode 时，provider=agentcore 的 ai.run.start（服务端设备推断翻转的
// 形态）必须被强制改回绑定的 provider——否则内核 id 会被喂给 claude --resume，
// confirmBinding mismatch 后会话从这一回合起永久报错。断言走的是 claude CLI
// （fake 可执行 + --resume 绑定 id）而不是内核分流。
func TestRunStartProviderForcedToConfirmedBinding(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	projectPath := setupAgentExecutionProjectForTest(t)
	setAgentAuthorizedExecutionDirectoriesCache([]string{projectPath})
	binDir := t.TempDir()
	argsPath := t.TempDir() + "/args"
	t.Setenv("ARGS_FILE", argsPath)
	writeFakeExecutable(t, binDir, "claude", `#!/bin/sh
if [ "$1" = "--version" ]; then
  printf '%s\n' '2.1.17 (Claude Code)'
  exit 0
fi
printf '%s\n' "$@" >> "$ARGS_FILE"
printf '%s\n' '{"type":"system","subtype":"init","session_id":"native-claude-1"}'
printf '%s\n' '{"type":"assistant","message":{"content":[{"type":"text","text":"ok"}]}}'
printf '%s\n' '{"type":"result","subtype":"success","session_id":"native-claude-1","result":"ok"}'
`)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	manager := newAgentAIManager()
	defer manager.closeAll()
	manager.bindings["managed-conversation-1"] = agentAIBindingRecord{
		ConversationID:  "managed-conversation-1",
		Provider:        "claudecode",
		NativeSessionID: "native-claude-1",
		State:           "confirmed",
		BindingVersion:  1,
	}
	mu, events, writer := captureAIWriter(t)
	start := map[string]interface{}{
		"type":         "ai.run.start",
		"session_id":   "managed-conversation-1",
		"run_id":       "run-force-1",
		"message_id":   "message-force-1",
		"provider":     "agentcore",
		"project_path": projectPath,
		"content":      "hello",
	}
	manager.runStart(start, writer)
	waitForAgentEvent(t, mu, events, "ai.done", func(event map[string]interface{}) bool {
		return remoteString(event, "run_id") == "run-force-1"
	})

	raw, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatalf("read claude args: %v", err)
	}
	args := string(raw)
	if !strings.Contains(args, "--resume\nnative-claude-1") {
		t.Fatalf("claude args = %q, want --resume native-claude-1 (guard must force provider back to the confirmed binding)", args)
	}
	if strings.Contains(args, "--session-id") {
		t.Fatalf("claude args = %q, must not pre-reserve a new session id", args)
	}
}

// v2 面绑定族守卫回归（Task G3）：裸 ai.message 带来的外来 provider（服务端
// 设备推断翻转 / 客户端误带 / 老服务端）不得把 agentcore 会话改派给 claude
// CLI 路径——修复前 message() 仅 normalize 无守卫，翻转会被执行路径分派真的
// 跑成 claude（内核原生 id 被喂给 --resume，confirmBinding mismatch 后会话
// 从这一回合起永久报错）。已存在会话形态：会话在内存、绑定 confirmed。
func TestMessageProviderForcedToConfirmedBindingAgentcore(t *testing.T) {
	setTestAgentHome(t) // journal 根在 newAgentAIManager 构造时解析
	up, srv := newAgentCoreUpstream(t, agentCoreTextTurn("你好", "，世界"))
	t.Setenv("ANTHROPIC_BASE_URL", srv.URL)
	t.Setenv("ANTHROPIC_MODEL", "")
	defer func() { _ = up }()

	// 防真机 PATH 上的 claude 被测试拉起：预置假 claude 计数。修复前翻转会把
	// 假 claude 跑起来（计数>0），修复后必须保持零。
	projectPath := setupAgentExecutionProjectForTest(t)
	setAgentAuthorizedExecutionDirectoriesCache([]string{projectPath})
	binDir := t.TempDir()
	counterPath := t.TempDir() + "/launches"
	t.Setenv("COUNTER_FILE", counterPath)
	writeFakeExecutable(t, binDir, "claude", `#!/bin/sh
if [ "$1" = "--version" ]; then
  printf '%s\n' '2.1.17 (Claude Code)'
  exit 0
fi
printf x >> "$COUNTER_FILE"
printf '%s\n' '{"type":"system","subtype":"init","session_id":"native-claude-x"}'
printf '%s\n' '{"type":"assistant","message":{"content":[{"type":"text","text":"ok"}]}}'
printf '%s\n' '{"type":"result","subtype":"success","session_id":"native-claude-x","result":"ok"}'
`)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	m := newAgentAIManager()
	defer m.closeAll()
	const sid = "s-msg-guard-agentcore"
	m.mu.Lock()
	m.bindings[sid] = agentAIBindingRecord{
		ConversationID:  sid,
		Provider:        "agentcore",
		NativeSessionID: "agentcore-444-1",
		State:           "confirmed",
		BindingVersion:  1,
	}
	m.sessions[sid] = &agentAISession{
		id:              sid,
		provider:        "agentcore",
		model:           "glm-5.2",
		resumeSessionID: "agentcore-444-1",
		history:         []agentAIMessage{{Role: "user", Content: "打个招呼"}},
	}
	m.mu.Unlock()

	mu, events, writer := captureAIWriter(t)
	m.message(map[string]interface{}{
		"session_id":   sid,
		"message_id":   "msg-guard-1",
		"content":      "再打个招呼",
		"provider":     "claude_code",
		"model":        "glm-5.2",
		"project_path": projectPath,
	}, writer)

	waitForAgentEvent(t, mu, events, models.AgentEventAIDone, func(event map[string]interface{}) bool {
		return remoteString(event, "run_id") == "msg-guard-1"
	})
	if got := len(up.requests()); got != 1 {
		t.Fatalf("内核上游请求数 = %d, want 1（守卫必须把执行改派回 agentcore 内核）", got)
	}
	if raw, err := os.ReadFile(counterPath); err == nil && len(raw) > 0 {
		t.Fatalf("claude 被拉起 %d 次，绑定族守卫未生效", len(raw))
	}
	mu.Lock()
	var errs []map[string]interface{}
	for _, f := range *events {
		if tp, _ := f["type"].(string); tp == models.AgentEventAIError {
			errs = append(errs, f)
		}
	}
	mu.Unlock()
	if len(errs) != 0 {
		t.Fatalf("ai.error 数量 = %d, want 0（守卫只改写 provider 不报错）: %v", len(errs), errs)
	}
}

// 守卫对称性（Task G3）：绑定 confirmed 为 claudecode 的会话收到
// provider=agentcore 的 ai.message，必须仍走 claude CLI 并 --resume 绑定的
// 原生 id——与 runStart 守卫（TestRunStartProviderForcedToConfirmedBinding）
// 同一断言面，只是入口换成裸 ai.message。
func TestMessageProviderForcedToConfirmedBindingClaude(t *testing.T) {
	setTestAgentHome(t)
	up, srv := newAgentCoreUpstream(t, agentCoreTextTurn("不该", "被调用"))
	t.Setenv("ANTHROPIC_BASE_URL", srv.URL)
	t.Setenv("ANTHROPIC_MODEL", "")
	defer func() { _ = up }()

	projectPath := setupAgentExecutionProjectForTest(t)
	setAgentAuthorizedExecutionDirectoriesCache([]string{projectPath})
	binDir := t.TempDir()
	argsPath := t.TempDir() + "/args"
	t.Setenv("ARGS_FILE", argsPath)
	writeFakeExecutable(t, binDir, "claude", `#!/bin/sh
if [ "$1" = "--version" ]; then
  printf '%s\n' '2.1.17 (Claude Code)'
  exit 0
fi
printf '%s\n' "$@" >> "$ARGS_FILE"
printf '%s\n' '{"type":"system","subtype":"init","session_id":"native-claude-1"}'
printf '%s\n' '{"type":"assistant","message":{"content":[{"type":"text","text":"ok"}]}}'
printf '%s\n' '{"type":"result","subtype":"success","session_id":"native-claude-1","result":"ok"}'
`)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	m := newAgentAIManager()
	defer m.closeAll()
	const sid = "s-msg-guard-claude"
	m.mu.Lock()
	m.bindings[sid] = agentAIBindingRecord{
		ConversationID:  sid,
		Provider:        "claudecode",
		NativeSessionID: "native-claude-1",
		State:           "confirmed",
		BindingVersion:  1,
	}
	m.sessions[sid] = &agentAISession{
		id:              sid,
		provider:        "claudecode",
		model:           "glm-5.2",
		resumeSessionID: "native-claude-1",
		history:         []agentAIMessage{{Role: "user", Content: "hello"}},
	}
	m.mu.Unlock()

	mu, events, writer := captureAIWriter(t)
	m.message(map[string]interface{}{
		"session_id":   sid,
		"message_id":   "msg-guard-2",
		"content":      "hello again",
		"provider":     "agentcore",
		"model":        "glm-5.2",
		"project_path": projectPath,
	}, writer)

	waitForAgentEvent(t, mu, events, models.AgentEventAIDone, func(event map[string]interface{}) bool {
		return remoteString(event, "run_id") == "msg-guard-2"
	})
	raw, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatalf("read claude args: %v（守卫必须把执行改派回 claude CLI）", err)
	}
	args := string(raw)
	if !strings.Contains(args, "--resume\nnative-claude-1") {
		t.Fatalf("claude args = %q, want --resume native-claude-1", args)
	}
	if strings.Contains(args, "--session-id") {
		t.Fatalf("claude args = %q, must not pre-reserve a new session id", args)
	}
	if got := len(up.requests()); got != 0 {
		t.Fatalf("内核上游请求数 = %d, want 0（翻转不得把 claude 会话改派给内核）", got)
	}
}

func TestManagedInventoryUsesBindingAlias(t *testing.T) {
	manager := newAgentAIManager()
	manager.bindings["ai-canonical"] = agentAIBindingRecord{
		ConversationID:  "ai-canonical",
		Provider:        "claudecode",
		NativeSessionID: "native-claude-1",
		State:           "reserved",
		BindingVersion:  1,
	}
	sessions := manager.annotateManagedVibeSessions([]models.AgentVibeSession{
		{ID: "claude_native-claude-1", Provider: "claude"},
		{ID: "claude_external-1", Provider: "claude"},
	})
	if sessions[0].Origin != "managed" || sessions[0].ManagedConversationID != "ai-canonical" {
		t.Fatalf("managed inventory = %+v", sessions[0])
	}
	if sessions[1].Origin != "external" || sessions[1].ManagedConversationID != "" {
		t.Fatalf("external inventory = %+v", sessions[1])
	}
}
