package ssh

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"aliang.one/nursorgate/common/logger"
	"aliang.one/nursorgate/processor/config"

	gossh "golang.org/x/crypto/ssh"
)

// channelDirectTCPIP 是入口唯一放行的通道类型(RFC 4254 §7.2);
// session/auth-agent@openssh.com/x11/forwarded-tcpip 等一律拒绝(安全收敛)。
const channelDirectTCPIP = "direct-tcpip"

// handshakeTimeout 约束单条入口连接的完整握手:version 协商+密钥交换+
// 用户认证。var 而非 const 仅为测试注入短超时(默认值即生产值);取值与
// 出口侧先例 outbound/proxy/ssh/chain.go handshakeTimeout 同款 15s。
var handshakeTimeout = 15 * time.Second

// IngressHostKeyFileName 是 host key 落盘文件名(cache/ssh/ 目录下)。导出供
// 装配层(Task 7 reconcile)与 host key 指纹 API(ssh_ingress_handler 的
// FromCache 构造)拼路径——编译器强制三方与 Server.Start 落盘同源,同出口侧
// sshout.KnownHostsFileName 的收敛先例。文件内容归本包管理。
const IngressHostKeyFileName = "ingress_host_key"

// Server 是 SSH 入口服务实例。生命周期三件套(Start/Stop/IsRunning)幂等;
// 实例不承载热更新——配置变更由装配层(Reconcile)重建实例。
//
// 监听归属(可选专用口,spec §6.4 修订):RunConfig.Port 显式指定(>0)
// → 另开常驻专用监听口 ServiceBindHost:port(随入口开关,不随代理启停);
// Port=0(缺省,含旧默认 56442 由翻译层哨兵归零)→ 不自持监听,由 56432
// 统一入口嗅探承载(随代理运行态)。两路径并存、同一实例同一套限速/凭证/
// 通道收敛;注入缝 HandleIngressConn/ServeConn 恒可用。
type Server struct {
	rc          RunConfig
	hostKeyPath string
	limiter     *authLimiter

	mu       sync.Mutex
	running  bool
	listener net.Listener // 专用口监听(Port>0 时起;0=跟随统一口,为 nil)
	conns    map[net.Conn]struct{}
	rootCtx  context.Context // Start 建立的根取消链:注入连接的停机兜底
	cancel   context.CancelFunc
	wg       sync.WaitGroup // accept 循环 + 每连接 goroutine

	hostKeyMu sync.Mutex
	hostKey   gossh.Signer

	// obsMu 保护 lastIngressForm/lastIngressDeviceID:最近一次通道读到的
	// 入口形态与 mesh 对端身份(extMeshDeviceID;观测先于矩阵拒绝判定,
	// 被拒通道同样留痕)。仅测试观测 Permissions→通道传递链用,生产行为不读。
	obsMu               sync.Mutex
	lastIngressForm     string
	lastIngressDeviceID string
}

// NewServer 创建 SSH 入口服务实例。hostKeyPath 指向 PEM 私钥文件
// (ed25519);缺失或损坏时 Start/HostKeyFingerprint 会生成新钥并原子落盘。
// rc 视为不可变(配置变更重建实例,而非原地改写)。
func NewServer(rc RunConfig, hostKeyPath string) *Server {
	return &Server{
		rc:          rc,
		hostKeyPath: hostKeyPath,
		limiter:     newAuthLimiter(),
		conns:       make(map[net.Conn]struct{}),
	}
}

// Start 进入运行态(幂等:已运行直接返回 nil)。Port>0 时另起专用监听口
// (bind 冲突即返回错误,reconcile 停留止态下次重试);Port=0 不监听,由
// 56432 统一入口承载。注入缝 HandleIngressConn/ServeConn 恒可用。
func (s *Server) Start() error {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return nil
	}
	// host key 就绪(缺失/损坏则生成+原子落盘)在持生命周期锁下完成:
	// 防止并发的 Start/Stop 观察到半初始化状态(冷路径,开销可忽略)。
	signer, err := s.ensureHostKey()
	if err != nil {
		s.mu.Unlock()
		return fmt.Errorf("ssh ingress host key: %w", err)
	}
	s.running = true
	// 根取消链保留(Stop 时 cancel):注入连接的拆除主路径是连接快照关闭,
	// 根 ctx 是 Stop 语义的完整性兜底——handleConn 的 per-conn ctx 监视挂它,
	// 快照后滑入/交错的注入连接由它兜底关闭(评审 B1:旧监听路径 acceptLoop
	// 传 Start 作用域 ctx 的同款兜底,注入缝不可丢)。
	ctx, cancel := context.WithCancel(context.Background())
	s.rootCtx = ctx
	s.cancel = cancel

	// 专用口(可选):显式端口才起监听;bind 失败回滚 running 置位。
	if s.rc.Port > 0 {
		addr := net.JoinHostPort(config.ServiceBindHost(), strconv.Itoa(int(s.rc.Port)))
		l, lerr := net.Listen("tcp", addr)
		if lerr != nil {
			s.running = false
			s.rootCtx = nil
			s.cancel = nil
			s.mu.Unlock()
			return fmt.Errorf("ssh ingress listen %s: %w", addr, lerr)
		}
		s.listener = l
		s.wg.Add(1)
		s.mu.Unlock()

		logger.Info(fmt.Sprintf("SSH ingress dedicated listener on %s (unified 56432 dispatch also active)", l.Addr().String()))
		go s.acceptLoop(ctx, l, s.serverConfig(signer))
		return nil
	}
	s.mu.Unlock()

	logger.Info("SSH ingress running on unified ingress 56432 (no dedicated listener)")
	return nil
}

// Stop 退出运行态(幂等):关专用监听(若有)、断开在途连接并等待 accept/
// handle 协程退出(drain/global-request 协程随连接拆除退出,不在此 WaitGroup)。
func (s *Server) Stop() error {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return nil
	}
	s.running = false
	cancel := s.cancel
	s.cancel = nil
	l := s.listener
	s.listener = nil
	pending := make([]net.Conn, 0, len(s.conns))
	for c := range s.conns {
		pending = append(pending, c)
	}
	s.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if l != nil {
		_ = l.Close() // 中断 accept 循环
	}
	// 快照内的在途连接全部断开,让握手/通道 goroutine 解除阻塞退出;
	// 快照后滑入/交错的连接由根 ctx 兜底关闭(见 Start 注释)。
	for _, c := range pending {
		_ = c.Close()
	}
	s.wg.Wait()
	logger.Info("SSH ingress server stopped")
	return nil
}

// IsRunning 报告服务是否在运行。
func (s *Server) IsRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

// Addr 返回专用监听口地址(host:port);未起专用口(Port=0 跟随统一口)或
// 未运行返回空串。56432 统一入口的可达性以 config.UnifiedIngressListenAddr
// 为准,不在本观测面。
func (s *Server) Addr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener == nil {
		return ""
	}
	return s.listener.Addr().String()
}

// acceptLoop 接受专用口连接并派发;监听关闭(Stop)后退出。连接记账与
// ServeConn 同款停机交错纪律:登记+wg.Add 在同一 s.mu 临界区完成,与 Stop
// 的「翻转→快照」互斥(评审 B1);Stop 关 listener 后 ctx.Done 保底退出。
func (s *Server) acceptLoop(ctx context.Context, l net.Listener, cfg *gossh.ServerConfig) {
	defer s.wg.Done()
	for {
		conn, err := l.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return
			default:
				logger.Debug(fmt.Sprintf("SSH ingress accept error: %v", err))
				continue
			}
		}
		s.mu.Lock()
		if !s.running {
			s.mu.Unlock()
			_ = conn.Close()
			return
		}
		s.conns[conn] = struct{}{}
		s.wg.Add(1)
		s.mu.Unlock()
		go s.handleConn(ctx, conn, cfg)
	}
}

// HostKeyFingerprint 返回 host key 的 SHA256 指纹(gossh.FingerprintSHA256,
// 形如 "SHA256:xxx")与算法名(如 "ssh-ed25519");必要时触发生成。
// 失败时返回空串(供 API 层兜底报错)。
func (s *Server) HostKeyFingerprint() (fingerprint, algorithm string) {
	signer, err := s.ensureHostKey()
	if err != nil {
		logger.Error(fmt.Sprintf("SSH ingress host key unavailable: %v", err))
		return "", ""
	}
	return gossh.FingerprintSHA256(signer.PublicKey()), signer.PublicKey().Type()
}

// HostKeyInfo 是 host key 的只读展示面(指纹 API 口径):指纹/算法名/
// 生成时间(文件 mtime,UTC)。
type HostKeyInfo struct {
	Fingerprint string
	Algorithm   string
	GeneratedAt time.Time
}

// ReadHostKeyInfo 只读地读取 path 处的 host key 并返回指纹信息。不生成、
// 不写盘——生成职责单一归 Server.Start:指纹 API 若也触发生成,双进程
// (core/user-agent)各持 handler 并发命中缺失文件时会各自生成、原子写
// rename 最后者胜,与 Server 已加载的钥错位(展示指纹≠实际服务钥)。
// 文件缺失返回包裹 fs.ErrNotExist 的错误(调用方 errors.Is 判 404 语义);
// 文件存在但损坏/不可读返回相应错误(由调用方显式报错,不静默换钥)。
func ReadHostKeyInfo(path string) (HostKeyInfo, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return HostKeyInfo{}, err
	}
	signer, err := gossh.ParsePrivateKey(data)
	if err != nil {
		return HostKeyInfo{}, fmt.Errorf("parse host key %s: %w", path, err)
	}
	st, err := os.Stat(path)
	if err != nil {
		return HostKeyInfo{}, err
	}
	pub := signer.PublicKey()
	return HostKeyInfo{
		Fingerprint: gossh.FingerprintSHA256(pub),
		Algorithm:   pub.Type(),
		GeneratedAt: st.ModTime().UTC(),
	}, nil
}

// serverConfig 构造每连接共用的服务端配置。不设 PasswordCallback 与
// KeyboardInteractiveCallback——密码/交互式认证不注册(spec §6),
// 客户端此类尝试必然失败;唯一准入路径是公钥。
func (s *Server) serverConfig(signer gossh.Signer) *gossh.ServerConfig {
	cfg := &gossh.ServerConfig{
		PublicKeyCallback: s.publicKeyCallback,
	}
	cfg.AddHostKey(signer)
	return cfg
}

// publicKeyCallback 是双密钥准入点(Task 3):公钥即形态区分器——
// 命中 MeshKeys→Permissions.Extensions["ingress_form"]=mesh 且同时携带
// device_id 标签(extMeshDeviceID,尾注释为空回落指纹前 16 字符);命中
// AuthorizedKeys→bare;都不命中→拒并计入限速。成功认证清零限速。
//
// 比较用解析后的公钥身份(key.Marshal() 字节,编码含类型与公钥体),
// 不比较 authorized_keys 文本行——同键多行/textual variants 无法借此
// 区分形态(spec §4 同键歧义;配置层 Validate 的逐字节重复键拦截是
// 第一道,这里是第二道)。双列表同键(绕过 Validate 直构造 RunConfig
// 可达)→mesh 优先:运行期保守取向,注释即约定。
func (s *Server) publicKeyCallback(meta gossh.ConnMetadata, key gossh.PublicKey) (*gossh.Permissions, error) {
	ip := remoteIP(meta.RemoteAddr())
	if mk, ok := meshKeyFor(key, s.rc.MeshKeys); ok {
		s.limiter.Reset(ip)
		return &gossh.Permissions{Extensions: map[string]string{
			extIngressForm:  FormMesh,
			extMeshDeviceID: meshDeviceIDLabel(mk),
		}}, nil
	}
	if keyInList(key, s.rc.AuthorizedKeys) {
		s.limiter.Reset(ip)
		return &gossh.Permissions{Extensions: map[string]string{extIngressForm: FormBare}}, nil
	}
	s.limiter.Fail(ip)
	logger.Debug(fmt.Sprintf("SSH ingress rejected unknown key %s for %q from %s",
		gossh.FingerprintSHA256(key), meta.User(), ip))
	return nil, fmt.Errorf("unknown public key for %q", meta.User())
}

// meshKeyFor 按公钥身份查 mesh 凭证,命中返回完整 MeshKey(DeviceID 随之
// 进入通道层)。
func meshKeyFor(key gossh.PublicKey, list []MeshKey) (MeshKey, bool) {
	for _, mk := range list {
		if bytes.Equal(key.Marshal(), mk.Key.Marshal()) {
			return mk, true
		}
	}
	return MeshKey{}, false
}

// meshDeviceIDLabel 尾注释为空时回落指纹前 16 字符。
func meshDeviceIDLabel(mk MeshKey) string {
	if id := strings.TrimSpace(mk.DeviceID); id != "" {
		return id
	}
	fp := gossh.FingerprintSHA256(mk.Key)
	if len(fp) > 16 {
		return fp[:16]
	}
	return fp
}

// keyInList 按公钥身份比对(只服务裸钥列表 AuthorizedKeys;mesh 列表走
// meshKeyFor 带出 DeviceID):Marshal 编码含类型与公钥体,字节级相等即同钥
// (v0.48 无 KeysEqual,与出口侧 sshtest 同款比对方式)。
func keyInList(key gossh.PublicKey, list []gossh.PublicKey) bool {
	for _, listed := range list {
		if bytes.Equal(key.Marshal(), listed.Marshal()) {
			return true
		}
	}
	return false
}

// ServeConn 承载一条外部注入的已 accept 连接(统一口分派缝,spec §6.4):
// 限速检查/握手 deadline/双形态通道收敛与 listener 路径完全同语义,差别只在
// 「谁 accept」。阻塞至连接结束;Stop 的连接快照拆除与 wg 排水同样覆盖注入
// 连接(注入前 wg.Add(1),与 acceptLoop 同款记账)。
//
// 入口未在运行态时直接关连接返回——与合并前「入口未启动则无人监听、连接被
// 拒」语义等价;不回显任何协议字节。
// ServeConn 承载一条外部注入的已 accept 连接(统一口分派缝,spec §6.4)。
// 阻塞至连接结束;关闭归本函数与 handleConn(双关幂等)。
//
// 停机交错纪律(评审 B1):running 检查、conns 登记、wg.Add 必须在同一
// s.mu 临界区内完成——与 Stop 的「running 翻转→快照」互斥后,注入连接
// 要么整段落在快照前(被快照拆除+wg.Wait 排水),要么整段落在翻转后
// (running=false,关连接返回,零记账)。分三段临界区会产生「登记晚于
// 快照但 wg.Add 早于 Wait」的交错:Stop 的 wg.Wait 挂死在一条无人拆除、
// ctx 又是 Background 的长活隧道上,连带 reconcileMu 永久持锁。
//
// ctx 取自 Start 的根取消链(不收外部 ctx):handleConn 的 per-conn 监视
// 挂根 ctx,Stop 的 cancel() 对快照外交错连接兜底关闭——与旧监听路径
// acceptLoop 传 Start 作用域 ctx 的兜底同源。
func (s *Server) ServeConn(conn net.Conn) {
	if conn == nil {
		return
	}
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		_ = conn.Close()
		return
	}
	ctx := s.rootCtx
	// 与旧 acceptLoop 相同的记账顺序:登记连接 + wg.Add 一气呵成(见上
	// 交错纪律),wg 计数交给 handleConn(其首行 wg.Done)。
	s.conns[conn] = struct{}{}
	s.wg.Add(1)
	s.mu.Unlock()

	// host key 缺失/损坏时此处会生成并落盘——与 Start 同源同锁(ensureHostKey
	// 内部 hostKeyMu),不会出现双钥并存。失败即拆除:已登记的连接由
	// handleConn 的 defer 关闭/除名。
	signer, err := s.ensureHostKey()
	if err != nil {
		logger.Error(fmt.Sprintf("SSH ingress serve injected conn: host key: %v", err))
		s.wg.Add(-1)
		_ = conn.Close()
		s.mu.Lock()
		delete(s.conns, conn)
		s.mu.Unlock()
		return
	}
	s.handleConn(ctx, conn, s.serverConfig(signer))
}

// handleConn 承载一条 TCP 连接:限速检查→SSH 握手→全局请求应答→通道收敛循环。
func (s *Server) handleConn(ctx context.Context, conn net.Conn, cfg *gossh.ServerConfig) {
	defer s.wg.Done()
	defer func() {
		s.mu.Lock()
		delete(s.conns, conn)
		s.mu.Unlock()
	}()
	defer conn.Close()

	// 停机兜底:Stop 只能关到快照内的连接;此处每条连接挂 ctx 监视,
	// 保证 wg.Wait 不被停机瞬间新滑入的连接卡死。
	gone := make(chan struct{})
	defer close(gone)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-gone:
		}
	}()

	// 连接级可取消 ctx:连接拆除(或 Stop 根取消)时先取消——挂它的
	// 裸流注入协程在途 Handle 有界退出(Task 4;TestStopWithInflightChannel
	// 钉住)。注入协程不入 WaitGroup,与 drain 协程同边界。
	connCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	ip := remoteIP(conn.RemoteAddr())
	if !s.limiter.Allow(ip) {
		// 认证失败退避窗口内:握手前直接断开(客户端表现为版本交换即断,
		// 与「认证被拒」错误可区分)。
		logger.Debug(fmt.Sprintf("SSH ingress rate-limited connection from %s", ip))
		return
	}

	// 握手 deadline:NewServerConn 不吃任何超时配置,慢速/挂起的客户端
	// 不允许无限占用连接——手动给 conn 设 deadline 让整段握手受
	// handshakeTimeout 约束,握手完立即清除,不污染后续会话流量
	// (先设后清,出口侧 chain.go 同款先例)。
	_ = conn.SetDeadline(time.Now().Add(handshakeTimeout))
	sconn, chans, reqs, err := gossh.NewServerConn(conn, cfg)
	_ = conn.SetDeadline(time.Time{})
	if err != nil {
		// 认证失败的记账在 PublicKeyCallback 内完成,此处仅日志。
		logger.Debug(fmt.Sprintf("SSH ingress handshake from %s failed: %v", ip, err))
		return
	}
	defer sconn.Close()

	logger.Debug(fmt.Sprintf("SSH ingress connection from %s authenticated as %q", ip, sconn.User()))
	go serveGlobalRequests(reqs)
	// v0.48 的 ServerConn.Permissions 是字段(认证回调返回值的落点)。
	s.serveChannels(connCtx, chans, sconn.Permissions, conn.RemoteAddr())
}

// serveGlobalRequests 应答连接级全局请求:keepalive@openssh.com 放行,
// 其余一律拒绝(入口不提供 tcpip-forward 等服务端反向转发)。
func serveGlobalRequests(reqs <-chan *gossh.Request) {
	for req := range reqs {
		if !req.WantReply {
			continue
		}
		if req.Type == "keepalive@openssh.com" {
			_ = req.Reply(true, nil)
			continue
		}
		_ = req.Reply(false, nil)
	}
}

// serveChannels 是通道安全收敛矩阵(SPEC §4.2):公钥身份决定 form,form
// 决定唯一可用 channel;所有拒绝在 Accept 前发出(Reject reason 会原样回给
// A 侧 OpenChannel)。bare→direct-tcpip 裸流注入(channel.go
// injectDirectTCPIP);mesh→mesh-exit ExtraData 注入(mesh_inject.go
// handleMeshExitChannel,SPEC §4);未知 form 全拒(UnknownChannelType——
// 认证层只产出 bare/mesh,此为防御分支,含 perms 未就绪的 nil 场景)。
// 观测面在拒绝判定之前:被拒通道同样留下 form/deviceID 审计痕迹
// (TestMeshExitMatrix 钉住先读 form 再 Reject 的次序)。
func (s *Server) serveChannels(ctx context.Context, chans <-chan gossh.NewChannel, perms *gossh.Permissions, remote net.Addr) {
	for newCh := range chans {
		form, meshDeviceID := "", ""
		if perms != nil {
			form = perms.Extensions[extIngressForm]
			meshDeviceID = perms.Extensions[extMeshDeviceID]
		}
		s.observeForm(form)
		s.observeDeviceID(meshDeviceID)

		var allowed string
		switch form {
		case FormBare:
			allowed = channelDirectTCPIP
		case FormMesh:
			allowed = channelMeshExit
		default:
			_ = newCh.Reject(gossh.UnknownChannelType, "unknown credential form")
			continue
		}
		if newCh.ChannelType() != allowed {
			_ = newCh.Reject(gossh.Prohibited, "channel not allowed for this credential form")
			continue
		}

		switch form {
		case FormBare:
			ch, chReqs, err := newCh.Accept()
			if err != nil {
				logger.Debug(fmt.Sprintf("SSH ingress direct-tcpip accept failed: %v", err))
				continue
			}
			go gossh.DiscardRequests(chReqs)
			injectDirectTCPIP(ctx, ch, remote, newCh.ExtraData())
		case FormMesh:
			handleMeshExitChannel(ctx, newCh, newCh.ExtraData(), s.rc, meshDeviceID, remote)
		}
	}
}

// observeForm 记录最近一次通道携带的入口形态,供测试观测
// Permissions→通道层的传递链(生产行为不读它)。
func (s *Server) observeForm(form string) {
	s.obsMu.Lock()
	s.lastIngressForm = form
	s.obsMu.Unlock()
}

func (s *Server) observedForm() string {
	s.obsMu.Lock()
	defer s.obsMu.Unlock()
	return s.lastIngressForm
}

// observeDeviceID 记录最近一次通道携带的 mesh 对端身份标签
// (extMeshDeviceID,bare 形态恒空),观测面与 observeForm 同族。
func (s *Server) observeDeviceID(id string) {
	s.obsMu.Lock()
	s.lastIngressDeviceID = id
	s.obsMu.Unlock()
}

func (s *Server) observedDeviceID() string {
	s.obsMu.Lock()
	defer s.obsMu.Unlock()
	return s.lastIngressDeviceID
}

// remoteIP 提取 net.Addr 的 host 部分作为限速键(SplitHostPort 失败时
// 原样返回——WithRemoteLabel 的标签地址即靠此语义落串桶,SPEC §8.2)。
func remoteIP(a net.Addr) string {
	if a == nil {
		return ""
	}
	if host, _, err := net.SplitHostPort(a.String()); err == nil {
		return host
	}
	return a.String()
}

// ensureHostKey 返回可复用的 host key signer:优先内存缓存,其次从
// hostKeyPath 加载;文件缺失或损坏(解析失败)则生成 ed25519 新钥并
// 原子落盘。
func (s *Server) ensureHostKey() (gossh.Signer, error) {
	s.hostKeyMu.Lock()
	defer s.hostKeyMu.Unlock()
	if s.hostKey != nil {
		return s.hostKey, nil
	}
	signer, err := s.loadOrGenerateHostKey()
	if err != nil {
		return nil, err
	}
	s.hostKey = signer
	return signer, nil
}

// loadOrGenerateHostKey 读取-解析 host key;文件缺失或内容损坏时生成
// ed25519 新钥并原子落盘覆盖。除缺失/损坏外的真实 IO 错误(如权限)
// 原样上抛,不静默换钥。
func (s *Server) loadOrGenerateHostKey() (gossh.Signer, error) {
	data, err := os.ReadFile(s.hostKeyPath)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		logger.Info(fmt.Sprintf("SSH ingress host key %s missing, generating ed25519 key", s.hostKeyPath))
	} else if signer, perr := gossh.ParsePrivateKey(data); perr == nil {
		return signer, nil
	} else {
		logger.Warn(fmt.Sprintf("SSH ingress host key %s corrupt (%v), regenerating", s.hostKeyPath, perr))
	}
	signer, pemBytes, err := generateHostKey()
	if err != nil {
		return nil, err
	}
	if err := writeHostKeyAtomic(s.hostKeyPath, pemBytes); err != nil {
		return nil, fmt.Errorf("ssh ingress persist host key: %w", err)
	}
	return signer, nil
}

// generateHostKey 生成 ed25519 host key 及其 OpenSSH PEM 编码。
func generateHostKey() (gossh.Signer, []byte, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate ed25519: %w", err)
	}
	signer, err := gossh.NewSignerFromKey(priv)
	if err != nil {
		return nil, nil, fmt.Errorf("host key signer: %w", err)
	}
	block, err := gossh.MarshalPrivateKey(priv, "ssh-ingress host key")
	if err != nil {
		return nil, nil, fmt.Errorf("marshal host key: %w", err)
	}
	return signer, pem.EncodeToMemory(block), nil
}

// writeHostKeyAtomic 以「临时文件+rename」原子落盘(0o600,先建目录),
// 避免崩溃留下半写文件被误当有效 host key。
func writeHostKeyAtomic(path string, pemBytes []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("temp file: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(pemBytes); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("write: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("chmod: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("close: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}
