package storage

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/common/cache"
)

// quickSetupComboMaxPerSoftware 限制单个 software 下的组合数量（防御性上限，spec §4）。
const quickSetupComboMaxPerSoftware = 50

var (
	quickSetupComboDBOnce sync.Once
	quickSetupComboDB     *gorm.DB
	quickSetupComboDBErr  error
)

type QuickSetupComboStore struct {
	db      *gorm.DB
	initErr error
}

func NewQuickSetupComboStore() *QuickSetupComboStore {
	db, err := getQuickSetupComboDB()
	return &QuickSetupComboStore{db: db, initErr: err}
}

func NewQuickSetupComboStoreWithDBPath(dbPath string) (*QuickSetupComboStore, error) {
	db, err := openQuickSetupComboDB(dbPath)
	if err != nil {
		return nil, err
	}
	return &QuickSetupComboStore{db: db}, nil
}

func getQuickSetupComboDB() (*gorm.DB, error) {
	quickSetupComboDBOnce.Do(func() {
		dbPath, err := cache.GetUnifiedDataDBPath()
		if err != nil {
			quickSetupComboDBErr = err
			return
		}
		quickSetupComboDB, quickSetupComboDBErr = openQuickSetupComboDB(dbPath)
	})
	return quickSetupComboDB, quickSetupComboDBErr
}

// ResetQuickSetupComboStoreForTest clears the package singleton so tests can isolate db path resolution.
func ResetQuickSetupComboStoreForTest() {
	quickSetupComboDB = nil
	quickSetupComboDBErr = nil
	quickSetupComboDBOnce = sync.Once{}
	cache.ResetCacheDirForTest()
}

func openQuickSetupComboDB(dbPath string) (*gorm.DB, error) {
	if dbPath == "" {
		return nil, errors.New("quick setup combo db path is empty")
	}

	absPath, err := cache.ExpandHomePath(dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve db path: %w", err)
	}

	db, err := gorm.Open(sqlite.Open(absPath), &gorm.Config{})
	if err != nil {
		return nil, fmt.Errorf("failed to open sqlite database: %w", err)
	}

	if err := db.AutoMigrate(&models.QuickSetupCombo{}); err != nil {
		return nil, fmt.Errorf("failed to migrate quick_setup_combos table: %w", err)
	}

	return db, nil
}

func (s *QuickSetupComboStore) ensureReady() error {
	if s == nil {
		return errors.New("quick setup combo store is nil")
	}
	if s.initErr != nil {
		return s.initErr
	}
	if s.db == nil {
		return errors.New("quick setup combo store db is nil")
	}
	return nil
}

// marshalComboPayload serializes the runtime fields into the JSON text columns.
// nil 语义：Variables → {}，Files → []。
func marshalComboPayload(variables map[string]string, files []models.QuickSetupComboFile) (string, string, error) {
	if variables == nil {
		variables = map[string]string{}
	}
	variablesJSON, err := json.Marshal(variables)
	if err != nil {
		return "", "", fmt.Errorf("failed to marshal combo variables: %w", err)
	}
	if files == nil {
		files = []models.QuickSetupComboFile{}
	}
	filesJSON, err := json.Marshal(files)
	if err != nil {
		return "", "", fmt.Errorf("failed to marshal combo files: %w", err)
	}
	return string(variablesJSON), string(filesJSON), nil
}

// hydrateCombo deserializes the JSON text columns into the runtime fields.
// 空列/nil 语义：Variables 空 map / Files 空 slice。
func hydrateCombo(row *models.QuickSetupCombo) error {
	variables := map[string]string{}
	if row.VariablesJSON != "" {
		if err := json.Unmarshal([]byte(row.VariablesJSON), &variables); err != nil {
			return fmt.Errorf("failed to unmarshal combo variables: %w", err)
		}
	}
	files := []models.QuickSetupComboFile{}
	if row.FilesJSON != "" {
		if err := json.Unmarshal([]byte(row.FilesJSON), &files); err != nil {
			return fmt.Errorf("failed to unmarshal combo files: %w", err)
		}
	}
	row.Variables = variables
	row.Files = files
	return nil
}

func (s *QuickSetupComboStore) Create(c *models.QuickSetupCombo) error {
	if err := s.ensureReady(); err != nil {
		return err
	}
	if c == nil {
		return errors.New("combo is nil")
	}
	if c.Software == "" {
		return errors.New("software is required")
	}
	if c.Name == "" {
		return errors.New("name is required")
	}

	count, err := s.CountBySoftware(c.Software)
	if err != nil {
		return err
	}
	if count >= quickSetupComboMaxPerSoftware {
		return fmt.Errorf("combo cap exceeded for software %s", c.Software)
	}

	variablesJSON, filesJSON, err := marshalComboPayload(c.Variables, c.Files)
	if err != nil {
		return err
	}
	c.VariablesJSON = variablesJSON
	c.FilesJSON = filesJSON
	return s.db.Create(c).Error
}

func (s *QuickSetupComboStore) Update(c *models.QuickSetupCombo) error {
	if err := s.ensureReady(); err != nil {
		return err
	}
	if c == nil {
		return errors.New("combo is nil")
	}
	if c.ID == 0 {
		return errors.New("combo id is required")
	}

	variablesJSON, filesJSON, err := marshalComboPayload(c.Variables, c.Files)
	if err != nil {
		return err
	}
	c.VariablesJSON = variablesJSON
	c.FilesJSON = filesJSON
	// 用 map 更新，防 gorm 跳过零值字段（如 is_default=false）。
	return s.db.Model(&models.QuickSetupCombo{ID: c.ID}).Updates(map[string]interface{}{
		"software":       c.Software,
		"name":           c.Name,
		"is_default":     c.IsDefault,
		"variables_json": variablesJSON,
		"files_json":     filesJSON,
	}).Error
}

func (s *QuickSetupComboStore) Delete(id int64) error {
	if err := s.ensureReady(); err != nil {
		return err
	}
	return s.db.Delete(&models.QuickSetupCombo{}, "id = ?", id).Error
}

func (s *QuickSetupComboStore) GetByID(id int64) (*models.QuickSetupCombo, error) {
	if err := s.ensureReady(); err != nil {
		return nil, err
	}

	var row models.QuickSetupCombo
	if err := s.db.First(&row, "id = ?", id).Error; err != nil {
		// gorm.ErrRecordNotFound 原样上抛，调用方分类处理。
		return nil, err
	}
	if err := hydrateCombo(&row); err != nil {
		return nil, err
	}
	return &row, nil
}

func (s *QuickSetupComboStore) ListBySoftware(software string) ([]models.QuickSetupCombo, error) {
	if err := s.ensureReady(); err != nil {
		return nil, err
	}

	var rows []models.QuickSetupCombo
	if err := s.db.Where("software = ?", software).Order("is_default DESC, id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	for i := range rows {
		if err := hydrateCombo(&rows[i]); err != nil {
			return nil, err
		}
	}
	return rows, nil
}

func (s *QuickSetupComboStore) CountBySoftware(software string) (int64, error) {
	if err := s.ensureReady(); err != nil {
		return 0, err
	}

	var count int64
	if err := s.db.Model(&models.QuickSetupCombo{}).Where("software = ?", software).Count(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}

// SetDefault 保证同一 software 下默认组合互斥：先清全部 default，再设指定 id；
// 指定 id 不属于该 software 时 UPDATE 影响 0 行 → 报错。
func (s *QuickSetupComboStore) SetDefault(software string, id int64) error {
	if err := s.ensureReady(); err != nil {
		return err
	}
	if software == "" {
		return errors.New("software is required")
	}

	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.QuickSetupCombo{}).
			Where("software = ? AND is_default = ?", software, true).
			Update("is_default", false).Error; err != nil {
			return err
		}
		result := tx.Model(&models.QuickSetupCombo{}).
			Where("id = ? AND software = ?", id, software).
			Update("is_default", true)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return fmt.Errorf("combo %d not found in software %s", id, software)
		}
		return nil
	})
}

// ComboToView 将存储行转换为 API 视图（Variables/Files 已反序列化；
// nil 语义：Variables 空 map / Files 空 slice）。
// 行内运行时字段为空时回退到反序列化 JSON 文本列。
func ComboToView(row *models.QuickSetupCombo) (*models.QuickSetupComboView, error) {
	if row == nil {
		return nil, errors.New("combo row is nil")
	}
	variables := make(map[string]string, len(row.Variables))
	for k, v := range row.Variables {
		variables[k] = v
	}
	if len(variables) == 0 && row.VariablesJSON != "" {
		if err := json.Unmarshal([]byte(row.VariablesJSON), &variables); err != nil {
			return nil, fmt.Errorf("failed to unmarshal combo variables: %w", err)
		}
	}
	files := make([]models.QuickSetupComboFile, len(row.Files))
	copy(files, row.Files)
	if len(files) == 0 && row.FilesJSON != "" {
		if err := json.Unmarshal([]byte(row.FilesJSON), &files); err != nil {
			return nil, fmt.Errorf("failed to unmarshal combo files: %w", err)
		}
	}
	return &models.QuickSetupComboView{
		ID:        row.ID,
		Software:  row.Software,
		Name:      row.Name,
		IsDefault: row.IsDefault,
		Variables: variables,
		Files:     files,
	}, nil
}
