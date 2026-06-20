package models

type LoginRequest struct {
	Email          string `json:"email"`
	Password       string `json:"password"`
	TurnstileToken string `json:"turnstile_token,omitempty"`
}

type RefreshTokenRequest struct {
	RefreshToken string `json:"refresh_token,omitempty"`
}

type LogoutRequest struct {
	RefreshToken string `json:"refresh_token,omitempty"`
}

// ScanActivateRequest 扫码登录激活请求：authorized 状态下 PC 从轮询结果中拿到
// session_token(st_) + refresh_token(sub2api)，提交此接口完成与密码登录等价的本地登录。
type ScanActivateRequest struct {
	SessionToken string `json:"session_token,omitempty"`
	RefreshToken string `json:"refresh_token,omitempty"`
}

// UserInfoResponse 用户信息响应
type UserInfoResponse struct {
	ID             int64   `json:"id"`
	Username       string  `json:"username"`
	Email          string  `json:"email,omitempty"`
	Role           string  `json:"role,omitempty"`
	Status         string  `json:"status,omitempty"`
	Balance        float64 `json:"balance"`
	Concurrency    int     `json:"concurrency"`
	AllowedGroups  []int64 `json:"allowed_groups,omitempty"`
	CreatedAt      string  `json:"created_at,omitempty"`
	ProfileUpdated string  `json:"profile_updated_at,omitempty"`
	ExpiresIn      int     `json:"expires_in,omitempty"`
	ExpiresAt      string  `json:"expires_at,omitempty"`
	UpdatedAt      string  `json:"updated_at"`
}
