package user

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"aliang.one/nursorgate/common/logger"
	"aliang.one/nursorgate/processor/config"
)

type UserProfile struct {
	ID            int64   `json:"id"`
	Email         string  `json:"email"`
	Username      string  `json:"username"`
	Role          string  `json:"role"`
	Balance       float64 `json:"balance"`
	Concurrency   int     `json:"concurrency"`
	Status        string  `json:"status"`
	AllowedGroups []int64 `json:"allowed_groups"`
	CreatedAt     string  `json:"created_at"`
	UpdatedAt     string  `json:"updated_at"`
}

type userProfileEnvelope struct {
	Data    UserProfile `json:"data"`
	Message string      `json:"message"`
}

type userCenterUsageSummary struct {
	ActiveCount   int               `json:"active_count"`
	TotalUsedUSD  float64           `json:"total_used_usd"`
	Subscriptions []json.RawMessage `json:"subscriptions"`
}

type userUsageSummaryEnvelope struct {
	Data    userCenterUsageSummary `json:"data"`
	Message string                 `json:"message"`
}

type UserUsageSummary struct {
	ActiveCount   int               `json:"active_count"`
	TotalUsedUSD  float64           `json:"total_used_usd"`
	Subscriptions []json.RawMessage `json:"subscriptions"`
}

type userUsageProgressEnvelope struct {
	Data    []json.RawMessage `json:"data"`
	Message string            `json:"message"`
}

type UserUsageProgress struct {
	Items []json.RawMessage `json:"items"`
}

type RedeemResult struct {
	Data json.RawMessage `json:"data"`
}

type APIKeyGroup struct {
	ID                    int64   `json:"id"`
	Name                  string  `json:"name"`
	Description           string  `json:"description"`
	Platform              string  `json:"platform"`
	RateMultiplier        float64 `json:"rate_multiplier"`
	ClaudeCodeOnly        bool    `json:"claude_code_only"`
	AllowMessagesDispatch bool    `json:"allow_messages_dispatch"`
}

type userAPIKeyGroupListEnvelope struct {
	Data    []APIKeyGroup `json:"data"`
	Message string        `json:"message"`
}

type UserAPIKey struct {
	ID              int64        `json:"id"`
	Key             string       `json:"key"`
	Name            string       `json:"name"`
	GroupID         *int64       `json:"group_id,omitempty"`
	Status          string       `json:"status"`
	Provider        string       `json:"provider"`
	Masked          bool         `json:"masked"`
	SecretAvailable bool         `json:"secret_available"`
	Group           *APIKeyGroup `json:"group,omitempty"`
}

type userAPIKeyListItem struct {
	ID      int64        `json:"id"`
	Key     string       `json:"key"`
	Name    string       `json:"name"`
	GroupID *int64       `json:"group_id"`
	Status  string       `json:"status"`
	Group   *APIKeyGroup `json:"group,omitempty"`
}

type userAPIKeyListEnvelope struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    struct {
		Items    []userAPIKeyListItem `json:"items"`
		Total    int                  `json:"total"`
		Page     int                  `json:"page"`
		PageSize int                  `json:"page_size"`
		Pages    int                  `json:"pages"`
	} `json:"data"`
}

type authenticatedAPIError struct {
	Endpoint   string
	StatusCode int
	Body       []byte
}

func (e *authenticatedAPIError) Error() string {
	return fmt.Sprintf("api %s returned status %d: %s", e.Endpoint, e.StatusCode, string(e.Body))
}

func resolveAccessToken() (string, error) {
	current := GetCurrentUserInfoOrLoad()
	if current == nil {
		return "", fmt.Errorf("no user session")
	}
	token := strings.TrimSpace(current.AccessToken)
	if token == "" {
		return "", fmt.Errorf("missing access token")
	}
	return token, nil
}

func callAuthenticatedAPI(method, endpoint, accessToken string, body any) ([]byte, error) {
	var reqBody io.Reader
	if body != nil {
		jsonData, marshalErr := json.Marshal(body)
		if marshalErr != nil {
			return nil, fmt.Errorf("failed to marshal request: %w", marshalErr)
		}
		reqBody = bytes.NewBuffer(jsonData)
	}

	req, err := http.NewRequest(method, endpoint, reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", accessToken))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	applyEndpointSpecificHeaders(req)

	client := &http.Client{Timeout: apiTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, &authenticatedAPIError{
			Endpoint:   endpoint,
			StatusCode: resp.StatusCode,
			Body:       responseBody,
		}
	}

	return responseBody, nil
}

func callUserCenterAPI(method, endpoint string, body any) ([]byte, error) {
	authToken, err := resolveAuthTokenForEndpoint(endpoint)
	if err != nil {
		return nil, err
	}
	responseBody, err := callAuthenticatedAPI(method, endpoint, authToken, body)
	if err != nil {
		var apiErr *authenticatedAPIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusUnauthorized {
			// A 401 can be transient (backend deploy, load-balancer hiccup, clock
			// skew, or a momentary network failure surfacing as 401). Confirm the
			// session is really dead before wiping — wiping forces a full re-login
			// and is the reason a single hiccup took the agent offline.
			if sessionConfirmedDeadAfter401() {
				return nil, ErrSessionExpired
			}
			// Transient: retain the session (and the freshly refreshed token) so
			// the caller retries next cycle instead of forcing a re-login.
			return nil, err
		}
		return nil, err
	}
	return responseBody, nil
}

// sessionConfirmedDeadAfter401 decides whether a 401 from a user-center API
// really means the local session is dead. It probes liveness by attempting a
// token refresh:
//   - refresh succeeds  -> the session is alive; the 401 was spurious.
//   - refresh fails      -> if the refresh attempt itself invalidated the
//                           session (refresh token rejected) the session is
//                           genuinely dead; otherwise the refresh failure was
//                           transient (e.g. network) and we keep the session so
//                           the caller retries later.
//
// Returns true only when the session is confirmed dead. In that case the
// session has already been wiped by the refresh path, so callers must NOT wipe
// again (doing so would re-fire the auth-expired side effects).
func sessionConfirmedDeadAfter401() bool {
	if refreshed, refreshErr := RefreshSession(""); refreshErr == nil && refreshed != nil {
		logger.Warn("user-center API returned 401 but token refresh succeeded; treating 401 as transient and retaining session")
		return false
	}
	if GetCurrentUserInfoOrLoad() == nil {
		return true
	}
	logger.Warn("user-center API returned 401 and refresh failed transiently; retaining session for retry")
	return false
}

func resolveAuthTokenForEndpoint(endpoint string) (string, error) {
	_ = endpoint

	// 在拿 token 前先尝试刷新
	refresher := GetTokenRefresher()
	if refresher != nil && refresher.IsRunning() {
		_ = refresher.RefreshNow() // 忽略错误，继续用现有 token 尝试
	}

	current := GetCurrentUserInfoOrLoad()
	if current == nil {
		return "", fmt.Errorf("no user session")
	}

	token := strings.TrimSpace(current.AccessToken)
	if token == "" {
		return "", fmt.Errorf("missing access token")
	}
	return token, nil
}

func applyEndpointSpecificHeaders(req *http.Request) {
	// no-op: kept for forward compatibility
}

func verifyCurrentSessionWithAuthMe(accessToken string, originalErr error) error {
	var apiErr *authenticatedAPIError
	if !errors.As(originalErr, &apiErr) || apiErr.StatusCode != http.StatusUnauthorized {
		return nil
	}

	urlBuilder, err := config.NewURLBuilder()
	if err != nil {
		return nil
	}
	authMeURL, err := urlBuilder.GetAuthMeURL()
	if err != nil {
		return nil
	}

	_, err = callAuthenticatedAPI(http.MethodGet, authMeURL, accessToken, nil)
	if err == nil {
		return nil
	}

	var authMeErr *authenticatedAPIError
	if !errors.As(err, &authMeErr) {
		return nil
	}
	if !errors.Is(classifyAccessTokenFailure(authMeErr.StatusCode, authMeErr.Body), ErrSessionExpired) {
		return nil
	}

	clearLocalSessionAfterExpiredAccessToken()
	return ErrSessionExpired
}

func GetUserProfileWithToken(accessToken string) (*UserProfile, error) {
	if strings.TrimSpace(accessToken) == "" {
		return nil, fmt.Errorf("missing access token")
	}

	urlBuilder, err := config.NewURLBuilder()
	if err != nil {
		return nil, err
	}
	profileURL, err := urlBuilder.GetUserProfileURL()
	if err != nil {
		return nil, err
	}

	body, err := callAuthenticatedAPI(http.MethodGet, profileURL, accessToken, nil)
	if err != nil {
		return nil, err
	}

	var envelope userProfileEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return &envelope.Data, nil
}

func GetUserProfile() (*UserProfile, error) {
	if _, err := resolveAccessToken(); err != nil {
		return nil, err
	}

	urlBuilder, err := config.NewURLBuilder()
	if err != nil {
		return nil, err
	}
	profileURL, err := urlBuilder.GetUserProfileURL()
	if err != nil {
		return nil, err
	}

	body, err := callUserCenterAPI(http.MethodGet, profileURL, nil)
	if err != nil {
		return nil, err
	}

	var envelope userProfileEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return &envelope.Data, nil
}

func UpdateUserProfile(username string) (*UserProfile, error) {
	trimmed := strings.TrimSpace(username)
	if trimmed == "" {
		return nil, fmt.Errorf("username cannot be empty")
	}

	if _, err := resolveAccessToken(); err != nil {
		return nil, err
	}

	urlBuilder, err := config.NewURLBuilder()
	if err != nil {
		return nil, err
	}
	updateURL, err := urlBuilder.GetUserUpdateURL()
	if err != nil {
		return nil, err
	}

	body, err := callUserCenterAPI(http.MethodPut, updateURL, map[string]string{"username": trimmed})
	if err != nil {
		return nil, err
	}

	var envelope userProfileEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	current := GetCurrentUserInfo()
	if current != nil {
		applyUserProfileToUserInfo(current, &envelope.Data)
		if strings.TrimSpace(current.Username) == "" {
			current.Username = trimmed
		}
		current.UpdatedAt = time.Now()
		if err := SaveUserInfo(current); err != nil {
			return &envelope.Data, nil
		}
	}

	return &envelope.Data, nil
}

func GetUserUsageSummary() (*UserUsageSummary, error) {
	if _, err := resolveAccessToken(); err != nil {
		return nil, err
	}

	urlBuilder, err := config.NewURLBuilder()
	if err != nil {
		return nil, err
	}
	usageURL, err := urlBuilder.GetSubscriptionsSummaryURL()
	if err != nil {
		return nil, err
	}

	body, err := callUserCenterAPI(http.MethodGet, usageURL, nil)
	if err != nil {
		return nil, err
	}

	var envelope userUsageSummaryEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return &UserUsageSummary{
		ActiveCount:   envelope.Data.ActiveCount,
		TotalUsedUSD:  envelope.Data.TotalUsedUSD,
		Subscriptions: envelope.Data.Subscriptions,
	}, nil
}

func GetUserUsageProgress() (*UserUsageProgress, error) {
	if _, err := resolveAccessToken(); err != nil {
		return nil, err
	}

	urlBuilder, err := config.NewURLBuilder()
	if err != nil {
		return nil, err
	}
	progressURL, err := urlBuilder.GetSubscriptionsProgressURL()
	if err != nil {
		return nil, err
	}

	body, err := callUserCenterAPI(http.MethodGet, progressURL, nil)
	if err != nil {
		return nil, err
	}

	var envelope userUsageProgressEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return &UserUsageProgress{Items: envelope.Data}, nil
}

func GetAvailableAPIKeyGroups() ([]APIKeyGroup, error) {
	if _, err := resolveAccessToken(); err != nil {
		return nil, err
	}

	urlBuilder, err := config.NewURLBuilder()
	if err != nil {
		return nil, err
	}
	groupsURL, err := urlBuilder.GetAvailableGroupsURL()
	if err != nil {
		return nil, err
	}

	body, err := callUserCenterAPI(http.MethodGet, groupsURL, nil)
	if err != nil {
		if isHTTP404Error(err) {
			return []APIKeyGroup{}, nil
		}
		return nil, err
	}

	var envelope userAPIKeyGroupListEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return envelope.Data, nil
}

func GetUserAPIKeys() ([]UserAPIKey, error) {
	if _, err := resolveAccessToken(); err != nil {
		return nil, err
	}

	urlBuilder, err := config.NewURLBuilder()
	if err != nil {
		return nil, err
	}
	keysURL, err := urlBuilder.GetUserAPIKeysURL()
	if err != nil {
		return nil, err
	}

	groupList, groupErr := GetAvailableAPIKeyGroups()
	groupByID := make(map[int64]APIKeyGroup, len(groupList))
	for _, group := range groupList {
		groupByID[group.ID] = group
	}

	page := 1
	perPage := 200
	var items []UserAPIKey
	for {
		endpoint := fmt.Sprintf("%s?page=%d&page_size=%d&timezone=Asia%%2FShanghai", keysURL, page, perPage)
		body, err := callUserCenterAPI(http.MethodGet, endpoint, nil)
		if err != nil {
			if isHTTP404Error(err) {
				return nil, buildAPIKeysEndpointNotFoundError(endpoint)
			}
			return nil, err
		}

		var envelope userAPIKeyListEnvelope
		if err := json.Unmarshal(body, &envelope); err != nil {
			return nil, fmt.Errorf("failed to parse response: %w", err)
		}

		if envelope.Code != 0 && strings.TrimSpace(envelope.Message) != "" {
			return nil, fmt.Errorf("api %s failed: %s", endpoint, strings.TrimSpace(envelope.Message))
		}

		for _, item := range envelope.Data.Items {
			group := item.Group
			if group == nil && item.GroupID != nil {
				if matched, ok := groupByID[*item.GroupID]; ok {
					copyGroup := matched
					group = &copyGroup
				}
			}

			keyValue := strings.TrimSpace(item.Key)
			provider := ""
			if group != nil {
				provider = strings.ToLower(strings.TrimSpace(group.Platform))
			}
			if provider == "" {
				provider = inferProviderFromAPIKeyMetadata(item.Name, keyValue)
			}
			items = append(items, UserAPIKey{
				ID:              item.ID,
				Key:             keyValue,
				Name:            strings.TrimSpace(item.Name),
				GroupID:         item.GroupID,
				Status:          strings.TrimSpace(item.Status),
				Provider:        provider,
				Masked:          looksMaskedAPIKey(keyValue),
				SecretAvailable: keyValue != "" && !looksMaskedAPIKey(keyValue),
				Group:           group,
			})
		}

		if envelope.Data.Pages <= 1 || page >= envelope.Data.Pages || len(envelope.Data.Items) == 0 {
			break
		}
		page++
	}

	if len(items) == 0 && groupErr != nil {
		return nil, groupErr
	}

	return items, nil
}

func looksMaskedAPIKey(value string) bool {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return false
	}
	return strings.Contains(trimmed, "***") || strings.Contains(trimmed, "…") || strings.Contains(trimmed, "...")
}

func isHTTP404Error(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "status 404")
}

func inferProviderFromAPIKeyMetadata(name string, key string) string {
	combined := strings.ToLower(strings.TrimSpace(name + " " + key))
	switch {
	case strings.Contains(combined, "anthropic"), strings.Contains(combined, "claude"):
		return "anthropic"
	case strings.Contains(combined, "openai"), strings.Contains(combined, "gpt"), strings.Contains(combined, "codex"):
		return "openai"
	default:
		return ""
	}
}

func buildAPIKeysEndpointNotFoundError(endpoint string) error {
	configuredBaseURL := ""
	if cfg := config.GetGlobalConfig(); cfg != nil {
		configuredBaseURL = strings.TrimSpace(cfg.APIBaseURL())
	}

	var detail strings.Builder
	detail.WriteString("quick setup could not load API keys because the configured account backend does not expose the expected API key endpoint")
	if configuredBaseURL != "" {
		detail.WriteString(fmt.Sprintf(" (core.api_server=%s)", configuredBaseURL))
	}
	if strings.TrimSpace(endpoint) != "" {
		detail.WriteString(fmt.Sprintf("; requested %s", endpoint))
	}
	detail.WriteString("; update core.api_server to your real account backend host")
	return fmt.Errorf("%s", detail.String())
}

func RedeemCode(code string) (*RedeemResult, error) {
	trimmed := strings.TrimSpace(code)
	if trimmed == "" {
		return nil, fmt.Errorf("redeem code cannot be empty")
	}

	if _, err := resolveAccessToken(); err != nil {
		return nil, err
	}

	urlBuilder, err := config.NewURLBuilder()
	if err != nil {
		return nil, err
	}
	redeemURL, err := urlBuilder.GetRedeemURL()
	if err != nil {
		return nil, err
	}

	body, err := callUserCenterAPI(http.MethodPost, redeemURL, map[string]string{"code": trimmed})
	if err != nil {
		return nil, err
	}

	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return &RedeemResult{Data: envelope.Data}, nil
}
