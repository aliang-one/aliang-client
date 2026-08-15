package handlers

import (
	"encoding/json"
	"net/http"
	"strings"

	"aliang.one/nursorgate/app/http/common"
	"aliang.one/nursorgate/processor/config"
	"aliang.one/nursorgate/processor/geoip"
	"aliang.one/nursorgate/processor/routing"
	"aliang.one/nursorgate/processor/rules"
)

// RulesHandler handles HTTP requests for routing rules operations
type RulesHandler struct{}

// NewRulesHandler creates a new rules handler instance
func NewRulesHandler() *RulesHandler {
	return &RulesHandler{}
}

// HandleGetGeoIPStatus handles GET /api/rules/geoip/status
// Returns the current status of the GeoIP service
func (rh *RulesHandler) HandleGetGeoIPStatus(w http.ResponseWriter, r *http.Request) {
	service := geoip.GetService()

	status := map[string]interface{}{
		"enabled":      service.IsEnabled(),
		"databasePath": service.GetDatabasePath(),
	}

	common.Success(w, status)
}

// HandleGeoIPLookup handles POST /api/rules/geoip/lookup
// Performs a GeoIP lookup for the provided IP address
func (rh *RulesHandler) HandleGeoIPLookup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IP string `json:"ip"`
	}

	if err := common.DecodeJSON(r.Body, &req); err != nil {
		common.ErrorBadRequest(w, "Invalid request format", nil)
		return
	}

	if req.IP == "" {
		common.ErrorBadRequest(w, "IP address is required", nil)
		return
	}

	// Parse IP
	ip := common.ParseIP(req.IP)
	if ip == nil {
		common.ErrorBadRequest(w, "Invalid IP address format", nil)
		return
	}

	// Lookup country
	service := geoip.GetService()
	if !service.IsEnabled() {
		common.ErrorServiceUnavailable(w, "GeoIP service is not enabled")
		return
	}

	country, err := service.LookupCountry(ip)
	if err != nil {
		common.ErrorInternalServer(w, "GeoIP lookup failed: "+err.Error(), nil)
		return
	}

	data := map[string]interface{}{
		"ip":      req.IP,
		"country": country.ISOCode,
		"name":    country.Name,
		"isChina": country.ISOCode == "CN",
	}

	common.Success(w, data)
}

// HandleGetCacheStats handles GET /api/rules/cache/stats
// Returns cache statistics (hit rate, size, etc.)
func (rh *RulesHandler) HandleGetCacheStats(w http.ResponseWriter, r *http.Request) {
	engine := rules.GetEngine()
	if engine == nil {
		common.ErrorNotFound(w, "Rule engine not initialized")
		return
	}

	stats := engine.GetCacheStats()
	common.Success(w, stats)
}

// HandleClearCache handles POST /api/rules/cache/clear
// Clears all cached routing decisions
func (rh *RulesHandler) HandleClearCache(w http.ResponseWriter, r *http.Request) {
	engine := rules.GetEngine()
	if engine == nil {
		common.ErrorNotFound(w, "Rule engine not initialized")
		return
	}

	engine.ClearCache()

	common.Success(w, map[string]string{
		"status": "cache cleared successfully",
	})
}

// HandleGetRuleEngineStatus handles GET /api/rules/engine/status
// Returns the overall status of the rule engine
func (rh *RulesHandler) HandleGetRuleEngineStatus(w http.ResponseWriter, r *http.Request) {
	engine := rules.GetEngine()
	geoipService := geoip.GetService()
	switchStatus := activeSwitchStatus()

	status := map[string]interface{}{
		"engineEnabled":    engine != nil && engine.IsEnabled(),
		"geoipEnabled":     geoipService != nil && geoipService.IsEnabled(),
		"aliangEnabled":    switchStatus.AliangEnabled,
		"socksEnabled":     switchStatus.SocksEnabled,
		"geoipRuleEnabled": switchStatus.GeoIPEnabled,
	}

	if engine != nil && engine.IsEnabled() {
		stats := engine.GetCacheStats()
		status["cache"] = stats
	}

	common.Success(w, status)
}

// HandleEnableRuleEngine handles POST /api/rules/engine/enable
// Enables the rule engine
func (rh *RulesHandler) HandleEnableRuleEngine(w http.ResponseWriter, r *http.Request) {
	engine := rules.GetEngine()
	if engine == nil {
		common.ErrorNotFound(w, "Rule engine not initialized")
		return
	}

	engine.Enable()

	common.Success(w, map[string]interface{}{
		"status":  "success",
		"enabled": true,
	})
}

// HandleDisableRuleEngine handles POST /api/rules/engine/disable
// Disables the rule engine
func (rh *RulesHandler) HandleDisableRuleEngine(w http.ResponseWriter, r *http.Request) {
	engine := rules.GetEngine()
	if engine == nil {
		common.ErrorNotFound(w, "Rule engine not initialized")
		return
	}

	engine.Disable()

	common.Success(w, map[string]interface{}{
		"status":  "success",
		"enabled": false,
	})
}

// T040: HandleGetGlobalSwitchStatus handles GET /api/rules/switches/status
// Returns the status of global routing switches
func (rh *RulesHandler) HandleGetGlobalSwitchStatus(w http.ResponseWriter, r *http.Request) {
	status := activeSwitchStatus()

	common.Success(w, map[string]interface{}{
		"aliangEnabled": status.AliangEnabled,
		"socksEnabled":  status.SocksEnabled,
		"geoipEnabled":  status.GeoIPEnabled,
	})
}

// T041: HandleSetGlobalSwitch handles POST /api/rules/switches/{switch_name}
// Controls individual global switches (aliang, socks, geoip)
func (rh *RulesHandler) HandleSetGlobalSwitch(w http.ResponseWriter, r *http.Request) {
	// Parse request body
	var req struct {
		Enabled bool `json:"enabled"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		common.ErrorBadRequest(w, "Invalid JSON format", map[string]interface{}{
			"error": err.Error(),
		})
		return
	}

	// Extract switch name from URL path
	// Expected: /api/rules/switches/{switch_name}
	switchName := r.URL.Query().Get("switch")
	if switchName == "" {
		// Try to get it from path
		pathParts := r.URL.Path
		// Simple parsing: /api/rules/switches/aliang -> aliang
		if len(pathParts) > 0 {
			parts := strings.Split(pathParts, "/")
			if len(parts) > 0 {
				switchName = parts[len(parts)-1]
			}
		}
	}

	switch switchName {
	case "aliang", "socks", "geoip":
		common.ErrorBadRequest(w, "Legacy mutable switch write path is removed; update canonical routing config via /api/config/routing", map[string]interface{}{
			"switch":  switchName,
			"enabled": req.Enabled,
		})
	default:
		common.ErrorBadRequest(w, "Invalid switch name. Must be one of: aliang, socks, geoip", map[string]interface{}{
			"provided": switchName,
		})
	}
}

// T042: HandleBulkSwitchControl handles POST /api/rules/switches/bulk
// Controls multiple switches at once
func (rh *RulesHandler) HandleBulkSwitchControl(w http.ResponseWriter, r *http.Request) {
	// Parse request body
	var req struct {
		AliangEnabled *bool `json:"aliang_enabled"`
		SocksEnabled  *bool `json:"socks_enabled"`
		GeoIPEnabled  *bool `json:"geoip_enabled"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		common.ErrorBadRequest(w, "Invalid JSON format", map[string]interface{}{
			"error": err.Error(),
		})
		return
	}

	status := activeSwitchStatus()
	common.ErrorBadRequest(w, "Legacy mutable switch bulk write path is removed; update canonical routing config via /api/config/routing", map[string]interface{}{
		"status":        "rejected",
		"aliangEnabled": status.AliangEnabled,
		"socksEnabled":  status.SocksEnabled,
		"geoipEnabled":  status.GeoIPEnabled,
	})
}

func activeSwitchStatus() struct {
	AliangEnabled bool
	SocksEnabled  bool
	GeoIPEnabled  bool
} {
	canonical := config.GetRoutingApplyStore().ActiveCanonicalSchema()
	if canonical == nil {
		return struct {
			AliangEnabled bool
			SocksEnabled  bool
			GeoIPEnabled  bool
		}{
			AliangEnabled: true,
			SocksEnabled:  true,
			GeoIPEnabled:  false,
		}
	}

	return struct {
		AliangEnabled bool
		SocksEnabled  bool
		GeoIPEnabled  bool
	}{
		AliangEnabled: canonical.Egress.ToAliang.Enabled,
		SocksEnabled:  canonical.Egress.ToSocks.Enabled,
		GeoIPEnabled:  false,
	}
}

// T067: HandleGeoIPLookupAdvanced handles POST /api/rules/geoip/lookup
// Performs GeoIP lookup with cache support
func (rh *RulesHandler) HandleGeoIPLookupAdvanced(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IP string `json:"ip"`
	}

	if err := common.DecodeJSON(r.Body, &req); err != nil {
		common.ErrorBadRequest(w, "Invalid request format", nil)
		return
	}

	if req.IP == "" {
		common.ErrorBadRequest(w, "IP address is required", nil)
		return
	}

	// Use GeoIP cache for lookup
	cache := routing.GetDefaultCache()
	if !cache.IsEnabled() {
		common.ErrorServiceUnavailable(w, "GeoIP service is not enabled")
		return
	}

	country, err := cache.Lookup(req.IP)
	if err != nil {
		common.ErrorInternalServer(w, "GeoIP lookup failed: "+err.Error(), nil)
		return
	}

	data := map[string]interface{}{
		"ip":      req.IP,
		"country": country.ISOCode,
		"name":    country.Name,
		"isChina": country.ISOCode == "CN",
	}

	common.Success(w, data)
}

// T068: HandleClearGeoIPCache handles POST /api/rules/cache/clear/geoip
// Clears the GeoIP cache
func (rh *RulesHandler) HandleClearGeoIPCache(w http.ResponseWriter, r *http.Request) {
	cache := routing.GetDefaultCache()
	oldSize := cache.Size()

	cache.Clear()

	common.Success(w, map[string]interface{}{
		"status":          "success",
		"message":         "GeoIP cache cleared successfully",
		"entries_cleared": oldSize,
	})
}

// T070: HandleGetGeoIPCacheStats handles GET /api/rules/geoip/cache-stats
// Returns GeoIP cache statistics
func (rh *RulesHandler) HandleGetGeoIPCacheStats(w http.ResponseWriter, r *http.Request) {
	cache := routing.GetDefaultCache()
	stats := cache.GetStats()

	common.Success(w, stats)
}

// T069: HandleUpdateGeoIPDatabase handles POST /api/rules/geoip/update
// Updates the GeoIP database (stub for now - requires download implementation)
func (rh *RulesHandler) HandleUpdateGeoIPDatabase(w http.ResponseWriter, r *http.Request) {
	common.Success(w, map[string]interface{}{
		"status":  "not_implemented",
		"message": "GeoIP database update is not yet implemented. Manual database update required.",
	})
}
