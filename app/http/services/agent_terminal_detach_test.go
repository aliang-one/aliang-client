package services

import (
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"aliang.one/nursorgate/app/http/models"
)

// shrinkDetachedWatch points the idle watcher and the detached-idle threshold
// at tiny test values and restores both when the test ends. Callers must not
// use t.Parallel (package vars are process-global).
func shrinkDetachedWatch(t *testing.T, watchInterval, detachedIdle time.Duration) {
	t.Helper()
	origInterval, origIdle := agentTerminalIdleWatchInterval, agentTerminalDetachedIdle
	agentTerminalIdleWatchInterval = watchInterval
	agentTerminalDetachedIdle = detachedIdle
	t.Cleanup(func() {
		agentTerminalIdleWatchInterval = origInterval
		agentTerminalDetachedIdle = origIdle
	})
}

// killRecorder replaces a session's killer with one that records kills in an
// atomic counter so tests can assert whether the PTY process was touched.
func killRecorder(s *agentTerminalSession) *int32 {
	var kills int32
	s.killer = func() error {
		atomic.AddInt32(&kills, 1)
		return nil
	}
	return &kills
}

func TestMarkAllDetachedKeepsProcessesAlive(t *testing.T) {
	m := newAgentTerminalManager()
	live := newAttachTestSession("t-detach", newTerminalRingBuffer(4096))
	kills := killRecorder(live)
	m.mu.Lock()
	m.sessions["t-detach"] = live
	m.mu.Unlock()

	m.markAllDetached()

	if live.detachedAt.IsZero() {
		t.Fatalf("markAllDetached must stamp detachedAt (zero = still attached)")
	}
	if got := m.get("t-detach"); got == nil {
		t.Fatalf("markAllDetached must keep the session registered: the PTY process stays alive")
	}
	if got := atomic.LoadInt32(kills); got != 0 {
		t.Fatalf("markAllDetached must not kill the PTY process, kills=%d", got)
	}

	// A second transient disconnect (no reconnect in between) re-stamps the
	// moment and still must not touch the process.
	first := live.detachedAt
	time.Sleep(2 * time.Millisecond)
	m.markAllDetached()
	if !live.detachedAt.After(first) {
		t.Fatalf("second disconnect must re-stamp detachedAt (%v not after %v)", live.detachedAt, first)
	}
	if got := atomic.LoadInt32(kills); got != 0 {
		t.Fatalf("markAllDetached must never kill, kills=%d", got)
	}
}

func TestAttachLiveRearmsAttachedState(t *testing.T) {
	m := newAgentTerminalManager()
	coll, write := newPayloadCollector()
	live := newAttachTestSession("t-rearm", newTerminalRingBuffer(4096))
	live.ring.push([]byte("still-here"))
	m.mu.Lock()
	m.sessions["t-rearm"] = live
	m.mu.Unlock()

	rearm := time.Now()
	m.markAllDetached()
	m.create(map[string]interface{}{
		"type":       models.AgentEventTerminalCreate,
		"session_id": "t-rearm",
		"attach":     true,
		"rows":       24,
		"cols":       80,
	}, write)

	if !live.detachedAt.IsZero() {
		t.Fatalf("a successful live attach must re-arm attached state (zero detachedAt), got %v", live.detachedAt)
	}
	// Re-arming attached state must include the idle clocks, not just the
	// detach stamp: a session that sat detached and fully silent carries
	// stale lastActiveAt/lastInputAt, and without a refresh the very next
	// watchTerminalIdle tick would kill the shell the user just attached to.
	if live.lastActiveAt.Before(rearm) {
		t.Fatalf("attach must re-arm the attached activity clock (lastActiveAt=%v predates the attach)", live.lastActiveAt)
	}
	if live.lastInputAt.Before(rearm) {
		t.Fatalf("attach must re-arm the input clock for a future detach (lastInputAt=%v predates the attach)", live.lastInputAt)
	}
	created := coll.ofTypes(models.AgentEventTerminalCreated)
	if len(created) != 1 || created[0]["resumed"] != true {
		t.Fatalf("attach must confirm resume, created=%v", created)
	}
	if frames := coll.ofTypes(models.AgentEventTerminalReplay); len(frames) != 1 {
		t.Fatalf("attach must replay the scrollback, frames=%d", len(frames))
	}
}

// TestReattachedStaleSessionSurvivesIdleWatcher pins the clock half of the
// reap race end-to-end: a session that sat detached and fully silent past
// every idle budget still carries stale clocks when re-attached. The watcher
// must not kill the shell the user just attached to — attachLive re-arms the
// idle clocks, so every post-attach tick evaluates against a fresh budget.
func TestReattachedStaleSessionSurvivesIdleWatcher(t *testing.T) {
	shrinkDetachedWatch(t, 5*time.Millisecond, 30*time.Minute)
	m := newAgentTerminalManager()
	live := newAttachTestSession("t-survive", newTerminalRingBuffer(4096))
	stale := time.Now().Add(-2 * time.Hour) // past every idle budget
	live.lastActiveAt = stale
	live.lastInputAt = stale
	live.detachedAt = stale
	kills := killRecorder(live)
	m.mu.Lock()
	m.sessions["t-survive"] = live
	m.mu.Unlock()

	if !m.attachLive("t-survive", 24, 80, func(interface{}) error { return nil }) {
		t.Fatalf("attachLive must re-attach a live session")
	}
	go m.watchTerminalIdle("t-survive", live.token, func(interface{}) error { return nil })

	// Several watcher ticks after the attach: the stale pre-attach idle state
	// must not reap the freshly re-attached shell.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if got := atomic.LoadInt32(kills); got != 0 {
			t.Fatalf("watchTerminalIdle killed the session %d time(s) right after re-attach: attach must re-arm the idle clocks", got)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if m.get("t-survive") == nil {
		t.Fatalf("re-attached session must still be registered")
	}
}

// TestAttachLiveRetractsCommittedReap pins the execution half of the reap
// race: watchTerminalIdle decides a reap under m.mu but kills after
// unlocking, so a re-attach landing in that window must retract the kill.
// The error-frame write is the handshake point where the attach lands.
func TestAttachLiveRetractsCommittedReap(t *testing.T) {
	shrinkDetachedWatch(t, 5*time.Millisecond, 30*time.Minute)
	m := newAgentTerminalManager()
	live := newAttachTestSession("t-retract", newTerminalRingBuffer(4096))
	stale := time.Now().Add(-2 * time.Hour) // past every idle budget
	live.lastActiveAt = stale
	live.lastInputAt = stale
	live.detachedAt = stale
	kills := killRecorder(live)
	m.mu.Lock()
	m.sessions["t-retract"] = live
	m.mu.Unlock()

	var committed, errorFrames int32
	write := func(v interface{}) error {
		if p, ok := v.(map[string]interface{}); ok && p["type"] == models.AgentEventTerminalError {
			atomic.AddInt32(&errorFrames, 1)
			// The reap has been decided but not yet executed: the user
			// re-attaches exactly here.
			if atomic.CompareAndSwapInt32(&committed, 0, 1) {
				if !m.attachLive("t-retract", 24, 80, func(interface{}) error { return nil }) {
					t.Error("attachLive must still find the live session")
				}
			}
		}
		return nil
	}
	go m.watchTerminalIdle("t-retract", live.token, write)

	deadline := time.Now().Add(5 * time.Second)
	for atomic.LoadInt32(&committed) == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("watcher never committed a reap decision for the long-silent detached session")
		}
		time.Sleep(2 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond) // let the committed decision run to its kill point

	if got := atomic.LoadInt32(kills); got != 0 {
		t.Fatalf("a committed reap must be retractable by a concurrent attach, kills=%d", got)
	}
	if m.get("t-retract") == nil {
		t.Fatalf("a retracted reap must leave the session registered")
	}
	if !live.detachedAt.IsZero() {
		t.Fatalf("the retracting attach must have re-armed attached state, detachedAt=%v", live.detachedAt)
	}
	if got := atomic.LoadInt32(&errorFrames); got != 1 {
		t.Fatalf("terminal.error frames = %d, want exactly 1 (a retracted reap must not re-fire every tick)", got)
	}
}

func TestDetachedIdleReapRules(t *testing.T) {
	shrinkDetachedWatch(t, time.Minute, 30*time.Minute)
	now := time.Now()
	cases := []struct {
		name    string
		session *agentTerminalSession
		want    string
	}{
		{
			name:    "attached and recently active survives",
			session: makeDetachRuleSession(false, now, now),
			want:    "",
		},
		{
			name:    "attached past the activity timeout is reaped (legacy behavior)",
			session: makeDetachRuleSession(false, now.Add(-time.Hour), now.Add(-time.Hour)),
			want:    "idle timeout",
		},
		{
			name:    "attached with fresh activity but ancient input survives (activity timer rules)",
			session: makeDetachRuleSession(false, now, now.Add(-time.Hour)),
			want:    "",
		},
		{
			name:    "detached with recent input survives",
			session: makeDetachRuleSession(true, now, now),
			want:    "",
		},
		{
			name:    "detached past the input timeout is reaped",
			session: makeDetachRuleSession(true, now.Add(-time.Hour), now.Add(-time.Hour)),
			want:    "without input",
		},
		{
			name:    "detached with recent OUTPUT but stale input is reaped (output does not extend life)",
			session: makeDetachRuleSession(true, now, now.Add(-time.Hour)),
			want:    "without input",
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			got := terminalIdleReapReason(test.session)
			if test.want == "" {
				if got != "" {
					t.Fatalf("reap reason = %q, want none", got)
				}
				return
			}
			if !strings.Contains(got, test.want) {
				t.Fatalf("reap reason = %q, want it to contain %q", got, test.want)
			}
		})
	}
}

// makeDetachRuleSession builds a session for terminalIdleReapReason cases:
// lastActiveAt/lastInputAt are absolute times and detached controls the
// detachedAt stamp.
func makeDetachRuleSession(detached bool, lastActiveAt, lastInputAt time.Time) *agentTerminalSession {
	s := newAttachTestSession("t-rules", newTerminalRingBuffer(64))
	s.lastActiveAt = lastActiveAt
	s.lastInputAt = lastInputAt
	if detached {
		s.detachedAt = lastActiveAt
	}
	return s
}

func TestDetachedIdleReapKillsAfterInputIdle(t *testing.T) {
	shrinkDetachedWatch(t, 5*time.Millisecond, 40*time.Millisecond)
	m := newAgentTerminalManager()
	coll, write := newPayloadCollector()
	live := newAttachTestSession("t-reap", newTerminalRingBuffer(4096))
	live.lastInputAt = time.Now().Add(-time.Second) // input idle far past the 40ms threshold
	live.lastActiveAt = time.Now()
	live.detachedAt = time.Now()
	var kills int32
	killed := make(chan struct{})
	live.killer = func() error {
		atomic.AddInt32(&kills, 1)
		close(killed)
		return nil
	}
	// Mirror production wiring: create() starts waitTerminal next to the idle
	// watcher, and waitTerminal owns the live-map cleanup + tombstone.
	live.waiter = func() (int, error) {
		<-killed
		return 0, nil
	}
	m.mu.Lock()
	m.sessions["t-reap"] = live
	m.mu.Unlock()

	watchDone := make(chan struct{})
	go func() {
		m.watchTerminalIdle("t-reap", live.token, write)
		close(watchDone)
	}()
	go m.waitTerminal("t-reap", live.token, write)

	deadline := time.Now().Add(5 * time.Second)
	for atomic.LoadInt32(&kills) == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("detached session with stale input must be reaped by watchTerminalIdle")
		}
		time.Sleep(5 * time.Millisecond)
	}

	errs := coll.ofTypes(models.AgentEventTerminalError)
	if len(errs) == 0 {
		t.Fatalf("reap must emit a terminal.error explaining the kill")
	}
	if !strings.Contains(fmt.Sprint(errs[0]["error"]), "without input") {
		t.Fatalf("reap error = %v, want a without-input reason", errs[0]["error"])
	}

	// waitTerminal settles after the kill: live map entry dropped, tombstone left.
	tombDeadline := time.Now().Add(5 * time.Second)
	for {
		m.mu.Lock()
		_, stillLive := m.sessions["t-reap"]
		tomb := m.history["t-reap"]
		m.mu.Unlock()
		if !stillLive && tomb != nil {
			break
		}
		if time.Now().After(tombDeadline) {
			t.Fatalf("reaped session must settle into history (stillLive=%t tomb=%v)", stillLive, tomb)
		}
		time.Sleep(5 * time.Millisecond)
	}

	select {
	case <-watchDone:
	case <-time.After(2 * time.Second):
		t.Fatalf("watchTerminalIdle must return after reaping its session")
	}
}

func TestDetachedOutputDoesNotExtendLife(t *testing.T) {
	shrinkDetachedWatch(t, 5*time.Millisecond, 60*time.Millisecond)
	m := newAgentTerminalManager()
	live := newAttachTestSession("t-out", newTerminalRingBuffer(4096))
	live.lastInputAt = time.Now().Add(-time.Second)
	live.lastActiveAt = time.Now()
	live.detachedAt = time.Now()
	kills := killRecorder(live)
	m.mu.Lock()
	m.sessions["t-out"] = live
	m.mu.Unlock()

	go m.watchTerminalIdle("t-out", live.token, func(v interface{}) error { return nil })

	// Keep the session visibly "active" with fresh output while its input idle
	// clock runs out: output must refresh lastActiveAt (attached semantics) but
	// must NOT save a detached session.
	deadline := time.Now().Add(5 * time.Second)
	for atomic.LoadInt32(kills) == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("detached session must be reaped despite ongoing output (input timer rules)")
		}
		if m.acceptTerminalOutput("t-out", 16) {
			t.Fatalf("16-byte paced output must not trip the flood limiter")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if live.lastActiveAt.IsZero() || !live.lastActiveAt.After(time.Now().Add(-time.Second)) {
		t.Fatalf("output must have kept refreshing lastActiveAt, got %v", live.lastActiveAt)
	}
}

func TestForceDisconnectStillKillsAll(t *testing.T) {
	m := newAgentTerminalManager()
	live := newAttachTestSession("t-force", newTerminalRingBuffer(4096))
	kills := killRecorder(live)
	m.mu.Lock()
	m.sessions["t-force"] = live
	m.mu.Unlock()
	svc := &AgentService{terminal: m, ai: newAgentAIManager()}

	svc.forceDisconnectRemote("disabled")

	if got := atomic.LoadInt32(kills); got != 1 {
		t.Fatalf("forceDisconnectRemote must kill live terminal sessions, kills=%d", got)
	}
	if got := m.get("t-force"); got != nil {
		t.Fatalf("forceDisconnectRemote must drop the session from the live map")
	}
	m.mu.Lock()
	tomb := m.history["t-force"]
	m.mu.Unlock()
	if tomb == nil || tomb.exitCode != -1 {
		t.Fatalf("forceDisconnectRemote must tombstone the killed session (tomb=%v)", tomb)
	}
}

func TestEnvDurationParsing(t *testing.T) {
	cases := []struct {
		key  string
		def  time.Duration
		env  string
		want time.Duration
	}{
		{"ALIANG_TERMINAL_DETACHED_IDLE", 30 * time.Minute, "", 30 * time.Minute},
		{"ALIANG_TERMINAL_DETACHED_IDLE", 30 * time.Minute, "   ", 30 * time.Minute},
		{"ALIANG_TERMINAL_DETACHED_IDLE", 30 * time.Minute, "45m", 45 * time.Minute},
		{"ALIANG_TERMINAL_DETACHED_IDLE", 30 * time.Minute, "1h30m", 90 * time.Minute},
		{"ALIANG_TERMINAL_DETACHED_IDLE", 30 * time.Minute, "garbage", 30 * time.Minute},
		{"ALIANG_TERMINAL_DETACHED_IDLE", 30 * time.Minute, "0s", 30 * time.Minute},
		{"ALIANG_TERMINAL_DETACHED_IDLE", 30 * time.Minute, "-5m", 30 * time.Minute},
		{"ALIANG_TERMINAL_HISTORY_TTL", 24 * time.Hour, "", 24 * time.Hour},
		{"ALIANG_TERMINAL_HISTORY_TTL", 24 * time.Hour, "48h", 48 * time.Hour},
		{"ALIANG_TERMINAL_HISTORY_TTL", 24 * time.Hour, "garbage", 24 * time.Hour},
		{"ALIANG_TERMINAL_HISTORY_TTL", 24 * time.Hour, "0", 24 * time.Hour},
		{"ALIANG_TERMINAL_HISTORY_TTL", 24 * time.Hour, "-1h", 24 * time.Hour},
	}
	for _, test := range cases {
		t.Run(test.key+"="+test.env, func(t *testing.T) {
			t.Setenv(test.key, test.env)
			if got := resolveEnvDuration(test.key, test.def); got != test.want {
				t.Fatalf("resolveEnvDuration(%q, %s) with env %q = %s, want %s", test.key, test.def, test.env, got, test.want)
			}
		})
	}
}

func TestHistoryTTLReapsExpiredTombstones(t *testing.T) {
	m := newAgentTerminalManager()
	now := time.Now()
	m.mu.Lock()
	m.history["t-old"] = &agentTerminalHistory{shell: "/bin/sh", exitedAt: now.Add(-48 * time.Hour), ring: newTerminalRingBuffer(64)}
	m.history["t-fresh"] = &agentTerminalHistory{shell: "/bin/sh", exitedAt: now.Add(-time.Minute), ring: newTerminalRingBuffer(64)}
	m.mu.Unlock()

	dropped := m.reapExpiredHistory(now)

	if dropped != 1 {
		t.Fatalf("reapExpiredHistory dropped %d tombstones, want 1", dropped)
	}
	m.mu.Lock()
	_, oldGone := m.history["t-old"]
	fresh := m.history["t-fresh"]
	m.mu.Unlock()
	if oldGone {
		t.Fatalf("tombstone past the history TTL must be dropped (its ring freed)")
	}
	if fresh == nil {
		t.Fatalf("tombstone within the TTL must be kept")
	}
}
