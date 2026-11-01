package services

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"aliang.one/nursorgate/common/logger"
)

// 审批请求投递 journal(L1 可靠投递,2026-11-01 三端一致性审计根因 1):
// 审批请求此前是 fire-and-forget WS 写——半开 socket 时 TCP 缓冲未满写恒
// 「成功」,帧静默丢失(server 无卡),agent 侧 waiter 同步空等最长 24h,重连
// 后只能靠 ai.approval.sync 让服务端走恢复路径重建(契约降级:缺自动批、缺
// supersede、推手机)。run 终态有 ai_run_terminals.json journal + ack 重放,
// 而审批请求——整个系统里最需要可靠投递的帧——没有。本文件对齐同一模式:
//
//	remember(requestApproval 发送前落盘) → 服务端 intake 回
//	ai.approval.request.ack(带幂等 outcome)→ 清除;ack 未达/连接断 →
//	registered / heartbeat-ack 时重放原始 request(服务端 intake 幂等:
//	duplicate_pending 不重复升级,already_resolved 回告知)。
//
// waiter 本体仍在 m.approvals(deliver 决策按 approval_id 命中同一 waiter),
// journal 只保证「请求帧必达服务端」。

const agentAIApprovalJournalFilename = "ai_approval_requests.json"

func agentAIApprovalJournalPath() (string, error) {
	statePath, err := agentStatePath()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(statePath), agentAIApprovalJournalFilename), nil
}

func (m *agentAIManager) persistPendingApprovalRequestsLocked() error {
	path, err := agentAIApprovalJournalPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	values := make([]map[string]interface{}, 0, len(m.pendingApprovalRequests))
	for _, payload := range m.pendingApprovalRequests {
		if copy := cloneRunTerminal(payload); copy != nil {
			values = append(values, copy)
		}
	}
	raw, err := json.MarshalIndent(values, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// rememberPendingApprovalRequest 在发送前调用(write-through journal,与
// rememberPendingTerminal 同序)。持久化失败只降级为现状(fire-and-forget),
// 不阻塞审批主链。
func (m *agentAIManager) rememberPendingApprovalRequest(payload map[string]interface{}) {
	id := strings.TrimSpace(remoteString(payload, "approval_id"))
	copy := cloneRunTerminal(payload)
	if id == "" || copy == nil {
		return
	}
	m.mu.Lock()
	if m.pendingApprovalRequests == nil {
		m.pendingApprovalRequests = make(map[string]map[string]interface{})
	}
	m.pendingApprovalRequests[id] = copy
	if m.approvalJournalEnabled {
		if err := m.persistPendingApprovalRequestsLocked(); err != nil {
			logger.Warn(fmt.Sprintf("approval-journal: persist failed id=%s error=%v", id, err))
		}
	}
	m.mu.Unlock()
}

// acknowledgePendingApprovalRequest 清除一条已确认送达的审批请求(服务端
// ai.approval.request.ack / 本地 waiter 终结均可触发)。持久化清理失败保留
// 内存副本:重放已决审批对服务端幂等(intake already_resolved 告知),后续
// ack 重试再清。
func (m *agentAIManager) acknowledgePendingApprovalRequest(approvalID string) {
	approvalID = strings.TrimSpace(approvalID)
	if approvalID == "" {
		return
	}
	m.mu.Lock()
	if _, ok := m.pendingApprovalRequests[approvalID]; ok {
		delete(m.pendingApprovalRequests, approvalID)
		if m.approvalJournalEnabled {
			if err := m.persistPendingApprovalRequestsLocked(); err != nil {
				logger.Warn(fmt.Sprintf("approval-journal: ack persist failed id=%s error=%v", approvalID, err))
			}
		}
	}
	m.mu.Unlock()
}

// replayPendingApprovalRequests 在 registered / heartbeat-ack 时把未确认的
// 审批请求原样重发。服务端 intake 以 approval_id 幂等,重放安全。
func (m *agentAIManager) replayPendingApprovalRequests(write agentTerminalWriter) {
	if write == nil {
		return
	}
	m.mu.Lock()
	if m.approvalJournalEnabled {
		// 与终端 journal 同纪律:未落盘不重放(先治愈临时写失败)。
		if err := m.persistPendingApprovalRequestsLocked(); err != nil {
			m.mu.Unlock()
			return
		}
	}
	values := make([]map[string]interface{}, 0, len(m.pendingApprovalRequests))
	for _, payload := range m.pendingApprovalRequests {
		if copy := cloneRunTerminal(payload); copy != nil {
			values = append(values, copy)
		}
	}
	m.mu.Unlock()
	if len(values) == 0 {
		return
	}
	logger.Info(fmt.Sprintf("approval-journal: replaying %d unacked approval request(s)", len(values)))
	for _, payload := range values {
		if err := write(payload); err != nil {
			logger.Warn(fmt.Sprintf("approval-journal: replay send failed id=%s error=%v", remoteString(payload, "approval_id"), err))
		}
	}
}

// loadPendingApprovalRequests 进程启动时从磁盘回灌(与 loadPendingTerminals
// 同风格):waiter 已随进程消失,重放只会得到服务端 already_resolved/duplicate
// 告知,安全收敛。
func (m *agentAIManager) loadPendingApprovalRequests() {
	m.mu.Lock()
	m.approvalJournalEnabled = true
	m.mu.Unlock()
	path, err := agentAIApprovalJournalPath()
	if err != nil {
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var values []map[string]interface{}
	if json.Unmarshal(raw, &values) != nil {
		return
	}
	m.mu.Lock()
	if m.pendingApprovalRequests == nil {
		m.pendingApprovalRequests = make(map[string]map[string]interface{})
	}
	for _, payload := range values {
		if id := strings.TrimSpace(remoteString(payload, "approval_id")); id != "" {
			m.pendingApprovalRequests[id] = payload
		}
	}
	m.mu.Unlock()
}
