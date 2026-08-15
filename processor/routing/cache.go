package routing

import (
	"fmt"
	"net"
	"sync"

	"aliang.one/nursorgate/common/logger"
	"aliang.one/nursorgate/processor/geoip"
)

// T064: GeoIPCache manages GeoIP lookups with LRU caching
type GeoIPCache struct {
	mu         sync.RWMutex
	service    *geoip.Service
	cache      map[string]*geoip.CountryInfo // Simple map-based cache (not true LRU, but sufficient)
	maxSize    int
	hitCount   int64
	missCount  int64
	lookupTime int64 // Accumulated lookup time in milliseconds
}

var (
	defaultCache *GeoIPCache
	cacheOnce    sync.Once
)

// T065: NewGeoIPCache creates a new GeoIP cache instance
// It loads the GeoIP database from the configured path
func NewGeoIPCache(maxSize int) *GeoIPCache {
	service := geoip.GetService()

	if !service.IsEnabled() {
		logger.Warn("GeoIP service is not enabled, cache will not perform actual lookups")
	}

	return &GeoIPCache{
		service:   service,
		cache:     make(map[string]*geoip.CountryInfo),
		maxSize:   maxSize,
		hitCount:  0,
		missCount: 0,
	}
}

// GetDefaultCache returns the singleton GeoIPCache instance
func GetDefaultCache() *GeoIPCache {
	cacheOnce.Do(func() {
		defaultCache = NewGeoIPCache(10000) // Default: 10000 entries
	})
	return defaultCache
}

// T066: Lookup performs a GeoIP lookup with caching
// Returns country info or error if lookup fails
func (gc *GeoIPCache) Lookup(ip string) (*geoip.CountryInfo, error) {
	// Validate IP format
	parsedIP := net.ParseIP(ip)
	if parsedIP == nil {
		return nil, fmt.Errorf("invalid IP address: %s", ip)
	}

	// Check cache first
	gc.mu.RLock()
	if cached, exists := gc.cache[ip]; exists {
		gc.hitCount++
		gc.mu.RUnlock()
		return cached, nil
	}
	gc.mu.RUnlock()

	// Cache miss - perform actual lookup
	gc.mu.Lock()
	gc.missCount++
	gc.mu.Unlock()

	// Perform lookup via GeoIP service
	country, err := gc.service.LookupCountry(parsedIP)
	if err != nil {
		return nil, fmt.Errorf("GeoIP lookup failed: %w", err)
	}

	// Cache the result
	gc.mu.Lock()
	// Simple eviction: if cache is full, clear it (not ideal, but simple)
	if len(gc.cache) >= gc.maxSize {
		logger.Debug(fmt.Sprintf("GeoIP cache full (%d entries), clearing", len(gc.cache)))
		gc.cache = make(map[string]*geoip.CountryInfo)
	}
	gc.cache[ip] = country
	gc.mu.Unlock()

	return country, nil
}

// Clear clears all entries from the cache
func (gc *GeoIPCache) Clear() {
	gc.mu.Lock()
	defer gc.mu.Unlock()

	oldSize := len(gc.cache)
	gc.cache = make(map[string]*geoip.CountryInfo)
	logger.Debug(fmt.Sprintf("GeoIP cache cleared (%d entries removed)", oldSize))
}

// GetStats returns cache statistics
func (gc *GeoIPCache) GetStats() map[string]interface{} {
	gc.mu.RLock()
	defer gc.mu.RUnlock()

	hitRate := 0.0
	total := gc.hitCount + gc.missCount
	if total > 0 {
		hitRate = float64(gc.hitCount) / float64(total) * 100.0
	}

	return map[string]interface{}{
		"size":       len(gc.cache),
		"max_size":   gc.maxSize,
		"hit_count":  gc.hitCount,
		"miss_count": gc.missCount,
		"hit_rate":   fmt.Sprintf("%.2f%%", hitRate),
		"total":      total,
	}
}

// ResetStats resets cache statistics counters
func (gc *GeoIPCache) ResetStats() {
	gc.mu.Lock()
	defer gc.mu.Unlock()

	gc.hitCount = 0
	gc.missCount = 0
	gc.lookupTime = 0
	logger.Debug("GeoIP cache statistics reset")
}

// Size returns the current number of cached entries
func (gc *GeoIPCache) Size() int {
	gc.mu.RLock()
	defer gc.mu.RUnlock()
	return len(gc.cache)
}

// IsEnabled returns whether the GeoIP service is enabled
func (gc *GeoIPCache) IsEnabled() bool {
	return gc.service.IsEnabled()
}
