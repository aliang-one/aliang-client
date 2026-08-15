package services

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/common/logger"
	auth "aliang.one/nursorgate/processor/auth"
	"aliang.one/nursorgate/processor/config"
	"aliang.one/nursorgate/processor/runtime"
)

// AuthService 认证服务
type AuthService struct{}

var remoteLogoutDispatch = auth.LogoutSession

func teardownLocalSessionAfterLogout() {
	// Logout is the linearization point: invalidate remote auth operations before
	// any local teardown that an old completion could otherwise undo.
	auth.GetSessionAuthority().NotifyLoggedOut()
	auth.StopTokenRefresh()
	// The synchronous authority listener closes ingress before this function
	// proceeds. Keep teardown single-sourced through that transition so a logout
	// cannot race or double-stop the proxy lifecycle.
	config.SetHasLocalUserInfo(false)
	clearStartupStateAfterLogout()
	if err := auth.DeleteUserInfo(); err != nil {
		logger.Warn(fmt.Sprintf("Failed to delete user info: %v", err))
		auth.SetCurrentUserInfo(nil)
	}
	// Retry asynchronously in addition to the structured session event. Both
	// paths are idempotent; this covers a short user-agent restart race.
	RequestUserAgentDisableForSessionEnd("logout")
}

// NewAuthService 创建新的认证服务实例
func NewAuthService() *AuthService {
	return &AuthService{}
}

func mapUserInfo(userInfo *auth.UserInfo) models.UserInfoResponse {
	expiresAt := ""
	if !userInfo.UpdatedAt.IsZero() && userInfo.ExpiresIn > 0 {
		expiresAt = userInfo.UpdatedAt.Add(time.Duration(userInfo.ExpiresIn) * time.Second).Format(time.RFC3339)
	}

	return models.UserInfoResponse{
		ID:             userInfo.ID,
		Username:       userInfo.Username,
		Email:          userInfo.Email,
		Role:           userInfo.Role,
		Status:         userInfo.Status,
		Balance:        userInfo.Balance,
		Concurrency:    userInfo.Concurrency,
		AllowedGroups:  append([]int64(nil), userInfo.AllowedGroups...),
		CreatedAt:      userInfo.CreatedAt,
		ProfileUpdated: userInfo.ProfileUpdated,
		ExpiresIn:      userInfo.ExpiresIn,
		ExpiresAt:      expiresAt,
		UpdatedAt:      userInfo.UpdatedAt.Format(time.RFC3339),
	}
}

func syncStartupStateForAuthenticatedUser(userInfo *auth.UserInfo) {
	if userInfo == nil {
		return
	}
	startupState := runtime.GetStartupState()
	startupState.SetFetchSuccess(true)
	startupState.SetStatus(runtime.READY)
}

func clearStartupStateAfterLogout() {
	startupState := runtime.GetStartupState()
	startupState.SetFetchSuccess(false)
	startupState.SetStatus(runtime.UNCONFIGURED)
}

// agentSyncDispatch performs the PhoneServer sync after an auth-state change.
// It is a package-level var so tests can swap in a recording/blocking stub.
// Auth HTTP handlers fire it asynchronously (see agentSyncResult) so the
// response is never blocked by this side-effect.
var agentSyncDispatch = SyncUserAgentAfterAuthWithRetry

// EnsureAgentAfterAuthHook 在 auth-success 事件上确保 user-agent 进程已在运行。
// 默认 no-op；真实实现由 agentruntime 包通过 init() 注入，以此规避 services →
// agentruntime 的循环依赖（agentruntime 已 import services）。所有登录/会话恢复/
// 会话刷新/扫码激活成功都经 agentSyncResult 触发它，保证 user-agent 缺席时能被
// 登录事件自愈拉起，而不是只向 127.0.0.1:56433 转发 sync、agent 不在就静默失败。
var EnsureAgentAfterAuthHook = func() error { return nil }

func agentSyncResult(reason string) map[string]interface{} {
	// Fire the PhoneServer sync off the request path. Pushing the auth token to
	// PhoneServer is a side-effect of login/session-restore, not part of
	// determining login state, so it must not gate the HTTP response — otherwise
	// /api/auth/session hangs (and the page stays blank on first load) whenever
	// PhoneServer is slow or unreachable. The frontend does not consume the
	// agent_sync field from this response.
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logger.Warn(fmt.Sprintf("post-auth agent bootstrap panicked (reason=%s): %v", reason, r))
			}
		}()
		// 先确保 user-agent 进程在跑，再触发 PhoneServer sync。顺序不能反：sync 走
		// 127.0.0.1:56433，agent 缺席时必 refused，所以必须 ensure → sync。
		if err := EnsureAgentAfterAuthHook(); err != nil {
			logger.Warn(fmt.Sprintf("Ensure user-agent after auth failed (reason=%s): %v", reason, err))
		}
		if err := agentSyncDispatch(reason); err != nil {
			logger.Warn(fmt.Sprintf("Async agent sync failed (reason=%s): %v", reason, err))
		}
	}()
	return map[string]interface{}{
		"status": "async",
	}
}

func (s *AuthService) Login(email, password, turnstileToken string) map[string]interface{} {
	if strings.TrimSpace(email) == "" {
		return map[string]interface{}{
			"status": "failed",
			"error":  "email_required",
			"msg":    "Email cannot be empty",
		}
	}
	if strings.TrimSpace(password) == "" {
		return map[string]interface{}{
			"status": "failed",
			"error":  "password_required",
			"msg":    "Password cannot be empty",
		}
	}

	userInfo, err := auth.LoginWithPassword(email, password, turnstileToken)
	if err != nil {
		logger.Error(fmt.Sprintf("Login failed: %v", err))
		return map[string]interface{}{
			"status": "failed",
			"error":  "login_failed",
			"msg":    fmt.Sprintf("Failed to login: %v", err),
		}
	}

	syncStartupStateForAuthenticatedUser(userInfo)
	agentSync := agentSyncResult("login")

	return map[string]interface{}{
		"status":     "success",
		"msg":        "Login successful",
		"data":       mapUserInfo(userInfo),
		"agent_sync": agentSync,
	}
}

func (s *AuthService) RestoreSession() map[string]interface{} {
	return map[string]interface{}{
		"status": "success",
		"data":   s.GetSessionSnapshot(),
	}
}

// GetSessionSnapshot is deliberately side-effect free. Session restore belongs
// to the session-owner boot coordinator; browser reads never perform remote I/O,
// mutate persistence, transition authority state, sync Agent, or issue cookies.
func (s *AuthService) GetSessionSnapshot() SessionSnapshotPayload {
	return BuildSessionSnapshotPayload(auth.GetSessionAuthority().Snapshot())
}

func (s *AuthService) RefreshSession(refreshToken string) map[string]interface{} {
	userInfo, err := auth.RefreshSession(refreshToken)
	if err != nil {
		if errors.Is(err, auth.ErrRefreshTokenInvalid) || errors.Is(err, auth.ErrSessionExpired) {
			clearStartupStateAfterLogout()
			return map[string]interface{}{
				"status": "failed",
				"error":  "session_expired",
				"msg":    "Session expired. Please log in again.",
			}
		}
		logger.Error(fmt.Sprintf("Session refresh failed: %v", err))
		return map[string]interface{}{
			"status": "failed",
			"error":  "refresh_failed",
			"msg":    fmt.Sprintf("Failed to refresh session: %v", err),
		}
	}

	syncStartupStateForAuthenticatedUser(userInfo)
	agentSync := agentSyncResult("refresh_session")

	return map[string]interface{}{
		"status":     "success",
		"msg":        "Session refreshed successfully",
		"data":       mapUserInfo(userInfo),
		"agent_sync": agentSync,
	}
}

// GetUserInfo 获取当前用户信息
func (s *AuthService) GetUserInfo() map[string]interface{} {
	userInfo := auth.GetCurrentUserInfoOrLoad()
	if userInfo == nil {
		return map[string]interface{}{
			"status": "no_user",
			"msg":    "No user info available",
		}
	}

	return map[string]interface{}{
		"status": "success",
		"data":   mapUserInfo(userInfo),
	}
}

// ScanInit 扫码登录初始化：向 official-website 申请 device_code + 二维码内容。
func (s *AuthService) ScanInit() map[string]interface{} {
	result, err := auth.ScanInit()
	if err != nil {
		logger.Error(fmt.Sprintf("Scan login init failed: %v", err))
		return map[string]interface{}{
			"status": "failed",
			"error":  "scan_init_failed",
			"msg":    fmt.Sprintf("Failed to init scan login: %v", err),
		}
	}
	return map[string]interface{}{
		"status": "success",
		"data":   result,
	}
}

// ScanStatus 按 device_code 轮询扫码状态。scan 阶段置于 data.status（pending/scanned/
// authorized/denied/expired）；行不存在视为 expired（前端重生码）。
func (s *AuthService) ScanStatus(deviceCode string) map[string]interface{} {
	result, err := auth.ScanStatus(deviceCode)
	if err != nil {
		if errors.Is(err, auth.ErrScanCodeNotFound) {
			return map[string]interface{}{
				"status": "success",
				"data":   map[string]interface{}{"status": "expired"},
			}
		}
		logger.Warn(fmt.Sprintf("Scan login status failed: %v", err))
		return map[string]interface{}{
			"status": "failed",
			"error":  "scan_status_failed",
			"msg":    fmt.Sprintf("Failed to query scan status: %v", err),
		}
	}
	return map[string]interface{}{
		"status": "success",
		"data":   result,
	}
}

// ActivateScanLogin 用扫码拿到的本地 session 兼容字段完成登录，
// 返回结构与密码登录 Login 一致（data 为 UserInfoResponse、附带 agent_sync）。
func (s *AuthService) ActivateScanLogin(sessionToken, refreshToken string) map[string]interface{} {
	sessionToken = strings.TrimSpace(sessionToken)
	refreshToken = strings.TrimSpace(refreshToken)
	if sessionToken == "" {
		return map[string]interface{}{
			"status": "failed",
			"error":  "session_token_required",
			"msg":    "Session token cannot be empty",
		}
	}
	if refreshToken == "" {
		return map[string]interface{}{
			"status": "failed",
			"error":  "refresh_token_required",
			"msg":    "Refresh token cannot be empty",
		}
	}

	userInfo, err := auth.ActivateWithTokens(sessionToken, refreshToken)
	if err != nil {
		logger.Error(fmt.Sprintf("Scan login activation failed: %v", err))
		return map[string]interface{}{
			"status": "failed",
			"error":  "scan_activate_failed",
			"msg":    fmt.Sprintf("Failed to activate scan login: %v", err),
		}
	}

	syncStartupStateForAuthenticatedUser(userInfo)
	agentSync := agentSyncResult("scan_login")

	return map[string]interface{}{
		"status":     "success",
		"msg":        "Scan login successful",
		"data":       mapUserInfo(userInfo),
		"agent_sync": agentSync,
	}
}

// LogoutUser 登出用户
func (s *AuthService) LogoutUser(refreshToken string) map[string]interface{} {
	// Capture the local st_ before deleting the local session. Remote revoke
	// is best-effort and must never delay the local security boundary.
	token := ""
	if current := auth.GetCurrentUserInfoOrLoad(); current != nil {
		token = strings.TrimSpace(current.AccessToken)
	}
	if token == "" {
		token = strings.TrimSpace(refreshToken)
	}

	teardownLocalSessionAfterLogout()

	if token != "" {
		dispatch := remoteLogoutDispatch
		go func(refreshToken string) {
			if err := dispatch(refreshToken); err != nil {
				logger.Warn(fmt.Sprintf("Remote logout failed after local cleanup: %v", err))
			}
		}(token)
	}

	logger.Info("User logged out successfully")

	return map[string]interface{}{
		"status": "success",
		"msg":    "User logged out successfully",
	}
}
