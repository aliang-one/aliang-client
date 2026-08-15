package services

import (
	"errors"
	"sync"
	"testing"

	"aliang.one/nursorgate/app/http/models"
)

func TestAgentCapabilitiesAdvertiseRunProtocolV2(t *testing.T) {
	for _, capability := range agentCapabilities() {
		if capability == "ai_run_protocol_v2" {
			return
		}
	}
	t.Fatal("agent capabilities do not advertise ai_run_protocol_v2")
}

func TestAgentAIRunEmitterFencesWhenTerminalJournalFails(t *testing.T) {
	var events []map[string]interface{}
	emitter := newAgentAIRunEmitter(
		agentAIRun{runID: "run-journal-fail", messageID: "msg-journal-fail"},
		agentTerminalWriter(func(value interface{}) error {
			events = append(events, value.(map[string]interface{}))
			return nil
		}),
	)
	emitter.onTerminal = func(map[string]interface{}) error {
		return errors.New("disk unavailable")
	}

	if err := emitter.emit(map[string]interface{}{"type": "ai.done"}); err == nil {
		t.Fatal("terminal emit error = nil, want journal failure")
	}
	if err := emitter.emit(map[string]interface{}{"type": "ai.run.progress"}); err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("events = %#v, want no terminal or post-terminal progress on wire", events)
	}
}

func TestAgentAIRunEmitterOrdersAndFencesTerminal(t *testing.T) {
	var mu sync.Mutex
	events := make([]map[string]interface{}, 0)
	writer := agentTerminalWriter(func(value interface{}) error {
		mu.Lock()
		events = append(events, value.(map[string]interface{}))
		mu.Unlock()
		return nil
	})
	emitter := newAgentAIRunEmitter(agentAIRun{runID: "run-1", messageID: "msg-1"}, writer)

	if err := emitter.emit(map[string]interface{}{"type": "ai.run.progress"}); err != nil {
		t.Fatal(err)
	}
	if err := emitter.emit(map[string]interface{}{"type": "ai.done"}); err != nil {
		t.Fatal(err)
	}
	// This models the ticker winning CPU only after the main path emitted done.
	if err := emitter.emit(map[string]interface{}{"type": "ai.run.progress"}); err != nil {
		t.Fatal(err)
	}

	if len(events) != 2 {
		t.Fatalf("events = %d, want 2", len(events))
	}
	if events[0]["run_id"] != "run-1" || events[1]["run_id"] != "run-1" {
		t.Fatalf("run ids = %v, %v", events[0]["run_id"], events[1]["run_id"])
	}
	if events[0]["event_seq"] != int64(1) || events[1]["event_seq"] != int64(2) {
		t.Fatalf("event seqs = %v, %v", events[0]["event_seq"], events[1]["event_seq"])
	}
	if events[1]["type"] != "ai.done" {
		t.Fatalf("last event = %v, want ai.done", events[1]["type"])
	}
}

func TestAgentAIRunEmitterTreatsSessionCloseAsTerminal(t *testing.T) {
	var mu sync.Mutex
	events := make([]map[string]interface{}, 0)
	emitter := newAgentAIRunEmitter(
		agentAIRun{runID: "run-close", messageID: "msg-close"},
		agentTerminalWriter(func(value interface{}) error {
			mu.Lock()
			defer mu.Unlock()
			events = append(events, value.(map[string]interface{}))
			return nil
		}),
	)

	if err := emitter.emit(map[string]interface{}{"type": models.AgentEventAISessionClosed}); err != nil {
		t.Fatal(err)
	}
	if err := emitter.emit(map[string]interface{}{"type": models.AgentEventAIRunProgress}); err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0]["type"] != models.AgentEventAISessionClosed {
		t.Fatalf("events = %#v, want only ai.session.closed", events)
	}
	if events[0]["run_id"] != "run-close" || events[0]["event_seq"] != int64(1) {
		t.Fatalf("close metadata = %#v", events[0])
	}
}

func TestAgentAIRunEmitterUsesMessageIDForLegacyServer(t *testing.T) {
	var got map[string]interface{}
	emitter := newAgentAIRunEmitter(
		agentAIRun{messageID: "msg-fallback"},
		agentTerminalWriter(func(value interface{}) error {
			got = value.(map[string]interface{})
			return nil
		}),
	)
	_ = emitter.emit(map[string]interface{}{"type": "ai.run.started"})
	if got["run_id"] != "msg-fallback" {
		t.Fatalf("run_id = %v, want msg-fallback", got["run_id"])
	}
}

func TestAgentAIRunTerminalRemainsUntilAck(t *testing.T) {
	m := newAgentAIManager()
	var emitted map[string]interface{}
	emitter := m.runEmitter(
		agentAIRun{runID: "run-pending", messageID: "msg-pending"},
		agentTerminalWriter(func(value interface{}) error {
			emitted = value.(map[string]interface{})
			return nil
		}),
	)
	_ = emitter.emit(map[string]interface{}{"type": "ai.done", "session_id": "s1"})

	pending := m.pendingTerminalSnapshot()
	if len(pending) != 1 || pending[0]["run_id"] != "run-pending" {
		t.Fatalf("pending = %#v", pending)
	}
	if emitted["event_seq"] != int64(1) {
		t.Fatalf("emitted seq = %v", emitted["event_seq"])
	}
	m.acknowledgePendingTerminal("run-pending", 0)
	if got := m.pendingTerminalSnapshot(); len(got) != 1 {
		t.Fatalf("pending after stale ack = %#v", got)
	}
	m.acknowledgePendingTerminal("run-pending", 1)
	if got := m.pendingTerminalSnapshot(); len(got) != 0 {
		t.Fatalf("pending after ack = %#v", got)
	}
}

func TestAgentGoalRunTerminalClearsOnGoalAck(t *testing.T) {
	m := newAgentAIManager()
	emitter := m.runEmitter(
		agentAIRun{
			runID:     "goal-run-pending",
			messageID: "goal-message-pending",
			goalIdentity: map[string]interface{}{
				"goal_id":     "goal-1",
				"goal_run_id": "goal-run-pending",
			},
		},
		agentTerminalWriter(func(interface{}) error { return nil }),
	)
	if err := emitter.emit(map[string]interface{}{"type": "ai.error", "session_id": "goal-1"}); err != nil {
		t.Fatal(err)
	}
	if got := m.pendingTerminalSnapshot(); len(got) != 1 {
		t.Fatalf("pending before Goal ACK = %#v", got)
	}

	svc := &AgentService{ai: m}
	svc.handleRemoteAgentMessage(map[string]interface{}{
		"type":         models.AgentEventGoalRunEventAck,
		"goal_run_id":  "goal-run-pending",
		"accepted_seq": 6,
		"result":       "accepted",
	}, func(interface{}) error { return nil })
	if got := m.pendingTerminalSnapshot(); len(got) != 0 {
		t.Fatalf("pending after Goal ACK = %#v", got)
	}
}
