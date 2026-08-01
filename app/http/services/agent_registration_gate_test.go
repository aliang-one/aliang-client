package services

import (
	"errors"
	"sync"
	"testing"
	"time"
)

// TestRegistrationGate_BlocksUntilReady locks the registration-gate contract
// used by runRemoteAgentSession: a freshly-armed gate makes
// waitForRegistration time out; markRegistrationReady opens it (and is
// idempotent — a periodic re-hello that re-ACKs agent.registered is a no-op);
// a never-armed service is treated as already open so non-WS callers and tests
// are never blocked.
func TestRegistrationGate_BlocksUntilReady(t *testing.T) {
	prev := agentRemoteRegistrationWait
	agentRemoteRegistrationWait = 40 * time.Millisecond
	defer func() { agentRemoteRegistrationWait = prev }()

	// Never-armed service → gate open.
	s := &AgentService{}
	if ok := s.waitForRegistration(time.Second); !ok {
		t.Fatal("never-armed gate should be treated as open (waitForRegistration=true)")
	}

	// Armed but not opened → times out.
	s.armRegistrationGate()
	if ok := s.waitForRegistration(20 * time.Millisecond); ok {
		t.Fatal("waitForRegistration returned true before markRegistrationReady (gate should block)")
	}

	// Open it → unblocks.
	s.markRegistrationReady()
	if ok := s.waitForRegistration(time.Second); !ok {
		t.Fatal("waitForRegistration returned false after markRegistrationReady")
	}

	// Idempotent: a second mark must not panic (periodic re-hello re-ACK).
	s.markRegistrationReady()
	if ok := s.waitForRegistration(time.Second); !ok {
		t.Fatal("waitForRegistration returned false after second markRegistrationReady")
	}
}

// TestRemoteWriter_GatedUntilRegistration reproduces the vibecoding "stuck on
// thinking" race at the unit level: an in-flight business write (an
// ai.approval.request) issued AFTER the writer was published but BEFORE the
// server ACKed agent.registered must NOT reach the socket. Only once
// registration is confirmed does the gated write proceed.
//
// Before the fix, setCurrentRemoteWriter published rawWriter directly, so the
// approval request was flushed immediately and silently rejected by the server
// with agent_not_registered, leaving the approval waiter blocked up to 24h.
func TestRemoteWriter_GatedUntilRegistration(t *testing.T) {
	prev := agentRemoteRegistrationWait
	agentRemoteRegistrationWait = 2 * time.Second // generous: the write must BLOCK, not fail
	defer func() { agentRemoteRegistrationWait = prev }()

	s := &AgentService{}
	s.armRegistrationGate()

	var (
		mu    sync.Mutex
		wrote []map[string]interface{}
	)
	// publishedWriter mirrors runRemoteAgentSession: wait for registration,
	// then record the payload (stands in for the real socket write).
	published := agentTerminalWriter(func(payload interface{}) error {
		if !s.waitForRegistration(agentRemoteRegistrationWait) {
			return errors.New("remote agent not registered within deadline")
		}
		m, _ := payload.(map[string]interface{})
		mu.Lock()
		wrote = append(wrote, m)
		mu.Unlock()
		return nil
	})
	s.setCurrentRemoteWriter(published)
	defer s.clearCurrentRemoteWriter()

	// Shim mirrors the writeJSON closure: delegate to the current live writer.
	shim := func(p interface{}) error {
		w := s.currentRemoteWriter()
		if w == nil {
			return errors.New("disconnected")
		}
		return w(p)
	}

	// Issue an approval request BEFORE registration. It must block — neither
	// reaching the recorder nor returning an error.
	done := make(chan error, 1)
	go func() {
		done <- shim(map[string]interface{}{
			"type":        "ai.approval.request",
			"approval_id": "race-1",
		})
	}()

	select {
	case err := <-done:
		t.Fatalf("gated write returned before registration: %v", err)
	case <-time.After(120 * time.Millisecond):
	}
	mu.Lock()
	n := len(wrote)
	mu.Unlock()
	if n != 0 {
		t.Fatalf("business write reached server before registration: %d write(s)", n)
	}

	// Open registration: the blocked write must now complete and be recorded.
	s.markRegistrationReady()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("gated write failed after registration: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("gated write did not complete after registration")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(wrote) != 1 || wrote[0]["approval_id"] != "race-1" {
		t.Fatalf("expected exactly the one gated approval write, got %v", wrote)
	}
}
