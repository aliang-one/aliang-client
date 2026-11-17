package logger

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// failingWriter 恒败写器,模拟 lumberjack 轮转永久失败窗口。
type failingWriter struct{ fails int }

func (w *failingWriter) Write(p []byte) (int, error) {
	w.fails++
	return 0, errors.New("rotate failed: rename requires target permit existing")
}

// recoverableWriter 前 fails 次失败、之后成功,模拟对方进程退出后轮转恢复。
type recoverableWriter struct {
	mu    sync.Mutex
	fails int
	n     int
}

func (w *recoverableWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.fails > 0 {
		w.fails--
		return 0, errors.New("rotate failed")
	}
	w.n++
	return len(p), nil
}

func (w *recoverableWriter) writes() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.n
}

func writeLog(t *testing.T, w *rotateResilientWriter, line string) {
	t.Helper()
	if _, err := w.Write([]byte(line + "\n")); err != nil {
		t.Fatalf("write should be absorbed by fallback: %v", err)
	}
}

func TestRotateResilientFallbackKeepsMessagesOnDisk(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "aliang_core.log")

	primary := &failingWriter{}
	w := newRotateResilientWriter(primary, path)
	writeLog(t, w, "line-1")
	writeLog(t, w, "line-2")

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("fallback file not written: %v", err)
	}
	if !strings.Contains(string(data), "line-1") || !strings.Contains(string(data), "line-2") {
		t.Fatalf("messages lost during degraded window: %q", string(data))
	}
	// 主路确实被尝试过(每次 Write 都重试轮转,这是 lumberjack 的内建语义)。
	if primary.fails < 2 {
		t.Fatalf("primary must be retried on every write, got %d attempts", primary.fails)
	}
}

func TestRotateResilientWritesDegradedBreadcrumb(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "aliang_core.log")

	w := newRotateResilientWriter(&failingWriter{}, path)
	writeLog(t, w, "msg")

	breadcrumb := filepath.Join(dir, breadcrumbName)
	data, err := os.ReadFile(breadcrumb)
	if err != nil {
		t.Fatalf("breadcrumb not written: %v", err)
	}
	if !strings.Contains(string(data), "file logger degraded") {
		t.Fatalf("breadcrumb missing degraded notice: %q", string(data))
	}
}

func TestRotateResilientThrottlesBreadcrumbs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "aliang_core.log")

	w := newRotateResilientWriter(&failingWriter{}, path)
	for i := 0; i < 5; i++ {
		writeLog(t, w, "msg")
	}

	breadcrumb := filepath.Join(dir, breadcrumbName)
	data, err := os.ReadFile(breadcrumb)
	if err != nil {
		t.Fatalf("breadcrumb not written: %v", err)
	}
	if got := strings.Count(string(data), "file logger degraded"); got != 1 {
		t.Fatalf("breadcrumb must throttle to first notice, got %d", got)
	}
}

func TestRotateResilientRecoversToPrimary(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "aliang_core.log")

	primary := &recoverableWriter{fails: 2}
	w := newRotateResilientWriter(primary, path)

	// 窗口内两条消息走兜底(主路各失败一次,还剩一次失败预算)。
	writeLog(t, w, "degraded-1")
	writeLog(t, w, "degraded-2")
	// 对方进程退出(预算耗尽),主路恢复。
	writeLog(t, w, "recovered")
	writeLog(t, w, "recovered-2")

	if primary.writes() != 2 {
		t.Fatalf("primary must resume after recovery, got %d writes", primary.writes())
	}

	breadcrumb := filepath.Join(dir, breadcrumbName)
	data, err := os.ReadFile(breadcrumb)
	if err != nil {
		t.Fatalf("breadcrumb not written: %v", err)
	}
	if !strings.Contains(string(data), "file logger recovered") {
		t.Fatalf("recovery breadcrumb missing: %q", string(data))
	}
}

func TestRotateResilientConcurrentWritesSafe(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "aliang_core.log")

	primary := &recoverableWriter{fails: 3}
	w := newRotateResilientWriter(primary, path)

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := w.Write([]byte("concurrent\n")); err != nil && i < 12 {
				// 前期主路失败且兜底不应失败;此处只防 panic。
				t.Logf("write returned error: %v", err)
			}
		}(i)
	}
	wg.Wait()
}
