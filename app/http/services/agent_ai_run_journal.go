package services

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"aliang.one/nursorgate/common/logger"
)

const agentAIRunJournalFilename = "ai_run_terminals.json"

func agentAIRunSequence(value interface{}) int64 {
	switch n := value.(type) {
	case int:
		return int64(n)
	case int64:
		return n
	case float64:
		return int64(n)
	case json.Number:
		value, _ := n.Int64()
		return value
	default:
		return 0
	}
}

func agentAIRunJournalPath() (string, error) {
	statePath, err := agentStatePath()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(statePath), agentAIRunJournalFilename), nil
}

func cloneRunTerminal(payload map[string]interface{}) map[string]interface{} {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil
	}
	var out map[string]interface{}
	if json.Unmarshal(raw, &out) != nil {
		return nil
	}
	return out
}

func (m *agentAIManager) rememberPendingTerminal(payload map[string]interface{}) error {
	runID := strings.TrimSpace(remoteString(payload, "run_id"))
	copy := cloneRunTerminal(payload)
	if runID == "" || copy == nil {
		return fmt.Errorf("invalid AI run terminal payload")
	}
	m.mu.Lock()
	m.pendingTerminals[runID] = copy
	if m.runJournalEnabled {
		if err := m.persistPendingTerminalsLocked(); err != nil {
			m.mu.Unlock()
			return fmt.Errorf("persist AI run terminal: %w", err)
		}
	}
	m.mu.Unlock()
	return nil
}

func (m *agentAIManager) acknowledgePendingTerminal(runID string, acceptedSeq int64) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return
	}
	m.mu.Lock()
	if payload, ok := m.pendingTerminals[runID]; ok {
		terminalSeq := agentAIRunSequence(payload["event_seq"])
		if acceptedSeq < terminalSeq || terminalSeq <= 0 {
			m.mu.Unlock()
			return
		}
		previous := m.pendingTerminals[runID]
		delete(m.pendingTerminals, runID)
		if m.runJournalEnabled {
			if err := m.persistPendingTerminalsLocked(); err != nil {
				// Keep the in-memory copy when durable ACK cleanup fails. Replaying
				// an already-committed terminal is safe and lets a later ACK retry
				// the cleanup; deleting it here could resurrect after a crash.
				m.pendingTerminals[runID] = previous
			}
		}
	}
	m.mu.Unlock()
}

// discardPendingTerminal 对明确被服务端拒收的终态账强制清账（2026-11-17 审计
// P2）：拒收帧重放永远同样被拒，seq 防回退守卫会把它永久留在账上——每 10s 心跳
// 全量重放、账目文件无界增长。与 acknowledgePendingTerminal 的 seq 语义不同：
// 这里不看 seq，凭服务端终局裁决清账。拒收本身留日志；终态内容仍在 journal 与
// identity 台账可查。
func (m *agentAIManager) discardPendingTerminal(runID, reason string) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return
	}
	m.mu.Lock()
	if _, ok := m.pendingTerminals[runID]; ok {
		delete(m.pendingTerminals, runID)
		if m.runJournalEnabled {
			if err := m.persistPendingTerminalsLocked(); err != nil {
				// 持久化失败时内存清账保留（与 ack 路径同口径：宁重放不可复活）。
				m.mu.Unlock()
				logger.Warn(fmt.Sprintf("[ai.run.terminal] 拒收清账持久化失败 run=%s reason=%s err=%v", runID, reason, err))
				return
			}
		}
	}
	m.mu.Unlock()
	logger.Warn(fmt.Sprintf("[ai.run.terminal] 服务端拒收终态帧,清账止循环 run=%s reason=%s", runID, reason))
}

func (m *agentAIManager) pendingTerminalSnapshot() []map[string]interface{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]map[string]interface{}, 0, len(m.pendingTerminals))
	for _, payload := range m.pendingTerminals {
		if copy := cloneRunTerminal(payload); copy != nil {
			out = append(out, copy)
		}
	}
	return out
}

func (m *agentAIManager) replayPendingTerminals(write agentTerminalWriter) {
	m.mu.Lock()
	if m.runJournalEnabled {
		// Never put a terminal on the wire unless the current pending set is
		// durable. This also heals a prior temporary journal write failure.
		if err := m.persistPendingTerminalsLocked(); err != nil {
			m.mu.Unlock()
			return
		}
	}
	values := make([]map[string]interface{}, 0, len(m.pendingTerminals))
	for _, payload := range m.pendingTerminals {
		if copy := cloneRunTerminal(payload); copy != nil {
			values = append(values, copy)
		}
	}
	m.mu.Unlock()
	for _, payload := range values {
		_ = write(payload)
	}
}

func (m *agentAIManager) loadPendingTerminals() {
	m.mu.Lock()
	m.runJournalEnabled = true
	m.mu.Unlock()
	path, err := agentAIRunJournalPath()
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
	for _, payload := range values {
		if runID := strings.TrimSpace(remoteString(payload, "run_id")); runID != "" {
			m.pendingTerminals[runID] = payload
		}
	}
	m.mu.Unlock()
}

// agentAIRunJournalPathFn 是 journal 路径的可注入缝（测试用），默认走真实路径。
var agentAIRunJournalPathFn = agentAIRunJournalPath

func (m *agentAIManager) persistPendingTerminalsLocked() error {
	path, err := agentAIRunJournalPathFn()
	if err != nil {
		return err
	}
	if len(m.pendingTerminals) == 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	values := make([]map[string]interface{}, 0, len(m.pendingTerminals))
	for _, payload := range m.pendingTerminals {
		values = append(values, payload)
	}
	raw, err := json.Marshal(values)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	file, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err = file.Write(raw); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
