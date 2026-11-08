package test

// mesh 集成测试(计划 Task 13,design docs/mesh-forwarding-network-design.md):
// 多节点 loopback 全链首次合奏——A 入站(真处理器含真 SNI 嗅探)→ 规则 →
// mesh 出口(帧头)→ B 侧常驻 relay(app/mesh/relay)→ HandleMeshConn 门禁 →
// 注入 B 自己的处理管线 → B 规则再决策(多跳/环拒/兜底)。八个用例共享一套
// harness,取舍见各 helper 注释与任务报告。
//
// Harness 设计取舍(与任务报告同步):
//   - 「多节点」= 同进程多 listener:每节点 = relay.New(逐连接 deps)+ loopback
//     Serve(载体 lan);节点差异(SelfID/ACL/Handler)全部经 MeshInboundDeps
//     显式注入(生产同款注入面);规则差异经「相位换配」实现——全局配置是单例,
//     快照按连接时刻编译(decideRouteWithRoutingEngine 每次
//     CompileRuntimeSnapshotFromRuntimeInputs),在 B 注入闸门(gate)处换配即可
//     让 A/B/C 各持不同规则。gate 是 deps.Handler 的包装(到达记录+阻塞+放行),
//     时序确定(先到记、后换配、再放行),不依赖睡眠。
//   - A 侧入站 = 测试注入起点:loopback listener accept → 真
//     TCPConnectionHandler.Handle(与 TUN/HTTP 入站同一处理链)。SNI 断言要求
//     过真 handler:用手工构造的真 TLS ClientHello 字节(buildClientHelloTLS12)
//     驱动真 NewDefaultTLSHandler().ExtractSNI 嗅探链路(processor/tls 解析器)。
//   - B→echo 落点走 B 的 toSocks 真实出口分支(dialViaSocksOrDirect),socks
//     上游以 outbound 注册表 fake 落到 echo(route_override_test fakeAliangProxy
//     同款注入面)——「注入流量继承 B 全部规则」由 B 的普通出口分支接管注入
//     连接来证明。
//   - 离线/AI 改道复用 SetMeshOfflineSignal/SetMeshSelfIDSource 缝
//     (handler_mesh_test.go swap 模式);缝是进程级单发,A/B「都离线」即共享
//     同一 true 信号,正是本用例要的形态。
//   - 全部监听 127.0.0.1 随机端口;连接目标 IP 用 TEST-NET(203.0.113.0/24)
//     避开回环/私网 direct 快速路径;全程零真实外联。

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"net/netip"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"aliang.one/nursorgate/app/mesh/relay"
	apptunnel "aliang.one/nursorgate/app/tunnel"
	"aliang.one/nursorgate/inbound/tun/adapter"
	M "aliang.one/nursorgate/inbound/tun/metadata"
	"aliang.one/nursorgate/inbound/tun/tunnel"
	"aliang.one/nursorgate/outbound"
	"aliang.one/nursorgate/outbound/meshproto"
	meshproxy "aliang.one/nursorgate/outbound/proxy/mesh"
	"aliang.one/nursorgate/outbound/proxy/proto"
	auth "aliang.one/nursorgate/processor/auth" // 包名实为 user,别名对齐仓内先例
	"aliang.one/nursorgate/processor/config"
	"aliang.one/nursorgate/processor/egress"
	"aliang.one/nursorgate/processor/routing"
	"aliang.one/nursorgate/processor/statistic"
	"aliang.one/nursorgate/processor/tcp"

	"github.com/miekg/dns"
	"github.com/sagernet/gvisor/pkg/tcpip"
	"github.com/sagernet/gvisor/pkg/tcpip/stack"
)

// ===== 全局态与相位换配 =====

// meshItResetGlobals 隔离本文件用到的全部进程级单例(配置/apply store/三缝/
// 出口注册表):进场清一次、t.Cleanup 再清一次,测试间与 test 包既有用例均
// 零残留。非并行安全(本文件无 t.Parallel)。
func meshItResetGlobals(t *testing.T) {
	t.Helper()
	reset := func() {
		config.ResetGlobalConfigForTest()
		config.ResetRoutingApplyStoreForTest()
		tcp.SetMeshSelfIDSource(nil)
		tcp.SetMeshOfflineSignal(nil)
		tcp.SetMeshPikoDialSource(nil)
		egress.SetOfflineSignal(nil)
		egress.SetPeerSource(nil)
		outbound.GetRegistry().Clear()
	}
	reset()
	t.Cleanup(reset)
}

// meshItSelfID 注入链头 selfID(全局缝;本文件各用例 A 侧链头恒 dev-a,中间跳
// 出口走继承帧头不消费该缝)。
func meshItSelfID(t *testing.T, id string) {
	t.Helper()
	tcp.SetMeshSelfIDSource(func() string { return id })
}

// meshItPeers 构造 peers 列表(id→addr 成对)。
func meshItPeers(pairs ...[2]string) []config.MeshPeer {
	peers := make([]config.MeshPeer, 0, len(pairs))
	for _, p := range pairs {
		peers = append(peers, config.MeshPeer{DeviceID: p[0], LANAddrs: []string{p[1]}})
	}
	return peers
}

// meshItSetConfig 相位换配:全局配置 = 规则 + 可选 customer.proxy(socks 上游,
// 供 B 的 toSocks 出口分支)+ mesh(peers/兜底)。快照按连接时刻编译,换配对
// 「已决策」的连接零影响。
func meshItSetConfig(t *testing.T, rules []string, proxyType string, mesh *config.MeshConfig) {
	t.Helper()
	customer := &config.CustomerConfig{ProxyRules: rules, Mesh: mesh}
	if proxyType != "" {
		customer.Proxy = &config.CustomerProxyConfig{Type: proxyType}
	}
	config.SetGlobalConfig(&config.Config{Customer: customer})
}

// meshItForwardEnable 「允许被转发」门禁的显式开启值(默认关)。
func meshItForwardEnable() *bool { b := true; return &b }

// ===== TLS ClientHello 构造(真 SNI 嗅探链路的驱动字节) =====

// buildClientHelloTLS12 手工构造带 SNI 扩展的 TLS ClientHello 记录。布局与
// processor/tls parseSNIFromBuffer 的解析口径互锁:5B 记录头 + 1B 握手类型 +
// 3B 长度 + 版本/随机/sessionID/套件/压缩/扩展。
func buildClientHelloTLS12(sni string) []byte {
	name := []byte(sni)
	listLen := 1 + 2 + len(name) // type + nameLen + name
	sniBody := []byte{byte(listLen >> 8), byte(listLen), 0x00, byte(len(name) >> 8), byte(len(name))}
	sniBody = append(sniBody, name...)
	ext := append([]byte{0x00, 0x00, byte(len(sniBody) >> 8), byte(len(sniBody))}, sniBody...)

	body := []byte{0x03, 0x03}               // client_version TLS1.2
	body = append(body, make([]byte, 32)...) // random
	body = append(body, 0x00)                // session_id 长度 0
	body = append(body, 0x00, 0x02)          // cipher_suites 长度 2
	body = append(body, 0xc0, 0x2f)          // ECDHE-RSA-AES128-GCM-SHA256
	body = append(body, 0x01, 0x00)          // compression_methods: null
	body = append(body, byte(len(ext)>>8), byte(len(ext)))
	body = append(body, ext...)

	hs := append([]byte{0x01, byte(len(body) >> 16), byte(len(body) >> 8), byte(len(body))}, body...)
	return append([]byte{0x16, 0x03, 0x01, byte(len(hs) >> 8), byte(len(hs))}, hs...)
}

// ===== 节点与处理器基建 =====

// newMeshItPipelineHandler 构造真处理器(与 tcp.GetHandler 生产单例同款组件;
// 统计管理器必须真身——tracker Close 无条件 manager.Leave,nil 会 panic)。
func newMeshItPipelineHandler() tcp.TCPConnHandler {
	return tcp.NewTCPConnectionHandler(
		tcp.NewDefaultProtocolDetector(),
		tcp.NewDefaultTLSHandler(),
		tcp.NewDefaultRelayManager(),
		statistic.DefaultManager,
	)
}

// meshItInjection 一次注入的到达快照(ctx 携带继承帧头)。
type meshItInjection struct {
	ctx context.Context
	md  *M.Metadata
}

// meshItGate 注入闸门:包装 deps.Handler——到达先记录再阻塞,测试在阻塞窗口
// 内完成相位换配后放行;release 关闭后永久放行(once 防重复 close)。15s 兜底
// 超时防挂死。
type meshItGate struct {
	arrived chan *meshItInjection
	release chan struct{}
	once    sync.Once
}

func newMeshItGate() *meshItGate {
	return &meshItGate{arrived: make(chan *meshItInjection, 8), release: make(chan struct{})}
}

func (g *meshItGate) open() { g.once.Do(func() { close(g.release) }) }

type meshItGatedHandler struct {
	gate  *meshItGate
	inner tcp.TCPConnHandler
}

func (h *meshItGatedHandler) Handle(ctx context.Context, conn net.Conn, md *M.Metadata) error {
	select {
	case h.gate.arrived <- &meshItInjection{ctx: ctx, md: md}:
	default:
	}
	select {
	case <-h.gate.release:
	case <-time.After(15 * time.Second):
		// 兜底:断链等异常时放行,由断言收口,绝不让测试挂死。
	}
	return h.inner.Handle(ctx, conn, md)
}

// meshItCaptureHandler 终节点捕获器:记录 (ctx, md) 后按 deadline 读注入载荷
// (内容透传断言),随后关连接(所有权契约:handler 负责关闭)。
type meshItCaptureHandler struct {
	events   chan *meshItInjection
	payloads chan []byte
}

func newMeshItCaptureHandler() *meshItCaptureHandler {
	return &meshItCaptureHandler{
		events:   make(chan *meshItInjection, 8),
		payloads: make(chan []byte, 8),
	}
}

func (h *meshItCaptureHandler) Handle(ctx context.Context, conn net.Conn, md *M.Metadata) error {
	defer conn.Close()
	select {
	case h.events <- &meshItInjection{ctx: ctx, md: md}:
	default:
	}
	// 到达即推(payloads 按块上报,不等问题终结——deadline 只是读侧收尾)。
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 4096)
	for {
		n, readErr := conn.Read(buf)
		if n > 0 {
			h.payloads <- append([]byte(nil), buf[:n]...)
		}
		if readErr != nil {
			return nil
		}
	}
}

// meshItNode 一个测试「节点」:常驻 relay(loopback,载体 lan)+ 逐连接 deps
// (SelfID/ACL/MaxHops 显式注入;DNS 代解析器可注 fake)。PeerAllowed 观测记录
// from,供「ACL 走过」断言。
type meshItNode struct {
	id         string
	addr       string
	forwarders chan string
}

func startMeshItNode(t *testing.T, id string, allowed []string, handler tcp.TCPConnHandler, resolver tcp.LocalDNSResolver) *meshItNode {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	allow := make(map[string]bool, len(allowed))
	for _, p := range allowed {
		allow[p] = true
	}
	node := &meshItNode{id: id, addr: ln.Addr().String(), forwarders: make(chan string, 8)}
	r := relay.New(func() tcp.MeshInboundDeps {
		return tcp.MeshInboundDeps{
			SelfID:         id,
			MaxHops:        meshproto.DefaultHops,
			ForwardEnabled: true,
			PeerAllowed: func(deviceID string) bool {
				select {
				case node.forwarders <- deviceID:
				default:
				}
				return allow[deviceID]
			},
			Handler:     handler,
			DNSResolver: resolver,
		}
	})
	go func() { _ = r.Serve(ln, meshproto.CarrierLAN) }()
	t.Cleanup(func() { _ = ln.Close() })
	return node
}

// meshItIngress A 侧入站注入起点:accept → 真 handler.Handle(md 按模板逐连接
// 拷贝)。mds/done 供测试观察每连接的 metadata 指针与 Handle 返回。
type meshItIngress struct {
	addr string
	mds  chan *M.Metadata
	done chan error
}

func startMeshItIngress(t *testing.T, handler tcp.TCPConnHandler, template M.Metadata) *meshItIngress {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ing := &meshItIngress{addr: ln.Addr().String(), mds: make(chan *M.Metadata, 4), done: make(chan error, 4)}
	go func() {
		for {
			conn, acceptErr := ln.Accept()
			if acceptErr != nil {
				return
			}
			md := template // 逐连接拷贝,互不串扰
			select {
			case ing.mds <- &md:
			default:
			}
			go func() { ing.done <- handler.Handle(context.Background(), conn, &md) }()
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return ing
}

// startMeshItEcho 回显目标:逐块原样回显并按块上报收到的字节(内容透传断言)。
func startMeshItEcho(t *testing.T) (addr string, received chan []byte) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	received = make(chan []byte, 16)
	go func() {
		for {
			conn, acceptErr := ln.Accept()
			if acceptErr != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 4096)
				for {
					n, readErr := c.Read(buf)
					if n > 0 {
						chunk := append([]byte(nil), buf[:n]...)
						received <- chunk
						if _, writeErr := c.Write(chunk); writeErr != nil {
							return
						}
					}
					if readErr != nil {
						return
					}
				}
			}(conn)
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return ln.Addr().String(), received
}

// startMeshItHeaderPeer 原始帧头对端(outbound/proxy/mesh mesh_test fakePeer
// 同款):读首行 JSON 帧头上报后排水。
func startMeshItHeaderPeer(t *testing.T) (addr string, headers chan *meshproto.Header) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	headers = make(chan *meshproto.Header, 8)
	go func() {
		for {
			conn, acceptErr := ln.Accept()
			if acceptErr != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				line, readErr := bufio.NewReader(c).ReadString('\n')
				if readErr != nil {
					return
				}
				hdr, _, parseErr := meshproto.Parse([]byte(line))
				if parseErr != nil {
					return
				}
				headers <- hdr
				_, _ = io.Copy(io.Discard, c)
			}(conn)
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return ln.Addr().String(), headers
}

// closedMeshItAddr 监听后立即关闭的回环地址(拨它稳定 connection refused)。
func closedMeshItAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

// meshItFakeSocks B 侧 toSocks 出口分支的上游落点(注册表注入,生产 socks 出口
// 同位):按 metadata 拨真实 echo,记录所见域名。
type meshItFakeSocks struct {
	target string
	hosts  chan string
}

func (p *meshItFakeSocks) DialContext(ctx context.Context, md *M.Metadata) (net.Conn, error) {
	host := ""
	if md != nil {
		host = md.HostName
	}
	select {
	case p.hosts <- host:
	default:
	}
	return net.DialTimeout("tcp", p.target, 2*time.Second)
}

func (p *meshItFakeSocks) DialUDP(*M.Metadata) (net.PacketConn, error) {
	return nil, errors.New("mesh-it: fake socks DialUDP unused")
}

func (p *meshItFakeSocks) Addr() string       { return "mesh-it-fake-socks" }
func (p *meshItFakeSocks) Proto() proto.Proto { return proto.Socks5 }

// registerMeshItFakeSocks 注册 fake socks 上游(dialByRoute 的 toSocks 分支消费)。
func registerMeshItFakeSocks(t *testing.T, target string) *meshItFakeSocks {
	t.Helper()
	fake := &meshItFakeSocks{
		target: target,
		hosts:  make(chan string, 4),
	}
	if err := outbound.GetRegistry().Register("socks", fake); err != nil {
		t.Fatalf("register fake socks: %v", err)
	}
	return fake
}

// ===== 断言助手 =====

func meshItWaitInjection(t *testing.T, gate *meshItGate) *meshItInjection {
	t.Helper()
	select {
	case inj := <-gate.arrived:
		return inj
	case <-time.After(3 * time.Second):
		t.Fatal("超时等待 mesh 注入到达(闸门未记录到注入)")
		return nil
	}
}

func meshItWaitArrival(t *testing.T, events chan *meshItInjection) *meshItInjection {
	t.Helper()
	select {
	case inj := <-events:
		return inj
	case <-time.After(3 * time.Second):
		t.Fatal("超时等待捕获器注入")
		return nil
	}
}

func meshItWaitForwarder(t *testing.T, node *meshItNode, want string) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case got := <-node.forwarders:
			if got == want {
				return
			}
			continue // 其它 from(如环拒场景的二次连接)不干扰
		case <-deadline:
			t.Fatalf("超时等待 %s 的 ACL 观察到 from=%s", node.id, want)
		}
	}
}

func meshItWaitHeader(t *testing.T, headers chan *meshproto.Header) *meshproto.Header {
	t.Helper()
	select {
	case hdr := <-headers:
		return hdr
	case <-time.After(3 * time.Second):
		t.Fatal("超时等待 mesh 帧头")
		return nil
	}
}

func meshItExpectNoHeader(t *testing.T, headers chan *meshproto.Header, what string) {
	t.Helper()
	select {
	case hdr := <-headers:
		t.Fatalf("%s 不应收到帧头, got: %v", what, hdr)
	case <-time.After(300 * time.Millisecond):
	}
}

// meshItAssertVia 断言注入 ctx 继承帧头的 From/Via/Hops。
func meshItAssertVia(t *testing.T, inj *meshItInjection, wantFrom string, wantVia []string, wantHops int) {
	t.Helper()
	hdr, ok := meshproxy.InheritedHeaderFromContext(inj.ctx)
	if !ok {
		t.Fatal("注入 ctx 未携带继承帧头(Via/Hops 续传缺失)")
	}
	if hdr.From != wantFrom {
		t.Fatalf("From = %q, want %q(链头保留)", hdr.From, wantFrom)
	}
	if len(hdr.Via) != len(wantVia) {
		t.Fatalf("Via = %v, want %v", hdr.Via, wantVia)
	}
	for i := range wantVia {
		if hdr.Via[i] != wantVia[i] {
			t.Fatalf("Via = %v, want %v", hdr.Via, wantVia)
		}
	}
	if hdr.Hops != wantHops {
		t.Fatalf("Hops = %d, want %d", hdr.Hops, wantHops)
	}
}

// meshItReadLineThenEOF 读一行(错误回源行)并断言随后连接被对端断链。
func meshItReadLineThenEOF(t *testing.T, conn net.Conn, wantSubstr string) {
	t.Helper()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Fatalf("期望一行错误回源文本, got err=%v line=%q", err, line)
	}
	if !strings.Contains(line, wantSubstr) {
		t.Fatalf("错误回源行 %q 缺少 %q", line, wantSubstr)
	}
	n, err := conn.Read(make([]byte, 32))
	if n != 0 || !errors.Is(err, io.EOF) {
		t.Fatalf("拒绝后应断链(EOF), got n=%d err=%v", n, err)
	}
}

// meshItReadFull 客户端读满 n 字节(带 deadline)。
func meshItReadFull(t *testing.T, conn net.Conn, n int) []byte {
	t.Helper()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, n)
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("读满 %d 字节失败: %v", n, err)
	}
	return buf
}

// meshItCollectBytes 从 chunk 通道收满恰好 n 字节。
func meshItCollectBytes(t *testing.T, chunks chan []byte, n int) []byte {
	t.Helper()
	out := make([]byte, 0, n)
	deadline := time.After(3 * time.Second)
	for len(out) < n {
		select {
		case c := <-chunks:
			out = append(out, c...)
		case <-deadline:
			t.Fatalf("超时收集字节: %d/%d", len(out), n)
		}
	}
	if len(out) != n {
		t.Fatalf("收集字节超量: %d, want %d", len(out), n)
	}
	return out
}

// meshItWaitGoroutines 等 goroutine 数回落到基线(±slack):拒绝链路收敛后以
// goroutine 计数代观测连接/goroutine 无泄漏。
func meshItWaitGoroutines(t *testing.T, baseline, slack int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= baseline+slack {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("goroutine 未回落: now=%d baseline=%d slack=%d", runtime.NumGoroutine(), baseline, slack)
}

// meshItDialWrite 拨地址并一次写出载荷。
func meshItDialWrite(t *testing.T, addr string, payload []byte) net.Conn {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(payload) > 0 {
		if _, err := conn.Write(payload); err != nil {
			t.Fatal(err)
		}
	}
	return conn
}

// meshItAssertRoute 等最新一条入站连接的 Handle 返回后断言其路由标签。
func meshItAssertRoute(t *testing.T, ing *meshItIngress, want string) {
	t.Helper()
	select {
	case <-ing.done:
	case <-time.After(3 * time.Second):
		t.Fatal("超时等待 A 侧 Handle 返回(连接未收链)")
	}
	select {
	case md := <-ing.mds:
		if md.Route != want {
			t.Fatalf("metadata.Route = %q, want %q", md.Route, want)
		}
	default:
		t.Fatal("未取到入站连接的 metadata")
	}
}

// ===== 用例 1:双节点 LAN 全链(真 SNI 嗅探 + 内容透传) =====

// TestMeshTwoNodeLANEndToEnd A(入站→规则 mesh:dev-b)→B(loopback relay)→
// echo 目标。断言:内容双向透传;echo 侧看到 A 的原始 TLS 流(真 ClientHello
// 字节原样抵达);真管线 SNI 断言(真 ExtractSNI 嗅探出的 SNI 成为 mesh 帧目标
// 并在 B 侧注入还原);B 的 ACL 走过;注入流量经 B 自己的 toSocks 出口分支落地。
func TestMeshTwoNodeLANEndToEnd(t *testing.T) {
	meshItResetGlobals(t)
	meshItSelfID(t, "dev-a")

	echoAddr, echoReceived := startMeshItEcho(t)
	fakeSocks := registerMeshItFakeSocks(t, echoAddr)

	bGate := newMeshItGate()
	bNode := startMeshItNode(t, "dev-b", []string{"dev-a"},
		&meshItGatedHandler{gate: bGate, inner: newMeshItPipelineHandler()}, nil)

	aIngress := startMeshItIngress(t, newMeshItPipelineHandler(), M.Metadata{
		Network: M.TCP,
		DstIP:   netip.MustParseAddr("203.0.113.7"), // TEST-NET:绕开回环/私网 direct 快速路径
		DstPort: 443,
	})

	// 相位 A:A 的规则 mesh:dev-b(B 相位的 socks 上游尚未配置,防误拨)。
	meshItSetConfig(t, []string{"domains,mesh-echo.example,mesh:dev-b"}, "",
		&config.MeshConfig{ForwardEnable: meshItForwardEnable(), Peers: meshItPeers([2]string{"dev-b", bNode.addr})})

	clientHello := buildClientHelloTLS12("mesh-echo.example")
	payload := "PING-THROUGH-MESH"
	sent := append(append([]byte(nil), clientHello...), []byte(payload)...)

	client := meshItDialWrite(t, aIngress.addr, sent)
	defer client.Close()

	// B 注入到达:断言 SNI 经真嗅探链路成为帧目标/注入 HostName,再换配 B 相位。
	inj := meshItWaitInjection(t, bGate)
	if inj.md == nil || inj.md.HostName != "mesh-echo.example" {
		t.Fatalf("注入 HostName = %+v, want mesh-echo.example(真 SNI 嗅探→帧目标→注入还原)", inj.md)
	}
	hdr, ok := meshproxy.InheritedHeaderFromContext(inj.ctx)
	if !ok {
		t.Fatal("注入 ctx 未携带继承帧头")
	}
	if hdr.Target != "mesh-echo.example:443" {
		t.Fatalf("帧头 Target = %q, want mesh-echo.example:443(SNI 透传)", hdr.Target)
	}
	if hdr.Class != meshproto.ClassCustom {
		t.Fatalf("帧头 Class = %q, want custom(非 56432 推断)", hdr.Class)
	}
	meshItAssertVia(t, inj, "dev-a", []string{"dev-a", "dev-b"}, 1)

	// 相位 B:B 的普通出口规则(toSocks)+ socks 上游就位,放行注入。
	meshItSetConfig(t, []string{"domains,mesh-echo.example"}, "socks5",
		&config.MeshConfig{ForwardEnable: meshItForwardEnable(), Peers: meshItPeers([2]string{"dev-b", bNode.addr})})
	bGate.open()

	// ACL 观测:B 的 PeerAllowed 必须看到转发方 dev-a(门禁走过)。
	meshItWaitForwarder(t, bNode, "dev-a")

	// 目标侧看到 A 的原始流:echo 收到 ClientHello+payload 原字节。
	if got := meshItCollectBytes(t, echoReceived, len(sent)); string(got) != string(sent) {
		t.Fatalf("echo 收到的字节与 A 原始流不一致: got %d bytes want %d bytes", len(got), len(sent))
	}

	// 内容双向透传:客户端读回 echo 的原样回显。
	if got := meshItReadFull(t, client, len(sent)); string(got) != string(sent) {
		t.Fatalf("客户端回显不一致: got %q", got)
	}

	// B 的 toSocks 出口按域名接管注入连接(注入流量继承 B 全部规则的证明)。
	select {
	case host := <-fakeSocks.hosts:
		if host != "mesh-echo.example" {
			t.Fatalf("socks 出口所见域名 = %q, want mesh-echo.example", host)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("超时等待 B 侧 socks 出口拨出")
	}
}

// ===== 用例 2:三跳链 =====

// TestMeshThreeHopChain A→B→C:中间跳 B 规则再命中 mesh:C(相位换配),帧头
// Via=[A,B,C] 递增、Hops 单调不重置;三跳内容透传(ClientHello 原样抵达 C)。
func TestMeshThreeHopChain(t *testing.T) {
	meshItResetGlobals(t)
	meshItSelfID(t, "dev-a")

	cCapture := newMeshItCaptureHandler()
	cNode := startMeshItNode(t, "dev-c", []string{"dev-a", "dev-b"}, cCapture, nil)

	bGate := newMeshItGate()
	bNode := startMeshItNode(t, "dev-b", []string{"dev-a"},
		&meshItGatedHandler{gate: bGate, inner: newMeshItPipelineHandler()}, nil)

	aIngress := startMeshItIngress(t, newMeshItPipelineHandler(), M.Metadata{
		Network: M.TCP,
		DstIP:   netip.MustParseAddr("203.0.113.8"),
		DstPort: 443,
	})

	peers := func() []config.MeshPeer {
		return meshItPeers([2]string{"dev-b", bNode.addr}, [2]string{"dev-c", cNode.addr})
	}
	// 相位 A:A 的规则 mesh:dev-b。
	meshItSetConfig(t, []string{"domains,three.test,mesh:dev-b"}, "",
		&config.MeshConfig{ForwardEnable: meshItForwardEnable(), Peers: peers()})

	clientHello := buildClientHelloTLS12("three.test")
	payload := "THREE-HOP-BODY"
	sent := append(append([]byte(nil), clientHello...), []byte(payload)...)

	client := meshItDialWrite(t, aIngress.addr, sent)
	defer client.Close()

	// 第一跳 B 到达:Via 已含 B;换配 B 相位(规则→dev-c)后放行,B 以继承
	// 帧头原样续传上线(不重置不追加)。
	bInj := meshItWaitInjection(t, bGate)
	meshItAssertVia(t, bInj, "dev-a", []string{"dev-a", "dev-b"}, 1)
	meshItSetConfig(t, []string{"domains,three.test,mesh:dev-c"}, "",
		&config.MeshConfig{ForwardEnable: meshItForwardEnable(), Peers: peers()})
	bGate.open()

	// 第二跳 C 注入:Via=[A,B,C] 递增(出口续传 [A,B] + C 入站 Append)。
	cInj := meshItWaitArrival(t, cCapture.events)
	meshItAssertVia(t, cInj, "dev-a", []string{"dev-a", "dev-b", "dev-c"}, 2)
	if cInj.md == nil || cInj.md.HostName != "three.test" {
		t.Fatalf("C 注入 HostName = %+v, want three.test", cInj.md)
	}
	// 三跳内容透传:原始流(ClientHello+载荷)原样抵达 C。
	if got := meshItCollectBytes(t, cCapture.payloads, len(sent)); string(got) != string(sent) {
		t.Fatalf("C 收到的字节与 A 原始流不一致: got %d bytes want %d bytes", len(got), len(sent))
	}
}

// ===== 用例 3:A↔B 互指环路 =====

// TestMeshLoopRejected A↔B 互指规则:B 规则再指回 dev-a,继承帧头(Via 含 A、
// B)抵达 A 出口时被 path vector 拦截:拒绝行一路回源到 A 侧客户端、随后断链;
// goroutine/连接无泄漏(计数回落基线)。
func TestMeshLoopRejected(t *testing.T) {
	meshItResetGlobals(t)
	meshItSelfID(t, "dev-a")

	bGate := newMeshItGate()
	bNode := startMeshItNode(t, "dev-b", []string{"dev-a"},
		&meshItGatedHandler{gate: bGate, inner: newMeshItPipelineHandler()}, nil)
	// A 也常驻 relay(SelfID=dev-a,放行 dev-b):B 回指 dev-a 时落到这里。
	aRelay := startMeshItNode(t, "dev-a", []string{"dev-b"}, newMeshItCaptureHandler(), nil)

	aIngress := startMeshItIngress(t, newMeshItPipelineHandler(), M.Metadata{
		Network: M.TCP,
		DstIP:   netip.MustParseAddr("203.0.113.9"),
		DstPort: 443,
	})

	peers := func() []config.MeshPeer {
		return meshItPeers([2]string{"dev-b", bNode.addr}, [2]string{"dev-a", aRelay.addr})
	}
	meshItSetConfig(t, []string{"domains,ping.test,mesh:dev-b"}, "",
		&config.MeshConfig{ForwardEnable: meshItForwardEnable(), Peers: peers()})

	baseline := runtime.NumGoroutine() // 节点/入站常驻 goroutine 计入基线

	client := meshItDialWrite(t, aIngress.addr, buildClientHelloTLS12("ping.test"))

	// B 到达:换配 B 相位(规则回指 dev-a)放行 → B 以继承帧头拨 A → A 出口
	// path vector 拦截(Via 已含 dev-a)。
	_ = meshItWaitInjection(t, bGate)
	meshItSetConfig(t, []string{"domains,ping.test,mesh:dev-a"}, "",
		&config.MeshConfig{ForwardEnable: meshItForwardEnable(), Peers: peers()})
	bGate.open()

	// 快速失败 + 错误回源:拒绝行沿 B→A 链路回到 A 侧客户端,随后断链。
	meshItReadLineThenEOF(t, client, "loop detected via dev-a")
	_ = client.Close()

	meshItWaitGoroutines(t, baseline, 3)
}

// ===== 用例 4:跨节点环 A→B→C→B(Task 7 强制验收) =====

// TestMeshCrossNodeLoopBlocked A→B→C→B 环:C 相位规则指回 dev-b,继承帧头经
// B 出口时 Via 已含 B → path vector 拦截。本环 Hops=2 远小于上限,证明拦截
// 来自 Via 自查(而非跳数帽);拒绝行回源至 A 侧客户端。
func TestMeshCrossNodeLoopBlocked(t *testing.T) {
	meshItResetGlobals(t)
	meshItSelfID(t, "dev-a")

	cGate := newMeshItGate()
	cNode := startMeshItNode(t, "dev-c", []string{"dev-a", "dev-b"},
		&meshItGatedHandler{gate: cGate, inner: newMeshItPipelineHandler()}, nil)

	bGate := newMeshItGate()
	bNode := startMeshItNode(t, "dev-b", []string{"dev-a"},
		&meshItGatedHandler{gate: bGate, inner: newMeshItPipelineHandler()}, nil)

	aIngress := startMeshItIngress(t, newMeshItPipelineHandler(), M.Metadata{
		Network: M.TCP,
		DstIP:   netip.MustParseAddr("203.0.113.10"),
		DstPort: 443,
	})

	peers := func() []config.MeshPeer {
		return meshItPeers([2]string{"dev-b", bNode.addr}, [2]string{"dev-c", cNode.addr})
	}
	setRules := func(rule string) {
		meshItSetConfig(t, []string{rule}, "",
			&config.MeshConfig{ForwardEnable: meshItForwardEnable(), Peers: peers()})
	}
	setRules("domains,ring.test,mesh:dev-b")

	baseline := runtime.NumGoroutine()

	client := meshItDialWrite(t, aIngress.addr, buildClientHelloTLS12("ring.test"))

	// 第一跳 B 到达 → B 相位规则→dev-c → 放行。
	_ = meshItWaitInjection(t, bGate)
	setRules("domains,ring.test,mesh:dev-c")
	bGate.open()

	// 第二跳 C 到达(Via=[A,B,C])→ C 相位规则回指 dev-b → 放行。
	cInj := meshItWaitInjection(t, cGate)
	meshItAssertVia(t, cInj, "dev-a", []string{"dev-a", "dev-b", "dev-c"}, 2)
	setRules("domains,ring.test,mesh:dev-b")
	cGate.open()

	// B 出口第二次收流:Via 含 dev-b → path vector 拦截,错误回源到 A 侧客户端。
	meshItReadLineThenEOF(t, client, "loop detected via dev-b")
	_ = client.Close()

	meshItWaitGoroutines(t, baseline, 3)
}

// ===== 用例 5:中继载体拒绝 custom 类别 =====

// TestMeshRelayCarrierRejectsCustomClass spec §11/design §4 集成级复验:中继
// 载体端点(生产 relay.ProductionDeps + 全局配置映射)收到 custom 类别帧 →
// 门禁/ACL 通过后在类别闸被拒,一行错误回源后断链。
func TestMeshRelayCarrierRejectsCustomClass(t *testing.T) {
	meshItResetGlobals(t)

	// 生产 deps 组装:ForwardEnable/Peers 经全局配置映射(relay.ProductionDeps)。
	meshItSetConfig(t, nil, "",
		&config.MeshConfig{ForwardEnable: meshItForwardEnable(), Peers: meshItPeers([2]string{"dev-a", closedMeshItAddr(t)})})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	r := relay.New(relay.ProductionDeps("dev-b"))
	go func() { _ = r.Serve(ln, meshproto.CarrierRelay) }() // 中继端点载体
	t.Cleanup(func() { _ = ln.Close() })

	conn := meshItDialWrite(t, ln.Addr().String(), nil)
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

	hdr := &meshproto.Header{
		From:   "dev-a",
		Class:  meshproto.ClassCustom, // 用户自定义:仅 LAN 载体(design §4)
		Target: "a.test:443",
		Via:    []string{"dev-a"},
	}
	line, err := hdr.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte(line)); err != nil {
		t.Fatal(err)
	}

	resp, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Fatalf("期望拒绝行, got err=%v line=%q", err, resp)
	}
	if !strings.Contains(resp, "custom") || !strings.Contains(resp, "relay") {
		t.Fatalf("拒绝行 %q 应标明 custom×relay 类别闸", resp)
	}
	n, err := conn.Read(make([]byte, 32))
	if n != 0 || !errors.Is(err, io.EOF) {
		t.Fatalf("类别闸拒绝后应断链, got n=%d err=%v", n, err)
	}
}

// ===== 用例 6:DNS 代转发端到端 =====

// meshItFakeResolver B 侧代解析 fake:A 查询回固定 IP(不发真查询)。
type meshItFakeResolver struct {
	gotHost chan string
}

func (r *meshItFakeResolver) LookupIP(ctx context.Context, network, host string) ([]net.IP, error) {
	select {
	case r.gotHost <- host:
	default:
	}
	return []net.IP{net.ParseIP("203.0.113.66")}, nil
}
func (r *meshItFakeResolver) LookupAddr(ctx context.Context, addr string) ([]string, error) {
	return nil, nil
}
func (r *meshItFakeResolver) LookupMX(ctx context.Context, name string) ([]*net.MX, error) {
	return nil, nil
}
func (r *meshItFakeResolver) LookupSRV(ctx context.Context, service, proto, name string) (string, []*net.SRV, error) {
	return "", nil, nil
}
func (r *meshItFakeResolver) LookupTXT(ctx context.Context, name string) ([]string, error) {
	return nil, nil
}

var _ tcp.LocalDNSResolver = (*meshItFakeResolver)(nil)

// meshItErrDialer 公网直连 seam:DNS 门激活时绝不 fallback DialUDP(fail-closed
// 的阴性观测——被调用即让测试失败)。
type meshItErrDialer struct {
	dialUDPCalled chan struct{}
}

func (d *meshItErrDialer) DialContext(ctx context.Context, md *M.Metadata) (net.Conn, error) {
	return nil, errors.New("mesh-it: DialContext unused")
}

func (d *meshItErrDialer) DialUDP(md *M.Metadata) (net.PacketConn, error) {
	close(d.dialUDPCalled)
	return nil, errors.New("mesh-it: public DialUDP must not be called when mesh DNS gate active")
}

// meshItUDPConn 假 TUN UDP 连接(tunnel 包 dns_forward_test fakeTUNUDPConn 同款):
// 一次预置查询 + WriteTo 上报回包。
type meshItUDPConn struct {
	id        stack.TransportEndpointID
	query     []byte
	writes    chan []byte
	closeOnce sync.Once
	closeCh   chan struct{}
}

func newMeshItUDPConn(query []byte) *meshItUDPConn {
	return &meshItUDPConn{
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

func (c *meshItUDPConn) ID() *stack.TransportEndpointID { return &c.id }

func (c *meshItUDPConn) ReadFrom(p []byte) (int, net.Addr, error) {
	if c.query != nil {
		n := copy(p, c.query)
		c.query = nil
		return n, &net.UDPAddr{IP: net.IP{192, 168, 0, 2}, Port: 5353}, nil
	}
	<-c.closeCh
	return 0, nil, errors.New("mesh-it: fake tun udp closed")
}

func (c *meshItUDPConn) WriteTo(p []byte, _ net.Addr) (int, error) {
	select {
	case c.writes <- append([]byte(nil), p...):
		return len(p), nil
	case <-c.closeCh:
		return 0, errors.New("mesh-it: fake tun udp closed")
	}
}

func (c *meshItUDPConn) Close() error {
	c.closeOnce.Do(func() { close(c.closeCh) })
	return nil
}

func (c *meshItUDPConn) Read(p []byte) (int, error)       { return 0, errors.New("unused") }
func (c *meshItUDPConn) Write(p []byte) (int, error)      { return 0, errors.New("unused") }
func (c *meshItUDPConn) LocalAddr() net.Addr              { return &net.UDPAddr{} }
func (c *meshItUDPConn) RemoteAddr() net.Addr             { return &net.UDPAddr{} }
func (c *meshItUDPConn) SetDeadline(time.Time) error      { return nil }
func (c *meshItUDPConn) SetReadDeadline(time.Time) error  { return nil }
func (c *meshItUDPConn) SetWriteDeadline(time.Time) error { return nil }

var _ adapter.UDPConn = (*meshItUDPConn)(nil)

// TestMeshDNSForwardEndToEnd TUN 侧伪造 UDP/53 查询 → 规则门激活(dns 规则经
// 真编译路径生效 + peer LANAddrs 装配)→ mesh stream 到 B → B 真代解析(fake
// 回固定 IP)→ 回包交付 TUN 侧;公网 DialUDP 零调用(fail-closed)。
func TestMeshDNSForwardEndToEnd(t *testing.T) {
	meshItResetGlobals(t)
	meshItSelfID(t, "dev-a")

	resolver := &meshItFakeResolver{gotHost: make(chan string, 4)}
	bNode := startMeshItNode(t, "dev-b", []string{"dev-a"}, newMeshItCaptureHandler(), resolver)

	// dns 规则 + peer LANAddrs:门控两要素就位(下方经真编译路径断言激活)。
	meshItSetConfig(t, []string{"dns,mesh:dev-b"}, "",
		&config.MeshConfig{ForwardEnable: meshItForwardEnable(), Peers: meshItPeers([2]string{"dev-b", bNode.addr})})

	// 会话租约:HandleUDP 经 worker + AcquireProxyLease,需 Active 会话。
	authority := auth.ResetSessionAuthorityForTest()
	op := authority.BeginOperation()
	if err := authority.CommitAuthenticated(op, &auth.UserInfo{Username: "mesh-it"}, "", nil); err != nil {
		t.Fatalf("激活会话: %v", err)
	}
	t.Cleanup(func() { _ = auth.ResetSessionAuthorityForTest() })

	dialer := &meshItErrDialer{dialUDPCalled: make(chan struct{})}
	tun := tunnel.New(dialer, statistic.DefaultManager)

	// 门激活断言(dns 规则经真编译路径生效):用与 tls.go 决策/MeshDNSForwardTarget
	// 同源的 CompileRuntimeSnapshotFromRuntimeInputs 现编译快照,断言 dns 规则
	// 已编译为 toNode:dev-b。不直接调 routing.MeshDNSForwardTarget——其 10s memo
	// 是生产热更容忍语义(跨 -count 迭代持有上一迭代的已关监听地址,本测试的
	// 随机端口与它天然互斥;memo 行为由 processor/routing 包内测试钉死)。
	snapshot, err := routing.CompileRuntimeSnapshotFromRuntimeInputs(config.GetGlobalConfig(), routing.EffectiveSwitchStatus())
	if err != nil {
		t.Fatalf("编译快照: %v", err)
	}
	dnsRuleActive := false
	for _, rule := range snapshot.Rules() {
		if rule.Type() == "dns" && rule.Enabled() && rule.Target() == "toNode:dev-b" {
			dnsRuleActive = true
			break
		}
	}
	if !dnsRuleActive {
		t.Fatal("dns,mesh:dev-b 规则未编译为启用的 toNode:dev-b dns 规则(门未激活)")
	}

	// A 侧接线缝(生产由 app/tunnel 组装):按 peers 的 LANAddrs 装配 mesh Peer
	// (MeshDNSForwardTarget 同款派生,免 memo 确定性直取)。
	tun.SetMeshDNSForwardPeerSource(func() *meshproxy.Peer {
		cfg := config.GetGlobalConfig()
		if cfg == nil || cfg.Customer == nil || cfg.Customer.Mesh == nil {
			return nil
		}
		for _, peer := range cfg.Customer.Mesh.Peers {
			if peer.DeviceID != "dev-b" || len(peer.LANAddrs) == 0 {
				continue
			}
			return meshproxy.NewPeer(meshproxy.PeerTarget{
				DeviceID: "dev-b",
				LANAddrs: peer.LANAddrs,
				Relay:    false, // TUN DNS 一律 custom 仅 LAN 载体
				SelfID:   "dev-a",
			})
		}
		return nil
	})
	tun.ProcessAsync()
	t.Cleanup(tun.Close)

	query := new(dns.Msg)
	query.Id = 0x77aa
	query.RecursionDesired = true
	query.Question = []dns.Question{{Name: "mesh-dns.example.", Qtype: dns.TypeA, Qclass: dns.ClassINET}}
	queryBytes, err := query.Pack()
	if err != nil {
		t.Fatal(err)
	}

	uc := newMeshItUDPConn(queryBytes)
	tun.HandleUDP(uc)
	defer uc.Close()

	// 回包交付 TUN 侧(带 deadline)。
	select {
	case got := <-uc.writes:
		resp := new(dns.Msg)
		if err := resp.Unpack(got); err != nil {
			t.Fatalf("回包拆包失败: %v", err)
		}
		if resp.Rcode != dns.RcodeSuccess {
			t.Fatalf("Rcode = %d(%s), want success", resp.Rcode, dns.RcodeToString[resp.Rcode])
		}
		if resp.Id != 0x77aa {
			t.Fatalf("Id = %#x, want 0x77aa(回显查询 ID)", resp.Id)
		}
		if len(resp.Answer) != 1 {
			t.Fatalf("answers = %d, want 1: %v", len(resp.Answer), resp.Answer)
		}
		if a, ok := resp.Answer[0].(*dns.A); !ok || a.A.String() != "203.0.113.66" {
			t.Fatalf("answer = %v, want A 203.0.113.66(B 侧代解析结果)", resp.Answer[0])
		}
	case <-time.After(5 * time.Second):
		t.Fatal("超时等待 DNS 回包交付 TUN 侧")
	}

	// B 侧观测:真代解析被触发、ACL 走过(帧头 From=dev-a)。
	select {
	case host := <-resolver.gotHost:
		if host != "mesh-dns.example." {
			t.Fatalf("B 代解析查询名 = %q, want mesh-dns.example.", host)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("超时等待 B 侧代解析")
	}
	meshItWaitForwarder(t, bNode, "dev-a")

	// fail-closed:门激活路径绝不拨公网 DNS。
	select {
	case <-dialer.dialUDPCalled:
		t.Fatal("mesh DNS 门激活时公网 DialUDP 不得被调用(fail-closed)")
	default:
	}
}

// ===== 用例 7:离线 AI 改道两跳(Task 12 评审回填,强制) =====

// TestMeshOfflineAIRedirectTwoHop A 离线 AI 改道→B;B 再离线(同一全局信号)
// 触发 B 自己的改道→C,继承帧头 Via 续传([A,B]→[A,B,C]);ping-pong 变体
// (B 兜底指回 A)被 path vector 拦截、错误回源。
func TestMeshOfflineAIRedirectTwoHop(t *testing.T) {
	aiFallback := func(addr string) *config.MeshFallbackConfig {
		return &config.MeshFallbackConfig{Enable: meshItForwardEnable(), Addr: addr, Classes: []string{meshproto.ClassAI}}
	}
	aiIngressMD := M.Metadata{
		Network: M.TCP,
		DstIP:   netip.MustParseAddr("127.0.0.1"),
		DstPort: 56432, // AI 代理回环端口(shouldForceAliangRoute 判据)
	}
	aiRequest := []byte("GET /v1/models HTTP/1.1\r\nHost: 127.0.0.1:56432\r\n\r\n")

	t.Run("B 再改道到 C 且 Via 续传", func(t *testing.T) {
		meshItResetGlobals(t)
		meshItSelfID(t, "dev-a")
		tcp.SetMeshOfflineSignal(func() bool { return true })

		cCapture := newMeshItCaptureHandler()
		cNode := startMeshItNode(t, "dev-c", []string{"dev-a", "dev-b"}, cCapture, nil)

		bGate := newMeshItGate()
		bNode := startMeshItNode(t, "dev-b", []string{"dev-a"},
			&meshItGatedHandler{gate: bGate, inner: newMeshItPipelineHandler()}, nil)

		aIngress := startMeshItIngress(t, newMeshItPipelineHandler(), aiIngressMD)

		peers := func() []config.MeshPeer {
			return meshItPeers([2]string{"dev-b", bNode.addr}, [2]string{"dev-c", cNode.addr})
		}
		// 相位 1:A 的离线兜底=B(帧头 From=dev-a/Class=ai/Target=回环:56432)。
		meshItSetConfig(t, nil, "",
			&config.MeshConfig{ForwardEnable: meshItForwardEnable(), Fallback: aiFallback(bNode.addr), Peers: peers()})

		client := meshItDialWrite(t, aIngress.addr, aiRequest)
		defer client.Close()

		// A 改道→B 到达:帧头 Class=ai、Via=[A,B](B 入站 Append)。
		bInj := meshItWaitInjection(t, bGate)
		hdr, ok := meshproxy.InheritedHeaderFromContext(bInj.ctx)
		if !ok {
			t.Fatal("B 注入 ctx 未携带继承帧头")
		}
		if hdr.Class != meshproto.ClassAI {
			t.Fatalf("帧头 Class = %q, want ai(56432 回环判据)", hdr.Class)
		}
		if hdr.Target != "127.0.0.1:56432" {
			t.Fatalf("帧头 Target = %q, want 127.0.0.1:56432", hdr.Target)
		}
		if bInj.md == nil || bInj.md.DstPort != 56432 || !bInj.md.DstIP.IsLoopback() {
			t.Fatalf("B 注入 metadata = %+v, want 127.0.0.1:56432", bInj.md)
		}

		// 相位 2:B 离线兜底=C(B 的改道读连接时刻配置)→ 放行。
		meshItSetConfig(t, nil, "",
			&config.MeshConfig{ForwardEnable: meshItForwardEnable(), Fallback: aiFallback(cNode.addr), Peers: peers()})
		bGate.open()

		// B 再改道→C:Via=[A,B,C] 续传(链头保留 dev-a、Class/Target 继承)。
		cInj := meshItWaitArrival(t, cCapture.events)
		meshItAssertVia(t, cInj, "dev-a", []string{"dev-a", "dev-b", "dev-c"}, 2)
		cHdr, ok := meshproxy.InheritedHeaderFromContext(cInj.ctx)
		if !ok || cHdr.Class != meshproto.ClassAI || cHdr.Target != "127.0.0.1:56432" {
			t.Fatalf("C 注入帧头 = %+v ok=%v, want ai/127.0.0.1:56432 继承", cHdr, ok)
		}
	})

	t.Run("ping-pong 兜底环被拒", func(t *testing.T) {
		meshItResetGlobals(t)
		meshItSelfID(t, "dev-a")
		tcp.SetMeshOfflineSignal(func() bool { return true })

		// A 也常驻 relay:B 的兜底指回 A 时,Via 已含 dev-a → path vector 拦截。
		aRelay := startMeshItNode(t, "dev-a", []string{"dev-b"}, newMeshItCaptureHandler(), nil)

		bGate := newMeshItGate()
		bNode := startMeshItNode(t, "dev-b", []string{"dev-a"},
			&meshItGatedHandler{gate: bGate, inner: newMeshItPipelineHandler()}, nil)

		aIngress := startMeshItIngress(t, newMeshItPipelineHandler(), aiIngressMD)

		peers := func() []config.MeshPeer {
			return meshItPeers([2]string{"dev-b", bNode.addr}, [2]string{"dev-a", aRelay.addr})
		}
		// 相位 1:A 的离线兜底=B。
		meshItSetConfig(t, nil, "",
			&config.MeshConfig{ForwardEnable: meshItForwardEnable(), Fallback: aiFallback(bNode.addr), Peers: peers()})

		baseline := runtime.NumGoroutine()
		client := meshItDialWrite(t, aIngress.addr, aiRequest)

		// B 到达 → 相位 2:B 的离线兜底=A(ping-pong)→ 放行。
		_ = meshItWaitInjection(t, bGate)
		meshItSetConfig(t, nil, "",
			&config.MeshConfig{ForwardEnable: meshItForwardEnable(), Fallback: aiFallback(aRelay.addr), Peers: peers()})
		bGate.open()

		// 环拒 + 错误回源:A 出口 path vector 拦截,拒绝行回到 A 侧客户端。
		meshItReadLineThenEOF(t, client, "loop detected via dev-a")
		_ = client.Close()
		meshItWaitGoroutines(t, baseline, 3)
	})
}

// ===== 用例 8:离线兜底激活(兜底式) =====

// TestMeshOfflineFallbackActivates 兜底式两态:正常路径(mesh peer)可用时兜底
// 不触发;切断正常路径后链头先落兜底(RouteMeshFallback,帧头抵达兜底节点)。
func TestMeshOfflineFallbackActivates(t *testing.T) {
	meshItResetGlobals(t)
	meshItSelfID(t, "dev-a")

	fbAddr, fbHeaders := startMeshItHeaderPeer(t)
	bAddr, bHeaders := startMeshItHeaderPeer(t)

	aIngress := startMeshItIngress(t, newMeshItPipelineHandler(), M.Metadata{
		Network: M.TCP,
		DstIP:   netip.MustParseAddr("203.0.113.20"),
		DstPort: 80, // HTTP 入站形态(GET 提取 Host 驱动规则)
	})
	httpReq := []byte("GET /x HTTP/1.1\r\nHost: fb.example\r\n\r\n")

	fallbackCfg := func() *config.MeshFallbackConfig {
		return &config.MeshFallbackConfig{Enable: meshItForwardEnable(), Addr: fbAddr, Classes: []string{meshproto.ClassCustom}}
	}

	t.Run("正常路径可用不落兜底", func(t *testing.T) {
		meshItSetConfig(t, []string{"domains,fb.example,mesh:dev-b"}, "",
			&config.MeshConfig{ForwardEnable: meshItForwardEnable(), Fallback: fallbackCfg(),
				Peers: meshItPeers([2]string{"dev-b", bAddr})})

		client := meshItDialWrite(t, aIngress.addr, httpReq)
		hdr := meshItWaitHeader(t, bHeaders)
		if hdr.From != "dev-a" || hdr.Target != "fb.example:80" {
			t.Fatalf("正常路径帧头 = %+v, want dev-a→fb.example:80", hdr)
		}
		meshItExpectNoHeader(t, fbHeaders, "兜底节点(正常路径可用)")

		// 收链后断言路由标签:正常路径成功 = RouteToNode,非兜底。
		_ = client.Close()
		meshItAssertRoute(t, aIngress, "RouteToNode")
	})

	t.Run("切断正常路径后落兜底", func(t *testing.T) {
		// 正常路径 dev-b 换成不可达地址(配置漂移/节点下线),兜底节点可用。
		meshItSetConfig(t, []string{"domains,fb.example,mesh:dev-b"}, "",
			&config.MeshConfig{ForwardEnable: meshItForwardEnable(), Fallback: fallbackCfg(),
				Peers: meshItPeers([2]string{"dev-b", closedMeshItAddr(t)})})

		client := meshItDialWrite(t, aIngress.addr, httpReq)
		defer client.Close()

		hdr := meshItWaitHeader(t, fbHeaders)
		if hdr.From != "dev-a" || hdr.Target != "fb.example:80" {
			t.Fatalf("兜底帧头 = %+v, want dev-a→fb.example:80", hdr)
		}
		if hdr.Class != meshproto.ClassCustom {
			t.Fatalf("兜底帧头 Class = %q, want custom", hdr.Class)
		}
		meshItExpectNoHeader(t, bHeaders, "正常路径(已切断)")

		// 收链后断言路由标签:链头先落兜底 = RouteMeshFallback。
		_ = client.Close()
		meshItAssertRoute(t, aIngress, "RouteMeshFallback")
	})
}

// ===== 用例 9:生产装配冒烟(Task 14) =====

// TestProductionAssemblyEgressOfflineSmoke 生产装配线的端到端冒烟:真
// manager(Configure 全程真跑——validateConfig 过、runTunnel 拨死回环 piko
// 落 failed)装配的 egress 双缝驱动真出口——离线(会话 failed) + self 类兜底
// 勾选时,egress.PlatformClient 的平台请求真走 mesh 轨:帧头 From=dev-a(manager
// meshSelfDeviceID)/Class=self/Target=TEST-NET 域名,ACL 观察到 from,HTTP 字节
// 抵达 Peers[0] 对端;同源的 tcp 侧 AI 改道门(MeshOfflineAIRedirectActive)随
// 同一离线信号激活。B 侧注入管线语义由用例 1-8 钉,此处 B 用捕获器只钉 A 侧
// 装配真的把流量送进了 mesh。
func TestProductionAssemblyEgressOfflineSmoke(t *testing.T) {
	meshItResetGlobals(t)

	capture := newMeshItCaptureHandler()
	nodeB := startMeshItNode(t, "dev-b", []string{"dev-a"}, capture, nil)

	// 全局配置:mesh 开 + peers[0]=dev-b(egress peer 源装配面)+ self/ai 两类
	// 离线兜底勾选(egress 双轨 + AI 改道门)。
	meshItSetConfig(t, nil, "", &config.MeshConfig{
		ForwardEnable: meshItForwardEnable(),
		Fallback: &config.MeshFallbackConfig{
			Enable:  meshItForwardEnable(),
			Addr:    nodeB.addr,
			Classes: []string{meshproto.ClassSelf, meshproto.ClassAI},
		},
		Peers: meshItPeers([2]string{"dev-b", nodeB.addr}),
	})

	// 生产装配:真 manager 接线(validateConfig:egress 双缝+selfID+pikoDial)。
	// 回环死口 piko 下 runTunnel 的 Listen 是惰性重连(piko 客户端后台重试,
	// 状态停留 connecting/reconnecting = 活跃 = 在线),故用确定性的 Stop()
	// 翻离线——「平台不可达」信号语义即「无活跃隧道会话」,Stop 后恒真,
	// 零手工注入缝。
	mgr := apptunnel.NewManager(nil)
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := mgr.Configure(apptunnel.Config{
		DeviceID:        "dev-a",
		PikoUpstreamURL: "http://127.0.0.1:1",
		TunnelToken:     "mesh-it-token",
		RoutePublicKey:  base64.StdEncoding.EncodeToString(pub),
		ExpiresAt:       time.Now().Add(time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	mgr.Stop()
	deadline := time.After(3 * time.Second)
	for !egress.OfflineSignalForTest() {
		select {
		case <-deadline:
			t.Fatal("超时等待 Stop 后离线信号翻真")
		case <-time.After(20 * time.Millisecond):
		}
	}

	// tcp 侧冒烟:同一离线信号下 AI 改道门激活(行为面布尔投影)。
	if !tcp.MeshOfflineAIRedirectActive() {
		t.Fatal("离线 + ai 兜底勾选时 MeshOfflineAIRedirectActive 应为 true")
	}

	// egress 侧冒烟:平台请求(登录/用量族)经 mesh 轨抵达 Peers[0] 对端。
	go func() {
		client := egress.PlatformClient(6 * time.Second)
		_, _ = client.Get("http://203.0.113.20:81/assembly-probe") //nolint:bodyclose // conn 由捕获器关
	}()

	meshItWaitForwarder(t, nodeB, "dev-a")
	inj := meshItWaitArrival(t, capture.events)
	// IP 目标经 meshMetadataFromTarget 落 DstIP/DstPort(域名才进 HostName)。
	if inj.md == nil || inj.md.DstIP.String() != "203.0.113.20" || inj.md.DstPort != 81 {
		t.Fatalf("B 注入 metadata = %+v, want 203.0.113.20:81", inj.md)
	}
	select {
	case chunk := <-capture.payloads:
		if !strings.Contains(string(chunk), "GET /assembly-probe HTTP/1.1") {
			t.Fatalf("B 收到字节 = %q, want 含 GET /assembly-probe", chunk)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("超时等待 HTTP 请求字节抵达 B")
	}
}
