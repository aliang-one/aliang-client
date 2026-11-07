package services

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/common/logger"
)

const (
	agentAIIdentityFilename = "agent_ai_identity.json"
	agentAIProcessedRunCap  = 512
)

type agentAIBindingRecord struct {
	ConversationID  string `json:"conversation_id"`
	Provider        string `json:"provider"`
	NativeSessionID string `json:"native_session_id"`
	State           string `json:"state"`
	BindingVersion  int    `json:"binding_version"`
	UpdatedAt       string `json:"updated_at"`
}

type agentAIProcessedRun struct {
	RunID          string                 `json:"run_id"`
	ConversationID string                 `json:"conversation_id"`
	MessageID      string                 `json:"message_id"`
	State          string                 `json:"state"`
	Terminal       map[string]interface{} `json:"terminal,omitempty"`
	UpdatedAt      string                 `json:"updated_at"`
	GoalIdentity   map[string]interface{} `json:"goal_identity,omitempty"`
	Recovered      bool                   `json:"-"`
}

type agentAIIdentityState struct {
	Bindings      []agentAIBindingRecord `json:"bindings"`
	ProcessedRuns []agentAIProcessedRun  `json:"processed_runs"`
}

func agentAIIdentityPath() (string, error) {
	statePath, err := agentStatePath()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(statePath), agentAIIdentityFilename), nil
}

func (m *agentAIManager) loadIdentityState() {
	path, err := agentAIIdentityPath()
	if err != nil {
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var state agentAIIdentityState
	if json.Unmarshal(raw, &state) != nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, binding := range state.Bindings {
		if binding.ConversationID != "" && binding.NativeSessionID != "" {
			m.bindings[binding.ConversationID] = binding
		}
	}
	for _, run := range state.ProcessedRuns {
		if run.RunID != "" {
			run.Recovered = true
			m.processedRuns[run.RunID] = run
		}
	}
}

func (m *agentAIManager) persistIdentityState() error {
	m.identityPersistMu.Lock()
	defer m.identityPersistMu.Unlock()
	m.mu.Lock()
	state := agentAIIdentityState{
		Bindings:      make([]agentAIBindingRecord, 0, len(m.bindings)),
		ProcessedRuns: make([]agentAIProcessedRun, 0, len(m.processedRuns)),
	}
	for _, binding := range m.bindings {
		state.Bindings = append(state.Bindings, binding)
	}
	for _, run := range m.processedRuns {
		state.ProcessedRuns = append(state.ProcessedRuns, run)
	}
	m.mu.Unlock()
	sort.Slice(state.Bindings, func(i, j int) bool {
		return state.Bindings[i].ConversationID < state.Bindings[j].ConversationID
	})
	sort.Slice(state.ProcessedRuns, func(i, j int) bool {
		return state.ProcessedRuns[i].UpdatedAt > state.ProcessedRuns[j].UpdatedAt
	})
	if len(state.ProcessedRuns) > agentAIProcessedRunCap {
		state.ProcessedRuns = state.ProcessedRuns[:agentAIProcessedRunCap]
	}
	path, err := agentAIIdentityPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (m *agentAIManager) claimProcessedRun(runID, sessionID, messageID string, goalIdentity map[string]interface{}) (bool, error) {
	m.mu.Lock()
	if _, exists := m.processedRuns[runID]; exists {
		m.mu.Unlock()
		return false, nil
	}
	m.processedRuns[runID] = agentAIProcessedRun{
		RunID:          runID,
		ConversationID: sessionID,
		MessageID:      messageID,
		State:          "received",
		UpdatedAt:      time.Now().UTC().Format(time.RFC3339Nano),
		GoalIdentity:   cloneGoalIdentity(goalIdentity),
	}
	m.mu.Unlock()
	if err := m.persistIdentityState(); err != nil {
		m.mu.Lock()
		if current, ok := m.processedRuns[runID]; ok && current.State == "received" {
			delete(m.processedRuns, runID)
		}
		m.mu.Unlock()
		return false, err
	}
	return true, nil
}

func (m *agentAIManager) replayProcessedRun(runID string, writeJSON agentTerminalWriter) bool {
	m.mu.Lock()
	run, exists := m.processedRuns[runID]
	m.mu.Unlock()
	if !exists {
		return false
	}
	if run.Terminal != nil {
		copy := make(map[string]interface{}, len(run.Terminal))
		for key, value := range run.Terminal {
			copy[key] = value
		}
		_ = writeJSON(copy)
		return true
	}
	if run.Recovered && len(run.GoalIdentity) > 0 {
		terminal := map[string]interface{}{
			"type":       models.AgentEventAIError,
			"session_id": run.ConversationID,
			"message_id": run.MessageID,
			"run_id":     run.RunID,
			"event_seq":  1,
			"error":      "agent restarted before the Goal run reached a terminal event",
			"goal_report": map[string]interface{}{
				"schema_version":      1,
				"outcome":             "failed",
				"summary":             "Agent restarted before the Goal run reached a terminal event.",
				"blocker_code":        "agent_restarted_before_terminal",
				"evidence_refs":       []interface{}{},
				"completion_proposed": false,
			},
		}
		for key, value := range run.GoalIdentity {
			terminal[key] = value
		}
		_ = m.completeProcessedRun(runID, terminal)
		_ = writeJSON(terminal)
		return true
	}
	_ = writeJSON(map[string]interface{}{
		"type":       models.AgentEventAIMessageReceived,
		"session_id": run.ConversationID,
		"message_id": run.MessageID,
		"run_id":     run.RunID,
		"event_seq":  1,
		"duplicate":  true,
	})
	return true
}

func (m *agentAIManager) completeProcessedRun(runID string, payload map[string]interface{}) error {
	m.mu.Lock()
	run, ok := m.processedRuns[runID]
	if !ok {
		run = agentAIProcessedRun{RunID: runID}
	}
	copy := make(map[string]interface{}, len(payload))
	for key, value := range payload {
		copy[key] = value
	}
	run.State = "terminal"
	run.Terminal = copy
	run.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	m.processedRuns[runID] = run
	m.mu.Unlock()
	return m.persistIdentityState()
}

func (m *agentAIManager) confirmBinding(
	sessionID, provider, nativeSessionID string,
	bindingVersion int,
) (agentAIBindingRecord, error) {
	nativeSessionID = strings.TrimSpace(nativeSessionID)
	if nativeSessionID == "" {
		return agentAIBindingRecord{}, fmt.Errorf("provider returned an empty native session id")
	}
	m.mu.Lock()
	session := m.sessions[sessionID]
	existing := m.bindings[sessionID]
	for conversationID, candidate := range m.bindings {
		if conversationID != sessionID && candidate.NativeSessionID == nativeSessionID && providersEquivalent(candidate.Provider, provider) {
			m.mu.Unlock()
			return agentAIBindingRecord{}, fmt.Errorf("native session id %s is already bound to conversation %s", nativeSessionID, conversationID)
		}
	}
	expected := existing.NativeSessionID
	if expected == "" && session != nil {
		expected = strings.TrimSpace(session.reservedNativeSessionID)
	}
	if expected != "" && expected != nativeSessionID {
		m.mu.Unlock()
		return agentAIBindingRecord{}, fmt.Errorf("native session id mismatch: expected %s, got %s", expected, nativeSessionID)
	}
	if bindingVersion <= 0 {
		bindingVersion = existing.BindingVersion
	}
	if bindingVersion <= 0 {
		bindingVersion = 1
	}
	record := agentAIBindingRecord{
		ConversationID:  sessionID,
		Provider:        provider,
		NativeSessionID: nativeSessionID,
		State:           "confirmed",
		BindingVersion:  bindingVersion,
		UpdatedAt:       time.Now().UTC().Format(time.RFC3339Nano),
	}
	m.bindings[sessionID] = record
	if session != nil {
		session.resumeSessionID = nativeSessionID
		session.reservedNativeSessionID = ""
		session.bindingVersion = bindingVersion
	}
	m.mu.Unlock()
	if err := m.persistIdentityState(); err != nil {
		return agentAIBindingRecord{}, err
	}
	return record, nil
}

func (m *agentAIManager) reserveBinding(
	sessionID, provider, nativeSessionID string,
	bindingVersion int,
) error {
	nativeSessionID = strings.TrimSpace(nativeSessionID)
	if nativeSessionID == "" {
		return nil
	}
	m.mu.Lock()
	existing := m.bindings[sessionID]
	for conversationID, candidate := range m.bindings {
		if conversationID != sessionID && candidate.NativeSessionID == nativeSessionID && providersEquivalent(candidate.Provider, provider) {
			m.mu.Unlock()
			return fmt.Errorf("native session id %s is already bound to conversation %s", nativeSessionID, conversationID)
		}
	}
	if existing.NativeSessionID != "" && existing.NativeSessionID != nativeSessionID {
		m.mu.Unlock()
		return fmt.Errorf("reserved native session id changed from %s to %s", existing.NativeSessionID, nativeSessionID)
	}
	if bindingVersion <= 0 {
		bindingVersion = existing.BindingVersion
	}
	if bindingVersion <= 0 {
		bindingVersion = 1
	}
	m.bindings[sessionID] = agentAIBindingRecord{
		ConversationID:  sessionID,
		Provider:        provider,
		NativeSessionID: nativeSessionID,
		State:           "reserved",
		BindingVersion:  bindingVersion,
		UpdatedAt:       time.Now().UTC().Format(time.RFC3339Nano),
	}
	m.mu.Unlock()
	return m.persistIdentityState()
}

// releaseReservedBinding 释放滞留的 reserved 绑定。reserved 是 server 下发的
// 占位 id，一旦被证实不可按 fresh 兑现（jsonl 已落盘 / codex mismatch），留着
// 只会让后续每个回合继续拿它当错锚（2026-10-29 already-in-use 事故类）。
// confirmed 是合法 resume 锚，绝不释放。expected 非空时还要求 id 相等，防误删
// 换绑后的新绑定。落盘失败只 Warn：内存已干净，下一次成功的 persistIdentityState
// 会用当前内存状态覆盖磁盘（与 zombie_revoke 的取舍一致）。
func (m *agentAIManager) releaseReservedBinding(sessionID, expected string) {
	m.mu.Lock()
	binding, ok := m.bindings[sessionID]
	if !ok || binding.State != "reserved" || (expected != "" && binding.NativeSessionID != expected) {
		m.mu.Unlock()
		return
	}
	delete(m.bindings, sessionID)
	if session := m.sessions[sessionID]; session != nil && session.reservedNativeSessionID == binding.NativeSessionID {
		session.reservedNativeSessionID = ""
	}
	m.mu.Unlock()
	if err := m.persistIdentityState(); err != nil {
		logger.Warn(fmt.Sprintf("ai.binding.release: persist_failed session=%s err=%v", sessionID, err))
	}
}

// reservedBindingID 返回会话当前 reserved 绑定的原生 id（非 reserved 态返回空）。
func (m *agentAIManager) reservedBindingID(sessionID string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if binding, ok := m.bindings[sessionID]; ok && binding.State == "reserved" {
		return binding.NativeSessionID
	}
	return ""
}

func (m *agentAIManager) bindingForNative(provider, nativeSessionID string) (agentAIBindingRecord, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, binding := range m.bindings {
		if binding.NativeSessionID == nativeSessionID && providersEquivalent(binding.Provider, provider) {
			return binding, true
		}
	}
	return agentAIBindingRecord{}, false
}

func (m *agentAIManager) bindingForConversation(sessionID string) (agentAIBindingRecord, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	binding, ok := m.bindings[sessionID]
	return binding, ok && binding.NativeSessionID != ""
}

func (m *agentAIManager) annotateManagedVibeSessions(sessions []models.AgentVibeSession) []models.AgentVibeSession {
	if m == nil || len(sessions) == 0 {
		return sessions
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for index := range sessions {
		session := &sessions[index]
		nativeSessionID := strings.TrimSpace(session.SourceSessionID)
		if nativeSessionID == "" {
			nativeSessionID = inventoryNativeSessionID(session.Provider, session.ID)
			session.SourceSessionID = nativeSessionID
		}
		// agentcore journal 条目 ID=会话 id(壳),原生句柄只在绑定表;不回填则
		// 服务端壳只能拿壳 id 冒充原生 id,跨端 resume 恒错(2026-11-07 错位)。
		if nativeSessionID == "" {
			if binding, ok := m.bindings[session.ID]; ok &&
				binding.NativeSessionID != "" &&
				providersEquivalent(binding.Provider, session.Provider) {
				nativeSessionID = binding.NativeSessionID
				session.SourceSessionID = nativeSessionID
			}
		}
		// agentcore 会话 id 即托管会话 id(journal 头行 session_id=conversation id),
		// 无绑定也声明托管归属——服务端据此物化 canonical 记录而非铸造壳(S1)。
		// ai_import_ 前缀是历史拆分产物,归 heal 治愈,不自我声明。
		if session.ManagedConversationID == "" &&
			providersEquivalent(session.Provider, "agentcore") &&
			!strings.HasPrefix(session.ID, "ai_import_") {
			session.ManagedConversationID = session.ID
		}
		if session.Origin == "" {
			session.Origin = "external"
		}
		for _, binding := range m.bindings {
			if nativeSessionID == "" || binding.NativeSessionID != nativeSessionID || !providersEquivalent(binding.Provider, session.Provider) {
				continue
			}
			// 同会话绑定(agentcore 常态:条目 id 即绑定会话 id)不翻转 origin
			// ——dashboard/phone 头行语义保留,服务端来源角标不失真;异会话
			// 别名(claude TUI 条目→手机会话)维持 managed 现状。
			if binding.ConversationID != session.ID {
				session.Origin = "managed"
			}
			session.ManagedConversationID = binding.ConversationID
			session.BindingState = binding.State
			session.BindingVersion = binding.BindingVersion
			break
		}
	}
	return sessions
}

func inventoryNativeSessionID(provider, inventoryID string) string {
	inventoryID = strings.TrimSpace(inventoryID)
	prefixes := []string{strings.ToLower(strings.TrimSpace(provider)) + "_"}
	if providersEquivalent(provider, "claudecode") {
		prefixes = append(prefixes, "claude_", "claudecode_")
	}
	for _, prefix := range prefixes {
		if prefix != "_" && strings.HasPrefix(strings.ToLower(inventoryID), prefix) {
			return inventoryID[len(prefix):]
		}
	}
	return ""
}

func providersEquivalent(left, right string) bool {
	normalize := func(value string) string {
		value = strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(value), "-", ""), "_", ""))
		if value == "claude" || value == "claudecode" {
			return "claudecode"
		}
		return value
	}
	return normalize(left) == normalize(right)
}

// healSplitAgentCoreJournals 治愈存量错位(2026-11-07):手机续聊曾把回合写进
// ai_import_<hash>.jsonl(壳 id 冒充原生 id 的历史产物),桌面清单因此多出
// 「新对话」。绑定表存在两种可映射拓扑:
//  1. 链式(存量主形态,评审实锤):壳绑定 ai_import_h→local-abc 的 native
//     本身是另一绑定的 ConversationID(kernel adopt 语义:非空 SessionID 原样
//     采纳,壳线程的原生 id=冒充的壳 id);
//  2. 同 native:另一会话绑定了同一内核句柄(兼容保留)。
//
// 两种形态都把拆分 journal 并回 canonical 并删源、清孤儿绑定。映射不了
// (无伙伴/目标运行中)则保守跳过——源文件在,下个周期重试。
// 在 hello/sync 收集入口调用(collectAgentSyncSnapshotWithActiveRuns),治愈后
// 的文件自然退出清单面。
func (m *agentAIManager) healSplitAgentCoreJournals() {
	if m == nil || m.coreJournal == nil || m.coreJournal.dir == "" {
		return
	}
	type healPair struct {
		sourceID string
		targetID string
	}
	m.mu.Lock()
	var pairs []healPair
	for conversationID, binding := range m.bindings {
		if !strings.HasPrefix(conversationID, "ai_import_") {
			continue
		}
		if !providersEquivalent(binding.Provider, "agentcore") {
			continue
		}
		if binding.State != "confirmed" || strings.TrimSpace(binding.NativeSessionID) == "" {
			continue
		}
		// 运行中的会话(壳或目标)都不动手:回合在写文件,合并会撕出半行。
		if session := m.sessions[conversationID]; session != nil && session.cancel != nil {
			continue
		}
		targetID := ""
		// 形态一(链式,主):壳的"native"是另一会话的 id。provider 等价防
		// 跨族误配(评审建议);ConversationID != 壳 id 排除自环变体。
		if chained, ok := m.bindings[binding.NativeSessionID]; ok &&
			chained.ConversationID != conversationID &&
			providersEquivalent(chained.Provider, binding.Provider) {
			targetID = chained.ConversationID
		}
		// 形态二(同 native,兼容):另一会话绑定了同一内核句柄。
		if targetID == "" {
			for candidateID, candidate := range m.bindings {
				// 目标必须是 canonical 会话:排除自身与其他壳(壳并壳无意义)。
				if candidateID == conversationID || strings.HasPrefix(candidateID, "ai_import_") {
					continue
				}
				if candidate.NativeSessionID == binding.NativeSessionID && providersEquivalent(candidate.Provider, binding.Provider) {
					targetID = candidateID
					break
				}
			}
		}
		if targetID == "" {
			continue
		}
		if session := m.sessions[targetID]; session != nil && session.cancel != nil {
			continue
		}
		pairs = append(pairs, healPair{sourceID: conversationID, targetID: targetID})
	}
	m.mu.Unlock()
	for _, pair := range pairs {
		moved, ok := m.coreJournal.healMergeJournal(pair.sourceID, pair.targetID)
		if !ok {
			continue
		}
		m.mu.Lock()
		delete(m.bindings, pair.sourceID)
		m.mu.Unlock()
		// persistIdentityState 内部自持 m.mu——绝不能在持锁状态下调用
		// (自锁死=测试 10min 超时 panic,与 cc_session_lineage.go:310 同款纪律)。
		persistErr := m.persistIdentityState()
		logger.Info(fmt.Sprintf("agentcore-journal: heal split source=%s target=%s rows=%d persist_err=%v",
			pair.sourceID, pair.targetID, moved, persistErr))
	}
}
