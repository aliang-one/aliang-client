package services

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

const activityLimit = 400

type operationConnectionAccount struct{}
type operationSnapshotAccount struct{}

type agentOperation struct {
	OperationID string `json:"operation_id"`
	Revision    int64  `json:"revision"`
	UpdatedAt   string `json:"updated_at"`
	FinishedAt  string `json:"finished_at"`
}

type cloudOperation struct {
	agentOperation
	payload json.RawMessage
}

type agentOperationMirror struct {
	mu         sync.Mutex
	account    string
	connected  bool
	refreshing context.Context
	rows       map[string]cloudOperation
	wake       chan struct{}
	syncedAt   string
}

func (m *agentOperationMirror) signalLocked() {
	if m.wake != nil {
		close(m.wake)
	}
	m.wake = make(chan struct{})
}

func (m *agentOperationMirror) setConnected(connected bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.connected != connected {
		m.connected = connected
		m.signalLocked()
	}
}

func (m *agentOperationMirror) scopeLocked(account string) {
	if m.account == account {
		return
	}
	m.account = account
	m.connected = false
	m.refreshing = nil
	m.rows = nil
	m.syncedAt = ""
	m.signalLocked()
}

func (m *agentOperationMirror) mergeLocked(payload json.RawMessage) bool {
	var item agentOperation
	if err := json.Unmarshal(payload, &item); err != nil || item.OperationID == "" || item.Revision <= 0 {
		return false
	}
	if m.rows == nil {
		m.rows = make(map[string]cloudOperation)
	}
	prev := m.rows[item.OperationID]
	if prev.Revision >= item.Revision || (prev.FinishedAt != "" && item.FinishedAt == "") {
		return false
	}
	m.rows[item.OperationID] = cloudOperation{agentOperation: item, payload: payload}
	return true
}

func (m *agentOperationMirror) pruneLocked() {
	cutoff := time.Now().UTC().Add(-7 * 24 * time.Hour).Format(time.RFC3339)
	for id, row := range m.rows {
		if row.FinishedAt != "" && row.FinishedAt < cutoff {
			delete(m.rows, id)
		}
	}
	if len(m.rows) <= activityLimit {
		return
	}
	for _, row := range m.sortedLocked()[activityLimit:] {
		delete(m.rows, row.OperationID)
	}
}

func (m *agentOperationMirror) sortedLocked() []cloudOperation {
	rows := make([]cloudOperation, 0, len(m.rows))
	for _, row := range m.rows {
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool {
		left, right := rows[i], rows[j]
		if (left.FinishedAt == "") != (right.FinishedAt == "") {
			return left.FinishedAt == ""
		}
		if left.UpdatedAt != right.UpdatedAt {
			return left.UpdatedAt > right.UpdatedAt
		}
		return left.OperationID < right.OperationID
	})
	return rows
}

func (s *AgentService) operationAccount() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.TrimSpace(s.state.RegisteredUser)
}

func (s *AgentService) applyCloudOperation(account string, frame map[string]interface{}) {
	raw, err := json.Marshal(frame["operation"])
	if err != nil || account == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if account != strings.TrimSpace(s.state.RegisteredUser) {
		return
	}
	m := &s.operations
	m.mu.Lock()
	defer m.mu.Unlock()
	m.scopeLocked(account)
	if m.mergeLocked(raw) {
		m.pruneLocked()
		m.signalLocked()
	}
}

func (s *AgentService) refreshOperationMirror(ctx context.Context) {
	account, _ := ctx.Value(operationConnectionAccount{}).(string)
	s.mu.Lock()
	if account == "" || account != strings.TrimSpace(s.state.RegisteredUser) {
		s.mu.Unlock()
		return
	}
	m := &s.operations
	m.mu.Lock()
	writer := s.currentRemoteWriter()
	s.mu.Unlock()
	m.scopeLocked(account)
	if m.refreshing != nil && m.refreshing.Err() == nil {
		m.mu.Unlock()
		return
	}
	m.refreshing = ctx
	m.mu.Unlock()
	go func() {
		if writer != nil && ctx.Err() == nil {
			_ = writer(map[string]interface{}{"type": "operation.reconcile", "agent_instance_id": progressAgentInstance})
		}
		requestCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
		defer cancel()
		requestCtx = context.WithValue(requestCtx, operationSnapshotAccount{}, account)
		raw, err := s.doAgentServerJSON(requestCtx, http.MethodGet, agentPeerServerURL()+"/api/operations/snapshot", nil)
		var snapshot struct {
			Operations []json.RawMessage `json:"operations"`
		}
		if err == nil {
			err = json.Unmarshal(raw, &snapshot)
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.refreshing != ctx || m.account != account || strings.TrimSpace(s.state.RegisteredUser) != account {
			return
		}
		m.refreshing = nil
		if err != nil || ctx.Err() != nil {
			return
		}
		for _, operation := range snapshot.Operations {
			m.mergeLocked(operation)
		}
		m.pruneLocked()
		m.connected = true
		m.syncedAt = time.Now().UTC().Format(time.RFC3339)
		m.signalLocked()
	}()
}

func (s *AgentService) ServeOperationEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unavailable", http.StatusInternalServerError)
		return
	}
	account := s.operationAccount()
	if account == "" {
		http.Error(w, "Agent account unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	m := &s.operations
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		s.mu.Lock()
		if strings.TrimSpace(s.state.RegisteredUser) != account {
			s.mu.Unlock()
			return
		}
		m.mu.Lock()
		m.scopeLocked(account)
		s.mu.Unlock()
		if m.wake == nil {
			m.signalLocked()
		}
		wake := m.wake
		rows := m.sortedLocked()
		items := make([]json.RawMessage, len(rows))
		for i, row := range rows {
			items[i] = row.payload
		}
		data, err := json.Marshal(map[string]interface{}{
			"operations": items, "connected": m.connected, "synced_at": m.syncedAt,
		})
		m.mu.Unlock()
		if err != nil {
			return
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
			return
		}
		flusher.Flush()
		select {
		case <-wake:
			timer := time.NewTimer(200 * time.Millisecond)
			select {
			case <-timer.C:
			case <-r.Context().Done():
				timer.Stop()
				return
			}
		case <-ticker.C:
		case <-r.Context().Done():
			return
		}
	}
}
