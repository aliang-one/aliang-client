package mesh

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"

	M "aliang.one/nursorgate/inbound/tun/metadata"
	"aliang.one/nursorgate/outbound/meshproto"
	"aliang.one/nursorgate/processor/config"
)

// fakePeer 模拟 B 侧:接受一条连接,读出首行 JSON 帧头送回测试,之后对
// 剩余字节做回显(模拟 B 侧透传语义)。loopback 真监听,仓内测试惯例。
type fakePeer struct {
	ln       net.Listener
	headerCh chan *meshproto.Header
	errCh    chan error
}

func startFakePeer(t *testing.T) *fakePeer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	fp := &fakePeer{
		ln:       ln,
		headerCh: make(chan *meshproto.Header, 1),
		errCh:    make(chan error, 1),
	}
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			fp.errCh <- err
			return
		}
		defer conn.Close()
		// 读头必须从 bufio 拷贝:客户端头后紧跟的字节可能已被 bufio 缓存。
		reader := bufio.NewReader(conn)
		line, err := reader.ReadString('\n')
		if err != nil {
			fp.errCh <- fmt.Errorf("read header: %w", err)
			return
		}
		hdr, _, err := meshproto.Parse([]byte(line))
		if err != nil {
			fp.errCh <- fmt.Errorf("parse header: %w", err)
			return
		}
		fp.headerCh <- hdr
		_, _ = io.Copy(conn, reader)
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return fp
}

// closedPortAddr 返回一个「监听后立即关闭」的回环地址,拨它稳定 connection refused。
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

func waitHeader(t *testing.T, fp *fakePeer) *meshproto.Header {
	t.Helper()
	select {
	case hdr := <-fp.headerCh:
		return hdr
	case err := <-fp.errCh:
		t.Fatalf("fake peer: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for mesh header")
	}
	return nil
}

// TestDialContextLANCarrierWritesHeader LAN 载体拨出:首行 JSON 帧头
// From/Class/Target/Via 正确,后续字节双向可写。
func TestDialContextLANCarrierWritesHeader(t *testing.T) {
	fp := startFakePeer(t)
	peer := NewPeer(PeerTarget{
		DeviceID: "dev-b",
		LANAddrs: []string{fp.ln.Addr().String()},
		SelfID:   "dev-a",
		MaxHops:  5,
	})

	md := &M.Metadata{HostName: "example.com", DstPort: 443}
	conn, err := peer.DialContext(context.Background(), md)
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	defer conn.Close()

	hdr := waitHeader(t, fp)
	if hdr.From != "dev-a" {
		t.Fatalf("From = %q, want dev-a", hdr.From)
	}
	if hdr.Class != meshproto.ClassCustom {
		t.Fatalf("Class = %q, want custom(默认推断:非 56432 回环)", hdr.Class)
	}
	if hdr.Target != "example.com:443" {
		t.Fatalf("Target = %q, want example.com:443", hdr.Target)
	}
	if len(hdr.Via) != 1 || hdr.Via[0] != "dev-a" {
		t.Fatalf("Via = %v, want [dev-a]", hdr.Via)
	}
	if hdr.Hops != 0 {
		t.Fatalf("Hops = %d, want 0", hdr.Hops)
	}

	// 头之后是原始字节流:写入应被 B 侧回显回来。
	payload := []byte("mesh-lan-payload")
	if _, err := conn.Write(payload); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	buf := make([]byte, len(payload))
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if string(buf) != string(payload) {
		t.Fatalf("echo mismatch: got %q want %q", buf, payload)
	}
}

// TestDialContextCarrierFallback LAN 全败且无中继可用:错误回源,不吞不回落。
func TestDialContextCarrierFallback(t *testing.T) {
	unreachable := closedPortAddr(t)

	peer := NewPeer(PeerTarget{
		DeviceID: "dev-b",
		LANAddrs: []string{unreachable},
		Relay:    false,
		SelfID:   "dev-a",
	})
	_, err := peer.DialContext(context.Background(), &M.Metadata{HostName: "example.com", DstPort: 443})
	if err == nil {
		t.Fatal("LAN 不可达且 Relay=false 时应报错")
	}
	if !strings.Contains(err.Error(), "dev-b") {
		t.Fatalf("错误应携带对端 device_id 便于回源定位, got: %v", err)
	}

	// Relay=true 但未注入 PikoDial(生产未接线/平台端点未放行)同样只剩报错。
	peerNoDial := NewPeer(PeerTarget{
		DeviceID: "dev-b",
		LANAddrs: []string{unreachable},
		Relay:    true,
		SelfID:   "dev-a",
	})
	if _, err := peerNoDial.DialContext(context.Background(), &M.Metadata{HostName: "example.com", DstPort: 443}); err == nil {
		t.Fatal("Relay=true 但 PikoDial 未注入时应报错")
	}
}

// TestDialContextRelayCarrierDeniedForCustomClass custom 类别仅剩中继载体:
// 引擎层硬约束拒绝(design §4),且 PikoDial 根本不被调用。
func TestDialContextRelayCarrierDeniedForCustomClass(t *testing.T) {
	dialCalled := false
	peer := NewPeer(PeerTarget{
		DeviceID: "dev-b",
		Relay:    true,
		PikoDial: func(ctx context.Context, endpointID string) (net.Conn, error) {
			dialCalled = true
			return nil, fmt.Errorf("must not be called for custom class")
		},
		SelfID: "dev-a",
	})

	ctx := WithClass(context.Background(), meshproto.ClassCustom)
	_, err := peer.DialContext(ctx, &M.Metadata{HostName: "example.com", DstPort: 443})
	if err == nil {
		t.Fatal("custom 类别禁走中继载体,应报错")
	}
	if dialCalled {
		t.Fatal("custom 类别被拒后 PikoDial 不应被调用")
	}
}

// TestRelayCarrierHeadHeaderAndEndpointNaming 链头帧头语义(YAGNI 简化):
// 链头不做中间跳 AppendHop(那是 Task 6 relay 的事),断言帧头 Via=[SelfID]、
// Hops 由 0 起。经中继载体(fake PikoDial)发送,顺带覆盖中继载体路径 +
// 端点命名 <DeviceID>.mesh + AI 默认推断(56432 回环)+ ConnID 透传。
func TestRelayCarrierHeadHeaderAndEndpointNaming(t *testing.T) {
	fp := startFakePeer(t)
	peer := NewPeer(PeerTarget{
		DeviceID: "dev-b",
		Relay:    true,
		PikoDial: func(ctx context.Context, endpointID string) (net.Conn, error) {
			if endpointID != "dev-b.mesh" {
				return nil, fmt.Errorf("endpoint = %q, want dev-b.mesh", endpointID)
			}
			if got := ConnIDFromContext(ctx); got != "conn-42" {
				return nil, fmt.Errorf("conn id = %q, want conn-42", got)
			}
			return net.Dial("tcp", fp.ln.Addr().String())
		},
		SelfID:  "dev-a",
		MaxHops: 5,
	})

	// 不显式 WithClass:DstPort=56432 且回环 → 默认推断 ai → 中继准入。
	md := &M.Metadata{DstIP: netip.MustParseAddr("127.0.0.1"), DstPort: 56432, ConnID: "conn-42"}
	conn, err := peer.DialContext(context.Background(), md)
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	defer conn.Close()

	hdr := waitHeader(t, fp)
	if hdr.Class != meshproto.ClassAI {
		t.Fatalf("Class = %q, want ai(56432 回环默认推断)", hdr.Class)
	}
	if hdr.Target != "127.0.0.1:56432" {
		t.Fatalf("Target = %q, want 127.0.0.1:56432(HostName 空用 DstIP)", hdr.Target)
	}
	if len(hdr.Via) != 1 || hdr.Via[0] != "dev-a" {
		t.Fatalf("链头帧头 Via = %v, want [dev-a]", hdr.Via)
	}
	if hdr.Hops != 0 {
		t.Fatalf("链头帧头 Hops = %d, want 0", hdr.Hops)
	}
}

// TestDialContextRelayCarrierFallsBackAfterLANFailure LAN 全败 → 中继载体
// 接管(自身/AI 类别;design §5 LAN 优先、中继兜底)。
func TestDialContextRelayCarrierFallsBackAfterLANFailure(t *testing.T) {
	fp := startFakePeer(t)
	unreachable := closedPortAddr(t)
	peer := NewPeer(PeerTarget{
		DeviceID: "dev-b",
		LANAddrs: []string{unreachable},
		Relay:    true,
		PikoDial: func(ctx context.Context, endpointID string) (net.Conn, error) {
			return net.Dial("tcp", fp.ln.Addr().String())
		},
		SelfID: "dev-a",
	})

	ctx := WithClass(context.Background(), meshproto.ClassSelf)
	conn, err := peer.DialContext(ctx, &M.Metadata{HostName: "example.com", DstPort: 443})
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	defer conn.Close()

	hdr := waitHeader(t, fp)
	if hdr.Class != meshproto.ClassSelf {
		t.Fatalf("Class = %q, want self(WithClass 显式指定优先)", hdr.Class)
	}
	if hdr.Target != "example.com:443" {
		t.Fatalf("Target = %q, want example.com:443", hdr.Target)
	}
}

// TestClassInference 类别判定:显式 WithClass 优先;默认推断走
// looksLikeAITraffic(56432 回环口径,与 processor/tcp route_override 同源)。
func TestClassInference(t *testing.T) {
	aiMD := &M.Metadata{DstIP: netip.MustParseAddr("127.0.0.1"), DstPort: 56432}
	if got := classFromContext(context.Background(), aiMD); got != meshproto.ClassAI {
		t.Fatalf("class = %q, want ai", got)
	}
	// 端口对但目标非回环 → custom
	if got := classFromContext(context.Background(), &M.Metadata{DstIP: netip.MustParseAddr("1.1.1.1"), DstPort: 56432}); got != meshproto.ClassCustom {
		t.Fatalf("class = %q, want custom", got)
	}
	// 回环但端口不对 → custom
	if got := classFromContext(context.Background(), &M.Metadata{DstIP: netip.MustParseAddr("127.0.0.1"), DstPort: 8443}); got != meshproto.ClassCustom {
		t.Fatalf("class = %q, want custom", got)
	}
	// hostName=localhost 且端口对 → ai(HostName 回环判据)
	if got := classFromContext(context.Background(), &M.Metadata{HostName: "localhost", DstPort: 56432}); got != meshproto.ClassAI {
		t.Fatalf("class = %q, want ai", got)
	}
	// 显式指定优先于端口推断
	for _, class := range []string{meshproto.ClassSelf, meshproto.ClassAI, meshproto.ClassCustom} {
		ctx := WithClass(context.Background(), class)
		if got := classFromContext(ctx, aiMD); got != class {
			t.Fatalf("explicit class = %q, want %q", got, class)
		}
	}
	// 非法类别值按未指定处理,回落推断
	ctx := WithClass(context.Background(), "bogus")
	if got := classFromContext(ctx, aiMD); got != meshproto.ClassAI {
		t.Fatalf("bogus class should fall back to inference, got %q", got)
	}
}

// TestTargetFromMetadata 帧头目标构造:HostName:port 优先、裸域名 443 兜底、
// 已带端口的 HostName 直用、HostName 空回落 DstIP、IPv6 补括号
// (与 meshproto.SplitTarget 拆分口径互逆,B 侧 Task 6 消费)。
func TestTargetFromMetadata(t *testing.T) {
	cases := []struct {
		name string
		md   *M.Metadata
		want string
	}{
		{"host+port", &M.Metadata{HostName: "example.com", DstPort: 443}, "example.com:443"},
		{"裸域名 443 兜底", &M.Metadata{HostName: "example.com"}, "example.com:443"},
		{"HostName 已带端口直用", &M.Metadata{HostName: "example.com:8443", DstPort: 443}, "example.com:8443"},
		{"HostName 空回落 DstIP", &M.Metadata{DstIP: netip.MustParseAddr("192.168.1.2"), DstPort: 80}, "192.168.1.2:80"},
		{"IPv6 补括号", &M.Metadata{HostName: "2001:db8::1", DstPort: 443}, "[2001:db8::1]:443"},
		{"IPv6 已带括号端口直用", &M.Metadata{HostName: "[2001:db8::1]:8443", DstPort: 443}, "[2001:db8::1]:8443"},
		{"dns 目标原样透传", &M.Metadata{HostName: "dns:"}, meshproto.DNSTarget},
		{"dns 目标透传忽略端口", &M.Metadata{HostName: "dns:", DstPort: 53}, meshproto.DNSTarget},
		{"nil", nil, ""},
	}
	for _, tc := range cases {
		if got := targetFromMetadata(tc.md); got != tc.want {
			t.Fatalf("%s: target = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestDialContextLANTriesInOrder LAN 载体顺序尝试:第一地址不可达换第二个,
// 帧头最终到达 fake peer。
func TestDialContextLANTriesInOrder(t *testing.T) {
	fp := startFakePeer(t)
	peer := NewPeer(PeerTarget{
		DeviceID: "dev-b",
		LANAddrs: []string{closedPortAddr(t), fp.ln.Addr().String()},
		SelfID:   "dev-a",
	})

	conn, err := peer.DialContext(context.Background(), &M.Metadata{HostName: "example.com", DstPort: 443})
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	defer conn.Close()

	hdr := waitHeader(t, fp)
	if hdr.From != "dev-a" || hdr.Target != "example.com:443" {
		t.Fatalf("header = %+v, want from dev-a target example.com:443", hdr)
	}
}

// TestDialContextHeaderBuildFailFast 帧头预编码 fail-fast:空 metadata
// (无 HostName/DstIP)在拨任何载体前报错,错误含 build header。
func TestDialContextHeaderBuildFailFast(t *testing.T) {
	peer := NewPeer(PeerTarget{
		DeviceID: "dev-b",
		LANAddrs: []string{"127.0.0.1:1"}, // 不应被拨
		SelfID:   "dev-a",
	})
	_, err := peer.DialContext(context.Background(), &M.Metadata{})
	if err == nil {
		t.Fatal("空 metadata 应在帧头编码处失败")
	}
	if !strings.Contains(err.Error(), "build header") {
		t.Fatalf("错误应含 build header, got: %v", err)
	}
}

// TestRelayFailureIncludesLANError 中继失败分支保留 LAN 失败因果链:
// LAN 全败 + 中继拨号也失败时,错误同时携带两段(lan_last_err)。
func TestRelayFailureIncludesLANError(t *testing.T) {
	peer := NewPeer(PeerTarget{
		DeviceID: "dev-b",
		LANAddrs: []string{closedPortAddr(t)},
		Relay:    true,
		PikoDial: func(ctx context.Context, endpointID string) (net.Conn, error) {
			return nil, fmt.Errorf("endpoint not allowlisted")
		},
		SelfID: "dev-a",
	})

	ctx := WithClass(context.Background(), meshproto.ClassSelf)
	_, err := peer.DialContext(ctx, &M.Metadata{HostName: "example.com", DstPort: 443})
	if err == nil {
		t.Fatal("LAN 与中继双失败应报错")
	}
	if !strings.Contains(err.Error(), "endpoint not allowlisted") {
		t.Fatalf("错误应含中继失败原因, got: %v", err)
	}
	if !strings.Contains(err.Error(), "lan_last_err") {
		t.Fatalf("错误应附 lan_last_err 保留 LAN 失败因果链, got: %v", err)
	}
}

// TestDialContextInheritsHeader 中间跳续传(Task 7 强制验收):ctx 携带
// 继承帧头(mesh_inbound AppendHop 后挂入)时,出口用继承帧头原样上线——
// Via/Hops 不被重置、不再追加本跳(入站侧已 Append 过)。
// 环路语义注:构造 Via 含目标链自身 id 的继承帧头不可达——入站 AppendHop
// 的 Via 自查在 B 侧收流时已拒绝该帧;本用例以 Via=[A,B] Hops=2 断言
// Hops 单调递增不被链头语义重置。
func TestDialContextInheritsHeader(t *testing.T) {
	fp := startFakePeer(t)
	peer := NewPeer(PeerTarget{
		DeviceID: "dev-c",
		LANAddrs: []string{fp.ln.Addr().String()},
		SelfID:   "dev-b", // 中间跳 B 出口;链头帧 From/Via 由继承帧头决定
	})

	inherited := &meshproto.Header{
		From:   "dev-a",
		Class:  meshproto.ClassCustom,
		Target: "c.test:443",
		Via:    []string{"dev-a", "dev-b"},
		Hops:   2,
	}
	ctx := WithInheritedHeader(context.Background(), inherited)
	conn, err := peer.DialContext(ctx, &M.Metadata{HostName: "c.test", DstPort: 443})
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	defer conn.Close()

	hdr := waitHeader(t, fp)
	if hdr.From != "dev-a" {
		t.Fatalf("From = %q, want dev-a(继承帧头保留链头 From)", hdr.From)
	}
	if len(hdr.Via) != 2 || hdr.Via[0] != "dev-a" || hdr.Via[1] != "dev-b" {
		t.Fatalf("Via = %v, want [dev-a dev-b](继承帧头原样上线,不再 AppendHop)", hdr.Via)
	}
	if hdr.Hops != 2 {
		t.Fatalf("Hops = %d, want 2(中间跳续传单调递增,不被链头语义重置)", hdr.Hops)
	}
	if hdr.Target != "c.test:443" {
		t.Fatalf("Target = %q, want c.test:443", hdr.Target)
	}
	if hdr.Class != meshproto.ClassCustom {
		t.Fatalf("Class = %q, want custom(继承帧头类别不被本跳重推断)", hdr.Class)
	}

	// 继承帧头共享 Via 切片防御:出口不得就地改动调用方帧头(追加/重排)。
	if len(inherited.Via) != 2 || inherited.Hops != 2 {
		t.Fatalf("inherited header mutated by DialContext: %+v", inherited)
	}
}

// TestInheritedHeaderClassGovernsCarrierAdmission 中间跳载体准入按继承帧头
// 的流量类别判定(design §4 约束针对流量的原始类别):ai 流量经 B 转发,
// 即使出站目标形态推断为 custom(非 56432 回环),中继载体仍准入、线上帧
// Class 保持 ai——转发跳不得重推断改判。
func TestInheritedHeaderClassGovernsCarrierAdmission(t *testing.T) {
	fp := startFakePeer(t)
	peer := NewPeer(PeerTarget{
		DeviceID: "dev-c",
		Relay:    true,
		PikoDial: func(ctx context.Context, endpointID string) (net.Conn, error) {
			return net.Dial("tcp", fp.ln.Addr().String())
		},
		SelfID: "dev-b",
	})

	inherited := &meshproto.Header{
		From:   "dev-a",
		Class:  meshproto.ClassAI,
		Target: "api.openai.com:443",
		Via:    []string{"dev-a", "dev-b"},
		Hops:   1,
	}
	// 出站形态刻意非 ai(外部地址 + 非 56432 端口):若按推断类别判custom,
	// 中继会被拒——断言继承类别优先。
	ctx := WithInheritedHeader(context.Background(), inherited)
	conn, err := peer.DialContext(ctx, &M.Metadata{DstIP: netip.MustParseAddr("1.2.3.4"), DstPort: 443})
	if err != nil {
		t.Fatalf("DialContext: %v (ai 流量转发不得因本跳推断 custom 被禁中继)", err)
	}
	defer conn.Close()

	hdr := waitHeader(t, fp)
	if hdr.Class != meshproto.ClassAI {
		t.Fatalf("Class = %q, want ai(继承帧头类别原样上线)", hdr.Class)
	}
	if hdr.Target != "api.openai.com:443" {
		t.Fatalf("Target = %q, want 继承帧头目标", hdr.Target)
	}
}

// TestInheritedHeaderFromContext 三件套契约:WithInheritedHeader 注入、
// 未携带返回 false、nil 帧头不误报存在。
func TestInheritedHeaderFromContext(t *testing.T) {
	if _, ok := InheritedHeaderFromContext(context.Background()); ok {
		t.Fatal("未携带继承帧头应返回 ok=false")
	}
	hdr := &meshproto.Header{From: "dev-a", Class: meshproto.ClassSelf, Target: "a.test:443"}
	got, ok := InheritedHeaderFromContext(WithInheritedHeader(context.Background(), hdr))
	if !ok || got != hdr {
		t.Fatalf("InheritedHeaderFromContext() = (%p, %v), want 原帧头指针", got, ok)
	}
	if _, ok := InheritedHeaderFromContext(WithInheritedHeader(context.Background(), nil)); ok {
		t.Fatal("nil 继承帧头应返回 ok=false")
	}
}

// TestAIProxyPortSyncAnchor 锚定本地 aiProxyPort 与 processor/config 的
// DefaultHTTPProxyPort 同值(test-only import 无环已实测;两处判据须同步改,
// 见 mesh.go looksLikeAITraffic 注释)。
func TestAIProxyPortSyncAnchor(t *testing.T) {
	if aiProxyPort != config.DefaultHTTPProxyPort {
		t.Fatalf("aiProxyPort = %d, want %d (config.DefaultHTTPProxyPort)——两处判据失同步",
			aiProxyPort, config.DefaultHTTPProxyPort)
	}
}

// ===== 统一口双端口候选(spec §6.7) =====

// TestLANDialCandidatesExpansion 手填地址双端口候选展开:旧口原样优先,
// 追加统一口候选;已是统一口或畸形地址(无端口)不展开。
func TestLANDialCandidatesExpansion(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"手填旧口追加统一口", "1.2.3.4:56441", []string{"1.2.3.4:56441", "1.2.3.4:56432"}},
		{"已是统一口不展开", "1.2.3.4:56432", []string{"1.2.3.4:56432"}},
		{"畸形无端口原样", "myhost", []string{"myhost"}},
		{"IPv6 旧口展开", "[::1]:56441", []string{"[::1]:56441", "[::1]:56432"}},
		{"任意自定义端口也展开", "10.0.0.9:9999", []string{"10.0.0.9:9999", "10.0.0.9:56432"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := lanDialCandidates(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("lanDialCandidates(%q) = %v, want %v", tc.in, got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("lanDialCandidates(%q)[%d] = %q, want %q", tc.in, i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestDialContextLANCandidateFallback 钉拨号循环的候选展开语义:首个候选
// (手填旧口)失败后,追加的统一口候选接续尝试并成功。
func TestDialContextLANCandidateFallback(t *testing.T) {
	fp := startFakePeer(t)
	orig := dialLANFn
	t.Cleanup(func() { dialLANFn = orig })

	var tried []string
	dialLANFn = func(_ context.Context, addr string) (net.Conn, error) {
		tried = append(tried, addr)
		if strings.HasSuffix(addr, ":56441") {
			return nil, fmt.Errorf("simulated old-port refused")
		}
		c, err := net.Dial("tcp", fp.ln.Addr().String())
		if err != nil {
			return nil, err
		}
		return c, nil
	}

	peer := NewPeer(PeerTarget{
		DeviceID: "dev-b",
		LANAddrs: []string{"127.0.0.1:56441"},
		SelfID:   "dev-a",
	})
	conn, err := peer.DialContext(context.Background(), &M.Metadata{HostName: "example.com", DstPort: 443})
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	defer conn.Close()

	want := []string{"127.0.0.1:56441", "127.0.0.1:56432"}
	if len(tried) != len(want) {
		t.Fatalf("tried = %v, want %v", tried, want)
	}
	for i := range want {
		if tried[i] != want[i] {
			t.Fatalf("tried[%d] = %q, want %q", i, tried[i], want[i])
		}
	}
	hdr := waitHeader(t, fp)
	if hdr == nil {
		t.Fatal("header must reach the peer via the appended candidate")
	}
}
