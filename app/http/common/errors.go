package common

import "net/http"

// Business error codes definition
const (
	// Success
	CodeSuccess = 0

	// Client errors (100-199)
	CodeBadRequest   = 100
	CodeUnauthorized = 101
	CodeForbidden    = 102
	CodeNotFound     = 103
	CodeConflict     = 104

	// Logger errors (110-119)
	CodeInvalidLogLevel       = 110
	CodeInvalidLogConfig      = 111
	CodeLogServiceUnavailable = 112

	// Proxy errors (120-129)
	CodeProxyNotFound      = 120
	CodeProxyAlreadyExists = 121
	CodeInvalidProxyConfig = 122

	// Run/Mode errors (130-139)
	CodeInvalidRunMode        = 130
	CodeModeTransitionFailed  = 131
	CodeServiceAlreadyRunning = 132
	CodeServiceNotRunning     = 133

	// Config errors (140-149)
	CodeInvalidConfigValue = 140
	CodeConfigNotFound     = 141
	CodeConfigUpdateFailed = 142

	// Token errors (150-159)
	CodeInvalidToken = 150
	CodeTokenExpired = 151

	// Server errors (200-299)
	CodeInternalServer      = 200
	CodeServiceUnavailable  = 201
	CodeDatabaseError       = 202
	CodeExternalServiceFail = 203

	// Logger service errors (210-219)
	CodeLogClearFailed = 210

	// Proxy service errors (220-229)
	CodeProxyStartFailed = 220
	CodeProxyStopFailed  = 221

	// Run service errors (230-239)
	CodeRunStartFailed = 230
	CodeRunStopFailed  = 231
)

// ErrorCodeToHTTPStatus converts business error code to HTTP status code
func ErrorCodeToHTTPStatus(code int) int {
	switch {
	case code == CodeSuccess:
		return http.StatusOK
	case code >= 100 && code < 150:
		// Client errors (100-149)
		switch code {
		case CodeBadRequest:
			return http.StatusBadRequest
		case CodeUnauthorized:
			return http.StatusUnauthorized
		case CodeForbidden:
			return http.StatusForbidden
		case CodeNotFound:
			return http.StatusNotFound
		case CodeConflict:
			return http.StatusConflict
		default:
			return http.StatusBadRequest
		}
	case code >= 150 && code < 200:
		// Client errors continued (150-199)
		return http.StatusBadRequest
	case code >= 200 && code < 300:
		// Server errors (200-299)
		return http.StatusInternalServerError
	default:
		return http.StatusInternalServerError
	}
}

// ErrorCodeToMessage returns human-readable error message for error code
func ErrorCodeToMessage(code int) string {
	switch code {
	case CodeSuccess:
		return "success"

	// Client errors
	case CodeBadRequest:
		return "bad request"
	case CodeUnauthorized:
		return "unauthorized"
	case CodeForbidden:
		return "forbidden"
	case CodeNotFound:
		return "not found"
	case CodeConflict:
		return "conflict"

	// Logger errors
	case CodeInvalidLogLevel:
		return "invalid log level"
	case CodeInvalidLogConfig:
		return "invalid log config"
	case CodeLogServiceUnavailable:
		return "log service unavailable"

	// Proxy errors
	case CodeProxyNotFound:
		return "proxy not found"
	case CodeProxyAlreadyExists:
		return "proxy already exists"
	case CodeInvalidProxyConfig:
		return "invalid proxy config"

	// Run/Mode errors
	case CodeInvalidRunMode:
		return "invalid run mode"
	case CodeModeTransitionFailed:
		return "mode transition failed"
	case CodeServiceAlreadyRunning:
		return "service already running"
	case CodeServiceNotRunning:
		return "service not running"

	// Config errors
	case CodeInvalidConfigValue:
		return "invalid config value"
	case CodeConfigNotFound:
		return "config not found"
	case CodeConfigUpdateFailed:
		return "config update failed"

	// Token errors
	case CodeInvalidToken:
		return "invalid token"
	case CodeTokenExpired:
		return "token expired"

	// Server errors
	case CodeInternalServer:
		return "internal server error"
	case CodeServiceUnavailable:
		return "service unavailable"
	case CodeDatabaseError:
		return "database error"
	case CodeExternalServiceFail:
		return "external service failed"

	// Logger service errors
	case CodeLogClearFailed:
		return "failed to clear logs"

	// Proxy service errors
	case CodeProxyStartFailed:
		return "failed to start proxy"
	case CodeProxyStopFailed:
		return "failed to stop proxy"

	// Run service errors
	case CodeRunStartFailed:
		return "failed to start service"
	case CodeRunStopFailed:
		return "failed to stop service"

	default:
		return "unknown error"
	}
}
