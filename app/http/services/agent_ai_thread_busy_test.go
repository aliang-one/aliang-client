package services

// codex 线程单写者冲突「询问式 fork」处置的单元测试：分类器对生产原文与
// -32600 干扰项的判别、confirmBindingFork 的 fork 容忍、P0 终态的 error_code
// 通道、询问三态（作答/超时/默认不 fork）与 cancel 翻卡帧。

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"aliang.one/nursorgate/app/http/models"
)

// 生产案发现场逐字原文（2026-11-25 run_RUzC5DhxzsWxdwux,ai_sessions.turn_pipeline）。
const codexWriterConflictVerbatim = "Codex app-server thread/resume failed: thread 01a2111f-3b14-7091-98db-e2cae393a4c7 already has an active writer (-32600); codex stderr: ... 2026-11-25T12:38:55.350594Z ERROR codex_core::session::session: failed to initialize thread persistence: thread-store conflict: thread 01a2111f-3b14-7091-98db-e2cae393a4c7 already has an active writer"

func TestIsAgentAICodexWriterConflict(t *testing.T) {
	if !isAgentAICodexWriterConflict(codexWriterConflictVerbatim) {
		t.Fatalf("verbatim production writer-conflict text must classify")
	}
	if !isAgentAICodexWriterConflict("thread-store conflict: thread x already has an active writer") {
		t.Fatalf("bare conflict text must classify")
	}
	// -32600 被多种错误复用（2026-11-25 daemon 实测），文本分类不得误报：
	for _, distractor := range []string{
		"no rollout found for thread id 01a212f7",
		"Invalid request: missing field `threadId`",
		"thread already has an active or pending turn",
		"No conversation found with session ID xyz",
	} {
		if isAgentAICodexWriterConflict(distractor) {
			t.Fatalf("distractor %q must NOT classify as writer conflict", distractor)
		}
	}
}

func TestAgentAICodexThreadBusyErrorCode(t *testing.T) {
	err := &agentAICodexThreadBusyError{msg: "codex thread busy: ..."}
	if err.ErrorCode() != "codex_thread_busy" {
		t.Fatalf("ErrorCode = %q, want codex_thread_busy", err.ErrorCode())
	}
	payload := agentAIErrorPayload("sess", "msg", err)
	if got, _ := payload["error_code"].(string); got != "codex_thread_busy" {
		t.Fatalf("payload error_code = %v, want codex_thread_busy", payload["error_code"])
	}
	if text, _ := payload["error"].(string); !strings.HasPrefix(text, "codex thread busy:") {
		t.Fatalf("payload error text = %q, want stable prefix", text)
	}
}

func TestConfirmBindingForkTolerance(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := newAgentAIManager()
	defer m.closeAll()
	m.bindings["sess-fork"] = agentAIBindingRecord{
		ConversationID:  "sess-fork",
		Provider:        "codex",
		NativeSessionID: "old-thread",
		State:           "confirmed",
		BindingVersion:  3,
	}

	// fork 容忍：expected == fork 源 → 放行换绑到新线程（confirmed 锚前移）。
	record, err := m.confirmBindingFork("sess-fork", "codex", "new-thread", 3, "old-thread")
	if err != nil {
		t.Fatalf("fork rebind must be tolerated: %v", err)
	}
	if record.NativeSessionID != "new-thread" || record.State != "confirmed" {
		t.Fatalf("record = %+v, want new-thread/confirmed", record)
	}

	// fork 容忍不扩散：expected != fork 源 → 仍然 mismatch。
	if _, err := m.confirmBindingFork("sess-fork", "codex", "another-thread", 3, "unrelated-source"); err == nil {
		t.Fatalf("rebind with unrelated fork source must fail")
	}

	// 无 fork 语义（原 confirmBinding 路径）→ mismatch 行为不变。
	if _, err := m.confirmBinding("sess-fork", "codex", "yet-another-thread", 3); err == nil {
		t.Fatalf("plain confirmBinding must still reject mismatch")
	}
}

// 询问三态：作答 fork / 超时 cancel / 作答默认不 fork。writeJSON 用
// capturedWriter 捕获 tool_ask.request 并在应答侧手动 deliver。

func newThreadBusyTestRun() agentAIRun {
	return agentAIRun{
		sessionID:       "sess-tb",
		runID:           "run-tb-1",
		messageID:       "msg-tb-1",
		runSeq:          1,
		resumeSessionID: "busy-thread",
		activity:        newAgentAIActivity(),
	}
}

func TestAskAgentAIThreadBusyForkAnsweredFork(t *testing.T) {
	m := newAgentAIManager()
	defer m.closeAll()
	w := newCapturedWriter()
	run := newThreadBusyTestRun()

	done := make(chan struct{})
	var choice string
	var delivered bool
	go func() {
		defer close(done)
		choice, delivered = m.askAgentAIThreadBusyFork(context.Background(), &run, w.write, "busy-thread")
	}()

	frames := w.waitForFrames(1, 5*time.Second)
	if len(frames) == 0 || frames[0]["type"] != models.AgentEventToolAskRequest {
		t.Fatalf("tool_ask.request frame missing: %+v", frames)
	}
	req := frames[0]
	if remoteString(req, "session_id") != "sess-tb" || remoteString(req, "run_id") != "run-tb-1" {
		t.Fatalf("request frame addressing wrong: %+v", req)
	}
	interactionID := remoteString(req, "interaction_id")
	if interactionID == "" {
		t.Fatalf("interaction_id empty")
	}
	input, _ := req["tool_input"].(json.RawMessage)
	if input == nil || !strings.Contains(string(input), agentAIThreadBusyOptionFork) || !strings.Contains(string(input), agentAIThreadBusyOptionClose) {
		t.Fatalf("tool_input options missing: %s", string(input))
	}

	// 手机作答 fork_continue。
	m.ensureToolAskBridge().deliver(models.AgentToolAskResponse{
		InteractionID: interactionID,
		Status:        models.AgentToolAskStatusAnswered,
		Answer:        &models.AgentToolAskAnswer{SelectedOptionID: agentAIThreadBusyOptionFork},
	})
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("ask did not return")
	}
	if !delivered || choice != agentAIThreadBusyOptionFork {
		t.Fatalf("choice=%q delivered=%v, want fork_continue/true", choice, delivered)
	}
	// 作答落地必须广播 resolved 同型帧。
	resolved := false
	for _, f := range w.frames() {
		if f["type"] == models.AgentEventToolAskRequest && f["status"] == "resolved" {
			resolved = true
		}
	}
	if !resolved {
		t.Fatalf("resolved broadcast frame missing")
	}
}

func TestAskAgentAIThreadBusyForkAnsweredDefaultNoFork(t *testing.T) {
	m := newAgentAIManager()
	defer m.closeAll()
	w := newCapturedWriter()
	run := newThreadBusyTestRun()

	done := make(chan struct{})
	var choice string
	var delivered bool
	go func() {
		defer close(done)
		choice, delivered = m.askAgentAIThreadBusyFork(context.Background(), &run, w.write, "busy-thread")
	}()
	frames := w.waitForFrames(1, 5*time.Second)
	interactionID := remoteString(frames[0], "interaction_id")

	m.ensureToolAskBridge().deliver(models.AgentToolAskResponse{
		InteractionID: interactionID,
		Status:        models.AgentToolAskStatusAnswered,
		Answer:        &models.AgentToolAskAnswer{SelectedOptionID: agentAIThreadBusyOptionClose},
	})
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("ask did not return")
	}
	if !delivered || choice == agentAIThreadBusyOptionFork {
		t.Fatalf("choice=%q delivered=%v; default semantics require non-fork", choice, delivered)
	}
}

func TestAskAgentAIThreadBusyForkTimeoutCancels(t *testing.T) {
	t.Setenv("ALIANG_AI_THREAD_BUSY_ASK_TIMEOUT", "30ms")
	m := newAgentAIManager()
	defer m.closeAll()
	w := newCapturedWriter()
	run := newThreadBusyTestRun()

	choice, delivered := m.askAgentAIThreadBusyFork(context.Background(), &run, w.write, "busy-thread")
	if delivered || choice != "" {
		t.Fatalf("timeout must return not-delivered, got choice=%q delivered=%v", choice, delivered)
	}
	cancelled := false
	for _, f := range w.frames() {
		if f["type"] == models.AgentEventToolAskCancel && f["reason"] == models.AgentToolAskReasonTimeout {
			cancelled = true
		}
	}
	if !cancelled {
		t.Fatalf("timeout must emit tool_ask.cancel(reason=timeout)")
	}
	// pending 必须清干净（到期强制收帧，绝不永挂）。
	if left := m.ensureToolAskBridge().pendingSnapshot(); len(left) != 0 {
		t.Fatalf("pending leaked: %v", left)
	}
}

func TestHandleAgentAICodexThreadBusyDeclineEmitsTypedError(t *testing.T) {
	m := newAgentAIManager()
	defer m.closeAll()
	w := newCapturedWriter()
	run := newThreadBusyTestRun()

	done := make(chan struct{})
	go func() {
		defer close(done)
		m.handleAgentAICodexThreadBusy(context.Background(), &run, w.write)
	}()
	frames := w.waitForFrames(1, 5*time.Second)
	m.ensureToolAskBridge().deliver(models.AgentToolAskResponse{
		InteractionID: remoteString(frames[0], "interaction_id"),
		Status:        models.AgentToolAskStatusAnswered,
		Answer:        &models.AgentToolAskAnswer{SelectedOptionID: agentAIThreadBusyOptionClose},
	})
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("handler did not return")
	}
	var errFrame map[string]interface{}
	for _, f := range w.frames() {
		if f["type"] == models.AgentEventAIError {
			errFrame = f
		}
	}
	if errFrame == nil {
		t.Fatalf("decline path must emit ai.error")
	}
	if got, _ := errFrame["error_code"].(string); got != "codex_thread_busy" {
		t.Fatalf("error_code = %v, want codex_thread_busy", got)
	}
}
