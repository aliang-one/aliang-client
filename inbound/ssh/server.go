package ssh

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"aliang.one/nursorgate/common/logger"

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
// 监听归属(统一口收编,spec §6.4):本实例不再 net.Listen——56432 统一
// 入口 accept 后嗅探为 SSH 的连接经 HandleIngressConn/ServeConn 注入本实例。
// Start 因此只剩「host key 就绪 + 运行态置位」两件事。
type Server struct {
	rc          RunConfig
	hostKeyPath string
	limiter     *authLimiter

	mu      sync.Mutex
	running bool
	conns   map[net.Conn]struct{}
	cancel  context.CancelFunc
	wg      sync.WaitGroup // 每连接 goroutine(注入路径 ServeConn 记账)

	hostKeyMu sync.Mutex
	hostKey   gossh.Signer

	// obsMu 保护 lastIngressForm:最近一次 direct-tcpip 通道读到的入口
	// 形态。仅测试观测 Permissions→通道传递链用,生产行为不读。
	obsMu           sync.Mutex
	lastIngressForm string
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

// Start 进入运行态(幂等:已运行直接返回 nil)。不再监听——56432 统一
// 入口承载监听(spec §6.4),本函数只剩「host key 就绪 + 运行态置位」;
// 连接进入走 HandleIngressConn/ServeConn 注入缝。
func (s *Server) Start() error {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return nil
	}
	// host key 就绪(缺失/损坏则生成+原子落盘)在持生命周期锁下完成:
	// 防止并发的 Start/Stop 观察到半初始化状态(冷路径,开销可忽略)。
	if _, err := s.ensureHostKey(); err != nil {
		s.mu.Unlock()
		return fmt.Errorf("ssh ingress host key: %w", err)
	}
	s.running = true
	// 根取消链保留(Stop 时 cancel):注入连接的拆除主路径是连接快照关闭,
	// cancel 是 Stop 语义的完整性兜底(与旧监听路径同款生命周期字段)。
	_, s.cancel = context.WithCancel(context.Background())
	s.mu.Unlock()

	logger.Info("SSH ingress server running (listening is owned by the unified ingress 56432)")
	return nil
}

// Stop 退出运行态(幂等):断开在途连接并等待 handle 协程退出(drain/
// global-request 协程随连接拆除退出,不在此 WaitGroup)。无监听可关——
// 连接是统一口注入的,拆连接快照即可。
func (s *Server) Stop() error {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return nil
	}
	s.running = false
	cancel := s.cancel
	s.cancel = nil
	pending := make([]net.Conn, 0, len(s.conns))
	for c := range s.conns {
		pending = append(pending, c)
	}
	s.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	// 快照内的在途连接全部断开,让握手/通道 goroutine 解除阻塞退出;
	// 快照后滑入的连接由 handleConn 的 ctx 监视兜底关闭。
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
// 命中 MeshKeys→Permissions.Extensions["ingress_form"]=mesh;命中
// AuthorizedKeys→bare;都不命中→拒并计入限速。成功认证清零限速。
//
// 比较用解析后的公钥身份(key.Marshal() 字节,编码含类型与公钥体),
// 不比较 authorized_keys 文本行——同键多行/textual variants 无法借此
// 区分形态(spec §4 同键歧义;配置层 Validate 的逐字节重复键拦截是
// 第一道,这里是第二道)。双列表同键(绕过 Validate 直构造 RunConfig
// 可达)→mesh 优先:运行期保守取向,注释即约定。
func (s *Server) publicKeyCallback(meta gossh.ConnMetadata, key gossh.PublicKey) (*gossh.Permissions, error) {
	ip := remoteIP(meta.RemoteAddr())
	switch {
	case keyInList(key, s.rc.MeshKeys):
		s.limiter.Reset(ip)
		return &gossh.Permissions{Extensions: map[string]string{extIngressForm: FormMesh}}, nil
	case keyInList(key, s.rc.AuthorizedKeys):
		s.limiter.Reset(ip)
		return &gossh.Permissions{Extensions: map[string]string{extIngressForm: FormBare}}, nil
	default:
		s.limiter.Fail(ip)
		logger.Debug(fmt.Sprintf("SSH ingress rejected unknown key %s for %q from %s",
			gossh.FingerprintSHA256(key), meta.User(), ip))
		return nil, fmt.Errorf("unknown public key for %q", meta.User())
	}
}

// keyInList 按公钥身份比对:Marshal 编码含类型与公钥体,字节级相等即同钥
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
func (s *Server) ServeConn(ctx context.Context, conn net.Conn) {
	if conn == nil {
		return
	}
	s.mu.Lock()
	running := s.running
	s.mu.Unlock()
	if !running {
		_ = conn.Close()
		return
	}

	// host key 缺失/损坏时此处会生成并落盘——与 Start 同源同锁(ensureHostKey
	// 内部 hostKeyMu),不会出现双钥并存。
	signer, err := s.ensureHostKey()
	if err != nil {
		logger.Error(fmt.Sprintf("SSH ingress serve injected conn: host key: %v", err))
		_ = conn.Close()
		return
	}

	// 与 acceptLoop 相同的记账顺序:先登记连接再把 wg 计数交给 handleConn
	// (handleConn 首行 wg.Done)。注入连接不在 acceptLoop 的生命周期内,故
	// 由本函数自己 Add。
	s.mu.Lock()
	s.conns[conn] = struct{}{}
	s.mu.Unlock()
	s.wg.Add(1)
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

// serveChannels 是通道安全收敛白名单:仅 direct-tcpip 放行,其余
// (session/auth-agent@openssh.com/x11/forwarded-tcpip 等)全拒。
// 通道形态经认证 Permissions.Extensions["ingress_form"] 读取,分流注入
// 见 channel.go injectDirectTCPIP(bare→裸流;mesh→帧外壳;未知保守 drain)。
func (s *Server) serveChannels(ctx context.Context, chans <-chan gossh.NewChannel, perms *gossh.Permissions, remote net.Addr) {
	for newCh := range chans {
		if newCh.ChannelType() != channelDirectTCPIP {
			_ = newCh.Reject(gossh.UnknownChannelType, "only direct-tcpip is accepted on this ingress")
			continue
		}
		form := ""
		if perms != nil {
			form = perms.Extensions[extIngressForm]
		}
		s.observeForm(form)
		ch, chReqs, err := newCh.Accept()
		if err != nil {
			logger.Debug(fmt.Sprintf("SSH ingress direct-tcpip accept failed: %v", err))
			continue
		}
		logger.Debug(fmt.Sprintf("SSH ingress direct-tcpip accepted (form=%s)", form))
		go gossh.DiscardRequests(chReqs)
		// 形态分流注入在 channel.go(bare→裸流;mesh→帧外壳;未知保守
		// drain);dispatcher 本身同步派发各形态的注入 goroutine。
		injectDirectTCPIP(ctx, ch, remote, form, newCh.ExtraData(), s.rc)
	}
}

// observeForm 记录最近一次 direct-tcpip 通道携带的入口形态,供测试观测
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

// drainAndClose 排空并关闭通道(bare/mesh 两形态均由注入路径接管;
// 此处仅剩未知形态的防御分支,见 channel.go injectDirectTCPIP)。
func drainAndClose(ch gossh.Channel) {
	_, _ = io.Copy(io.Discard, ch)
	_ = ch.Close()
}

// remoteIP 提取 net.Addr 的 host 部分作为限速键。
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
