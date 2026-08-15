package models

import "encoding/json"

type UpdateProfileRequest struct {
	Username string `json:"username"`
}

type RedeemCodeRequest struct {
	Code string `json:"code"`
}

type UserProfileResponse struct {
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

type UsageSummaryResponse struct {
	ActiveCount   int               `json:"active_count"`
	TotalUsedUSD  float64           `json:"total_used_usd"`
	Subscriptions []json.RawMessage `json:"subscriptions"`
}

type UsageProgressResponse struct {
	Items []json.RawMessage `json:"items"`
}

type RedeemCodeResponse struct {
	Data json.RawMessage `json:"data"`
}

type APIKeyGroupResponse struct {
	ID                    int64   `json:"id"`
	Name                  string  `json:"name"`
	Description           string  `json:"description"`
	Platform              string  `json:"platform"`
	RateMultiplier        float64 `json:"rate_multiplier"`
	ClaudeCodeOnly        bool    `json:"claude_code_only"`
	AllowMessagesDispatch bool    `json:"allow_messages_dispatch"`
}

type UserAPIKeyResponse struct {
	ID              int64                `json:"id"`
	Key             string               `json:"key"`
	Name            string               `json:"name"`
	GroupID         *int64               `json:"group_id,omitempty"`
	Status          string               `json:"status"`
	Provider        string               `json:"provider"`
	Masked          bool                 `json:"masked"`
	SecretAvailable bool                 `json:"secret_available"`
	Group           *APIKeyGroupResponse `json:"group,omitempty"`
}

type UserAPIKeyListResponse struct {
	Items []UserAPIKeyResponse `json:"items"`
}
