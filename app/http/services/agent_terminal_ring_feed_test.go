package services

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"aliang.one/nursorgate/app/http/models"
)

// failingWriter is an agentTerminalWriter that rejects everything, standing in
// for a dead WebSocket connection.
func failingWriter() agentTerminalWriter {
	return func(interface{}) error { return errors.New("ws gone") }
}

// TestTerminalOutputAlwaysFeedsRingEvenWhenWriterDead pins the dual-write
// contract: the ring is the authoritative copy of the output stream, so a dead
// live writer must no longer lose those bytes — the next attach replays them.
func TestTerminalOutputAlwaysFeedsRingEvenWhenWriterDead(t *testing.T) {
	m := newAgentTerminalManager()
	ring := newTerminalRingBuffer(1 << 20)
	live := newAttachTestSession("t-dead-writer", ring)
	m.mu.Lock()
	m.sessions["t-dead-writer"] = live
	m.mu.Unlock()

	m.copyTerminalOutput("t-dead-writer", strings.NewReader("hello ring"), failingWriter())

	if got := string(ring.snapshot()); got != "hello ring" {
		t.Fatalf("ring = %q, want %q — output must reach the ring even when the live writer rejects every frame", got, "hello ring")
	}
}

// TestInputRefreshesLastInputAt pins that terminal.input refreshes both activity
// clocks: lastActiveAt (attached idle semantics) and lastInputAt (the input-only
// clock that governs a detached session's life).
func TestInputRefreshesLastInputAt(t *testing.T) {
	dir := t.TempDir()
	setAgentAuthorizedExecutionDirectoriesCache([]string{dir})
	t.Cleanup(func() { setAgentAuthorizedExecutionDirectoriesCache(nil) })

	m := newAgentTerminalManager()
	coll, write := newPayloadCollector()
	blockExit := make(chan struct{})
	spawner := &fakeTerminalSpawner{blockExit: blockExit}
	m.startProcess = spawner.start
	t.Cleanup(func() { close(blockExit) })

	m.create(map[string]interface{}{
		"type":       models.AgentEventTerminalCreate,
		"session_id": "t-input",
		"shell":      "/bin/sh",
		"cwd":        dir,
	}, write)
	session := m.get("t-input")
	if session == nil {
		t.Fatalf("session must be live after create")
	}

	stale := time.Now().Add(-10 * time.Minute)
	m.mu.Lock()
	session.lastActiveAt = stale
	session.lastInputAt = stale
	m.mu.Unlock()

	m.write(map[string]interface{}{
		"type":       models.AgentEventTerminalInput,
		"session_id": "t-input",
		"data":       "echo hi\n",
	}, write)

	m.mu.Lock()
	gotInput, gotActive := session.lastInputAt, session.lastActiveAt
	m.mu.Unlock()
	if !gotInput.After(stale) || time.Since(gotInput) > 5*time.Second {
		t.Fatalf("terminal.input must refresh lastInputAt, got %v (stale %v)", gotInput, stale)
	}
	if !gotActive.After(stale) {
		t.Fatalf("terminal.input must refresh lastActiveAt too, got %v (stale %v)", gotActive, stale)
	}
	for _, p := range coll.ofTypes(models.AgentEventTerminalError) {
		t.Fatalf("input path must not error, got %v", p)
	}
}

// signalAfterDataReader sends on fed exactly once, right after the first Read
// that yields data — the consumer now holds the bytes in hand and its very next
// synchronization point is the output gate, so a subsequent negative assertion
// about the gate does not race the read itself.
type signalAfterDataReader struct {
	r    io.Reader
	fed  chan struct{}
	once bool
}

func (s *signalAfterDataReader) Read(buf []byte) (int, error) {
	n, err := s.r.Read(buf)
	if n > 0 && !s.once {
		s.once = true
		s.fed <- struct{}{}
	}
	return n, err
}

// TestReplayGateExcludesLiveChunksDuringSnapshot pins the replay/live seam: a
// chunk produced while a replay holds the output gate is neither mixed into the
// snapshot nor lost — it waits behind the gate and flows to ring+live stream
// after the snapshot, in order, exactly once.
func TestReplayGateExcludesLiveChunksDuringSnapshot(t *testing.T) {
	m := newAgentTerminalManager()
	ring := newTerminalRingBuffer(1 << 20)
	ring.push([]byte("snap-"))
	live := newAttachTestSession("t-seam", ring)
	m.mu.Lock()
	m.sessions["t-seam"] = live
	m.mu.Unlock()

	var mu sync.Mutex
	var written []string
	record := func(kind string, p map[string]interface{}) {
		mu.Lock()
		written = append(written, kind+":"+fmt.Sprint(p["data"]))
		mu.Unlock()
	}

	// A writer that stalls the replay on its first frame while sendReplay is
	// holding the output gate, simulating a slow WebSocket mid-snapshot.
	firstReplayOut := make(chan struct{})
	release := make(chan struct{})
	write := func(v interface{}) error {
		p, ok := v.(map[string]interface{})
		if !ok {
			return nil
		}
		switch p["type"] {
		case models.AgentEventTerminalReplay:
			record("replay", p)
			firstReplayOut <- struct{}{}
			<-release
		case models.AgentEventTerminalOutput:
			record("output", p)
		}
		return nil
	}

	replayDone := make(chan struct{})
	go func() {
		defer close(replayDone)
		m.sendReplay("t-seam", ring, terminalReplayStatusLive, &live.outputGate, write)
	}()
	<-firstReplayOut // snapshot taken, gate held, replay stalled mid-send

	fed := make(chan struct{})
	liveDone := make(chan struct{})
	go func() {
		defer close(liveDone)
		m.copyTerminalOutput("t-seam", &signalAfterDataReader{r: strings.NewReader("live-1"), fed: fed}, write)
	}()
	<-fed // the consumer holds the chunk and is heading for the gate

	select {
	case <-liveDone:
		t.Fatalf("copyTerminalOutput must block on the output gate while a replay holds it")
	case <-time.After(100 * time.Millisecond):
	}
	mu.Lock()
	var leaked []string
	for _, f := range written {
		if strings.HasPrefix(f, "output:") {
			leaked = append(leaked, f)
		}
	}
	mu.Unlock()
	if len(leaked) != 0 {
		t.Fatalf("live output frames emitted while the gate was held: %v (must not interleave with the replay snapshot)", leaked)
	}
	if got := string(ring.snapshot()); got != "snap-" {
		t.Fatalf("ring while gate held = %q, want %q (the ring push must wait for the gate so the snapshot stays coherent)", got, "snap-")
	}

	// Replay ends, gate released: the chunk must flow to BOTH the ring and the
	// live stream — nothing dropped, nothing duplicated, replay before live.
	close(release)
	select {
	case <-replayDone:
	case <-time.After(2 * time.Second):
		t.Fatalf("sendReplay must finish once released")
	}
	select {
	case <-liveDone:
	case <-time.After(2 * time.Second):
		t.Fatalf("copyTerminalOutput must proceed once the gate is released")
	}

	if got := string(ring.snapshot()); got != "snap-live-1" {
		t.Fatalf("ring after release = %q, want %q (the gated chunk must not be lost)", got, "snap-live-1")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(written) != 2 || written[0] != "replay:snap-" || written[1] != "output:live-1" {
		t.Fatalf("frames = %v, want exactly [replay:snap- output:live-1] in that order", written)
	}
}
