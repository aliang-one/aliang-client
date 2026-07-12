package middleware

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"time"

	"aliang.one/nursorgate/common/logger"
)

// LoggingMiddleware logs incoming requests and outgoing responses
func LoggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Record request start time
		startTime := time.Now()

		// Create a response writer wrapper to capture status code
		wrappedWriter := &responseWriterWrapper{
			ResponseWriter: w,
			statusCode:     http.StatusOK,
		}

		// Log request
		logger.HttpTrace(fmt.Sprintf("[HTTP] %s %s from %s", r.Method, r.RequestURI, r.RemoteAddr))

		// Call next handler
		next.ServeHTTP(wrappedWriter, r)

		// Calculate elapsed time
		elapsed := time.Since(startTime)

		// Log response
		logger.HttpTrace(fmt.Sprintf("[HTTP] %s %s - Status: %d - Duration: %v",
			r.Method, r.RequestURI, wrappedWriter.statusCode, elapsed))
	})
}

// responseWriterWrapper wraps http.ResponseWriter to capture status code
type responseWriterWrapper struct {
	http.ResponseWriter
	statusCode int
}

// WriteHeader captures the status code
func (w *responseWriterWrapper) WriteHeader(statusCode int) {
	w.statusCode = statusCode
	w.ResponseWriter.WriteHeader(statusCode)
}

// Hijack implements http.Hijacker interface for WebSocket support
func (w *responseWriterWrapper) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if hijacker, ok := w.ResponseWriter.(http.Hijacker); ok {
		return hijacker.Hijack()
	}
	return nil, nil, fmt.Errorf("underlying ResponseWriter does not implement http.Hijacker")
}

// Flush implements http.Flusher so SSE/streaming handlers (which key off
// w.(http.Flusher)) work through this wrapper. Without it the wrapper only
// promotes the embedded interface's methods (Header/Write/WriteHeader) and the
// Flusher assertion fails -> "streaming unsupported" 500.
func (w *responseWriterWrapper) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// compile-time guarantees: the wrapper must satisfy the interfaces it delegates.
var (
	_ http.Flusher  = (*responseWriterWrapper)(nil)
	_ http.Hijacker = (*responseWriterWrapper)(nil)
)
