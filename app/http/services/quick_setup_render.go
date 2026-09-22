package services

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"aliang.one/nursorgate/app/http/models"
	auth "aliang.one/nursorgate/processor/auth"
	"aliang.one/nursorgate/processor/config"
)

func (s *QuickSetupService) Render(req models.QuickSetupRenderRequest) (*models.QuickSetupRenderResponse, error) {
	software := strings.TrimSpace(req.Software)
	if software == "" {
		return nil, errors.New("software is required")
	}

	softwareDef, ok := findQuickSetupSoftware(software)
	if !ok {
		return nil, fmt.Errorf("unsupported software: %s", software)
	}
	if len(req.KeyIDs) == 0 {
		return nil, errors.New("at least one key_id is required")
	}

	baseRoot, err := quickSetupBaseURL()
	if err != nil {
		return nil, err
	}
	// 接入模式只换 host 根（spec §7.1）：local → 本地推理代理；public/未知/空 → 推理面
	// 域名。非法 mode 值与空值一致回落 public 而不报错，旧前端不传 mode 时保持现行为。
	modeRoot := quickSetupModeRoot(req.Mode, baseRoot)

	apiKeys, err := quickSetupGetAPIKeysFn()
	if err != nil {
		if isSessionMissingError(err) {
			return nil, ErrQuickSetupUnauthenticated
		}
		return nil, err
	}

	selectedIDs := make(map[int64]struct{}, len(req.KeyIDs))
	for _, id := range req.KeyIDs {
		if id <= 0 {
			return nil, fmt.Errorf("selected API key id is not valid: %d", id)
		}
		selectedIDs[id] = struct{}{}
	}

	keys := toQuickSetupAPIKeys(apiKeys, modeRoot)

	// 合并发生在 Render（预览）阶段（spec §7）：先读磁盘现有内容，与我们的键合并后
	// 作为预览返回。解析目标用户失败时不报错——Render 是只读预览，不应因本机环境
	// 失败，此时全部文件走模板兜底（home 为空即触发兜底）。
	targetUser, userErr := quickSetupTargetUserFn()
	home := ""
	if userErr == nil {
		home = targetUser.homeDir
	}

	if softwareDef.Code == "opencode" {
		variants, err := renderOpenCodeVariants(softwareDef, keys, selectedIDs, req.OpenCode, modeRoot, home)
		if err != nil {
			return nil, err
		}
		return &models.QuickSetupRenderResponse{
			Software: softwareDef.Code,
			Variants: variants,
		}, nil
	}

	var variants []models.QuickSetupVariant
	matchedIDs := make(map[int64]struct{}, len(selectedIDs))
	for _, key := range keys {
		if _, ok := selectedIDs[key.ID]; !ok {
			continue
		}
		if !softwareSupportsProvider(softwareDef, key.Provider) {
			continue
		}
		if !quickSetupAPIKeyHasPlainSecret(key) {
			return nil, fmt.Errorf("plaintext secret is required for selected API key %q", key.Name)
		}

		files, notes, err := renderQuickSetupFiles(softwareDef, key, modeRoot, home)
		if err != nil {
			return nil, err
		}

		variants = append(variants, models.QuickSetupVariant{
			Software: softwareDef.Code,
			Label:    fmt.Sprintf("%s · %s", key.Name, strings.ToUpper(key.Provider)),
			Provider: key.Provider,
			APIKey:   key,
			Files:    files,
			Notes:    notes,
		})
		matchedIDs[key.ID] = struct{}{}
	}
	if len(matchedIDs) != len(selectedIDs) {
		return nil, errors.New("one or more selected API keys are not valid for this software")
	}

	sort.SliceStable(variants, func(i, j int) bool {
		if variants[i].Provider == variants[j].Provider {
			return variants[i].APIKey.Name < variants[j].APIKey.Name
		}
		return variants[i].Provider < variants[j].Provider
	})

	return &models.QuickSetupRenderResponse{
		Software: softwareDef.Code,
		Variants: variants,
	}, nil
}

func validateQuickSetupOpenCode(content string) error {
	var cfg quickSetupOpenCodeConfig
	if err := json.Unmarshal([]byte(content), &cfg); err != nil {
		return err
	}
	if len(cfg.Providers) == 0 {
		return errors.New("provider must contain at least one entry")
	}

	providers := make(map[string]quickSetupOpenCodeProvider, len(cfg.Providers))
	for providerID, raw := range cfg.Providers {
		providerID = strings.TrimSpace(providerID)
		if providerID == "" {
			return errors.New("provider id cannot be empty")
		}
		var provider quickSetupOpenCodeProvider
		if err := json.Unmarshal(raw, &provider); err != nil {
			return fmt.Errorf("provider %q must be an object", providerID)
		}
		if strings.TrimSpace(provider.NPM) == "" {
			return fmt.Errorf("provider %q npm is required", providerID)
		}
		if strings.TrimSpace(provider.Options.APIKey) == "" || quickSetupLooksMaskedAPIKey(provider.Options.APIKey) {
			return fmt.Errorf("provider %q requires a plaintext options.apiKey", providerID)
		}
		baseURL, err := url.Parse(strings.TrimSpace(provider.Options.BaseURL))
		if err != nil || baseURL.Host == "" || (baseURL.Scheme != "http" && baseURL.Scheme != "https") {
			return fmt.Errorf("provider %q options.baseURL must be an absolute HTTP(S) URL", providerID)
		}
		if len(provider.Models) == 0 {
			return fmt.Errorf("provider %q models must contain at least one model", providerID)
		}
		providers[providerID] = provider
	}

	if err := validateQuickSetupOpenCodeModelRef("model", cfg.Model, providers, true); err != nil {
		return err
	}
	if err := validateQuickSetupOpenCodeModelRef("small_model", cfg.SmallModel, providers, false); err != nil {
		return err
	}
	return nil
}

func validateQuickSetupOpenCodeModelRef(field string, ref string, providers map[string]quickSetupOpenCodeProvider, required bool) error {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		if required {
			return fmt.Errorf("%s is required", field)
		}
		return nil
	}
	providerID, modelID, ok := strings.Cut(ref, "/")
	providerID = strings.TrimSpace(providerID)
	modelID = strings.TrimSpace(modelID)
	if !ok || providerID == "" || modelID == "" {
		return fmt.Errorf("%s must use provider/model format", field)
	}
	provider, exists := providers[providerID]
	if !exists {
		return fmt.Errorf("%s references unknown provider %q", field, providerID)
	}
	if _, exists := provider.Models[modelID]; !exists {
		return fmt.Errorf("%s references unknown model %q for provider %q", field, modelID, providerID)
	}
	return nil
}

func toQuickSetupAPIKeys(apiKeys []auth.UserAPIKey, apiRoot string) []models.QuickSetupAPIKey {
	items := make([]models.QuickSetupAPIKey, 0, len(apiKeys))
	for _, key := range apiKeys {
		if strings.ToLower(strings.TrimSpace(key.Status)) == "inactive" {
			continue
		}

		var group *models.APIKeyGroupResponse
		if key.Group != nil {
			group = &models.APIKeyGroupResponse{
				ID:                    key.Group.ID,
				Name:                  key.Group.Name,
				Description:           key.Group.Description,
				Platform:              key.Group.Platform,
				RateMultiplier:        key.Group.RateMultiplier,
				ClaudeCodeOnly:        key.Group.ClaudeCodeOnly,
				AllowMessagesDispatch: key.Group.AllowMessagesDispatch,
			}
		}

		items = append(items, models.QuickSetupAPIKey{
			ID:              key.ID,
			Key:             key.Key,
			Name:            key.Name,
			Provider:        strings.ToLower(strings.TrimSpace(key.Provider)),
			BaseURL:         quickSetupProviderBaseURL(strings.ToLower(strings.TrimSpace(key.Provider)), apiRoot),
			Status:          key.Status,
			Masked:          key.Masked,
			SecretAvailable: key.SecretAvailable,
			Group:           group,
		})
	}

	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Provider == items[j].Provider {
			return items[i].Name < items[j].Name
		}
		return items[i].Provider < items[j].Provider
	})

	return items
}

func quickSetupAPIKeyHasPlainSecret(apiKey models.QuickSetupAPIKey) bool {
	keyValue := strings.TrimSpace(apiKey.Key)
	return keyValue != "" && !quickSetupLooksMaskedAPIKey(keyValue)
}

func renderQuickSetupFiles(software models.QuickSetupSoftware, apiKey models.QuickSetupAPIKey, apiRoot string, home string) ([]models.QuickSetupPreviewFile, []string, error) {
	switch software.Code {
	case "opencode":
		return renderOpenCodeFiles(software, []models.QuickSetupAPIKey{apiKey}, apiKey.ID, "", "", apiRoot, home)
	case "codex":
		return renderCodexFiles(software, apiKey, apiRoot, home)
	case "claude-code":
		return renderClaudeCodeFiles(software, apiKey, apiRoot, home)
	default:
		return nil, nil, fmt.Errorf("unsupported software: %s", software.Code)
	}
}

func renderOpenCodeVariants(software models.QuickSetupSoftware, keys []models.QuickSetupAPIKey, selectedIDs map[int64]struct{}, spec *models.OpenCodeRenderSpec, apiRoot string, home string) ([]models.QuickSetupVariant, error) {
	var selected []models.QuickSetupAPIKey
	matchedIDs := make(map[int64]struct{}, len(selectedIDs))
	for _, key := range keys {
		if !softwareSupportsProvider(software, key.Provider) {
			continue
		}
		if _, ok := selectedIDs[key.ID]; !ok {
			continue
		}
		selected = append(selected, key)
		matchedIDs[key.ID] = struct{}{}
	}
	if len(matchedIDs) != len(selectedIDs) {
		return nil, errors.New("one or more selected API keys are not valid for OpenCode")
	}

	modelKeyID := int64(0)
	modelProvider := ""
	model := ""
	smallModel := ""
	if spec != nil {
		modelKeyID = spec.ModelKeyID
		modelProvider = strings.TrimSpace(spec.ModelProvider)
		model = strings.TrimSpace(spec.Model)
		smallModel = strings.TrimSpace(spec.SmallModel)
		if modelKeyID != 0 {
			if _, ok := matchedIDs[modelKeyID]; !ok {
				return nil, errors.New("model_key_id must reference a selected API key")
			}
		}
	}
	modelKey := selectOpenCodeModelKey(selected, modelKeyID, modelProvider)
	if modelProvider != "" && modelKey.Provider != modelProvider {
		return nil, errors.New("model_provider must reference a selected API key provider")
	}
	files, notes, err := renderOpenCodeFiles(software, selected, modelKey.ID, model, smallModel, apiRoot, home)
	if err != nil {
		return nil, err
	}

	label := fmt.Sprintf("OpenCode · %d provider", len(selected))
	if len(selected) != 1 {
		label += "s"
	}
	return []models.QuickSetupVariant{
		{
			Software: software.Code,
			Label:    label,
			Provider: "opencode",
			APIKey:   modelKey,
			APIKeys:  selected,
			Files:    files,
			Notes:    notes,
		},
	}, nil
}

func selectOpenCodeModelKey(keys []models.QuickSetupAPIKey, preferredID int64, preferredProvider string) models.QuickSetupAPIKey {
	if len(keys) == 0 {
		return models.QuickSetupAPIKey{}
	}
	for _, key := range keys {
		if preferredID != 0 && key.ID == preferredID {
			return key
		}
	}
	for _, key := range keys {
		if preferredProvider != "" && key.Provider == preferredProvider {
			return key
		}
	}
	for _, key := range keys {
		if key.Provider == "openai" {
			return key
		}
	}
	return keys[0]
}

func renderOpenCodeFiles(software models.QuickSetupSoftware, apiKeys []models.QuickSetupAPIKey, modelKeyID int64, modelOverride string, smallModelOverride string, apiRoot string, home string) ([]models.QuickSetupPreviewFile, []string, error) {
	fileDef := software.Files[0]
	if len(apiKeys) == 0 {
		return nil, nil, errors.New("at least one API key is required for OpenCode")
	}
	for _, apiKey := range apiKeys {
		if !quickSetupAPIKeyHasPlainSecret(apiKey) {
			return nil, nil, fmt.Errorf("plaintext secret is required for selected API key %q", apiKey.Name)
		}
	}
	providers := make(map[string]interface{}, len(apiKeys))
	seen := map[string]int{}
	modelProviderID := ""
	modelName := ""
	notes := []string{
		"OpenCode config uses provider/model strings such as aliang-openai-openai-key/gpt-5.4.",
		"Each selected API key becomes one provider entry, so multiple baseURL/API key combinations can coexist.",
	}
	for _, apiKey := range apiKeys {
		providerID := quickSetupOpenCodeProviderID(apiKey, seen)
		baseURL := quickSetupProviderBaseURL(apiKey.Provider, apiRoot)
		providers[providerID] = map[string]interface{}{
			"npm":  quickSetupOpenCodeProviderNPM(apiKey.Provider),
			"name": quickSetupOpenCodeProviderName(apiKey),
			"options": map[string]interface{}{
				"baseURL": baseURL,
				"apiKey":  apiKey.Key,
			},
			"models": quickSetupOpenCodeModels(apiKey.Provider),
		}
		if modelProviderID == "" || apiKey.ID == modelKeyID {
			modelProviderID = providerID
			modelName = quickSetupDefaultModel(apiKey.Provider, false)
		}
		notes = append(notes, fmt.Sprintf("%s uses %s.", providerID, baseURL))
		if apiKey.Masked {
			notes = append(notes, fmt.Sprintf("%s looks masked. Replace it with the plaintext value before applying.", apiKey.Name))
		}
	}
	if strings.TrimSpace(modelOverride) != "" {
		modelName = strings.TrimSpace(modelOverride)
	}
	if modelName == "" {
		modelName = "gpt-5.4"
	}
	ensureOpenCodeProviderModel(providers, modelProviderID, modelName)
	defaultModel := fmt.Sprintf("%s/%s", modelProviderID, modelName)
	config := map[string]interface{}{
		"$schema":  "https://opencode.ai/config.json",
		"provider": providers,
		"model":    defaultModel,
	}
	if strings.TrimSpace(smallModelOverride) != "" {
		smallModelName := strings.TrimSpace(smallModelOverride)
		ensureOpenCodeProviderModel(providers, modelProviderID, smallModelName)
		config["small_model"] = fmt.Sprintf("%s/%s", modelProviderID, smallModelName)
	}
	merged, mergedFromDisk, degradedNote, err := quickSetupMergedJSONObject(software.Code, quickSetupLeafFileName(fileDef.DefaultPath), fileDef.DefaultPath, home, config)
	if err != nil {
		return nil, nil, err
	}
	raw, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		return nil, nil, err
	}
	if degradedNote != "" {
		notes = append(notes, degradedNote)
	}

	return []models.QuickSetupPreviewFile{
		{
			Code:           fileDef.Code,
			Label:          fileDef.Label,
			Path:           fileDef.DefaultPath,
			Format:         fileDef.Format,
			Kind:           fileDef.Kind,
			Content:        string(raw),
			MergedFromDisk: mergedFromDisk,
		},
	}, notes, nil
}

func quickSetupOpenCodeProviderID(apiKey models.QuickSetupAPIKey, seen map[string]int) string {
	base := "aliang-" + strings.ToLower(strings.TrimSpace(apiKey.Provider))
	name := strings.ToLower(strings.TrimSpace(apiKey.Name))
	if name != "" {
		base += "-" + name
	}
	base = strings.Trim(strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			return r
		}
		return '-'
	}, base), "-")
	for strings.Contains(base, "--") {
		base = strings.ReplaceAll(base, "--", "-")
	}
	if base == "" {
		base = "aliang-provider"
	}
	seen[base]++
	if seen[base] == 1 {
		return base
	}
	return fmt.Sprintf("%s-%d", base, seen[base])
}

func quickSetupOpenCodeProviderName(apiKey models.QuickSetupAPIKey) string {
	name := strings.TrimSpace(apiKey.Name)
	if name == "" {
		name = quickSetupProviderLabel(apiKey.Provider)
	}
	return "Aliang " + name
}

func firstNonEmptyQuickSetupString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func quickSetupOpenCodeModels(provider string) map[string]interface{} {
	switch provider {
	case "anthropic":
		return map[string]interface{}{
			"claude-sonnet-4-5-20250929": map[string]interface{}{"name": "Claude Sonnet 4.5"},
		}
	default:
		return map[string]interface{}{
			"gpt-5.4": map[string]interface{}{"name": "GPT-5.4"},
		}
	}
}

func quickSetupOpenCodeProviderNPM(provider string) string {
	if provider == "anthropic" {
		return "@ai-sdk/anthropic"
	}
	return "@ai-sdk/openai-compatible"
}

func ensureOpenCodeProviderModel(providers map[string]interface{}, providerID string, modelName string) {
	if modelName == "" {
		return
	}
	provider, ok := providers[providerID].(map[string]interface{})
	if !ok {
		return
	}
	models, ok := provider["models"].(map[string]interface{})
	if !ok {
		models = map[string]interface{}{}
		provider["models"] = models
	}
	if _, exists := models[modelName]; !exists {
		models[modelName] = map[string]interface{}{"name": modelName}
	}
}

func quickSetupModelListBaseURL(apiRoot string) string {
	root := strings.TrimRight(strings.TrimSpace(apiRoot), "/")
	if strings.HasSuffix(root, "/v1") {
		return root
	}
	return root + "/v1"
}

func fetchQuickSetupModels(baseURL string, apiKey string) ([]models.QuickSetupModel, error) {
	endpoint := strings.TrimRight(strings.TrimSpace(baseURL), "/") + "/models"
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(apiKey))
	req.Header.Set("Accept", "application/json")

	resp, err := quickSetupModelsHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to load model list: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("failed to read model list response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		message := strings.TrimSpace(string(body))
		if message == "" {
			message = resp.Status
		}
		if len(message) > 240 {
			message = message[:240] + "..."
		}
		return nil, fmt.Errorf("model list request failed (%d): %s", resp.StatusCode, message)
	}

	modelsList, err := parseQuickSetupModels(body)
	if err != nil {
		return nil, fmt.Errorf("failed to parse model list response: %w", err)
	}
	return modelsList, nil
}

type quickSetupModelEntry struct {
	ID      string
	Name    string
	OwnedBy string
	Created int64
}

func parseQuickSetupModels(body []byte) ([]models.QuickSetupModel, error) {
	var payload interface{}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}

	var entries []quickSetupModelEntry
	collectQuickSetupModelEntries(payload, &entries)

	modelsList := make([]models.QuickSetupModel, 0, len(entries))
	seen := map[string]struct{}{}
	for _, item := range entries {
		id := strings.TrimSpace(item.ID)
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		name := strings.TrimSpace(item.Name)
		if name == "" {
			name = id
		}
		modelsList = append(modelsList, models.QuickSetupModel{
			ID:      id,
			Name:    name,
			OwnedBy: strings.TrimSpace(item.OwnedBy),
			Created: item.Created,
		})
	}
	sort.SliceStable(modelsList, func(i, j int) bool {
		return modelsList[i].ID < modelsList[j].ID
	})
	return modelsList, nil
}

func collectQuickSetupModelEntries(value interface{}, entries *[]quickSetupModelEntry) {
	switch typed := value.(type) {
	case []interface{}:
		for _, item := range typed {
			collectQuickSetupModelEntries(item, entries)
		}
	case map[string]interface{}:
		entryAdded := false
		if entry, ok := quickSetupModelEntryFromMap(typed); ok {
			*entries = append(*entries, entry)
			entryAdded = true
		}
		nestedAdded := false
		for _, key := range []string{"data", "models", "items"} {
			if nested, ok := typed[key]; ok {
				collectQuickSetupModelEntries(nested, entries)
				nestedAdded = true
			}
		}
		if !entryAdded && !nestedAdded {
			collectQuickSetupModelMapEntries(typed, entries)
		}
	case string:
		if id := strings.TrimSpace(typed); id != "" {
			*entries = append(*entries, quickSetupModelEntry{ID: id, Name: id})
		}
	}
}

func collectQuickSetupModelMapEntries(value map[string]interface{}, entries *[]quickSetupModelEntry) {
	for key, raw := range value {
		id := strings.TrimSpace(key)
		if id == "" {
			continue
		}
		switch typed := raw.(type) {
		case map[string]interface{}:
			name := firstNonEmptyQuickSetupString(
				quickSetupStringField(typed, "name"),
				quickSetupStringField(typed, "display_name"),
				quickSetupStringField(typed, "label"),
				id,
			)
			*entries = append(*entries, quickSetupModelEntry{
				ID:      id,
				Name:    name,
				OwnedBy: firstNonEmptyQuickSetupString(quickSetupStringField(typed, "owned_by"), quickSetupStringField(typed, "ownedBy"), quickSetupStringField(typed, "owner")),
				Created: quickSetupInt64Field(typed, "created"),
			})
		case string:
			name := firstNonEmptyQuickSetupString(typed, id)
			*entries = append(*entries, quickSetupModelEntry{ID: id, Name: name})
		}
	}
}

func quickSetupModelEntryFromMap(item map[string]interface{}) (quickSetupModelEntry, bool) {
	id := firstNonEmptyQuickSetupString(
		quickSetupStringField(item, "id"),
		quickSetupStringField(item, "model"),
		quickSetupStringField(item, "name"),
	)
	id = strings.TrimSpace(id)
	if id == "" {
		return quickSetupModelEntry{}, false
	}
	name := firstNonEmptyQuickSetupString(
		quickSetupStringField(item, "name"),
		quickSetupStringField(item, "display_name"),
		quickSetupStringField(item, "label"),
		id,
	)
	return quickSetupModelEntry{
		ID:      id,
		Name:    name,
		OwnedBy: firstNonEmptyQuickSetupString(quickSetupStringField(item, "owned_by"), quickSetupStringField(item, "ownedBy"), quickSetupStringField(item, "owner")),
		Created: quickSetupInt64Field(item, "created"),
	}, true
}

func quickSetupStringField(item map[string]interface{}, key string) string {
	value, ok := item[key]
	if !ok || value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case fmt.Stringer:
		return strings.TrimSpace(typed.String())
	default:
		return strings.TrimSpace(fmt.Sprint(typed))
	}
}

func quickSetupInt64Field(item map[string]interface{}, key string) int64 {
	value, ok := item[key]
	if !ok || value == nil {
		return 0
	}
	switch typed := value.(type) {
	case float64:
		return int64(typed)
	case int64:
		return typed
	case int:
		return int64(typed)
	default:
		return 0
	}
}

// quickSetupReadState 是读磁盘现有配置的三态结果（Task 9 代码评审）：
// missing = 不存在（或路径不可达：家目录为空/策略拒绝）→ 模板形态、无警告；
// unreadable = 存在但读不了（非普通文件/权限拒绝/超大小上限）→ 模板形态 + 点名警告；
// ok = 成功读出内容 → 走合并。
type quickSetupReadState int

const (
	quickSetupReadMissing quickSetupReadState = iota
	quickSetupReadUnreadable
	quickSetupReadOK
)

// quickSetupReadExistingFile 只读读取磁盘上 defaultPath 对应的现有配置文件内容，
// 供 Render 预览合并（spec §7）。任何失败都不报错——Render 是只读操作，绝不因
// 本机环境差异整体报错，由调用方按三态走模板兜底或降级警告。
// 特例：0 字节的已存在文件归 missing（评审）——空文件没有内容可保留，
// MergedFromDisk 不该为 true，也不该触发解析失败警告。
func quickSetupReadExistingFile(software, defaultPath, home string) (string, quickSetupReadState) {
	home = strings.TrimSpace(home)
	if home == "" {
		return "", quickSetupReadMissing
	}
	resolved, err := resolveQuickSetupApplyPath(software, defaultPath, home)
	if err != nil {
		// 解析失败需区分实情：目标位置存在但非普通文件（resolve 的普通文件闸门
		// 拒绝，如目录占位）属于「存在但读不了」→ unreadable；其余（家目录空已
		// 前置、路径越界等策略拒绝）维持现行为 missing，避免对未知状态误报警告。
		if quickSetupTargetIsNonRegular(defaultPath, home) {
			return "", quickSetupReadUnreadable
		}
		return "", quickSetupReadMissing
	}
	info, err := os.Stat(resolved)
	if err != nil {
		if os.IsNotExist(err) {
			return "", quickSetupReadMissing
		}
		return "", quickSetupReadUnreadable
	}
	if !info.Mode().IsRegular() || info.Size() > quickSetupMaxApplyFileBytes {
		return "", quickSetupReadUnreadable
	}
	raw, err := os.ReadFile(resolved)
	if err != nil {
		return "", quickSetupReadUnreadable
	}
	if len(raw) > quickSetupMaxApplyFileBytes {
		return "", quickSetupReadUnreadable
	}
	if len(raw) == 0 {
		return "", quickSetupReadMissing
	}
	return string(raw), quickSetupReadOK
}

// quickSetupTargetIsNonRegular 判断 defaultPath 展开后目标位置是否为「存在但非
// 普通文件」（目录/设备等），供 resolve 策略拒绝时区分 unreadable 与 missing。
// stat 失败（含不存在）一律不算此情形。
func quickSetupTargetIsNonRegular(defaultPath, home string) bool {
	expanded := expandQuickSetupHomePath(defaultPath, home)
	if !filepath.IsAbs(expanded) {
		return false
	}
	info, err := os.Stat(expanded)
	if err != nil {
		return false
	}
	return !info.Mode().IsRegular()
}

// quickSetupLeafFileName 从 defaultPath 取降级警告里点名的文件名（如 auth.json，
// 评审 Minor 3——与 TOML 侧点名 config.toml 对称）。取不到有效段时原样返回。
func quickSetupLeafFileName(defaultPath string) string {
	leaf := filepath.Base(strings.TrimSpace(defaultPath))
	if leaf == "" || leaf == "." || leaf == string(filepath.Separator) || leaf == "~" {
		return defaultPath
	}
	return leaf
}

// quickSetupMergedJSONObject 读磁盘 existing 并与 incoming 深合并（incoming 键胜出），
// 返回合并后的对象与合并状态。fileName 由调用方传入，用于降级警告点名具体文件
// （评审 Minor 3）。磁盘无文件 → 返回 incoming 原样、无警告；存在但读不了 → 同样
// 返回模板并给出点名读盘警告；existing 解析失败 → 返回模板并给出点名解析警告
// （spec §7：损坏的磁盘内容不得让预览失败）。
// 注意：返回值可能与 incoming 共享子 map 引用（mergeQuickSetupJSONInto 契约），
// 调用方不得再修改 incoming 的子对象；各渲染器的载荷均为每次调用新建，满足该约束。
func quickSetupMergedJSONObject(software, fileName, defaultPath, home string, incoming map[string]interface{}) (merged map[string]interface{}, mergedFromDisk bool, degradedNote string, err error) {
	raw, state := quickSetupReadExistingFile(software, defaultPath, home)
	switch state {
	case quickSetupReadMissing:
		return incoming, false, "", nil
	case quickSetupReadUnreadable:
		return incoming, false, "Could not read the existing " + fileName + " on disk; showing a fresh template instead. It will not be modified.", nil
	}
	var existing map[string]interface{}
	if jsonErr := json.Unmarshal([]byte(raw), &existing); jsonErr != nil {
		return incoming, false, "Could not parse your existing " + fileName + "; showing a fresh template instead. Review carefully before applying — applying will replace the existing file.", nil
	}
	merged, _ = mergeQuickSetupJSONObjects(existing, incoming)
	return merged, true, "", nil
}

func renderCodexFiles(software models.QuickSetupSoftware, apiKey models.QuickSetupAPIKey, apiRoot string, home string) ([]models.QuickSetupPreviewFile, []string, error) {
	files := make([]models.QuickSetupPreviewFile, 0, len(software.Files))
	providerKey := apiKey.Provider
	model := quickSetupDefaultModel(providerKey, true)
	// 统一 [model_providers.aliang] 段（spec §7）：段内走 OpenAI wire 语义
	// （env_key = OPENAI_API_KEY），base_url 与旧 openai 表一致使用 /v1 根。
	baseURL := quickSetupProviderBaseURL("openai", apiRoot)

	notes := []string{
		"Codex auth.json officially stores OPENAI_API_KEY for API-key sign-in.",
	}
	if providerKey != "openai" {
		notes = append(notes, "For non-openai providers, Codex still relies on a custom provider in config.toml. Verify your gateway can serve OpenAI Responses semantics for this key.")
	}
	if apiKey.Masked {
		notes = append(notes, "This API key looks masked. Replace it with the plaintext value before applying.")
	}

	for _, fileDef := range software.Files {
		preview := models.QuickSetupPreviewFile{
			Code:   fileDef.Code,
			Label:  fileDef.Label,
			Path:   fileDef.DefaultPath,
			Format: fileDef.Format,
			Kind:   fileDef.Kind,
		}
		if fileDef.Code == "auth" {
			content, mergedFromDisk, degradedNote, err := renderCodexAuthPreview(apiKey, software.Code, fileDef.DefaultPath, home)
			if err != nil {
				return nil, nil, err
			}
			preview.Content = content
			preview.MergedFromDisk = mergedFromDisk
			if degradedNote != "" {
				notes = append(notes, degradedNote)
			}
		} else {
			content, mergedFromDisk, degradedNote := renderCodexConfigPreview(model, baseURL, software.Code, fileDef.DefaultPath, home)
			preview.Content = content
			preview.MergedFromDisk = mergedFromDisk
			if degradedNote != "" {
				notes = append(notes, degradedNote)
			}
		}
		files = append(files, preview)
	}
	return files, notes, nil
}

// renderCodexConfigPreview 产出 config.toml 预览：磁盘有文件走 mergeCodexTOML
// 行级拼接；磁盘无文件/读不了也走 mergeCodexTOML("") 的模板形态——保证全新安装
// 同样产出统一 [model_providers.aliang] 段，而非旧版 openai/gateway 表（DoD #4 与
// config-state 的 managed 判定都依赖这一点）。读不了（评审三态）补点名警告。
// 返回内容、是否合并自磁盘、降级警告。
func renderCodexConfigPreview(model, baseURL, softwareCode, defaultPath, home string) (string, bool, string) {
	fileName := quickSetupLeafFileName(defaultPath)
	existing, state := quickSetupReadExistingFile(softwareCode, defaultPath, home)
	if state != quickSetupReadOK {
		existing = "" // missing/unreadable 一律模板形态（unreadable 在成功路径补警告）
	}
	merged, err := mergeCodexTOML(existing, model, baseURL)
	if err == nil {
		note := ""
		if state == quickSetupReadUnreadable {
			note = "Could not read the existing " + fileName + " on disk; showing a fresh template instead. It will not be modified."
		}
		return merged, state == quickSetupReadOK, note
	}
	// Task 6 审查红线：mergeCodexTOML 的错误必须显式处理，不得静默吞掉。
	// 降级为「模板形态」（空 existing），并给出人话警告让用户应用前自查。
	merged, err = mergeCodexTOML("", model, baseURL)
	if err != nil {
		// 理论不可达（空输入必产出合法 TOML）；仍按红线兜底为最小模板字符串。
		merged = fallbackCodexTemplateTOML(model, baseURL)
	}
	return merged, false, "Your existing " + fileName + " could not be merged safely, so the preview shows a fresh template. Review carefully before applying — applying will replace the existing file."
}

// fallbackCodexTemplateTOML 是 renderCodexConfigPreview 的最后兜底：与
// mergeCodexTOML("", ...) 的模板形态等价的最小字符串（仅在我们键 + aliang 段）。
// 末尾换行与 mergeCodexTOML 产物保持一致（评审 Minor 4）。
func fallbackCodexTemplateTOML(model, baseURL string) string {
	lines := append([]string{
		"model = " + quickSetupTOMLQuote(model),
		"model_provider = " + quickSetupTOMLQuote(quickSetupCodexProviderID),
		"",
	}, buildCodexAliangSection(baseURL, "")...)
	return strings.Join(lines, "\n") + "\n"
}

// renderCodexAuthPreview 产出 auth.json 预览：磁盘有文件走 JSON 深合并（保住
// ChatGPT 登录态 tokens，Task 5 已锁）；磁盘无文件用模板。
func renderCodexAuthPreview(apiKey models.QuickSetupAPIKey, softwareCode, defaultPath, home string) (string, bool, string, error) {
	value := apiKey.Key
	if apiKey.Provider != "openai" && apiKey.Masked {
		value = ""
	}
	template := map[string]interface{}{"OPENAI_API_KEY": value}
	merged, mergedFromDisk, degradedNote, err := quickSetupMergedJSONObject(softwareCode, quickSetupLeafFileName(defaultPath), defaultPath, home, template)
	if err != nil {
		return "", false, "", err
	}
	raw, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		return "", false, "", err
	}
	return string(raw), mergedFromDisk, degradedNote, nil
}

// renderClaudeSettingsEnv 生成 settings.json 的 env 块载荷（Render 会把它深合并进
// 用户磁盘上的 settings.json）。每次调用都新建 map 返回，深合并时子 map 不与其他
// 调用共享引用（规避 mergeQuickSetupJSONInto 的子 map 引用共享契约）。env 用
// map[string]interface{} 而非 map[string]string：只有这样深合并才会逐键并入用户
// 既有 env 块（json.Unmarshal 出的子对象是 map[string]interface{}），整块替换会丢
// 用户自定义变量。用 ANTHROPIC_AUTH_TOKEN（Bearer 语义），不用 ANTHROPIC_API_KEY；
// baseURL 不带 /v1（Claude Code 自行追加 /v1/messages——相对旧 env.sh 渲染器是行为
// 变更，spec §7.1）。
func renderClaudeSettingsEnv(apiKey, model, baseURLNoV1 string) map[string]interface{} {
	return map[string]interface{}{
		"env": map[string]interface{}{
			"ANTHROPIC_BASE_URL":   baseURLNoV1,
			"ANTHROPIC_AUTH_TOKEN": apiKey,
			"ANTHROPIC_MODEL":      model,
		},
	}
}

func renderClaudeCodeFiles(software models.QuickSetupSoftware, apiKey models.QuickSetupAPIKey, apiRoot string, home string) ([]models.QuickSetupPreviewFile, []string, error) {
	fileDef := software.Files[0]
	model := quickSetupDefaultModel(apiKey.Provider, false)
	// apiRoot 已是推理面根（不带 /v1）；再过一次 resolve 保证即使上层传入控制面
	// URL 也落在推理面。Claude Code 自行追加 /v1/messages，这里不能带 /v1；
	// TrimSuffix 兜底剥掉配置尾缀的 /v1（只剥一次且只剥结尾），避免 /v1/v1/messages。
	baseURL := strings.TrimSuffix(resolveQuickSetupInferenceBaseURL(apiRoot), "/v1")
	payload := renderClaudeSettingsEnv(apiKey.Key, model, baseURL)

	merged, mergedFromDisk, degradedNote, err := quickSetupMergedJSONObject(software.Code, quickSetupLeafFileName(fileDef.DefaultPath), fileDef.DefaultPath, home, payload)
	if err != nil {
		return nil, nil, err
	}

	notes := []string{
		"The gateway env block is written into your Claude Code settings.json, taking effect on the next Claude Code start.",
		"Uses ANTHROPIC_AUTH_TOKEN (Bearer auth) rather than ANTHROPIC_API_KEY.",
	}
	if degradedNote != "" {
		notes = append(notes, degradedNote)
	}
	// 单鉴权源（spec §7）：我们用 AUTH_TOKEN 接管鉴权，残留的 ANTHROPIC_API_KEY
	// 会造成双鉴权源歧义，合并后必须清除。
	if env, ok := merged["env"].(map[string]interface{}); ok {
		if _, had := env["ANTHROPIC_API_KEY"]; had {
			delete(env, "ANTHROPIC_API_KEY")
			notes = append(notes, "Removed ANTHROPIC_API_KEY from your existing settings so the gateway authenticates only via ANTHROPIC_AUTH_TOKEN.")
		}
	}
	raw, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		return nil, nil, err
	}

	if apiKey.Masked {
		notes = append(notes, "This API key looks masked. Replace it with the plaintext value before applying.")
	}

	return []models.QuickSetupPreviewFile{
		{
			Code:           fileDef.Code,
			Label:          fileDef.Label,
			Path:           fileDef.DefaultPath,
			Format:         fileDef.Format,
			Kind:           fileDef.Kind,
			Content:        string(raw),
			MergedFromDisk: mergedFromDisk,
		},
	}, notes, nil
}

func quickSetupProviderLabel(provider string) string {
	switch provider {
	case "anthropic":
		return "Anthropic"
	case "openai":
		return "OpenAI"
	default:
		return strings.ToUpper(provider)
	}
}

func quickSetupDefaultModel(provider string, codex bool) string {
	switch provider {
	case "anthropic":
		if codex {
			return "claude-sonnet-4-5"
		}
		return "claude-sonnet-4-5-20250929"
	case "openai":
		if codex {
			return "gpt-5-codex"
		}
		return "gpt-5.4"
	default:
		if codex {
			return "gpt-5-codex"
		}
		return "gpt-5.4"
	}
}

func quickSetupProviderBaseURL(provider string, apiRoot string) string {
	root := strings.TrimRight(strings.TrimSpace(apiRoot), "/")
	switch provider {
	case "anthropic", "openai":
		if strings.HasSuffix(root, "/v1") {
			return root
		}
		return root + "/v1"
	default:
		return root
	}
}

func quickSetupBaseURL() (string, error) {
	cfg := config.GetGlobalConfig()
	if cfg == nil {
		return "", errors.New("config not initialized")
	}
	baseURL := strings.TrimSpace(cfg.APIBaseURL())
	if baseURL == "" {
		return "", errors.New("config.core.api_server is required for quick setup")
	}
	return resolveQuickSetupInferenceBaseURL(baseURL), nil
}

func resolveQuickSetupInferenceBaseURL(baseURL string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	parsed, err := url.Parse(trimmed)
	if err != nil || !strings.EqualFold(parsed.Hostname(), quickSetupControlPlaneHost) {
		return trimmed
	}
	host := quickSetupInferenceHost
	if port := parsed.Port(); port != "" {
		host += ":" + port
	}
	parsed.Host = host
	return strings.TrimRight(parsed.String(), "/")
}

// quickSetupModeRoot 把接入模式换算成 host 根（spec §7.1）：
// local → 本地推理代理（引用 defaults 常量，禁止硬编码）；public/未知/空 → 推理面域名。
// apiRoot 非空由调用方（Render 的 quickSetupBaseURL 校验）保证，本函数不重复校验。
// local 用 loopback 常量在 --host 覆盖下是有意选择：客户端目标固定指向 loopback
// 更安全，且本地推理代理的 listener 本身拒绝非 loopback Host 的请求。
func quickSetupModeRoot(mode, apiRoot string) string {
	if strings.EqualFold(strings.TrimSpace(mode), "local") {
		return "http://" + config.DefaultHTTPProxyAddr
	}
	return resolveQuickSetupInferenceBaseURL(apiRoot)
}
