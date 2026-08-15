package logger

import (
	"context"
	"os"
)

// SingBoxLogger is now a type alias for the unified Logger interface
// for backward compatibility with sing-box integration code
type SingBoxLogger = Logger

// NewSingBoxLogger creates a new SingBox-compatible logger
// This is for backward compatibility - factory.go provides the actual implementation
func NewSingBoxLogger() Logger {
	// GetSingBoxLogger() is defined in factory.go and uses sync.Once for thread safety
	return GetSingBoxLogger()
}

// NewSingBoxLoggerWithPrefix creates a SingBox logger with custom prefix
func NewSingBoxLoggerWithPrefix(prefix string) Logger {
	// Prefix is ignored - global singleton from factory
	return GetSingBoxLogger()
}

// SilentLogger is a logger that discards all output
type SilentLogger struct{}

func (l *SilentLogger) Trace(args ...interface{})                             {}
func (l *SilentLogger) Debug(args ...interface{})                             {}
func (l *SilentLogger) Info(args ...interface{})                              {}
func (l *SilentLogger) Warn(args ...interface{})                              {}
func (l *SilentLogger) Error(args ...interface{})                             {}
func (l *SilentLogger) Fatal(args ...interface{})                             { os.Exit(1) }
func (l *SilentLogger) Panic(args ...interface{})                             { panic("silent panic") }
func (l *SilentLogger) TraceContext(ctx context.Context, args ...interface{}) {}
func (l *SilentLogger) DebugContext(ctx context.Context, args ...interface{}) {}
func (l *SilentLogger) InfoContext(ctx context.Context, args ...interface{})  {}
func (l *SilentLogger) WarnContext(ctx context.Context, args ...interface{})  {}
func (l *SilentLogger) ErrorContext(ctx context.Context, args ...interface{}) {}
func (l *SilentLogger) FatalContext(ctx context.Context, args ...interface{}) { os.Exit(1) }
func (l *SilentLogger) PanicContext(ctx context.Context, args ...interface{}) { panic("silent panic") }
func (l *SilentLogger) WithContext(ctx context.Context) Logger                { return l }
func (l *SilentLogger) Flush()                                                {}

// NewSilentLogger creates a new silent logger
func NewSilentLogger() Logger {
	return &SilentLogger{}
}
