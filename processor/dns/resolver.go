package dns

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"aliang.one/nursorgate/common/logger"
)

// BaseResolver 基础DNS解析器实现
type BaseResolver struct {
	config *DNSConfig
	mu     sync.RWMutex

	// 缓存相关
	cache     map[string]*cacheEntry
	cacheMu   sync.RWMutex
	cacheHits int64
	cacheMiss int64

	// 清理 goroutine 控制
	cleanupCtx    context.Context
	cleanupCancel context.CancelFunc
}

// cacheEntry DNS缓存条目
type cacheEntry struct {
	ips       []net.IP
	expiresAt time.Time
	source    string
}

// NewBaseResolver 创建基础DNS解析器
func NewBaseResolver(config *DNSConfig) *BaseResolver {
	if config == nil {
		config = DefaultDNSConfig()
	}

	// 创建可取消的上下文
	ctx, cancel := context.WithCancel(context.Background())

	r := &BaseResolver{
		config:        config,
		cache:         make(map[string]*cacheEntry),
		cleanupCtx:    ctx,
		cleanupCancel: cancel,
	}

	// 启动缓存清理协程（可被取消）
	go r.startCacheCleanup()

	return r
}

// LookupA 解析A记录（IPv4）
func (r *BaseResolver) LookupA(ctx context.Context, domain string) ([]net.IP, error) {
	return r.lookup(ctx, domain, true) // true表示查找IPv4
}

// LookupAAAA 解析AAAA记录（IPv6）
func (r *BaseResolver) LookupAAAA(ctx context.Context, domain string) ([]net.IP, error) {
	return r.lookup(ctx, domain, false) // false表示查找IPv6
}

// ResolveIP 解析IP地址（优先返回IPv4，可配置）
func (r *BaseResolver) ResolveIP(ctx context.Context, domain string) (*ResolveResult, error) {
	result := &ResolveResult{
		Success: false,
		Source:  "unknown",
	}

	// 检查是否是IP地址
	if ip := net.ParseIP(domain); ip != nil {
		result.IPs = []net.IP{ip}
		result.Success = true
		result.Source = "direct"
		result.TTL = r.config.MaxTTL
		return result, nil
	}

	// 尝试IPv4解析
	if ipv4s, err := r.LookupA(ctx, domain); err == nil && len(ipv4s) > 0 {
		result.IPs = ipv4s
		result.Success = true
		result.TTL = r.config.MaxTTL
		return result, nil
	}

	// 尝试IPv6解析
	if ipv6s, err := r.LookupAAAA(ctx, domain); err == nil && len(ipv6s) > 0 {
		result.IPs = ipv6s
		result.Success = true
		result.TTL = r.config.MaxTTL
		return result, nil
	}

	result.Error = fmt.Errorf("no IP addresses found for domain %s", domain)
	return result, result.Error
}

// ResolveWithFallback 带回退机制的解析（基类方法，子类可重写）
func (r *BaseResolver) ResolveWithFallback(ctx context.Context, domain string) (*ResolveResult, error) {
	return r.ResolveIP(ctx, domain)
}

// PreloadDomains 批量预解析域名
func (r *BaseResolver) PreloadDomains(ctx context.Context, domains []string) map[string]*PreloadResult {
	results := make(map[string]*PreloadResult)

	// 去重
	uniqueDomains := make(map[string]bool)
	for _, domain := range domains {
		domain = strings.ToLower(strings.TrimSpace(domain))
		if domain != "" && !r.isIPAddress(domain) {
			uniqueDomains[domain] = true
		}
	}

	// 并发解析，限制并发数
	sem := make(chan struct{}, r.config.ConcurrentLimit)
	var wg sync.WaitGroup
	var mu sync.Mutex

	for domain := range uniqueDomains {
		wg.Add(1)
		go func(d string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			start := time.Now()
			result := &PreloadResult{
				Domain:    d,
				Timestamp: start,
			}

			resolveResult, err := r.ResolveIP(ctx, d)
			result.Duration = time.Since(start)

			if err == nil && resolveResult.Success && len(resolveResult.IPs) > 0 {
				result.Success = true
				// 优先选择IPv4地址
				for _, ip := range resolveResult.IPs {
					if ip.To4() != nil {
						result.IP = ip
						break
					}
				}
				// 如果没有IPv4，使用第一个IPv6
				if result.IP == nil {
					result.IP = resolveResult.IPs[0]
				}
				logger.Debug(fmt.Sprintf("[DNS] Preloaded %s -> %s (%v)", d, result.IP, result.Duration))
			} else {
				result.Error = fmt.Errorf("preload failed for %s: %v", d, err)
				logger.Warn(fmt.Sprintf("[DNS] Preload failed for %s: %v", d, err))
			}

			mu.Lock()
			results[d] = result
			mu.Unlock()
		}(domain)
	}

	wg.Wait()
	return results
}

// GetType 获取解析器类型
func (r *BaseResolver) GetType() DNSResolverType {
	return r.config.Type
}

// SetTimeout 设置超时时间
func (r *BaseResolver) SetTimeout(timeout time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.config.Timeout = timeout
}

// GetTimeout 获取超时时间
func (r *BaseResolver) GetTimeout() time.Duration {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.config.Timeout
}

// SetCacheEnabled 启用/禁用缓存
func (r *BaseResolver) SetCacheEnabled(enabled bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.config.CacheEnabled = enabled
	if !enabled {
		r.ClearCache()
	}
}

// IsCacheEnabled 检查缓存是否启用
func (r *BaseResolver) IsCacheEnabled() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.config.CacheEnabled
}

// ClearCache 清理缓存
func (r *BaseResolver) ClearCache() {
	r.cacheMu.Lock()
	defer r.cacheMu.Unlock()
	r.cache = make(map[string]*cacheEntry)
	r.cacheHits = 0
	r.cacheMiss = 0
	logger.Debug("[DNS] Cache cleared")
}

// GetCacheStats 获取缓存统计信息
func (r *BaseResolver) GetCacheStats() *CacheStats {
	r.cacheMu.RLock()
	defer r.cacheMu.RUnlock()

	total := r.cacheHits + r.cacheMiss
	hitRate := float64(0)
	if total > 0 {
		hitRate = float64(r.cacheHits) / float64(total)
	}

	// 计算平均TTL
	var totalTTL time.Duration
	var count int
	now := time.Now()
	for _, entry := range r.cache {
		if entry.expiresAt.After(now) {
			totalTTL += entry.expiresAt.Sub(now)
			count++
		}
	}

	avgTTL := time.Duration(0)
	if count > 0 {
		avgTTL = totalTTL / time.Duration(count)
	}

	return &CacheStats{
		Entries:   len(r.cache),
		Hits:      r.cacheHits,
		Misses:    r.cacheMiss,
		HitRate:   hitRate,
		LastClean: now,
		AvgTTL:    avgTTL,
	}
}

// lookup 内部解析方法
//
// 调用链:
//  1. 检查是否为IP地址
//  2. 检查缓存
//  3. 调用 r.performLookup() ← 这里通过多态调用子类实现
//     - BaseResolver.performLookup() → 系统DNS
//     - HybridResolver.performLookup() → 混合解析（三级回退）
//     - PrimaryResolver.performLookup() → Dialer DNS (通过代理)
//  4. 缓存结果
func (r *BaseResolver) lookup(ctx context.Context, domain string, preferIPv4 bool) ([]net.IP, error) {
	domain = strings.ToLower(strings.TrimSpace(domain))

	// 如果是IP地址，直接返回
	if ip := net.ParseIP(domain); ip != nil {
		if preferIPv4 && ip.To4() != nil {
			return []net.IP{ip}, nil
		}
		if !preferIPv4 && ip.To4() == nil {
			return []net.IP{ip}, nil
		}
		return []net.IP{ip}, nil
	}

	// 检查缓存
	if r.config.CacheEnabled {
		cacheKey := fmt.Sprintf("%s|%v", domain, preferIPv4)
		if cached := r.getCached(cacheKey); cached != nil {
			r.cacheHits++
			return cached.ips, nil
		}
		r.cacheMiss++
	}

	// 执行实际解析（基类方法，子类应该重写）
	// 通过多态调用具体的 performLookup() 实现
	// 实际调用哪个实现取决于接收器的具体类型：
	//   - 如果 r 是 *PrimaryResolver，调用 PrimaryResolver.performLookup()
	//   - 如果 r 是 *HybridResolver，调用 HybridResolver.performLookup()
	//   - 如果 r 是 *BaseResolver，调用 BaseResolver.performLookup()
	logger.Debug(fmt.Sprintf("[DNS] Calling performLookup() for domain: %s, preferIPv4: %v", domain, preferIPv4))
	ips, ttl, err := r.performLookup(ctx, domain, preferIPv4)
	if err != nil {
		logger.Debug(fmt.Sprintf("[DNS] performLookup() failed for %s: %v", domain, err))
		return nil, err
	}
	logger.Debug(fmt.Sprintf("[DNS] performLookup() succeeded for %s: found %d IPs (TTL: %v)", domain, len(ips), ttl))

	// 缓存结果
	if r.config.CacheEnabled && len(ips) > 0 {
		cacheKey := fmt.Sprintf("%s|%v", domain, preferIPv4)
		life := ttl
		if life <= 0 || life > r.config.MaxTTL {
			life = r.config.MaxTTL
		}
		r.setCached(cacheKey, ips, life, "base")
	}

	return ips, nil
}

// performLookup 执行实际DNS解析（基类实现，使用系统解析器）
func (r *BaseResolver) performLookup(ctx context.Context, domain string, preferIPv4 bool) ([]net.IP, time.Duration, error) {
	logger.Debug(fmt.Sprintf("[DNS] BaseResolver.performLookup() called for %s (system DNS)", domain))

	// 创建带超时的上下文
	ctx, cancel := context.WithTimeout(ctx, r.config.Timeout)
	defer cancel()

	resolver := &net.Resolver{
		PreferGo: true,
	}

	startTime := time.Now()

	if preferIPv4 {
		ips, err := resolver.LookupIPAddr(ctx, domain)
		duration := time.Since(startTime)

		if err != nil {
			logger.Debug(fmt.Sprintf("[DNS] System DNS lookup failed for %s (took %v): %v", domain, duration, err))
			return nil, 0, err
		}

		var ipv4s []net.IP
		for _, ipAddr := range ips {
			if ipAddr.IP.To4() != nil {
				ipv4s = append(ipv4s, ipAddr.IP)
			}
		}
		if len(ipv4s) == 0 {
			return nil, 0, fmt.Errorf("no IPv4 addresses found for %s", domain)
		}

		logger.Debug(fmt.Sprintf("[DNS] System DNS resolved %s: %d IPv4 addresses (took %v)", domain, len(ipv4s), duration))
		return ipv4s, r.config.MaxTTL, nil
	} else {
		ips, err := resolver.LookupIPAddr(ctx, domain)
		duration := time.Since(startTime)

		if err != nil {
			logger.Debug(fmt.Sprintf("[DNS] System DNS lookup failed for %s (took %v): %v", domain, duration, err))
			return nil, 0, err
		}

		var ipv6s []net.IP
		for _, ipAddr := range ips {
			if ipAddr.IP.To4() == nil && ipAddr.IP.To16() != nil {
				ipv6s = append(ipv6s, ipAddr.IP)
			}
		}
		if len(ipv6s) == 0 {
			return nil, 0, fmt.Errorf("no IPv6 addresses found for %s", domain)
		}

		logger.Debug(fmt.Sprintf("[DNS] System DNS resolved %s: %d IPv6 addresses (took %v)", domain, len(ipv6s), duration))
		return ipv6s, r.config.MaxTTL, nil
	}
}

// getCached 获取缓存条目
func (r *BaseResolver) getCached(key string) *cacheEntry {
	r.cacheMu.RLock()
	defer r.cacheMu.RUnlock()

	entry, exists := r.cache[key]
	if !exists || time.Now().After(entry.expiresAt) {
		return nil
	}
	return entry
}

// setCached 设置缓存条目
func (r *BaseResolver) setCached(key string, ips []net.IP, ttl time.Duration, source string) {
	r.cacheMu.Lock()
	defer r.cacheMu.Unlock()

	r.cache[key] = &cacheEntry{
		ips:       cloneIPs(ips),
		expiresAt: time.Now().Add(ttl),
		source:    source,
	}
}

// startCacheCleanup 启动缓存清理协程（可被取消）
func (r *BaseResolver) startCacheCleanup() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			r.cleanExpiredCache()
		case <-r.cleanupCtx.Done():
			logger.Debug("DNS cache cleanup goroutine 停止")
			return
		}
	}
}

// defaultDNSCacheMaxEntries 是 DNS 缓存的默认容量上限。代理会见到海量不同
// 域名，仅靠 TTL 在高 cardinality 场景下仍可能让缓存增长过大，因此叠加一个
// 容量兜底：超过上限时按过期时间最早优先驱逐。
const defaultDNSCacheMaxEntries = 10000

// maxCacheEntries 返回生效的缓存容量上限，0/未配置时回退默认值。
func (r *BaseResolver) maxCacheEntries() int {
	if r.config != nil && r.config.MaxCacheEntries > 0 {
		return r.config.MaxCacheEntries
	}
	return defaultDNSCacheMaxEntries
}

// cleanExpiredCache 清理过期缓存
func (r *BaseResolver) cleanExpiredCache() {
	r.cacheMu.Lock()
	defer r.cacheMu.Unlock()

	now := time.Now()
	for key, entry := range r.cache {
		if now.After(entry.expiresAt) {
			delete(r.cache, key)
		}
	}

	// 容量兜底：删除过期项后若仍超上限，按过期时间最早优先驱逐，避免海量
	// 不同域名在高 cardinality 下让缓存无界增长。每分钟一次，O(n) 可接受。
	if max := r.maxCacheEntries(); max > 0 {
		for len(r.cache) > max {
			var oldestKey string
			var oldestExpiry time.Time
			for k, e := range r.cache {
				if oldestKey == "" || e.expiresAt.Before(oldestExpiry) {
					oldestKey = k
					oldestExpiry = e.expiresAt
				}
			}
			if oldestKey == "" {
				break
			}
			delete(r.cache, oldestKey)
		}
	}
}

// Close 关闭 resolver 并停止后台 goroutine
func (r *BaseResolver) Close() {
	if r.cleanupCancel != nil {
		r.cleanupCancel()
		logger.Debug("DNS resolver cleanup canceled")
	}
}

// isIPAddress 检查字符串是否是IP地址
func (r *BaseResolver) isIPAddress(s string) bool {
	return net.ParseIP(s) != nil
}

// cloneIPs 克隆IP地址切片
func cloneIPs(src []net.IP) []net.IP {
	out := make([]net.IP, len(src))
	for i := range src {
		if src[i] != nil {
			b := make([]byte, len(src[i]))
			copy(b, src[i])
			out[i] = net.IP(b)
		}
	}
	return out
}
