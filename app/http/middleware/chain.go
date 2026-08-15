package middleware

import "net/http"

// Chain applies multiple middleware handlers in sequence
// Middleware is applied in reverse order, so the first middleware in the list
// will be the outermost (last to execute on request, first to execute on response)
func Chain(handler http.Handler, middlewares ...func(http.Handler) http.Handler) http.Handler {
	// Apply middleware in reverse order
	for i := len(middlewares) - 1; i >= 0; i-- {
		handler = middlewares[i](handler)
	}
	return handler
}

// ChainHandlerFunc applies multiple middleware handlers to a handler function
func ChainHandlerFunc(handler http.HandlerFunc, middlewares ...func(http.Handler) http.Handler) http.Handler {
	return Chain(handler, middlewares...)
}

// GetDefaultMiddleware returns the default middleware stack for all routes
func GetDefaultMiddleware() []func(http.Handler) http.Handler {
	return []func(http.Handler) http.Handler{
		TracingMiddleware,       // Outermost - first to process requests
		StartupStatusMiddleware, // Gate APIs based on system startup status
		LoggingMiddleware,       // Log all requests/responses
		RecoveryMiddleware,      // Innermost - catch panics
	}
}
