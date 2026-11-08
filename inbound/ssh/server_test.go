package ssh

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	gossh "golang.org/x/crypto/ssh"
)

// newTestKey 生成一对 ed25519 测试密钥:公钥(配进 RunConfig 凭证列表)
// 与客户端签名器。
func newTestKey(t *testing.T) (gossh.PublicKey, gossh.Signer) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519 generate: %v", err)
	}
	signer, err := gossh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	return signer.PublicKey(), signer
}

// startTestServer 起一台内置 sshd(Port=0 随机口),host key 落 t.TempDir,
// t.Cleanup 兜底停机。
func startTestServer(t *testing.T, rc RunConfig) (*Server, string) {
	t.Helper()
	return startTestServerAt(t, rc, filepath.Join(t.TempDir(), "ingress_host_key"))
}

func startTestServerAt(t *testing.T, rc RunConfig, hostKeyPath string) (*Server, string) {
	t.Helper()
	s := NewServer(rc, hostKeyPath)
	if err := s.Start(); err != nil {
		t.Fatalf("start ssh ingress: %v", err)
	}
	t.Cleanup(func() { _ = s.Stop() })
	return s, dialAddr(t, s)
}

// dialAddr 取服务实际监听口并拼回环地址(ServiceBindHost 默认 127.0.0.1,
// 显式拼 127.0.0.1 对 --host 覆盖场景也稳)。
func dialAddr(t *testing.T, s *Server) string {
	t.Helper()
	addr := s.Addr()
	if addr == "" {
		t.Fatal("server not running")
	}
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("split addr %s: %v", addr, err)
	}
	return net.JoinHostPort("127.0.0.1", port)
}

// dialIngress 用给定认证方式连入口。成功时注册 t.Cleanup 关闭;失败时
// 返回错误供调用方断言(认证失败/限速预断/连接拒绝可区分)。
func dialIngress(t *testing.T, addr string, authMethods ...gossh.AuthMethod) (gossh.Conn, error) {
	t.Helper()
	cfg := &gossh.ClientConfig{
		User: "ingress",
		Auth: authMethods,
		// 环回测试不校验 host key(host key 自管测试另测)。
		HostKeyCallback: gossh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	}
	conn, err := gossh.Dial("tcp", addr, cfg)
	if err == nil {
		t.Cleanup(func() { _ = conn.Close() })
	}
	return conn, err
}

// directTCPIPMsgMirror 与 RFC 4254 §7.2 direct-tcpip 负载同构
// (出口侧 sshtest 先例;channel.go 解析在 Task 4 落地)。
type directTCPIPMsgMirror struct {
	Addr       string
	Port       uint32
	OriginAddr string
	OriginPort uint32
}

func TestSSHIngressStartStopIsRunning(t *testing.T) {
	humanPub, _ := newTestKey(t)
	s := NewServer(RunConfig{AuthorizedKeys: []gossh.PublicKey{humanPub}}, filepath.Join(t.TempDir(), "k"))
	if s.IsRunning() {
		t.Fatal("fresh server must not be running")
	}
	if err := s.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	if !s.IsRunning() {
		t.Fatal("started server must be running")
	}
	if err := s.Start(); err != nil {
		t.Fatalf("second start must be idempotent: %v", err)
	}
	if !s.IsRunning() {
		t.Fatal("idempotent start must keep server running")
	}
	if err := s.Stop(); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if s.IsRunning() {
		t.Fatal("stopped server must not be running")
	}
	if err := s.Stop(); err != nil {
		t.Fatalf("second stop must be idempotent: %v", err)
	}
}

func TestSSHIngressHostKeyStableAcrossRestarts(t *testing.T) {
	humanPub, _ := newTestKey(t)
	path := filepath.Join(t.TempDir(), "ingress_host_key")
	rc := RunConfig{AuthorizedKeys: []gossh.PublicKey{humanPub}}

	s1 := NewServer(rc, path)
	if err := s1.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	fp1, algo := s1.HostKeyFingerprint()
	if err := s1.Stop(); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if !strings.HasPrefix(fp1, "SHA256:") || algo != "ssh-ed25519" {
		t.Fatalf("unexpected fingerprint/algorithm: %q %q", fp1, algo)
	}

	s2 := NewServer(rc, path)
	if err := s2.Start(); err != nil {
		t.Fatalf("restart: %v", err)
	}
	defer func() { _ = s2.Stop() }()
	if fp2, _ := s2.HostKeyFingerprint(); fp2 != fp1 {
		t.Fatalf("host key fingerprint must survive restarts: %q -> %q", fp1, fp2)
	}
}

func TestSSHIngressHostKeyRegeneratedOnCorrupt(t *testing.T) {
	humanPub, _ := newTestKey(t)
	path := filepath.Join(t.TempDir(), "ingress_host_key")
	rc := RunConfig{AuthorizedKeys: []gossh.PublicKey{humanPub}}

	s1 := NewServer(rc, path)
	if err := s1.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	fp1, _ := s1.HostKeyFingerprint()
	if err := s1.Stop(); err != nil {
		t.Fatalf("stop: %v", err)
	}

	// 覆写为损坏内容:重启必须重生成(指纹变化)且落盘为可解析 PEM。
	if err := os.WriteFile(path, []byte("this is not a pem private key"), 0o600); err != nil {
		t.Fatalf("corrupt host key: %v", err)
	}
	s2 := NewServer(rc, path)
	if err := s2.Start(); err != nil {
		t.Fatalf("start with corrupt key: %v", err)
	}
	defer func() { _ = s2.Stop() }()
	fp2, _ := s2.HostKeyFingerprint()
	if fp2 == "" || fp2 == fp1 {
		t.Fatalf("corrupt host key must be regenerated with a new fingerprint: old=%q new=%q", fp1, fp2)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read regenerated key: %v", err)
	}
	if _, err := gossh.ParsePrivateKey(data); err != nil {
		t.Fatalf("regenerated key file must parse: %v", err)
	}
}

func TestSSHIngressDisabledRefuses(t *testing.T) {
	humanPub, signer := newTestKey(t)
	s, addr := startTestServer(t, RunConfig{AuthorizedKeys: []gossh.PublicKey{humanPub}})

	// 运行中:TCP 层可达(错误要么 nil 要么是认证层错误,不能是拨号层拒绝)。
	_, err := dialIngress(t, addr, gossh.PublicKeys(signer))
	if err != nil && strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("server must be reachable while running: %v", err)
	}

	// 停机(=reconcile 层开关关/未启动):连接层直接拒绝。
	if err := s.Stop(); err != nil {
		t.Fatalf("stop: %v", err)
	}
	_, err = dialIngress(t, addr, gossh.PublicKeys(signer))
	if err == nil {
		t.Fatal("dial must fail after stop")
	}
	if !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("stopped server must refuse at connect layer, got: %v", err)
	}
}

func TestSSHIngressPasswordAuthNotRegistered(t *testing.T) {
	humanPub, _ := newTestKey(t)
	// 实证口径(spec §6「密码不注册」):服务端唯一注册的认证方法是
	// publickey,x/crypto 客户端只尝试服务端播报列表内的方法,密码凭证
	// 根本不会上线——报错为 "attempted methods [none]"。
	// 每子测独立起服务:认证失败会计入限速,复用同一实例会让第二个子测
	// 在退避窗口内被握手前掐断,断言「unable to authenticate」就不再成立。
	for _, tc := range []struct {
		name string
		auth gossh.AuthMethod
	}{
		{"password", gossh.Password("hunter2")},
		{"keyboard-interactive", gossh.KeyboardInteractive(func(name, instruction string, questions []string, echos []bool) ([]string, error) {
			return nil, nil
		})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, addr := startTestServer(t, RunConfig{AuthorizedKeys: []gossh.PublicKey{humanPub}})
			_, err := dialIngress(t, addr, tc.auth)
			if err == nil {
				t.Fatalf("%s auth must be rejected: no such callback registered", tc.name)
			}
			if !strings.Contains(err.Error(), "unable to authenticate") {
				t.Fatalf("expected unable-to-authenticate error, got: %v", err)
			}
			if !strings.Contains(err.Error(), "attempted methods [none]") {
				t.Fatalf("%s credentials must never reach the wire (server advertises publickey only): %v", tc.name, err)
			}
		})
	}
}

func TestSSHIngressRateLimitsAuthFailures(t *testing.T) {
	humanPub, _ := newTestKey(t)
	_, unknown := newTestKey(t)
	_, addr := startTestServer(t, RunConfig{AuthorizedKeys: []gossh.PublicKey{humanPub}})

	// 第一次未知钥:标准认证拒绝。
	_, err := dialIngress(t, addr, gossh.PublicKeys(unknown))
	if err == nil || !strings.Contains(err.Error(), "unable to authenticate") {
		t.Fatalf("unknown key must fail authentication: %v", err)
	}
	// 退避窗口内(fails=1→2s)再连:握手前直接断开——错误不再是认证失败,
	// 而是版本交换阶段的 EOF/连接重置(与认证拒绝文案可区分)。
	_, err = dialIngress(t, addr, gossh.PublicKeys(unknown))
	if err == nil {
		t.Fatal("rate-limited dial must fail")
	}
	if strings.Contains(err.Error(), "unable to authenticate") {
		t.Fatalf("second dial should be cut before auth (rate limited), got auth error: %v", err)
	}
}

func TestSSHIngressRejectsNonDirectTCPIPChannels(t *testing.T) {
	humanPub, signer := newTestKey(t)
	_, addr := startTestServer(t, RunConfig{AuthorizedKeys: []gossh.PublicKey{humanPub}})
	conn, err := dialIngress(t, addr, gossh.PublicKeys(signer))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	for _, chType := range []string{"session", "forwarded-tcpip", "auth-agent@openssh.com", "x11"} {
		ch, _, err := conn.OpenChannel(chType, nil)
		if err == nil {
			_ = ch.Close()
			t.Fatalf("channel %q must be rejected", chType)
		}
		var oce *gossh.OpenChannelError
		if !errors.As(err, &oce) || oce.Reason != gossh.UnknownChannelType {
			t.Fatalf("channel %q rejection reason = %v, want UnknownChannelType", chType, err)
		}
	}
	// direct-tcpip 是唯一放行通道:Accept 后(Task 2 边界)drain+close,
	// 客户端半关后读到 EOF。
	payload := gossh.Marshal(directTCPIPMsgMirror{Addr: "127.0.0.1", Port: 9, OriginAddr: "127.0.0.1", OriginPort: 0})
	ch, _, err := conn.OpenChannel(channelDirectTCPIP, payload)
	if err != nil {
		t.Fatalf("direct-tcpip open: %v", err)
	}
	if _, err := ch.Write([]byte("ping")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := ch.CloseWrite(); err != nil {
		t.Fatalf("close write: %v", err)
	}
	if _, err := ch.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("drained channel should EOF after server close, got %v", err)
	}
}

func TestSSHIngressGlobalRequests(t *testing.T) {
	humanPub, signer := newTestKey(t)
	_, addr := startTestServer(t, RunConfig{AuthorizedKeys: []gossh.PublicKey{humanPub}})
	conn, err := dialIngress(t, addr, gossh.PublicKeys(signer))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	ok, _, err := conn.SendRequest("keepalive@openssh.com", true, nil)
	if err != nil || !ok {
		t.Fatalf("keepalive must be affirmed: ok=%v err=%v", ok, err)
	}
	// 其余全局请求(tcpip-forward 等)一律拒绝:入口不提供服务端反向转发。
	ok, _, err = conn.SendRequest("tcpip-forward", true, nil)
	if err != nil || ok {
		t.Fatalf("other global requests must be denied: ok=%v err=%v", ok, err)
	}
}

func TestAuthHumanKeyMarksBareForm(t *testing.T) {
	humanPub, humanSigner := newTestKey(t)
	meshPub, _ := newTestKey(t)
	s, addr := startTestServer(t, RunConfig{
		AuthorizedKeys: []gossh.PublicKey{humanPub},
		MeshKeys:       []gossh.PublicKey{meshPub},
	})
	conn, err := dialIngress(t, addr, gossh.PublicKeys(humanSigner))
	if err != nil {
		t.Fatalf("human key must authenticate: %v", err)
	}
	payload := gossh.Marshal(directTCPIPMsgMirror{Addr: "127.0.0.1", Port: 9, OriginAddr: "127.0.0.1", OriginPort: 0})
	if _, _, err := conn.OpenChannel(channelDirectTCPIP, payload); err != nil {
		t.Fatalf("direct-tcpip open: %v", err)
	}
	// 认证 Permissions 的形态标记必须传到通道层(观测点在通道 Accept 侧)。
	if got := s.observedForm(); got != FormBare {
		t.Fatalf("ingress form = %q, want %q", got, FormBare)
	}
}

func TestAuthMeshKeyMarksMeshForm(t *testing.T) {
	humanPub, _ := newTestKey(t)
	meshPub, meshSigner := newTestKey(t)
	s, addr := startTestServer(t, RunConfig{
		AuthorizedKeys: []gossh.PublicKey{humanPub},
		MeshKeys:       []gossh.PublicKey{meshPub},
	})
	conn, err := dialIngress(t, addr, gossh.PublicKeys(meshSigner))
	if err != nil {
		t.Fatalf("mesh key must authenticate: %v", err)
	}
	payload := gossh.Marshal(directTCPIPMsgMirror{Addr: "127.0.0.1", Port: 9, OriginAddr: "127.0.0.1", OriginPort: 0})
	if _, _, err := conn.OpenChannel(channelDirectTCPIP, payload); err != nil {
		t.Fatalf("direct-tcpip open: %v", err)
	}
	// 帧处理在 Task 5 接;本断言=Accept 且形态标记为 mesh。
	if got := s.observedForm(); got != FormMesh {
		t.Fatalf("ingress form = %q, want %q", got, FormMesh)
	}
}

func TestAuthUnknownKeyRejectedAndRateLimited(t *testing.T) {
	humanPub, _ := newTestKey(t)
	_, unknown := newTestKey(t)
	s, addr := startTestServer(t, RunConfig{AuthorizedKeys: []gossh.PublicKey{humanPub}})

	_, err := dialIngress(t, addr, gossh.PublicKeys(unknown))
	if err == nil || !strings.Contains(err.Error(), "unable to authenticate") {
		t.Fatalf("unknown key must be rejected: %v", err)
	}
	// 服务端限速器已记账:回环 IP 进入退避窗口(fails=1→2s)。
	if s.limiter.Allow("127.0.0.1") {
		t.Fatal("unknown-key failure must put the source IP into a backoff window")
	}
	// 已知好钥同样被窗口拦住(限速在 IP 维度,不区分凭证)。
	if _, err := dialIngress(t, addr, gossh.PublicKeys(unknown)); err == nil ||
		strings.Contains(err.Error(), "unable to authenticate") {
		t.Fatalf("windowed IP must be cut before auth even with a valid key: %v", err)
	}
}

func TestAuthDualListedKeyMeshWinsAtRuntime(t *testing.T) {
	// 绕过配置层 Validate(同键双列表歧义拦截)直构造 RunConfig:
	// 运行期第二道防线——mesh 优先(保守)。
	dualPub, dualSigner := newTestKey(t)
	s, addr := startTestServer(t, RunConfig{
		AuthorizedKeys: []gossh.PublicKey{dualPub},
		MeshKeys:       []gossh.PublicKey{dualPub},
	})
	conn, err := dialIngress(t, addr, gossh.PublicKeys(dualSigner))
	if err != nil {
		t.Fatalf("dual-listed key must authenticate: %v", err)
	}
	payload := gossh.Marshal(directTCPIPMsgMirror{Addr: "example.com", Port: 443, OriginAddr: "127.0.0.1", OriginPort: 0})
	if _, _, err := conn.OpenChannel(channelDirectTCPIP, payload); err != nil {
		t.Fatalf("direct-tcpip open: %v", err)
	}
	if got := s.observedForm(); got != FormMesh {
		t.Fatalf("dual-listed key must conservatively map to %q, got %q", FormMesh, got)
	}
}

func TestAuthLimiterBackoffWindows(t *testing.T) {
	now := time.Unix(1700000000, 0)
	l := newAuthLimiter()
	l.now = func() time.Time { return now }

	ip := "203.0.113.7"
	if !l.Allow(ip) {
		t.Fatal("fresh IP must be allowed")
	}
	// 窗口公式:1s<<min(fails,5) 封顶 30s(fails=5→32s 落帽)。
	steps := []struct {
		fails  int
		window time.Duration
	}{
		{1, 2 * time.Second}, {2, 4 * time.Second}, {3, 8 * time.Second},
		{4, 16 * time.Second}, {5, 30 * time.Second}, {6, 30 * time.Second},
	}
	cur := 0
	for _, st := range steps {
		for cur < st.fails {
			l.Fail(ip)
			cur++
		}
		now = now.Add(st.window - time.Millisecond)
		if l.Allow(ip) {
			t.Fatalf("fails=%d: must be blocked inside window %v", st.fails, st.window)
		}
		now = now.Add(2 * time.Millisecond)
		if !l.Allow(ip) {
			t.Fatalf("fails=%d: must be allowed after window %v", st.fails, st.window)
		}
	}
	// 成功认证清零:再失败从 2s 窗口重新起步。
	l.Reset(ip)
	l.Fail(ip)
	now = now.Add(1999 * time.Millisecond)
	if l.Allow(ip) {
		t.Fatal("after reset, a single failure must still back off for 2s")
	}
}

func TestAuthLimiterConcurrent(t *testing.T) {
	l := newAuthLimiter()
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			ip := fmt.Sprintf("10.0.0.%d", g)
			for i := 0; i < 200; i++ {
				l.Allow(ip)
				l.Fail(ip)
				if i%50 == 0 {
					l.Reset(ip)
				}
			}
		}(g)
	}
	wg.Wait() // -race 检测器兜底:无数据竞争即过。
}
