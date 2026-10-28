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

func TestSessionApprovalRefusesDegenerateKeys(t *testing.T) {
	m := newTestAISocketsManager(t)
	// codex fileChange 形状（kind=file_change、无 ToolName）：拒绝记通配键，
	// 一次放行不得免审全会话所有文件变更——宁可逐次问。
	m.rememberSessionApproval("s1", "codex", models.AgentAIApprovalKindFileChange, "", "")
	if m.sessionApprovalAllows("s1", "codex", models.AgentAIApprovalKindFileChange, "", "") {
		t.Fatal("空工具名不应命中")
	}
	if len(m.sessionApprovals) != 0 {
		t.Fatalf("空工具名记录应为 no-op, 实际 %v", m.sessionApprovals)
	}
	// 空白工具名同口径拒绝。
	m.rememberSessionApproval("s1", "codex", models.AgentAIApprovalKindTool, "   ", "")
	if m.sessionApprovalAllows("s1", "codex", models.AgentAIApprovalKindTool, "   ", "") {
		t.Fatal("空白工具名不应命中")
	}
	// 正常 tool kind 不受影响。
	m.rememberSessionApproval("s1", "codex", models.AgentAIApprovalKindTool, "mcp__x__y", "")
	if !m.sessionApprovalAllows("s1", "codex", models.AgentAIApprovalKindTool, "mcp__x__y", "任意输入") {
		t.Fatal("正常 tool kind 应照常命中")
	}
}
