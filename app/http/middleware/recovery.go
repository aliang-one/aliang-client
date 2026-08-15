package middleware

import (
	"fmt"
	"net/http"
	"runtime/debug"

	"aliang.one/nursorgate/app/http/common"
	"aliang.one/nursorgate/common/logger"
)

// RecoveryMiddleware recovers from panics and returns a 500 error with proper response format
func RecoveryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if err := recover(); err != nil {
				// Log the panic with stack trace
				logger.Error(fmt.Sprintf("Panic recovered: %v\nStack trace:\n%s", err, debug.Stack()))

				// Return error response
				common.Error(w, common.CodeInternalServer, "Internal server error", map[string]string{
					"error": fmt.Sprintf("%v", err),
				})
			}
		}()

		next.ServeHTTP(w, r)
	})
}
