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
