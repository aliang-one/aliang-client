package services

import (
	"context"
	"errors"
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
	// 强拆打标：只对本 runSeq 生效。生产端唯一的写入点是 finalizeStalledGuard
	// 身份校验临界区内的内联赋值，这里直接构造同一个标记。
	m.mu.Lock()
	session.finalizedRunSeq = 2
	m.mu.Unlock()
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
	// 直接构造「已被强拆收尾」的标记（生产端写入点在 finalizeStalledGuard
	// 身份校验的同一临界区）。
	m.mu.Lock()
	session.finalizedRunSeq = 1
	m.mu.Unlock()
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

func TestStaleScannerKillsNonWaitingRunEvenWithHeartbeat(t *testing.T) {
	m, session, guard, rec := newGuardTestManager(t)
	oldGrace := agentAIKillGrace
	agentAIKillGrace = 20 * time.Millisecond
	t.Cleanup(func() { agentAIKillGrace = oldGrace })

	// 常驻扫描器已在 TestMain 关闭（agentAIStaleScannerEnabled=false），这里写
	// 包级 var 不构成竞争；显式设置是为了不受 ambient ALIANG_AI_STALE_RUN 影响。
	oldThreshold := agentAIStaleRunThreshold
	agentAIStaleRunThreshold = 5 * time.Minute
	t.Cleanup(func() { agentAIStaleRunThreshold = oldThreshold })

	// lastActivityAt 回拨 10min，相对 5min 阈值有 2 倍余量。
	// 只让「进度心跳」在流：心跳不经 activity，lastActivityAt 仍是旧的。
	guard.activity.lastActivityAt.Store(time.Now().Add(-10 * time.Minute).UnixNano())
	guard.activity.lastProgressAt.Store(time.Now().Add(-10 * time.Minute).UnixNano())

	m.scanStaleRuns()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(rec.snapshot()) == 1 {
			if got := rec.snapshot()[0]["error_code"]; got != "turn_stalled" {
				t.Fatalf("error_code = %v, want turn_stalled", got)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("scanner did not converge the stale run (session=%s)", session.id)
}

func TestStaleScannerSparesHealthyToolWait(t *testing.T) {
	// 常驻扫描器已在 TestMain 关闭（agentAIStaleScannerEnabled=false），这里写
	// 包级 var 不构成竞争；显式设置是为了不受 ambient ALIANG_AI_STALE_RUN 影响。
	oldThreshold := agentAIStaleRunThreshold
	agentAIStaleRunThreshold = 5 * time.Minute
	t.Cleanup(func() { agentAIStaleRunThreshold = oldThreshold })

	// lastActivityAt 回拨 30min，相对 5min 阈值有 6 倍余量。
	m, _, guard, rec := newGuardTestManager(t)
	guard.activity.beginToolUseWait()
	guard.activity.lastActivityAt.Store(time.Now().Add(-30 * time.Minute).UnixNano())

	m.scanStaleRuns()

	// kill() 同步写 killReason，不受宽限期影响：一旦被误杀，这里立刻能看到，
	// 不必等到 30s 宽限后才出现的终态帧（那是下面的第二重断言）。
	if reason := guard.activity.killReasonOr(""); reason != "" {
		t.Fatalf("healthy tool wait was killed, reason=%q", reason)
	}

	time.Sleep(100 * time.Millisecond)
	if got := len(rec.snapshot()); got != 0 {
		t.Fatalf("healthy tool wait was killed (payloads=%d)", got)
	}
}

func TestStaleDeathReasonApprovalOverflow(t *testing.T) {
	a := newAgentAIActivity()
	a.setAwaitingApproval(true)
	a.approvalWaitStartedAt.Store(time.Now().Add(-25 * time.Hour).UnixNano())

	reason, dead := a.staleDeathReason(5*time.Minute, 24*time.Hour, time.Hour, 6*time.Hour)
	if !dead || reason != "approval_timeout" {
		t.Fatalf("staleDeathReason = (%q, %t), want (approval_timeout, true)", reason, dead)
	}

	// 3h 审批（> 2h 硬顶）既不该被 stale 判死，也不该被审批天花板判死。
	b := newAgentAIActivity()
	b.setAwaitingApproval(true)
	b.approvalWaitStartedAt.Store(time.Now().Add(-3 * time.Hour).UnixNano())
	// setAwaitingApproval 会 bump()，idleFor 因此归零；必须把 lastActivityAt
	// 回拨，否则即使 staleDeathReason 里的 awaiting 分支被整段删掉，本用例也会
	// 因为「刚 bump 过、还没到 stale 阈值」而假绿。
	b.lastActivityAt.Store(time.Now().Add(-time.Hour).UnixNano())
	if reason, dead := b.staleDeathReason(5*time.Minute, 24*time.Hour, time.Hour, 6*time.Hour); dead {
		t.Fatalf("3h approval was judged dead (reason=%q)", reason)
	}
}

// TestSetAwaitingApprovalCountsOverlappingApprovals 覆盖同一回合多个审批重叠的
// 场景：第一个审批结束时后一个仍在等，审批钟与 awaiting 都不得被清掉。
func TestSetAwaitingApprovalCountsOverlappingApprovals(t *testing.T) {
	a := newAgentAIActivity()
	a.setAwaitingApproval(true)
	a.setAwaitingApproval(true)
	time.Sleep(5 * time.Millisecond) // coarse wall-clock guard: the clock must tick
	if !a.awaiting() {
		t.Fatal("two overlapping approvals should keep the run awaiting")
	}

	// 第一个审批结束：B 还在等，审批钟不得清零、awaiting 不得变 false。
	a.setAwaitingApproval(false)
	if !a.awaiting() {
		t.Fatal("overlapping approval still pending: awaiting must stay true")
	}
	if got := a.approvalWaitFor(); got <= 0 {
		t.Fatalf("approval clock reset by the first of two approvals: approvalWaitFor = %v, want > 0", got)
	}

	// 最后一个审批结束才真正离开等待态。
	a.setAwaitingApproval(false)
	if a.awaiting() {
		t.Fatal("last approval resolved: awaiting must be false")
	}
	if got := a.approvalWaitFor(); got != 0 {
		t.Fatalf("approval clock = %v after last approval resolved, want 0", got)
	}

	// 多余的 false 不得把计数减成负数（否则后续一次 true 仍读作「无人在等」）。
	a.setAwaitingApproval(false)
	a.setAwaitingApproval(true)
	if !a.awaiting() {
		t.Fatal("count underflow: a fresh approval after a spurious false must be awaiting")
	}
}

// TestSetAwaitingApprovalDrivesApprovalClock 覆盖审批钟的写入/清零，以及审批
// 结束时对仍有 pending 工具的工具钟重新臂起。
func TestSetAwaitingApprovalDrivesApprovalClock(t *testing.T) {
	a := newAgentAIActivity()
	if got := a.approvalWaitFor(); got != 0 {
		t.Fatalf("fresh activity approvalWaitFor = %v, want 0", got)
	}

	a.setAwaitingApproval(true)
	time.Sleep(5 * time.Millisecond) // coarse wall-clock guard: the clock must tick
	if got := a.approvalWaitFor(); got <= 0 {
		t.Fatalf("after setAwaitingApproval(true) approvalWaitFor = %v, want > 0", got)
	}
	if got := a.toolWaitFor(); got != 0 {
		t.Fatalf("entering approval must pause the tool-wait clock, got %v", got)
	}

	a.setAwaitingApproval(false)
	if got := a.approvalWaitFor(); got != 0 {
		t.Fatalf("after setAwaitingApproval(false) approvalWaitFor = %v, want 0", got)
	}

	// 审批结束时工具仍在飞：工具钟必须重新臂起，而不是保持 0（否则挂死的工具
	// 永远等不到 tool_wait_timeout）。
	a.beginToolUseWait()
	time.Sleep(5 * time.Millisecond) // coarse wall-clock guard: the clock must tick
	if got := a.toolWaitFor(); got <= 0 {
		t.Fatalf("after beginToolUseWait toolWaitFor = %v, want > 0", got)
	}
	a.setAwaitingApproval(true)
	if got := a.toolWaitFor(); got != 0 {
		t.Fatalf("approval must pause the tool-wait clock, got %v", got)
	}
	time.Sleep(5 * time.Millisecond) // coarse wall-clock guard: the clock must tick
	if got := a.approvalWaitFor(); got <= 0 {
		t.Fatalf("approval clock = %v, want > 0", got)
	}
	a.setAwaitingApproval(false)
	time.Sleep(5 * time.Millisecond) // coarse wall-clock guard: re-armed clock must tick
	if got := a.toolWaitFor(); got <= 0 {
		t.Fatalf("approval resolve with a pending tool must re-arm the tool clock, got %v", got)
	}

	a.endToolUseWait()
	if got := a.toolWaitFor(); got != 0 {
		t.Fatalf("after endToolUseWait toolWaitFor = %v, want 0", got)
	}
}

// TestForceFinalizeDoesNotTouchReusedRunSeq 覆盖「僵尸 → 用户 /clear 自救 →
// 新回合复用 runSeq=1」这一最可能发生的剧本：旧回合迟到的强拆必须按 guard
// 身份识别出自己已不是当前回合，绝不能再复位会话。
//
// 旧实现（只比 runSeq）下 finalize 会命中新回合：finalizedRunSeq 被写成 1、
// session.guard 与 session.cancel 被清空，本用例必红。
func TestForceFinalizeDoesNotTouchReusedRunSeq(t *testing.T) {
	m, session, guard1, rec1 := newGuardTestManager(t)

	// 模拟 /clear：runSeq 归零。真实 /clear（agent_ai.go 约 1990）会同时清零
	// finalizedRunSeq——漏掉这一步，旧回合若在停顿后才强拆就会把标记留在
	// runSeq=1 上，新回合便被误挡（假红）。
	m.mu.Lock()
	session.runSeq = 0
	session.finalizedRunSeq = 0
	m.mu.Unlock()

	// 新回合起跑，又复用 runSeq=1。
	activity2 := newAgentAIActivity()
	_, cancelFn2 := context.WithCancel(context.Background())
	t.Cleanup(cancelFn2)

	m.mu.Lock()
	session.runSeq = 1
	session.cancel = cancelFn2
	session.activity = activity2
	m.mu.Unlock()

	run2 := agentAIRun{
		sessionID: session.id,
		runID:     "run-2",
		messageID: "msg-2",
		runSeq:    1,
		activity:  activity2,
		cancel:    cancelFn2,
	}
	rec2 := &recordingWriter{}
	guard2 := m.attachRunGuard(run2, rec2.write)
	if guard2 == nil {
		t.Fatal("guard2 was not attached to the fresh run")
	}

	// 新回合挂好之后同步驱动迟到的强拆。定时器→宽限→强拆这条链由
	// TestRunGuardKillLogsAndForceFinalizesAfterGrace 覆盖；这里只验证迟到
	// 强拆的身份校验，因此同步驱动，finalizeOnce 保证与真实定时器幂等合一。
	guard1.forceFinalize("stalled")

	// 正向证据：被测代码真的执行过——旧回合的 recorder 恰好收到一帧
	// turn_stalled（emit 在 goroutine 里，用有界轮询等它落地）。
	deadline := time.Now().Add(2 * time.Second)
	for len(rec1.snapshot()) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	payloads1 := rec1.snapshot()
	if len(payloads1) != 1 {
		t.Fatalf("old guard emitted %d terminal payloads, want exactly 1 (forceFinalize did not run)", len(payloads1))
	}
	if got := payloads1[0]["error_code"]; got != "turn_stalled" {
		t.Fatalf("old guard error_code = %v, want turn_stalled", got)
	}

	m.mu.Lock()
	curGuard := session.guard
	curCancel := session.cancel
	finalized2 := session.finalizedRunSeq
	m.mu.Unlock()
	if got := len(rec2.snapshot()); got != 0 {
		t.Fatalf("new run received %d terminal payloads, want 0", got)
	}
	if curGuard != guard2 {
		t.Fatal("late forceFinalize detached the current run's guard")
	}
	if finalized2 != 0 {
		t.Fatalf("finalizedRunSeq = %d, want 0 (late forceFinalize fenced the reused runSeq=1)", finalized2)
	}
	if curCancel == nil {
		t.Fatal("late forceFinalize cleared the fresh run's cancel")
	}
}

// TestZombieLateCleanupDoesNotTouchReusedRunSeq 锁死 runAgentCore 的两个 defer
// 必须带上「自己回合的 guard」。
//
// /clear 把 runSeq 归零，下一回合又复用同一个 runSeq；只比 runSeq 的收尾会
// 把新回合的 cancel/activity/guard/done 通道一起清掉——新回合从此既停不下来
// （cancel=nil），宽限计时器也被提前关掉（done 已 close），真僵尸无人强拆。
func TestZombieLateCleanupDoesNotTouchReusedRunSeq(t *testing.T) {
	m, session, guard1, rec1 := newGuardTestManager(t)

	// 模拟 /clear：runSeq 归零，finalizedRunSeq 一并清零（真实 /clear 同款）。
	m.mu.Lock()
	session.runSeq = 0
	session.finalizedRunSeq = 0
	m.mu.Unlock()

	// 新回合起跑，复用 runSeq=1。
	activity2 := newAgentAIActivity()
	_, cancelFn2 := context.WithCancel(context.Background())
	t.Cleanup(cancelFn2)
	m.mu.Lock()
	session.runSeq = 1
	session.cancel = cancelFn2
	session.activity = activity2
	m.mu.Unlock()

	run2 := agentAIRun{
		sessionID: session.id,
		runID:     "run-2",
		messageID: "msg-2",
		runSeq:    1,
		activity:  activity2,
		cancel:    cancelFn2,
	}
	rec2 := &recordingWriter{}
	guard2 := m.attachRunGuard(run2, rec2.write)
	if guard2 == nil {
		t.Fatal("guard2 was not attached to the fresh run")
	}

	// 僵尸（旧回合）迟返回，执行自己的两个 defer。
	m.markRunGuardDone(session.id, 1, guard1)
	m.clearRunning(session.id, 1, rec1.write, guard1)

	m.mu.Lock()
	curGuard := session.guard
	curCancel := session.cancel
	curActivity := session.activity
	curFinalized := session.finalizedRunSeq
	m.mu.Unlock()

	if curGuard != guard2 {
		t.Fatal("僵尸的收尾把新回合的 guard 摘掉了")
	}
	if curCancel == nil {
		t.Fatal("僵尸的收尾把新回合的 cancel 清空了（新回合将停不下来）")
	}
	if curActivity != activity2 {
		t.Fatal("僵尸的收尾把新回合的 activity 清空了（看门狗失去线索）")
	}
	if curFinalized != 0 {
		t.Fatalf("finalizedRunSeq = %d, want 0", curFinalized)
	}
	select {
	case <-guard2.done:
		t.Fatal("僵尸的 markRunGuardDone 关掉了新回合的 done（宽限计时器被提前关死）")
	default:
	}
	if got := len(rec2.snapshot()); got != 0 {
		t.Fatalf("新回合收到 %d 帧终态，want 0", got)
	}
}

// 【本轮应修-2】强拆终态的投递必须有时间上界。
//
// 病态现场：僵尸 run 正卡在 emitter 互斥锁或底层网络写侧，强拆发终态帧的
// goroutine 会跟着无限期挂住——会话状态已复位、手机却永远等不到终态帧，而
// 日志里一个字都没有。上界 + 哨兵错误把「没送到」变成可观测事实。
func TestEmitTerminalBoundedTimesOutOnBlockedEmitter(t *testing.T) {
	block := make(chan struct{})
	defer close(block)

	started := time.Now()
	err := emitTerminalBounded(func(map[string]interface{}) error {
		<-block
		return nil
	}, map[string]interface{}{"type": models.AgentEventAIError}, 50*time.Millisecond)
	elapsed := time.Since(started)

	if !errors.Is(err, errAgentAITerminalBlocked) {
		t.Fatalf("error = %v, want errAgentAITerminalBlocked", err)
	}
	if elapsed < 40*time.Millisecond {
		t.Fatalf("elapsed = %s：早于超时就返回，说明根本没等 emit", elapsed)
	}
	if elapsed > time.Second {
		t.Fatalf("elapsed = %s：超时上界没生效", elapsed)
	}
}

func TestEmitTerminalBoundedPropagatesEmitterError(t *testing.T) {
	writeErr := errors.New("write side closed")
	err := emitTerminalBounded(func(map[string]interface{}) error {
		return writeErr
	}, map[string]interface{}{"type": models.AgentEventAIError}, 50*time.Millisecond)
	if !errors.Is(err, writeErr) {
		t.Fatalf("error = %v, want 原样透传的写侧错误", err)
	}
	if errors.Is(err, errAgentAITerminalBlocked) {
		t.Fatal("写侧错误被误判成超时哨兵：两条失败路径必须可区分")
	}
}

func TestEmitTerminalBoundedPassesThroughSuccess(t *testing.T) {
	calls := int32(0)
	err := emitTerminalBounded(func(map[string]interface{}) error {
		atomic.AddInt32(&calls, 1)
		return nil
	}, map[string]interface{}{"type": models.AgentEventAIError}, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("error = %v, want nil", err)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("emit 调用次数 = %d, want 1", got)
	}
}

// forceFinalize 自己绝不能被卡死的写侧拖住：会话复位（手机回到 idle）是
// 强拆的第一目的，投递只是尽力而为。
// 刻意不覆盖 agentAIKillGrace：实参求值读一次 agentAIKillGrace 后，投递
// goroutine 便卡死在 emit 内部永不返回；此时若用 t.Cleanup 回写这个包级
// var，回写与那次读之间没有任何同步边，构成真实数据竞争（-race 已实测
// 报出）。用生产默认 30s 判据更强：同步投递的话 forceFinalize 会卡 30s，
// 异步则立刻返回。
func TestForceFinalizeReturnsImmediatelyWithBlockedEmitter(t *testing.T) {
	m := newAgentAIManager()
	activity := newAgentAIActivity()
	_, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	session := &agentAISession{
		id:       "sess-blocked",
		runSeq:   1,
		cancel:   cancel,
		activity: activity,
	}
	m.sessions[session.id] = session
	run := agentAIRun{
		sessionID: session.id,
		runID:     "run-blocked",
		messageID: "msg-blocked",
		runSeq:    1,
		activity:  activity,
		cancel:    cancel,
	}
	block := make(chan struct{})
	defer close(block)
	guard := m.attachRunGuard(run, func(interface{}) error {
		<-block
		return nil
	})
	if guard == nil {
		t.Fatal("guard 挂载失败")
	}

	started := time.Now()
	guard.forceFinalize("stalled")
	elapsed := time.Since(started)
	if elapsed > 500*time.Millisecond {
		t.Fatalf("forceFinalize 耗时 %s：被卡死的写侧拖住了", elapsed)
	}

	m.mu.Lock()
	stillRunning := m.sessions[session.id].cancel != nil
	attached := m.sessions[session.id].guard != nil
	finalized := m.sessions[session.id].finalizedRunSeq
	m.mu.Unlock()
	if stillRunning {
		t.Fatal("强拆后 session.cancel 仍在：手机不会回到 idle")
	}
	if attached {
		t.Fatal("强拆后 guard 未摘除")
	}
	if finalized != 1 {
		t.Fatalf("finalizedRunSeq = %d, want 1", finalized)
	}
}
