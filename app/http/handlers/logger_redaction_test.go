package handlers

import (
	"strings"
	"testing"
	"time"

	"aliang.one/nursorgate/common/logger"
)

func TestLogRedaction(t *testing.T) {
	logger.ClearBuffer()
	logger.AppendToBuffer(&logger.LogEntry{
		Level:     logger.INFO,
		Timestamp: time.Now(),
		Message:   "authorization=Bearer super-secret-token api_key=abc123 password=topsecret",
		Source:    "http",
	})

	service := NewLogService()
	entries := service.GetLogs(LogsQueryParams{Limit: 10})
	if len(entries) != 1 {
		t.Fatalf("entries=%d, want 1", len(entries))
	}

	message := entries[0].Message
	if strings.Contains(message, "super-secret-token") || strings.Contains(message, "abc123") || strings.Contains(message, "topsecret") {
		t.Fatalf("expected sensitive values to be redacted, got %q", message)
	}
	if !strings.Contains(message, redactionMarker) {
		t.Fatalf("expected redaction marker %q, got %q", redactionMarker, message)
	}
}
