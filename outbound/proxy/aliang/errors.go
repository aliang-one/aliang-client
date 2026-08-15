package aliang

import "fmt"

// Error codes for cursor_h2 operations
const (
	ErrInvalidConfig           = "ERR_INVALID_CONFIG"
	ErrTLSHandshakeFailed      = "ERR_TLS_HANDSHAKE_FAILED"
	ErrProtocolDetectionFailed = "ERR_PROTOCOL_DETECTION_FAILED"
	ErrHTTP2SetupFailed        = "ERR_HTTP2_SETUP_FAILED"
	ErrConnectionPoolFull      = "ERR_CONNECTION_POOL_FULL"
	ErrConnectionTimeout       = "ERR_CONNECTION_TIMEOUT"
	ErrStreamAllocationFailed  = "ERR_STREAM_ALLOCATION_FAILED"
	ErrTokenProviderError      = "ERR_TOKEN_PROVIDER_ERROR"
	ErrFrameInterceptionFailed = "ERR_FRAME_INTERCEPTION_FAILED"
)

// Error represents a cursor_h2 error with context
type Error struct {
	Code    string
	Message string
	Cause   error
}

// Error implements the error interface
func (e *Error) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s: %s (cause: %v)", e.Code, e.Message, e.Cause)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// Unwrap returns the underlying cause
func (e *Error) Unwrap() error {
	return e.Cause
}

// NewError creates a new cursor_h2 error
func NewError(code string, message string) *Error {
	return &Error{
		Code:    code,
		Message: message,
		Cause:   nil,
	}
}

// NewErrorf creates a new cursor_h2 error with formatted message
func NewErrorf(code string, format string, args ...interface{}) *Error {
	return &Error{
		Code:    code,
		Message: fmt.Sprintf(format, args...),
		Cause:   nil,
	}
}

// NewErrorWithCause creates a new cursor_h2 error with an underlying cause
func NewErrorWithCause(code string, message string, cause error) *Error {
	return &Error{
		Code:    code,
		Message: message,
		Cause:   cause,
	}
}

// IsError checks if an error is a cursor_h2 error with specific code
func IsError(err error, code string) bool {
	if e, ok := err.(*Error); ok {
		return e.Code == code
	}
	return false
}
