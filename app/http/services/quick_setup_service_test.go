package services

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aliang.one/nursorgate/app/http/models"
	auth "aliang.one/nursorgate/processor/auth"
	"aliang.one/nursorgate/processor/config"
)

func TestQuickSetupService_Catalog_Unauthenticated(t *testing.T) {
	previous := quickSetupGetAPIKeysFn
	quickSetupGetAPIKeysFn = func() ([]auth.UserAPIKey, error) {
		return nil, fmt.Errorf("no user session")
	}
	t.Cleanup(func() {
		quickSetupGetAPIKeysFn = previous
	})

	svc := NewQuickSetupService()
	result := svc.Catalog()
	if result["status"] != "unauthenticated" {
		t.Fatalf("expected unauthenticated status, got %#v", result["status"])
	}
}

func TestQuickSetupService_Models_FetchesFromSelectedKeyBaseURL(t *testing.T) {
	requestedPath := ""
	requestedAuth := ""
	modelResponse := `{
		"object": "list",
		"data": [
			{"id": "z-model", "owned_by": "aliang"},
			{"id": "a-model", "name": "A Model", "owned_by": "aliang"},
			{"id": "a-model", "name": "Duplicate"}
		]
	}`
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestedPath = r.URL.Path
		requestedAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(modelResponse))
	}))
	t.Cleanup(modelServer.Close)

	config.ResetGlobalConfigForTest()
	config.SetGlobalConfig(&config.Config{
		Core: &config.CoreConfig{APIServer: modelServer.URL},
	})
	t.Cleanup(config.ResetGlobalConfigForTest)

	previousKeys := quickSetupGetAPIKeysFn
	quickSetupGetAPIKeysFn = func() ([]auth.UserAPIKey, error) {
		return []auth.UserAPIKey{
			{ID: 11, Key: "sk-openai-real", Name: "OpenAI Key", Status: "active", Provider: "openai", SecretAvailable: true},
			{ID: 22, Key: "sk-ant-real", Name: "Anthropic Key", Status: "active", Provider: "anthropic", SecretAvailable: true},
			{ID: 33, Key: "sk-flag-false-real", Name: "Flag False Key", Status: "active", Provider: "openai", SecretAvailable: false},
		}, nil
	}
	t.Cleanup(func() {
		quickSetupGetAPIKeysFn = previousKeys
	})

	previousClient := quickSetupModelsHTTPClient
	quickSetupModelsHTTPClient = modelServer.Client()
	t.Cleanup(func() {
		quickSetupModelsHTTPClient = previousClient
	})

	resp, err := NewQuickSetupService().Models(models.QuickSetupModelsRequest{KeyID: 11})
	if err != nil {
		t.Fatalf("models failed: %v", err)
	}
	if requestedPath != "/v1/models" {
		t.Fatalf("requested path = %q, want /v1/models", requestedPath)
	}
	if requestedAuth != "Bearer sk-openai-real" {
		t.Fatalf("authorization = %q", requestedAuth)
	}
	if resp.BaseURL != modelServer.URL+"/v1" {
		t.Fatalf("base_url = %q, want %q", resp.BaseURL, modelServer.URL+"/v1")
	}
	if len(resp.Models) != 2 {
		t.Fatalf("models length = %d, want 2: %#v", len(resp.Models), resp.Models)
	}
	if resp.Models[0].ID != "a-model" || resp.Models[1].ID != "z-model" {
		t.Fatalf("models sorted/deduped incorrectly: %#v", resp.Models)
	}

	requestedPath = ""
	resp, err = NewQuickSetupService().Models(models.QuickSetupModelsRequest{KeyID: 22})
	if err != nil {
		t.Fatalf("anthropic models failed: %v", err)
	}
	if requestedPath != "/v1/models" {
		t.Fatalf("anthropic requested path = %q, want /v1/models", requestedPath)
	}
	if resp.BaseURL != modelServer.URL+"/v1" {
		t.Fatalf("anthropic model list base_url = %q, want %q", resp.BaseURL, modelServer.URL+"/v1")
	}

	requestedAuth = ""
	resp, err = NewQuickSetupService().Models(models.QuickSetupModelsRequest{KeyID: 33})
	if err != nil {
		t.Fatalf("plain key with false secret flag failed: %v", err)
	}
	if requestedAuth != "Bearer sk-flag-false-real" {
		t.Fatalf("plain key with false secret flag authorization = %q", requestedAuth)
	}

	modelResponse = `{
		"data": {
			"models": [
				{"model": "wrapped-b", "display_name": "Wrapped B"},
				{"name": "wrapped-a"}
			]
		}
	}`
	resp, err = NewQuickSetupService().Models(models.QuickSetupModelsRequest{KeyID: 11})
	if err != nil {
		t.Fatalf("wrapped models failed: %v", err)
	}
	if len(resp.Models) != 2 {
		t.Fatalf("wrapped models length = %d, want 2: %#v", len(resp.Models), resp.Models)
	}
	if resp.Models[0].ID != "wrapped-a" || resp.Models[1].ID != "wrapped-b" {
		t.Fatalf("wrapped models parsed/sorted incorrectly: %#v", resp.Models)
	}
}

func TestQuickSetupService_Render_OpenCodeCombinedProviders(t *testing.T) {
	config.ResetGlobalConfigForTest()
	config.SetGlobalConfig(&config.Config{
		Core: &config.CoreConfig{APIServer: "https://api.example.com"},
	})
	t.Cleanup(config.ResetGlobalConfigForTest)

	previous := quickSetupGetAPIKeysFn
	quickSetupGetAPIKeysFn = func() ([]auth.UserAPIKey, error) {
		return []auth.UserAPIKey{
			{ID: 11, Key: "sk-openai-real", Name: "OpenAI Key", Status: "active", Provider: "openai", SecretAvailable: true},
			{ID: 22, Key: "sk-ant-real", Name: "Anthropic Key", Status: "active", Provider: "anthropic", SecretAvailable: true},
		}, nil
	}
	t.Cleanup(func() {
		quickSetupGetAPIKeysFn = previous
	})

	svc := NewQuickSetupService()
	resp, err := svc.Render(models.QuickSetupRenderRequest{
		Software: "opencode",
		KeyIDs:   []int64{11, 22},
		OpenCode: &models.OpenCodeRenderSpec{
			ModelKeyID:  22,
			Model:       "claude-sonnet-4-5",
			ProviderIDs: []int64{11, 22},
		},
	})
	if err != nil {
		t.Fatalf("render opencode failed: %v", err)
	}
	if len(resp.Variants) != 1 {
		t.Fatalf("expected combined variant, got %d", len(resp.Variants))
	}
	if len(resp.Variants[0].APIKeys) != 2 {
		t.Fatalf("expected 2 API keys in variant, got %d", len(resp.Variants[0].APIKeys))
	}

	var cfg map[string]interface{}
	if err := json.Unmarshal([]byte(resp.Variants[0].Files[0].Content), &cfg); err != nil {
		t.Fatalf("decode opencode config: %v\n%s", err, resp.Variants[0].Files[0].Content)
	}
	model, _ := cfg["model"].(string)
	if !strings.HasSuffix(model, "/claude-sonnet-4-5") {
		t.Fatalf("model = %q, want selected anthropic provider model", model)
	}
	if _, ok := cfg["model"].(map[string]interface{}); ok {
		t.Fatalf("model must be a provider/model string, got object")
	}
	providers, ok := cfg["provider"].(map[string]interface{})
	if !ok || len(providers) != 2 {
		t.Fatalf("providers = %#v, want 2 entries", cfg["provider"])
	}
	baseURLs := map[string]bool{}
	for id, raw := range providers {
		provider, _ := raw.(map[string]interface{})
		options, _ := provider["options"].(map[string]interface{})
		baseURLs[fmt.Sprint(options["baseURL"])] = true
		if options["apiKey"] == "" {
			t.Fatalf("%s apiKey empty", id)
		}
		wantNPM := "@ai-sdk/openai-compatible"
		if strings.Contains(id, "anthropic") {
			wantNPM = "@ai-sdk/anthropic"
		}
		if provider["npm"] != wantNPM {
			t.Fatalf("%s npm = %#v", id, provider["npm"])
		}
	}
	for _, want := range []string{"https://api.example.com/v1", "https://api.example.com"} {
		if !baseURLs[want] {
			t.Fatalf("missing baseURL %s in %#v", want, baseURLs)
		}
	}
}

func TestQuickSetupService_Render_OpenCodeDefaultsToProviderAwareGateway(t *testing.T) {
	config.ResetGlobalConfigForTest()
	config.SetGlobalConfig(&config.Config{
		Core: &config.CoreConfig{APIServer: "https://api.example.com"},
	})
	t.Cleanup(config.ResetGlobalConfigForTest)

	previous := quickSetupGetAPIKeysFn
	quickSetupGetAPIKeysFn = func() ([]auth.UserAPIKey, error) {
		return []auth.UserAPIKey{
			{ID: 22, Key: "sk-ant-real", Name: "Anthropic Key", Status: "active", Provider: "anthropic", SecretAvailable: true},
		}, nil
	}
	t.Cleanup(func() {
		quickSetupGetAPIKeysFn = previous
	})

	svc := NewQuickSetupService()
	resp, err := svc.Render(models.QuickSetupRenderRequest{
		Software: "opencode",
		KeyIDs:   []int64{22},
		OpenCode: &models.OpenCodeRenderSpec{
			ModelKeyID: 22,
			Model:      "custom-claude-compatible",
			SmallModel: "custom-small-compatible",
		},
	})
	if err != nil {
		t.Fatalf("render opencode failed: %v", err)
	}

	var cfg map[string]interface{}
	if err := json.Unmarshal([]byte(resp.Variants[0].Files[0].Content), &cfg); err != nil {
		t.Fatalf("decode opencode config: %v\n%s", err, resp.Variants[0].Files[0].Content)
	}
	providers := cfg["provider"].(map[string]interface{})
	for id, raw := range providers {
		provider := raw.(map[string]interface{})
		options := provider["options"].(map[string]interface{})
		if options["baseURL"] != "https://api.example.com" {
			t.Fatalf("%s default baseURL = %#v, want Anthropic gateway root", id, options["baseURL"])
		}
		if provider["npm"] != "@ai-sdk/anthropic" {
			t.Fatalf("%s npm = %#v, want Anthropic provider package", id, provider["npm"])
		}
		models := provider["models"].(map[string]interface{})
		if _, ok := models["custom-claude-compatible"]; !ok {
			t.Fatalf("%s models missing custom default model: %#v", id, models)
		}
		if _, ok := models["custom-small-compatible"]; !ok {
			t.Fatalf("%s models missing custom small model: %#v", id, models)
		}
	}
	if !strings.HasSuffix(fmt.Sprint(cfg["small_model"]), "/custom-small-compatible") {
		t.Fatalf("small_model = %#v", cfg["small_model"])
	}
}

func TestQuickSetupService_Catalog_IncludesProviderAwareBaseURLs(t *testing.T) {
	config.ResetGlobalConfigForTest()
	config.SetGlobalConfig(&config.Config{
		Core: &config.CoreConfig{APIServer: "https://api.example.com"},
	})
	t.Cleanup(config.ResetGlobalConfigForTest)

	previous := quickSetupGetAPIKeysFn
	quickSetupGetAPIKeysFn = func() ([]auth.UserAPIKey, error) {
		return []auth.UserAPIKey{
			{ID: 11, Key: "sk-openai-real", Name: "OpenAI Key", Status: "active", Provider: "openai", SecretAvailable: true},
			{ID: 22, Key: "sk-ant-real", Name: "Anthropic Key", Status: "active", Provider: "anthropic", SecretAvailable: true},
		}, nil
	}
	t.Cleanup(func() {
		quickSetupGetAPIKeysFn = previous
	})

	result := NewQuickSetupService().Catalog()
	data := result["data"].(models.QuickSetupCatalogResponse)
	baseURLs := map[string]string{}
	for _, key := range data.APIKeys {
		baseURLs[key.Provider] = key.BaseURL
	}
	if baseURLs["openai"] != "https://api.example.com/v1" {
		t.Fatalf("openai base_url = %q", baseURLs["openai"])
	}
	if baseURLs["anthropic"] != "https://api.example.com" {
		t.Fatalf("anthropic base_url = %q", baseURLs["anthropic"])
	}
}

func TestQuickSetupService_Render_MultiProvider(t *testing.T) {
	config.ResetGlobalConfigForTest()
	config.SetGlobalConfig(&config.Config{
		Core: &config.CoreConfig{APIServer: "https://api.example.com"},
	})
	t.Cleanup(config.ResetGlobalConfigForTest)

	previous := quickSetupGetAPIKeysFn
	quickSetupGetAPIKeysFn = func() ([]auth.UserAPIKey, error) {
		openaiGroupID := int64(1)
		anthropicGroupID := int64(2)
		return []auth.UserAPIKey{
			{
				ID:              11,
				Key:             "sk-openai-real",
				Name:            "OpenAI Key",
				GroupID:         &openaiGroupID,
				Status:          "active",
				Provider:        "openai",
				Masked:          false,
				SecretAvailable: true,
				Group: &auth.APIKeyGroup{
					ID:       1,
					Name:     "OpenAI Group",
					Platform: "openai",
				},
			},
			{
				ID:              22,
				Key:             "sk-ant-real",
				Name:            "Anthropic Key",
				GroupID:         &anthropicGroupID,
				Status:          "active",
				Provider:        "anthropic",
				Masked:          false,
				SecretAvailable: true,
				Group: &auth.APIKeyGroup{
					ID:       2,
					Name:     "Anthropic Group",
					Platform: "anthropic",
				},
			},
		}, nil
	}
	t.Cleanup(func() {
		quickSetupGetAPIKeysFn = previous
	})

	svc := NewQuickSetupService()
	resp, err := svc.Render(models.QuickSetupRenderRequest{Software: "codex"})
	if err != nil {
		t.Fatalf("render failed: %v", err)
	}
	if len(resp.Variants) != 2 {
		t.Fatalf("expected 2 variants, got %d", len(resp.Variants))
	}

	if !strings.Contains(resp.Variants[0].Files[0].Content, "model_provider") {
		t.Fatalf("expected codex config.toml content, got %q", resp.Variants[0].Files[0].Content)
	}
	if !strings.Contains(resp.Variants[0].Files[1].Content, "OPENAI_API_KEY") {
		t.Fatalf("expected auth.json content, got %q", resp.Variants[0].Files[1].Content)
	}
}

func TestQuickSetupService_Apply_WritesFiles(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	svc := NewQuickSetupService()
	resp, err := svc.Apply(models.QuickSetupApplyRequest{
		Software: "codex",
		Files: []models.QuickSetupApplyFile{
			{
				Path:    "~/.codex/config.toml",
				Content: "model = \"gpt-5-codex\"\n",
				Kind:    "file",
			},
			{
				Path:    "~/.codex/auth.json",
				Content: "{\"OPENAI_API_KEY\":\"sk-test\"}",
				Kind:    "file",
			},
		},
	})
	if err != nil {
		t.Fatalf("apply failed: %v", err)
	}
	if len(resp.Written) != 2 {
		t.Fatalf("expected 2 written files, got %d", len(resp.Written))
	}

	configPath := filepath.Join(tempHome, ".codex", "config.toml")
	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read written config failed: %v", err)
	}
	if !strings.Contains(string(content), "gpt-5-codex") {
		t.Fatalf("unexpected config content: %s", string(content))
	}
}
