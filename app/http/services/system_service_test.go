package services

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"aliang.one/nursorgate/processor/config"
	"aliang.one/nursorgate/processor/setup"
)

func TestEnsureManagedSystemServiceConfigPathPersistsCurrentConfig(t *testing.T) {
	t.Setenv("ALIANG_DATA_DIR", t.TempDir())
	config.ResetGlobalConfigForTest()
	t.Cleanup(config.ResetGlobalConfigForTest)

	currentConfig := &config.Config{
		Core: &config.CoreConfig{
			APIServer: "https://current-api.example.test",
		},
	}
	config.SetGlobalConfig(currentConfig)

	configPath, err := ensureManagedSystemServiceConfigPathWithSource("")
	if err != nil {
		t.Fatalf("ensureManagedSystemServiceConfigPathWithSource() error = %v", err)
	}
	if configPath != setup.RuntimeConfigPath() {
		t.Fatalf("config path = %q, want %q", configPath, setup.RuntimeConfigPath())
	}

	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("expected managed config file to be written: %v", err)
	}

	var persisted config.Config
	if err := json.Unmarshal(raw, &persisted); err != nil {
		t.Fatalf("persisted config is invalid json: %v", err)
	}
	if persisted.Core == nil || persisted.Core.APIServer != currentConfig.Core.APIServer {
		t.Fatalf("persisted config core.api_server = %+v, want %q", persisted.Core, currentConfig.Core.APIServer)
	}
}

func TestEnsureManagedSystemServiceConfigPathPrefersExplicitSource(t *testing.T) {
	t.Setenv("ALIANG_DATA_DIR", t.TempDir())
	config.ResetGlobalConfigForTest()
	t.Cleanup(config.ResetGlobalConfigForTest)

	config.SetGlobalConfig(&config.Config{
		Core: &config.CoreConfig{APIServer: "https://current-api.example.test"},
	})

	sourcePath := filepath.Join(t.TempDir(), "config.json")
	sourceConfig := &config.Config{
		Core: &config.CoreConfig{
			APIServer: "https://explicit-api.example.test",
		},
	}
	if err := config.SaveConfigToFile(sourceConfig, sourcePath); err != nil {
		t.Fatalf("failed to write source config: %v", err)
	}

	configPath, err := ensureManagedSystemServiceConfigPathWithSource(sourcePath)
	if err != nil {
		t.Fatalf("ensureManagedSystemServiceConfigPathWithSource() error = %v", err)
	}

	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("expected managed config file to be written: %v", err)
	}

	var persisted config.Config
	if err := json.Unmarshal(raw, &persisted); err != nil {
		t.Fatalf("persisted config is invalid json: %v", err)
	}
	if persisted.Core == nil || persisted.Core.APIServer != sourceConfig.Core.APIServer {
		t.Fatalf("persisted config core.api_server = %+v, want %q", persisted.Core, sourceConfig.Core.APIServer)
	}
}
