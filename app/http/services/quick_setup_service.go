package services

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/common/cache"
	auth "aliang.one/nursorgate/processor/auth"
	"aliang.one/nursorgate/processor/config"
)

type QuickSetupService struct{}

var quickSetupGetAPIKeysFn = auth.GetUserAPIKeys
var quickSetupModelsHTTPClient = &http.Client{Timeout: 12 * time.Second}

func NewQuickSetupService() *QuickSetupService {
	return &QuickSetupService{}
}

func (s *QuickSetupService) Catalog() map[string]interface{} {
	apiKeys, err := quickSetupGetAPIKeysFn()
	if err != nil {
		if isSessionMissingError(err) {
			return map[string]interface{}{
				"status": "unauthenticated",
				"error":  "session_missing",
				"msg":    "No authenticated session found",
			}
		}
		return map[string]interface{}{
			"status": "failed",
			"error":  "quick_setup_catalog_failed",
			"msg":    fmt.Sprintf("Failed to load quick setup catalog: %v", err),
		}
	}

	baseRoot, _ := quickSetupBaseURL()
	return map[string]interface{}{
		"status": "success",
		"data": models.QuickSetupCatalogResponse{
			Softwares: quickSetupSoftwares(),
			APIKeys:   toQuickSetupAPIKeys(apiKeys, baseRoot),
		},
	}
}

func (s *QuickSetupService) Render(req models.QuickSetupRenderRequest) (*models.QuickSetupRenderResponse, error) {
	software := strings.TrimSpace(req.Software)
	if software == "" {
		return nil, errors.New("software is required")
	}

	softwareDef, ok := findQuickSetupSoftware(software)
	if !ok {
		return nil, fmt.Errorf("unsupported software: %s", software)
	}

	baseRoot, err := quickSetupBaseURL()
	if err != nil {
		return nil, err
	}

	apiKeys, err := quickSetupGetAPIKeysFn()
	if err != nil {
		return nil, err
	}

	selectedIDs := make(map[int64]struct{}, len(req.KeyIDs))
	for _, id := range req.KeyIDs {
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
	for _, key := range keys {
		if len(selectedIDs) > 0 {
			if _, ok := selectedIDs[key.ID]; !ok {
				continue
			}
		}
		if !softwareSupportsProvider(softwareDef, key.Provider) {
			continue
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

func (s *QuickSetupService) Models(req models.QuickSetupModelsRequest) (*models.QuickSetupModelsResponse, error) {
	if req.KeyID == 0 {
		return nil, errors.New("key_id is required")
	}

	baseRoot, err := quickSetupBaseURL()
	if err != nil {
		return nil, err
	}

	apiKeys, err := quickSetupGetAPIKeysFn()
	if err != nil {
		return nil, err
	}

	keys := toQuickSetupAPIKeys(apiKeys, baseRoot)
	var selected *models.QuickSetupAPIKey
	for i := range keys {
		if keys[i].ID == req.KeyID {
			selected = &keys[i]
			break
		}
	}
	if selected == nil {
		return nil, fmt.Errorf("api key not found: %d", req.KeyID)
	}
	if !quickSetupAPIKeyHasPlainSecret(*selected) {
		return nil, errors.New("selected API key secret is not available")
	}

	modelListBaseURL := quickSetupModelListBaseURL(baseRoot)
	modelsList, err := fetchQuickSetupModels(modelListBaseURL, selected.Key)
	if err != nil {
		return nil, err
	}

	return &models.QuickSetupModelsResponse{
		KeyID:    selected.ID,
		Provider: selected.Provider,
		BaseURL:  modelListBaseURL,
		Models:   modelsList,
	}, nil
}

func (s *QuickSetupService) Apply(req models.QuickSetupApplyRequest) (*models.QuickSetupApplyResponse, error) {
	software := strings.TrimSpace(req.Software)
	if software == "" {
		return nil, errors.New("software is required")
	}
	if len(req.Files) == 0 {
		return nil, errors.New("files are required")
	}

	written := make([]string, 0, len(req.Files))
	for _, file := range req.Files {
		targetPath := strings.TrimSpace(file.Path)
		if targetPath == "" {
			return nil, errors.New("file path is required")
		}

		resolvedPath, err := expandQuickSetupPath(targetPath)
		if err != nil {
			return nil, err
		}

		if err := writeConfigFile(resolvedPath, file.Content); err != nil {
			return nil, err
		}
		written = append(written, resolvedPath)
	}

	return &models.QuickSetupApplyResponse{
		Software: software,
		Written:  written,
	}, nil
}

func quickSetupSoftwares() []models.QuickSetupSoftware {
	return []models.QuickSetupSoftware{
		{
			Code:               "opencode",
			Name:               "OpenCode",
			Description:        "Generate a ready-to-edit OpenCode config with selected gateway provider combinations.",
			SupportedProviders: []string{"openai", "anthropic"},
			Files: []models.QuickSetupSoftwareFile{
				{
					Code:        "config",
					Label:       "opencode.json",
					FileName:    "opencode.json",
					DefaultPath: "~/.config/opencode/opencode.json",
					Format:      "json",
					Kind:        "file",
					Description: "Main OpenCode runtime configuration.",
				},
			},
		},
		{
			Code:               "codex",
			Name:               "Codex",
			Description:        "Prepare Codex config.toml plus auth.json so the CLI can start with your chosen provider.",
			SupportedProviders: []string{"openai", "anthropic"},
			Files: []models.QuickSetupSoftwareFile{
				{
					Code:        "config",
					Label:       "config.toml",
					FileName:    "config.toml",
					DefaultPath: "~/.codex/config.toml",
					Format:      "toml",
					Kind:        "file",
					Description: "Codex CLI configuration.",
				},
				{
					Code:        "auth",
					Label:       "auth.json",
					FileName:    "auth.json",
					DefaultPath: "~/.codex/auth.json",
					Format:      "json",
					Kind:        "file",
					Description: "Codex CLI auth cache for API-key sign-in.",
				},
			},
		},
		{
			Code:               "claude-code",
			Name:               "Claude Code",
			Description:        "Generate a shell snippet for ANTHROPIC_* environment variables plus a local helper script.",
			SupportedProviders: []string{"anthropic"},
			Files: []models.QuickSetupSoftwareFile{
				{
					Code:        "command",
					Label:       "env.sh",
					FileName:    "env.sh",
					DefaultPath: "~/.claude-code/env.sh",
					Format:      "shell",
					Kind:        "file",
					Description: "Shell snippet to export the gateway base URL and API key.",
				},
			},
		},
	}
}

func findQuickSetupSoftware(code string) (models.QuickSetupSoftware, bool) {
	normalized := strings.ToLower(strings.TrimSpace(code))
	for _, software := range quickSetupSoftwares() {
		if software.Code == normalized {
			return software, true
		}
	}
	return models.QuickSetupSoftware{}, false
}

func softwareSupportsProvider(software models.QuickSetupSoftware, provider string) bool {
	for _, candidate := range software.SupportedProviders {
		if candidate == provider {
			return true
		}
	}
	return false
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

func quickSetupLooksMaskedAPIKey(value string) bool {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return false
	}
	return strings.Contains(trimmed, "***") || strings.Contains(trimmed, "…") || strings.Contains(trimmed, "...")
}

func renderQuickSetupFiles(software models.QuickSetupSoftware, apiKey models.QuickSetupAPIKey, apiRoot string) ([]models.QuickSetupPreviewFile, []string, error) {
	switch software.Code {
	case "opencode":
		return renderOpenCodeFiles(software, []models.QuickSetupAPIKey{apiKey}, apiKey.ID, "", "", "", nil, apiRoot)
	case "codex":
		return renderCodexFiles(software, apiKey, apiRoot)
	case "claude-code":
		return renderClaudeCodeFiles(software, apiKey, apiRoot)
	default:
		return nil, nil, fmt.Errorf("unsupported software: %s", software.Code)
	}
}

func renderOpenCodeVariants(software models.QuickSetupSoftware, keys []models.QuickSetupAPIKey, selectedIDs map[int64]struct{}, spec *models.OpenCodeRenderSpec, apiRoot string) ([]models.QuickSetupVariant, error) {
	providerIDs := make(map[int64]struct{}, len(selectedIDs)+len(specProviderIDs(spec)))
	for id := range selectedIDs {
		providerIDs[id] = struct{}{}
	}
	for _, id := range specProviderIDs(spec) {
		providerIDs[id] = struct{}{}
	}

	var selected []models.QuickSetupAPIKey
	for _, key := range keys {
		if !softwareSupportsProvider(software, key.Provider) {
			continue
		}
		if len(providerIDs) > 0 {
			if _, ok := providerIDs[key.ID]; !ok {
				continue
			}
		}
		selected = append(selected, key)
	}
	if len(selected) == 0 {
		return nil, nil
	}

	modelKeyID := int64(0)
	modelProvider := ""
	model := ""
	smallModel := ""
	baseURL := ""
	baseURLOverrides := map[int64]string{}
	if spec != nil {
		modelKeyID = spec.ModelKeyID
		modelProvider = strings.TrimSpace(spec.ModelProvider)
		model = strings.TrimSpace(spec.Model)
		smallModel = strings.TrimSpace(spec.SmallModel)
		baseURL = strings.TrimSpace(spec.BaseURL)
		for _, provider := range spec.Providers {
			if provider.KeyID == 0 {
				continue
			}
			baseURLOverrides[provider.KeyID] = strings.TrimSpace(provider.BaseURL)
		}
	}
	modelKey := selectOpenCodeModelKey(selected, modelKeyID, modelProvider)
	files, notes, err := renderOpenCodeFiles(software, selected, modelKey.ID, model, smallModel, baseURL, baseURLOverrides, apiRoot)
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

func specProviderIDs(spec *models.OpenCodeRenderSpec) []int64 {
	if spec == nil {
		return nil
	}
	ids := make([]int64, 0, len(spec.ProviderIDs)+len(spec.Providers))
	ids = append(ids, spec.ProviderIDs...)
	for _, provider := range spec.Providers {
		if provider.KeyID != 0 {
			ids = append(ids, provider.KeyID)
		}
	}
	return ids
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

func renderOpenCodeFiles(software models.QuickSetupSoftware, apiKeys []models.QuickSetupAPIKey, modelKeyID int64, modelOverride string, smallModelOverride string, baseURLOverride string, baseURLOverrides map[int64]string, apiRoot string) ([]models.QuickSetupPreviewFile, []string, error) {
	fileDef := software.Files[0]
	if len(apiKeys) == 0 {
		return nil, nil, errors.New("at least one API key is required for OpenCode")
	}
	providers := make(map[string]interface{}, len(apiKeys))
	seen := map[string]int{}
	modelProviderID := ""
	modelName := ""
	notes := []string{
		"OpenCode config uses provider/model strings such as aliang-openai-openai-key/gpt-5.",
		"Each selected API key becomes one provider entry, so multiple baseURL/API key combinations can coexist.",
	}
	for _, apiKey := range apiKeys {
		providerID := quickSetupOpenCodeProviderID(apiKey, seen)
		baseURL := quickSetupOpenCodeBaseURL(apiKey.Provider, firstNonEmptyQuickSetupString(baseURLOverrides[apiKey.ID], baseURLOverride), apiRoot)
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
		modelName = "gpt-5"
	}
	ensureOpenCodeProviderModel(providers, modelProviderID, modelName)
	defaultModel := fmt.Sprintf("%s/%s", modelProviderID, modelName)
	config := map[string]interface{}{
		"$schema":  "https://opencode.ai/config.json",
		"theme":    "system",
		"provider": providers,
		"model":    defaultModel,
	}
	if strings.TrimSpace(smallModelOverride) != "" {
		smallModelName := strings.TrimSpace(smallModelOverride)
		ensureOpenCodeProviderModel(providers, modelProviderID, smallModelName)
		config["small_model"] = fmt.Sprintf("%s/%s", modelProviderID, smallModelName)
	}
	config["workspace"] = map[string]interface{}{
		"autoApply": false,
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
			"claude-sonnet-4-5": map[string]interface{}{"name": "Claude Sonnet 4.5"},
			"claude-opus-4-1":   map[string]interface{}{"name": "Claude Opus 4.1"},
		}
	default:
		return map[string]interface{}{
			"gpt-5": map[string]interface{}{"name": "GPT-5"},
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

func quickSetupOpenCodeBaseURL(provider string, override string, apiRoot string) string {
	if trimmed := strings.TrimSpace(override); trimmed != "" {
		return strings.TrimRight(trimmed, "/")
	}
	return quickSetupProviderBaseURL(provider, apiRoot)
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

func renderClaudeCodeFiles(software models.QuickSetupSoftware, apiKey models.QuickSetupAPIKey, apiRoot string) ([]models.QuickSetupPreviewFile, []string, error) {
	fileDef := software.Files[0]
	model := quickSetupDefaultModel(apiKey.Provider, false)
	content := strings.Join([]string{
		"#!/usr/bin/env bash",
		fmt.Sprintf("export ANTHROPIC_BASE_URL=%q", quickSetupProviderBaseURL(apiKey.Provider, apiRoot)),
		fmt.Sprintf("export ANTHROPIC_API_KEY=%q", apiKey.Key),
		fmt.Sprintf("export ANTHROPIC_MODEL=%q", model),
		"",
		"# Run this before starting Claude Code:",
		"# source ~/.claude-code/env.sh",
	}, "\n")

	notes := []string{
		"Claude Code uses ANTHROPIC_* environment variables. The generated script is ready to source in your shell.",
	}
	if apiKey.Provider == "openai" {
		notes = append(notes, "This variant points Claude Code at your gateway base URL. Confirm your gateway accepts Anthropic-style /v1/messages traffic for this key.")
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
		return "claude-sonnet-4-5"
	case "openai":
		if codex {
			return "gpt-5-codex"
		}
		return "gpt-5"
	default:
		if codex {
			return "gpt-5-codex"
		}
		return "gpt-5"
	}
}

func quickSetupProviderBaseURL(provider string, apiRoot string) string {
	root := strings.TrimRight(strings.TrimSpace(apiRoot), "/")
	switch provider {
	case "anthropic":
		return root
	case "openai":
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
	return strings.TrimRight(baseURL, "/"), nil
}

func expandQuickSetupPath(path string) (string, error) {
	expanded, err := cache.ExpandHomePath(path)
	if err == nil {
		return expanded, nil
	}
	return filepath.Clean(path), nil
}
