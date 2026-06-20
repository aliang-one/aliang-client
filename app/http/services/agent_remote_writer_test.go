package services

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// TestAgentRemoteWriterIndirection locks the writer-decoupling contract: a
// published writer is observable until cleared, and the shim used by
// runRemoteAgentSession errors (rather than panics) while disconnected.
func TestAgentRemoteWriterIndirection(t *testing.T) {
	s := &AgentService{}
	if w := s.currentRemoteWriter(); w != nil {
		t.Fatalf("fresh service writer = %v, want nil", w)
	}

	var calls int
	published := agentTerminalWriter(func(payload interface{}) error {
		calls++
		return nil
	})
	s.setCurrentRemoteWriter(published)

	w := s.currentRemoteWriter()
	if w == nil {
		t.Fatal("setCurrentRemoteWriter did not publish a writer")
	}
	if err := w(map[string]string{"type": "ping"}); err != nil {
		t.Fatalf("published writer returned error: %v", err)
	}
	if calls != 1 {
		t.Fatalf("writer invoked %d times, want 1", calls)
	}

	s.clearCurrentRemoteWriter()
	if w := s.currentRemoteWriter(); w != nil {
		t.Fatalf("writer = %v after clear, want nil", w)
	}

	// Shim semantics mirroring runRemoteAgentSession: disconnected -> error.
	shim := func(payload interface{}) error {
		cw := s.currentRemoteWriter()
		if cw == nil {
			return errors.New("disconnected")
		}
		return cw(payload)
	}
	if err := shim(nil); err == nil {
		t.Fatal("shim should return an error while disconnected")
	}

	// Re-publish: the shim picks the new writer up (reconnect reattachment).
	var reconnectCalls int
	s.setCurrentRemoteWriter(agentTerminalWriter(func(payload interface{}) error {
		reconnectCalls++
		return nil
	}))
	if err := shim(nil); err != nil {
		t.Fatalf("shim error after reconnect: %v", err)
	}
	if reconnectCalls != 1 {
		t.Fatalf("reconnect writer invoked %d times, want 1", reconnectCalls)
	}
}

// TestRunRemoteAgentSession_EndsWhenPeerGoesSilent locks the C liveness fix:
// when the remote peer stops responding (no pongs, no messages) the enforced
// read deadline must fire so ReadJSON errors and the session ends — letting
// remoteConnectionLoop reconnect — instead of hanging on a dead/half-open
// socket (the old behavior, where the agent believed itself online while
// PhoneServer saw no traffic).
func TestRunRemoteAgentSession_EndsWhenPeerGoesSilent(t *testing.T) {
	prevHeartbeat := agentRemoteHeartbeatInterval
	prevPing := agentRemotePingInterval
	prevRead := agentRemoteReadWindow
	prevWrite := agentRemoteWriteTimeout
	agentRemoteHeartbeatInterval = 200 * time.Millisecond
	agentRemotePingInterval = 100 * time.Millisecond
	agentRemoteReadWindow = 400 * time.Millisecond
	agentRemoteWriteTimeout = 300 * time.Millisecond
	defer func() {
		agentRemoteHeartbeatInterval = prevHeartbeat
		agentRemotePingInterval = prevPing
		agentRemoteReadWindow = prevRead
		agentRemoteWriteTimeout = prevWrite
	}()

	upgrader := websocket.Upgrader{}
	silent := make(chan struct{})
	helloReceived := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		// Read the initial hello, then go silent: stop reading so the agent's
		// pings are never turned into pongs. The agent's read deadline should fire.
		if _, _, err := conn.ReadMessage(); err != nil {
			return
		}
		helloReceived <- struct{}{}
		<-silent
	}))
	defer server.Close()
	defer close(silent)

	// Isolate from the real environment so the inventory snapshot scan (which
	// walks ~/.codex / ~/.claude) and persisted agent state don't make the test
	// slow or depend on the host.
	baseDir, err := os.MkdirTemp("", "aliang-remote-ws-liveness-*")
	if err != nil {
		t.Fatalf("MkdirTemp() error = %v", err)
	}
	defer os.RemoveAll(baseDir)
	t.Setenv("HOME", filepath.Join(baseDir, "home"))
	t.Setenv("ALIANG_CACHE_DIR", filepath.Join(baseDir, "cache"))

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial failed: %v", err)
	}
	defer conn.Close()

	s := NewAgentService()

	sessionDone := make(chan error, 1)
	go func() { sessionDone <- s.runRemoteAgentSession(conn) }()

	// Confirm the session actually started (hello was sent) before requiring the
	// silent-peer termination, so a hello failure can't masquerade as a pass.
	select {
	case <-helloReceived:
	case <-time.After(5 * time.Second):
		t.Fatal("agent never sent hello within 5s")
	}

	select {
	case <-sessionDone:
		// Expected: the read deadline fired on the silent peer and the session ended.
	case <-time.After(4 * time.Second):
		t.Fatal("runRemoteAgentSession did not end within 4s against a silent peer; read-deadline/ping liveness is not working")
	}
}
