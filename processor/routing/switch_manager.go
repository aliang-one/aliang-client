package routing

import (
	"sync"

	"aliang.one/nursorgate/common/logger"
	"aliang.one/nursorgate/common/model"
)

// SwitchManager manages the global routing switches
type SwitchManager struct {
	mu            sync.RWMutex
	aliangEnabled bool
	socksEnabled  bool
	geoIPEnabled  bool
	lastUpdatedAt int64
	enabledAt     map[string]int64 // Track when each switch was last changed
}

var (
	defaultSwitchManager *SwitchManager
	switchOnce           sync.Once
)

// GetSwitchManager returns the singleton SwitchManager instance
func GetSwitchManager() *SwitchManager {
	switchOnce.Do(func() {
		defaultSwitchManager = &SwitchManager{
			aliangEnabled: true,
			socksEnabled:  true,
			geoIPEnabled:  false,
			enabledAt:     make(map[string]int64),
		}
	})
	return defaultSwitchManager
}

// UpdateSwitches updates the global switches based on configuration
func (sm *SwitchManager) UpdateSwitches(config *model.RoutingRulesConfig) {
	if config == nil {
		logger.Warn("Cannot update switches: config is nil")
		return
	}

	sm.mu.Lock()
	defer sm.mu.Unlock()

	settings := config.Settings

	if sm.aliangEnabled != settings.AliangEnabled {
		if settings.AliangEnabled {
			logger.Debug("Global switch: Aliang ENABLED")
		} else {
			logger.Debug("Global switch: Aliang DISABLED")
		}
		sm.aliangEnabled = settings.AliangEnabled
	}

	if sm.socksEnabled != settings.SocksEnabled {
		if settings.SocksEnabled {
			logger.Debug("Global switch: SOCKS ENABLED")
		} else {
			logger.Debug("Global switch: SOCKS DISABLED")
		}
		sm.socksEnabled = settings.SocksEnabled
	}

	if sm.geoIPEnabled != settings.GeoIPEnabled {
		if settings.GeoIPEnabled {
			logger.Debug("Global switch: GeoIP ENABLED")
		} else {
			logger.Debug("Global switch: GeoIP DISABLED")
		}
		sm.geoIPEnabled = settings.GeoIPEnabled
	}
}

// IsAliangEnabled returns the Aliang switch status
func (sm *SwitchManager) IsAliangEnabled() bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.aliangEnabled
}

// IsSocksEnabled returns the SOCKS switch status
func (sm *SwitchManager) IsSocksEnabled() bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.socksEnabled
}

// IsGeoIPEnabled returns the GeoIP switch status
func (sm *SwitchManager) IsGeoIPEnabled() bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.geoIPEnabled
}

// SetAliangEnabled sets the Aliang switch
func (sm *SwitchManager) SetAliangEnabled(enabled bool) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if sm.aliangEnabled != enabled {
		sm.aliangEnabled = enabled
		if enabled {
			logger.Debug("Global switch: Aliang ENABLED (manual)")
		} else {
			logger.Debug("Global switch: Aliang DISABLED (manual)")
		}
	}
}

// SetSocksEnabled sets the SOCKS switch
func (sm *SwitchManager) SetSocksEnabled(enabled bool) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if sm.socksEnabled != enabled {
		sm.socksEnabled = enabled
		if enabled {
			logger.Debug("Global switch: SOCKS ENABLED (manual)")
		} else {
			logger.Debug("Global switch: SOCKS DISABLED (manual)")
		}
	}
}

// SetGeoIPEnabled sets the GeoIP switch
func (sm *SwitchManager) SetGeoIPEnabled(enabled bool) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if sm.geoIPEnabled != enabled {
		sm.geoIPEnabled = enabled
		if enabled {
			logger.Debug("Global switch: GeoIP ENABLED (manual)")
		} else {
			logger.Debug("Global switch: GeoIP DISABLED (manual)")
		}
	}
}

// GetStatus returns current switch status as a model struct
func (sm *SwitchManager) GetStatus() model.RulesSettings {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	return model.RulesSettings{
		AliangEnabled: sm.aliangEnabled,
		SocksEnabled:  sm.socksEnabled,
		GeoIPEnabled:  sm.geoIPEnabled,
	}
}

// DisableAll disables all switches
func (sm *SwitchManager) DisableAll() {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	sm.aliangEnabled = false
	sm.socksEnabled = false
	sm.geoIPEnabled = false
	logger.Debug("Global switches: ALL DISABLED")
}

// EnableAll enables all switches
func (sm *SwitchManager) EnableAll() {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	sm.aliangEnabled = true
	sm.socksEnabled = true
	sm.geoIPEnabled = true
	logger.Debug("Global switches: ALL ENABLED")
}

// ResetToDefaults resets switches to default state
func (sm *SwitchManager) ResetToDefaults() {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	sm.aliangEnabled = true
	sm.socksEnabled = true
	sm.geoIPEnabled = false
	logger.Debug("Global switches: RESET to defaults (Aliang=ON, SOCKS=ON, GeoIP=OFF)")
}
