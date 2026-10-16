package routing

import (
	"sync"

	"aliang.one/nursorgate/common/logger"
	"aliang.one/nursorgate/common/model"
	"aliang.one/nursorgate/processor/config"
)

// SwitchManager manages the global routing switches
//
// Deprecated: 本结构体已不再是路由开关的事实源。运行时唯一事实源是
// config.GetRoutingApplyStore() 的 ActiveCanonicalSchema（egress 三段开关，
// 由 /api/config/routing 提交并持久化到 <state>/routing.json）。字段与
// setter 仅为源码兼容保留（当前零生产调用者）；读取开关状态请用
// EffectiveSwitchStatus。
type SwitchManager struct {
	mu            sync.RWMutex
	aliangEnabled bool
	socksEnabled  bool
	// geoIPEnabled 为兼容保留的内存态字段：geoip 规则生效与否由「规则存在 + 数据库已加载」
	// 决定，本字段不参与任何行为分支（历史遗留，勿新增读取）。
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
//
// Deprecated: 开关事实源已迁移至 routing apply store（canonical egress 段），
// 本方法不再有生产调用者；开关变更请提交 /api/config/routing。
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
//
// Deprecated: 只改本结构体内存态，不影响 ActiveCanonicalSchema 派生的真实
// 开关状态（当前零生产调用者）。开关变更请提交 /api/config/routing。
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
//
// Deprecated: 只改本结构体内存态，不影响 ActiveCanonicalSchema 派生的真实
// 开关状态（当前零生产调用者）。开关变更请提交 /api/config/routing。
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
//
// Deprecated: geoip 生效与否由「规则存在 + 数据库已加载」决定，本 setter 无行为影响。
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

// GetStatus returns current switch status as a model struct.
// 回显不撒谎：store 存在 canonical 时返回从 egress 段派生的真实状态
// （与决策层/执行层同一事实源）；store 为空时回落内存默认值。
func (sm *SwitchManager) GetStatus() model.RulesSettings {
	return EffectiveSwitchStatus()
}

// EffectiveSwitchStatus 返回决策层编译运行时快照应使用的开关状态——从单一
// 事实源派生：
//   - store 有 canonical：AliangEnabled/SocksEnabled 取自 egress.toAliang /
//     egress.toSocks（无拷贝读，适合每连接调用）；
//   - store 为空：回落 SwitchManager 内存态（即既有默认行为 true/true）。
//
// GeoIPEnabled 为历史兼容序列化字段，不参与行为分支，恒取内存值。
func EffectiveSwitchStatus() model.RulesSettings {
	sm := GetSwitchManager()
	aliangEnabled, socksEnabled, ok := config.GetRoutingApplyStore().ActiveEgressSwitches()
	if ok {
		return model.RulesSettings{
			AliangEnabled: aliangEnabled,
			SocksEnabled:  socksEnabled,
			GeoIPEnabled:  sm.IsGeoIPEnabled(),
		}
	}

	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return model.RulesSettings{
		AliangEnabled: sm.aliangEnabled,
		SocksEnabled:  sm.socksEnabled,
		GeoIPEnabled:  sm.geoIPEnabled,
	}
}

// DisableAll disables all switches
//
// Deprecated: 只改本结构体内存态（当前零生产调用者）。
func (sm *SwitchManager) DisableAll() {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	sm.aliangEnabled = false
	sm.socksEnabled = false
	sm.geoIPEnabled = false
	logger.Debug("Global switches: ALL DISABLED")
}

// EnableAll enables all switches
//
// Deprecated: 只改本结构体内存态（当前零生产调用者）。
func (sm *SwitchManager) EnableAll() {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	sm.aliangEnabled = true
	sm.socksEnabled = true
	sm.geoIPEnabled = true
	logger.Debug("Global switches: ALL ENABLED")
}

// ResetToDefaults resets switches to default state
//
// Deprecated: 只改本结构体内存态（当前零生产调用者）。
func (sm *SwitchManager) ResetToDefaults() {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	sm.aliangEnabled = true
	sm.socksEnabled = true
	sm.geoIPEnabled = false
	logger.Debug("Global switches: RESET to defaults (Aliang=ON, SOCKS=ON, GeoIP=OFF)")
}
