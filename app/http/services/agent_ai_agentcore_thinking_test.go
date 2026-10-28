package services

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"aliang.one/nursorgate/app/http/models"
)

// agentCoreThinkingStreamNDJSON 是 agentcore 内核 streamWriter 对「先纯思考、
// 后短回答」一回合发出的**逐字帧序**（agentcore/stream.go 的
// ThinkingBlockStart/ThinkingDelta + 既有 text 路径）：index 0 是 thinking 块
// （start / 两段 delta / stop），index 1 是 text 块，finalized assistant 帧
// 只带 text 块。服务层要把 thinking 流翻成 ai.thinking 并刷新活性，同时不得
// 让思考内容进正文。
const agentCoreThinkingStreamNDJSON = `{"type":"stream_event","event":{"type":"message_start","message":{"id":"msg_k1","model":"glm-x"}},"session_id":"sess-k"}` + "\n" +
	`{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}}` + "\n" +
	`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"我在想"}}}` + "\n" +
	`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"这个问题…"}}}` + "\n" +
	`{"type":"stream_event","event":{"type":"content_block_stop","index":0}}` + "\n" +
	`{"type":"stream_event","event":{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}}` + "\n" +
	`{"type":"stream_event","event":{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"答案"}}}` + "\n" +
	`{"type":"stream_event","event":{"type":"content_block_stop","index":1}}` + "\n" +
	`{"type":"assistant","message":{"id":"msg_k1","role":"assistant","model":"glm-x","content":[{"type":"text","text":"答案"}]}}` + "\n" +
	`{"type":"result","subtype":"success","result":"答案","session_id":"sess-k","is_error":false}` + "\n"

// agentCorePureThinkingNDJSON 是纯推理段（无任何文本）的内核帧序。活性验收
// 必须用这一条：一旦混进 text_delta，emitAIDelta 的 markProgress 会掩盖
// thinking 接线的缺失，变异验证就不红了。
const agentCorePureThinkingNDJSON = `{"type":"stream_event","event":{"type":"message_start","message":{"id":"msg_k1","model":"glm-x"}},"session_id":"sess-k"}` + "\n" +
	`{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}}` + "\n" +
	`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"我在想"}}}` + "\n" +
	`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"这个问题…"}}}` + "\n" +
	`{"type":"stream_event","event":{"type":"content_block_stop","index":0}}` + "\n"

// backdateActivityStale 把活性时钟回拨到 stale 阈值之外，模拟「纯推理开始前
// 已经静默了很久」——此后的生死完全取决于 thinking 流能不能刷新活性。
func backdateActivityStale(t *testing.T, a *agentAIActivity) {
	t.Helper()
	stale := time.Now().Add(-6 * time.Minute).UnixNano()
	a.lastActivityAt.Store(stale)
	a.lastProgressAt.Store(stale)
}

// TestAgentCoreThinkingFrameKeepsPureReasoningOffStaleScanner 是本任务的验收
// 核心：内核把 thinking_delta 透传成帧之后，服务层 decoder 必须 markProgress
// ——纯思考 5-6 分钟的回合不再被 L3 stale 扫描器判死。对照臂钉住旧行为
// （内核丢帧 → 服务层零信号 → 6 分钟静默即 turn_stalled）。
func TestAgentCoreThinkingFrameKeepsPureReasoningOffStaleScanner(t *testing.T) {
	oldThreshold := agentAIStaleRunThreshold
	agentAIStaleRunThreshold = 5 * time.Minute
	t.Cleanup(func() { agentAIStaleRunThreshold = oldThreshold })

	oldGrace := agentAIKillGrace
	agentAIKillGrace = 30 * time.Millisecond
	t.Cleanup(func() { agentAIKillGrace = oldGrace })

	// ---- 健康臂：thinking 帧到达服务层 ----
	healthy, _, healthyGuard, healthyRec := newGuardTestManager(t)
	backdateActivityStale(t, healthyGuard.activity)

	// 流的 wire 输出走独立的丢弃 writer：rec 只留给 guard 终态帧，
	// 否则 ai.delta/ai.thinking 会被误记成终态。
	streamOut := agentTerminalWriter(func(interface{}) error { return nil })
	run := agentAIRun{sessionID: "sess-1", messageID: "msg-1", runSeq: 1, activity: healthyGuard.activity}
	// 纯推理流：全程无 text_delta，活性只能来自 thinking 接线本身。
	streamStructuredAIDelta(
		bytes.NewReader([]byte(agentCorePureThinkingNDJSON)),
		agentAIOutputClaudeStreamJSON,
		run, streamOut, nil, nil, nil, &claudeRetryInfo{}, nil,
	)

	// thinking_delta（以及其后的 text_delta）必须已经把活性刷到阈值之内。
	if idle := healthyGuard.activity.idleFor(); idle >= agentAIStaleRunThreshold {
		t.Fatalf("thinking frames did not refresh liveness: idleFor=%v >= threshold=%v", idle, agentAIStaleRunThreshold)
	}
	if reason, dead := healthyGuard.activity.staleDeathReason(
		agentAIStaleRunThreshold, agentAIApprovalTimeout, agentAIToolWaitCeiling, agentAISubagentWaitCeiling,
	); dead {
		t.Fatalf("healthy pure-thinking run judged dead: reason=%q", reason)
	}

	healthy.scanStaleRuns()
	// 扫描周期之外的收敛余量：误杀的话 killReason 同步写、终态帧在宽限期后
	// 出现，sleep 覆盖两者。
	time.Sleep(200 * time.Millisecond)
	if reason := healthyGuard.activity.killReasonOr(""); reason != "" {
		t.Fatalf("pure-thinking run was killed by stale scanner, reason=%q", reason)
	}
	if got := len(healthyRec.snapshot()); got != 0 {
		t.Fatalf("pure-thinking run produced %d terminal payloads, want 0", got)
	}

	// ---- 对照臂：内核丢帧（旧行为）→ 服务层零信号 ----
	silent, _, silentGuard, silentRec := newGuardTestManager(t)
	backdateActivityStale(t, silentGuard.activity)

	// 旧内核在纯推理期间一个帧都不发：驱动一个空流等价于「什么都没到」。
	streamStructuredAIDelta(
		bytes.NewReader(nil),
		agentAIOutputClaudeStreamJSON,
		agentAIRun{sessionID: "sess-1", messageID: "msg-1", runSeq: 1, activity: silentGuard.activity},
		streamOut, nil, nil, nil, &claudeRetryInfo{}, nil,
	)

	silent.scanStaleRuns()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if payloads := silentRec.snapshot(); len(payloads) == 1 {
			if got := payloads[0]["error_code"]; got != "turn_stalled" {
				t.Fatalf("control arm error_code = %v, want turn_stalled", got)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("control arm (no frames) was not killed — stale scanner broken, the healthy arm proves nothing")
}

// TestAgentCoreThinkingStreamStaysOutOfTranscript 锁两条契约：
//  1. 内核 thinking 帧被翻成既有 ai.thinking 协议帧（带 delta / item_id /
//     active:false 收口），思考文本绝不混进 ai.delta 正文（capture 即落库
//     正文来源，同样不得出现）；
//  2. thinking 流算实打实的进展——lastProgressAt 必须被刷新（与 CLI 路径
//     同一判据，这条是「有输出了」假完成判定的反义：thinking 不触发任何
//     终态帧）。
func TestAgentCoreThinkingStreamStaysOutOfTranscript(t *testing.T) {
	var captured strings.Builder
	var events []map[string]interface{}
	writeJSON := agentTerminalWriter(func(v interface{}) error {
		if ev, ok := v.(map[string]interface{}); ok {
			events = append(events, ev)
		}
		return nil
	})
	run := agentAIRun{sessionID: "sess-k", messageID: "m", runSeq: 1, activity: newAgentAIActivity()}

	streamStructuredAIDelta(
		bytes.NewReader([]byte(agentCoreThinkingStreamNDJSON)),
		agentAIOutputClaudeStreamJSON,
		run, writeJSON, nil,
		func(text string) { captured.WriteString(text) }, // capture = 落库正文来源
		nil, &claudeRetryInfo{}, nil,
	)

	// 思考文本只走 ai.thinking，绝不进 ai.delta、绝不进 capture（正文）。
	var deltaText strings.Builder
	thinkingDeltas := 0
	thinkingActiveFalse := 0
	thinkingMsgIDs := map[string]bool{}
	for _, ev := range events {
		switch remoteString(ev, "type") {
		case models.AgentEventAIDelta:
			deltaText.WriteString(remoteString(ev, "delta"))
		case models.AgentEventAIThinking:
			thinkingMsgIDs[remoteString(ev, "message_id")] = true
			if d := remoteString(ev, "delta"); d != "" {
				thinkingDeltas++
				if !strings.Contains("我在想这个问题…", d) {
					t.Fatalf("ai.thinking delta = %q, want a fragment of the kernel thinking stream", d)
				}
				if id := remoteString(ev, "item_id"); id != "tb0" {
					t.Fatalf("ai.thinking item_id = %q, want tb0", id)
				}
			}
			if active, ok := ev["active"].(bool); ok && !active {
				thinkingActiveFalse++
			}
		}
	}
	if thinkingDeltas != 2 {
		t.Fatalf("ai.thinking delta events = %d, want 2 (one per thinking_delta)", thinkingDeltas)
	}
	if thinkingActiveFalse != 1 {
		t.Fatalf("ai.thinking active:false events = %d, want 1 (content_block_stop of the thinking block)", thinkingActiveFalse)
	}
	if !thinkingMsgIDs["msg_k1"] {
		t.Fatalf("ai.thinking message_ids = %v, want msg_k1 (per-turn native id, same as CLI path)", thinkingMsgIDs)
	}
	if got := deltaText.String(); got != "答案" {
		t.Fatalf("ai.delta text = %q, want exactly 答案 — thinking must never leak into the prose channel", got)
	}
	if got := captured.String(); got != "答案" {
		t.Fatalf("capture (transcript body) = %q, want exactly 答案 — thinking must never be persisted", got)
	}

	// 活性判据：thinking 流与 CLI 路径同一口径刷新 lastProgressAt。用纯推理
	// 流单测——全量流里的 text_delta 也会刷进度，混在一起测不出 thinking。
	pureRun := agentAIRun{sessionID: "sess-k", messageID: "m", runSeq: 1, activity: newAgentAIActivity()}
	pureBefore := pureRun.activity.lastProgressAt.Load()
	streamStructuredAIDelta(
		bytes.NewReader([]byte(agentCorePureThinkingNDJSON)),
		agentAIOutputClaudeStreamJSON,
		pureRun, agentTerminalWriter(func(interface{}) error { return nil }),
		nil, nil, nil, &claudeRetryInfo{}, nil,
	)
	if after := pureRun.activity.lastProgressAt.Load(); after == pureBefore {
		t.Fatal("pure thinking stream did not refresh lastProgressAt — pure reasoning would stall the run")
	}

	// 反假完成：thinking 流自身不产生任何终态帧（ai.error / result 类）。
	for _, ev := range events {
		switch remoteString(ev, "type") {
		case models.AgentEventAIError:
			t.Fatalf("thinking stream produced a terminal ai.error: %v", ev)
		}
	}
}
