package cache

import (
	"net/netip"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestNewIPDomainCache(t *testing.T) {
	cache := NewIPDomainCache(100, 5*time.Minute)
	assert.NotNil(t, cache)
	assert.Equal(t, 0, cache.Size())
	assert.Equal(t, 100, cache.MaxSize())
	assert.Equal(t, 5*time.Minute, cache.GetDefaultTTL())
}

func TestIPDomainCache_SetAndGet(t *testing.T) {
	cache := NewIPDomainCache(10, 5*time.Minute)

	ip := netip.MustParseAddr("8.8.8.8")
	entry := &CacheEntry{
		Domain: "google.com",
		IP:     ip,
		Route:  RouteToSocks,
	}

	// Set entry
	cache.Set("google.com", entry)
	assert.Equal(t, 1, cache.Size())

	// Get entry
	retrieved, found := cache.Get("google.com")
	assert.True(t, found)
	assert.Equal(t, "google.com", retrieved.Domain)
	assert.Equal(t, RouteToSocks, retrieved.Route)
	assert.Equal(t, ip, retrieved.IP)
}

func TestIPDomainCache_GetNonExistent(t *testing.T) {
	cache := NewIPDomainCache(10, 5*time.Minute)

	entry, found := cache.Get("nonexistent.com")
	assert.False(t, found)
	assert.Nil(t, entry)
}

func TestIPDomainCache_Expiration(t *testing.T) {
	cache := NewIPDomainCache(10, 100*time.Millisecond)

	ip := netip.MustParseAddr("1.1.1.1")
	entry := &CacheEntry{
		Domain: "cloudflare.com",
		IP:     ip,
		Route:  RouteDirect,
	}

	cache.Set("cloudflare.com", entry)

	// Should be found immediately
	_, found := cache.Get("cloudflare.com")
	assert.True(t, found)

	// Wait for expiration
	time.Sleep(150 * time.Millisecond)

	// Should not be found after expiration
	_, found = cache.Get("cloudflare.com")
	assert.False(t, found)
	assert.Empty(t, cache.GetByIP(ip))
}

func TestIPDomainCache_SetWithTTL(t *testing.T) {
	cache := NewIPDomainCache(10, 5*time.Minute)

	ip := netip.MustParseAddr("8.8.8.8")
	entry := &CacheEntry{
		Domain: "example.com",
		IP:     ip,
		Route:  RouteDirect,
	}

	// Set with custom short TTL
	cache.SetWithTTL("example.com", entry, 50*time.Millisecond)

	// Should be found immediately
	_, found := cache.Get("example.com")
	assert.True(t, found)

	// Wait for custom TTL expiration
	time.Sleep(100 * time.Millisecond)

	// Should not be found
	_, found = cache.Get("example.com")
	assert.False(t, found)
}

func TestIPDomainCache_LRU_Eviction(t *testing.T) {
	cache := NewIPDomainCache(3, 5*time.Minute) // Small cache for testing

	// Fill cache to capacity
	for i := 0; i < 3; i++ {
		ip := netip.MustParseAddr("1.1.1.1")
		entry := &CacheEntry{
			Domain: "",
			IP:     ip,
			Route:  RouteDirect,
		}
		cache.Set(string(rune('a'+i)), entry)
	}

	assert.Equal(t, 3, cache.Size())

	// Add one more entry, should evict oldest
	ip := netip.MustParseAddr("8.8.8.8")
	entry := &CacheEntry{
		Domain: "new.com",
		IP:     ip,
		Route:  RouteToSocks,
	}
	cache.Set("new.com", entry)

	// Cache should still be at max capacity
	assert.Equal(t, 3, cache.Size())

	// First entry should be evicted
	_, found := cache.Get("a")
	assert.False(t, found)

	// New entry should be present
	_, found = cache.Get("new.com")
	assert.True(t, found)
}

func TestIPDomainCache_LRU_MoveToFront(t *testing.T) {
	cache := NewIPDomainCache(3, 5*time.Minute)

	// Add 3 entries: a, b, c
	for i := 0; i < 3; i++ {
		ip := netip.MustParseAddr("1.1.1.1")
		entry := &CacheEntry{
			IP:    ip,
			Route: RouteDirect,
		}
		cache.Set(string(rune('a'+i)), entry)
	}

	// Access 'a' to move it to front
	cache.Get("a")

	// Add new entry 'd', should evict 'b' (oldest unused)
	ip := netip.MustParseAddr("8.8.8.8")
	entry := &CacheEntry{
		IP:    ip,
		Route: RouteToSocks,
	}
	cache.Set("d", entry)

	// 'b' should be evicted
	_, found := cache.Get("b")
	assert.False(t, found)

	// 'a' should still be present (was accessed)
	_, found = cache.Get("a")
	assert.True(t, found)
}

func TestIPDomainCache_Delete(t *testing.T) {
	cache := NewIPDomainCache(10, 5*time.Minute)

	ip := netip.MustParseAddr("1.1.1.1")
	entry := &CacheEntry{
		Domain: "test.com",
		IP:     ip,
		Route:  RouteDirect,
	}

	cache.Set("test.com", entry)
	assert.Equal(t, 1, cache.Size())

	// Delete entry
	deleted := cache.Delete("test.com")
	assert.True(t, deleted)
	assert.Equal(t, 0, cache.Size())

	// Try to delete non-existent entry
	deleted = cache.Delete("nonexistent.com")
	assert.False(t, deleted)
}

func TestIPDomainCache_Clear(t *testing.T) {
	cache := NewIPDomainCache(10, 5*time.Minute)

	// Add multiple entries
	for i := 0; i < 5; i++ {
		ip := netip.MustParseAddr("1.1.1.1")
		entry := &CacheEntry{
			IP:    ip,
			Route: RouteDirect,
		}
		cache.Set(string(rune('a'+i)), entry)
	}

	assert.Equal(t, 5, cache.Size())

	// Clear cache
	cache.Clear()
	assert.Equal(t, 0, cache.Size())

	// Stats should be reset
	stats := cache.Stats()
	assert.Equal(t, uint64(0), stats["hits"])
	assert.Equal(t, uint64(0), stats["misses"])
}

func TestIPDomainCache_Stats(t *testing.T) {
	cache := NewIPDomainCache(10, 5*time.Minute)

	ip := netip.MustParseAddr("1.1.1.1")
	entry := &CacheEntry{
		Domain: "test.com",
		IP:     ip,
		Route:  RouteDirect,
	}

	cache.Set("test.com", entry)

	// Trigger hits
	cache.Get("test.com")
	cache.Get("test.com")

	// Trigger misses
	cache.Get("nonexistent1.com")
	cache.Get("nonexistent2.com")

	stats := cache.Stats()
	assert.Equal(t, uint64(2), stats["hits"])
	assert.Equal(t, uint64(2), stats["misses"])
	assert.Equal(t, 50.0, stats["hitRate"])
	assert.Equal(t, 1, stats["size"])
	assert.Equal(t, 10, stats["maxEntries"])
}

func TestIPDomainCache_Update(t *testing.T) {
	cache := NewIPDomainCache(10, 5*time.Minute)

	ip1 := netip.MustParseAddr("1.1.1.1")
	entry1 := &CacheEntry{
		Domain: "test.com",
		IP:     ip1,
		Route:  RouteDirect,
	}

	cache.Set("test.com", entry1)

	// Update with new route
	ip2 := netip.MustParseAddr("8.8.8.8")
	entry2 := &CacheEntry{
		Domain: "test.com",
		IP:     ip2,
		Route:  RouteToSocks,
	}

	cache.Set("test.com", entry2)

	// Should still have only one entry
	assert.Equal(t, 1, cache.Size())

	// Retrieved entry should have updated values
	retrieved, found := cache.Get("test.com")
	assert.True(t, found)
	assert.Equal(t, RouteToSocks, retrieved.Route)
	assert.Equal(t, ip2, retrieved.IP)
	assert.Empty(t, cache.GetByIP(ip1))
	assert.Len(t, cache.GetByIP(ip2), 1)
}

func TestCacheEntry_IsExpired(t *testing.T) {
	entry := &CacheEntry{
		ExpiresAt: time.Now().Add(-1 * time.Second),
	}
	assert.True(t, entry.IsExpired())

	entry.ExpiresAt = time.Now().Add(1 * time.Hour)
	assert.False(t, entry.IsExpired())
}

func TestCacheEntry_TimeToLive(t *testing.T) {
	entry := &CacheEntry{
		ExpiresAt: time.Now().Add(5 * time.Minute),
	}

	ttl := entry.TimeToLive()
	assert.True(t, ttl > 4*time.Minute && ttl <= 5*time.Minute)
}

func TestIPDomainCache_SetDefaultTTL(t *testing.T) {
	cache := NewIPDomainCache(10, 5*time.Minute)
	assert.Equal(t, 5*time.Minute, cache.GetDefaultTTL())

	cache.SetDefaultTTL(10 * time.Minute)
	assert.Equal(t, 10*time.Minute, cache.GetDefaultTTL())
}

func TestIPDomainCache_Cleanup(t *testing.T) {
	cache := NewIPDomainCache(10, 50*time.Millisecond)

	// Add entries
	for i := 0; i < 3; i++ {
		ip := netip.MustParseAddr("1.1.1.1")
		entry := &CacheEntry{
			IP:    ip,
			Route: RouteDirect,
		}
		cache.Set(string(rune('a'+i)), entry)
	}

	assert.Equal(t, 3, cache.Size())

	// Wait for expiration
	time.Sleep(100 * time.Millisecond)

	// Trigger cleanup
	cache.cleanup()

	// All entries should be removed
	assert.Equal(t, 0, cache.Size())
	stats := cache.Stats()
	assert.Equal(t, 0, stats["uniqueIPs"])
}

func TestIPDomainCache_CleanupRemovesReverseIndexEntries(t *testing.T) {
	cache := NewIPDomainCache(10, 50*time.Millisecond)

	ip := netip.MustParseAddr("1.1.1.1")
	entry := &CacheEntry{
		Domain: "expired.example",
		IP:     ip,
		Route:  RouteDirect,
	}
	cache.Set("expired.example", entry)
	assert.Len(t, cache.GetByIP(ip), 1)

	time.Sleep(100 * time.Millisecond)
	cache.cleanup()

	assert.Equal(t, 0, cache.Size())
	assert.Empty(t, cache.GetByIP(ip))
	stats := cache.Stats()
	assert.Equal(t, 0, stats["uniqueIPs"])
}

func TestIPDomainCache_LRUEvictionRemovesReverseIndexEntry(t *testing.T) {
	cache := NewIPDomainCache(1, 5*time.Minute)

	evictedIP := netip.MustParseAddr("1.1.1.1")
	cache.Set("old.example", &CacheEntry{
		Domain: "old.example",
		IP:     evictedIP,
		Route:  RouteDirect,
	})
	assert.Len(t, cache.GetByIP(evictedIP), 1)

	cache.Set("new.example", &CacheEntry{
		Domain: "new.example",
		IP:     netip.MustParseAddr("8.8.8.8"),
		Route:  RouteToSocks,
	})

	assert.Empty(t, cache.GetByIP(evictedIP))
	stats := cache.Stats()
	assert.Equal(t, 1, stats["uniqueIPs"])
}
