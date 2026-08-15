package dns

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"aliang.one/nursorgate/common/logger"
	"aliang.one/nursorgate/outbound/proxy"
)

// HybridResolver 混合DNS解析器
// 支持多级回退机制：primary → fallback → system
type HybridResolver struct {
	*BaseResolver
	primaryResolver  DNSResolverInterface
	fallbackResolver DNSResolverInterface
	systemResolver   DNSResolverInterface
}

// NewHybridResolver 创建混合DNS解析器
func NewHybridResolver(config *DNSConfig, primaryDialer, fallbackDialer proxy.Dialer) *HybridResolver {
	base := NewBaseResolver(config)

	// 创建主解析器（使用door代理）
	primaryConfig := &DNSConfig{
		Type:         ResolverTypePrimary,
		PrimaryDNS:   config.PrimaryDNS,
		Timeout:      config.Timeout,
		MaxTTL:       config.MaxTTL,
		CacheEnabled: config.CacheEnabled,
	}
	primaryResolver := NewPrimaryResolver(primaryConfig, primaryDialer)

	// 创建回退解析器（使用direct代理）
	var fallbackResolver DNSResolverInterface
	if fallbackDialer != nil {
		fallbackConfig := &DNSConfig{
			Type:         ResolverTypePrimary,
			PrimaryDNS:   config.FallbackDNS,
			Timeout:      config.Timeout,
			MaxTTL:       config.MaxTTL,
			CacheEnabled: config.CacheEnabled,
		}
		fallbackResolver = NewPrimaryResolver(fallbackConfig, fallbackDialer)
	} else {
		fallbackResolver = nil
	}

	// 创建系统解析器
	var systemResolver DNSResolverInterface
	if config.SystemDNSEnabled {
		systemConfig := &DNSConfig{
			Type:         ResolverTypeSystem,
			Timeout:      config.Timeout,
			MaxTTL:       config.MaxTTL,
			CacheEnabled: config.CacheEnabled,
		}
		systemResolver = NewBaseResolver(systemConfig)
	} else {
		systemResolver = nil
	}

	return &HybridResolver{
		BaseResolver:     base,
		primaryResolver:  primaryResolver,
		fallbackResolver: fallbackResolver,
		systemResolver:   systemResolver,
	}
}

// ResolveWithFallback 实现带回退机制的DNS解析
func (h *HybridResolver) ResolveWithFallback(ctx context.Context, domain string) (*ResolveResult, error) {
	domain = normalizeDomainForResolution(domain)
	if domain == "" {
		return &ResolveResult{
			Success: false,
			Error:   fmt.Errorf("empty domain"),
			Source:  "none",
		}, fmt.Errorf("empty domain")
	}

	// 检查是否是IP地址
	if ip := net.ParseIP(domain); ip != nil {
		return &ResolveResult{
			IPs:     []net.IP{ip},
			Success: true,
			Source:  "direct",
			TTL:     h.config.MaxTTL,
		}, nil
	}

	// 1. 尝试主解析器（通常是door代理）
	if h.primaryResolver != nil {
		result, err := h.primaryResolver.ResolveIP(ctx, domain)
		if err == nil && result.Success && len(result.IPs) > 0 {
			result.Source = "primary"
			logger.Debug(fmt.Sprintf("[DNS Hybrid] ✓ %s resolved via primary (%s)", domain, result.IPs[0]))
			return result, nil
		}
		logger.Debug(fmt.Sprintf("[DNS Hybrid] ✗ Primary resolver failed for %s: %v", domain, err))
	}

	// 2. 回退到回退解析器（通常是direct代理）
	if h.fallbackResolver != nil {
		result, err := h.fallbackResolver.ResolveIP(ctx, domain)
		if err == nil && result.Success && len(result.IPs) > 0 {
			result.Source = "fallback"
			logger.Debug(fmt.Sprintf("[DNS Hybrid] ✓ %s resolved via fallback (%s)", domain, result.IPs[0]))
			return result, nil
		}
		logger.Debug(fmt.Sprintf("[DNS Hybrid] ✗ Fallback resolver failed for %s: %v", domain, err))
	}

	// 3. 最后回退到系统DNS解析
	if h.systemResolver != nil {
		result, err := h.systemResolver.ResolveIP(ctx, domain)
		if err == nil && result.Success && len(result.IPs) > 0 {
			result.Source = "system"
			logger.Debug(fmt.Sprintf("[DNS Hybrid] ✓ %s resolved via system (%s)", domain, result.IPs[0]))
			return result, nil
		}
		logger.Debug(fmt.Sprintf("[DNS Hybrid] ✗ System resolver failed for %s: %v", domain, err))
	}

	// 所有解析器都失败了
	return &ResolveResult{
		Success: false,
		Error:   fmt.Errorf("all DNS resolvers failed for %s", domain),
		Source:  "none",
	}, fmt.Errorf("all DNS resolvers failed for %s", domain)
}

// performLookup 重写基础解析器的performLookup方法
func (h *HybridResolver) performLookup(ctx context.Context, domain string, preferIPv4 bool) ([]net.IP, time.Duration, error) {
	logger.Debug(fmt.Sprintf("[DNS] HybridResolver.performLookup() called for %s", domain))

	result, err := h.ResolveWithFallback(ctx, domain)
	if err != nil {
		logger.Debug(fmt.Sprintf("[DNS] HybridResolver.ResolveWithFallback() failed for %s: %v", domain, err))
		return nil, 0, err
	}

	if !result.Success {
		return nil, 0, fmt.Errorf("no IP addresses found for %s", domain)
	}

	// 根据偏好过滤IP地址
	var filteredIPs []net.IP
	if preferIPv4 {
		for _, ip := range result.IPs {
			if ip.To4() != nil {
				filteredIPs = append(filteredIPs, ip)
			}
		}
	} else {
		for _, ip := range result.IPs {
			if ip.To4() == nil {
				filteredIPs = append(filteredIPs, ip)
			}
		}
	}

	// 如果没有找到偏好类型的IP，返回所有IP
	if len(filteredIPs) == 0 {
		filteredIPs = result.IPs
	}

	logger.Debug(fmt.Sprintf("[DNS] HybridResolver resolved %s: %d IPs filtered from %d total", domain, len(filteredIPs), len(result.IPs)))
	return filteredIPs, result.TTL, nil
}

// GetType 返回解析器类型
func (h *HybridResolver) GetType() DNSResolverType {
	return ResolverTypeHybrid
}

// GetPrimaryResolver 获取主解析器
func (h *HybridResolver) GetPrimaryResolver() DNSResolverInterface {
	return h.primaryResolver
}

// GetFallbackResolver 获取回退解析器
func (h *HybridResolver) GetFallbackResolver() DNSResolverInterface {
	return h.fallbackResolver
}

// GetSystemResolver 获取系统解析器
func (h *HybridResolver) GetSystemResolver() DNSResolverInterface {
	return h.systemResolver
}

// PrimaryResolver 主解析器（使用单一dialer）
type PrimaryResolver struct {
	*BaseResolver
	dialer proxy.Dialer
}

// NewPrimaryResolver 创建主解析器
func NewPrimaryResolver(config *DNSConfig, dialer proxy.Dialer) *PrimaryResolver {
	return &PrimaryResolver{
		BaseResolver: NewBaseResolver(config),
		dialer:       dialer,
	}
}

// GetType 返回解析器类型
func (p *PrimaryResolver) GetType() DNSResolverType {
	return ResolverTypePrimary
}

// performLookup 使用指定dialer执行DNS解析（PrimaryResolver的实现）
//
// ⚠️ 注意：这个方法被 BaseResolver.lookup() 通过多态调用
//
// 调用路径:
//
//	BaseResolver.lookup() [第297行]
//	  → r.performLookup() [多态调用]
//	  → PrimaryResolver.performLookup() [本方法]
//	  → DialerResolverUtil.ResolveThroughDialer() [DNS-over-TCP via proxy]
//
// 功能:
//   - 如果 dialer 已配置: 通过代理进行 DNS-over-TCP 查询
//   - 如果 dialer 未配置: 回退到系统 DNS 解析
//   - 如果代理 DNS 失败: 自动回退到系统 DNS 解析
func (p *PrimaryResolver) performLookup(ctx context.Context, domain string, preferIPv4 bool) ([]net.IP, time.Duration, error) {
	logger.Debug(fmt.Sprintf("[DNS] PrimaryResolver.performLookup() called for %s", domain))

	// 使用TUN模块的DNS解析逻辑，但使用我们的dialer
	if p.dialer == nil {
		logger.Debug("[DNS] Dialer not configured, falling back to BaseResolver.performLookup()")
		// 如果没有dialer，回退到系统解析器
		return p.BaseResolver.performLookup(ctx, domain, preferIPv4)
	}

	logger.Debug("[DNS] Using dialer for DNS resolution (DNS-over-TCP via proxy)")

	// 创建带超时的上下文
	ctx, cancel := context.WithTimeout(ctx, p.config.Timeout)
	defer cancel()

	// 使用 dialer 执行 DNS 解析 (DNS-over-TCP via proxy)
	startTime := time.Now()
	util := NewDialerResolverUtil(p.dialer, p.config)
	ips, ttl, err := util.ResolveThroughDialer(ctx, domain, preferIPv4)
	duration := time.Since(startTime)

	if err != nil {
		logger.Debug(fmt.Sprintf("[DNS] DNS resolution via dialer failed for %s (took %v): %v, falling back to system resolver", domain, duration, err))
		// Fallback to system resolver
		return p.BaseResolver.performLookup(ctx, domain, preferIPv4)
	}
	if ttl <= 0 {
		ttl = p.config.MaxTTL
	}

	logger.Debug(fmt.Sprintf("[DNS] DNS resolved via dialer for %s: %d IPs (took %v)", domain, len(ips), duration))
	return ips, ttl, nil
}

// SystemResolver 系统DNS解析器
type SystemResolver struct {
	*BaseResolver
}

// NewSystemResolver 创建系统解析器
func NewSystemResolver(config *DNSConfig) *SystemResolver {
	return &SystemResolver{
		BaseResolver: NewBaseResolver(config),
	}
}

// GetType 返回解析器类型
func (s *SystemResolver) GetType() DNSResolverType {
	return ResolverTypeSystem
}

// Close 关闭混合解析器并停止所有后台 goroutine
func (h *HybridResolver) Close() {
	logger.Debug("Closing hybrid DNS resolver...")

	// 关闭主解析器
	if pr, ok := h.primaryResolver.(*PrimaryResolver); ok {
		pr.Close()
	}

	// 关闭回退解析器
	if fr, ok := h.fallbackResolver.(*PrimaryResolver); ok {
		fr.Close()
	}

	// 关闭系统解析器
	if sr, ok := h.systemResolver.(*BaseResolver); ok {
		sr.Close()
	}

	// 关闭自身（BaseResolver）
	h.BaseResolver.Close()

	logger.Debug("Hybrid DNS resolver closed successfully")
}

// 工具函数

// normalizeDomainForResolution 规范化域名用于解析
func normalizeDomainForResolution(domain string) string {
	if domain == "" {
		return ""
	}
	domain = strings.ToLower(strings.TrimSpace(domain))
	// 确保域名以点结尾（DNS标准格式）
	if !strings.HasSuffix(domain, ".") {
		domain += "."
	}
	return domain
}

// CreateDefaultHybridResolver 创建默认配置的混合解析器
func CreateDefaultHybridResolver(primaryDialer, fallbackDialer proxy.Dialer) DNSResolverInterface {
	config := DefaultDNSConfig()
	return NewHybridResolver(config, primaryDialer, fallbackDialer)
}
