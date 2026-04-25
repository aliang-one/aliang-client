package user

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"aliang.one/nursorgate/common/cache"
	"aliang.one/nursorgate/common/logger"
)

// UserInfo is the locally persisted Sub2API session snapshot.
type UserInfo struct {
	AccessToken    string    `json:"access_token"`
	RefreshToken   string    `json:"refresh_token"`
	TokenType      string    `json:"token_type,omitempty"`
	ExpiresIn      int       `json:"expires_in,omitempty"`
	ID             int64     `json:"id,omitempty"`
	Email          string    `json:"email,omitempty"`
	Username       string    `json:"username,omitempty"`
	Role           string    `json:"role,omitempty"`
	Balance        float64   `json:"balance,omitempty"`
	Concurrency    int       `json:"concurrency,omitempty"`
	Status         string    `json:"status,omitempty"`
	AllowedGroups  []int64   `json:"allowed_groups,omitempty"`
	CreatedAt      string    `json:"created_at,omitempty"`
	ProfileUpdated string    `json:"profile_updated_at,omitempty"`
	UpdatedAt      time.Time `json:"updated_at"`
}

var (
	userInfoMutex     sync.RWMutex
	currentUserInfo   *UserInfo
	authSessionDBOnce sync.Once
	authSessionDB     *gorm.DB
	authSessionDBErr  error
)

const (
	authTokenRecordID   = 1
	authProfileRecordID = 1
)

type authTokenRecord struct {
	ID           uint      `gorm:"primaryKey"`
	AccessToken  string    `gorm:"type:text;not null"`
	RefreshToken string    `gorm:"type:text"`
	TokenType    string    `gorm:"type:varchar(32)"`
	ExpiresIn    int       `gorm:"not null;default:0"`
	UpdatedAt    time.Time `gorm:"not null"`
}

func (authTokenRecord) TableName() string {
	return "sub2api_auth_tokens"
}

type authProfileRecord struct {
	ID                uint      `gorm:"primaryKey"`
	UserID            int64     `gorm:"not null;default:0;index"`
	Email             string    `gorm:"type:varchar(255);index"`
	Username          string    `gorm:"type:varchar(255)"`
	Role              string    `gorm:"type:varchar(64)"`
	Balance           float64   `gorm:"not null;default:0"`
	Concurrency       int       `gorm:"not null;default:0"`
	Status            string    `gorm:"type:varchar(64);index"`
	AllowedGroupsJSON string    `gorm:"column:allowed_groups_json;type:text"`
	RemoteCreatedAt   string    `gorm:"column:remote_created_at;type:varchar(64)"`
	RemoteUpdatedAt   string    `gorm:"column:remote_updated_at;type:varchar(64)"`
	UpdatedAt         time.Time `gorm:"not null"`
}

func (authProfileRecord) TableName() string {
	return "sub2api_user_profiles"
}

// GetAuthSessionDBPath returns the canonical shared SQLite data file path.
func GetAuthSessionDBPath() (string, error) {
	return cache.GetUnifiedDataDBPath()
}

// InitializeAuthPersistence ensures the shared auth tables exist.
func InitializeAuthPersistence() error {
	_, err := getAuthSessionDB()
	return err
}

// SaveUserInfo persists the current Sub2API session snapshot.
func SaveUserInfo(info *UserInfo) error {
	if info == nil {
		return fmt.Errorf("user info cannot be nil")
	}

	info.UpdatedAt = time.Now()

	// 内存优先 — 运行时正确性依赖此顺序
	userInfoMutex.Lock()
	copyInfo := *info
	currentUserInfo = &copyInfo
	userInfoMutex.Unlock()

	// SQLite 尽力写入，失败不影响内存
	if err := saveUserInfoToSQLite(info); err != nil {
		logger.Warn(fmt.Sprintf("Failed to persist user info to sqlite (memory is up-to-date): %v", err))
		return nil
	}

	logger.Debug("User info saved successfully (memory + sqlite)")
	return nil
}

// LoadUserInfo loads the current Sub2API session snapshot from aliang.db.
func LoadUserInfo() (*UserInfo, error) {
	if err := InitializeAuthPersistence(); err != nil {
		return nil, err
	}

	sqliteInfo, err := loadUserInfoFromSQLite()
	if err != nil {
		return nil, fmt.Errorf("failed to load user info from sqlite: %w", err)
	}

	userInfoMutex.Lock()
	copyInfo := *sqliteInfo
	currentUserInfo = &copyInfo
	userInfoMutex.Unlock()

	return sqliteInfo, nil
}

// UpdateUserInfo updates the local user snapshot.
func UpdateUserInfo(info *UserInfo) error {
	return SaveUserInfo(info)
}

// DeleteUserInfo deletes all persisted auth session data from aliang.db.
func DeleteUserInfo() error {
	if err := deleteUserInfoFromSQLite(); err != nil {
		return err
	}

	userInfoMutex.Lock()
	currentUserInfo = nil
	userInfoMutex.Unlock()

	return nil
}

// HasPersistedUserInfo reports whether a local auth session exists in aliang.db.
func HasPersistedUserInfo() (bool, error) {
	db, err := getAuthSessionDB()
	if err != nil {
		return false, err
	}

	var count int64
	if err := db.Model(&authTokenRecord{}).Where("id = ?", authTokenRecordID).Count(&count).Error; err != nil {
		return false, err
	}

	return count > 0, nil
}

func getAuthSessionDB() (*gorm.DB, error) {
	authSessionDBOnce.Do(func() {
		dbPath, err := GetAuthSessionDBPath()
		if err != nil {
			authSessionDBErr = err
			return
		}

		if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
			authSessionDBErr = fmt.Errorf("failed to create auth session db directory: %w", err)
			return
		}

		db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
		if err != nil {
			authSessionDBErr = fmt.Errorf("failed to open sqlite database: %w", err)
			return
		}

		if err := db.AutoMigrate(&authTokenRecord{}, &authProfileRecord{}); err != nil {
			authSessionDBErr = fmt.Errorf("failed to migrate auth session tables: %w", err)
			return
		}

		authSessionDB = db
	})

	return authSessionDB, authSessionDBErr
}

func saveUserInfoToSQLite(info *UserInfo) error {
	db, err := getAuthSessionDB()
	if err != nil {
		return err
	}
	return saveUserInfoWithDB(db, info)
}

func saveUserInfoWithDB(db *gorm.DB, info *UserInfo) error {
	allowedGroupsJSON, err := json.Marshal(info.AllowedGroups)
	if err != nil {
		return fmt.Errorf("failed to marshal allowed groups: %w", err)
	}

	token := authTokenRecord{
		ID:           authTokenRecordID,
		AccessToken:  info.AccessToken,
		RefreshToken: info.RefreshToken,
		TokenType:    info.TokenType,
		ExpiresIn:    info.ExpiresIn,
		UpdatedAt:    info.UpdatedAt,
	}

	profile := authProfileRecord{
		ID:                authProfileRecordID,
		UserID:            info.ID,
		Email:             info.Email,
		Username:          info.Username,
		Role:              info.Role,
		Balance:           info.Balance,
		Concurrency:       info.Concurrency,
		Status:            info.Status,
		AllowedGroupsJSON: string(allowedGroupsJSON),
		RemoteCreatedAt:   info.CreatedAt,
		RemoteUpdatedAt:   info.ProfileUpdated,
		UpdatedAt:         info.UpdatedAt,
	}

	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Save(&token).Error; err != nil {
			return err
		}
		if err := tx.Save(&profile).Error; err != nil {
			return err
		}
		return nil
	})
}

func loadUserInfoFromSQLite() (*UserInfo, error) {
	db, err := getAuthSessionDB()
	if err != nil {
		return nil, err
	}

	var token authTokenRecord
	if err := db.First(&token, "id = ?", authTokenRecordID).Error; err != nil {
		return nil, err
	}

	var profile authProfileRecord
	if err := db.First(&profile, "id = ?", authProfileRecordID).Error; err != nil && err != gorm.ErrRecordNotFound {
		return nil, err
	}

	var allowedGroups []int64
	if profile.AllowedGroupsJSON != "" {
		if err := json.Unmarshal([]byte(profile.AllowedGroupsJSON), &allowedGroups); err != nil {
			return nil, fmt.Errorf("failed to unmarshal allowed groups: %w", err)
		}
	}

	info := &UserInfo{
		AccessToken:    token.AccessToken,
		RefreshToken:   token.RefreshToken,
		TokenType:      token.TokenType,
		ExpiresIn:      token.ExpiresIn,
		ID:             profile.UserID,
		Email:          profile.Email,
		Username:       profile.Username,
		Role:           profile.Role,
		Balance:        profile.Balance,
		Concurrency:    profile.Concurrency,
		Status:         profile.Status,
		AllowedGroups:  allowedGroups,
		CreatedAt:      profile.RemoteCreatedAt,
		ProfileUpdated: profile.RemoteUpdatedAt,
		UpdatedAt:      token.UpdatedAt,
	}

	if info.UpdatedAt.IsZero() {
		info.UpdatedAt = profile.UpdatedAt
	}

	return info, nil
}

func deleteUserInfoFromSQLite() error {
	db, err := getAuthSessionDB()
	if err != nil {
		return err
	}

	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Delete(&authTokenRecord{}, "id = ?", authTokenRecordID).Error; err != nil {
			return fmt.Errorf("failed to delete auth token record: %w", err)
		}
		if err := tx.Delete(&authProfileRecord{}, "id = ?", authProfileRecordID).Error; err != nil {
			return fmt.Errorf("failed to delete auth profile record: %w", err)
		}
		return nil
	})
}

// GetCurrentUserInfo returns a copy of the currently loaded user info.
func GetCurrentUserInfo() *UserInfo {
	userInfoMutex.RLock()
	defer userInfoMutex.RUnlock()
	if currentUserInfo == nil {
		return nil
	}
	info := *currentUserInfo
	return &info
}

// SetCurrentUserInfo sets the in-memory user session.
func SetCurrentUserInfo(info *UserInfo) {
	userInfoMutex.Lock()
	defer userInfoMutex.Unlock()
	if info == nil {
		currentUserInfo = nil
		return
	}
	copyInfo := *info
	currentUserInfo = &copyInfo
}

// ResetAuthPersistenceForTest resets auth persistence singletons for isolated tests.
func ResetAuthPersistenceForTest() {
	userInfoMutex.Lock()
	currentUserInfo = nil
	userInfoMutex.Unlock()

	if authSessionDB != nil {
		if sqlDB, err := authSessionDB.DB(); err == nil {
			_ = sqlDB.Close()
		}
	}
	authSessionDB = nil
	authSessionDBErr = nil
	authSessionDBOnce = sync.Once{}
	cache.ResetCacheDirForTest()
}
