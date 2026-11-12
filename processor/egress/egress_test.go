package egress

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	M "aliang.one/nursorgate/inbound/tun/metadata"
	"aliang.one/nursorgate/outbound/meshproto"
	mesh "aliang.one/nursorgate/outbound/proxy/mesh"
	"aliang.one/nursorgate/processor/config"
	gossh "golang.org/x/crypto/ssh"
)

// startEchoServer loopback 回显:收到什么回拷什么(测试基建,任务书定死)。
func startEchoServer(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}(conn)
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return ln.Addr().String()
}

// testIdentityKeyPEM 生成一把真 ed25519 测试身份(ExitClient 构造期消费,
// sshd 侧准入用同一把钥的公钥——两侧同源)。
func testIdentityKeyPEM(t *testing.T) (keyPEM string, signer gossh.Signer) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate ed25519: %v", err)
	}
	signer, err = gossh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("new signer: %v", err)
	}
	block, err := gossh.MarshalPrivateKey(priv, "egress-test-identity")
	if err != nil {
		t.Fatalf("marshal private key: %v", err)
	}
	return string(pem.EncodeToMemory(block)), signer
}

// meshTestSSHD 本包自建的最小真 sshd(与 outbound/proxy/mesh sshexit_test 同款
// 配方):固定 mesh 身份公钥准入,mesh-exit channel 的 ExtraData 逐条记录,
// httpMode=true 时对每条 channel 回一段最小 HTTP 200(平台流量的 HTTP 语义
// 全链路:client.Get 拿到合法响应才算通)。
type meshTestSSHD struct {
	ln       net.Listener
	hostFP   string
	httpMode bool

	mu       sync.Mutex
	payloads [][]byte
}

func startMeshTestSSHD(t *testing.T, admittedPub gossh.PublicKey, httpMode bool) *meshTestSSHD {
	t.Helper()
	_, hostPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate host key: %v", err)
	}
	hostSigner, err := gossh.NewSignerFromKey(hostPriv)
	if err != nil {
		t.Fatalf("new host signer: %v", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s := &meshTestSSHD{ln: ln, hostFP: gossh.FingerprintSHA256(hostSigner.PublicKey()), httpMode: httpMode}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go s.handle(c, hostSigner, admittedPub)
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return s
}

func (s *meshTestSSHD) Addr() string          { return s.ln.Addr().String() }
func (s *meshTestSSHD) HostKeyPinned() string { return s.hostFP }

// LastPayload 返回最近一条 mesh-exit ExtraData(到达序末条);尚无 channel
// 时返回 nil。
func (s *meshTestSSHD) LastPayload() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n := len(s.payloads); n > 0 {
		return s.payloads[n-1]
	}
	return nil
}

func (s *meshTestSSHD) record(payload []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.payloads = append(s.payloads, payload)
}

func (s *meshTestSSHD) handle(c net.Conn, hostSigner gossh.Signer, admittedPub gossh.PublicKey) {
	sc := &gossh.ServerConfig{
		PublicKeyCallback: func(_ gossh.ConnMetadata, key gossh.PublicKey) (*gossh.Permissions, error) {
			if !bytes.Equal(key.Marshal(), admittedPub.Marshal()) {
				return nil, errors.New("mesh test sshd: unknown client identity key")
			}
			return nil, nil
		},
	}
	sc.AddHostKey(hostSigner)
	_, chans, reqs, err := gossh.NewServerConn(c, sc)
	if err != nil {
		_ = c.Close()
		return
	}
	go gossh.DiscardRequests(reqs)
	for newCh := range chans {
		if newCh.ChannelType() != mesh.ChannelMeshExit {
			_ = newCh.Reject(gossh.UnknownChannelType, "unsupported")
			continue
		}
		s.record(newCh.ExtraData())
		ch, chReqs, err := newCh.Accept()
		if err != nil {
			continue
		}
		go gossh.DiscardRequests(chReqs)
		go func() {
			if s.httpMode {
				// 读一轮请求后回最小 HTTP 200(B 侧出口语义的最小等价物)。
				buf := make([]byte, 8192)
				if _, err := ch.Read(buf); err == nil {
					_, _ = ch.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nok"))
				}
			} else {
				_, _ = io.Copy(ch, ch) // 回显
			}
			_ = ch.Close()
		}()
	}
}

// closedPortAddr 监听后立即关闭的回环地址:拨它稳定 connection refused。
func closedPortAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

func boolPtr(b bool) *bool { return &b }

// setGlobalConfigForTest 保存/恢复全局配置(egress 门控逐连接读全局配置)。
func setGlobalConfigForTest(t *testing.T, cfg *config.Config) {
	t.Helper()
	prev := config.GetGlobalConfig()
	t.Cleanup(func() { config.SetGlobalConfig(prev) })
	config.SetGlobalConfig(cfg)
}

// setPeerSourceForTest 注入/恢复 peer 提供器,Cleanup 统一清空。
func setPeerSourceForTest(t *testing.T, fn func() mesh.Dialer) {
	t.Helper()
	SetPeerSource(fn)
	t.Cleanup(func() { SetPeerSource(nil) })
}

// setOfflineSignalForTest 注入/恢复离线信号,Cleanup 统一清空(未接线=在线)。
func setOfflineSignalForTest(t *testing.T, offline func() bool) {
	t.Helper()
	SetOfflineSignal(offline)
	t.Cleanup(func() { SetOfflineSignal(nil) })
}

// newTestExitPeer 按对端 LAN 地址装配 ExitClient(生产 egressPeer 同构装配:
// SSHUser="mesh"、真身份私钥)。
func newTestExitPeer(t *testing.T, keyPEM, lanAddr, hostKey string, relay bool, pikoDial mesh.PikoDialFunc) mesh.Dialer {
	t.Helper()
	spec := mesh.PeerSpec{
		DeviceID: "dev-b",
		LANAddrs: []string{lanAddr},
		HostKey:  hostKey,
		SSHUser:  "mesh",
		Relay:    relay,
	}
	client, err := mesh.NewExitClient(spec, keyPEM, "", pikoDial, nil)
	if err != nil {
		t.Fatalf("NewExitClient: %v", err)
	}
	return client
}

// fallbackSelfConfig 构造兜底使能且含 self 的最小配置。SSH 入口显式开:
// 判据切到 config.MeshEffectivelyEnabled 后,ssh_ingress 未置位会让兜底门
// 被依赖降级误关(非各用例要钉的条件)。
func fallbackSelfConfig() *config.Config {
	return &config.Config{Customer: &config.CustomerConfig{
		Mesh: &config.MeshConfig{Fallback: &config.MeshFallbackConfig{
			Enable:  boolPtr(true),
			Classes: []string{meshproto.ClassSelf},
			Addr:    "192.168.1.9:56440",
		}},
		SSHIngress: &config.SSHIngressConfig{Enable: boolPtr(true)},
	}}
}

// clearProxyEnv 防开发机 HTTP_PROXY 干扰非回环域名(platform.test)请求:
// 全清 + NO_PROXY 点名放行。
func clearProxyEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy"} {
		t.Setenv(k, "")
	}
	t.Setenv("NO_PROXY", "platform.test")
	t.Setenv("no_proxy", "platform.test")
}

// TestPlatformClientDirectWhenOnline 未注入 peer source / 兜底未配置 →
// DialContext 走 net.Dialer(loopback echo 可达即通;无任何 mesh 依赖)。
func TestPlatformClientDirectWhenOnline(t *testing.T) {
	setGlobalConfigForTest(t, &config.Config{})
	setPeerSourceForTest(t, nil)

	echoAddr := startEchoServer(t)
	client := PlatformClient(5 * time.Second)
	tr, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Transport 类型 = %T, want *http.Transport", client.Transport)
	}
	conn, err := tr.DialContext(context.Background(), "tcp", echoAddr)
	if err != nil {
		t.Fatalf("direct dial: %v", err)
	}
	payload := []byte("egress-direct-payload")
	if _, err := conn.Write(payload); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, len(payload))
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if string(buf) != string(payload) {
		t.Fatalf("echo mismatch: got %q want %q", buf, payload)
	}
	_ = conn.Close()

	// 完整请求链路同样直连可达(loopback 恒不被 env 代理)。
	var hits atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()
	resp, err := client.Get(ts.URL)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("direct hits = %d, want 1", got)
	}
}

// TestPlatformClientRoutesViaMeshWhenFallbackActive 兜底配置含 self 且 peer
// 可用 → 平台流量经 B:mesh-exit channel 的 ExtraData payload 携带平台域名
// 与端口(域名直转,不本地解析);Host 头为平台域名。
func TestPlatformClientRoutesViaMeshWhenFallbackActive(t *testing.T) {
	clearProxyEnv(t)
	keyPEM, identity := testIdentityKeyPEM(t)
	sshd := startMeshTestSSHD(t, identity.PublicKey(), true)
	setGlobalConfigForTest(t, fallbackSelfConfig())
	setOfflineSignalForTest(t, func() bool { return true })
	setPeerSourceForTest(t, func() mesh.Dialer {
		return newTestExitPeer(t, keyPEM, sshd.Addr(), sshd.HostKeyPinned(), false, nil)
	})

	client := PlatformClient(5 * time.Second)
	resp, err := client.Get("http://platform.test/v1/ping")
	if err != nil {
		t.Fatalf("get via mesh: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "ok" {
		t.Fatalf("resp = %d/%q, want 200/ok", resp.StatusCode, body)
	}

	// mesh-exit payload:平台域名:80(80 端口形态由 Transport 的 scheme 折算)。
	// Host 头语义经真 HTTP 全链路验证:响应成功即代表请求头已送达(读回的
	// HTTP 200 由本测试 sshd 在收到请求后回写)。
	if got := waitSSHDLastPayload(t, sshd); !bytes.Contains(got, []byte(`"h":"platform.test"`)) || !bytes.Contains(got, []byte(`"p":80`)) {
		t.Fatalf("payload = %s, want 携带 platform.test:80", got)
	}

	// 443 形态裸拨验证:mesh 拨出的是原始 TCP 流,TLS 握手在 Transport 层
	// 照常发生,无需特殊处理。
	tr := client.Transport.(*http.Transport)
	conn, err := tr.DialContext(context.Background(), "tcp", "platform.test:443")
	if err != nil {
		t.Fatalf("dial :443 via mesh: %v", err)
	}
	_ = conn.Close()
	if got := waitSSHDLastPayload(t, sshd); !bytes.Contains(got, []byte(`"p":443`)) {
		t.Fatalf("payload = %s, want 携带 platform.test:443", got)
	}
}

// waitSSHDLastPayload 等待 sshd 至少收到一条 mesh-exit channel 后返回其
// payload(3s 上限)。
func waitSSHDLastPayload(t *testing.T, s *meshTestSSHD) []byte {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if p := s.LastPayload(); p != nil {
			return p
		}
		if time.Now().After(deadline) {
			t.Fatal("timeout waiting for mesh-exit channel at fake sshd")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestPlatformClientFallsBackOnError mesh 拨号失败(假 peer 指向死端口)→
// direct 重试成功;direct 恰好一次(不循环)。
func TestPlatformClientFallsBackOnError(t *testing.T) {
	keyPEM, _ := testIdentityKeyPEM(t)
	dead := closedPortAddr(t)
	setGlobalConfigForTest(t, fallbackSelfConfig())
	setOfflineSignalForTest(t, func() bool { return true })
	setPeerSourceForTest(t, func() mesh.Dialer {
		return newTestExitPeer(t, keyPEM, dead, "SHA256:AAAA", false, nil)
	})

	var hits atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	client := PlatformClient(5 * time.Second)
	resp, err := client.Get(ts.URL)
	if err != nil {
		t.Fatalf("get after mesh failure: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("direct hits = %d, want 1(只重试一次,不循环)", got)
	}
}

// TestPlatformClientFallbackNotConfiguredIgnoresMesh 兜底未配置但 peer source
// 已注入 → 不走 mesh(直连),假 B 一个 channel 都收不到。
func TestPlatformClientFallbackNotConfiguredIgnoresMesh(t *testing.T) {
	keyPEM, identity := testIdentityKeyPEM(t)
	sshd := startMeshTestSSHD(t, identity.PublicKey(), false)
	setGlobalConfigForTest(t, &config.Config{Customer: &config.CustomerConfig{
		Mesh: &config.MeshConfig{}, // mesh 层存在但 Fallback 未配置
		// SSH 入口显式开:让「Fallback 未配置」成为唯一关闸条件(判据含
		// SSH 入口依赖后,不补则本用例被依赖降级误吞)。
		SSHIngress: &config.SSHIngressConfig{Enable: boolPtr(true)},
	}})
	setPeerSourceForTest(t, func() mesh.Dialer {
		return newTestExitPeer(t, keyPEM, sshd.Addr(), sshd.HostKeyPinned(), false, nil)
	})

	var hits atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	client := PlatformClient(5 * time.Second)
	resp, err := client.Get(ts.URL)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("direct hits = %d, want 1", got)
	}
	// 假 B 不应收到任何 channel(留 200ms 排除在途)。
	time.Sleep(200 * time.Millisecond)
	if sshd.LastPayload() != nil {
		t.Fatal("兜底未配置却走了 mesh")
	}
}

// assertGoesDirect 门控不激活的统一断言:live 假 B peer 已注入,请求仍必须
// 直连(httptest 计数==1)且假 B 零 channel。
func assertGoesDirect(t *testing.T, cfg *config.Config, offline func() bool) {
	t.Helper()
	keyPEM, identity := testIdentityKeyPEM(t)
	sshd := startMeshTestSSHD(t, identity.PublicKey(), false)
	setGlobalConfigForTest(t, cfg)
	setOfflineSignalForTest(t, offline)
	setPeerSourceForTest(t, func() mesh.Dialer {
		return newTestExitPeer(t, keyPEM, sshd.Addr(), sshd.HostKeyPinned(), false, nil)
	})

	var hits atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	client := PlatformClient(5 * time.Second)
	resp, err := client.Get(ts.URL)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("direct hits = %d, want 1", got)
	}
	time.Sleep(200 * time.Millisecond)
	if sshd.LastPayload() != nil {
		t.Fatal("不应走 mesh 却收到了 mesh-exit channel")
	}
}

// TestPlatformClientOnlineSignalBlocksMesh 离线信号为 false(甚至未接线)时,
// 即使兜底配置 + peer 齐全也不走 mesh(design §9.4「平台不可达才兜底」,
// 在线一律直连)。
func TestPlatformClientOnlineSignalBlocksMesh(t *testing.T) {
	t.Run("信号注入在线", func(t *testing.T) {
		assertGoesDirect(t, fallbackSelfConfig(), func() bool { return false })
	})
	t.Run("信号未接线视为在线", func(t *testing.T) {
		assertGoesDirect(t, fallbackSelfConfig(), nil)
	})
}

// TestPlatformClientGateVariantsBlockMesh 兜底门控变体:Enable=false /
// Classes 不含 self / mesh 总开关显式 false——任一不满足即纯 direct。
// 各变体 SSH 入口显式开(判据含 SSH 入口依赖):让被测变体成为唯一关闸
// 条件,不补则三个用例都被依赖降级误吞。
func TestPlatformClientGateVariantsBlockMesh(t *testing.T) {
	online := func() bool { return true }
	sshIn := func() *config.SSHIngressConfig { return &config.SSHIngressConfig{Enable: boolPtr(true)} }
	t.Run("兜底Enable=false", func(t *testing.T) {
		assertGoesDirect(t, &config.Config{Customer: &config.CustomerConfig{
			Mesh: &config.MeshConfig{Fallback: &config.MeshFallbackConfig{
				Enable:  boolPtr(false),
				Classes: []string{meshproto.ClassSelf},
				Addr:    "192.168.1.9:56440",
			}},
			SSHIngress: sshIn(),
		}}, online)
	})
	t.Run("Classes不含self", func(t *testing.T) {
		assertGoesDirect(t, &config.Config{Customer: &config.CustomerConfig{
			Mesh: &config.MeshConfig{Fallback: &config.MeshFallbackConfig{
				Enable:  boolPtr(true),
				Classes: []string{meshproto.ClassAI, meshproto.ClassCustom},
				Addr:    "192.168.1.9:56440",
			}},
			SSHIngress: sshIn(),
		}}, online)
	})
	t.Run("mesh总开关显式false", func(t *testing.T) {
		assertGoesDirect(t, &config.Config{Customer: &config.CustomerConfig{
			Mesh: &config.MeshConfig{
				Enable:   boolPtr(false),
				Fallback: fallbackSelfConfig().Customer.Mesh.Fallback,
			},
			SSHIngress: sshIn(),
		}}, online)
	})
}

// TestPlatformClientDoubleFailureErrorChain mesh 与 direct 双败:多 %w 错误
// 链上 errors.Is 两个分量各自可达。mesh 分量经 fake PikoDial 注入哨兵错
// (self 类别中继准入、LAN 空载体 → 必落 fake PikoDial),direct 分量用已
// 取消 ctx 造 context.Canceled。
func TestPlatformClientDoubleFailureErrorChain(t *testing.T) {
	keyPEM, _ := testIdentityKeyPEM(t)
	setGlobalConfigForTest(t, fallbackSelfConfig())
	setOfflineSignalForTest(t, func() bool { return true })
	meshSentinel := errors.New("mesh: fake relay carrier down")
	setPeerSourceForTest(t, func() mesh.Dialer {
		return newTestExitPeer(t, keyPEM, "", "SHA256:AAAA", true,
			func(ctx context.Context, endpointID string) (net.Conn, error) {
				return nil, meshSentinel
			})
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // direct 轨道必败(context.Canceled)
	_, err := PlatformClient(5*time.Second).Transport.(*http.Transport).
		DialContext(ctx, "tcp", "platform.test:443")
	if err == nil {
		t.Fatal("双败应返回错误")
	}
	if !errors.Is(err, meshSentinel) {
		t.Fatalf("错误链应含 mesh 分量哨兵: %v", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("错误链应含 direct 分量 context.Canceled: %v", err)
	}
}

// TestSetPeerSourceConcurrentSetAndRead 注入缝并发契约(同 processor/tcp
// TestMeshTransportDialSeamConcurrentSetAndRead 先例):生产写方(组装层)与
// 拨号读方跨 goroutine 并发,-race 下必须无竞争。
func TestSetPeerSourceConcurrentSetAndRead(t *testing.T) {
	peer := fakeDialer{}
	var wg sync.WaitGroup
	done := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			SetPeerSource(func() mesh.Dialer { return peer })
			SetPeerSource(nil)
		}
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			_ = peerFromSource()
		}
	}()
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout: seam 并发读写死锁或过慢")
	}
	// 收尾留一个非 nil 语义验证后清空。
	SetPeerSource(func() mesh.Dialer { return peer })
	if got := peerFromSource(); got == nil {
		t.Fatal("注入后 peerFromSource 应返回非 nil peer")
	}
	SetPeerSource(nil)
	if got := peerFromSource(); got != nil {
		t.Fatalf("清空后 peerFromSource = %v, want nil", got)
	}
}

// fakeDialer 最小 mesh.Dialer(并发契约测试专用,不真拨)。
type fakeDialer struct{}

func (fakeDialer) DialContext(context.Context, *M.Metadata) (net.Conn, error) {
	return nil, errors.New("fakeDialer: unused")
}
