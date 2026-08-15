package storage

import (
	"errors"
	"fmt"
	"sync"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/common/cache"
)

var (
	softwareConfigDBOnce sync.Once
	softwareConfigDB     *gorm.DB
	softwareConfigDBErr  error
)

type SoftwareConfigStore struct {
	db      *gorm.DB
	initErr error
}

func NewSoftwareConfigStore() *SoftwareConfigStore {
	db, err := getSoftwareConfigDB()
	return &SoftwareConfigStore{db: db, initErr: err}
}

func NewSoftwareConfigStoreWithDBPath(dbPath string) (*SoftwareConfigStore, error) {
	db, err := openSoftwareConfigDB(dbPath)
	if err != nil {
		return nil, err
	}
	return &SoftwareConfigStore{db: db}, nil
}

func getSoftwareConfigDB() (*gorm.DB, error) {
	softwareConfigDBOnce.Do(func() {
		dbPath, err := cache.GetUnifiedDataDBPath()
		if err != nil {
			softwareConfigDBErr = err
			return
		}
		softwareConfigDB, softwareConfigDBErr = openSoftwareConfigDB(dbPath)
	})
	return softwareConfigDB, softwareConfigDBErr
}

// InitializeSoftwareConfigStore ensures the shared software-config tables are ready.
func InitializeSoftwareConfigStore() error {
	_, err := getSoftwareConfigDB()
	return err
}

// ResetSoftwareConfigDBForTest clears the package singleton so tests can isolate db path resolution.
func ResetSoftwareConfigDBForTest() {
	softwareConfigDB = nil
	softwareConfigDBErr = nil
	softwareConfigDBOnce = sync.Once{}
	cache.ResetCacheDirForTest()
}

func openSoftwareConfigDB(dbPath string) (*gorm.DB, error) {
	if dbPath == "" {
		return nil, errors.New("software config db path is empty")
	}

	absPath, err := cache.ExpandHomePath(dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve db path: %w", err)
	}

	db, err := gorm.Open(sqlite.Open(absPath), &gorm.Config{})
	if err != nil {
		return nil, fmt.Errorf("failed to open sqlite database: %w", err)
	}

	if err := db.AutoMigrate(
		&models.SoftwareConfig{},
		&models.SoftwareConfigOperationLog{},
		&models.SoftwareEffectiveConfigSnapshot{},
		&models.SoftwareVersionUpdateSnapshot{},
		&models.SoftwareVersionUpdateDismissal{},
		&models.UIPromptState{},
	); err != nil {
		return nil, fmt.Errorf("failed to migrate software_configs table: %w", err)
	}

	if err := db.Exec("UPDATE software_configs SET software = 'opencode' WHERE software IS NULL OR software = ''").Error; err != nil {
		return nil, fmt.Errorf("failed to backfill software column: %w", err)
	}

	return db, nil
}

func (s *SoftwareConfigStore) ensureReady() error {
	if s == nil {
		return errors.New("software config store is nil")
	}
	if s.initErr != nil {
		return s.initErr
	}
	if s.db == nil {
		return errors.New("software config store db is nil")
	}
	return nil
}

func (s *SoftwareConfigStore) Upsert(cfg models.SoftwareConfig) error {
	if err := s.ensureReady(); err != nil {
		return err
	}
	return s.db.Save(&cfg).Error
}

func (s *SoftwareConfigStore) Activate(cfg models.SoftwareConfig) error {
	if err := s.ensureReady(); err != nil {
		return err
	}
	if cfg.Software == "" {
		return errors.New("software is required")
	}

	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.SoftwareConfig{}).Where("software = ? AND in_use = ?", cfg.Software, true).Update("in_use", false).Error; err != nil {
			return err
		}
		cfg.InUse = true
		return tx.Save(&cfg).Error
	})
}

func (s *SoftwareConfigStore) List() ([]models.SoftwareConfig, error) {
	if err := s.ensureReady(); err != nil {
		return nil, err
	}

	var configs []models.SoftwareConfig
	if err := s.db.Order("updated_at DESC").Find(&configs).Error; err != nil {
		return nil, err
	}
	return configs, nil
}

func (s *SoftwareConfigStore) ListBySoftware(software string) ([]models.SoftwareConfig, error) {
	if err := s.ensureReady(); err != nil {
		return nil, err
	}

	var configs []models.SoftwareConfig
	if err := s.db.Where("software = ?", software).Order("updated_at DESC").Find(&configs).Error; err != nil {
		return nil, err
	}
	return configs, nil
}

func (s *SoftwareConfigStore) ListSelectedBySoftware(software string) ([]models.SoftwareConfig, error) {
	if err := s.ensureReady(); err != nil {
		return nil, err
	}

	var configs []models.SoftwareConfig
	query := s.db.Where("selected = ?", true)
	if software != "" {
		query = query.Where("software = ?", software)
	}
	if err := query.Order("updated_at DESC").Find(&configs).Error; err != nil {
		return nil, err
	}
	return configs, nil
}

func (s *SoftwareConfigStore) ListByUUIDs(ids []string) ([]models.SoftwareConfig, error) {
	if err := s.ensureReady(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return []models.SoftwareConfig{}, nil
	}

	var configs []models.SoftwareConfig
	if err := s.db.Where("uuid IN ?", ids).Order("updated_at DESC").Find(&configs).Error; err != nil {
		return nil, err
	}
	return configs, nil
}

func (s *SoftwareConfigStore) GetByUUID(id string) (*models.SoftwareConfig, error) {
	if err := s.ensureReady(); err != nil {
		return nil, err
	}

	var cfg models.SoftwareConfig
	if err := s.db.First(&cfg, "uuid = ?", id).Error; err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (s *SoftwareConfigStore) FindByUUID(id string) (*models.SoftwareConfig, bool, error) {
	if err := s.ensureReady(); err != nil {
		return nil, false, err
	}

	var cfg models.SoftwareConfig
	err := s.db.First(&cfg, "uuid = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return &cfg, true, nil
}

func (s *SoftwareConfigStore) DeleteByUUID(id string) error {
	if err := s.ensureReady(); err != nil {
		return err
	}
	return s.db.Delete(&models.SoftwareConfig{}, "uuid = ?", id).Error
}

func (s *SoftwareConfigStore) SetSelected(id string, selected bool) error {
	if err := s.ensureReady(); err != nil {
		return err
	}
	return s.db.Model(&models.SoftwareConfig{}).Where("uuid = ?", id).Update("selected", selected).Error
}

func (s *SoftwareConfigStore) SaveOperationLog(log models.SoftwareConfigOperationLog) error {
	if err := s.ensureReady(); err != nil {
		return err
	}
	return s.db.Create(&log).Error
}

func (s *SoftwareConfigStore) SaveEffectiveConfigSnapshot(snapshot models.SoftwareEffectiveConfigSnapshot) error {
	if err := s.ensureReady(); err != nil {
		return err
	}
	return s.db.Create(&snapshot).Error
}

func (s *SoftwareConfigStore) GetLatestEffectiveConfigSnapshot() (*models.SoftwareEffectiveConfigSnapshot, error) {
	if err := s.ensureReady(); err != nil {
		return nil, err
	}

	var snapshot models.SoftwareEffectiveConfigSnapshot
	result := s.db.Order("created_at DESC, id DESC").Limit(1).Find(&snapshot)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 0 {
		return nil, nil
	}
	return &snapshot, nil
}

func (s *SoftwareConfigStore) GetLatestEffectiveConfigSnapshotBySoftwareAndName(software string, configName string) (*models.SoftwareEffectiveConfigSnapshot, error) {
	if err := s.ensureReady(); err != nil {
		return nil, err
	}

	var snapshot models.SoftwareEffectiveConfigSnapshot
	result := s.db.
		Where("software = ? AND config_name = ?", software, configName).
		Order("created_at DESC, id DESC").
		Limit(1).
		Find(&snapshot)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 0 {
		return nil, nil
	}
	return &snapshot, nil
}

func (s *SoftwareConfigStore) MergeByLatest(incoming []models.SoftwareConfig) (inserted int, updated int, keptLocalNewer int, err error) {
	if err = s.ensureReady(); err != nil {
		return 0, 0, 0, err
	}

	err = s.db.Transaction(func(tx *gorm.DB) error {
		for _, remoteCfg := range incoming {
			var localCfg models.SoftwareConfig
			err := tx.First(&localCfg, "uuid = ?", remoteCfg.UUID).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				if remoteCfg.InUse {
					if clearErr := tx.Model(&models.SoftwareConfig{}).Where("software = ? AND in_use = ?", remoteCfg.Software, true).Update("in_use", false).Error; clearErr != nil {
						return clearErr
					}
				}
				if createErr := tx.Create(&remoteCfg).Error; createErr != nil {
					return createErr
				}
				inserted++
				continue
			}
			if err != nil {
				return err
			}

			if remoteCfg.UpdatedAt.After(localCfg.UpdatedAt) {
				if remoteCfg.InUse {
					if clearErr := tx.Model(&models.SoftwareConfig{}).Where("software = ? AND in_use = ?", remoteCfg.Software, true).Update("in_use", false).Error; clearErr != nil {
						return clearErr
					}
				}
				if saveErr := tx.Save(&remoteCfg).Error; saveErr != nil {
					return saveErr
				}
				updated++
			} else {
				keptLocalNewer++
			}
		}
		return nil
	})

	return inserted, updated, keptLocalNewer, err
}
