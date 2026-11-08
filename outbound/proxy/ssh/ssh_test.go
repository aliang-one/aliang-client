package ssh

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"runtime"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"aliang.one/nursorgate/inbound/tun/metadata"
	"aliang.one/nursorgate/outbound/proxy"
	"aliang.one/nursorgate/outbound/proxy/proto"
	"aliang.one/nursorgate/outbound/proxy/ssh/sshtest"
	gossh "golang.org/x/crypto/ssh"
)

// hostOf / portOf 解析 sshtest 返回的 "127.0.0.1:PORT" 形态地址。
func hostOf(t *testing.T, addr string) string {
	t.Helper()
	host, _, err := splitHostPort(addr)
	if err != nil {
		t.Fatalf("split %q: %v", addr, err)
	}
	return host
}

func portOf(t *testing.T, addr string) uint16 {
	t.Helper()
	_, port, err := splitHostPort(addr)
	if err != nil {
		t.Fatalf("split %q: %v", addr, err)
	}
	return port
}

func splitHostPort(addr string) (string, uint16, error) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return "", 0, err
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return "", 0, err
	}
	if port < 1 || port > 65535 {
		return "", 0, fmt.Errorf("port %d out of range 1-65535", port)
	}
	return host, uint16(port), nil
}

// finalNode 从 "host:port" 串组装节点(既有用例的测试辅助;生产侧组装走
// NodeFromAddrPort / NodeConfig 字面量)。
func finalNode(hostport, user, pass, key, keyPass string) NodeConfig {
	host, port, err := splitHostPort(hostport)
	if err != nil {
		panic("finalNode: bad addr " + hostport + ": " + err.Error())
	}
	return NodeFromAddrPort(host, port, user, pass, key, keyPass)
}

func mdTo(t *testing.T, addr string) *metadata.Metadata {
	t.Helper()
	return &metadata.Metadata{HostName: hostOf(t, addr), DstPort: portOf(t, addr)}
}

// echoRound 在 conn 上写 payload 并断言回显一致。
func echoRound(t *testing.T, conn io.ReadWriter, payload string) {
	t.Helper()
	if _, err := conn.Write([]byte(payload)); err != nil {
		t.Fatalf("echo write: %v", err)
	}
	buf := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("echo read: %v", err)
	}
	if string(buf) != payload {
		t.Fatalf("echo = %q, want %q", buf, payload)
	}
}

// TestSSHDialContextEcho 主体全链路:New(懒建)→ DialContext(direct-tcpip)
// → 回显;指纹落盘进 hostsDir;Addr/Proto 契约;active 计数对称。
func TestSSHDialContextEcho(t *testing.T) {
	echoAddr, closeEcho := sshtest.EchoListener(t)
	defer closeEcho()
	srv, addr := sshtest.Start(t, sshtest.WithUsername("u"), sshtest.WithPassword("p"))
	baseline := runtime.NumGoroutine()

	hostsDir := t.TempDir()
	final := NodeFromAddrPort(hostOf(t, addr), portOf(t, addr), "u", "p", "", "")
	p, err := New(final, 1, nil, hostsDir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if p.Addr() != addr {
		t.Fatalf("Addr() = %q, want final %q", p.Addr(), addr)
	}
	if p.Proto() != proto.SSH {
		t.Fatalf("Proto() = %v, want ssh", p.Proto())
	}

	conn, err := p.DialContext(context.Background(), mdTo(t, echoAddr))
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	echoRound(t, conn, "hi")
	_ = conn.Close()

	// active 计数对称:trackedChannel.Close 归还 acquire 记的活跃数。
	if got := p.pool.chains[0].active.Load(); got != 0 {
		t.Fatalf("active after conn close = %d, want 0", got)
	}

	// 指纹已落盘:另起 KnownHosts 读同一文件,证明磁盘持久化而非内存态。
	kh := NewKnownHosts(filepath.Join(hostsDir, "known_hosts"))
	entries := kh.List()
	if len(entries) != 1 {
		t.Fatalf("known_hosts entries = %d, want 1", len(entries))
	}
	want := gossh.FingerprintSHA256(srv.HostKey.PublicKey())
	if entries[0].Fingerprint != want {
		t.Fatalf("fingerprint = %s, want %s", entries[0].Fingerprint, want)
	}

	_ = p.Close()
	assertNoGoroutineLeak(t, baseline)
}

// TestSSHDialUDPUnsupported UDP 恒不支持(SSH 协议无 UDP 转发)。
func TestSSHDialUDPUnsupported(t *testing.T) {
	final := NodeFromAddrPort("127.0.0.1", 1, "u", "p", "", "")
	p, err := New(final, 1, nil, t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer p.Close()
	if _, err := p.DialUDP(nil); !errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("DialUDP err = %v, want ErrUnsupported", err)
	}
}

// TestSSHBadPrivateKeyFailFast 坏 PEM 在 New 即报错(fail-fast):终点与
// 跳板都要校验,不把解析错误留到运行期拨号。
func TestSSHBadPrivateKeyFailFast(t *testing.T) {
	good := NodeFromAddrPort("127.0.0.1", 22, "u", "p", "", "")
	bad := NodeFromAddrPort("127.0.0.1", 2222, "u", "", "not a pem", "")

	if _, err := New(bad, 1, nil, t.TempDir()); err == nil {
		t.Fatal("bad pem on final node must fail at New (fail-fast)")
	}
	if _, err := New(good, 1, []NodeConfig{bad}, t.TempDir()); err == nil {
		t.Fatal("bad pem on jump node must fail at New (fail-fast)")
	}
	// hostsDir 为空同样 fail-fast:否则 known_hosts 会落到相对路径(CWD)。
	if _, err := New(good, 1, nil, ""); err == nil {
		t.Fatal("empty hostsDir must fail at New (fail-fast)")
	}
}

// TestSSHPoolSizeClamp 钉 poolSize 语义(registry 契约):
// 0 → 默认 3;负数钳 1;>16 钳 16;显式 1..16 原样。
func TestSSHPoolSizeClamp(t *testing.T) {
	for _, tc := range []struct {
		in   int
		want int
	}{
		{0, 3},
		{-5, 1},
		{1, 1},
		{3, 3},
		{16, 16},
		{99, 16},
	} {
		p, err := New(NodeFromAddrPort("127.0.0.1", 22, "u", "p", "", ""), tc.in, nil, t.TempDir())
		if err != nil {
			t.Fatalf("New(poolSize=%d): %v", tc.in, err)
		}
		if got := len(p.pool.chains); got != tc.want {
			t.Fatalf("poolSize=%d → %d chains, want %d", tc.in, got, tc.want)
		}
		_ = p.Close()
	}
}

// TestSSHReconnectAfterChainDeath 断链重建(池=1):拨通回显后杀死底层链,
// 再次 DialContext 必须重建新链并回显成功——client 指针更换即重建证据。
func TestSSHReconnectAfterChainDeath(t *testing.T) {
	echoAddr, closeEcho := sshtest.EchoListener(t)
	defer closeEcho()
	_, addr := sshtest.Start(t, sshtest.WithUsername("u"), sshtest.WithPassword("p"))
	baseline := runtime.NumGoroutine()

	final := NodeFromAddrPort(hostOf(t, addr), portOf(t, addr), "u", "p", "", "")
	p, err := New(final, 1, nil, t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer p.Close()

	// 第一段:拨通 + 回显。
	conn1, err := p.DialContext(context.Background(), mdTo(t, echoAddr))
	if err != nil {
		t.Fatalf("first DialContext: %v", err)
	}
	echoRound(t, conn1, "one")
	_ = conn1.Close()

	p.pool.chains[0].mu.Lock()
	cl1 := p.pool.chains[0].client
	p.pool.chains[0].mu.Unlock()
	if cl1 == nil {
		t.Fatal("first dial must leave a live client on the chain")
	}

	// 杀死链:级联关闭底层 client,等监控协程标记死亡。
	if err := cl1.Close(); err != nil {
		t.Fatalf("kill client: %v", err)
	}
	waitUntil(t, "monitor must mark clientErr after kill", func() bool {
		p.pool.chains[0].mu.Lock()
		defer p.pool.chains[0].mu.Unlock()
		return p.pool.chains[0].clientErr != nil
	})

	// 第二段:同目标再次拨号,必须重建并回显成功。
	conn2, err := p.DialContext(context.Background(), mdTo(t, echoAddr))
	if err != nil {
		t.Fatalf("DialContext after chain death must rebuild: %v", err)
	}
	echoRound(t, conn2, "two")
	_ = conn2.Close()

	p.pool.chains[0].mu.Lock()
	cl2 := p.pool.chains[0].client
	p.pool.chains[0].mu.Unlock()
	if cl2 == nil || cl2 == cl1 {
		t.Fatal("second dial must be served by a rebuilt client, not the killed one")
	}
	if got := p.pool.chains[0].active.Load(); got != 0 {
		t.Fatalf("active after both conns closed = %d, want 0", got)
	}

	// 显式收尾后断言(defer 只兜底):泄漏检查时链必须已断开。
	_ = p.Close()
	assertNoGoroutineLeak(t, baseline)
}

// TestSSHCloseIsTerminal 钉 Close 的终结语义:Close 后 DialContext 直接
// 报错,不再懒重建——被替换的旧实例不能在残余流量下复活出无人回收的链。
func TestSSHCloseIsTerminal(t *testing.T) {
	final := NodeFromAddrPort("127.0.0.1", 1, "u", "p", "", "")
	p, err := New(final, 1, nil, t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := p.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// 幂等:重复 Close 不 panic。
	if err := p.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	md := &metadata.Metadata{HostName: "127.0.0.1", DstPort: 1}
	if _, err := p.DialContext(context.Background(), md); !errors.Is(err, ErrClosed) {
		t.Fatalf("DialContext after Close err = %v, want ErrClosed (no lazy revival)", err)
	}
}

// TestSSHLiveChainOpenFailureNoRetry 钉「换链重试按 client 已死门控」
// (spec §4):活链上的通道打开失败(目标不可达 → sshd 以 ConnectionFailed
// 拒绝)必须直接透传,不换链重试——服务器只见一次通道打开请求;
// 且活链不被废,后续拨号仍复用同一 client。
func TestSSHLiveChainOpenFailureNoRetry(t *testing.T) {
	echoAddr, closeEcho := sshtest.EchoListener(t)
	defer closeEcho()
	srv, addr := sshtest.Start(t, sshtest.WithUsername("u"), sshtest.WithPassword("p"))

	final := NodeFromAddrPort(hostOf(t, addr), portOf(t, addr), "u", "p", "", "")
	p, err := New(final, 1, nil, t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer p.Close()

	// 预热:拨可达 echo,链建立并保持活。
	conn, err := p.DialContext(context.Background(), mdTo(t, echoAddr))
	if err != nil {
		t.Fatalf("warm-up dial: %v", err)
	}
	echoRound(t, conn, "ok")
	_ = conn.Close()
	if got := srv.DirectDials.Load(); got != 1 {
		t.Fatalf("direct-tcpip opens = %d after warm-up, want 1", got)
	}
	p.pool.chains[0].mu.Lock()
	cl1 := p.pool.chains[0].client
	p.pool.chains[0].mu.Unlock()

	// 活链失败:目标 127.0.0.1:1(特权端口,无 root 监听恒拒绝)→ sshd
	// ConnectionFailed 拒绝通道 → 客户端 OpenChannel 报错。必须只发生一次
	// 通道打开(无重试)且错误透传。
	_, err = p.DialContext(context.Background(), &metadata.Metadata{HostName: "127.0.0.1", DstPort: 1})
	if err == nil {
		t.Fatal("dial to unreachable port must fail")
	}
	if got := srv.DirectDials.Load(); got != 2 {
		t.Fatalf("direct-tcpip opens = %d after live-chain failure, want 2 (exactly one open, no retry)", got)
	}

	// 活链未被废:client 原样存活,后续可达拨号复用之(不再拨新链)。
	p.pool.chains[0].mu.Lock()
	sameClient := p.pool.chains[0].client == cl1 && p.pool.chains[0].clientErr == nil
	p.pool.chains[0].mu.Unlock()
	if !sameClient {
		t.Fatal("live chain must survive a business-level open failure")
	}
	conn2, err := p.DialContext(context.Background(), mdTo(t, echoAddr))
	if err != nil {
		t.Fatalf("dial after business failure must reuse live chain: %v", err)
	}
	echoRound(t, conn2, "ok2")
	_ = conn2.Close()
	if got := srv.DirectDials.Load(); got != 3 {
		t.Fatalf("direct-tcpip opens = %d after reuse, want 3", got)
	}
}

// scriptedConn 按测试脚本演绎「client 在拨到后、通道打开前死亡」:
// Wait 沿用 fakeConn(测试调 Close 即死 → 监控协程标记 clientErr);
// OpenChannel 停在 open 门闩上,测试放行后才报错——两次事件之间可以
// 精确插入 Close,复现 Close 与在途换链重试的竞态窗口。
type scriptedConn struct {
	fakeConn
	open chan struct{}
}

func (s *scriptedConn) OpenChannel(string, []byte) (gossh.Channel, <-chan *gossh.Request, error) {
	<-s.open
	return nil, nil, errors.New("scripted: open channel refused")
}

// nopChannel 是 gossh.Channel 的最小桩,供 trackedChannel 单测。
type nopChannel struct{}

func (nopChannel) Read([]byte) (int, error)    { return 0, io.EOF }
func (nopChannel) Write(p []byte) (int, error) { return len(p), nil }
func (nopChannel) Close() error                { return nil }
func (nopChannel) CloseWrite() error           { return nil }
func (nopChannel) SendRequest(string, bool, []byte) (bool, error) {
	return false, nil
}
func (nopChannel) Stderr() io.ReadWriter { return nopRW{} }

type nopRW struct{}

func (nopRW) Read([]byte) (int, error)    { return 0, io.EOF }
func (nopRW) Write(p []byte) (int, error) { return len(p), nil }

// TestTrackedChannelDoubleClose 钉通道双 Close 幂等:只归还一次活跃计数,
// 否则双关连接会永久偏置最少活跃选链(多通道在途时把别人的计数也吃掉)。
func TestTrackedChannelDoubleClose(t *testing.T) {
	c := newChain(func(context.Context) (*cascadeClient, error) {
		return nil, errors.New("unused")
	})
	c.addActive() // 本通道(acquire 记入)
	c.addActive() // 另一在途通道
	ch := newTrackedChannel(nopChannel{}, c)
	if err := ch.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := ch.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if got := c.active.Load(); got != 1 {
		t.Fatalf("active after double Close = %d, want 1 (double release must not steal the peer's count)", got)
	}
}

// TestSSHCloseBlocksInflightRetry 钉 Close 与在途 DialContext 的竞态窗口:
// 在途调用通过入口检查、已拿到 client,client 死亡(监控已标记)触发
// 「换链重试」路径,此刻实例已被 Close——重试必须在拨新链之前被 attempt
// 顶部的 closed 检查拦下。修前:重试会真连一条新链(dialChain 计数翻倍,
// 且 pool.close 顺序不利时无人回收)。
func TestSSHCloseBlocksInflightRetry(t *testing.T) {
	openGate := make(chan struct{})
	var dials1 atomic.Int32
	scCh := make(chan *scriptedConn, 8) // 容量预留给修前形态的多余重拨,避免装置阻塞干扰断言
	p := &SSH{Base: &proxy.Base{Address: "127.0.0.1:1", Protocol: proto.SSH}}
	p.pool = newPool(2, func(i int) func(context.Context) (*cascadeClient, error) {
		return func(context.Context) (*cascadeClient, error) {
			if i == 0 {
				return fakeCascadeClient(), nil
			}
			dials1.Add(1)
			sc := &scriptedConn{fakeConn: fakeConn{closed: make(chan struct{})}, open: openGate}
			scCh <- sc
			chans := make(chan gossh.NewChannel)
			close(chans)
			reqs := make(chan *gossh.Request)
			close(reqs)
			return &cascadeClient{Client: gossh.NewClient(sc, chans, reqs)}, nil
		}
	})

	// 把 Close 的 pool.close 挡在链 0 之前:测试直持链 0 的 mu,链 1 的
	// 在途 client 与其死亡标记不会被级联关闭触碰。
	p.pool.chains[0].mu.Lock()
	// 偏置选链:链 0 挂一个在途活跃,在途 DialContext 必落链 1。
	p.pool.chains[0].addActive()

	done := make(chan error, 1)
	go func() {
		_, err := p.DialContext(context.Background(), &metadata.Metadata{HostName: "127.0.0.1", DstPort: 9})
		done <- err
	}()

	// 等链 1 的 scripted client 拨出,然后杀死它 → 监控协程标记 clientErr。
	sc := <-scCh
	sc.Close()
	waitUntil(t, "monitor must mark chain 1 dead after scripted kill", func() bool {
		p.pool.chains[1].mu.Lock()
		defer p.pool.chains[1].mu.Unlock()
		return p.pool.chains[1].clientErr != nil
	})

	// Close:closed 先置位,级联关闭被挡在链 0 的 mu 上(持有 p.mu)——
	// 链 1 原样保留:client 在、死亡标记在,正是 drop 会收走的形态。
	closeDone := make(chan struct{})
	go func() {
		_ = p.Close()
		close(closeDone)
	}()
	waitUntil(t, "Close must set closed flag", func() bool { return p.closed.Load() })

	// 放行在途调用:OpenChannel 失败 → drop 收走死链(pool.close 仍挡在
	// 链 0,链 1 原样)→ attempt 1 顶部必须被 closed 拦下,不得拨新链
	//(closed 的置位 happens-before openGate 放行,读取确定见 true)。
	// 修前形态:attempt 1 闯过(不存在的)closed 检查,acquire 在
	// pool.close 持有的 p.mu 上排队——卡住即证重试未被拦下。
	close(openGate)
	select {
	case err := <-done:
		if !errors.Is(err, ErrClosed) {
			t.Fatalf("inflight DialContext err = %v, want ErrClosed", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("inflight DialContext stuck: retry reached acquire after Close (closed check missing)")
	}
	if got := dials1.Load(); got != 1 {
		t.Fatalf("chain 1 dialed %d times, want 1 (no revival after Close)", got)
	}

	// 在途调用已收尾,现在放行 pool.close(不再与 drop 竞争链 1 的 mu),
	// Close 必须能完成。
	p.pool.chains[0].mu.Unlock()
	select {
	case <-closeDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not complete after releasing the gate")
	}
}
