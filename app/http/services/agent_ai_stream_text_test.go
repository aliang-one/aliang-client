package services

import (
	"bytes"
	"strings"
	"testing"

	"aliang.one/nursorgate/app/http/models"
)

// driveClaudeStream runs a claude --print stream-json byte stream through the
// real streaming loop and returns the concatenated text of every ai.delta the
// agent emitted. Mirrors how streamStructuredAIDelta is invoked in production
// (claude format, no limiter/capture/fileSink) so this exercises the exact
// text-emission path the phone ultimately sees.
func driveClaudeStream(t *testing.T, ndjson string) string {
	t.Helper()
	var deltas []string
	writeJSON := agentTerminalWriter(func(v interface{}) error {
		ev, ok := v.(map[string]interface{})
		if !ok {
			return nil
		}
		if models.AgentEventAIDelta == "ai.delta" && remoteString(ev, "type") == "ai.delta" {
			deltas = append(deltas, remoteString(ev, "delta"))
		}
		return nil
	})
	run := agentAIRun{
		sessionID: "s",
		messageID: "m",
		runSeq:    1,
		activity:  newAgentAIActivity(),
	}
	retry := &claudeRetryInfo{}
	streamStructuredAIDelta(
		bytes.NewReader([]byte(ndjson)),
		agentAIOutputClaudeStreamJSON,
		run,
		writeJSON,
		nil, // limiter
		nil, // capture
		nil, // fileSink
		retry,
		nil, // resumeSessionID
	)
	return strings.Join(deltas, "")
}

// TestStreamClaudeMultiAssistantAllTextEmitted is the regression test for the
// "中间 AI 对话不显示" bug. The old loop used a per-RUN `emitted` flag: once the
// first text_delta set it, every later finalized `assistant` message was
// suppressed (allowFinal=false), so any assistant message whose text arrived
// only in its finalized event — never streamed as text_delta — was dropped on
// the wire and only recovered later by the server's end-of-run session-detail
// pull. The phone thus showed long "thinking" then a sudden conclusion.
//
// Fix: per-message dedup. Each finalized assistant message emits the un-streamed
// suffix; the flag no longer persists across messages. This stream has TWO
// assistant messages; the second carries text ONLY in its finalized event (no
// text_delta). Both messages' text must appear in the emitted ai.delta stream.
func TestStreamClaudeMultiAssistantAllTextEmitted(t *testing.T) {
	const ndjson = `{"type":"system","subtype":"init","session_id":"sess_test"}` + "\n" +
		// message 1 — text streamed normally via text_delta
		`{"type":"stream_event","event":{"type":"message_start","message":{"id":"msg_1"}}}` + "\n" +
		`{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"text"}}}` + "\n" +
		`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"First answer"}}}` + "\n" +
		`{"type":"stream_event","event":{"type":"content_block_stop","index":0}}` + "\n" +
		`{"type":"stream_event","event":{"type":"message_stop"}}` + "\n" +
		`{"type":"assistant","message":{"id":"msg_1","role":"assistant","model":"claude","content":[{"type":"text","text":"First answer"}]}}` + "\n" +
		// message 2 — text appears ONLY in the finalized assistant event
		// (no text_delta). Old code dropped this; new code must emit it.
		`{"type":"stream_event","event":{"type":"message_start","message":{"id":"msg_2"}}}` + "\n" +
		`{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"text"}}}` + "\n" +
		`{"type":"stream_event","event":{"type":"content_block_stop","index":0}}` + "\n" +
		`{"type":"stream_event","event":{"type":"message_stop"}}` + "\n" +
		`{"type":"assistant","message":{"id":"msg_2","role":"assistant","model":"claude","content":[{"type":"text","text":"Second answer"}]}}` + "\n" +
		`{"type":"result","result":"Second answer","session_id":"sess_test","is_error":false}` + "\n"

	got := driveClaudeStream(t, ndjson)
	if !strings.Contains(got, "First answer") {
		t.Fatalf("emitted ai.delta = %q; missing first assistant message text", got)
	}
	if !strings.Contains(got, "Second answer") {
		t.Fatalf("emitted ai.delta = %q; missing second assistant message text (the finalized-only message that the old global `emitted` flag dropped)", got)
	}
	// Must not double-emit the first message (delta + finalized should dedup).
	if c := strings.Count(got, "First answer"); c != 1 {
		t.Fatalf("First answer emitted %d times, want 1 (delta/finalized dedup): %q", c, got)
	}
}

// TestStreamClaudeFinalizedSuffixRecovery: a single assistant message whose
// text_delta partials covered only a prefix; the finalized event carries the
// full text. The un-streamed suffix must be emitted (not the whole thing, not
// nothing).
func TestStreamClaudeFinalizedSuffixRecovery(t *testing.T) {
	const ndjson = `{"type":"stream_event","event":{"type":"message_start","message":{"id":"msg_1"}}}` + "\n" +
		`{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"text"}}}` + "\n" +
		`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hel"}}}` + "\n" +
		`{"type":"stream_event","event":{"type":"content_block_stop","index":0}}` + "\n" +
		`{"type":"assistant","message":{"id":"msg_1","role":"assistant","model":"claude","content":[{"type":"text","text":"Hello world"}]}}` + "\n" +
		`{"type":"result","result":"Hello world","session_id":"sess_test","is_error":false}` + "\n"

	got := driveClaudeStream(t, ndjson)
	if !strings.Contains(got, "Hello world") {
		t.Fatalf("emitted ai.delta = %q; missing full text (suffix recovery failed)", got)
	}
	if c := strings.Count(got, "Hel"); c != 1 {
		t.Fatalf("prefix Hel appeared %d times, want 1 (no duplicate of the streamed prefix): %q", c, got)
	}
}

// TestSuffixNotStreamed pins the dedup helper's edge cases.
func TestSuffixNotStreamed(t *testing.T) {
	cases := []struct {
		streamed, final, want string
	}{
		{"", "abc", "abc"},     // nothing streamed → emit all
		{"abc", "abc", ""},     // fully streamed → nothing
		{"abc", "abcdef", "def"}, // partial → emit tail
		{"abcdef", "abc", ""},  // streamed exceeded final → nothing
		{"abc", "xyz", "xyz"},  // diverged → emit final wholesale
		{"", "", ""},           // both empty
		{"abc", "", ""},        // final empty
	}
	for _, c := range cases {
		if got := suffixNotStreamed(c.streamed, c.final); got != c.want {
			t.Errorf("suffixNotStreamed(%q,%q) = %q, want %q", c.streamed, c.final, got, c.want)
		}
	}
}

// driveClaudeStreamEvents is like driveClaudeStream but returns EVERY emitted
// event (not just ai.delta text), so structured-event tests can inspect
// ai.thinking active flips.
func driveClaudeStreamEvents(t *testing.T, ndjson string) []map[string]interface{} {
	t.Helper()
	var got []map[string]interface{}
	writeJSON := agentTerminalWriter(func(v interface{}) error {
		if ev, ok := v.(map[string]interface{}); ok {
			got = append(got, ev)
		}
		return nil
	})
	run := agentAIRun{sessionID: "s", messageID: "m", runSeq: 1, activity: newAgentAIActivity()}
	streamStructuredAIDelta(
		bytes.NewReader([]byte(ndjson)),
		agentAIOutputClaudeStreamJSON,
		run, writeJSON, nil, nil, nil, &claudeRetryInfo{}, nil,
	)
	return got
}

// TestStreamClaudeThinkingStopsAtContentBlockStop: a thinking content_block
// must emit an explicit ai.thinking active:false when it closes, giving the
// thinking activity a reliable end boundary (not only the run-terminal
// ai.done). Before this fix the agent only ever sent thinking_delta (active
// implied true) and never a stop, so the server-side active flag stayed true
// for the whole run.
func TestStreamClaudeThinkingStopsAtContentBlockStop(t *testing.T) {
	const ndjson = `{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"thinking"}}}` + "\n" +
		`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"reasoning"}}}` + "\n" +
		`{"type":"stream_event","event":{"type":"content_block_stop","index":0}}` + "\n" +
		`{"type":"stream_event","event":{"type":"content_block_start","index":1,"content_block":{"type":"text"}}}` + "\n" +
		`{"type":"stream_event","event":{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"answer"}}}` + "\n" +
		`{"type":"stream_event","event":{"type":"content_block_stop","index":1}}` + "\n" +
		`{"type":"assistant","message":{"id":"msg_1","role":"assistant","model":"claude","content":[{"type":"text","text":"answer"}]}}` + "\n" +
		`{"type":"result","result":"answer","session_id":"sess_test","is_error":false}` + "\n"

	var sawThinkingDelta, sawThinkingActiveFalse bool
	for _, ev := range driveClaudeStreamEvents(t, ndjson) {
		if remoteString(ev, "type") != "ai.thinking" {
			continue
		}
		if remoteString(ev, "delta") == "reasoning" {
			sawThinkingDelta = true
		}
		if active, ok := ev["active"].(bool); ok && !active {
			sawThinkingActiveFalse = true
		}
	}
	if !sawThinkingDelta {
		t.Fatal("expected an ai.thinking delta event for the reasoning, got none")
	}
	if !sawThinkingActiveFalse {
		t.Fatal("expected an ai.thinking active:false event at content_block_stop of the thinking block, got none — thinking has no reliable end boundary")
	}
}
