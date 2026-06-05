package services

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/google/uuid"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/app/http/storage"
	"aliang.one/nursorgate/common/logger"
	M "aliang.one/nursorgate/inbound/tun/metadata"
	"aliang.one/nursorgate/outbound"
	"aliang.one/nursorgate/processor/config"
	"aliang.one/nursorgate/processor/mirror"
	"aliang.one/nursorgate/processor/rules"
	"aliang.one/nursorgate/processor/statistic"
)

const customerConfigFilePath = "~/.aliang/config.json"
const startupLocalCustomerBaseConfigPath = "./config.json"

type CustomerConfigService struct {
	store *storage.SoftwareConfigStore
}

type CustomerConfigCommitResult struct {
	Version  uint64
	Customer map[string]interface{}
}

type CustomerConfigValidationError struct {
	err error
}

func (e *CustomerConfigValidationError) Error() string {
	if e == nil || e.err == nil {
		return "invalid customer config"
	}
	return e.err.Error()
}

func (e *CustomerConfigValidationError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

func NewCustomerConfigService() *CustomerConfigService {
	return &CustomerConfigService{store: storage.NewSoftwareConfigStore()}
}

func (s *CustomerConfigService) GetPresetAIRuleProviders() []config.AIRuleProviderPreset {
	return config.PresetAIRuleProviders
}

func (s *CustomerConfigService) GetCommittedCustomerConfig() (map[string]interface{}, uint64, error) {
	coordinator := config.GetEffectiveConfigCommitCoordinator()
	if committed := coordinator.LastCommittedSnapshot(); committed != nil && strings.TrimSpace(committed.Content) != "" {
		customer, err := extractCustomerFromConfigContent(committed.Content)
		if err != nil {
			return nil, 0, err
		}
		return customer, coordinator.Version(), nil
	}

	globalCfg := config.GetGlobalConfig()
	if globalCfg == nil {
		return map[string]interface{}{}, coordinator.Version(), nil
	}

	raw, err := json.Marshal(globalCfg)
	if err != nil {
		return nil, 0, fmt.Errorf("marshal global config: %w", err)
	}

	customer, err := extractCustomerFromConfigContent(string(raw))
	if err != nil {
		return nil, 0, err
	}
	return customer, coordinator.Version(), nil
}

func (s *CustomerConfigService) UpdateCommittedCustomerConfig(payload []byte) (*CustomerConfigCommitResult, error) {
	if len(payload) == 0 {
		return nil, &CustomerConfigValidationError{err: errors.New("request body is empty")}
	}

	globalCfg, err := resolveBaseConfigForCustomerUpdate()
	if err != nil {
		return nil, err
	}

	mergedContent, nextCfg, err := mergeCustomerPayload(globalCfg, payload)
	if err != nil {
		return nil, &CustomerConfigValidationError{err: err}
	}

	if err := nextCfg.Validate(); err != nil {
		return nil, &CustomerConfigValidationError{err: err}
	}

	nextCfgRaw, err := json.Marshal(nextCfg)
	if err != nil {
		return nil, fmt.Errorf("marshal validated config: %w", err)
	}

	nextSnapshot := &config.EffectiveConfigSnapshot{
		UUID:     uuid.NewString(),
		Software: "runtime",
		Name:     "customer-config",
		FilePath: customerConfigFilePath,
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
			expandedPath, err := resolveServiceConfigPath(snapshot.FilePath)
			if err != nil {
				return fmt.Errorf("expand file path: %w", err)
			}
			return config.SaveConfigToFile(&fileCfg, expandedPath)
		},
		func(snapshot *config.EffectiveConfigSnapshot) error {
			if snapshot == nil {
				return errors.New("db snapshot is required")
			}
			if s == nil || s.store == nil {
				return errors.New("customer config store is not initialized")
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
			previousCfg := config.GetGlobalConfig()
			config.SetGlobalConfig(&committedCfg)
			if err := outbound.GetRegistry().RefreshCustomerProxy(&committedCfg); err != nil {
				return fmt.Errorf("refresh customer proxy registry: %w", err)
			}
			closeDisabledAIAcceleratedConnections(previousCfg, &committedCfg)
			mirror.InitGlobalForwarder()
			return nil
		},
	)
	if err != nil {
		return nil, err
	}

	customer, err := extractCustomerFromConfigContent(mergedContent)
	if err != nil {
		return nil, err
	}

	return &CustomerConfigCommitResult{
		Version:  commitResult.Version,
		Customer: customer,
	}, nil
}

func IsCustomerConfigValidationError(err error) bool {
	var target *CustomerConfigValidationError
	return errors.As(err, &target)
}

func mergeCustomerPayload(baseCfg *config.Config, payload []byte) (string, *config.Config, error) {
	if baseCfg == nil {
		return "", nil, errors.New("global config is not initialized")
	}

	customerPatch, err := normalizeCustomerPayload(payload)
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

	var currentCustomer map[string]interface{}
	if rawCustomer, ok := root["customer"]; ok && len(rawCustomer) > 0 {
		if err := json.Unmarshal(rawCustomer, &currentCustomer); err != nil {
			return "", nil, fmt.Errorf("decode current customer config: %w", err)
		}
	}
	if currentCustomer == nil {
		currentCustomer = map[string]interface{}{}
	}

	mergedCustomer := deepMergeJSONObjects(currentCustomer, customerPatch)
	mergedCustomerRaw, err := json.Marshal(mergedCustomer)
	if err != nil {
		return "", nil, fmt.Errorf("marshal merged customer config: %w", err)
	}
	root["customer"] = mergedCustomerRaw

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

func closeDisabledAIAcceleratedConnections(previousCfg, nextCfg *config.Config) {
	disabledDomains := disabledAIAccelerationDomains(previousCfg, nextCfg)
	if len(disabledDomains) == 0 {
		return
	}

	if engine := rules.GetEngine(); engine != nil {
		engine.ClearCache()
	}

	closed := statistic.DefaultManager.CloseTrackedConnectionsByMetadata(func(metadata *M.Metadata) bool {
		if metadata == nil || metadata.Route != "RouteToALiang" || metadata.HostName == "" {
			return false
		}
		return mirror.MatchesAnyDomain(metadata.HostName, disabledDomains)
	})

	if closed > 0 {
		logger.Info(fmt.Sprintf("Closed %d accelerated connections after AI rule disable", closed))
	}
}

func disabledAIAccelerationDomains(previousCfg, nextCfg *config.Config) []string {
	if previousCfg == nil || previousCfg.Customer == nil || len(previousCfg.Customer.AIRules) == 0 {
		return nil
	}

	var nextRules map[string]*config.CustomerAIRuleSetting
	if nextCfg != nil && nextCfg.Customer != nil {
		nextRules = nextCfg.Customer.AIRules
	}

	disabled := make(map[string]struct{})
	for providerKey, previousRule := range previousCfg.Customer.AIRules {
		if previousRule == nil || previousRule.Enble == nil || !*previousRule.Enble {
			continue
		}

		nextRule, ok := config.FindCustomerAIRule(nextRules, providerKey)
		if ok && nextRule != nil && nextRule.Enble != nil && *nextRule.Enble {
			continue
		}

		for _, domain := range previousRule.Include {
			normalized := strings.ToLower(strings.TrimSpace(domain))
			if normalized == "" {
				continue
			}
			disabled[normalized] = struct{}{}
		}
	}

	if len(disabled) == 0 {
		return nil
	}

	domains := make([]string, 0, len(disabled))
	for domain := range disabled {
		domains = append(domains, domain)
	}
	return domains
}

func normalizeCustomerPayload(payload []byte) (map[string]interface{}, error) {
	var rawRoot map[string]json.RawMessage
	if err := json.Unmarshal(payload, &rawRoot); err != nil {
		return nil, fmt.Errorf("invalid json format: %w", err)
	}

	if len(rawRoot) == 0 {
		return nil, errors.New("customer config payload is required")
	}

	if rawCustomer, ok := rawRoot["customer"]; ok {
		if len(rawRoot) > 1 {
			forbidden := make([]string, 0, len(rawRoot)-1)
			for key := range rawRoot {
				if key == "customer" {
					continue
				}
				forbidden = append(forbidden, key)
			}
			sort.Strings(forbidden)
			return nil, fmt.Errorf("customer.%s is forbidden: editable customer fields are [proxy ai_rules proxy_rules traffic_mirror http1_drop]", forbidden[0])
		}
		rawRoot = map[string]json.RawMessage{}
		if err := json.Unmarshal(rawCustomer, &rawRoot); err != nil {
			return nil, fmt.Errorf("invalid customer json format: %w", err)
		}
	} else {
		if err := validateEditableCustomerKeys(rawRoot); err != nil {
			return nil, err
		}
	}

	normalized := make(map[string]interface{}, len(rawRoot))
	for key, rawValue := range rawRoot {
		var value interface{}
		if err := json.Unmarshal(rawValue, &value); err != nil {
			return nil, fmt.Errorf("invalid customer.%s format: %w", key, err)
		}
		normalized[key] = value
	}

	return normalized, nil
}

func validateEditableCustomerKeys(rawRoot map[string]json.RawMessage) error {
	for key := range rawRoot {
		switch key {
		case "proxy", "ai_rules", "proxy_rules", "traffic_mirror", "http1_drop":
		default:
			return fmt.Errorf("customer.%s is forbidden: editable customer fields are [proxy ai_rules proxy_rules traffic_mirror http1_drop]", key)
		}
	}
	return nil
}

func deepMergeJSONObjects(base, patch map[string]interface{}) map[string]interface{} {
	if len(base) == 0 && len(patch) == 0 {
		return map[string]interface{}{}
	}

	merged := make(map[string]interface{}, len(base)+len(patch))
	for key, value := range base {
		merged[key] = cloneJSONValue(value)
	}

	for key, value := range patch {
		if value == nil {
			continue
		}

		baseMap, baseIsMap := merged[key].(map[string]interface{})
		patchMap, patchIsMap := value.(map[string]interface{})
		if baseIsMap && patchIsMap {
			merged[key] = deepMergeJSONObjects(baseMap, patchMap)
			continue
		}
		merged[key] = cloneJSONValue(value)
	}

	return merged
}

func cloneJSONValue(value interface{}) interface{} {
	switch typed := value.(type) {
	case map[string]interface{}:
		cloned := make(map[string]interface{}, len(typed))
		for key, child := range typed {
			cloned[key] = cloneJSONValue(child)
		}
		return cloned
	case []interface{}:
		cloned := make([]interface{}, len(typed))
		for i, child := range typed {
			cloned[i] = cloneJSONValue(child)
		}
		return cloned
	default:
		if typed == nil {
			return nil
		}
		if reflect.TypeOf(typed).Kind() == reflect.Slice {
			return typed
		}
		return typed
	}
}

func extractCustomerFromConfigContent(content string) (map[string]interface{}, error) {
	if strings.TrimSpace(content) == "" {
		return map[string]interface{}{}, nil
	}

	var root map[string]interface{}
	if err := json.Unmarshal([]byte(content), &root); err != nil {
		return nil, fmt.Errorf("decode config content: %w", err)
	}

	customer, ok := root["customer"].(map[string]interface{})
	if !ok || customer == nil {
		return map[string]interface{}{}, nil
	}

	return customer, nil
}

func resolveBaseConfigForCustomerUpdate() (*config.Config, error) {
	if globalCfg := config.GetGlobalConfig(); globalCfg != nil {
		return globalCfg, nil
	}

	coordinator := config.GetEffectiveConfigCommitCoordinator()
	if committed := coordinator.LastCommittedSnapshot(); committed != nil && strings.TrimSpace(committed.Content) != "" {
		var committedCfg config.Config
		if err := json.Unmarshal([]byte(committed.Content), &committedCfg); err != nil {
			return nil, fmt.Errorf("decode committed snapshot content: %w", err)
		}
		return &committedCfg, nil
	}

	for _, candidatePath := range customerUpdateBaseConfigCandidates() {
		fileCfg, found, err := readConfigFromPath(candidatePath)
		if err != nil {
			return nil, err
		}
		if found {
			return fileCfg, nil
		}
	}

	return bootstrapBaseConfigForCustomerUpdate(), nil
}

func bootstrapBaseConfigForCustomerUpdate() *config.Config {
	return &config.Config{
		Core: &config.CoreConfig{
			APIServer: "https://sub2api.liang.home",
		},
		Customer: &config.CustomerConfig{},
	}
}

func customerUpdateBaseConfigCandidates() []string {
	return []string{
		startupLocalCustomerBaseConfigPath,
		customerConfigFilePath,
	}
}

func readConfigFromPath(path string) (*config.Config, bool, error) {
	resolvedPath, err := resolveServiceConfigPath(path)
	if err != nil {
		return nil, false, fmt.Errorf("expand config file path %q: %w", path, err)
	}

	raw, err := os.ReadFile(resolvedPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("read config file %q: %w", resolvedPath, err)
	}

	var fileCfg config.Config
	if err := json.Unmarshal(raw, &fileCfg); err != nil {
		return nil, false, fmt.Errorf("decode config file %q: %w", filepath.Clean(resolvedPath), err)
	}

	return &fileCfg, true, nil
}
