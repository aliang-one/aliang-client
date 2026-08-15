package handlers

import (
	"errors"
	"regexp"
	"strings"

	"aliang.one/nursorgate/common/logger"
)

// Custom errors for logger operations
var (
	ErrInvalidLogLevel       = errors.New("invalid log level")
	ErrInvalidDurationFormat = errors.New("invalid duration format")
	ErrNoValidConfigFields   = errors.New("no valid configuration fields provided")
	ErrInvalidRequestBody    = errors.New("invalid request body")
)

const redactionMarker = "***REDACTED***"

var (
	bearerTokenPattern     = regexp.MustCompile(`(?i)\bBearer\s+[^\s]+`)
	sensitiveKeyValPattern = regexp.MustCompile(`(?i)\b(token|secret|password|api_key|apikey|access_key|refresh_token|sentry_dsn|dsn)\b(\s*[:=]\s*)("?)([^"\s]+)("?)`)
)

// LogLevelTypeToString converts logger.LogLevelType to string
func LogLevelTypeToString(level logger.LogLevelType) string {
	switch level {
	case logger.TRACE:
		return "TRACE"
	case logger.DEBUG:
		return "DEBUG"
	case logger.INFO:
		return "INFO"
	case logger.WARN:
		return "WARN"
	case logger.ERROR:
		return "ERROR"
	case logger.FATAL:
		return "FATAL"
	case logger.PANIC:
		return "PANIC"
	default:
		return "UNKNOWN"
	}
}

// StringToLogLevelType converts string to logger.LogLevelType
func StringToLogLevelType(levelStr string) (logger.LogLevelType, error) {
	switch levelStr {
	case "TRACE":
		return logger.TRACE, nil
	case "DEBUG":
		return logger.DEBUG, nil
	case "INFO":
		return logger.INFO, nil
	case "WARN":
		return logger.WARN, nil
	case "ERROR":
		return logger.ERROR, nil
	case "FATAL":
		return logger.FATAL, nil
	case "PANIC":
		return logger.PANIC, nil
	default:
		return 0, ErrInvalidLogLevel
	}
}

// maskSensitiveData masks sensitive information like DSN
func maskSensitiveData(data string) string {
	if data == "" {
		return ""
	}
	if len(data) <= 8 {
		return "***"
	}
	return data[:8] + "***"
}

func redactSensitiveLogMessage(message string) string {
	if message == "" {
		return ""
	}
	redacted := bearerTokenPattern.ReplaceAllStringFunc(message, func(match string) string {
		parts := strings.SplitN(match, " ", 2)
		if len(parts) != 2 {
			return redactionMarker
		}
		return parts[0] + " " + redactionMarker
	})
	return sensitiveKeyValPattern.ReplaceAllString(redacted, "$1$2$3"+redactionMarker+"$5")
}

// BuildResponse creates a standardized API response
func BuildResponse(code int, msg string, data interface{}) LogAPIResponse {
	return LogAPIResponse{
		Code: code,
		Msg:  msg,
		Data: data,
	}
}

// BuildSuccessResponse creates a success response
func BuildSuccessResponse(data interface{}) LogAPIResponse {
	return BuildResponse(0, "success", data)
}

// BuildErrorResponse creates an error response
func BuildErrorResponse(msg string) LogAPIResponse {
	return BuildResponse(1, msg, nil)
}
