package services

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// TestClaudeAPIRetrySurfacedAsProgress verifies that a Claude stream-json
// {"type":"system","subtype":"api_retry",...} event is forwarded as
// ai.run.progress (so the phone can render "重试 2/10 · 网关 502") and that the
// last retry is captured for failure enrichment, while a subsequent successful
// turn still streams the assistant text and emits NO ai.error.
func TestClaudeAPIRetrySurfacedAsProgress(t *testing.T) {
	run := agentAIRun{sessionID: "s", messageID: "m", runSeq: 1}
	mu, events, writer := captureAIWriter(t)
	var lastRetry claudeRetryInfo

	input := strings.Join([]string{
		`{"type":"system","subtype":"api_retry","attempt":2,"max_retries":10,"retry_delay_ms":1100,"error_status":502,"error":"server_error"}`,
		`{"type":"assistant","message":{"model":"claude-x","content":[{"type":"text","text":"hi"}]}}`,
		`{"type":"result","subtype":"success","is_error":false,"result":"hi"}`,
	}, "\n")
	streamStructuredAIDelta(strings.NewReader(input), agentAIOutputClaudeStreamJSON, run, writer, &agentAIOutputLimiter{}, nil, nil, &lastRetry, nil)

	progress := findAIEvents(mu, events, "ai.run.progress")
	if len(progress) != 1 {
		t.Fatalf("ai.run.progress count = %d, want 1", len(progress))
	}
	p := progress[0]
	if v, _ := p["retry_attempt"].(int); v != 2 {
		t.Fatalf("retry_attempt = %v, want 2", p["retry_attempt"])
	}
	if v, _ := p["retry_max"].(int); v != 10 {
		t.Fatalf("retry_max = %v, want 10", p["retry_max"])
	}
	if v, _ := p["error_status"].(int); v != 502 {
		t.Fatalf("error_status = %v, want 502", p["error_status"])
	}
	if p["error_type"] != "server_error" {
		t.Fatalf("error_type = %v, want server_error", p["error_type"])
	}
	if p["retry_active"] != true {
		t.Fatalf("retry_active = %v, want true", p["retry_active"])
	}
	if !lastRetry.has || lastRetry.attempt != 2 || lastRetry.errorStatus != 502 {
		t.Fatalf("lastRetry not captured: %+v", lastRetry)
	}
	if len(findAIEvents(mu, events, "ai.error")) != 0 {
		t.Fatalf("did not expect ai.error on retry-then-success")
	}
	if len(findAIEvents(mu, events, "ai.delta")) == 0 {
		t.Fatalf("expected ai.delta for the assistant reply")
	}
}

// TestClaudeAPIRetryFailureEnriched verifies that when retries exhaust, the
// terminal ai.error carries a STRUCTURED cause (error_status + retry counts)
// derived from the last api_retry — not the bare "exit status 1" / raw API
// text — and that the synthetic error message is NOT streamed as an assistant
// delta (which would mask the failure as success).
func TestClaudeAPIRetryFailureEnriched(t *testing.T) {
	run := agentAIRun{sessionID: "s", messageID: "m", runSeq: 1}
	mu, events, writer := captureAIWriter(t)
	var lastRetry claudeRetryInfo

	input := strings.Join([]string{
		`{"type":"system","subtype":"api_retry","attempt":10,"max_retries":10,"retry_delay_ms":32000,"error_status":502,"error":"server_error"}`,
		`{"type":"assistant","message":{"model":"<synthetic>","content":[{"type":"text","text":"API Error: Repeated 529 Overloaded"}]},"error":"server_error"}`,
		`{"type":"result","subtype":"success","is_error":true,"result":"API Error: Repeated 529 Overloaded"}`,
	}, "\n")
	streamStructuredAIDelta(strings.NewReader(input), agentAIOutputClaudeStreamJSON, run, writer, &agentAIOutputLimiter{}, nil, nil, &lastRetry, nil)

	errs := findAIEvents(mu, events, "ai.error")
	if len(errs) != 1 {
		t.Fatalf("ai.error count = %d, want 1", len(errs))
	}
	e := errs[0]
	if v, _ := e["error_status"].(int); v != 502 {
		t.Fatalf("error_status = %v, want 502", e["error_status"])
	}
	if v, _ := e["retry_attempt"].(int); v != 10 {
		t.Fatalf("retry_attempt = %v, want 10", e["retry_attempt"])
	}
	if v, _ := e["retry_max"].(int); v != 10 {
		t.Fatalf("retry_max = %v, want 10", e["retry_max"])
	}
	cause, _ := e["error"].(string)
	if !strings.Contains(cause, "gateway 502") {
		t.Fatalf("error cause = %q, want it to mention gateway 502", cause)
	}
	if !strings.Contains(cause, "10/10") {
		t.Fatalf("error cause = %q, want retry count 10/10", cause)
	}
	detail, _ := e["detail"].(string)
	if !strings.Contains(detail, "API Error") {
		t.Fatalf("detail = %q, want raw result text preserved as detail", detail)
	}
	// The synthetic error message must NOT leak as an assistant delta.
	if len(findAIEvents(mu, events, "ai.delta")) != 0 {
		t.Fatalf("synthetic error must not be streamed as ai.delta")
	}
	// The retry progress must still have been emitted before the failure.
	if len(findAIEvents(mu, events, "ai.run.progress")) != 1 {
		t.Fatalf("expected 1 ai.run.progress before failure, got %d", len(findAIEvents(mu, events, "ai.run.progress")))
	}
}

// TestClaudeSessionIDCapturedFromResultEvent verifies the agent captures
// Claude's assigned session_id from the final "result" event. setAgentAIResume
// SessionIDIfEmpty then pins it on the session so the next turn resumes it via
// --resume → one continuous .jsonl per vibecoding session instead of N fresh
// runs. An error result (is_error) must NOT capture (the stream returns early).
func TestClaudeSessionIDCapturedFromResultEvent(t *testing.T) {
	run := agentAIRun{sessionID: "s", messageID: "m", runSeq: 1}
	mu, events, writer := captureAIWriter(t)
	var lastRetry claudeRetryInfo
	var capturedSessionID string

	input := strings.Join([]string{
		`{"type":"assistant","message":{"model":"claude-x","content":[{"type":"text","text":"hi"}]}}`,
		`{"type":"result","subtype":"success","is_error":false,"session_id":"cl-uuid-123","result":"hi"}`,
	}, "\n")
	streamStructuredAIDelta(strings.NewReader(input), agentAIOutputClaudeStreamJSON, run, writer, &agentAIOutputLimiter{}, nil, nil, &lastRetry, &capturedSessionID)

	if capturedSessionID != "cl-uuid-123" {
		t.Fatalf("capturedSessionID = %q, want cl-uuid-123", capturedSessionID)
	}
	if len(findAIEvents(mu, events, "ai.delta")) == 0 {
		t.Fatalf("expected ai.delta for the assistant reply")
	}
}

func TestClaudeToolUseActivityTracksSubagentWait(t *testing.T) {
	activity := newAgentAIActivity()
	run := agentAIRun{sessionID: "s", messageID: "m", runSeq: 1, activity: activity}
	_, _, writer := captureAIWriter(t)

	input := `{"type":"assistant","message":{"model":"claude-x","content":[{"type":"tool_use","id":"toolu_task_1","name":"Task","input":{"description":"inspect","prompt":"go deep"}}]}}`
	streamStructuredAIDelta(strings.NewReader(input), agentAIOutputClaudeStreamJSON, run, writer, &agentAIOutputLimiter{}, nil, nil, nil, nil)
	if !activity.idlePaused() {
		t.Fatal("Task tool_use should pause the idle watchdog while the subagent is running")
	}

	updateClaudeToolUseActivity(map[string]interface{}{
		"type": "user",
		"message": map[string]interface{}{
			"content": []interface{}{
				map[string]interface{}{"type": "tool_result", "tool_use_id": "toolu_task_1", "content": "done"},
			},
		},
	}, activity, map[string]struct{}{"toolu_task_1": {}})
	if activity.idlePaused() {
		t.Fatal("matching tool_result should re-enable normal idle timeout")
	}
}

// agentCoreRetryOnlyNDJSON 是内核重试期间**唯一**会出现在宿主 stdin 上的帧：
// 上游首尝试失败、内核即将重试。活性验收必须用这一条纯帧——一旦混进
// text_delta，emitAIDelta 的 markProgress 会掩盖 retry 接线的缺失，变异验证
// 就不红了（与 Task B thinking 验收的纯推理流同一道理）。
const agentCoreRetryOnlyNDJSON = `{"type":"system","subtype":"api_retry","attempt":1,"max_retries":3,"retry_delay_ms":500,"error_status":503,"error":"server_error","session_id":"sess-k"}` + "\n"

// agentCoreRetryTurnNDJSON 是 agentcore 内核「首尝试 503、重试成功」一回合的
// **逐字帧序**（retry 帧 + 第二次尝试成功收尾），用于验证转发与终态语义。
const agentCoreRetryTurnNDJSON = agentCoreRetryOnlyNDJSON +
	`{"type":"stream_event","event":{"type":"message_start","message":{"id":"msg_k1","model":"glm-x"}},"session_id":"sess-k"}` + "\n" +
	`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"重试后成功"}}}` + "\n" +
	`{"type":"assistant","message":{"id":"msg_k1","role":"assistant","model":"glm-x","content":[{"type":"text","text":"重试后成功"}]}}` + "\n" +
	`{"type":"result","subtype":"success","result":"重试后成功","session_id":"sess-k","is_error":false}` + "\n"

// TestAgentCoreRetryFrameKeepsRetryingTurnOffStaleScanner 是本任务的验收核心：
// 内核重试期间（首尝试已静默 5.5min、正在退避等下一次尝试），retry 帧到达
// 服务层后必须把活性刷回阈值之内、不被 stale 判死——这正是重试链最坏
// 4×120s + 退避 ≈ 8min 里最危险的一段：此刻没有任何正文，retry 帧是唯一
// 活性信号。对照臂钉住旧内核行为（重试全程零帧 → 5.5min 静默即 turn_stalled），
// 保证健康臂不是扫描器坏掉带来的假绿。
//
// 变异守卫：摘掉 api_retry 分支的 run.activity.markProgress()，健康臂即红。
func TestAgentCoreRetryFrameKeepsRetryingTurnOffStaleScanner(t *testing.T) {
	oldThreshold := agentAIStaleRunThreshold
	agentAIStaleRunThreshold = 5 * time.Minute
	t.Cleanup(func() { agentAIStaleRunThreshold = oldThreshold })

	oldGrace := agentAIKillGrace
	agentAIKillGrace = 30 * time.Millisecond
	t.Cleanup(func() { agentAIKillGrace = oldGrace })

	// 流的 wire 输出走独立丢弃 writer：guard 的 rec 只留给终态帧，否则
	// ai.delta/ai.run.progress 会被误记成终态。
	streamOut := agentTerminalWriter(func(interface{}) error { return nil })

	// ---- 健康臂：纯 retry 帧（无任何正文）必须独立撑住活性 ----
	healthy, _, healthyGuard, healthyRec := newGuardTestManager(t)
	backdateActivityStale(t, healthyGuard.activity)

	streamStructuredAIDelta(
		strings.NewReader(agentCoreRetryOnlyNDJSON),
		agentAIOutputClaudeStreamJSON,
		agentAIRun{sessionID: "sess-1", messageID: "msg-1", runSeq: 1, activity: healthyGuard.activity},
		streamOut, nil, nil, nil, &claudeRetryInfo{}, nil,
	)

	// retry 帧必须已把活性刷回阈值之内：下一次尝试从此刻起还有整段 ≤120s+
	// 退避的合法静默余量，5min 判据不再误伤。
	if idle := healthyGuard.activity.idleFor(); idle >= agentAIStaleRunThreshold {
		t.Fatalf("api_retry frame did not refresh liveness: idleFor=%v >= threshold=%v", idle, agentAIStaleRunThreshold)
	}
	if reason, dead := healthyGuard.activity.staleDeathReason(
		agentAIStaleRunThreshold, agentAIApprovalTimeout, agentAIToolWaitCeiling, agentAISubagentWaitCeiling,
	); dead {
		t.Fatalf("retrying turn judged dead: reason=%q", reason)
	}

	healthy.scanStaleRuns()
	// 扫描周期之外的收敛余量：误杀的话 killReason 同步写、终态帧在宽限期后
	// 出现，sleep 覆盖两者。
	time.Sleep(200 * time.Millisecond)
	if reason := healthyGuard.activity.killReasonOr(""); reason != "" {
		t.Fatalf("retrying turn was killed by stale scanner, reason=%q", reason)
	}
	if got := len(healthyRec.snapshot()); got != 0 {
		t.Fatalf("retrying turn produced %d terminal payloads, want 0", got)
	}

	// ---- 转发臂：内核 retry 帧与 CLI 同权转成 ai.run.progress 上手机 ----
	mu, events, captureOut := captureAIWriter(t)
	streamStructuredAIDelta(
		strings.NewReader(agentCoreRetryTurnNDJSON),
		agentAIOutputClaudeStreamJSON,
		agentAIRun{sessionID: "sess-1", messageID: "msg-1", runSeq: 1, activity: newAgentAIActivity()},
		captureOut, nil, nil, nil, &claudeRetryInfo{}, nil,
	)
	progress := findAIEvents(mu, events, "ai.run.progress")
	if len(progress) != 1 {
		t.Fatalf("ai.run.progress count = %d, want 1（内核 retry 帧应与 CLI 同权转发）", len(progress))
	}
	p := progress[0]
	if v, _ := p["retry_attempt"].(int); v != 1 {
		t.Fatalf("retry_attempt = %v, want 1", p["retry_attempt"])
	}
	if v, _ := p["retry_max"].(int); v != 3 {
		t.Fatalf("retry_max = %v, want 3", p["retry_max"])
	}
	if v, _ := p["error_status"].(int); v != 503 {
		t.Fatalf("error_status = %v, want 503", p["error_status"])
	}
	if p["retry_active"] != true {
		t.Fatalf("retry_active = %v, want true", p["retry_active"])
	}
	if len(findAIEvents(mu, events, "ai.delta")) == 0 {
		t.Fatal("expected ai.delta for the retried-then-successful reply")
	}
	if len(findAIEvents(mu, events, "ai.error")) != 0 {
		t.Fatal("did not expect ai.error on retry-then-success")
	}

	// ---- 对照臂：旧内核重试全程零帧 → 服务层零信号 → 必须判死 ----
	silent, _, silentGuard, silentRec := newGuardTestManager(t)
	backdateActivityStale(t, silentGuard.activity)

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
	t.Fatal("control arm (no retry frames) was not killed — stale scanner broken, the healthy arm proves nothing")
}
