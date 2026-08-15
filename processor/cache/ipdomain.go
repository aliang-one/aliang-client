package cache

import (
	"container/list"
	"net/netip"
	"sync"
	"time"

	M "aliang.one/nursorgate/inbound/tun/metadata"
)

// IPStatistics holds statistics about a specific IP address
type IPStatistics struct {
	IP                netip.Addr
	AssociatedDomains []string
	HitCount          uint64
	SourceCount       int
	FirstSeen         time.Time
	LastSeen          time.Time
	IsHotspot         bool
}

// IPDomainCache is a thread-safe LRU cache for IP-domain routing decisions
// It maintains both forward (Domain→IP) and reverse (IP→Domain) indexes
type IPDomainCache struct {
	mu         sync.RWMutex
	entries    map[string]*list.Element // Key: domain or IP string (forward index)
	ipIndex    map[string][]*CacheEntry // Reverse index: IP → list of entries
	lru        *list.List               // LRU list for eviction
	maxEntries int                      // Maximum cache size
	defaultTTL time.Duration            // Default TTL for entries
	hits       uint64                   // Cache hit counter
	misses     uint64                   // Cache miss counter
	evictions  uint64                   // Cache eviction counter
}

// cacheItem is a wrapper for cache entry with its key
type cacheItem struct {
	key   string
	entry *CacheEntry
}

// NewIPDomainCache creates a new IP-domain cache with specified capacity and TTL
func NewIPDomainCache(maxEntries int, ttl time.Duration) *IPDomainCache {
	cache := &IPDomainCache{
		entries:    make(map[string]*list.Element),
		ipIndex:    make(map[string][]*CacheEntry),
		lru:        list.New(),
		maxEntries: maxEntries,
		defaultTTL: ttl,
	}

	// Start background cleanup goroutine
	go cache.cleanupLoop()

	return cache
}

// Get retrieves a cache entry by key (domain or IP string)
// Returns the entry and true if found and not expired, nil and false otherwise
func (c *IPDomainCache) Get(key string) (*CacheEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	elem, exists := c.entries[key]
	if !exists {
		c.misses++
		return nil, false
	}

	item := elem.Value.(*cacheItem)

	// Check expiration
	if item.entry.IsExpired() {
		c.removeElementLocked(elem)
		c.misses++
		return nil, false
	}

	// LRU: Move to front (most recently used)
	c.lru.MoveToFront(elem)
	c.hits++

	// Update hit count for this specific cache entry
	item.entry.HitCount++

	return item.entry, true
}

// Set adds or updates a cache entry
func (c *IPDomainCache) Set(key string, entry *CacheEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Set expiration time if not already set
	if entry.ExpiresAt.IsZero() {
		entry.ExpiresAt = time.Now().Add(c.defaultTTL)
	}
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = time.Now()
	}

	// If entry exists, update and move to front
	if elem, exists := c.entries[key]; exists {
		c.lru.MoveToFront(elem)
		oldEntry := elem.Value.(*cacheItem).entry
		c.removeFromIPIndex(oldEntry)
		elem.Value.(*cacheItem).entry = entry
		// Update reverse index
		c.updateIPIndex(entry)
		return
	}

	// Evict oldest entry if cache is full
	if c.lru.Len() >= c.maxEntries {
		c.evictOldest()
	}

	// Add new entry
	item := &cacheItem{key: key, entry: entry}
	elem := c.lru.PushFront(item)
	c.entries[key] = elem

	// Update reverse index
	c.updateIPIndex(entry)
}

// updateIPIndex updates the reverse IP→Domain index
// Must be called with lock held
func (c *IPDomainCache) updateIPIndex(entry *CacheEntry) {
	ipStr := entry.IP.String()

	// Check if this domain is already in the index for this IP
	oldEntries := c.ipIndex[ipStr]
	var newEntries []*CacheEntry

	// Keep entries from other domains
	for _, e := range oldEntries {
		if e.Domain != entry.Domain {
			newEntries = append(newEntries, e)
		}
	}

	// Add the new/updated entry
	newEntries = append(newEntries, entry)
	c.ipIndex[ipStr] = newEntries
}

// SetWithTTL adds or updates a cache entry with custom TTL
func (c *IPDomainCache) SetWithTTL(key string, entry *CacheEntry, ttl time.Duration) {
	entry.ExpiresAt = time.Now().Add(ttl)
	c.Set(key, entry)
}

// Delete removes a cache entry by key
func (c *IPDomainCache) Delete(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	elem, exists := c.entries[key]
	if !exists {
		return false
	}

	// Remove from forward index
	c.removeElementLocked(elem)

	return true
}

// removeFromIPIndex removes an entry from the reverse IP index
// Must be called with lock held
func (c *IPDomainCache) removeFromIPIndex(entry *CacheEntry) {
	if entry == nil {
		return
	}

	ipStr := entry.IP.String()
	oldEntries := c.ipIndex[ipStr]
	var newEntries []*CacheEntry

	for _, e := range oldEntries {
		if e.Domain != entry.Domain {
			newEntries = append(newEntries, e)
		}
	}

	if len(newEntries) == 0 {
		delete(c.ipIndex, ipStr)
	} else {
		c.ipIndex[ipStr] = newEntries
	}
}

// removeElementLocked removes an LRU element from both forward and reverse indexes.
// Must be called with lock held.
func (c *IPDomainCache) removeElementLocked(elem *list.Element) {
	if elem == nil {
		return
	}

	item := elem.Value.(*cacheItem)
	c.lru.Remove(elem)
	delete(c.entries, item.key)
	c.removeFromIPIndex(item.entry)
}

// evictOldest removes the least recently used entry
// Must be called with lock held
func (c *IPDomainCache) evictOldest() {
	elem := c.lru.Back()
	if elem != nil {
		c.removeElementLocked(elem)
		c.evictions++
	}
}

// cleanupLoop periodically removes expired entries
func (c *IPDomainCache) cleanupLoop() {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		c.cleanup()
	}
}

// cleanup removes all expired entries
func (c *IPDomainCache) cleanup() {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	toDelete := make([]string, 0)

	// Find expired entries
	for key, elem := range c.entries {
		item := elem.Value.(*cacheItem)
		if now.After(item.entry.ExpiresAt) {
			toDelete = append(toDelete, key)
		}
	}

	// Delete expired entries
	for _, key := range toDelete {
		elem := c.entries[key]
		c.removeElementLocked(elem)
	}
}

// GetByDomain retrieves entries by domain name (forward query)
func (c *IPDomainCache) GetByDomain(domain string) (*CacheEntry, bool) {
	return c.Get(domain)
}

// GetByIP retrieves all entries associated with an IP address (reverse query)
func (c *IPDomainCache) GetByIP(ip netip.Addr) []*CacheEntry {
	c.mu.RLock()
	defer c.mu.RUnlock()

	ipStr := ip.String()
	entries, ok := c.ipIndex[ipStr]
	if !ok {
		return nil
	}

	// Filter expired entries
	var validEntries []*CacheEntry
	for _, entry := range entries {
		if !entry.IsExpired() {
			validEntries = append(validEntries, entry)
		}
	}

	return validEntries
}

// GetIPStatistics returns statistics about a specific IP address
func (c *IPDomainCache) GetIPStatistics(ip netip.Addr) *IPStatistics {
	c.mu.RLock()
	defer c.mu.RUnlock()

	ipStr := ip.String()
	entries, ok := c.ipIndex[ipStr]
	if !ok || len(entries) == 0 {
		return nil
	}

	var domains []string
	var hitCount uint64
	sources := make(map[M.BindingSource]bool)
	var firstSeen, lastSeen time.Time

	for _, entry := range entries {
		if !entry.IsExpired() {
			domains = append(domains, entry.Domain)
			hitCount += entry.HitCount

			if firstSeen.IsZero() || entry.CreatedAt.Before(firstSeen) {
				firstSeen = entry.CreatedAt
			}
			if entry.CreatedAt.After(lastSeen) {
				lastSeen = entry.CreatedAt
			}

			for _, src := range entry.BindingSources {
				sources[src] = true
			}
		}
	}

	return &IPStatistics{
		IP:                ip,
		AssociatedDomains: domains,
		HitCount:          hitCount,
		SourceCount:       len(sources),
		FirstSeen:         firstSeen,
		LastSeen:          lastSeen,
		IsHotspot:         hitCount > 100,
	}
}

// GetHotspotIPs returns the top N hotspot IPs
func (c *IPDomainCache) GetHotspotIPs(limit int) []*IPStatistics {
	c.mu.RLock()
	defer c.mu.RUnlock()

	var stats []*IPStatistics
	for ipStr := range c.ipIndex {
		ip, _ := netip.ParseAddr(ipStr)

		// Build stats for this IP
		entries := c.ipIndex[ipStr]
		if len(entries) == 0 {
			continue
		}

		var hitCount uint64
		for _, entry := range entries {
			if !entry.IsExpired() {
				hitCount += entry.HitCount
			}
		}

		if hitCount > 100 {
			stat := &IPStatistics{
				IP:        ip,
				HitCount:  hitCount,
				IsHotspot: true,
			}
			for _, entry := range entries {
				if !entry.IsExpired() {
					stat.AssociatedDomains = append(stat.AssociatedDomains, entry.Domain)
				}
			}
			stats = append(stats, stat)
		}
	}

	// Sort by HitCount (descending)
	for i := 0; i < len(stats)-1; i++ {
		for j := i + 1; j < len(stats); j++ {
			if stats[j].HitCount > stats[i].HitCount {
				stats[i], stats[j] = stats[j], stats[i]
			}
		}
	}

	if len(stats) > limit {
		stats = stats[:limit]
	}

	return stats
}

// GetAll returns all valid cache entries
func (c *IPDomainCache) GetAll() []*CacheEntry {
	c.mu.RLock()
	defer c.mu.RUnlock()

	var result []*CacheEntry
	seen := make(map[string]bool)
	now := time.Now()

	for _, elem := range c.entries {
		item := elem.Value.(*cacheItem)
		entry := item.entry
		key := item.key

		// Skip duplicates and expired entries
		if !seen[key] && !now.After(entry.ExpiresAt) {
			result = append(result, entry)
			seen[key] = true
		}
	}

	return result
}

// Stats returns cache statistics
func (c *IPDomainCache) Stats() map[string]interface{} {
	c.mu.RLock()
	defer c.mu.RUnlock()

	total := c.hits + c.misses
	hitRate := 0.0
	if total > 0 {
		hitRate = float64(c.hits) / float64(total) * 100.0
	}

	// Calculate unique domains (non-expired entries only)
	uniqueDomains := make(map[string]bool)
	for _, elem := range c.entries {
		item := elem.Value.(*cacheItem)
		if !item.entry.IsExpired() {
			uniqueDomains[item.entry.Domain] = true
		}
	}

	return map[string]interface{}{
		"size":          c.lru.Len(),
		"maxEntries":    c.maxEntries,
		"hits":          c.hits,
		"misses":        c.misses,
		"evictions":     c.evictions,
		"hitRate":       hitRate,
		"totalLookups":  total,
		"uniqueDomains": len(uniqueDomains),
		"uniqueIPs":     len(c.ipIndex),
	}
}

// Clear removes all entries from the cache and resets statistics
func (c *IPDomainCache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.entries = make(map[string]*list.Element)
	c.ipIndex = make(map[string][]*CacheEntry)
	c.lru.Init()
	c.hits = 0
	c.misses = 0
	c.evictions = 0
}

// Size returns the current number of entries in the cache
func (c *IPDomainCache) Size() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.lru.Len()
}

// MaxSize returns the maximum capacity of the cache
func (c *IPDomainCache) MaxSize() int {
	return c.maxEntries
}

// GetDefaultTTL returns the default TTL for cache entries
func (c *IPDomainCache) GetDefaultTTL() time.Duration {
	return c.defaultTTL
}

// SetDefaultTTL updates the default TTL for new entries
func (c *IPDomainCache) SetDefaultTTL(ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.defaultTTL = ttl
}
