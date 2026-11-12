package services

// mesh UDP 打洞批次 Task 10a——B 侧 WS 帧处理(peer.punch.session →
// listener 生命周期 + peer.punch.candidates 双向接收的 B 端半边)。
//
// 架构定位:peer.lan 家族的数据面兄弟件。peer.lan 是「B 起 TCP listener、
// token 过门、单连接一生」;punch 是「B 起 QUIC listener 于打洞 socket 之上、
// token 过门、TTL 收摊」,多出的编排动作全在洞上:绑物理接口 socket →
// STUN 判型(顺带得自身公网映射)→ 起听 → 收 A 候选后从同一 transport
// 发探测 Initial 开 B 侧 NAT 映射。A 侧(result pending 接收/DialAndPut/
// dial 源注册)归 Task 10b,本文件不碰。
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
	"crypto/tls"
	"fmt"
	"net"
	"strconv"
	"sync"
	"time"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/app/mesh/punch"
	"aliang.one/nursorgate/common/logger"
	sshin "aliang.one/nursorgate/inbound/ssh"
	"aliang.one/nursorgate/processor/config"
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
// (契约:Required type/request_id/ok,Optional port/fingerprint/fail_reason)。
func agentPunchSessionResultPayload(requestID string, ok bool, port int, fingerprint, failReason string) map[string]interface{} {
	payload := map[string]interface{}{
		"type":       models.AgentEventPeerPunchSessionResult,
		"request_id": requestID,
		"ok":         ok,
	}
	if ok {
		payload["port"] = port
		payload["fingerprint"] = fingerprint
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

// newAgentPeerPunchB 生产默认:物理口绑定+默认 STUN 对+punch.ServeListener
// +配置门+ssh 入口注入。
func newAgentPeerPunchB() *agentPeerPunchB {
	return &agentPeerPunchB{
		sessions: map[string]*agentPeerPunchSession{},
		skipMemo: &punch.SkipMemo{},
		bind:     func() (*net.UDPConn, string, error) { return punch.NewBoundUDPConn(punch.DefaultResolver) },
		servers:  punch.DefaultSTUNServers,
		classify: punch.Classify,
		serve:    punch.ServeListener,
		gate:     agentPunchConfigEnabled,
		handler:  agentPeerPunchIngressHandler,
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

// handleAgentPeerPunchCandidates 是 B 端 peer.punch.candidates 的 WS 入口:
// 按 session_id 路由到在册会话,探测起火在注册表内异步(safeGo)。
func (s *AgentService) handleAgentPeerPunchCandidates(msg map[string]interface{}) {
	sharedAgentPeerPunchB().handleCandidates(msg)
}

// handleSession B 端主流程(顺序硬约束见文件头):门禁 → 帧校验 → 绑
// socket → STUN 判型(记账)→ 现签证书 → 单 transport 起听 → 注册+收摊
// reaper → 回 result → 回自身候选(判型有映射时)。
func (b *agentPeerPunchB) handleSession(msg map[string]interface{}, writeJSON func(interface{}) error, selfPeerID string) {
	requestID := remoteString(msg, "request_id")
	if requestID == "" {
		logger.Warn("[AGENT-PEER-PUNCH] session request dropped reason=missing_request_id")
		return
	}
	fail := func(reason string) {
		_ = writeJSON(agentPunchSessionResultPayload(requestID, false, 0, "", reason))
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
		Handler:   b.handler(sessionID),
		Transport: transport,
	})
	if err != nil {
		_ = transport.Close()
		_ = socket.Close()
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
	_ = writeJSON(agentPunchSessionResultPayload(requestID, true, port, fingerprint, ""))
	if len(selfCandidates) > 0 {
		// B 自身公网候选回发(server 按 session_id 转给 A);判型失败无映射
		// 可发则不发——A 侧拿 result 的端口照试。
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
				// 理论外(A 侧起了 QUIC 服务端):立即关掉即收,探测只为出门。
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
