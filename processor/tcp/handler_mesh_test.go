package tcp

// dialByRoute toNode 分支与链头策略测试(design §3.3/§3.4/§9.4,计划 Task 7):
// mesh 拨出失败时链头就地执行「先兜底(customer.mesh.Fallback,类别勾选)
// → 再规则失败策略(direct/deny/fallback:<id2> 限一次)」;中间跳(继承帧头)
// 错误回源不落任何策略;custom 类别仅 LAN 载体;继承帧头 Via/Hops 续传上线。

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	M "aliang.one/nursorgate/inbound/tun/metadata"
	"aliang.one/nursorgate/outbound/meshproto"
	meshproxy "aliang.one/nursorgate/outbound/proxy/mesh"
	"aliang.one/nursorgate/processor/config"
)

// ---- 测试基建 ----

// newMeshDialTestEnv 隔离全局配置并注入 selfID 缝(dev-a)。seam 覆写+恢复,
// 非并行安全(与 geoipLookup 先例同款约定)。
func newMeshDialTestEnv(t *testing.T) {
	t.Helper()
	config.ResetGlobalConfigForTest()
	config.ResetRoutingApplyStoreForTest()
	t.Cleanup(func() {
		config.ResetGlobalConfigForTest()
		config.ResetRoutingApplyStoreForTest()
	})
	swapMeshSelfIDSource(t, func() string { return "dev-a" })
}

// swapMeshSelfIDSource/swapMeshPikoDialSource 覆写注入缝并恢复(质量评审
// Important 2 的 swap+Cleanup 模式):seam 为 atomic.Value,同包测试经
// Load 取原值、Set(nil) 清空归位;非并行安全(覆写+恢复,包内测试无
// t.Parallel)。
func swapMeshSelfIDSource(t *testing.T, fn func() string) {
	t.Helper()
	orig, _ := meshSelfIDSource.Load().(func() string)
	SetMeshSelfIDSource(fn)
	t.Cleanup(func() { SetMeshSelfIDSource(orig) })
}

func swapMeshPikoDialSource(t *testing.T, fn func() meshproxy.PikoDialFunc) {
	t.Helper()
	orig, _ := meshPikoDialSource.Load().(func() meshproxy.PikoDialFunc)
	SetMeshPikoDialSource(fn)
	t.Cleanup(func() { SetMeshPikoDialSource(orig) })
}

func setMeshDialConfig(t *testing.T, peers []config.MeshPeer, fallback *config.MeshFallbackConfig) {
	t.Helper()
	config.SetGlobalConfig(&config.Config{
		Customer: &config.CustomerConfig{
			Mesh: &config.MeshConfig{Peers: peers, Fallback: fallback},
		},
	})
}

// meshDialMetadata 构造直测 dialByRoute 的 metadata:Route 字段承载
// toNode 决策字符串(tls.go decideRouteWithRoutingEngine 的寄存形态)。
func meshDialMetadata(decision, directAddr string) *M.Metadata {
	host, portStr, _ := net.SplitHostPort(directAddr)
	port, _ := strconv.ParseUint(portStr, 10, 16)
	return &M.Metadata{
		Network: M.TCP,
		ConnID:  "mesh-dial-test",
		DstIP:   netip.MustParseAddr(host),
		DstPort: uint16(port),
		Route:   decision,
	}
}

// startAcceptedListener 起一个仅记录 accept 的 loopback 监听(直连/兜底/mesh
// 落点断言;不读载荷,帧头写进内核缓冲即可)。
func startAcceptedListener(t *testing.T) (string, chan net.Conn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	accepted := make(chan net.Conn, 4)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			accepted <- conn
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return ln.Addr().String(), accepted
}

// startMeshHeaderPeer 起一个读首行 JSON 帧头的 fake mesh 对端(Via/Hops 续传
// 断言);读出头后保持连接排水直至对端关闭。
func startMeshHeaderPeer(t *testing.T) (string, chan *meshproto.Header) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	headers := make(chan *meshproto.Header, 4)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				line, err := bufio.NewReader(c).ReadString('\n')
				if err != nil {
					return
				}
				hdr, _, err := meshproto.Parse([]byte(line))
				if err != nil {
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

// closedMeshAddr 返回「监听后立即关闭」的回环地址,拨它稳定 connection refused。
func closedMeshAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

func expectAccepted(t *testing.T, accepted chan net.Conn, what string) {
	t.Helper()
	select {
	case conn := <-accepted:
		_ = conn.Close()
	case <-time.After(2 * time.Second):
		t.Fatalf("%s 未被拨(2s 超时)", what)
	}
}

func expectNotAccepted(t *testing.T, accepted chan net.Conn, what string) {
	t.Helper()
	select {
	case conn := <-accepted:
		_ = conn.Close()
		t.Fatalf("%s 不应被拨", what)
	case <-time.After(300 * time.Millisecond):
	}
}

// ---- 用例 ----

// TestMeshSeamConcurrentSetAndRead 数据竞争钉死(质量评审 Critical 1):生产
// 写方(app/tunnel manager validateConfig,隧道每次续期重跑 Set)与 mesh
// 拨号 goroutine 读方跨包并发;缝必须原子化,本测试在 -race 下必须干净。
func TestMeshSeamConcurrentSetAndRead(t *testing.T) {
	config.ResetGlobalConfigForTest()
	t.Cleanup(func() {
		config.ResetGlobalConfigForTest()
		SetMeshSelfIDSource(nil)
		SetMeshPikoDialSource(nil)
	})
	setMeshDialConfig(t, []config.MeshPeer{{DeviceID: "dev-b"}}, nil)

	stop := make(chan struct{})
	var wg sync.WaitGroup

	// 写方:模拟隧道续期反复重设 selfID/PikoDial 提供器(含清空)。
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			SetMeshSelfIDSource(func() string { return fmt.Sprintf("dev-a-%d", i%3) })
			SetMeshPikoDialSource(func() meshproxy.PikoDialFunc {
				return func(ctx context.Context, endpointID string) (net.Conn, error) {
					return nil, fmt.Errorf("noop dial %s", endpointID)
				}
			})
		}
	}()

	// 读方:模拟 mesh 拨号 goroutine 经装配路径读两缝。
	handler := &TCPConnectionHandler{}
	deadline := time.Now().Add(300 * time.Millisecond)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for time.Now().Before(deadline) {
			if _, err := handler.getMeshProxyForExecution("dev-b"); err != nil {
				t.Errorf("getMeshProxyForExecution: %v", err)
				return
			}
		}
	}()

	// 等读方跑满窗口后再停写方,保证读写窗口重叠。
	time.Sleep(320 * time.Millisecond)
	close(stop)
	wg.Wait()
}

// TestDetermineRouteWithContext_MeshDecisionBecomesRouteToNode 决策→执行
// 映射(tls.go):mesh 规则命中返回 RouteToNode,decision 整串(含策略段)
// 寄存 metadata.Route 抵达 dialByRoute——非 toNode 决策不触碰 Route 寄存。
func TestDetermineRouteWithContext_MeshDecisionBecomesRouteToNode(t *testing.T) {
	newMeshDialTestEnv(t)
	config.SetGlobalConfig(&config.Config{
		Customer: &config.CustomerConfig{
			ProxyRules: []string{"domains,corp.example,mesh:dev-b,fallback:dev-c"},
			Mesh: &config.MeshConfig{Peers: []config.MeshPeer{
				{DeviceID: "dev-b"}, {DeviceID: "dev-c"},
			}},
		},
	})

	handler := NewDefaultTLSHandler()
	md := &M.Metadata{Network: M.TCP, HostName: "corp.example", DstPort: 443}
	route, requiresSNI := handler.DetermineRouteWithContext(md)
	if route != RouteToNode {
		t.Fatalf("route = %v, want RouteToNode", route)
	}
	if requiresSNI {
		t.Fatal("已有域名信息不应要求 SNI")
	}
	if want := "toNode:dev-b:fallback:dev-c"; md.Route != want {
		t.Fatalf("metadata.Route = %q, want %q(decision 整串寄存,策略段不得剥落)", md.Route, want)
	}

	// 非 toNode 决策(默认 direct)不残留 decision 寄存:Route 保持原值,
	// 由执行层按分支覆写。
	plain := &M.Metadata{Network: M.TCP, HostName: "unmatched.example", DstPort: 443}
	if route, _ := handler.DetermineRouteWithContext(plain); route != RouteDirect {
		t.Fatalf("route = %v, want RouteDirect(未命中)", route)
	}
	if plain.Route != "" {
		t.Fatalf("metadata.Route = %q, want 空(非 mesh 决策不寄存)", plain.Route)
	}
}

// TestDialByRouteMeshNodeUnreachableFallsBackDirect 策略缺省(=direct):
// mesh 拨出失败 → 兜底未配置 → direct 回落成功。
func TestDialByRouteMeshNodeUnreachableFallsBackDirect(t *testing.T) {
	newMeshDialTestEnv(t)
	directAddr, accepted := startAcceptedListener(t)
	setMeshDialConfig(t, []config.MeshPeer{{DeviceID: "dev-b", LANAddrs: []string{closedMeshAddr(t)}}}, nil)

	handler := &TCPConnectionHandler{}
	md := meshDialMetadata("toNode:dev-b", directAddr)
	conn, err := handler.dialByRoute(context.Background(), md, RouteToNode)
	if err != nil {
		t.Fatalf("dialByRoute() error = %v, want 缺省策略 mesh 失败后 direct 成功", err)
	}
	defer conn.Close()
	if md.Route != "RouteDirect" {
		t.Fatalf("metadata.Route = %q, want RouteDirect", md.Route)
	}
	expectAccepted(t, accepted, "direct 回落")

	// 回落 direct 也失败:终端错误,两段失败同报(质量整改 Minor ①)。
	mdBoth := meshDialMetadata("toNode:dev-b", closedMeshAddr(t))
	if _, err := handler.dialByRoute(context.Background(), mdBoth, RouteToNode); err == nil {
		t.Fatal("direct 回落也失败应报错")
	} else if !strings.Contains(err.Error(), "direct fallback also failed") {
		t.Fatalf("错误应标明 direct 回落也失败, got: %v", err)
	}
}

// TestDialByRouteMeshFailurePolicyDeny deny 策略:mesh 失败 → 拒绝连接,
// 不落 direct。不配兜底——兜底位于规则失败策略之前且会成功,deny 语义在
// 无兜底路径上才可达(兜底位次由 TestDialByRouteMeshFallbackNodeSecond 钉死)。
func TestDialByRouteMeshFailurePolicyDeny(t *testing.T) {
	newMeshDialTestEnv(t)
	directAddr, accepted := startAcceptedListener(t)
	setMeshDialConfig(t, []config.MeshPeer{{DeviceID: "dev-b", LANAddrs: []string{closedMeshAddr(t)}}}, nil)

	handler := &TCPConnectionHandler{}
	md := meshDialMetadata("toNode:dev-b:deny", directAddr)
	conn, err := handler.dialByRoute(context.Background(), md, RouteToNode)
	if err == nil {
		_ = conn.Close()
		t.Fatal("deny 策略:mesh 失败必须拒绝连接")
	}
	if !strings.Contains(err.Error(), "denied by failure policy") {
		t.Fatalf("错误应标明策略拒绝, got: %v", err)
	}
	expectNotAccepted(t, accepted, "direct(deny 不得回落)")
}

// TestDialByRouteMeshFallbackNodeSecond fallback:dev-c:先兜底节点(勾选类别)
// 再失败策略;未勾选类别直接走失败策略;兜底也不可达时落到失败策略。
func TestDialByRouteMeshFallbackNodeSecond(t *testing.T) {
	closed := func(t *testing.T) string { return closedMeshAddr(t) }

	t.Run("兜底勾选类别先于失败策略节点", func(t *testing.T) {
		newMeshDialTestEnv(t)
		fbAddr, fbAccepted := startAcceptedListener(t)
		cAddr, cAccepted := startAcceptedListener(t)
		setMeshDialConfig(t,
			[]config.MeshPeer{
				{DeviceID: "dev-b", LANAddrs: []string{closed(t)}},
				{DeviceID: "dev-c", LANAddrs: []string{cAddr}},
			},
			&config.MeshFallbackConfig{Enable: boolPtr(true), Addr: fbAddr, Classes: []string{meshproto.ClassCustom}},
		)

		handler := &TCPConnectionHandler{}
		// 回环直连地址 → custom 类别(非 56432 端口)被兜底勾选
		md := meshDialMetadata("toNode:dev-b:fallback:dev-c", closed(t))
		conn, err := handler.dialByRoute(context.Background(), md, RouteToNode)
		if err != nil {
			t.Fatalf("dialByRoute() error = %v, want 勾选类别先落兜底成功", err)
		}
		defer conn.Close()
		if md.Route != "RouteMeshFallback" {
			t.Fatalf("metadata.Route = %q, want RouteMeshFallback", md.Route)
		}
		expectAccepted(t, fbAccepted, "兜底节点")
		expectNotAccepted(t, cAccepted, "失败策略节点(兜底成功后不再执行)")
	})

	t.Run("未勾选类别直接走失败策略", func(t *testing.T) {
		newMeshDialTestEnv(t)
		fbAddr, fbAccepted := startAcceptedListener(t)
		cAddr, cAccepted := startAcceptedListener(t)
		setMeshDialConfig(t,
			[]config.MeshPeer{
				{DeviceID: "dev-b", LANAddrs: []string{closed(t)}},
				{DeviceID: "dev-c", LANAddrs: []string{cAddr}},
			},
			&config.MeshFallbackConfig{Enable: boolPtr(true), Addr: fbAddr, Classes: []string{meshproto.ClassAI}}, // 只勾 ai
		)

		handler := &TCPConnectionHandler{}
		md := meshDialMetadata("toNode:dev-b:fallback:dev-c", closed(t)) // custom 连接
		conn, err := handler.dialByRoute(context.Background(), md, RouteToNode)
		if err != nil {
			t.Fatalf("dialByRoute() error = %v, want 未勾选类别直接走失败策略节点", err)
		}
		defer conn.Close()
		if md.Route != "RouteToNode" {
			t.Fatalf("metadata.Route = %q, want RouteToNode(失败策略节点拨出)", md.Route)
		}
		expectAccepted(t, cAccepted, "失败策略节点 dev-c")
		expectNotAccepted(t, fbAccepted, "兜底节点(类别未勾选)")
	})

	t.Run("兜底不可达落到失败策略", func(t *testing.T) {
		newMeshDialTestEnv(t)
		cAddr, cAccepted := startAcceptedListener(t)
		setMeshDialConfig(t,
			[]config.MeshPeer{
				{DeviceID: "dev-b", LANAddrs: []string{closed(t)}},
				{DeviceID: "dev-c", LANAddrs: []string{cAddr}},
			},
			&config.MeshFallbackConfig{Enable: boolPtr(true), Addr: closed(t), Classes: []string{meshproto.ClassCustom}},
		)

		handler := &TCPConnectionHandler{}
		md := meshDialMetadata("toNode:dev-b:fallback:dev-c", closed(t))
		conn, err := handler.dialByRoute(context.Background(), md, RouteToNode)
		if err != nil {
			t.Fatalf("dialByRoute() error = %v, want 兜底失败后失败策略节点接管", err)
		}
		defer conn.Close()
		expectAccepted(t, cAccepted, "失败策略节点 dev-c")
	})

	t.Run("fallback 目标不在 peers 终局失败", func(t *testing.T) {
		// 编译期不校验 fallback:<id2> 是否列入 peers(仅非空校验,见
		// classifyEntryForCompile 注释);运行期拨不到指定节点由「限一次防链」
		// 收口:终端错误,不回落 direct、不再叠加兜底。
		newMeshDialTestEnv(t)
		directAddr, accepted := startAcceptedListener(t)
		setMeshDialConfig(t,
			[]config.MeshPeer{{DeviceID: "dev-b", LANAddrs: []string{closed(t)}}},
			&config.MeshFallbackConfig{Enable: boolPtr(true), Addr: closed(t), Classes: []string{meshproto.ClassCustom}},
		)

		handler := &TCPConnectionHandler{}
		md := meshDialMetadata("toNode:dev-b:fallback:dev-nope", directAddr)
		conn, err := handler.dialByRoute(context.Background(), md, RouteToNode)
		if err == nil {
			_ = conn.Close()
			t.Fatal("fallback 目标不存在必须终局失败(限一次,不再链式)")
		}
		if !strings.Contains(err.Error(), "fallback to dev-nope unavailable") {
			t.Fatalf("错误应标明 fallback 目标不可用, got: %v", err)
		}
		expectNotAccepted(t, accepted, "direct(fallback 失败不回落 direct)")
	})
}

// TestDialByRouteMeshRelayOnlyForAI custom 类别仅 LAN 载体:LAN 失败即失败,
// 中继(PikoDial)不被调用;ai 类别(56432 回环推断)LAN 失败后中继接管。
func TestDialByRouteMeshRelayOnlyForAI(t *testing.T) {
	t.Run("custom 类别不试中继", func(t *testing.T) {
		newMeshDialTestEnv(t)
		closed := closedMeshAddr(t)
		relayDialed := make(chan string, 1)
		swapMeshPikoDialSource(t, func() meshproxy.PikoDialFunc {
			return func(ctx context.Context, endpointID string) (net.Conn, error) {
				relayDialed <- endpointID
				return nil, fmt.Errorf("relay dialed for custom class: %s", endpointID)
			}
		})
		setMeshDialConfig(t, []config.MeshPeer{{DeviceID: "dev-b", LANAddrs: []string{closed}, Relay: boolPtr(true)}}, nil)

		handler := &TCPConnectionHandler{}
		// 外部地址 + 非 56432 端口 → 推断 custom;deny 策略隔离后续分支
		md := &M.Metadata{
			Network: M.TCP, ConnID: "mesh-relay-custom",
			DstIP: netip.MustParseAddr("1.2.3.4"), DstPort: 8443,
			Route: "toNode:dev-b:deny",
		}
		_, err := handler.dialByRoute(context.Background(), md, RouteToNode)
		if err == nil {
			t.Fatal("custom 类别 LAN 不可达即失败,应报错")
		}
		select {
		case ep := <-relayDialed:
			t.Fatalf("custom 类别不得试中继载体,却拨了 %s", ep)
		default:
		}
		if !strings.Contains(err.Error(), "no carrier") {
			t.Fatalf("错误应为载体不可达, got: %v", err)
		}
	})

	t.Run("ai 类别 LAN 失败后中继接管", func(t *testing.T) {
		newMeshDialTestEnv(t)
		closed := closedMeshAddr(t)
		relayAddr, relayAccepted := startAcceptedListener(t)
		swapMeshPikoDialSource(t, func() meshproxy.PikoDialFunc {
			return func(ctx context.Context, endpointID string) (net.Conn, error) {
				if endpointID != "dev-b.mesh" {
					return nil, fmt.Errorf("endpoint = %s, want dev-b.mesh", endpointID)
				}
				return net.Dial("tcp", relayAddr)
			}
		})
		setMeshDialConfig(t, []config.MeshPeer{{DeviceID: "dev-b", LANAddrs: []string{closed}, Relay: boolPtr(true)}}, nil)

		handler := &TCPConnectionHandler{}
		// 56432 回环 → 推断 ai → 中继载体准入
		md := &M.Metadata{
			Network: M.TCP, ConnID: "mesh-relay-ai",
			DstIP: netip.MustParseAddr("127.0.0.1"), DstPort: 56432,
			Route: "toNode:dev-b",
		}
		conn, err := handler.dialByRoute(context.Background(), md, RouteToNode)
		if err != nil {
			t.Fatalf("dialByRoute() error = %v, want ai 类别经中继成功", err)
		}
		defer conn.Close()
		if md.Route != "RouteToNode" {
			t.Fatalf("metadata.Route = %q, want RouteToNode", md.Route)
		}
		expectAccepted(t, relayAccepted, "中继载体")
	})
}

// TestDialByRouteInheritsViaForMiddleHop B 侧注入连接(ctx 带继承帧头)经
// mesh 拨出 → 线上帧 Via/Hops 续传(不重置、不追加),From 保留链头。
func TestDialByRouteInheritsViaForMiddleHop(t *testing.T) {
	newMeshDialTestEnv(t)
	cAddr, headers := startMeshHeaderPeer(t)
	setMeshDialConfig(t, []config.MeshPeer{{DeviceID: "dev-c", LANAddrs: []string{cAddr}}}, nil)

	inherited := &meshproto.Header{
		From:   "dev-a",
		Class:  meshproto.ClassCustom,
		Target: "c.test:443",
		Via:    []string{"dev-a", "dev-b"},
		Hops:   2,
	}
	ctx := meshproxy.WithInheritedHeader(context.Background(), inherited)

	handler := &TCPConnectionHandler{}
	md := &M.Metadata{
		Network: M.TCP, ConnID: "mesh-via-test",
		DstIP: netip.MustParseAddr("127.0.0.1"), DstPort: 443,
		Route: "toNode:dev-c",
	}
	conn, err := handler.dialByRoute(ctx, md, RouteToNode)
	if err != nil {
		t.Fatalf("dialByRoute() error = %v", err)
	}
	defer conn.Close()
	if md.Route != "RouteToNode" {
		t.Fatalf("metadata.Route = %q, want RouteToNode", md.Route)
	}

	select {
	case hdr := <-headers:
		if hdr.From != "dev-a" {
			t.Fatalf("From = %q, want dev-a(链头保留)", hdr.From)
		}
		if len(hdr.Via) != 2 || hdr.Via[0] != "dev-a" || hdr.Via[1] != "dev-b" {
			t.Fatalf("Via = %v, want [dev-a dev-b](继承续传)", hdr.Via)
		}
		if hdr.Hops != 2 {
			t.Fatalf("Hops = %d, want 2(单调递增不被重置)", hdr.Hops)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("mesh 出口未上线(fake peer 未收到帧头)")
	}
}

// TestDialByRouteMeshUnknownPeerFallsBackDirect decision 指向不存在 peer
// (运行期配置漂移)→ 视作拨出失败,按缺省策略 direct 处理。
func TestDialByRouteMeshUnknownPeerFallsBackDirect(t *testing.T) {
	newMeshDialTestEnv(t)
	directAddr, accepted := startAcceptedListener(t)
	setMeshDialConfig(t, []config.MeshPeer{{DeviceID: "dev-b", LANAddrs: []string{closedMeshAddr(t)}}}, nil)

	handler := &TCPConnectionHandler{}
	md := meshDialMetadata("toNode:dev-nope", directAddr)
	conn, err := handler.dialByRoute(context.Background(), md, RouteToNode)
	if err != nil {
		t.Fatalf("dialByRoute() error = %v, want 未知 peer 按缺省策略落 direct", err)
	}
	defer conn.Close()
	if md.Route != "RouteDirect" {
		t.Fatalf("metadata.Route = %q, want RouteDirect", md.Route)
	}
	expectAccepted(t, accepted, "direct 回落")
}

// TestMeshFallbackAddrForClassRespectsMasterSwitch tcp 兜底闸总开关穿透钉子
// (计划 Task 12 评审抓过的缺口):meshFallbackAddrForClass 原实现无
// EffectiveEnabled 检查——总开关显式 false 时兜底门必须关闭,统一抽取为
// config.MeshFallbackActiveForClass 后由助手保证。
func TestMeshFallbackAddrForClassRespectsMasterSwitch(t *testing.T) {
	newMeshDialTestEnv(t)
	config.SetGlobalConfig(&config.Config{
		Customer: &config.CustomerConfig{
			Mesh: &config.MeshConfig{
				Enable: boolPtr(false), // 总开关显式 false
				Fallback: &config.MeshFallbackConfig{
					Enable:  boolPtr(true),
					Addr:    "192.168.1.9:56440",
					Classes: []string{meshproto.ClassAI},
				},
			},
		},
	})
	// 56432 回环 → 推断 ai 且被勾选;唯总开关关。
	md := &M.Metadata{
		Network: M.TCP,
		DstIP:   netip.MustParseAddr("127.0.0.1"),
		DstPort: 56432,
	}
	if addr, ok := meshFallbackAddrForClass(context.Background(), md); ok {
		t.Fatalf("总开关显式 false 时兜底门必须关闭, got addr=%q", addr)
	}
}

// TestDialByRouteMiddleHopFailureBubblesWithoutPolicy 中间跳(继承帧头)mesh
// 拨出失败:错误回源,不落兜底/失败策略——design §3.3「B 的规则若再指向 C
// 而 C 失败,同样向上冒泡给 A」,否则流量会从中间节点位置出去;§9.4「兜底
// 仅由链头执行,中间跳不落兜底」。
func TestDialByRouteMiddleHopFailureBubblesWithoutPolicy(t *testing.T) {
	newMeshDialTestEnv(t)
	directAddr, accepted := startAcceptedListener(t)
	fbAddr, fbAccepted := startAcceptedListener(t)
	setMeshDialConfig(t,
		[]config.MeshPeer{{DeviceID: "dev-b", LANAddrs: []string{closedMeshAddr(t)}}},
		&config.MeshFallbackConfig{Enable: boolPtr(true), Addr: fbAddr, Classes: []string{meshproto.ClassCustom}},
	)

	inherited := &meshproto.Header{
		From:   "dev-a",
		Class:  meshproto.ClassCustom,
		Target: "b.test:443",
		Via:    []string{"dev-a"},
		Hops:   1,
	}
	ctx := meshproxy.WithInheritedHeader(context.Background(), inherited)

	handler := &TCPConnectionHandler{}
	// 缺省策略 direct + 兜底可用:若链头策略被错误执行,会拨 direct/兜底成功
	md := meshDialMetadata("toNode:dev-b", directAddr)
	conn, err := handler.dialByRoute(ctx, md, RouteToNode)
	if err == nil {
		_ = conn.Close()
		t.Fatal("中间跳 mesh 失败必须错误回源,不得执行链头策略")
	}
	if !strings.Contains(err.Error(), "middle hop") {
		t.Fatalf("错误应标明中间跳回源语义, got: %v", err)
	}
	expectNotAccepted(t, accepted, "direct(中间跳不落策略)")
	expectNotAccepted(t, fbAccepted, "兜底(中间跳不落兜底)")
}
