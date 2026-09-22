package services

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// externalInterruptFixture writes a Claude pid record into a fresh fake home's
// ~/.claude/sessions/ directory, points HOME/USERPROFILE at it (agentHome()
// reads them), and returns the home path. The record shape mirrors real
// ~/.claude/sessions/<pid>.json files (UNIX-millisecond updatedAt — the
// 437bc99 lesson).
func externalInterruptFixture(t *testing.T, sid string, pidRecord string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	dir := filepath.Join(home, ".claude", "sessions")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	if pidRecord != "" {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "12345.json"), []byte(pidRecord), 0o600))
	}
	return home
}

// stubExternalInterrupt swaps the injectable signal sender for a capturing
// stub, forces the pid-identity check permissive, and resets the debounce
// map — all restored on cleanup. Returns a pointer to the last pid the stub
// saw (0 = never called); the stub responds with respondErr.
func stubExternalInterrupt(t *testing.T, respondErr error) *atomic.Int64 {
	t.Helper()
	var lastPid atomic.Int64
	previousSignal := externalInterruptFunc
	previousMatch := externalInterruptTargetMatches
	externalInterruptFunc = func(pid int) error {
		lastPid.Store(int64(pid))
		return respondErr
	}
	externalInterruptTargetMatches = func(pid int) bool { return true }
	externalInterruptMu.Lock()
	externalInterruptRecent = map[string]time.Time{}
	externalInterruptMu.Unlock()
	t.Cleanup(func() {
		externalInterruptFunc = previousSignal
		externalInterruptTargetMatches = previousMatch
	})
	return &lastPid
}

func lastStubbedPid(ptr *atomic.Int64) int {
	if ptr == nil {
		return 0
	}
	return int(ptr.Load())
}

// statusReplies collects the ai.status payloads emitted through the capture
// writer (mu must be non-nil — findAIEvents locks it).
func statusReplies(t *testing.T, mu *sync.Mutex, events *[]map[string]interface{}) []map[string]interface{} {
	t.Helper()
	return findAIEvents(mu, events, "ai.status")
}

// TestInterruptExternalClaudeTurnSignalsLivePid is the C2 anchor: ai.stop for
// a session this agent never spawned, carrying source_session_id of a live TUI
// process, delivers SIGINT (one Esc press — never SIGKILL) to that pid.
func TestInterruptExternalClaudeTurnSignalsLivePid(t *testing.T) {
	const sid = "interrupt-live-pid"
	home := externalInterruptFixture(t, sid, livePidRecord(t, sid, `"status":"busy","entrypoint":"cli"`))
	lastPid := stubExternalInterrupt(t, nil)

	pid, ok := interruptExternalClaudeTurn(home, sid)

	require.True(t, ok)
	assert.Equal(t, os.Getpid(), lastStubbedPid(lastPid))
	assert.Equal(t, os.Getpid(), pid)
}

// TestInterruptExternalClaudeTurnMissingSourceSessionID: an empty native id
// must never signal anything (belt for the stop() gate).
func TestInterruptExternalClaudeTurnMissingSourceSessionID(t *testing.T) {
	home := externalInterruptFixture(t, "interrupt-empty-sid", livePidRecord(t, "interrupt-empty-sid", `"status":"busy"`))
	lastPid := stubExternalInterrupt(t, nil)

	pid, ok := interruptExternalClaudeTurn(home, "")

	assert.False(t, ok)
	assert.Zero(t, lastStubbedPid(lastPid))
	assert.Zero(t, pid)
}

// TestInterruptExternalClaudeTurnUnknownSession: no pid record for the native
// id → no signal, not ok.
func TestInterruptExternalClaudeTurnUnknownSession(t *testing.T) {
	home := externalInterruptFixture(t, "other-session", livePidRecord(t, "other-session", `"status":"busy"`))
	lastPid := stubExternalInterrupt(t, nil)

	pid, ok := interruptExternalClaudeTurn(home, "never-seen-session")

	assert.False(t, ok)
	assert.Zero(t, lastStubbedPid(lastPid))
	assert.Zero(t, pid)
}

// TestInterruptExternalClaudeTurnDeadPid: a zombie pid record must not be
// signaled (isPidAlive gates; signaling a reused pid would hit an unrelated
// process).
func TestInterruptExternalClaudeTurnDeadPid(t *testing.T) {
	const sid = "interrupt-dead-pid"
	home := externalInterruptFixture(t, sid, deadPidRecord(t, sid))
	lastPid := stubExternalInterrupt(t, nil)

	pid, ok := interruptExternalClaudeTurn(home, sid)

	assert.False(t, ok)
	assert.Zero(t, lastStubbedPid(lastPid))
	assert.Zero(t, pid)
}

// deadPidRecord renders a busy pid record pointing at a pid no test process
// owns — dead by construction on every platform the tests run on (above every
// realistic pid_max, matching the zombie fixtures used across the rename
// tests).
func deadPidRecord(t *testing.T, sid string) string {
	t.Helper()
	return `{"pid":99999999,"sessionId":"` + sid + `","status":"busy","updatedAt":` + msTimestamp(t, 0) + `}`
}

// TestInterruptExternalClaudeTurnSignalError: a failed signal delivery (e.g.
// EPERM across a user boundary) is a failure, not a success.
func TestInterruptExternalClaudeTurnSignalError(t *testing.T) {
	const sid = "interrupt-eperm"
	home := externalInterruptFixture(t, sid, livePidRecord(t, sid, `"status":"busy"`))
	lastPid := stubExternalInterrupt(t, errors.New("operation not permitted"))

	pid, ok := interruptExternalClaudeTurn(home, sid)

	assert.False(t, ok)
	assert.Equal(t, os.Getpid(), lastStubbedPid(lastPid))
	assert.Zero(t, pid)
}

// TestInterruptExternalClaudeTurnDebouncesRepeat: a second ai.stop for the
// same native session inside the debounce window is idempotent success
// WITHOUT re-signaling — two SIGINTs in quick succession are Claude Code's
// double-Esc, which QUITS the whole TUI (contract: interrupt the turn, keep
// the TUI open).
func TestInterruptExternalClaudeTurnDebouncesRepeat(t *testing.T) {
	const sid = "interrupt-debounce"
	home := externalInterruptFixture(t, sid, livePidRecord(t, sid, `"status":"busy","entrypoint":"cli"`))
	lastPid := stubExternalInterrupt(t, nil)

	pid1, ok1 := interruptExternalClaudeTurn(home, sid)
	pid2, ok2 := interruptExternalClaudeTurn(home, sid)

	require.True(t, ok1)
	require.True(t, ok2)
	assert.Equal(t, os.Getpid(), lastStubbedPid(lastPid))
	assert.Equal(t, os.Getpid(), pid1)
	assert.Equal(t, os.Getpid(), pid2)
}

// TestInterruptExternalClaudeTurnTargetMismatch: the pid-reuse guard — when
// the identity probe says the recycled pid is NOT a claude process, no signal
// is delivered (an unrelated process must never be interrupted).
func TestInterruptExternalClaudeTurnTargetMismatch(t *testing.T) {
	const sid = "interrupt-mismatch"
	home := externalInterruptFixture(t, sid, livePidRecord(t, sid, `"status":"busy"`))
	lastPid := stubExternalInterrupt(t, nil)
	previousMatch := externalInterruptTargetMatches
	externalInterruptTargetMatches = func(pid int) bool { return false }
	t.Cleanup(func() { externalInterruptTargetMatches = previousMatch })

	pid, ok := interruptExternalClaudeTurn(home, sid)

	assert.False(t, ok)
	assert.Zero(t, lastStubbedPid(lastPid))
	assert.Zero(t, pid)
}

// TestInterruptExternalClaudeTurnNoHome: empty home (agent cannot resolve the
// desktop user's home) is a clean no-op.
func TestInterruptExternalClaudeTurnNoHome(t *testing.T) {
	lastPid := stubExternalInterrupt(t, nil)

	pid, ok := interruptExternalClaudeTurn("", "some-session")

	assert.False(t, ok)
	assert.Zero(t, lastStubbedPid(lastPid))
	assert.Zero(t, pid)
}

// TestExternalTUIBusyPrecheck is the C3 truth table: record exists && pid
// alive && identity matches && status busy. Idle records must NOT block a
// resume spawn.
func TestExternalTUIBusyPrecheck(t *testing.T) {
	// The fixture pid is the test process itself — its comm is the test
	// binary, not "claude", so the identity gate must be permissive here.
	previousMatch := externalInterruptTargetMatches
	externalInterruptTargetMatches = func(pid int) bool { return true }
	t.Cleanup(func() { externalInterruptTargetMatches = previousMatch })

	t.Run("busy+alive→true", func(t *testing.T) {
		const sid = "precheck-busy"
		home := externalInterruptFixture(t, sid, livePidRecord(t, sid, `"status":"busy","entrypoint":"cli"`))
		assert.True(t, externalTUIBusy(home, sid))
	})
	t.Run("idle+alive→false", func(t *testing.T) {
		const sid = "precheck-idle"
		home := externalInterruptFixture(t, sid, livePidRecord(t, sid, `"status":"idle","entrypoint":"cli"`))
		assert.False(t, externalTUIBusy(home, sid))
	})
	t.Run("busy+dead→false", func(t *testing.T) {
		const sid = "precheck-dead"
		home := externalInterruptFixture(t, sid, deadPidRecord(t, sid))
		assert.False(t, externalTUIBusy(home, sid))
	})
	t.Run("no record→false", func(t *testing.T) {
		home := externalInterruptFixture(t, "someone-else", livePidRecord(t, "someone-else", `"status":"busy"`))
		assert.False(t, externalTUIBusy(home, "missing-session"))
	})
	t.Run("empty id→false", func(t *testing.T) {
		home := externalInterruptFixture(t, "precheck-empty", livePidRecord(t, "precheck-empty", `"status":"busy"`))
		assert.False(t, externalTUIBusy(home, ""))
	})
}

// TestGuardExternalTUIClaudeSpawn: the spawn precheck only fires for the
// claude tool path with a known resume id over a busy live TUI.
func TestGuardExternalTUIClaudeSpawn(t *testing.T) {
	const sid = "guard-busy"
	home := externalInterruptFixture(t, sid, livePidRecord(t, sid, `"status":"busy","entrypoint":"cli"`))

	t.Run("claude+busy→refused", func(t *testing.T) {
		err := guardExternalTUIClaudeSpawn(home, "claude", sid)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "TUI session is running an active turn in its terminal")
		var coded interface{ ErrorCode() string }
		require.True(t, errors.As(err, &coded))
		assert.Equal(t, "tui_busy", coded.ErrorCode())
	})
	t.Run("claudecode+busy→refused", func(t *testing.T) {
		assert.Error(t, guardExternalTUIClaudeSpawn(home, "claudecode", sid))
	})
	t.Run("claude+idle→allowed", func(t *testing.T) {
		const idleSid = "guard-idle"
		idleHome := externalInterruptFixture(t, idleSid, livePidRecord(t, idleSid, `"status":"idle","entrypoint":"cli"`))
		assert.NoError(t, guardExternalTUIClaudeSpawn(idleHome, "claude", idleSid))
	})
	t.Run("claude+no record→allowed", func(t *testing.T) {
		assert.NoError(t, guardExternalTUIClaudeSpawn(home, "claude", "unknown-native"))
	})
	t.Run("codex+busy→allowed", func(t *testing.T) {
		assert.NoError(t, guardExternalTUIClaudeSpawn(home, "codex", sid))
	})
	t.Run("auto+busy→refused", func(t *testing.T) {
		// "auto" is the default provider on ai.message and resolves to claude
		// for imported sessions — the precheck must gate it too, or the race
		// window stays open for provider-less messages.
		assert.Error(t, guardExternalTUIClaudeSpawn(home, "auto", sid))
	})
	t.Run("empty resume→allowed", func(t *testing.T) {
		assert.NoError(t, guardExternalTUIClaudeSpawn(home, "claude", ""))
	})
	t.Run("empty home→allowed", func(t *testing.T) {
		assert.NoError(t, guardExternalTUIClaudeSpawn("", "claude", sid))
	})
	t.Run("identity mismatch→allowed", func(t *testing.T) {
		// pid 复用防护的另一半:记录指向的 pid 已被回收给无关进程时,不得把
		// 它当「TUI busy」拦下手机回合——否则一条迟清理的死记录能永久拒绝
		// 派发(比罕见的首轮双写更伤)。
		previousMatch := externalInterruptTargetMatches
		externalInterruptTargetMatches = func(pid int) bool { return false }
		t.Cleanup(func() { externalInterruptTargetMatches = previousMatch })
		assert.NoError(t, guardExternalTUIClaudeSpawn(home, "claude", sid))
	})
}

// TestStopUnregisteredSessionInterruptsExternalTUI is the stop()-level C2
// anchor: ai.stop for a session the agent never spawned, carrying
// source_session_id, signals the live TUI and reports "stopping".
func TestStopUnregisteredSessionInterruptsExternalTUI(t *testing.T) {
	const sid = "stop-external-live"
	_ = externalInterruptFixture(t, sid, livePidRecord(t, sid, `"status":"busy","entrypoint":"cli"`))
	lastPid := stubExternalInterrupt(t, nil)

	m := newAgentAIManager()
	mu, events, writeJSON := captureAIWriter(t)

	m.stop(map[string]interface{}{
		"session_id":        "cloud-session-not-spawned-here",
		"source_session_id": sid,
	}, writeJSON)

	require.Equal(t, os.Getpid(), lastStubbedPid(lastPid))
	statuses := statusReplies(t, mu, events)
	require.Len(t, statuses, 1)
	assert.Equal(t, "stopping", statuses[0]["status"])
	assert.Equal(t, "cloud-session-not-spawned-here", statuses[0]["session_id"])
}

// TestStopUnregisteredSessionInterruptFailsReportsStopped: unknown / dead pid
// all degrade to the legacy "stopped" reply.
func TestStopUnregisteredSessionInterruptFailsReportsStopped(t *testing.T) {
	t.Run("dead pid", func(t *testing.T) {
		const sid = "stop-external-dead"
		_ = externalInterruptFixture(t, sid, deadPidRecord(t, sid))
		lastPid := stubExternalInterrupt(t, nil)

		m := newAgentAIManager()
		mu, events, writeJSON := captureAIWriter(t)

		m.stop(map[string]interface{}{
			"session_id":        "cloud-session",
			"source_session_id": sid,
		}, writeJSON)

		assert.Zero(t, lastStubbedPid(lastPid))
		statuses := statusReplies(t, mu, events)
		require.Len(t, statuses, 1)
		assert.Equal(t, "stopped", statuses[0]["status"])
	})
	t.Run("no source_session_id keeps legacy reply", func(t *testing.T) {
		const sid = "stop-external-nosource"
		_ = externalInterruptFixture(t, sid, livePidRecord(t, sid, `"status":"busy"`))
		lastPid := stubExternalInterrupt(t, nil)

		m := newAgentAIManager()
		mu, events, writeJSON := captureAIWriter(t)

		m.stop(map[string]interface{}{"session_id": "cloud-session"}, writeJSON)

		assert.Zero(t, lastStubbedPid(lastPid))
		statuses := statusReplies(t, mu, events)
		require.Len(t, statuses, 1)
		assert.Equal(t, "stopped", statuses[0]["status"])
	})
}

// TestStopRegisteredIdleSessionAttemptsExternalInterrupt: a registered but
// idle session has no agent-spawned run this turn, so the turn may belong to
// the external TUI — the external interrupt is attempted, and the legacy
// "stopping" reply is unchanged either way.
func TestStopRegisteredIdleSessionAttemptsExternalInterrupt(t *testing.T) {
	const sid = "stop-registered-idle"
	_ = externalInterruptFixture(t, sid, livePidRecord(t, sid, `"status":"busy","entrypoint":"cli"`))
	lastPid := stubExternalInterrupt(t, nil)

	m := newAgentAIManager()
	m.mu.Lock()
	m.sessions["reg-session"] = &agentAISession{id: "reg-session", resumeSessionID: sid}
	m.mu.Unlock()
	mu, events, writeJSON := captureAIWriter(t)

	m.stop(map[string]interface{}{
		"session_id":        "reg-session",
		"source_session_id": sid,
	}, writeJSON)

	require.Equal(t, os.Getpid(), lastStubbedPid(lastPid))
	statuses := statusReplies(t, mu, events)
	require.Len(t, statuses, 1)
	assert.Equal(t, "stopping", statuses[0]["status"])
}

// TestStopRegisteredRunningSessionKeepsLocalCancel: an agent-spawned active
// run is cancelled locally as before; no external signal is sent.
func TestStopRegisteredRunningSessionKeepsLocalCancel(t *testing.T) {
	const sid = "stop-registered-running"
	_ = externalInterruptFixture(t, sid, livePidRecord(t, sid, `"status":"busy"`))
	lastPid := stubExternalInterrupt(t, nil)

	m := newAgentAIManager()
	cancelled := false
	m.mu.Lock()
	m.sessions["live-session"] = &agentAISession{
		id:              "live-session",
		resumeSessionID: sid,
		cancel:          func() { cancelled = true },
		activeRunID:     "run-1",
	}
	m.mu.Unlock()
	mu, events, writeJSON := captureAIWriter(t)

	m.stop(map[string]interface{}{
		"session_id":        "live-session",
		"source_session_id": sid,
	}, writeJSON)

	assert.True(t, cancelled)
	assert.Zero(t, lastStubbedPid(lastPid))
	statuses := statusReplies(t, mu, events)
	require.Len(t, statuses, 1)
	assert.Equal(t, "stopping", statuses[0]["status"])
}

// TestAgentAIErrorPayloadCarriesTuiBusyCode: the precheck refusal travels on
// the ai.error payload with its machine-readable code.
func TestAgentAIErrorPayloadCarriesTuiBusyCode(t *testing.T) {
	payload := agentAIErrorPayload("sess-1", "msg-1", newAgentExternalTUIBusyError())
	assert.Equal(t, "tui_busy", payload["error_code"])

	plain := agentAIErrorPayload("sess-1", "msg-1", errors.New("ordinary failure"))
	_, hasCode := plain["error_code"]
	assert.False(t, hasCode)
}
