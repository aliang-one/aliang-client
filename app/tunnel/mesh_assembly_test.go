package tunnel

// mesh 生产装配接线测试(Task 14):egress 双缝(SetOfflineSignal/
// SetPeerSource)+ tcp 离线缝(SetMeshOfflineSignal)的同源同真假断言
// (Task 12 评审建议:把契约从注释升级为测试钉子)、Peers[0] 装配、
// LAN 载体+beacon 生命周期。

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"net"
	"testing"
	"time"

	"aliang.one/nursorgate/app/mesh/lanbeacon"
	tuntun "aliang.one/nursorgate/inbound/tun/tunnel"
	M "aliang.one/nursorgate/inbound/tun/metadata"
	"aliang.one/nursorgate/outbound/meshproto"
	meshproxy "aliang.one/nursorgate/outbound/proxy/mesh"
	"aliang.one/nursorgate/processor/config"
	"aliang.one/nursorgate/processor/egress"
	"aliang.one/nursorgate/processor/routing"
	"aliang.one/nursorgate/processor/tcp"
	gossh "golang.org/x/crypto/ssh"
)

// restoreMeshAssemblySeams validateConfig 现在写 egress/tcp 全局缝
// (中继拨号/offline×2/peerSource),触达装配面的测试结束后全部清空
// 归位(同 restoreMeshTransportDialSeam 的纪律:缝已原子化,归位并发安全)。
func restoreMeshAssemblySeams(t *testing.T) {
	t.Helper()
	restoreMeshTransportDialSeam(t)
	t.Cleanup(func() {
		egress.SetOfflineSignal(nil)
		egress.SetPeerSource(nil)
		tcp.SetMeshOfflineSignal(nil)
	})
}

// TestOfflineSeamsShareManagerSource 同源同真假契约的测试钉子:validateConfig
// 装配后,egress.SetOfflineSignal 与 tcp.SetMeshOfflineSignal 必须随同一
// manager 隧道状态同步翻转——漏接任一缝或两缝取值分裂,本测试红。信号语义:
// true=平台不可达=当前无活跃隧道会话(failed/stopped/从未配置);connecting/
// connected/reconnecting 均算活跃(在线,保守不激活兜底)。
func TestOfflineSeamsShareManagerSource(t *testing.T) {
	restoreMeshAssemblySeams(t)
	manager := NewManager(nil)
	manager.run = func(ctx context.Context, _ runConfig, ready func()) error {
		ready()
		<-ctx.Done()
		return nil
	}

	// validateConfig(生产装配点,Configure 内同款调用)接线两缝。
	testConfig := validTestConfig(t)
	if _, _, err := manager.validateConfig(testConfig); err != nil {
		t.Fatal(err)
	}

	// 从未起会话:无活跃隧道 → 两缝同真(离线)。
	if !egress.OfflineSignalForTest() {
		t.Fatal("egress 离线信号 = false, want true(无活跃会话)")
	}
	if !tcp.MeshOfflineSignalForTest() {
		t.Fatal("tcp 离线信号 = false, want true(无活跃会话)")
	}

	// 会话建立:两缝同翻在线。
	if _, _, err := manager.Configure(testConfig); err != nil {
		t.Fatal(err)
	}
	if egress.OfflineSignalForTest() || tcp.MeshOfflineSignalForTest() {
		t.Fatalf("会话活跃期两缝应同步为在线: egress=%t tcp=%t",
			egress.OfflineSignalForTest(), tcp.MeshOfflineSignalForTest())
	}

	// Stop:两缝同翻离线。
	manager.Stop()
	if !egress.OfflineSignalForTest() || !tcp.MeshOfflineSignalForTest() {
		t.Fatalf("Stop 后两缝应同步为离线: egress=%t tcp=%t",
			egress.OfflineSignalForTest(), tcp.MeshOfflineSignalForTest())
	}
}

// TestEgressPeerSourceAssemblesFirstPeer 生产 peer 源装配:validateConfig 后
// egress 读到当前全局配置 Peers[0] 装配的 ExitClient(决策 #4:消费面走
// mesh.Dialer 接口,类型断言钉住具体形态);mesh 未配置/SSH 依赖降级/关/
// 空 peers → nil(纯直连)。快照不缓存:换配即生效。
func TestEgressPeerSourceAssemblesFirstPeer(t *testing.T) {
	restoreMeshAssemblySeams(t)
	manager := NewManager(nil)
	testConfig := validTestConfig(t)
	if _, _, err := manager.validateConfig(testConfig); err != nil {
		t.Fatal(err)
	}

	restoreConfig := swapGlobalMeshConfig(t, &config.MeshConfig{
		PrivateKey: meshAssemblyTestKeyPEM(t),
		Peers:      []config.MeshPeer{{DeviceID: "dev-b", LANAddrs: []string{"192.168.1.20:56432"}, HostKey: "SHA256:AAAA"}},
	})
	defer restoreConfig()

	peer := egress.PeerSourceForTest()
	if peer == nil {
		t.Fatal("配置 peers[0] 后 peer 源应返回 ExitClient")
	}
	if _, ok := peer.(*meshproxy.ExitClient); !ok {
		t.Fatalf("peer 类型 = %T, want *meshproxy.ExitClient(决策 #4 承载形态)", peer)
	}

	// 换配(mesh 总开关显式关)即时生效:下一次读取返回 nil。
	restoreConfig2 := swapGlobalMeshConfig(t, &config.MeshConfig{
		Enable:     boolPtr(false),
		PrivateKey: meshAssemblyTestKeyPEM(t),
		Peers:      []config.MeshPeer{{DeviceID: "dev-b", HostKey: "SHA256:AAAA"}},
	})
	defer restoreConfig2()
	if peer := egress.PeerSourceForTest(); peer != nil {
		t.Fatalf("总开关关后 peer 源应返回 nil, got %+v", peer)
	}
}

// TestManagerEgressPeerAssemblyNilCases egressPeer 装配的 nil 分支单元钉:
// 未配置/空 peers/缺身份私钥/空白 device_id → nil。
func TestManagerEgressPeerAssemblyNilCases(t *testing.T) {
	restoreMeshAssemblySeams(t)
	manager := NewManager(nil)
	if _, _, err := manager.validateConfig(validTestConfig(t)); err != nil {
		t.Fatal(err)
	}
	keyPEM := meshAssemblyTestKeyPEM(t)

	cases := []struct {
		name string
		mesh *config.MeshConfig
	}{
		{"未配置(nil)", nil},
		{"空 peers", &config.MeshConfig{PrivateKey: keyPEM}},
		{"缺身份私钥", &config.MeshConfig{Peers: []config.MeshPeer{{DeviceID: "dev-b", HostKey: "SHA256:AAAA"}}}},
		{"空白 device_id", &config.MeshConfig{PrivateKey: keyPEM, Peers: []config.MeshPeer{{DeviceID: "  "}}}},
	}
	for _, tc := range cases {
		restore := swapGlobalMeshConfig(t, tc.mesh)
		if peer := manager.egressPeer(); peer != nil {
			t.Fatalf("%s: egressPeer = %+v, want nil", tc.name, peer)
		}
		restore()
	}
}

// TestManagerEgressPeerAssemblyFields 装配字段钉:返回 *ExitClient(决策 #4)、
// LANAddrs 过滤空白项后拷贝、PikoDial 为 manager 的 dialMeshEndpoint
// (proxy url 未配置时错误链含 errMeshRelayUnavailable——中继载体「不可用」
// 软失败契约,证明闭包确为 manager 侧接线而非空置)。
func TestManagerEgressPeerAssemblyFields(t *testing.T) {
	restoreMeshAssemblySeams(t)
	manager := NewManager(nil)
	testConfig := validTestConfig(t)
	if _, _, err := manager.validateConfig(testConfig); err != nil {
		t.Fatal(err)
	}

	// LAN 首地址拨「监听即关」的回环端口:connection refused 即时失败(确定性,
	// 不依赖 TEST-NET 的路由环境差异——某些环境 TEST-NET 会连上黑洞直致 10s
	// 握手超时),随后中继载体分支触发软失败(self 类别中继准入)。
	closedLn, lnErr := net.Listen("tcp", "127.0.0.1:0")
	if lnErr != nil {
		t.Fatal(lnErr)
	}
	closedAddr := closedLn.Addr().String()
	_ = closedLn.Close()
	restore := swapGlobalMeshConfig(t, &config.MeshConfig{
		PrivateKey: meshAssemblyTestKeyPEM(t),
		Peers: []config.MeshPeer{{
			DeviceID: "dev-b",
			LANAddrs: []string{closedAddr, "  "},
			HostKey:  "SHA256:AAAA",
		}},
	})
	defer restore()

	peer, ok := manager.egressPeer().(*meshproxy.ExitClient)
	if !ok || peer == nil {
		t.Fatalf("egressPeer = %T, want *meshproxy.ExitClient", manager.egressPeer())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ctx = meshproxy.WithClass(ctx, meshproto.ClassSelf)
	_, err := peer.DialContext(ctx, &M.Metadata{HostName: "probe.invalid", DstPort: 443})
	if err == nil {
		t.Fatal("proxy url 未配置时拨号应失败")
	}
	if !errors.Is(err, errMeshRelayUnavailable) {
		t.Fatalf("错误链应含 errMeshRelayUnavailable(证明 PikoDial=manager 接线): %v", err)
	}
}

// TestMeshLANAssemblyLifecycle beacon 生命周期(LAN 帧接收面已收编统一口,
// spec §6.5,装配只剩发现候选面):总开关开 → beacon 注册 Default 且广播
// 统一口 56432;关 → Default 清空;再开 → 重起(beacon Stop 后用全新实例
// Start,避免改已启动实例字段)。MeshLANPort 是常量承载口,不再作运行态
// 观测(0=未装配旧语义随独立监听退役失效)。
func TestMeshLANAssemblyLifecycle(t *testing.T) {
	t.Cleanup(StopMeshLANAssembly)
	StopMeshLANAssembly()

	deviceID := func() string { return "dev-lan-a" }
	interval := 15 * time.Millisecond
	beaconPort := pickFreeUDPForAssembly(t)
	startMeshLANAssembly(deviceID, interval, beaconPort)

	// MeshLANPort 收敛为常量统一口:与装配态无关的静态拓扑事实。
	if got := MeshLANPort(); got != config.DefaultHTTPProxyPort {
		t.Fatalf("MeshLANPort = %d, want 统一口 %d", got, config.DefaultHTTPProxyPort)
	}

	// 初始未配置 → beacon 不启动。
	if lanbeacon.Default() != nil {
		t.Fatal("未配置 mesh 时 lanbeacon.Default 应为 nil")
	}

	restore := swapGlobalMeshConfig(t, &config.MeshConfig{})
	defer restore()
	waitForMeshLAN(t, true)

	beacon := lanbeacon.Default()
	if beacon == nil {
		t.Fatal("mesh 开后 lanbeacon.Default 应非 nil")
	}
	if got := beacon.MeshPort(); got != config.DefaultHTTPProxyPort {
		t.Fatalf("beacon 广播端口 = %d, want 统一口 %d", got, config.DefaultHTTPProxyPort)
	}

	// 关:下个 tick 收摊。
	restoreOff := swapGlobalMeshConfig(t, &config.MeshConfig{Enable: boolPtr(false)})
	waitForMeshLAN(t, false)
	if lanbeacon.Default() != nil {
		t.Fatal("总开关关后 lanbeacon.Default 应为 nil")
	}
	restoreOff() // 恢复为「开」

	// 再开:重起(全新 beacon 实例)。
	waitForMeshLAN(t, true)
	if lanbeacon.Default() == nil {
		t.Fatal("再开后应重起 beacon")
	}
	StopMeshLANAssembly()
	if lanbeacon.Default() != nil {
		t.Fatalf("Stop 后应收摊: default=%v", lanbeacon.Default())
	}
}

// ===== 测试基建 =====

// swapGlobalMeshConfig 换配全局 customer.mesh(返回恢复函数;恢复测试外的
// 全局配置原样,避免抹掉其他用例的换配)。SSH 入口显式开:egressPeer 的
// 生效门切到 MeshEffectivelyEnabled(SPEC §10.3)后,不补则所有换配被依赖
// 降级误吞;被测的关闸条件(总开关/peers/私钥)在各用例内自持。
func swapGlobalMeshConfig(t *testing.T, mesh *config.MeshConfig) func() {
	t.Helper()
	prev := config.GetGlobalConfig()
	config.SetGlobalConfig(&config.Config{Customer: &config.CustomerConfig{
		Mesh:       mesh,
		SSHIngress: &config.SSHIngressConfig{Enable: boolPtr(true)},
	}})
	return func() { config.SetGlobalConfig(prev) }
}

// meshAssemblyTestKeyPEM 生成一把真 ed25519 测试身份(ExitClient 构造期
// SignerFromPEM 解析,格式非法即装配失败;每用例新钥互不串味)。
func meshAssemblyTestKeyPEM(t *testing.T) string {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate ed25519: %v", err)
	}
	block, err := gossh.MarshalPrivateKey(priv, "mesh-assembly-test")
	if err != nil {
		t.Fatalf("marshal private key: %v", err)
	}
	return string(pem.EncodeToMemory(block))
}

func boolPtr(b bool) *bool { return &b }

// waitForMeshLAN 按装配期望态轮询(2s 上限):running 对应 beacon 注册态。
func waitForMeshLAN(t *testing.T, wantRunning bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		running := lanbeacon.Default() != nil
		if running == wantRunning {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("mesh LAN 装配未达到期望态 running=%t", wantRunning)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// pickFreeUDPForAssembly 选空闲 UDP 端口给测试 beacon(bind 竞态由 Start 失败
// 兜底路径收口,不重试)。
func pickFreeUDPForAssembly(t *testing.T) int {
	t.Helper()
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero})
	if err != nil {
		t.Fatalf("pick udp: %v", err)
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).Port
}

// meshDNSForwardPeerForTest DNS 代转发装配矩阵的全局配置脚手架:规则串 +
// peers → SetGlobalConfig + memo 重置(热更即时生效路径的断言前提)。
func meshDNSForwardPeerForTest(t *testing.T, proxyRules []string, privateKey string, peers ...config.MeshPeer) {
	t.Helper()
	config.ResetGlobalConfigForTest()
	t.Cleanup(config.ResetGlobalConfigForTest)
	config.SetGlobalConfig(&config.Config{
		Customer: &config.CustomerConfig{
			ProxyRules: proxyRules,
			SSHIngress: &config.SSHIngressConfig{Enable: boolPtr(true)},
			Mesh: &config.MeshConfig{
				PrivateKey: privateKey,
				Peers:      peers,
			},
		},
	})
	routing.ResetMeshDNSForwardMemo()
}

// TestMeshDNSForwardPeer DNS 代转发对端装配矩阵(SPEC §9 接线,2026-11-12):
// 规则驱动 + 载体可用性分层——relay-only peer 编译放行(meshDNSForwardTarget
// 不再因空 LANAddrs 关闭)但运行期 fail-open(TUN DNS 恒 ClassCustom、
// PikoTransport 拒 custom,激活后只会 SERVFAIL 风暴),绝不带病上岗。
func TestMeshDNSForwardPeer(t *testing.T) {
	dnsPeer := func(mutation func(*config.MeshPeer)) config.MeshPeer {
		p := config.MeshPeer{
			DeviceID: "dev-b",
			LANAddrs: []string{"192.168.1.20:56432"},
			HostKey:  "SHA256:test",
		}
		if mutation != nil {
			mutation(&p)
		}
		return p
	}

	t.Run("dns 规则+载体可用 peer → ExitClient", func(t *testing.T) {
		meshDNSForwardPeerForTest(t, []string{"dns,mesh:dev-b"},
			meshAssemblyTestKeyPEM(t), dnsPeer(nil))
		m := NewManager(nil)
		if m.meshDNSForwardPeer() == nil {
			t.Fatal("dns 规则 + 载体可用 peer 应装配出 ExitClient")
		}
	})

	t.Run("无 dns 规则 → nil fail-open", func(t *testing.T) {
		meshDNSForwardPeerForTest(t, []string{"domains,x.com"},
			meshAssemblyTestKeyPEM(t), dnsPeer(nil))
		m := NewManager(nil)
		if m.meshDNSForwardPeer() != nil {
			t.Fatal("无 dns 规则不应激活代转发(直连透传)")
		}
	})

	t.Run("relay-only peer → nil fail-open(载体分层)", func(t *testing.T) {
		// 编译放行(规则可见)但运行期不可达:custom 禁中继,激活后只会
		// SERVFAIL 风暴——装配层按载体可用性拦下。
		meshDNSForwardPeerForTest(t, []string{"dns,mesh:dev-b"},
			meshAssemblyTestKeyPEM(t), dnsPeer(func(p *config.MeshPeer) {
				p.LANAddrs = nil
				p.Relay = boolPtr(true)
			}))
		m := NewManager(nil)
		if m.meshDNSForwardPeer() != nil {
			t.Fatal("relay-only peer 无 LAN 载体,应 fail-open 不激活")
		}
	})

	t.Run("缺私钥 → nil", func(t *testing.T) {
		meshDNSForwardPeerForTest(t, []string{"dns,mesh:dev-b"}, "", dnsPeer(nil))
		m := NewManager(nil)
		if m.meshDNSForwardPeer() != nil {
			t.Fatal("缺节点身份私钥不应装配出口")
		}
	})

	t.Run("peer 缺钉扎 → 编译期能力闸拦下 → nil", func(t *testing.T) {
		// meshForwardAvailable 对未钉扎 peer 判能力不成立,dns 规则不编译。
		meshDNSForwardPeerForTest(t, []string{"dns,mesh:dev-b"},
			meshAssemblyTestKeyPEM(t), dnsPeer(func(p *config.MeshPeer) { p.HostKey = "" }))
		m := NewManager(nil)
		if m.meshDNSForwardPeer() != nil {
			t.Fatal("未钉扎 peer 的 dns 规则不应编译/激活")
		}
	})

	t.Run("接线冒烟:wireMeshAssemblySeams 后隧道缝走同一装配", func(t *testing.T) {
		meshDNSForwardPeerForTest(t, []string{"dns,mesh:dev-b"},
			meshAssemblyTestKeyPEM(t), dnsPeer(nil))
		m := NewManager(nil)
		m.wireMeshAssemblySeams()
		t.Cleanup(func() { tuntun.T().SetMeshDNSForwardPeerSource(nil) })
		if m.meshDNSForwardPeer() == nil {
			t.Fatal("接线后装配仍应可用")
		}
	})
}
