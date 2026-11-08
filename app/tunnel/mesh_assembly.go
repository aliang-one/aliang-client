// mesh 生产装配(Task 14,design docs/mesh-forwarding-network-design.md):
// 本文件收口三条装配线——
//
//   - egress 兜底 peer 源(egress.SetPeerSource):从当前全局配置
//     customer.mesh.Peers[0] 装配 mesh.Peer,未配置/总开关关/空 peers → nil
//     (egress 纯直连)。闭包读调用时刻的配置,保存即生效,不缓存快照。
//   - 离线信号两缝(egress.SetOfflineSignal + tcp.SetMeshOfflineSignal):
//     同一 manager 隧道状态 getter(TunnelSessionActive)同位接线——同源同
//     真假契约(两缝注释互指,Task 12 评审钉死):漏接 tcp 缝 = AI 改道永久
//     静默休眠;两缝取值分裂 = 「自身流量经 B 兜底而 AI 流量仍撞死隧道」。
//   - LAN 载体 + lanbeacon 生命周期:mesh 总开关(层配置存在且
//     EffectiveEnabled)开 → relay LAN TCP 常驻监听口起口 + beacon Start 后
//     SetDefault;关 → 双双收摊。config 无变更回调机制(SetGlobalConfig 是
//     config 层咽喉,不能反向 import app 层),用低频轮询对账——mesh 发现
//     本就是最终一致(beacon 3s 周期广播),5s 反应延迟无损语义。
//
// 进程归属:装配由 cmd/core.go(core 进程)驱动——mesh 数据面(relay 注入
// 的 processor/tcp 管线、56432 AI 出口、TUN)全部在 core 进程,user agent
// runtime 不装配(双进程同装会抢 56440/56441 端口并广播双 beacon)。
package tunnel

import (
	"context"
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
	"time"

	"aliang.one/nursorgate/app/mesh/lanbeacon"
	"aliang.one/nursorgate/app/mesh/relay"
	"aliang.one/nursorgate/outbound/meshproto"
	meshproxy "aliang.one/nursorgate/outbound/proxy/mesh"
	"aliang.one/nursorgate/processor/config"
	"aliang.one/nursorgate/processor/egress"
	"aliang.one/nursorgate/processor/tcp"
)

// DefaultMeshLANPort mesh LAN 载体 TCP 常驻监听口(与 UDP beacon 56440 同族
// 固定高位端口)。固定而非 :0:手填对端 LAN 地址(§9.4 兜底)须跨重启稳定;
// 被占时回落 :0 并 Warn——beacon 广播实际端口,发现路径不损,仅手填路径
// 降级(重启换端口)。
const DefaultMeshLANPort = 56441

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

// egressPeer 从当前全局配置装配 egress 兜底 peer(Peers[0]):LANAddrs 逐项
// 过滤空白后拷贝(不共享配置切片),Relay 缺省放行,PikoDial 取 manager 的
// dialMeshEndpoint(每次拨号读当前 proxy url/token),SelfID 取 manager 当前
// 会话的 device_id。未配置/总开关关/空 peers/空白 device_id → nil。
func (m *Manager) egressPeer() *meshproxy.Peer {
	cfg := config.GetGlobalConfig()
	if cfg == nil || cfg.Customer == nil || cfg.Customer.Mesh == nil {
		return nil
	}
	mesh := cfg.Customer.Mesh
	if !mesh.EffectiveEnabled() || len(mesh.Peers) == 0 {
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
	return meshproxy.NewPeer(meshproxy.PeerTarget{
		DeviceID: deviceID,
		LANAddrs: lanAddrs,
		Relay:    first.Relay == nil || *first.Relay,
		PikoDial: m.dialMeshEndpoint,
		MaxHops:  mesh.EffectiveMaxHops(),
		SelfID:   m.currentDeviceID(),
	})
}

// currentDeviceID manager 视角的本机 device_id(最近一次 validateConfig 写入
// 的 meshSelfDeviceID;device_id 是设备常驻身份,续期不变,Stop 不清——离线
// 兜底拨号恰发生在会话停止后)。
func (m *Manager) currentDeviceID() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.meshSelfDeviceID
}

// setMeshSelfDeviceID validateConfig 写入当前 device_id(meshDial 同款先例)。
func (m *Manager) setMeshSelfDeviceID(deviceID string) {
	m.mu.Lock()
	m.meshSelfDeviceID = strings.TrimSpace(deviceID)
	m.mu.Unlock()
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

	running  bool              // 当前生效态(mesh 总开关)
	listener net.Listener      // relay LAN TCP 监听口
	beacon   *lanbeacon.Beacon // Start 失败时为未启动实例(Stop 幂等安全)
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

// startMeshLANLocked 起口(meshLAN.mu 保护):LAN 监听 + relay serve + beacon。
// 任一环节失败保持 running=false,下个对账周期重试(端口被占属环境态,重试
// 有意义;beacon 失败不拖累监听口——发现候选不可用,转发接收照常)。
func startMeshLANLocked(deviceID func() string) {
	selfID := strings.TrimSpace(deviceID())
	listener, port, err := listenMeshLAN()
	if err != nil {
		log.Printf("[MESH-LAN] assembly start failed (will retry): %v", err)
		return
	}
	r := relay.New(relay.ProductionDeps(selfID))
	go func() { _ = r.Serve(listener, meshproto.CarrierLAN) }()
	beacon := lanbeacon.NewBeacon(selfID, port, meshLAN.beaconPort)
	if berr := beacon.Start(context.Background()); berr != nil {
		// 软失败:监听口保留(接收面可用),候选面缺失;SetDefault 不注册,
		// handler 视角即空表(合法态)。
		log.Printf("[MESH-LAN] beacon start failed (candidates unavailable): %v", berr)
	} else {
		lanbeacon.SetDefault(beacon)
	}
	meshLAN.listener = listener
	meshLAN.beacon = beacon
	meshLAN.running = true
	log.Printf("[MESH-LAN] assembly started lan_port=%d beacon_port=%d", port, meshLAN.beaconPort)
}

// teardownMeshLANLocked 收摊(meshLAN.mu 保护):beacon 先 Stop 再清 Default,
// listener 关闭令 relay accept 循环退出。
func teardownMeshLANLocked() {
	if meshLAN.beacon != nil {
		meshLAN.beacon.Stop()
		meshLAN.beacon = nil
	}
	lanbeacon.SetDefault(nil)
	if meshLAN.listener != nil {
		_ = meshLAN.listener.Close()
		meshLAN.listener = nil
	}
	wasRunning := meshLAN.running
	meshLAN.running = false
	if wasRunning {
		log.Printf("[MESH-LAN] assembly stopped")
	}
}

// listenMeshLAN 绑 LAN 监听口:先试默认固定口,被占回落 :0(取舍见
// DefaultMeshLANPort 注释)。
func listenMeshLAN() (net.Listener, int, error) {
	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", DefaultMeshLANPort))
	if err == nil {
		return listener, DefaultMeshLANPort, nil
	}
	log.Printf("[MESH-LAN] default port %d unavailable (%v); falling back to ephemeral (manual peer addrs lose stability)", DefaultMeshLANPort, err)
	listener, err = net.Listen("tcp", ":0")
	if err != nil {
		return nil, 0, err
	}
	return listener, listener.Addr().(*net.TCPAddr).Port, nil
}

// MeshLANPort 当前 mesh LAN 监听口端口(0=未装配/未启动);/api/mesh/status
// 消费(手填对端地址须知道本机端口)。
func MeshLANPort() int {
	meshLAN.mu.Lock()
	defer meshLAN.mu.Unlock()
	if meshLAN.listener == nil {
		return 0
	}
	return meshLAN.listener.Addr().(*net.TCPAddr).Port
}

// wireMeshAssemblySeams validateConfig 的装配接线点(与 SetMeshSelfIDSource/
// SetMeshPikoDialSource 同位):egress 双缝 + tcp 离线缝。抽成单函数使接线
// 顺序与注释同处一页;每次 Configure(含幂等续期)重跑,atomic Store 幂等。
func (m *Manager) wireMeshAssemblySeams() {
	offline := m.meshOfflineSignal()
	egress.SetOfflineSignal(offline)
	tcp.SetMeshOfflineSignal(offline)
	egress.SetPeerSource(m.egressPeer)
}
