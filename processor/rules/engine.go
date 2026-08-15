package rules

import (
	"fmt"
	"net"
	"sync"
	"time"

	"aliang.one/nursorgate/common/logger"
	M "aliang.one/nursorgate/inbound/tun/metadata"
	"aliang.one/nursorgate/processor/cache"
	"aliang.one/nursorgate/processor/config"
	"aliang.one/nursorgate/processor/geoip"
	"aliang.one/nursorgate/processor/routing"
)

// RuleEngine evaluates routing rules in priority order
type RuleEngine struct {
	mu            sync.RWMutex
	geoipService  *geoip.Service
	ipDomainCache *cache.IPDomainCache
	chinaDirect   bool // Whether Chinese IPs should route directly
	enabled       bool // Whether rule engine is enabled
}

var (
	defaultEngine *RuleEngine
	engineOnce    sync.Once
)

// GetEngine returns the singleton rule engine instance
func GetEngine() *RuleEngine {
	engineOnce.Do(func() {
		defaultEngine = &RuleEngine{
			enabled: false,
		}
	})
	return defaultEngine
}

// GetCache returns the IP-Domain cache from the singleton rule engine
func GetCache() *cache.IPDomainCache {
	engine := GetEngine()
	if engine != nil {
		engine.mu.RLock()
		defer engine.mu.RUnlock()
		return engine.ipDomainCache
	}
	return nil
}

// Initialize initializes the rule engine with configuration
// TODO(US2): Full implementation will be completed in Phase 4 - User Story 2
func (e *RuleEngine) Initialize(cfg *config.Config) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	_ = cfg

	// Initialize GeoIP service reference (optional)
	e.geoipService = geoip.GetService()

	// Initialize IP-domain cache with safe defaults
	if e.ipDomainCache == nil {
		e.ipDomainCache = cache.NewIPDomainCache(10000, 5*time.Minute)
	}

	e.enabled = true
	logger.Info("Rule engine initialized successfully")
	return nil
}

// EvaluateRoute evaluates routing rules for a connection
// Priority order:
// 1. IP-Domain cache (previously evaluated routes)
// 2. SNI allowlist (MITM to Aliang)
// 3. GeoIP rules (optional)
// 4. Default (SOCKS if configured, otherwise direct)
func (e *RuleEngine) EvaluateRoute(ctx *EvaluationContext) (*RuleResult, error) {
	if !e.enabled {
		return &RuleResult{
			Route:       cache.RouteDirect,
			RequiresSNI: ctx.DstPort == 443, // TLS traffic may need SNI
			Reason:      "rule engine disabled",
		}, nil
	}

	// TODO(US2): Implement routing decision logic with priority: Aliang > SOCKS  > Direct

	snapshot, err := e.compileRuntimeSnapshot()
	if err != nil {
		return nil, err
	}

	// Priority 2: Check cache (avoid repeated SNI extraction)
	if result := e.checkCache(ctx); result != nil {
		return result, nil
	}

	// Priority 3: Check allowlist
	if ctx.Domain != "" {
		if result := e.checkAllowlist(ctx); result != nil {
			e.cacheResult(ctx, result)
			return result, nil
		}
	}

	// Priority 4: Check GeoIP (country-based routing, optional)
	if result := e.checkGeoIP(ctx); result != nil {
		e.cacheResult(ctx, result)
		return result, nil
	}

	result := e.evaluateWithSnapshot(snapshot, ctx)
	e.cacheResult(ctx, result)
	return result, nil
}

// checkBypassRules removed - will be reimplemented as part of routing decision engine in US2
/*
func (e *RuleEngine) checkBypassRules(ctx *EvaluationContext) *RuleResult {
	// TODO(US2): Reimplement this with new model
	return nil
}
*/

// checkCache checks if the routing decision is cached
func (e *RuleEngine) checkCache(ctx *EvaluationContext) *RuleResult {
	if e.ipDomainCache == nil {
		return nil
	}

	e.mu.RLock()
	defer e.mu.RUnlock()

	// Try domain-based lookup first
	if ctx.Domain != "" {
		if entry, ok := e.ipDomainCache.Get(ctx.Domain); ok {
			return &RuleResult{
				Route:       entry.Route,
				MatchedRule: "cache_domain",
				RequiresSNI: false,
				Reason:      fmt.Sprintf("Cache hit for domain %s", ctx.Domain),
			}
		}
	}

	// Try IP-based lookup
	ipKey := ctx.DstIP.String()
	if entry, ok := e.ipDomainCache.Get(ipKey); ok {
		return &RuleResult{
			Route:       entry.Route,
			MatchedRule: "cache_ip",
			RequiresSNI: false,
			Reason:      fmt.Sprintf("Cache hit for IP %s", ctx.DstIP),
		}
	}

	return nil
}

// checkAllowlist checks local SNI allowlist and default SOCKS fallback.
func (e *RuleEngine) checkAllowlist(ctx *EvaluationContext) *RuleResult {
	if ctx.Domain == "" {
		return nil
	}

	snapshot, err := e.compileRuntimeSnapshot()
	if err != nil {
		return nil
	}

	for _, rule := range snapshot.Rules() {
		if !rule.Enabled() {
			continue
		}
		if rule.Type() != "domain" {
			continue
		}
		if rule.Target() != routing.SnapshotActionToAliang {
			continue
		}
		if MatchDomain(rule.Condition(), ctx.Domain) {
			return &RuleResult{
				Route:       cache.RouteToALiang,
				MatchedRule: "snapshot_allowlist",
				RequiresSNI: false,
				Reason:      fmt.Sprintf("Domain %s matched snapshot allowlist", ctx.Domain),
			}
		}
	}

	if snapshot.BranchCapabilities().ToSocks() {
		return &RuleResult{
			Route:       cache.RouteToSocks,
			MatchedRule: "snapshot_default_socks",
			RequiresSNI: false,
			Reason:      "Default route to SOCKS proxy from snapshot",
		}
	}

	return nil
}

// checkGeoIP checks country-based routing using GeoIP database
func (e *RuleEngine) checkGeoIP(ctx *EvaluationContext) *RuleResult {
	if e.geoipService == nil || !e.geoipService.IsEnabled() {
		return nil
	}

	e.mu.RLock()
	defer e.mu.RUnlock()

	// Convert netip.Addr to net.IP
	ip := net.IP(ctx.DstIP.AsSlice())

	// Check if IP is in China
	isChina := e.geoipService.IsChina(ip)

	if isChina {
		// Chinese IP handling
		if e.chinaDirect {
			return &RuleResult{
				Route:       cache.RouteDirect,
				MatchedRule: "geoip_china",
				RequiresSNI: false,
				Reason:      fmt.Sprintf("IP %s is in China (direct route)", ctx.DstIP),
			}
		} else {
			return &RuleResult{
				Route:       cache.RouteToSocks,
				MatchedRule: "geoip_china",
				RequiresSNI: false,
				Reason:      fmt.Sprintf("IP %s is in China (accelerated route)", ctx.DstIP),
			}
		}
	}

	// Foreign IP - accelerate via SOCKS proxy
	return &RuleResult{
		Route:       cache.RouteToSocks,
		MatchedRule: "geoip_foreign",
		RequiresSNI: false,
		Reason:      fmt.Sprintf("IP %s is outside China (accelerated)", ctx.DstIP),
	}
}

// defaultRoute provides fallback routing decision
func (e *RuleEngine) defaultRoute(ctx *EvaluationContext) *RuleResult {
	// For TLS traffic (port 443) without domain, we need SNI extraction
	requiresSNI := ctx.DstPort == 443 && ctx.Domain == ""
	defaultRoute := cache.RouteDirect

	return &RuleResult{
		Route:       defaultRoute,
		MatchedRule: "default",
		RequiresSNI: requiresSNI,
		Reason:      "No rules matched, using default route",
	}
}

func (e *RuleEngine) compileRuntimeSnapshot() (*routing.RuntimeSnapshot, error) {
	switchStatus := routing.GetSwitchManager().GetStatus()
	snapshot, err := routing.CompileRuntimeSnapshotFromRuntimeInputs(config.GetGlobalConfig(), switchStatus)
	if err != nil {
		return nil, fmt.Errorf("compile runtime snapshot failed: %w", err)
	}
	return snapshot, nil
}

func (e *RuleEngine) evaluateWithSnapshot(snapshot *routing.RuntimeSnapshot, ctx *EvaluationContext) *RuleResult {
	routeCtx := &routing.MatchContext{
		Domain: ctx.Domain,
	}
	if ctx.DstIP.IsValid() && !ctx.DstIP.IsUnspecified() {
		routeCtx.IP = ctx.DstIP.String()
	}

	decision, err := routing.DecideRouteFromSnapshot(snapshot, routeCtx)
	if err != nil {
		return e.defaultRoute(ctx)
	}

	requiresSNI := ctx.DstPort == 443 && ctx.Domain == ""
	switch decision {
	case routing.RouteToAliang:
		return &RuleResult{
			Route:       cache.RouteToALiang,
			MatchedRule: "snapshot",
			RequiresSNI: requiresSNI,
			Reason:      "snapshot decision: toAliang",
		}
	case routing.RouteToSocks:
		return &RuleResult{
			Route:       cache.RouteToSocks,
			MatchedRule: "snapshot",
			RequiresSNI: requiresSNI,
			Reason:      "snapshot decision: toSocks",
		}
	case routing.RouteDeny:
		return &RuleResult{
			Route:       cache.RouteDeny,
			MatchedRule: "snapshot_deny",
			RequiresSNI: false,
			Reason:      "snapshot decision: deny",
		}
	default:
		return &RuleResult{
			Route:       cache.RouteDirect,
			MatchedRule: "snapshot",
			RequiresSNI: requiresSNI,
			Reason:      "snapshot decision: direct",
		}
	}
}

// cacheResult stores a routing decision in the cache
func (e *RuleEngine) cacheResult(ctx *EvaluationContext, result *RuleResult) {
	if e.ipDomainCache == nil {
		return
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	entry := &cache.CacheEntry{
		Domain: ctx.Domain,
		IP:     ctx.DstIP,
		Route:  result.Route,
	}

	// Cache by domain if available
	if ctx.Domain != "" {
		e.ipDomainCache.Set(ctx.Domain, entry)
	}

	// Always cache by IP
	e.ipDomainCache.Set(ctx.DstIP.String(), entry)
}

// IsEnabled returns whether the rule engine is enabled
func (e *RuleEngine) IsEnabled() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.enabled
}

// GetCacheStats returns cache statistics
func (e *RuleEngine) GetCacheStats() map[string]interface{} {
	if e.ipDomainCache == nil {
		return map[string]interface{}{"enabled": false}
	}

	e.mu.RLock()
	defer e.mu.RUnlock()

	return e.ipDomainCache.Stats()
}

// ClearCache clears all cached routing decisions
func (e *RuleEngine) ClearCache() {
	if e.ipDomainCache == nil {
		return
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	e.ipDomainCache.Clear()
	logger.Debug("Rule engine cache cleared")
}

// StoreBinding stores DNS binding from connection metadata to cache
// This persists domain-IP relationships observed through SNI, HTTP Host, and CONNECT
func (e *RuleEngine) StoreBinding(metadata *M.Metadata) {
	if e.ipDomainCache == nil || metadata == nil {
		return
	}

	if metadata.DNSInfo == nil || !metadata.DNSInfo.ShouldCache {
		return
	}

	if metadata.HostName == "" || metadata.DstIP.IsUnspecified() {
		return
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	// Convert route string to RouteDecision
	var route cache.RouteDecision
	switch metadata.Route {
	case "RouteToALiang":
		route = cache.RouteToALiang
	case "RouteToSocks":
		route = cache.RouteToSocks
	case "RouteDirect":
		route = cache.RouteDirect
	default:
		route = cache.RouteDirect
	}

	// Create cache entry from DNS binding
	entry := &cache.CacheEntry{
		Domain:         metadata.HostName,
		IP:             metadata.DstIP,
		Route:          route,
		BindingSources: []M.BindingSource{metadata.DNSInfo.BindingSource},
		CreatedAt:      metadata.DNSInfo.BindingTime,
		ExpiresAt:      metadata.DNSInfo.BindingTime.Add(metadata.DNSInfo.CacheTTL),
	}

	// Store by domain
	if metadata.HostName != "" {
		e.ipDomainCache.SetWithTTL(metadata.HostName, entry, metadata.DNSInfo.CacheTTL)
	}

	logger.Debug(fmt.Sprintf("Stored DNS binding: %s (%s) via %s, route: %s",
		metadata.HostName, metadata.DstIP, metadata.DNSInfo.BindingSource, metadata.Route))
}

// Disable disables the rule engine
func (e *RuleEngine) Disable() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.enabled = false
	logger.Debug("Rule engine disabled")
}

// Enable enables the rule engine
func (e *RuleEngine) Enable() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.enabled = true
	logger.Debug("Rule engine enabled")
}
