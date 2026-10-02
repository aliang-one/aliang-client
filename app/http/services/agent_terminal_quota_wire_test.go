package services

import (
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"testing"

	"aliang.one/nursorgate/app/http/models"
)

// Wire-level tests for the terminal output quota challenge (spec §5.2/§6.1):
// the copyTerminalOutput loop and the terminal.quota.resolved handler. Sessions
// carry a shrunk quota policy (checkpoint 100 bytes, hard cap 1000) with an
// inert rate window (1 GiB per 5s) so every case exercises only the quota
// path — TestTerminalQuotaWire_RateTrip builds its own meter with a live rate
// window. minInterval is 0: the dual-gate interval suppression is B3's
// coverage (Quota_DualGateIntervalBlocks); here every checkpoint crossing is
// eligible.

const (
	quotaWireCheckpoint = 100  // bytes per challenge checkpoint
	quotaWireMax        = 1000 // bytes — hard cap (max_bytes)
)

// newQuotaWireSession builds a manually registered live session (mirrors
// newAttachTestSession) whose meter carries the shrunk quota policy.
func newQuotaWireSession(m *agentTerminalManager, id string) *agentTerminalSession {
	s := newAttachTestSession(id, newTerminalRingBuffer(1<<20))
	s.meter = newTerminalOutputMeter(agentTerminalOutputRateWindow, 1<<30,
		newQuotaPolicy(quotaWireCheckpoint, quotaWireMax, 0))
	m.mu.Lock()
	m.sessions[id] = s
	m.mu.Unlock()
	return s
}

// quotaChunkReader hands the copy loop exact per-Read byte counts (one chunk
// per Read call, never split), so each meter record sees a deterministic size
// and checkpoint crossings are exact. Chunk i is filled with byte('a'+i) so
// ring content assertions can tell chunks apart. EOF follows the last chunk.
type quotaChunkReader struct {
	chunks []int
	i      int
}

func (r *quotaChunkReader) Read(buf []byte) (int, error) {
	if r.i >= len(r.chunks) {
		return 0, io.EOF
	}
	n := r.chunks[r.i]
	if n > len(buf) {
		panic("quota wire test chunk exceeds the copy loop's read buffer")
	}
	fill := byte('a' + r.i)
	for i := 0; i < n; i++ {
		buf[i] = fill
	}
	r.i++
	return n, nil
}

// frameTypes returns the "type" of every collected frame in arrival order.
func frameTypes(c *payloadCollector) []string {
	var out []string
	for _, p := range c.snapshot() {
		out = append(out, fmt.Sprint(p["type"]))
	}
	return out
}

// TestTerminalQuotaWire_ChallengeDoesNotBlockStream pins spec §5.2 rule 4 from
// the wire side: crossing a checkpoint emits ONE terminal.quota.challenge_
// required frame with the exact §6.1 payload, and the stream KEEPS STREAMING —
// the checkpoint-crossing chunk itself still reaches ring and live writer.
func TestTerminalQuotaWire_ChallengeDoesNotBlockStream(t *testing.T) {
	m := newAgentTerminalManager()
	coll, write := newPayloadCollector()
	session := newQuotaWireSession(m, "t-q-issue")
	kills := killRecorder(session)

	m.copyTerminalOutput("t-q-issue", &quotaChunkReader{chunks: []int{quotaWireCheckpoint, 50}}, write)

	challenges := coll.ofTypes(models.AgentEventTerminalQuotaChallengeRequired)
	if len(challenges) != 1 {
		t.Fatalf("challenge frames = %d, want 1 (one checkpoint crossing), frames=%v",
			len(challenges), frameTypes(coll))
	}
	f := challenges[0]
	if f["session_id"] != "t-q-issue" {
		t.Fatalf("challenge session_id = %v, want t-q-issue", f["session_id"])
	}
	if seq, ok := f["seq"].(uint64); !ok || seq != 1 {
		t.Fatalf("challenge seq = %v (%T), want uint64 1", f["seq"], f["seq"])
	}
	if used, ok := f["used_bytes"].(int64); !ok || used != quotaWireCheckpoint {
		t.Fatalf("challenge used_bytes = %v (%T), want int64 %d", f["used_bytes"], f["used_bytes"], quotaWireCheckpoint)
	}
	if killAt, ok := f["kill_at_bytes"].(int64); !ok || killAt != 2*quotaWireCheckpoint {
		t.Fatalf("challenge kill_at_bytes = %v (%T), want int64 %d (the NEXT checkpoint, not max_bytes)",
			f["kill_at_bytes"], f["kill_at_bytes"], 2*quotaWireCheckpoint)
	}
	if max, ok := f["max_bytes"].(int64); !ok || max != quotaWireMax {
		t.Fatalf("challenge max_bytes = %v (%T), want int64 %d", f["max_bytes"], f["max_bytes"], quotaWireMax)
	}
	// The challenge is a control-plane frame: it precedes the output of the
	// chunk that crossed the checkpoint, and every byte still flows.
	wantTypes := []string{
		models.AgentEventTerminalQuotaChallengeRequired,
		models.AgentEventTerminalOutput,
		models.AgentEventTerminalOutput,
	}
	if got := frameTypes(coll); fmt.Sprint(got) != fmt.Sprint(wantTypes) {
		t.Fatalf("frame sequence = %v, want %v (challenge then both output chunks)", got, wantTypes)
	}
	if got := string(session.ring.snapshot()); got != strings.Repeat("a", quotaWireCheckpoint)+strings.Repeat("b", 50) {
		t.Fatalf("ring = %d bytes, want the full unblocked stream (%d bytes)", len(got), quotaWireCheckpoint+50)
	}
	if errs := coll.ofTypes(models.AgentEventTerminalError); len(errs) != 0 {
		t.Fatalf("a challenge must not error, got %v", errs)
	}
	if got := atomic.LoadInt32(kills); got != 0 {
		t.Fatalf("a challenge must not kill, kills=%d", got)
	}
}

// TestTerminalQuotaWire_UnansweredChallengeKillsAtNextCheckpoint pins the
// kill reason contract: an unanswered challenge kills at kill_at_bytes with a
// reason whose prefix is the cross-repo token quota_unanswered (the phone
// humanizes terminal.exit by matching the "quota_" prefix) plus the spec §5.2
// rule 3 remediation hint. The killing chunk itself is never streamed — the
// stream stops AT the kill point.
func TestTerminalQuotaWire_UnansweredChallengeKillsAtNextCheckpoint(t *testing.T) {
	m := newAgentTerminalManager()
	coll, write := newPayloadCollector()
	session := newQuotaWireSession(m, "t-q-unanswered")
	kills := killRecorder(session)

	m.copyTerminalOutput("t-q-unanswered",
		&quotaChunkReader{chunks: []int{quotaWireCheckpoint, quotaWireCheckpoint}}, write)

	errs := coll.ofTypes(models.AgentEventTerminalError)
	if len(errs) != 1 {
		t.Fatalf("error frames = %d, want exactly one kill frame, frames=%v", len(errs), frameTypes(coll))
	}
	want := "quota_unanswered: output quota exhausted (unanswered checkpoint 200 bytes) — for long-running noisy commands use: <cmd> > log 2>&1"
	if errs[0]["error"] != want {
		t.Fatalf("kill reason = %q, want %q", errs[0]["error"], want)
	}
	if got := atomic.LoadInt32(kills); got != 1 {
		t.Fatalf("the session must be killed exactly once, kills=%d", got)
	}
	if got := string(session.ring.snapshot()); got != strings.Repeat("a", quotaWireCheckpoint) {
		t.Fatalf("ring = %d bytes, want only the pre-kill chunk (%d) — the stream stops at the kill point",
			len(got), quotaWireCheckpoint)
	}
}

// TestTerminalQuotaWire_HardCapKillsRegardlessOfPending pins the third kill
// token: max_bytes kills even when the pending challenge's own kill point was
// never reached, and hard-cap is evaluated first (spec §5.2 rule 2).
func TestTerminalQuotaWire_HardCapKillsRegardlessOfPending(t *testing.T) {
	m := newAgentTerminalManager()
	coll, write := newPayloadCollector()
	session := newQuotaWireSession(m, "t-q-hardcap")
	kills := killRecorder(session)

	// 100 bytes issue seq=1 (kill point 200), then one fat chunk straight to
	// the 1000-byte hard cap — the 200-byte unanswered point is skipped.
	m.copyTerminalOutput("t-q-hardcap",
		&quotaChunkReader{chunks: []int{quotaWireCheckpoint, quotaWireMax - quotaWireCheckpoint}}, write)

	errs := coll.ofTypes(models.AgentEventTerminalError)
	if len(errs) != 1 {
		t.Fatalf("error frames = %d, want exactly one kill frame, frames=%v", len(errs), frameTypes(coll))
	}
	want := "quota_hard_cap: output quota exhausted (hard cap 1000 bytes) — for long-running noisy commands use: <cmd> > log 2>&1"
	if errs[0]["error"] != want {
		t.Fatalf("kill reason = %q, want %q", errs[0]["error"], want)
	}
	if got := atomic.LoadInt32(kills); got != 1 {
		t.Fatalf("the hard cap must kill exactly once, kills=%d", got)
	}
}

// TestTerminalQuotaWire_GrantRenewsAndStreamContinues pins the granted half of
// terminal.quota.resolved: pending clears with no frame and no kill, the next
// checkpoint issues a renewed challenge (seq=2), and the stream never stopped.
func TestTerminalQuotaWire_GrantRenewsAndStreamContinues(t *testing.T) {
	m := newAgentTerminalManager()
	coll, write := newPayloadCollector()
	session := newQuotaWireSession(m, "t-q-grant")
	kills := killRecorder(session)

	// First reader crosses checkpoint 1 → challenge seq=1.
	m.copyTerminalOutput("t-q-grant", &quotaChunkReader{chunks: []int{quotaWireCheckpoint}}, write)
	// The human grants (spec §6.2): pending cleared silently.
	m.quotaResolved(map[string]interface{}{
		"session_id": "t-q-grant", "seq": 1, "verdict": "granted",
	}, write)
	// Second reader crosses checkpoint 2 → renewed challenge seq=2.
	m.copyTerminalOutput("t-q-grant", &quotaChunkReader{chunks: []int{quotaWireCheckpoint}}, write)

	challenges := coll.ofTypes(models.AgentEventTerminalQuotaChallengeRequired)
	if len(challenges) != 2 {
		t.Fatalf("challenge frames = %d, want 2 (original + renewed), frames=%v",
			len(challenges), frameTypes(coll))
	}
	if seq, ok := challenges[1]["seq"].(uint64); !ok || seq != 2 {
		t.Fatalf("renewed challenge seq = %v, want 2", challenges[1]["seq"])
	}
	if errs := coll.ofTypes(models.AgentEventTerminalError); len(errs) != 0 {
		t.Fatalf("a grant must not error, got %v", errs)
	}
	if got := atomic.LoadInt32(kills); got != 0 {
		t.Fatalf("a grant must not kill, kills=%d", got)
	}
	if got := len(string(session.ring.snapshot())); got != 2*quotaWireCheckpoint {
		t.Fatalf("ring = %d bytes, want the full stream (%d)", got, 2*quotaWireCheckpoint)
	}
}

// TestTerminalQuotaWire_ResolvedDispatch covers the terminal.quota.resolved
// handler itself: a live denied verdict kills with its own reason token, stale
// seq verdicts are ignored without frames or state change, and verdicts for
// dead sessions are dropped silently.
func TestTerminalQuotaWire_ResolvedDispatch(t *testing.T) {
	t.Run("DeniedKillsWithFrame", func(t *testing.T) {
		m := newAgentTerminalManager()
		coll, write := newPayloadCollector()
		session := newQuotaWireSession(m, "t-q-denied")
		kills := killRecorder(session)

		m.copyTerminalOutput("t-q-denied", &quotaChunkReader{chunks: []int{quotaWireCheckpoint}}, write) // issue seq=1
		m.quotaResolved(map[string]interface{}{
			"session_id": "t-q-denied", "seq": 1, "verdict": "denied",
		}, write)

		errs := coll.ofTypes(models.AgentEventTerminalError)
		want := "quota_denied: output quota challenge denied by user"
		if len(errs) != 1 || errs[0]["error"] != want {
			t.Fatalf("denied must emit exactly one kill frame %q, got %v", want, errs)
		}
		if got := atomic.LoadInt32(kills); got != 1 {
			t.Fatalf("denied must kill exactly once, kills=%d", got)
		}
	})

	t.Run("StaleSeqIgnored", func(t *testing.T) {
		m := newAgentTerminalManager()
		coll, write := newPayloadCollector()
		session := newQuotaWireSession(m, "t-q-stale")
		kills := killRecorder(session)

		m.copyTerminalOutput("t-q-stale", &quotaChunkReader{chunks: []int{quotaWireCheckpoint}}, write) // issue seq=1
		framesBefore := len(coll.snapshot())
		m.quotaResolved(map[string]interface{}{"session_id": "t-q-stale", "seq": 999, "verdict": "granted"}, write)
		m.quotaResolved(map[string]interface{}{"session_id": "t-q-stale", "seq": 999, "verdict": "denied"}, write)
		if got := len(coll.snapshot()); got != framesBefore {
			t.Fatalf("stale resolutions must emit nothing, frames %d → %d", framesBefore, got)
		}
		if got := atomic.LoadInt32(kills); got != 0 {
			t.Fatalf("stale resolutions must not kill, kills=%d", got)
		}
		// The stale grant did NOT clear pending: the kill point still fires,
		// proving the state machine ignored the expired seq (spec §5.2 rule 5).
		m.copyTerminalOutput("t-q-stale", &quotaChunkReader{chunks: []int{quotaWireCheckpoint}}, write)
		errs := coll.ofTypes(models.AgentEventTerminalError)
		if len(errs) != 1 || !strings.HasPrefix(fmt.Sprint(errs[0]["error"]), "quota_unanswered") {
			t.Fatalf("the challenge must still be pending after a stale grant, errors=%v", errs)
		}
		if got := atomic.LoadInt32(kills); got != 1 {
			t.Fatalf("the surviving pending challenge must still kill, kills=%d", got)
		}
	})

	t.Run("DeadSessionDroppedSilently", func(t *testing.T) {
		m := newAgentTerminalManager()
		coll, write := newPayloadCollector()
		// The session died and was reaped before the verdict arrived: its
		// challenge is moot, so the verdict is dropped without a frame.
		m.quotaResolved(map[string]interface{}{
			"session_id": "t-q-gone", "seq": 1, "verdict": "denied",
		}, write)
		if got := len(coll.snapshot()); got != 0 {
			t.Fatalf("a verdict for a dead session must be dropped silently, got %v", coll.snapshot())
		}
	})

	t.Run("MissingSessionIDErrors", func(t *testing.T) {
		m := newAgentTerminalManager()
		coll, write := newPayloadCollector()
		m.quotaResolved(map[string]interface{}{"seq": 1, "verdict": "granted"}, write)
		errs := coll.ofTypes(models.AgentEventTerminalError)
		if len(errs) != 1 || !strings.Contains(fmt.Sprint(errs[0]["error"]), "missing session_id") {
			t.Fatalf("a verdict without session_id must error like terminal.create, got %v", errs)
		}
	})

	t.Run("UnknownVerdictRejected", func(t *testing.T) {
		// A verdict is only ever granted or denied (spec §6.2). An unknown
		// verdict is a protocol deviation from a version-skewed server, NOT a
		// human rejection: falling through to denied would accelerate the
		// kill — the opposite of what this liveness mechanism exists for — so
		// it must be rejected with an error frame and the pending challenge
		// left untouched.
		m := newAgentTerminalManager()
		coll, write := newPayloadCollector()
		session := newQuotaWireSession(m, "t-q-snooze")
		kills := killRecorder(session)

		m.copyTerminalOutput("t-q-snooze", &quotaChunkReader{chunks: []int{quotaWireCheckpoint}}, write) // issue seq=1
		m.quotaResolved(map[string]interface{}{
			"session_id": "t-q-snooze", "seq": 1, "verdict": "snooze",
		}, write)

		errs := coll.ofTypes(models.AgentEventTerminalError)
		if len(errs) != 1 || !strings.Contains(fmt.Sprint(errs[0]["error"]), `unknown verdict "snooze"`) {
			t.Fatalf("an unknown verdict must be rejected with an error frame, got %v", errs)
		}
		if got := atomic.LoadInt32(kills); got != 0 {
			t.Fatalf("an unknown verdict must not kill, kills=%d", got)
		}
		// The pending challenge survived the garbage verdict: the kill point
		// still fires as unanswered.
		m.copyTerminalOutput("t-q-snooze", &quotaChunkReader{chunks: []int{quotaWireCheckpoint}}, write)
		errs = coll.ofTypes(models.AgentEventTerminalError)
		if len(errs) != 2 || !strings.HasPrefix(fmt.Sprint(errs[1]["error"]), "quota_unanswered") {
			t.Fatalf("the challenge must still be pending after an unknown verdict, errors=%v", errs)
		}
		if got := atomic.LoadInt32(kills); got != 1 {
			t.Fatalf("the surviving pending challenge must still kill, kills=%d", got)
		}
	})
}

// TestTerminalQuotaWire_RateTripKillsForFloodAndSkipsQuotaAction pins the
// rate-first ordering contract structurally (spec §5.3): when one record call
// returns both a rate trip and a quota action, the loop kills for the FLOOD —
// unredeemable — and never performs the action: no challenge frame may leave
// the agent for a session dying of flood.
func TestTerminalQuotaWire_RateTripKillsForFloodAndSkipsQuotaAction(t *testing.T) {
	m := newAgentTerminalManager()
	coll, write := newPayloadCollector()
	// Live rate window (max 1000 bytes per 5s) plus the quota machine: one
	// 2000-byte chunk floods the window AND crosses the checkpoint (hard cap
	// far away, so the dropped action is exactly an issue, mirroring B3's
	// Quota_RateFirst).
	session := newAttachTestSession("t-q-rate", newTerminalRingBuffer(1<<20))
	session.meter = newTerminalOutputMeter(agentTerminalOutputRateWindow, 1000,
		newQuotaPolicy(quotaWireCheckpoint, 1<<30, 0))
	m.mu.Lock()
	m.sessions["t-q-rate"] = session
	m.mu.Unlock()
	kills := killRecorder(session)

	m.copyTerminalOutput("t-q-rate", &quotaChunkReader{chunks: []int{2000}}, write)

	if got := len(coll.ofTypes(models.AgentEventTerminalQuotaChallengeRequired)); got != 0 {
		t.Fatalf("a rate-tripped record must not perform the quota action, challenge frames=%d", got)
	}
	errs := coll.ofTypes(models.AgentEventTerminalError)
	if len(errs) != 1 {
		t.Fatalf("error frames = %d, want exactly the flood kill, frames=%v", len(errs), frameTypes(coll))
	}
	reason := fmt.Sprint(errs[0]["error"])
	if !strings.HasPrefix(reason, "terminal output flood limit exceeded") {
		t.Fatalf("kill reason = %q, want the existing flood message", reason)
	}
	if strings.Contains(reason, "quota_") {
		t.Fatalf("the flood kill reason must not carry a quota token: %q", reason)
	}
	if got := atomic.LoadInt32(kills); got != 1 {
		t.Fatalf("the flood must kill exactly once, kills=%d", got)
	}
}

// TestTerminalQuotaWire_DuplicateKillSuppressed pins the duplicate-kill guard
// required by the B4 wiring notes on record: a denied verdict and the copy
// loop's own unanswered kill can race over one session — exactly one of them
// may frame and kill.
func TestTerminalQuotaWire_DuplicateKillSuppressed(t *testing.T) {
	t.Run("DeniedFirstSilencesCopyLoopKill", func(t *testing.T) {
		m := newAgentTerminalManager()
		coll, write := newPayloadCollector()
		session := newQuotaWireSession(m, "t-q-dup")
		kills := killRecorder(session)

		m.copyTerminalOutput("t-q-dup", &quotaChunkReader{chunks: []int{quotaWireCheckpoint}}, write) // issue seq=1
		// The denied verdict wins the race: one frame, one kill.
		m.quotaResolved(map[string]interface{}{
			"session_id": "t-q-dup", "seq": 1, "verdict": "denied",
		}, write)
		if got := atomic.LoadInt32(kills); got != 1 {
			t.Fatalf("the denied verdict must kill, kills=%d", got)
		}
		// The copy loop then reaches its own kill point (total 200, still
		// pending): kill_unanswered is evaluated but must stay silent — no
		// second frame, no second kill.
		m.copyTerminalOutput("t-q-dup", &quotaChunkReader{chunks: []int{quotaWireCheckpoint}}, write)

		errs := coll.ofTypes(models.AgentEventTerminalError)
		if len(errs) != 1 || !strings.HasPrefix(fmt.Sprint(errs[0]["error"]), "quota_denied") {
			t.Fatalf("exactly one kill frame (the denied one) expected, got %v", errs)
		}
		if got := atomic.LoadInt32(kills); got != 1 {
			t.Fatalf("the copy loop must not double-kill, kills=%d", got)
		}
	})

	t.Run("CopyLoopKillFirstSilencesLateDenied", func(t *testing.T) {
		// The reverse race: the copy loop's unanswered kill claims first, and
		// a denied verdict arrives late — the session is still in the map and
		// still pending, so resolveQuota does report kill_denied, but the
		// claim is gone: the denied frame must be suppressed.
		m := newAgentTerminalManager()
		coll, write := newPayloadCollector()
		session := newQuotaWireSession(m, "t-q-dup2")
		kills := killRecorder(session)

		m.copyTerminalOutput("t-q-dup2", &quotaChunkReader{chunks: []int{quotaWireCheckpoint}}, write) // issue seq=1
		// The copy loop reaches its kill point first: one frame, one kill.
		m.copyTerminalOutput("t-q-dup2", &quotaChunkReader{chunks: []int{quotaWireCheckpoint}}, write) // kill_unanswered, claimed
		if got := atomic.LoadInt32(kills); got != 1 {
			t.Fatalf("the unanswered kill must fire, kills=%d", got)
		}
		// The late denied verdict: still a live pending seq match, so the
		// meter reports kill_denied — and the handler must stay silent.
		m.quotaResolved(map[string]interface{}{
			"session_id": "t-q-dup2", "seq": 1, "verdict": "denied",
		}, write)

		errs := coll.ofTypes(models.AgentEventTerminalError)
		if len(errs) != 1 || !strings.HasPrefix(fmt.Sprint(errs[0]["error"]), "quota_unanswered") {
			t.Fatalf("exactly one kill frame (the unanswered one) expected, got %v", errs)
		}
		if got := atomic.LoadInt32(kills); got != 1 {
			t.Fatalf("the late denied verdict must not double-kill, kills=%d", got)
		}
	})

	t.Run("ClaimGuardsUnknownAndReapedSessions", func(t *testing.T) {
		m := newAgentTerminalManager()
		coll, write := newPayloadCollector()
		session := newQuotaWireSession(m, "t-q-reaped")
		kills := killRecorder(session)

		// An unknown session can never be claimed.
		if m.claimTerminalKill("never-registered") {
			t.Fatal("claim for an unknown session must fail")
		}
		// A session removed from the live map (reaped by waitTerminal after a
		// concurrent kill) can no longer be kill-framed either.
		m.copyTerminalOutput("t-q-reaped", &quotaChunkReader{chunks: []int{quotaWireCheckpoint}}, write) // issue seq=1
		m.mu.Lock()
		delete(m.sessions, "t-q-reaped")
		m.mu.Unlock()
		if m.claimTerminalKill("t-q-reaped") {
			t.Fatal("claim for a reaped session must fail")
		}
		// And the copy loop over a vanished session records nothing, kills
		// nothing, frames nothing.
		m.copyTerminalOutput("t-q-reaped", &quotaChunkReader{chunks: []int{quotaWireCheckpoint}}, write)
		if errs := coll.ofTypes(models.AgentEventTerminalError); len(errs) != 0 {
			t.Fatalf("a reaped session must not be kill-framed, got %v", errs)
		}
		if got := atomic.LoadInt32(kills); got != 0 {
			t.Fatalf("a reaped session must not be killed, kills=%d", got)
		}
	})
}
