package services

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/common/cache"
	auth "aliang.one/nursorgate/processor/auth"
	"aliang.one/nursorgate/processor/config"
)

type QuickSetupService struct{}

var quickSetupGetAPIKeysFn = auth.GetUserAPIKeys

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

	return map[string]interface{}{
		"status": "success",
		"data": models.QuickSetupCatalogResponse{
			Softwares: quickSetupSoftwares(),
			APIKeys:   toQuickSetupAPIKeys(apiKeys),
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

	var variants []models.QuickSetupVariant
	for _, key := range toQuickSetupAPIKeys(apiKeys) {
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
			Description:        "Generate a ready-to-edit OpenCode config with your selected gateway API key.",
			SupportedProviders: []string{"openai", "anthropic"},
			Files: []models.QuickSetupSoftwareFile{
				{
					Code:        "config",
					Label:       "config.json",
					FileName:    "config.json",
					DefaultPath: "~/.config/opencode/config.json",
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

func toQuickSetupAPIKeys(apiKeys []auth.UserAPIKey) []models.QuickSetupAPIKey {
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

func renderQuickSetupFiles(software models.QuickSetupSoftware, apiKey models.QuickSetupAPIKey, apiRoot string) ([]models.QuickSetupPreviewFile, []string, error) {
	switch software.Code {
	case "opencode":
		return renderOpenCodeFiles(software, apiKey, apiRoot)
	case "codex":
		return renderCodexFiles(software, apiKey, apiRoot)
	case "claude-code":
		return renderClaudeCodeFiles(software, apiKey, apiRoot)
	default:
		return nil, nil, fmt.Errorf("unsupported software: %s", software.Code)
	}
}

func renderOpenCodeFiles(software models.QuickSetupSoftware, apiKey models.QuickSetupAPIKey, apiRoot string) ([]models.QuickSetupPreviewFile, []string, error) {
	fileDef := software.Files[0]
	providerKey := apiKey.Provider
	model := quickSetupDefaultModel(providerKey, false)
	config := map[string]interface{}{
		"$schema": "https://opencode.ai/config.json",
		"theme":   "system",
		"provider": map[string]interface{}{
			providerKey: map[string]interface{}{
				"id":   providerKey,
				"name": quickSetupProviderLabel(providerKey),
				"type": providerKey,
				"options": map[string]interface{}{
					"baseURL": quickSetupProviderBaseURL(providerKey, apiRoot),
					"apiKey":  apiKey.Key,
				},
			},
		},
		"model": map[string]interface{}{
			"default": fmt.Sprintf("%s/%s", providerKey, model),
		},
	}
	config["workspace"] = map[string]interface{}{
		"autoApply": false,
	}

	raw, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return nil, nil, err
	}

	notes := []string{
		fmt.Sprintf("Uses %s style requests against %s.", strings.ToUpper(providerKey), quickSetupProviderBaseURL(providerKey, apiRoot)),
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
			Content: string(raw),
		},
	}, notes, nil
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
