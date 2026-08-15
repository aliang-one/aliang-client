package models

import "time"

type SoftwareVersionUpdateSnapshot struct {
	ID             uint      `json:"id" gorm:"primaryKey;autoIncrement"`
	Software       string    `json:"software" gorm:"type:varchar(128);not null;uniqueIndex:idx_version_update_snapshot_lookup"`
	Platform       string    `json:"platform" gorm:"type:varchar(32);not null;uniqueIndex:idx_version_update_snapshot_lookup"`
	CurrentVersion string    `json:"current_version" gorm:"type:varchar(128)"`
	LatestVersion  string    `json:"latest_version" gorm:"type:varchar(128)"`
	DownloadURL    string    `json:"download_url" gorm:"type:text"`
	FileType       string    `json:"file_type" gorm:"type:varchar(32)"`
	Changelog      string    `json:"changelog" gorm:"type:text"`
	NeedsUpdate    bool      `json:"needs_update" gorm:"not null;default:false;index"`
	ForceUpdate    bool      `json:"force_update" gorm:"not null;default:false"`
	Status         string    `json:"status" gorm:"type:varchar(32);not null;default:'unknown'"`
	LastError      string    `json:"last_error" gorm:"type:text"`
	CheckedAt      time.Time `json:"checked_at" gorm:"index"`
	FirstSeenAt    time.Time `json:"first_seen_at"`
	LastSeenAt     time.Time `json:"last_seen_at"`
	CreatedAt      time.Time `json:"created_at" gorm:"autoCreateTime"`
	UpdatedAt      time.Time `json:"updated_at" gorm:"autoUpdateTime;index"`
}

func (SoftwareVersionUpdateSnapshot) TableName() string {
	return "software_version_update_snapshots"
}

type SoftwareVersionUpdateDismissal struct {
	ID            uint      `json:"id" gorm:"primaryKey;autoIncrement"`
	Software      string    `json:"software" gorm:"type:varchar(128);not null;uniqueIndex:idx_version_update_dismissal_lookup"`
	Platform      string    `json:"platform" gorm:"type:varchar(32);not null;uniqueIndex:idx_version_update_dismissal_lookup"`
	LatestVersion string    `json:"latest_version" gorm:"type:varchar(128);not null;uniqueIndex:idx_version_update_dismissal_lookup"`
	DismissedAt   time.Time `json:"dismissed_at" gorm:"not null;index"`
	CreatedAt     time.Time `json:"created_at" gorm:"autoCreateTime"`
	UpdatedAt     time.Time `json:"updated_at" gorm:"autoUpdateTime"`
}

func (SoftwareVersionUpdateDismissal) TableName() string {
	return "software_version_update_dismissals"
}

type SoftwareVersionUpdateFrontendStatus struct {
	Software           string `json:"software"`
	Platform           string `json:"platform"`
	CurrentVersion     string `json:"current_version"`
	LatestVersion      string `json:"latest_version"`
	DownloadURL        string `json:"download_url"`
	FileType           string `json:"file_type"`
	Changelog          string `json:"changelog"`
	NeedsUpdate        bool   `json:"needs_update"`
	ForceUpdate        bool   `json:"force_update"`
	Dismissed          bool   `json:"dismissed"`
	ShowModal          bool   `json:"show_modal"`
	IndicatorVisible   bool   `json:"indicator_visible"`
	BlockingProxyStart bool   `json:"blocking_proxy_start"`
	Status             string `json:"status"`
	LastError          string `json:"last_error,omitempty"`
	CheckedAtUnix      int64  `json:"checked_at_unix"`
	FirstSeenAtUnix    int64  `json:"first_seen_at_unix"`
	LastSeenAtUnix     int64  `json:"last_seen_at_unix"`
	DismissedAtUnix    int64  `json:"dismissed_at_unix"`
}
