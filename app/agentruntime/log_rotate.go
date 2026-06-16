package agentruntime

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// agentLog rotation bounds the user-agent subprocess stdout/stderr capture
// (agent.log). The agent is a long-running process (relaunched on demand, often
// kept alive across app sessions), so an unbounded append-only log would grow
// without limit. These bounds keep the current file plus a small set of backups.
const (
	agentLogMaxSize    int64 = 10 * 1024 * 1024 // rotate once the current file passes 10 MiB
	agentLogMaxBackups       = 3                // keep agent.log plus .1/.2/.3
)

// rotatingLogWriter is an io.Writer that caps a log file at maxSize, keeping up
// to backups rotated copies (path, path.1, ..., path.backups). It backs the
// agent subprocess's stdout/stderr: because os/exec copies a non-*os.File
// writer from a pipe in a goroutine it owns, the supervisor controls rotation
// here while the child keeps running. The writer must stay open for the child's
// lifetime, so the launcher intentionally does not close it from EnsureStarted;
// it is reclaimed once the child exits and the pipe copy goroutine stops.
type rotatingLogWriter struct {
	mu      sync.Mutex
	path    string
	f       *os.File
	written int64
	maxSize int64
	backups int
}

func newRotatingLogWriter(dir string, baseName string, maxSize int64, backups int) (*rotatingLogWriter, error) {
	path := filepath.Join(dir, baseName)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	w := &rotatingLogWriter{
		path:    path,
		f:       f,
		maxSize: maxSize,
		backups: backups,
	}
	if info, err := f.Stat(); err == nil {
		w.written = info.Size()
	}
	return w, nil
}

func (w *rotatingLogWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return 0, fmt.Errorf("agent log writer closed")
	}
	n, err := w.f.Write(p)
	w.written += int64(n)
	if err != nil {
		return n, err
	}
	if w.maxSize > 0 && w.written >= w.maxSize {
		w.rotateLocked()
	}
	return n, nil
}

// rotateLocked closes the current file, shifts each backup up by one (the
// oldest is dropped by os.Rename replacing it), and opens a fresh file.
func (w *rotatingLogWriter) rotateLocked() {
	if w.f != nil {
		_ = w.f.Close()
		w.f = nil
	}
	for i := w.backups; i > 1; i-- {
		_ = os.Rename(w.backupPath(i-1), w.backupPath(i))
	}
	if w.backups >= 1 {
		_ = os.Rename(w.path, w.backupPath(1))
	}
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	w.f = f
	w.written = 0
}

func (w *rotatingLogWriter) backupPath(i int) string {
	return fmt.Sprintf("%s.%d", w.path, i)
}

func (w *rotatingLogWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	err := w.f.Close()
	w.f = nil
	return err
}
