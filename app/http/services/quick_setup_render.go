package services

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
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

	keys := toQuickSetupAPIKeys(apiKeys, baseRoot)
	if softwareDef.Code == "opencode" {
		variants, err := renderOpenCodeVariants(softwareDef, keys, selectedIDs, req.OpenCode, baseRoot)
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

		files, notes, err := renderQuickSetupFiles(softwareDef, key, baseRoot)
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

func renderQuickSetupFiles(software models.QuickSetupSoftware, apiKey models.QuickSetupAPIKey, apiRoot string) ([]models.QuickSetupPreviewFile, []string, error) {
	switch software.Code {
	case "opencode":
		return renderOpenCodeFiles(software, []models.QuickSetupAPIKey{apiKey}, apiKey.ID, "", "", apiRoot)
	case "codex":
		return renderCodexFiles(software, apiKey, apiRoot)
	case "claude-code":
		return renderClaudeCodeFiles(software, apiKey, apiRoot)
	default:
		return nil, nil, fmt.Errorf("unsupported software: %s", software.Code)
	}
}

func renderOpenCodeVariants(software models.QuickSetupSoftware, keys []models.QuickSetupAPIKey, selectedIDs map[int64]struct{}, spec *models.OpenCodeRenderSpec, apiRoot string) ([]models.QuickSetupVariant, error) {
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
	files, notes, err := renderOpenCodeFiles(software, selected, modelKey.ID, model, smallModel, apiRoot)
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

func renderOpenCodeFiles(software models.QuickSetupSoftware, apiKeys []models.QuickSetupAPIKey, modelKeyID int64, modelOverride string, smallModelOverride string, apiRoot string) ([]models.QuickSetupPreviewFile, []string, error) {
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
	raw, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return nil, nil, err
	}

	return []models.QuickSetupPreviewFile{
		{
			Code:    fileDef.Code,
			Label:   fileDef.Label,
			Path:    fileDef.DefaultPath,
			Format:  fileDef.Format,
			Kind:    fileDef.Kind,
			Content: string(raw),
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

func renderCodexFiles(software models.QuickSetupSoftware, apiKey models.QuickSetupAPIKey, apiRoot string) ([]models.QuickSetupPreviewFile, []string, error) {
	files := make([]models.QuickSetupPreviewFile, 0, len(software.Files))
	providerKey := apiKey.Provider
	model := quickSetupDefaultModel(providerKey, true)
	configBody := renderCodexConfigTOML(providerKey, model, apiRoot)
	authBody := renderCodexAuthJSON(apiKey)

	for _, fileDef := range software.Files {
		content := configBody
		if fileDef.Code == "auth" {
			content = authBody
		}
		files = append(files, models.QuickSetupPreviewFile{
			Code:    fileDef.Code,
			Label:   fileDef.Label,
			Path:    fileDef.DefaultPath,
			Format:  fileDef.Format,
			Kind:    fileDef.Kind,
			Content: content,
		})
	}

	notes := []string{
		"Codex auth.json officially stores OPENAI_API_KEY for API-key sign-in.",
	}
	if providerKey != "openai" {
		notes = append(notes, "For non-openai providers, Codex still relies on a custom provider in config.toml. Verify your gateway can serve OpenAI Responses semantics for this key.")
	}
	if apiKey.Masked {
		notes = append(notes, "This API key looks masked. Replace it with the plaintext value before applying.")
	}
	return files, notes, nil
}

// renderClaudeSettingsEnv 生成 settings.json 的 env 块载荷（Task 9 将把它深合并进
// 用户磁盘上的 settings.json）。每次调用都新建 map 返回，深合并时子 map 不与其他
// 调用共享引用（规避 mergeQuickSetupJSONInto 的子 map 引用共享契约）。
// 用 ANTHROPIC_AUTH_TOKEN（Bearer 语义），不用 ANTHROPIC_API_KEY；baseURL 不带 /v1
// （Claude Code 自行追加 /v1/messages——相对旧 env.sh 渲染器是行为变更，spec §7.1）。
func renderClaudeSettingsEnv(apiKey, model, baseURLNoV1 string) map[string]interface{} {
	return map[string]interface{}{
		"env": map[string]string{
			"ANTHROPIC_BASE_URL":   baseURLNoV1,
			"ANTHROPIC_AUTH_TOKEN": apiKey,
			"ANTHROPIC_MODEL":      model,
		},
	}
}

func renderClaudeCodeFiles(software models.QuickSetupSoftware, apiKey models.QuickSetupAPIKey, apiRoot string) ([]models.QuickSetupPreviewFile, []string, error) {
	fileDef := software.Files[0]
	model := quickSetupDefaultModel(apiKey.Provider, false)
	// apiRoot 已是推理面根（不带 /v1）；再过一次 resolve 保证即使上层传入控制面
	// URL 也落在推理面。Claude Code 自行追加 /v1/messages，这里不能带 /v1；
	// TrimSuffix 兜底剥掉配置尾缀的 /v1（只剥一次且只剥结尾），避免 /v1/v1/messages。
	baseURL := strings.TrimSuffix(resolveQuickSetupInferenceBaseURL(apiRoot), "/v1")
	payload := renderClaudeSettingsEnv(apiKey.Key, model, baseURL)
	raw, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return nil, nil, err
	}
	content := string(raw)

	notes := []string{
		"The gateway env block is written into your Claude Code settings.json, taking effect on the next Claude Code start.",
		"Uses ANTHROPIC_AUTH_TOKEN (Bearer auth) rather than ANTHROPIC_API_KEY.",
	}
	if apiKey.Masked {
		notes = append(notes, "This API key looks masked. Replace it with the plaintext value before applying.")
	}

	return []models.QuickSetupPreviewFile{
		{
			Code:    fileDef.Code,
			Label:   fileDef.Label,
			Path:    fileDef.DefaultPath,
			Format:  fileDef.Format,
			Kind:    fileDef.Kind,
			Content: content,
		},
	}, notes, nil
}

func renderCodexConfigTOML(provider string, model string, apiRoot string) string {
	if provider == "openai" {
		return strings.Join([]string{
			fmt.Sprintf("model = %q", model),
			`model_provider = "openai"`,
			`approval_policy = "never"`,
			``,
			`[model_providers.openai]`,
			`name = "OpenAI"`,
			fmt.Sprintf("base_url = %q", quickSetupProviderBaseURL(provider, apiRoot)),
			`wire_api = "responses"`,
			``,
		}, "\n")
	}

	return strings.Join([]string{
		fmt.Sprintf("model = %q", model),
		`model_provider = "anthropic_gateway"`,
		`approval_policy = "never"`,
		``,
		`[model_providers.anthropic_gateway]`,
		`name = "Anthropic Gateway"`,
		fmt.Sprintf("base_url = %q", quickSetupProviderBaseURL("openai", apiRoot)),
		`env_key = "OPENAI_API_KEY"`,
		`wire_api = "responses"`,
		``,
	}, "\n")
}

func renderCodexAuthJSON(apiKey models.QuickSetupAPIKey) string {
	value := apiKey.Key
	if apiKey.Provider != "openai" && apiKey.Masked {
		value = ""
	}
	body := map[string]interface{}{
		"OPENAI_API_KEY": value,
	}
	raw, err := json.MarshalIndent(body, "", "  ")
	if err != nil {
		return "{\n  \"OPENAI_API_KEY\": \"\"\n}"
	}
	return string(raw)
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
