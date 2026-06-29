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

// runWatchdogUntilCancelled runs the watchdog loop with the given windows and
// returns whether ctx was cancelled within the timeout, plus the kill reason.
func runWatchdogUntilCancelled(t *testing.T, idleWindow, hardCeiling, interval, wait time.Duration, configure func(*agentAIActivity)) (cancelled bool, reason string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	activity := newAgentAIActivity()
	if configure != nil {
		configure(activity)
	}
	done := make(chan struct{})
	go func() {
		agentAIWatchdogLoop(ctx, activity, cancel, idleWindow, hardCeiling, interval)
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
		agentAIWatchdogLoop(ctx, activity, cancel, 40*time.Millisecond, 0, 10*time.Millisecond)
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
		agentAIWatchdogLoop(ctx, activity, cancel, 40*time.Millisecond, 0, 10*time.Millisecond)
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
