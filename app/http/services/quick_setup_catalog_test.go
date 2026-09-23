package services

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"

	"aliang.one/nursorgate/app/http/models"
	auth "aliang.one/nursorgate/processor/auth"
	"aliang.one/nursorgate/processor/config"
)

func TestDetectQuickSetupInstalled(t *testing.T) {
	home := t.TempDir()
	stub := func(name string) (string, error) { return "", os.ErrNotExist }
	t.Run("cli hit", func(t *testing.T) {
		orig := quickSetupLookPathCLIFn
		quickSetupLookPathCLIFn = func(name string) (string, error) {
			if name == "codex" {
				return "/usr/local/bin/codex", nil
			}
			return "", os.ErrNotExist
		}
		defer func() { quickSetupLookPathCLIFn = orig }()
		if !detectQuickSetupInstalled("codex", home) {
			t.Fatal("codex should be installed via cli")
		}
	})
	t.Run("dir hit", func(t *testing.T) {
		orig := quickSetupLookPathCLIFn
		quickSetupLookPathCLIFn = stub
		defer func() { quickSetupLookPathCLIFn = orig }()
		if detectQuickSetupInstalled("codex", home) {
			t.Fatal("must not detect without dir")
		}
		if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o700); err != nil {
			t.Fatal(err)
		}
		if !detectQuickSetupInstalled("codex", home) {
			t.Fatal("codex should be installed via dir")
		}
	})
	t.Run("neither", func(t *testing.T) {
		orig := quickSetupLookPathCLIFn
		quickSetupLookPathCLIFn = stub
		defer func() { quickSetupLookPathCLIFn = orig }()
		if detectQuickSetupInstalled("opencode", home) {
			t.Fatal("opencode must not be detected")
		}
	})
	t.Run("empty home", func(t *testing.T) {
		if detectQuickSetupInstalled("codex", "") {
			t.Fatal("empty home must not detect")
		}
	})
}

func TestQuickSetupSoftwares_ClaudeUsesSettingsJSON(t *testing.T) {
	sw, ok := findQuickSetupSoftware("claude-code")
	if !ok {
		t.Fatal("claude-code missing")
	}
	if len(sw.Files) != 1 || sw.Files[0].DefaultPath != "~/.claude/settings.json" || sw.Files[0].Format != "json" {
		t.Fatalf("unexpected files %+v", sw.Files)
	}
	root, err := quickSetupAllowedRoot("claude-code", "/home/u")
	if err != nil {
		t.Fatal(err)
	}
	if root != filepath.Join("/home/u", ".claude") {
		t.Fatalf("allowed root: %s", root)
	}
}

func TestQuickSetupSoftwares_PiDeclared(t *testing.T) {
	sw, ok := findQuickSetupSoftware("pi")
	if !ok {
		t.Fatal("pi missing")
	}
	if len(sw.Files) != 2 {
		t.Fatalf("pi files: %+v", sw.Files)
	}
	if sw.Files[0].DefaultPath != "~/.pi/agent/models.json" || sw.Files[1].DefaultPath != "~/.pi/agent/settings.json" {
		t.Fatalf("pi paths: %+v", sw.Files)
	}
	// format 均 json；code 分别为 models/settings
	if sw.Files[0].Format != "json" || sw.Files[1].Format != "json" || sw.Files[0].Code != "models" || sw.Files[1].Code != "settings" {
		t.Fatalf("pi files meta: %+v", sw.Files)
	}
}

func TestDetectQuickSetupInstalled_Pi(t *testing.T) {
	home := t.TempDir()
	orig := quickSetupLookPathCLIFn
	quickSetupLookPathCLIFn = func(string) (string, error) { return "", os.ErrNotExist }
	defer func() { quickSetupLookPathCLIFn = orig }()
	if detectQuickSetupInstalled("pi", home) {
		t.Fatal("must not detect without dir")
	}
	if err := os.MkdirAll(filepath.Join(home, ".pi"), 0o700); err != nil {
		t.Fatal(err)
	}
	if !detectQuickSetupInstalled("pi", home) {
		t.Fatal("pi should be installed via ~/.pi")
	}
}

func TestCatalogMarksInstalled(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	config.ResetGlobalConfigForTest()
	config.SetGlobalConfig(&config.Config{
		Core: &config.CoreConfig{APIServer: "https://api.example.com"},
	})
	t.Cleanup(config.ResetGlobalConfigForTest)

	origKeys, origHome, origCLI := quickSetupGetAPIKeysFn, quickSetupDetectionHomeFn, quickSetupLookPathCLIFn
	quickSetupGetAPIKeysFn = func() ([]auth.UserAPIKey, error) { return nil, nil }
	quickSetupDetectionHomeFn = func() string { return home }
	quickSetupLookPathCLIFn = func(string) (string, error) { return "", os.ErrNotExist }
	t.Cleanup(func() {
		quickSetupGetAPIKeysFn, quickSetupDetectionHomeFn, quickSetupLookPathCLIFn = origKeys, origHome, origCLI
	})

	catalog := (&QuickSetupService{}).Catalog()
	data, ok := catalog["data"].(models.QuickSetupCatalogResponse)
	if !ok {
		t.Fatalf("catalog data missing: %#v", catalog)
	}
	found := map[string]bool{}
	for _, s := range data.Softwares {
		found[s.Code] = s.Installed
	}
	if !found["claude-code"] {
		t.Fatal("claude-code should be marked installed")
	}
	if found["codex"] || found["opencode"] || found["pi"] {
		t.Fatal("codex/opencode/pi must not be marked installed")
	}
}

func TestQuickSetupComboBlankTemplates(t *testing.T) {
	for _, code := range []string{"claude-code", "codex", "opencode", "pi"} {
		files := quickSetupComboBlankTemplates(code)
		if len(files) == 0 {
			t.Fatalf("%s: empty templates", code)
		}
		sw, _ := findQuickSetupSoftware(code)
		if len(files) != len(sw.Files) {
			t.Fatalf("%s: file count mismatch", code)
		}
		// 占位符按 software 级并集断言：codex auth.json 只有 {{api_key}}、
		// pi settings.json 只有 {{model}}——三占位符天然分布在多个文件里。
		seen := map[string]bool{}
		for i, f := range files {
			if f.Code != sw.Files[i].Code {
				t.Fatalf("%s: code mismatch", code)
			}
			hasAny := false
			for _, ph := range []string{"{{base_url}}", "{{api_key}}", "{{model}}"} {
				if strings.Contains(f.Content, ph) {
					seen[ph] = true
					hasAny = true
				}
			}
			if !hasAny {
				t.Fatalf("%s/%s: no placeholder at all:\n%s", code, f.Code, f.Content)
			}
		}
		for _, ph := range []string{"{{base_url}}", "{{api_key}}", "{{model}}"} {
			if !seen[ph] {
				t.Fatalf("%s: placeholder %s missing across files", code, ph)
			}
		}
	}
	if quickSetupComboBlankTemplates("nope") != nil {
		t.Fatal("unknown software must yield nil")
	}
}

// TestQuickSetupComboBlankTemplatesRoundTrip 自审兜底：JSON 模板必须经
// json.Unmarshal 往返解析且占位符落在预期位置；codex config.toml 必须是合法
// TOML，且与 v2 模板形态（fallbackCodexTemplateTOML）在共享键上语义一致。
func TestQuickSetupComboBlankTemplatesRoundTrip(t *testing.T) {
	byCode := func(code string) map[string]string {
		files := quickSetupComboBlankTemplates(code)
		m := make(map[string]string, len(files))
		for _, f := range files {
			m[f.Code] = f.Content
		}
		return m
	}

	t.Run("claude-code settings.json", func(t *testing.T) {
		var parsed struct {
			Env struct {
				BaseURL   string `json:"ANTHROPIC_BASE_URL"`
				AuthToken string `json:"ANTHROPIC_AUTH_TOKEN"`
				Model     string `json:"ANTHROPIC_MODEL"`
			} `json:"env"`
		}
		if err := json.Unmarshal([]byte(byCode("claude-code")["settings"]), &parsed); err != nil {
			t.Fatal(err)
		}
		if parsed.Env.BaseURL != "{{base_url}}" || parsed.Env.AuthToken != "{{api_key}}" || parsed.Env.Model != "{{model}}" {
			t.Fatalf("claude env placeholders misplaced: %+v", parsed.Env)
		}
	})

	t.Run("codex auth.json", func(t *testing.T) {
		var parsed map[string]string
		if err := json.Unmarshal([]byte(byCode("codex")["auth"]), &parsed); err != nil {
			t.Fatal(err)
		}
		if parsed["OPENAI_API_KEY"] != "{{api_key}}" {
			t.Fatalf("codex auth.json mismatch: %#v", parsed)
		}
	})

	t.Run("opencode opencode.json", func(t *testing.T) {
		var parsed struct {
			Schema   string `json:"$schema"`
			Model    string `json:"model"`
			Provider map[string]struct {
				NPM     string `json:"npm"`
				Options struct {
					BaseURL string `json:"baseURL"`
					APIKey  string `json:"apiKey"`
				} `json:"options"`
			} `json:"provider"`
		}
		if err := json.Unmarshal([]byte(byCode("opencode")["config"]), &parsed); err != nil {
			t.Fatal(err)
		}
		if parsed.Schema != "https://opencode.ai/config.json" {
			t.Fatalf("$schema mismatch: %q", parsed.Schema)
		}
		if parsed.Model != "aliang/{{model}}" {
			t.Fatalf("model mismatch: %q", parsed.Model)
		}
		provider, ok := parsed.Provider["aliang"]
		if !ok {
			t.Fatalf("provider.aliang missing: %#v", parsed.Provider)
		}
		if provider.NPM != "@ai-sdk/anthropic" {
			t.Fatalf("npm mismatch: %q", provider.NPM)
		}
		if provider.Options.BaseURL != "{{base_url}}" || provider.Options.APIKey != "{{api_key}}" {
			t.Fatalf("opencode options placeholders misplaced: %+v", provider.Options)
		}
	})

	t.Run("pi models.json + settings.json", func(t *testing.T) {
		files := byCode("pi")
		var modelsParsed struct {
			Providers map[string]struct {
				Name    string `json:"name"`
				BaseURL string `json:"baseUrl"`
				APIKey  string `json:"apiKey"`
				API     string `json:"api"`
				Models  []struct {
					ID   string `json:"id"`
					Name string `json:"name"`
				} `json:"models"`
			} `json:"providers"`
		}
		if err := json.Unmarshal([]byte(files["models"]), &modelsParsed); err != nil {
			t.Fatal(err)
		}
		provider, ok := modelsParsed.Providers["aliang"]
		if !ok {
			t.Fatalf("providers.aliang missing: %#v", modelsParsed.Providers)
		}
		if provider.Name != "Aliang Gateway" || provider.BaseURL != "{{base_url}}" ||
			provider.APIKey != "{{api_key}}" || provider.API != "anthropic-messages" {
			t.Fatalf("pi provider mismatch: %+v", provider)
		}
		if len(provider.Models) != 1 || provider.Models[0].ID != "{{model}}" || provider.Models[0].Name != "{{model}}" {
			t.Fatalf("pi models mismatch: %+v", provider.Models)
		}

		var settingsParsed struct {
			DefaultProvider string `json:"defaultProvider"`
			DefaultModel    string `json:"defaultModel"`
		}
		if err := json.Unmarshal([]byte(files["settings"]), &settingsParsed); err != nil {
			t.Fatal(err)
		}
		if settingsParsed.DefaultProvider != "aliang" || settingsParsed.DefaultModel != "{{model}}" {
			t.Fatalf("pi settings mismatch: %+v", settingsParsed)
		}
	})

	t.Run("codex config.toml matches v2 template shape", func(t *testing.T) {
		content := byCode("codex")["config"]
		var blank map[string]interface{}
		if err := toml.Unmarshal([]byte(content), &blank); err != nil {
			t.Fatalf("blank config.toml is not valid TOML: %v\n%s", err, content)
		}
		if blank["model"] != "{{model}}" || blank["model_provider"] != "aliang" || blank["approval_policy"] != "never" {
			t.Fatalf("codex top-level keys mismatch: %#v", blank)
		}

		var v2 map[string]interface{}
		if err := toml.Unmarshal([]byte(fallbackCodexTemplateTOML("{{model}}", "{{base_url}}")), &v2); err != nil {
			t.Fatal(err)
		}
		if blank["model"] != v2["model"] || blank["model_provider"] != v2["model_provider"] {
			t.Fatalf("codex shared top-level keys diverge from v2: %#v vs %#v", blank, v2)
		}
		blankSection, _ := blank["model_providers"].(map[string]interface{})
		v2Section, _ := v2["model_providers"].(map[string]interface{})
		if !reflect.DeepEqual(blankSection["aliang"], v2Section["aliang"]) {
			t.Fatalf("codex aliang section diverges from v2:\nblank: %#v\nv2:    %#v", blankSection["aliang"], v2Section["aliang"])
		}
		if base, _ := blankSection["aliang"].(map[string]interface{})["base_url"].(string); base != "{{base_url}}" {
			t.Fatalf("base_url placeholder mismatch: %#v", base)
		}
	})
}
