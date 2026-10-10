package services

import (
	"context"
	"testing"
)

// 评审确认的语义矛盾(2026-10-10):remoteConnectionLoop 的循环级 defer 无条件
// closeAll 终端与 AI 会话,而 registration_lost_recoverable 分支(2026-10-08
// 事故形态:enabled+有凭据+有 device_id 但内存注册态丢失)恰恰期望 keepalive
// 重注册后由新循环接管——可恢复路径不应清场,会话必须像瞬断一样存活。

func newTeardownTestService(t *testing.T, registered bool) (*AgentService, *bool, *bool) {
	t.Helper()
	t.Setenv(AgentRuntimeEnv, "1")
	svc := &AgentService{terminal: newAgentTerminalManager(), ai: newAgentAIManager()}
	svc.forwardedUserAuthorization = "Bearer test-token"
	svc.state.Enabled = true
	svc.state.DeviceID = "device-1"
	svc.state.Registered = registered
	termKilled := false
	svc.terminal.mu.Lock()
	svc.terminal.sessions["term-1"] = &agentTerminalSession{id: "term-1", killer: func() error {
		termKilled = true
		return nil
	}}
	svc.terminal.mu.Unlock()
	aiCancelled := false
	svc.ai.mu.Lock()
	svc.ai.sessions["ai-1"] = &agentAISession{cancel: context.CancelFunc(func() {
		aiCancelled = true
	})}
	svc.ai.mu.Unlock()
	return svc, &termKilled, &aiCancelled
}

func TestRemoteLoopExitCleanupRecoverableKeepsSessions(t *testing.T) {
	svc, termKilled, aiCancelled := newTeardownTestService(t, false)
	if !svc.registrationLostRecoverable() {
		t.Fatalf("前置:该状态应判定为可恢复注册丢失")
	}
	// recoverable 由循环体首次判定后传入,cleanup 内不得二次求值——
	// 保活并发重注册会把 Registered 翻回 true,二次求值会误走清场分支
	// (TOCTOU,评审 2026-10-11)。
	svc.remoteLoopExitCleanup(true)
	if *termKilled {
		t.Error("可恢复注册丢失不得杀终端会话(断线保留语义)")
	}
	if *aiCancelled {
		t.Error("可恢复注册丢失不得取消 AI 会话")
	}
	if svc.wsConnecting {
		t.Error("wsConnecting 应被清零以便重入")
	}
}

func TestRemoteLoopExitCleanupNonRecoverableClosesSessions(t *testing.T) {
	svc, termKilled, aiCancelled := newTeardownTestService(t, true)
	if svc.registrationLostRecoverable() {
		t.Fatalf("前置:已注册状态不属于可恢复注册丢失")
	}
	svc.remoteLoopExitCleanup(false)
	if !*termKilled {
		t.Error("不可恢复终态(已注册后掉线/禁用)应保留既有 closeAll 清场行为")
	}
	if !*aiCancelled {
		t.Error("不可恢复终态应取消 AI 会话")
	}
}
