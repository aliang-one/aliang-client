package middleware

import (
	"context"
	"net/http"

	"github.com/google/uuid"
)

const (
	// TraceIDHeader is the header name for trace ID
	TraceIDHeader = "X-Trace-ID"
	// TraceIDContextKey is the context key for trace ID
	TraceIDContextKey = "trace_id"
)

// TracingMiddleware generates or extracts trace ID and adds it to context and response headers
func TracingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Try to get trace ID from request headers
		traceID := r.Header.Get(TraceIDHeader)

		// If not provided, generate a new trace ID
		if traceID == "" {
			traceID = uuid.New().String()
		}

		// Add trace ID to response header
		w.Header().Set(TraceIDHeader, traceID)

		// Add trace ID to request context for use in handlers
		ctx := context.WithValue(r.Context(), TraceIDContextKey, traceID)
		r = r.WithContext(ctx)

		next.ServeHTTP(w, r)
	})
}

// GetTraceIDFromContext extracts trace ID from request context
func GetTraceIDFromContext(r *http.Request) string {
	if traceID, ok := r.Context().Value(TraceIDContextKey).(string); ok {
		return traceID
	}
	return ""
}
