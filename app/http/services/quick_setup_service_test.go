package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

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

func TestQuickSetupService_RenderAndModelsReturnUnauthenticatedError(t *testing.T) {
	config.ResetGlobalConfigForTest()
	config.SetGlobalConfig(&config.Config{Core: &config.CoreConfig{APIServer: "https://api.example.com"}})
	t.Cleanup(config.ResetGlobalConfigForTest)

	previous := quickSetupGetAPIKeysFn
	quickSetupGetAPIKeysFn = func() ([]auth.UserAPIKey, error) {
		return nil, fmt.Errorf("no user session")
	}
	t.Cleanup(func() { quickSetupGetAPIKeysFn = previous })

	if _, err := NewQuickSetupService().Render(models.QuickSetupRenderRequest{Software: "opencode", KeyIDs: []int64{1}}); !errors.Is(err, ErrQuickSetupUnauthenticated) {
		t.Fatalf("Render() error = %v, want ErrQuickSetupUnauthenticated", err)
	}
	if _, err := NewQuickSetupService().Models(models.QuickSetupModelsRequest{KeyID: 1}); !errors.Is(err, ErrQuickSetupUnauthenticated) {
		t.Fatalf("Models() error = %v, want ErrQuickSetupUnauthenticated", err)
	}
}

func TestQuickSetupService_Catalog_FailsWithoutAPIBaseURL(t *testing.T) {
	config.ResetGlobalConfigForTest()
	t.Cleanup(config.ResetGlobalConfigForTest)

	previous := quickSetupGetAPIKeysFn
	quickSetupGetAPIKeysFn = func() ([]auth.UserAPIKey, error) { return nil, nil }
	t.Cleanup(func() { quickSetupGetAPIKeysFn = previous })

	result := NewQuickSetupService().Catalog()
	if result["status"] != "failed" {
		t.Fatalf("Catalog() status = %#v, want failed", result["status"])
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
			ModelKeyID: 22,
			Model:      "claude-sonnet-4-5",
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
	if _, ok := cfg["workspace"]; ok {
		t.Fatalf("workspace is not supported by the OpenCode schema: %#v", cfg["workspace"])
	}
	if _, ok := cfg["theme"]; ok {
		t.Fatalf("theme is not supported by the current OpenCode schema: %#v", cfg["theme"])
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
	if want := "https://api.example.com/v1"; !baseURLs[want] {
		t.Fatalf("missing baseURL %s in %#v", want, baseURLs)
	}
}

func TestQuickSetupService_Render_OpenCodeUsesProductionInferenceBaseURL(t *testing.T) {
	config.ResetGlobalConfigForTest()
	config.SetGlobalConfig(&config.Config{
		Core: &config.CoreConfig{APIServer: "https://backend.aliang.one"},
	})
	t.Cleanup(config.ResetGlobalConfigForTest)

	previous := quickSetupGetAPIKeysFn
	quickSetupGetAPIKeysFn = func() ([]auth.UserAPIKey, error) {
		return []auth.UserAPIKey{
			{ID: 11, Key: "sk-openai-real", Name: "OpenAI Key", Status: "active", Provider: "openai", SecretAvailable: true},
		}, nil
	}
	t.Cleanup(func() {
		quickSetupGetAPIKeysFn = previous
	})

	svc := NewQuickSetupService()
	resp, err := svc.Render(models.QuickSetupRenderRequest{
		Software: "opencode",
		KeyIDs:   []int64{11},
		OpenCode: &models.OpenCodeRenderSpec{
			ModelKeyID: 11,
			Model:      "gpt-5.4",
		},
	})
	if err != nil {
		t.Fatalf("render opencode failed: %v", err)
	}

	var cfg map[string]interface{}
	if err := json.Unmarshal([]byte(resp.Variants[0].Files[0].Content), &cfg); err != nil {
		t.Fatalf("decode opencode config: %v", err)
	}
	providers := cfg["provider"].(map[string]interface{})
	for id, raw := range providers {
		provider := raw.(map[string]interface{})
		options := provider["options"].(map[string]interface{})
		if got, want := options["baseURL"], "https://api.aliang.one/v1"; got != want {
			t.Fatalf("%s baseURL = %#v, want %q", id, got, want)
		}
	}
}

func TestResolveQuickSetupInferenceBaseURL_OnlyMapsProductionControlPlane(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "production", in: "https://backend.aliang.one", want: "https://api.aliang.one"},
		{name: "production trailing slash", in: "https://backend.aliang.one/", want: "https://api.aliang.one"},
		{name: "production path", in: "https://backend.aliang.one/gateway", want: "https://api.aliang.one/gateway"},
		{name: "already inference", in: "https://api.aliang.one", want: "https://api.aliang.one"},
		{name: "custom deployment", in: "https://models.example.com", want: "https://models.example.com"},
		{name: "local test server", in: "http://127.0.0.1:18080", want: "http://127.0.0.1:18080"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveQuickSetupInferenceBaseURL(tt.in); got != tt.want {
				t.Fatalf("resolveQuickSetupInferenceBaseURL(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestQuickSetupService_Render_OpenCodeGeneratedConfigWorksWithCLI(t *testing.T) {
	apiKey := strings.TrimSpace(os.Getenv("ALIANG_OPENCODE_TEST_API_KEY"))
	provider := strings.TrimSpace(os.Getenv("ALIANG_OPENCODE_TEST_PROVIDER"))
	model := strings.TrimSpace(os.Getenv("ALIANG_OPENCODE_TEST_MODEL"))
	if apiKey == "" || provider == "" || model == "" {
		t.Skip("set ALIANG_OPENCODE_TEST_API_KEY, ALIANG_OPENCODE_TEST_PROVIDER, and ALIANG_OPENCODE_TEST_MODEL to run")
	}
	opencodePath, err := exec.LookPath("opencode")
	if err != nil {
		t.Skip("opencode is not installed")
	}

	config.ResetGlobalConfigForTest()
	config.SetGlobalConfig(&config.Config{
		Core: &config.CoreConfig{APIServer: "https://backend.aliang.one"},
	})
	t.Cleanup(config.ResetGlobalConfigForTest)

	previous := quickSetupGetAPIKeysFn
	quickSetupGetAPIKeysFn = func() ([]auth.UserAPIKey, error) {
		return []auth.UserAPIKey{
			{ID: 11, Key: apiKey, Name: "CLI Integration Key", Status: "active", Provider: provider, SecretAvailable: true},
		}, nil
	}
	t.Cleanup(func() {
		quickSetupGetAPIKeysFn = previous
	})

	svc := NewQuickSetupService()
	resp, err := svc.Render(models.QuickSetupRenderRequest{
		Software: "opencode",
		KeyIDs:   []int64{11},
		OpenCode: &models.OpenCodeRenderSpec{ModelKeyID: 11, ModelProvider: provider, Model: model},
	})
	if err != nil {
		t.Fatalf("render opencode failed: %v", err)
	}
	content := resp.Variants[0].Files[0].Content
	var rendered map[string]interface{}
	if err := json.Unmarshal([]byte(content), &rendered); err != nil {
		t.Fatalf("decode rendered config: %v", err)
	}
	modelRef, _ := rendered["model"].(string)
	if modelRef == "" {
		t.Fatal("rendered config has no default model")
	}

	testHome := t.TempDir()
	previousAuth := quickSetupAuthorizationHeaderFn
	previousTargetUser := quickSetupTargetUserFn
	quickSetupAuthorizationHeaderFn = func() string { return "Bearer cli-integration" }
	quickSetupTargetUserFn = func() (quickSetupTargetUser, error) {
		return quickSetupTargetUser{homeDir: testHome}, nil
	}
	t.Cleanup(func() {
		quickSetupAuthorizationHeaderFn = previousAuth
		quickSetupTargetUserFn = previousTargetUser
	})
	applyResp, err := svc.Apply(models.QuickSetupApplyRequest{
		Software: "opencode",
		Files: []models.QuickSetupApplyFile{
			{Path: resp.Variants[0].Files[0].Path, Content: content, Format: "json", Kind: "file"},
		},
	})
	if err != nil {
		t.Fatalf("apply rendered opencode config failed: %v", err)
	}
	if len(applyResp.Written) != 1 {
		t.Fatalf("applied file count = %d, want 1", len(applyResp.Written))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, opencodePath, "run", "--pure", "--model", modelRef, "Reply with OPENCODE_QUICK_SETUP_OK only.")
	cmd.Dir = t.TempDir()
	cmd.Env = append(os.Environ(),
		"HOME="+testHome,
		"XDG_CONFIG_HOME="+filepath.Join(testHome, ".config"),
		"XDG_DATA_HOME="+filepath.Join(testHome, ".local", "share"),
		"XDG_CACHE_HOME="+filepath.Join(testHome, ".cache"),
	)
	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("opencode timed out: %v", ctx.Err())
	}
	if err != nil {
		t.Fatalf("opencode rejected or could not use generated config: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "OPENCODE_QUICK_SETUP_OK") {
		t.Fatalf("opencode response did not contain success marker: %s", output)
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
		if options["baseURL"] != "https://api.example.com/v1" {
			t.Fatalf("%s default baseURL = %#v, want versioned Anthropic gateway URL", id, options["baseURL"])
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

func TestQuickSetupDefaultModel_UsesVerifiedOpenCodeModelIDs(t *testing.T) {
	if got, want := quickSetupDefaultModel("openai", false), "gpt-5.4"; got != want {
		t.Fatalf("OpenCode OpenAI default model = %q, want %q", got, want)
	}
	if got, want := quickSetupDefaultModel("anthropic", false), "claude-sonnet-4-5-20250929"; got != want {
		t.Fatalf("OpenCode Anthropic default model = %q, want %q", got, want)
	}
	if got, want := quickSetupDefaultModel("openai", true), "gpt-5-codex"; got != want {
		t.Fatalf("Codex OpenAI default model changed = %q, want %q", got, want)
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
	if baseURLs["anthropic"] != "https://api.example.com/v1" {
		t.Fatalf("anthropic base_url = %q", baseURLs["anthropic"])
	}
}

func TestQuickSetupProviderBaseURL_UsesSingleVersionPrefix(t *testing.T) {
	for _, provider := range []string{"openai", "anthropic"} {
		for _, root := range []string{"https://api.example.com", "https://api.example.com/v1"} {
			if got := quickSetupProviderBaseURL(provider, root); got != "https://api.example.com/v1" {
				t.Fatalf("provider %s root %s = %q", provider, root, got)
			}
		}
	}
}

func TestQuickSetupService_Render_OpenCodeRejectsUnavailableSecrets(t *testing.T) {
	config.ResetGlobalConfigForTest()
	config.SetGlobalConfig(&config.Config{
		Core: &config.CoreConfig{APIServer: "https://api.example.com"},
	})
	t.Cleanup(config.ResetGlobalConfigForTest)

	previous := quickSetupGetAPIKeysFn
	t.Cleanup(func() {
		quickSetupGetAPIKeysFn = previous
	})

	for _, tc := range []struct {
		name string
		key  string
	}{
		{name: "empty", key: ""},
		{name: "masked", key: "sk-***-masked"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			quickSetupGetAPIKeysFn = func() ([]auth.UserAPIKey, error) {
				return []auth.UserAPIKey{
					{ID: 11, Key: tc.key, Name: "Unavailable Key", Status: "active", Provider: "openai"},
				}, nil
			}

			_, err := NewQuickSetupService().Render(models.QuickSetupRenderRequest{
				Software: "opencode",
				KeyIDs:   []int64{11},
			})
			if err == nil || !strings.Contains(err.Error(), "plaintext secret is required") {
				t.Fatalf("render error = %v, want plaintext secret validation", err)
			}
		})
	}
}

func TestQuickSetupService_Render_RequiresExplicitValidSelection(t *testing.T) {
	config.ResetGlobalConfigForTest()
	config.SetGlobalConfig(&config.Config{
		Core: &config.CoreConfig{APIServer: "https://api.example.com"},
	})
	t.Cleanup(config.ResetGlobalConfigForTest)

	previous := quickSetupGetAPIKeysFn
	quickSetupGetAPIKeysFn = func() ([]auth.UserAPIKey, error) {
		return []auth.UserAPIKey{
			{ID: 11, Key: "sk-openai-real", Name: "OpenAI Key", Status: "active", Provider: "openai"},
		}, nil
	}
	t.Cleanup(func() {
		quickSetupGetAPIKeysFn = previous
	})

	for _, req := range []models.QuickSetupRenderRequest{
		{Software: "opencode"},
		{Software: "opencode", KeyIDs: []int64{999}},
		{Software: "opencode", KeyIDs: []int64{11}, OpenCode: &models.OpenCodeRenderSpec{ModelKeyID: 999}},
	} {
		if _, err := NewQuickSetupService().Render(req); err == nil {
			t.Fatalf("Render(%+v) succeeded, want selection validation error", req)
		}
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
	resp, err := svc.Render(models.QuickSetupRenderRequest{Software: "codex", KeyIDs: []int64{11, 22}})
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
	previousAuth := quickSetupAuthorizationHeaderFn
	quickSetupAuthorizationHeaderFn = func() string { return "Bearer test-access" }
	t.Cleanup(func() { quickSetupAuthorizationHeaderFn = previousAuth })

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
	if runtime.GOOS != "windows" {
		info, err := os.Stat(configPath)
		if err != nil {
			t.Fatalf("stat written config failed: %v", err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("written config permissions = %o, want 600", got)
		}
	}
	entries, err := os.ReadDir(filepath.Dir(configPath))
	if err != nil {
		t.Fatalf("read config directory failed: %v", err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".tmp-") {
			t.Fatalf("atomic write left temporary file %q", entry.Name())
		}
	}
}

func TestQuickSetupService_Apply_RejectsUnauthenticatedAndUnsafePathsBeforeWriting(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)
	previousAuth := quickSetupAuthorizationHeaderFn
	quickSetupAuthorizationHeaderFn = func() string { return "" }
	t.Cleanup(func() { quickSetupAuthorizationHeaderFn = previousAuth })

	svc := NewQuickSetupService()
	request := models.QuickSetupApplyRequest{
		Software: "opencode",
		Files: []models.QuickSetupApplyFile{
			{Path: "~/.config/opencode/opencode.json", Content: "{}"},
		},
	}
	if _, err := svc.Apply(request); !errors.Is(err, ErrQuickSetupUnauthenticated) {
		t.Fatalf("unauthenticated Apply() error = %v", err)
	}

	quickSetupAuthorizationHeaderFn = func() string { return "Bearer test-access" }
	outside := filepath.Join(filepath.Dir(tempHome), "outside-opencode.json")
	firstPath := filepath.Join(tempHome, ".config", "opencode", "opencode.json")
	request.Files = []models.QuickSetupApplyFile{
		{Path: firstPath, Content: "{}"},
		{Path: outside, Content: "{}"},
	}
	if _, err := svc.Apply(request); err == nil {
		t.Fatal("Apply() accepted a target outside HOME")
	}
	if _, err := os.Stat(firstPath); !os.IsNotExist(err) {
		t.Fatalf("first file was written before validation completed: %v", err)
	}
	request.Files = []models.QuickSetupApplyFile{{Path: filepath.Join(tempHome, ".zshrc"), Content: "malicious"}}
	if _, err := svc.Apply(request); err == nil {
		t.Fatal("Apply() accepted a path outside the software config directory")
	}
	request.Files = []models.QuickSetupApplyFile{{Path: filepath.Join(tempHome, ".config", "opencode", "plugin.js"), Content: "malicious"}}
	if _, err := svc.Apply(request); err == nil {
		t.Fatal("Apply() accepted an undeclared file inside the software config directory")
	}
}

func TestQuickSetupService_Apply_UsesResolvedInteractiveUserHome(t *testing.T) {
	daemonHome := t.TempDir()
	interactiveHome := t.TempDir()
	t.Setenv("HOME", daemonHome)

	previousAuth := quickSetupAuthorizationHeaderFn
	previousTargetUser := quickSetupTargetUserFn
	previousAdjustOwnership := quickSetupAdjustOwnershipFn
	var adjustedPath string
	quickSetupAuthorizationHeaderFn = func() string { return "Bearer test-access" }
	quickSetupTargetUserFn = func() (quickSetupTargetUser, error) {
		return quickSetupTargetUser{homeDir: interactiveHome, uid: 501, gid: 20, adjustOwner: true}, nil
	}
	quickSetupAdjustOwnershipFn = func(path string, target quickSetupTargetUser) error {
		if target.uid != 501 || target.gid != 20 || !target.adjustOwner {
			t.Fatalf("unexpected target user passed to ownership adjustment: %#v", target)
		}
		adjustedPath = path
		return nil
	}
	t.Cleanup(func() {
		quickSetupAuthorizationHeaderFn = previousAuth
		quickSetupTargetUserFn = previousTargetUser
		quickSetupAdjustOwnershipFn = previousAdjustOwnership
	})

	resp, err := NewQuickSetupService().Apply(models.QuickSetupApplyRequest{
		Software: "opencode",
		Files: []models.QuickSetupApplyFile{
			{Path: "~/.config/opencode/opencode.json", Content: validQuickSetupOpenCodeConfig()},
		},
	})
	if err != nil {
		t.Fatalf("Apply() failed: %v", err)
	}
	wantPath := filepath.Join(interactiveHome, ".config", "opencode", "opencode.json")
	canonicalWantPath, err := canonicalizeQuickSetupPath(wantPath)
	if err != nil {
		t.Fatalf("canonicalize expected path: %v", err)
	}
	if len(resp.Written) != 1 || resp.Written[0] != canonicalWantPath {
		t.Fatalf("written paths = %#v, want [%q]", resp.Written, canonicalWantPath)
	}
	if adjustedPath != canonicalWantPath {
		t.Fatalf("ownership adjusted path = %q, want %q", adjustedPath, canonicalWantPath)
	}
	if _, err := os.Stat(wantPath); err != nil {
		t.Fatalf("interactive user config was not written: %v", err)
	}
	writtenContent, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatalf("read interactive user config: %v", err)
	}
	if got, want := string(writtenContent), validQuickSetupOpenCodeConfig(); got != want {
		t.Fatalf("written manual content changed\ngot:  %s\nwant: %s", got, want)
	}
	wrongPath := filepath.Join(daemonHome, ".config", "opencode", "opencode.json")
	if _, err := os.Stat(wrongPath); !os.IsNotExist(err) {
		t.Fatalf("daemon home unexpectedly received OpenCode config: %v", err)
	}
}

func TestQuickSetupService_Apply_ValidatesContentBeforeWriting(t *testing.T) {
	tempHome := t.TempDir()
	previousAuth := quickSetupAuthorizationHeaderFn
	previousTargetUser := quickSetupTargetUserFn
	previousWrite := quickSetupWriteConfigFileFn
	quickSetupAuthorizationHeaderFn = func() string { return "Bearer test-access" }
	quickSetupTargetUserFn = func() (quickSetupTargetUser, error) {
		return quickSetupTargetUser{homeDir: tempHome}, nil
	}
	writes := 0
	quickSetupWriteConfigFileFn = func(path string, content string) error {
		writes++
		return nil
	}
	t.Cleanup(func() {
		quickSetupAuthorizationHeaderFn = previousAuth
		quickSetupTargetUserFn = previousTargetUser
		quickSetupWriteConfigFileFn = previousWrite
	})

	tests := []struct {
		name    string
		content string
		format  string
		kind    string
		want    string
	}{
		{name: "empty", content: "  ", want: "cannot be empty"},
		{name: "invalid json", content: `{`, want: "not valid JSON"},
		{name: "missing providers", content: `{"model":"p/m"}`, want: "provider must contain"},
		{name: "unknown provider reference", content: strings.Replace(validQuickSetupOpenCodeConfig(), `"model": "aliang-openai/gpt-5.4"`, `"model": "missing/gpt-5.4"`, 1), want: "unknown provider"},
		{name: "unknown model reference", content: strings.Replace(validQuickSetupOpenCodeConfig(), `"model": "aliang-openai/gpt-5.4"`, `"model": "aliang-openai/not-declared"`, 1), want: "unknown model"},
		{name: "wrong declared format", content: validQuickSetupOpenCodeConfig(), format: "yaml", want: "expected json"},
		{name: "wrong declared kind", content: validQuickSetupOpenCodeConfig(), kind: "directory", want: "expected file"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewQuickSetupService().Apply(models.QuickSetupApplyRequest{
				Software: "opencode",
				Files: []models.QuickSetupApplyFile{{
					Path:    "~/.config/opencode/opencode.json",
					Format:  firstNonEmptyQuickSetupString(tt.format, "json"),
					Kind:    firstNonEmptyQuickSetupString(tt.kind, "file"),
					Content: tt.content,
				}},
			})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Apply() error = %v, want substring %q", err, tt.want)
			}
		})
	}
	if writes != 0 {
		t.Fatalf("invalid configurations reached disk writer %d times", writes)
	}
}

func validQuickSetupOpenCodeConfig() string {
	return `{
  "$schema": "https://opencode.ai/config.json",
  "provider": {
    "aliang-openai": {
      "npm": "@ai-sdk/openai-compatible",
      "options": {
        "baseURL": "https://api.aliang.one/v1",
        "apiKey": "sk-test"
      },
      "models": {
        "gpt-5.4": {"name": "GPT-5.4"}
      }
    }
  },
  "model": "aliang-openai/gpt-5.4"
}`
}

func TestQuickSetupService_Apply_RejectsSymlinkEscape(t *testing.T) {
	tempHome := t.TempDir()
	outside := t.TempDir()
	t.Setenv("HOME", tempHome)
	previousAuth := quickSetupAuthorizationHeaderFn
	quickSetupAuthorizationHeaderFn = func() string { return "Bearer test-access" }
	t.Cleanup(func() { quickSetupAuthorizationHeaderFn = previousAuth })

	configDir := filepath.Join(tempHome, ".config")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(configDir, "opencode")
	if err := os.Symlink(outside, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlink unavailable on Windows: %v", err)
		}
		t.Fatal(err)
	}
	_, err := NewQuickSetupService().Apply(models.QuickSetupApplyRequest{
		Software: "opencode",
		Files: []models.QuickSetupApplyFile{
			{Path: filepath.Join(link, "opencode.json"), Content: "{}"},
		},
	})
	if err == nil {
		t.Fatal("Apply() accepted a symlink target outside HOME")
	}
}

func TestQuickSetupService_Apply_RollsBackEarlierFilesOnWriteFailure(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)
	previousAuth := quickSetupAuthorizationHeaderFn
	quickSetupAuthorizationHeaderFn = func() string { return "Bearer test-access" }
	t.Cleanup(func() { quickSetupAuthorizationHeaderFn = previousAuth })

	configDir := filepath.Join(tempHome, ".codex")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	firstPath := filepath.Join(configDir, "config.toml")
	secondPath := filepath.Join(configDir, "auth.json")
	if err := os.WriteFile(firstPath, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}

	previousWriter := quickSetupWriteConfigFileFn
	writes := 0
	quickSetupWriteConfigFileFn = func(path string, content string) error {
		writes++
		if writes == 2 {
			if err := writeConfigFile(path, content); err != nil {
				return err
			}
			return errors.New("injected write failure")
		}
		return writeConfigFile(path, content)
	}
	t.Cleanup(func() { quickSetupWriteConfigFileFn = previousWriter })

	_, err := NewQuickSetupService().Apply(models.QuickSetupApplyRequest{
		Software: "codex",
		Files: []models.QuickSetupApplyFile{
			{Path: firstPath, Content: "replacement"},
			{Path: secondPath, Content: "new auth"},
		},
	})
	if err == nil {
		t.Fatal("Apply() succeeded despite injected write failure")
	}
	content, readErr := os.ReadFile(firstPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(content) != "original" {
		t.Fatalf("first file content = %q, want rollback to original", content)
	}
	if _, statErr := os.Stat(secondPath); !os.IsNotExist(statErr) {
		t.Fatalf("second file exists after failed apply: %v", statErr)
	}
}
