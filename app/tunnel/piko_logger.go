package tunnel

import (
	"strings"

	"go.uber.org/zap"
)

// pikoStateLogger adapts the piko client logger into tunnel state
// transitions. Piko reconnects internally on retryable failures, so without
// this hook the manager would keep reporting "connected" while the session
// is actually down. Auth failures (401/403) are non-retryable in piko and
// fail the generation through the regular error path, so only network flaps
// surface here as "reconnecting".
type pikoStateLogger struct {
	onState func(state string)
}

func newPikoStateLogger(onState func(state string)) *pikoStateLogger {
	return &pikoStateLogger{onState: onState}
}

func (l *pikoStateLogger) Debug(msg string, _ ...zap.Field) {
	l.report(msg)
}

func (l *pikoStateLogger) Info(string, ...zap.Field) {}

func (l *pikoStateLogger) Warn(msg string, _ ...zap.Field) {
	l.report(msg)
}

func (l *pikoStateLogger) Error(string, ...zap.Field) {}

func (l *pikoStateLogger) Sync() error { return nil }

func (l *pikoStateLogger) report(msg string) {
	if l.onState == nil {
		return
	}
	if state, ok := pikoStateFromLog(msg); ok {
		l.onState(state)
	}
}

// pikoStateFromLog maps piko client log messages to tunnel states. The
// matched strings are piko v0.10.0 message constants (client/listener.go
// "disconnected; reconnecting", client/upstream.go "connected" and
// "connect failed; retrying").
func pikoStateFromLog(msg string) (string, bool) {
	switch {
	case msg == "connected":
		return "connected", true
	case strings.Contains(msg, "reconnect"), strings.Contains(msg, "retrying"):
		return "reconnecting", true
	}
	return "", false
}
