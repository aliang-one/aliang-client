package config

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"sync"

	"aliang.one/nursorgate/common/logger"
)

var (
	globalConfig *Config
	configMutex  sync.RWMutex
)

// SetGlobalConfig sets the global configuration instance
func SetGlobalConfig(cfg *Config) {
	configMutex.Lock()
	defer configMutex.Unlock()
	globalConfig = cfg
}

// GetGlobalConfig returns the global configuration instance
func GetGlobalConfig() *Config {
	configMutex.RLock()
	defer configMutex.RUnlock()
	return globalConfig
}

// SaveConfigToFile persists the global configuration to a JSON file
func SaveConfigToFile(cfg *Config, filePath string) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	parentDir := filepath.Dir(filePath)
	if parentDir != "" && parentDir != "." {
		if err := os.MkdirAll(parentDir, 0o755); err != nil {
			return fmt.Errorf("failed to create config directory %s: %w", parentDir, err)
		}
	}

	if err := ioutil.WriteFile(filePath, data, 0644); err != nil {
		return fmt.Errorf("failed to write config file: %w", err)
	}

	logger.Debug(fmt.Sprintf("Configuration saved to: %s", filePath))
	return nil
}

// ===== Test-Only Exports =====

// ResetGlobalConfigForTest resets global config state for testing
// This allows tests to run in isolation without state pollution
func ResetGlobalConfigForTest() {
	configMutex.Lock()
	defer configMutex.Unlock()
	globalConfig = nil

	// Also reset the config state
	state := GetConfigState()
	state.mu.Lock()
	defer state.mu.Unlock()
	state.usingDefaultConfig = false
	state.hasLocalUserInfo = false
}
