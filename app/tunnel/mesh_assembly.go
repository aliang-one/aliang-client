// mesh 生产装配(Task 14,design docs/mesh-forwarding-network-design.md):
// 本文件收口三条装配线——
//
//   - egress 兜底 peer 源(egress.SetPeerSource):从当前全局配置
//     customer.mesh.Peers[0] 装配 ExitClient(SPEC §5.1),未配置/总开关关/
//     SSH 依赖降级/空 peers/缺身份 → nil(egress 纯直连)。闭包读调用时刻
//     的配置,保存即生效,不缓存快照。
//   - 离线信号两缝(egress.SetOfflineSignal + tcp.SetMeshOfflineSignal):
//     同一 manager 隧道状态 getter(TunnelSessionActive)同位接线——同源同
//     真假契约(两缝注释互指,Task 12 评审钉死):漏接 tcp 缝 = AI 改道永久
//     静默休眠;两缝取值分裂 = 「自身流量经 B 兜底而 AI 流量仍撞死隧道」。
//   - LAN beacon 生命周期:mesh 总开关(层配置存在且 EffectiveEnabled)开 →
//     beacon Start 后 SetDefault(广播统一口 56432);关 → 收摊。LAN 帧接收
//     面已收编进 56432 统一入口(WireUnifiedIngressMeshDispatch 分派接线,
//     spec §6.5),本装配只剩发现候选面。config 无变更回调机制
//     (SetGlobalConfig 是 config 层咽喉,不能反向 import app 层),用低频
//     轮询对账——mesh 发现本就是最终一致(beacon 3s 周期广播),5s 反应延迟
//     无损语义。
//
// 进程归属:装配由 cmd/core.go(core 进程)驱动——mesh 数据面(统一口
// 分派注入的 processor/tcp 管线、56432 AI 出口、TUN)全部在 core 进程,
// user agent runtime 不装配(双进程同装会抢 56440 beacon 并广播双 beacon)。
package tunnel

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"

	"aliang.one/nursorgate/app/mesh/lanbeacon"
	meshproxy "aliang.one/nursorgate/outbound/proxy/mesh"
	"aliang.one/nursorgate/processor/config"
	"aliang.one/nursorgate/processor/egress"
	"aliang.one/nursorgate/processor/tcp"
)

// DefaultMeshLANPort 已退役:LAN 载体 TCP 监听收编进 56432 统一入口
// (spec §6.5),承载口见 MeshLANPort。原 56441 专用监听口不再存在。

// meshLANPollInterval 装配对账轮询周期(测试经 startMeshLANAssembly 缩短)。
const meshLANPollInterval = 5 * time.Second

// TunnelSessionActive 隧道状态 getter(egress/tcp 两缝同接的本体):当前是否
// 存在活跃隧道会话。connecting/connected/reconnecting 均算活跃(cancel 已
// 立、会话未终结);failed/stopped/从未配置 → false。离线信号语义
// (true=平台不可达)即 !TunnelSessionActive():connecting 期计在线是保守
// 取向——半建立的会话不触发兜底/改道翻搅,真失败会很快落到 failed 翻真。
// Configure 续期窗口(cancel 置 nil 到新 cancel 落位,最长 10s)内信号瞬时
// 翻真属预期——隧道确实不在,有界自愈。
func (m *Manager) TunnelSessionActive() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cancel != nil
}

// meshOfflineSignal 构造两缝共用的离线信号闭包:单一代码点物化「同源同
// 真假」契约,validateConfig 把同一闭包值分别交给 egress/tcp 两条 Set*。
func (m *Manager) meshOfflineSignal() func() bool {
	return func() bool { return !m.TunnelSessionActive() }
}

// egressPeer 从当前全局配置装配 egress 兜底 peer(Peers[0]):SSH 依赖降级
// 由 MeshEffectivelyEnabled 承担(SPEC §10.3);私钥缺失 → nil(构造期解析
// 即失败);钉扎缺失 → 出口仍成立但拨号恒拒(fail-closed,SPEC §10.4)。
// ExitClient 每次调用重建——离线兜底触发频率低,构造期解析私钥开销可接受
// (SPEC 计划决策 #1)。LANAddrs 逐项过滤空白后拷贝(不共享配置切片),
// Relay 缺省放行,PikoDial 取 manager 的 dialMeshEndpoint(每次拨号读当前
// proxy url/token)。
func (m *Manager) egressPeer() meshproxy.Dialer {
	cfg := config.GetGlobalConfig()
	if cfg == nil || cfg.Customer == nil || cfg.Customer.Mesh == nil {
		return nil
	}
	mesh := cfg.Customer.Mesh
	if !cfg.MeshEffectivelyEnabled() || len(mesh.Peers) == 0 {
		return nil
	}
	if strings.TrimSpace(mesh.PrivateKey) == "" {
		return nil
	}
	first := mesh.Peers[0]
	deviceID := strings.TrimSpace(first.DeviceID)
	if deviceID == "" {
		return nil
	}
	lanAddrs := make([]string, 0, len(first.LANAddrs))
	for _, addr := range first.LANAddrs {
		if addr = strings.TrimSpace(addr); addr != "" {
			lanAddrs = append(lanAddrs, addr)
		}
	}
	spec := meshproxy.PeerSpec{
		DeviceID: deviceID,
		LANAddrs: lanAddrs,
		HostKey:  strings.TrimSpace(first.HostKey),
		SSHUser:  "mesh",
		Relay:    first.Relay == nil || *first.Relay,
	}
	client, err := meshproxy.NewExitClient(spec, mesh.PrivateKey, mesh.KeyPassphrase, m.dialMeshEndpoint)
	if err != nil {
		log.Printf("[MESH-LAN] egress fallback peer %s unavailable: %v", deviceID, err)
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
// 帧接收面(统一口分派)不归本装配管——mesh 总开关关时的拒收由
// HandleMeshConn 门禁承担,这里只收发现候选面。
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
// SetMeshTransportDialSource 同位):egress 双缝 + tcp 离线缝。抽成单函数使
// 接线顺序与注释同处一页;每次 Configure(含幂等续期)重跑,atomic Store 幂等。
func (m *Manager) wireMeshAssemblySeams() {
	offline := m.meshOfflineSignal()
	egress.SetOfflineSignal(offline)
	tcp.SetMeshOfflineSignal(offline)
	egress.SetPeerSource(m.egressPeer)
}
