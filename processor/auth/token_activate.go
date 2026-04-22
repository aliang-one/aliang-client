package user

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"aliang.one/nursorgate/common/logger"
	"aliang.one/nursorgate/processor/config"
)

const (
	// API调用超时
	apiTimeout = 10 * time.Second
)

func ActivateToken(token string) (*UserInfo, error) {
	if token == "" {
		return nil, fmt.Errorf("token cannot be empty")
	}

	logger.Info(fmt.Sprintf("Activating legacy token compatibility refresh: %s...", maskToken(token)))

	userInfo, err := RefreshSession(token)
	if err == nil {
		return userInfo, nil
	}

	logger.Warn(fmt.Sprintf("Token activation failed: %v, trying to load local user info", err))

	localUserInfo, err := LoadUserInfo()
	if err == nil {
		startTokenRefresh()

		return localUserInfo, nil
	}

	logger.Error(fmt.Sprintf("No local user info found, compatibility activation failed: %v", err))
	return nil, fmt.Errorf("failed to activate token and no local user info found: %w", err)
}

func LoginWithPassword(email, password, turnstileToken string) (*UserInfo, error) {
	if strings.TrimSpace(email) == "" {
		return nil, fmt.Errorf("email cannot be empty")
	}
	if strings.TrimSpace(password) == "" {
		return nil, fmt.Errorf("password cannot be empty")
	}

	urlBuilder, err := config.NewURLBuilder()
	if err != nil {
		return nil, err
	}

	loginURL, err := urlBuilder.GetAuthLoginURL()
	if err != nil {
		return nil, err
	}

	requestBody := map[string]string{
		"email":    strings.TrimSpace(email),
		"password": password,
	}
	if strings.TrimSpace(turnstileToken) != "" {
		requestBody["turnstile_token"] = strings.TrimSpace(turnstileToken)
	}

	jsonData, err := json.Marshal(requestBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, loginURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{
		Timeout: apiTimeout,
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("api returned status %d: %s", resp.StatusCode, string(body))
	}

	var response authTokenEnvelope
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	if strings.TrimSpace(response.Data.AccessToken) == "" {
		return nil, fmt.Errorf("login response missing access_token")
	}
	if strings.TrimSpace(response.Data.RefreshToken) == "" {
		return nil, fmt.Errorf("login response missing refresh_token")
	}

	profile, err := GetUserProfileWithToken(response.Data.AccessToken)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch user profile after login: %w", err)
	}

	userInfo := buildUserInfoFromProfile(profile)
	userInfo.AccessToken = response.Data.AccessToken
	userInfo.RefreshToken = response.Data.RefreshToken
	userInfo.TokenType = response.Data.TokenType
	userInfo.ExpiresIn = response.Data.ExpiresIn
	userInfo.UpdatedAt = time.Now()

	if err := SaveUserInfo(userInfo); err != nil {
		logger.Warn(fmt.Sprintf("Failed to save user info locally: %v", err))
	}

	startTokenRefresh()
	config.SetUsingDefaultConfig(false)
	config.SetHasLocalUserInfo(true)

	return userInfo, nil
}

func RestoreSession() (*UserInfo, error) {
	localUserInfo, err := LoadUserInfo()
	if err != nil {
		return nil, err
	}

	refreshedInfo, refreshErr := RefreshSession(localUserInfo.RefreshToken)
	if refreshErr == nil {
		return refreshedInfo, nil
	}
	if errors.Is(refreshErr, ErrRefreshTokenInvalid) {
		return nil, refreshErr
	}

	if strings.TrimSpace(localUserInfo.AccessToken) == "" {
		startTokenRefresh()
		config.SetHasLocalUserInfo(true)
		return localUserInfo, nil
	}

	profile, profileErr := GetUserProfileWithToken(localUserInfo.AccessToken)
	if profileErr != nil {
		logger.Warn(fmt.Sprintf("Session restore profile sync skipped: refresh failed (%v), profile fetch failed (%v)", refreshErr, profileErr))
		startTokenRefresh()
		config.SetHasLocalUserInfo(true)
		return localUserInfo, nil
	}

	latestProfile := buildUserInfoFromProfile(profile)
	latestProfile.AccessToken = localUserInfo.AccessToken
	latestProfile.RefreshToken = localUserInfo.RefreshToken
	latestProfile.TokenType = localUserInfo.TokenType
	latestProfile.ExpiresIn = localUserInfo.ExpiresIn
	latestProfile.UpdatedAt = time.Now()

	if err := SaveUserInfo(latestProfile); err != nil {
		logger.Warn(fmt.Sprintf("Failed to save restored session profile: %v", err))
	}

	startTokenRefresh()
	config.SetHasLocalUserInfo(true)

	return latestProfile, nil
}

func RefreshSession(refreshToken string) (*UserInfo, error) {
	token := strings.TrimSpace(refreshToken)
	if token == "" {
		current := GetCurrentUserInfoOrLoad()
		if current != nil {
			token = strings.TrimSpace(current.RefreshToken)
		}
	}
	if token == "" {
		return nil, fmt.Errorf("refresh token cannot be empty")
	}

	refreshSessionMu.Lock()
	defer refreshSessionMu.Unlock()

	// Another caller may have refreshed and rotated the token while we were
	// waiting for the lock. Reuse the newest local session to avoid sending an
	// already-invalidated refresh token again.
	current := GetCurrentUserInfoOrLoad()
	if current != nil {
		latestToken := strings.TrimSpace(current.RefreshToken)
		if latestToken != "" && latestToken != token && strings.TrimSpace(current.AccessToken) != "" {
			logger.Debug("Refresh token rotated while waiting; reusing latest local session")
			return current, nil
		}
		if latestToken != "" {
			token = latestToken
		}
	}

	urlBuilder, err := config.NewURLBuilder()
	if err != nil {
		return nil, err
	}

	refreshURL, err := urlBuilder.GetAuthRefreshURL()
	if err != nil {
		return nil, err
	}

	requestBody := map[string]string{
		"refresh_token": token,
	}

	jsonData, err := json.Marshal(requestBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, refreshURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: apiTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		if invalidTokenErr := classifyRefreshSessionFailure(resp.StatusCode, body); invalidTokenErr != nil {
			clearLocalSessionAfterInvalidRefreshToken()
			return nil, fmt.Errorf("saved session expired: %w", invalidTokenErr)
		}
		return nil, fmt.Errorf("api returned status %d: %s", resp.StatusCode, string(body))
	}

	var response authTokenEnvelope
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	if strings.TrimSpace(response.Data.AccessToken) == "" {
		return nil, fmt.Errorf("refresh response missing access_token")
	}
	if strings.TrimSpace(response.Data.RefreshToken) == "" {
		response.Data.RefreshToken = token
	}

	userInfo := mergeRefreshedSessionWithCurrentUser(current, response.Data.AccessToken, response.Data.RefreshToken, response.Data.TokenType, response.Data.ExpiresIn)

	profile, err := GetUserProfileWithToken(response.Data.AccessToken)
	if err != nil {
		logger.Warn(fmt.Sprintf("Profile sync after refresh failed, keeping last known user profile: %v", err))
	} else {
		applyUserProfileToUserInfo(userInfo, profile)
	}

	if err := SaveUserInfo(userInfo); err != nil {
		return nil, fmt.Errorf("failed to save refreshed user info: %w", err)
	}

	startTokenRefresh()
	config.SetHasLocalUserInfo(true)

	return userInfo, nil
}

func mergeRefreshedSessionWithCurrentUser(current *UserInfo, accessToken, refreshToken, tokenType string, expiresIn int) *UserInfo {
	info := &UserInfo{}
	if current != nil {
		*info = *current
	}

	info.AccessToken = accessToken
	info.RefreshToken = refreshToken
	info.TokenType = tokenType
	info.ExpiresIn = expiresIn
	info.UpdatedAt = time.Now()

	return info
}

func LogoutSession(refreshToken string) error {
	token := strings.TrimSpace(refreshToken)
	if token == "" {
		current := GetCurrentUserInfoOrLoad()
		if current != nil {
			token = strings.TrimSpace(current.RefreshToken)
		}
	}

	urlBuilder, err := config.NewURLBuilder()
	if err != nil {
		return err
	}

	logoutURL, err := urlBuilder.GetAuthLogoutURL()
	if err != nil {
		return err
	}

	requestBody := map[string]string{}
	if token != "" {
		requestBody["refresh_token"] = token
	}

	jsonData, err := json.Marshal(requestBody)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, logoutURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: apiTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("api returned status %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

type authTokenEnvelope struct {
	Data struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
		TokenType    string `json:"token_type"`
	} `json:"data"`
	Message string `json:"message"`
}

func buildUserInfoFromProfile(profile *UserProfile) *UserInfo {
	info := &UserInfo{UpdatedAt: time.Now()}
	applyUserProfileToUserInfo(info, profile)
	return info
}

func applyUserProfileToUserInfo(info *UserInfo, profile *UserProfile) {
	if info == nil || profile == nil {
		return
	}

	username := strings.TrimSpace(profile.Username)
	if username == "" {
		username = strings.TrimSpace(profile.Email)
	}

	info.ID = profile.ID
	info.Email = strings.TrimSpace(profile.Email)
	info.Username = username
	info.Role = strings.TrimSpace(profile.Role)
	info.Balance = profile.Balance
	info.Concurrency = profile.Concurrency
	info.Status = strings.TrimSpace(profile.Status)
	info.AllowedGroups = append([]int64(nil), profile.AllowedGroups...)
	info.CreatedAt = strings.TrimSpace(profile.CreatedAt)
	info.ProfileUpdated = strings.TrimSpace(profile.UpdatedAt)

	if info.Status == "" {
		info.Status = "active"
	}
}

// maskToken 掩盖Token用于日志显示（只显示前后几个字符）
func maskToken(token string) string {
	if len(token) <= 8 {
		return "***"
	}
	return token[:4] + "..." + token[len(token)-4:]
}

var (
	tokenRefresher   *TokenRefresher
	refreshSessionMu sync.Mutex
)

// startTokenRefresh 启动定时刷新
func startTokenRefresh() {
	if tokenRefresher != nil && tokenRefresher.IsRunning() {
		return // 已经在运行
	}

	tokenRefresher = NewTokenRefresher()
	if err := tokenRefresher.Start(); err != nil {
		logger.Warn(fmt.Sprintf("Failed to start token refresher: %v", err))
	}
}

// StopTokenRefresh 停止定时刷新
func StopTokenRefresh() {
	if tokenRefresher != nil {
		if err := tokenRefresher.Stop(); err != nil {
			logger.Warn(fmt.Sprintf("Failed to stop token refresher: %v", err))
		}
	}
}

// GetTokenRefresher 获取Token刷新器实例
func GetTokenRefresher() *TokenRefresher {
	return tokenRefresher
}
