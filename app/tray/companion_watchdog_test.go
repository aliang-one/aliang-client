package tray

import (
	"testing"
	"time"
)

// 白名单扩到 refresh_invalid 等粘性禁用态之后,core 自己也没有有效会话时
// reconcile 会持续失败;若每个 10s tick 都重试就是失败风暴。退避门必须:
// 首次立即放行、失败后按间隔冷却、间隔期满放行重试、reset 后立即放行。
func TestReconcileGateBackoff(t *testing.T) {
	var gate reconcileGate
	now := time.Now()

	if !gate.allow(now) {
		t.Fatal("first attempt must be allowed immediately")
	}
	gate.mark(now)

	if gate.allow(now.Add(10 * time.Second)) {
		t.Fatal("retry one tick after failure must be blocked")
	}
	if gate.allow(now.Add(agentWatchdogReconcileRetryInterval - time.Second)) {
		t.Fatal("retry just before the interval elapses must be blocked")
	}
	if !gate.allow(now.Add(agentWatchdogReconcileRetryInterval)) {
		t.Fatal("retry after the interval must be allowed")
	}

	gate.reset()
	if !gate.allow(now) {
		t.Fatal("reset must allow an immediate retry")
	}
}

// registrationRecoveryTracker：坏态持续 ≥ 阈值才放行 reconcile+首次提醒；
// 提醒按独立间隔节流；好态复位计时（下次坏态重新计满阈值）。
func TestRegistrationRecoveryTracker(t *testing.T) {
	var tracker registrationRecoveryTracker
	start := time.Now()

	if r, n := tracker.observe(true, start); r || n {
		t.Fatal("first bad observation must only start the clock, not act")
	}
	if r, n := tracker.observe(true, start.Add(agentWatchdogRegistrationRecoveryThreshold-time.Minute)); r || n {
		t.Fatal("bad state under threshold must not act")
	}
	if r, n := tracker.observe(true, start.Add(agentWatchdogRegistrationRecoveryThreshold)); !r || !n {
		t.Fatal("sustained bad state past threshold must reconcile and notify once")
	}
	if r, n := tracker.observe(true, start.Add(agentWatchdogRegistrationRecoveryThreshold+5*time.Minute)); !r || n {
		t.Fatal("sustained bad state must keep requesting reconciliation but throttle the notification")
	}
	if r, n := tracker.observe(true, start.Add(agentWatchdogRegistrationRecoveryThreshold+agentWatchdogRegistrationNotifyInterval)); !r || !n {
		t.Fatal("notification must be due again after the throttle interval")
	}

	if r, n := tracker.observe(false, start.Add(agentWatchdogRegistrationRecoveryThreshold+agentWatchdogRegistrationNotifyInterval+time.Minute)); r || n {
		t.Fatal("healthy state must not act")
	}
	if r, n := tracker.observe(true, start.Add(agentWatchdogRegistrationRecoveryThreshold+agentWatchdogRegistrationNotifyInterval+2*time.Minute)); r || n {
		t.Fatal("recovered-then-bad state must restart the clock and not act before the threshold")
	}
}
