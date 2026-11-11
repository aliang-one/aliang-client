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

// TestUnifiedIngressListenAddrFollowsSSHIngress 统一入口 56432 的绑定面
// (SPEC §5.5,判据已由 mesh 总开关换为 ssh_ingress 开关):
//   - ssh_ingress 开(无论 mesh 开关)→ 0.0.0.0(SSH 入口要有 LAN 可达面
//     才能被对端拨入,绑回环等于把入口静默废掉);
//   - ssh_ingress 关(无论 mesh 开关,包括 mesh 开)→ ServiceBindHost 安全
//     默认回环;
//   - nil cfg / Customer nil / SSHIngress nil(默认关)→ 回环。
//
// 旧用例「mesh 开 → 0.0.0.0」已翻转为「mesh 开 + ssh 关 → 回环」——SPEC
// §5.5 的语义反转点:mesh 收敛进 SSH 入口后,旧判据在两个方向同时错。
// 此处只验绑定面映射,ssh 开但 mesh 关把 HTTP 代理面(socks5/CONNECT)一并
// 暴露到 LAN 的对称风险见 §5.5.1 与发布说明。
func TestUnifiedIngressListenAddrFollowsSSHIngress(t *testing.T) {
	resetServiceBindHost(t)
	ResetGlobalConfigForTest()
	t.Cleanup(ResetGlobalConfigForTest)

	// nil cfg → 回环。
	if got := UnifiedIngressListenAddrFor(nil); got != "127.0.0.1:56432" {
		t.Fatalf("nil cfg 时 UnifiedIngressListenAddrFor = %q, want 127.0.0.1:56432", got)
	}
	// 全局未配置(customer 缺失)→ 回环。
	if got := UnifiedIngressListenAddr(); got != "127.0.0.1:56432" {
		t.Fatalf("未配置 customer 时 UnifiedIngressListenAddr() = %q, want 127.0.0.1:56432", got)
	}
	// Customer 非 nil 但 SSHIngress 未配(nil=默认关)→ 回环。
	SetGlobalConfig(&Config{Customer: &CustomerConfig{}})
	if got := UnifiedIngressListenAddr(); got != "127.0.0.1:56432" {
		t.Fatalf("SSHIngress 未配(nil=关)时 = %q, want 127.0.0.1:56432", got)
	}

	off := false
	on := true

	// ssh 显式关 + mesh 开 → 回环。SPEC §5.5 的语义反转点:旧判据下
	// mesh 开 → 0.0.0.0,新模型下 mesh 开而 ssh 关时组网本就不生效
	// (MeshEffectivelyEnabled 恒 false),白绑 0.0.0.0 只会白暴露 HTTP
	// 代理面,必须收回。
	SetGlobalConfig(&Config{Customer: &CustomerConfig{
		Mesh:       &MeshConfig{Enable: &on},
		SSHIngress: &SSHIngressConfig{Enable: &off},
	}})
	if got := UnifiedIngressListenAddr(); got != "127.0.0.1:56432" {
		t.Fatalf("mesh 开 + ssh 显式关时 = %q, want 127.0.0.1:56432", got)
	}

	// SSHIngress.Enable nil(默认关)+ mesh 开 → 回环(nil=false)。
	SetGlobalConfig(&Config{Customer: &CustomerConfig{Mesh: &MeshConfig{Enable: &on}}})
	if got := UnifiedIngressListenAddr(); got != "127.0.0.1:56432" {
		t.Fatalf("mesh 开 + ssh Enable=nil(默认关)时 = %q, want 127.0.0.1:56432", got)
	}

	// ssh 显式开 + mesh 关 → 0.0.0.0(SSH 入口要 LAN 可达才能被对端拨入)。
	SetGlobalConfig(&Config{Customer: &CustomerConfig{
		Mesh:       &MeshConfig{Enable: &off},
		SSHIngress: &SSHIngressConfig{Enable: &on},
	}})
	if got := UnifiedIngressListenAddr(); got != "0.0.0.0:56432" {
		t.Fatalf("ssh 开 + mesh 关时 = %q, want 0.0.0.0:56432", got)
	}

	// ssh 开 + mesh 开 → 0.0.0.0(判据只看 ssh,与 mesh 无关)。
	SetGlobalConfig(&Config{Customer: &CustomerConfig{
		Mesh:       &MeshConfig{Enable: &on},
		SSHIngress: &SSHIngressConfig{Enable: &on},
	}})
	if got := UnifiedIngressListenAddr(); got != "0.0.0.0:56432" {
		t.Fatalf("ssh 开 + mesh 开时 = %q, want 0.0.0.0:56432", got)
	}

	// ssh 开且管理员指定了别的 --host:LAN 可达优先于回环偏好,仍是 0.0.0.0。
	if err := SetServiceBindHost("192.168.1.9"); err != nil {
		t.Fatalf("SetServiceBindHost: %v", err)
	}
	if got := UnifiedIngressListenAddr(); got != "0.0.0.0:56432" {
		t.Fatalf("ssh 开 + 指定 host 时 = %q, want 0.0.0.0:56432", got)
	}

	// ssh 关 + 指定 host:无 LAN 可达需求,沿用 ServiceBindHost 本身
	// (管理员显式指定的绑定地址),不强制 0.0.0.0 也不强制回环。
	if got := UnifiedIngressListenAddrFor(&Config{Customer: &CustomerConfig{
		SSHIngress: &SSHIngressConfig{Enable: &off},
	}}); got != "192.168.1.9:56432" {
		t.Fatalf("ssh 关 + 指定 host 时 = %q, want 192.168.1.9:56432", got)
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
