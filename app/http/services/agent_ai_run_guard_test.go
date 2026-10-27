package services

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"aliang.one/nursorgate/app/http/models"
)

type recordingWriter struct {
	mu       sync.Mutex
	payloads []map[string]interface{}
}

func (w *recordingWriter) write(value interface{}) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if payload, ok := value.(map[string]interface{}); ok {
		w.payloads = append(w.payloads, payload)
	}
	return nil
}

func (w *recordingWriter) snapshot() []map[string]interface{} {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]map[string]interface{}(nil), w.payloads...)
}

func newGuardTestManager(t *testing.T) (*agentAIManager, *agentAISession, *agentAIRunGuard, *recordingWriter) {
	t.Helper()
	m := newAgentAIManager()
	activity := newAgentAIActivity()
	cancelCount := int32(0)
	_, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	session := &agentAISession{
		id:       "sess-1",
		runSeq:   1,
		cancel:   func() { atomic.AddInt32(&cancelCount, 1); cancel() },
		activity: activity,
	}
	m.sessions[session.id] = session

	run := agentAIRun{
		sessionID: session.id,
		runID:     "run-1",
		messageID: "msg-1",
		runSeq:    session.runSeq,
		activity:  activity,
		cancel:    session.cancel,
	}
	rec := &recordingWriter{}
	guard := m.attachRunGuard(run, rec.write)
	return m, session, guard, rec
}

func TestRunGuardKillLogsAndForceFinalizesAfterGrace(t *testing.T) {
	oldGrace := agentAIKillGrace
	agentAIKillGrace = 40 * time.Millisecond
	t.Cleanup(func() { agentAIKillGrace = oldGrace })

	m, session, guard, rec := newGuardTestManager(t)
	guard.kill("stalled")

	if got := len(rec.snapshot()); got != 0 {
		t.Fatalf("terminal emitted before grace elapsed: %d", got)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		payloads := rec.snapshot()
		if len(payloads) == 1 {
			if payloads[0]["type"] != models.AgentEventAIError {
				t.Fatalf("terminal type = %v, want ai.error", payloads[0]["type"])
			}
			if payloads[0]["error_code"] != "turn_stalled" {
				t.Fatalf("error_code = %v, want turn_stalled", payloads[0]["error_code"])
			}
			if !strings.Contains(remoteString(payloads[0], "error"), "判死") {
				t.Fatalf("error message = %q, want 判死", remoteString(payloads[0], "error"))
			}
			m.mu.Lock()
			idle := m.sessions[session.id].cancel == nil
			m.mu.Unlock()
			if !idle {
				t.Fatal("session still has cancel set after force finalize")
			}
			// 「恰好一个终态」是不变量：再 settle 一段时间确认没有第二帧。
			time.Sleep(80 * time.Millisecond)
			if got := len(rec.snapshot()); got != 1 {
				t.Fatalf("terminal emitted %d times, want exactly 1", got)
			}
			// 强拆必须真的把本 runSeq 记账，否则僵尸非帧副作用守卫形同虚设。
			m.mu.Lock()
			finalized := m.sessions[session.id].finalizedRunSeq
			m.mu.Unlock()
			if finalized != guard.runSeq {
				t.Fatalf("finalizedRunSeq = %d, want %d", finalized, guard.runSeq)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no terminal after grace: %d payloads", len(rec.snapshot()))
}

func TestRunGuardNoForceFinalizeWhenRunReturns(t *testing.T) {
	oldGrace := agentAIKillGrace
	agentAIKillGrace = 60 * time.Millisecond
	t.Cleanup(func() { agentAIKillGrace = oldGrace })

	_, _, guard, rec := newGuardTestManager(t)
	guard.kill("idle_timeout")
	guard.markDone()

	time.Sleep(150 * time.Millisecond)
	if got := len(rec.snapshot()); got != 0 {
		t.Fatalf("terminal emitted after run returned normally: %d", got)
	}
}

func TestRunGuardKillIsIdempotent(t *testing.T) {
	oldGrace := agentAIKillGrace
	agentAIKillGrace = 30 * time.Millisecond
	t.Cleanup(func() { agentAIKillGrace = oldGrace })

	_, _, guard, rec := newGuardTestManager(t)
	guard.markDone() // 抑制强拆，只看 cancel 次数
	oldCancel := guard.cancel
	calls := int32(0)
	guard.cancel = func() { atomic.AddInt32(&calls, 1); oldCancel() }

	guard.kill("a")
	guard.kill("b")
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("cancel called %d times, want 1", got)
	}
	if got := len(rec.snapshot()); got != 0 {
		t.Fatalf("terminal = %d, want 0 (done already closed)", got)
	}
	// 睡过宽限期再断言：证明 done 已关闭确实抑制了强拆，而不只是「还没到点」。
	time.Sleep(80 * time.Millisecond)
	if got := len(rec.snapshot()); got != 0 {
		t.Fatalf("terminal emitted %d times after grace despite markDone, want 0", got)
	}
}

func TestRunSideEffectsFencedAfterForceFinalize(t *testing.T) {
	m := newAgentAIManager()
	session := &agentAISession{id: "sess-2", runSeq: 2, cancel: func() {}}
	m.sessions[session.id] = session

	if !m.agentAIRunSideEffectsAllowed("sess-2", 2) {
		t.Fatal("live run must be allowed")
	}
	// 强拆打标：只对本 runSeq 生效。
	m.markRunFinalized("sess-2", 2)
	if m.agentAIRunSideEffectsAllowed("sess-2", 2) {
		t.Fatal("force-finalized run must be fenced")
	}
	// Retry 起新回合（runSeq 推进）后，旧标记不得牵连新回合。
	session.runSeq = 3
	if !m.agentAIRunSideEffectsAllowed("sess-2", 3) {
		t.Fatal("a fresh runSeq must not inherit the previous run's fence")
	}
	session.runSeq = 2
	if m.agentAIRunSideEffectsAllowed("sess-2", 2) {
		t.Fatal("stale runSeq must stay fenced")
	}
	// 无 cancel 的夹具会话（单测/TUI 形态）默认必须放行——判据是显式标记。
	legacy := &agentAISession{id: "sess-legacy", runSeq: 1}
	m.sessions[legacy.id] = legacy
	if !m.agentAIRunSideEffectsAllowed("sess-legacy", 1) {
		t.Fatal("session without cancel must not be treated as a zombie")
	}
	if !m.agentAIRunSideEffectsAllowed("missing-session", 9) {
		t.Fatal("unregistered session (tests/TUI) must keep legacy behaviour")
	}
}

func TestClearResetsFinalizedRunSeq(t *testing.T) {
	m := newAgentAIManager()
	session := &agentAISession{id: "sess-clear", runSeq: 1, cancel: func() {}}
	m.sessions[session.id] = session
	m.markRunFinalized("sess-clear", 1)
	if m.agentAIRunSideEffectsAllowed("sess-clear", 1) {
		t.Fatal("precondition: force-finalized run must be fenced")
	}

	m.applyLocalSlashCommand(session, "clear", "", "agentcore")

	if session.runSeq != 0 || session.finalizedRunSeq != 0 {
		t.Fatalf("after /clear runSeq=%d finalizedRunSeq=%d, want 0/0",
			session.runSeq, session.finalizedRunSeq)
	}
	// /clear 之后复用同一 runSeq 是合法回合，绝不能被旧标记挡住。
	// 模拟下一回合起跑：runUserMessage 在起跑时执行 session.runSeq++，
	// 所以 /clear 归零后的第一个回合拿到的正是 runSeq=1。
	session.runSeq++
	if !m.agentAIRunSideEffectsAllowed("sess-clear", 1) {
		t.Fatal("a run after /clear must not inherit the previous fence")
	}
}
