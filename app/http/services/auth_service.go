package services

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/common/logger"
	auth "aliang.one/nursorgate/processor/auth"
	"aliang.one/nursorgate/processor/runtime"
)

// AuthService 认证服务
type AuthService struct{}

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

	return map[string]interface{}{
		"status": "success",
		"msg":    "Login successful",
		"data":   mapUserInfo(userInfo),
	}
}

func (s *AuthService) RestoreSession() map[string]interface{} {
	userInfo, err := auth.RestoreSession()
	if err != nil {
		if errors.Is(err, auth.ErrRefreshTokenInvalid) {
			clearStartupStateAfterLogout()
			return map[string]interface{}{
				"status": "no_session",
				"error":  "session_expired",
				"msg":    "Saved session expired. Please log in again.",
			}
		}
		return map[string]interface{}{
			"status": "no_session",
			"msg":    "No local auth session available",
		}
	}

	syncStartupStateForAuthenticatedUser(userInfo)

	return map[string]interface{}{
		"status": "success",
		"msg":    "Session restored successfully",
		"data":   mapUserInfo(userInfo),
	}
}

func (s *AuthService) RefreshSession(refreshToken string) map[string]interface{} {
	userInfo, err := auth.RefreshSession(refreshToken)
	if err != nil {
		if errors.Is(err, auth.ErrRefreshTokenInvalid) {
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

	return map[string]interface{}{
		"status": "success",
		"msg":    "Session refreshed successfully",
		"data":   mapUserInfo(userInfo),
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

// LogoutUser 登出用户
func (s *AuthService) LogoutUser(refreshToken string) map[string]interface{} {
	err := auth.LogoutSession(refreshToken)
	if err != nil {
		logger.Warn(fmt.Sprintf("Remote logout failed, continue local cleanup: %v", err))
	}

	auth.StopTokenRefresh()

	if err := auth.DeleteUserInfo(); err != nil {
		logger.Warn(fmt.Sprintf("Failed to delete user info: %v", err))
	}

	clearStartupStateAfterLogout()

	logger.Info("User logged out successfully")

	return map[string]interface{}{
		"status": "success",
		"msg":    "User logged out successfully",
	}
}
