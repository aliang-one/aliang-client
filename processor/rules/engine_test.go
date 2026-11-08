package rules

import (
	"net/netip"
	"testing"
	"time"

	M "aliang.one/nursorgate/inbound/tun/metadata"
	"aliang.one/nursorgate/processor/cache"
)

// meshBindingMetadata 构造满足 StoreBinding 前置条件(Guard 全通过)的
// metadata:mesh 路由连接是否会毒化域绑定缓存由本组测试钉死。
func meshBindingMetadata(t *testing.T, route string) *M.Metadata {
	t.Helper()
	return &M.Metadata{
		Network:  M.TCP,
		HostName: "mesh-bound.example",
		DstIP:    netip.MustParseAddr("203.0.113.10"),
		DstPort:  443,
		Route:    route,
		DNSInfo: &M.DNSInfo{
			BindingSource: M.BindingSourceCONNECT,
			BindingTime:   time.Now(),
			CacheTTL:      time.Minute,
			ShouldCache:   true,
		},
	}
}

func newBindingTestEngine() *RuleEngine {
	return &RuleEngine{
		ipDomainCache: cache.NewIPDomainCache(16, time.Minute),
		enabled:       true,
	}
}

// TestStoreBindingSkipsMeshRoutedConnections mesh 路由(RouteToNode/
// RouteMeshFallback)的连接不得进域绑定缓存:StoreBinding 的 switch 若落
// default 会把 mesh 连接缓存成 RouteDirect——EvaluateRoute 现虽休眠,复活
// 即静默绕过 mesh 规则把后续同名连接直连出去。
func TestStoreBindingSkipsMeshRoutedConnections(t *testing.T) {
	for _, route := range []string{"RouteToNode", "RouteMeshFallback"} {
		engine := newBindingTestEngine()
		engine.StoreBinding(meshBindingMetadata(t, route))
		if _, hit := engine.ipDomainCache.GetByDomain("mesh-bound.example"); hit {
			t.Fatalf("route=%s: mesh 路由连接不得写入域绑定缓存(防 EvaluateRoute 复活静默绕过 mesh 规则)", route)
		}
	}

	// 对照:常规路由照旧写入(既有行为不回退)。
	engine := newBindingTestEngine()
	engine.StoreBinding(meshBindingMetadata(t, "RouteDirect"))
	entry, hit := engine.ipDomainCache.GetByDomain("mesh-bound.example")
	if !hit {
		t.Fatal("RouteDirect 连接应照旧写入域绑定缓存(既有行为)")
	}
	if entry.Route != cache.RouteDirect {
		t.Fatalf("entry.Route = %v, want RouteDirect", entry.Route)
	}
}
