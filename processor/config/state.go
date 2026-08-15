package config

import "sync"

// ConfigState tracks the source of the current configuration
type ConfigState struct {
	mu                 sync.RWMutex
	usingDefaultConfig bool
	hasLocalUserInfo   bool
}

var (
	globalConfigState *ConfigState
	configStateOnce   sync.Once
)

// GetConfigState returns the global config state singleton
func GetConfigState() *ConfigState {
	configStateOnce.Do(func() {
		globalConfigState = &ConfigState{
			usingDefaultConfig: false,
		}
	})
	return globalConfigState
}

// SetUsingDefaultConfig marks whether the default embedded configuration is being used
func SetUsingDefaultConfig(value bool) {
	state := GetConfigState()
	state.mu.Lock()
	defer state.mu.Unlock()
	state.usingDefaultConfig = value
}

// IsUsingDefaultConfig returns whether the default embedded configuration is being used
func IsUsingDefaultConfig() bool {
	state := GetConfigState()
	state.mu.RLock()
	defer state.mu.RUnlock()
	return state.usingDefaultConfig
}

// SetHasLocalUserInfo marks whether valid local user information exists
func SetHasLocalUserInfo(value bool) {
	state := GetConfigState()
	state.mu.Lock()
	defer state.mu.Unlock()
	state.hasLocalUserInfo = value
}

// HasLocalUserInfo returns whether valid local user information exists
func HasLocalUserInfo() bool {
	state := GetConfigState()
	state.mu.RLock()
	defer state.mu.RUnlock()
	return state.hasLocalUserInfo
}
