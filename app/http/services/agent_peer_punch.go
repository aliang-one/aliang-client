package services

// mesh UDP 打洞批次 Task 10/15——peer.punch 双端编排体:上半(Task 10a)
// B 侧 WS 帧处理(peer.punch.session → listener 生命周期 + candidates 双向
// 接收的 B 端半边);下半(Task 10b)A 侧 cache-first 拨号源(result
// pending 接收/DialAndPut/dial 源注册,见文末 A 侧分节)。
//
// 架构定位:peer.lan 家族的数据面兄弟件。peer.lan 是「B 起 TCP listener、
// token 过门、单连接一生」;punch 是「B 起 QUIC listener 于打洞 socket 之上、
// token 过门、TTL 收摊」,多出的编排动作全在洞上:B 侧绑物理接口 socket →
// STUN 判型(顺带得自身公网映射)→ 起听 → 收 A 候选后从同一 transport
// 发探测 Initial 开 B 侧 NAT 映射;A 侧 miss 时同步走完整信令链+打洞,
// 活会话经 SessionCache 复用(免逐拨重打洞,仅 mesh;传输腿一次性拨号,
// transfer-punch-stun 质量评审裁决 5)。
//
// B 端流程含 purpose 路由(transfer-punch-stun Task 7):session 帧的
// purpose 缺省/mesh → 过门流喂 ssh 入口(既有语义零变化);transfer → 按
// transfer_id 单次消费接收上下文注册表(handleAgentPeerLanPushListen 注册,
// agent_peer_lan.go)换 transferServe 缝喂 lan_push 协议体;缺上下文
// fail-loud 回 transfer_context_missing,绝不静默喂 ssh 入口(用途错配=
// 数据喂错 handler)。
//
// B 端流程的顺序硬约束(Task 4 规格审查锁定):
//  1. 先绑 socket + Classify,后 QUIC listen——STUN 探测(Discover 的
//     ReadFromUDP)与 quic.Transport 读循环同 conn 抢包,listen 先起会把
//     STUN 应答偷走(单 Transport 铁律同源事故);顺序不可倒。
//  2. Classify 报 symmetricSuspect → 写 SkipMemo(编排层记账职责在此)
//     + 照常继续——B 侧对称 NAT 仍可能被 A 打通,EIF(endpoint-dependent
//     filtering)判型 v1 不做,真失败由 A 侧兜底落 piko。
//  3. Classify 报 error(STUN 不可达)→ 铁律:不写 SkipMemo(不可达≠
//     symmetric,冤枉进 memo 会白禁 5min)+ 打洞照试(A 侧兜底)。
//
// 凭据模型与线协议(Task 1 契约):session 帧带 token(云端下发的一次性
// 门禁凭据)与 ttl_seconds;B 回 session.result{ok,port,fingerprint}——
// port 是 QUIC listener 的本地 UDP 端口,fingerprint 是 B 现签 1h 证书的
// sha256(A 侧钉扎用,机器复用 generateAgentLanCertificate,agent_peer_
// lan.go:130);随后 B 把 Classify 学到的自身公网映射经 candidates 帧发
// 回(server 按 session_id 转给 A)。v1 线协议不携带 A 证书指纹,B 侧
// mTLS 只要求出示证书不钉扎(信任裁决=A 钉 B + B 侧 token 门禁,见
// punch.ServerTLSConfig 的 v1 降级注记)。

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strconv"
	"sync"
	"time"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/app/mesh/punch"
	"aliang.one/nursorgate/common/logger"
	sshin "aliang.one/nursorgate/inbound/ssh"
	meshproxy "aliang.one/nursorgate/outbound/proxy/mesh"
	"aliang.one/nursorgate/processor/config"
	"aliang.one/nursorgate/processor/tcp"
	"github.com/quic-go/quic-go"
)

const (
	// agentPunchSessionMaxTTLSeconds listener 寿命契约上限:与 punch 包
	// punchListenerDefaultTTL(90s)同值——打洞协商+A 侧重拨余量的设计值,
	// 服务端越界下发按此收敛而不是拒帧(照 peer.lan 的 ttl 钳制先例)。
	agentPunchSessionMaxTTLSeconds = 90
	// agentPunchProbeRounds 每帧 candidates 的探测轮封顶:探测是起火即忘的
	// 开洞助力,不是必成的拨号——多轮只为与 A 侧重试轮在空中相遇,烧完
	// 即止,失败由 A 侧 DialPeer 兜底。
	agentPunchProbeRounds = 3
	// agentPunchMaxCandidates 单帧候选处理上限:防御性封顶,信令面正常
	// 一两个候选,越界视为错位/滥用,截断处理不拒帧。
	agentPunchMaxCandidates = 8
)

// 探测节拍是包变量而非配置项(punch 包 classifyDiscoverTimeout 先例):
// 生产语义是常量,同包测试调小避免真等——B 侧 Initial 即探测,轮间隔给
// 信令送达与 NAT 映射生效留时间(与 A 侧 DialPeer 的轮参数同族)。
var (
	agentPunchProbeAttemptTimeout = 750 * time.Millisecond
	agentPunchProbeRoundInterval  = 300 * time.Millisecond
)

// agentPunchSessionResultPayload 组 peer.punch.session.result 帧
// (契约:Required type/request_id/ok,Optional port/fingerprint/fail_reason;
// public 为 punch-public-peer 批次新增声明键——成功且公网直挂才出键,
// 条件键不出键惯例,失败臂恒传 false)。
func agentPunchSessionResultPayload(requestID string, ok bool, port int, fingerprint, failReason string, public bool) map[string]interface{} {
	payload := map[string]interface{}{
		"type":       models.AgentEventPeerPunchSessionResult,
		"request_id": requestID,
		"ok":         ok,
	}
	if ok {
		payload["port"] = port
		payload["fingerprint"] = fingerprint
		if public {
			payload["public"] = true
		}
	} else if failReason != "" {
		payload["fail_reason"] = failReason
	}
	return payload
}

// agentPunchCandidatesPayload 组 peer.punch.candidates 帧(契约:Required
// type/session_id/candidates)。B 侧用于回发自身 STUN 映射。
func agentPunchCandidatesPayload(sessionID string, candidates []string) map[string]interface{} {
	return map[string]interface{}{
		"type":       models.AgentEventPeerPunchCandidates,
		"session_id": sessionID,
		"candidates": candidates,
	}
}

// agentPeerPunchSession 是一个打洞会话的 B 侧接收面状态:socket 由本编排
// 绑定(所有权归注册表,收摊时关);transport 是 listener 与探测 Dial 共用
// 的单实例(单 Transport 铁律,见 punch.ListenerOpts.Transport);done 由
// punch.ServeListener 关闭(TTL 到点/accept 报错即收摊);peerCandidates
// 是收到的 A 候选快照(诊断面,探测在到达时即发)。
type agentPeerPunchSession struct {
	sessionID      string
	socket         *net.UDPConn
	transport      *quic.Transport
	done           <-chan struct{}
	fingerprint    string
	peerCandidates []string
}

// agentPeerPunchB 是 B 侧打洞编排体:跨帧状态表(session 帧立表、
// candidates 帧查表)+ SkipMemo 持有者。全部依赖经注入缝(生产默认
// newAgentPeerPunchB,测试替换):bind(物理口绑定)/servers+classify
// (STUN 判型)/serve(起听,注入以见证顺序硬约束)/gate(punch 配置门)/
// handler(每条过门流的去向)。
//
// SkipMemo 归属说明:Task 4 的 memo 是 per-peer 失败记忆,但线协议 session
// 帧不携带 A 设备身份(Task 1 契约锁定),B 侧判的又是自身 NAT 的对称性
// ——故记账键取本机 device_id(「本机 NAT 近 5min 判过 symmetric」)。
// v1 没有读取面(判了也照常继续),纯粹给编排层留下事实账;真正按
// peer 维度记账/决策的用武之地在 A 侧(Task 10b,那边知道对端是谁)。
type agentPeerPunchB struct {
	mu       sync.Mutex
	sessions map[string]*agentPeerPunchSession
	skipMemo *punch.SkipMemo

	bind     func() (*net.UDPConn, string, error)
	servers  func() []string
	classify func(*net.UDPConn, []string) (punch.PublicMapping, bool, error)
	serve    func(*net.UDPConn, punch.ListenerOpts) (int, <-chan struct{}, error)
	gate     func() bool
	handler  func(sessionID string) func(net.Conn)
	// transferServe 是 purpose=transfer 会话的过门流去向(transfer-punch-stun
	// Task 7 注入缝):生产默认 agentPunchTransferServe——先设握手窗再喂
	// lan_push 接收协议体(punch 流自带 TLS,禁再包 tls.Server)。
	transferServe func(transferID string, tctx agentPunchTransferContext) func(net.Conn)
}

// agentPunchConfigEnabled punch 载体配置门:customer.mesh.punch_enable
// (让渡型能力,默认关,types.go EffectivePunchEnabled)。mesh 总开关的
// 合取在保存期校验(ValidateMeshDependency 硬拒)与装配门(Task 12),
// 这里按 Task 10 契约只读 punch 位——手改配置文件绕过校验的窗口由
// Task 12 运行期装配门兜底。
func agentPunchConfigEnabled() bool {
	cfg := config.GetGlobalConfig()
	if cfg == nil || cfg.Customer == nil {
		return false
	}
	return cfg.Customer.Mesh.EffectivePunchEnabled()
}

// agentPunchConfiguredServers 生产 STUN 服务器取值:customer.mesh.stun_servers
// 读侧(空回落 MeshDefaultStunServers,EffectiveStunServers 语义)。闭包逐调用
// 回读全局配置——保存即生效,不缓存(工厂 CacheKey 派生式置位同款哲学)。
func agentPunchConfiguredServers() []string {
	cfg := config.GetGlobalConfig()
	if cfg == nil || cfg.Customer == nil {
		// 与 EffectiveStunServers 同款拷贝纪律:不外泄共享常量切片。
		return append([]string(nil), config.MeshDefaultStunServers...)
	}
	return cfg.Customer.Mesh.EffectiveStunServers()
}

// cfgEffectivePublicListen public_listen 配置读侧(nil 安全,punch-public-peer
// 批次)。逐调用回读不缓存(保存即生效,agentPunchConfigEnabled 同款哲学);
// 与 punch 总开关的合取由 handleSession 开头的 b.gate 门结构性兜住。
func cfgEffectivePublicListen() bool {
	cfg := config.GetGlobalConfig()
	if cfg == nil || cfg.Customer == nil {
		return false
	}
	return cfg.Customer.Mesh.EffectivePublicListen()
}

// isPublicMapping 公网直挂判据(纯函数,snapshot 注入便于机器无关单测):
// STUN 映射 IP 命中任一非回环接口 IPv4 → 本机无 NAT,候选可直拨。
// 云 EIP 1:1 NAT 接口只见私网 IP → 不命中 → 不自动声明(显式
// public_listen 配置覆盖该场景)。DNAT 端口映射场景映射 IP=路由器公网
// ≠接口地址 → 同样不命中(保守正确)。
func isPublicMapping(mapping punch.PublicMapping, snap agentNetSnapshot) bool {
	if mapping.IP == nil {
		return false
	}
	for _, iface := range snap.Interfaces {
		ip := net.ParseIP(iface.IPv4)
		if ip != nil && ip.Equal(mapping.IP) {
			return true
		}
	}
	return false
}

// firstNonLoopbackIPv4 接口快照兜底候选的地址源(纯函数,机器无关单测)。
// collectAgentNetSnapshot 采集期已滤回环,这里的 IsLoopback 复核是纯函数
// 对注入快照的防御(快照无非回环 IPv4 → 空串,调用方留空候选退化落中继)。
func firstNonLoopbackIPv4(snap agentNetSnapshot) string {
	for _, iface := range snap.Interfaces {
		ip := net.ParseIP(iface.IPv4)
		if ip != nil && !ip.IsLoopback() {
			return iface.IPv4
		}
	}
	return ""
}

// agentPeerPunchIngressHandler 生产 Handler:每条过门流=一条「到 ssh 入口」
// 的连接,喂 sshin.HandleIngressConn(app/tunnel/manager.go mesh relay 的
// 同款喂法);remote label 带 punch- 前缀+session 供入口审计区分载体来源。
func agentPeerPunchIngressHandler(sessionID string) func(net.Conn) {
	return func(conn net.Conn) {
		labeled := sshin.WithRemoteLabel(conn, "punch-"+sessionID)
		if !sshin.HandleIngressConn(labeled) {
			logger.Warn(fmt.Sprintf("[AGENT-PEER-PUNCH] ssh ingress not running, dropped punch stream session=%s remote=%s", sessionID, conn.RemoteAddr()))
		}
	}
}

// agentPunchTransferServe 生产传输服务体:过门流直接进 lan_push 协议体
// (punch 流自带 TLS,禁再包 tls.Server——协议体拆层契约)。⚠️ 评审强制
// 接线(T6 质量评审 Important-1):协议体首行读无自带 deadline,依赖调用方
// 预设握手窗——TCP 薄壳设了(agentLanPushServeOnce),punch 路径必须在此
// 设,否则已连通但静默的对端会让 hello 读无限悬挂(TCP 路径 5s 失败落
// COS,punch 路径却挂在 QUIC 空闲超时)。wrapper 交付 conn 前先
// SetDeadline(握手窗)。
func agentPunchTransferServe(transferID string, tctx agentPunchTransferContext) func(net.Conn) {
	return func(conn net.Conn) {
		_ = conn.SetDeadline(time.Now().Add(agentLanHandshakeTimeout)) // 握手窗(评审强制)
		serveAgentLanPushConn(conn, tctx.token, transferID, tctx.filename, tctx.targetDir, tctx.maxBytes, tctx.writeJSON)
	}
}

// newAgentPeerPunchB 生产默认:物理口绑定+默认 STUN 对+punch.ServeListener
// +配置门+ssh 入口注入。
func newAgentPeerPunchB() *agentPeerPunchB {
	return &agentPeerPunchB{
		sessions:      map[string]*agentPeerPunchSession{},
		skipMemo:      &punch.SkipMemo{},
		bind:          func() (*net.UDPConn, string, error) { return punch.NewBoundUDPConn(punch.DefaultResolver) },
		servers:       agentPunchConfiguredServers,
		classify:      punch.Classify,
		serve:         punch.ServeListener,
		gate:          agentPunchConfigEnabled,
		handler:       agentPeerPunchIngressHandler,
		transferServe: agentPunchTransferServe,
	}
}

// 进程级单例(懒初始化):WS dispatch 每帧取用;同包测试直接 new 自持
// 实例,不经单例(agentPeerServiceProvider 先例的懒初始化形态)。
var (
	agentPeerPunchBMu     sync.RWMutex
	agentPeerPunchBShared *agentPeerPunchB
)

// sharedAgentPeerPunchB 取进程级 B 侧编排单例(与类型名 agentPeerPunchB
// 区分,故 accessor 冠 shared)。
func sharedAgentPeerPunchB() *agentPeerPunchB {
	agentPeerPunchBMu.RLock()
	inst := agentPeerPunchBShared
	agentPeerPunchBMu.RUnlock()
	if inst != nil {
		return inst
	}
	agentPeerPunchBMu.Lock()
	defer agentPeerPunchBMu.Unlock()
	if agentPeerPunchBShared == nil {
		agentPeerPunchBShared = newAgentPeerPunchB()
	}
	return agentPeerPunchBShared
}

// handleAgentPeerPunchSession 是 B 端 peer.punch.session 的 WS 入口
// (agent_remote_ws.go 显式臂 safeGo 调度,enabled-device 门禁在 dispatch)。
// selfPeerID 是本机 device_id(SkipMemo 记账键,语义见 agentPeerPunchB)。
// 立即回 result 帧(绑定+判型+起听完成即回,服务过程异步由 TTL 收摊)。
func (s *AgentService) handleAgentPeerPunchSession(msg map[string]interface{}, writeJSON func(interface{}) error) {
	sharedAgentPeerPunchB().handleSession(msg, writeJSON, s.currentDeviceID())
}

// handleAgentPeerPunchCandidates 是 peer.punch.candidates 的双端 WS 入口:
// B 端按 session_id 路由到在册会话,探测起火在注册表内异步(safeGo);
// A 端按 pending 表喂等待方(Task 10b)——双角色各自查表,查无不互扰。
func (s *AgentService) handleAgentPeerPunchCandidates(msg map[string]interface{}) {
	sharedAgentPeerPunchB().handleCandidates(msg)
	sharedAgentPeerPunchA().handleCandidatesFrame(msg)
}

// handleSession B 端主流程(顺序硬约束见文件头):门禁 → 帧校验(含
// purpose 路由/传输上下文单次消费)→ 绑 socket → STUN 判型(记账)→
// 现签证书 → 单 transport 起听 → 注册+收摊 reaper → 回 result → 回自身
// 候选(判型有映射时)。
func (b *agentPeerPunchB) handleSession(msg map[string]interface{}, writeJSON func(interface{}) error, selfPeerID string) {
	requestID := remoteString(msg, "request_id")
	if requestID == "" {
		logger.Warn("[AGENT-PEER-PUNCH] session request dropped reason=missing_request_id")
		return
	}
	fail := func(reason string) {
		_ = writeJSON(agentPunchSessionResultPayload(requestID, false, 0, "", reason, false))
	}

	if !b.gate() {
		// 配置门关:载体自禁的常生态(让渡型能力默认关),不算错误态。
		fail("punch_disabled")
		return
	}
	sessionID := remoteString(msg, "session_id")
	if sessionID == "" || len(sessionID) > 128 {
		fail("bad_session_id")
		return
	}
	token := remoteString(msg, "token")
	if token == "" || len(token) > 128 {
		fail("bad_token")
		return
	}
	ttlSeconds := agentTransferInt64(msg, "ttl_seconds")
	if ttlSeconds <= 0 || ttlSeconds > agentPunchSessionMaxTTLSeconds {
		// 越界(含缺省)按设计值收敛,不拒帧(照 peer.lan 的钳制口径)。
		ttlSeconds = agentPunchSessionMaxTTLSeconds
	}
	// advertise_ip 可选字段 v1 收下不消费:B 的候选来自 STUN 映射;LAN 提示
	// 作候选源是二期看数据再定的事(计划 Task 15 遗留账)。

	// 会话目的(transfer-punch-stun Task 7):缺省显式化为 mesh(老 A 侧
	// 无 purpose 帧=mesh,purposeOrMesh 同款归一——路由缺省→b.handler 缝,
	// 既有语义零变化);未知 purpose 拒帧——用途错配即数据喂错 handler,
	// fail-loud。
	purpose := purposeOrMesh(remoteString(msg, "purpose"))
	if purpose != "mesh" && purpose != "transfer" {
		fail("bad_purpose")
		return
	}
	transferID := remoteString(msg, "transfer_id")

	// 顺序硬约束①②:先绑 socket,STUN 判型也必须先于 QUIC listen——探测
	// 窗口(Discover 的 ReadFromUDP)与 Transport 读循环同 conn 抢包。
	socket, iface, err := b.bind()
	if err != nil {
		// 绑定失败=载体自禁(TUN-on 下裸 socket 的映射会打错 5-tuple,
		// socket.go 绑定铁律),回失败由 A 侧落 piko 中继。
		logger.Info(fmt.Sprintf("[AGENT-PEER-PUNCH] socket bind failed session=%s err=%v", sessionID, err))
		fail("socket_bind_failed")
		return
	}
	mapping, symmetricSuspect, classifyErr := b.classify(socket, b.servers())
	var selfCandidates []string
	if classifyErr != nil {
		// 铁律③:STUN 不可达≠symmetric,不写 SkipMemo;打洞照试——洞开
		// 与否最终由 A 侧 Initial 重试裁决,B 侧少学映射不碍事。
		logger.Info(fmt.Sprintf("[AGENT-PEER-PUNCH] classify unavailable session=%s err=%v", sessionID, classifyErr))
	} else {
		if symmetricSuspect {
			// 铁律②:判 symmetric 记账(键=本机 device_id,语义见结构体注)
			// 但照常继续——B 侧对称 NAT 仍可能被 A 打通,EIF 判型 v1 不做。
			b.skipMemo.Record(selfPeerID)
			logger.Info(fmt.Sprintf("[AGENT-PEER-PUNCH] symmetric suspect memoized session=%s self=%s(仍照常起听)", sessionID, selfPeerID))
		}
		if mapping.IP != nil && mapping.Port > 0 {
			selfCandidates = []string{net.JoinHostPort(mapping.IP.String(), strconv.Itoa(mapping.Port))}
		}
	}

	// 公网直连声明(punch-public-peer):自动判据(映射=接口地址,本机
	// 直挂)∨ 显式 public_listen(云 EIP 场景配置覆盖)。声明后 result 捎带
	// public,A 侧跳过自身 classify 直拨。classify 失败时自动判据短路
	// (|| 求值序)不取 mapping——判据两点互不污染。
	publicPeer := cfgEffectivePublicListen() ||
		(classifyErr == nil && mapping.IP != nil && isPublicMapping(mapping, collectAgentNetSnapshot()))

	// 传输上下文 serve 前取出(Handler 是 serve 的入参,先于 serve 求值);
	// serve 失败在下方 listen_failed 分支放回表项,消费语义归成功路径独占。
	// purpose 校验先行(bad_purpose 在帧校验段,缺上下文在此),失败早退
	// 不白签证书。
	tctx, haveTctx := agentPunchTransferContext{}, false
	if purpose == "transfer" {
		if !validAgentTransferID(transferID) {
			// socket 已绑,拒单须自收(与 cert_generate_failed 臂同款纪律)。
			_ = socket.Close()
			fail("bad_transfer_id")
			return
		}
		tctx, haveTctx = takeAgentPunchTransferContext(transferID)
		if !haveTctx {
			// 声明了传输用途却无接收上下文(listen 未发生/已被消费/TTL 摘除):
			// fail-loud 回 A,绝不静默喂 ssh 入口(用途错配=数据喂错 handler)。
			// socket 已绑,拒单须自收(与 cert_generate_failed 臂同款纪律)。
			_ = socket.Close()
			fail("transfer_context_missing")
			return
		}
	}
	handlerFn := b.handler(sessionID)
	if haveTctx {
		handlerFn = b.transferServe(transferID, tctx)
	}

	// 证书机器复用 peer.lan 的现签件(ECDSA P-256 自签 1h,指纹=叶子 DER
	// 的 sha256 hex),语义同构不重写(agent_peer_lan.go:130)。
	cert, fingerprint, err := generateAgentLanCertificate()
	if err != nil {
		_ = socket.Close()
		fail("cert_generate_failed")
		return
	}

	// 单 Transport 铁律:listener 与后续探测 Dial 必须同一实例(多 Transport
	// 同 conn 互相偷包,Task 7 实锤)——这里预建并经 ListenerOpts.Transport
	// 出让给 ServeListener,注册表留同一引用供探测用。
	transport := &quic.Transport{Conn: socket}
	port, done, err := b.serve(socket, punch.ListenerOpts{
		TTL:       time.Duration(ttlSeconds) * time.Second,
		Token:     token,
		TLS:       punch.PunchTLSConfig{ServerCert: cert, ExpectedToken: token},
		Handler:   handlerFn,
		Transport: transport,
	})
	if err != nil {
		_ = transport.Close()
		_ = socket.Close()
		if haveTctx {
			// 放回表项:serve 失败不消费(单次消费语义由成功路径独占)。
			registerAgentPunchTransferContext(transferID, tctx, time.Duration(ttlSeconds)*time.Second)
		}
		logger.Info(fmt.Sprintf("[AGENT-PEER-PUNCH] listener failed session=%s err=%v", sessionID, err))
		fail("listen_failed")
		return
	}

	// 收摊 reaper:done 关闭(TTL 到点/accept 报错)后清表+关 socket
	// (transport 已由 ServeListener 收摊,removeSession 里再关一次是幂等
	// 兜底)。按实例身份删表:同 session_id 顶替后,旧会话的 reaper 迟到
	// 醒来不得误删新表项(SessionCache.Release 的孤儿表项同款语义)。
	sess := &agentPeerPunchSession{
		sessionID:   sessionID,
		socket:      socket,
		transport:   transport,
		done:        done,
		fingerprint: fingerprint,
	}
	b.register(sess)
	safeGo("peer_punch_reaper", func() {
		<-done
		b.removeSession(sess)
	})

	logger.Info(fmt.Sprintf("[AGENT-PEER-PUNCH] listening session=%s iface=%s port=%d ttl=%ds", sessionID, iface, port, ttlSeconds))
	_ = writeJSON(agentPunchSessionResultPayload(requestID, true, port, fingerprint, "", publicPeer))
	if publicPeer && len(selfCandidates) == 0 && classifyErr != nil {
		// public_listen 声明且判型失败(STUN 全挂/云 EIP 接口私网):接口
		// 快照候选兜底(spec §2.1 STUN-独立逃生门;裁决口径=接口快照,
		// 非 bind iface 字符串)。落点在 serve 之后(port 此刻才可得)、
		// candidates 发送 gate 之前。快照无非回环 IPv4 → 留空(退化落中继,
		// spec §3 精度注记)。
		if ifaceIP := firstNonLoopbackIPv4(collectAgentNetSnapshot()); ifaceIP != "" {
			selfCandidates = []string{net.JoinHostPort(ifaceIP, strconv.Itoa(port))}
		}
	}
	if len(selfCandidates) > 0 {
		// B 自身公网候选回发(server 按 session_id 转给 A);判型失败无映射
		// 可发则不发——A 侧拿 result 的端口照试(public_listen 兜底候选除外)。
		_ = writeJSON(agentPunchCandidatesPayload(sessionID, selfCandidates))
	}
}

// handleCandidates B 端收 A 候选:存档 + 起火即忘式探测。未知 session_id
// (TTL 已收摊/错投)静默丢弃——candidates 无回包契约,日志留痕即止。
func (b *agentPeerPunchB) handleCandidates(msg map[string]interface{}) {
	sessionID := remoteString(msg, "session_id")
	if sessionID == "" {
		logger.Warn("[AGENT-PEER-PUNCH] candidates dropped reason=missing_session_id")
		return
	}
	candidates := validAgentPunchCandidates(remoteStringSlice(msg, "candidates"))
	if len(candidates) == 0 {
		logger.Info(fmt.Sprintf("[AGENT-PEER-PUNCH] candidates dropped session=%s reason=no_valid_candidate", sessionID))
		return
	}
	sess := b.addCandidates(sessionID, candidates)
	if sess == nil {
		logger.Info(fmt.Sprintf("[AGENT-PEER-PUNCH] candidates for unknown/expired session=%s dropped", sessionID))
		return
	}
	// 起火即忘:safeGo 里跑满轮数,错误全吞,不回帧不重试。
	safeGo("peer_punch_probe", func() { b.probeDial(sess, candidates) })
}

// validAgentPunchCandidates 过滤候选帧里的合法条目:host:port、IPv4 字面量
// (v1 打洞面是 udp4,与 socket.go 绑定铁律同界),截断到封顶常量。
func validAgentPunchCandidates(raw []string) []string {
	out := make([]string, 0, len(raw))
	for _, cand := range raw {
		if len(out) >= agentPunchMaxCandidates {
			break
		}
		host, port, err := net.SplitHostPort(cand)
		if err != nil {
			continue
		}
		ip := net.ParseIP(host)
		if ip == nil || ip.To4() == nil {
			continue
		}
		portNum, err := net.LookupPort("udp", port)
		if err != nil || portNum <= 0 {
			continue
		}
		out = append(out, cand)
	}
	return out
}

// probeDial B 侧探测:「向 A 候选发 QUIC Initial,开 B 侧 NAT 的出站映射」。
// 与 A 侧 DialPeer 的相遇语义——双方各自向对方发 Initial,A 侧的重试轮
// (DialPeer 300ms 一轮)与 B 侧这里的三轮在空中交叉,双向映射互开,任一
// 方的下一个 Initial 即可完成握手;B 侧 Initial 本身不期待应答(A 侧是
// 纯客户端 transport,不回 ServerHello),dial 超时是常态不是错误。
// punch.DialPeer 在此不可用——那是带 token 门禁+握手语义的完整链,探测
// 只要 Initial 出门。失败忽略、轮数封顶,会话收摊(done 关闭/transport
// 死)即刻止损退出。
func (b *agentPeerPunchB) probeDial(sess *agentPeerPunchSession, candidates []string) {
	// 探测 Initial 与真实拨号同 ALPN 同形(punch.PunchALPN):握手永不
	// 完成,InsecureSkipVerify 只为凑齐 quic-go 的客户端配置形态。
	tlsConf := &tls.Config{NextProtos: []string{punch.PunchALPN}, InsecureSkipVerify: true}
	var lastErr error
	for round := 0; round < agentPunchProbeRounds; round++ {
		if round > 0 {
			select {
			case <-sess.done:
				return
			case <-time.After(agentPunchProbeRoundInterval):
			}
		}
		for _, cand := range candidates {
			select {
			case <-sess.done:
				return
			default:
			}
			addr, err := net.ResolveUDPAddr("udp4", cand)
			if err != nil {
				lastErr = err
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), agentPunchProbeAttemptTimeout)
			qconn, err := sess.transport.Dial(ctx, addr, tlsConf, nil)
			cancel()
			if err == nil {
				// 握手面成功是常态非理论外(TLS 1.3 拒单时序不对称:客户端
				// 发完 Finished 即视为完成,服务端的无证书拒单随后必到且
				// 永不 Accept——punch 包已立档);立即关掉即收,探测只为出门。
				_ = qconn.CloseWithError(0, "")
				continue
			}
			lastErr = err
		}
	}
	logger.Info(fmt.Sprintf("[AGENT-PEER-PUNCH] probe cycle done session=%s rounds=%d candidates=%d last_err=%v",
		sess.sessionID, agentPunchProbeRounds, len(candidates), lastErr))
}

// register 入表;同 session_id 已在册(服务端重试/WS 重连重发)按顶替
// 处理——旧 socket 即刻收摊(v1 无 close 帧,顶替即显式收摊路径),新
// 会话接管表槽。
func (b *agentPeerPunchB) register(sess *agentPeerPunchSession) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.sessions == nil {
		b.sessions = map[string]*agentPeerPunchSession{}
	}
	if old, ok := b.sessions[sess.sessionID]; ok {
		old.closeLocked()
		logger.Info(fmt.Sprintf("[AGENT-PEER-PUNCH] session=%s replaced, old listener torn down", sess.sessionID))
	}
	b.sessions[sess.sessionID] = sess
}

// addCandidates 存档候选并取回会话(未知/已收摊返回 nil)。去重追加,
// 总量封顶两倍单帧上限(诊断面快照,不值得为它另设淘汰策略)。
func (b *agentPeerPunchB) addCandidates(sessionID string, candidates []string) *agentPeerPunchSession {
	b.mu.Lock()
	defer b.mu.Unlock()
	sess, ok := b.sessions[sessionID]
	if !ok {
		return nil
	}
	for _, cand := range candidates {
		dup := false
		for _, existing := range sess.peerCandidates {
			if existing == cand {
				dup = true
				break
			}
		}
		if !dup && len(sess.peerCandidates) < agentPunchMaxCandidates*2 {
			sess.peerCandidates = append(sess.peerCandidates, cand)
		}
	}
	return sess
}

// removeSession 收摊单个会话:按实例身份删表——表槽已被同 session_id 的
// 顶替者占据时,旧会话的迟到 reaper 不动作(新表项由自己的生命周期收
// 摊)。删表+关 socket+幂等关 transport(生产路径两者都已由 ServeListener
// 收摊,这里兜底覆盖 serve 注入/异常路径)。
func (b *agentPeerPunchB) removeSession(sess *agentPeerPunchSession) {
	if sess == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if cur, ok := b.sessions[sess.sessionID]; ok && cur == sess {
		delete(b.sessions, sess.sessionID)
	}
	sess.closeLocked()
}

// closeAll 全量收摊:测试卫生件 + 未来显式收摊钩子(v1 生产仅 TTL 收摊)。
func (b *agentPeerPunchB) closeAll() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for id, sess := range b.sessions {
		delete(b.sessions, id)
		sess.closeLocked()
	}
}

// closeLocked 关会话资源(须持 b.mu):transport/socket 双关皆幂等。
func (s *agentPeerPunchSession) closeLocked() {
	if s.transport != nil {
		_ = s.transport.Close()
	}
	if s.socket != nil {
		_ = s.socket.Close()
	}
}

// sessionCount/sessionByID 是同包测试的观测面(在册数/按 id 取会话)。
func (b *agentPeerPunchB) sessionCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.sessions)
}

func (b *agentPeerPunchB) sessionByID(sessionID string) *agentPeerPunchSession {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sessions[sessionID]
}

// ---------------------------------------------------------------------------
// A 侧(Task 10b):cache-first 拨号源 + result/candidates pending 接收 +
// 平行缝注册。
// ---------------------------------------------------------------------------

// 信令等待是包变量而非配置项(punch 包 classifyDiscoverTimeout 先例):
// 生产语义是常量;result 等待须覆盖 B 侧 bind+双 STUN 判型的最坏时延
// (10a 实测 ~6s),candidates 等待覆盖 B 侧回程一跳。
var (
	agentPunchResultWait     = 10 * time.Second
	agentPunchCandidatesWait = 5 * time.Second
)

// agentPeerPunchA 是 A 侧打洞编排体:cache-first 拨号源(活会话直接开流,
// miss 走同步完整打洞链)+ pending 接收面(result 按 request_id、candidates
// 按 session_id 路由)+ per-peer SkipMemo(A 侧知道对端是谁,记账键=对端
// deviceID——与 B 侧「键=本机」的语义分工见各自结构体注)。
//
// 并发契约:dialMu 把 miss 路径全局串行——同 conn 并发重拨会互偷包
// (Task 8 情报:同 UDP conn 的多个 Transport 互为读循环对家);快路径
// (Acquire 命中)不持 dialMu,缓存自带的表级并发安全足够。sockets 表存
// 每 peer 活会话的打洞 socket:SessionCache 只关 Transport 不关底层 conn
// (Task 8 契约),socket 的回收=下次同键重拨时驱逐旧件(僵尸窗口内至多
// 滞留一张/键,进程级封顶)。键=punchCacheKey(peerID, purpose)——mesh
// 裸 peerID,传输腿带前缀;传输腿一次性拨号(裁决 5)不写这两张表,前缀
// 键退化为查表侧防线:传输拨号若用裸键,快路径会命中 mesh 会话(传输流
// 喂进对端 ssh handler)。注释见 dialMiss。
type agentPeerPunchA struct {
	mu                sync.Mutex
	pendingResult     map[string]chan map[string]interface{}
	pendingCandidates map[string]chan []string
	candidateStash    map[string][]string // 早到的 B 候选(B 判型可先于 A 注册完成,信令异步无先序保证)
	sockets           map[string]*net.UDPConn
	skipMemo          *punch.SkipMemo
	cache             *punch.SessionCache
	dialMu            sync.Mutex

	gate     func() bool
	bind     func() (*net.UDPConn, string, error)
	servers  func() []string
	classify func(*net.UDPConn, []string) (punch.PublicMapping, bool, error)
	cert     func() (tls.Certificate, string, error)
	send     func(map[string]interface{}) error
}

// newAgentPeerPunchA 生产默认:配置门+物理口绑定+默认 STUN 对+现签证书
// (generateAgentLanCertificate,B 侧不钉 A 但证书出示仍被要求)+真拨号件
// +自发帧走 currentRemoteWriterFunc(断连 nil 即失败——punch 信令发不出去
// 就该落 piko,不该等)。send 先查 AgentPeerPunchSetFrameSink 注入缝
// (Task 14:集成测试与诊断面接 mock 信令桥;生产恒 nil,零影响)。
func newAgentPeerPunchA() *agentPeerPunchA {
	return &agentPeerPunchA{
		pendingResult:     map[string]chan map[string]interface{}{},
		pendingCandidates: map[string]chan []string{},
		candidateStash:    map[string][]string{},
		sockets:           map[string]*net.UDPConn{},
		skipMemo:          &punch.SkipMemo{},
		cache:             punch.NewSessionCache(),
		gate:              agentPunchConfigEnabled,
		bind:              func() (*net.UDPConn, string, error) { return punch.NewBoundUDPConn(punch.DefaultResolver) },
		servers:           agentPunchConfiguredServers,
		classify:          punch.Classify,
		cert:              generateAgentLanCertificate,
		send: func(payload map[string]interface{}) error {
			if sink := agentPunchFrameSinkLoad(); sink != nil {
				return sink(payload)
			}
			w := currentRemoteWriterFunc()
			if w == nil {
				return errors.New("agent punch: remote writer offline")
			}
			return w(payload)
		},
	}
}

// 进程级单例与平行缝注册(镜像 B 侧 shared 形态):init 注册保证 Task 12
// 的工厂在任意时刻取缝都能拿到当前实例的 dialSource(闭包延迟解引用,
// 单例懒初始化竞态由 shared 内部锁兜住)。
var (
	agentPeerPunchAMu     sync.RWMutex
	agentPeerPunchAShared *agentPeerPunchA
)

func sharedAgentPeerPunchA() *agentPeerPunchA {
	agentPeerPunchAMu.RLock()
	inst := agentPeerPunchAShared
	agentPeerPunchAMu.RUnlock()
	if inst != nil {
		return inst
	}
	agentPeerPunchAMu.Lock()
	defer agentPeerPunchAMu.Unlock()
	if agentPeerPunchAShared == nil {
		agentPeerPunchAShared = newAgentPeerPunchA()
	}
	return agentPeerPunchAShared
}

func init() {
	tcp.SetMeshPunchDialSource(func() meshproxy.PunchDialFunc {
		return sharedAgentPeerPunchA().dialSource
	})
}

// punchDialOpts 是拨号链的变体输入(transfer-punch-stun Task 5):mesh
// 语义全零值;传输腿带加长预算与 purpose/transferID(会话帧捎带,B 侧按
// purpose 路由过门流)。
type punchDialOpts struct {
	budget     time.Duration // DialPeer 预算;0=punch.DialBudget
	purpose    string        // ""=mesh;"transfer"=文件传输腿
	transferID string        // purpose=transfer 时必填(信令寻址+接收侧查表)
}

// punchCacheKey 会话缓存/sock 表键(计划期裁决 4 + 质量评审裁决 5):mesh
// 沿用裸 peerID(存量键形态不变);transfer 查表键带前缀——传输腿一次性
// 拨号不写表,前缀仍是查表侧硬防线:传输拨号若用裸键,快路径会命中 mesh
// 会话,把传输流喂给对端的 ssh handler。skipMemo 保持裸 peerID
// (NAT 属性按对端,与用途无关)。
func punchCacheKey(peerID, purpose string) string {
	if purpose == "" {
		return peerID
	}
	return purpose + ":" + peerID
}

// purposeOrMesh 缺省 purpose 显式化为 "mesh"(B 侧单分支判读;帧面语义:
// 老版本 A 侧发的无 purpose 帧 = mesh)。
func purposeOrMesh(purpose string) string {
	if purpose == "" {
		return "mesh"
	}
	return purpose
}

// agentTransferPunchBudget 传输腿拨号预算:覆盖 B 侧 bind+双 STUN 判型最坏
// ~6s + 正常拨号,替代预热(spec 计划期裁决 1);耗尽落 COS 由上传链兜底。
const agentTransferPunchBudget = 12 * time.Second

// dialSource 是 PunchDialFunc 的 A 侧本体(cache-first):快路径缓存开流,
// miss 同步完整打洞链——首拨付信令成本,后续走缓存;一切失败恒错误,
// 由 ExitClient 装配序(LAN→udp→piko)落 piko 中继。mesh 语义零值变体
// (签名是注册缝,不可改)。
func (a *agentPeerPunchA) dialSource(ctx context.Context, peerID string) (net.Conn, error) {
	return a.dialSourceOpts(ctx, peerID, punchDialOpts{})
}

// dialSourceOpts 变体拨号入口:门禁/快路径/miss 链对 mesh 与传输腿同源,
// 仅键(punchCacheKey)与预算/帧面(punchDialOpts)不同;成功路径分流
// (裁决 5):mesh 入缓存复用,传输腿一次性出让(见 dialMiss 文末)。
func (a *agentPeerPunchA) dialSourceOpts(ctx context.Context, peerID string, opts punchDialOpts) (net.Conn, error) {
	if !a.gate() {
		return nil, errors.New("agent punch: disabled by config")
	}
	key := punchCacheKey(peerID, opts.purpose)
	if conn, ok := a.tryCached(ctx, key); ok {
		return conn, nil
	}
	return a.dialMiss(ctx, peerID, opts)
}

// tryCached 快路径:Acquire 命中→开流;开流失败=会话已死(含僵尸窗口),
// 返回未命中交 miss 路径驱逐重拨。lease 即取即释——流的生命周期独立于
// lease,Release 只减引用计数(Task 8 契约)。key=punchCacheKey 产物
// (用途隔离在调用侧落键,本函数只管开流)。
func (a *agentPeerPunchA) tryCached(ctx context.Context, key string) (net.Conn, bool) {
	lease := a.cache.Acquire(key)
	if lease == nil {
		return nil, false
	}
	conn, err := lease.OpenStream(ctx)
	lease.Release()
	if err != nil {
		return nil, false
	}
	return conn, true
}

// dialMiss miss 路径(dialMu 串行;transfer miss 同样全程持锁——一次性
// 拨号后锁内含 DialPeer,与 mesh 同构):重拨检查 → 驱逐旧 socket → memo
// 预检 → 信令链(session→result→Classify→candidates)→ DialPeer →
// 成功路径分流(mesh=DialAndPut 同款缓存复用;transfer=一次性出让,裁决 5)。
func (a *agentPeerPunchA) dialMiss(ctx context.Context, peerID string, opts punchDialOpts) (net.Conn, error) {
	a.dialMu.Lock()
	defer a.dialMu.Unlock()

	// 键分层(计划期裁决 4):cache 与 sockets 表按用途隔离,同 peer 的
	// mesh 会话与传输会话互不可见;skipMemo/日志/SkipMemo 记账保持裸
	// peerID(NAT 属性按对端,与用途无关)。
	key := punchCacheKey(peerID, opts.purpose)

	// 双重检查:等锁期间别的 goroutine 已建会话→直接快路径。
	if conn, ok := a.tryCached(ctx, key); ok {
		return conn, nil
	}

	// 驱逐旧 socket:该键上一个会话已死(否则快路径就命中了),其
	// 打洞 socket 由本表持有(SessionCache 只关 Transport);僵尸窗口内
	// 的滞留件在下次重拨时此刻回收——每键至多一张,进程级封顶。
	a.mu.Lock()
	oldSock := a.sockets[key]
	delete(a.sockets, key)
	a.mu.Unlock()
	if oldSock != nil {
		_ = oldSock.Close()
	}

	// memo 预检(per-peer):已知对称 NAT 的对端直接拒绝——省掉 B 侧起听
	// 90s 与信令一整个来回(评审定谳:NAT 判型/失败记忆,否则对称 NAT
	// 对端每次拨号白烧 2-3s)。
	if a.skipMemo.ShouldSkip(peerID) {
		return nil, fmt.Errorf("agent punch: peer %s skip memo (symmetric suspect)", peerID)
	}

	token := agentPunchRandomID()
	sessionID := agentPunchRandomID()
	requestID := "punch-" + agentPunchRandomID()

	// ① session 帧 → pending 等 result(注册先于发送,消除时序丢帧——
	// 与 10a B 侧 register 先于 writeJSON 同款纪律)。
	resCh := make(chan map[string]interface{}, 1)
	a.mu.Lock()
	a.pendingResult[requestID] = resCh
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		delete(a.pendingResult, requestID)
		a.mu.Unlock()
	}()
	if err := a.send(map[string]interface{}{
		"type":        models.AgentEventPeerPunchSession,
		"request_id":  requestID,
		"session_id":  sessionID,
		"token":       token,
		"ttl_seconds": agentPunchSessionMaxTTLSeconds,
		// 路由信封(Task 13 契约):server 靠它寻址 B,转发前剥除——B 收到
		// 的帧与 Task 1 锁定契约逐字一致。B 侧忽略未知字段,向后兼容。
		"target_device_id": peerID,
		// 会话目的(transfer-punch-stun Task 5):缺省=mesh 语义向后兼容
		//(老 B 侧忽略未知字段);transfer 带 transfer_id 供 B 侧查接收
		// 上下文。target_device_id 路由信封不变。
		"purpose":     purposeOrMesh(opts.purpose),
		"transfer_id": opts.transferID,
	}); err != nil {
		return nil, fmt.Errorf("agent punch: send session: %w", err)
	}
	var fingerprint string
	select {
	case res := <-resCh:
		if ok, _ := res["ok"].(bool); !ok {
			reason := remoteString(res, "fail_reason")
			return nil, fmt.Errorf("agent punch: peer refused session: %s", reason)
		}
		fingerprint = remoteString(res, "fingerprint")
	case <-ctx.Done():
		return nil, fmt.Errorf("agent punch: awaiting result: %w", ctx.Err())
	case <-time.After(agentPunchResultWait):
		return nil, errors.New("agent punch: result timeout")
	}

	// ② 绑 socket + Classify(铁律:先于任何拨号,探测窗口不吞打洞包)。
	socket, _, err := a.bind()
	if err != nil {
		// 绑定失败=载体自禁(socket.go 绑定铁律),落 piko。
		return nil, fmt.Errorf("agent punch: bind: %w", err)
	}
	mapping, symmetricSuspect, classifyErr := a.classify(socket, a.servers())
	var selfCandidates []string
	if classifyErr != nil {
		// 铁律:不可达≠symmetric,不写 SkipMemo;打洞照试。
		logger.Info(fmt.Sprintf("[AGENT-PEER-PUNCH-A] classify unavailable peer=%s err=%v", peerID, classifyErr))
	} else {
		if symmetricSuspect {
			// per-peer 记账后拒绝:本对端 5min 内不再尝试(重拨检查前有
			// ShouldSkip 预检兜住)。
			a.skipMemo.Record(peerID)
			_ = socket.Close()
			return nil, fmt.Errorf("agent punch: peer %s symmetric nat suspect", peerID)
		}
		if mapping.IP != nil && mapping.Port > 0 {
			selfCandidates = []string{net.JoinHostPort(mapping.IP.String(), strconv.Itoa(mapping.Port))}
		}
	}

	// ③ candidates 帧 → pending 等 B 候选。
	candCh := make(chan []string, 1)
	a.mu.Lock()
	a.pendingCandidates[sessionID] = candCh
	stashed := a.candidateStash[sessionID]
	delete(a.candidateStash, sessionID)
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		delete(a.pendingCandidates, sessionID)
		a.mu.Unlock()
	}()
	if len(stashed) > 0 {
		// 注册前早到的候选直接投递(先到先服务,不与后到帧抢序)。
		select {
		case candCh <- stashed:
		default:
		}
	}
	if err := a.send(agentPunchCandidatesPayload(sessionID, selfCandidates)); err != nil {
		_ = socket.Close()
		return nil, fmt.Errorf("agent punch: send candidates: %w", err)
	}
	var peerCandidates []string
	select {
	case peerCandidates = <-candCh:
	case <-ctx.Done():
		_ = socket.Close()
		return nil, fmt.Errorf("agent punch: awaiting candidates: %w", ctx.Err())
	case <-time.After(agentPunchCandidatesWait):
		_ = socket.Close()
		return nil, errors.New("agent punch: candidates timeout")
	}
	if len(peerCandidates) == 0 {
		// B 判型失败无映射可发(v1 线协议无候选)——A 拿 result 端口也
		// 拼不出公网地址,直连无望,落 piko。
		_ = socket.Close()
		return nil, errors.New("agent punch: no peer candidates")
	}

	// ④ 现签证书 + 真拨号(B 不钉 A,证书出示仍被要求;A 钉 B 指纹
	// fail-closed)。此处是 DialAndPut 的展开形态(DialPeer→Put 手动链,
	// mesh 路径),展开只为多一步「首流收口」:PunchSession 的首流只承担
	// 过门凭据(token 行已写毕),后续取流面是全新流(mesh 走
	// Lease.OpenStream,传输腿走 Conn.OpenStreamSync),首流若任其悬挂,
	// B 侧过门 handler(喂 ssh 入口)会在其上空等 SSH 版本串直到握手
	// deadline——每次成功打洞都在对端留一个 ~15s 的僵尸 ServeConn
	// (Task 14 集成测试实锤)。FIN 掉首流,B 侧读面立即 EOF 退场。
	cert, _, err := a.cert()
	if err != nil {
		_ = socket.Close()
		return nil, fmt.Errorf("agent punch: cert: %w", err)
	}
	session, err := punch.DialPeer(socket, punch.DialOpts{
		Token:      token,
		TLS:        punch.PunchTLSConfig{ClientCert: cert, ServerFingerprint: fingerprint},
		Candidates: func() []string { return peerCandidates },
		// 预算接线(计划期裁决 1):mesh 零值走 punch.DialBudget 默认;
		// 传输腿带 agentTransferPunchBudget 加长预算——漏接=传输腿静默
		// 退回默认预算,整个「预算吸收 B 侧判型时延」设计失效。
		// KeepAlive(质量评审裁决 5):仅 mesh 置真——缓存会话要跨取用
		// 间隙存活等 NAT 映射,保活刷洞;传输腿一次性拨号,推完即弃,
		// 保活没有服务对象。
		Budget:    opts.budget,
		KeepAlive: opts.purpose == "",
	})
	if err != nil {
		_ = socket.Close()
		if errors.Is(err, punch.ErrPunchGateRejected) {
			// token 永久无效,不重试(重试烧的是同一 token)。
			logger.Info(fmt.Sprintf("[AGENT-PEER-PUNCH-A] gate rejected peer=%s err=%v", peerID, err))
			return nil, fmt.Errorf("agent punch: gate rejected: %w", err)
		}
		logger.Info(fmt.Sprintf("[AGENT-PEER-PUNCH-A] punch failed peer=%s err=%v(落 piko)", peerID, err))
		return nil, fmt.Errorf("agent punch: dial peer: %w", err)
	}

	// 成功路径分流(质量评审裁决 5):mesh=缓存复用(现状原样);传输腿
	// =一次性拨号——不 Put 缓存、不 sockets 入表。传输会话若入缓存,同
	// 对端第二个传输(新 transferID)在会话存活窗内会命中 t-1 缓存会话:
	// 不发新 session 帧→B 侧无新接收上下文(单次消费注册表已空),数据流
	// 进 t-1 绑定的 handler,lan_push hello 的 token+transfer_id 双核验必
	// 失配→punch 腿必败恒落 COS。每传输独立信令即消解。
	if opts.purpose != "" {
		return a.dialTransferOneShot(ctx, session, socket, peerID, key, sessionID)
	}

	lease := a.cache.Put(key, session)
	if lease == nil {
		// 拨号成功却入表被拒=会话残缺(理论不可达):别泄漏,当场收摊。
		_ = session.Transport.Close()
		_ = socket.Close()
		return nil, errors.New("agent punch: 会话残缺,入缓存被拒")
	}
	if session.Stream != nil {
		_ = session.Stream.Close() // 首流收口(见 ④ 注)
	}

	// ⑤ 成功(mesh):socket 入表(会话物理底层,键=用途复合键),开流
	// 交付,lease 即取即释。
	a.mu.Lock()
	a.sockets[key] = socket
	a.mu.Unlock()
	conn, err := lease.OpenStream(ctx)
	lease.Release()
	if err != nil {
		return nil, fmt.Errorf("agent punch: open stream: %w", err)
	}
	logger.Info(fmt.Sprintf("[AGENT-PEER-PUNCH-A] punched peer=%s key=%s session=%s", peerID, key, sessionID))
	return conn, nil
}

// dialTransferOneShot 传输腿成功路径(裁决 5):首流收口(与 mesh 同款,见
// dialMiss ④ 注——B 侧过门 handler 不在首流上空等)后开新流交付,出让
// 一次性包装 conn(oneShotPunchConn:Close 连动收摊 transport 与底层
// socket——传输路径不入缓存/sock 表,无人代收,推完即弃的确定性收口)。
func (a *agentPeerPunchA) dialTransferOneShot(ctx context.Context, session *punch.PunchSession, socket *net.UDPConn, peerID, key, sessionID string) (net.Conn, error) {
	if session.Stream != nil {
		_ = session.Stream.Close() // 首流收口(见 dialMiss ④ 注)
	}
	stream, err := session.Conn.OpenStreamSync(ctx)
	if err != nil {
		_ = session.Transport.Close()
		_ = socket.Close()
		return nil, fmt.Errorf("agent punch: open stream: %w", err)
	}
	logger.Info(fmt.Sprintf("[AGENT-PEER-PUNCH-A] punched peer=%s key=%s session=%s(one-shot)", peerID, key, sessionID))
	return &oneShotPunchConn{
		Conn:      &punchStreamConn{stream: stream, conn: session.Conn},
		transport: session.Transport,
		socket:    socket,
	}, nil
}

// punchStreamConn 把 quic.Stream 适配成 net.Conn:语义贴 punch 包同名件
// (包内件未导出,services 侧一次性拨号自持同款)——Read/Write/Close 委托
// 流,Local/RemoteAddr 取所属 QUIC 会话(quic.Stream 自身不携带地址),
// 三个 Deadline 委托流。B 侧免门契约下每条流=一条独立上层「连接」的载体。
type punchStreamConn struct {
	stream *quic.Stream
	conn   *quic.Conn
}

// 编译期契约:punchStreamConn 必须是完整 net.Conn,缺方法在此现形。
var _ net.Conn = (*punchStreamConn)(nil)

func (c *punchStreamConn) Read(p []byte) (int, error)  { return c.stream.Read(p) }
func (c *punchStreamConn) Write(p []byte) (int, error) { return c.stream.Write(p) }
func (c *punchStreamConn) Close() error                { return c.stream.Close() }
func (c *punchStreamConn) LocalAddr() net.Addr         { return c.conn.LocalAddr() }
func (c *punchStreamConn) RemoteAddr() net.Addr        { return c.conn.RemoteAddr() }

func (c *punchStreamConn) SetDeadline(t time.Time) error      { return c.stream.SetDeadline(t) }
func (c *punchStreamConn) SetReadDeadline(t time.Time) error  { return c.stream.SetReadDeadline(t) }
func (c *punchStreamConn) SetWriteDeadline(t time.Time) error { return c.stream.SetWriteDeadline(t) }

// oneShotPunchConn 一次性拨号的出让件:Close 时连动收摊 transport 与底层
// socket(transfer 路径不入缓存/sock 表,无人代收——推完即弃的确定性收口)。
// delegate 是 QUIC 流的 net.Conn 适配。
type oneShotPunchConn struct {
	net.Conn
	transport *quic.Transport
	socket    *net.UDPConn
	closeOnce sync.Once
}

func (c *oneShotPunchConn) Close() error {
	c.closeOnce.Do(func() {
		_ = c.Conn.Close()
		_ = c.transport.Close()
		_ = c.socket.Close()
	})
	return nil
}

// handleResultFrame A 端 session.result 接收:按 request_id 路由 pending;
// 无 pending(超时已放弃/错投)静默消费防 default 噪声(peer.lan
// listen_result 的 unexpected 臂同款)。
func (a *agentPeerPunchA) handleResultFrame(msg map[string]interface{}) {
	requestID := remoteString(msg, "request_id")
	a.mu.Lock()
	ch, ok := a.pendingResult[requestID]
	a.mu.Unlock()
	if !ok {
		logger.Info(fmt.Sprintf("[AGENT-PEER-PUNCH-A] result for unknown request_id=%s dropped", requestID))
		return
	}
	select {
	case ch <- msg:
	default:
	}
}

// handleCandidatesFrame A 端 candidates 接收:按 session_id 路由 pending
// (非法条目过滤在 validAgentPunchCandidates,B 侧同款);无 pending 静默。
func (a *agentPeerPunchA) handleCandidatesFrame(msg map[string]interface{}) {
	sessionID := remoteString(msg, "session_id")
	cands := validAgentPunchCandidates(remoteStringSlice(msg, "candidates"))
	a.mu.Lock()
	ch, ok := a.pendingCandidates[sessionID]
	if !ok && len(cands) > 0 {
		// 早到帧(B 判型先于 A 的注册完成):暂存等注册方捞回,封顶防滥用。
		if len(a.candidateStash[sessionID]) < agentPunchMaxCandidates {
			a.candidateStash[sessionID] = append(a.candidateStash[sessionID], cands...)
		}
		a.mu.Unlock()
		return
	}
	a.mu.Unlock()
	if !ok {
		return
	}
	select {
	case ch <- cands:
	default:
	}
}

// closeAll 全量收摊(测试卫生件+未来显式收摊钩子):pending 清空(等待方
// 走超时/ctx 路径自然失败)+socket 全关。
func (a *agentPeerPunchA) closeAll() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.pendingResult = map[string]chan map[string]interface{}{}
	a.pendingCandidates = map[string]chan []string{}
	a.candidateStash = map[string][]string{}
	for id, sock := range a.sockets {
		delete(a.sockets, id)
		_ = sock.Close()
	}
}

// agentPunchRandomID 会话/请求标识(crypto/rand hex;rand 失败退化纳秒
// 36 进制——标识唯一性降级为进程内唯一,不碍信令路由)。
func agentPunchRandomID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(b)
}

// ---------------------------------------------------------------------------
// 导出投递面(Task 14):集成测试与诊断面入口
// ---------------------------------------------------------------------------

// agentPunchFrameSink 是 punch 信令帧的注入出口:A 侧编排的自发帧(send
// 缝)与 B 侧编排的回帧(AgentPeerPunchHandleFrame 组的 writer)共用一个
// 出口——loopback 双角色同进程拓扑下,由接进来的桥按帧型区分方向
// (session=A 发 / result=B 发 / candidates 在 loopback 恒为 A 发,见
// test/punch_integration_test.go 的拓扑注记)。nil = 生产默认
// (currentRemoteWriterFunc)。
var agentPunchFrameSinkMu sync.RWMutex
var agentPunchFrameSinkFn func(map[string]interface{}) error

func agentPunchFrameSinkLoad() func(map[string]interface{}) error {
	agentPunchFrameSinkMu.RLock()
	defer agentPunchFrameSinkMu.RUnlock()
	return agentPunchFrameSinkFn
}

// AgentPeerPunchSetFrameSink 注入 punch 信令帧出口(集成测试与诊断面缝,
// mesh.ExitClient.TransportNames 观测面同族):非 nil 时 A 侧自发帧与 B 侧
// result/candidates 回帧均经 sink 投出,调用方(集成测试的 mock 信令桥)
// 自行路由;nil 恢复生产默认。生产路径恒不置位,零影响。并发安全。
func AgentPeerPunchSetFrameSink(sink func(map[string]interface{}) error) {
	agentPunchFrameSinkMu.Lock()
	agentPunchFrameSinkFn = sink
	agentPunchFrameSinkMu.Unlock()
}

// AgentPeerPunchDial A 侧打洞拨号入口(集成测试与诊断面):与生产装配
// (init 注册给 tcp.SetMeshPunchDialSource 的同源函数)一致地包住共享
// 单例的 cache-first 拨号源——快路径缓存开流,miss 同步完整打洞链。
func AgentPeerPunchDial(ctx context.Context, peerID string) (net.Conn, error) {
	return sharedAgentPeerPunchA().dialSource(ctx, peerID)
}

// AgentPeerPunchTransferDial 文件传输打洞拨号入口(上传腿 punch 载体,
// transfer-punch-stun Task 5):transfer purpose 会话+加长预算;门禁/信令
// 链与 mesh 同源,但成功路径是一次性拨号(质量评审裁决 5:不进缓存/sock
// 表,KeepAlive=false,推完即收——B 侧接收上下文单次消费,二次传输复用
// 旧会话必 hello 失配;每传输独立信令)。失败恒错误,由上传链落 COS。
func AgentPeerPunchTransferDial(ctx context.Context, peerID, transferID string) (net.Conn, error) {
	return sharedAgentPeerPunchA().dialSourceOpts(ctx, peerID, punchDialOpts{
		budget:     agentTransferPunchBudget,
		purpose:    "transfer",
		transferID: transferID,
	})
}

// AgentPeerPunchHandleFrame 供集成测试与诊断面投递 punch 帧(生产路径走
// agent_remote_ws.go 的 WS 分派臂;本缝路由等价但少了 enabled-device 门与
// setRemoteConnectionState——门禁语义以生产臂为准:session 进 B 侧编排体,
// result/candidates 进 A 侧 pending,candidates 亦进 B 侧注册表——与分派
// 臂逐臂同源,仅少了 setRemoteConnectionState 的 WS 状态副作用与 safeGo
// 异步化:时序差由两侧既有的 pending 注册先行/candidates 暂存机制吸收,
// 那两件本来就在为信令异步兜底)。另收传输腿的前置帧 peer.lan.push.listen
// (transfer-punch-stun Task 9:生产=server 下发 listen 帧给目标设备注册
// punch 接收上下文,同款路由等价直调;listen_result 经共用 writer 回帧,
// 接桥时桥按帧型静默忽略非 punch 帧)。未知帧型静默忽略。
func AgentPeerPunchHandleFrame(msg map[string]interface{}) {
	switch remoteString(msg, "type") {
	case models.AgentEventPeerPunchSession:
		GetSharedAgentService().handleAgentPeerPunchSession(msg, agentPunchFrameWriter())
	case models.AgentEventPeerPunchSessionResult:
		sharedAgentPeerPunchA().handleResultFrame(msg)
	case models.AgentEventPeerPunchCandidates:
		GetSharedAgentService().handleAgentPeerPunchCandidates(msg)
	case models.AgentEventPeerLanPushListen:
		GetSharedAgentService().handleAgentPeerLanPushListen(msg, agentPunchFrameWriter())
	}
}

// agentPunchFrameWriter 组 B 侧回帧 writer(AgentPeerPunchHandleFrame 的
// session 臂用):sink 注入时走桥,否则生产默认(currentRemoteWriterFunc,
// 断连 nil 即失败——punch 信令发不出去就该落 piko,不该等)。
func agentPunchFrameWriter() func(interface{}) error {
	return func(v interface{}) error {
		if payload, ok := v.(map[string]interface{}); ok {
			if sink := agentPunchFrameSinkLoad(); sink != nil {
				return sink(payload)
			}
		}
		w := currentRemoteWriterFunc()
		if w == nil {
			return errors.New("agent punch: remote writer offline")
		}
		return w(v)
	}
}
