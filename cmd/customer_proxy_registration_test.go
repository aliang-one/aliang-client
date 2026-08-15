package cmd

import (
	"testing"

	"aliang.one/nursorgate/outbound"
	"aliang.one/nursorgate/outbound/proxy/proto"
	"aliang.one/nursorgate/processor/config"
)

func TestRegisterCustomerProxy_HTTPRegistersHTTPOutbound(t *testing.T) {
	registry := outbound.GetRegistry()
	registry.Clear()
	defer registry.Clear()

	cfg := &config.Config{
		Customer: &config.CustomerConfig{
			Proxy: &config.CustomerProxyConfig{
				Type:   "http",
				Server: "127.0.0.1:8080",
			},
		},
	}

	if err := registerCustomerProxy(cfg); err != nil {
		t.Fatalf("registerCustomerProxy() error = %v", err)
	}

	registeredProxy, err := registry.Get("http")
	if err != nil {
		t.Fatalf("registry.Get(\"http\") error = %v", err)
	}
	if got := registeredProxy.Proto(); got != proto.HTTP {
		t.Fatalf("registered proxy proto = %s, want %s", got, proto.HTTP)
	}
	if got := registeredProxy.Addr(); got != "127.0.0.1:8080" {
		t.Fatalf("registered proxy addr = %q, want 127.0.0.1:8080", got)
	}
}

func TestRegisterCustomerProxy_Socks5RegistersSocksOutbound(t *testing.T) {
	registry := outbound.GetRegistry()
	registry.Clear()
	defer registry.Clear()

	cfg := &config.Config{
		Customer: &config.CustomerConfig{
			Proxy: &config.CustomerProxyConfig{
				Type:   "socks5",
				Server: "127.0.0.1:1080",
			},
		},
	}

	if err := registerCustomerProxy(cfg); err != nil {
		t.Fatalf("registerCustomerProxy() error = %v", err)
	}

	registeredProxy, err := registry.Get("socks")
	if err != nil {
		t.Fatalf("registry.Get(\"socks\") error = %v", err)
	}
	if got := registeredProxy.Proto(); got != proto.Socks5 {
		t.Fatalf("registered proxy proto = %s, want %s", got, proto.Socks5)
	}
	if got := registeredProxy.Addr(); got != "127.0.0.1:1080" {
		t.Fatalf("registered proxy addr = %q, want 127.0.0.1:1080", got)
	}
}

func TestRegisterCustomerProxy_HTTPURLAuthRegistersOutboundAuth(t *testing.T) {
	registry := outbound.GetRegistry()
	registry.Clear()
	defer registry.Clear()

	cfg := &config.Config{
		Core: &config.CoreConfig{APIServer: "https://api.aliang.one"},
		Customer: &config.CustomerConfig{
			Proxy: &config.CustomerProxyConfig{
				Type:   "http",
				Server: "http://user:pass@cd.liangsqrt.com:27750",
			},
		},
	}

	if err := registerCustomerProxy(cfg); err != nil {
		t.Fatalf("registerCustomerProxy() error = %v", err)
	}

	registeredProxy, err := registry.Get("http")
	if err != nil {
		t.Fatalf("registry.Get(\"http\") error = %v", err)
	}
	if got := registeredProxy.Addr(); got != "cd.liangsqrt.com:27750" {
		t.Fatalf("registered proxy addr = %q, want cd.liangsqrt.com:27750", got)
	}

	authProxy, ok := registeredProxy.(interface {
		GetUser() string
		GetPass() string
	})
	if !ok {
		t.Fatalf("registered proxy does not expose HTTP auth getters: %T", registeredProxy)
	}
	if authProxy.GetUser() != "user" || authProxy.GetPass() != "pass" {
		t.Fatalf("registered proxy auth = %q/%q, want user/pass", authProxy.GetUser(), authProxy.GetPass())
	}
}

func TestRegisterCustomerProxy_SocksIPv6UsesJoinHostPort(t *testing.T) {
	registry := outbound.GetRegistry()
	registry.Clear()
	defer registry.Clear()

	cfg := &config.Config{
		Core: &config.CoreConfig{APIServer: "https://api.aliang.one"},
		Customer: &config.CustomerConfig{
			Proxy: &config.CustomerProxyConfig{
				Type:   "socks5",
				Server: "[2001:db8::1]:1080",
			},
		},
	}

	if err := registerCustomerProxy(cfg); err != nil {
		t.Fatalf("registerCustomerProxy() error = %v", err)
	}

	registeredProxy, err := registry.Get("socks")
	if err != nil {
		t.Fatalf("registry.Get(\"socks\") error = %v", err)
	}
	if got := registeredProxy.Addr(); got != "[2001:db8::1]:1080" {
		t.Fatalf("registered proxy addr = %q, want [2001:db8::1]:1080", got)
	}
}
