package ratelimitwin

import (
	"net/http"
	"strconv"
	"testing"
	"time"
)

// assertWindow 断言 until 相对 now 落在 want 秒附近（±3s 容忍实现内部取 now
// 的毫秒级漂移）。
func assertWindow(t *testing.T, until, now int64, want int64) {
	t.Helper()
	got := until - now
	if got < want-3 || got > want+3 {
		t.Fatalf("window = %ds, want ~%ds (until=%d now=%d)", got, want, until, now)
	}
}

func header(kv ...string) http.Header {
	h := http.Header{}
	for i := 0; i+1 < len(kv); i += 2 {
		h.Set(kv[i], kv[i+1])
	}
	return h
}

func TestParseNoHeadersReturnsZero(t *testing.T) {
	if got := Parse(http.StatusTooManyRequests, nil); got != 0 {
		t.Fatalf("nil header: got %d, want 0", got)
	}
	if got := Parse(http.StatusTooManyRequests, http.Header{}); got != 0 {
		t.Fatalf("empty header: got %d, want 0", got)
	}
}

func TestParseRetryAfterSeconds(t *testing.T) {
	now := time.Now().Unix()
	got := Parse(http.StatusTooManyRequests, header("Retry-After", "120"))
	assertWindow(t, got, now, 120)
}

func TestParseRetryAfterClampsLow(t *testing.T) {
	now := time.Now().Unix()
	got := Parse(http.StatusTooManyRequests, header("Retry-After", "5"))
	assertWindow(t, got, now, 60)
}

func TestParseRetryAfterClampsHigh(t *testing.T) {
	now := time.Now().Unix()
	got := Parse(http.StatusTooManyRequests, header("Retry-After", "999999"))
	assertWindow(t, got, now, 21600)
}

func TestParseRetryAfterHTTPDate(t *testing.T) {
	now := time.Now().Unix()
	date := time.Now().Add(300 * time.Second).UTC().Format(http.TimeFormat)
	got := Parse(http.StatusTooManyRequests, header("Retry-After", date))
	assertWindow(t, got, now, 300)
}

func TestParseRetryAfterInvalidReturnsZero(t *testing.T) {
	if got := Parse(http.StatusTooManyRequests, header("Retry-After", "soon-ish")); got != 0 {
		t.Fatalf("invalid Retry-After: got %d, want 0", got)
	}
	if got := Parse(http.StatusTooManyRequests, header("Retry-After", "-10")); got != 0 {
		t.Fatalf("negative Retry-After: got %d, want 0", got)
	}
}

func TestParseUnifiedReset(t *testing.T) {
	now := time.Now().Unix()
	reset := time.Now().Add(180 * time.Second).Unix()
	got := Parse(http.StatusTooManyRequests, header("anthropic-ratelimit-unified-reset", itoa(reset)))
	assertWindow(t, got, now, 180)
}

func TestParseUnifiedResetInvalidIgnored(t *testing.T) {
	if got := Parse(http.StatusTooManyRequests, header("anthropic-ratelimit-unified-reset", "not-a-number")); got != 0 {
		t.Fatalf("invalid unified-reset: got %d, want 0", got)
	}
	if got := Parse(http.StatusTooManyRequests, header("anthropic-ratelimit-unified-reset", "0")); got != 0 {
		t.Fatalf("zero unified-reset: got %d, want 0", got)
	}
}

func TestParseTakesFartherHeader(t *testing.T) {
	now := time.Now().Unix()

	// Retry-After 更远：120s vs 60s。
	reset60 := time.Now().Add(60 * time.Second).Unix()
	got := Parse(http.StatusTooManyRequests, header(
		"Retry-After", "120",
		"anthropic-ratelimit-unified-reset", itoa(reset60),
	))
	assertWindow(t, got, now, 120)

	// unified-reset 更远：60s vs 600s。
	reset600 := time.Now().Add(600 * time.Second).Unix()
	got = Parse(http.StatusTooManyRequests, header(
		"Retry-After", "60",
		"anthropic-ratelimit-unified-reset", itoa(reset600),
	))
	assertWindow(t, got, now, 600)
}

func TestParsePastResetClampsToMin(t *testing.T) {
	now := time.Now().Unix()
	past := time.Now().Add(-time.Hour).Unix()
	got := Parse(http.StatusTooManyRequests, header("anthropic-ratelimit-unified-reset", itoa(past)))
	assertWindow(t, got, now, 60)
}

func itoa(v int64) string {
	return strconv.FormatInt(v, 10)
}
