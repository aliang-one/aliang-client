package services

// 子 agent 终态帧重发测试（2026-11-10）。
//
// 背景（17-agent 全链审计 CONFIRMED P2）：整回合停止/看门狗击杀时，
// newAgentCoreEmit 的 ctx 守卫先关 io.Pipe → 桥 defer 里 mirror.finish 的
// emit 撞上关闭的管道被吞 → 终态帧永久丢失；而宿主补偿路径
// finalizeRunSubagents 只补「仍 active」的记录，补不了已被桥翻成终态但帧没
// 出去的行 → 服务端/手机卡死在 started/progress 永转 spinner。
//
// 修复契约：mirror.emitRecord 返回 emit 错误；finish 遇错置位
// registry.markTerminalUnsent；finalizeRunSubagents 经 drainUnsentTerminals
// 在 writeJSON 仍活的收尾路径补发——恰一帧、相位为已定终态、幂等（重复收尾
// 不重发）、activity 不受影响（mirror.finish 已配平）。

import (
	"errors"
	"sync"
	"testing"
	"time"
)

type deadPipeEmit struct {
	mu    sync.Mutex
	calls int
}

func (e *deadPipeEmit) emit([]byte) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls++
	return errors.New("io: read/write on closed pipe")
}

func (e *deadPipeEmit) count() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls
}

// TestMirrorFinishMarksTerminalUnsentOnEmitError：终态帧 emit 失败 → registry
// 置位重发标记；drain 取走恰一帧并清标记（幂等）。start 帧丢失不算数（finalize
// 的 active 口径自会兜），只有终态行置位。
func TestMirrorFinishMarksTerminalUnsentOnEmitError(t *testing.T) {
	dead := &deadPipeEmit{}
	mirror := newMirrorForTest(t, dead.emit)

	taskID, tracked := mirror.start("researcher", "调研", "任务")
	if !tracked || taskID == "" {
		t.Fatalf("start 未追踪: %q %v", taskID, tracked)
	}
	// start 帧已丢（emit 失败被吞）——此刻不得置位（记录仍 active）。
	if got := mirror.registry.drainUnsentTerminals(); len(got) != 0 {
		t.Fatalf("active 行不得置位重发: %v", got)
	}

	mirror.finish("researcher", taskID, "completed", "done", 10*time.Millisecond, 165)

	if dead.count() < 2 {
		t.Fatalf("start+finish 至少两次 emit 尝试: %d", dead.count())
	}
	drained := mirror.registry.drainUnsentTerminals()
	if len(drained) != 1 {
		t.Fatalf("drainUnsentTerminals = %d 条, want 1: %v", len(drained), drained)
	}
	if drained[0].TaskID != taskID || drained[0].Status != "completed" {
		t.Fatalf("重发记录错误: taskID=%q status=%q", drained[0].TaskID, drained[0].Status)
	}
	// 幂等:第二次 drain 必须为空。
	if again := mirror.registry.drainUnsentTerminals(); len(again) != 0 {
		t.Fatalf("drain 必须幂等, 又取到 %d 条", len(again))
	}
	// activity 已在 finish 内配平,重发不得再动它。
	if got := mirror.activity.activeSubagents.Load(); got != 0 {
		t.Fatalf("activeSubagents = %d, want 0", got)
	}
}

// TestFinalizeRunSubagentsResendsUnsentTerminal：整链复刻事故序列——emit 管道
// 死透 → 桥 finish 帧被吞 →（宿主收尾路径,writeJSON 仍活）finalizeRunSubagents
// 必须补发恰一帧终态,且与 active 翻转互不重复。
func TestFinalizeRunSubagentsResendsUnsentTerminal(t *testing.T) {
	dead := &deadPipeEmit{}
	mirror := newMirrorForTest(t, dead.emit)
	run := agentAIRun{sessionID: "s1", messageID: "m1", activity: mirror.activity, subagents: mirror.registry}

	taskID, tracked := mirror.start("researcher", "调研", "任务")
	if !tracked {
		t.Fatalf("start 未追踪")
	}
	mirror.finish("researcher", taskID, "completed", "done", 10*time.Millisecond, 0)

	var payloads []map[string]interface{}
	write := func(v interface{}) error {
		if p, ok := v.(map[string]interface{}); ok {
			payloads = append(payloads, p)
		}
		return nil
	}
	finalizeRunSubagents(run, write)

	if len(payloads) != 1 {
		t.Fatalf("补发帧数 = %d, want 1（已终态行不得经 active 口径重复发）: %v", len(payloads), payloads)
	}
	if payloads[0]["type"] != "ai.subagent" || payloads[0]["phase"] != "completed" {
		t.Fatalf("补发帧错误: %v", payloads[0])
	}
	if tid, _ := payloads[0]["task_id"].(string); tid != taskID {
		t.Fatalf("补发帧 task_id = %v, want %q", payloads[0]["task_id"], taskID)
	}
	// 幂等:重复收尾零补发。
	payloads = payloads[:0]
	finalizeRunSubagents(run, write)
	if len(payloads) != 0 {
		t.Fatalf("重复收尾不得重发: %v", payloads)
	}
}

// TestRegistryMarkTerminalUnsentGuards：置位守卫——未知 id / 仍 active 的记录
// 一律 no-op;正常终态才置位。
func TestRegistryMarkTerminalUnsentGuards(t *testing.T) {
	reg := newAgentAISubagentRegistry()

	reg.markTerminalUnsent("unknown") // 未知 id:不得炸不得置位
	if got := reg.drainUnsentTerminals(); len(got) != 0 {
		t.Fatalf("未知 id 置位泄漏: %v", got)
	}

	rec, ok := reg.start("t1", "tool-1", "m", "d", "general", 1, "", false, time.Now())
	if !ok {
		t.Fatal("start failed")
	}
	reg.markTerminalUnsent(rec.TaskID) // 仍 active:no-op
	if got := reg.drainUnsentTerminals(); len(got) != 0 {
		t.Fatalf("active 行被误置位: %v", got)
	}

	if _, ok := reg.finish(rec.TaskID, "failed", "", time.Now()); !ok {
		t.Fatal("finish failed")
	}
	reg.markTerminalUnsent(rec.TaskID)
	got := reg.drainUnsentTerminals()
	if len(got) != 1 || got[0].Status != "failed" {
		t.Fatalf("终态行置位失败: %v", got)
	}
}
