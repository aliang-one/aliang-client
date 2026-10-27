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
