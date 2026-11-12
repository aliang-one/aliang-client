package config

import "testing"

// meshCfgFor 用同一个 bool 同时喂 Mesh.Enable 与 SSHIngress.Enable:依赖门
// 的两行断言(双开→生效 / ssh 关→降级)本就要求两闸同起同落。Mesh 块缺失
// 的形态(nil)与共享 bool 冲突,助手表达不了——由用例内单独构造。
func meshCfgFor(on bool) *Config {
	sshOn := on
	return &Config{Customer: &CustomerConfig{
		Mesh:       &MeshConfig{Enable: &sshOn, PrivateKey: "k", Peers: []MeshPeer{{DeviceID: "dev-b", HostKey: "h"}}},
		SSHIngress: &SSHIngressConfig{Enable: &sshOn},
	}}
}

func TestMeshEffectivelyEnabledFollowsSSHIngress(t *testing.T) {
	if !meshCfgFor(true).MeshEffectivelyEnabled() {
		t.Fatal("both on → effective")
	}
	if meshCfgFor(false).MeshEffectivelyEnabled() {
		t.Fatal("ssh off → degraded")
	}
	var nilCfg *Config
	if nilCfg.MeshEffectivelyEnabled() {
		t.Fatal("nil config → false")
	}
	// Mesh 块缺失 + SSH 入口开:EffectiveEnabled 的 Mesh==nil 默认开语义
	// 放行,依赖门只看 SSH 入口 → true。
	on := true
	meshNil := &Config{Customer: &CustomerConfig{SSHIngress: &SSHIngressConfig{Enable: &on}}}
	if !meshNil.MeshEffectivelyEnabled() {
		t.Fatal("mesh 块缺失(默认开)+ ssh on → true")
	}
}

func TestValidateMeshDependencyFourCombos(t *testing.T) {
	on, off := true, false
	mk := func(mesh, ssh *bool) *Config {
		return &Config{Customer: &CustomerConfig{
			Mesh:       &MeshConfig{Enable: mesh, PrivateKey: "k", Peers: []MeshPeer{{DeviceID: "d", HostKey: "h"}}},
			SSHIngress: &SSHIngressConfig{Enable: ssh},
		}}
	}
	if err := mk(&on, &on).ValidateMeshDependency(); err != nil {
		t.Fatalf("mesh+ssh: %v", err)
	}
	if err := mk(&off, &off).ValidateMeshDependency(); err != nil {
		t.Fatalf("both off: %v", err)
	}
	if err := mk(&off, &on).ValidateMeshDependency(); err != nil {
		t.Fatalf("mesh off: %v", err)
	}
	if err := mk(&on, &off).ValidateMeshDependency(); err == nil {
		t.Fatal("mesh on + ssh off must be rejected at save time")
	}
	// Mesh 块缺失(JSON 无 mesh 键 = 用户从未触及 mesh)→ nil:硬拒只在
	// 保存携带 mesh 块时触发,不在别的键的保存上误伤存量用户。
	meshMissing := &Config{Customer: &CustomerConfig{SSHIngress: &SSHIngressConfig{Enable: &off}}}
	if err := meshMissing.ValidateMeshDependency(); err != nil {
		t.Fatalf("mesh 块缺失(ssh off)→ nil, got %v", err)
	}
	// Mesh 块在场但 Enable 未置(nil=默认开)+ ssh 关 → 硬拒:默认开语义
	// 与依赖门同源,块在场即视为用户配置过 mesh。
	if err := mk(nil, &off).ValidateMeshDependency(); err == nil {
		t.Fatal("mesh 块在场 Enable 未置(默认开)+ ssh off must be rejected")
	}
}

// (TestMeshFallbackHostKey 已随出口链路改造移除:fallback.host_key 独立
// 字段不再存在,下一跳钉扎从 peers 条目继承,校验归
// TestMeshValidateFallbackNextHopRequired / TestMeshDelegateNextHopUnpinnedPeerFailsClosed
// 见 mesh_fallback_gate_test.go。)
