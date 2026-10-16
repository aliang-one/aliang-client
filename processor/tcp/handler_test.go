package tcp

import (
	"strings"
	"testing"

	M "aliang.one/nursorgate/inbound/tun/metadata"
)

func TestDowngradeNoSNIAliangRoute(t *testing.T) {
	cases := []struct {
		name string
		in   ProxyRoute
		want ProxyRoute
	}{
		// tcp 包仅三常量（interfaces.go）；引擎 RouteToSocks 在 tls.go
		// 已映射为 RouteToLocalProxy——socks 场景锁 LocalProxy 即可
		{"toaliang-downgraded", RouteToALiang, RouteDirect},
		{"localproxy-kept", RouteToLocalProxy, RouteToLocalProxy},
		{"direct-kept", RouteDirect, RouteDirect},
		{"unknown-kept", ProxyRoute(99), ProxyRoute(99)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := downgradeNoSNIAliangRoute(tc.in); got != tc.want {
				t.Fatalf("downgradeNoSNIAliangRoute(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// 【加固】观察到的 SNI 与 HTTP Host 路径同标准校验（normalizeRoutingHost）：
// 畸形 SNI（多尾点/空标签/IP 字面量/超长标签等）不再按原样落入 HostName——
// 畸形串永不命中域名规则，等于绕过（含镜像/AI 归因）。非法 → HostName 保持空，
// 走无 SNI 路径（IP/geoip 规则仍可命中）。
func TestSetMetadataHostFromObservedSNI(t *testing.T) {
	cases := []struct {
		name     string
		sni      string
		wantHost string
	}{
		{"plain-host", "www.example.com", "www.example.com"},
		// 与 HTTP Host 路径同标准的归一：大小写/单尾点/:port
		{"case-and-trailing-dot-normalized", "Example.COM.", "example.com"},
		{"port-form-normalized", "example.com:443", "example.com"},
		// 畸形 SNI → 无 SNI 路径
		{"double-trailing-dot-dropped", "www.example.com..", ""},
		{"empty-label-dropped", "a..b.com", ""},
		{"leading-dot-dropped", ".example.com", ""},
		{"ip-literal-dropped", "1.2.3.4", ""},
		{"v6-literal-dropped", "2001:db8::1", ""},
		{"overlong-label-dropped", strings.Repeat("a", 64) + ".example.com", ""},
		{"empty-sni-ignored", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			metadata := &M.Metadata{}
			setMetadataHostFromObservedSNI(metadata, tc.sni)
			if metadata.HostName != tc.wantHost {
				t.Fatalf("setMetadataHostFromObservedSNI(sni=%q): HostName = %q, want %q", tc.sni, metadata.HostName, tc.wantHost)
			}
			if tc.wantHost == "" {
				if metadata.DNSInfo != nil {
					t.Fatalf("setMetadataHostFromObservedSNI(sni=%q): DNSInfo should stay nil for dropped SNI, got %+v", tc.sni, metadata.DNSInfo)
				}
				return
			}
			if metadata.DNSInfo == nil || metadata.DNSInfo.BindingSource != M.BindingSourceSNI {
				t.Fatalf("setMetadataHostFromObservedSNI(sni=%q): DNSInfo.BindingSource = %+v, want %q", tc.sni, metadata.DNSInfo, M.BindingSourceSNI)
			}
		})
	}

	// nil metadata 守卫：不得 panic
	setMetadataHostFromObservedSNI(nil, "www.example.com")
}
