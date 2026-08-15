package model

import "time"

type Cache struct {
	UserInfo      User           `json:"user_info"`
	TotalAskCount int            `json:"total_ask_count"`
	Req           map[string]Req `json:"req"`
}

type Req struct {
	ModelName  string   `json:"model_name"`
	AskCount   int      `json:"ask_count"`
	TokenUsage int      `json:"token_usage"`
	Records    []Record `json:"records"`
}

type Record struct {
	CursorID  int    `json:"cursor_id"`
	AskTime   int    `json:"ask_time"`
	ModelName string `json:"model_name"`
	UserID    int    `json:"user_id"`
}

type LoginStatus struct {
	Username  string    `json:"username"`
	ExpiredAt time.Time `json:"expired_at"`
	LastLogin time.Time `json:"last_login"`
	UserID    int       `json:"user_id"`
	CursorID  int       `json:"cursor_id"`
}
