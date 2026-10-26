package services

import (
	"fmt"
	"strings"
	"testing"
)

// TestClaudeWhitespaceDeltasStreamed verifies that whitespace-only text deltas
// reach the wire byte-for-byte. 2026-10-26 RCA: the emit loop used to skip
// deltas whose TrimSpace was empty, so a standalone "\n" (e.g. right after the
// ```aliang-options fence language tag) and standalone spaces at random token
// boundaries never reached the server — the persisted message lost those bytes
// ("```aliang-options{\"title\"" …), which broke fence parsing/markdown and
// made the phone render option blocks as raw text.
func TestClaudeWhitespaceDeltasStreamed(t *testing.T) {
	run := agentAIRun{sessionID: "s", messageID: "m", runSeq: 1}
	mu, events, writer := captureAIWriter(t)

	input := strings.Join([]string{
		"{\"type\":\"stream_event\",\"event\":{\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"```aliang-options\"}}}",
		"{\"type\":\"stream_event\",\"event\":{\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"\\n\"}}}",
		"{\"type\":\"stream_event\",\"event\":{\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"{\\\"title\\\":\\\"q\\\",\\\"options\\\":[]}\"}}}",
		"{\"type\":\"stream_event\",\"event\":{\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\" \"}}}",
		"{\"type\":\"stream_event\",\"event\":{\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"tail\"}}}",
	}, "\n")
	streamStructuredAIDelta(strings.NewReader(input), agentAIOutputClaudeStreamJSON, run, writer, &agentAIOutputLimiter{}, nil, nil, nil, nil)

	var joined strings.Builder
	for _, d := range findAIEvents(mu, events, "ai.delta") {
		joined.WriteString(fmt.Sprint(d["delta"]))
	}
	want := "```aliang-options\n{\"title\":\"q\",\"options\":[]} tail"
	if joined.String() != want {
		t.Fatalf("streamed deltas = %q, want %q", joined.String(), want)
	}
}

// TestClaudeFinalizedWhitespaceSuffixStreamed verifies the finalized-suffix
// path no longer drops a pure-whitespace tail (guard used TrimSpace, silently
// eating e.g. the trailing newline of the final text).
func TestClaudeFinalizedWhitespaceSuffixStreamed(t *testing.T) {
	run := agentAIRun{sessionID: "s", messageID: "m", runSeq: 1}
	mu, events, writer := captureAIWriter(t)

	input := strings.Join([]string{
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"hello"}}}`,
		`{"type":"assistant","message":{"model":"claude-x","content":[{"type":"text","text":"hello\n"}]}}`,
	}, "\n")
	streamStructuredAIDelta(strings.NewReader(input), agentAIOutputClaudeStreamJSON, run, writer, &agentAIOutputLimiter{}, nil, nil, nil, nil)

	var joined strings.Builder
	for _, d := range findAIEvents(mu, events, "ai.delta") {
		joined.WriteString(fmt.Sprint(d["delta"]))
	}
	if joined.String() != "hello\n" {
		t.Fatalf("streamed deltas = %q, want %q", joined.String(), "hello\n")
	}
}
