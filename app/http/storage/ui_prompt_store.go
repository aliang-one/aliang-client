package storage

import (
	"errors"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"aliang.one/nursorgate/app/http/models"
)

type UIPromptStateStore struct {
	db      *gorm.DB
	initErr error
}

func NewUIPromptStateStore() *UIPromptStateStore {
	db, err := getSoftwareConfigDB()
	return &UIPromptStateStore{db: db, initErr: err}
}

func NewUIPromptStateStoreWithDBPath(dbPath string) (*UIPromptStateStore, error) {
	db, err := openSoftwareConfigDB(dbPath)
	if err != nil {
		return nil, err
	}
	return &UIPromptStateStore{db: db}, nil
}

func (s *UIPromptStateStore) ensureReady() error {
	if s == nil {
		return errors.New("ui prompt state store is nil")
	}
	if s.initErr != nil {
		return s.initErr
	}
	if s.db == nil {
		return errors.New("ui prompt state store db is nil")
	}
	return nil
}

func (s *UIPromptStateStore) Upsert(state models.UIPromptState) error {
	if err := s.ensureReady(); err != nil {
		return err
	}

	return s.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "prompt_key"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"seen_at",
			"updated_at",
		}),
	}).Create(&state).Error
}

func (s *UIPromptStateStore) GetByKey(promptKey string) (*models.UIPromptState, error) {
	if err := s.ensureReady(); err != nil {
		return nil, err
	}

	var state models.UIPromptState
	err := s.db.First(&state, "prompt_key = ?", promptKey).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &state, nil
}
