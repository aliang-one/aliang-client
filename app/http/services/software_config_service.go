package services

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"gopkg.in/yaml.v3"
	"gorm.io/gorm"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/app/http/storage"
	"aliang.one/nursorgate/common/cache"
	"aliang.one/nursorgate/processor/config"
)

type SoftwareConfigService struct {
	store      *storage.SoftwareConfigStore
	httpClient *http.Client
}

func NewSoftwareConfigService() *SoftwareConfigService {
	client := &http.Client{Timeout: 30 * time.Second}
	return &SoftwareConfigService{
		store:      storage.NewSoftwareConfigStore(),
		httpClient: client,
	}
}

func NewSoftwareConfigServiceWithStore(store *storage.SoftwareConfigStore, client *http.Client) *SoftwareConfigService {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &SoftwareConfigService{store: store, httpClient: client}
}

func (s *SoftwareConfigService) Save(req models.SaveSoftwareConfigRequest) (*models.SoftwareConfig, error) {
	cfg, err := s.normalizeSaveRequest(req)
	if err != nil {
		return nil, err
	}

	if err := s.store.Upsert(*cfg); err != nil {
		return nil, err
	}
	_ = s.store.SaveOperationLog(models.SoftwareConfigOperationLog{
		Action:     "save",
		Software:   cfg.Software,
		ConfigUUID: cfg.UUID,
		ConfigName: cfg.Name,
		Detail:     "saved software config",
	})
	return cfg, nil
}

func (s *SoftwareConfigService) ListBySoftware(software string) ([]models.SoftwareConfig, error) {
	software = strings.TrimSpace(software)
	if software == "" {
		return s.store.List()
	}
	return s.store.ListBySoftware(software)
}

func (s *SoftwareConfigService) Activate(req models.ActivateSoftwareConfigRequest) (*models.SoftwareConfig, error) {
	if strings.TrimSpace(req.FilePath) == "" {
		return nil, errors.New("file_path is required")
	}

	cfg, err := s.normalizeActivateRequest(req)
	if err != nil {
		return nil, err
	}

	coordinator := config.GetEffectiveConfigCommitCoordinator()
	_, err = coordinator.Commit(
		&config.EffectiveConfigSnapshot{
			UUID:     cfg.UUID,
			Software: cfg.Software,
			Name:     cfg.Name,
			FilePath: cfg.FilePath,
			Version:  cfg.Version,
			Format:   cfg.Format,
			Content:  cfg.Content,
		},
		func(snapshot *config.EffectiveConfigSnapshot) error {
			if snapshot == nil {
				return errors.New("file snapshot is required")
			}
			return writeConfigFile(snapshot.FilePath, snapshot.Content)
		},
		func(snapshot *config.EffectiveConfigSnapshot) error {
			if snapshot == nil {
				return errors.New("db snapshot is required")
			}
			if err := s.store.Activate(models.SoftwareConfig{
				UUID:      snapshot.UUID,
				Software:  snapshot.Software,
				Name:      snapshot.Name,
				FilePath:  snapshot.FilePath,
				Version:   snapshot.Version,
				InUse:     true,
				Format:    snapshot.Format,
				Content:   snapshot.Content,
				CreatedAt: cfg.CreatedAt,
				UpdatedAt: cfg.UpdatedAt,
			}); err != nil {
				return err
			}

			effectiveSnapshotJSON, err := buildEffectiveSnapshotJSON(snapshot)
			if err != nil {
				return err
			}

			return s.store.SaveEffectiveConfigSnapshot(models.SoftwareEffectiveConfigSnapshot{
				Software:       snapshot.Software,
				ConfigUUID:     snapshot.UUID,
				ConfigName:     snapshot.Name,
				ConfigFilePath: snapshot.FilePath,
				ConfigVersion:  snapshot.Version,
				ConfigFormat:   snapshot.Format,
				SnapshotJSON:   effectiveSnapshotJSON,
			})
		},
	)
	if err != nil {
		return nil, err
	}
	_ = s.store.SaveOperationLog(models.SoftwareConfigOperationLog{
		Action:     "activate",
		Software:   cfg.Software,
		ConfigUUID: cfg.UUID,
		ConfigName: cfg.Name,
		Detail:     "activated config and wrote to local path",
	})

	return cfg, nil
}

func (s *SoftwareConfigService) List() ([]models.SoftwareConfig, error) {
	return s.store.List()
}

func (s *SoftwareConfigService) PushToCloud(req models.CloudPushRequest) (*models.CloudPushResponse, error) {
	cloudURL := strings.TrimSpace(req.CloudURL)
	if cloudURL == "" {
		return nil, errors.New("cloud_url is required")
	}

	configs, err := s.selectPushCandidates(req)
	if err != nil {
		return nil, err
	}
	if len(configs) == 0 {
		return &models.CloudPushResponse{PushedCount: 0}, nil
	}

	payload, err := json.Marshal(models.CloudConfigBatch{Configs: configs})
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequest(http.MethodPost, cloudURL, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if strings.TrimSpace(req.AuthToken) != "" {
		httpReq.Header.Set("Authorization", "Bearer "+strings.TrimSpace(req.AuthToken))
	}

	resp, err := s.httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("cloud push failed: status=%d body=%s", resp.StatusCode, string(body))
	}

	_ = s.store.SaveOperationLog(models.SoftwareConfigOperationLog{
		Action:   "cloud_push",
		Software: "*",
		Detail:   fmt.Sprintf("pushed %d configs to cloud", len(configs)),
	})

	return &models.CloudPushResponse{PushedCount: len(configs)}, nil
}

func (s *SoftwareConfigService) PullFromCloud(req models.CloudPullRequest) (*models.CloudPullResponse, error) {
	cloudURL := strings.TrimSpace(req.CloudURL)
	if cloudURL == "" {
		return nil, errors.New("cloud_url is required")
	}

	httpReq, err := http.NewRequest(http.MethodGet, cloudURL, nil)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.AuthToken) != "" {
		httpReq.Header.Set("Authorization", "Bearer "+strings.TrimSpace(req.AuthToken))
	}

	resp, err := s.httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("cloud pull failed: status=%d body=%s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var batch models.CloudConfigBatch
	if err := json.Unmarshal(body, &batch); err != nil {
		return nil, err
	}

	now := time.Now()
	for i := range batch.Configs {
		if strings.TrimSpace(batch.Configs[i].UUID) == "" {
			batch.Configs[i].UUID = uuid.NewString()
		}
		if batch.Configs[i].CreatedAt.IsZero() {
			batch.Configs[i].CreatedAt = now
		}
		if batch.Configs[i].UpdatedAt.IsZero() {
			batch.Configs[i].UpdatedAt = now
		}
	}

	inserted, updated, kept, err := s.store.MergeByLatest(batch.Configs)
	if err != nil {
		return nil, err
	}
	_ = s.store.SaveOperationLog(models.SoftwareConfigOperationLog{
		Action:   "cloud_pull",
		Software: "*",
		Detail:   fmt.Sprintf("pulled=%d inserted=%d updated=%d kept_local=%d", len(batch.Configs), inserted, updated, kept),
	})

	return &models.CloudPullResponse{
		PulledCount:       len(batch.Configs),
		InsertedCount:     inserted,
		UpdatedFromCloud:  updated,
		KeptLocalNewerCnt: kept,
	}, nil
}

func (s *SoftwareConfigService) Delete(req models.DeleteSoftwareConfigRequest) error {
	id := strings.TrimSpace(req.UUID)
	if id == "" {
		return errors.New("uuid is required")
	}

	cfg, found, err := s.store.FindByUUID(id)
	if err != nil {
		return err
	}
	if !found {
		return gorm.ErrRecordNotFound
	}

	if err := s.store.DeleteByUUID(id); err != nil {
		return err
	}
	_ = s.store.SaveOperationLog(models.SoftwareConfigOperationLog{
		Action:     "delete",
		Software:   cfg.Software,
		ConfigUUID: cfg.UUID,
		ConfigName: cfg.Name,
		Detail:     "deleted software config",
	})
	return nil
}

func (s *SoftwareConfigService) SetSelected(req models.SelectSoftwareConfigRequest) error {
	id := strings.TrimSpace(req.UUID)
	if id == "" {
		return errors.New("uuid is required")
	}

	cfg, found, err := s.store.FindByUUID(id)
	if err != nil {
		return err
	}
	if !found {
		return gorm.ErrRecordNotFound
	}

	if err := s.store.SetSelected(id, req.Selected); err != nil {
		return err
	}
	_ = s.store.SaveOperationLog(models.SoftwareConfigOperationLog{
		Action:     "select",
		Software:   cfg.Software,
		ConfigUUID: cfg.UUID,
		ConfigName: cfg.Name,
		Detail:     fmt.Sprintf("selected=%v", req.Selected),
	})
	return nil
}

func (s *SoftwareConfigService) LogOperation(req models.LogSoftwareConfigOperationRequest) error {
	action := strings.TrimSpace(req.Action)
	if action == "" {
		return errors.New("action is required")
	}
	log := models.SoftwareConfigOperationLog{
		Action:     action,
		Software:   strings.TrimSpace(req.Software),
		ConfigUUID: strings.TrimSpace(req.ConfigUUID),
		ConfigName: strings.TrimSpace(req.ConfigName),
		Detail:     strings.TrimSpace(req.Detail),
	}
	return s.store.SaveOperationLog(log)
}

func (s *SoftwareConfigService) GetLatestEffectiveConfigSnapshot() (*models.SoftwareEffectiveConfigSnapshot, error) {
	return s.store.GetLatestEffectiveConfigSnapshot()
}

func (s *SoftwareConfigService) CompareWithCloud(req models.CompareSoftwareConfigRequest) (*models.CompareSoftwareConfigResponse, error) {
	cloudURL := strings.TrimSpace(req.CloudURL)
	if cloudURL == "" {
		return nil, errors.New("cloud_url is required")
	}

	localConfigs, err := s.store.List()
	if err != nil {
		return nil, err
	}
	remoteBatch, err := s.fetchCloudBatch(req.CloudURL, req.AuthToken)
	if err != nil {
		return nil, err
	}

	localMap := make(map[string]models.SoftwareConfig, len(localConfigs))
	for i := range localConfigs {
		localMap[localConfigs[i].UUID] = localConfigs[i]
	}

	items := make([]models.ConfigFreshnessItem, 0)
	seen := make(map[string]bool)
	for i := range remoteBatch.Configs {
		r := remoteBatch.Configs[i]
		local, ok := localMap[r.UUID]
		item := models.ConfigFreshnessItem{
			UUID:           r.UUID,
			Software:       r.Software,
			Name:           r.Name,
			CloudUpdatedAt: r.UpdatedAt.UTC().Format(time.RFC3339),
		}
		if ok {
			item.Software = local.Software
			item.Name = local.Name
			item.LocalUpdatedAt = local.UpdatedAt.UTC().Format(time.RFC3339)
			switch {
			case local.UpdatedAt.After(r.UpdatedAt):
				item.Status = "local_newer"
			case r.UpdatedAt.After(local.UpdatedAt):
				item.Status = "cloud_newer"
			default:
				item.Status = "same"
			}
		} else {
			item.Status = "cloud_only"
		}
		seen[r.UUID] = true
		items = append(items, item)
	}

	for i := range localConfigs {
		l := localConfigs[i]
		if seen[l.UUID] {
			continue
		}
		items = append(items, models.ConfigFreshnessItem{
			UUID:           l.UUID,
			Software:       l.Software,
			Name:           l.Name,
			LocalUpdatedAt: l.UpdatedAt.UTC().Format(time.RFC3339),
			Status:         "local_only",
		})
	}

	_ = s.store.SaveOperationLog(models.SoftwareConfigOperationLog{
		Action:   "compare",
		Software: "*",
		Detail:   fmt.Sprintf("compared local/cloud entries=%d", len(items)),
	})

	return &models.CompareSoftwareConfigResponse{Items: items}, nil
}

func (s *SoftwareConfigService) fetchCloudBatch(cloudURL string, authToken string) (*models.CloudConfigBatch, error) {
	httpReq, err := http.NewRequest(http.MethodGet, strings.TrimSpace(cloudURL), nil)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(authToken) != "" {
		httpReq.Header.Set("Authorization", "Bearer "+strings.TrimSpace(authToken))
	}

	resp, err := s.httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("cloud pull failed: status=%d body=%s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var batch models.CloudConfigBatch
	if err := json.Unmarshal(body, &batch); err != nil {
		return nil, err
	}

	now := time.Now()
	for i := range batch.Configs {
		if strings.TrimSpace(batch.Configs[i].UUID) == "" {
			batch.Configs[i].UUID = uuid.NewString()
		}
		if batch.Configs[i].CreatedAt.IsZero() {
			batch.Configs[i].CreatedAt = now
		}
		if batch.Configs[i].UpdatedAt.IsZero() {
			batch.Configs[i].UpdatedAt = now
		}
	}

	return &batch, nil
}

func (s *SoftwareConfigService) selectPushCandidates(req models.CloudPushRequest) ([]models.SoftwareConfig, error) {
	trimmedIDs := make([]string, 0, len(req.UUIDs))
	for i := range req.UUIDs {
		id := strings.TrimSpace(req.UUIDs[i])
		if id != "" {
			trimmedIDs = append(trimmedIDs, id)
		}
	}

	if len(trimmedIDs) > 0 {
		return s.store.ListByUUIDs(trimmedIDs)
	}
	if req.OnlySelected {
		return s.store.ListSelectedBySoftware("")
	}
	return s.store.List()
}

func (s *SoftwareConfigService) normalizeSaveRequest(req models.SaveSoftwareConfigRequest) (*models.SoftwareConfig, error) {
	if strings.TrimSpace(req.Name) == "" {
		return nil, errors.New("name is required")
	}
	if strings.TrimSpace(req.Software) == "" {
		return nil, errors.New("software is required")
	}
	if strings.TrimSpace(req.FilePath) == "" {
		return nil, errors.New("file_path is required")
	}

	format, err := validateFormat(req.Format)
	if err != nil {
		return nil, err
	}
	if err := validateContentByFormat(format, req.Content); err != nil {
		return nil, err
	}

	now := time.Now()
	createdAt, err := parseTimeOrError(req.CreatedAt)
	if err != nil {
		return nil, err
	}
	updatedAt, err := parseTimeOrError(req.UpdatedAt)
	if err != nil {
		return nil, err
	}

	id := strings.TrimSpace(req.UUID)
	if id == "" {
		id = uuid.NewString()
	}

	path, err := cache.ExpandHomePath(strings.TrimSpace(req.FilePath))
	if err != nil {
		return nil, err
	}

	if createdAt.IsZero() {
		createdAt = now
	}
	if updatedAt.IsZero() {
		updatedAt = now
	}

	if strings.TrimSpace(req.UUID) != "" {
		existing, found, findErr := s.store.FindByUUID(id)
		if findErr != nil {
			return nil, findErr
		}
		if found && strings.TrimSpace(req.CreatedAt) == "" {
			createdAt = existing.CreatedAt
		}
	}

	return &models.SoftwareConfig{
		UUID:      id,
		Software:  strings.TrimSpace(req.Software),
		Name:      strings.TrimSpace(req.Name),
		FilePath:  path,
		Version:   strings.TrimSpace(req.Version),
		InUse:     req.InUse,
		Selected:  req.Selected,
		Format:    format,
		Content:   req.Content,
		CreatedAt: createdAt,
		UpdatedAt: updatedAt,
	}, nil
}

func (s *SoftwareConfigService) normalizeActivateRequest(req models.ActivateSoftwareConfigRequest) (*models.SoftwareConfig, error) {
	if strings.TrimSpace(req.Name) == "" {
		return nil, errors.New("name is required")
	}
	if strings.TrimSpace(req.Software) == "" {
		return nil, errors.New("software is required")
	}
	if strings.TrimSpace(req.FilePath) == "" {
		return nil, errors.New("file_path is required")
	}

	format, err := validateFormat(req.Format)
	if err != nil {
		return nil, err
	}
	if err := validateContentByFormat(format, req.Content); err != nil {
		return nil, err
	}

	now := time.Now()
	createdAt, err := parseTimeOrError(req.CreatedAt)
	if err != nil {
		return nil, err
	}
	updatedAt, err := parseTimeOrError(req.UpdatedAt)
	if err != nil {
		return nil, err
	}

	id := strings.TrimSpace(req.UUID)
	if id == "" {
		id = uuid.NewString()
	}

	path, err := cache.ExpandHomePath(strings.TrimSpace(req.FilePath))
	if err != nil {
		return nil, err
	}

	if createdAt.IsZero() {
		createdAt = now
	}
	if updatedAt.IsZero() {
		updatedAt = now
	}

	if strings.TrimSpace(req.UUID) != "" {
		existing, found, findErr := s.store.FindByUUID(id)
		if findErr != nil {
			return nil, findErr
		}
		if found && strings.TrimSpace(req.CreatedAt) == "" {
			createdAt = existing.CreatedAt
		}
	}

	return &models.SoftwareConfig{
		UUID:      id,
		Software:  strings.TrimSpace(req.Software),
		Name:      strings.TrimSpace(req.Name),
		FilePath:  path,
		Version:   strings.TrimSpace(req.Version),
		InUse:     true,
		Format:    format,
		Content:   req.Content,
		CreatedAt: createdAt,
		UpdatedAt: updatedAt,
	}, nil
}

func validateFormat(raw string) (string, error) {
	format := strings.ToLower(strings.TrimSpace(raw))
	if format == "yml" {
		format = models.ConfigFormatYAML
	}
	if format != models.ConfigFormatJSON && format != models.ConfigFormatYAML {
		return "", errors.New("format must be json or yaml")
	}
	return format, nil
}

func validateContentByFormat(format string, content string) error {
	if strings.TrimSpace(content) == "" {
		return errors.New("content is required")
	}

	switch format {
	case models.ConfigFormatJSON:
		if !json.Valid([]byte(content)) {
			return errors.New("content is not valid json")
		}
	case models.ConfigFormatYAML:
		var out interface{}
		if err := yaml.Unmarshal([]byte(content), &out); err != nil {
			return errors.New("content is not valid yaml")
		}
	}
	return nil
}

func parseTimeOrError(raw string) (time.Time, error) {
	if strings.TrimSpace(raw) == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(raw))
	if err != nil {
		return time.Time{}, errors.New("time fields must be RFC3339")
	}
	return t, nil
}

func writeConfigFile(filePath string, content string) error {
	if filePath == "" {
		return errors.New("file_path is required")
	}
	if info, err := os.Lstat(filePath); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			resolved, resolveErr := filepath.EvalSymlinks(filePath)
			if resolveErr != nil {
				return resolveErr
			}
			filePath = resolved
		} else if !info.Mode().IsRegular() {
			return errors.New("file_path must reference a regular file")
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	dir := filepath.Dir(filePath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(filePath)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	closed := false
	defer func() {
		if !closed {
			_ = tmp.Close()
		}
		_ = os.Remove(tmpPath)
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return err
	}
	if _, err := io.WriteString(tmp, content); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	closed = true
	if err := os.Rename(tmpPath, filePath); err != nil {
		return err
	}
	return os.Chmod(filePath, 0o600)
}

func buildEffectiveSnapshotJSON(snapshot *config.EffectiveConfigSnapshot) (string, error) {
	if snapshot == nil {
		return "", errors.New("effective config snapshot is required")
	}

	merged := make(map[string]interface{})

	globalCfg := config.GetGlobalConfig()
	if globalCfg != nil {
		globalRaw, err := json.Marshal(globalCfg)
		if err != nil {
			return "", fmt.Errorf("marshal global config: %w", err)
		}
		if err := json.Unmarshal(globalRaw, &merged); err != nil {
			return "", fmt.Errorf("unmarshal global config: %w", err)
		}
	}

	contentMap, err := parseConfigContentToMap(snapshot.Format, snapshot.Content)
	if err != nil {
		return "", err
	}
	for key, value := range contentMap {
		merged[key] = value
	}

	merged["effective_snapshot_meta"] = map[string]interface{}{
		"config_uuid":      snapshot.UUID,
		"software":         snapshot.Software,
		"config_name":      snapshot.Name,
		"config_file_path": snapshot.FilePath,
		"config_version":   snapshot.Version,
		"config_format":    snapshot.Format,
		"committed_at":     time.Now().UTC().Format(time.RFC3339),
	}

	raw, err := json.Marshal(merged)
	if err != nil {
		return "", fmt.Errorf("marshal effective snapshot json: %w", err)
	}
	return string(raw), nil
}

func parseConfigContentToMap(format string, content string) (map[string]interface{}, error) {
	result := make(map[string]interface{})

	trimmedFormat := strings.ToLower(strings.TrimSpace(format))
	if trimmedFormat == "yml" {
		trimmedFormat = models.ConfigFormatYAML
	}

	switch trimmedFormat {
	case models.ConfigFormatJSON:
		if err := json.Unmarshal([]byte(content), &result); err != nil {
			return nil, fmt.Errorf("invalid json content for effective snapshot: %w", err)
		}
	case models.ConfigFormatYAML:
		if err := yaml.Unmarshal([]byte(content), &result); err != nil {
			return nil, fmt.Errorf("invalid yaml content for effective snapshot: %w", err)
		}
	default:
		return nil, fmt.Errorf("unsupported config format for effective snapshot: %s", format)
	}

	return result, nil
}
