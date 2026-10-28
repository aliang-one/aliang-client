package services

import (
	"fmt"
	"testing"

	"aliang.one/nursorgate/app/http/models"
)

func TestSessionApprovalMemoryHitSkipsCloud(t *testing.T) {
	m := newTestAISocketsManager(t)
	// 记忆：会话 s1 放行 command:ls -la
	m.rememberSessionApproval("s1", "claude", models.AgentAIApprovalKindCommand, "Bash", "ls -la")
	if !m.sessionApprovalAllows("s1", "claude", models.AgentAIApprovalKindCommand, "Bash", "ls -la") {
		t.Fatal("同键应命中记忆")
	}
	if m.sessionApprovalAllows("s1", "claude", models.AgentAIApprovalKindCommand, "Bash", "rm -rf /") {
		t.Fatal("不同命令不应命中")
	}
	if m.sessionApprovalAllows("s2", "claude", models.AgentAIApprovalKindCommand, "Bash", "ls -la") {
		t.Fatal("跨会话不应命中")
	}
	if m.sessionApprovalAllows("s1", "codex", models.AgentAIApprovalKindCommand, "Bash", "ls -la") {
		t.Fatal("跨 provider 不应命中")
	}
}

func TestSessionApprovalToolKindMatchesByName(t *testing.T) {
	m := newTestAISocketsManager(t)
	m.rememberSessionApproval("s1", "agentcore", models.AgentAIApprovalKindTool, "mcp__x__y", "")
	if !m.sessionApprovalAllows("s1", "agentcore", models.AgentAIApprovalKindTool, "mcp__x__y", "任何输入") {
		t.Fatal("tool 类按名匹配应命中")
	}
}

func TestSessionApprovalCapClears(t *testing.T) {
	m := newTestAISocketsManager(t)
	for i := 0; i < 100; i++ {
		m.rememberSessionApproval(fmt.Sprintf("s-%d", i), "claude", models.AgentAIApprovalKindTool, "t", "")
	}
	if len(m.sessionApprovals) > 32 {
		t.Fatalf("会话数超限未清: %d", len(m.sessionApprovals))
	}
}
