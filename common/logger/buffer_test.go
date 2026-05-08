package logger

import (
	"fmt"
	"testing"
	"time"
)

func TestGlobalLogBufferDefaultSize(t *testing.T) {
	if globalBuffer.maxSize != DefaultLogBufferSize {
		t.Fatalf("global buffer size = %d, want %d", globalBuffer.maxSize, DefaultLogBufferSize)
	}
}

func TestLogBufferRetainsOnlyConfiguredCapacity(t *testing.T) {
	buffer := NewLogBuffer(3)

	for i := 0; i < 5; i++ {
		buffer.Append(&LogEntry{
			Level:     INFO,
			Timestamp: time.Unix(int64(i), 0),
			Message:   fmt.Sprintf("entry-%d", i),
		})
	}

	entries := buffer.GetAll()
	if len(entries) != 3 {
		t.Fatalf("entry count = %d, want 3", len(entries))
	}
	if entries[0].Message != "entry-2" || entries[2].Message != "entry-4" {
		t.Fatalf("entries = %q, %q, %q; want entry-2..entry-4",
			entries[0].Message,
			entries[1].Message,
			entries[2].Message,
		)
	}
}

func TestLogBufferSubscribeUnsubscribe(t *testing.T) {
	buffer := NewLogBuffer(3)
	ch := make(chan *LogEntry, 1)

	unsubscribe := buffer.Subscribe(func(entry *LogEntry) {
		select {
		case ch <- entry:
		default:
		}
	})

	buffer.Append(&LogEntry{Level: INFO, Message: "before"})
	select {
	case entry := <-ch:
		if entry.Message != "before" {
			t.Fatalf("message = %q, want before", entry.Message)
		}
	default:
		t.Fatal("observer did not receive first log entry")
	}

	unsubscribe()
	buffer.Append(&LogEntry{Level: INFO, Message: "after"})
	select {
	case entry := <-ch:
		t.Fatalf("observer received entry after unsubscribe: %q", entry.Message)
	default:
	}
}
