package tunnel

// mesh 生产装配接线测试(Task 14):egress 双缝(SetOfflineSignal/
// SetPeerSource)+ tcp 离线缝(SetMeshOfflineSignal)的同源同真假断言
// (Task 12 评审建议:把契约从注释升级为测试钉子)、Peers[0] 装配、
// LAN 载体+beacon 生命周期。

import (
	"context"
	"errors"
	"net"
	"strconv"
	"testing"
	"time"

	"aliang.one/nursorgate/app/mesh/lanbeacon"
	M "aliang.one/nursorgate/inbound/tun/metadata"
	"aliang.one/nursorgate/outbound/meshproto"
	meshproxy "aliang.one/nursorgate/outbound/proxy/mesh"
	"aliang.one/nursorgate/outbound/proxy/proto"
	"aliang.one/nursorgate/processor/config"
	"aliang.one/nursorgate/processor/egress"
	"aliang.one/nursorgate/processor/tcp"
)

// restoreMeshAssemblySeams validateConfig 现在写 egress/tcp 四条全局缝
// (selfID/pikoDial/offline×2/peerSource),触达装配面的测试结束后全部清空
// 归位(同 restoreMeshSelfIDSeam 的纪律:缝已原子化,归位并发安全)。
func restoreMeshAssemblySeams(t *testing.T) {
	t.Helper()
	restoreMeshSelfIDSeam(t)
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
// egress 读到当前全局配置 Peers[0] 装配的 Peer(设备号进 Base.Address);
// mesh 未配置/关/空 peers → nil(纯直连)。快照不缓存:换配即生效。
func TestEgressPeerSourceAssemblesFirstPeer(t *testing.T) {
	restoreMeshAssemblySeams(t)
	manager := NewManager(nil)
	testConfig := validTestConfig(t)
	if _, _, err := manager.validateConfig(testConfig); err != nil {
		t.Fatal(err)
	}

	restoreConfig := swapGlobalMeshConfig(t, &config.MeshConfig{
		Peers: []config.MeshPeer{{DeviceID: "dev-b", LANAddrs: []string{"192.168.1.20:56441"}}},
	})
	defer restoreConfig()

	peer := egress.PeerSourceForTest()
	if peer == nil {
		t.Fatal("配置 peers[0] 后 peer 源应返回 Peer")
	}
	if peer.Address != "dev-b" {
		t.Fatalf("peer.Address = %q, want dev-b", peer.Address)
	}

	// 换配(mesh 总开关显式关)即时生效:下一次读取返回 nil。
	restoreConfig2 := swapGlobalMeshConfig(t, &config.MeshConfig{
		Enable: boolPtr(false),
		Peers:  []config.MeshPeer{{DeviceID: "dev-b"}},
	})
	defer restoreConfig2()
	if peer := egress.PeerSourceForTest(); peer != nil {
		t.Fatalf("总开关关后 peer 源应返回 nil, got %+v", peer)
	}
}

// TestManagerEgressPeerAssemblyNilCases egressPeer 装配的 nil 分支单元钉:
// 未配置/空 peers/空白 device_id → nil。
func TestManagerEgressPeerAssemblyNilCases(t *testing.T) {
	restoreMeshAssemblySeams(t)
	manager := NewManager(nil)
	if _, _, err := manager.validateConfig(validTestConfig(t)); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		mesh *config.MeshConfig
	}{
		{"未配置(nil)", nil},
		{"空 peers", &config.MeshConfig{}},
		{"空白 device_id", &config.MeshConfig{Peers: []config.MeshPeer{{DeviceID: "  "}}}},
	}
	for _, tc := range cases {
		restore := swapGlobalMeshConfig(t, tc.mesh)
		if peer := manager.egressPeer(); peer != nil {
			t.Fatalf("%s: egressPeer = %+v, want nil", tc.name, peer)
		}
		restore()
	}
}

// TestManagerEgressPeerAssemblyFields 装配字段钉:DeviceID 进 Base.Address、
// 协议=mesh、LANAddrs 过滤空白项后拷贝、PikoDial 为 manager 的
// dialMeshEndpoint(proxy url 未配置时错误链含 errMeshRelayUnavailable——
// 中继载体「不可用」软失败契约,证明闭包确为 manager 侧接线而非空置)。
func TestManagerEgressPeerAssemblyFields(t *testing.T) {
	restoreMeshAssemblySeams(t)
	manager := NewManager(nil)
	testConfig := validTestConfig(t)
	if _, _, err := manager.validateConfig(testConfig); err != nil {
		t.Fatal(err)
	}

	// TEST-NET 地址:LAN 载体首轮拨号不可达(与本仓集成测试的避网段约定
	// 一致),随后中继载体分支触发软失败。
	restore := swapGlobalMeshConfig(t, &config.MeshConfig{
		Peers: []config.MeshPeer{{
			DeviceID: "dev-b",
			LANAddrs: []string{"203.0.113.5:56441", "  "},
		}},
	})
	defer restore()

	peer := manager.egressPeer()
	if peer == nil {
		t.Fatal("egressPeer = nil, want Peer")
	}
	if peer.Address != "dev-b" {
		t.Fatalf("Address = %q, want dev-b", peer.Address)
	}
	if peer.Protocol != proto.Mesh {
		t.Fatalf("Protocol = %q, want %q", peer.Protocol, proto.Mesh)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
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

// TestMeshLANAssemblyLifecycle LAN 载体+beacon 生命周期:总开关开 → LAN 监听
// 起口+beacon 注册 Default;关 → 端口归零+Default 清空;再开 → 重起(beacon
// Stop 后用全新实例 Start,避免改已启动实例字段)。
func TestMeshLANAssemblyLifecycle(t *testing.T) {
	t.Cleanup(StopMeshLANAssembly)
	StopMeshLANAssembly()

	deviceID := func() string { return "dev-lan-a" }
	interval := 15 * time.Millisecond
	beaconPort := pickFreeUDPForAssembly(t)
	startMeshLANAssembly(deviceID, interval, beaconPort)

	// 初始未配置 → 不启动。
	if got := MeshLANPort(); got != 0 {
		t.Fatalf("未配置 mesh 时 MeshLANPort = %d, want 0", got)
	}

	restore := swapGlobalMeshConfig(t, &config.MeshConfig{})
	defer restore()
	waitForMeshLAN(t, true)

	port := MeshLANPort()
	if port <= 0 {
		t.Fatal("mesh 开后 MeshLANPort 应 > 0")
	}
	if lanbeacon.Default() == nil {
		t.Fatal("mesh 开后 lanbeacon.Default 应非 nil")
	}
	conn, dialErr := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), 500*time.Millisecond)
	if dialErr != nil {
		t.Fatalf("MeshLANPort=%d 无 TCP 监听: %v", port, dialErr)
	}
	_ = conn.Close()

	// 关:下个 tick 收摊。
	restoreOff := swapGlobalMeshConfig(t, &config.MeshConfig{Enable: boolPtr(false)})
	waitForMeshLAN(t, false)
	if got := MeshLANPort(); got != 0 {
		t.Fatalf("总开关关后 MeshLANPort = %d, want 0", got)
	}
	if lanbeacon.Default() != nil {
		t.Fatal("总开关关后 lanbeacon.Default 应为 nil")
	}
	restoreOff() // 恢复为「开」

	// 再开:重起(全新 beacon 实例)。
	waitForMeshLAN(t, true)
	if MeshLANPort() <= 0 || lanbeacon.Default() == nil {
		t.Fatalf("再开后应重起: port=%d default=%v", MeshLANPort(), lanbeacon.Default())
	}
	StopMeshLANAssembly()
	if MeshLANPort() != 0 || lanbeacon.Default() != nil {
		t.Fatalf("Stop 后应收摊: port=%d default=%v", MeshLANPort(), lanbeacon.Default())
	}
}

// ===== 测试基建 =====

// swapGlobalMeshConfig 换配全局 customer.mesh(返回恢复函数;恢复测试外的
// 全局配置原样,避免抹掉其他用例的换配)。
func swapGlobalMeshConfig(t *testing.T, mesh *config.MeshConfig) func() {
	t.Helper()
	prev := config.GetGlobalConfig()
	config.SetGlobalConfig(&config.Config{Customer: &config.CustomerConfig{Mesh: mesh}})
	return func() { config.SetGlobalConfig(prev) }
}

func boolPtr(b bool) *bool { return &b }

// waitForMeshLAN 按装配期望态轮询(2s 上限):running 对应端口非零。
func waitForMeshLAN(t *testing.T, wantRunning bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		running := MeshLANPort() > 0
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
