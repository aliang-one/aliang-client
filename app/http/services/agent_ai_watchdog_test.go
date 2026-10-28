package services

import (
	"context"
	"testing"
	"time"
)

func TestAgentAIActivityLifecycle(t *testing.T) {
	a := newAgentAIActivity()
	if a == nil {
		t.Fatal("newAgentAIActivity returned nil")
	}
	if a.awaiting() {
		t.Fatalf("new activity should not be awaiting approval")
	}
	if idle := a.idleFor(); idle < 0 || idle > time.Second {
		t.Fatalf("fresh activity idleFor = %v, want within 1s of 0", idle)
	}
	if got := a.killReasonOr("fallback"); got != "fallback" {
		t.Fatalf("killReasonOr = %q, want fallback", got)
	}

	// Bump resets the idle clock to "now".
	a.bump()
	if idle := a.idleFor(); idle > 50*time.Millisecond {
		t.Fatalf("after bump idleFor = %v, want ~0", idle)
	}

	// Entering approval-wait pauses the idle watchdog and bumps activity.
	a.setAwaitingApproval(true)
	if !a.awaiting() {
		t.Fatal("setAwaitingApproval(true) should mark awaiting")
	}
	if idle := a.idleFor(); idle > 50*time.Millisecond {
		t.Fatalf("setAwaitingApproval should bump, idleFor = %v", idle)
	}
	// Leaving approval-wait clears the flag (and bumps, granting a fresh window).
	a.setAwaitingApproval(false)
	if a.awaiting() {
		t.Fatal("setAwaitingApproval(false) should clear awaiting")
	}

	// Tool/subagent waits also pause the idle watchdog and bump activity.
	a.beginToolUseWait()
	if !a.idlePaused() {
		t.Fatal("beginToolUseWait should pause idle watchdog")
	}
	if idle := a.idleFor(); idle > 50*time.Millisecond {
		t.Fatalf("beginToolUseWait should bump, idleFor = %v", idle)
	}
	a.endToolUseWait()
	if a.idlePaused() {
		t.Fatal("endToolUseWait should unpause idle watchdog once all tools resolved")
	}

	a.setKillReason("idle_timeout")
	if got := a.killReasonOr("fallback"); got != "idle_timeout" {
		t.Fatalf("killReasonOr = %q, want idle_timeout", got)
	}
}

func TestAgentAIActivityToolWaitClock(t *testing.T) {
	a := newAgentAIActivity()

	// 未等待时时钟为 0。
	if got := a.toolWaitFor(); got != 0 {
		t.Fatalf("fresh activity toolWaitFor = %v, want 0", got)
	}

	// 首次 begin 起表。
	a.beginToolUseWait()
	time.Sleep(5 * time.Millisecond) // coarse wall-clock guard: the clock must tick
	if got := a.toolWaitFor(); got <= 0 {
		t.Fatalf("after beginToolUseWait toolWaitFor = %v, want > 0", got)
	}

	// 嵌套等待：第二个 begin 不得重置时钟（连续等待语义）。
	first := a.toolWaitFor()
	time.Sleep(5 * time.Millisecond)
	a.beginToolUseWait()
	if got := a.toolWaitFor(); got < first {
		t.Fatalf("nested beginToolUseWait reset the clock: %v < %v", got, first)
	}

	// 审批挂起：时钟清零（人的决策时间不计入）。
	a.setAwaitingApproval(true)
	if got := a.toolWaitFor(); got != 0 {
		t.Fatalf("approval should pause tool-wait clock, got %v", got)
	}

	// 审批解除且仍有 pending：重新起表。
	a.setAwaitingApproval(false)
	time.Sleep(5 * time.Millisecond) // coarse wall-clock guard: re-armed clock must tick
	if got := a.toolWaitFor(); got <= 0 {
		t.Fatalf("approval resolve with pending tools should re-arm clock, got %v", got)
	}

	// 归零清表：最后一个 tool_result 到达后时钟回 0。
	a.endToolUseWait()
	a.endToolUseWait()
	if got := a.toolWaitFor(); got != 0 {
		t.Fatalf("after all tools resolved toolWaitFor = %v, want 0", got)
	}
	if a.idlePaused() {
		t.Fatal("idle watchdog should be unpaused once all tools resolved")
	}

	// 审批解除但无 pending 时不误起表。
	a.setAwaitingApproval(true)
	a.setAwaitingApproval(false)
	if got := a.toolWaitFor(); got != 0 {
		t.Fatalf("approval resolve with no pending tools should leave clock at 0, got %v", got)
	}
}

func TestAgentAIActivityProgressTracking(t *testing.T) {
	a := newAgentAIActivity()

	// A fresh run starts with a full no-progress window.
	if p := a.progressIdleFor(); p < 0 || p > time.Second {
		t.Fatalf("fresh progressIdleFor = %v, want within 1s of 0", p)
	}

	// Raw output (bump) must NOT count as progress — that is exactly the
	// degenerate-loop blind spot this tracking exists for (a model emitting
	// filler `echo` tool calls every few seconds keeps bump() fed for hours
	// while producing nothing).
	time.Sleep(20 * time.Millisecond)
	a.bump()
	if p := a.progressIdleFor(); p < 15*time.Millisecond {
		t.Fatalf("after bump progressIdleFor = %v, want unchanged (~>15ms)", p)
	}

	// markProgress (assistant text / file change) resets the progress clock.
	a.markProgress()
	if p := a.progressIdleFor(); p > 15*time.Millisecond {
		t.Fatalf("after markProgress progressIdleFor = %v, want ~0", p)
	}
}

func TestAgentAIActivityProgressNilSafe(t *testing.T) {
	var a *agentAIActivity
	a.markProgress()
	if p := a.progressIdleFor(); p != 0 {
		t.Fatalf("nil progressIdleFor = %v, want 0", p)
	}
}

func TestAgentAIActivityNilSafe(t *testing.T) {
	var a *agentAIActivity
	// None of these must panic on a nil receiver.
	a.bump()
	a.setAwaitingApproval(true)
	_ = a.awaiting()
	a.beginToolUseWait()
	a.endToolUseWait()
	_ = a.idlePaused()
	_ = a.idleFor()
	a.setKillReason("idle_timeout")
	if got := a.killReasonOr("fallback"); got != "fallback" {
		t.Fatalf("nil killReasonOr = %q, want fallback", got)
	}
}

func TestAgentAIRunStoppedStatus(t *testing.T) {
	a := newAgentAIActivity()

	// No kill reason, no limiter -> plain stop.
	if status, msg := agentAIRunStoppedStatus(a, nil); status != "stopped" || msg != "" {
		t.Fatalf("plain stop = (%q,%q), want (stopped,\"\")", status, msg)
	}

	// No-progress kill reason maps to the server-recognized idle_timeout
	// status (→ timed_out / error on the backend) with a distinct message.
	progress := newAgentAIActivity()
	progress.setKillReason("no_progress")
	if status, msg := agentAIRunStoppedStatus(progress, nil); status != "idle_timeout" || msg == "" {
		t.Fatalf("no_progress = (%q,%q), want (idle_timeout, non-empty)", status, msg)
	}

	// Idle kill reason.
	a.setKillReason("idle_timeout")
	if status, msg := agentAIRunStoppedStatus(a, nil); status != "idle_timeout" || msg == "" {
		t.Fatalf("idle = (%q,%q), want (idle_timeout, non-empty)", status, msg)
	}

	// Hard ceiling kill reason.
	a.setKillReason("hard_ceiling")
	if status, msg := agentAIRunStoppedStatus(a, nil); status != "hard_timeout" || msg == "" {
		t.Fatalf("hard = (%q,%q), want (hard_timeout, non-empty)", status, msg)
	}

	// No kill reason but limiter exceeded -> output_limited.
	fresh := newAgentAIActivity()
	exceeded := &agentAIOutputLimiter{exceeded: true}
	if status, msg := agentAIRunStoppedStatus(fresh, exceeded); status != "output_limited" || msg == "" {
		t.Fatalf("limiter = (%q,%q), want (output_limited, non-empty)", status, msg)
	}

	// nil activity -> fallback plain stop.
	if status, msg := agentAIRunStoppedStatus(nil, nil); status != "stopped" || msg != "" {
		t.Fatalf("nil activity = (%q,%q), want (stopped,\"\")", status, msg)
	}
}

func TestAgentAIRunStoppedStatusToolWaitTimeout(t *testing.T) {
	activity := newAgentAIActivity()
	activity.setKillReason("tool_wait_timeout")
	status, msg := agentAIRunStoppedStatus(activity, nil)
	if status != "timeout" {
		t.Fatalf("status = %q, want timeout (server terminal-status family)", status)
	}
	if msg == "" {
		t.Fatal("expected a non-empty human-readable reason")
	}
}

// runWatchdogUntilCancelled runs the watchdog loop with the given windows and
// returns whether ctx was cancelled within the timeout, plus the kill reason.
func runWatchdogUntilCancelled(t *testing.T, idleWindow, hardCeiling, noProgressWindow, interval, wait time.Duration, configure func(*agentAIActivity)) (cancelled bool, reason string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	activity := newAgentAIActivity()
	if configure != nil {
		configure(activity)
	}
	done := make(chan struct{})
	go func() {
		agentAIWatchdogLoop(ctx, activity, cancel, idleWindow, hardCeiling, noProgressWindow, 0, 0, interval)
		close(done)
	}()
	select {
	case <-done:
		cancelled = true
	case <-time.After(wait):
		cancelled = false
	}
	return cancelled, activity.killReasonOr("")
}

func TestAgentAIWatchdogLoopIdleKills(t *testing.T) {
	// Never bumped, not awaiting -> cancelled shortly after the idle window.
	cancelled, reason := runWatchdogUntilCancelled(t,
		50*time.Millisecond,  /* idleWindow */
		0,                    /* hardCeiling disabled */
		0,                    /* noProgressWindow disabled */
		10*time.Millisecond,  /* interval */
		500*time.Millisecond, /* wait */
		nil)
	if !cancelled {
		t.Fatal("watchdog did not cancel an idle run")
	}
	if reason != "idle_timeout" {
		t.Fatalf("kill reason = %q, want idle_timeout", reason)
	}
}

func TestAgentAIWatchdogLoopAwaitingExempt(t *testing.T) {
	// Awaiting approval must pause the idle watchdog: no cancel within the wait.
	cancelled, reason := runWatchdogUntilCancelled(t,
		40*time.Millisecond,  /* idleWindow */
		0,                    /* hardCeiling disabled */
		0,                    /* noProgressWindow disabled */
		10*time.Millisecond,  /* interval */
		200*time.Millisecond, /* wait (5x idleWindow) */
		func(a *agentAIActivity) { a.setAwaitingApproval(true) })
	if cancelled {
		t.Fatal("watchdog cancelled an approval-waiting run; approval must be exempt")
	}
	if reason != "" {
		t.Fatalf("kill reason = %q, want empty (no kill)", reason)
	}
}

func TestAgentAIWatchdogLoopToolUseWaitExempt(t *testing.T) {
	// A Claude tool_use (including Task/subagent) can be legitimately silent
	// while waiting for its tool_result. That must not be mistaken for idle.
	cancelled, reason := runWatchdogUntilCancelled(t,
		40*time.Millisecond,  /* idleWindow */
		0,                    /* hardCeiling disabled */
		0,                    /* noProgressWindow disabled */
		10*time.Millisecond,  /* interval */
		200*time.Millisecond, /* wait (5x idleWindow) */
		func(a *agentAIActivity) { a.beginToolUseWait() })
	if cancelled {
		t.Fatal("watchdog cancelled a run waiting for a tool/subagent result")
	}
	if reason != "" {
		t.Fatalf("kill reason = %q, want empty (no kill)", reason)
	}
}

func TestAgentAIWatchdogLoopToolResultReenablesIdle(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	activity := newAgentAIActivity()
	activity.beginToolUseWait()
	done := make(chan struct{})
	go func() {
		agentAIWatchdogLoop(ctx, activity, cancel, 40*time.Millisecond, 0, 0, 0, 0, 10*time.Millisecond)
		close(done)
	}()

	select {
	case <-done:
		t.Fatal("watchdog cancelled while tool/subagent result was pending")
	case <-time.After(150 * time.Millisecond):
	}

	activity.endToolUseWait()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("watchdog did not cancel after tool/subagent result wait ended and output stayed idle")
	}
	if reason := activity.killReasonOr(""); reason != "idle_timeout" {
		t.Fatalf("kill reason = %q, want idle_timeout", reason)
	}
}

func TestAgentAIWatchdogLoopActivityResetsIdle(t *testing.T) {
	// Continuous output keeps the run alive past several idle windows, then once
	// output stops the watchdog cancels as idle.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	activity := newAgentAIActivity()
	done := make(chan struct{})
	go func() {
		agentAIWatchdogLoop(ctx, activity, cancel, 40*time.Millisecond, 0, 0, 0, 0, 10*time.Millisecond)
		close(done)
	}()

	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(10 * time.Millisecond):
				activity.bump()
			}
		}
	}()

	// While bumping, the run must survive well past the idle window.
	select {
	case <-done:
		t.Fatal("watchdog cancelled a run that was still producing output")
	case <-time.After(150 * time.Millisecond):
	}
	close(stop)

	// Once output stops, it must cancel as idle within the wait.
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("watchdog did not cancel after output stopped")
	}
	if reason := activity.killReasonOr(""); reason != "idle_timeout" {
		t.Fatalf("kill reason = %q, want idle_timeout", reason)
	}
}

func TestAgentAIWatchdogLoopHardCeilingKills(t *testing.T) {
	// A tiny hard ceiling fires regardless of idle window (which is set large).
	cancelled, reason := runWatchdogUntilCancelled(t,
		time.Hour,            /* idleWindow: never fires */
		time.Millisecond,     /* hardCeiling */
		0,                    /* noProgressWindow disabled */
		5*time.Millisecond,   /* interval */
		300*time.Millisecond, /* wait */
		nil)
	if !cancelled {
		t.Fatal("watchdog did not cancel a run past the hard ceiling")
	}
	if reason != "hard_ceiling" {
		t.Fatalf("kill reason = %q, want hard_ceiling", reason)
	}
}

func TestAgentAIWatchdogLoopNoProgressKills(t *testing.T) {
	// A run that keeps EMITTING output (bump every few ms — the degenerate
	// `echo` filler-tool-call loop signature) but never markProgress must be
	// killed by the no-progress window even though the idle watchdog sees
	// constant activity.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	activity := newAgentAIActivity()
	done := make(chan struct{})
	go func() {
		agentAIWatchdogLoop(ctx, activity, cancel, time.Hour /* idleWindow: never fires */, 0 /* hardCeiling disabled */, 50*time.Millisecond /* noProgressWindow */, 0 /* toolWaitCeiling disabled */, 0 /* subagentCeiling disabled */, 10*time.Millisecond /* interval */)
		close(done)
	}()

	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(5 * time.Millisecond):
				activity.bump()
			}
		}
	}()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("watchdog did not cancel a run that never made progress")
	}
	if reason := activity.killReasonOr(""); reason != "no_progress" {
		t.Fatalf("kill reason = %q, want no_progress", reason)
	}
}

func TestAgentAIWatchdogLoopProgressPreventsNoProgressKill(t *testing.T) {
	// markProgress (assistant text / file change) must keep the run alive past
	// several no-progress windows.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	activity := newAgentAIActivity()
	done := make(chan struct{})
	go func() {
		agentAIWatchdogLoop(ctx, activity, cancel, time.Hour, 0, 60*time.Millisecond, 0, 0, 10*time.Millisecond)
		close(done)
	}()

	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(15 * time.Millisecond):
				activity.markProgress()
			}
		}
	}()

	select {
	case <-done:
		t.Fatal("watchdog cancelled a run that kept making progress")
	case <-time.After(250 * time.Millisecond):
	}
	if reason := activity.killReasonOr(""); reason != "" {
		t.Fatalf("kill reason = %q, want empty (no kill)", reason)
	}
}

func TestAgentAIWatchdogLoopNoProgressExemptDuringToolWait(t *testing.T) {
	// A single long-lived tool/subagent wait (e.g. a Task tool running for an
	// hour) must not be killed for no-progress: at check time a PENDING tool
	// wait means work is in flight, not looping.
	cancelled, reason := runWatchdogUntilCancelled(t,
		time.Hour,            /* idleWindow: never fires */
		0,                    /* hardCeiling disabled */
		40*time.Millisecond,  /* noProgressWindow */
		10*time.Millisecond,  /* interval */
		200*time.Millisecond, /* wait (5x noProgressWindow) */
		func(a *agentAIActivity) { a.beginToolUseWait() })
	if cancelled {
		t.Fatal("watchdog cancelled a run with a pending tool/subagent wait")
	}
	if reason != "" {
		t.Fatalf("kill reason = %q, want empty (no kill)", reason)
	}
}

func TestAgentAIWatchdogLoopToolWaitCeilingKills(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	activity := newAgentAIActivity()
	activity.beginToolUseWait()
	done := make(chan struct{})
	go func() {
		// idle 窗口巨大：确保唯一可能触发的是 tool-wait 上限。
		agentAIWatchdogLoop(ctx, activity, cancel, time.Hour, 0, 0, 40*time.Millisecond, 0, 5*time.Millisecond)
		close(done)
	}()

	select {
	case <-done:
		if reason := activity.killReasonOr(""); reason != "tool_wait_timeout" {
			t.Fatalf("kill reason = %q, want tool_wait_timeout", reason)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("watchdog did not cancel a hung tool wait past the ceiling")
	}
}

func TestAgentAIWatchdogLoopApprovalPausesToolWaitClock(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	activity := newAgentAIActivity()
	activity.beginToolUseWait()
	activity.setAwaitingApproval(true) // 时钟清零 → 上限不得触发
	done := make(chan struct{})
	go func() {
		agentAIWatchdogLoop(ctx, activity, cancel, time.Hour, 0, 0, 40*time.Millisecond, 0, 5*time.Millisecond)
		close(done)
	}()

	select {
	case <-done:
		t.Fatalf("watchdog cancelled during approval wait, reason=%q", activity.killReasonOr(""))
	case <-time.After(200 * time.Millisecond):
	}
}

func TestAgentAIWatchdogLoopApprovalResolveRearmsToolWaitClock(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	activity := newAgentAIActivity()
	activity.beginToolUseWait()
	activity.setAwaitingApproval(true)
	done := make(chan struct{})
	go func() {
		agentAIWatchdogLoop(ctx, activity, cancel, time.Hour, 0, 0, 40*time.Millisecond, 0, 5*time.Millisecond)
		close(done)
	}()
	time.Sleep(80 * time.Millisecond)   // 在审批中度过整个上限窗口
	activity.setAwaitingApproval(false) // 重新起表 → 一个新窗口后必须触发

	select {
	case <-done:
		if reason := activity.killReasonOr(""); reason != "tool_wait_timeout" {
			t.Fatalf("kill reason = %q, want tool_wait_timeout", reason)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("watchdog did not cancel after approval resolved and tool stayed pending")
	}
}

func TestAgentAIWatchdogHardCeilingExcludesWaitingStates(t *testing.T) {
	// 硬顶只看引擎活跃时长：整段处于工具等待态的回合不应被硬顶杀掉。
	activity := newAgentAIActivity()
	activity.beginToolUseWait()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	killed := make(chan string, 1)
	loopCancel := func() { killed <- activity.killReasonOr("") }

	go agentAIWatchdogLoop(ctx, activity, loopCancel,
		// idle 分支是唯一没有 `>0` 禁用开关的（条件为
		// `!idlePaused() && idleFor() > idleWindow`），传 0 反而「一 tick 就杀」。
		// 所以用 time.Hour 让它永不触发，与既有 TestAgentAIWatchdogLoopHardCeilingKills 写法一致。
		time.Hour,           // idleWindow: 实际关闭
		20*time.Millisecond, // hardCeiling: 引擎活跃 20ms
		0,                   // noProgressWindow: 关
		0,                   // toolWaitCeiling: 关（本用例只验硬顶口径）
		0,                   // subagentCeiling: 关
		5*time.Millisecond,
	)

	select {
	case reason := <-killed:
		t.Fatalf("hard ceiling killed a pure tool-wait run (reason=%q)", reason)
	case <-time.After(120 * time.Millisecond):
		// 期望：等待态不计入引擎时钟，硬顶不触发
	}

	activity.endToolUseWait()

	select {
	case reason := <-killed:
		if reason != "hard_ceiling" {
			t.Fatalf("kill reason = %q, want hard_ceiling", reason)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("hard ceiling did not fire after the run left the waiting state")
	}
}

func TestAgentAIWatchdogHardCeilingAccumulatesAcrossWaits(t *testing.T) {
	// 证伪「离开（或进入）等待态时把 engineActive 清零」这类自然误重构：
	// 硬顶必须跨越多段等待继续累计引擎活跃时长——等待期间的拍既不计入，
	// 也不该把既有的累计值抹掉。
	//
	// 时间轴（裕量刻意取大，宁可测试慢一点也不要 flake）：
	//   阶段一 ~150ms 纯活跃            → 累计约 150ms（< 300ms，不应被杀）
	//   阶段二 ~350ms beginToolUseWait  → 不累加，期间断言未被杀
	//   阶段三 离开等待后 220ms 内必须收到 hard_ceiling：
	//          正确实现只需再攒约 150ms（裕量 ~70ms）；
	//          若误为「离开等待即清零」则需重新攒满 300ms，收不到 → 用例红。
	//
	// 计时按 tick 间的 wall delta 累加，ticker 迟到不会改变累计量，只会稍微
	// 推迟判定，所以裕量只需要覆盖「判定延迟」，不需要覆盖 tick 精度。
	const (
		interval    = 10 * time.Millisecond
		hardCeiling = 300 * time.Millisecond
	)

	activity := newAgentAIActivity()

	ctx, cancel := context.WithCancel(context.Background())
	// 兜住所有失败路径：ctx 取消后 loop 必然退出，不会有 goroutine 泄漏。
	defer cancel()

	// killed 有缓冲：loop 的 loopCancel 只会触发一次，且即使测试已经返回
	// 也不会让 loop 阻塞在发送上。
	killed := make(chan string, 1)
	loopCancel := func() { killed <- activity.killReasonOr("") }

	go agentAIWatchdogLoop(ctx, activity, loopCancel,
		// idle 分支没有 `>0` 禁用开关（条件为
		// `!idlePaused() && idleFor() > idleWindow`），传 0 会一 tick 就杀，
		// 所以传 time.Hour 关闭它。
		time.Hour,   // idleWindow: 实际关闭
		hardCeiling, // hardCeiling
		0,           // noProgressWindow: 关
		0,           // toolWaitCeiling: 关（本用例只验硬顶口径）
		0,           // subagentCeiling: 关
		interval,
	)

	// 阶段一：纯活跃，累计约 150ms，远未到硬顶。
	select {
	case reason := <-killed:
		t.Fatalf("hard ceiling killed during the active phase (reason=%q)", reason)
	case <-time.After(150 * time.Millisecond):
	}

	// 阶段二：进入工具等待，这段不得计入引擎活跃时长，也不得被杀。
	activity.beginToolUseWait()
	select {
	case reason := <-killed:
		t.Fatalf("hard ceiling killed during a tool wait (reason=%q)", reason)
	case <-time.After(350 * time.Millisecond):
	}

	// 阶段三：离开等待。累计应保留阶段一的 ~150ms，只需再攒约 150ms 就越过
	// 300ms 硬顶；220ms 的窗口对正确实现有 ~70ms 裕量，对「清零」误实现则
	// 远远不够（它需要重新攒满 300ms）。
	activity.endToolUseWait()
	select {
	case reason := <-killed:
		if reason != "hard_ceiling" {
			t.Fatalf("kill reason = %q, want hard_ceiling", reason)
		}
	case <-time.After(220 * time.Millisecond):
		t.Fatal("hard ceiling did not fire within 220ms after leaving the wait; engine-active time was probably reset instead of accumulated across the wait")
	}
}

// TestAgentAIHardCeilingDisabledByDefault：时间墙默认关闭。2h 硬顶是纯配额——
// 一条消息触发的通宵任务(spec 执行/大改造)engine-active 两小时算健康工作,
// 不该被判死。runaway 防护交给活性家族(idle/no_progress/tool_wait/stale 扫描),
// 硬顶降级为显式 opt-in(env ALIANG_AI_HARD_CEILING,0 以外即启用)。
// 2026-10-28 预算审计二轮:用户拍板「时间墙没有必要,移除」。
func TestAgentAIHardCeilingDisabledByDefault(t *testing.T) {
	if agentAIHardCeiling != 0 {
		t.Fatalf("agentAIHardCeiling 默认应为 0(关闭),实际 %s;时间墙已是 opt-in", agentAIHardCeiling)
	}
}

// TestAgentAIWatchdogHardCeilingZeroNeverKills：硬顶为 0 时即使持续活跃也绝不
// 判死——钉死零值禁用语义,防止未来有人把默认值改回正数时顺手破坏该分支。
func TestAgentAIWatchdogHardCeilingZeroNeverKills(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	activity := newAgentAIActivity()
	// 持续 bump:引擎时钟每拍都在累加,若硬顶分支无视零值,任意小正数都会杀。
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			default:
				activity.bump()
				time.Sleep(5 * time.Millisecond)
			}
		}
	}()
	cancelled, reason := runWatchdogUntilCancelled(t,
		time.Hour /*idle*/, 0 /*hard*/, 0 /*noProgress*/, 10*time.Millisecond, 200*time.Millisecond,
		func(a *agentAIActivity) { _ = a })
	if cancelled {
		t.Fatalf("硬顶为 0 不许判死,reason=%q", reason)
	}
}
