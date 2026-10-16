package tcp

import (
	"context"
	"net"
	"net/netip"
	"testing"

	M "aliang.one/nursorgate/inbound/tun/metadata"
	"aliang.one/nursorgate/outbound"
	"aliang.one/nursorgate/processor/config"
)

const toSocksDisabledCanonicalRaw = `{
	"version": 1,
	"ingress": {"mode": "tun"},
	"egress": {
		"direct": {"enabled": true},
		"toAliang": {"enabled": true},
		"toSocks": {"enabled": false, "upstream": {"type": "socks"}}
	},
	"routing": {"rules": [], "default_egress": "direct"}
}`

func applyToSocksDisabledCanonical(t *testing.T) {
	t.Helper()
	if _, err := config.GetRoutingApplyStore().Apply([]byte(toSocksDisabledCanonicalRaw), func(cfg *config.CanonicalRoutingSchema) (any, error) {
		return cfg, nil
	}); err != nil {
		t.Fatalf("apply toSocks-disabled canonical failed: %v", err)
	}
}

func setEnabledCustomerProxyConfig(t *testing.T) {
	t.Helper()
	config.SetGlobalConfig(&config.Config{
		Customer: &config.CustomerConfig{
			Proxy: &config.CustomerProxyConfig{
				Enable: boolPtr(true),
				Type:   "socks5",
				Server: "127.0.0.1:1", // 已配置但故意不可达：证明不因 upstream 存在而走 socks
			},
		},
	})
}

// 用户关闭 toSocks 后，即使 customer 上游仍配置（upstreamConfigured=true），
// 执行层也必须拒绝 toSocks 分支——不再被上游配置短路重新启用。
func TestGetToSocksProxyForExecution_DisabledDeniesEvenWithUpstreamConfigured(t *testing.T) {
	config.ResetGlobalConfigForTest()
	config.ResetRoutingApplyStoreForTest()
	t.Cleanup(func() {
		config.ResetGlobalConfigForTest()
		config.ResetRoutingApplyStoreForTest()
	})

	registry := outbound.GetRegistry()
	registry.Clear()
	t.Cleanup(registry.Clear)
	unreachable, err := outbound.CreateSocksProxy("127.0.0.1:1", "", "")
	if err != nil {
		t.Fatalf("CreateSocksProxy() error = %v", err)
	}
	if err := registry.Register("socks", unreachable); err != nil {
		t.Fatalf("register socks proxy failed: %v", err)
	}

	setEnabledCustomerProxyConfig(t)
	applyToSocksDisabledCanonical(t)

	handler := &TCPConnectionHandler{}
	_, _, err = handler.getToSocksProxyForExecution()
	if err == nil {
		t.Fatal("getToSocksProxyForExecution() error = nil, want deny when toSocks disabled")
	}
	if !IsBranchDenyError(err) {
		t.Fatalf("error is not BranchDenyError: %v", err)
	}
	if got := BranchDenyReason(err); got != DenyReasonToSocksDisabled {
		t.Fatalf("deny reason = %q, want %q", got, DenyReasonToSocksDisabled)
	}
}

// toSocks 关闭 + 上游已配：dialViaSocksOrDirect 降级 direct（defaultFallbackRoute
// 语义），连接不断链、不走 socks。
func TestDialViaSocksOrDirect_DisabledToSocksDegradesToDirect(t *testing.T) {
	config.ResetGlobalConfigForTest()
	config.ResetRoutingApplyStoreForTest()
	t.Cleanup(func() {
		config.ResetGlobalConfigForTest()
		config.ResetRoutingApplyStoreForTest()
	})

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen failed: %v", err)
	}
	defer listener.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			accepted <- conn
		}
	}()

	registry := outbound.GetRegistry()
	registry.Clear()
	t.Cleanup(registry.Clear)
	// 注册一个必然失败的 socks 代理：若实现仍走 socks，拨号会失败而非降级
	unreachable, err := outbound.CreateSocksProxy("127.0.0.1:1", "", "")
	if err != nil {
		t.Fatalf("CreateSocksProxy() error = %v", err)
	}
	if err := registry.Register("socks", unreachable); err != nil {
		t.Fatalf("register socks proxy failed: %v", err)
	}

	setEnabledCustomerProxyConfig(t)
	applyToSocksDisabledCanonical(t)

	handler := &TCPConnectionHandler{}
	metadata := &M.Metadata{
		Network: M.TCP,
		DstIP:   netip.MustParseAddr("127.0.0.1"),
		DstPort: uint16(listener.Addr().(*net.TCPAddr).Port),
	}

	conn, err := handler.dialViaSocksOrDirect(context.Background(), metadata)
	if err != nil {
		t.Fatalf("dialViaSocksOrDirect() error = %v, want direct degrade (no chain break)", err)
	}
	defer conn.Close()

	if got := metadata.Route; got != "RouteDirect" {
		t.Fatalf("metadata.Route = %q, want RouteDirect", got)
	}

	peer := <-accepted
	peer.Close()
}

// 回归保护：toSocks 开启 + 上游已配时仍走 socks（既有行为不变）。
func TestGetToSocksProxyForExecution_EnabledStillSelectsSocks(t *testing.T) {
	config.ResetGlobalConfigForTest()
	config.ResetRoutingApplyStoreForTest()
	t.Cleanup(func() {
		config.ResetGlobalConfigForTest()
		config.ResetRoutingApplyStoreForTest()
	})

	registry := outbound.GetRegistry()
	registry.Clear()
	t.Cleanup(registry.Clear)
	socksProxy, err := outbound.CreateSocksProxy("127.0.0.1:1080", "", "")
	if err != nil {
		t.Fatalf("CreateSocksProxy() error = %v", err)
	}
	if err := registry.Register("socks", socksProxy); err != nil {
		t.Fatalf("register socks proxy failed: %v", err)
	}

	config.SetGlobalConfig(&config.Config{
		Customer: &config.CustomerConfig{
			Proxy: &config.CustomerProxyConfig{
				Enable: boolPtr(true),
				Type:   "socks5",
				Server: "127.0.0.1:1080",
			},
		},
	})
	if _, err := config.GetRoutingApplyStore().Apply([]byte(`{
		"version": 1,
		"ingress": {"mode": "tun"},
		"egress": {
			"direct": {"enabled": true},
			"toAliang": {"enabled": true},
			"toSocks": {"enabled": true, "upstream": {"type": "socks"}}
		},
		"routing": {"rules": [], "default_egress": "direct"}
	}`), func(cfg *config.CanonicalRoutingSchema) (any, error) {
		return cfg, nil
	}); err != nil {
		t.Fatalf("apply enabled canonical failed: %v", err)
	}

	handler := &TCPConnectionHandler{}
	selected, upstreamType, err := handler.getToSocksProxyForExecution()
	if err != nil {
		t.Fatalf("getToSocksProxyForExecution() error = %v, want socks selected", err)
	}
	if upstreamType != toSocksUpstreamTypeSocks {
		t.Fatalf("upstreamType = %q, want socks", upstreamType)
	}
	if selected == nil {
		t.Fatal("selected proxy is nil, want socks proxy")
	}
}
