// Package lanbeacon 实现 mesh LAN 发现数据面(design「LAN 发现(实施期
// 修正)」节/plan Task 10):UDP 全局广播自描述(device_id + mesh 监听端口
// + 证书指纹占位 + 时间戳),离线(无平台信令)可用,供仪表盘离线模式点选
// 后手填 customer.mesh.peers。
//
// 范围(56fd7d1 controller 裁决后的一期口径):只做发现数据面——LAN 载体
// 现为明文 + relay 帧头门禁(Task 6 已落地);cert_fingerprint 字段保留占位、
// 本期恒空串,relay TLS 化(自签证书+指纹钉扎)单列为追加任务 10b,届时
// 填充本字段。候选列表与 Mesh.Peers 配置不做自动同步(点选后手填,前端
// Task 14 职责)。
//
// 报文与时间契约:payload JSON snake_case;ts 为 unix 秒,仅用于超龄判定
// (>45s 丢弃,容忍对端时钟小幅超前)与对外展示;去重按 device_id,TTL
// 剪枝以本机接收时刻为准(map 语义天然满足「TTL 内重复只刷新、不产生
// 新记录」)。
package lanbeacon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"sort"
	"sync"
	"time"
)

const (
	// DefaultBeaconPort beacon 广播/监听共用 UDP 端口(一期常量,不做配置项)。
	DefaultBeaconPort = 56440
	// beaconPeriod 广播周期(design:3s)。
	beaconPeriod = 3 * time.Second
	// beaconWriteTimeout 广播写超时:短围栏,阻塞不得拖垮广播 goroutine。
	beaconWriteTimeout = time.Second
	// beaconMaxAge 报文 ts 最大年龄:超龄视为陈旧/重放,丢弃。
	beaconMaxAge = 45 * time.Second
	// beaconDedupTTL 去重窗口:窗口内重复广播只刷新 last_seen,过期移除。
	beaconDedupTTL = 15 * time.Second
	// beaconMaxCandidates 候选容量上限:防内网泛洪撑爆内存。
	beaconMaxCandidates = 64
	// beaconReadLimit 读缓冲上限:payload 为百字节级小 JSON,满缓冲即
	// 截断报文、JSON 必然解析失败,提前按畸形丢弃。
	beaconReadLimit = 512
)

// beaconPayload 广播报文。CertFingerprint 本期恒空(任务 10b 落 TLS 后填充),
// 字段保留以固定线上报文形状,避免后续加字段造成新旧舰队互不识别。
type beaconPayload struct {
	DeviceID        string `json:"device_id"`
	MeshPort        int    `json:"mesh_port"`
	CertFingerprint string `json:"cert_fingerprint"`
	TS              int64  `json:"ts"`
}

// LanPeerCandidate 一条 LAN 候选;GET /api/mesh/lan_peers 的 data.peers[]
// 与此同形(snake_case)。
type LanPeerCandidate struct {
	DeviceID        string `json:"device_id"`
	MeshPort        int    `json:"mesh_port"`
	CertFingerprint string `json:"cert_fingerprint"`
	LastSeenUnix    int64  `json:"last_seen_unix"`
}

// lanCandidate 内部候选态:导出形状(LanPeerCandidate)+ 本地接收时刻。
// TTL 剪枝以 lastArrival(本地单调精确时钟)为准,而非报文 ts——ts 是整秒
// 粒度,亚秒级 TTL 会把刚到的报文在插入瞬间判过期(实测踩坑);ts 只用于
// 超龄判定与对外展示(last_seen_unix)。
type lanCandidate struct {
	LanPeerCandidate
	lastArrival time.Time
}

// Beacon LAN 发现信标:同一端口上既广播自己(3s 周期)也收听邻居,候选
// map[device_id] 去重。Start/Stop 由组装层驱动(读 cfg.Customer.Mesh 的
// 钩子在组装层,本体力求零配置依赖);Stop 后可重新 Start(总开关往复)。
type Beacon struct {
	selfID   string
	meshPort int
	port     int

	// 时序旋钮:默认取常量;仅测试在 Start 前缩短(读写均在 b.mu 内,
	// Start 后改字段会与 goroutine 竞态)。
	period   time.Duration
	dedupTTL time.Duration
	maxAge   time.Duration

	mu         sync.Mutex
	started    bool
	listenConn *net.UDPConn
	sendConn   *net.UDPConn
	cancel     context.CancelFunc
	// bcastErrLogged(b.mu 保护)仅在错误态切换时打日志:受限环境(无广播
	// 权限/无外发接口)下每 3s 一条会刷屏,恢复成功时复位。
	bcastErrLogged bool
	candidates     map[string]*lanCandidate
}

// NewBeacon 构造信标。meshPort 为本机 relay 常驻 LAN 监听口端口(Task 6
// relay 的 listener 端口),由组装层注入——beacon 不自摸 relay 状态;
// broadcastPort 广播/监听共用,生产恒 DefaultBeaconPort。
func NewBeacon(selfDeviceID string, meshPort int, broadcastPort int) *Beacon {
	return &Beacon{
		selfID:     selfDeviceID,
		meshPort:   meshPort,
		port:       broadcastPort,
		period:     beaconPeriod,
		dedupTTL:   beaconDedupTTL,
		maxAge:     beaconMaxAge,
		candidates: make(map[string]*lanCandidate),
	}
}

var errBeaconAlreadyStarted = errors.New("lanbeacon: already started")

// Start 起广播与监听两个 goroutine,各自持有独立 conn(广播用临时源端口,
// 监听绑广播端口)。立即首播一次,发现延迟不依赖周期相位。重复 Start 报错
// 而非静默复用——半启动状态对外不可见。
func (b *Beacon) Start(ctx context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.started {
		return errBeaconAlreadyStarted
	}
	listenPacket, err := beaconListenConfig.ListenPacket(ctx, "udp4", fmt.Sprintf(":%d", b.port))
	if err != nil {
		return fmt.Errorf("lanbeacon: listen udp :%d: %w", b.port, err)
	}
	sendPacket, err := beaconListenConfig.ListenPacket(ctx, "udp4", ":0")
	if err != nil {
		_ = listenPacket.Close()
		return fmt.Errorf("lanbeacon: bind send socket: %w", err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	b.listenConn = listenPacket.(*net.UDPConn)
	b.sendConn = sendPacket.(*net.UDPConn)
	b.cancel = cancel
	b.started = true
	go b.broadcastLoop(runCtx)
	go b.readLoop(b.listenConn)
	// ctx 全生命周期收口:父 ctx 取消而调用方未调 Stop 时,监听 conn 由
	// 这里代为关闭,readLoop 不泄漏。Stop 幂等(mu 内 started 位防重入),
	// 本 watcher 与显式 Stop 任意先后皆收敛为同一次收摊。
	go func() {
		<-runCtx.Done()
		b.Stop()
	}()
	return nil
}

// Stop 停播停听(幂等):关 conn 令两个 goroutine 以读写错误自然退出。
// 已收候选不清场——TTL 内懒剪枝自然过期,对端视角即「本机下线」。
func (b *Beacon) Stop() {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.started {
		return
	}
	b.started = false
	if b.cancel != nil {
		b.cancel()
		b.cancel = nil
	}
	if b.listenConn != nil {
		_ = b.listenConn.Close()
		b.listenConn = nil
	}
	if b.sendConn != nil {
		_ = b.sendConn.Close()
		b.sendConn = nil
	}
}

// MeshPort 返回广播载荷里通告的本机承载口(装配层=统一口 56432)。只读
// 观测面:装配测试钉「广播端口=统一口」用,诊断面板可复用。
func (b *Beacon) MeshPort() int {
	return b.meshPort
}

// Candidates 返回未过期候选,按 last_seen 降序(平局按 device_id 稳定)。
// nil 接收者安全:未装配默认实例时返回空数组(peers:[] 而非 null)。
func (b *Beacon) Candidates() []LanPeerCandidate {
	if b == nil {
		return []LanPeerCandidate{}
	}
	b.mu.Lock()
	b.pruneLocked(time.Now())
	out := make([]LanPeerCandidate, 0, len(b.candidates))
	for _, c := range b.candidates {
		out = append(out, c.LanPeerCandidate)
	}
	b.mu.Unlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].LastSeenUnix != out[j].LastSeenUnix {
			return out[i].LastSeenUnix > out[j].LastSeenUnix
		}
		return out[i].DeviceID < out[j].DeviceID
	})
	return out
}

func (b *Beacon) broadcastLoop(ctx context.Context) {
	b.broadcastOnce()
	ticker := time.NewTicker(b.period)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			b.broadcastOnce()
		}
	}
}

func (b *Beacon) broadcastOnce() {
	b.mu.Lock()
	send, selfID, meshPort, port := b.sendConn, b.selfID, b.meshPort, b.port
	b.mu.Unlock()
	if send == nil {
		return
	}
	payload, err := json.Marshal(beaconPayload{
		DeviceID: selfID,
		MeshPort: meshPort,
		TS:       time.Now().Unix(),
	})
	if err != nil {
		return // 纯内值序列化,不可达
	}
	_ = send.SetWriteDeadline(time.Now().Add(beaconWriteTimeout))
	_, werr := send.WriteToUDP(payload, &net.UDPAddr{IP: net.IPv4bcast, Port: port})
	b.mu.Lock()
	if werr != nil {
		if !b.bcastErrLogged {
			b.bcastErrLogged = true
			log.Printf("[MESH-BEACON] broadcast to :%d failed (will retry silently): %v", port, werr)
		}
	} else {
		b.bcastErrLogged = false
	}
	b.mu.Unlock()
}

func (b *Beacon) readLoop(conn *net.UDPConn) {
	buf := make([]byte, beaconReadLimit)
	for {
		n, _, err := conn.ReadFromUDP(buf)
		if err != nil {
			return // Stop 关闭 conn 是唯一退出路径
		}
		if n <= 0 || n >= len(buf) {
			continue // 满缓冲=被截断的超限报文,按畸形丢弃
		}
		b.ingest(buf[:n], time.Now())
	}
}

// ingest 一条到达报文的入候选判定:非 JSON/缺字段/ts 超龄/自身广播 →
// 丢弃;通过则刷新或新建候选。测试直接调用以钉判定逻辑,不经 UDP。
func (b *Beacon) ingest(data []byte, now time.Time) {
	var p beaconPayload
	if err := json.Unmarshal(data, &p); err != nil {
		return
	}
	if p.DeviceID == "" || p.MeshPort <= 0 || p.MeshPort > 65535 {
		return // 缺字段/畸形端口:候选要供点选拨号,死端口无价值
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if p.DeviceID == b.selfID {
		return // 本机广播回环自见,忽略
	}
	age := now.Unix() - p.TS
	if age < 0 {
		age = 0 // 对端时钟小幅超前容忍,不因负龄误判超龄
	}
	if age > int64(b.maxAge/time.Second) {
		return
	}
	b.pruneLocked(now)
	entry := lanCandidate{
		LanPeerCandidate: LanPeerCandidate{
			DeviceID:        p.DeviceID,
			MeshPort:        p.MeshPort,
			CertFingerprint: p.CertFingerprint,
			LastSeenUnix:    p.TS,
		},
		lastArrival: now,
	}
	if cur, ok := b.candidates[p.DeviceID]; ok {
		// 乱序到达的旧报文不回退 last_seen(展示值跟随对端 ts)。
		if cur.LastSeenUnix > entry.LastSeenUnix {
			entry.LastSeenUnix = cur.LastSeenUnix
		}
	} else if len(b.candidates) >= beaconMaxCandidates {
		b.evictStalestLocked()
	}
	b.candidates[p.DeviceID] = &entry
}

// pruneLocked 惰性剪枝:距最近一次本机接收超过 dedupTTL 的候选移除(以
// 本地接收时刻为准,见 lanCandidate.lastArrival 注释)。在每次 ingest 与
// Candidates 读取时执行,不设独立清扫 goroutine——64 条上限下遍历成本
// 可忽略,少一条常驻 goroutine 就少一处 Stop 收敛路径。
func (b *Beacon) pruneLocked(now time.Time) {
	for id, c := range b.candidates {
		if now.After(c.lastArrival.Add(b.dedupTTL)) {
			delete(b.candidates, id)
		}
	}
}

func (b *Beacon) evictStalestLocked() {
	stalestID := ""
	var stalestArrival time.Time
	first := true
	for id, c := range b.candidates {
		if first || c.lastArrival.Before(stalestArrival) {
			stalestID, stalestArrival, first = id, c.lastArrival, false
		}
	}
	if stalestID != "" {
		delete(b.candidates, stalestID)
	}
}

// ---------------------------------------------------------------------------
// 进程级默认实例:handler(handlers.MeshHandler)与组装层(Task 14 生命
// 周期接线)之间的交接缝,同 processor/tcp GetHandler/SetHandler 先例。
// ---------------------------------------------------------------------------

var (
	defaultMu     sync.RWMutex
	defaultBeacon *Beacon
)

// Default 返回组装层注册的进程级 beacon;未装配返回 nil(Candidates 对
// nil 安全→空表)。
func Default() *Beacon {
	defaultMu.RLock()
	defer defaultMu.RUnlock()
	return defaultBeacon
}

// SetDefault 注册/清空默认实例。组装层约定:mesh 总开关开 → Start 后
// SetDefault(beacon);关 → Stop 后 SetDefault(nil)。
func SetDefault(b *Beacon) {
	defaultMu.Lock()
	defer defaultMu.Unlock()
	defaultBeacon = b
}
