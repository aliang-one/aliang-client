package outbound

import (
	"testing"

	"aliang.one/nursorgate/outbound/proxy/proto"
	"aliang.one/nursorgate/processor/config"
)

func TestRegistryRefreshCustomerProxy_ReplacesOldSocksWithHTTP(t *testing.T) {
	registry := GetRegistry()
	registry.Clear()
	defer registry.Clear()

	oldCfg := &config.Config{
		Customer: &config.CustomerConfig{
			Proxy: &config.CustomerProxyConfig{
				Enable: boolPtr(true),
				Type:   "socks5",
				Server: "182.138.136.39:27751",
			},
		},
	}
	if err := registry.RefreshCustomerProxy(oldCfg); err != nil {
		t.Fatalf("RefreshCustomerProxy(old) error = %v", err)
	}

	oldProxy, err := registry.Get("socks")
	if err != nil {
		t.Fatalf("registry.Get(\"socks\") error = %v", err)
	}
	if got := oldProxy.Addr(); got != "182.138.136.39:27751" {
		t.Fatalf("old proxy addr = %q, want 182.138.136.39:27751", got)
	}

	nextCfg := &config.Config{
		Customer: &config.CustomerConfig{
			Proxy: &config.CustomerProxyConfig{
				Enable:   boolPtr(true),
				Type:     "http",
				Server:   "cd.liangsqrt.com:27750",
				Username: "clash",
				Password: "asd123456",
			},
		},
	}
	if err := registry.RefreshCustomerProxy(nextCfg); err != nil {
		t.Fatalf("RefreshCustomerProxy(next) error = %v", err)
	}

	if _, err := registry.Get("socks"); err == nil {
		t.Fatal("registry.Get(\"socks\") error = nil, want stale socks proxy removed")
	}
	httpProxy, err := registry.Get("http")
	if err != nil {
		t.Fatalf("registry.Get(\"http\") error = %v", err)
	}
	if got := httpProxy.Proto(); got != proto.HTTP {
		t.Fatalf("http proxy proto = %s, want %s", got, proto.HTTP)
	}
	if got := httpProxy.Addr(); got != "cd.liangsqrt.com:27750" {
		t.Fatalf("http proxy addr = %q, want cd.liangsqrt.com:27750", got)
	}

	authProxy, ok := httpProxy.(interface {
		GetUser() string
		GetPass() string
	})
	if !ok {
		t.Fatalf("http proxy does not expose auth getters: %T", httpProxy)
	}
	if authProxy.GetUser() != "clash" || authProxy.GetPass() != "asd123456" {
		t.Fatalf("http proxy auth = %q/%q, want clash/asd123456", authProxy.GetUser(), authProxy.GetPass())
	}
}

func TestRegistryRefreshCustomerProxy_DisabledRemovesCustomerProxies(t *testing.T) {
	registry := GetRegistry()
	registry.Clear()
	defer registry.Clear()

	if err := registry.RefreshCustomerProxy(&config.Config{
		Customer: &config.CustomerConfig{
			Proxy: &config.CustomerProxyConfig{
				Enable: boolPtr(true),
				Type:   "http",
				Server: "cd.liangsqrt.com:27750",
			},
		},
	}); err != nil {
		t.Fatalf("RefreshCustomerProxy(enabled) error = %v", err)
	}

	if err := registry.RefreshCustomerProxy(&config.Config{
		Customer: &config.CustomerConfig{
			Proxy: &config.CustomerProxyConfig{
				Enable: boolPtr(false),
				Type:   "http",
				Server: "cd.liangsqrt.com:27750",
			},
		},
	}); err != nil {
		t.Fatalf("RefreshCustomerProxy(disabled) error = %v", err)
	}

	if _, err := registry.Get("http"); err == nil {
		t.Fatal("registry.Get(\"http\") error = nil, want disabled customer proxy removed")
	}
	if _, err := registry.Get("socks"); err == nil {
		t.Fatal("registry.Get(\"socks\") error = nil, want disabled customer proxy removed")
	}
}

func TestRegistryRefreshAliangReplacesExistingEntry(t *testing.T) {
	registry := GetRegistry()
	registry.Clear()
	defer registry.Clear()

	if err := registry.RefreshAliang("old-core.example:443"); err != nil {
		t.Fatalf("RefreshAliang(old) error = %v", err)
	}
	oldProxy, err := registry.GetAliang()
	if err != nil {
		t.Fatalf("GetAliang() old error = %v", err)
	}
	if got := oldProxy.Addr(); got != "old-core.example:443" {
		t.Fatalf("old aliang addr = %q, want old-core.example:443", got)
	}

	if err := registry.RefreshAliang("15.nat0.cn:16749"); err != nil {
		t.Fatalf("RefreshAliang(next) error = %v", err)
	}
	nextProxy, err := registry.GetAliang()
	if err != nil {
		t.Fatalf("GetAliang() next error = %v", err)
	}
	if got := nextProxy.Addr(); got != "15.nat0.cn:16749" {
		t.Fatalf("next aliang addr = %q, want 15.nat0.cn:16749", got)
	}
}

func boolPtr(value bool) *bool {
	return &value
}
