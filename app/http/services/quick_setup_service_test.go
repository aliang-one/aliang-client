package services

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
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

// TestQuickSetupService_CatalogCombosAndPresets 锁定 catalog 的组合接线（spec §4.0，
// presets 按 software 下发的细化口径）：已安装 agent 触发默认组合种子并下发组合列表，
// 未安装不种子；presets codex 带 /v1、claude-code 不带；种子幂等。
func TestQuickSetupService_CatalogCombosAndPresets(t *testing.T) {
	// stubComboServiceEnv：全局 config api_server=https://api.example.com +
	// 临时路径组合 store（quickSetupComboStoreFn 钩子，绝不碰真实库）。
	_, _ = stubComboServiceEnv(t)

	previousKeys := quickSetupGetAPIKeysFn
	quickSetupGetAPIKeysFn = func() ([]auth.UserAPIKey, error) {
		return nil, nil
	}
	t.Cleanup(func() { quickSetupGetAPIKeysFn = previousKeys })

	// CLI 检测只命中 claude；检测家目录隔离到临时目录防读到本机真实配置目录。
	previousLook := quickSetupLookPathCLIFn
	quickSetupLookPathCLIFn = func(name string) (string, error) {
		if name == "claude" {
			return "/usr/local/bin/claude", nil
		}
		return "", errors.New("cli not found")
	}
	t.Cleanup(func() { quickSetupLookPathCLIFn = previousLook })

	previousHome := quickSetupDetectionHomeFn
	detectHome := t.TempDir()
	quickSetupDetectionHomeFn = func() string { return detectHome }
	t.Cleanup(func() { quickSetupDetectionHomeFn = previousHome })

	svc := NewQuickSetupService()
	result := svc.Catalog()
	if result["status"] != "success" {
		t.Fatalf("catalog status = %#v, want success: %#v", result["status"], result)
	}
	data := result["data"].(models.QuickSetupCatalogResponse)

	if len(data.Combos) != 1 {
		t.Fatalf("combos len = %d, want 1: %+v", len(data.Combos), data.Combos)
	}
	if combo := data.Combos[0]; combo.Software != "claude-code" || combo.Name != "默认" || !combo.IsDefault {
		t.Fatalf("seeded combo = %+v, want claude-code 默认 is_default=true", combo)
	}

	byCode := map[string]models.QuickSetupSoftware{}
	for _, sw := range data.Softwares {
		byCode[sw.Code] = sw
	}
	if !byCode["claude-code"].Installed {
		t.Fatal("claude-code should be installed via CLI stub")
	}
	if byCode["codex"].Installed {
		t.Fatal("codex should not be installed")
	}
	if got := byCode["claude-code"].Presets; got == nil || got.BaseURLLocal != "http://127.0.0.1:56432" {
		t.Fatalf("claude-code presets = %+v, want base_url_local http://127.0.0.1:56432 (no /v1)", byCode["claude-code"].Presets)
	}
	if got := byCode["claude-code"].Presets; got.BaseURLPublic != "https://api.example.com" {
		t.Fatalf("claude-code base_url_public = %q, want https://api.example.com", got.BaseURLPublic)
	}
	if got := byCode["codex"].Presets; got == nil || got.BaseURLLocal != "http://127.0.0.1:56432/v1" {
		t.Fatalf("codex presets = %+v, want base_url_local http://127.0.0.1:56432/v1", byCode["codex"].Presets)
	}
	if got := byCode["codex"].Presets; got.BaseURLPublic != "https://api.example.com/v1" {
		t.Fatalf("codex base_url_public = %q, want https://api.example.com/v1", got.BaseURLPublic)
	}
	for _, combo := range data.Combos {
		if combo.Software == "codex" {
			t.Fatal("codex must not seed combos while not installed")
		}
	}

	// 二次 Catalog：种子幂等，组合数不变。
	second := svc.Catalog()
	if second["status"] != "success" {
		t.Fatalf("second catalog status = %#v: %#v", second["status"], second)
	}
	secondData := second["data"].(models.QuickSetupCatalogResponse)
	if len(secondData.Combos) != len(data.Combos) {
		t.Fatalf("second catalog combos len = %d, want %d (seed idempotent)", len(secondData.Combos), len(data.Combos))
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
			format := tt.format
			if format == "" {
				format = "json"
			}
			kind := tt.kind
			if kind == "" {
				kind = "file"
			}
			_, err := NewQuickSetupService().Apply(models.QuickSetupApplyRequest{
				Software: "opencode",
				Files: []models.QuickSetupApplyFile{{
					Path:    "~/.config/opencode/opencode.json",
					Format:  format,
					Kind:    kind,
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
