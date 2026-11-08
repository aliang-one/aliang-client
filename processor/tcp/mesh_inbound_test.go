package tcp

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	M "aliang.one/nursorgate/inbound/tun/metadata"
	"aliang.one/nursorgate/outbound/meshproto"
)

// meshInboundCaptureHandler 测试内 handler seam:捕获注入的 (conn, metadata),
// 断言注入形态而非真实处理(design §3.3 注入语义的测试锚)。
type meshInboundCaptureHandler struct {
	events chan meshInboundCaptured
}

type meshInboundCaptured struct {
	conn net.Conn
	md   *M.Metadata
}

func newMeshInboundCapture() *meshInboundCaptureHandler {
	return &meshInboundCaptureHandler{events: make(chan meshInboundCaptured, 8)}
}

func (h *meshInboundCaptureHandler) Handle(_ context.Context, conn net.Conn, md *M.Metadata) error {
	h.events <- meshInboundCaptured{conn: conn, md: md}
	return nil
}

// meshInboundDeps 构造默认放行 dev-a 的 deps(测试按需覆写字段)。
func meshInboundDeps(handler TCPConnHandler) MeshInboundDeps {
	return MeshInboundDeps{
		SelfID:         "dev-b",
		MaxHops:        meshproto.DefaultHops,
		ForwardEnabled: true,
		PeerAllowed:    func(deviceID string) bool { return deviceID == "dev-a" },
		Handler:        handler,
	}
}

// meshFrameLine 构造合法帧头行(From=dev-a → dev-b,self 类别)。
func meshFrameLine(t *testing.T, mutate func(*meshproto.Header)) string {
	t.Helper()
	h := &meshproto.Header{
		From:   "dev-a",
		Class:  meshproto.ClassSelf,
		Target: "a.test:443",
		Via:    []string{"dev-a"},
		Hops:   0,
	}
	if mutate != nil {
		mutate(h)
	}
	line, err := h.Encode()
	if err != nil {
		t.Fatal(err)
	}
	return line
}

// startMeshInbound 起一个 loopback 监听,每个 accept 交 HandleMeshConn。
func startMeshInbound(t *testing.T, carrier string, deps MeshInboundDeps) (net.Addr, <-chan error) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	errs := make(chan error, 16)
	go func() {
		for {
			conn, acceptErr := ln.Accept()
			if acceptErr != nil {
				return
			}
			go func(c net.Conn) { errs <- HandleMeshConn(c, carrier, deps) }(conn)
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return ln.Addr(), errs
}

// assertMeshReject 断言:发送帧头 → 对端回写一行含 wantSubstr 的错误文本
// → 连接被服务端关闭(design §3.3 错误回源后断链),且 HandleMeshConn 报错。
func assertMeshReject(t *testing.T, addr net.Addr, errs <-chan error, header, wantSubstr string) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr.String(), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if header != "" {
		if _, err := conn.Write([]byte(header)); err != nil {
			t.Fatal(err)
		}
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Fatalf("expected one reject line, got err=%v line=%q", err, line)
	}
	if !strings.Contains(line, wantSubstr) {
		t.Fatalf("reject line %q missing %q", line, wantSubstr)
	}
	n, err := conn.Read(make([]byte, 16))
	if n != 0 || !errors.Is(err, io.EOF) {
		t.Fatalf("expected EOF after reject line, got n=%d err=%v", n, err)
	}
	select {
	case handleErr := <-errs:
		if handleErr == nil {
			t.Fatal("expected HandleMeshConn to return an error on reject")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for HandleMeshConn to return")
	}
}

func TestMeshInboundRejectsLoop(t *testing.T) {
	deps := meshInboundDeps(newMeshInboundCapture())
	addr, errs := startMeshInbound(t, meshproto.CarrierLAN, deps)
	header := meshFrameLine(t, func(h *meshproto.Header) {
		h.Via = []string{"dev-a", "dev-b"} // Via 已含本机 ID → 防环拒绝
		h.Hops = 1
	})
	assertMeshReject(t, addr, errs, header, "loop")
}

func TestMeshInboundRejectsHopLimit(t *testing.T) {
	deps := meshInboundDeps(newMeshInboundCapture())
	addr, errs := startMeshInbound(t, meshproto.CarrierLAN, deps)
	header := meshFrameLine(t, func(h *meshproto.Header) {
		h.Hops = meshproto.DefaultHops // Hops >= MaxHops → 跳数上限拒绝
	})
	assertMeshReject(t, addr, errs, header, "hop limit")
}

func TestMeshInboundRejectsGateClosed(t *testing.T) {
	t.Run("forward disabled", func(t *testing.T) {
		deps := meshInboundDeps(newMeshInboundCapture())
		deps.ForwardEnabled = false
		addr, errs := startMeshInbound(t, meshproto.CarrierLAN, deps)
		assertMeshReject(t, addr, errs, meshFrameLine(t, nil), "forward disabled")
	})
	t.Run("mesh nil maps to disabled and deny-all", func(t *testing.T) {
		// Mesh==nil 由组装层(relay.ProductionDeps)映射为 ForwardEnabled=false
		// + PeerAllowed=nil;此处钉死该形态在 mesh_inbound 侧同样被拒。
		deps := meshInboundDeps(newMeshInboundCapture())
		deps.ForwardEnabled = false
		deps.PeerAllowed = nil
		addr, errs := startMeshInbound(t, meshproto.CarrierLAN, deps)
		assertMeshReject(t, addr, errs, meshFrameLine(t, nil), "forward disabled")
	})
}

func TestMeshInboundRejectsUnknownForwarder(t *testing.T) {
	deps := meshInboundDeps(newMeshInboundCapture())
	deps.PeerAllowed = func(deviceID string) bool { return deviceID == "dev-c" } // 只认 dev-c
	addr, errs := startMeshInbound(t, meshproto.CarrierLAN, deps)
	assertMeshReject(t, addr, errs, meshFrameLine(t, nil), "not allowed")
}

func TestMeshInboundRejectsCustomOnRelayCarrier(t *testing.T) {
	deps := meshInboundDeps(newMeshInboundCapture())
	addr, errs := startMeshInbound(t, meshproto.CarrierRelay, deps)
	header := meshFrameLine(t, func(h *meshproto.Header) {
		h.Class = meshproto.ClassCustom // custom 仅 LAN 载体(design §4)
	})
	assertMeshReject(t, addr, errs, header, "custom")
}

func TestMeshInboundRejectsWhenSelfUnregistered(t *testing.T) {
	// M6 边界(计划风险区第 0 条):selfID 为空(未注册首启)→ 未注册设备
	// 不参与 mesh,直接拒收。
	deps := meshInboundDeps(newMeshInboundCapture())
	deps.SelfID = ""
	addr, errs := startMeshInbound(t, meshproto.CarrierLAN, deps)
	assertMeshReject(t, addr, errs, meshFrameLine(t, nil), "unregistered")
}

func TestMeshInboundRejectsDNSTargetNotImplemented(t *testing.T) {
	// dns: 目标属 Task 8;本任务拒绝并回写桩信息,分流点留在
	// handleMeshDNSTarget。
	deps := meshInboundDeps(newMeshInboundCapture())
	addr, errs := startMeshInbound(t, meshproto.CarrierLAN, deps)
	header := meshFrameLine(t, func(h *meshproto.Header) {
		h.Target = meshproto.DNSTarget
	})
	assertMeshReject(t, addr, errs, header, "dns forward not implemented")
}

func TestMeshInboundRejectsMalformedHeader(t *testing.T) {
	deps := meshInboundDeps(newMeshInboundCapture())
	addr, errs := startMeshInbound(t, meshproto.CarrierLAN, deps)
	assertMeshReject(t, addr, errs, "this is not json\n", "bad header")
}

func TestMeshInboundRejectsHeaderReadTimeout(t *testing.T) {
	// 测试缝:meshHeaderReadTimeout 覆写为短 deadline(非并行测试,覆写+恢复),
	// 断言静默连接触发超时拒绝路径——防慢速/静默连接长期占住 goroutine。
	restore := meshHeaderReadTimeout
	meshHeaderReadTimeout = 120 * time.Millisecond
	defer func() { meshHeaderReadTimeout = restore }()

	deps := meshInboundDeps(newMeshInboundCapture())
	addr, errs := startMeshInbound(t, meshproto.CarrierLAN, deps)
	// 不写任何字节:帧头读取超时 → 回写 "read header: ... timeout" 后断链。
	assertMeshReject(t, addr, errs, "", "read header")
}

func TestMeshInboundInjectsConnIntoPipeline(t *testing.T) {
	capture := newMeshInboundCapture()
	deps := meshInboundDeps(capture)
	addr, errs := startMeshInbound(t, meshproto.CarrierLAN, deps)

	conn, err := net.DialTimeout("tcp", addr.String(), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

	// 头行与载荷一次写出:钉死 bufio 预读字节的回放(bridge.go 同款坑)。
	header := meshFrameLine(t, func(h *meshproto.Header) {
		h.Class = meshproto.ClassCustom // custom 在 LAN 载体合法
		h.Target = "a.test:443"
	})
	payload := "MESH-PAYLOAD-1"
	if _, err := conn.Write([]byte(header + payload)); err != nil {
		t.Fatal(err)
	}

	var got meshInboundCaptured
	select {
	case got = <-capture.events:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for pipeline injection")
	}
	defer got.conn.Close()

	if got.md == nil {
		t.Fatal("injected metadata is nil")
	}
	if got.md.HostName != "a.test" {
		t.Fatalf("HostName = %q, want a.test", got.md.HostName)
	}
	if got.md.DstPort != 443 {
		t.Fatalf("DstPort = %d, want 443", got.md.DstPort)
	}
	if got.md.Route != RouteMesh {
		t.Fatalf("Route = %q, want %q", got.md.Route, RouteMesh)
	}
	if got.md.Network != M.TCP {
		t.Fatalf("Network = %v, want TCP", got.md.Network)
	}
	if got.md.ConnID == "" {
		t.Fatal("ConnID must be assigned for tracing")
	}
	// 源地址派生自 conn.RemoteAddr(loopback 拨入 → 127.0.0.1)。
	if got.md.SrcIP.String() != "127.0.0.1" {
		t.Fatalf("SrcIP = %q, want 127.0.0.1 (derived from conn.RemoteAddr)", got.md.SrcIP.String())
	}
	_ = got.conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, len(payload))
	if _, err := io.ReadFull(got.conn, buf); err != nil {
		t.Fatalf("reading replayed payload from injected conn: %v", err)
	}
	if string(buf) != payload {
		t.Fatalf("replayed payload = %q, want %q", string(buf), payload)
	}

	// 拒绝路径之外:注入成功 → 连接所有权移交 handler,HandleMeshConn 返回
	// handler 的返回值。
	_ = conn.Close()
	_ = got.conn.Close()
	select {
	case handleErr := <-errs:
		if handleErr != nil {
			t.Fatalf("injection should not error, got %v", handleErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for HandleMeshConn to return")
	}
}

func TestMeshInboundMetadataFromTarget(t *testing.T) {
	t.Run("domain with port", func(t *testing.T) {
		md, err := meshMetadataFromTarget("a.test:8443", nil)
		if err != nil {
			t.Fatal(err)
		}
		if md.HostName != "a.test" || md.DstPort != 8443 || md.DstIP.IsValid() {
			t.Fatalf("md = %+v", md)
		}
	})
	t.Run("bare domain defaults to 443", func(t *testing.T) {
		md, err := meshMetadataFromTarget("a.test", nil)
		if err != nil {
			t.Fatal(err)
		}
		if md.HostName != "a.test" || md.DstPort != 443 {
			t.Fatalf("md = %+v", md)
		}
	})
	t.Run("ip target fills dst ip", func(t *testing.T) {
		md, err := meshMetadataFromTarget("10.1.2.3:8443", nil)
		if err != nil {
			t.Fatal(err)
		}
		if md.HostName != "" || md.DstIP.String() != "10.1.2.3" || md.DstPort != 8443 {
			t.Fatalf("md = %+v", md)
		}
	})
	t.Run("bad port rejected", func(t *testing.T) {
		if _, err := meshMetadataFromTarget("a.test:notaport", nil); err == nil {
			t.Fatal("expected port parse error")
		}
	})
}
