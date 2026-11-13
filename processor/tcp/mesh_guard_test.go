package tcp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	M "aliang.one/nursorgate/inbound/tun/metadata"
	"aliang.one/nursorgate/outbound/meshproto"
	"aliang.one/nursorgate/processor/config"
)

func TestDialViaMeshRejectsInjected(t *testing.T) {
	h := &TCPConnectionHandler{}
	md := &M.Metadata{Route: "toNode:dev-b", MeshInjected: true}
	_, err := h.dialViaMesh(context.Background(), md)
	if err == nil || !errors.Is(err, errMeshInjectedLoop) {
		t.Fatalf("injected traffic must be rejected with loop guard sentinel, got %v", err)
	}
	if !strings.Contains(err.Error(), "route=toNode:dev-b") {
		t.Fatalf("loop guard error must carry route context, got %v", err)
	}
}

// TestMeshDelegateGuardRequiresSelfID 本机节点标识未接线 → 委托不成立
// (fail-closed:没有身份就无法判回环,宁可不上链)。
func TestMeshDelegateGuardRequiresSelfID(t *testing.T) {
	newMeshDialTestEnv(t)
	config.SetGlobalConfig(&config.Config{Customer: &config.CustomerConfig{
		Mesh: &config.MeshConfig{
			Fallback: &config.MeshFallbackConfig{Enable: boolPtr(true), NextHop: "dev-b"},
			Peers:    []config.MeshPeer{{DeviceID: "dev-b", HostKey: "SHA256:x"}},
		},
		SSHIngress: &config.SSHIngressConfig{Enable: boolPtr(true)},
	}})
	h := &TCPConnectionHandler{}
	if h.meshDelegateEligible(aiRedirectMetadata()) {
		t.Fatal("本机标识未接线时委托必须不成立(fail-closed)")
	}
}

// TestMeshDelegateGuardLoopAndCap 链守卫两态:via 已含本机 id → 回环拒;
// via 长度达 max_hops(默认 meshproto.DefaultHops)→ 放大上限拒。守卫命中
// 即本机是链的终点(调用方就地处理)。
func TestMeshDelegateGuardLoopAndCap(t *testing.T) {
	newMeshDialTestEnv(t)
	config.SetGlobalConfig(&config.Config{Customer: &config.CustomerConfig{
		Mesh: &config.MeshConfig{
			Fallback: &config.MeshFallbackConfig{Enable: boolPtr(true), NextHop: "dev-b"},
			Peers:    []config.MeshPeer{{DeviceID: "dev-b", HostKey: "SHA256:x"}},
		},
		SSHIngress: &config.SSHIngressConfig{Enable: boolPtr(true)},
	}})
	swapMeshSelfID(t, "dev-a")
	t.Cleanup(func() { SetMeshSelfIDSource(nil) })

	h := &TCPConnectionHandler{}
	loop := aiRedirectMetadata()
	loop.MeshVia = []string{"dev-a"}
	if h.meshDelegateEligible(loop) {
		t.Fatal("via 含自己(回环)必须拒")
	}
	cap := aiRedirectMetadata()
	for i := 0; i < meshproto.DefaultHops; i++ {
		cap.MeshVia = append(cap.MeshVia, fmt.Sprintf("hop-%d", i))
	}
	if h.meshDelegateEligible(cap) {
		t.Fatal("via 达 max_hops(放大上限)必须拒")
	}
	// 空链 + 开关开 → 通过(委托资格成立)。
	fresh := aiRedirectMetadata()
	if !h.meshDelegateEligible(fresh) {
		t.Fatal("空 via + 开关开应有委托资格")
	}
}

// TestMeshDelegateActiveProjection 布尔投影与守卫同源同真假:开关关 → false,
// 开关开(配置齐备)→ true。
func TestMeshDelegateActiveProjection(t *testing.T) {
	newMeshDialTestEnv(t)
	if MeshDelegateActive() {
		t.Fatal("mesh 未配置时投影必须为假")
	}
	config.SetGlobalConfig(&config.Config{Customer: &config.CustomerConfig{
		Mesh: &config.MeshConfig{
			Fallback: &config.MeshFallbackConfig{Enable: boolPtr(true), NextHop: "dev-b"},
			Peers:    []config.MeshPeer{{DeviceID: "dev-b", HostKey: "SHA256:x"}},
		},
		SSHIngress: &config.SSHIngressConfig{Enable: boolPtr(true)},
	}})
	if !MeshDelegateActive() {
		t.Fatal("开关开 + next_hop 指向已钉扎 peer 时投影必须为真")
	}
}
