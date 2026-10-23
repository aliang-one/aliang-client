package services

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"aliang.one/nursorgate/app/http/models"
)

func TestAgentAISubagentRegistryTracksIndependentProgress(t *testing.T) {
	base := time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)
	registry := newAgentAISubagentRegistry()
	if _, ok := registry.start("task-a", "tool-a", "", "A", "general", 1, "local_agent", false, base); !ok {
		t.Fatal("start task-a failed")
	}
	if _, ok := registry.start("task-b", "tool-b", "", "B", "general", 1, "local_agent", false, base); !ok {
		t.Fatal("start task-b failed")
	}
	if _, ok := registry.progress("task-a", base.Add(59*time.Minute), agentAISubagentProgress{CurrentStep: "still working"}); !ok {
		t.Fatal("progress task-a failed")
	}

	stalled := registry.stalled(base.Add(61*time.Minute), time.Hour)
	if len(stalled) != 1 || stalled[0].TaskID != "task-b" {
		t.Fatalf("stalled tasks = %#v, want only task-b", stalled)
	}
}

func TestAgentAISubagentRegistryUsesTaskAliasAndKeepsTerminal(t *testing.T) {
	base := time.Now()
	registry := newAgentAISubagentRegistry()
	started, ok := registry.start("task-a", "tool-a", "", "A", "general", 1, "local_agent", false, base)
	if !ok || started.ToolUseID != "tool-a" {
		t.Fatalf("start = %#v, %v", started, ok)
	}
	completed, ok := registry.finish("task-a", "completed", "done", base.Add(time.Second))
	if !ok || completed.Status != "completed" {
		t.Fatalf("finish by task alias = %#v, %v", completed, ok)
	}
	if _, ok := registry.progress("tool-a", base.Add(2*time.Second), agentAISubagentProgress{CurrentStep: "late"}); ok {
		t.Fatal("late progress must not reopen a terminal subagent")
	}
	if got := registry.activeCount(); got != 0 {
		t.Fatalf("activeCount = %d, want 0", got)
	}
}

func TestAgentAISubagentRegistryCapsEntriesWithoutEvictingActiveTasks(t *testing.T) {
	base := time.Now()
	registry := newAgentAISubagentRegistry()
	for i := 0; i < agentAISubagentRegistryLimit; i++ {
		id := fmt.Sprintf("task-%d", i)
		if _, ok := registry.start(id, "tool-"+id, "", id, "general", 1, "local_agent", false, base.Add(time.Duration(i))); !ok {
			t.Fatalf("start %s failed before cap", id)
		}
	}
	if _, ok := registry.start("overflow", "tool-overflow", "", "overflow", "general", 1, "local_agent", false, base); ok {
		t.Fatal("registry accepted an entry past the cap while all entries are active")
	}
	if _, ok := registry.finish("task-0", "completed", "done", base.Add(time.Hour)); !ok {
		t.Fatal("finish task-0 failed")
	}
	if _, ok := registry.start("replacement", "tool-replacement", "", "replacement", "general", 1, "local_agent", false, base.Add(2*time.Hour)); !ok {
		t.Fatal("registry should evict a terminal entry for a new task")
	}
	if got := registry.count(); got != agentAISubagentRegistryLimit {
		t.Fatalf("count = %d, want cap %d", got, agentAISubagentRegistryLimit)
	}
}

func TestAgentAISubagentRegistryTracksContinuousWaitCeiling(t *testing.T) {
	base := time.Now()
	registry := newAgentAISubagentRegistry()
	_, _ = registry.start("task-a", "tool-a", "", "A", "general", 1, "local_agent", false, base)
	_, _ = registry.start("task-b", "tool-b", "", "B", "general", 1, "local_agent", false, base.Add(time.Minute))
	if got := registry.waitFor(base.Add(90 * time.Minute)); got != 90*time.Minute {
		t.Fatalf("waitFor = %v, want 90m from first active task", got)
	}
	_, _ = registry.finish("task-a", "completed", "done", base.Add(91*time.Minute))
	if got := registry.waitFor(base.Add(92 * time.Minute)); got != 92*time.Minute {
		t.Fatalf("waitFor = %v, want continuous wait start preserved while task-b remains active", got)
	}
	_, _ = registry.finish("task-b", "completed", "done", base.Add(93*time.Minute))
	if got := registry.waitFor(base.Add(94 * time.Minute)); got != 0 {
		t.Fatalf("waitFor = %v after all tasks finished, want 0", got)
	}
}

func TestAgentAISubagentRegistryPrunesOrderOnEviction(t *testing.T) {
	base := time.Now()
	registry := newAgentAISubagentRegistry()
	// 3×cap 个顺序 start→finish 循环:逐个驱逐最旧终态条目。order 必须同步
	// 修剪,否则 finalize/stalled 拖着死 id 无限增长。
	for i := 0; i < agentAISubagentRegistryLimit*3; i++ {
		id := fmt.Sprintf("task-%d", i)
		if _, ok := registry.start(id, "tool-"+id, "", id, "general", 1, "local_agent", false, base); !ok {
			t.Fatalf("start %s failed", id)
		}
		if _, ok := registry.finish(id, "completed", "", base); !ok {
			t.Fatalf("finish %s failed", id)
		}
	}
	if got := registry.count(); got != agentAISubagentRegistryLimit {
		t.Fatalf("count = %d, want cap %d", got, agentAISubagentRegistryLimit)
	}
	// 最新一条终态记录:别名必须仍可解析(progress 返回 record 但不重开,
	// ok=false 才对——别名若被误删,resolveLocked 会直接 !ok 且 record 为空)。
	if record, ok := registry.progress(fmt.Sprintf("task-%d", agentAISubagentRegistryLimit*3-1), base, agentAISubagentProgress{}); ok || record.TaskID == "" {
		t.Fatalf("terminal alias must resolve without reopening: record=%#v ok=%v", record, ok)
	}
}

func TestAgentAISubagentRegistryLegacyFlow(t *testing.T) {
	base := time.Now()
	registry := newAgentAISubagentRegistry()
	if _, ok := registry.startLegacy("call_legacy", "msg-1", "review", "general", base); !ok {
		t.Fatal("startLegacy failed")
	}
	if !registry.legacyStarted("call_legacy") {
		t.Fatal("legacy record should report legacyStarted")
	}
	// 真 task_started 到达(新 CC):同 toolUseID 覆盖登记 → legacy 清零、TaskID 换真值。
	record, ok := registry.start("task-real", "call_legacy", "msg-1", "review", "general", 1, "local_agent", false, base)
	if !ok || record.TaskID != "task-real" || record.Legacy {
		t.Fatalf("re-anchor = %#v, %v; want real task id and legacy cleared", record, ok)
	}
	if registry.legacyStarted("call_legacy") {
		t.Fatal("legacy flag must clear once real frames re-anchor the record")
	}
}

func TestHandleClaudeTaskEventsEmitsLifecycle(t *testing.T) {
	run := agentAIRun{sessionID: "s1", messageID: "m1", activity: newAgentAIActivity(), subagents: newAgentAISubagentRegistry()}
	var mu sync.Mutex
	var payloads []map[string]interface{}
	write := func(v interface{}) error {
		mu.Lock()
		defer mu.Unlock()
		if p, ok := v.(map[string]interface{}); ok {
			payloads = append(payloads, p)
		}
		return nil
	}
	frames := []string{
		`{"type":"system","subtype":"task_started","task_id":"a39e08a134a58eb09","tool_use_id":"call_468e","description":"dir-counter","subagent_type":"general-purpose","is_backgrounded":false,"spawn_depth":1,"task_type":"local_agent"}`,
		`{"type":"system","subtype":"task_progress","task_id":"a39e08a134a58eb09","tool_use_id":"call_468e","description":"Running Count entries in /etc","subagent_type":"general-purpose","usage":{"total_tokens":1,"tool_uses":2,"duration_ms":15160},"last_tool_name":"Bash"}`,
		`{"type":"system","subtype":"task_updated","task_id":"a39e08a134a58eb09","patch":{"status":"completed","end_time":1792682032295}}`,
		`{"type":"system","subtype":"task_notification","task_id":"a39e08a134a58eb09","tool_use_id":"call_468e","status":"completed","output_file":"/tmp/x.output","summary":"Both counts done.","usage":{"total_tokens":9,"tool_uses":2,"duration_ms":15161}}`,
	}
	for _, frame := range frames {
		var event map[string]interface{}
		if err := json.Unmarshal([]byte(frame), &event); err != nil {
			t.Fatalf("bad frame: %v", err)
		}
		handleClaudeTaskEvents(event, run, write)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(payloads) != 4 {
		t.Fatalf("payloads = %d, want 4 (started/progress/updated/notification)", len(payloads))
	}
	wantPhases := []string{"started", "progress", "completed", "completed"}
	for i, payload := range payloads {
		if payload["type"] != models.AgentEventAISubagent {
			t.Fatalf("payload[%d] type = %v", i, payload["type"])
		}
		if payload["task_id"] != "a39e08a134a58eb09" {
			t.Fatalf("payload[%d] task_id = %v", i, payload["task_id"])
		}
		if got := payload["phase"]; got != wantPhases[i] {
			t.Fatalf("payload[%d] phase = %v, want %s", i, got, wantPhases[i])
		}
	}
	if got := run.activity.activeSubagents.Load(); got != 0 {
		t.Fatalf("activeSubagents = %d after terminal frames, want 0", got)
	}
	last := payloads[3]
	if last["summary"] != "Both counts done." || last["output_file"] != "/tmp/x.output" {
		t.Fatalf("notification enrichment missing: %#v", last)
	}
}

func TestClaudeParentedFramesAreGated(t *testing.T) {
	// ① 文本提取:parented assistant / stream_event 都不得产文本。
	if texts := extractClaudeStreamTexts(map[string]interface{}{
		"parent_tool_use_id": "call_468e", "type": "assistant",
		"message": map[string]interface{}{"content": []interface{}{map[string]interface{}{"type": "text", "text": "leak"}}},
	}); texts.finalText != "" || len(texts.deltas) != 0 {
		t.Fatalf("parented assistant text leaked: %#v", texts)
	}
	if texts := extractClaudeStreamTexts(map[string]interface{}{
		"parent_tool_use_id": "call_468e", "type": "stream_event",
		"event": map[string]interface{}{"type": "content_block_delta", "delta": map[string]interface{}{"type": "text_delta", "text": "leak"}},
	}); len(texts.deltas) != 0 {
		t.Fatalf("parented delta leaked: %#v", texts)
	}
	// ② 结构化发射:parented assistant tool_use 不产任何事件。
	run := agentAIRun{sessionID: "s1", messageID: "m1", activity: newAgentAIActivity(), subagents: newAgentAISubagentRegistry()}
	var writes int
	write := func(v interface{}) error { writes++; return nil }
	emitClaudeStructuredEvents(agentAIOutputClaudeStreamJSON, map[string]interface{}{
		"parent_tool_use_id": "call_468e", "type": "assistant",
		"message": map[string]interface{}{"content": []interface{}{map[string]interface{}{"type": "tool_use", "id": "call_inner", "name": "Bash", "input": map[string]interface{}{"command": "ls"}}}},
	}, run, write, map[string]string{}, map[int]string{})
	if writes != 0 {
		t.Fatalf("parented tool_use emitted %d events, want 0", writes)
	}
	// ③ 看门狗配对:parented tool_use 不得武装 pendingToolUses。
	activity := newAgentAIActivity()
	pending := map[string]struct{}{}
	updateClaudeToolUseActivity(map[string]interface{}{
		"parent_tool_use_id": "call_468e", "type": "assistant",
		"message": map[string]interface{}{"content": []interface{}{map[string]interface{}{"type": "tool_use", "id": "call_inner"}}},
	}, activity, pending)
	if len(pending) != 0 || activity.pendingToolUses.Load() != 0 {
		t.Fatalf("parented tool_use armed the wait: pending=%v count=%d", pending, activity.pendingToolUses.Load())
	}
	// 父会话自身帧(parent null)不受门影响。
	updateClaudeToolUseActivity(map[string]interface{}{
		"type":    "assistant",
		"message": map[string]interface{}{"content": []interface{}{map[string]interface{}{"type": "tool_use", "id": "call_parent"}}},
	}, activity, pending)
	if activity.pendingToolUses.Load() != 1 {
		t.Fatalf("parent tool_use lost: count=%d", activity.pendingToolUses.Load())
	}
}

func TestSubagentProgressResetsToolWaitClock(t *testing.T) {
	activity := newAgentAIActivity()
	activity.beginToolUseWait()
	// 把连续等待钟拨回 2h 前,再喂一次子 agent 进度。
	activity.toolWaitStartedAt.Store(time.Now().Add(-2 * time.Hour).UnixNano())
	activity.subagentProgress()
	if got := activity.toolWaitFor(); got > time.Minute {
		t.Fatalf("toolWaitFor = %v after progress, want reset (small)", got)
	}
	if activity.lastProgressAt.Load() == 0 {
		t.Fatal("progress must count as meaningful progress")
	}
}

func TestAgentAIWatchdogSubagentCeiling(t *testing.T) {
	activity := newAgentAIActivity()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan string, 1)
	go func() {
		agentAIWatchdogLoop(ctx, activity, cancel, time.Hour /*idle*/, 0 /*hard*/, 0 /*noProgress*/, 0 /*toolWait*/, 20*time.Millisecond /*subagent*/, 2*time.Millisecond)
		done <- "loop-exited"
	}()
	activity.beginSubagent() // 活跃子 agent:idle/no-progress 暂停,只有本上限能杀
	select {
	case <-done:
		// loop 退出 = 触发 subagent_wait_timeout
	case <-time.After(2 * time.Second):
		t.Fatal("subagent ceiling did not fire within 2s")
	}
	if got := activity.killReasonOr(""); got != "subagent_wait_timeout" {
		t.Fatalf("killReason = %q, want subagent_wait_timeout", got)
	}
}

func TestFinalizeRunSubagentsEmitsInterrupted(t *testing.T) {
	run := agentAIRun{sessionID: "s1", messageID: "m1", activity: newAgentAIActivity(), subagents: newAgentAISubagentRegistry()}
	if _, ok := run.subagents.start("task-a", "tool-a", "msg-1", "A", "general", 1, "local_agent", false, time.Now()); !ok {
		t.Fatal("start failed")
	}
	var payloads []map[string]interface{}
	write := func(v interface{}) error {
		if p, ok := v.(map[string]interface{}); ok {
			payloads = append(payloads, p)
		}
		return nil
	}
	finalizeRunSubagents(run, write)
	if len(payloads) != 1 {
		t.Fatalf("payloads = %d, want 1", len(payloads))
	}
	if payloads[0]["phase"] != "interrupted" {
		t.Fatalf("phase = %v, want interrupted", payloads[0]["phase"])
	}
	if again := run.subagents.finalize(time.Now()); len(again) != 0 {
		t.Fatalf("finalize must be idempotent, got %d records", len(again))
	}
}
