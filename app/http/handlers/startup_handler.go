package handlers

import (
	"net/http"

	"aliang.one/nursorgate/app/http/common"
	authuser "aliang.one/nursorgate/processor/auth"
	"aliang.one/nursorgate/processor/runtime"
)

// StartupHandler handles system startup status queries
type StartupHandler struct{}

// NewStartupHandler creates a new startup handler instance
func NewStartupHandler() *StartupHandler {
	return &StartupHandler{}
}

// HandleStartupStatus handles GET /api/startup/status
// Returns the current system startup status and related information
func (h *StartupHandler) HandleStartupStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		common.Error(w, common.CodeBadRequest, "Method not allowed", nil)
		return
	}

	startupState := runtime.GetStartupState()
	status := startupState.GetStatus()
	fetchSuccess := startupState.GetFetchSuccess()
	userInfo := authuser.GetCurrentUserInfoOrLoad()

	// Build response data
	response := map[string]interface{}{
		"status":        string(status),
		"fetch_success": fetchSuccess,
		"timestamp":     startupState.GetTimestamp().Unix(),
	}

	// Add user info if available
	if userInfo != nil {
		response["user"] = map[string]interface{}{
			"id":              userInfo.ID,
			"username":        userInfo.Username,
			"email":           userInfo.Email,
			"status":          userInfo.Status,
			"balance":         userInfo.Balance,
			"concurrency":     userInfo.Concurrency,
			"created_at":      userInfo.CreatedAt,
			"profile_updated": userInfo.ProfileUpdated,
		}
	}

	// Add helpful information about status transitions
	response["transitions"] = getStatusTransitionInfo(status)

	common.Success(w, response)
}

// HandleStartupDetail handles GET /api/startup/detail
// Returns detailed startup diagnostic information
func (h *StartupHandler) HandleStartupDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		common.Error(w, common.CodeBadRequest, "Method not allowed", nil)
		return
	}

	startupState := runtime.GetStartupState()
	status := startupState.GetStatus()
	fetchSuccess := startupState.GetFetchSuccess()
	userInfo := authuser.GetCurrentUserInfoOrLoad()

	hasLocalUserInfo, checkErr := authuser.HasPersistedUserInfo()
	if checkErr != nil {
		hasLocalUserInfo = false
	}

	authStoragePath, pathErr := authuser.GetAuthSessionDBPath()
	if pathErr != nil {
		authStoragePath = ""
	}

	// Build detailed response
	response := map[string]interface{}{
		"status": map[string]interface{}{
			"current":     string(status),
			"description": getStatusDescription(status),
		},
		"fetch": map[string]interface{}{
			"success": fetchSuccess,
			"message": getFetchStatusMessage(fetchSuccess),
		},
		"user": map[string]interface{}{
			"has_local_info": hasLocalUserInfo,
			"info_path":      authStoragePath,
		},
		"diagnostics": map[string]interface{}{
			"timestamp":              startupState.GetTimestamp().Unix(),
			"suggested_next_actions": getSuggestedActions(status),
		},
	}

	// Add user details if available
	if userInfo != nil {
		response["user"].(map[string]interface{})["details"] = map[string]interface{}{
			"id":              userInfo.ID,
			"username":        userInfo.Username,
			"email":           userInfo.Email,
			"role":            userInfo.Role,
			"status":          userInfo.Status,
			"balance":         userInfo.Balance,
			"concurrency":     userInfo.Concurrency,
			"allowed_groups":  userInfo.AllowedGroups,
			"created_at":      userInfo.CreatedAt,
			"profile_updated": userInfo.ProfileUpdated,
		}
	}

	common.Success(w, response)
}

// getStatusDescription returns a human-readable description of the status
func getStatusDescription(status runtime.StartupStatus) string {
	descriptions := map[runtime.StartupStatus]string{
		runtime.UNCONFIGURED: "System awaiting authentication - no local session found",
		runtime.CONFIGURING:  "System authenticating - login/session restore in progress",
		runtime.CONFIGURED:   "System configured - user info loaded but not started",
		runtime.READY:        "System ready - user authenticated",
	}

	if desc, ok := descriptions[status]; ok {
		return desc
	}
	return "Unknown status"
}

// getFetchStatusMessage returns a message about fetch status
func getFetchStatusMessage(success bool) string {
	if success {
		return "User authenticated"
	}
	return "User not authenticated"
}

// getSuggestedActions returns suggested actions based on current status
func getSuggestedActions(status runtime.StartupStatus) []string {
	actions := map[runtime.StartupStatus][]string{
		runtime.UNCONFIGURED: {
			"POST /api/auth/login - Login with email/password",
			"POST /api/auth/scan/init - Scan to sign in with the mobile app",
			"GET /api/auth/session - Try local session restore",
			"GET /api/startup/status - Check status again",
		},
		runtime.CONFIGURING: {
			"GET /api/auth/session - Retry local session restore",
			"POST /api/auth/login - Login if no local session",
			"GET /api/startup/status - Check authentication progress",
		},
		runtime.CONFIGURED: {
			"System has authenticated user info but proxy not started",
			"POST /api/run/start - Start proxy",
			"POST /api/auth/refresh - Refresh auth session",
		},
		runtime.READY: {
			"System ready for proxy operations",
			"GET /api/proxy/list - List available proxies",
			"GET /api/auth/me - Check user information",
		},
	}

	if actions, ok := actions[status]; ok {
		return actions
	}
	return []string{}
}

// getStatusTransitionInfo returns information about possible status transitions
func getStatusTransitionInfo(status runtime.StartupStatus) map[string]interface{} {
	transitions := map[runtime.StartupStatus]map[string]interface{}{
		runtime.UNCONFIGURED: {
			"description": "Initial state - no authenticated session",
			"possible_transitions": []string{
				"→ CONFIGURING (authentication session initialization)",
				"→ READY (local session restored or login successful)",
			},
		},
		runtime.CONFIGURING: {
			"description": "Authentication in progress",
			"possible_transitions": []string{
				"→ READY (session restore or login success)",
				"→ UNCONFIGURED (authentication failed, no local session)",
			},
		},
		runtime.CONFIGURED: {
			"description": "User info available but proxy not started",
			"possible_transitions": []string{
				"→ READY (start proxy)",
				"→ UNCONFIGURED (user info deleted)",
			},
		},
		runtime.READY: {
			"description": "System ready for proxy operations",
			"possible_transitions": []string{
				"→ CONFIGURED (user logs out)",
				"→ UNCONFIGURED (user logout or info deleted)",
			},
		},
	}

	if info, ok := transitions[status]; ok {
		return info
	}
	return map[string]interface{}{}
}
