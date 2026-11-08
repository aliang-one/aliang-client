package tcp

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	M "aliang.one/nursorgate/inbound/tun/metadata"
	"aliang.one/nursorgate/outbound/meshproto"
	meshproxy "aliang.one/nursorgate/outbound/proxy/mesh"
	"github.com/miekg/dns"
)

// meshInboundCaptureHandler 测试内 handler seam:捕获注入的 (ctx, conn, metadata),
// 断言注入形态而非真实处理(design §3.3 注入语义的测试锚)。ctx 一并捕获:
// Task 7 Via/Hops 续传要求注入连接携带继承帧头(出站拨 mesh 时原样上线)。
type meshInboundCaptureHandler struct {
	events chan meshInboundCaptured
}

type meshInboundCaptured struct {
	ctx  context.Context
	conn net.Conn
	md   *M.Metadata
}

func newMeshInboundCapture() *meshInboundCaptureHandler {
	return &meshInboundCaptureHandler{events: make(chan meshInboundCaptured, 8)}
}

func (h *meshInboundCaptureHandler) Handle(ctx context.Context, conn net.Conn, md *M.Metadata) error {
	h.events <- meshInboundCaptured{ctx: ctx, conn: conn, md: md}
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

// fakeLocalDNSResolver LocalDNSResolver 的测试 fake:按方法注行为,缺省零值
// 返回空结果(合法 NODATA 形态);want 前缀字段记录收到的入参供断言。
type fakeLocalDNSResolver struct {
	lookupIP   func(ctx context.Context, network, host string) ([]net.IP, error)
	lookupAddr func(ctx context.Context, addr string) ([]string, error)
	lookupMX   func(ctx context.Context, name string) ([]*net.MX, error)
	lookupSRV  func(ctx context.Context, service, proto, name string) (string, []*net.SRV, error)
	lookupTXT  func(ctx context.Context, name string) ([]string, error)

	gotIPNetwork string
	gotIPHost    string
	gotAddrArg   string
	gotMXName    string
	gotSRVArgs   [3]string
	gotTXTName   string
}

func (f *fakeLocalDNSResolver) LookupIP(ctx context.Context, network, host string) ([]net.IP, error) {
	f.gotIPNetwork, f.gotIPHost = network, host
	if f.lookupIP == nil {
		return nil, nil
	}
	return f.lookupIP(ctx, network, host)
}

func (f *fakeLocalDNSResolver) LookupAddr(ctx context.Context, addr string) ([]string, error) {
	f.gotAddrArg = addr
	if f.lookupAddr == nil {
		return nil, nil
	}
	return f.lookupAddr(ctx, addr)
}

func (f *fakeLocalDNSResolver) LookupMX(ctx context.Context, name string) ([]*net.MX, error) {
	f.gotMXName = name
	if f.lookupMX == nil {
		return nil, nil
	}
	return f.lookupMX(ctx, name)
}

func (f *fakeLocalDNSResolver) LookupSRV(ctx context.Context, service, proto, name string) (string, []*net.SRV, error) {
	f.gotSRVArgs = [3]string{service, proto, name}
	if f.lookupSRV == nil {
		return "", nil, nil
	}
	return f.lookupSRV(ctx, service, proto, name)
}

func (f *fakeLocalDNSResolver) LookupTXT(ctx context.Context, name string) ([]string, error) {
	f.gotTXTName = name
	if f.lookupTXT == nil {
		return nil, nil
	}
	return f.lookupTXT(ctx, name)
}

// newDNSQuery 构造 DNS 查询报文(A 查询形态,按需覆写类型)。
func newDNSQuery(t *testing.T, id uint16, name string, qtype uint16) []byte {
	t.Helper()
	m := new(dns.Msg)
	m.Id = id
	m.RecursionDesired = true
	m.Question = []dns.Question{{Name: name, Qtype: qtype, Qclass: dns.ClassINET}}
	b, err := m.Pack()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// dialMeshDNSTarget 建立到假 B 的连接并一次性写出「帧头+查询帧」——钉死
// bufio 预读回放路径(头与载荷一次写出是常态)。
func dialMeshDNSTarget(t *testing.T, addr net.Addr, query []byte) net.Conn {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr.String(), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	header := meshFrameLine(t, func(h *meshproto.Header) {
		h.Class = meshproto.ClassCustom // A 侧 TUN DNS 一律 custom(design §2 行12)
		h.Target = meshproto.DNSTarget
	})
	var frame [2]byte
	binary.BigEndian.PutUint16(frame[:], uint16(len(query)))
	if _, err := conn.Write(append(append([]byte(header), frame[:]...), query...)); err != nil {
		t.Fatal(err)
	}
	return conn
}

// readDNSResponseFromConn 读回 [len][response] 并拆包。
func readDNSResponseFromConn(t *testing.T, conn net.Conn) *dns.Msg {
	t.Helper()
	var frame [2]byte
	if _, err := io.ReadFull(conn, frame[:]); err != nil {
		t.Fatalf("read response frame header: %v", err)
	}
	payload := make([]byte, binary.BigEndian.Uint16(frame[:]))
	if _, err := io.ReadFull(conn, payload); err != nil {
		t.Fatalf("read response payload: %v", err)
	}
	m := new(dns.Msg)
	if err := m.Unpack(payload); err != nil {
		t.Fatalf("unpack response: %v", err)
	}
	return m
}

// TestMeshInboundDNSTargetResolvesLocally B 侧代解析(design §6 层1):
// [len][A 查询] → net.Resolver 形解析(fake 注入,回 1.2.3.4)→ [len][响应]
// 含 A 记录、TTL 30、ID/Question 回显;解析失败 → SERVFAIL。
func TestMeshInboundDNSTargetResolvesLocally(t *testing.T) {
	t.Run("A 查询合成响应", func(t *testing.T) {
		fake := &fakeLocalDNSResolver{
			lookupIP: func(ctx context.Context, network, host string) ([]net.IP, error) {
				return []net.IP{net.ParseIP("1.2.3.4")}, nil
			},
		}
		deps := meshInboundDeps(newMeshInboundCapture())
		deps.DNSResolver = fake
		addr, _ := startMeshInbound(t, meshproto.CarrierLAN, deps)

		conn := dialMeshDNSTarget(t, addr, newDNSQuery(t, 0x1234, "example.com.", dns.TypeA))
		defer conn.Close()

		resp := readDNSResponseFromConn(t, conn)
		if resp.Rcode != dns.RcodeSuccess {
			t.Fatalf("Rcode = %d(%s), want success", resp.Rcode, dns.RcodeToString[resp.Rcode])
		}
		if resp.Id != 0x1234 {
			t.Fatalf("Id = %#x, want 0x1234(回显查询 ID)", resp.Id)
		}
		if !resp.Response {
			t.Fatal("response 位未置位")
		}
		if len(resp.Answer) != 1 {
			t.Fatalf("answers = %d, want 1: %v", len(resp.Answer), resp.Answer)
		}
		a, ok := resp.Answer[0].(*dns.A)
		if !ok {
			t.Fatalf("answer 类型 = %T, want *dns.A", resp.Answer[0])
		}
		if a.A.String() != "1.2.3.4" {
			t.Fatalf("A = %s, want 1.2.3.4", a.A)
		}
		if a.Hdr.Ttl != meshDNSAnswerTTL {
			t.Fatalf("TTL = %d, want %d(固定 TTL)", a.Hdr.Ttl, meshDNSAnswerTTL)
		}
		if a.Hdr.Name != "example.com." {
			t.Fatalf("Name = %q, want example.com.", a.Hdr.Name)
		}
		if fake.gotIPNetwork != "ip4" || fake.gotIPHost != "example.com." {
			t.Fatalf("LookupIP 入参 = (%q, %q), want (ip4, example.com.)", fake.gotIPNetwork, fake.gotIPHost)
		}
	})

	t.Run("解析失败 SERVFAIL", func(t *testing.T) {
		fake := &fakeLocalDNSResolver{
			lookupIP: func(ctx context.Context, network, host string) ([]net.IP, error) {
				return nil, errors.New("resolver boom")
			},
		}
		deps := meshInboundDeps(newMeshInboundCapture())
		deps.DNSResolver = fake
		addr, _ := startMeshInbound(t, meshproto.CarrierLAN, deps)

		conn := dialMeshDNSTarget(t, addr, newDNSQuery(t, 0x4321, "fail.example.com.", dns.TypeA))
		defer conn.Close()

		resp := readDNSResponseFromConn(t, conn)
		if resp.Rcode != dns.RcodeServerFailure {
			t.Fatalf("Rcode = %d, want SERVFAIL", resp.Rcode)
		}
		if resp.Id != 0x4321 {
			t.Fatalf("Id = %#x, want 0x4321(SERVFAIL 也须回显 ID)", resp.Id)
		}
	})

	t.Run("NXDOMAIN 负应答透传", func(t *testing.T) {
		// 质量评审精化:net.DNSError.IsNotFound(上游 NXDOMAIN/无此主机)是
		// 有效负应答,回 RcodeNameError 让客户端负缓存不重试;折叠成
		// SERVFAIL 会在离线冲浪的广告域名上引发重试风暴。
		fake := &fakeLocalDNSResolver{
			lookupIP: func(ctx context.Context, network, host string) ([]net.IP, error) {
				return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
			},
		}
		deps := meshInboundDeps(newMeshInboundCapture())
		deps.DNSResolver = fake
		addr, _ := startMeshInbound(t, meshproto.CarrierLAN, deps)

		conn := dialMeshDNSTarget(t, addr, newDNSQuery(t, 0x5566, "ads.example.com.", dns.TypeA))
		defer conn.Close()

		resp := readDNSResponseFromConn(t, conn)
		if resp.Rcode != dns.RcodeNameError {
			t.Fatalf("Rcode = %d(%s), want NXDOMAIN", resp.Rcode, dns.RcodeToString[resp.Rcode])
		}
		if resp.Id != 0x5566 {
			t.Fatalf("Id = %#x, want 0x5566(NXDOMAIN 也须回显 ID)", resp.Id)
		}
		if len(resp.Question) != 1 || resp.Question[0].Name != "ads.example.com." {
			t.Fatalf("Question 回显 = %v, want ads.example.com.(负应答按 RFC 应回显查询)", resp.Question)
		}
	})
}

// TestMeshInboundDNSTargetTypeMatrix 类型矩阵(design §6 层1 定案):
// A/AAAA→LookupIP;PTR→LookupAddr(反算地址);MX/SRV/TXT→对应 Lookup;
// 其余类型 SERVFAIL。
func TestMeshInboundDNSTargetTypeMatrix(t *testing.T) {
	t.Run("AAAA 走 ip6 LookupIP", func(t *testing.T) {
		fake := &fakeLocalDNSResolver{
			lookupIP: func(ctx context.Context, network, host string) ([]net.IP, error) {
				return []net.IP{net.ParseIP("2001:db8::1")}, nil
			},
		}
		deps := meshInboundDeps(newMeshInboundCapture())
		deps.DNSResolver = fake
		addr, _ := startMeshInbound(t, meshproto.CarrierLAN, deps)
		conn := dialMeshDNSTarget(t, addr, newDNSQuery(t, 7, "v6.example.com.", dns.TypeAAAA))
		defer conn.Close()
		resp := readDNSResponseFromConn(t, conn)
		aaaa, ok := resp.Answer[0].(*dns.AAAA)
		if !ok || aaaa.AAAA.String() != "2001:db8::1" {
			t.Fatalf("answer = %v, want AAAA 2001:db8::1", resp.Answer)
		}
		if fake.gotIPNetwork != "ip6" {
			t.Fatalf("LookupIP network = %q, want ip6", fake.gotIPNetwork)
		}
	})

	t.Run("PTR 反算 in-addr.arpa 为点分地址", func(t *testing.T) {
		fake := &fakeLocalDNSResolver{
			lookupAddr: func(ctx context.Context, addr string) ([]string, error) {
				return []string{"host.example.com."}, nil
			},
		}
		deps := meshInboundDeps(newMeshInboundCapture())
		deps.DNSResolver = fake
		addr, _ := startMeshInbound(t, meshproto.CarrierLAN, deps)
		conn := dialMeshDNSTarget(t, addr, newDNSQuery(t, 8, "4.3.2.1.in-addr.arpa.", dns.TypePTR))
		defer conn.Close()
		resp := readDNSResponseFromConn(t, conn)
		ptr, ok := resp.Answer[0].(*dns.PTR)
		if !ok || ptr.Ptr != "host.example.com." {
			t.Fatalf("answer = %v, want PTR host.example.com.", resp.Answer)
		}
		if fake.gotAddrArg != "1.2.3.4" {
			t.Fatalf("LookupAddr 入参 = %q, want 1.2.3.4(反算后的点分地址)", fake.gotAddrArg)
		}
	})

	t.Run("MX 合成", func(t *testing.T) {
		fake := &fakeLocalDNSResolver{
			lookupMX: func(ctx context.Context, name string) ([]*net.MX, error) {
				return []*net.MX{{Pref: 10, Host: "mail.example.com"}}, nil
			},
		}
		deps := meshInboundDeps(newMeshInboundCapture())
		deps.DNSResolver = fake
		addr, _ := startMeshInbound(t, meshproto.CarrierLAN, deps)
		conn := dialMeshDNSTarget(t, addr, newDNSQuery(t, 9, "example.com.", dns.TypeMX))
		defer conn.Close()
		resp := readDNSResponseFromConn(t, conn)
		mx, ok := resp.Answer[0].(*dns.MX)
		if !ok || mx.Preference != 10 || mx.Mx != "mail.example.com." {
			t.Fatalf("answer = %v, want MX 10 mail.example.com.", resp.Answer)
		}
	})

	t.Run("SRV 拆 _service._proto 前缀", func(t *testing.T) {
		fake := &fakeLocalDNSResolver{
			lookupSRV: func(ctx context.Context, service, proto, name string) (string, []*net.SRV, error) {
				return "sip.example.com", []*net.SRV{{Priority: 1, Weight: 2, Port: 5060, Target: "sip.example.com"}}, nil
			},
		}
		deps := meshInboundDeps(newMeshInboundCapture())
		deps.DNSResolver = fake
		addr, _ := startMeshInbound(t, meshproto.CarrierLAN, deps)
		conn := dialMeshDNSTarget(t, addr, newDNSQuery(t, 10, "_sip._tcp.example.com.", dns.TypeSRV))
		defer conn.Close()
		resp := readDNSResponseFromConn(t, conn)
		srv, ok := resp.Answer[0].(*dns.SRV)
		if !ok || srv.Port != 5060 || srv.Target != "sip.example.com." {
			t.Fatalf("answer = %v, want SRV 5060 sip.example.com.", resp.Answer)
		}
		if got := fake.gotSRVArgs; got != [3]string{"sip", "tcp", "example.com"} {
			t.Fatalf("LookupSRV 入参 = %v, want [sip tcp example.com]", got)
		}
	})

	t.Run("TXT 合成", func(t *testing.T) {
		fake := &fakeLocalDNSResolver{
			lookupTXT: func(ctx context.Context, name string) ([]string, error) {
				return []string{"hello mesh"}, nil
			},
		}
		deps := meshInboundDeps(newMeshInboundCapture())
		deps.DNSResolver = fake
		addr, _ := startMeshInbound(t, meshproto.CarrierLAN, deps)
		conn := dialMeshDNSTarget(t, addr, newDNSQuery(t, 11, "example.com.", dns.TypeTXT))
		defer conn.Close()
		resp := readDNSResponseFromConn(t, conn)
		txt, ok := resp.Answer[0].(*dns.TXT)
		if !ok || len(txt.Txt) != 1 || txt.Txt[0] != "hello mesh" {
			t.Fatalf("answer = %v, want TXT [hello mesh]", resp.Answer)
		}
	})
}

// TestMeshInboundDNSTargetUnsupportedTypeServfail 其余类型一律 SERVFAIL
// (design §6 层1 定案),含空 Question 的畸形查询。
func TestMeshInboundDNSTargetUnsupportedTypeServfail(t *testing.T) {
	for name, qtype := range map[string]uint16{
		"CNAME": dns.TypeCNAME,
		"NS":    dns.TypeNS,
		"未注册类型": dns.TypeNULL,
	} {
		t.Run(name, func(t *testing.T) {
			deps := meshInboundDeps(newMeshInboundCapture())
			deps.DNSResolver = &fakeLocalDNSResolver{}
			addr, _ := startMeshInbound(t, meshproto.CarrierLAN, deps)
			conn := dialMeshDNSTarget(t, addr, newDNSQuery(t, 12, "example.com.", qtype))
			defer conn.Close()
			resp := readDNSResponseFromConn(t, conn)
			if resp.Rcode != dns.RcodeServerFailure {
				t.Fatalf("Rcode = %d, want SERVFAIL(qtype %d 不支持)", resp.Rcode, qtype)
			}
		})
	}

	t.Run("空 Question SERVFAIL", func(t *testing.T) {
		deps := meshInboundDeps(newMeshInboundCapture())
		deps.DNSResolver = &fakeLocalDNSResolver{}
		addr, _ := startMeshInbound(t, meshproto.CarrierLAN, deps)
		m := new(dns.Msg)
		m.Id = 13
		b, _ := m.Pack()
		conn := dialMeshDNSTarget(t, addr, b)
		defer conn.Close()
		resp := readDNSResponseFromConn(t, conn)
		if resp.Rcode != dns.RcodeServerFailure {
			t.Fatalf("Rcode = %d, want SERVFAIL", resp.Rcode)
		}
	})
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

// TestMeshInboundInjectsInheritedHeaderIntoContext Via/Hops 续传注入点
// (Task 7 强制验收):注入连接的 ctx 必须携带 AppendHop 后的帧头——B 侧
// 规则再决策 toNode 走 mesh 出口时,出口用继承帧头原样上线(Via/Hops 单调,
// 不重置不追加),多跳 A→B→C 的链路向量由此续传。
func TestMeshInboundInjectsInheritedHeaderIntoContext(t *testing.T) {
	capture := newMeshInboundCapture()
	deps := meshInboundDeps(capture)
	addr, _ := startMeshInbound(t, meshproto.CarrierLAN, deps)

	conn, err := net.DialTimeout("tcp", addr.String(), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

	// A 侧链头帧:Via=[dev-a] Hops=0;B 侧 AppendHop(dev-b) 后应为
	// Via=[dev-a dev-b] Hops=1。
	if _, err := conn.Write([]byte(meshFrameLine(t, nil))); err != nil {
		t.Fatal(err)
	}

	var got meshInboundCaptured
	select {
	case got = <-capture.events:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for pipeline injection")
	}
	defer got.conn.Close()

	hdr, ok := meshproxy.InheritedHeaderFromContext(got.ctx)
	if !ok {
		t.Fatal("injected ctx must carry the inherited mesh header (Via/Hops 续传)")
	}
	if hdr.From != "dev-a" {
		t.Fatalf("From = %q, want dev-a(链头发起者保留)", hdr.From)
	}
	if len(hdr.Via) != 2 || hdr.Via[0] != "dev-a" || hdr.Via[1] != "dev-b" {
		t.Fatalf("Via = %v, want [dev-a dev-b](AppendHop 后帧头注入)", hdr.Via)
	}
	if hdr.Hops != 1 {
		t.Fatalf("Hops = %d, want 1(AppendHop 后跳数)", hdr.Hops)
	}
	if hdr.Target != "a.test:443" {
		t.Fatalf("Target = %q, want a.test:443", hdr.Target)
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
