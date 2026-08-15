package storage

import (
	"errors"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"aliang.one/nursorgate/app/http/models"
)

type SoftwareVersionUpdateStore struct {
	db      *gorm.DB
	initErr error
}

func NewSoftwareVersionUpdateStore() *SoftwareVersionUpdateStore {
	db, err := getSoftwareConfigDB()
	return &SoftwareVersionUpdateStore{db: db, initErr: err}
}

func NewSoftwareVersionUpdateStoreWithDBPath(dbPath string) (*SoftwareVersionUpdateStore, error) {
	db, err := openSoftwareConfigDB(dbPath)
	if err != nil {
		return nil, err
	}
	return &SoftwareVersionUpdateStore{db: db}, nil
}

func (s *SoftwareVersionUpdateStore) ensureReady() error {
	if s == nil {
		return errors.New("software version update store is nil")
	}
	if s.initErr != nil {
		return s.initErr
	}
	if s.db == nil {
		return errors.New("software version update store db is nil")
	}
	return nil
}

func (s *SoftwareVersionUpdateStore) UpsertSnapshot(snapshot models.SoftwareVersionUpdateSnapshot) error {
	if err := s.ensureReady(); err != nil {
		return err
	}

	return s.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{
			{Name: "software"},
			{Name: "platform"},
		},
		DoUpdates: clause.AssignmentColumns([]string{
			"current_version",
			"latest_version",
			"download_url",
			"file_type",
			"changelog",
			"needs_update",
			"force_update",
			"status",
			"last_error",
			"checked_at",
			"first_seen_at",
			"last_seen_at",
			"updated_at",
		}),
	}).Create(&snapshot).Error
}

func (s *SoftwareVersionUpdateStore) GetSnapshot(software string, platform string) (*models.SoftwareVersionUpdateSnapshot, error) {
	if err := s.ensureReady(); err != nil {
		return nil, err
	}

	var snapshot models.SoftwareVersionUpdateSnapshot
	err := s.db.First(&snapshot, "software = ? AND platform = ?", software, platform).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &snapshot, nil
}

func (s *SoftwareVersionUpdateStore) UpsertDismissal(dismissal models.SoftwareVersionUpdateDismissal) error {
	if err := s.ensureReady(); err != nil {
		return err
	}

	return s.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{
			{Name: "software"},
			{Name: "platform"},
			{Name: "latest_version"},
		},
		DoUpdates: clause.AssignmentColumns([]string{
			"dismissed_at",
			"updated_at",
		}),
	}).Create(&dismissal).Error
}

func (s *SoftwareVersionUpdateStore) GetDismissal(software string, platform string, latestVersion string) (*models.SoftwareVersionUpdateDismissal, error) {
	if err := s.ensureReady(); err != nil {
		return nil, err
	}

	var dismissal models.SoftwareVersionUpdateDismissal
	err := s.db.First(&dismissal, "software = ? AND platform = ? AND latest_version = ?", software, platform, latestVersion).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &dismissal, nil
}
