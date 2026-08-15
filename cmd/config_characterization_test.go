package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"aliang.one/nursorgate/app/http/storage"
	"aliang.one/nursorgate/common/cache"
	processorconfig "aliang.one/nursorgate/processor/config"
	"aliang.one/nursorgate/processor/setup"
)

func resetConfigPipelineStateForTest(t *testing.T) {
	t.Helper()
	processorconfig.ResetGlobalConfigForTest()
	cache.ResetCacheDirForTest()
	storage.ResetSoftwareConfigDBForTest()
	setUseDefaultConfig(false)
	t.Cleanup(func() {
		processorconfig.ResetGlobalConfigForTest()
		cache.ResetCacheDirForTest()
		storage.ResetSoftwareConfigDBForTest()
		setUseDefaultConfig(false)
	})
}

func TestLoadAndApplyConfig_WhenFileMissing_ReturnsError(t *testing.T) {
	resetConfigPipelineStateForTest(t)

	tempDir := t.TempDir()
	tempFile, err := os.CreateTemp(tempDir, "config-*.json")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	missingPath := tempFile.Name()
	_ = tempFile.Close()
	if err := os.Remove(missingPath); err != nil {
		t.Fatalf("failed to remove temp file for missing-path test: %v", err)
	}

	err = LoadAndApplyConfig(missingPath)
	if err == nil {
		t.Fatalf("expected error for missing config file, got nil")
	}
}

func TestApplyDefaultConfig_SetsUsingDefaultConfigTrue(t *testing.T) {
	resetConfigPipelineStateForTest(t)

	if IsUsingDefaultConfig() {
		t.Fatalf("expected default config flag to start false")
	}

	if err := ApplyDefaultConfig(); err != nil {
		t.Fatalf("expected ApplyDefaultConfig to succeed, got: %v", err)
	}

	if !IsUsingDefaultConfig() {
		t.Fatalf("expected IsUsingDefaultConfig() to be true after ApplyDefaultConfig")
	}
}

func TestLoadConfigFromBytes_WithInvalidJSON_ReturnsError(t *testing.T) {
	resetConfigPipelineStateForTest(t)

	invalidJSON := []byte(`{"core":{"api_server":`)

	cfg, err := LoadConfigFromBytes(invalidJSON)
	if err == nil {
		t.Fatalf("expected error for invalid JSON, got nil")
	}
	if cfg != nil {
		t.Fatalf("expected nil config on invalid JSON, got %#v", cfg)
	}
}

func TestLoadConfigFromBytes_WithValidJSON_ReturnsConfig(t *testing.T) {
	resetConfigPipelineStateForTest(t)

	validJSON := []byte(`{
		"core": {
			"api_server": "https://api.aliang.one",
			"aliangServer": {
				"type": "aliang",
				"core_server": "gateway.example.com:443"
			}
		}
	}`)

	cfg, err := LoadConfigFromBytes(validJSON)
	if err != nil {
		t.Fatalf("expected no error for valid JSON, got: %v", err)
	}
	if cfg == nil {
		t.Fatalf("expected parsed config, got nil")
	}
	if cfg.APIBaseURL() != "https://api.aliang.one" {
		t.Fatalf("expected APIBaseURL to be parsed, got: %q", cfg.APIBaseURL())
	}
	if cfg.Core == nil || cfg.Core.AliangServer == nil {
		t.Fatalf("expected core.aliangServer to be present")
	}
	if cfg.Core.AliangServer.CoreServer != "gateway.example.com:443" {
		t.Fatalf("expected aliangServer core_server to be parsed, got: %q", cfg.Core.AliangServer.CoreServer)
	}

}

func writeStartupTestConfigFile(t *testing.T, dir string, fileName string, content string) string {
	t.Helper()
	path := filepath.Join(dir, fileName)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("failed to write test config file %s: %v", path, err)
	}
	return path
}

func TestApplyStartupConfig_UsesExplicitConfigPathOverLocalConfigNewJSON(t *testing.T) {
	resetConfigPipelineStateForTest(t)

	originalWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(originalWd)
	})

	tempDir := t.TempDir()
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("failed to change working directory: %v", err)
	}

	writeStartupTestConfigFile(t, tempDir, "config.json", `{"api_server":`)
	explicitPath := writeStartupTestConfigFile(t, tempDir, "explicit.json", `{
		"core": {
			"api_server": "https://api.explicit.example.com",
			"aliangServer": {
				"type": "aliang",
				"core_server": "explicit-gateway.example.com:443"
			}
		}
	}`)

	if err := ApplyStartupConfig(explicitPath); err != nil {
		t.Fatalf("expected explicit --config path to be used even when config.json exists, got: %v", err)
	}

	globalCfg := processorconfig.GetGlobalConfig()
	if globalCfg == nil {
		t.Fatalf("expected global config to be set")
	}
	if globalCfg.APIBaseURL() != "https://api.explicit.example.com" {
		t.Fatalf("expected explicit config APIBaseURL, got %q", globalCfg.APIBaseURL())
	}
	if IsUsingDefaultConfig() {
		t.Fatalf("expected custom config flag for explicit path")
	}
}

func TestApplyStartupConfig_UsesConfigNewJSONWhenNoExplicitPath(t *testing.T) {
	resetConfigPipelineStateForTest(t)

	originalWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(originalWd)
	})

	tempDir := t.TempDir()
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("failed to change working directory: %v", err)
	}
	t.Setenv("HOME", tempDir)

	writeStartupTestConfigFile(t, tempDir, "config.json", `{
		"core": {
			"api_server": "https://api.local-config-new.example.com",
			"aliangServer": {
				"type": "aliang",
				"core_server": "local-gateway.example.com:443"
			}
		}
	}`)

	if err := ApplyStartupConfig(""); err != nil {
		t.Fatalf("expected config.json to be used when no explicit path, got: %v", err)
	}

	globalCfg := processorconfig.GetGlobalConfig()
	if globalCfg == nil {
		t.Fatalf("expected global config to be set")
	}
	if globalCfg.APIBaseURL() != "https://api.local-config-new.example.com" {
		t.Fatalf("expected config.json APIBaseURL, got %q", globalCfg.APIBaseURL())
	}
	if IsUsingDefaultConfig() {
		t.Fatalf("expected custom config flag when loading config.json")
	}
}

func TestApplyStartupConfig_UsesEmbeddedDefaultWhenNoExplicitAndNoConfigNewJSON(t *testing.T) {
	resetConfigPipelineStateForTest(t)

	originalWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(originalWd)
	})

	tempDir := t.TempDir()
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("failed to change working directory: %v", err)
	}
	t.Setenv("HOME", tempDir)

	if err := ApplyStartupConfig(""); err != nil {
		t.Fatalf("expected embedded default config to be used, got: %v", err)
	}

	globalCfg := processorconfig.GetGlobalConfig()
	if globalCfg == nil {
		t.Fatalf("expected global config to be set")
	}
	if !IsUsingDefaultConfig() {
		t.Fatalf("expected embedded default to mark using-default flag true")
	}
}

func TestApplyStartupConfig_FailsFastWhenConfigNewJSONExistsButInvalid(t *testing.T) {
	resetConfigPipelineStateForTest(t)

	originalWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(originalWd)
	})

	tempDir := t.TempDir()
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("failed to change working directory: %v", err)
	}

	writeStartupTestConfigFile(t, tempDir, "config.json", `{"api_server":`)

	err = ApplyStartupConfig("")
	if err == nil {
		t.Fatalf("expected fail-fast error when config.json exists but is invalid")
	}
	if processorconfig.GetGlobalConfig() != nil {
		t.Fatalf("expected global config to remain unset on fail-fast invalid config.json")
	}
}

func TestApplyStartupConfigForMode_UsesRuntimeConfigInDaemonMode(t *testing.T) {
	resetConfigPipelineStateForTest(t)

	originalWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(originalWd)
	})

	tempDir := t.TempDir()
	runtimeDir := filepath.Join(tempDir, "runtime")
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		t.Fatalf("failed to create runtime directory: %v", err)
	}
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("failed to change working directory: %v", err)
	}
	t.Setenv("HOME", "")
	t.Setenv("ALIANG_DATA_DIR", runtimeDir)

	writeStartupTestConfigFile(t, runtimeDir, "config.json", `{
		"core": {
			"api_server": "https://api.runtime.example.com",
			"aliangServer": {
				"type": "aliang",
				"core_server": "runtime-gateway.example.com:443"
			}
		}
	}`)

	if err := ApplyStartupConfigForMode(setup.RuntimeModeDaemon, ""); err != nil {
		t.Fatalf("expected runtime config to be used in daemon mode, got: %v", err)
	}

	globalCfg := processorconfig.GetGlobalConfig()
	if globalCfg == nil {
		t.Fatalf("expected global config to be set")
	}
	if globalCfg.APIBaseURL() != "https://api.runtime.example.com" {
		t.Fatalf("expected runtime config APIBaseURL, got %q", globalCfg.APIBaseURL())
	}
	if IsUsingDefaultConfig() {
		t.Fatalf("expected custom config flag when loading runtime config")
	}
}
