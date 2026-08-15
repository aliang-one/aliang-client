package logger

import "context"

// Logger is a unified logging interface for all logging needs
type Logger interface {
	// Basic logging methods
	Debug(v ...interface{})
	Info(v ...interface{})
	Warn(v ...interface{})
	Error(v ...interface{})

	// Context-aware logging for connection tracing
	DebugContext(ctx context.Context, v ...interface{})
	InfoContext(ctx context.Context, v ...interface{})
	WarnContext(ctx context.Context, v ...interface{})
	ErrorContext(ctx context.Context, v ...interface{})

	// For sing-box compatibility
	Trace(v ...interface{})
	Fatal(v ...interface{})
	Panic(v ...interface{})
	TraceContext(ctx context.Context, v ...interface{})
	FatalContext(ctx context.Context, v ...interface{})
	PanicContext(ctx context.Context, v ...interface{})

	// Management
	Flush()
	WithContext(ctx context.Context) Logger
}
