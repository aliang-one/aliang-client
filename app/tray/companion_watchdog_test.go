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
