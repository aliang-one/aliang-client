// Package sshtest 提供同进程 loopback SSH 服务器,供 ssh outbound 全链路测试。
// 仅测试 import,不进生产二进制。
package sshtest

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	gossh "golang.org/x/crypto/ssh"
)

// Server 是一台内存 sshd。Password/Authorized 二选一或并存。
type Server struct {
	Listener   net.Listener
	HostKey    gossh.Signer
	Password   string
	Authorized gossh.PublicKey
	Username   string
	// NextDial 非空时,通道目标经它转发(多跳级联用:下一跳 sshd)。
	// 返回的 net.Conn 必须实现 CloseWrite(如 *net.TCPConn 或 WrapChannel
	// 包装的下一跳通道),否则 pipe 的半关语义无法生效。
	NextDial func(addr string) (net.Conn, error)

	// chClosed 记录每个已接受连接的关闭函数;Close 时全部调用,
	// 保证测试结束后 serve/handle/pipe goroutine 全部退出,不泄漏。
	mu       sync.Mutex
	chClosed []func()
}

// Start 在 127.0.0.1 随机端口起一台 sshd,返回实例与 host:port。
// t.Cleanup 自动注册 Close。
func Start(t *testing.T, opts ...Option) (*Server, string) {
	t.Helper()
	s := &Server{Username: "tester"}
	for _, o := range opts {
		o(s)
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("host key gen: %v", err)
	}
	s.HostKey, err = gossh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("host key signer: %v", err)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s.Listener = l
	go s.serve()
	t.Cleanup(s.Close)
	return s, l.Addr().String()
}

type Option func(*Server)

func WithPassword(p string) Option { return func(s *Server) { s.Password = p } }
func WithUsername(u string) Option { return func(s *Server) { s.Username = u } }
func WithAuthorized(k gossh.PublicKey) Option {
	return func(s *Server) { s.Authorized = k }
}
func WithNextDial(f func(addr string) (net.Conn, error)) Option {
	return func(s *Server) { s.NextDial = f }
}

// Port 返回监听端口。
func (s *Server) Port() int {
	return s.Listener.Addr().(*net.TCPAddr).Port
}

// Close 关闭监听与所有已接受连接(幂等),用于 t.Cleanup 兜底清场。
func (s *Server) Close() {
	_ = s.Listener.Close()
	s.mu.Lock()
	for _, f := range s.chClosed {
		f()
	}
	s.chClosed = nil
	s.mu.Unlock()
}

func (s *Server) trackConn(c net.Conn) {
	s.mu.Lock()
	s.chClosed = append(s.chClosed, func() { _ = c.Close() })
	s.mu.Unlock()
}

func (s *Server) serve() {
	for {
		c, err := s.Listener.Accept()
		if err != nil {
			return
		}
		go s.handle(c)
	}
}

func (s *Server) handle(c net.Conn) {
	s.trackConn(c)
	cfg := &gossh.ServerConfig{}
	// 注:NewServerConn 要求先 AddHostKey,否则报 "server has no host keys"。
	cfg.AddHostKey(s.HostKey)
	if s.Password != "" {
		cfg.PasswordCallback = func(conn gossh.ConnMetadata, password []byte) (*gossh.Permissions, error) {
			if string(password) != s.Password || conn.User() != s.Username {
				return nil, fmt.Errorf("auth rejected for %s", conn.User())
			}
			return &gossh.Permissions{}, nil
		}
	}
	if s.Authorized != nil {
		cfg.PublicKeyCallback = func(conn gossh.ConnMetadata, key gossh.PublicKey) (*gossh.Permissions, error) {
			// v0.48 无 KeysEqual;Marshal 编码含类型与公钥体,字节级比对即等价。
			if key.Type() != s.Authorized.Type() || !bytes.Equal(key.Marshal(), s.Authorized.Marshal()) {
				return nil, fmt.Errorf("key mismatch")
			}
			if conn.User() != s.Username {
				return nil, fmt.Errorf("auth rejected for %s", conn.User())
			}
			return &gossh.Permissions{}, nil
		}
	}
	if s.Password == "" && s.Authorized == nil {
		// 两个认证回调都没配时放开鉴权,让裸 Start 也可用于纯转发测试。
		cfg.NoClientAuth = true
	}
	sconn, chans, reqs, err := gossh.NewServerConn(c, cfg)
	if err != nil {
		return
	}
	defer sconn.Close()
	go s.handleReqs(reqs)
	for newCh := range chans {
		if newCh.ChannelType() != "direct-tcpip" {
			_ = newCh.Reject(gossh.UnknownChannelType, "unsupported")
			continue
		}
		s.handleDirectTCPIP(newCh)
	}
}

// directTCPIPMsg 是 RFC 4254 §7.2 的 direct-tcpip 通道负载。
type directTCPIPMsg struct {
	Addr       string
	Port       uint32
	OriginAddr string
	OriginPort uint32
}

func (s *Server) handleDirectTCPIP(newCh gossh.NewChannel) {
	payload := newCh.ExtraData()
	var m directTCPIPMsg
	if err := gossh.Unmarshal(payload, &m); err != nil {
		_ = newCh.Reject(gossh.Prohibited, "bad payload")
		return
	}
	target := net.JoinHostPort(m.Addr, strconv.Itoa(int(m.Port)))
	var upstream net.Conn
	var err error
	if s.NextDial != nil {
		upstream, err = s.NextDial(target)
	} else {
		upstream, err = net.Dial("tcp", target)
	}
	if err != nil {
		_ = newCh.Reject(gossh.ConnectionFailed, err.Error())
		return
	}
	ch, reqs, err := newCh.Accept()
	if err != nil {
		_ = upstream.Close()
		return
	}
	go gossh.DiscardRequests(reqs)
	go func() {
		defer upstream.Close()
		defer ch.Close()
		pipe(ch, upstream)
	}()
}

// writeCloseSink 是 ssh.Channel 与 net.Conn 共有的半关能力(io.WriteCloser
// 本身没有 CloseWrite,必须用这个窄接口)。
type writeCloseSink interface {
	io.Writer
	CloseWrite() error
}

// pipe 双向搬运并逐侧半关:一侧 EOF → CloseWrite 对侧 → 对侧也 EOF → 收尾。
// a、b 的动态类型都必须实现 writeCloseSink,否则 panic 并指认具体类型
// (裸断言失败只会崩整个测试二进制且不带任何信息)。
func pipe(a io.ReadWriter, b io.ReadWriter) {
	done := make(chan struct{}, 2)
	copySide := func(dst writeCloseSink, src io.Reader) {
		_, _ = io.Copy(struct{ io.Writer }{dst}, src)
		_ = dst.CloseWrite()
		done <- struct{}{}
	}
	asink, ok := a.(writeCloseSink)
	if !ok {
		panic("sshtest: conn 必须实现 CloseWrite: " + fmt.Sprintf("%T", a))
	}
	bsink, ok := b.(writeCloseSink)
	if !ok {
		panic("sshtest: conn 必须实现 CloseWrite: " + fmt.Sprintf("%T", b))
	}
	go copySide(bsink, a)
	go copySide(asink, b)
	<-done
	<-done
}

func (s *Server) handleReqs(reqs <-chan *gossh.Request) {
	for req := range reqs {
		if req.WantReply {
			_ = req.Reply(false, nil)
		}
	}
}

// chanAddr 是 WrapChannel 占位用的 net.Addr。
type chanAddr struct{}

func (chanAddr) Network() string { return "ssh-channel" }
func (chanAddr) String() string  { return "ssh-channel" }

// chanConn 把 gossh.Channel 适配成 net.Conn:NextDial 要返回下一跳 sshd 的
// direct-tcpip 通道,而 gossh.Channel 本身不是 net.Conn(无 addr/deadline)。
// deadline 为 no-op:pipe 不依赖它们。
type chanConn struct {
	ch gossh.Channel
}

// WrapChannel 把下一跳 SSH 通道包装成 net.Conn,供 NextDial 返回。
func WrapChannel(ch gossh.Channel) net.Conn { return chanConn{ch: ch} }

func (c chanConn) Read(p []byte) (int, error)         { return c.ch.Read(p) }
func (c chanConn) Write(p []byte) (int, error)        { return c.ch.Write(p) }
func (c chanConn) Close() error                       { return c.ch.Close() }
func (c chanConn) CloseWrite() error                  { return c.ch.CloseWrite() }
func (c chanConn) LocalAddr() net.Addr                { return chanAddr{} }
func (c chanConn) RemoteAddr() net.Addr               { return chanAddr{} }
func (c chanConn) SetDeadline(t time.Time) error      { return nil }
func (c chanConn) SetReadDeadline(t time.Time) error  { return nil }
func (c chanConn) SetWriteDeadline(t time.Time) error { return nil }

// EchoListener 是 direct-tcpip 测试的回显目标,返回地址与幂等 close。
// 与 Start 对称,t.Cleanup 自动注册 close;返回的 close 供提前收尾用,重复调用安全。
func EchoListener(t *testing.T) (addr string, close func()) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("echo listen: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}(c)
		}
	}()
	return l.Addr().String(), func() { _ = l.Close() }
}
