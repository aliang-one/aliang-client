package tunnel

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"aliang.one/nursorgate/inbound/tun/adapter"
	M "aliang.one/nursorgate/inbound/tun/metadata"
	meshproxy "aliang.one/nursorgate/outbound/proxy/mesh"
	"aliang.one/nursorgate/processor/statistic"
	"github.com/miekg/dns"
	"github.com/sagernet/gvisor/pkg/tcpip"
	"github.com/sagernet/gvisor/pkg/tcpip/stack"
	gossh "golang.org/x/crypto/ssh"
)

// ===== 测试夹具 =====

// meshDNSFakeB loopback 假 B:真 sshd(SPEC §15.1 出口测试夹具配方),mesh-exit
// channel 的 ExtraData payload 逐条记录;channel 内读 [len][查询] 送回测试,
// respond=true 时回写 [len][响应] 后关 channel(单问单答),false 时收下查询
// 后挂住不回包(A 侧超时路径)。A 侧经 ExitClient(SSH)拨入。
type meshDNSFakeB struct {
	ln        net.Listener
	hostFP    string
	payloadCh chan []byte
	queryCh   chan []byte
	respond   bool
	response  []byte
}

func startMeshDNSFakeB(t *testing.T, identity gossh.Signer, respond bool, response []byte) *meshDNSFakeB {
	t.Helper()
	b := &meshDNSFakeB{
		payloadCh: make(chan []byte, 4),
		queryCh:   make(chan []byte, 4),
		respond:   respond,
		response:  response,
	}
	_, hostPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate host key: %v", err)
	}
	hostSigner, err := gossh.NewSignerFromKey(hostPriv)
	if err != nil {
		t.Fatalf("new host signer: %v", err)
	}
	b.hostFP = gossh.FingerprintSHA256(hostSigner.PublicKey())
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	b.ln = ln
	admitted := identity.PublicKey()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go b.serve(c, hostSigner, admitted)
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return b
}

// dnsForwardTestIdentity 生成一把真 ed25519 测试身份:PEM 交给 A 侧
// ExitClient,signer/pub 供假 B 的 sshd 准入比对——两侧同源。
func dnsForwardTestIdentity(t *testing.T) (keyPEM string, signer gossh.Signer) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate ed25519: %v", err)
	}
	signer, err = gossh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("new signer: %v", err)
	}
	block, err := gossh.MarshalPrivateKey(priv, "dns-forward-test")
	if err != nil {
		t.Fatalf("marshal private key: %v", err)
	}
	return string(pem.EncodeToMemory(block)), signer
}

func (b *meshDNSFakeB) serve(c net.Conn, hostSigner gossh.Signer, admittedPub gossh.PublicKey) {
	sc := &gossh.ServerConfig{
		PublicKeyCallback: func(_ gossh.ConnMetadata, key gossh.PublicKey) (*gossh.Permissions, error) {
			if !bytes.Equal(key.Marshal(), admittedPub.Marshal()) {
				return nil, errors.New("mesh dns fake B: unknown client identity key")
			}
			return nil, nil
		},
	}
	sc.AddHostKey(hostSigner)
	_, chans, reqs, err := gossh.NewServerConn(c, sc)
	if err != nil {
		_ = c.Close()
		return
	}
	go gossh.DiscardRequests(reqs)
	for newCh := range chans {
		if newCh.ChannelType() != meshproxy.ChannelMeshExit {
			_ = newCh.Reject(gossh.UnknownChannelType, "unsupported")
			continue
		}
		b.payloadCh <- append([]byte(nil), newCh.ExtraData()...)
		ch, chReqs, err := newCh.Accept()
		if err != nil {
			continue
		}
		go gossh.DiscardRequests(chReqs)
		go func() {
			defer ch.Close()
			query, err := readTestDNSFrame(ch)
			if err != nil {
				return
			}
			b.queryCh <- query
			if b.respond {
				if err := writeTestDNSFrame(ch, b.response); err != nil {
					return
				}
			}
			_, _ = io.Copy(io.Discard, ch) // respond=false:挂住直到对端断开
		}()
	}
}

func (b *meshDNSFakeB) addr() string { return b.ln.Addr().String() }

func (b *meshDNSFakeB) hostKey() string { return b.hostFP }

// identityPEM 假 B 准入的客户端身份(与 startMeshDNSFakeB 内生成的 signer
// 同源——A 侧 ExitClient 用它拨入)。
func waitMeshDNSFakePayload(t *testing.T, b *meshDNSFakeB) []byte {
	t.Helper()
	select {
	case p := <-b.payloadCh:
		return p
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for mesh-exit payload at fake B")
		return nil
	}
}

func waitMeshDNSFakeQuery(t *testing.T, b *meshDNSFakeB) []byte {
	t.Helper()
	select {
	case q := <-b.queryCh:
		return q
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for DNS query frame at fake B")
		return nil
	}
}

// writeTestDNSFrame/readTestDNSFrame 测试侧独立的 [2B 大端长度][载荷] 读写
// (不依赖被测实现,线格式契约由测试自持)。
func writeTestDNSFrame(w io.Writer, payload []byte) error {
	var frame [2]byte
	binary.BigEndian.PutUint16(frame[:], uint16(len(payload)))
	if _, err := w.Write(frame[:]); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}

func readTestDNSFrame(r io.Reader) ([]byte, error) {
	var frame [2]byte
	if _, err := io.ReadFull(r, frame[:]); err != nil {
		return nil, err
	}
	payload := make([]byte, binary.BigEndian.Uint16(frame[:]))
	if _, err := io.ReadFull(r, payload); err != nil {
		return nil, err
	}
	return payload, nil
}

// closedPortAddr 返回「监听后立即关闭」的回环地址,拨它稳定 connection refused。
func closedPortAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

func newTestDNSQuery(t *testing.T, id uint16, name string, qtype uint16) []byte {
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

func buildTestDNSAResponse(t *testing.T, id uint16, name, ip string) []byte {
	t.Helper()
	m := new(dns.Msg)
	m.Id = id
	m.Response = true
	m.RecursionAvailable = true
	m.Question = []dns.Question{{Name: name, Qtype: dns.TypeA, Qclass: dns.ClassINET}}
	m.Answer = []dns.RR{&dns.A{
		Hdr: dns.RR_Header{Name: name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 30},
		A:   net.ParseIP(ip),
	}}
	b, err := m.Pack()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// fakeTUNUDPConn adapter.UDPConn 假件:ReadFrom 交付一次预置查询后阻塞到
// 关闭;WriteTo 把「回给客户端的响应」推入 writes 供断言。
type fakeTUNUDPConn struct {
	id        stack.TransportEndpointID
	query     []byte
	writes    chan []byte
	closeOnce sync.Once
	closeCh   chan struct{}
}

// newFakeTUNUDPConn 构造 Dst=8.8.8.8:53 / Src=192.168.0.2:5353 的 DNS 流。
func newFakeTUNUDPConn(query []byte) *fakeTUNUDPConn {
	return &fakeTUNUDPConn{
		id: stack.TransportEndpointID{
			LocalAddress:  tcpip.AddrFrom4([4]byte{8, 8, 8, 8}),
			LocalPort:     53,
			RemoteAddress: tcpip.AddrFrom4([4]byte{192, 168, 0, 2}),
			RemotePort:    5353,
		},
		query:   append([]byte(nil), query...),
		writes:  make(chan []byte, 4),
		closeCh: make(chan struct{}),
	}
}

func (c *fakeTUNUDPConn) ID() *stack.TransportEndpointID { return &c.id }

func (c *fakeTUNUDPConn) ReadFrom(p []byte) (int, net.Addr, error) {
	if c.query != nil {
		n := copy(p, c.query)
		c.query = nil
		return n, &net.UDPAddr{IP: net.IP{192, 168, 0, 2}, Port: 5353}, nil
	}
	<-c.closeCh
	return 0, nil, errors.New("fake tun udp conn closed")
}

func (c *fakeTUNUDPConn) WriteTo(p []byte, _ net.Addr) (int, error) {
	select {
	case c.writes <- append([]byte(nil), p...):
		return len(p), nil
	case <-c.closeCh:
		return 0, errors.New("fake tun udp conn closed")
	}
}

func (c *fakeTUNUDPConn) Close() error {
	c.closeOnce.Do(func() { close(c.closeCh) })
	return nil
}

// net.Conn 未用面(adapter.UDPConn 接口要求实现,UDP 路径不触达)。
func (c *fakeTUNUDPConn) Read(p []byte) (int, error)       { return 0, errors.New("unused") }
func (c *fakeTUNUDPConn) Write(p []byte) (int, error)      { return 0, errors.New("unused") }
func (c *fakeTUNUDPConn) LocalAddr() net.Addr              { return &net.UDPAddr{} }
func (c *fakeTUNUDPConn) RemoteAddr() net.Addr             { return &net.UDPAddr{} }
func (c *fakeTUNUDPConn) SetDeadline(time.Time) error      { return nil }
func (c *fakeTUNUDPConn) SetReadDeadline(time.Time) error  { return nil }
func (c *fakeTUNUDPConn) SetWriteDeadline(time.Time) error { return nil }

// recordingDialer 记录 DialUDP 调用的假 proxy.Dialer(公网直连 seam)。
type recordingDialer struct {
	dialUDP chan *M.Metadata
	pc      net.PacketConn
}

func (d *recordingDialer) DialContext(ctx context.Context, md *M.Metadata) (net.Conn, error) {
	return nil, errors.New("unused in udp path")
}

func (d *recordingDialer) DialUDP(md *M.Metadata) (net.PacketConn, error) {
	d.dialUDP <- md
	return d.pc, nil
}

// stubPublicPacketConn 假公网 UDP socket:ReadFrom 阻塞到关闭,WriteTo 记录。
type stubPublicPacketConn struct {
	writes    chan []byte
	closeOnce sync.Once
	closeCh   chan struct{}
}

func newStubPublicPacketConn() *stubPublicPacketConn {
	return &stubPublicPacketConn{writes: make(chan []byte, 4), closeCh: make(chan struct{})}
}

func (p *stubPublicPacketConn) ReadFrom(b []byte) (int, net.Addr, error) {
	<-p.closeCh
	return 0, nil, errors.New("closed")
}

func (p *stubPublicPacketConn) WriteTo(b []byte, _ net.Addr) (int, error) {
	select {
	case p.writes <- append([]byte(nil), b...):
		return len(b), nil
	case <-p.closeCh:
		return 0, errors.New("closed")
	}
}

func (p *stubPublicPacketConn) Close() error {
	p.closeOnce.Do(func() { close(p.closeCh) })
	return nil
}

func (p *stubPublicPacketConn) LocalAddr() net.Addr              { return &net.UDPAddr{} }
func (p *stubPublicPacketConn) SetDeadline(time.Time) error      { return nil }
func (p *stubPublicPacketConn) SetReadDeadline(time.Time) error  { return nil }
func (p *stubPublicPacketConn) SetWriteDeadline(time.Time) error { return nil }

var (
	_ adapter.UDPConn = (*fakeTUNUDPConn)(nil)
	_ proxyDialer     = (*recordingDialer)(nil)
)

// proxyDialer 别名锚:recordingDialer 须满足 proxy.Dialer(New 入参类型)。
type proxyDialer = interface {
	DialContext(ctx context.Context, md *M.Metadata) (net.Conn, error)
	DialUDP(md *M.Metadata) (net.PacketConn, error)
}

// dnsForwardTestPeer 构造指向 fakeB 的 DNS 代转发 ExitClient(Relay:false——
// TUN DNS 一律 custom 仅 LAN 载体,design §2 行12)。
func dnsForwardTestPeer(t *testing.T, keyPEM, lanAddr, hostKey string) meshproxy.Dialer {
	t.Helper()
	spec := meshproxy.PeerSpec{
		DeviceID: "dev-b",
		LANAddrs: []string{lanAddr},
		HostKey:  hostKey,
		SSHUser:  "mesh",
		Relay:    false,
	}
	client, err := meshproxy.NewExitClient(spec, keyPEM, "", nil)
	if err != nil {
		t.Fatalf("NewExitClient: %v", err)
	}
	return client
}

// ===== Step 1 规定用例 =====

// TestDNSForwardPacketConnRelaysQueryViaMesh A 侧 PacketConn 交换语义:
// WriteTo(查询) 打开 mesh SSH 连接写 [len][查询],假 B 回 [len][响应],
// ReadFrom 原样交付;mesh-exit ExtraData payload = {"t":"dns"}(特殊目标无
// h/p);日志 seam 收到查询。
func TestDNSForwardPacketConnRelaysQueryViaMesh(t *testing.T) {
	keyPEM, identity := dnsForwardTestIdentity(t)
	query := newTestDNSQuery(t, 0x77, "mesh.example.com.", dns.TypeA)
	response := buildTestDNSAResponse(t, 0x77, "mesh.example.com.", "1.2.3.4")
	fakeB := startMeshDNSFakeB(t, identity, true, response)

	var logged [][]byte
	pc := newDNSForwardPacketConn(dnsForwardTestPeer(t, keyPEM, fakeB.addr(), fakeB.hostKey()), func(q []byte) {
		logged = append(logged, append([]byte(nil), q...))
	})
	defer pc.Close()

	dst := &net.UDPAddr{IP: net.IP{8, 8, 8, 8}, Port: 53}
	if _, err := pc.WriteTo(query, dst); err != nil {
		t.Fatalf("WriteTo: %v", err)
	}

	buf := make([]byte, 1500)
	_ = pc.SetReadDeadline(time.Now().Add(3 * time.Second))
	n, from, err := pc.ReadFrom(buf)
	if err != nil {
		t.Fatalf("ReadFrom: %v", err)
	}
	if !bytes.Equal(buf[:n], response) {
		t.Fatalf("response bytes mismatch: got %d bytes, want fake B's response", n)
	}
	if from == nil || from.String() != "8.8.8.8:53" {
		t.Fatalf("ReadFrom addr = %v, want 8.8.8.8:53(客户端视角的 DNS 服务器)", from)
	}

	// mesh-exit ExtraData:dns 特殊目标无 h/p(与 B 侧代解析面的识别契约)。
	payload := waitMeshDNSFakePayload(t, fakeB)
	if !bytes.Equal(payload, []byte(`{"t":"dns"}`)) {
		t.Fatalf("mesh-exit payload = %s, want {\"t\":\"dns\"}(dns 特殊目标)", payload)
	}
	if got := waitMeshDNSFakeQuery(t, fakeB); !bytes.Equal(got, query) {
		t.Fatalf("query frame bytes mismatch: got %d bytes", len(got))
	}
	if len(logged) != 1 || !bytes.Equal(logged[0], query) {
		t.Fatalf("logQuery seam = %d calls, want 1 with query bytes(QNAME 日志语义并入 forward conn)", len(logged))
	}
}

// TestDNSForwardFailsClosedWhenPeerUnreachable fail-closed 边界:门激活但
// peer LAN 不可达 → 客户端收到 SERVFAIL(非错误),且公网 DialUDP 不被调用
// (激活路径下无公网 socket,注入 seam 断言)。整条链走 handleUDPConn。
func TestDNSForwardFailsClosedWhenPeerUnreachable(t *testing.T) {
	query := newTestDNSQuery(t, 0x88, "down.example.com.", dns.TypeA)
	keyPEM, _ := dnsForwardTestIdentity(t)
	publicPC := newStubPublicPacketConn()
	dialer := &recordingDialer{dialUDP: make(chan *M.Metadata, 1), pc: publicPC}
	tun := New(dialer, statistic.DefaultManager)
	tun.SetMeshDNSForwardPeerSource(func() meshproxy.Dialer {
		return dnsForwardTestPeer(t, keyPEM, closedPortAddr(t), "SHA256:AAAA")
	})

	uc := newFakeTUNUDPConn(query)
	flow := newUDPFlowCloser(uc)
	done := make(chan struct{})
	go func() {
		tun.handleUDPConn(uc, flow)
		close(done)
	}()
	defer func() {
		flow.Close()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("handleUDPConn did not return after flow close")
		}
	}()

	select {
	case resp := <-uc.writes:
		m := new(dns.Msg)
		if err := m.Unpack(resp); err != nil {
			t.Fatalf("SERVFAIL response must be a valid DNS message: %v", err)
		}
		if m.Rcode != dns.RcodeServerFailure {
			t.Fatalf("Rcode = %d, want SERVFAIL(fail-closed,绝不回落公网 DNS)", m.Rcode)
		}
		if m.Id != 0x88 {
			t.Fatalf("Id = %#x, want 0x88(SERVFAIL 回显查询 ID)", m.Id)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for SERVFAIL response")
	}

	select {
	case md := <-dialer.dialUDP:
		t.Fatalf("激活路径不得拨公网 UDP socket, got DialUDP(%s)", md.DestinationAddress())
	default:
	}
}

// TestDNSForwardTimeoutYieldsServfail 交换超时:假 B 收到查询不回包 →
// SERVFAIL 在缩短后的交换窗口内交付(测试缝覆写 dnsForwardExchangeTimeout)。
func TestDNSForwardTimeoutYieldsServfail(t *testing.T) {
	restore := dnsForwardExchangeTimeout
	dnsForwardExchangeTimeout = 300 * time.Millisecond
	defer func() { dnsForwardExchangeTimeout = restore }()

	query := newTestDNSQuery(t, 0x99, "slow.example.com.", dns.TypeA)
	keyPEM, identity := dnsForwardTestIdentity(t)
	fakeB := startMeshDNSFakeB(t, identity, false, nil)

	pc := newDNSForwardPacketConn(dnsForwardTestPeer(t, keyPEM, fakeB.addr(), fakeB.hostKey()), nil)
	defer pc.Close()

	start := time.Now()
	if _, err := pc.WriteTo(query, &net.UDPAddr{IP: net.IP{8, 8, 8, 8}, Port: 53}); err != nil {
		t.Fatalf("WriteTo: %v", err)
	}
	buf := make([]byte, 1500)
	_ = pc.SetReadDeadline(time.Now().Add(3 * time.Second))
	n, _, err := pc.ReadFrom(buf)
	if err != nil {
		t.Fatalf("timeout must yield SERVFAIL response, not error: %v", err)
	}
	m := new(dns.Msg)
	if err := m.Unpack(buf[:n]); err != nil {
		t.Fatalf("unpack SERVFAIL: %v", err)
	}
	if m.Rcode != dns.RcodeServerFailure || m.Id != 0x99 {
		t.Fatalf("response = rcode %d id %#x, want SERVFAIL id 0x99", m.Rcode, m.Id)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("SERVFAIL should arrive within the shortened exchange window, elapsed=%v", elapsed)
	}
	if got := waitMeshDNSFakeQuery(t, fakeB); !bytes.Equal(got, query) {
		t.Fatal("fake B should have received the query bytes")
	}
}

// TestGateInactivePassesThroughDirect fail-open 边界:门未激活(未注入 peer
// source / source 返回 nil = 无 dns 规则)→ 走既有 DialUDP 直连路径,行为不变。
func TestGateInactivePassesThroughDirect(t *testing.T) {
	run := func(t *testing.T, configure func(*Tunnel)) {
		query := newTestDNSQuery(t, 0xAA, "direct.example.com.", dns.TypeA)
		publicPC := newStubPublicPacketConn()
		dialer := &recordingDialer{dialUDP: make(chan *M.Metadata, 1), pc: publicPC}
		tun := New(dialer, statistic.DefaultManager)
		if configure != nil {
			configure(tun)
		}

		uc := newFakeTUNUDPConn(query)
		flow := newUDPFlowCloser(uc)
		done := make(chan struct{})
		go func() {
			tun.handleUDPConn(uc, flow)
			close(done)
		}()

		var md *M.Metadata
		select {
		case md = <-dialer.dialUDP:
		case <-time.After(3 * time.Second):
			t.Fatal("gate inactive must keep the existing DialUDP direct path")
		}
		if md.DstPort != 53 {
			t.Fatalf("DialUDP metadata DstPort = %d, want 53", md.DstPort)
		}

		select {
		case got := <-publicPC.writes:
			if !bytes.Equal(got, query) {
				t.Fatalf("direct path must relay query bytes unchanged, got %d bytes", len(got))
			}
		case <-time.After(3 * time.Second):
			t.Fatal("query bytes never reached the public socket")
		}

		flow.Close()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("handleUDPConn did not return after flow close")
		}
		// Route 断言须在 handleUDPConn 返回后:DialUDP 发布的是共享 metadata
		// 指针,Route 在 DialUDP 返回后才被写入(读早了即数据竞争)。
		if md.Route != "RouteDirect" {
			t.Fatalf("Route = %q, want RouteDirect(直连路径标记不变)", md.Route)
		}
	}

	t.Run("未注入 peer source", func(t *testing.T) { run(t, nil) })
	t.Run("source 返回 nil", func(t *testing.T) {
		run(t, func(tun *Tunnel) {
			tun.SetMeshDNSForwardPeerSource(func() meshproxy.Dialer { return nil })
		})
	})
}
