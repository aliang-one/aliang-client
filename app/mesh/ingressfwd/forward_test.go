package ingressfwd

import (
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"aliang.one/nursorgate/inbound/ssh"
	"aliang.one/nursorgate/processor/config"
)

// sshRunningForTest 测试进程通常无 reconcile(单例缺席),但防御其他测试
// 意外起服导致决策矩阵不可测——此时跳过而非误报。
func sshRunningForTest() bool { return ssh.IsRunning() }

func enabledCfg(port uint16) *config.Config {
	enable := true
	return &config.Config{
		Customer: &config.CustomerConfig{
			SSHIngress: &config.SSHIngressConfig{
				Enable: &enable,
				Port:   port,
			},
		},
	}
}

// sshBanner 是 mesh 对端拨入时的首段(SSH 版本串);回环测试用它充当「合法
// 的中继/打洞流」。
var sshBanner = []byte("SSH-2.0-OpenSSH_ingressfwd-test\r\n")

// TestTarget 钉拼接目标的推导:跟随形态(专用口未写 / 0 / 旧哨兵 56442)一律
// 拨统一口 56432;显式专用口拨 core 侧专用 listener。
//
// 2026-11-24 审计实证的缺陷:EffectiveSSHIngress() 早已把 port 0 归一成哨兵
// 56442(types.go,2026-11-10 进主线),而旧 Target 以「!= 0」判「是否配了专用
// 口」——默认配置下把全部中继/打洞流拼到 127.0.0.1:56442 这个 core 从不监听
// 的口,连接被拒后丢弃。11-20 的双进程 B 侧修复在默认配置下等于没生效。
func TestTarget(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		cfg  *config.Config
		want string
	}{
		{"nil cfg:拨统一口(core 侧自会按入口开关执法)", nil, "127.0.0.1:56432"},
		{"agent 的配置副本没有 ssh_ingress:仍拨统一口", &config.Config{}, "127.0.0.1:56432"},
		{"显式关(副本可能滞后):仍拨统一口", func() *config.Config {
			disabled := false
			return &config.Config{Customer: &config.CustomerConfig{
				SSHIngress: &config.SSHIngressConfig{Enable: &disabled},
			}}
		}(), "127.0.0.1:56432"},
		{"port=0(跟随统一口)", enabledCfg(0), "127.0.0.1:56432"},
		{"port=56442(旧默认哨兵,UI 留空时落盘的值)", enabledCfg(config.SSHIngressDefaultPort), "127.0.0.1:56432"},
		{"专用口 56499", enabledCfg(56499), "127.0.0.1:56499"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Target(tc.cfg); got != tc.want {
				t.Fatalf("Target() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestTargetsFollowOrder 专用口形态先试专用口、失败回落统一口(专用口
// listener 不随代理启停;统一口随代理——两个都可能在)。
func TestTargetsFollowOrder(t *testing.T) {
	t.Parallel()
	got := Targets(enabledCfg(56499))
	if len(got) != 2 || got[0] != "127.0.0.1:56499" || got[1] != "127.0.0.1:56432" {
		t.Fatalf("Targets(dedicated) = %v, want [127.0.0.1:56499 127.0.0.1:56432]", got)
	}
	if got := Targets(enabledCfg(0)); len(got) != 1 || got[0] != "127.0.0.1:56432" {
		t.Fatalf("Targets(follow) = %v, want [127.0.0.1:56432]", got)
	}
}

// Splice 契约:c ↔ addr 双向透传;对端关闭后 c 随之关闭;无数据损耗。
func TestSpliceBidirectional(t *testing.T) {
	t.Parallel()
	// 上游 = 本地 echo server(扮演 core 统一入口的 SSH ingress)
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	upConnCh := make(chan net.Conn, 1)
	go func() {
		c, err := upstream.Accept()
		if err != nil {
			return
		}
		upConnCh <- c
		// echo 一切回 c
		buf := make([]byte, 512)
		for {
			n, err := c.Read(buf)
			if err != nil {
				return
			}
			if _, err := c.Write(buf[:n]); err != nil {
				return
			}
		}
	}()

	clientSide, peerSide := net.Pipe()
	errCh := make(chan error, 1)
	go func() { errCh <- Splice(clientSide, upstream.Addr().String()) }()

	up := <-upConnCh
	// c → 上游 → echo → c
	if _, err := peerSide.Write([]byte("ping")); err != nil {
		t.Fatalf("write to pipe: %v", err)
	}
	peerSide.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 16)
	n, err := peerSide.Read(buf)
	if err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if string(buf[:n]) != "ping" {
		t.Fatalf("echo mismatch: %q", buf[:n])
	}
	// 上游主动关 → Splice 收尾 → c 关闭
	_ = up.Close()
	select {
	case err := <-errCh:
		if err == nil {
			t.Log("splice returned nil on upstream close(允许)")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("splice 未在上游关闭后收尾")
	}
	peerSide.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := peerSide.Read(buf); err == nil {
		t.Fatal("splice 收尾后 c 仍可读 = 未关闭")
	}
}

// fakeCore 起一个回环 listener 扮演 core 侧入口,计数被拨次数。
type fakeCore struct {
	addr  string
	dials int32
}

func newFakeCore(t *testing.T) *fakeCore {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	fc := &fakeCore{addr: l.Addr().String()}
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			atomic.AddInt32(&fc.dials, 1)
			go func() { _ = c.Close() }()
		}
	}()
	t.Cleanup(func() { _ = l.Close() })
	return fc
}

// portOf 取回环 listener 地址的端口(拼专用口形态的配置用)。
func portOf(t *testing.T, addr string) uint16 {
	t.Helper()
	_, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	var port uint16
	for _, ch := range portStr {
		port = port*10 + uint16(ch-'0')
	}
	return port
}

// runServeOrForward 在 goroutine 里跑 ServeOrForward(它阻塞至流结束),
// 返回服务端连接(喂给它的那一端)与结束信号。
func runServeOrForward(t *testing.T, cfg *config.Config) (server net.Conn, returned <-chan bool) {
	t.Helper()
	client, serverEnd := net.Pipe()
	t.Cleanup(func() { _ = client.Close(); _ = serverEnd.Close() })
	done := make(chan bool, 1)
	go func() {
		done <- ServeOrForward(serverEnd, cfg)
	}()
	return client, done
}

func waitReturned(t *testing.T, done <-chan bool) bool {
	t.Helper()
	select {
	case got := <-done:
		return got
	case <-time.After(8 * time.Second):
		t.Fatal("ServeOrForward 未在期限内返回(挂死)")
		return false
	}
}

// TestServeOrForwardForwardsToCore 转发路径:目标=本地 echo,SSH 首段写入即
// 应原样回(证明字节过了 splice)。
//
// 注:旧版本用例在测试 goroutine 里同步调用 ServeOrForward,之后的写入永远
// 排在它返回之后——而 Splice 要等双向都结束才返回,于是必然挂死(默认
// -timeout 10m 才 panic)。ServeOrForward 必须在 goroutine 里跑。
func TestServeOrForwardForwardsToCore(t *testing.T) {
	if sshRunningForTest() {
		t.Skip("本测试进程恰有 ingress 单例在跑,决策矩阵不可测")
	}
	// 让 Targets 指向 echo listener:借用专用口形态。
	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	go func() {
		c, err := echo.Accept()
		if err != nil {
			return
		}
		buf := make([]byte, 512)
		for {
			n, err := c.Read(buf)
			if err != nil {
				return
			}
			if _, err := c.Write(buf[:n]); err != nil {
				return
			}
		}
	}()
	port := portOf(t, echo.Addr().String())

	client, done := runServeOrForward(t, enabledCfg(port))
	_ = client.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := client.Write(sshBanner); err != nil {
		t.Fatalf("write: %v", err)
	}
	echoed := make([]byte, len(sshBanner))
	if _, err := io.ReadFull(client, echoed); err != nil {
		t.Fatalf("read forwarded banner: %v", err)
	}
	if string(echoed) != string(sshBanner) {
		t.Fatalf("forwarded payload mismatch: %q", echoed)
	}
	_ = client.Close()
	if got := waitReturned(t, done); got {
		t.Fatal("本进程无单例,应走转发分支返回 false")
	}
}

// TestServeOrForwardDropsNonSSHStream 非 SSH 流不得拼到 core:中继/打洞
// 通道是给 mesh 对端拨入用的(SSH 族);拼接是回环拨号,把 HTTP/socks5 字节
// 拼过去等于让远端以「回环来源」身份使用统一入口的代理面(来源闸在
// inbound/http 按 TCP 对端地址判,对回环拨号恒放行)。
func TestServeOrForwardDropsNonSSHStream(t *testing.T) {
	if sshRunningForTest() {
		t.Skip("本测试进程恰有 ingress 单例在跑,决策矩阵不可测")
	}
	fc := newFakeCore(t)
	port := portOf(t, fc.addr)

	payloads := map[string][]byte{
		"HTTP CONNECT(经代理面回环穿透)": []byte("CONNECT 127.0.0.1:56431 HTTP/1.1\r\nHost: 127.0.0.1:56431\r\n\r\n"),
		"socks5 greeting":        {0x05, 0x01, 0x00, 0x05, 0x01, 0x00, 0x01},
	}
	for name, payload := range payloads {
		t.Run(name, func(t *testing.T) {
			client, done := runServeOrForward(t, enabledCfg(port))
			go func() {
				_, _ = client.Write(payload)
				time.Sleep(10 * time.Second)
				_ = client.Close()
			}()
			if got := waitReturned(t, done); got {
				t.Fatal("非 SSH 流应被丢弃并返回 false")
			}
			if n := atomic.LoadInt32(&fc.dials); n != 0 {
				t.Fatalf("非 SSH 流不得拨 core,dials=%d", n)
			}
		})
	}
}

// TestServeOrForwardDropsSilentStream 无首段(静默)的流按嗅探超时丢弃,
// 同样不拨 core。
func TestServeOrForwardDropsSilentStream(t *testing.T) {
	if sshRunningForTest() {
		t.Skip("本测试进程恰有 ingress 单例在跑,决策矩阵不可测")
	}
	fc := newFakeCore(t)
	client, done := runServeOrForward(t, enabledCfg(portOf(t, fc.addr)))
	_ = client // 不写任何字节
	if got := waitReturned(t, done); got {
		t.Fatal("静默流应被丢弃并返回 false")
	}
	if n := atomic.LoadInt32(&fc.dials); n != 0 {
		t.Fatalf("静默流不得拨 core,dials=%d", n)
	}
}

// TestDialFirstReachableFallsBackInOrder 目标按序尝试:前一个拒连就试下一个
// (专用口 listener 不随代理启停、统一口随代理——两个都可能在,任一活着即可)。
func TestDialFirstReachableFallsBackInOrder(t *testing.T) {
	dead, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	deadAddr := dead.Addr().String()
	_ = dead.Close()

	alive := newFakeCore(t)
	conn, hit, err := dialFirstReachable([]string{deadAddr, alive.addr}, time.Second)
	if err != nil {
		t.Fatalf("dialFirstReachable: %v", err)
	}
	defer conn.Close()
	if hit != alive.addr {
		t.Fatalf("hit = %q, want %q", hit, alive.addr)
	}
	if _, _, err := dialFirstReachable([]string{deadAddr}, 300*time.Millisecond); err == nil {
		t.Fatal("全部目标不可达时必须返回错误")
	}
}
