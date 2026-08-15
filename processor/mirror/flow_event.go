package mirror

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"strings"
	"time"
)

// FlowEventType distinguishes lifecycle events from data chunks.
type FlowEventType string

const (
	FlowEventStart FlowEventType = "flow_start"
	FlowEventEnd   FlowEventType = "flow_end"
)

// FlowEvent represents a flow lifecycle event (start or end).
// On the wire it shares the same HTTP POST channel as StreamChunk;
// the receiver uses event_type to discriminate.
type FlowEvent struct {
	EventType    FlowEventType `json:"event_type"`
	FlowID       string        `json:"flow_id"`
	ConnID       string        `json:"conn_id,omitempty"`
	Timestamp    int64         `json:"timestamp"`
	ClientAddr   string        `json:"client_addr"`
	UpstreamAddr string        `json:"upstream_addr"`
	ProtocolHint string        `json:"protocol_hint,omitempty"`
	HostName     string        `json:"host_name,omitempty"`

	// flow_end fields
	ClientToServerBytes int64  `json:"client_to_server_bytes,omitempty"`
	ServerToClientBytes int64  `json:"server_to_client_bytes,omitempty"`
	DurationMs          int64  `json:"duration_ms,omitempty"`
	Error               string `json:"error,omitempty"`
	ErrorClass          string `json:"error_class,omitempty"`
}

// MirrorMessage is the union type for messages sent through the forwarder channel.
type MirrorMessage interface{ isMirrorMessage() }

func (*StreamChunk) isMirrorMessage() {}
func (*FlowEvent) isMirrorMessage()    {}

// classifyError returns a human-readable error class for flow_end events.
func classifyError(err error) string {
	if err == nil {
		return "clean"
	}

	// Clean close
	if errors.Is(err, net.ErrClosed) || errors.Is(err, context.Canceled) {
		return "clean"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}

	msg := err.Error()

	// TLS errors
	var tlsErr *tls.CertificateVerificationError
	if errors.As(err, &tlsErr) {
		return "tls_error"
	}
	if strings.Contains(msg, "tls:") || strings.Contains(msg, "certificate") || strings.Contains(msg, "handshake") {
		return "tls_error"
	}

	// Connection reset
	if strings.Contains(msg, "connection reset") || strings.Contains(msg, "RST") {
		return "reset"
	}

	// Timeout
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "timeout"
	}
	if strings.Contains(msg, "timeout") || strings.Contains(msg, "deadline exceeded") {
		return "timeout"
	}

	// Context cancel
	if errors.Is(err, context.Canceled) {
		return "context_cancel"
	}
	if strings.Contains(msg, "context canceled") {
		return "context_cancel"
	}

	return "unknown"
}

// nowUnixMilli returns current time as Unix milliseconds.
func nowUnixMilli() int64 {
	return time.Now().UnixMilli()
}
