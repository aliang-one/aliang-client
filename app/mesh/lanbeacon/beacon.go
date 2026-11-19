// Package lanbeacon 实现 mesh LAN 发现数据面(design「LAN 发现(实施期
// 修正)」节/plan Task 10):UDP 全局广播自描述(device_id + mesh 监听端口
// + host key 指纹 + 时间戳),离线(无平台信令)可用,供仪表盘离线模式点选
// 后手填 customer.mesh.peers。
//
// 范围(任务 10b 落地后的口径):发现数据面 + 安全钉扎原料——cert_fingerprint
// 携带本机 ssh_ingress host key 的 SHA256 指纹(装配层注入读取函数,未配置
// ssh_ingress 时恒空=「发现结果不可转为拨号凭据」的诚实态);候选捕获 UDP
// 来源 IP 组成可拨 addr。发现结果与 Mesh.Peers 配置仍不做自动同步——addr
// 增补(装配层)只在候选指纹与 peer 已钉扎 host key 精确相等时采纳,身份
// 绑定真闸在 SSH 握手的 host key 核验(钉扎 fail-closed)。
//
// 报文与时间契约:payload JSON snake_case;ts 为 unix 秒,仅用于超龄判定
// (>45s 丢弃,容忍对端时钟小幅超前)与对外展示;去重按 device_id,TTL
// 剪枝以本机接收时刻为准(map 语义天然满足「TTL 内重复只刷新、不产生
// 新记录」)。
package lanbeacon

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"aliang.one/nursorgate/common/logger"
	gossh "golang.org/x/crypto/ssh"
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

// beaconPayload 广播报文。CertFingerprint=本机 ssh_ingress host key 指纹
// (SHA256:…,装配层注入读取函数;ssh_ingress 未配置=空串,候选诚实降级
// 为「仅展示不可钉扎」),字段名保留历史形状避免新旧舰队互不识别。
// AccountHash=本机登录账号 ID 的 sha256 截断(AccountHashFromID;未登录/
// 旧版=空串——空串按旧版兼容收,非空且与己不等=跨账号丢弃,同账号自动
// 组网批次引入)。MeshPubKey=本机 mesh 身份公钥的 authorized_keys 行
// (type+b64,公钥本公开无泄密;空=未配置 mesh 身份/旧版)。Sig=MeshPubKey
// 对签名域(canonicalSignBytes)的 Ed25519 签名 b64——⚠️ 保证边界(评审
// major 诚实化):签名只防「无签名伪造降级/顶替」,不防「自签新钥顶替」
// (攻击者自有钥+嗅探账号哈希可签出合法 Sig)——信任锚缺带外权威,故自动
// 组网消费面被 mesh.auto_peers 显式开关关住(默认关),跨信任域绑定须平台
// roster(台账遗留);无 Sig=旧版兼容收(仅展示,不入自动组网)。
type beaconPayload struct {
	DeviceID        string `json:"device_id"`
	MeshPort        int    `json:"mesh_port"`
	CertFingerprint string `json:"cert_fingerprint"`
	AccountHash     string `json:"account_hash,omitempty"`
	MeshPubKey      string `json:"mesh_pub_key,omitempty"`
	Sig             string `json:"sig,omitempty"`
	TS              int64  `json:"ts"`
}

// BeaconSignFields 参与签名的载荷字段(canonicalSignBytes 同序;TS 进域防
// 重播:同公钥+同 ts 的报文签名相同属正常重播容忍,超龄闸另行兜底)。
type BeaconSignFields struct {
	DeviceID        string
	MeshPort        int
	CertFingerprint string
	AccountHash     string
	MeshPubKey      string
	TS              int64
}

func (p beaconPayload) signFields() BeaconSignFields {
	return BeaconSignFields{
		DeviceID:        p.DeviceID,
		MeshPort:        p.MeshPort,
		CertFingerprint: p.CertFingerprint,
		AccountHash:     p.AccountHash,
		MeshPubKey:      p.MeshPubKey,
		TS:              p.TS,
	}
}

// canonicalSignBytes 定长序拼接(NUL 分隔;int 用十进制)。签名域扩展须
// 同步 signFields 与此处——序列化形状即协议。
func canonicalSignBytes(f BeaconSignFields) []byte {
	return []byte(strings.Join([]string{
		"mesh-beacon-v1",
		f.DeviceID,
		strconv.Itoa(f.MeshPort),
		f.CertFingerprint,
		f.AccountHash,
		f.MeshPubKey,
		strconv.FormatInt(f.TS, 10),
	}, "\x00"))
}

// SignBeaconFields 以 mesh 身份私钥(PEM,Ed25519 锚定——512B 读限预算
// 内放得下签名)对广播域签名,返回签名 b64。
func SignBeaconFields(privateKeyPEM string, f BeaconSignFields) (string, error) {
	signer, err := gossh.ParsePrivateKey([]byte(privateKeyPEM))
	if err != nil {
		return "", fmt.Errorf("parse mesh identity key: %w", err)
	}
	sig, err := signer.Sign(rand.Reader, canonicalSignBytes(f))
	if err != nil {
		return "", fmt.Errorf("sign beacon: %w", err)
	}
	if sig.Format != gossh.KeyAlgoED25519 {
		return "", fmt.Errorf("mesh identity key must be Ed25519, got %q", sig.Format)
	}
	return base64.StdEncoding.EncodeToString(sig.Blob), nil
}

// verifyBeaconSignature 用广播内携带的公钥验签(公告完整性:绑定
// device_id+公钥,防真实身份被第三方伪造广播顶替;首触信任仍以握手
// fail-closed 兜底)。
func verifyBeaconSignature(pub gossh.PublicKey, f BeaconSignFields, sigB64 string) bool {
	blob, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil {
		return false
	}
	return pub.Verify(canonicalSignBytes(f), &gossh.Signature{Format: gossh.KeyAlgoED25519, Blob: blob}) == nil
}

// LanPeerCandidate 一条 LAN 候选;GET /api/mesh/lan_peers 的 data.peers[]
// 与此同形(snake_case)。Addr=UDP 来源 IP + 报文 mesh_port 组成的可拨地址
// (广播源端口是临时口,拨号端口恒取报文内 mesh_port);空=来源地址缺失
// (理论上仅测试直调 ingest 才会发生)。AccountHash=对端账号哈希(空=旧版
// 广播,未验证同账号;同账号判定见 ingest)。MeshPubKey=对端 mesh 公钥行
// (坏行仍收候选展示,但不算自动 peer 要素)。
type LanPeerCandidate struct {
	DeviceID        string `json:"device_id"`
	MeshPort        int    `json:"mesh_port"`
	Addr            string `json:"addr"`
	CertFingerprint string `json:"cert_fingerprint"`
	AccountHash     string `json:"account_hash,omitempty"`
	MeshPubKey      string `json:"mesh_pub_key,omitempty"`
	LastSeenUnix    int64  `json:"last_seen_unix"`
}

// lanCandidate 内部候选态:导出形状(LanPeerCandidate)+ 本地接收时刻。
// TTL 剪枝以 lastArrival(本地单调精确时钟)为准,而非报文 ts——ts 是整秒
// 粒度,亚秒级 TTL 会把刚到的报文在插入瞬间判过期(实测踩坑);ts 只用于
// 超龄判定与对外展示(last_seen_unix)。
type lanCandidate struct {
	LanPeerCandidate
	// pubKey 是 MeshPubKey 行的预解析公钥(插入时一次;nil=坏行/缺行,
	// 不算自动 peer 要素)。与 LanPeerCandidate 同生命周期,随 TTL 剪枝。
	pubKey gossh.PublicKey
	// sigVerified=广播携带 Sig 且以公告内公钥验签通过(明文 UDP 上的身份
	// 绑定:防真实 peer 条目被无签名伪造广播顶替)。自动组网晋升硬要素。
	sigVerified bool
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
	// fingerprintFn 广播指纹源(装配层注入,任务 10b):返回本机 ssh_ingress
	// host key 的 SHA256 指纹(「SHA256:…」)或空串。读盘级成本,广播周期
	// 3s 调用一次;nil = 恒空(诚实降级)。SetFingerprintProvider 须在 Start
	// 前(Set 后字段写入与 broadcastLoop 读并发,由 b.mu 保护读取侧)。
	fingerprintFn func() string
	// accountHashFn 同账号判定源(装配层注入):返回本机登录账号 ID 哈希
	// (AccountHashFromID 形态)或空串(未登录)。内存级读取,广播/ingest 各
	// 调用一次;nil = 恒空(未登录态,发现面维持旧版全收兼容)。Set 后字段
	// 写入与广播/ingest 读并发,由 b.mu 保护读取侧(同 fingerprintFn 纪律)。
	accountHashFn func() string
	// meshPubKeyFn 本机 mesh 身份公钥行源(装配层注入):返回 authorized_keys
	// 行(type+b64)或空串(未配置 mesh 身份)。读盘级成本同指纹,广播周期
	// 调用一次;nil = 恒空。
	meshPubKeyFn func() string
	// payloadSignerFn 广播签名源(装配层注入):以本机 mesh 身份私钥对签名域
	// 签名,返回 b64;空串/nil = 广播不带 Sig(旧版形状,自动组网不晋升)。
	// 签名在 assemblePayload 锁外计算(私钥解析+签名,读盘级成本)。
	payloadSignerFn func(BeaconSignFields) string
}

// SetFingerprintProvider 注入广播指纹源(任务 10b)。nil=清除(恒空)。
func (b *Beacon) SetFingerprintProvider(fn func() string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.fingerprintFn = fn
}

// SetAccountHashProvider 注入同账号判定源。nil=清除(恒空,全收兼容)。
func (b *Beacon) SetAccountHashProvider(fn func() string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.accountHashFn = fn
}

// SetMeshPubKeyProvider 注入本机 mesh 公钥行源。nil=清除(恒空)。
func (b *Beacon) SetMeshPubKeyProvider(fn func() string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.meshPubKeyFn = fn
}

// SetPayloadSigner 注入广播签名源。nil=清除(广播不带 Sig,旧版形状)。
func (b *Beacon) SetPayloadSigner(fn func(BeaconSignFields) string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.payloadSignerFn = fn
}

// AccountHashFromID 账号 ID → 候选同账号判定哈希(sha256 截断 16 hex)。
// 截断防明文账号经广播泄漏;16 hex=64bit,误撞概率在同子网设备量级下可忽。
func AccountHashFromID(id int64) string {
	if id == 0 {
		return ""
	}
	sum := sha256.Sum256([]byte(strconv.FormatInt(id, 10)))
	return hex.EncodeToString(sum[:8])
}

// ownAccountHash 取本机账号哈希(短临界区,provider 在锁外调用——provider
// 读 auth 内存态,但锁外调用与 fingerprint 同纪律,防未来变慢读拖住 ingest)。
func (b *Beacon) ownAccountHash() string {
	b.mu.Lock()
	fn := b.accountHashFn
	b.mu.Unlock()
	if fn == nil {
		return ""
	}
	return strings.TrimSpace(fn())
}

// InjectCandidateForTests 测试缝:绕过 UDP 时序直插候选(跨包测试用,
// beacon_test.go 同包直调 ingest)。sourceIP 与生产 readLoop 同语义——
// addr 由来源 IP+mesh_port 组成。
func (b *Beacon) InjectCandidateForTests(deviceID string, meshPort int, sourceIP, fingerprint string, ts time.Time) {
	data, err := json.Marshal(beaconPayload{
		DeviceID:        deviceID,
		MeshPort:        meshPort,
		CertFingerprint: fingerprint,
		TS:              ts.Unix(),
	})
	if err != nil {
		return
	}
	b.ingest(data, ts, sourceIP)
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

// Running beacon 是否处于运行态(Start 成功至 Stop/父 ctx 收摊之间为真)。
// /api/mesh/status 运行态真值消费(D2 假绿治理):这是配置推导字段之外的
// 唯一「活着」事实。nil 接收者对齐 Stop 的防御惯例。
func (b *Beacon) Running() bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.started
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

// assemblePayload 广播载荷装配(锁内读字段、锁外调 provider)——broadcastOnce
// 与测试缝共用同一取值路径,防双写漂移。
func (b *Beacon) assemblePayload() beaconPayload {
	b.mu.Lock()
	selfID, meshPort, fingerprintFn := b.selfID, b.meshPort, b.fingerprintFn
	accountHashFn := b.accountHashFn
	meshPubKeyFn := b.meshPubKeyFn
	payloadSignerFn := b.payloadSignerFn
	b.mu.Unlock()
	// 指纹读取(读盘)在锁外:3s 周期一次,失败/慢读不得拖住 Candidates 读侧。
	fingerprint := ""
	if fingerprintFn != nil {
		fingerprint = fingerprintFn()
	}
	// 账号哈希(内存读)与公钥行(读盘)同在锁外;TrimSpace 收 provider 输出。
	accountHash := ""
	if accountHashFn != nil {
		accountHash = strings.TrimSpace(accountHashFn())
	}
	meshPubKey := ""
	if meshPubKeyFn != nil {
		meshPubKey = strings.TrimSpace(meshPubKeyFn())
	}
	p := beaconPayload{
		DeviceID:        selfID,
		MeshPort:        meshPort,
		CertFingerprint: fingerprint,
		AccountHash:     accountHash,
		MeshPubKey:      meshPubKey,
		TS:              time.Now().Unix(),
	}
	// 广播签名(私钥解析+签名,读盘级)在锁外;公钥缺席/签名失败=不带 Sig
	// (旧版形状——诚实降级:自动组网三要素缺一即不晋升,见自动 peer 闸)。
	if payloadSignerFn != nil && meshPubKey != "" {
		p.Sig = payloadSignerFn(p.signFields())
	}
	return p
}

func (b *Beacon) broadcastOnce() {
	b.mu.Lock()
	send, port := b.sendConn, b.port
	b.mu.Unlock()
	if send == nil {
		return
	}
	payload, err := json.Marshal(b.assemblePayload())
	if err != nil {
		return // 纯内值序列化,不可达
	}
	_ = send.SetWriteDeadline(time.Now().Add(beaconWriteTimeout))
	_, werr := send.WriteToUDP(payload, &net.UDPAddr{IP: net.IPv4bcast, Port: port})
	b.mu.Lock()
	if werr != nil {
		if !b.bcastErrLogged {
			b.bcastErrLogged = true
			logger.Info(fmt.Sprintf("[MESH-BEACON] broadcast to :%d failed (will retry silently): %v", port, werr))
		}
	} else {
		b.bcastErrLogged = false
	}
	b.mu.Unlock()
}

func (b *Beacon) readLoop(conn *net.UDPConn) {
	buf := make([]byte, beaconReadLimit)
	for {
		n, sender, err := conn.ReadFromUDP(buf)
		if err != nil {
			return // Stop 关闭 conn 是唯一退出路径
		}
		if n <= 0 || n >= len(buf) {
			continue // 满缓冲=被截断的超限报文,按畸形丢弃
		}
		// 来源 IP 是候选可拨地址的唯一可信来源(报文内无地址字段,自报可伪造
		// 到任意主机);源端口是广播临时口,拨号端口恒取报文内 mesh_port。
		senderIP := ""
		if sender != nil && sender.IP != nil {
			senderIP = sender.IP.String()
		}
		b.ingest(buf[:n], time.Now(), senderIP)
	}
}

// ingest 一条到达报文的入候选判定:非 JSON/缺字段/ts 超龄/自身广播 →
// 丢弃;通过则刷新或新建候选(Addr=senderIP+报文 mesh_port)。测试直接调用
// 以钉判定逻辑,不经 UDP。
func (b *Beacon) ingest(data []byte, now time.Time, senderIP string) {
	var p beaconPayload
	if err := json.Unmarshal(data, &p); err != nil {
		return
	}
	if p.DeviceID == "" || p.MeshPort <= 0 || p.MeshPort > 65535 {
		return // 缺字段/畸形端口:候选要供点选拨号,死端口无价值
	}
	// 同账号闸:双方哈希非空且不等=跨账号广播,丢弃(自动组网批次)。
	// 发送方无哈希(旧版/未登录)=旧版兼容收;本方无哈希(未登录)=全收兼容
	// ——两个兼容态只影响候选展示,连接面仍被钉扎+握手 fail-closed 挡住。
	if own := b.ownAccountHash(); own != "" && p.AccountHash != "" && p.AccountHash != own {
		return
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
	addr := ""
	if senderIP != "" {
		addr = net.JoinHostPort(senderIP, fmt.Sprintf("%d", p.MeshPort))
	}
	b.pruneLocked(now)
	entry := lanCandidate{
		LanPeerCandidate: LanPeerCandidate{
			DeviceID:        p.DeviceID,
			MeshPort:        p.MeshPort,
			Addr:            addr,
			CertFingerprint: p.CertFingerprint,
			AccountHash:     p.AccountHash,
			MeshPubKey:      p.MeshPubKey,
			LastSeenUnix:    p.TS,
		},
		lastArrival: now,
	}
	// mesh 公钥行预解析(自动 peer 双要素之二):坏行候选仍收录(展示面),
	// 但 parsedKey 为 nil 即不算自动 peer 要素。
	if line := strings.TrimSpace(p.MeshPubKey); line != "" {
		if k, _, _, _, err := gossh.ParseAuthorizedKey([]byte(line)); err == nil {
			entry.pubKey = k
		}
	}
	// 广播签名验签(自动组网批次):带 Sig 而验签失败=整包丢弃;无 Sig=
	// 旧版兼容收,但 sigVerified=false 永不晋升自动 peer。⚠️ Sig 通过只证
	// 明「公告自洽」(绑定 device_id+公钥),不证明账号成员——同 id 自签
	// 新钥可顶替条目,信任锚在 mesh.auto_peers 开关+握手(见类型注释)。
	if p.Sig != "" {
		if entry.pubKey == nil || !verifyBeaconSignature(entry.pubKey, p.signFields(), p.Sig) {
			return
		}
		entry.sigVerified = true
	}
	if cur, ok := b.candidates[p.DeviceID]; ok {
		// 已验签条目不被无签名报文降级(防降级攻击的**半个补丁**:只挡无
		// 签名方向;自签新钥的带签名广播仍可顶替——残余风险由 auto_peers
		// 显式开关+可信 LAN 语义+握手收口,见 beaconPayload 信任边界注释)。
		// 乱序到达的旧报文不回退 last_seen(展示值跟随对端 ts)。
		if cur.sigVerified && !entry.sigVerified {
			return
		}
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
