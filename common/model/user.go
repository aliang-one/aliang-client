package model

import (
	"time"

	"gorm.io/gorm"
)

type User struct {
	gorm.Model
	MembershipType string    `json:"membership_type"`
	SalesChannel   string    `json:"sales_channel"`
	IsDispatched   bool      `json:"is_dispatched"`
	IsFree         bool      `json:"is_free"`
	Name           string    `json:"name"`
	Email          string    `json:"email"`
	Password       string    `json:"password"`
	AccessToken    string    `json:"access_token"`
	RefreshToken   string    `json:"refresh_token"`
	Limit          int       `json:"limit"`
	Usage          int       `json:"usage"`
	ExpiredAt      time.Time `json:"expired_at"`
	CreatedAt      time.Time `json:"created_at" gorm:"autoCreateTime"`
	UpdatedAt      time.Time `json:"updated_at" gorm:"autoUpdateTime"`
}

func (User) TableName() string {
	return "user_user"
}
