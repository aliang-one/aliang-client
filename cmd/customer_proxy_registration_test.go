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
}
