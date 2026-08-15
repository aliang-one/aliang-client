package models

import "time"

// ConfigGetRequest is the request to get specific config
type ConfigGetRequest struct {
	Name string `json:"name"`
}

// ConfigInfo represents configuration information
type ConfigInfo struct {
	Name string      `json:"name"`
	Data interface{} `json:"data"`
}

const (
	ConfigFormatJSON = "json"
	ConfigFormatYAML = "yaml"
)

type SoftwareConfig struct {
	UUID      string    `json:"uuid" gorm:"type:varchar(64);primaryKey"`
	Software  string    `json:"software" gorm:"type:varchar(128);not null;index"`
	Name      string    `json:"name" gorm:"type:varchar(255);not null;index"`
	FilePath  string    `json:"file_path" gorm:"type:text;not null"`
	Version   string    `json:"version" gorm:"type:varchar(128)"`
	InUse     bool      `json:"in_use" gorm:"not null;default:false"`
	Selected  bool      `json:"selected" gorm:"not null;default:false;index"`
	Format    string    `json:"format" gorm:"type:varchar(16);not null"`
	Content   string    `json:"content" gorm:"type:text;not null"`
	CreatedAt time.Time `json:"created_at" gorm:"autoCreateTime"`
	UpdatedAt time.Time `json:"updated_at" gorm:"autoUpdateTime;index"`
}

func (SoftwareConfig) TableName() string {
	return "software_configs"
}

type SaveSoftwareConfigRequest struct {
	UUID      string `json:"uuid"`
	Software  string `json:"software"`
	Name      string `json:"name"`
	FilePath  string `json:"file_path"`
	Version   string `json:"version"`
	InUse     bool   `json:"in_use"`
	Selected  bool   `json:"selected"`
	Format    string `json:"format"`
	Content   string `json:"content"`
	CreatedAt string `json:"created_at,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

type ActivateSoftwareConfigRequest struct {
	UUID      string `json:"uuid"`
	Software  string `json:"software"`
	Name      string `json:"name"`
	FilePath  string `json:"file_path"`
	Version   string `json:"version"`
	Format    string `json:"format"`
	Content   string `json:"content"`
	CreatedAt string `json:"created_at,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

type CloudPushRequest struct {
	CloudURL     string   `json:"cloud_url"`
	AuthToken    string   `json:"auth_token,omitempty"`
	OnlySelected bool     `json:"only_selected"`
	UUIDs        []string `json:"uuids,omitempty"`
}

type CloudPullRequest struct {
	CloudURL  string `json:"cloud_url"`
	AuthToken string `json:"auth_token,omitempty"`
}

type CloudConfigBatch struct {
	Configs []SoftwareConfig `json:"configs"`
}

type CloudPushResponse struct {
	PushedCount int `json:"pushed_count"`
}

type CloudPullResponse struct {
	PulledCount       int `json:"pulled_count"`
	InsertedCount     int `json:"inserted_count"`
	UpdatedFromCloud  int `json:"updated_from_cloud"`
	KeptLocalNewerCnt int `json:"kept_local_newer"`
}

type CloudSyncResponse struct {
	SyncedCount  int    `json:"synced_count"`
	LastSyncedAt string `json:"last_synced_at"`
}

type DeleteSoftwareConfigRequest struct {
	UUID string `json:"uuid"`
}

type SelectSoftwareConfigRequest struct {
	UUID     string `json:"uuid"`
	Selected bool   `json:"selected"`
}

type ConfigFreshnessItem struct {
	UUID           string `json:"uuid"`
	Software       string `json:"software"`
	Name           string `json:"name"`
	LocalUpdatedAt string `json:"local_updated_at,omitempty"`
	CloudUpdatedAt string `json:"cloud_updated_at,omitempty"`
	Status         string `json:"status"`
}

type CompareSoftwareConfigRequest struct {
	CloudURL  string `json:"cloud_url"`
	AuthToken string `json:"auth_token,omitempty"`
}

type CompareSoftwareConfigResponse struct {
	Items []ConfigFreshnessItem `json:"items"`
}

type SoftwareConfigOperationLog struct {
	ID         uint      `json:"id" gorm:"primaryKey;autoIncrement"`
	Action     string    `json:"action" gorm:"type:varchar(64);not null;index"`
	Software   string    `json:"software" gorm:"type:varchar(128);index"`
	ConfigUUID string    `json:"config_uuid" gorm:"type:varchar(64);index"`
	ConfigName string    `json:"config_name" gorm:"type:varchar(255)"`
	Detail     string    `json:"detail" gorm:"type:text"`
	CreatedAt  time.Time `json:"created_at" gorm:"autoCreateTime;index"`
}

func (SoftwareConfigOperationLog) TableName() string {
	return "software_config_operation_logs"
}

type SoftwareEffectiveConfigSnapshot struct {
	ID             uint      `json:"id" gorm:"primaryKey;autoIncrement"`
	Software       string    `json:"software" gorm:"type:varchar(128);not null;index"`
	ConfigUUID     string    `json:"config_uuid" gorm:"type:varchar(64);index"`
	ConfigName     string    `json:"config_name" gorm:"type:varchar(255)"`
	ConfigFilePath string    `json:"config_file_path" gorm:"type:text"`
	ConfigVersion  string    `json:"config_version" gorm:"type:varchar(128)"`
	ConfigFormat   string    `json:"config_format" gorm:"type:varchar(16)"`
	SnapshotJSON   string    `json:"snapshot_json" gorm:"type:text;not null"`
	CreatedAt      time.Time `json:"created_at" gorm:"autoCreateTime;index"`
}

func (SoftwareEffectiveConfigSnapshot) TableName() string {
	return "software_effective_config_snapshots"
}

type LogSoftwareConfigOperationRequest struct {
	Action     string `json:"action"`
	Software   string `json:"software"`
	ConfigUUID string `json:"config_uuid,omitempty"`
	ConfigName string `json:"config_name,omitempty"`
	Detail     string `json:"detail,omitempty"`
}
