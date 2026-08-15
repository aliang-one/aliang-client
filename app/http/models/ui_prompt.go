package models

import "time"

type UIPromptState struct {
	ID        uint      `json:"id" gorm:"primaryKey;autoIncrement"`
	PromptKey string    `json:"prompt_key" gorm:"type:varchar(128);not null;uniqueIndex"`
	SeenAt    time.Time `json:"seen_at" gorm:"not null;index"`
	CreatedAt time.Time `json:"created_at" gorm:"autoCreateTime"`
	UpdatedAt time.Time `json:"updated_at" gorm:"autoUpdateTime"`
}

func (UIPromptState) TableName() string {
	return "ui_prompt_states"
}
