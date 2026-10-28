package services

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"aliang.one/nursorgate/app/http/models"
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
//
// 2026-10-28 边界降级后，vibe 会话的越界 Edit 走策略升级（require_approval，
// rule=outside-project-path）弹手机审批，不再被 scope 或边界本地判死——所以
// hook 会阻塞等审批，测试改为 goroutine 发起、登记后替用户拒绝，断言三件事：
// (a) 审批弹窗携带的是边界规则而非 scope；(b) scope enforcement 未触发；
// (c) 拒绝后 decision 非 allow。
func TestApprovalHookGoalScopeVibeNotTriggered(t *testing.T) {
	setupAgentPolicyTestEnv(t)
	projectPath := setupAgentExecutionProjectForTest(t)
	manager := newAgentAIManager()
	svc := &AgentService{ai: manager}
	manager.service = svc
	defer manager.closeAll()
	goalScopeHookSession(t, manager, projectPath, nil, []string{projectPath + "/src"}, []string{"npm"})

	type hookResult struct {
		resp map[string]interface{}
		err  error
	}
	resultCh := make(chan hookResult, 1)
	go func() {
		resp, err := manager.handleClaudeApprovalHook(
			context.Background(), "ai_goal_scope", "assistant_goal:goal-run-scope", "scope-token",
			map[string]interface{}{"hook_event_name": "PreToolUse", "tool_name": "Edit",
				"tool_input": map[string]interface{}{"file_path": "/etc/passwd", "old_string": "a", "new_string": "b"}},
		)
		resultCh <- hookResult{resp: resp, err: err}
	}()
	waiter := waitForAgentCoreApproval(t, manager, "ai_goal_scope", &agentCoreEventLog{})
	if got := waiter.request.MatchedRuleID; got != "outside-project-path" {
		t.Fatalf("越界 Edit 审批 rule = %q, want outside-project-path（scope 不该触发）", got)
	}
	waiter.request.respond <- agentAIApprovalResponse{Decision: models.AgentAIApprovalDecisionDecline}
	select {
	case got := <-resultCh:
		if got.err != nil {
			t.Fatalf("hook error: %v", got.err)
		}
		if strings.Contains(fmt.Sprint(got.resp), "outside allowed roots") {
			t.Errorf("vibe session triggered scope enforcement (should be skipped): %#v", got.resp)
		}
		if permDecision(got.resp) == "allow" {
			t.Errorf("被拒绝的越界 Edit 不应放行: %#v", got.resp)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("替用户拒绝后 hook 没有返回")
	}
}
