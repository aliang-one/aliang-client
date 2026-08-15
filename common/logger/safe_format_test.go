package logger

import (
	"io"
	"log"
	"strings"
	"sync"
	"testing"
	"time"
)

type panickingError struct{}

func (panickingError) Error() string {
	panic("boom")
}

type panickingWriter struct{}

func (panickingWriter) Write([]byte) (int, error) {
	panic("write boom")
}

func TestSafeValueStringRecoversFromPanickingError(t *testing.T) {
	got := SafeValueString(panickingError{})

	if !strings.Contains(got, "PANIC=Error method: boom") {
		t.Fatalf("SafeValueString() = %q, want fmt panic marker", got)
	}
	if got == "" {
		t.Fatalf("SafeValueString() returned an empty string")
	}
}

func TestSafeRecoveredValueStringAvoidsErrorMethod(t *testing.T) {
	got := SafeRecoveredValueString(panickingError{})

	if strings.Contains(got, "boom") {
		t.Fatalf("SafeRecoveredValueString() = %q, should not call Error()", got)
	}
	if !strings.Contains(got, "panickingError") {
		t.Fatalf("SafeRecoveredValueString() = %q, want type information", got)
	}
}

func TestSafeSprintPreservesPrefixWhenAnArgumentPanics(t *testing.T) {
	got := SafeSprint("panic: ", panickingError{})

	if !strings.HasPrefix(got, "panic: ") {
		t.Fatalf("SafeSprint() = %q, want prefix to be preserved", got)
	}
	if !strings.Contains(got, "PANIC=Error method: boom") {
		t.Fatalf("SafeSprint() = %q, want fmt panic marker", got)
	}
}

func TestMainLoggerInfoDoesNotPanicWhenWriterPanics(t *testing.T) {
	ml := &mainLogger{
		config: &LogConfig{
			Level:         DEBUG,
			ErrorWindow:   time.Minute,
			MaxErrorCount: 1,
		},
		mu:      &sync.RWMutex{},
		loggers: []*log.Logger{log.New(panickingWriter{}, "", 0)},
	}

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Info() panicked: %v", r)
		}
	}()

	ml.Info("hello")
}

func TestMainLoggerErrorDoesNotPanicWhenFormattingBrokenError(t *testing.T) {
	ml := &mainLogger{
		config: &LogConfig{
			Level:         DEBUG,
			ErrorWindow:   time.Minute,
			MaxErrorCount: 1,
		},
		mu:      &sync.RWMutex{},
		loggers: []*log.Logger{log.New(io.Discard, "", 0)},
	}

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Error() panicked: %v", r)
		}
	}()

	ml.Error("panic: ", panickingError{})
}

type recordingWriter struct {
	mu     sync.Mutex
	writes [][]byte
}

func (w *recordingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	cp := make([]byte, len(p))
	copy(cp, p)
	w.writes = append(w.writes, cp)
	return len(p), nil
}

func (w *recordingWriter) Count() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.writes)
}

func TestMainLoggerFileLoggerSkipsDebugBelowInfo(t *testing.T) {
	stdoutWriter := &recordingWriter{}
	fileWriter := &recordingWriter{}
	ml := &mainLogger{
		config: &LogConfig{
			Level:         DEBUG,
			ErrorWindow:   time.Minute,
			MaxErrorCount: 1,
		},
		mu:         &sync.RWMutex{},
		loggers:    []*log.Logger{log.New(stdoutWriter, "", 0)},
		fileLogger: log.New(fileWriter, "", 0),
	}

	ml.Debug("debug only")
	if stdoutWriter.Count() != 1 {
		t.Fatalf("stdout writer count = %d, want 1", stdoutWriter.Count())
	}
	if fileWriter.Count() != 0 {
		t.Fatalf("file writer count = %d, want 0 for debug", fileWriter.Count())
	}

	ml.Info("info persists")
	if fileWriter.Count() != 1 {
		t.Fatalf("file writer count = %d, want 1 for info", fileWriter.Count())
	}
}

func TestAsyncLogWriterSerializesWritesAndFlushes(t *testing.T) {
	sink := &recordingWriter{}
	writer := newAsyncLogWriter(sink)
	t.Cleanup(func() {
		_ = writer.Close()
	})

	if _, err := writer.Write([]byte("first")); err != nil {
		t.Fatalf("first write failed: %v", err)
	}
	if _, err := writer.Write([]byte("second")); err != nil {
		t.Fatalf("second write failed: %v", err)
	}
	if err := writer.Flush(); err != nil {
		t.Fatalf("flush failed: %v", err)
	}

	if sink.Count() != 2 {
		t.Fatalf("sink write count = %d, want 2", sink.Count())
	}
}
