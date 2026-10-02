package services

import (
	"testing"
	"time"
)

// Quota state machine tests (spec §5.2). Meters are constructed directly with
// policy values injected through newQuotaPolicy — the package env vars are
// never rewritten. The rate window is inert (huge rateMax) so every case
// exercises only the quota path; Quota_RateFirst builds its own meter with a
// live rate window.

const (
	quotaTestCheckpoint = 128 << 20 // 128 MiB — spec default scale
	quotaTestMax        = 512 << 20 // 512 MiB — spec default scale
)

var quotaTestBase = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

// newTestQuotaMeter builds a terminal-shaped meter (pure rate gate + quota
// state machine) with an inert rate window.
func newTestQuotaMeter(checkpointBytes, maxBytes int64, minInterval time.Duration) *outputMeter {
	return newTerminalOutputMeter(agentTerminalOutputRateWindow, 1<<30, newQuotaPolicy(checkpointBytes, maxBytes, minInterval))
}

func TestQuota_StateMachine(t *testing.T) {
	cases := []struct {
		name string
		run  func(t *testing.T)
	}{
		{"Quota_DisabledWhenMaxZero", func(t *testing.T) {
			// maxBytes == 0 disables the whole quota mechanism (spec §5.2 rule 1):
			// no challenge is ever issued and no kill ever fires, at any cumulative
			// volume — the meter stays a pure rate gate.
			m := newTestQuotaMeter(quotaTestCheckpoint, 0, 30*time.Minute)
			now := quotaTestBase
			for i := 0; i < 4; i++ {
				if m.add(1<<30, now) { // 1 GiB chunks, far past every threshold
					t.Fatalf("rate gate must stay inert in quota tests (chunk %d)", i)
				}
				if act := m.consumeQuota(1<<30, now); act.kind != "" {
					t.Fatalf("disabled quota returned action %q at chunk %d", act.kind, i)
				}
				now = now.Add(time.Minute)
			}
			if m.quota.pending || m.quota.seq != 0 {
				t.Fatalf("disabled quota mutated state: pending=%v seq=%d", m.quota.pending, m.quota.seq)
			}
		}},

		{"Quota_IssuesAtCheckpoint", func(t *testing.T) {
			m := newTestQuotaMeter(quotaTestCheckpoint, quotaTestMax, 30*time.Minute)
			if m.add(quotaTestCheckpoint, quotaTestBase) {
				t.Fatal("rate gate must stay inert in quota tests")
			}
			act := m.consumeQuota(quotaTestCheckpoint, quotaTestBase)
			if act.kind != quotaActionIssue {
				t.Fatalf("action kind = %q, want %q", act.kind, quotaActionIssue)
			}
			if act.seq != 1 {
				t.Fatalf("first challenge seq = %d, want 1", act.seq)
			}
			if act.usedBytes != quotaTestCheckpoint {
				t.Fatalf("usedBytes = %d, want %d", act.usedBytes, quotaTestCheckpoint)
			}
			// The kill point of an unanswered challenge is the NEXT checkpoint
			// (128 MiB issue → kill_at 256 MiB), not maxBytes.
			if act.killAtBytes != 2*quotaTestCheckpoint {
				t.Fatalf("killAtBytes = %d, want %d", act.killAtBytes, 2*quotaTestCheckpoint)
			}
			if !m.quota.pending {
				t.Fatal("issue must set pending")
			}
			if m.quota.nextChallengeBytes != 2*quotaTestCheckpoint {
				t.Fatalf("nextChallengeBytes = %d, want %d", m.quota.nextChallengeBytes, 2*quotaTestCheckpoint)
			}
			if !m.quota.lastChallengeAt.Equal(quotaTestBase) {
				t.Fatalf("lastChallengeAt = %v, want %v", m.quota.lastChallengeAt, quotaTestBase)
			}
		}},

		{"Quota_DualGateIntervalBlocks", func(t *testing.T) {
			// Bytes alone are not enough: a new challenge within minInterval of
			// the last issue is suppressed and output keeps streaming (spec §3
			// dual gate).
			m := newTestQuotaMeter(quotaTestCheckpoint, quotaTestMax, 30*time.Minute)
			m.add(quotaTestCheckpoint, quotaTestBase)
			if act := m.consumeQuota(quotaTestCheckpoint, quotaTestBase); act.kind != quotaActionIssue {
				t.Fatalf("first challenge must issue, got %q", act.kind)
			}
			if act := m.resolveQuota(1, true); act.kind != "" {
				t.Fatalf("grant must report no action, got %q", act.kind)
			}
			// Cross the next checkpoint only 10 minutes after the first issue.
			later := quotaTestBase.Add(10 * time.Minute)
			m.add(quotaTestCheckpoint, later)
			act := m.consumeQuota(quotaTestCheckpoint, later)
			if act.kind != "" {
				t.Fatalf("challenge within minInterval must be suppressed, got %q", act.kind)
			}
			if m.quota.pending {
				t.Fatal("interval-gated consume must not set pending")
			}
			if m.quota.seq != 1 || m.quota.nextChallengeBytes != 2*quotaTestCheckpoint {
				t.Fatalf("state advanced despite interval gate: seq=%d next=%d",
					m.quota.seq, m.quota.nextChallengeBytes)
			}
		}},

		{"Quota_UnansweredKillAtNextCheckpoint", func(t *testing.T) {
			// An unanswered challenge kills once output reaches the next
			// checkpoint (kill_at_bytes) — no matter how much time passed
			// (spec §5.2 rule 3 has no time gate).
			m := newTestQuotaMeter(quotaTestCheckpoint, quotaTestMax, 30*time.Minute)
			m.add(quotaTestCheckpoint, quotaTestBase)
			m.consumeQuota(quotaTestCheckpoint, quotaTestBase) // issue seq=1
			later := quotaTestBase.Add(time.Hour)
			m.add(quotaTestCheckpoint, later) // total = 256 MiB
			act := m.consumeQuota(quotaTestCheckpoint, later)
			if act.kind != quotaActionKillUnanswered {
				t.Fatalf("action kind = %q, want %q", act.kind, quotaActionKillUnanswered)
			}
			if act.killAtBytes != 2*quotaTestCheckpoint {
				t.Fatalf("killAtBytes = %d, want %d", act.killAtBytes, 2*quotaTestCheckpoint)
			}
			if act.seq != 1 {
				t.Fatalf("kill must carry the unanswered challenge seq, got %d", act.seq)
			}
		}},

		{"Quota_GrantRenews", func(t *testing.T) {
			// A grant only clears pending: nextChallengeBytes advanced at issue
			// time and stays put, so the renewed challenge fires at the
			// checkpoint after that (issue at 256 MiB → kill_at 384 MiB).
			m := newTestQuotaMeter(quotaTestCheckpoint, quotaTestMax, 30*time.Minute)
			m.add(quotaTestCheckpoint, quotaTestBase)
			if act := m.consumeQuota(quotaTestCheckpoint, quotaTestBase); act.kind != quotaActionIssue || act.seq != 1 {
				t.Fatalf("first issue missing: %+v", act)
			}
			if act := m.resolveQuota(1, true); act.kind != "" || m.quota.pending {
				t.Fatalf("grant must clear pending without action: %+v pending=%v", act, m.quota.pending)
			}
			// Cross 256 MiB one full interval later → second challenge.
			later := quotaTestBase.Add(31 * time.Minute)
			m.add(quotaTestCheckpoint, later)
			act := m.consumeQuota(quotaTestCheckpoint, later)
			if act.kind != quotaActionIssue || act.seq != 2 {
				t.Fatalf("renewed issue missing: %+v", act)
			}
			if act.killAtBytes != 3*quotaTestCheckpoint {
				t.Fatalf("killAtBytes = %d, want %d", act.killAtBytes, 3*quotaTestCheckpoint)
			}
			if !m.quota.pending || m.quota.nextChallengeBytes != 3*quotaTestCheckpoint {
				t.Fatalf("post-renew state: pending=%v next=%d",
					m.quota.pending, m.quota.nextChallengeBytes)
			}
		}},

		{"Quota_DeniedKills", func(t *testing.T) {
			m := newTestQuotaMeter(quotaTestCheckpoint, quotaTestMax, 30*time.Minute)
			m.add(quotaTestCheckpoint, quotaTestBase)
			m.consumeQuota(quotaTestCheckpoint, quotaTestBase) // issue seq=1
			act := m.resolveQuota(1, false)
			if act.kind != quotaActionKillDenied {
				t.Fatalf("action kind = %q, want %q", act.kind, quotaActionKillDenied)
			}
			if act.seq != 1 {
				t.Fatalf("denied kill must carry the challenge seq, got %d", act.seq)
			}
		}},

		{"Quota_HardCapOverridesPending", func(t *testing.T) {
			// maxBytes kills regardless of pending state and is evaluated
			// BEFORE the unanswered rule (spec §5.2 rule 2): with a pending
			// challenge past the next checkpoint AND at the hard cap, the hard
			// cap reason wins.
			m := newTestQuotaMeter(quotaTestCheckpoint, quotaTestMax, 30*time.Minute)
			m.add(quotaTestCheckpoint, quotaTestBase)
			m.consumeQuota(quotaTestCheckpoint, quotaTestBase) // issue, pending=true
			later := quotaTestBase.Add(time.Minute)
			m.add(quotaTestMax-quotaTestCheckpoint, later) // total = 512 MiB
			act := m.consumeQuota(quotaTestMax-quotaTestCheckpoint, later)
			if act.kind != quotaActionKillHardCap {
				t.Fatalf("action kind = %q, want %q", act.kind, quotaActionKillHardCap)
			}
			if act.killAtBytes != quotaTestMax {
				t.Fatalf("killAtBytes = %d, want %d (maxBytes)", act.killAtBytes, quotaTestMax)
			}
		}},

		{"Quota_StaleSeqIgnored", func(t *testing.T) {
			m := newTestQuotaMeter(quotaTestCheckpoint, quotaTestMax, 30*time.Minute)
			m.add(quotaTestCheckpoint, quotaTestBase)
			m.consumeQuota(quotaTestCheckpoint, quotaTestBase) // issue seq=1
			// Both stale grant and stale deny are no-ops; state is untouched.
			if act := m.resolveQuota(999, true); act.kind != "" {
				t.Fatalf("stale grant must be a no-op, got %q", act.kind)
			}
			if act := m.resolveQuota(999, false); act.kind != "" {
				t.Fatalf("stale deny must be a no-op, got %q", act.kind)
			}
			if !m.quota.pending || m.quota.seq != 1 {
				t.Fatalf("stale resolution mutated state: pending=%v seq=%d", m.quota.pending, m.quota.seq)
			}
			// The live seq still resolves afterwards.
			if act := m.resolveQuota(1, true); act.kind != "" || m.quota.pending {
				t.Fatalf("live grant must clear pending: %+v pending=%v", act, m.quota.pending)
			}
		}},

		{"Quota_RateFirst", func(t *testing.T) {
			// One chunk that both floods the rate window and crosses the
			// checkpoint: add reports the rate trip, and consumeQuota evaluates
			// the quota state independently — it never looks at the rate
			// verdict. Caller contract (copyTerminalOutput wiring, B4): check
			// add() FIRST; when it reports a rate trip, kill for the rate and
			// do NOT additionally process the quota action's kill — a rate kill
			// is unredeemable (spec §5.3). The meter layer cannot enforce call
			// order, so the enforcement itself is pinned by the B4 wiring test;
			// this test pins the independence the contract relies on.
			m := newTerminalOutputMeter(5*time.Second, 1000, newQuotaPolicy(100, 1<<30, 30*time.Minute))
			if !m.add(5000, quotaTestBase) {
				t.Fatal("chunk must trip the rate window")
			}
			act := m.consumeQuota(5000, quotaTestBase)
			if act.kind != quotaActionIssue {
				t.Fatalf("quota evaluation must be independent of the rate verdict, got %q", act.kind)
			}
			if act.seq != 1 || act.killAtBytes != 200 {
				t.Fatalf("issue fields after rate trip: %+v", act)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, tc.run)
	}
}
