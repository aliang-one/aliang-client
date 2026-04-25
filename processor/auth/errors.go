package user

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"aliang.one/nursorgate/common/logger"
	"aliang.one/nursorgate/processor/config"
)

var ErrRefreshTokenInvalid = errors.New("refresh token invalid")
var ErrSessionExpired = errors.New("auth session expired")

var (
	authExpirationHandlerMu sync.RWMutex
	authExpirationHandler   func()
)

type authAPIErrorEnvelope struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Reason  string `json:"reason"`
}

func classifyRefreshSessionFailure(statusCode int, body []byte) error {
	if statusCode != http.StatusUnauthorized {
		return nil
	}

	var envelope authAPIErrorEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil
	}

	message := strings.ToLower(strings.TrimSpace(envelope.Message))
	reason := strings.ToUpper(strings.TrimSpace(envelope.Reason))
	if envelope.Code == http.StatusUnauthorized && (reason == "REFRESH_TOKEN_INVALID" || strings.Contains(message, "invalid refresh token")) {
		return ErrRefreshTokenInvalid
	}

	return nil
}

func classifyAccessTokenFailure(statusCode int, body []byte) error {
	if statusCode != http.StatusUnauthorized {
		return nil
	}

	var envelope map[string]any
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil
	}

	code := strings.ToUpper(strings.TrimSpace(stringValue(envelope["code"])))
	message := strings.ToLower(strings.TrimSpace(stringValue(envelope["message"])))
	reason := strings.ToUpper(strings.TrimSpace(stringValue(envelope["reason"])))

	if code == "TOKEN_REVOKED" || reason == "TOKEN_REVOKED" || strings.Contains(message, "token has been revoked") {
		return ErrSessionExpired
	}
	if code == "ACCESS_TOKEN_INVALID" || reason == "ACCESS_TOKEN_INVALID" || strings.Contains(message, "invalid access token") {
		return ErrSessionExpired
	}
	if code == "TOKEN_EXPIRED" || reason == "TOKEN_EXPIRED" || strings.Contains(message, "token expired") || strings.Contains(message, "expired token") {
		return ErrSessionExpired
	}

	return nil
}

func clearLocalSessionAfterInvalidRefreshToken() {
	clearLocalSessionAfterExpiration("invalid refresh token")
}

func clearLocalSessionAfterExpiredAccessToken() {
	clearLocalSessionAfterExpiration("expired access token")
}

func clearLocalSessionAfterExpiration(reason string) {
	StopTokenRefresh()

	if err := DeleteUserInfo(); err != nil {
		logger.Warn(fmt.Sprintf("Failed to clear invalid local auth session: %v", err))
		SetCurrentUserInfo(nil)
	}

	config.SetHasLocalUserInfo(false)
	logger.Info(fmt.Sprintf("Local auth session cleared after %s", reason))
	notifyAuthExpirationHandler()
	logger.Warn("Authentication expired - proxy service should be stopped")
}

func stringValue(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case float64:
		return fmt.Sprintf("%.0f", typed)
	default:
		return ""
	}
}

func SetAuthExpirationHandler(handler func()) {
	authExpirationHandlerMu.Lock()
	defer authExpirationHandlerMu.Unlock()
	authExpirationHandler = handler
}

func notifyAuthExpirationHandler() {
	authExpirationHandlerMu.RLock()
	handler := authExpirationHandler
	authExpirationHandlerMu.RUnlock()

	if handler != nil {
		handler()
	}
}
