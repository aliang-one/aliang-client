// mesh 生产装配(Task 14,design docs/mesh-forwarding-network-design.md):
// 本文件收口三条装配线——
//
//   - egress 委托 peer 源(egress.SetPeerSource):出口链路下一跳
//     (config.MeshDelegateNextHop:开关开 ∧ next_hop 指向已钉扎 peer)装配
//     ExitClient,不满足 → nil(egress 纯直连)。闭包读调用时刻的配置,
//     保存即生效,不缓存快照。
//   - tcp 本机节点标识缝(tcp.SetMeshSelfIDSource):via 防环链的追加与
//     判定要本机 device_id(与 inbound/ssh LocalNodeID 同源同值)。
//   - TUN DNS 代转发对端源(tunnel.T().SetMeshDNSForwardPeerSource):规则
//     驱动(routing.MeshDNSForwardTarget 10s memo 取首条启用 dns,mesh:<id>
//     规则),装配 ExitClient 交给 TUN 单例消费(2026-11-12 接线,SPEC §9)。
//     peer 无 LAN 地址(relay-only)→ nil fail-open 走本机 DNS(TUN DNS 恒
//     ClassCustom、PikoTransport 拒 custom——载体层不可达,绝不激活后
//     SERVFAIL 风暴)。
//   - LAN beacon 生命周期:mesh 总开关(层配置存在且 EffectiveEnabled)开 →
//     beacon Start 后 SetDefault(广播统一口 56432);关 → 收摊。帧协议
//     接收面已退役(SPEC §13.1),本装配只剩发现候选面。config 无变更回调机制
//     (SetGlobalConfig 是 config 层咽喉,不能反向 import app 层),用低频
//     轮询对账——mesh 发现本就是最终一致(beacon 3s 周期广播),5s 反应延迟
//     无损语义。
//
// 进程归属:装配由 cmd/core.go(core 进程)驱动——mesh 数据面(56432
// AI 出口、TUN、SSH 入口)全部在 core 进程,
// user agent runtime 不装配(双进程同装会抢 56440 beacon 并广播双 beacon)。
package tunnel

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"

	"aliang.one/nursorgate/app/mesh/lanbeacon"
	tuntun "aliang.one/nursorgate/inbound/tun/tunnel"
	meshproxy "aliang.one/nursorgate/outbound/proxy/mesh"
	"aliang.one/nursorgate/processor/config"
	"aliang.one/nursorgate/processor/egress"
	"aliang.one/nursorgate/processor/routing"
	"aliang.one/nursorgate/processor/tcp"
)

// DefaultMeshLANPort 已退役:LAN 载体 TCP 监听收编进 56432 统一入口
// (spec §6.5),承载口见 MeshLANPort。原 56441 专用监听口不再存在。

// meshLANPollInterval 装配对账轮询周期(测试经 startMeshLANAssembly 缩短)。
const meshLANPollInterval = 5 * time.Second

// egressPeer 从当前全局配置装配 egress 出口链路下一跳 peer:开关开且
// next_hop 指向已钉扎 peer(config.MeshDelegateNextHop,含 SSH 依赖降级与
// 总开关判定)才成立,否则 nil(egress 纯直连)。ExitClient 每次调用重建
// ——委托触发频率=平台调用粒度,构造期解析私钥开销可接受。LANAddrs 逐项
// 过滤空白后拷贝(不共享配置切片),Relay 缺省放行,PikoDial 取 manager 的
// dialMeshEndpoint(每次拨号读当前 proxy url/token)。
func (m *Manager) egressPeer() meshproxy.Dialer {
	cfg := config.GetGlobalConfig()
	peer, ok := cfg.MeshDelegateNextHop()
	if !ok {
		return nil
	}
	meshCfg := cfg.Customer.Mesh
	deviceID := strings.TrimSpace(peer.DeviceID)
	lanAddrs := make([]string, 0, len(peer.LANAddrs))
	for _, addr := range peer.LANAddrs {
		if addr = strings.TrimSpace(addr); addr != "" {
			lanAddrs = append(lanAddrs, addr)
		}
	}
	spec := meshproxy.PeerSpec{
		DeviceID: deviceID,
		LANAddrs: lanAddrs,
		HostKey:  strings.TrimSpace(peer.HostKey),
		SSHUser:  "mesh",
		Relay:    peer.Relay == nil || *peer.Relay,
	}
	client, err := meshproxy.NewExitClient(spec, meshCfg.PrivateKey, meshCfg.KeyPassphrase, m.dialMeshEndpoint)
	if err != nil {
		log.Printf("[MESH-LAN] egress delegate peer %s unavailable: %v", deviceID, err)
		return nil
	}
	return client
}

// meshDNSForwardPeer TUN UDP/53 DNS 代转发对端(规则驱动,SPEC §9/§6 层1):
// routing.MeshDNSForwardTarget(10s 原子 memo)取首条启用 dns,mesh:<id>
// 规则的 peer,装配 ExitClient 交给 tunnel.T() 消费(udp.go 门控:对端非 nil
// 才走 mesh)。构建频率=每 DNS 交换一次,但交换本身一次完整 SSH 握手(百毫
// 秒量级),构造期解析私钥可忽略——不做缓存,配置热更即时生效。
//
// 激活按载体可用性过滤(编译期/运行期分层):A3 放行 relay-only peer 进
// 编译(meshDNSForwardTargetFromSnapshot 不再因空 LANAddrs 关闭),但 TUN
// DNS 恒 ClassCustom、PikoTransport.Admit 拒 custom——relay-only peer 在
// 载体层不可达。peer 无 LAN 地址 → 返回 nil → fail-open 走本机 DNS,绝不
// 激活后让查询 SERVFAIL 风暴。ssh 关闭时规则本就不编译(design 降级链),
// 私钥/钉扎缺失由编译期能力闸(meshForwardAvailable)与构造失败双层兜底。
func (m *Manager) meshDNSForwardPeer() meshproxy.Dialer {
	deviceID, lanAddrs, ok := routing.MeshDNSForwardTarget()
	if !ok {
		return nil
	}
	// 载体可用性过滤:lanAddrs 空 = relay-only = custom 不可达,fail-open。
	if len(lanAddrs) == 0 {
		return nil
	}
	cfg := config.GetGlobalConfig()
	if cfg == nil || cfg.Customer == nil || cfg.Customer.Mesh == nil {
		return nil
	}
	mesh := cfg.Customer.Mesh
	if strings.TrimSpace(mesh.PrivateKey) == "" {
		return nil
	}
	var entry *config.MeshPeer
	for i := range mesh.Peers {
		if strings.TrimSpace(mesh.Peers[i].DeviceID) == deviceID {
			entry = &mesh.Peers[i]
			break
		}
	}
	if entry == nil {
		return nil // 编译→拨出窗口内 peer 被移除:fail-open
	}
	addrs := make([]string, 0, len(entry.LANAddrs))
	for _, addr := range entry.LANAddrs {
		if addr = strings.TrimSpace(addr); addr != "" {
			addrs = append(addrs, addr)
		}
	}
	spec := meshproxy.PeerSpec{
		DeviceID: deviceID,
		LANAddrs: addrs,
		HostKey:  strings.TrimSpace(entry.HostKey),
		SSHUser:  "mesh",
		Relay:    entry.Relay == nil || *entry.Relay,
	}
	client, err := meshproxy.NewExitClient(spec, mesh.PrivateKey, mesh.KeyPassphrase, m.dialMeshEndpoint)
	if err != nil {
		log.Printf("[MESH-LAN] dns forward peer %s unavailable: %v", deviceID, err)
		return nil
	}
	return client
}

// ---------------------------------------------------------------------------
// LAN 载体 + lanbeacon 生命周期装配(进程级单例)
// ---------------------------------------------------------------------------

var meshLAN struct {
	mu         sync.Mutex
	started    bool          // 装配器(轮询 goroutine)是否存活
	stop       chan struct{} // started 置位前写入,goroutine 启动后只读
	interval   time.Duration // 对账周期(生产 5s,测试缩短)
	beaconPort int           // beacon 广播端口(生产 DefaultBeaconPort)

	running bool              // 当前生效态(mesh 总开关)
	beacon  *lanbeacon.Beacon // Start 失败时为未启动实例(Stop 幂等安全)
}

// StartMeshLANAssembly 生产装配入口(core 进程 boot 调用;幂等)。deviceID
// 提供器解析常驻设备身份(identity 文件),调用时必须已可返回非空值。
func StartMeshLANAssembly(deviceID func() string) {
	startMeshLANAssembly(deviceID, meshLANPollInterval, lanbeacon.DefaultBeaconPort)
}

// startMeshLANAssembly 测试可缩周期/换 beacon 端口的装配本体。
func startMeshLANAssembly(deviceID func() string, interval time.Duration, beaconPort int) {
	meshLAN.mu.Lock()
	if meshLAN.started {
		meshLAN.mu.Unlock()
		return
	}
	meshLAN.started = true
	meshLAN.interval = interval
	meshLAN.beaconPort = beaconPort
	meshLAN.stop = make(chan struct{})
	stop := meshLAN.stop
	meshLAN.mu.Unlock()

	reconcileMeshLAN(deviceID)
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				reconcileMeshLAN(deviceID)
			case <-stop:
				return
			}
		}
	}()
}

// StopMeshLANAssembly 收摊装配器与全部生效资源(生产 shutdown/测试归位;
// 幂等)。
func StopMeshLANAssembly() {
	meshLAN.mu.Lock()
	defer meshLAN.mu.Unlock()
	if !meshLAN.started {
		return
	}
	meshLAN.started = false
	close(meshLAN.stop)
	meshLAN.stop = nil
	teardownMeshLANLocked()
}

// reconcileMeshLAN 对账期望态与生效态(轮询与首次装配共用)。
func reconcileMeshLAN(deviceID func() string) {
	desired := meshLANDesired(deviceID)
	meshLAN.mu.Lock()
	defer meshLAN.mu.Unlock()
	if desired == meshLAN.running {
		return
	}
	if desired {
		startMeshLANLocked(deviceID)
	} else {
		teardownMeshLANLocked()
	}
}

// meshLANDesired 期望态:mesh 层已配置且总开关开,且 deviceID 可得。
// Mesh==nil(未配置)= 整层不生效(types.go 契约),不监听不广播。
func meshLANDesired(deviceID func() string) bool {
	if deviceID == nil || strings.TrimSpace(deviceID()) == "" {
		return false
	}
	cfg := config.GetGlobalConfig()
	if cfg == nil || cfg.Customer == nil || cfg.Customer.Mesh == nil {
		return false
	}
	return cfg.Customer.Mesh.EffectiveEnabled()
}

// startMeshLANLocked 起口(meshLAN.mu 保护):beacon 广播统一口 56432 作为
// LAN 承载口(LAN 帧接收面已收编进统一入口分派,spec §6.5,本装配只剩发现
// 候选面)。beacon Start 失败保持 running=false,下个对账周期重试(UDP 端口
// 被占属环境态,重试有意义);帧接收不依赖 beacon——统一口分派独立生效。
func startMeshLANLocked(deviceID func() string) {
	selfID := strings.TrimSpace(deviceID())
	beacon := lanbeacon.NewBeacon(selfID, config.DefaultHTTPProxyPort, meshLAN.beaconPort)
	if berr := beacon.Start(context.Background()); berr != nil {
		log.Printf("[MESH-LAN] assembly start failed (candidates unavailable, will retry): %v", berr)
		return
	}
	lanbeacon.SetDefault(beacon)
	meshLAN.beacon = beacon
	meshLAN.running = true
	log.Printf("[MESH-LAN] assembly started (carrier=%d beacon_port=%d)", config.DefaultHTTPProxyPort, meshLAN.beaconPort)
}

// teardownMeshLANLocked 收摊(meshLAN.mu 保护):beacon 先 Stop 再清 Default。
// 本装配只收发现候选面——转发面走 SSH 入口(HandleIngressConn→form 矩阵,
// SPEC §8),其开关语义归入口自身,不归本装配管。
func teardownMeshLANLocked() {
	if meshLAN.beacon != nil {
		meshLAN.beacon.Stop()
		meshLAN.beacon = nil
	}
	lanbeacon.SetDefault(nil)
	wasRunning := meshLAN.running
	meshLAN.running = false
	if wasRunning {
		log.Printf("[MESH-LAN] assembly stopped")
	}
}

// MeshLANPort LAN 载体承载口(常量统一口 56432);/api/mesh/status 消费
// (手填对端地址须知道本机端口)。独立监听退役后承载口不再随装配态变化
// (0=未装配的旧语义随之失效):端口是静态拓扑事实,「此刻可不可达」由
// 代理运行态决定(D4),两者不再混在一个字段里。
func MeshLANPort() int {
	return config.DefaultHTTPProxyPort
}

// wireMeshAssemblySeams validateConfig 的装配接线点(与
// SetMeshTransportDialSource 同位):egress 委托 peer 源 + tcp 本机节点
// 标识缝 + TUN DNS 代转发对端。抽成单函数使接线顺序与注释同处一页;每次
// Configure(含幂等续期)重跑,atomic Store 幂等。
func (m *Manager) wireMeshAssemblySeams(deviceID string) {
	selfID := strings.TrimSpace(deviceID)
	tcp.SetMeshSelfIDSource(func() string { return selfID })
	egress.SetPeerSource(m.egressPeer)
	// TUN DNS 代转发(tunnel.T() 进程级单例,engine.go SetDialer 同款接法):
	// 规则驱动,fn 内部每调用读 memo 化的门控结果,重复接线幂等。
	tuntun.T().SetMeshDNSForwardPeerSource(m.meshDNSForwardPeer)
}
