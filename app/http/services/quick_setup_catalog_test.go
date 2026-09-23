package services

import (
	"os"
	"path/filepath"
	"testing"

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
