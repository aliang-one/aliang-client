package dns

import (
	"context"
	"net"
	"net/netip"
	"time"

	M "aliang.one/nursorgate/inbound/tun/metadata"
	"aliang.one/nursorgate/outbound/proxy"
)

// TunDNSBridge 为TUN模块提供DNS功能的桥接适配器
// 实现TUN模块期望的DNS接口，内部使用新的processor/dns模块
type TunDNSBridge struct {
	resolver DNSResolverInterface
}

// NewTunDNSBridge 创建TUN DNS桥接适配器
func NewTunDNSBridge(resolver DNSResolverInterface) *TunDNSBridge {
	return &TunDNSBridge{
		resolver: resolver,
	}
}

// LookupA 解析A记录（保持与TUN模块兼容）
func (b *TunDNSBridge) LookupA(ctx context.Context, qname string) ([]net.IP, error) {
	return b.resolver.LookupA(ctx, qname)
}

// LookupAAAA 解析AAAA记录（保持与TUN模块兼容）
func (b *TunDNSBridge) LookupAAAA(ctx context.Context, qname string) ([]net.IP, error) {
	return b.resolver.LookupAAAA(ctx, qname)
}

// LegacyHybridResolver 遗留混合解析器适配器
// 为了完全兼容TUN模块的HybridDNSResolver接口
type LegacyHybridResolver struct {
	*TunDNSBridge
}

// NewLegacyHybridResolver 创建遗留格式的混合解析器
// 这个函数用于在inbound/tun/tunnel/dns.go中调用
func NewLegacyHybridResolver(primaryDNS, fallbackDNS string, primaryDialer, fallbackDialer proxy.Dialer, timeout, maxTTL time.Duration) *LegacyHybridResolver {
	// 创建混合DNS配置
	config := &DNSConfig{
		Type:             ResolverTypeHybrid,
		PrimaryDNS:       primaryDNS,
		FallbackDNS:      fallbackDNS,
		SystemDNSEnabled: true,
		Timeout:          timeout,
		MaxTTL:           maxTTL,
		CacheEnabled:     true,
	}

	// 创建混合解析器
	hybridResolver := NewHybridResolver(config, primaryDialer, fallbackDialer)

	return &LegacyHybridResolver{
		TunDNSBridge: NewTunDNSBridge(hybridResolver),
	}
}

// NewLegacyDNSResolver 创建遗留格式的DNS解析器
// 用于完全兼容原来的NewDNSResolver函数
func NewLegacyDNSResolver(dnsServer string, dialer proxy.Dialer, timeout, maxTTL time.Duration) *TunDNSBridge {
	config := &DNSConfig{
		Type:         ResolverTypePrimary,
		PrimaryDNS:   dnsServer,
		Timeout:      timeout,
		MaxTTL:       maxTTL,
		CacheEnabled: true,
	}

	resolver := NewPrimaryResolver(config, dialer)
	return NewTunDNSBridge(resolver)
}

// 为了向后兼容，提供一些类型别名
type (
	// DNSResolverInterface 兼容TUN模块的接口名
	TUNResolverInterface = interface {
		LookupA(ctx context.Context, qname string) ([]net.IP, error)
		LookupAAAA(ctx context.Context, qname string) ([]net.IP, error)
	}
)

// ResolveMetadataForProxy 为代理连接解析元数据中的域名
// 这是一个便捷函数，用于在代理连接前快速解析域名
func ResolveMetadataForProxy(ctx context.Context, metadata *M.Metadata) (*M.Metadata, error) {
	resolver := GetGlobalResolver()
	if resolver == nil {
		return metadata, nil // 没有配置全局解析器，直接返回
	}

	// 如果已经有IP地址，不需要解析
	if metadata.DstIP.IsValid() {
		return metadata, nil
	}

	// 如果没有主机名，无法解析
	if metadata.HostName == "" {
		return metadata, nil
	}

	// 执行DNS解析
	result, err := resolver.ResolveIP(ctx, metadata.HostName)
	if err != nil || !result.Success {
		return metadata, err // 解析失败，返回原始元数据
	}

	// 选择一个IP地址
	var selectedIP net.IP
	if len(result.IPs) > 0 {
		// 优先选择IPv4
		for _, ip := range result.IPs {
			if ip.To4() != nil {
				selectedIP = ip
				break
			}
		}
		// 如果没有IPv4，使用第一个IPv6
		if selectedIP == nil {
			selectedIP = result.IPs[0]
		}
	}

	if selectedIP != nil {
		// 创建新的元数据，替换域名为IP
		newMetadata := *metadata
		if v4 := selectedIP.To4(); v4 != nil {
			newMetadata.DstIP = netip.AddrFrom4([4]byte{v4[0], v4[1], v4[2], v4[3]})
		} else if v6 := selectedIP.To16(); v6 != nil {
			var arr [16]byte
			copy(arr[:], v6)
			newMetadata.DstIP = netip.AddrFrom16(arr)
		}
		return &newMetadata, nil
	}

	return metadata, nil
}

// 预定义的解析器创建函数，用于快速创建不同类型的解析器

// CreateSystemResolver 创建系统DNS解析器
func CreateSystemResolver() DNSResolverInterface {
	config := DefaultDNSConfig()
	config.Type = ResolverTypeSystem
	return NewBaseResolver(config)
}

// CreateHybridResolverWithDialers 创建使用指定dialer的混合解析器
func CreateHybridResolverWithDialers(primaryDialer, fallbackDialer proxy.Dialer) DNSResolverInterface {
	config := DefaultDNSConfig()
	config.Type = ResolverTypeHybrid
	return NewHybridResolver(config, primaryDialer, fallbackDialer)
}

// CreateResolverForProxy 创建用于特定代理的DNS解析器
func CreateResolverForProxy(dialer proxy.Dialer) DNSResolverInterface {
	config := &DNSConfig{
		Type:         ResolverTypePrimary,
		PrimaryDNS:   "8.8.8.8:53",
		Timeout:      5 * time.Second,
		MaxTTL:       5 * time.Minute,
		CacheEnabled: true,
	}
	return NewPrimaryResolver(config, dialer)
}
