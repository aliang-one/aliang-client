package user

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"aliang.one/nursorgate/common/logger"
	"aliang.one/nursorgate/processor/config"
)

var ErrRefreshTokenInvalid = errors.New("refresh token invalid")
var ErrSessionExpired = errors.New("auth session expired")
var ErrSessionRecovering = errors.New("auth session recovering")
var ErrNoLocalSession = errors.New("no local auth session")

var (
	authSuccessHandlerMu sync.RWMutex
	authSuccessHandler   func()
)

// refreshRejectionPhrases are lowercase substrings the auth backend emits (in
// either the "message" or "error" field) to describe a rejected refresh
// credential. A 401 whose body matches one of these phrases (or carries the
// REFRESH_TOKEN_INVALID reason/code) is a definitive terminal rejection;
// unknown or unparseable 401 bodies are treated as transient so the SoftExpired
// recovery chain can retry instead of wiping the session.
var refreshRejectionPhrases = []string{
	"invalid refresh token",
	"refresh token is no longer valid",
	"refresh token expired",
	"refresh token has been revoked",
	"local session is no longer valid",
}

// messageFieldMatchesRefreshRejection reports whether either the "message" or
// "error" JSON field carries a known refresh-token-rejection phrase.
func messageFieldMatchesRefreshRejection(envelope map[string]any) bool {
	for _, key := range []string{"message", "error"} {
		msg := strings.ToLower(strings.TrimSpace(stringValue(envelope[key])))
		if msg == "" {
			continue
		}
		for _, phrase := range refreshRejectionPhrases {
			if strings.Contains(msg, phrase) {
				return true
			}
		}
	}
	return false
}

func classifyRefreshSessionFailure(statusCode int, body []byte) error {
	if statusCode == http.StatusForbidden {
		return ErrRefreshTokenInvalid
	}
	if statusCode != http.StatusUnauthorized {
		return nil
	}

	// Parse as a generic map: refresh-rejection bodies vary in shape and some
	// only carry an "error" field, so a fixed struct would silently drop the
	// very field we must match on.
	var envelope map[string]any
	if err := json.Unmarshal(body, &envelope); err != nil {
		// 无法解析的 401 视为瞬时,返回 nil 让上层进入 SoftExpired 重试而非清会话。
		return nil
	}

	reason := strings.ToUpper(strings.TrimSpace(stringValue(envelope["reason"])))
	code := strings.ToUpper(strings.TrimSpace(stringValue(envelope["code"])))
	if reason == "REFRESH_TOKEN_INVALID" || code == "REFRESH_TOKEN_INVALID" {
		return ErrRefreshTokenInvalid
	}
	if messageFieldMatchesRefreshRejection(envelope) {
		return ErrRefreshTokenInvalid
	}

	// 兜底 — 不明确的 401（未知 JSON/不可解析/空 body）视为瞬时，返回 nil 让上层
	// 进入 SoftExpired 重试而非清会话。只有上面明确拒绝语义才返回 ErrRefreshTokenInvalid。
	return nil
}

func classifyAccessTokenFailure(statusCode int, body []byte) error {
	if statusCode != http.StatusUnauthorized {
		return nil
	}

	var envelope map[string]any
	if err := json.Unmarshal(body, &envelope); err != nil {
		// 兜底 — 不明确的 401（未知 JSON/不可解析/空 body）视为瞬时，返回 nil 让上层
		// 进入 SoftExpired 重试而非清会话。只有上面明确拒绝语义才返回 ErrSessionExpired。
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
	if strings.Contains(message, "invalid authentication") {
		return ErrSessionExpired
	}

	// 兜底 — 不明确的 401（未知 JSON/不可解析/空 body）视为瞬时，返回 nil 让上层
	// 进入 SoftExpired 重试而非清会话。只有上面明确拒绝语义才返回 ErrSessionExpired。
	return nil
}

func clearLocalSessionAfterInvalidRefreshToken() {
	clearLocalSessionAfterExpiration("invalid refresh token")
}

func clearLocalSessionAfterExpiredAccessToken() {
	// Self-heal before wiping: an access token typically only expires because
	// the background refresher missed its lead-time window (e.g. a transient
	// network failure during refresh). If the refresh token is still valid,
	// refreshing renews the access token and the session survives — no
	// auth_expired, no forced re-login, no agent going offline. Only when the
	// refresh genuinely fails do we treat the session as expired.
	RecoverOrExpireLocalSession("expired access token")
}

// RecoverOrExpireLocalSession signals that the cloud rejected the access token
// (e.g. agent device-register 401, user-center access-token 401). The session
// enters SoftExpired: the ingress proxy is paused (no forwarding with a rejected
// token — closes 缺口 B) and the recovery coordinator renews the token async.
// Transient failures keep retrying without forcing a re-login; only a permanent
// rejection escalates to HardInvalid.
//
// MUST NOT be called while the caller holds a lock that the SoftExpired listener
// re-acquires: onSessionEvent(StateSoftExpired) calls runService.StopIngressIfActive
// (runService mutex). Recovery itself runs in a goroutine and does not block the caller.
func RecoverOrExpireLocalSession(reason string) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "expired access token"
	}
	// 逾期标尺:access token 被拒时若本地锚点已判逾期,说明后台刷新错过了
	// lead 窗(滑窗污染或漏 tick),下一次刷新必撞零宽限 401。在这里把逾期量
	// 直接钉进日志,事故现场不再需要反推。
	if info := GetCurrentUserInfoOrLoad(); info != nil {
		if diagnosis := overdueDiagnostic(info, time.Now()); diagnosis != "" {
			logger.Warn(diagnosis)
		}
	}
	GetSessionAuthority().NotifyAccessRejected(reason)
	// Trigger recovery directly (not only via the authority listener) so the
	// recoverable case is robust to the authority's current state. Single-flight
	// guards a duplicate start if the SoftExpired listener also fires.
	StartSoftExpiryRecovery()
}

func ExpireLocalSession(reason string) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "expired access token"
	}
	clearLocalSessionAfterExpiration(reason)
}

func clearLocalSessionAfterExpiration(reason string) {
	// Invalidate every in-flight restore/refresh/login before deleting local data.
	// A late success can no longer repersist credentials or publish Active after
	// this terminal boundary.
	GetSessionAuthority().NotifyRefreshFailed(true, sessionReasonFromWipeReason(reason))
	StopTokenRefresh()
	if !IsSessionOwnerProcess() {
		SetCurrentUserInfo(nil)
		logger.Warn(fmt.Sprintf("Non-owner process ignored persisted session cleanup after %s", reason))
		return
	}

	if err := DeleteUserInfo(); err != nil {
		logger.Warn(fmt.Sprintf("Failed to clear invalid local auth session: %v", err))
		SetCurrentUserInfo(nil)
	}

	config.SetHasLocalUserInfo(false)
	logger.Info(fmt.Sprintf("Local auth session cleared after %s", reason))
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

// SetAuthSuccessHandler registers a callback fired after a successful token
// refresh/login. The agent uses it to push its freshly-rotated access_token to
// PhoneServer over the live WS (agent.session.refresh) so the server's recorded
// session expiry (userTokenExp) stays current without a reconnect.
func SetAuthSuccessHandler(handler func()) {
	authSuccessHandlerMu.Lock()
	defer authSuccessHandlerMu.Unlock()
	authSuccessHandler = handler
}

func notifyAuthSuccessHandler() {
	authSuccessHandlerMu.RLock()
	handler := authSuccessHandler
	authSuccessHandlerMu.RUnlock()

	if handler != nil {
		handler()
	}
}
