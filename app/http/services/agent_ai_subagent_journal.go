package services

// 子 agent 终态帧断线暂存（2026-11-10 P2 #4）。
//
// 背景（17-agent 全链审计 CONFIRMED）：run 进行中 WS 断窗内的 ai.subagent
// 终态帧在写侧被吞（agent_remote_ws writeJSON 间接层断连即错，透传/finalize
// 路径 `_ =` 丢弃），重连后没有任何补发通道——completed/failed 帧丢失=服务端
// 该行永卡 started/progress，手机永转 spinner。与 run 终态
// rememberPendingTerminal（持久化 journal+ACK 清账）不同：子 agent 帧服务端
// 本就幂等 upsert（(session,msg,task) 单行+终态防回退），这里取「纯内存暂存
// + 重连 hello 重放 + 写成功即清」的轻量账——重放成功这一动作本身就是清账凭
// 据，无需专门 ACK。进程崩溃场景 registry 同亡，靠服务端详情合成兜底（
// synthesizeSubagentEnvelopes）；持久化 journal 留作 crash-recovery 落地时的
// fast-follow。

import (
	"strings"
)

// agentAIPendingSubagentCap 封顶：workflow 扇出 20+ 也远够；超限丢最旧（最旧
// 的断窗帧其会话大概率已被详情合成兜底覆盖，新帧价值更高）。
const agentAIPendingSubagentCap = 128

func agentAIPendingSubagentKey(payload map[string]interface{}) string {
	session := strings.TrimSpace(remoteString(payload, "session_id"))
	task := strings.TrimSpace(remoteString(payload, "task_id"))
	return session + "|" + task
}

// rememberPendingSubagentTerminal 暂存一帧终态（同键覆盖=新终态胜，终态防回
// 退在服务端同样兜底）。封顶时丢最旧。
func (m *agentAIManager) rememberPendingSubagentTerminal(payload map[string]interface{}) {
	key := agentAIPendingSubagentKey(payload)
	if strings.Trim(key, "|") == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.pendingSubagentTerminals[key]; !exists {
		m.pendingSubagentOrder = append(m.pendingSubagentOrder, key)
	}
	m.pendingSubagentTerminals[key] = payload
	for len(m.pendingSubagentOrder) > agentAIPendingSubagentCap {
		oldest := m.pendingSubagentOrder[0]
		m.pendingSubagentOrder = m.pendingSubagentOrder[1:]
		delete(m.pendingSubagentTerminals, oldest)
	}
}

// replayPendingSubagentTerminals 把暂存终态逐帧补发；写成功即清账（重放通道
// 只在重连 hello 后触发，此刻写失败=对端仍不可达，保留待下一次 hello）。
func (m *agentAIManager) replayPendingSubagentTerminals(write agentTerminalWriter) {
	if m == nil || write == nil {
		return
	}
	m.mu.Lock()
	keys := make([]string, len(m.pendingSubagentOrder))
	copy(keys, m.pendingSubagentOrder)
	values := make([]map[string]interface{}, 0, len(keys))
	for _, key := range keys {
		if payload, ok := m.pendingSubagentTerminals[key]; ok {
			values = append(values, payload)
		}
	}
	m.mu.Unlock()
	for _, payload := range values {
		if err := write(payload); err == nil {
			m.mu.Lock()
			key := agentAIPendingSubagentKey(payload)
			delete(m.pendingSubagentTerminals, key)
			for i, k := range m.pendingSubagentOrder {
				if k == key {
					m.pendingSubagentOrder = append(m.pendingSubagentOrder[:i], m.pendingSubagentOrder[i+1:]...)
					break
				}
			}
			m.mu.Unlock()
		}
	}
}

// pendingSubagentTerminalCount 诊断面：当前暂存量。
func (m *agentAIManager) pendingSubagentTerminalCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.pendingSubagentTerminals)
}
