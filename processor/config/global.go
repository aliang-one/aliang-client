package config

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"sync"

	"aliang.one/nursorgate/common/logger"
	"aliang.one/nursorgate/processor/geoip"
)

var (
	globalConfig *Config
	configMutex  sync.RWMutex
)

// SetGlobalConfig sets the global configuration instance.
// 该函数是配置生效（Apply 成功）的唯一咽喉：启动加载、客户配置提交、核心配置
// 提交三条路径都在 Validate 通过后到达这里——customer.geoip.download_url 的
// 注入挂在此处，保证任意路径生效的配置同步到 geoip 下载源（env 层仍最高）。
// 注入必须在 configMutex 临界区内完成：两个并发提交若在解锁后各自注入，
// 旧配置的 override 可能后落地并持久到下次提交（顺序翻转窗口）。
// SetDownloadURLOverride 是非阻塞 atomic store 且 geoip 不回调 config，
// 锁内调用零死锁风险。
func SetGlobalConfig(cfg *Config) {
	configMutex.Lock()
	defer configMutex.Unlock()
	globalConfig = cfg
	geoip.SetDownloadURLOverride(cfg.EffectiveGeoIPDownloadURL())
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
	geoip.SetDownloadURLOverride("")

	// Also reset the config state
	state := GetConfigState()
	state.mu.Lock()
	defer state.mu.Unlock()
	state.usingDefaultConfig = false
	state.hasLocalUserInfo = false
}
