package dns

import (
	"reflect"
	"sync"

	"aliang.one/nursorgate/common/logger"
	"aliang.one/nursorgate/outbound/proxy"
	"aliang.one/nursorgate/processor/config"
)

// Global DNS resolver instance (singleton)
var (
	globalResolver DNSResolverInterface
	resolverOnce   sync.Once
	resolverMu     sync.RWMutex
)

// InitGlobalResolver initializes the global DNS resolver from configuration.
// Should be called after proxies are registered.
// This replaces both SetGlobalResolver for simple cases and provides
// full configuration support for DNS pre-resolution.
func InitGlobalResolver(primaryProxy, fallbackProxy proxy.Proxy, cfg *config.Config) error {
	dnsCfg := cfg.EffectiveDNSPreResolution()
	if cfg == nil || dnsCfg == nil || !dnsCfg.Enabled {
		logger.Info("[DNS] DNS resolution disabled in config")
		return nil
	}

	// Create hybrid DNS resolver using primary and fallback proxies
	hybridResolver := NewHybridResolver(
		&DNSConfig{
			Type:             ResolverTypeHybrid,
			PrimaryDNS:       dnsCfg.GetPrimaryDNS(),
			FallbackDNS:      dnsCfg.GetFallbackDNS(),
			SystemDNSEnabled: dnsCfg.SystemDNSFallback,
			Timeout:          dnsCfg.GetTimeout(),
			MaxTTL:           dnsCfg.GetMaxCacheTTL(),
			CacheEnabled:     dnsCfg.CacheResults,
		},
		primaryProxy,  // primary dialer (implements proxy.Dialer)
		fallbackProxy, // fallback dialer (implements proxy.Dialer)
	)

	SetGlobalResolver(hybridResolver)
	logger.Info("[DNS] Global DNS resolver initialized successfully")
	return nil
}

// SetGlobalResolver sets the global DNS resolver instance.
// Use this for simple cases where you already have a resolver instance.
// For configuration-based initialization, use InitGlobalResolver instead.
func SetGlobalResolver(resolver DNSResolverInterface) {
	resolverMu.Lock()
	defer resolverMu.Unlock()

	previous := globalResolver
	if sameResolver(previous, resolver) {
		return
	}
	globalResolver = resolver
	closeResolver(previous)
}

// GetGlobalResolver returns the global DNS resolver instance.
// Returns nil if no resolver has been configured.
func GetGlobalResolver() DNSResolverInterface {
	resolverMu.RLock()
	defer resolverMu.RUnlock()
	return globalResolver
}

// HasGlobalResolver returns true if a global resolver has been configured.
func HasGlobalResolver() bool {
	resolverMu.RLock()
	defer resolverMu.RUnlock()
	return globalResolver != nil
}

// ResetGlobalResolver clears the global resolver (mainly for testing).
func ResetGlobalResolver() {
	SetGlobalResolver(nil)
}

type resolverCloser interface {
	Close()
}

func closeResolver(resolver DNSResolverInterface) {
	if closer, ok := resolver.(resolverCloser); ok {
		closer.Close()
	}
}

func sameResolver(a, b DNSResolverInterface) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	aValue := reflect.ValueOf(a)
	bValue := reflect.ValueOf(b)
	if aValue.Type() != bValue.Type() || !aValue.Type().Comparable() {
		return false
	}
	return aValue.Interface() == bValue.Interface()
}
