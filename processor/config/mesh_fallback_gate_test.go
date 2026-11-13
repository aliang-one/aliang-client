package config

// 判据含 SSH 入口依赖(SPEC §10.3),用例须显式开 ssh_ingress。

// MeshDelegateNextHop 出口链路下一跳助手测试(2026-11-12,替代旧离线兜底
// 助手 MeshFallbackActiveForClass):fallback.enable 显式 true ∧ next_hop
// 指向 peers 中已钉扎的节点 ∧ MeshEffectivelyEnabled(总开关含 SSH 入口
// 依赖降级),条件缺一不可。触发为常驻语义——不按本机在线状态判定。
// tcp/egress 消费方各自的接线回归见各自包测试;此处钉助手自身的语义边界。

import (
	"strings"
	"testing"
)

func boolPtrForMeshGate(v bool) *bool { return &v }

// meshGateConfig 构造给定总开关/兜底配置的最小 Config。SSH 入口显式开:
// 判据切到 MeshEffectivelyEnabled 后,ssh_ingress 未置位会让门恒关,
// 各用例将被依赖降级误吞而非测到各自钉的条件。
func meshGateConfig(master *bool, fb *MeshFallbackConfig, peers []MeshPeer) *Config {
	return &Config{Customer: &CustomerConfig{
		Mesh:       &MeshConfig{Enable: master, Fallback: fb, Peers: peers},
		SSHIngress: &SSHIngressConfig{Enable: boolPtrForMeshGate(true)},
	}}
}

func delegateEnabled(next string) *MeshFallbackConfig {
	return &MeshFallbackConfig{Enable: boolPtrForMeshGate(true), NextHop: next}
}

func pinnedPeer(id string) MeshPeer {
	return MeshPeer{DeviceID: id, LANAddrs: []string{"192.168.1.9:56432"}, HostKey: "SHA256:x"}
}

// TestMeshDelegateNextHopPositive 条件齐备(总开关默认 nil=true)→ 返回
// 下一跳 peer。next_hop 与 peer.device_id 的空白变体规整后匹配。
func TestMeshDelegateNextHopPositive(t *testing.T) {
	cfg := meshGateConfig(nil, delegateEnabled("dev-b"), []MeshPeer{pinnedPeer("dev-b")})
	peer, ok := cfg.MeshDelegateNextHop()
	if !ok {
		t.Fatal("条件齐备应激活")
	}
	if peer.DeviceID != "dev-b" {
		t.Fatalf("next hop = %q, want dev-b", peer.DeviceID)
	}
	// next_hop 带空白同样命中(读侧规整)。
	cfg2 := meshGateConfig(nil, delegateEnabled("  dev-b  "), []MeshPeer{pinnedPeer("dev-b")})
	if _, ok := cfg2.MeshDelegateNextHop(); !ok {
		t.Fatal("next_hop 空白变体规整后应命中")
	}
}

// TestMeshDelegateNextHopRespectsMasterSwitch mesh 总开关显式 false +
// fallback.enable=true → helper 返回 false:总开关契约穿透所有 mesh 消费方。
func TestMeshDelegateNextHopRespectsMasterSwitch(t *testing.T) {
	cfg := meshGateConfig(boolPtrForMeshGate(false), delegateEnabled("dev-b"), []MeshPeer{pinnedPeer("dev-b")})
	if _, ok := cfg.MeshDelegateNextHop(); ok {
		t.Fatal("mesh 总开关显式 false 时委托门必须关闭")
	}
}

// TestMeshDelegateNextHopUnpinnedPeerFailsClosed next_hop 指向的 peer 未钉扎
// (host_key 空)→ 能力不成立(fail-closed,不做 TOFU)。
func TestMeshDelegateNextHopUnpinnedPeerFailsClosed(t *testing.T) {
	peer := pinnedPeer("dev-b")
	peer.HostKey = "   "
	cfg := meshGateConfig(nil, delegateEnabled("dev-b"), []MeshPeer{peer})
	if _, ok := cfg.MeshDelegateNextHop(); ok {
		t.Fatal("下一跳未钉扎必须 fail-closed 关闭")
	}
}

// TestMeshDelegateNextHopGateVariants 其余门控变体:Enable 显式 false /
// next_hop 空/不指向任何 peer / SSH 入口依赖降级 / 配置缺层。
func TestMeshDelegateNextHopGateVariants(t *testing.T) {
	t.Run("Enable显式false", func(t *testing.T) {
		fb := delegateEnabled("dev-b")
		fb.Enable = boolPtrForMeshGate(false)
		if _, ok := meshGateConfig(nil, fb, []MeshPeer{pinnedPeer("dev-b")}).MeshDelegateNextHop(); ok {
			t.Fatal("Enable=false 应关闭")
		}
	})
	t.Run("NextHop空", func(t *testing.T) {
		if _, ok := meshGateConfig(nil, delegateEnabled("   "), []MeshPeer{pinnedPeer("dev-b")}).MeshDelegateNextHop(); ok {
			t.Fatal("next_hop 空白应关闭")
		}
	})
	t.Run("NextHop不指向任何peer", func(t *testing.T) {
		if _, ok := meshGateConfig(nil, delegateEnabled("dev-x"), []MeshPeer{pinnedPeer("dev-b")}).MeshDelegateNextHop(); ok {
			t.Fatal("next_hop 指向不存在的 peer 应关闭")
		}
	})
	t.Run("SSH入口依赖降级", func(t *testing.T) {
		cfg := &Config{Customer: &CustomerConfig{
			Mesh:       &MeshConfig{Fallback: delegateEnabled("dev-b"), Peers: []MeshPeer{pinnedPeer("dev-b")}},
			SSHIngress: &SSHIngressConfig{Enable: boolPtrForMeshGate(false)},
		}}
		if _, ok := cfg.MeshDelegateNextHop(); ok {
			t.Fatal("SSH 入口关闭(依赖降级)时委托必须关闭")
		}
	})
	t.Run("配置缺层", func(t *testing.T) {
		if _, ok := (*Config)(nil).MeshDelegateNextHop(); ok {
			t.Fatal("nil cfg 应关闭")
		}
		if _, ok := (&Config{}).MeshDelegateNextHop(); ok {
			t.Fatal("nil Customer 应关闭")
		}
		if _, ok := (&Config{Customer: &CustomerConfig{}}).MeshDelegateNextHop(); ok {
			t.Fatal("nil Mesh 应关闭")
		}
		if _, ok := meshGateConfig(nil, nil, nil).MeshDelegateNextHop(); ok {
			t.Fatal("nil Fallback 应关闭")
		}
	})
}

// TestMeshValidateFallbackNextHopRequired Validate 保存通道:enable=true 时
// next_hop 必填、须指向 peers 中的节点、该节点须已钉扎;规整就地落盘。
func TestMeshValidateFallbackNextHopRequired(t *testing.T) {
	on := true
	c := &MeshConfig{Fallback: &MeshFallbackConfig{Enable: &on}}

	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "fallback.next_hop required") {
		t.Fatalf("want next_hop required, got %v", err)
	}
	c.Fallback.NextHop = "dev-x"
	err = c.Validate()
	if err == nil || !strings.Contains(err.Error(), "must reference a configured peer") {
		t.Fatalf("want must reference a configured peer, got %v", err)
	}
	c.Peers = []MeshPeer{{DeviceID: "dev-x"}}
	err = c.Validate()
	// 未钉扎 peer 在 peers 循环先被通用规则拦下(兜底块之前),错误口径
	// 是 peers[0].host_key;钉扎缺失的兜底侧拦截见
	// TestMeshDelegateNextHopUnpinnedPeerFailsClosed(读侧 fail-closed)。
	if err == nil || !strings.Contains(err.Error(), "peers[0].host_key required") {
		t.Fatalf("want peers[0].host_key required, got %v", err)
	}
	c.Peers[0].HostKey = " SHA256:x "
	c.PrivateKey = "k" // peers 非空时节点身份私钥保存期必填
	c.Fallback.NextHop = "  dev-x  "
	if err := c.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if c.Fallback.NextHop != "dev-x" {
		t.Fatalf("next_hop 应就地规整落盘, got %q", c.Fallback.NextHop)
	}
	if c.Peers[0].HostKey != "SHA256:x" {
		t.Fatalf("peer host_key 应就地规整落盘, got %q", c.Peers[0].HostKey)
	}
}

// TestValidateMeshNextHopSelfRejected 自指拒绝:next_hop 指向自己 = 没换
// 出口且链式转发必自指成环,保存路径显式拒绝(ValidateMesh)。
func TestValidateMeshNextHopSelfRejected(t *testing.T) {
	cfg := meshGateConfig(nil, delegateEnabled("self-dev"), []MeshPeer{pinnedPeer("other-dev")})
	if err := cfg.ValidateMesh("self-dev"); err == nil || !strings.Contains(err.Error(), "is self") {
		t.Fatalf("want is self, got %v", err)
	}
	// 指向别的设备 → 通过。
	if err := cfg.ValidateMesh("another-dev"); err != nil {
		t.Fatalf("next_hop 非自指应通过: %v", err)
	}
}
