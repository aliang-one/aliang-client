package dns

import (
	"context"
	"net"
	"time"
)

// DNSResolverType 定义DNS解析器类型
type DNSResolverType string

const (
	ResolverTypeHybrid  DNSResolverType = "hybrid"  // 混合解析器（带回退机制）
	ResolverTypeSystem  DNSResolverType = "system"  // 系统解析器
	ResolverTypeCustom  DNSResolverType = "custom"  // 自定义解析器
	ResolverTypePrimary DNSResolverType = "primary" // 主解析器（单一路径）
)

// ResolveResult DNS解析结果
type ResolveResult struct {
	IPs     []net.IP      // 解析得到的IP地址列表
	TTL     time.Duration // 记录的TTL
	Source  string        // 解析来源（primary/fallback/system）
	Success bool          // 是否解析成功
	Error   error         // 解析错误（如果有）
}

// PreloadResult 预解析结果
type PreloadResult struct {
	Domain    string        // 原始域名
	IP        net.IP        // 解析得到的IP（优先使用IPv4）
	Success   bool          // 是否解析成功
	Error     error         // 解析错误
	Duration  time.Duration // 解析耗时
	Timestamp time.Time     // 解析时间戳
}

// DNSResolverInterface 定义DNS解析器接口
type DNSResolverInterface interface {
	// 基础解析方法
	LookupA(ctx context.Context, domain string) ([]net.IP, error)
	LookupAAAA(ctx context.Context, domain string) ([]net.IP, error)

	// 便捷方法
	ResolveIP(ctx context.Context, domain string) (*ResolveResult, error)
	ResolveWithFallback(ctx context.Context, domain string) (*ResolveResult, error)

	// 批量操作
	PreloadDomains(ctx context.Context, domains []string) map[string]*PreloadResult

	// 配置和状态
	GetType() DNSResolverType
	SetTimeout(timeout time.Duration)
	GetTimeout() time.Duration
	SetCacheEnabled(enabled bool)
	IsCacheEnabled() bool

	// 清理和统计
	ClearCache()
	GetCacheStats() *CacheStats
}

// DNSConfig DNS解析器配置
type DNSConfig struct {
	Type             DNSResolverType `json:"type"`
	PrimaryDNS       string          `json:"primary_dns"`        // 主DNS服务器
	FallbackDNS      string          `json:"fallback_dns"`       // 回退DNS服务器
	SystemDNSEnabled bool            `json:"system_dns_enabled"` // 是否启用系统DNS回退
	Timeout          time.Duration   `json:"timeout"`            // 解析超时时间
	MaxTTL           time.Duration   `json:"max_ttl"`            // 最大缓存TTL
	CacheEnabled     bool            `json:"cache_enabled"`      // 是否启用缓存
	PreloadEnabled   bool            `json:"preload_enabled"`    // 是否启用预解析
	ConcurrentLimit  int             `json:"concurrent_limit"`   // 并发解析限制
	RetryCount       int             `json:"retry_count"`        // 重试次数
	RetryDelay       time.Duration   `json:"retry_delay"`        // 重试延迟
	MaxCacheEntries  int             `json:"max_cache_entries"`  // 缓存最大条目数（0=用默认值）
}

// DefaultDNSConfig 返回默认DNS配置
func DefaultDNSConfig() *DNSConfig {
	return &DNSConfig{
		Type:             ResolverTypeHybrid,
		PrimaryDNS:       "8.8.8.8:53",
		FallbackDNS:      "223.5.5.5:53",
		SystemDNSEnabled: true,
		Timeout:          5 * time.Second,
		MaxTTL:           5 * time.Minute,
		CacheEnabled:     true,
		PreloadEnabled:   true,
		ConcurrentLimit:  10,
		RetryCount:       2,
		RetryDelay:       500 * time.Millisecond,
		MaxCacheEntries:  defaultDNSCacheMaxEntries,
	}
}

// CacheStats DNS缓存统计信息
type CacheStats struct {
	Entries   int           `json:"entries"`    // 缓存条目数
	Hits      int64         `json:"hits"`       // 命中次数
	Misses    int64         `json:"misses"`     // 未命中次数
	HitRate   float64       `json:"hit_rate"`   // 命中率
	LastClean time.Time     `json:"last_clean"` // 最后清理时间
	TotalSize int           `json:"total_size"` // 总大小（字节）
	AvgTTL    time.Duration `json:"avg_ttl"`    // 平均TTL
}

// DialerProvider 提供用于DNS解析的dialer接口
type DialerProvider interface {
	GetPrimaryDialer() Dialer
	GetFallbackDialer() Dialer
}

// Dialer 简化的dialer接口，用于DNS解析
type Dialer interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
}
