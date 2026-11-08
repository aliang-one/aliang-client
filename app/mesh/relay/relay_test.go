package relay

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	M "aliang.one/nursorgate/inbound/tun/metadata"
	"aliang.one/nursorgate/outbound/meshproto"
	"aliang.one/nursorgate/processor/config"
	"aliang.one/nursorgate/processor/tcp"
)

// relayCaptureHandler 捕获 mesh_inbound 注入到管线的 (conn, metadata)。
type relayCaptureHandler struct {
	events chan relayCaptured
}

type relayCaptured struct {
	conn net.Conn
	md   *M.Metadata
}

func newRelayCapture() *relayCaptureHandler {
	return &relayCaptureHandler{events: make(chan relayCaptured, 8)}
}

func (h *relayCaptureHandler) Handle(_ context.Context, conn net.Conn, md *M.Metadata) error {
	h.events <- relayCaptured{conn: conn, md: md}
	return nil
}

// relayTestDeps 构造放行 dev-a 的 deps 提供器(测试按需覆写)。
func relayTestDeps(handler tcp.TCPConnHandler) DepsFunc {
	return func() tcp.MeshInboundDeps {
		return tcp.MeshInboundDeps{
			SelfID:         "dev-b",
			MaxHops:        meshproto.DefaultHops,
			ForwardEnabled: true,
			PeerAllowed:    func(deviceID string) bool { return deviceID == "dev-a" },
			Handler:        handler,
		}
	}
}

func relayFrameLine(t *testing.T, target, class string) string {
	t.Helper()
	h := &meshproto.Header{
		From:   "dev-a",
		Class:  class,
		Target: target,
		Via:    []string{"dev-a"},
	}
	line, err := h.Encode()
	if err != nil {
		t.Fatal(err)
	}
	return line
}

func dialRelay(t *testing.T, addr net.Addr, header string) net.Conn {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr.String(), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write([]byte(header)); err != nil {
		t.Fatal(err)
	}
	return conn
}

func waitRelayCapture(t *testing.T, capture *relayCaptureHandler) relayCaptured {
	t.Helper()
	select {
	case got := <-capture.events:
		return got
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for inbound dispatch")
		return relayCaptured{}
	}
}

func TestRelayResidentListenerAcceptsMultipleConns(t *testing.T) {
	// 常驻性钉死:连续三条连接都被受理。agent_peer_lan.go:311-320 的
	// 「第一条连接后即关摊」一次性口是反面教材,不许复制。
	capture := newRelayCapture()
	r := New(relayTestDeps(capture))
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() { _ = r.Serve(ln, meshproto.CarrierLAN) }()

	for i := 0; i < 3; i++ {
		target := fmt.Sprintf("host%d.test:8443", i)
		conn := dialRelay(t, ln.Addr(), relayFrameLine(t, target, meshproto.ClassSelf))
		got := waitRelayCapture(t, capture)
		if got.md == nil || got.md.HostName != fmt.Sprintf("host%d.test", i) {
			t.Fatalf("conn %d: metadata = %+v, want host%d.test", i, got.md, i)
		}
		_ = conn.Close()
		_ = got.conn.Close()
	}
}

func TestRelayLANCarrierDispatchesToInbound(t *testing.T) {
	// LAN 载体准入:custom 类别仅 LAN 合法(design §4)——本测试用 custom
	// 反证 Serve 传入的 carrier 确为 "lan"(错传 relay 会被类别闸拒)。
	capture := newRelayCapture()
	r := New(relayTestDeps(capture))
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() { _ = r.Serve(ln, meshproto.CarrierLAN) }()

	conn := dialRelay(t, ln.Addr(), relayFrameLine(t, "c.test:443", meshproto.ClassCustom))
	got := waitRelayCapture(t, capture)
	if got.md == nil || got.md.HostName != "c.test" || got.md.DstPort != 443 {
		t.Fatalf("metadata = %+v, want c.test:443", got.md)
	}
	_ = conn.Close()
	_ = got.conn.Close()
}

func TestRelayErrorLineWrittenBackOnReject(t *testing.T) {
	// 拒绝时对端必须读到一行错误文本(design §3.3 错误回源),随后断链。
	capture := newRelayCapture()
	deps := relayTestDeps(capture)
	r := New(func() tcp.MeshInboundDeps {
		d := deps()
		d.ForwardEnabled = false
		return d
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() { _ = r.Serve(ln, meshproto.CarrierLAN) }()

	conn := dialRelay(t, ln.Addr(), relayFrameLine(t, "a.test:443", meshproto.ClassSelf))
	defer conn.Close()
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Fatalf("expected one error line, got err=%v line=%q", err, line)
	}
	if strings.TrimSpace(line) == "" {
		t.Fatal("error line must not be empty")
	}
	if !strings.Contains(line, "forward disabled") {
		t.Fatalf("error line %q missing gate reason", line)
	}
	n, err := conn.Read(make([]byte, 16))
	if n != 0 || !errors.Is(err, io.EOF) {
		t.Fatalf("expected EOF after error line, got n=%d err=%v", n, err)
	}
}

func TestProductionDepsMapsMeshConfig(t *testing.T) {
	prev := config.GetGlobalConfig()
	defer config.SetGlobalConfig(prev)

	forwardEnable := true
	config.SetGlobalConfig(&config.Config{
		Customer: &config.CustomerConfig{
			Mesh: &config.MeshConfig{
				ForwardEnable: &forwardEnable,
				MaxHops:       3,
				Peers:         []config.MeshPeer{{DeviceID: "dev-a"}},
			},
		},
	})
	deps := ProductionDeps("dev-b")()
	if deps.SelfID != "dev-b" {
		t.Fatalf("SelfID = %q", deps.SelfID)
	}
	if !deps.ForwardEnabled {
		t.Fatal("ForwardEnabled = false, want true")
	}
	if deps.MaxHops != 3 {
		t.Fatalf("MaxHops = %d, want 3", deps.MaxHops)
	}
	if deps.PeerAllowed == nil || !deps.PeerAllowed("dev-a") {
		t.Fatal("dev-a must be an allowed forwarder")
	}
	if deps.PeerAllowed("dev-x") {
		t.Fatal("dev-x must not be an allowed forwarder")
	}
	if deps.Handler == nil {
		t.Fatal("production deps must carry the pipeline handler")
	}

	// Mesh==nil → 不参与:门禁关 + ACL 空(nil = mesh_inbound 全拒)。
	config.SetGlobalConfig(nil)
	nilDeps := ProductionDeps("dev-b")()
	if nilDeps.ForwardEnabled {
		t.Fatal("Mesh==nil must map to ForwardEnabled=false")
	}
	if nilDeps.PeerAllowed != nil {
		t.Fatal("Mesh==nil must map to nil (deny-all) ACL")
	}
	if nilDeps.MaxHops != meshproto.DefaultHops {
		t.Fatalf("MaxHops = %d, want default %d", nilDeps.MaxHops, meshproto.DefaultHops)
	}
	if nilDeps.Handler == nil {
		t.Fatal("handler must still be wired for nil mesh config")
	}
}
