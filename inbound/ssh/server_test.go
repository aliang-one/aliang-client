package ssh

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	M "aliang.one/nursorgate/inbound/tun/metadata"

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

// startTestServer 起一台内置 sshd(独立监听已退役,统一口注入承载——
// 测试经 serveConnVia 的测试 listener accept→ServeConn 注入),host key
// 落 t.TempDir,t.Cleanup 兜底停机。
func startTestServer(t *testing.T, rc RunConfig) (*Server, string) {
	t.Helper()
	return startTestServerAt(t, rc, filepath.Join(t.TempDir(), IngressHostKeyFileName))
}

func startTestServerAt(t *testing.T, rc RunConfig, hostKeyPath string) (*Server, string) {
	t.Helper()
	s := NewServer(rc, hostKeyPath)
	if err := s.Start(); err != nil {
		t.Fatalf("start ssh ingress: %v", err)
	}
	t.Cleanup(func() { _ = s.Stop() })
	return s, serveConnVia(t, s)
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
	path := filepath.Join(t.TempDir(), IngressHostKeyFileName)
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
	// 指纹不变量(Task 7 附带断言):文件解析路径(ReadHostKeyInfo,指纹
	// API 的口径)与服务端加载路径(Server.Start 落盘→加载)必须产出同一
	// 指纹——两条路径若出现格式漂移,仪表盘展示的指纹就会与实服务钥错位,
	// 客户端 known_hosts 核对失真。
	info, err := ReadHostKeyInfo(path)
	if err != nil {
		t.Fatalf("ReadHostKeyInfo(%s): %v", path, err)
	}
	if info.Fingerprint != fp1 {
		t.Fatalf("file-parsed fingerprint %q must equal server-loaded fingerprint %q (no drift between parse paths)", info.Fingerprint, fp1)
	}
}

func TestSSHIngressHostKeyRegeneratedOnCorrupt(t *testing.T) {
	humanPub, _ := newTestKey(t)
	path := filepath.Join(t.TempDir(), IngressHostKeyFileName)
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

	// 运行中:注入缝可达(错误要么 nil 要么是认证层错误,不能是拨号层拒绝)。
	_, err := dialIngress(t, addr, gossh.PublicKeys(signer))
	if err != nil && strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("server must be reachable while running: %v", err)
	}

	// 停机(=reconcile 层开关关/未启动):注入连接被立即关闭,握手必失败
	// (统一口收编后测试 listener 仍在,拒绝语义从「TCP 拒连」变为「连接
	// 被入口拒收即断」;生产里统一口分派见 HandleIngressConn 返回 false)。
	// 断言:失败必须发生在传输层,不得落到认证层(那意味着入口还在服务)。
	if err := s.Stop(); err != nil {
		t.Fatalf("stop: %v", err)
	}
	_, err = dialIngress(t, addr, gossh.PublicKeys(signer))
	if err == nil {
		t.Fatal("dial must fail after stop")
	}
	if strings.Contains(err.Error(), "unable to authenticate") {
		t.Fatalf("stopped server must cut the connection before auth, got auth-level error: %v", err)
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
	// 打桩管线(swap 纪律同 inject_test.go):消除对真实管线单例的隐式
	// 依赖与 Error 日志噪音——direct-tcpip 尾段不真拨 127.0.0.1:9。
	swapTCPHandler(t, func(ctx context.Context, conn net.Conn, metadata *M.Metadata) error {
		_, _ = io.Copy(io.Discard, conn)
		return nil
	})
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
		// bare form 只放行 direct-tcpip,其余 channel 全按矩阵判「类型不符」
		// →Prohibited(UnknownChannelType 保留给未知 credential form,
		// TestMeshExitMatrix 钉)。
		var oce *gossh.OpenChannelError
		if !errors.As(err, &oce) || oce.Reason != gossh.Prohibited {
			t.Fatalf("channel %q rejection reason = %v, want Prohibited", chType, err)
		}
	}
	// direct-tcpip 是 bare form 唯一放行通道:Accept 后注入(本用例已打桩,
	// 桩排空即返回→关通道),客户端半关后读到 EOF。
	payload := gossh.Marshal(directTCPIPMsg{Addr: "127.0.0.1", Port: 9, OriginAddr: "127.0.0.1", OriginPort: 0})
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

// TestSSHIngressHandshakeDeadlineCutsStalledConn 钉住握手 deadline:挂起
// 客户端(连接后一个字节都不发)必须在 handshakeTimeout 内被服务端掐断。
// 注入 250ms 短超时跑(生产值 15s,不实等);客户端读侧自设 3s 上限,
// 若先撞自己的读超时(而非收到服务端 EOF)即判失败—— deadline 未生效。
// 正常握手不受影响由既有认证/通道用例背书(它们在 15s 默认值下握手即过)。
func TestSSHIngressHandshakeDeadlineCutsStalledConn(t *testing.T) {
	defer func(d time.Duration) { handshakeTimeout = d }(handshakeTimeout)
	handshakeTimeout = 250 * time.Millisecond

	humanPub, _ := newTestKey(t)
	_, addr := startTestServer(t, RunConfig{AuthorizedKeys: []gossh.PublicKey{humanPub}})

	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	// 一个字节不发,等 deadline 到期后服务端关闭连接(客户端读到 EOF)。
	// 服务端握手会先发自身 version banner,读循环丢弃它直到出错为止。
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	start := time.Now()
	buf := make([]byte, 256)
	var readErr error
	for {
		if _, readErr = conn.Read(buf); readErr != nil {
			break
		}
	}
	if ne, ok := readErr.(net.Error); ok && ne.Timeout() {
		t.Fatalf("stalled conn outlived handshake deadline: client hit its own 3s read timeout after %v", time.Since(start))
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("conn cut too late: %v (want ~handshakeTimeout)", elapsed)
	}
}

func TestAuthHumanKeyMarksBareForm(t *testing.T) {
	// 打桩管线(swap 纪律同 inject_test.go):fire-and-forget 打开不触达
	// 真实管线单例,免 Error 日志噪音。
	swapTCPHandler(t, func(ctx context.Context, conn net.Conn, metadata *M.Metadata) error {
		_, _ = io.Copy(io.Discard, conn)
		return nil
	})
	humanPub, humanSigner := newTestKey(t)
	meshPub, _ := newTestKey(t)
	s, addr := startTestServer(t, RunConfig{
		AuthorizedKeys: []gossh.PublicKey{humanPub},
		MeshKeys:       []MeshKey{{Key: meshPub}},
	})
	conn, err := dialIngress(t, addr, gossh.PublicKeys(humanSigner))
	if err != nil {
		t.Fatalf("human key must authenticate: %v", err)
	}
	// fire-and-forget 打开(已打桩):本用例只断言形态标记,通道生命周期
	// 随连接清理收口。
	payload := gossh.Marshal(directTCPIPMsg{Addr: "127.0.0.1", Port: 9, OriginAddr: "127.0.0.1", OriginPort: 0})
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
		MeshKeys:       []MeshKey{{Key: meshPub, DeviceID: "dev-b"}},
	})
	conn, err := dialIngress(t, addr, gossh.PublicKeys(meshSigner))
	if err != nil {
		t.Fatalf("mesh key must authenticate: %v", err)
	}
	// 矩阵(SPEC §4.2):mesh form 只放行 mesh-exit,direct-tcpip 在 Accept
	// 前按「类型不符」拒(Prohibited);观测面在拒绝判定之前留痕。
	payload := gossh.Marshal(directTCPIPMsg{Addr: "127.0.0.1", Port: 9, OriginAddr: "127.0.0.1", OriginPort: 0})
	_, _, openErr := conn.OpenChannel(channelDirectTCPIP, payload)
	var oce *gossh.OpenChannelError
	if !errors.As(openErr, &oce) || oce.Reason != gossh.Prohibited {
		t.Fatalf("mesh form × direct-tcpip rejection = %v, want Prohibited", openErr)
	}
	if got := s.observedForm(); got != FormMesh {
		t.Fatalf("ingress form = %q, want %q", got, FormMesh)
	}
	if got := s.observedDeviceID(); got != "dev-b" {
		t.Fatalf("device id = %q, want dev-b", got)
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
	// 运行期第二道防线——mesh 优先(保守),且 DeviceID 取 mesh 条目
	// (裸钥没有 DeviceID 概念,双钥各带不同标签、各自透传 extMeshDeviceID)。
	dualPub, dualSigner := newTestKey(t)
	secondPub, secondSigner := newTestKey(t)
	s, addr := startTestServer(t, RunConfig{
		AuthorizedKeys: []gossh.PublicKey{dualPub},
		MeshKeys: []MeshKey{
			{Key: dualPub, DeviceID: "dev-dual"},
			{Key: secondPub, DeviceID: "dev-second"},
		},
	})
	payload := gossh.Marshal(directTCPIPMsg{Addr: "example.com", Port: 443, OriginAddr: "127.0.0.1", OriginPort: 0})

	conn, err := dialIngress(t, addr, gossh.PublicKeys(dualSigner))
	if err != nil {
		t.Fatalf("dual-listed key must authenticate: %v", err)
	}
	// mesh form × direct-tcpip 按矩阵拒(Accept 前,Prohibited);观测面
	// 在拒绝判定之前留痕(先读 form 再 Reject 的次序)。
	if _, _, openErr := conn.OpenChannel(channelDirectTCPIP, payload); openErr == nil {
		t.Fatal("mesh form × direct-tcpip must be rejected by the form×channel matrix")
	}
	if got := s.observedForm(); got != FormMesh {
		t.Fatalf("dual-listed key must conservatively map to %q, got %q", FormMesh, got)
	}
	if got := s.observedDeviceID(); got != "dev-dual" {
		t.Fatalf("dual-listed key DeviceID = %q, want %q (mesh entry wins the label)", got, "dev-dual")
	}

	// 第二把 mesh 钥:各自透传自己的 extMeshDeviceID(观测点是最近一条
	// 通道,新连接覆盖前值)。
	conn2, err := dialIngress(t, addr, gossh.PublicKeys(secondSigner))
	if err != nil {
		t.Fatalf("second mesh key must authenticate: %v", err)
	}
	if _, _, openErr := conn2.OpenChannel(channelDirectTCPIP, payload); openErr == nil {
		t.Fatal("mesh form × direct-tcpip must be rejected by the form×channel matrix (second)")
	}
	if got := s.observedDeviceID(); got != "dev-second" {
		t.Fatalf("second mesh key DeviceID = %q, want %q", got, "dev-second")
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

func TestAuthLimiterIdleEviction(t *testing.T) {
	now := time.Unix(1700000000, 0)
	l := newAuthLimiter()
	l.now = func() time.Time { return now }
	ip := "203.0.113.9"

	// fails=3(窗口 8s)。恰好在 TTL 边界(未超过)不驱逐:条目存续,
	// 再失败窗口续涨到 16s。
	for i := 0; i < 3; i++ {
		l.Fail(ip)
	}
	now = now.Add(limiterIdleTTL) // 恰好 =,不触发驱逐
	if !l.Allow(ip) {
		t.Fatal("exactly-at-TTL entry must not be evicted (window long elapsed)")
	}
	l.Fail(ip) // 若条目存续则 fails=4→16s;若被误驱逐则 fails=1→2s
	now = now.Add(3 * time.Second)
	if l.Allow(ip) {
		t.Fatal("entry surviving exact-TTL must escalate its window (fails=4→16s)")
	}
	now = now.Add(13 * time.Second) // 16s 窗口走完
	if !l.Allow(ip) {
		t.Fatal("window elapsed must allow")
	}

	// 超过 TTL(严格 >):驱逐并放行,fails 衰减归零——再失败从 2s 窗口
	// 重新起步(否则攻击停止数小时后该 IP 仍被 30s 帽永久限速)。
	now = now.Add(limiterIdleTTL + time.Second)
	if !l.Allow(ip) {
		t.Fatal("entry past idle TTL must be evicted and allowed")
	}
	l.Fail(ip)
	now = now.Add(1999 * time.Millisecond)
	if l.Allow(ip) {
		t.Fatal("post-eviction failure must restart backoff from the 2s window")
	}
	now = now.Add(2 * time.Millisecond)
	if !l.Allow(ip) {
		t.Fatal("restarted 2s window elapsed must allow")
	}
}

func TestAuthLimiterCapacitySweep(t *testing.T) {
	now := time.Unix(1700000000, 0)
	l := newAuthLimiter()
	l.now = func() time.Time { return now }

	// 前半填充(将被清扫的过期条目)。
	for i := 0; i < limiterMaxEntries/2; i++ {
		l.Fail(fmt.Sprintf("10.1.%d.%d", i/256, i%256))
	}
	now = now.Add(limiterIdleTTL + time.Minute) // 前半过期
	// 后半填充至满(新鲜条目)。
	for i := limiterMaxEntries / 2; i < limiterMaxEntries; i++ {
		l.Fail(fmt.Sprintf("10.1.%d.%d", i/256, i%256))
	}
	if len(l.entries) != limiterMaxEntries {
		t.Fatalf("entries = %d, want %d", len(l.entries), limiterMaxEntries)
	}
	// 容量满但过半过期:新 IP 的 Fail 先整表清扫腾位,再记账成功。
	l.Fail("192.0.2.99")
	if _, ok := l.entries["192.0.2.99"]; !ok {
		t.Fatal("Fail at cap must sweep expired entries and record the new IP")
	}
	if len(l.entries) > limiterMaxEntries {
		t.Fatalf("entries = %d exceeds cap %d", len(l.entries), limiterMaxEntries)
	}

	// 全部新鲜打满:清扫无腾位,新 IP 记账被丢弃(内存有界取舍);
	// 已有条目的更新不受容量约束(map 不增长)。
	now = now.Add(time.Minute)
	for i := 0; len(l.entries) < limiterMaxEntries && i < 2*limiterMaxEntries; i++ {
		l.Fail(fmt.Sprintf("198.18.%d.%d", i/256, i%256))
	}
	if len(l.entries) != limiterMaxEntries {
		t.Fatalf("fill to cap: entries = %d, want %d", len(l.entries), limiterMaxEntries)
	}
	l.Fail("192.0.2.100")
	if _, ok := l.entries["192.0.2.100"]; ok {
		t.Fatal("new entry must be dropped when cap holds after sweep")
	}
	existing := "198.18.0.0"
	before := l.entries[existing].fails
	l.Fail(existing)
	if l.entries[existing].fails != before+1 {
		t.Fatal("updating an existing entry must stay effective at cap")
	}
	if len(l.entries) != limiterMaxEntries {
		t.Fatalf("existing-entry update must not grow the map: %d", len(l.entries))
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

// ===== 可选专用口(spec §6.4 修订:port 显式指定另开常驻监听口) =====

// TestSSHIngressDedicatedPortListens 显式端口 → Start 起专用监听口(与统一口
// 注入缝并存,同一实例双路径);Addr 可拨且读到 SSH 横幅。
func TestSSHIngressDedicatedPortListens(t *testing.T) {
	humanPub, signer := newTestKey(t)
	fixed := freeTCPPortForDedicated(t)
	s := NewServer(RunConfig{Port: fixed, AuthorizedKeys: []gossh.PublicKey{humanPub}}, filepath.Join(t.TempDir(), "k"))
	if err := s.Start(); err != nil {
		t.Fatalf("start with dedicated port: %v", err)
	}
	t.Cleanup(func() { _ = s.Stop() })

	addr := s.Addr()
	if addr == "" {
		t.Fatal("dedicated-port server must expose listen addr")
	}
	if _, port, err := net.SplitHostPort(addr); err != nil || port != strconv.Itoa(int(fixed)) {
		t.Fatalf("Addr = %q, want port %d", addr, fixed)
	}
	conn, err := dialIngress(t, net.JoinHostPort("127.0.0.1", strconv.Itoa(int(fixed))), gossh.PublicKeys(signer))
	if err != nil {
		t.Fatalf("dial dedicated port: %v", err)
	}
	_ = conn.Close()

	// 停机后专用口连接层拒绝。
	if err := s.Stop(); err != nil {
		t.Fatalf("stop: %v", err)
	}
	dialErr := dialRawRefused(t, net.JoinHostPort("127.0.0.1", strconv.Itoa(int(fixed))))
	if dialErr == nil {
		t.Fatal("dedicated port must be closed after stop")
	}
}

// TestSSHIngressUnifiedPortNoListener port=0(跟随统一口)→ 不起专用监听,
// Addr 为空;注入缝(ServeConn)照常服务。
func TestSSHIngressUnifiedPortNoListener(t *testing.T) {
	humanPub, humanSigner := newTestKey(t)
	s := NewServer(RunConfig{AuthorizedKeys: []gossh.PublicKey{humanPub}}, filepath.Join(t.TempDir(), "k"))
	if err := s.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = s.Stop() })

	if got := s.Addr(); got != "" {
		t.Fatalf("unified-port server must not listen, Addr = %q", got)
	}
	// 注入缝走本地实例(非 reconcile 单例):桥接对+完整握手。
	client, server := bridgedPair()
	defer client.Close()
	_ = client.SetDeadline(time.Now().Add(5 * time.Second))
	go s.ServeConn(server)
	cfg := &gossh.ClientConfig{
		User:            "ingress",
		Auth:            []gossh.AuthMethod{gossh.PublicKeys(humanSigner)},
		HostKeyCallback: gossh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	}
	conn, _, _, err := gossh.NewClientConn(client, "127.0.0.1:0", cfg)
	if err != nil {
		t.Fatalf("injection seam must still serve: %v", err)
	}
	_ = conn.Close()
}

// freeTCPPortForDedicated 预约空闲回环端口(专用口用例选口)。
func freeTCPPortForDedicated(t *testing.T) uint16 {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	defer l.Close()
	return uint16(l.Addr().(*net.TCPAddr).Port)
}

// dialRawRefused 裸拨号判连接层拒绝(拿到错误=拒绝/超时;nil=有连接)。
func dialRawRefused(t *testing.T, addr string) error {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, time.Second)
	if err == nil {
		_ = conn.Close()
	}
	return err
}
