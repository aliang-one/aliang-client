package logger

import (
	"errors"
	"io"
	"sync"
)

const defaultAsyncLogQueueSize = 1024

type asyncLogWriter struct {
	writer io.Writer

	queue chan asyncLogItem

	wg sync.WaitGroup

	closeOnce sync.Once
	mu        sync.RWMutex
	closed    bool
}

type asyncLogItem struct {
	data  []byte
	flush chan struct{}
}

func newAsyncLogWriter(writer io.Writer) *asyncLogWriter {
	if writer == nil {
		return nil
	}

	w := &asyncLogWriter{
		writer: writer,
		queue:  make(chan asyncLogItem, defaultAsyncLogQueueSize),
	}
	w.wg.Add(1)
	go w.run()
	return w
}

func (w *asyncLogWriter) Write(p []byte) (int, error) {
	if w == nil {
		return 0, io.ErrClosedPipe
	}

	w.mu.RLock()
	closed := w.closed
	w.mu.RUnlock()
	if closed {
		return 0, io.ErrClosedPipe
	}

	cp := make([]byte, len(p))
	copy(cp, p)

	defer func() {
		_ = recover()
	}()
	w.queue <- asyncLogItem{data: cp}
	return len(p), nil
}

func (w *asyncLogWriter) Flush() error {
	if w == nil {
		return nil
	}

	w.mu.RLock()
	closed := w.closed
	w.mu.RUnlock()
	if closed {
		return nil
	}

	flushCh := make(chan struct{})
	defer func() {
		_ = recover()
	}()
	w.queue <- asyncLogItem{flush: flushCh}
	<-flushCh

	if flusher, ok := w.writer.(interface{ Flush() error }); ok {
		return flusher.Flush()
	}
	if syncer, ok := w.writer.(interface{ Sync() error }); ok {
		return syncer.Sync()
	}
	return nil
}

func (w *asyncLogWriter) Close() error {
	if w == nil {
		return nil
	}

	var closeErr error
	w.closeOnce.Do(func() {
		_ = w.Flush()

		w.mu.Lock()
		w.closed = true
		close(w.queue)
		w.mu.Unlock()

		w.wg.Wait()

		if closer, ok := w.writer.(io.Closer); ok {
			closeErr = closer.Close()
		}
	})
	return closeErr
}

func (w *asyncLogWriter) run() {
	defer w.wg.Done()

	for item := range w.queue {
		if len(item.data) > 0 {
			_, _ = w.writer.Write(item.data)
		}
		if item.flush != nil {
			close(item.flush)
		}
	}
}

var errAsyncWriterClosed = errors.New("async log writer is closed")
