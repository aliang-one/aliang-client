package services

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/app/http/storage"
	"aliang.one/nursorgate/outbound"
	"aliang.one/nursorgate/processor/config"
)

const coreConfigFilePath = "./config.json"

type CoreConfigService struct {
	store *storage.SoftwareConfigStore
}

type CoreConfigCommitResult struct {
	Version uint64
	Core    map[string]interface{}
}

type CoreConfigValidationError struct {
	err error
}

func (e *CoreConfigValidationError) Error() string {
	if e == nil || e.err == nil {
		return "invalid core config"
	}
	return e.err.Error()
}

func (e *CoreConfigValidationError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

func NewCoreConfigService() *CoreConfigService {
	return &CoreConfigService{store: storage.NewSoftwareConfigStore()}
}

func (s *CoreConfigService) GetCommittedCoreConfig() (map[string]interface{}, uint64, error) {
	coordinator := config.GetEffectiveConfigCommitCoordinator()
	if committed := coordinator.LastCommittedSnapshot(); committed != nil && strings.TrimSpace(committed.Content) != "" {
		core, err := extractCoreFromConfigContent(committed.Content)
		if err != nil {
			return nil, 0, err
		}
		return core, coordinator.Version(), nil
	}

	globalCfg := config.GetGlobalConfig()
	if globalCfg == nil {
		return map[string]interface{}{}, coordinator.Version(), nil
	}

	raw, err := json.Marshal(globalCfg)
	if err != nil {
		return nil, 0, fmt.Errorf("marshal global config: %w", err)
	}

	core, err := extractCoreFromConfigContent(string(raw))
	if err != nil {
		return nil, 0, err
	}
	return core, coordinator.Version(), nil
}

func (s *CoreConfigService) UpdateCommittedCoreConfig(payload []byte) (*CoreConfigCommitResult, error) {
	if len(payload) == 0 {
		return nil, &CoreConfigValidationError{err: errors.New("request body is empty")}
	}

	globalCfg := config.GetGlobalConfig()
	if globalCfg == nil {
		return nil, errors.New("global config is not initialized")
	}

	mergedContent, nextCfg, err := mergeCorePayload(globalCfg, payload)
	if err != nil {
		return nil, &CoreConfigValidationError{err: err}
	}

	if err := nextCfg.Validate(); err != nil {
		return nil, &CoreConfigValidationError{err: err}
	}

	nextCfgRaw, err := json.Marshal(nextCfg)
	if err != nil {
		return nil, fmt.Errorf("marshal validated config: %w", err)
	}

	nextSnapshot := &config.EffectiveConfigSnapshot{
		UUID:     uuid.NewString(),
		Software: "runtime",
		Name:     "core-config",
		FilePath: coreConfigFilePath,
		Version:  "",
		Format:   models.ConfigFormatJSON,
		Content:  string(nextCfgRaw),
	}

	coordinator := config.GetEffectiveConfigCommitCoordinator()
	commitResult, err := coordinator.Commit(
		nextSnapshot,
		func(snapshot *config.EffectiveConfigSnapshot) error {
			if snapshot == nil {
				return errors.New("file snapshot is required")
			}
			var fileCfg config.Config
			if err := json.Unmarshal([]byte(snapshot.Content), &fileCfg); err != nil {
				return fmt.Errorf("decode snapshot content for file persist: %w", err)
			}
			resolvedPath, err := resolveServiceConfigPath(snapshot.FilePath)
			if err != nil {
				return fmt.Errorf("expand file path: %w", err)
			}
			return config.SaveConfigToFile(&fileCfg, resolvedPath)
		},
		func(snapshot *config.EffectiveConfigSnapshot) error {
			if snapshot == nil {
				return errors.New("db snapshot is required")
			}
			if s == nil || s.store == nil {
				return errors.New("core config store is not initialized")
			}
			if err := s.store.SaveEffectiveConfigSnapshot(models.SoftwareEffectiveConfigSnapshot{
				Software:       snapshot.Software,
				ConfigUUID:     snapshot.UUID,
				ConfigName:     snapshot.Name,
				ConfigFilePath: snapshot.FilePath,
				ConfigVersion:  snapshot.Version,
				ConfigFormat:   snapshot.Format,
				SnapshotJSON:   snapshot.Content,
			}); err != nil {
				return err
			}

			var committedCfg config.Config
			if err := json.Unmarshal([]byte(snapshot.Content), &committedCfg); err != nil {
				return fmt.Errorf("decode snapshot content for memory commit: %w", err)
			}
			config.SetGlobalConfig(&committedCfg)
			if err := outbound.GetRegistry().RefreshAliang(committedCfg.EffectiveAliangCoreServer()); err != nil {
				return fmt.Errorf("refresh aliang registry: %w", err)
			}
			return nil
		},
	)
	if err != nil {
		return nil, err
	}

	core, err := extractCoreFromConfigContent(mergedContent)
	if err != nil {
		return nil, err
	}

	return &CoreConfigCommitResult{
		Version: commitResult.Version,
		Core:    core,
	}, nil
}

func IsCoreConfigValidationError(err error) bool {
	var target *CoreConfigValidationError
	return errors.As(err, &target)
}

func mergeCorePayload(baseCfg *config.Config, payload []byte) (string, *config.Config, error) {
	if baseCfg == nil {
		return "", nil, errors.New("global config is not initialized")
	}

	coreRaw, err := normalizeCorePayload(payload)
	if err != nil {
		return "", nil, err
	}

	baseRaw, err := json.Marshal(baseCfg)
	if err != nil {
		return "", nil, fmt.Errorf("marshal current config: %w", err)
	}

	var root map[string]json.RawMessage
	if err := json.Unmarshal(baseRaw, &root); err != nil {
		return "", nil, fmt.Errorf("decode current config: %w", err)
	}
	root["core"] = coreRaw

	mergedRaw, err := json.Marshal(root)
	if err != nil {
		return "", nil, fmt.Errorf("marshal merged config: %w", err)
	}

	var nextCfg config.Config
	if err := json.Unmarshal(mergedRaw, &nextCfg); err != nil {
		return "", nil, err
	}

	return string(mergedRaw), &nextCfg, nil
}

func normalizeCorePayload(payload []byte) (json.RawMessage, error) {
	var rawRoot map[string]json.RawMessage
	if err := json.Unmarshal(payload, &rawRoot); err != nil {
		return nil, fmt.Errorf("invalid json format: %w", err)
	}

	if len(rawRoot) == 0 {
		return nil, errors.New("core config payload is required")
	}

	if rawCore, ok := rawRoot["core"]; ok {
		if len(rawRoot) > 1 {
			for key := range rawRoot {
				if key == "core" {
					continue
				}
				return nil, fmt.Errorf("core.%s is forbidden: editable core fields are [engine aliangServer api_server]", key)
			}
		}
		return rawCore, nil
	}

	for key := range rawRoot {
		switch key {
		case "engine", "aliangServer", "api_server":
		default:
			return nil, fmt.Errorf("core.%s is forbidden: editable core fields are [engine aliangServer api_server]", key)
		}
	}

	return json.RawMessage(payload), nil
}

func extractCoreFromConfigContent(content string) (map[string]interface{}, error) {
	if strings.TrimSpace(content) == "" {
		return map[string]interface{}{}, nil
	}

	var root map[string]interface{}
	if err := json.Unmarshal([]byte(content), &root); err != nil {
		return nil, fmt.Errorf("decode config content: %w", err)
	}

	core, ok := root["core"].(map[string]interface{})
	if !ok || core == nil {
		return map[string]interface{}{}, nil
	}

	return core, nil
}
