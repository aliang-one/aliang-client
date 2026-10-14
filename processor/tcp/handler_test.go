package tcp

import "testing"

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
