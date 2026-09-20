package services

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"aliang.one/nursorgate/app/http/models"
)

// payloadCollector is a thread-safe fake agentTerminalWriter that records every
// map payload it receives so tests can assert on the exact frame sequence.
type payloadCollector struct {
	mu  sync.Mutex
	got []map[string]interface{}
}

func newPayloadCollector() (*payloadCollector, agentTerminalWriter) {
	c := &payloadCollector{}
	return c, func(v interface{}) error {
		if m, ok := v.(map[string]interface{}); ok {
			c.mu.Lock()
			c.got = append(c.got, m)
			c.mu.Unlock()
		}
		return nil
	}
}

func (c *payloadCollector) snapshot() []map[string]interface{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]map[string]interface{}(nil), c.got...)
}

// ofTypes returns the payloads whose "type" is one of the wanted values, in
// arrival order.
func (c *payloadCollector) ofTypes(want ...string) []map[string]interface{} {
	set := make(map[string]bool, len(want))
	for _, t := range want {
		set[t] = true
	}
	var out []map[string]interface{}
	for _, p := range c.snapshot() {
		if t, ok := p["type"].(string); ok && set[t] {
			out = append(out, p)
		}
	}
	return out
}

// indexOf returns the arrival index of the first payload matching pred.
func (c *payloadCollector) indexOf(pred func(map[string]interface{}) bool) int {
	for i, p := range c.snapshot() {
		if pred(p) {
			return i
		}
	}
	return -1
}

// fakeTerminalSpawner stands in for startAgentTerminalProcess: it counts spawns
// and hands back a fully wired fake session. When blockExit is non-nil the fake
// process stays alive until the channel is closed, so tests can control the
// exit timing deterministically.
type fakeTerminalSpawner struct {
	mu        sync.Mutex
	count     int
	blockExit chan struct{}
}

func (f *fakeTerminalSpawner) start(sessionID string, shell string, cwd string, rows int, cols int) (*agentTerminalSession, []io.Reader, error) {
	f.mu.Lock()
	f.count++
	f.mu.Unlock()
	handle := &agentTerminalHandle{
		input: nopWriteCloser{Writer: &bytes.Buffer{}},
		wait: func() (int, error) {
			if f.blockExit != nil {
				<-f.blockExit
			}
			return 0, nil
		},
		kill:  func() error { return nil },
		close: func() error { return nil },
	}
	return newAgentTerminalSession(sessionID, shell, cwd, handle, false, rows, cols), nil, nil
}

func (f *fakeTerminalSpawner) spawned() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.count
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

// newAttachTestSession builds a manually managed live session for attach tests.
func newAttachTestSession(id string, ring *terminalRingBuffer) *agentTerminalSession {
	now := time.Now()
	return &agentTerminalSession{
		id:           id,
		shell:        "/bin/zsh",
		cwd:          "/tmp/attach-test",
		waiter:       func() (int, error) { return 0, nil },
		killer:       func() error { return nil },
		closer:       func() error { return nil },
		meter:        newOutputMeter(agentTerminalOutputRateWindow, agentTerminalOutputRateBytes, int64(agentTerminalOutputCapBytes)),
		token:        new(struct{}),
		startedAt:    now,
		lastActiveAt: now,
		ring:         ring,
		rows:         24,
		cols:         80,
	}
}

func TestAttachToLiveSessionReplaysAndResumes(t *testing.T) {
	m := newAgentTerminalManager()
	coll, write := newPayloadCollector()
	spawner := &fakeTerminalSpawner{}
	m.startProcess = spawner.start

	ring := newTerminalRingBuffer(1 << 20)
	ring.push([]byte("scrollback-你好-"))
	ring.push([]byte("tail"))

	var resizeRows, resizeCols int
	live := newAttachTestSession("t-live", ring)
	live.resizer = func(rows, cols int) error {
		resizeRows, resizeCols = rows, cols
		// Marker payload pins the ordering: resize must run before replay so
		// the TUI redraws (SIGWINCH) ahead of the scrollback being served.
		_ = write(map[string]interface{}{"type": "__test.resized", "session_id": "t-live"})
		return nil
	}
	m.mu.Lock()
	m.sessions["t-live"] = live
	m.mu.Unlock()

	m.create(map[string]interface{}{
		"type":       models.AgentEventTerminalCreate,
		"session_id": "t-live",
		"attach":     true,
		"rows":       40,
		"cols":       120,
	}, write)

	if got := spawner.spawned(); got != 0 {
		t.Fatalf("attach to a live session must not spawn a new shell, spawned=%d", got)
	}
	if resizeRows != 40 || resizeCols != 120 {
		t.Fatalf("resizer = (%d,%d), want (40,120)", resizeRows, resizeCols)
	}
	if markerIdx, replayIdx := coll.indexOf(func(p map[string]interface{}) bool {
		return p["type"] == "__test.resized"
	}), coll.indexOf(func(p map[string]interface{}) bool {
		return p["type"] == models.AgentEventTerminalReplay
	}); markerIdx < 0 || replayIdx < 0 || markerIdx > replayIdx {
		t.Fatalf("resize must precede replay: markerIdx=%d replayIdx=%d", markerIdx, replayIdx)
	}

	frames := coll.ofTypes(models.AgentEventTerminalReplay)
	if len(frames) != 1 {
		t.Fatalf("replay frames = %d, want 1 (small ring fits one 64KiB chunk)", len(frames))
	}
	f := frames[0]
	if f["session_id"] != "t-live" {
		t.Fatalf("replay session_id = %v, want t-live", f["session_id"])
	}
	if f["encoding"] != "text" {
		t.Fatalf("replay encoding = %v, want text", f["encoding"])
	}
	if f["data"] != "scrollback-你好-tail" {
		t.Fatalf("replay data = %q", f["data"])
	}
	if f["status"] != "live" {
		t.Fatalf("replay status = %v, want live", f["status"])
	}
	if f["final"] != true {
		t.Fatalf("replay final = %v, want true", f["final"])
	}
	if f["truncated"] != false {
		t.Fatalf("replay truncated = %v, want false", f["truncated"])
	}
	if seq, ok := f["seq"].(int); !ok || seq != 0 {
		t.Fatalf("replay seq = %v (%T), want int 0", f["seq"], f["seq"])
	}

	created := coll.ofTypes(models.AgentEventTerminalCreated)
	if len(created) != 1 {
		t.Fatalf("created payloads = %d, want 1", len(created))
	}
	c := created[0]
	if c["session_id"] != "t-live" {
		t.Fatalf("created session_id = %v", c["session_id"])
	}
	if c["resumed"] != true {
		t.Fatalf("created.resumed = %v, want true", c["resumed"])
	}
	if c["shell"] != live.shell || c["cwd"] != live.cwd {
		t.Fatalf("created shell/cwd = %v/%v, want the live session's %v/%v", c["shell"], c["cwd"], live.shell, live.cwd)
	}
	if c["rows"] != 40 || c["cols"] != 120 {
		t.Fatalf("created rows/cols = %v/%v, want 40/120", c["rows"], c["cols"])
	}
	if exited, ok := c["exited"]; ok && exited == true {
		t.Fatalf("live attach must not report exited=true")
	}
}

func TestAttachToExitedSessionServesTombstoneWithoutNewShell(t *testing.T) {
	m := newAgentTerminalManager()
	coll, write := newPayloadCollector()
	spawner := &fakeTerminalSpawner{}
	m.startProcess = spawner.start

	ring := newTerminalRingBuffer(1 << 20)
	ring.push([]byte("exited-scrollback"))

	exitedAt := time.Now().Add(-time.Minute)
	tombStartedAt := exitedAt.Add(-time.Hour).UTC().Format(time.RFC3339)
	m.mu.Lock()
	m.history["t-exited"] = &agentTerminalHistory{
		shell:     "/bin/bash",
		cwd:       "/tmp/exited-proj",
		rows:      30,
		cols:      100,
		startedAt: exitedAt.Add(-time.Hour),
		exitedAt:  exitedAt,
		exitCode:  0,
		ring:      ring,
	}
	m.mu.Unlock()

	m.create(map[string]interface{}{
		"type":       models.AgentEventTerminalCreate,
		"session_id": "t-exited",
		"attach":     true,
	}, write)

	if got := spawner.spawned(); got != 0 {
		t.Fatalf("tombstone attach must not spawn a new shell, spawned=%d", got)
	}
	if m.get("t-exited") != nil {
		t.Fatalf("tombstone attach must not register a live session")
	}

	frames := coll.ofTypes(models.AgentEventTerminalReplay)
	if len(frames) != 1 {
		t.Fatalf("replay frames = %d, want 1", len(frames))
	}
	f := frames[0]
	if f["data"] != "exited-scrollback" || f["status"] != "exited" || f["final"] != true {
		t.Fatalf("replay frame = data=%q status=%v final=%v", f["data"], f["status"], f["final"])
	}

	created := coll.ofTypes(models.AgentEventTerminalCreated)
	if len(created) != 1 {
		t.Fatalf("created payloads = %d, want 1", len(created))
	}
	c := created[0]
	if c["resumed"] != false {
		t.Fatalf("created.resumed = %v, want false", c["resumed"])
	}
	if c["exited"] != true {
		t.Fatalf("created.exited = %v, want true", c["exited"])
	}
	if c["shell"] != "/bin/bash" || c["cwd"] != "/tmp/exited-proj" {
		t.Fatalf("created shell/cwd = %v/%v, want tombstone metadata", c["shell"], c["cwd"])
	}
	if c["rows"] != 30 || c["cols"] != 100 {
		t.Fatalf("created rows/cols = %v/%v, want 30/100 from tombstone", c["rows"], c["cols"])
	}
	if startedAt, ok := c["started_at"].(string); !ok || startedAt != tombStartedAt {
		t.Fatalf("created started_at = %v, want tombstone started_at %v", c["started_at"], tombStartedAt)
	}
}

func TestAttachUnknownSessionCreatesFresh(t *testing.T) {
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
		"session_id": "t-new",
		"attach":     true,
		"shell":      "/bin/sh",
		"cwd":        dir,
	}, write)

	if got := spawner.spawned(); got != 1 {
		t.Fatalf("attach with no live session and no tombstone must create fresh, spawned=%d", got)
	}
	if frames := coll.ofTypes(models.AgentEventTerminalReplay); len(frames) != 0 {
		t.Fatalf("fresh create must not emit replay frames, got %d", len(frames))
	}
	if s := m.get("t-new"); s == nil {
		t.Fatalf("fresh create must register a live session")
	}
	created := coll.ofTypes(models.AgentEventTerminalCreated)
	if len(created) != 1 {
		t.Fatalf("created payloads = %d, want 1", len(created))
	}
	c := created[0]
	if c["resumed"] != false {
		t.Fatalf("created.resumed = %v, want false", c["resumed"])
	}
	if exited, ok := c["exited"]; ok && exited == true {
		t.Fatalf("fresh create must not report exited=true")
	}
	if c["shell"] != "/bin/sh" {
		t.Fatalf("created shell = %v, want /bin/sh", c["shell"])
	}
}

func TestAttachWithoutAttachFlagStillErrors(t *testing.T) {
	dir := t.TempDir()
	setAgentAuthorizedExecutionDirectoriesCache([]string{dir})
	t.Cleanup(func() { setAgentAuthorizedExecutionDirectoriesCache(nil) })

	m := newAgentTerminalManager()
	coll, write := newPayloadCollector()
	spawner := &fakeTerminalSpawner{}
	m.startProcess = spawner.start

	live := newAttachTestSession("t-dup", newTerminalRingBuffer(4096))
	m.mu.Lock()
	m.sessions["t-dup"] = live
	m.mu.Unlock()

	m.create(map[string]interface{}{
		"type":       models.AgentEventTerminalCreate,
		"session_id": "t-dup",
		"shell":      "/bin/sh",
		"cwd":        dir,
		"rows":       24,
		"cols":       80,
	}, write)

	errs := coll.ofTypes(models.AgentEventTerminalError)
	if len(errs) == 0 {
		t.Fatalf("duplicate create without attach must error, payloads=%v", coll.snapshot())
	}
	if !strings.Contains(fmt.Sprint(errs[0]["error"]), "already exists") {
		t.Fatalf("error = %v, want an already-exists message", errs[0]["error"])
	}
	if got := spawner.spawned(); got != 0 {
		t.Fatalf("pre-spawn duplicate guard must not spawn, spawned=%d", got)
	}
	if frames := coll.ofTypes(models.AgentEventTerminalReplay); len(frames) != 0 {
		t.Fatalf("no replay expected on duplicate error, got %d", len(frames))
	}
}

func TestSendReplayChunksLargeRingAndEmitsFinalFrameForEmptyRing(t *testing.T) {
	m := newAgentTerminalManager()
	coll, write := newPayloadCollector()

	// Empty ring: exactly one final:true frame with empty data (seq 0).
	ring := newTerminalRingBuffer(1 << 20)
	m.sendReplay("t-empty", ring, terminalReplayStatusLive, nil, write)

	// Ring slightly over one 64KiB chunk: two frames, seq 0/1, only the last
	// final, data concatenating back to the full snapshot.
	big := bytes.Repeat([]byte("x"), agentTerminalReplayChunkBytes+7)
	ringBig := newTerminalRingBuffer(4 << 20)
	ringBig.push(big)
	m.sendReplay("t-big", ringBig, terminalReplayStatusExited, nil, write)

	var emptyFrames, bigFrames []map[string]interface{}
	for _, p := range coll.ofTypes(models.AgentEventTerminalReplay) {
		switch p["session_id"] {
		case "t-empty":
			emptyFrames = append(emptyFrames, p)
		case "t-big":
			bigFrames = append(bigFrames, p)
		}
	}

	if len(emptyFrames) != 1 {
		t.Fatalf("empty ring frames = %d, want exactly one final frame", len(emptyFrames))
	}
	if emptyFrames[0]["final"] != true || emptyFrames[0]["data"] != "" {
		t.Fatalf("empty ring frame = final=%v data=%q, want final=true data=\"\"", emptyFrames[0]["final"], emptyFrames[0]["data"])
	}
	if seq, ok := emptyFrames[0]["seq"].(int); !ok || seq != 0 {
		t.Fatalf("empty ring seq = %v, want 0", emptyFrames[0]["seq"])
	}

	if len(bigFrames) != 2 {
		t.Fatalf("oversized ring frames = %d, want 2", len(bigFrames))
	}
	if got := fmt.Sprint(bigFrames[0]["data"]); len(got) != agentTerminalReplayChunkBytes || bigFrames[0]["final"] != false || bigFrames[0]["seq"] != 0 {
		t.Fatalf("first chunk: len=%d final=%v seq=%v, want %d/false/0", len(got), bigFrames[0]["final"], bigFrames[0]["seq"], agentTerminalReplayChunkBytes)
	}
	if got := fmt.Sprint(bigFrames[1]["data"]); len(got) != 7 || bigFrames[1]["final"] != true || bigFrames[1]["seq"] != 1 {
		t.Fatalf("final chunk: len=%d final=%v seq=%v, want 7/true/1", len(got), bigFrames[1]["final"], bigFrames[1]["seq"])
	}
	if bigFrames[1]["status"] != "exited" {
		t.Fatalf("big ring status = %v, want exited", bigFrames[1]["status"])
	}
	var joined strings.Builder
	joined.WriteString(fmt.Sprint(bigFrames[0]["data"]))
	joined.WriteString(fmt.Sprint(bigFrames[1]["data"]))
	if joined.Len() != len(big) {
		t.Fatalf("chunked replay lost bytes: joined=%d want %d", joined.Len(), len(big))
	}
}

func TestSendReplayHoldsOutputGateAcrossSend(t *testing.T) {
	m := newAgentTerminalManager()
	coll, write := newPayloadCollector()
	ring := newTerminalRingBuffer(4096)
	ring.push([]byte("gated"))

	var gate sync.Mutex
	gate.Lock() // simulate live output in flight
	done := make(chan struct{})
	go func() {
		defer close(done)
		m.sendReplay("t-gate", ring, terminalReplayStatusLive, &gate, write)
	}()

	select {
	case <-done:
		t.Fatalf("sendReplay must block while the output gate is held (replay must not interleave with live output)")
	case <-time.After(50 * time.Millisecond):
	}
	gate.Unlock()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("sendReplay must proceed once the gate is released")
	}
	frames := coll.ofTypes(models.AgentEventTerminalReplay)
	if len(frames) != 1 || frames[0]["data"] != "gated" {
		t.Fatalf("gated replay frames = %v", frames)
	}
}

func TestHistoryCapEvictsOldest(t *testing.T) {
	m := newAgentTerminalManager()
	base := time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)

	m.mu.Lock()
	defer m.mu.Unlock()
	for i := 0; i < agentTerminalHistoryCap+2; i++ {
		id := fmt.Sprintf("s-%02d", i)
		m.recordTerminalHistoryLocked(id, &agentTerminalHistory{
			shell:    "/bin/sh",
			rows:     24,
			cols:     80,
			exitedAt: base.Add(time.Duration(i) * time.Second),
			ring:     newTerminalRingBuffer(64),
		})
	}

	if len(m.history) != agentTerminalHistoryCap {
		t.Fatalf("history len = %d, want cap %d", len(m.history), agentTerminalHistoryCap)
	}
	if _, ok := m.history["s-00"]; ok {
		t.Fatalf("oldest tombstone must be evicted")
	}
	if _, ok := m.history["s-01"]; ok {
		t.Fatalf("second oldest tombstone must be evicted")
	}
	for i := 2; i < agentTerminalHistoryCap+2; i++ {
		id := fmt.Sprintf("s-%02d", i)
		if h, ok := m.history[id]; !ok || h == nil || h.ring == nil {
			t.Fatalf("tombstone %s missing or incomplete: %+v", id, h)
		}
	}
}

func TestCloseAllTombstonesSessions(t *testing.T) {
	dir := t.TempDir()
	setAgentAuthorizedExecutionDirectoriesCache([]string{dir})
	t.Cleanup(func() { setAgentAuthorizedExecutionDirectoriesCache(nil) })

	m := newAgentTerminalManager()
	_, write := newPayloadCollector()
	blockExit := make(chan struct{})
	spawner := &fakeTerminalSpawner{blockExit: blockExit}
	m.startProcess = spawner.start
	t.Cleanup(func() { close(blockExit) })

	m.create(map[string]interface{}{
		"type":       models.AgentEventTerminalCreate,
		"session_id": "t-closeall",
		"shell":      "/bin/sh",
		"cwd":        dir,
		"rows":       20,
		"cols":       70,
	}, write)
	if m.get("t-closeall") == nil {
		t.Fatalf("session must be live before closeAll")
	}
	m.mu.Lock()
	m.sessions["t-closeall"].ring.push([]byte("last-bytes"))
	m.mu.Unlock()

	m.closeAll()

	m.mu.Lock()
	tomb := m.history["t-closeall"]
	_, stillLive := m.sessions["t-closeall"]
	m.mu.Unlock()
	if stillLive || tomb == nil {
		t.Fatalf("closeAll must tombstone sessions (tomb=%v stillLive=%v)", tomb, stillLive)
	}
	if tomb.exitCode != -1 {
		t.Fatalf("closeAll tombstone exitCode = %d, want -1 (killed, unknown)", tomb.exitCode)
	}
	if tomb.rows != 20 || tomb.cols != 70 || tomb.shell != "/bin/sh" {
		t.Fatalf("closeAll tombstone metadata = shell=%v rows=%v cols=%v", tomb.shell, tomb.rows, tomb.cols)
	}
	if got := string(tomb.ring.snapshot()); got != "last-bytes" {
		t.Fatalf("closeAll tombstone ring = %q, want last-bytes", got)
	}
}

func TestExitPathMovesSessionIntoHistory(t *testing.T) {
	dir := t.TempDir()
	setAgentAuthorizedExecutionDirectoriesCache([]string{dir})
	t.Cleanup(func() { setAgentAuthorizedExecutionDirectoriesCache(nil) })

	m := newAgentTerminalManager()
	_, write := newPayloadCollector()
	blockExit := make(chan struct{})
	spawner := &fakeTerminalSpawner{blockExit: blockExit}
	m.startProcess = spawner.start

	m.create(map[string]interface{}{
		"type":       models.AgentEventTerminalCreate,
		"session_id": "t-die",
		"shell":      "/bin/sh",
		"cwd":        dir,
		"rows":       33,
		"cols":       111,
	}, write)
	live := m.get("t-die")
	if live == nil {
		t.Fatalf("session must be live after create")
	}
	live.ring.push([]byte("pre-exit-bytes"))

	close(blockExit) // the fake process exits

	deadline := time.Now().Add(5 * time.Second)
	for {
		m.mu.Lock()
		tomb := m.history["t-die"]
		_, stillLive := m.sessions["t-die"]
		m.mu.Unlock()
		if tomb != nil && !stillLive {
			if tomb.rows != 33 || tomb.cols != 111 || tomb.shell != "/bin/sh" {
				t.Fatalf("tombstone metadata = shell=%v rows=%v cols=%v", tomb.shell, tomb.rows, tomb.cols)
			}
			if tomb.exitCode != 0 {
				t.Fatalf("tombstone exitCode = %d, want 0", tomb.exitCode)
			}
			if tomb.exitedAt.IsZero() {
				t.Fatalf("tombstone exitedAt must be set")
			}
			if got := string(tomb.ring.snapshot()); got != "pre-exit-bytes" {
				t.Fatalf("tombstone ring = %q, want pre-exit-bytes", got)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("session exit must move ring+metadata into history (tomb=%v stillLive=%v)", tomb, stillLive)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
