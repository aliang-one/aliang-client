package services

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func goalScopeHookSession(t *testing.T, manager *agentAIManager, projectPath string,
	goalIdentity map[string]interface{}, roots, commands []string) {
	t.Helper()
	_, _, writer := captureAIWriter(t)
	_, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	manager.sessions["ai_goal_scope"] = &agentAISession{
		id:                  "ai_goal_scope",
		projectPath:         projectPath,
		provider:            "claudecode",
		cancel:              cancel,
		activeWriter:        writer,
		approvalToken:       "scope-token",
		runSeq:              1,
		activeRunID:         "goal-run-scope",
		goalIdentity:        goalIdentity,
		goalAllowedRoots:    roots,
		goalAllowedCommands: commands,
	}
}

func permDecision(resp map[string]interface{}) string {
	if h, ok := resp["hookSpecificOutput"].(map[string]interface{}); ok {
		return fmt.Sprint(h["permissionDecision"])
	}
	return ""
}

func TestApprovalHookGoalScopeEnforcement(t *testing.T) {
	setupAgentPolicyTestEnv(t)
	projectPath := setupAgentExecutionProjectForTest(t)
	manager := newAgentAIManager()
	svc := &AgentService{ai: manager}
	manager.service = svc
	defer manager.closeAll()

	goalIdent := map[string]interface{}{"goal_id": "g1", "goal_run_id": "goal-run-scope"}
	// 每个 case 重置 session（cancel/writer 一次性），allowedRoots=projectPath/src, allowedCommands=npm
	call := func(tool string, toolInput map[string]interface{}) map[string]interface{} {
		goalScopeHookSession(t, manager, projectPath, goalIdent, []string{projectPath + "/src"}, []string{"npm"})
		resp, err := manager.handleClaudeApprovalHook(
			context.Background(), "ai_goal_scope", "assistant_goal:goal-run-scope", "scope-token",
			map[string]interface{}{"hook_event_name": "PreToolUse", "tool_name": tool, "tool_input": toolInput},
		)
		if err != nil {
			t.Fatalf("hook error: %v", err)
		}
		return resp
	}

	// 1) Bash 命令不在 allowedCommands(npm) → scope deny
	if r := call("Bash", map[string]interface{}{"command": "cat /etc/passwd"}); permDecision(r) == "allow" {
		t.Errorf("out-of-scope Bash: got allow, want deny (cat not in [npm])")
	}
	// 2) Edit 路径在 allowedRoots(src) 外 → scope deny
	if r := call("Edit", map[string]interface{}{"file_path": "/etc/passwd", "old_string": "a", "new_string": "b"}); permDecision(r) == "allow" {
		t.Errorf("out-of-scope Edit: got allow, want deny (/etc/passwd outside src)")
	}
	// 3) Edit 路径在 src 内 → 不被 scope 挡
	r := call("Edit", map[string]interface{}{"file_path": projectPath + "/src/foo.ts", "old_string": "a", "new_string": "b"})
	if permDecision(r) == "deny" && strings.Contains(fmt.Sprint(r), "outside allowed roots") {
		t.Errorf("in-scope Edit wrongly scope-denied: %#v", r)
	}
}

// vibecoming 回归：goalIdentity 空 → scope 不触发（即便 session 挂了 roots）。
func TestApprovalHookGoalScopeVibeNotTriggered(t *testing.T) {
	setupAgentPolicyTestEnv(t)
	projectPath := setupAgentExecutionProjectForTest(t)
	manager := newAgentAIManager()
	svc := &AgentService{ai: manager}
	manager.service = svc
	defer manager.closeAll()
	goalScopeHookSession(t, manager, projectPath, nil, []string{projectPath + "/src"}, []string{"npm"})
	resp, err := manager.handleClaudeApprovalHook(
		context.Background(), "ai_goal_scope", "assistant_goal:goal-run-scope", "scope-token",
		map[string]interface{}{"hook_event_name": "PreToolUse", "tool_name": "Edit",
			"tool_input": map[string]interface{}{"file_path": "/etc/passwd", "old_string": "a", "new_string": "b"}},
	)
	if err != nil {
		t.Fatalf("hook error: %v", err)
	}
	if strings.Contains(fmt.Sprint(resp), "outside allowed roots") {
		t.Errorf("vibe session triggered scope enforcement (should be skipped): %#v", resp)
	}
}
