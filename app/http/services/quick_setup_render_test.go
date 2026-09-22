package services

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestRenderClaudeSettingsEnv(t *testing.T) {
	content := renderClaudeSettingsEnv("sk-aliang", "claude-sonnet-4-5-20250929", "https://api.aliang.one")
	var parsed struct {
		Env map[string]string `json:"env"`
	}
	if err := json.Unmarshal([]byte(content), &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Env["ANTHROPIC_BASE_URL"] != "https://api.aliang.one" {
		t.Fatalf("base url: %v", parsed.Env)
	}
	if parsed.Env["ANTHROPIC_AUTH_TOKEN"] != "sk-aliang" {
		t.Fatal("auth token missing")
	}
	if _, has := parsed.Env["ANTHROPIC_API_KEY"]; has {
		t.Fatal("must use AUTH_TOKEN, not API_KEY")
	}
	if parsed.Env["ANTHROPIC_MODEL"] != "claude-sonnet-4-5-20250929" {
		t.Fatal("model missing")
	}
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
