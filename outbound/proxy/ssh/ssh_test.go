package ssh

import (
	"context"
	"errors"
	"io"
	"net"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"

	"aliang.one/nursorgate/inbound/tun/metadata"
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
	if _, err := p.DialContext(context.Background(), md); err == nil {
		t.Fatal("DialContext after Close must fail (no lazy revival)")
	}
}
