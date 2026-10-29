package services

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"aliang.one/nursorgate/app/http/models"
)

// 这一组锁死 claude CLI 通道诊断聚合器的语义：assistant 帧计回合 + 取最近
// usage 三项和，result 帧定终态（num_turns 权威覆盖 / is_error 带真因），
// 进度心跳与收尾终帧共用同一 payload 形状（键与 agentcore 对齐，server 归一
// 化与手机面板不区分通道）。

func claudeAssistantEvent(msgID string, in, cacheRead int) string {
	return `{"type":"assistant","message":{"id":"` + msgID + `","role":"assistant","model":"claude",` +
		`"stop_reason":"tool_use","usage":{"input_tokens":` + diagItoa(in) +
		`,"cache_read_input_tokens":` + diagItoa(cacheRead) + `,"output_tokens":20}}}` + "\n"
}

func claudeResultEvent(isError bool, numTurns int) string {
	errFlag := "false"
	subtype := "success"
	if isError {
		errFlag = "true"
		subtype = "error_during_execution"
	}
	return `{"type":"result","subtype":"` + subtype + `","is_error":` + errFlag +
		`,"num_turns":` + diagItoa(numTurns) + `,"duration_ms":1234,"result":"done"}` + "\n"
}

func diagItoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := ""
	for n > 0 {
		digits = string(rune('0'+n%10)) + digits
		n /= 10
	}
	return digits
}

func driveClaudeStreamDiag(t *testing.T, ndjson string, run agentAIRun) {
	t.Helper()
	writeJSON := agentTerminalWriter(func(v interface{}) error { return nil })
	streamStructuredAIDelta(
		bytes.NewReader([]byte(ndjson)),
		agentAIOutputClaudeStreamJSON,
		run,
		writeJSON,
		nil, // limiter
		nil, // capture
		nil, // fileSink
		&claudeRetryInfo{},
		nil, // resumeSessionID
	)
}

// TestClaudeRunDiagnosticsPayload：两个 assistant 帧 + 成功 result → 终态
// payload 的 turns 被 num_turns 权威覆盖、context_tokens 为最近 usage 三项和、
// terminal=true、elapsed 用 result 的 duration_ms。
func TestClaudeRunDiagnosticsPayload(t *testing.T) {
	diag := newClaudeRunDiagnostics(200000)
	diag.observe(mustJSONMap(t, claudeAssistantEvent("msg_1", 100, 400)))
	diag.observe(mustJSONMap(t, claudeAssistantEvent("msg_2", 2500, 7000)))
	diag.observe(mustJSONMap(t, claudeResultEvent(false, 5)))

	payload := diag.payload()
	if payload == nil {
		t.Fatal("payload 不应为 nil")
	}
	if got := payloadInt(payload, "turns"); got != 5 {
		t.Errorf("turns = %d, want 5（result.num_turns 权威覆盖）", got)
	}
	if got := payloadInt(payload, "context_tokens"); got != 9500 {
		t.Errorf("context_tokens = %d, want 9500（2500+7000）", got)
	}
	if got := payloadInt(payload, "context_window"); got != 200000 {
		t.Errorf("context_window = %d, want 200000", got)
	}
	if payload["terminal"] != true {
		t.Errorf("terminal = %v, want true", payload["terminal"])
	}
	if _, has := payload["reason"]; has {
		t.Errorf("成功终态不应带 reason, got %v", payload["reason"])
	}
	if got := payloadInt(payload, "elapsed_ms"); got != 1234 {
		t.Errorf("elapsed_ms = %d, want 1234（result.duration_ms）", got)
	}
	for _, key := range []string{"dup_streak", "err_streak", "trunc_streak"} {
		if got := payloadInt(payload, key); got != 0 {
			t.Errorf("%s = %d, want 0（CLI 无内核守卫）", key, got)
		}
	}
}

// TestClaudeRunDiagnosticsResultError：失败 result → terminal + reason 带
// subtype。
func TestClaudeRunDiagnosticsResultError(t *testing.T) {
	diag := newClaudeRunDiagnostics(200000)
	diag.observe(mustJSONMap(t, claudeAssistantEvent("msg_1", 100, 0)))
	diag.observe(mustJSONMap(t, claudeResultEvent(true, 3)))

	payload := diag.payload()
	if payload["terminal"] != true {
		t.Fatalf("terminal = %v, want true", payload["terminal"])
	}
	if got, _ := payload["reason"].(string); !strings.Contains(got, "error_during_execution") {
		t.Errorf("reason = %q, 应含 subtype", got)
	}
}

// TestClaudeRunDiagnosticsFinishFallback：进程异常退出（无 result 事件）时
// finish 补终态标记；正常路径已 terminal 时 finish 不覆盖既有真因。
func TestClaudeRunDiagnosticsFinishFallback(t *testing.T) {
	diag := newClaudeRunDiagnostics(200000)
	diag.observe(mustJSONMap(t, claudeAssistantEvent("msg_1", 100, 0)))
	diag.finish("")
	payload := diag.payload()
	if payload["terminal"] != true {
		t.Fatalf("finish 后 terminal = %v, want true", payload["terminal"])
	}

	diag2 := newClaudeRunDiagnostics(200000)
	diag2.observe(mustJSONMap(t, claudeResultEvent(true, 2)))
	diag2.finish("被覆盖才错")
	if got, _ := diag2.payload()["reason"].(string); !strings.Contains(got, "error_during_execution") {
		t.Errorf("reason = %q, finish 不应覆盖 observe(result) 的真因", got)
	}
}

// TestStreamClaudeFeedsDiagnostics：streamStructuredAIDelta 主循环把
// assistant/result 帧喂给 run.claudeDiag——与生产同一入口（run 值副本共享
// 指针聚合器）。
func TestStreamClaudeFeedsDiagnostics(t *testing.T) {
	run := agentAIRun{
		sessionID: "s-diag",
		messageID: "m-diag",
		runSeq:    1,
		activity:  newAgentAIActivity(),
	}
	diag := newClaudeRunDiagnostics(100000)
	run.claudeDiag = diag

	ndjson := `{"type":"system","subtype":"init","session_id":"sess_x"}` + "\n" +
		claudeAssistantEvent("msg_1", 800, 200) +
		claudeResultEvent(false, 1)
	driveClaudeStreamDiag(t, ndjson, run)

	payload := diag.payload()
	if payload == nil {
		t.Fatal("stream 结束后 payload 不应为 nil")
	}
	if got := payloadInt(payload, "context_tokens"); got != 1000 {
		t.Errorf("context_tokens = %d, want 1000", got)
	}
	if payload["terminal"] != true {
		t.Errorf("terminal = %v, want true", payload["terminal"])
	}
}

// TestClaudeRunDiagnosticsNilSafe：nil 接收者全链路安全（agentcore/codex
// 路径不挂载）。
func TestClaudeRunDiagnosticsNilSafe(t *testing.T) {
	var diag *claudeRunDiagnostics
	diag.observe(mustJSONMap(t, claudeAssistantEvent("msg_1", 1, 0)))
	diag.finish("x")
	if p := diag.payload(); p != nil {
		t.Fatalf("nil 聚合器 payload = %v, want nil", p)
	}

	// 零回合不产出空壳 payload（与 agentcore「不下发空壳字段」同约定）。
	empty := newClaudeRunDiagnostics(100000)
	if p := empty.payload(); p != nil {
		t.Fatalf("零回合 payload = %v, want nil", p)
	}
}

func mustJSONMap(t *testing.T, line string) map[string]interface{} {
	t.Helper()
	decoder := json.NewDecoder(strings.NewReader(line))
	var event map[string]interface{}
	if err := decoder.Decode(&event); err != nil {
		t.Fatalf("解析测试事件失败: %v (%q)", err, line)
	}
	return event
}

func payloadInt(payload map[string]interface{}, key string) int {
	switch v := payload[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	}
	return -1
}

// 编译期引用，保持 models import 稳定（帧类型常量在实现里用）。
var _ = models.AgentEventAIRunProgress
