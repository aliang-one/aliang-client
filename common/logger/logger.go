package logger

import (
	"context"
	"crypto/md5"
	"fmt"
	"io"
	"log"
	"os"
	"sync"
	"time"

	"github.com/getsentry/sentry-go"
	"gopkg.in/natefinch/lumberjack.v2"
)

var (
	errorCache   = make(map[string]*errorInfo)
	errorCacheMu sync.RWMutex
	cleanupTick  *time.Ticker
	cleanupDone  chan bool
)

type errorInfo struct {
	Count     int
	FirstSeen time.Time
	LastSeen  time.Time
}

// mainLogger implements the Logger interface
type mainLogger struct {
	config     *LogConfig
	writers    []io.Writer
	mu         *sync.RWMutex
	loggers    []*log.Logger
	fileLogger *log.Logger
	fileSink   *asyncLogWriter
}

// initLoggers initializes the loggers with rotation support
func (ml *mainLogger) initLoggers() {
	ml.mu.Lock()
	defer ml.mu.Unlock()

	if len(ml.loggers) > 0 {
		return // Already initialized
	}

	var writers []io.Writer

	// Always add stdout for synchronous console visibility.
	writers = append(writers, os.Stdout)
	ml.writers = writers

	logger := log.New(os.Stdout, "", log.LstdFlags|log.Lshortfile)
	ml.loggers = append(ml.loggers, logger)

	if fileWriter := ml.newFileWriter(); fileWriter != nil {
		ml.fileSink = newAsyncLogWriter(fileWriter)
		ml.fileLogger = log.New(ml.fileSink, "", log.LstdFlags|log.Lshortfile)
	}
}

func (ml *mainLogger) newFileWriter() io.Writer {
	if ml.config == nil || ml.config.FileLogPath == "" {
		return nil
	}

	if ml.config.EnableFileRotation {
		return &lumberjack.Logger{
			Filename:   ml.config.FileLogPath,
			MaxSize:    int(ml.config.MaxLogSize / 1024 / 1024),
			MaxBackups: ml.config.MaxLogBackups,
			Compress:   true,
		}
	}

	file, err := os.OpenFile(ml.config.FileLogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if err != nil {
		return nil
	}
	_ = os.Chmod(ml.config.FileLogPath, 0666)
	return file
}

func (ml *mainLogger) Debug(v ...interface{}) {
	if ml.config.Level > DEBUG {
		return
	}
	ml.logf(DEBUG, "DEBUG", v...)
}

func (ml *mainLogger) Info(v ...interface{}) {
	if ml.config.Level > INFO {
		return
	}
	ml.logf(INFO, "INFO", v...)
}

func (ml *mainLogger) Warn(v ...interface{}) {
	if ml.config.Level > WARN {
		return
	}
	ml.logf(WARN, "WARN", v...)
}

func (ml *mainLogger) Error(v ...interface{}) {
	if ml.config.Level > ERROR {
		return
	}
	msg := SafeSprint(v...)
	ml.logf(ERROR, "ERROR", msg)

	// Error deduplication and Sentry
	errHash := ml.generateErrorHash(msg)
	if ml.shouldSendError(errHash) && ml.config.EnableSentry {
		sentry.WithScope(func(scope *sentry.Scope) {
			scope.SetTag("source", "mainLogger")
			scope.SetExtra("raw_args", msg)
			sentry.CaptureMessage(msg)
		})
		go sentry.Flush(2 * time.Second)
	}
}

func (ml *mainLogger) Trace(v ...interface{}) {
	if ml.config.Level > TRACE {
		return
	}
	ml.logf(TRACE, "TRACE", v...)
}

func (ml *mainLogger) Fatal(v ...interface{}) {
	ml.logf(ERROR, "FATAL", v...)
	os.Exit(1)
}

func (ml *mainLogger) Panic(v ...interface{}) {
	msg := SafeSprint(v...)
	ml.logf(ERROR, "PANIC", v...)
	panic(msg)
}

// Context variants — currently context is not utilized, kept for interface compatibility
func (ml *mainLogger) DebugContext(ctx context.Context, v ...interface{}) {
	ml.Debug(v...)
}

// Context variants — currently context is not utilized, kept for interface compatibility
func (ml *mainLogger) InfoContext(ctx context.Context, v ...interface{}) {
	ml.Info(v...)
}

// Context variants — currently context is not utilized, kept for interface compatibility
func (ml *mainLogger) WarnContext(ctx context.Context, v ...interface{}) {
	ml.Warn(v...)
}

// Context variants — currently context is not utilized, kept for interface compatibility
func (ml *mainLogger) ErrorContext(ctx context.Context, v ...interface{}) {
	ml.Error(v...)
}

// Context variants — currently context is not utilized, kept for interface compatibility
func (ml *mainLogger) TraceContext(ctx context.Context, v ...interface{}) {
	ml.Trace(v...)
}

// Context variants — currently context is not utilized, kept for interface compatibility
func (ml *mainLogger) FatalContext(ctx context.Context, v ...interface{}) {
	ml.Fatal(v...)
}

// Context variants — currently context is not utilized, kept for interface compatibility
func (ml *mainLogger) PanicContext(ctx context.Context, v ...interface{}) {
	ml.Panic(v...)
}

// WithContext returns a logger with context.
// NOTE: context is currently not utilized; this is kept for interface compatibility.
func (ml *mainLogger) WithContext(ctx context.Context) Logger {
	return ml
}

// Flush flushes all writers
func (ml *mainLogger) Flush() {
	if ml.fileSink != nil {
		_ = ml.fileSink.Flush()
	}
}

// logf formats and logs a message
func (ml *mainLogger) logf(level LogLevelType, prefix string, v ...interface{}) {
	ml.initLoggers()
	ml.mu.RLock()
	defer ml.mu.RUnlock()

	message := SafeSprint(v...)
	for _, logger := range ml.loggers {
		safeLoggerOutput(logger, 3, fmt.Sprintf("[%s] %s\n", prefix, message))
	}
	if level >= INFO && ml.fileLogger != nil {
		safeLoggerOutput(ml.fileLogger, 3, fmt.Sprintf("[%s] %s\n", prefix, message))
	}

	AppendToBuffer(&LogEntry{
		Level:     level,
		Timestamp: time.Now(),
		Message:   message,
		Source:    "main",
	})
}

func (ml *mainLogger) generateErrorHash(message string) string {
	h := md5.New()
	fmt.Fprint(h, message)
	return fmt.Sprintf("%x", h.Sum(nil))
}

func (ml *mainLogger) shouldSendError(hash string) bool {
	errorCacheMu.Lock()
	defer errorCacheMu.Unlock()

	now := time.Now()
	if info, exists := errorCache[hash]; exists {
		if now.Sub(info.FirstSeen) <= ml.config.ErrorWindow && info.Count < ml.config.MaxErrorCount {
			info.Count++
			info.LastSeen = now
			return true
		}
		info.LastSeen = now
		return false
	}
	errorCache[hash] = &errorInfo{Count: 1, FirstSeen: now, LastSeen: now}
	return true
}

func init() {
	startCleanupRoutineOnce()
}

var cleanupOnce sync.Once

func startCleanupRoutineOnce() {
	cleanupOnce.Do(func() {
		startCleanupRoutine()
	})
}

func Shutdown() {
	if ml, ok := mainLoggerInstance.(*mainLogger); ok && ml != nil {
		ml.Flush()
		if ml.fileSink != nil {
			_ = ml.fileSink.Close()
		}
	}
	if hl, ok := httpLoggerInstance.(*httpLogger); ok && hl != nil {
		hl.Flush()
		if hl.fileSink != nil {
			_ = hl.fileSink.Close()
		}
	}
	if cleanupTick != nil {
		cleanupTick.Stop()
		close(cleanupDone)
	}
}

func startCleanupRoutine() {
	cleanupTick = time.NewTicker(1 * time.Minute)
	cleanupDone = make(chan bool)

	go func() {
		for {
			select {
			case <-cleanupTick.C:
				cleanupExpiredErrors()
			case <-cleanupDone:
				return
			}
		}
	}()
}

func cleanupExpiredErrors() {
	errorCacheMu.Lock()
	defer errorCacheMu.Unlock()

	now := time.Now()
	for k, v := range errorCache {
		if now.Sub(v.LastSeen) > GetLogConfig().ErrorWindow {
			delete(errorCache, k)
		}
	}
}

// Backward compatible global logging functions
func Debug(v ...interface{}) {
	GetMainLogger().Debug(v...)
}

func Info(v ...interface{}) {
	GetMainLogger().Info(v...)
}

func Warn(v ...interface{}) {
	GetMainLogger().Warn(v...)
}

func Error(v ...interface{}) {
	GetMainLogger().Error(v...)
}

func SetUserInfo(userIdentity string) {
	sentry.ConfigureScope(func(scope *sentry.Scope) {
		scope.SetTag("session_user", userIdentity)
	})
}
