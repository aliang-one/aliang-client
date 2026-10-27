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
		if session.Origin == "" {
			session.Origin = "external"
		}
		for _, binding := range m.bindings {
			if nativeSessionID == "" || binding.NativeSessionID != nativeSessionID || !providersEquivalent(binding.Provider, session.Provider) {
				continue
			}
			session.Origin = "managed"
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
