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
	"aliang.one/nursorgate/processor/egress"
)

const (
	// API调用超时
	apiTimeout = 10 * time.Second

	// scanAccessTokenTTLSeconds 是扫码登录落地的 st_(access_token) 在客户端侧登记的过期秒数。
	// official-website 的本地会话(st_)服务端寿命为 24h；客户端在到期前 10 分钟
	// 调用本地 refresh 续期。上游 access token 由网关按自身 exp 独立维护。
	scanAccessTokenTTLSeconds = 24 * 60 * 60
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
	authority := GetSessionAuthority()
	operation := authority.BeginOperation()
	defer operation.Close()

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
	req = req.WithContext(operation.Context())

	client := egress.PlatformClient(apiTimeout)

	resp, err := client.Do(req)
	if err != nil {
		if !authority.OperationCurrent(operation) {
			return nil, ErrStaleSessionOperation
		}
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

	return finalizeAuthenticatedSessionWithOperation(operation, response.Data.AccessToken, response.Data.RefreshToken, response.Data.TokenType,
		effectiveRefreshExpiresIn(response.Data.ExpiresIn, response.Data.UpstreamExpiresIn))
}

// finalizeAuthenticatedSession 把已取得的 access/refresh token 落地为本地登录态：拉取个人资料、
// 组装 UserInfo、持久化、启动刷新器、置位就绪标志。密码登录与扫码登录共用此收尾，
// 确保两条登录路径产出的本地态逐字段等价（下游 /me、刷新器、Authorization-Inner 行为一致）。
func finalizeAuthenticatedSession(accessToken, refreshToken, tokenType string, expiresIn int) (*UserInfo, error) {
	authority := GetSessionAuthority()
	operation := authority.BeginOperation()
	defer operation.Close()
	return finalizeAuthenticatedSessionWithOperation(operation, accessToken, refreshToken, tokenType, expiresIn)
}

func finalizeAuthenticatedSessionWithOperation(operation *SessionOperation, accessToken, refreshToken, tokenType string, expiresIn int) (*UserInfo, error) {
	profile, err := GetUserProfileWithToken(accessToken)
	if err != nil {
		if !GetSessionAuthority().OperationCurrent(operation) {
			return nil, ErrStaleSessionOperation
		}
		return nil, fmt.Errorf("failed to fetch user profile after login: %w", err)
	}

	userInfo := buildUserInfoFromProfile(profile)
	userInfo.AccessToken = accessToken
	userInfo.RefreshToken = refreshToken
	userInfo.TokenType = tokenType
	userInfo.ExpiresIn = expiresIn
	userInfo.UpdatedAt = time.Now()

	authority := GetSessionAuthority()
	snapshot, err := authority.CommitAuthenticatedSnapshot(operation, userInfo, ReasonLogin, func(committed *UserInfo) error {
		if err := SaveUserInfo(committed); err != nil {
			logger.Warn(fmt.Sprintf("Failed to save user info locally: %v", err))
		}
		config.SetUsingDefaultConfig(false)
		config.SetHasLocalUserInfo(true)
		return nil
	})
	if err != nil {
		return nil, err
	}
	authority.RunIfGenerationActive(snapshot.Generation, startTokenRefresh)

	return userInfo, nil
}

// ActivateWithTokens 用扫码登录拿到的本地令牌完成登录收尾。
// accessToken 和兼容字段 refreshToken 都由 official-website 签发；sub2api
// token 始终留在服务端 broker 中。
// upstreamExpiresIn 是扫码状态响应透传的上游 access JWT 真实剩余秒数；与
// scanAccessTokenTTLSeconds 常量取较小者作刷新计时锚（旧服务端缺省 → 0 → 回退常量），
// 与 LoginWithPassword/RefreshSession 的 effectiveRefreshExpiresIn 语义一致。
// 与 LoginWithPassword 走同一条 finalizeAuthenticatedSession，故扫码后状态与密码登录等价。
func ActivateWithTokens(accessToken, refreshToken string, upstreamExpiresIn int) (*UserInfo, error) {
	accessToken = strings.TrimSpace(accessToken)
	refreshToken = strings.TrimSpace(refreshToken)
	if accessToken == "" {
		return nil, fmt.Errorf("access token cannot be empty")
	}
	if refreshToken == "" {
		return nil, fmt.Errorf("refresh token cannot be empty")
	}
	expiresIn := effectiveRefreshExpiresIn(scanAccessTokenTTLSeconds, upstreamExpiresIn)
	authority := GetSessionAuthority()
	operation := authority.BeginOperation()
	defer operation.Close()
	return finalizeAuthenticatedSessionWithOperation(operation, accessToken, refreshToken, "Bearer", expiresIn)
}

func RestoreSession() (*UserInfo, error) {
	authority := GetSessionAuthority()
	operation := authority.BeginOperation()
	defer operation.Close()

	localUserInfo, err := LoadUserInfo()
	if err != nil {
		if commitErr := authority.CommitUnauthenticated(operation, ReasonNoSession); commitErr != nil && !errors.Is(commitErr, ErrStaleSessionOperation) {
			logger.Warn(fmt.Sprintf("Failed to publish missing local session: %v", commitErr))
		}
		return nil, fmt.Errorf("%w: %v", ErrNoLocalSession, err)
	}

	refreshedInfo, refreshErr := RefreshSession(localUserInfo.AccessToken)
	if refreshErr == nil {
		return refreshedInfo, nil
	}
	if errors.Is(refreshErr, ErrStaleSessionOperation) {
		snapshot := authority.Snapshot()
		if snapshot.State == StateActive && snapshot.User != nil {
			return snapshot.User, nil
		}
		return nil, refreshErr
	}
	if isTerminalSessionError(refreshErr) {
		return nil, refreshErr
	}
	if !authority.OperationCurrent(operation) {
		return nil, ErrStaleSessionOperation
	}

	if strings.TrimSpace(localUserInfo.AccessToken) == "" {
		config.SetHasLocalUserInfo(true)
		if err := authority.CommitSoftExpired(operation, localUserInfo, ReasonRestoreUnavailable); err != nil {
			return nil, err
		}
		return localUserInfo, fmt.Errorf("%w: refresh failed and saved access token is empty", ErrSessionRecovering)
	}

	profile, profileErr := GetUserProfileWithToken(localUserInfo.AccessToken)
	if profileErr != nil {
		if !authority.OperationCurrent(operation) {
			return nil, ErrStaleSessionOperation
		}
		logger.Warn(fmt.Sprintf("Session restore profile sync skipped: refresh failed (%v), profile fetch failed (%v)", refreshErr, profileErr))
		config.SetHasLocalUserInfo(true)
		if err := authority.CommitSoftExpired(operation, localUserInfo, ReasonRestoreUnavailable); err != nil {
			return nil, err
		}
		return localUserInfo, fmt.Errorf("%w: refresh failed: %v; profile failed: %v", ErrSessionRecovering, refreshErr, profileErr)
	}

	latestProfile := buildUserInfoFromProfile(profile)
	latestProfile.AccessToken = localUserInfo.AccessToken
	latestProfile.RefreshToken = localUserInfo.RefreshToken
	latestProfile.TokenType = localUserInfo.TokenType
	latestProfile.ExpiresIn = localUserInfo.ExpiresIn
	// 锚点纪律:此路径 refresh 失败、仅用旧 token 同步到了 profile——服务端
	// 并没有延长会话,锚点必须保持原值。拨到 now 会让续期触发点落在服务端
	// 死线之后(2026-11-12 登出事故的同型解耦写)。
	latestProfile.UpdatedAt = localUserInfo.UpdatedAt

	snapshot, err := authority.CommitAuthenticatedSnapshot(operation, latestProfile, ReasonRestored, func(committed *UserInfo) error {
		if err := SaveUserInfo(committed); err != nil {
			logger.Warn(fmt.Sprintf("Failed to save restored session profile: %v", err))
		}
		config.SetHasLocalUserInfo(true)
		return nil
	})
	if err != nil {
		return nil, err
	}
	authority.RunIfGenerationActive(snapshot.Generation, startTokenRefresh)

	return latestProfile, nil
}

func isTerminalSessionError(err error) bool {
	return errors.Is(err, ErrRefreshTokenInvalid) || errors.Is(err, ErrSessionExpired)
}

func RefreshSession(refreshToken string) (*UserInfo, error) {
	if !IsSessionOwnerProcess() {
		return nil, ErrSessionOwnerRequired
	}
	authority := GetSessionAuthority()
	operation := authority.BeginOperation()
	defer operation.Close()

	current := GetCurrentUserInfoOrLoad()
	token := ""
	if current != nil {
		// official-website owns the upstream token family. Its refresh endpoint
		// accepts this device's local st_ session, never a sub2api refresh token.
		token = strings.TrimSpace(current.AccessToken)
	}
	if token == "" {
		token = strings.TrimSpace(refreshToken)
	}
	if token == "" {
		return nil, fmt.Errorf("refresh token cannot be empty")
	}

	refreshSessionMu.Lock()
	defer refreshSessionMu.Unlock()

	// Another caller may have refreshed and rotated the token while we were
	// waiting for the lock. Reuse the newest local session to avoid sending an
	// already-invalidated refresh token again.
	current = GetCurrentUserInfoOrLoad()
	if current != nil {
		latestToken := strings.TrimSpace(current.AccessToken)
		if latestToken != "" && latestToken != token && strings.TrimSpace(current.AccessToken) != "" {
			logger.Debug("Refresh token rotated while waiting; reusing latest local session")
			snapshot := authority.Snapshot()
			if snapshot.State == StateActive && snapshot.User != nil {
				return snapshot.User, nil
			}
			return nil, ErrStaleSessionOperation
		}
		if latestToken != "" {
			token = latestToken
		}
	}
	if !authority.OperationCurrent(operation) {
		return nil, ErrStaleSessionOperation
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
	req = req.WithContext(operation.Context())

	client := egress.PlatformClient(apiTimeout)
	resp, err := client.Do(req)
	if err != nil {
		if !authority.OperationCurrent(operation) {
			return nil, ErrStaleSessionOperation
		}
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		if invalidTokenErr := classifyRefreshSessionFailure(resp.StatusCode, body); invalidTokenErr != nil {
			if !authority.OperationCurrent(operation) {
				return nil, ErrStaleSessionOperation
			}
			// 硬清除前的逾期诊断:把「锚点已判逾期多久」钉进日志,区分
			// 迟刷(≈20 分钟量级)与锚点被解耦写污染(以小时计)。
			if diagnosis := overdueDiagnostic(current, time.Now()); diagnosis != "" {
				logger.Info(diagnosis)
			}
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

	nextRefreshToken := strings.TrimSpace(response.Data.RefreshToken)
	if nextRefreshToken == "" {
		// The hardened broker uses the local st_ for both compatibility fields.
		// If an older server omits refresh_token, the newly returned local access
		// credential is the only safe fallback; never resurrect a legacy upstream
		// refresh token from local persistence.
		logger.Warn("Refresh response missing refresh_token; using renewed local access token")
		nextRefreshToken = strings.TrimSpace(response.Data.AccessToken)
	}

	userInfo := mergeRefreshedSessionWithCurrentUser(current, response.Data.AccessToken, nextRefreshToken, response.Data.TokenType,
		effectiveRefreshExpiresIn(response.Data.ExpiresIn, response.Data.UpstreamExpiresIn))

	profile, err := GetUserProfileWithToken(response.Data.AccessToken)
	if err != nil {
		if !authority.OperationCurrent(operation) {
			return nil, ErrStaleSessionOperation
		}
		var profileAPIError *authenticatedAPIError
		if errors.As(err, &profileAPIError) && profileAPIError.StatusCode == http.StatusUnauthorized {
			clearLocalSessionAfterExpiration("refreshed access token rejected by profile endpoint")
			return nil, fmt.Errorf("refreshed session rejected: %w", ErrSessionExpired)
		}
		logger.Warn(fmt.Sprintf("Profile sync after refresh failed, keeping last known user profile: %v", err))
	} else {
		applyUserProfileToUserInfo(userInfo, profile)
	}

	snapshot, err := authority.CommitAuthenticatedSnapshot(operation, userInfo, ReasonRefreshed, func(committed *UserInfo) error {
		if err := SaveUserInfo(committed); err != nil {
			logger.Warn(fmt.Sprintf("Failed to persist refreshed user info: %v", err))
		}
		config.SetHasLocalUserInfo(true)
		return nil
	})
	if err != nil {
		return nil, err
	}
	authority.RunIfGenerationActive(snapshot.Generation, func() {
		startTokenRefresh()
		// A fresh access_token was obtained (new exp). Notify the agent so it can
		// push it to PhoneServer while this generation is still authoritative.
		notifyAuthSuccessHandler()
	})

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
	token := ""
	current := GetCurrentUserInfoOrLoad()
	if current != nil {
		token = strings.TrimSpace(current.AccessToken)
	}
	if token == "" {
		token = strings.TrimSpace(refreshToken)
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
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	client := egress.PlatformClient(apiTimeout)
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
		// UpstreamExpiresIn 上游 sub2api access JWT 的真实剩余秒数；旧服务端不下发，
		// 缺失时为 0，由 effectiveRefreshExpiresIn 回退到本地会话的 ExpiresIn。
		UpstreamExpiresIn int `json:"upstream_expires_in"`
	} `json:"data"`
	Message string `json:"message"`
}

// effectiveRefreshExpiresIn 返回刷新计时应采用的剩余秒数：本地滚动会话 TTL 与
// 上游真实剩余寿命两个正值中的较小者——例行刷新必须赶在两者中先死的那个之前。
// 只看本地会退回 2026-09-20 事故（上游 ~7.5h 短票被当 24h 计时，全天零次刷新）；
// 无条件优先上游则退回 2026-10-04 事故（liang-dev：服务端 v1.0.49+ 的本地 st_
// 会话 24h 滚动过期，上游 6.7 天 TTL 被当成本地会话 TTL，例行续期永不发生，
// 死凭据撞上第一个鉴权请求才落 refresh_invalid）。一方缺失（≤0）时用另一方；
// 双双缺失返回 0，由调用方回退常量。
func effectiveRefreshExpiresIn(expiresIn, upstreamExpiresIn int) int {
	switch {
	case expiresIn <= 0:
		return upstreamExpiresIn
	case upstreamExpiresIn <= 0:
		return expiresIn
	case upstreamExpiresIn < expiresIn:
		return upstreamExpiresIn
	default:
		return expiresIn
	}
}

func buildUserInfoFromProfile(profile *UserProfile) *UserInfo {
	// UpdatedAt(续期锚点)留零:只有服务端真·签发/续期的路径才允许设置,
	// 强制每个调用方显式表态,杜绝「顺手拨锚点」的解耦写。
	info := &UserInfo{}
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
	tokenRefresherMu sync.RWMutex
	tokenRefresher   *TokenRefresher
	refreshSessionMu sync.Mutex

	// 看门狗单例:首次 startTokenRefresh 时拉起,常驻巡检循环活性。
	tokenRefresherWatchdogStarted bool
)

// startTokenRefresh 启动定时刷新
func startTokenRefresh() {
	if !IsSessionOwnerProcess() {
		return
	}

	tokenRefresherMu.Lock()
	defer tokenRefresherMu.Unlock()

	if tokenRefresher != nil && tokenRefresher.IsRunning() {
		return // 已经在运行
	}

	tokenRefresher = NewTokenRefresher()
	if err := tokenRefresher.Start(); err != nil {
		logger.Warn(fmt.Sprintf("Failed to start token refresher: %v", err))
	}

	if !tokenRefresherWatchdogStarted {
		tokenRefresherWatchdogStarted = true
		go tokenRefresherWatchdogLoop()
	}
}

// tokenRefresherWatchdogLoop 常驻巡检刷新循环活性。循环是单 goroutine 同步
// 消费 tick——楔死时 lastTickAt 停止前进且 IsRunning() 恒真,自愈门
// (startTokenRefresh 的运行中检查)永远打不开。看门狗是循环之外的独立
// goroutine,专治这一形态:停摆即告警+重建(Stop 旧实例 + 起新实例;
// 旧 goroutine 解阻后消费 done 退出,refreshSessionMu 保证过渡期串行)。
func tokenRefresherWatchdogLoop() {
	ticker := time.NewTicker(tokenRefreshWatchdogTick)
	defer ticker.Stop()
	for range ticker.C {
		rebuildStalledTokenRefresherIfNecessary()
	}
}

// rebuildStalledTokenRefresherIfNecessary 判定当前刷新器是否停摆并重建;
// 返回是否发生了重建。健康循环直接放行。
func rebuildStalledTokenRefresherIfNecessary() bool {
	tr := GetTokenRefresher()
	if tr == nil || !tr.IsRunning() {
		return false
	}
	lastTick := tr.LastTickAt()
	if lastTick.IsZero() || time.Since(lastTick) < tokenRefreshStallThreshold {
		return false
	}

	logger.Warn(fmt.Sprintf("[TokenRefresher] loop appears wedged: no tick consumed for %s (threshold %s); rebuilding refresher",
		time.Since(lastTick).Round(time.Second), tokenRefreshStallThreshold))
	notifyRefreshLivenessAlarm(refresherAlarmRebuilt)
	StopTokenRefresh()
	startTokenRefresh()
	return true
}

// 刷新器活性告警事件:看门狗重建楔死循环时触发。handlers 层可挂桌面通知。
const refresherAlarmRebuilt = "token_refresher_rebuilt_stalled_loop"

var (
	refreshLivenessAlarmMu sync.RWMutex
	refreshLivenessAlarm   func(event string)
)

// SetRefreshLivenessAlarmHandler 注册刷新器活性告警回调(如桌面通知)。
func SetRefreshLivenessAlarmHandler(handler func(event string)) {
	refreshLivenessAlarmMu.Lock()
	defer refreshLivenessAlarmMu.Unlock()
	refreshLivenessAlarm = handler
}

func notifyRefreshLivenessAlarm(event string) {
	refreshLivenessAlarmMu.RLock()
	handler := refreshLivenessAlarm
	refreshLivenessAlarmMu.RUnlock()

	if handler != nil {
		handler(event)
	}
}

// StopTokenRefresh 停止定时刷新
func StopTokenRefresh() {
	tokenRefresherMu.Lock()
	defer tokenRefresherMu.Unlock()

	if tokenRefresher != nil {
		if err := tokenRefresher.Stop(); err != nil {
			logger.Warn(fmt.Sprintf("Failed to stop token refresher: %v", err))
		}
	}
}

// GetTokenRefresher 获取Token刷新器实例
func GetTokenRefresher() *TokenRefresher {
	tokenRefresherMu.RLock()
	defer tokenRefresherMu.RUnlock()

	return tokenRefresher
}
