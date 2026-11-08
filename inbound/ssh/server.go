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
	"strconv"
	"sync"

	"aliang.one/nursorgate/common/logger"
	"aliang.one/nursorgate/processor/config"

	gossh "golang.org/x/crypto/ssh"
)

// channelDirectTCPIP 是入口唯一放行的通道类型(RFC 4254 §7.2);
// session/auth-agent@openssh.com/x11/forwarded-tcpip 等一律拒绝(安全收敛)。
const channelDirectTCPIP = "direct-tcpip"

// Server 是 SSH 入口服务实例。生命周期三件套(Start/Stop/IsRunning)幂等,
// 照 inbound/http 模式;实例不承载热更新——配置变更由装配层(Reconcile,
// Task 7)重建实例。
type Server struct {
	rc          RunConfig
	hostKeyPath string
	limiter     *authLimiter

	mu       sync.Mutex
	running  bool
	listener net.Listener
	conns    map[net.Conn]struct{}
	cancel   context.CancelFunc
	wg       sync.WaitGroup // accept 循环+每连接 goroutine

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

// Start 启动监听(幂等:已运行直接返回 nil)。
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
	addr := net.JoinHostPort(config.ServiceBindHost(), strconv.Itoa(int(s.rc.Port)))
	l, err := net.Listen("tcp", addr)
	if err != nil {
		s.mu.Unlock()
		return fmt.Errorf("ssh ingress listen %s: %w", addr, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.running = true
	s.listener = l
	s.cancel = cancel
	s.wg.Add(1)
	s.mu.Unlock()

	logger.Info(fmt.Sprintf("SSH ingress server starting on %s", l.Addr().String()))
	go s.acceptLoop(ctx, l, s.serverConfig(signer))
	return nil
}

// Stop 停止服务(幂等):关监听、断开在途连接并等待 accept/handle
// 协程退出(drain/global-request 协程随连接拆除退出,不在此 WaitGroup)。
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
		_ = l.Close()
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

// Addr 返回监听地址(host:port);未运行返回空串。RunConfig.Port=0 时
// 供测试/装配层取实际随机端口。
func (s *Server) Addr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener == nil {
		return ""
	}
	return s.listener.Addr().String()
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

// acceptLoop 接受连接并派发;监听关闭(Stop)后退出。
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
		s.conns[conn] = struct{}{}
		s.mu.Unlock()
		s.wg.Add(1)
		go s.handleConn(ctx, conn, cfg)
	}
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

	sconn, chans, reqs, err := gossh.NewServerConn(conn, cfg)
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
