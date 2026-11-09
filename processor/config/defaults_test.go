package config

import "testing"

func TestServiceAddressesDefaultToLoopback(t *testing.T) {
	if DefaultManagementAddr != "127.0.0.1:56431" {
		t.Fatalf("DefaultManagementAddr = %q, want loopback", DefaultManagementAddr)
	}
	if DefaultHTTPProxyAddr != "127.0.0.1:56432" {
		t.Fatalf("DefaultHTTPProxyAddr = %q, want loopback", DefaultHTTPProxyAddr)
	}

	resetServiceBindHost(t)
	if got := ManagementListenAddr(); got != DefaultManagementAddr {
		t.Fatalf("ManagementListenAddr() = %q, want %q", got, DefaultManagementAddr)
	}
	if got := HTTPProxyListenAddr(); got != DefaultHTTPProxyAddr {
		t.Fatalf("HTTPProxyListenAddr() = %q, want %q", got, DefaultHTTPProxyAddr)
	}
}

func TestSetServiceBindHostExposesBothServices(t *testing.T) {
	resetServiceBindHost(t)
	userAgentAddr := DefaultUserAgentAddr

	if err := SetServiceBindHost("0.0.0.0"); err != nil {
		t.Fatalf("SetServiceBindHost() error = %v", err)
	}
	if got := ManagementListenAddr(); got != "0.0.0.0:56431" {
		t.Fatalf("ManagementListenAddr() = %q", got)
	}
	if got := HTTPProxyListenAddr(); got != "0.0.0.0:56432" {
		t.Fatalf("HTTPProxyListenAddr() = %q", got)
	}

	if DefaultUserAgentAddr != userAgentAddr {
		t.Fatalf("user agent address changed from %q to %q", userAgentAddr, DefaultUserAgentAddr)
	}
}

func TestSetServiceBindHostRejectsInvalidValueWithoutMutation(t *testing.T) {
	resetServiceBindHost(t)

	if err := SetServiceBindHost("0.0.0.0:56431"); err == nil {
		t.Fatal("SetServiceBindHost() accepted a host containing a port")
	}
	if got := ServiceBindHost(); got != DefaultServiceBindHost {
		t.Fatalf("ServiceBindHost() = %q after invalid update", got)
	}
}

// TestUnifiedIngressListenAddrFollowsMeshSwitch 统一入口 56432 的绑定面
// (spec D2):mesh 总开关生效 → 0.0.0.0(对端 LAN 可否达,与旧 LAN 载体
// parity);否则沿用 ServiceBindHost 的安全默认回环。三态指针语义由
// EffectiveEnabled 定,此处只验绑定面映射。
func TestUnifiedIngressListenAddrFollowsMeshSwitch(t *testing.T) {
	resetServiceBindHost(t)
	ResetGlobalConfigForTest()
	t.Cleanup(ResetGlobalConfigForTest)

	if got := UnifiedIngressListenAddr(); got != "127.0.0.1:56432" {
		t.Fatalf("未配置 mesh 时 UnifiedIngressListenAddr() = %q, want 127.0.0.1:56432", got)
	}

	// Enable 显式 false → 回环(mesh 关不放大暴露面)。
	off := false
	SetGlobalConfig(&Config{Customer: &CustomerConfig{Mesh: &MeshConfig{Enable: &off}}})
	if got := UnifiedIngressListenAddr(); got != "127.0.0.1:56432" {
		t.Fatalf("mesh 显式关时 = %q, want 127.0.0.1:56432", got)
	}

	// Enable 显式 true → 0.0.0.0。
	on := true
	SetGlobalConfig(&Config{Customer: &CustomerConfig{Mesh: &MeshConfig{Enable: &on}}})
	if got := UnifiedIngressListenAddr(); got != "0.0.0.0:56432" {
		t.Fatalf("mesh 开时 = %q, want 0.0.0.0:56432", got)
	}

	// Enable nil(默认开)→ 0.0.0.0。
	SetGlobalConfig(&Config{Customer: &CustomerConfig{Mesh: &MeshConfig{}}})
	if got := UnifiedIngressListenAddr(); got != "0.0.0.0:56432" {
		t.Fatalf("mesh Enable=nil(默认开)时 = %q, want 0.0.0.0:56432", got)
	}

	// mesh 开且管理员指定了别的 --host:LAN 可达优先于回环偏好,仍是 0.0.0.0。
	if err := SetServiceBindHost("192.168.1.9"); err != nil {
		t.Fatalf("SetServiceBindHost: %v", err)
	}
	if got := UnifiedIngressListenAddr(); got != "0.0.0.0:56432" {
		t.Fatalf("mesh 开 + 指定 host 时 = %q, want 0.0.0.0:56432", got)
	}
}

func resetServiceBindHost(t *testing.T) {
	t.Helper()
	if err := SetServiceBindHost(DefaultServiceBindHost); err != nil {
		t.Fatalf("reset service bind host: %v", err)
	}
	t.Cleanup(func() {
		_ = SetServiceBindHost(DefaultServiceBindHost)
	})
}
