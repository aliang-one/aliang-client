package services

import (
	"encoding/json"
	"strings"
	"testing"

	"aliang.one/nursorgate/app/http/models"
	auth "aliang.one/nursorgate/processor/auth"
	"aliang.one/nursorgate/processor/config"
)

func TestRenderClaudeSettingsEnv(t *testing.T) {
	payload := renderClaudeSettingsEnv("sk-aliang", "claude-sonnet-4-5-20250929", "https://api.aliang.one")
	env, ok := payload["env"].(map[string]string)
	if !ok {
		t.Fatalf("env block missing: %#v", payload)
	}
	if env["ANTHROPIC_BASE_URL"] != "https://api.aliang.one" {
		t.Fatalf("base url: %v", env)
	}
	if env["ANTHROPIC_AUTH_TOKEN"] != "sk-aliang" {
		t.Fatal("auth token missing")
	}
	if _, has := env["ANTHROPIC_API_KEY"]; has {
		t.Fatal("must use AUTH_TOKEN, not API_KEY")
	}
	if env["ANTHROPIC_MODEL"] != "claude-sonnet-4-5-20250929" {
		t.Fatal("model missing")
	}
}

func TestRenderClaudeCodeFiles(t *testing.T) {
	softwareDef, ok := findQuickSetupSoftware("claude-code")
	if !ok {
		t.Fatal("claude-code missing from catalog")
	}
	apiKey := models.QuickSetupAPIKey{Key: "sk-test", Provider: "anthropic"}

	cases := []struct {
		name    string
		apiRoot string
	}{
		{name: "inference root", apiRoot: "https://api.aliang.one"},
		{name: "api root with /v1 suffix", apiRoot: "https://api.aliang.one/v1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			files, notes, err := renderClaudeCodeFiles(softwareDef, apiKey, tc.apiRoot)
			if err != nil {
				t.Fatal(err)
			}
			if len(files) != 1 {
				t.Fatalf("want a single file, got %d", len(files))
			}
			file := files[0]
			if file.Code != "settings" {
				t.Fatalf("code: %s", file.Code)
			}
			if file.Format != "json" {
				t.Fatalf("format: %s", file.Format)
			}
			if file.Path != "~/.claude/settings.json" {
				t.Fatalf("path: %s", file.Path)
			}
			var parsed struct {
				Env map[string]string `json:"env"`
			}
			if err := json.Unmarshal([]byte(file.Content), &parsed); err != nil {
				t.Fatal(err)
			}
			if parsed.Env["ANTHROPIC_BASE_URL"] != "https://api.aliang.one" {
				t.Fatalf("base url must not carry /v1: %v", parsed.Env)
			}
			if parsed.Env["ANTHROPIC_AUTH_TOKEN"] != "sk-test" {
				t.Fatalf("auth token missing: %v", parsed.Env)
			}
			if len(notes) == 0 {
				t.Fatal("notes must not be empty")
			}
		})
	}
}

func TestQuickSetupModeRoot(t *testing.T) {
	if got := quickSetupModeRoot("local", "https://backend.aliang.one"); got != "http://127.0.0.1:56432" {
		t.Fatalf("local root: %s", got)
	}
	if got := quickSetupModeRoot("public", "https://backend.aliang.one"); got != "https://api.aliang.one" {
		t.Fatalf("public root: %s", got)
	}
	if got := quickSetupModeRoot("", "https://backend.aliang.one"); got != "https://api.aliang.one" {
		t.Fatalf("default must be public: %s", got)
	}
	if got := quickSetupModeRoot("bogus", "https://backend.aliang.one"); got != "https://api.aliang.one" {
		t.Fatalf("unknown mode falls back to public: %s", got)
	}
}

func TestQuickSetupV1SuffixPerSoftware(t *testing.T) {
	// codex/opencode: root + /v1；claude: root 原样（行为变更，spec §7.1，Task 7 已落地 claude 侧）
	if got := quickSetupProviderBaseURL("anthropic", "http://127.0.0.1:56432"); got != "http://127.0.0.1:56432/v1" {
		t.Fatalf("codex/opencode: %s", got)
	}
}

func TestQuickSetupService_Render_LocalModeSwitchesRoot(t *testing.T) {
	config.ResetGlobalConfigForTest()
	config.SetGlobalConfig(&config.Config{
		Core: &config.CoreConfig{APIServer: "https://backend.aliang.one"},
	})
	t.Cleanup(config.ResetGlobalConfigForTest)

	previous := quickSetupGetAPIKeysFn
	quickSetupGetAPIKeysFn = func() ([]auth.UserAPIKey, error) {
		return []auth.UserAPIKey{
			{ID: 22, Key: "sk-ant-real", Name: "Anthropic Key", Status: "active", Provider: "anthropic", SecretAvailable: true},
		}, nil
	}
	t.Cleanup(func() { quickSetupGetAPIKeysFn = previous })

	svc := NewQuickSetupService()

	// claude：modeRoot 原样，不带 /v1（Claude Code 自行追加 /v1/messages）。
	claudeResp, err := svc.Render(models.QuickSetupRenderRequest{Software: "claude-code", KeyIDs: []int64{22}, Mode: "local"})
	if err != nil {
		t.Fatalf("render claude-code failed: %v", err)
	}
	var claudeCfg struct {
		Env map[string]string `json:"env"`
	}
	if err := json.Unmarshal([]byte(claudeResp.Variants[0].Files[0].Content), &claudeCfg); err != nil {
		t.Fatal(err)
	}
	if got, want := claudeCfg.Env["ANTHROPIC_BASE_URL"], "http://127.0.0.1:56432"; got != want {
		t.Fatalf("claude local base url = %q, want %q", got, want)
	}

	// codex：modeRoot 经 quickSetupProviderBaseURL 自动 +/v1。
	codexResp, err := svc.Render(models.QuickSetupRenderRequest{Software: "codex", KeyIDs: []int64{22}, Mode: "local"})
	if err != nil {
		t.Fatalf("render codex failed: %v", err)
	}
	if !strings.Contains(codexResp.Variants[0].Files[0].Content, "base_url = \"http://127.0.0.1:56432/v1\"") {
		t.Fatalf("codex local base_url missing: %s", codexResp.Variants[0].Files[0].Content)
	}

	// opencode：provider options.baseURL = modeRoot + /v1。
	openCodeResp, err := svc.Render(models.QuickSetupRenderRequest{Software: "opencode", KeyIDs: []int64{22}, Mode: "local"})
	if err != nil {
		t.Fatalf("render opencode failed: %v", err)
	}
	var openCfg map[string]interface{}
	if err := json.Unmarshal([]byte(openCodeResp.Variants[0].Files[0].Content), &openCfg); err != nil {
		t.Fatal(err)
	}
	providers := openCfg["provider"].(map[string]interface{})
	for id, raw := range providers {
		provider := raw.(map[string]interface{})
		options := provider["options"].(map[string]interface{})
		if got, want := options["baseURL"], "http://127.0.0.1:56432/v1"; got != want {
			t.Fatalf("%s baseURL = %#v, want %q", id, got, want)
		}
	}
}
