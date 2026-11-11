package mesh

import (
	"context"
	"net"
	"testing"

	"aliang.one/nursorgate/outbound/meshproto"
)

func TestTransportAdmitMatrix(t *testing.T) {
	peerWithAddr := PeerSpec{LANAddrs: []string{"1.2.3.4:56432"}}
	peerNoAddr := PeerSpec{}
	lan := LANTransport{}
	piko := PikoTransport{dial: func(context.Context, string) (net.Conn, error) { return nil, nil }}

	if !lan.Admit(meshproto.ClassCustom, peerWithAddr) || !lan.Admit(meshproto.ClassSelf, peerWithAddr) || !lan.Admit(meshproto.ClassAI, peerWithAddr) {
		t.Fatal("lan admits all classes when addrs present")
	}
	if lan.Admit(meshproto.ClassCustom, peerNoAddr) || lan.Admit(meshproto.ClassAI, peerNoAddr) {
		t.Fatal("lan requires non-empty LANAddrs regardless of class")
	}
	if piko.Admit(meshproto.ClassCustom, peerWithAddr) {
		t.Fatal("custom must not ride relay")
	}
	if !piko.Admit(meshproto.ClassSelf, peerWithAddr) || !piko.Admit(meshproto.ClassAI, peerWithAddr) {
		t.Fatal("self/ai may ride relay")
	}
}

func TestPikoTransportBuildsEndpointID(t *testing.T) {
	var got string
	piko := PikoTransport{dial: func(_ context.Context, endpointID string) (net.Conn, error) {
		got = endpointID
		return nil, nil
	}}
	_, _ = piko.DialSSHD(context.Background(), PeerSpec{DeviceID: "dev-b"})
	if got != "dev-b.mesh" {
		t.Fatalf("endpoint id = %q", got)
	}
}

// TestLanTransportDialsInOrder 钉住 LAN 顺序尝试:第一个地址失败落第二个。
func TestLanTransportDialsInOrder(t *testing.T) {
	// 127.0.0.1:1 必然 connection refused;第二个口起真监听。
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	_, port, _ := net.SplitHostPort(l.Addr().String())
	conn, derr := LANTransport{}.DialSSHD(context.Background(), PeerSpec{
		LANAddrs: []string{"127.0.0.1:1", net.JoinHostPort("127.0.0.1", port)},
	})
	if derr != nil {
		t.Fatalf("second addr should succeed: %v", derr)
	}
	_ = conn.Close()
}
