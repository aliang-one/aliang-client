package middleware

import (
	"fmt"
	"net/http"
	"strings"

	"aliang.one/nursorgate/app/http/common"
	"aliang.one/nursorgate/processor/runtime"
)

// StartupStatusMiddleware checks the system's startup status and gates API access accordingly
func StartupStatusMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		// White-list: Configuration-related APIs are always allowed
		if isConfigurationAPI(path) {
			next.ServeHTTP(w, r)
			return
		}

		// Status query API is always allowed
		if path == "/api/run/status" || path == "/api/run/aliang/status" {
			next.ServeHTTP(w, r)
			return
		}

		// All other APIs require a startup state that is ready for proxy operations
		startupState := runtime.GetStartupState()
		status := startupState.GetStatus()

		if !isProxyOperableStatus(status) {
			// System not ready for proxy operations
			if path == "/api/run/start" {
				respondSystemNotReady(w, status)
				return
			}
		}

		next.ServeHTTP(w, r)
	})
}

// isConfigurationAPI checks if the request path is a configuration-related API
// Configuration APIs are allowed regardless of startup status to enable system initialization
func isConfigurationAPI(path string) bool {
	// Configuration endpoints that should always be accessible
	configEndpoints := []string{
		"/api/auth/login",
		"/api/auth/session",
		"/api/auth/refresh",
		"/api/auth/me",
		"/api/auth/logout",
		"/api/auth/scan/init",
		"/api/auth/scan/status",
		"/api/auth/scan/activate",
		"/api/user-center/profile",
		"/api/user-center/usage/summary",
		"/api/user-center/usage/progress",
		"/api/user-center/api-keys",
		"/api/user-center/redeem",
		"/api/dashboard/stats",
		"/api/dashboard/trend",
		"/api/dashboard/models",
		"/api/dashboard/usage",
		"/api/token/get",
		"/api/token/set",
		"/api/software-config/save",
		"/api/software-config/activate",
		"/api/software-config/list",
		"/api/software-config/cloud/push",
		"/api/software-config/cloud/pull",
		"/api/quick-setup/catalog",
		"/api/quick-setup/render",
		"/api/quick-setup/apply",
		"/api/logs", // Logs access (for diagnosis)
		"/api/run/status",
		"/api/run/aliang/status",
		"/api/software-update/status",
		"/api/software-update/dismiss",
		"/api/run/tun/status",
		"/api/run/wintun/install",
		"/api/run/wintun/status",
		"/api/system/service/status",
		"/api/system/service/install",
		"/api/system/service/uninstall",
	}

	for _, endpoint := range configEndpoints {
		if path == endpoint {
			return true
		}
	}

	// Partial match for paths with parameters
	if strings.HasPrefix(path, "/api/logs/") {
		return true
	}

	return false
}

// respondSystemNotReady sends a 503 Service Unavailable response when system is not ready
func respondSystemNotReady(w http.ResponseWriter, status runtime.StartupStatus) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusServiceUnavailable)

	statusStr := string(status)
	errorMsg := fmt.Sprintf("System not ready for proxy operations (status: %s)", statusStr)
	suggestedActions := []string{
		"POST /api/auth/login - Login with user credentials",
		"GET /api/auth/session - Restore local session",
		"GET /api/run/status - Check system status",
	}

	common.Error(w, common.CodeServiceUnavailable, errorMsg, map[string]interface{}{
		"status":            statusStr,
		"suggested_actions": suggestedActions,
	})
}

func isProxyOperableStatus(status runtime.StartupStatus) bool {
	return status == runtime.READY || status == runtime.CONFIGURED
}
