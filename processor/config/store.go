package config

import (
	"fmt"
	"sync"
)

// ConfigStore stores proxy configurations (not instances)
type ConfigStore struct {
	mu      sync.RWMutex
	configs map[string]*BaseProxyConfig
}

var (
	globalConfigStore *ConfigStore
	configStoreOnce   sync.Once
)

// GetConfigStore returns the global config store singleton
func GetConfigStore() *ConfigStore {
	configStoreOnce.Do(func() {
		globalConfigStore = &ConfigStore{
			configs: make(map[string]*BaseProxyConfig),
		}
	})
	return globalConfigStore
}

// Set stores a proxy configuration
func (s *ConfigStore) Set(name string, cfg *BaseProxyConfig) error {
	if name == "" {
		return fmt.Errorf("proxy name cannot be empty")
	}
	if cfg == nil {
		return fmt.Errorf("config cannot be nil")
	}

	// Validate config before storing
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Create a deep copy to prevent external modification
	cfgCopy := *cfg

	s.configs[name] = &cfgCopy
	return nil
}

// Get retrieves a proxy configuration
func (s *ConfigStore) Get(name string) (*BaseProxyConfig, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	cfg, exists := s.configs[name]
	if !exists {
		return nil, fmt.Errorf("config '%s' not found", name)
	}

	// Return a copy to prevent external modification
	cfgCopy := *cfg

	return &cfgCopy, nil
}

// List returns all config names
func (s *ConfigStore) List() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	names := make([]string, 0, len(s.configs))
	for name := range s.configs {
		names = append(names, name)
	}
	return names
}

// Delete removes a config
func (s *ConfigStore) Delete(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.configs[name]; !exists {
		return fmt.Errorf("config '%s' not found", name)
	}

	delete(s.configs, name)
	return nil
}

// Clear removes all configs
func (s *ConfigStore) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.configs = make(map[string]*BaseProxyConfig)
}

// GetAll returns all configs (for debugging/listing)
func (s *ConfigStore) GetAll() map[string]*BaseProxyConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make(map[string]*BaseProxyConfig, len(s.configs))
	for name, cfg := range s.configs {
		// Deep copy
		cfgCopy := *cfg
		result[name] = &cfgCopy
	}
	return result
}

// GetProxyConfigInfo retrieves complete proxy configuration information.
func GetProxyConfigInfo(proxyName string) (map[string]interface{}, error) {
	store := GetConfigStore()
	cfg, err := store.Get(proxyName)
	if err != nil {
		return nil, fmt.Errorf("proxy config not found: %w", err)
	}

	result := map[string]interface{}{
		"name":   proxyName,
		"type":   cfg.Type,
		"config": cfg,
		"source": "configuration",
	}

	return result, nil
}
