package outbound

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	M "aliang.one/nursorgate/inbound/tun/metadata"
	"aliang.one/nursorgate/outbound/proxy/proto"
	sshout "aliang.one/nursorgate/outbound/proxy/ssh"
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

func TestRegistryRefreshCustomerProxy_SSH(t *testing.T) {
	registry := GetRegistry()
	registry.Clear()
	defer registry.Clear()

	cfg := &config.Config{Customer: &config.CustomerConfig{
		Proxy: &config.CustomerProxyConfig{
			Enable: boolPtr(true), Type: "ssh",
			Server: "127.0.0.1:2222", Username: "u", Password: "p", PoolSize: 2,
		},
	}}
	if err := registry.RefreshCustomerProxy(cfg); err != nil {
		t.Fatalf("RefreshCustomerProxy ssh: %v", err)
	}
	p, err := registry.Get("ssh")
	if err != nil {
		t.Fatalf("Get(ssh): %v", err)
	}
	if got := p.Proto(); got != proto.SSH {
		t.Fatalf("ssh proxy proto = %s, want %s", got, proto.SSH)
	}
	if got := p.Addr(); got != "127.0.0.1:2222" {
		t.Fatalf("ssh proxy addr = %q, want 127.0.0.1:2222", got)
	}

	registry.UnregisterCustomerProxies()
	if _, err := registry.Get("ssh"); err == nil {
		t.Fatal("ssh entry must be unregistered")
	}

	// 守护:UnregisterCustomerProxies 不只删条目,还必须 Close 被移除的实例
	// (closeProxyIfSupported 接线)。删掉 close 循环会让泄漏无声复活——此断言
	// 拦在 DialContext 顶部的 closed 检查上,无网络。ctx 带 2s 上限:接线健在
	// 时 ErrClosed 即刻返回;若接线被删,DialContext 会真的去拨池链,2s 后以
	// ctx 错误干净失败而不是无限挂死。
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := p.DialContext(ctx, &M.Metadata{HostName: "127.0.0.1", DstPort: 80}); !errors.Is(err, sshout.ErrClosed) {
		t.Fatalf("DialContext after Unregister error = %v, want ssh.ErrClosed (removed instance must be closed)", err)
	}
}

// TestRegistryRefreshCustomerProxy_SSHMissingAuthRejected 锁定缺认证的拦截层:
// 解析层(EffectiveSSHProxy)只拆 server 串不校验认证;密码与私钥皆空的配置
// 由 registry 分支里的 SSHProxyConfig.Validate 拦下。失败刷新还需把陈旧的
// ssh 条目清干净(Unregister 先于 Register 的既有语义)。
func TestRegistryRefreshCustomerProxy_SSHMissingAuthRejected(t *testing.T) {
	registry := GetRegistry()
	registry.Clear()
	defer registry.Clear()

	valid := &config.Config{Customer: &config.CustomerConfig{
		Proxy: &config.CustomerProxyConfig{
			Enable: boolPtr(true), Type: "ssh",
			Server: "127.0.0.1:2222", Username: "u", Password: "p",
		},
	}}
	if err := registry.RefreshCustomerProxy(valid); err != nil {
		t.Fatalf("RefreshCustomerProxy(valid ssh) error = %v", err)
	}

	bad := &config.Config{Customer: &config.CustomerConfig{
		Proxy: &config.CustomerProxyConfig{
			Enable: boolPtr(true), Type: "ssh",
			Server: "127.0.0.1:2223", Username: "u",
			// Password 与 PrivateKey 均空
		},
	}}
	err := registry.RefreshCustomerProxy(bad)
	if err == nil {
		t.Fatal("RefreshCustomerProxy(ssh without auth) error = nil, want rejection")
	}
	if !strings.Contains(err.Error(), "password or private_key") {
		t.Fatalf("RefreshCustomerProxy(ssh without auth) error = %v, want registry-layer Validate rejection (password or private_key)", err)
	}
	if _, err := registry.Get("ssh"); err == nil {
		t.Fatal("stale ssh entry must be removed after failed refresh")
	}
}

func TestRegistryRefreshCustomerProxy_SSHWithJumps(t *testing.T) {
	registry := GetRegistry()
	registry.Clear()
	defer registry.Clear()

	cfg := &config.Config{Customer: &config.CustomerConfig{
		Proxy: &config.CustomerProxyConfig{
			Enable: boolPtr(true), Type: "ssh",
			Server: "127.0.0.1:2222", Username: "u", Password: "p", PoolSize: 2,
			JumpServers: []config.SSHJumpServerRaw{
				{Server: "10.0.0.1:2201", Username: "hop", Password: "hp"},
			},
		},
	}}
	if err := registry.RefreshCustomerProxy(cfg); err != nil {
		t.Fatalf("RefreshCustomerProxy ssh+jump: %v", err)
	}
	p, err := registry.Get("ssh")
	if err != nil {
		t.Fatalf("Get(ssh): %v", err)
	}
	// Addr 契约:注册地址是终点(跳板组装进 outbound,不体现在 Addr)。
	if got := p.Addr(); got != "127.0.0.1:2222" {
		t.Fatalf("ssh proxy addr = %q, want final endpoint 127.0.0.1:2222", got)
	}
	if got := p.Proto(); got != proto.SSH {
		t.Fatalf("ssh proxy proto = %s, want %s", got, proto.SSH)
	}

	// 类型切换也要把 ssh 条目清掉:UnregisterCustomerProxies 的 ssh delete
	// 不只在显式调用时生效,刷新路径同样经过它。
	switchCfg := &config.Config{Customer: &config.CustomerConfig{
		Proxy: &config.CustomerProxyConfig{
			Enable: boolPtr(true), Type: "socks5",
			Server: "182.138.136.39:27751",
		},
	}}
	if err := registry.RefreshCustomerProxy(switchCfg); err != nil {
		t.Fatalf("RefreshCustomerProxy(socks5) error = %v", err)
	}
	if _, err := registry.Get("ssh"); err == nil {
		t.Fatal("registry.Get(\"ssh\") error = nil, want ssh removed after type switch")
	}
}

func boolPtr(value bool) *bool {
	return &value
}
