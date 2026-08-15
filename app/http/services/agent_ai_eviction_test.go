package services

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// residentAISession builds a bare AI session handle with the given recency, for
// eviction-ordering tests. cancel != nil marks a session as mid-turn.
func residentAISession(id string, activeAt time.Time, running bool) *agentAISession {
	s := &agentAISession{id: id, lastActiveAt: activeAt}
	if running {
		// A running turn has a non-nil cancel. The actual func is irrelevant for
		// eviction (only != nil matters); a no-op keeps the test hermetic.
		s.cancel = func() {}
	}
	return s
}

// At cap, registering a new session must drop the OLDEST idle session, keep the
// new one, and leave the resident size at exactly the cap.
func TestRegisterAISessionEvictsOldestIdle(t *testing.T) {
	manager := newAgentAIManager()
	base := time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC)
	// s0 oldest … s(cap-1) newest, all idle.
	for i := 0; i < agentAISessionResidentCap; i++ {
		s := residentAISession(fmt.Sprintf("s%d", i), base.Add(time.Duration(i)*time.Minute), false)
		manager.sessions[s.id] = s
	}

	manager.mu.Lock()
	manager.registerAISessionLocked(residentAISession("s_new", base.Add(time.Hour), false))
	manager.mu.Unlock()

	if got := len(manager.sessions); got != agentAISessionResidentCap {
		t.Fatalf("resident size = %d, want %d", got, agentAISessionResidentCap)
	}
	if _, ok := manager.sessions["s0"]; ok {
		t.Error("oldest idle session s0 was not evicted")
	}
	if _, ok := manager.sessions["s_new"]; !ok {
		t.Error("newly registered session s_new is missing")
	}
}

// A running session must NEVER be evicted, even if it is the oldest. When room
// is needed and an idle session exists, the oldest IDLE one is sacrificed
// instead. (context import kept for realism of the running cancel.)
func TestRegisterAISessionNeverEvictsRunningTurn(t *testing.T) {
	manager := newAgentAIManager()
	base := time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC)
	// Oldest overall, but running — must survive.
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager.sessions["s_run"] = &agentAISession{id: "s_run", lastActiveAt: base, cancel: cancel}
	// Fill the rest with idle sessions newer than s_run.
	for i := 1; i < agentAISessionResidentCap; i++ {
		manager.sessions[fmt.Sprintf("s%d", i)] = residentAISession(fmt.Sprintf("s%d", i), base.Add(time.Duration(i)*time.Minute), false)
	}

	manager.mu.Lock()
	manager.registerAISessionLocked(residentAISession("s_new", base.Add(time.Hour), false))
	manager.mu.Unlock()

	if _, ok := manager.sessions["s_run"]; !ok {
		t.Error("running session s_run was evicted — running turns must never be evicted")
	}
	if _, ok := manager.sessions["s1"]; ok {
		t.Error("oldest idle session s1 should have been evicted in place of the running one")
	}
	if _, ok := manager.sessions["s_new"]; !ok {
		t.Error("new session s_new is missing")
	}
}

// Below the cap, eviction is a no-op — nothing is dropped.
func TestEvictionNoopBelowCap(t *testing.T) {
	manager := newAgentAIManager()
	manager.sessions["only"] = residentAISession("only", time.Now(), false)

	manager.mu.Lock()
	manager.evictOldestIdleAISessionLocked()
	manager.mu.Unlock()

	if got := len(manager.sessions); got != 1 {
		t.Fatalf("resident size = %d, want 1 (no eviction below cap)", got)
	}
}

// If every resident session is running, eviction finds no victim and is a
// no-op — the caller is allowed to overshoot rather than kill a live turn.
func TestEvictionNoopWhenAllRunning(t *testing.T) {
	manager := newAgentAIManager()
	base := time.Now()
	for i := 0; i < agentAISessionResidentCap; i++ {
		manager.sessions[fmt.Sprintf("s%d", i)] = residentAISession(fmt.Sprintf("s%d", i), base, true)
	}

	manager.mu.Lock()
	manager.evictOldestIdleAISessionLocked()
	manager.mu.Unlock()

	if got := len(manager.sessions); got != agentAISessionResidentCap {
		t.Fatalf("resident size = %d, want %d (must not evict any running turn)", got, agentAISessionResidentCap)
	}
}
