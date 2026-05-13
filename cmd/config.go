package cmd

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"

	"aliang.one/nursorgate/app/http/storage"
	"aliang.one/nursorgate/common/logger"
	"aliang.one/nursorgate/outbound"
	"aliang.one/nursorgate/processor/config"
	"aliang.one/nursorgate/processor/dns"
	"aliang.one/nursorgate/processor/mirror"
	"aliang.one/nursorgate/processor/setup"
)

const startupLocalConfigPath = "./config.json"

type startupConfigSource string

const (
	startupConfigSourceExplicitPath startupConfigSource = "--config"
	startupConfigSourceLocalFile    startupConfigSource = "./config.json"
	startupConfigSourceUserHome     startupConfigSource = "~/.aliang/config.json"
	startupConfigSourceRuntime      startupConfigSource = "runtime config"
	startupConfigSourceEmbedded     startupConfigSource = "embedded default"
)

// Re-export config types for backward compatibility
type Config = config.Config

// setUseDefaultConfig marks that the default configuration is being used
func setUseDefaultConfig(value bool) {
	config.SetUsingDefaultConfig(value)
}

// IsUsingDefaultConfig returns whether the default embedded configuration is being used
func IsUsingDefaultConfig() bool {
	return config.IsUsingDefaultConfig()
}

// GetDefaultConfigBytes 返回嵌入的默认配置字节数据
func GetDefaultConfigBytes() []byte {
	return config.GetDefaultConfigBytes()
}

func resolveStartupConfigSourceForMode(mode setup.RuntimeMode, explicitConfigPath string) (startupConfigSource, string, error) {
	if explicitConfigPath != "" {
		return startupConfigSourceExplicitPath, explicitConfigPath, nil
	}

	if _, err := os.Stat(startupLocalConfigPath); err == nil {
		return startupConfigSourceLocalFile, startupLocalConfigPath, nil
	} else if !os.IsNotExist(err) {
		return "", "", fmt.Errorf("failed to inspect %s: %w", startupLocalConfigPath, err)
	}

	if mode != setup.RuntimeModeDaemon {
		homeConfigPath, err := setup.UserConfigPath()
		if err != nil {
			logger.Debug("User home directory not available, skipping ~/.aliang/config.json check")
		} else {
			if _, err := os.Stat(homeConfigPath); err == nil {
				return startupConfigSourceUserHome, homeConfigPath, nil
			} else if !os.IsNotExist(err) {
				return "", "", fmt.Errorf("failed to inspect %s: %w", homeConfigPath, err)
			}
		}
	}

	if mode == setup.RuntimeModeDaemon {
		runtimeConfigPath := setup.RuntimeConfigPath()
		if _, err := os.Stat(runtimeConfigPath); err == nil {
			return startupConfigSourceRuntime, runtimeConfigPath, nil
		} else if !os.IsNotExist(err) {
			return "", "", fmt.Errorf("failed to inspect %s: %w", runtimeConfigPath, err)
		}
	}

	return startupConfigSourceEmbedded, "", nil
}

func ApplyStartupConfig(explicitConfigPath string) error {
	return ApplyStartupConfigForMode(setup.RuntimeModeInteractive, explicitConfigPath)
}

func ApplyStartupConfigForMode(mode setup.RuntimeMode, explicitConfigPath string) error {
	source, selectedPath, err := resolveStartupConfigSourceForMode(mode, explicitConfigPath)
	if err != nil {
		return err
	}

	switch source {
	case startupConfigSourceExplicitPath:
		logger.Debug(fmt.Sprintf("Loading configuration from explicit --config path: %s", selectedPath))
		if err := LoadAndApplyConfig(selectedPath); err != nil {
			return fmt.Errorf("failed to load startup config from --config path %s: %w", selectedPath, err)
		}
		logger.Debug(fmt.Sprintf("Startup configuration source: %s", source))
		return nil
	case startupConfigSourceLocalFile:
		logger.Debug(fmt.Sprintf("Loading configuration from current directory file: %s", selectedPath))
		if err := LoadAndApplyConfig(selectedPath); err != nil {
			return fmt.Errorf("failed to load startup config from %s (fail-fast, no fallback): %w", selectedPath, err)
		}
		logger.Debug(fmt.Sprintf("Startup configuration source: %s", source))
		return nil
	case startupConfigSourceUserHome:
		logger.Debug(fmt.Sprintf("Loading configuration from user home: %s", selectedPath))
		if err := LoadAndApplyConfig(selectedPath); err != nil {
			return fmt.Errorf("failed to load startup config from %s (fail-fast, no fallback): %w", selectedPath, err)
		}
		logger.Debug(fmt.Sprintf("Startup configuration source: %s", source))
		return nil
	case startupConfigSourceRuntime:
		logger.Debug(fmt.Sprintf("Loading configuration from runtime directory: %s", selectedPath))
		if err := LoadAndApplyConfig(selectedPath); err != nil {
			return fmt.Errorf("failed to load startup config from %s (fail-fast, no fallback): %w", selectedPath, err)
		}
		logger.Debug(fmt.Sprintf("Startup configuration source: %s", source))
		return nil
	default:
		// No config file found — try to restore from database before falling back to embedded default
		logger.Debug("No config file found, attempting to restore configuration from database...")
		if restored, err := tryRestoreConfigFromDatabase(); err != nil {
			logger.Warn(fmt.Sprintf("Database config restore failed: %v", err))
		} else if restored != nil {
			logger.Debug("Startup configuration source: database snapshot")
			if err := ApplyConfig(restored); err != nil {
				logger.Warn(fmt.Sprintf("Database config applied with warnings: %v", err))
				// Don't fail — fall through to embedded default
			} else {
				setUseDefaultConfig(false)
				return nil
			}
		}

		logger.Debug("No config file or database snapshot found, using embedded default configuration")
		if err := ApplyDefaultConfig(); err != nil {
			return fmt.Errorf("failed to apply startup config from embedded default: %w", err)
		}
		logger.Debug(fmt.Sprintf("Startup configuration source: %s", source))
		return nil
	}
}

// tryRestoreConfigFromDatabase attempts to load the most recent effective config
// snapshot from the software_configs database. Returns nil, nil if no snapshot exists.
func tryRestoreConfigFromDatabase() (*Config, error) {
	store := storage.NewSoftwareConfigStore()
	if store == nil {
		return nil, fmt.Errorf("config store is nil")
	}

	snapshot, err := store.GetLatestEffectiveConfigSnapshot()
	if err != nil {
		return nil, fmt.Errorf("failed to query latest snapshot: %w", err)
	}
	if snapshot == nil {
		return nil, nil
	}

	var cfg config.Config
	if err := json.Unmarshal([]byte(snapshot.SnapshotJSON), &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse snapshot json: %w", err)
	}

	logger.Debug(fmt.Sprintf("Configuration restored from database snapshot (id=%d, created_at=%v)", snapshot.ID, snapshot.CreatedAt))
	return &cfg, nil
}

// LoadConfig 从文件加载配置
func LoadConfig(configPath string) (*Config, error) {
	data, err := ioutil.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var config Config
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	return &config, nil
}

// LoadConfigFromBytes 从字节数据加载配置
func LoadConfigFromBytes(data []byte) (*Config, error) {
	var config Config
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}

	return &config, nil
}

// ApplyConfig applies the configuration to the system in clear phases.
//
// Phases:
// 1. Apply engine configuration (network stack, TUN device, etc.)
// 2. Register built-in proxies (direct + aliang) - always available
// 3. Set the active default proxy for routing
func ApplyConfig(cfg *Config) error {
	// Validate configuration
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("configuration validation failed: %w", err)
	}

	// Store config globally for access by other modules
	config.SetGlobalConfig(cfg)

	// Initialize traffic mirror forwarder (no-op if mirror is disabled)
	mirror.InitGlobalForwarder()

	// Phase 1: Register built-in proxies (direct + aliang)
	// These are mandatory and always available
	if err := registerBuiltinProxies(cfg); err != nil {
		return fmt.Errorf("phase 1 - builtin proxies registration failed: %w", err)
	}
	logger.Debug("Phase 1: Built-in proxies registered")

	// Phase 2: Register optional SOCKS proxy (if configured)
	if err := registerSocksProxy(cfg); err != nil {
		return fmt.Errorf("phase 2 - socks proxy registration failed: %w", err)
	}
	logger.Debug("Phase 2: Optional SOCKS proxy registered")

	// Phase 3: Set the active default proxy for routing decisions
	// Determines which proxy is used when no specific routing rule applies
	if err := setEffectiveDefaultProxy(cfg.EffectiveDefaultProxy()); err != nil {
		return fmt.Errorf("phase 3 - failed to set default proxy: %w", err)
	}
	logger.Debug("Phase 3: Default proxy set for routing")

	// Phase 4: Initialize global DNS resolver
	// Must be done after proxies are registered
	registry := outbound.GetRegistry()
	primaryProxy, _ := registry.Get("socks")
	directProxy, _ := registry.Get("direct")
	if err := dns.InitGlobalResolver(primaryProxy, directProxy, cfg); err != nil {
		logger.Warn(fmt.Sprintf("Phase 4 - Failed to initialize DNS resolver: %v", err))
	} else {
		logger.Debug("Phase 4: Global DNS resolver initialized")
	}

	logger.Info("Configuration applied successfully")
	return nil
}

// setEffectiveDefaultProxy sets the active default proxy.
func setEffectiveDefaultProxy(currentProxy string) error {
	registry := outbound.GetRegistry()

	if currentProxy == "" || currentProxy == "auto" {
		currentProxy = "direct"
	}

	if currentProxy != "direct" && currentProxy != "aliang" && currentProxy != "socks" {
		logger.Warn(fmt.Sprintf("Unsupported currentProxy '%s', fallback to direct", currentProxy))
		currentProxy = "direct"
	}

	if _, err := registry.Get(currentProxy); err != nil {
		logger.Warn(fmt.Sprintf("Configured proxy '%s' not found: %v, fallback to direct", currentProxy, err))
		currentProxy = "direct"
	}

	logger.Debug(fmt.Sprintf("Using proxy: %s", currentProxy))
	return nil
}

// registerBuiltinProxies 注册内置代理（direct 和 aliang）
func registerBuiltinProxies(cfg *Config) error {
	registry := outbound.GetRegistry()

	// 1. 注册 direct 代理
	if err := registry.RegisterDefault(); err != nil {
		return fmt.Errorf("failed to register direct proxy: %w", err)
	}

	// 2. 注册 aliang 代理
	coreServer := cfg.EffectiveAliangCoreServer()

	if err := registry.RegisterAliang(coreServer); err != nil {
		return fmt.Errorf("failed to register aliang proxy: %w", err)
	}

	return nil
}

// registerSocksProxy registers optional SOCKS5 proxy
func registerSocksProxy(cfg *config.Config) error {
	socksCfg, err := cfg.EffectiveSocksProxy()
	if err != nil {
		return fmt.Errorf("invalid customer socks5 proxy config: %w", err)
	}
	if socksCfg == nil {
		logger.Debug("No socks proxy configured")
		return nil
	}

	// Validate config (already validated in cfg.Validate, but keep safe)
	if err := socksCfg.Validate(); err != nil {
		return fmt.Errorf("invalid socks proxy config: %w", err)
	}

	addr := fmt.Sprintf("%s:%d", socksCfg.Server, socksCfg.ServerPort)
	proxyInstance := outbound.GetRegistry()

	socksProxy, err := outbound.CreateSocksProxy(addr, socksCfg.Username, socksCfg.Password)
	if err != nil {
		return fmt.Errorf("failed to create socks proxy: %w", err)
	}

	if err := proxyInstance.Register("socks", socksProxy); err != nil {
		return fmt.Errorf("failed to register socks proxy: %w", err)
	}

	logger.Debug(fmt.Sprintf("SOCKS proxy registered at %s", addr))
	return nil
}

// LoadAndApplyConfig 加载并应用配置文件
// 注意：如果用户显式指定了配置文件路径但文件不存在，应返回错误
func LoadAndApplyConfig(configPath string) error {
	// 检查文件是否存在
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		return fmt.Errorf("config file not found: %s", configPath)
	}

	// 加载配置
	cfg, err := LoadConfig(configPath)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// 应用配置
	if err := ApplyConfig(cfg); err != nil {
		return fmt.Errorf("failed to apply config: %w", err)
	}

	// 标记使用的是自定义配置（非默认配置）
	setUseDefaultConfig(false)

	logger.Info(fmt.Sprintf("Config loaded and applied successfully from: %s", configPath))
	return nil
}
