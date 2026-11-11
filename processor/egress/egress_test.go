package egress

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"aliang.one/nursorgate/outbound/meshproto"
	mesh "aliang.one/nursorgate/outbound/proxy/mesh"
	"aliang.one/nursorgate/processor/config"
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

// fakeRelay 假 B:复刻 outbound/proxy/mesh/mesh_test.go 的 startFakePeer 帧读取
// 模式,额外多连接 + HTTP 应答——读出首行 JSON 帧头后继续读 HTTP 请求头
// (request line + headers),回一个 200/ok,让 client.Get 完整走通。
type fakeRelay struct {
	ln        net.Listener
	headerCh  chan *meshproto.Header
	reqHeadCh chan string
}

func startFakeRelay(t *testing.T) *fakeRelay {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	fr := &fakeRelay{
		ln:        ln,
		headerCh:  make(chan *meshproto.Header, 4),
		reqHeadCh: make(chan string, 4),
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go fr.handle(conn)
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return fr
}

func (fr *fakeRelay) handle(conn net.Conn) {
	defer conn.Close()
	reader := bufio.NewReader(conn)
	line, err := reader.ReadString('\n')
	if err != nil {
		return
	}
	hdr, _, err := meshproto.Parse([]byte(line))
	if err != nil {
		return
	}
	fr.headerCh <- hdr
	// 读 HTTP 请求头(request line + headers,至空行);裸拨(不做 TLS/HTTP)
	// 场景客户端直接关连接,读到 EOF 就静默返回,不算错。
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	var head strings.Builder
	for {
		l, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		head.WriteString(l)
		if strings.TrimSpace(l) == "" {
			break
		}
	}
	fr.reqHeadCh <- head.String()
	_, _ = conn.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nok"))
}

// closedPortAddr 监听后立即关闭的回环地址:拨它稳定 connection refused
// (mesh_test.go 同款)。
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
func setPeerSourceForTest(t *testing.T, fn func() *mesh.Peer) {
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
// 可用 → 平台流量经 B:帧头 Target=平台域名:端口(域名直转,不本地解析)、
// Class=self;Host 头为平台域名。
func TestPlatformClientRoutesViaMeshWhenFallbackActive(t *testing.T) {
	clearProxyEnv(t)
	fr := startFakeRelay(t)
	setGlobalConfigForTest(t, fallbackSelfConfig())
	setOfflineSignalForTest(t, func() bool { return true })
	setPeerSourceForTest(t, func() *mesh.Peer {
		return mesh.NewPeer(mesh.PeerTarget{
			DeviceID: "dev-b",
			LANAddrs: []string{fr.ln.Addr().String()},
			Relay:    false,
			SelfID:   "dev-a",
		})
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

	hdr := waitRelayHeader(t, fr)
	if hdr.Target != "platform.test:80" {
		t.Fatalf("Target = %q, want platform.test:80", hdr.Target)
	}
	if hdr.Class != meshproto.ClassSelf {
		t.Fatalf("Class = %q, want %s", hdr.Class, meshproto.ClassSelf)
	}
	if hdr.From != "dev-a" || len(hdr.Via) != 1 || hdr.Via[0] != "dev-a" || hdr.Hops != 0 {
		t.Fatalf("帧头 From/Via/Hops = %q/%v/%d, want dev-a/[dev-a]/0", hdr.From, hdr.Via, hdr.Hops)
	}

	select {
	case head := <-fr.reqHeadCh:
		if !strings.Contains(head, "Host: platform.test") {
			t.Fatalf("Host 头缺失: %q", head)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for http request head")
	}

	// 443 端口形态(https 的 scheme 折算由 Transport 完成,此处裸拨验证帧头):
	// mesh 拨出的是原始 TCP 流,TLS 握手在 Transport 层照常发生,无需特殊处理。
	tr := client.Transport.(*http.Transport)
	conn, err := tr.DialContext(context.Background(), "tcp", "platform.test:443")
	if err != nil {
		t.Fatalf("dial :443 via mesh: %v", err)
	}
	_ = conn.Close()
	hdr443 := waitRelayHeader(t, fr)
	if hdr443.Target != "platform.test:443" {
		t.Fatalf("Target = %q, want platform.test:443", hdr443.Target)
	}
	if hdr443.Class != meshproto.ClassSelf {
		t.Fatalf("Class = %q, want %s", hdr443.Class, meshproto.ClassSelf)
	}
}

func waitRelayHeader(t *testing.T, fr *fakeRelay) *meshproto.Header {
	t.Helper()
	select {
	case hdr := <-fr.headerCh:
		return hdr
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for mesh header at fake relay B")
	}
	return nil
}

// TestPlatformClientFallsBackOnError mesh 拨号失败(假 peer 指向死端口)→
// direct 重试成功;direct 恰好一次(不循环)。
func TestPlatformClientFallsBackOnError(t *testing.T) {
	dead := closedPortAddr(t)
	setGlobalConfigForTest(t, fallbackSelfConfig())
	setOfflineSignalForTest(t, func() bool { return true })
	setPeerSourceForTest(t, func() *mesh.Peer {
		return mesh.NewPeer(mesh.PeerTarget{
			DeviceID: "dev-b",
			LANAddrs: []string{dead},
			Relay:    false,
			SelfID:   "dev-a",
		})
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
// 已注入 → 不走 mesh(直连),假 B 一个帧都收不到。
func TestPlatformClientFallbackNotConfiguredIgnoresMesh(t *testing.T) {
	fr := startFakeRelay(t)
	setGlobalConfigForTest(t, &config.Config{Customer: &config.CustomerConfig{
		Mesh: &config.MeshConfig{}, // mesh 层存在但 Fallback 未配置
		// SSH 入口显式开:让「Fallback 未配置」成为唯一关闸条件(判据含
		// SSH 入口依赖后,不补则本用例被依赖降级误吞)。
		SSHIngress: &config.SSHIngressConfig{Enable: boolPtr(true)},
	}})
	setPeerSourceForTest(t, func() *mesh.Peer {
		return mesh.NewPeer(mesh.PeerTarget{
			DeviceID: "dev-b",
			LANAddrs: []string{fr.ln.Addr().String()},
			Relay:    false,
			SelfID:   "dev-a",
		})
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
	// 假 B 不应收到任何帧头(留 200ms 排除在途)。
	time.Sleep(200 * time.Millisecond)
	select {
	case hdr := <-fr.headerCh:
		t.Fatalf("兜底未配置却走了 mesh: %v", hdr)
	default:
	}
}

// assertGoesDirect 门控不激活的统一断言:live 假 B peer 已注入,请求仍必须
// 直连(httptest 计数==1)且假 B 零帧。
func assertGoesDirect(t *testing.T, cfg *config.Config, offline func() bool) {
	t.Helper()
	fr := startFakeRelay(t)
	setGlobalConfigForTest(t, cfg)
	setOfflineSignalForTest(t, offline)
	setPeerSourceForTest(t, func() *mesh.Peer {
		return mesh.NewPeer(mesh.PeerTarget{
			DeviceID: "dev-b",
			LANAddrs: []string{fr.ln.Addr().String()},
			Relay:    false,
			SelfID:   "dev-a",
		})
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
	select {
	case hdr := <-fr.headerCh:
		t.Fatalf("不应走 mesh 却收到帧: %v", hdr)
	default:
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
// 链上 errors.Is 两个分量各自可达。mesh 分量经 fake PikoDial 注入哨兵错,
// direct 分量用已取消 ctx 造 context.Canceled。
func TestPlatformClientDoubleFailureErrorChain(t *testing.T) {
	setGlobalConfigForTest(t, fallbackSelfConfig())
	setOfflineSignalForTest(t, func() bool { return true })
	meshSentinel := errors.New("mesh: fake relay carrier down")
	setPeerSourceForTest(t, func() *mesh.Peer {
		return mesh.NewPeer(mesh.PeerTarget{
			DeviceID: "dev-b",
			Relay:    true, // self 类别中继准入,LAN 空载体 → 必落 fake PikoDial
			PikoDial: func(ctx context.Context, endpointID string) (net.Conn, error) {
				return nil, meshSentinel
			},
			SelfID: "dev-a",
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
// TestMeshSeamConcurrentSetAndRead 先例):生产写方(组装层)与拨号读方
// 跨 goroutine 并发,-race 下必须无竞争。
func TestSetPeerSourceConcurrentSetAndRead(t *testing.T) {
	peer := mesh.NewPeer(mesh.PeerTarget{DeviceID: "dev-b", SelfID: "dev-a"})
	var wg sync.WaitGroup
	done := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			SetPeerSource(func() *mesh.Peer { return peer })
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
	SetPeerSource(func() *mesh.Peer { return peer })
	if got := peerFromSource(); got == nil {
		t.Fatal("注入后 peerFromSource 应返回非 nil peer")
	}
	SetPeerSource(nil)
	if got := peerFromSource(); got != nil {
		t.Fatalf("清空后 peerFromSource = %v, want nil", got)
	}
}
