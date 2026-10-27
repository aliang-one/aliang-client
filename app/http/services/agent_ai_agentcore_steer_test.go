package services

import (
	"testing"
	"time"
)

// 依赖：recordingWriter 定义在同包的 agent_ai_run_guard_test.go，
// 所以本文件必须排在 guard 相关任务之后。

func TestCoreSteerDrainBumpsActivity(t *testing.T) {
	activity := newAgentAIActivity()
	activity.lastActivityAt.Store(time.Now().Add(-time.Hour).UnixNano())

	rec := &recordingWriter{}
	run := agentAIRun{sessionID: "sess-1", runSeq: 1, messageID: "msg-1", activity: activity}
	control := newAgentAICoreSteerControl(run, rec.write)
	control.activity = activity

	if _, err := control.deliver(agentAISteerMessage{MessageID: "steer-1", Content: "hi"}); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if got := control.drain(); len(got) != 1 {
		t.Fatalf("drain = %d messages, want 1", len(got))
	}
	if activity.idleFor() > time.Minute {
		t.Fatalf("drain did not refresh lastActivityAt (idleFor=%s)", activity.idleFor())
	}
}

// TestCoreSteerDeliverDoesNotBumpActivity 焊死「活性只在消息被取走时刷新」：
// 若有人把 bump 挪进 deliver，卡死的回合就能被用户反复插话无限续命。
func TestCoreSteerDeliverDoesNotBumpActivity(t *testing.T) {
	activity := newAgentAIActivity()
	stale := time.Now().Add(-time.Hour).UnixNano()
	activity.lastActivityAt.Store(stale)

	rec := &recordingWriter{}
	run := agentAIRun{sessionID: "sess-1", runSeq: 1, messageID: "msg-1", activity: activity}
	control := newAgentAICoreSteerControl(run, rec.write)
	control.activity = activity

	if _, err := control.deliver(agentAISteerMessage{MessageID: "steer-1", Content: "hi"}); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	// 投递只证明 run 尚未 close，不证明内核循环在转。活性必须等到 drain
	// （内核真的把消息取走）才刷新，否则僵尸可靠用户反复插话无限续命。
	if got := activity.lastActivityAt.Load(); got != stale {
		t.Fatalf("deliver 不应刷新 lastActivityAt（now-stale=%s）", time.Duration(got-stale))
	}
}

// queueLenOf 读队列长度（同包测试用，走同一把锁）。
func queueLenOf(c *agentAICoreSteerControl) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.queue)
}

// pendingLenOf 读待触发活性窗数量（同包测试用，走同一把锁）。
func pendingLenOf(c *agentAICoreSteerControl) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.pending)
}

func TestCoreSteerLivenessWindowEmitsTurnStalled(t *testing.T) {
	oldWindow := steerLivenessWindow
	steerLivenessWindow = 40 * time.Millisecond
	t.Cleanup(func() { steerLivenessWindow = oldWindow })

	activity := newAgentAIActivity()
	rec := &recordingWriter{}
	run := agentAIRun{sessionID: "sess-1", runSeq: 1, messageID: "msg-1", activity: activity}
	control := newAgentAICoreSteerControl(run, rec.write)
	control.activity = activity
	stalled := make(chan string, 1)
	control.onStalled = func(reason string) { stalled <- reason }

	if _, err := control.deliver(agentAISteerMessage{MessageID: "steer-9", Content: "hi"}); err != nil {
		t.Fatalf("deliver: %v", err)
	}

	select {
	case <-stalled:
	case <-time.After(2 * time.Second):
		t.Fatal("liveness window did not fire")
	}

	payloads := rec.snapshot()
	if len(payloads) != 1 {
		t.Fatalf("acks = %d, want 1", len(payloads))
	}
	if payloads[0]["result"] != "error" || payloads[0]["code"] != "turn_stalled" {
		t.Fatalf("ack = %v, want result=error code=turn_stalled", payloads[0])
	}
}

func TestCoreSteerLivenessWindowSparesToolWait(t *testing.T) {
	oldWindow := steerLivenessWindow
	steerLivenessWindow = 40 * time.Millisecond
	t.Cleanup(func() { steerLivenessWindow = oldWindow })

	activity := newAgentAIActivity()
	activity.beginToolUseWait()

	rec := &recordingWriter{}
	run := agentAIRun{sessionID: "sess-1", runSeq: 1, messageID: "msg-1", activity: activity}
	control := newAgentAICoreSteerControl(run, rec.write)
	control.activity = activity
	control.onStalled = func(string) { t.Error("must not judge dead during a legal waiting state") }
	// 定时器可能在测试返回之后才触发，届时 t.Error 会 panic；收尾关掉它。
	t.Cleanup(func() { control.close("not_running", "", "") })

	_, _ = control.deliver(agentAISteerMessage{MessageID: "steer-10", Content: "hi"})
	time.Sleep(150 * time.Millisecond)
	if got := len(rec.snapshot()); got != 0 {
		t.Fatalf("stall ack emitted during tool wait: %d", got)
	}
	// 等待态也不许从队列里摘掉消息：解冻后它必须还能被 drain 取走。
	if queueLenOf(control) != 1 {
		t.Fatalf("queued = %d, want 1 (message must survive a legal wait)", queueLenOf(control))
	}
}

// spec §5 #11：等审批期间是无响应还是合法静默，必须靠 idlePaused 区分，
// 不能只看「多久没动静」——人可能一晚上没点那个卡片。
func TestCoreSteerLivenessWindowSparesApprovalWait(t *testing.T) {
	oldWindow := steerLivenessWindow
	steerLivenessWindow = 40 * time.Millisecond
	t.Cleanup(func() { steerLivenessWindow = oldWindow })

	activity := newAgentAIActivity()
	activity.setAwaitingApproval(true)
	defer activity.setAwaitingApproval(false)

	rec := &recordingWriter{}
	run := agentAIRun{sessionID: "sess-1", runSeq: 1, messageID: "msg-1", activity: activity}
	control := newAgentAICoreSteerControl(run, rec.write)
	control.activity = activity
	control.onStalled = func(string) { t.Error("must not judge dead while an approval is pending") }
	t.Cleanup(func() { control.close("not_running", "", "") })

	_, _ = control.deliver(agentAISteerMessage{MessageID: "steer-12", Content: "hi"})
	time.Sleep(150 * time.Millisecond)
	if got := len(rec.snapshot()); got != 0 {
		t.Fatalf("stall ack emitted during approval wait: %d", got)
	}
	if queueLenOf(control) != 1 {
		t.Fatalf("queued = %d, want 1 (message must survive approval wait)", queueLenOf(control))
	}
}

// spec §5 #12 的对应面：子代理在跑时同样属于合法等待，不能被活性窗判死。
func TestCoreSteerLivenessWindowSparesSubagentWait(t *testing.T) {
	oldWindow := steerLivenessWindow
	steerLivenessWindow = 40 * time.Millisecond
	t.Cleanup(func() { steerLivenessWindow = oldWindow })

	activity := newAgentAIActivity()
	activity.activeSubagents.Store(1)
	defer activity.activeSubagents.Store(0)

	rec := &recordingWriter{}
	run := agentAIRun{sessionID: "sess-1", runSeq: 1, messageID: "msg-1", activity: activity}
	control := newAgentAICoreSteerControl(run, rec.write)
	control.activity = activity
	control.onStalled = func(string) { t.Error("must not judge dead while a subagent is running") }
	t.Cleanup(func() { control.close("not_running", "", "") })

	_, _ = control.deliver(agentAISteerMessage{MessageID: "steer-13", Content: "hi"})
	time.Sleep(150 * time.Millisecond)
	if got := len(rec.snapshot()); got != 0 {
		t.Fatalf("stall ack emitted during subagent wait: %d", got)
	}
	if queueLenOf(control) != 1 {
		t.Fatalf("queued = %d, want 1 (message must survive subagent wait)", queueLenOf(control))
	}
}

func TestCoreSteerDrainStopsLivenessTimer(t *testing.T) {
	oldWindow := steerLivenessWindow
	steerLivenessWindow = 60 * time.Millisecond
	t.Cleanup(func() { steerLivenessWindow = oldWindow })

	activity := newAgentAIActivity()
	rec := &recordingWriter{}
	run := agentAIRun{sessionID: "sess-1", runSeq: 1, messageID: "msg-1", activity: activity}
	control := newAgentAICoreSteerControl(run, rec.write)
	control.activity = activity
	control.onStalled = func(string) { t.Error("drained steer must not be reported stalled") }

	_, _ = control.deliver(agentAISteerMessage{MessageID: "steer-11", Content: "hi"})
	_ = control.drain()
	// drain 必须真的把计时器停掉：否则 expire 见到空队列会静默返回，
	// 这个用例就算抽掉 stopLivenessLocked 也照样全绿，不承重。
	if got := pendingLenOf(control); got != 0 {
		t.Fatalf("pending timers = %d, want 0 after drain", got)
	}
	time.Sleep(150 * time.Millisecond)

	for _, p := range rec.snapshot() {
		if p["code"] == "turn_stalled" {
			t.Fatalf("unexpected turn_stalled ack: %v", p)
		}
	}
}

func TestCoreSteerLivenessWindowSparesActiveRun(t *testing.T) {
	// 回合仍在出字（delta 会 bump lastActivityAt）时，即便插话还没被 drain，
	// 也不能判死：否则一次超过窗口时长的大模型调用就会杀掉健康回合。
	// 这里把窗口调到 1 小时再直接调 expire，做到零 sleep、零竞态。
	oldWindow := steerLivenessWindow
	steerLivenessWindow = time.Hour
	t.Cleanup(func() { steerLivenessWindow = oldWindow })

	rec := &recordingWriter{}
	activity := newAgentAIActivity()
	run := agentAIRun{sessionID: "sess-active", runSeq: 1, messageID: "msg-active", activity: activity}
	control := newAgentAICoreSteerControl(run, rec.write)
	control.activity = activity
	stalled := false
	control.onStalled = func(string) { stalled = true }
	t.Cleanup(func() { control.close("not_running", "", "") })

	// 模拟「刚出过字」：lastActivityAt 就在此刻。
	activity.bump()
	if _, err := control.deliver(agentAISteerMessage{MessageID: "a", Content: "插话"}); err != nil {
		t.Fatalf("deliver: %v", err)
	}

	control.expire("a")

	if got := queueLenOf(control); got != 1 {
		t.Fatalf("queued = %d, want 1 (active run must keep the steer)", got)
	}
	if got := len(rec.snapshot()); got != 0 {
		t.Fatalf("acks = %d, want 0 (active run must not be reported stalled)", got)
	}
	if stalled {
		t.Fatal("onStalled called for an actively generating run")
	}
}
