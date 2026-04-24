package services

import (
	"errors"
	"fmt"
	"strings"

	"aliang.one/nursorgate/app/http/models"
	auth "aliang.one/nursorgate/processor/auth"
)

type UserCenterService struct{}

var (
	getUserProfileFn       = auth.GetUserProfile
	updateUserProfileFn    = auth.UpdateUserProfile
	getUserUsageSummaryFn  = auth.GetUserUsageSummary
	getUserUsageProgressFn = auth.GetUserUsageProgress
	getUserAPIKeysFn       = auth.GetUserAPIKeys
	redeemCodeFn           = auth.RedeemCode
)

func NewUserCenterService() *UserCenterService {
	return &UserCenterService{}
}

func (s *UserCenterService) GetProfile() map[string]interface{} {
	profile, err := getUserProfileFn()
	if err != nil {
		if errors.Is(err, auth.ErrSessionExpired) {
			return map[string]interface{}{
				"status": "unauthenticated",
				"error":  "session_expired",
				"msg":    "Session expired. Please log in again.",
			}
		}
		if isSessionMissingError(err) {
			return map[string]interface{}{
				"status": "unauthenticated",
				"error":  "session_missing",
				"msg":    "No authenticated session found",
			}
		}
		return map[string]interface{}{
			"status": "failed",
			"error":  "profile_fetch_failed",
			"msg":    fmt.Sprintf("Failed to fetch profile: %v", err),
		}
	}

	return map[string]interface{}{
		"status": "success",
		"data": models.UserProfileResponse{
			ID:            profile.ID,
			Email:         profile.Email,
			Username:      profile.Username,
			Role:          profile.Role,
			Balance:       profile.Balance,
			Concurrency:   profile.Concurrency,
			Status:        profile.Status,
			AllowedGroups: profile.AllowedGroups,
			CreatedAt:     profile.CreatedAt,
			UpdatedAt:     profile.UpdatedAt,
		},
	}
}

func (s *UserCenterService) UpdateProfile(username string) map[string]interface{} {
	if strings.TrimSpace(username) == "" {
		return map[string]interface{}{
			"status": "failed",
			"error":  "username_required",
			"msg":    "Username cannot be empty",
		}
	}

	profile, err := updateUserProfileFn(username)
	if err != nil {
		if errors.Is(err, auth.ErrSessionExpired) {
			return map[string]interface{}{
				"status": "unauthenticated",
				"error":  "session_expired",
				"msg":    "Session expired. Please log in again.",
			}
		}
		if isSessionMissingError(err) {
			return map[string]interface{}{
				"status": "unauthenticated",
				"error":  "session_missing",
				"msg":    "No authenticated session found",
			}
		}
		return map[string]interface{}{
			"status": "failed",
			"error":  "profile_update_failed",
			"msg":    fmt.Sprintf("Failed to update profile: %v", err),
		}
	}

	return map[string]interface{}{
		"status": "success",
		"msg":    "Profile updated successfully",
		"data": models.UserProfileResponse{
			ID:            profile.ID,
			Email:         profile.Email,
			Username:      profile.Username,
			Role:          profile.Role,
			Balance:       profile.Balance,
			Concurrency:   profile.Concurrency,
			Status:        profile.Status,
			AllowedGroups: profile.AllowedGroups,
			CreatedAt:     profile.CreatedAt,
			UpdatedAt:     profile.UpdatedAt,
		},
	}
}

func (s *UserCenterService) GetUsageSummary() map[string]interface{} {
	summary, err := getUserUsageSummaryFn()
	if err != nil {
		if errors.Is(err, auth.ErrSessionExpired) {
			return map[string]interface{}{
				"status": "unauthenticated",
				"error":  "session_expired",
				"msg":    "Session expired. Please log in again.",
			}
		}
		if isSessionMissingError(err) {
			return map[string]interface{}{
				"status": "unauthenticated",
				"error":  "session_missing",
				"msg":    "No authenticated session found",
			}
		}
		return map[string]interface{}{
			"status": "failed",
			"error":  "usage_summary_fetch_failed",
			"msg":    fmt.Sprintf("Failed to fetch usage summary: %v", err),
		}
	}

	return map[string]interface{}{
		"status": "success",
		"data": models.UsageSummaryResponse{
			ActiveCount:   summary.ActiveCount,
			TotalUsedUSD:  summary.TotalUsedUSD,
			Subscriptions: summary.Subscriptions,
		},
	}
}

func (s *UserCenterService) GetUsageProgress() map[string]interface{} {
	progress, err := getUserUsageProgressFn()
	if err != nil {
		if errors.Is(err, auth.ErrSessionExpired) {
			return map[string]interface{}{
				"status": "unauthenticated",
				"error":  "session_expired",
				"msg":    "Session expired. Please log in again.",
			}
		}
		if isSessionMissingError(err) {
			return map[string]interface{}{
				"status": "unauthenticated",
				"error":  "session_missing",
				"msg":    "No authenticated session found",
			}
		}
		return map[string]interface{}{
			"status": "failed",
			"error":  "usage_progress_fetch_failed",
			"msg":    fmt.Sprintf("Failed to fetch usage progress: %v", err),
		}
	}

	return map[string]interface{}{
		"status": "success",
		"data":   models.UsageProgressResponse{Items: progress.Items},
	}
}

func (s *UserCenterService) GetAPIKeys() map[string]interface{} {
	apiKeys, err := getUserAPIKeysFn()
	if err != nil {
		if errors.Is(err, auth.ErrSessionExpired) {
			return map[string]interface{}{
				"status": "unauthenticated",
				"error":  "session_expired",
				"msg":    "Session expired. Please log in again.",
			}
		}
		if isSessionMissingError(err) {
			return map[string]interface{}{
				"status": "unauthenticated",
				"error":  "session_missing",
				"msg":    "No authenticated session found",
			}
		}
		return map[string]interface{}{
			"status": "failed",
			"error":  "api_keys_fetch_failed",
			"msg":    fmt.Sprintf("Failed to fetch API keys: %v", err),
		}
	}

	items := make([]models.UserAPIKeyResponse, 0, len(apiKeys))
	for _, key := range apiKeys {
		var group *models.APIKeyGroupResponse
		if key.Group != nil {
			group = &models.APIKeyGroupResponse{
				ID:                    key.Group.ID,
				Name:                  key.Group.Name,
				Description:           key.Group.Description,
				Platform:              key.Group.Platform,
				RateMultiplier:        key.Group.RateMultiplier,
				ClaudeCodeOnly:        key.Group.ClaudeCodeOnly,
				AllowMessagesDispatch: key.Group.AllowMessagesDispatch,
			}
		}

		items = append(items, models.UserAPIKeyResponse{
			ID:              key.ID,
			Key:             key.Key,
			Name:            key.Name,
			GroupID:         key.GroupID,
			Status:          key.Status,
			Provider:        key.Provider,
			Masked:          key.Masked,
			SecretAvailable: key.SecretAvailable,
			Group:           group,
		})
	}

	return map[string]interface{}{
		"status": "success",
		"data":   models.UserAPIKeyListResponse{Items: items},
	}
}

func (s *UserCenterService) RedeemCode(code string) map[string]interface{} {
	if strings.TrimSpace(code) == "" {
		return map[string]interface{}{
			"status": "failed",
			"error":  "redeem_code_required",
			"msg":    "Redeem code cannot be empty",
		}
	}

	result, err := redeemCodeFn(code)
	if err != nil {
		if errors.Is(err, auth.ErrSessionExpired) {
			return map[string]interface{}{
				"status": "unauthenticated",
				"error":  "session_expired",
				"msg":    "Session expired. Please log in again.",
			}
		}
		if isSessionMissingError(err) {
			return map[string]interface{}{
				"status": "unauthenticated",
				"error":  "session_missing",
				"msg":    "No authenticated session found",
			}
		}
		return map[string]interface{}{
			"status": "failed",
			"error":  "redeem_failed",
			"msg":    fmt.Sprintf("Failed to redeem code: %v", err),
		}
	}

	return map[string]interface{}{
		"status": "success",
		"msg":    "Redeem successful",
		"data":   models.RedeemCodeResponse{Data: result.Data},
	}
}

func isSessionMissingError(err error) bool {
	if err == nil {
		return false
	}
	errText := strings.ToLower(err.Error())
	return strings.Contains(errText, "no user session") ||
		strings.Contains(errText, "missing access token")
}
