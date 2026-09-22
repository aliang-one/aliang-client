package services

import (
	"encoding/json"
	"testing"

	"aliang.one/nursorgate/app/http/models"
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
