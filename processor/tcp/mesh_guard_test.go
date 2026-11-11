package tcp

import (
	"context"
	"errors"
	"strings"
	"testing"

	M "aliang.one/nursorgate/inbound/tun/metadata"
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

func TestDialMeshFallbackNodeRejectsInjected(t *testing.T) {
	h := &TCPConnectionHandler{}
	md := &M.Metadata{Route: "RouteMesh", MeshInjected: true}
	_, err := h.dialMeshFallbackNode(context.Background(), md, "1.2.3.4:56432")
	if err == nil || !errors.Is(err, errMeshInjectedLoop) {
		t.Fatalf("fallback dial must reject injected traffic with loop guard sentinel, got %v", err)
	}
	if !strings.Contains(err.Error(), "route=RouteMesh") {
		t.Fatalf("loop guard error must carry route context, got %v", err)
	}
}

func TestMeshOfflineRedirectForAIRejectsInjected(t *testing.T) {
	config.ResetGlobalConfigForTest()
	t.Cleanup(func() { config.ResetGlobalConfigForTest() })
	SetMeshOfflineSignal(func() bool { return true })
	t.Cleanup(func() { SetMeshOfflineSignal(nil) })

	// 注入连接第三守卫(SPEC §6.2):即使离线信号为真,注入连接也不改道。
	addr, ok := meshOfflineRedirectForAI(&M.Metadata{MeshInjected: true})
	if ok || addr != "" {
		t.Fatalf("injected traffic must not be redirected by AI offline redirect, got addr=%q ok=%v", addr, ok)
	}

	// nil 不得 panic,且按既有语义返回(nil = 视同非注入,守卫短路):进入
	// 离线信号与兜底门控判定(此处信号真但 mesh 未配置 → 不改道)。
	addr, ok = meshOfflineRedirectForAI(nil)
	if ok || addr != "" {
		t.Fatalf("nil metadata must follow existing semantics (no redirect), got addr=%q ok=%v", addr, ok)
	}
}
