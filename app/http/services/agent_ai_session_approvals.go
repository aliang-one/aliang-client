package services

// 会话级批准记忆（2026-10-28 跨端同态，spec §4.2.4）：用户对某回合选
// accept_for_session 时记录 (sessionID, provider, kind, 匹配键)，本会话内
// 同键后续请求直接放行（claude/agentcore/codex 三通道统一语义；codex 同时
// 保留其原生 scope=session 行为）。内存态、随进程消失，不落策略规则。
//
// 匹配键（计划期钉死）：
//
//	kind=command → "cmd:<原命令>"（逐字匹配，不放大到全部命令）
//	其余         → "tool:<tool_name>"（按工具名）
//
// 语义优先级：goal scope 边界 deny > 设备策略 auto_approve/auto_deny >
// 会话级记忆 > require_approval 云端往返。三个检查点（claude hook /
// agentcore approve / codex app-server approval）都在 policy 判定之后、
// requestApproval 之前——显式 auto_deny 优先于会话记忆，记忆只跳过
// require_approval 的云端往返。

import (
	"strings"

	"aliang.one/nursorgate/app/http/models"
)

const (
	// 容量护栏（与 completedApprovals 同风格：超限全清，防膨胀）。内存态、
	// 随进程消失，不持久化（spec §9 已声明）。
	agentAISessionApprovalSessionCap = 32
	agentAISessionApprovalKeyCap     = 64
)

// agentAISessionApprovalKey 计算一次审批请求的会话级匹配键。
func agentAISessionApprovalKey(kind, toolName, command string) string {
	if kind == models.AgentAIApprovalKindCommand && strings.TrimSpace(command) != "" {
		return "cmd:" + strings.TrimSpace(command)
	}
	return "tool:" + toolName
}

// rememberSessionApproval 记录一次会话级放行。容量超限全清（防膨胀）。
// 自身取 m.mu——调用方必须在 m.mu.Unlock() 之后。
func (m *agentAIManager) rememberSessionApproval(sessionID, provider, kind, toolName, command string) {
	if sessionID == "" || provider == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sessionApprovals == nil {
		m.sessionApprovals = make(map[string]map[string]struct{})
	}
	if len(m.sessionApprovals) >= agentAISessionApprovalSessionCap {
		if _, alive := m.sessionApprovals[sessionID]; !alive {
			m.sessionApprovals = make(map[string]map[string]struct{})
		}
	}
	keys := m.sessionApprovals[sessionID]
	if keys == nil {
		keys = make(map[string]struct{})
		m.sessionApprovals[sessionID] = keys
	}
	if len(keys) >= agentAISessionApprovalKeyCap {
		m.sessionApprovals[sessionID] = make(map[string]struct{})
		keys = m.sessionApprovals[sessionID]
	}
	keys[provider+"\x00"+agentAISessionApprovalKey(kind, toolName, command)] = struct{}{}
}

// sessionApprovalAllows 查询会话级放行。provider 必须与记录时完全一致才算
// 命中（跨 provider 不放大）。
func (m *agentAIManager) sessionApprovalAllows(sessionID, provider, kind, toolName, command string) bool {
	if sessionID == "" || provider == "" {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	keys := m.sessionApprovals[sessionID]
	if keys == nil {
		return false
	}
	_, ok := keys[provider+"\x00"+agentAISessionApprovalKey(kind, toolName, command)]
	return ok
}
