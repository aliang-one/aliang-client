package services

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"aliang.one/nursorgate/app/http/models"
)

// A teardown (logout / unbind / AI control disabled / connection loop exit) must
// publish a terminal ai.status for every in-flight run BEFORE the sessions are
// dropped. Losing that terminal is what leaves the server holding a live run,
// which its stale sweeper later paints as timed_out — a red "failed" card for a
// turn that was merely stopped (2026-10-21 incident).
func TestAgentAICloseAllConvergesInflightRunsToTerminal(t *testing.T) {
	m := newAgentAIManager()

	var mu sync.Mutex
	var written []map[string]interface{}
	writer := agentTerminalWriter(func(value interface{}) error {
		mu.Lock()
		defer mu.Unlock()
		written = append(written, value.(map[string]interface{}))
		return errors.New("remote agent writer unavailable (disconnected)")
	})
	emitter := m.runEmitter(agentAIRun{sessionID: "s-1", runID: "run-1", messageID: "msg-1"}, writer)

	_, cancel := context.WithCancel(context.Background())
	defer cancel()

	m.mu.Lock()
	m.sessions["s-1"] = &agentAISession{
		id:           "s-1",
		cancel:       cancel,
		activeWriter: emitter.emit,
		activeRunID:  "run-1",
		lastActiveAt: time.Now(),
	}
	m.mu.Unlock()

	m.closeAllWithReason("logout")

	mu.Lock()
	defer mu.Unlock()
	if len(written) != 1 {
		t.Fatalf("teardown wrote %d events, want exactly 1 terminal: %#v", len(written), written)
	}
	payload := written[0]
	if payload["type"] != models.AgentEventAIStatus {
		t.Fatalf("type = %v, want %s", payload["type"], models.AgentEventAIStatus)
	}
	if payload["status"] != agentAITeardownStatus {
		t.Fatalf("status = %v, want %s", payload["status"], agentAITeardownStatus)
	}
	if payload["session_id"] != "s-1" {
		t.Fatalf("session_id = %v, want s-1", payload["session_id"])
	}
	if payload["error"] == "" || payload["error"] == nil {
		t.Fatal("terminal carries no reason; the card cannot explain itself")
	}
	// The write failed (socket already closed) — the terminal must still be
	// durable, otherwise the next reconnect has nothing to replay.
	if _, ok := m.pendingTerminals["run-1"]; !ok {
		t.Fatal("terminal was not journaled; a crash or reconnect would lose it")
	}
	if m.sessions["s-1"] != nil {
		t.Fatal("session survived teardown")
	}
}

// After a teardown terminal, the run goroutine's own ctx.Err() branch must not be
// able to push a second, contradictory terminal through the same emitter.
func TestAgentAICloseAllFencesLateRunTerminal(t *testing.T) {
	m := newAgentAIManager()

	var mu sync.Mutex
	var written []map[string]interface{}
	writer := agentTerminalWriter(func(value interface{}) error {
		mu.Lock()
		defer mu.Unlock()
		written = append(written, value.(map[string]interface{}))
		return nil
	})
	emitter := m.runEmitter(agentAIRun{sessionID: "s-1", runID: "run-1", messageID: "msg-1"}, writer)

	_, cancel := context.WithCancel(context.Background())
	m.mu.Lock()
	m.sessions["s-1"] = &agentAISession{
		id:           "s-1",
		cancel:       cancel,
		activeWriter: emitter.emit,
		activeRunID:  "run-1",
	}
	m.mu.Unlock()

	m.closeAllWithReason("logout")
	// What runCLI/runTUITurn does once it observes ctx.Err() != nil.
	if err := emitter.emit(map[string]interface{}{
		"type":       models.AgentEventAIStatus,
		"session_id": "s-1",
		"status":     "idle_timeout",
	}); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(written) != 1 {
		t.Fatalf("events = %d, want 1 (late terminal must be fenced): %#v", len(written), written)
	}
	if written[0]["status"] != agentAITeardownStatus {
		t.Fatalf("status = %v, want the teardown verdict to win", written[0]["status"])
	}
}

// Idle sessions (no in-flight run) must not invent a terminal.
func TestAgentAICloseAllLeavesIdleSessionsSilent(t *testing.T) {
	m := newAgentAIManager()
	m.mu.Lock()
	m.sessions["idle"] = &agentAISession{id: "idle", lastActiveAt: time.Now()}
	m.mu.Unlock()

	m.closeAllWithReason("logout")

	if len(m.pendingTerminals) != 0 {
		t.Fatalf("pendingTerminals = %#v, want empty", m.pendingTerminals)
	}
	if len(m.sessions) != 0 {
		t.Fatalf("sessions = %#v, want empty", m.sessions)
	}
}
