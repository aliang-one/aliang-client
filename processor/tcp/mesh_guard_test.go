package tcp

import (
	"context"
	"strings"
	"testing"

	M "aliang.one/nursorgate/inbound/tun/metadata"
)

func TestDialViaMeshRejectsInjected(t *testing.T) {
	h := &TCPConnectionHandler{}
	md := &M.Metadata{Route: "toNode:dev-b", MeshInjected: true}
	_, err := h.dialViaMesh(context.Background(), md)
	if err == nil || !strings.Contains(err.Error(), "loop guard") {
		t.Fatalf("injected traffic must be rejected with loop guard error, got %v", err)
	}
}

func TestDialMeshFallbackNodeRejectsInjected(t *testing.T) {
	h := &TCPConnectionHandler{}
	md := &M.Metadata{MeshInjected: true}
	if _, err := h.dialMeshFallbackNode(context.Background(), md, "1.2.3.4:56432"); err == nil || !strings.Contains(err.Error(), "loop guard") {
		t.Fatalf("fallback dial must reject injected traffic, got %v", err)
	}
}
