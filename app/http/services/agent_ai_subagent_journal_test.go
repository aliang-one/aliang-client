package services

// 子 agent 终态帧断线暂存/重放测试（2026-11-10 P2 #4）。
//
// 锁死三条契约：
//  1. emitter 写失败时只对「终态子 agent 帧」触发 drop 回调（started/progress/
//     其他类型不触发）；写成功不触发。
//  2. 暂存表按 session|task 键覆盖、封顶丢最旧。
//  3. 重放：写失败保留待下次 hello，写成功清账（幂等，不重发）。

import (
	"errors"
	"fmt"
	"testing"
)

func subagentFrame(phase string) map[string]interface{} {
	return map[string]interface{}{
		"type":       "ai.subagent",
		"session_id": "sess-r",
		"task_id":    "task-" + phase,
		"phase":      phase,
	}
}

func TestEmitterDropsOnlyTerminalSubagentFrames(t *testing.T) {
	var dropped []string
	e := &agentAIRunEmitter{
		runID: "run-1",
		write: func(interface{}) error { return errors.New("remote agent writer unavailable (disconnected)") },
		onSubagentTerminalDrop: func(payload map[string]interface{}) {
			dropped = append(dropped, fmt.Sprint(payload["phase"]))
		},
	}
	_ = e.emit(subagentFrame("started"))
	_ = e.emit(subagentFrame("progress"))
	if len(dropped) != 0 {
		t.Fatalf("非终态帧不得触发 drop: %v", dropped)
	}
	_ = e.emit(subagentFrame("completed"))
	if len(dropped) != 1 || dropped[0] != "completed" {
		t.Fatalf("终态帧必须触发 drop: %v", dropped)
	}
}

func TestEmitterNoDropOnSuccessfulWrite(t *testing.T) {
	drops := 0
	e := &agentAIRunEmitter{
		runID: "run-1",
		write: func(interface{}) error { return nil },
		onSubagentTerminalDrop: func(map[string]interface{}) {
			drops++
		},
	}
	_ = e.emit(subagentFrame("completed"))
	if drops != 0 {
		t.Fatalf("写成功不得触发 drop: %d", drops)
	}
}

func TestEmitterTerminalRunStillForwardsSubagentTerminal(t *testing.T) {
	// run 终态屏障之后（terminal=true）后续 emit 被吞——但屏障内 onTerminal
	// 之后的写仍走 e.write；本测试锁死「屏障打开前」终态子agent帧照常投递。
	writes := 0
	e := &agentAIRunEmitter{
		runID: "run-1",
		write: func(interface{}) error { writes++; return nil },
	}
	_ = e.emit(subagentFrame("completed"))
	if writes != 1 {
		t.Fatalf("正常帧必须写出: %d", writes)
	}
}

func TestPendingSubagentRememberOverwriteAndCap(t *testing.T) {
	m := newAgentAIManager()
	m.rememberPendingSubagentTerminal(subagentFrame("completed"))
	m.rememberPendingSubagentTerminal(map[string]interface{}{
		"type": "ai.subagent", "session_id": "sess-r", "task_id": "task-completed", "phase": "failed",
	})
	if m.pendingSubagentTerminalCount() != 1 {
		t.Fatalf("同键必须覆盖: %d", m.pendingSubagentTerminalCount())
	}
	for i := 0; i < agentAIPendingSubagentCap+10; i++ {
		m.rememberPendingSubagentTerminal(map[string]interface{}{
			"type": "ai.subagent", "session_id": "sess-r", "task_id": fmt.Sprintf("cap-%d", i), "phase": "interrupted",
		})
	}
	if m.pendingSubagentTerminalCount() != agentAIPendingSubagentCap {
		t.Fatalf("封顶失效: %d", m.pendingSubagentTerminalCount())
	}
	// 最旧者（首个 completed 行,键 sess-r|task-completed）必须被挤掉。
	m.mu.Lock()
	_, exists := m.pendingSubagentTerminals["sess-r|task-completed"]
	m.mu.Unlock()
	if exists {
		t.Fatal("封顶必须丢最旧")
	}
}

func TestReplayPendingSubagentClearsOnSuccessKeepsOnFailure(t *testing.T) {
	m := newAgentAIManager()
	m.rememberPendingSubagentTerminal(subagentFrame("completed"))
	m.rememberPendingSubagentTerminal(subagentFrame("failed"))

	// 全部写失败：保留待下次 hello。
	failWrite := func(interface{}) error { return errors.New("disconnected") }
	m.replayPendingSubagentTerminals(failWrite)
	if m.pendingSubagentTerminalCount() != 2 {
		t.Fatalf("写失败必须保留: %d", m.pendingSubagentTerminalCount())
	}

	// 全部写成功：清账。
	okWrite := func(interface{}) error { return nil }
	m.replayPendingSubagentTerminals(okWrite)
	if m.pendingSubagentTerminalCount() != 0 {
		t.Fatalf("写成功必须清账: %d", m.pendingSubagentTerminalCount())
	}
	// 空表重放 no-op。
	m.replayPendingSubagentTerminals(okWrite)
	if m.pendingSubagentTerminalCount() != 0 {
		t.Fatalf("空表重放异常: %d", m.pendingSubagentTerminalCount())
	}
}

func TestAgentAISubagentFrameTerminalPredicate(t *testing.T) {
	cases := []struct {
		phase string
		want  bool
	}{
		{"completed", true}, {"failed", true}, {"interrupted", true},
		{"started", false}, {"progress", false}, {"", false}, {"finished", false},
	}
	for _, c := range cases {
		if got := agentAISubagentFrameTerminal(subagentFrame(c.phase)); got != c.want {
			t.Fatalf("phase=%q got=%v want=%v", c.phase, got, c.want)
		}
	}
	if agentAISubagentFrameTerminal(map[string]interface{}{"type": "ai.task", "phase": "completed"}) {
		t.Fatal("非 ai.subagent 帧不得命中")
	}
}
