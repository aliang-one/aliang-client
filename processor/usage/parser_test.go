package usage

import (
	"testing"
	"time"
)

// 真实 Claude Code assistant 行（字段裁剪自真实会话文件，content 已移除——
// 解析器本就不应触碰它）
const assistantLine = `{"parentUuid":null,"isSidechain":false,"userType":"external","cwd":"/tmp/proj","sessionId":"8f0e2c1a-1111-2222-3333-444455556666","version":"2.0.1","type":"assistant","message":{"id":"msg_01ABC","type":"message","role":"assistant","model":"claude-sonnet-4-5-20250929","content":[{"type":"text","text":"(内容不进入解析)"}],"usage":{"input_tokens":4,"cache_creation_input_tokens":5036,"cache_read_input_tokens":12000,"output_tokens":168}},"requestId":"req_01","uuid":"0b9e8f2a-aaaa-bbbb-cccc-dddddddddddd","timestamp":"2026-09-23T10:05:29.545Z"}`

func TestParseLineAssistantWithUsage(t *testing.T) {
	sample, err := ParseLine([]byte(assistantLine))
	if err != nil {
		t.Fatalf("ParseLine error: %v", err)
	}
	if sample == nil {
		t.Fatal("expected sample, got nil")
	}
	if sample.SessionID != "8f0e2c1a-1111-2222-3333-444455556666" {
		t.Errorf("SessionID = %q", sample.SessionID)
	}
	if sample.Model != "claude-sonnet-4-5-20250929" {
		t.Errorf("Model = %q", sample.Model)
	}
	if sample.InputTokens != 4 || sample.OutputTokens != 168 {
		t.Errorf("tokens = %d/%d", sample.InputTokens, sample.OutputTokens)
	}
	if sample.CacheReadTokens != 12000 || sample.CacheCreationTokens != 5036 {
		t.Errorf("cache = read %d / creation %d", sample.CacheReadTokens, sample.CacheCreationTokens)
	}
	want := time.Date(2026, 9, 23, 10, 5, 29, 545000000, time.UTC)
	if !sample.Timestamp.Equal(want) {
		t.Errorf("Timestamp = %v, want %v", sample.Timestamp, want)
	}
}

func TestParseLineSkips(t *testing.T) {
	cases := map[string]string{
		"user line":          `{"type":"user","sessionId":"s","uuid":"u","message":{"role":"user","content":"hi"}}`,
		"assistant no usage": `{"type":"assistant","sessionId":"s","uuid":"u","timestamp":"2026-09-23T10:05:29.545Z","message":{"model":"m","role":"assistant"}}`,
		"summary line":       `{"type":"summary","summary":"标题","leafUuid":"x"}`,
		"all zero usage":     `{"type":"assistant","sessionId":"s","uuid":"u","timestamp":"2026-09-23T10:05:29.545Z","message":{"model":"m","usage":{"input_tokens":0,"output_tokens":0}}}`,
	}
	for name, line := range cases {
		sample, err := ParseLine([]byte(line))
		if err != nil {
			t.Errorf("%s: unexpected error %v", name, err)
		}
		if sample != nil {
			t.Errorf("%s: expected nil sample, got %+v", name, sample)
		}
	}
}

func TestParseLineSidechainCounted(t *testing.T) {
	line := `{"type":"assistant","isSidechain":true,"sessionId":"s","uuid":"u","timestamp":"2026-09-23T10:05:29.545Z","message":{"model":"m","usage":{"input_tokens":10,"output_tokens":5}}}`
	sample, err := ParseLine([]byte(line))
	if err != nil || sample == nil {
		t.Fatalf("sidechain line must count, got sample=%v err=%v", sample, err)
	}
}

func TestParseLineMalformed(t *testing.T) {
	if _, err := ParseLine([]byte(`{"type":"assistant",`)); err == nil {
		t.Fatal("expected error for malformed JSON")
	}
}

func TestParseLineBlankSkipped(t *testing.T) {
	cases := map[string]string{
		"empty line":      "",
		"whitespace only": " \t\r\n",
	}
	for name, line := range cases {
		sample, err := ParseLine([]byte(line))
		if err != nil {
			t.Errorf("%s: unexpected error %v", name, err)
		}
		if sample != nil {
			t.Errorf("%s: expected nil sample, got %+v", name, sample)
		}
	}
}

func TestParseLineNegativeTokensClamped(t *testing.T) {
	line := `{"type":"assistant","sessionId":"s","uuid":"u","timestamp":"2026-09-23T10:05:29.545Z","message":{"model":"m","usage":{"input_tokens":-5,"output_tokens":7,"cache_read_input_tokens":-3,"cache_creation_input_tokens":9}}}`
	sample, err := ParseLine([]byte(line))
	if err != nil || sample == nil {
		t.Fatalf("expected sample, got sample=%v err=%v", sample, err)
	}
	if sample.InputTokens != 0 {
		t.Errorf("InputTokens = %d, want 0 (negative clamped)", sample.InputTokens)
	}
	if sample.CacheReadTokens != 0 {
		t.Errorf("CacheReadTokens = %d, want 0 (negative clamped)", sample.CacheReadTokens)
	}
	if sample.OutputTokens != 7 || sample.CacheCreationTokens != 9 {
		t.Errorf("positive fields must be kept: output=%d creation=%d", sample.OutputTokens, sample.CacheCreationTokens)
	}
}

func TestParseLineMissingTimestampFallsBackToNow(t *testing.T) {
	line := `{"type":"assistant","sessionId":"s","uuid":"u","message":{"model":"m","usage":{"input_tokens":1,"output_tokens":1}}}`
	before := time.Now().UTC().Add(-time.Minute)
	sample, err := ParseLine([]byte(line))
	if err != nil || sample == nil {
		t.Fatalf("expected sample, got sample=%v err=%v", sample, err)
	}
	after := time.Now().UTC().Add(time.Minute)
	if sample.Timestamp.Before(before) || sample.Timestamp.After(after) {
		t.Errorf("Timestamp = %v, want ~now between %v and %v", sample.Timestamp, before, after)
	}
}

func TestParseLineTimestampNormalizedToUTC(t *testing.T) {
	line := `{"type":"assistant","sessionId":"s","uuid":"u","timestamp":"2026-09-23T13:05:29.545+03:00","message":{"model":"m","usage":{"input_tokens":1,"output_tokens":1}}}`
	sample, err := ParseLine([]byte(line))
	if err != nil || sample == nil {
		t.Fatalf("expected sample, got sample=%v err=%v", sample, err)
	}
	want := time.Date(2026, 9, 23, 10, 5, 29, 545000000, time.UTC)
	if !sample.Timestamp.Equal(want) {
		t.Errorf("Timestamp = %v, want %v (UTC-normalized)", sample.Timestamp, want)
	}
	if _, offset := sample.Timestamp.Zone(); offset != 0 {
		t.Errorf("Timestamp zone offset = %ds, want 0 (UTC)", offset)
	}
}
