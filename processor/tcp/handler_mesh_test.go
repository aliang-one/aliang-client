package tcp

// ExitClient 缓存与 mesh 拨出链头测试(SPEC §5.1/§7.2/§10.4):
//   - getMeshExitForExecution 缓存键命中/失效(改私钥/换钉扎/换 relay → 重建,
//     LANAddrs 顺序无关);
//   - dialViaMesh 链头失败策略(缺省 direct/deny/fallback:<id2> 限一次)与
//     兜底位次(先兜底后失败策略;兜底经 DialMeshAddr 走 SSH,须真 sshd);
//   - AI 流量离线改道(Task 12/design §2 行11/§9.4,判定在 route_override.go,
//     执行落点在本文件的 handleNonTLS 分支)。
//
// 注入连接防环守卫见 mesh_guard_test.go;单跳模型下不再有中间跳续传路径
// (B 注入连接被 MeshInjected 守卫拦截,SPEC §6.2),旧帧语义用例已随
// Peer 出口退役。

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	M "aliang.one/nursorgate/inbound/tun/metadata"
	"aliang.one/nursorgate/outbound"
	"aliang.one/nursorgate/outbound/meshproto"
	meshproxy "aliang.one/nursorgate/outbound/proxy/mesh"
	"aliang.one/nursorgate/processor/config"
	gossh "golang.org/x/crypto/ssh"
)

// ---- 测试基建 ----

// newMeshDialTestEnv 隔离全局配置,并给 mesh 层配一把真测试身份(ExitClient
// 构造期解析私钥,缺私钥 = 出口不可用)。seam/配置覆写+恢复,非并行安全
// (与 geoipLookup 先例同款约定)。
func newMeshDialTestEnv(t *testing.T) string {
	t.Helper()
	config.ResetGlobalConfigForTest()
	config.ResetRoutingApplyStoreForTest()
	t.Cleanup(func() {
		config.ResetGlobalConfigForTest()
		config.ResetRoutingApplyStoreForTest()
	})
	return testMeshIdentityPEM(t)
}

// testMeshIdentityPEM 生成一把真 ed25519 测试身份的 PEM(ExitClient 构造期
// SignerFromPEM 消费;与 outbound/proxy/mesh sshexit_test.testIdentity 同款
// 配方,包间不共享测试夹具)。
func testMeshIdentityPEM(t *testing.T) string {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate ed25519: %v", err)
	}
	block, err := gossh.MarshalPrivateKey(priv, "mesh-test-identity")
	if err != nil {
		t.Fatalf("marshal private key: %v", err)
	}
	return string(pem.EncodeToMemory(block))
}

// swapMeshTransportDialSource 覆写 piko 中继拨号注入缝并恢复(质量评审
// Important 2 的 swap+Cleanup 模式):seam 为 atomic.Value,同包测试经
// Load 取原值、Set(nil) 清空归位;非并行安全。
func swapMeshTransportDialSource(t *testing.T, fn func() meshproxy.PikoDialFunc) {
	t.Helper()
	orig, _ := meshTransportDialSource.Load().(func() meshproxy.PikoDialFunc)
	SetMeshTransportDialSource(fn)
	t.Cleanup(func() { SetMeshTransportDialSource(orig) })
}

// setMeshDialConfig 隔离配置:mesh 层(peers/兜底,自动携带真身份私钥)+
// SSH 入口显式开(MeshEffectivelyEnabled 的依赖降级判据,不补则出口恒降级,
// 用例会被误吞)。
func setMeshDialConfig(t *testing.T, keyPEM string, peers []config.MeshPeer, fallback *config.MeshFallbackConfig) {
	t.Helper()
	config.SetGlobalConfig(&config.Config{
		Customer: &config.CustomerConfig{
			Mesh:       &config.MeshConfig{PrivateKey: keyPEM, Peers: peers, Fallback: fallback},
			SSHIngress: &config.SSHIngressConfig{Enable: boolPtr(true)},
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

// startAcceptedListener 起一个仅记录 accept 的 loopback 监听(direct 落点
// 断言;不读载荷,写入内核缓冲即可)。
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

// meshTestSSHD 本包自建的最小真 sshd(SPEC §15.1 出口测试夹具,与
// outbound/proxy/mesh sshexit_test 的同款配方):固定 mesh 身份公钥准入,
// 收到 mesh-exit channel 时记录 ExtraData 并执行 respond 行为(echo=回显,
// http=回一段最小 HTTP 200,hang=挂住不回包)。channel 类型与
// ChannelMeshExit 字面量写死互锁:实现侧改常量值时本 helper 必红。
type meshTestSSHD struct {
	ln     net.Listener
	hostFP string // host key SHA256 指纹(PeerSpec.HostKey 直用的钉扎格式)

	mu       sync.Mutex
	payloads [][]byte // 每条 mesh-exit channel 的 ExtraData(到达序)
	channels int      // mesh-exit channel 打开计数
}

func newMeshTestSSHD(t *testing.T, admittedPub gossh.PublicKey, respond string) *meshTestSSHD {
	t.Helper()
	_, hostPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate host key: %v", err)
	}
	hostSigner, err := gossh.NewSignerFromKey(hostPriv)
	if err != nil {
		t.Fatalf("new host signer: %v", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s := &meshTestSSHD{ln: ln, hostFP: gossh.FingerprintSHA256(hostSigner.PublicKey())}
	go func() {
		for {
			tcp, err := ln.Accept()
			if err != nil {
				return // listener 已被 t.Cleanup 关闭
			}
			go s.handle(tcp, hostSigner, admittedPub, respond)
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return s
}

func (s *meshTestSSHD) Addr() string { return s.ln.Addr().String() }

// HostKeyPinned 返回本 sshd host key 的 SHA256 指纹(SPEC §10.4 指纹格式)。
func (s *meshTestSSHD) HostKeyPinned() string { return s.hostFP }

// LastPayload 返回最近一条 mesh-exit ExtraData;尚无 channel 时返回 nil。
func (s *meshTestSSHD) LastPayload() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n := len(s.payloads); n > 0 {
		return s.payloads[n-1]
	}
	return nil
}

func (s *meshTestSSHD) record(payload []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.payloads = append(s.payloads, payload)
	s.channels++
}

func (s *meshTestSSHD) handle(tcp net.Conn, hostSigner gossh.Signer, admittedPub gossh.PublicKey, respond string) {
	sc := &gossh.ServerConfig{
		PublicKeyCallback: func(_ gossh.ConnMetadata, key gossh.PublicKey) (*gossh.Permissions, error) {
			if !bytes.Equal(key.Marshal(), admittedPub.Marshal()) {
				return nil, errors.New("mesh test sshd: unknown client identity key")
			}
			return nil, nil
		},
	}
	sc.AddHostKey(hostSigner)
	_, chans, reqs, err := gossh.NewServerConn(tcp, sc)
	if err != nil {
		_ = tcp.Close()
		return
	}
	go gossh.DiscardRequests(reqs)
	for newCh := range chans {
		if newCh.ChannelType() != "mesh-exit" {
			_ = newCh.Reject(gossh.UnknownChannelType, "unsupported")
			continue
		}
		// record 在 Accept 之前:OpenChannel 的确认由 Accept 发出,payload
		// 必然先于客户端 DialContext 返回被记录。
		s.record(newCh.ExtraData())
		ch, chReqs, err := newCh.Accept()
		if err != nil {
			continue
		}
		go gossh.DiscardRequests(chReqs)
		go func() {
			switch respond {
			case "echo":
				buf := make([]byte, 4096)
				for {
					n, err := ch.Read(buf)
					if n > 0 {
						_, _ = ch.Write(buf[:n])
					}
					if err != nil {
						break
					}
				}
			case "http":
				// 读一轮请求后回一段最小 HTTP 200(egress 式全链路:客户端拿到
				// 合法响应才算通)。SSH channel 无 deadline 语义(出口侧
				// exitConn 同款已知限制),读一轮即回,不等待 EOF。
				buf := make([]byte, 8192)
				if n, err := ch.Read(buf); err == nil || n > 0 {
					_, _ = ch.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nok"))
				}
			default: // "hang":收下 payload 挂住不回包,直到测试清理断链
				buf := make([]byte, 4096)
				for {
					if _, err := ch.Read(buf); err != nil {
						break
					}
				}
			}
			_ = ch.Close()
		}()
	}
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

// ---- ExitClient 缓存(SPEC §5.1/§10.4)----

// TestGetMeshExitCacheHitSameInstance 同配置重复装配必须命中缓存(同一
// *ExitClient 实例):构造期解析私钥/钉扎是一次性事实,SPEC §5.1「正确落点」
// 把缓存挂在 handler 实例上,包级变量会跨 ResetHandler 串味。
func TestGetMeshExitCacheHitSameInstance(t *testing.T) {
	keyPEM := newMeshDialTestEnv(t)
	setMeshDialConfig(t, keyPEM, []config.MeshPeer{
		{DeviceID: "dev-b", LANAddrs: []string{"192.168.1.20:56432"}, HostKey: "SHA256:AAAA"},
	}, nil)

	handler := NewTCPConnectionHandler(NewDefaultProtocolDetector(), NewDefaultTLSHandler(), nil, nil)
	first, err := handler.getMeshExitForExecution("dev-b")
	if err != nil {
		t.Fatalf("getMeshExitForExecution: %v", err)
	}
	second, err := handler.getMeshExitForExecution("dev-b")
	if err != nil {
		t.Fatalf("getMeshExitForExecution (2nd): %v", err)
	}
	if first != second {
		t.Fatal("同配置两次装配应命中缓存返回同一 *ExitClient 实例")
	}
}

// TestGetMeshExitCacheInvalidatesOnKeyRotate 改私钥必须重建(SPEC §10.4:
// 旧 ExitClient 缓存的 Signer 随私钥作废,摘要进键即自动失效);换 HostKey
// 钉扎/换 relay 开关同理(全部影响握手行为的配置值都在键里),LANAddrs
// 顺序无关(CacheKey 排序后拼接)。
func TestGetMeshExitCacheInvalidatesOnKeyRotate(t *testing.T) {
	keyPEM := newMeshDialTestEnv(t)
	peer := []config.MeshPeer{{DeviceID: "dev-b", LANAddrs: []string{"192.168.1.20:56432", "192.168.1.21:56432"}, HostKey: "SHA256:AAAA"}}
	setMeshDialConfig(t, keyPEM, peer, nil)

	handler := NewTCPConnectionHandler(NewDefaultProtocolDetector(), NewDefaultTLSHandler(), nil, nil)
	orig, err := handler.getMeshExitForExecution("dev-b")
	if err != nil {
		t.Fatalf("getMeshExitForExecution: %v", err)
	}

	t.Run("LANAddrs顺序无关", func(t *testing.T) {
		setMeshDialConfig(t, keyPEM, []config.MeshPeer{{DeviceID: "dev-b", LANAddrs: []string{"192.168.1.21:56432", "192.168.1.20:56432"}, HostKey: "SHA256:AAAA"}}, nil)
		got, err := handler.getMeshExitForExecution("dev-b")
		if err != nil {
			t.Fatalf("getMeshExitForExecution: %v", err)
		}
		if got != orig {
			t.Fatal("LANAddrs 仅顺序变化不应重建(键排序后拼接)")
		}
	})

	t.Run("换钉扎重建", func(t *testing.T) {
		setMeshDialConfig(t, keyPEM, []config.MeshPeer{{DeviceID: "dev-b", LANAddrs: peer[0].LANAddrs, HostKey: "SHA256:BBBB"}}, nil)
		got, err := handler.getMeshExitForExecution("dev-b")
		if err != nil {
			t.Fatalf("getMeshExitForExecution: %v", err)
		}
		if got == orig {
			t.Fatal("HostKey 变化应重建 ExitClient")
		}
		orig = got
	})

	t.Run("改私钥重建", func(t *testing.T) {
		newPEM := testMeshIdentityPEM(t)
		setMeshDialConfig(t, newPEM, peer, nil)
		got, err := handler.getMeshExitForExecution("dev-b")
		if err != nil {
			t.Fatalf("getMeshExitForExecution: %v", err)
		}
		if got == orig {
			t.Fatal("私钥轮换应重建 ExitClient(旧 Signer 随私钥作废)")
		}
	})
}

// TestGetMeshExitUnbuildableCases 装配兜错:未配置/依赖降级(SSH 入口关)/
// 缺私钥/peer 不存在 → 错误(编译期 meshForwardAvailable 已把关,此处兜住
// 决策→拨出窗口内的配置漂移)。
func TestGetMeshExitUnbuildableCases(t *testing.T) {
	keyPEM := newMeshDialTestEnv(t)
	peer := []config.MeshPeer{{DeviceID: "dev-b", LANAddrs: []string{"192.168.1.20:56432"}}}

	handler := NewTCPConnectionHandler(NewDefaultProtocolDetector(), NewDefaultTLSHandler(), nil, nil)

	t.Run("未配置mesh", func(t *testing.T) {
		config.SetGlobalConfig(&config.Config{})
		if _, err := handler.getMeshExitForExecution("dev-b"); err == nil {
			t.Fatal("mesh 未配置应报错")
		}
	})

	t.Run("SSH依赖降级", func(t *testing.T) {
		config.SetGlobalConfig(&config.Config{Customer: &config.CustomerConfig{
			Mesh: &config.MeshConfig{PrivateKey: keyPEM, Peers: peer},
		}})
		if _, err := handler.getMeshExitForExecution("dev-b"); err == nil {
			t.Fatal("SSH 入口未启用时出口应按依赖降级报错")
		}
	})

	t.Run("缺私钥", func(t *testing.T) {
		setMeshDialConfig(t, "   ", peer, nil)
		if _, err := handler.getMeshExitForExecution("dev-b"); err == nil || !strings.Contains(err.Error(), "private_key") {
			t.Fatalf("缺私钥应报错且指明 private_key, got: %v", err)
		}
	})

	t.Run("peer不存在", func(t *testing.T) {
		setMeshDialConfig(t, keyPEM, peer, nil)
		if _, err := handler.getMeshExitForExecution("dev-nope"); err == nil {
			t.Fatal("peer 不存在应报错")
		}
	})
}

// ---- 用例 ----

// TestMeshTransportDialSeamConcurrentSetAndRead 数据竞争钉死(质量评审
// Critical 1 先例):生产写方(app/tunnel manager validateConfig,隧道每次
// 续期重跑 Set)与 mesh 拨号 goroutine 读方跨包并发;缝必须原子化,本测试
// 在 -race 下必须干净。
func TestMeshTransportDialSeamConcurrentSetAndRead(t *testing.T) {
	keyPEM := newMeshDialTestEnv(t)
	setMeshDialConfig(t, keyPEM, []config.MeshPeer{{DeviceID: "dev-b", Relay: boolPtr(true)}}, nil)

	stop := make(chan struct{})
	var wg sync.WaitGroup

	// 写方:模拟隧道续期反复重设中继拨号提供器(含清空)。
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			SetMeshTransportDialSource(func() meshproxy.PikoDialFunc {
				return func(ctx context.Context, endpointID string) (net.Conn, error) {
					return nil, fmt.Errorf("noop dial %s", endpointID)
				}
			})
			if i%7 == 0 {
				SetMeshTransportDialSource(nil)
			}
		}
	}()

	// 读方:模拟 mesh 拨号 goroutine 经装配路径读缝+装配出口。
	handler := NewTCPConnectionHandler(NewDefaultProtocolDetector(), NewDefaultTLSHandler(), nil, nil)
	deadline := time.Now().Add(300 * time.Millisecond)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for time.Now().Before(deadline) {
			if _, err := handler.getMeshExitForExecution("dev-b"); err != nil {
				t.Errorf("getMeshExitForExecution: %v", err)
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
	keyPEM := newMeshDialTestEnv(t)
	config.SetGlobalConfig(&config.Config{
		Customer: &config.CustomerConfig{
			ProxyRules: []string{"domains,corp.example,mesh:dev-b,fallback:dev-c"},
			Mesh: &config.MeshConfig{PrivateKey: keyPEM, Peers: []config.MeshPeer{
				// 编译期能力闸(A3)要求目标 peer 已钉扎:未钉扎条目被静默跳过,
				// 决策落到 direct 而非 RouteToNode。
				{DeviceID: "dev-b", HostKey: "SHA256:AAAA"},
				{DeviceID: "dev-c", HostKey: "SHA256:BBBB"},
			}},
			SSHIngress: &config.SSHIngressConfig{Enable: boolPtr(true)},
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
// mesh 拨出失败(LAN 载体 connection refused)→ 兜底未配置 → direct 回落
// 成功。
func TestDialByRouteMeshNodeUnreachableFallsBackDirect(t *testing.T) {
	keyPEM := newMeshDialTestEnv(t)
	directAddr, accepted := startAcceptedListener(t)
	setMeshDialConfig(t, keyPEM, []config.MeshPeer{{DeviceID: "dev-b", LANAddrs: []string{closedMeshAddr(t)}}}, nil)

	handler := NewTCPConnectionHandler(NewDefaultProtocolDetector(), NewDefaultTLSHandler(), nil, nil)
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
	keyPEM := newMeshDialTestEnv(t)
	directAddr, accepted := startAcceptedListener(t)
	setMeshDialConfig(t, keyPEM, []config.MeshPeer{{DeviceID: "dev-b", LANAddrs: []string{closedMeshAddr(t)}}}, nil)

	handler := NewTCPConnectionHandler(NewDefaultProtocolDetector(), NewDefaultTLSHandler(), nil, nil)
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

// TestDialByRouteMeshFallbackNodeSecond fallback:dev-c:先兜底节点(勾选类别,
// 经 DialMeshAddr 走 SSH 到对端入口 56432)再失败策略;未勾选类别直接走失败
// 策略;兜底也不可达时落到失败策略。
func TestDialByRouteMeshFallbackNodeSecond(t *testing.T) {
	t.Run("兜底勾选类别先于失败策略节点", func(t *testing.T) {
		keyPEM := newMeshDialTestEnv(t)
		keySigner, err := gossh.ParsePrivateKey([]byte(keyPEM))
		if err != nil {
			t.Fatalf("parse identity: %v", err)
		}
		// 兜底与失败策略节点都是真 sshd:兜底(裸地址 DialMeshAddr)与
		// ExitClient(LAN 载体)的 SSH 握手都须走通。
		fb := newMeshTestSSHD(t, keySigner.PublicKey(), "hang")
		cNode := newMeshTestSSHD(t, keySigner.PublicKey(), "hang")
		setMeshDialConfig(t, keyPEM,
			[]config.MeshPeer{
				{DeviceID: "dev-b", LANAddrs: []string{closedMeshAddr(t)}},
				{DeviceID: "dev-c", LANAddrs: []string{cNode.Addr()}, HostKey: cNode.HostKeyPinned()},
			},
			&config.MeshFallbackConfig{Enable: boolPtr(true), Addr: fb.Addr(), HostKey: fb.HostKeyPinned(), Classes: []string{meshproto.ClassCustom}},
		)

		handler := NewTCPConnectionHandler(NewDefaultProtocolDetector(), NewDefaultTLSHandler(), nil, nil)
		// 回环直连地址 → custom 类别(非 56432 端口)被兜底勾选
		md := meshDialMetadata("toNode:dev-b:fallback:dev-c", closedMeshAddr(t))
		conn, err := handler.dialByRoute(context.Background(), md, RouteToNode)
		if err != nil {
			t.Fatalf("dialByRoute() error = %v, want 勾选类别先落兜底成功", err)
		}
		defer conn.Close()
		if md.Route != "RouteMeshFallback" {
			t.Fatalf("metadata.Route = %q, want RouteMeshFallback", md.Route)
		}
		// 兜底节点的 sshd 必须收到 mesh-exit channel(payload=目标四元组)。
		if fb.LastPayload() == nil {
			t.Fatal("兜底节点 sshd 未收到 mesh-exit channel")
		}
		if cNode.LastPayload() != nil {
			t.Fatal("失败策略节点不应被拨(兜底成功后不再执行)")
		}
	})

	t.Run("未勾选类别直接走失败策略", func(t *testing.T) {
		keyPEM := newMeshDialTestEnv(t)
		keySigner, err := gossh.ParsePrivateKey([]byte(keyPEM))
		if err != nil {
			t.Fatalf("parse identity: %v", err)
		}
		fb := newMeshTestSSHD(t, keySigner.PublicKey(), "hang")
		cNode := newMeshTestSSHD(t, keySigner.PublicKey(), "hang")
		setMeshDialConfig(t, keyPEM,
			[]config.MeshPeer{
				{DeviceID: "dev-b", LANAddrs: []string{closedMeshAddr(t)}},
				{DeviceID: "dev-c", LANAddrs: []string{cNode.Addr()}, HostKey: cNode.HostKeyPinned()},
			},
			&config.MeshFallbackConfig{Enable: boolPtr(true), Addr: fb.Addr(), HostKey: fb.HostKeyPinned(), Classes: []string{meshproto.ClassAI}}, // 只勾 ai
		)

		handler := NewTCPConnectionHandler(NewDefaultProtocolDetector(), NewDefaultTLSHandler(), nil, nil)
		md := meshDialMetadata("toNode:dev-b:fallback:dev-c", closedMeshAddr(t)) // custom 连接
		conn, err := handler.dialByRoute(context.Background(), md, RouteToNode)
		if err != nil {
			t.Fatalf("dialByRoute() error = %v, want 未勾选类别直接走失败策略节点", err)
		}
		defer conn.Close()
		if md.Route != "RouteToNode" {
			t.Fatalf("metadata.Route = %q, want RouteToNode(失败策略节点拨出)", md.Route)
		}
		if cNode.LastPayload() == nil {
			t.Fatal("失败策略节点 dev-c 未被拨")
		}
		if fb.LastPayload() != nil {
			t.Fatal("兜底节点不应被拨(类别未勾选)")
		}
	})

	t.Run("兜底不可达落到失败策略", func(t *testing.T) {
		keyPEM := newMeshDialTestEnv(t)
		keySigner, err := gossh.ParsePrivateKey([]byte(keyPEM))
		if err != nil {
			t.Fatalf("parse identity: %v", err)
		}
		cNode := newMeshTestSSHD(t, keySigner.PublicKey(), "hang")
		setMeshDialConfig(t, keyPEM,
			[]config.MeshPeer{
				{DeviceID: "dev-b", LANAddrs: []string{closedMeshAddr(t)}},
				{DeviceID: "dev-c", LANAddrs: []string{cNode.Addr()}, HostKey: cNode.HostKeyPinned()},
			},
			&config.MeshFallbackConfig{Enable: boolPtr(true), Addr: closedMeshAddr(t), HostKey: "SHA256:AAAA", Classes: []string{meshproto.ClassCustom}},
		)

		handler := NewTCPConnectionHandler(NewDefaultProtocolDetector(), NewDefaultTLSHandler(), nil, nil)
		md := meshDialMetadata("toNode:dev-b:fallback:dev-c", closedMeshAddr(t))
		conn, err := handler.dialByRoute(context.Background(), md, RouteToNode)
		if err != nil {
			t.Fatalf("dialByRoute() error = %v, want 兜底失败后失败策略节点接管", err)
		}
		defer conn.Close()
		if cNode.LastPayload() == nil {
			t.Fatal("失败策略节点 dev-c 未被拨")
		}
	})

	t.Run("fallback 目标不在 peers 终局失败", func(t *testing.T) {
		// 编译期不校验 fallback:<id2> 是否列入 peers(仅非空校验,见
		// classifyEntryForCompile 注释);运行期拨不到指定节点由「限一次防链」
		// 收口:终端错误,不回落 direct、不再叠加兜底。
		keyPEM := newMeshDialTestEnv(t)
		directAddr, accepted := startAcceptedListener(t)
		setMeshDialConfig(t, keyPEM,
			[]config.MeshPeer{{DeviceID: "dev-b", LANAddrs: []string{closedMeshAddr(t)}}},
			&config.MeshFallbackConfig{Enable: boolPtr(true), Addr: closedMeshAddr(t), HostKey: "SHA256:AAAA", Classes: []string{meshproto.ClassCustom}},
		)

		handler := NewTCPConnectionHandler(NewDefaultProtocolDetector(), NewDefaultTLSHandler(), nil, nil)
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

// TestDialByRouteMeshRelayOnlyForCustomLANNeg custom 类别仅 LAN 载体:LAN
// 失败即失败,中继(PikoTransport)不被调用(design §4 引擎层硬约束)。
func TestDialByRouteMeshRelayOnlyForCustomLANNeg(t *testing.T) {
	keyPEM := newMeshDialTestEnv(t)
	closed := closedMeshAddr(t)
	relayDialed := make(chan string, 1)
	swapMeshTransportDialSource(t, func() meshproxy.PikoDialFunc {
		return func(ctx context.Context, endpointID string) (net.Conn, error) {
			relayDialed <- endpointID
			return nil, fmt.Errorf("relay dialed for custom class: %s", endpointID)
		}
	})
	setMeshDialConfig(t, keyPEM, []config.MeshPeer{{DeviceID: "dev-b", LANAddrs: []string{closed}, Relay: boolPtr(true)}}, nil)

	handler := NewTCPConnectionHandler(NewDefaultProtocolDetector(), NewDefaultTLSHandler(), nil, nil)
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
	if !strings.Contains(err.Error(), "no SSH transport") {
		t.Fatalf("错误应为载体不可达, got: %v", err)
	}
}

// TestDialByRouteMeshRelayCarriesAI ai 类别(56432 回环推断)LAN 失败后中继
// 接管:pikoDial 收到 "<deviceID>.mesh" 端点名,拨到的裸流上完成真 SSH 握手
// 并打开 mesh-exit channel。
func TestDialByRouteMeshRelayCarriesAI(t *testing.T) {
	keyPEM := newMeshDialTestEnv(t)
	keySigner, err := gossh.ParsePrivateKey([]byte(keyPEM))
	if err != nil {
		t.Fatalf("parse identity: %v", err)
	}
	// 中继「对端」= 真 sshd:pikoDial 拨到的连接直接落在 sshd 上(单机集成:
	// 端点路由由 piko 承担的部分以直连 sshd 等价替代,握手/通道语义不变)。
	relaySSHD := newMeshTestSSHD(t, keySigner.PublicKey(), "hang")
	swapMeshTransportDialSource(t, func() meshproxy.PikoDialFunc {
		return func(ctx context.Context, endpointID string) (net.Conn, error) {
			if endpointID != "dev-b.mesh" {
				return nil, fmt.Errorf("endpoint = %s, want dev-b.mesh", endpointID)
			}
			return net.Dial("tcp", relaySSHD.Addr())
		}
	})
	setMeshDialConfig(t, keyPEM, []config.MeshPeer{{
		DeviceID: "dev-b",
		LANAddrs: []string{closedMeshAddr(t)},
		HostKey:  relaySSHD.HostKeyPinned(), // 中继载体同样按对端入口钉扎(SPEC §10.4)
		Relay:    boolPtr(true),
	}}, nil)

	handler := NewTCPConnectionHandler(NewDefaultProtocolDetector(), NewDefaultTLSHandler(), nil, nil)
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
	// 中继载体的 payload:56432 回环目标四元组。
	if got := relaySSHD.LastPayload(); got == nil || !bytes.Contains(got, []byte(`"p":56432`)) {
		t.Fatalf("中继载体 payload = %s, want 含 p:56432 的目标四元组", got)
	}
}

// TestDialByRouteMeshUnknownPeerFallsBackDirect decision 指向不存在 peer
// (运行期配置漂移)→ 视作拨出失败,按缺省策略 direct 处理。
func TestDialByRouteMeshUnknownPeerFallsBackDirect(t *testing.T) {
	keyPEM := newMeshDialTestEnv(t)
	directAddr, accepted := startAcceptedListener(t)
	setMeshDialConfig(t, keyPEM, []config.MeshPeer{{DeviceID: "dev-b", LANAddrs: []string{closedMeshAddr(t)}}}, nil)

	handler := NewTCPConnectionHandler(NewDefaultProtocolDetector(), NewDefaultTLSHandler(), nil, nil)
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

// ---- AI 流量离线改道(Task 12,design §2 行11/§9.4)----

// swapMeshOfflineSignal 覆写离线信号缝并恢复(swapMeshTransportDialSource
// 同款 swap+Cleanup 模式;非并行安全)。
func swapMeshOfflineSignal(t *testing.T, fn func() bool) {
	t.Helper()
	orig, _ := meshOfflineSignal.Load().(func() bool)
	SetMeshOfflineSignal(fn)
	t.Cleanup(func() { SetMeshOfflineSignal(orig) })
}

// registerFakeAliangProxy 注册 fake aliang 出口(在线/未勾选路径的
// RouteToALiang 拨出落点,route_override_test 同款)。
func registerFakeAliangProxy(t *testing.T) {
	t.Helper()
	registry := outbound.GetRegistry()
	registry.Clear()
	if err := registry.Register("aliang", &fakeAliangProxy{}); err != nil {
		t.Fatalf("register fake aliang proxy failed: %v", err)
	}
	t.Cleanup(registry.Clear)
}

// aiRedirectMetadata 构造 56432 回环 AI 流量 metadata(shouldForceAliangRoute
// 必命中形态)。
func aiRedirectMetadata() *M.Metadata {
	return &M.Metadata{
		Network: M.TCP,
		ConnID:  "ai-redirect-test",
		DstIP:   netip.MustParseAddr("127.0.0.1"),
		DstPort: 56432,
	}
}

// setAIRedirectFallbackConfig 配置「AI 类离线兜底勾选」的 fallback:兜底地址
// 指向真 sshd(离线改道经 DialMeshAddr 走 SSH,须钉扎+真握手)。
func setAIRedirectFallbackConfig(t *testing.T, keyPEM string, sshd *meshTestSSHD) {
	t.Helper()
	setMeshDialConfig(t, keyPEM, nil, &config.MeshFallbackConfig{
		Enable:  boolPtr(true),
		Addr:    sshd.Addr(),
		HostKey: sshd.HostKeyPinned(),
		Classes: []string{meshproto.ClassAI},
	})
}

// TestForceAliangRouteRedirectsToNodeWhenOffline 离线信号真 + AI 类兜底勾选 →
// shouldForceAliangRoute 命中改判 mesh 兜底节点(经 DialMeshAddr 走 SSH):
// 兜底 sshd 收到 mesh-exit channel,payload 携带 56432 回环目标;originConn
// 裸透传返回(不套 wrapAliangHTTPConnByProto——该改包逻辑只适用于 aliang
// 隧道);总开关显式 false 时改道关闭(门控统一助手的穿透钉子)。
func TestForceAliangRouteRedirectsToNodeWhenOffline(t *testing.T) {
	t.Run("离线改判mesh兜底节点", func(t *testing.T) {
		keyPEM := newMeshDialTestEnv(t)
		keySigner, err := gossh.ParsePrivateKey([]byte(keyPEM))
		if err != nil {
			t.Fatalf("parse identity: %v", err)
		}
		swapMeshOfflineSignal(t, func() bool { return true })
		fbSSHD := newMeshTestSSHD(t, keySigner.PublicKey(), "hang")
		setAIRedirectFallbackConfig(t, keyPEM, fbSSHD)

		if !MeshOfflineAIRedirectActive() {
			t.Fatal("离线+AI 兜底勾选时 MeshOfflineAIRedirectActive 应为真")
		}

		handler := NewTCPConnectionHandler(NewDefaultProtocolDetector(), NewDefaultTLSHandler(), nil, nil)
		origin := newFakeConn(nil)
		md := aiRedirectMetadata()
		remote, newOrigin, err := handler.handleNonTLS(context.Background(), origin, md)
		if err != nil {
			t.Fatalf("handleNonTLS failed: %v, want 离线改道拨兜底节点成功", err)
		}
		defer remote.Close()
		if md.Route != "RouteMeshFallback" {
			t.Fatalf("metadata.Route = %q, want RouteMeshFallback(改道至 toNode 家族的兜底节点)", md.Route)
		}
		if newOrigin != net.Conn(origin) {
			t.Fatalf("originConn 应裸透传返回(不套 wrapAliangHTTPConnByProto), got %T", newOrigin)
		}
		payload := fbSSHD.LastPayload()
		if payload == nil {
			t.Fatal("兜底节点 sshd 未收到 mesh-exit channel(2s 内)")
		}
		if !bytes.Contains(payload, []byte(`"h":"127.0.0.1"`)) || !bytes.Contains(payload, []byte(`"p":56432`)) {
			t.Fatalf("兜底 payload = %s, want 携带 127.0.0.1:56432 目标", payload)
		}
	})

	t.Run("总开关显式false改道关闭", func(t *testing.T) {
		keyPEM := newMeshDialTestEnv(t)
		keySigner, err := gossh.ParsePrivateKey([]byte(keyPEM))
		if err != nil {
			t.Fatalf("parse identity: %v", err)
		}
		swapMeshOfflineSignal(t, func() bool { return true })
		registerFakeAliangProxy(t)
		fbSSHD := newMeshTestSSHD(t, keySigner.PublicKey(), "hang")
		config.SetGlobalConfig(&config.Config{
			Customer: &config.CustomerConfig{
				Mesh: &config.MeshConfig{
					Enable:     boolPtr(false), // 总开关显式 false
					PrivateKey: keyPEM,
					Fallback: &config.MeshFallbackConfig{
						Enable:  boolPtr(true),
						Addr:    fbSSHD.Addr(),
						HostKey: fbSSHD.HostKeyPinned(),
						Classes: []string{meshproto.ClassAI},
					},
				},
				SSHIngress: &config.SSHIngressConfig{Enable: boolPtr(true)},
			},
		})

		if MeshOfflineAIRedirectActive() {
			t.Fatal("总开关显式 false 时改道判定必须关闭")
		}

		handler := NewTCPConnectionHandler(NewDefaultProtocolDetector(), NewDefaultTLSHandler(), nil, nil)
		md := aiRedirectMetadata()
		remote, _, err := handler.handleNonTLS(context.Background(), newFakeConn(nil), md)
		if err != nil {
			t.Fatalf("handleNonTLS failed: %v, want 保持原路由(RouteToALiang)", err)
		}
		defer remote.Close()
		if md.Route != "RouteToALiang" {
			t.Fatalf("metadata.Route = %q, want RouteToALiang(总开关关,改道不生效)", md.Route)
		}
		if fbSSHD.LastPayload() != nil {
			t.Fatal("总开关关时兜底节点不应被拨")
		}
	})

	t.Run("兜底不可达如实失败", func(t *testing.T) {
		keyPEM := newMeshDialTestEnv(t)
		swapMeshOfflineSignal(t, func() bool { return true })
		setMeshDialConfig(t, keyPEM, nil, &config.MeshFallbackConfig{
			Enable:  boolPtr(true),
			Addr:    closedMeshAddr(t),
			HostKey: "SHA256:AAAA",
			Classes: []string{meshproto.ClassAI},
		})

		handler := NewTCPConnectionHandler(NewDefaultProtocolDetector(), NewDefaultTLSHandler(), nil, nil)
		md := aiRedirectMetadata()
		_, _, err := handler.handleNonTLS(context.Background(), newFakeConn(nil), md)
		if err == nil {
			t.Fatal("兜底节点不可达时离线改道应如实失败")
		}
		if !strings.Contains(err.Error(), "offline AI redirect") {
			t.Fatalf("错误应标明离线改道语义, got: %v", err)
		}
	})
}

// TestForceAliangRouteUnchangedWhenOnline 在线行为零变化:信号 false / 未接线
// → 保持 RouteToALiang,兜底节点不被拨。
func TestForceAliangRouteUnchangedWhenOnline(t *testing.T) {
	t.Run("信号注入在线", func(t *testing.T) {
		keyPEM := newMeshDialTestEnv(t)
		keySigner, err := gossh.ParsePrivateKey([]byte(keyPEM))
		if err != nil {
			t.Fatalf("parse identity: %v", err)
		}
		swapMeshOfflineSignal(t, func() bool { return false })
		registerFakeAliangProxy(t)
		fbSSHD := newMeshTestSSHD(t, keySigner.PublicKey(), "hang")
		setAIRedirectFallbackConfig(t, keyPEM, fbSSHD)

		if MeshOfflineAIRedirectActive() {
			t.Fatal("在线时 MeshOfflineAIRedirectActive 应为假")
		}

		handler := NewTCPConnectionHandler(NewDefaultProtocolDetector(), NewDefaultTLSHandler(), nil, nil)
		md := aiRedirectMetadata()
		remote, _, err := handler.handleNonTLS(context.Background(), newFakeConn(nil), md)
		if err != nil {
			t.Fatalf("handleNonTLS failed: %v, want 在线保持 RouteToALiang 现状", err)
		}
		defer remote.Close()
		if md.Route != "RouteToALiang" {
			t.Fatalf("metadata.Route = %q, want RouteToALiang(在线零变化)", md.Route)
		}
		if fbSSHD.LastPayload() != nil {
			t.Fatal("在线时兜底节点不应被拨")
		}
	})

	t.Run("信号未接线视为在线", func(t *testing.T) {
		keyPEM := newMeshDialTestEnv(t)
		keySigner, err := gossh.ParsePrivateKey([]byte(keyPEM))
		if err != nil {
			t.Fatalf("parse identity: %v", err)
		}
		swapMeshOfflineSignal(t, nil) // nil = 未接线 = 在线保守
		registerFakeAliangProxy(t)
		fbSSHD := newMeshTestSSHD(t, keySigner.PublicKey(), "hang")
		setAIRedirectFallbackConfig(t, keyPEM, fbSSHD)

		if MeshOfflineAIRedirectActive() {
			t.Fatal("信号未接线(视为在线)时 MeshOfflineAIRedirectActive 应为假")
		}

		handler := NewTCPConnectionHandler(NewDefaultProtocolDetector(), NewDefaultTLSHandler(), nil, nil)
		md := aiRedirectMetadata()
		remote, _, err := handler.handleNonTLS(context.Background(), newFakeConn(nil), md)
		if err != nil {
			t.Fatalf("handleNonTLS failed: %v, want 未接线保持 RouteToALiang 现状", err)
		}
		defer remote.Close()
		if md.Route != "RouteToALiang" {
			t.Fatalf("metadata.Route = %q, want RouteToALiang(未接线零变化)", md.Route)
		}
		if fbSSHD.LastPayload() != nil {
			t.Fatal("未接线时兜底节点不应被拨")
		}
	})
}

// TestForceAliangRouteNoAIClassKeepsOriginal 离线但兜底 Classes 不含 ai →
// 保持原路由(RouteToALiang,离线下拨号失败如实上抛,由调用方/AI 客户端
// 重试策略收口)。
func TestForceAliangRouteNoAIClassKeepsOriginal(t *testing.T) {
	keyPEM := newMeshDialTestEnv(t)
	keySigner, err := gossh.ParsePrivateKey([]byte(keyPEM))
	if err != nil {
		t.Fatalf("parse identity: %v", err)
	}
	swapMeshOfflineSignal(t, func() bool { return true })
	registerFakeAliangProxy(t)
	fbSSHD := newMeshTestSSHD(t, keySigner.PublicKey(), "hang")
	setMeshDialConfig(t, keyPEM, nil, &config.MeshFallbackConfig{
		Enable:  boolPtr(true),
		Addr:    fbSSHD.Addr(),
		HostKey: fbSSHD.HostKeyPinned(),
		Classes: []string{meshproto.ClassCustom}, // 只勾 custom,不含 ai
	})

	if MeshOfflineAIRedirectActive() {
		t.Fatal("ai 类未勾选时 MeshOfflineAIRedirectActive 应为假")
	}

	handler := NewTCPConnectionHandler(NewDefaultProtocolDetector(), NewDefaultTLSHandler(), nil, nil)
	md := aiRedirectMetadata()
	remote, _, err := handler.handleNonTLS(context.Background(), newFakeConn(nil), md)
	if err != nil {
		t.Fatalf("handleNonTLS failed: %v, want AI 兜底未勾选保持 RouteToALiang", err)
	}
	defer remote.Close()
	if md.Route != "RouteToALiang" {
		t.Fatalf("metadata.Route = %q, want RouteToALiang(未勾选 ai 不改道)", md.Route)
	}
	if fbSSHD.LastPayload() != nil {
		t.Fatal("ai 未勾选时兜底节点不应被拨")
	}
}

// TestMeshOfflineSignalSeamConcurrentSetAndRead 离线信号缝并发契约
// (TestMeshTransportDialSeamConcurrentSetAndRead / egress
// TestSetPeerSourceConcurrentSetAndRead 同款):生产写方(组装层接线)与
// AI 拨号路径读方跨包并发,-race 下必须无竞争。
func TestMeshOfflineSignalSeamConcurrentSetAndRead(t *testing.T) {
	config.ResetGlobalConfigForTest()
	t.Cleanup(func() {
		config.ResetGlobalConfigForTest()
		SetMeshOfflineSignal(nil)
	})

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			v := i%2 == 0
			SetMeshOfflineSignal(func() bool { return v })
		}
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 2000; i++ {
			_ = meshOfflineActive()
			_ = MeshOfflineAIRedirectActive()
		}
	}()
	time.Sleep(50 * time.Millisecond)
	close(stop)
	wg.Wait()
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
			// SSH 入口显式开:判据切到 MeshEffectivelyEnabled 后,不补则本
			// 用例被依赖降级误吞而非测到总开关穿透。
			SSHIngress: &config.SSHIngressConfig{Enable: boolPtr(true)},
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
