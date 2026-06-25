package services

import (
	"strings"
	"testing"
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
	streamStructuredAIDelta(strings.NewReader(input), agentAIOutputClaudeStreamJSON, run, writer, &agentAIOutputLimiter{}, nil, nil, &lastRetry)

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
	streamStructuredAIDelta(strings.NewReader(input), agentAIOutputClaudeStreamJSON, run, writer, &agentAIOutputLimiter{}, nil, nil, &lastRetry)

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
