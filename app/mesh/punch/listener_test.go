// listener_test.go 见证 B 侧常驻接收面(Task 6)的六面:甜路径(过门后
// 流内剩余字节直达 Handler)/同会话第二条流免门/错 token 拒单且 Handler
// 不被调/TTL 到点收摊+后续拨号必败/双并发会话互不串扰/Handler panic 被
// 隔离不拖垮 accept 面。全部跑在 loopback(127.0.0.1 普通 UDPConn,不碰
// 物理接口绑定),证书与拨号件复用 tls_test.go 的既有 helper。

package punch

import (
	"context"
	"io"
	"net"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// punchListenerEnv 起 B 侧 listener:loopback UDPConn + 现签证书对 + 双向
// 钉扎全对的配置。返回服务端地址、客户端拨号用的配置与 done 通道;TTL 由
// 用例给(常规用例 5s 富余,(d) 收摊用例给小值)。Cleanup 只关 UDPConn——
// conn 归调用方所有正是 ServeListener 的契约,关闭后 listener 内部自然收摊。
func punchListenerEnv(t *testing.T, token string, ttl time.Duration, handler func(net.Conn)) (net.Addr, PunchTLSConfig, <-chan struct{}) {
	t.Helper()
	serverCert, serverFP := testPunchCert(t, "punch-listener-server")
	clientCert, clientFP := testPunchCert(t, "punch-listener-client")
	cfg := PunchTLSConfig{
		ServerCert:        serverCert,
		ClientCert:        clientCert,
		ServerFingerprint: serverFP,
		ClientFingerprint: clientFP,
		ExpectedToken:     token,
	}
	udpConn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("起 listener UDP socket 失败: %v", err)
	}
	t.Cleanup(func() { _ = udpConn.Close() })
	port, done, err := ServeListener(udpConn, ListenerOpts{TTL: ttl, Token: token, TLS: cfg, Handler: handler})
	if err != nil {
		t.Fatalf("起 listener 失败: %v", err)
	}
	return &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port}, cfg, done
}

// ---------------------------------------------------------------------------
// (a) 甜路径:过门 → Handler 收到 net.Conn、token 行已被消费、回写可读回
// ---------------------------------------------------------------------------

// TestServeListenerSweetPathGateThenStream A 侧拨号 + 首流写 "token\nhello":
// listener 必须先过 TokenGate(token 行被消费)再把同一流交 Handler——
// Handler 读到的是干净的 "hello"(token 行若没被消费会读到 "tok\nh"),
// 回写 "world" 客户端可读回;顺带见证适配件维持 net.Conn 全套语义
// (RemoteAddr/SetReadDeadline 在流上都活着)。
func TestServeListenerSweetPathGateThenStream(t *testing.T) {
	const hello = "hello"
	const world = "world"
	gotRemote := make(chan string, 1)
	addr, cfg, _ := punchListenerEnv(t, "punch-test-token", 5*time.Second, func(c net.Conn) {
		buf := make([]byte, len(hello))
		if _, err := io.ReadFull(c, buf); err != nil {
			t.Errorf("Handler 读数据失败: %v", err)
			return
		}
		if string(buf) != hello {
			t.Errorf("Handler 读到 %q(token 行应已被门禁消费),期望 %q", string(buf), hello)
		}
		// 适配件契约抽检:net.Conn 的 deadline/地址族在流上必须可用。
		if err := c.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
			t.Errorf("Handler SetReadDeadline 失败: %v", err)
		}
		if _, err := c.Write([]byte(world)); err != nil {
			t.Errorf("Handler 回写失败: %v", err)
		}
		gotRemote <- c.RemoteAddr().String()
	})

	conn := punchTestDial(t, addr, ClientTLSConfig(cfg))
	stream := punchTestOpenStream(t, conn)
	// token 行与数据一次写入:顺带见证门禁逐字节读不吞后续字节(tls_test 同款手法)。
	if _, err := stream.Write([]byte("punch-test-token\n" + hello)); err != nil {
		t.Fatalf("客户端写 token+数据失败: %v", err)
	}
	got := make([]byte, len(world))
	if _, err := io.ReadFull(stream, got); err != nil {
		t.Fatalf("客户端读回写失败(甜路径断链?): %v", err)
	}
	if string(got) != world {
		t.Fatalf("回写内容不符: 得到 %q, 期望 %q", string(got), world)
	}
	select {
	case remote := <-gotRemote:
		if !strings.Contains(remote, "127.0.0.1") {
			t.Errorf("RemoteAddr 应是 loopback,得到 %q", remote)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("等 Handler 侧见证超时")
	}
}

// ---------------------------------------------------------------------------
// (b) 同会话第二条流直接到 Handler(不过门)
// ---------------------------------------------------------------------------

// TestServeListenerSecondStreamSkipsGate 同一会话先开首流过门,再开第二条
// 流只写裸数据(无 token 行)——第二条必须直达 Handler 且读到的就是裸数据
// 本身:若被门禁再拦一道,"two" 无换行会把门禁吊到围栏到点、Handler 收不
// 到;若门禁没消费首流 token 行,首流 Handler 会读到 "tok"。两侧都验。
func TestServeListenerSecondStreamSkipsGate(t *testing.T) {
	got := make(chan string, 2)
	addr, cfg, _ := punchListenerEnv(t, "punch-test-token", 5*time.Second, func(c net.Conn) {
		buf := make([]byte, 3)
		if _, err := io.ReadFull(c, buf); err != nil {
			t.Errorf("Handler 读流数据失败: %v", err)
			return
		}
		got <- string(buf)
	})

	conn := punchTestDial(t, addr, ClientTLSConfig(cfg))
	s1 := punchTestOpenStream(t, conn)
	if _, err := s1.Write([]byte("punch-test-token\none")); err != nil {
		t.Fatalf("客户端写首流失败: %v", err)
	}
	s2 := punchTestOpenStream(t, conn)
	if _, err := s2.Write([]byte("two")); err != nil { // 裸数据,无 token 行
		t.Fatalf("客户端写第二条流失败: %v", err)
	}

	var payloads []string
	deadline := time.After(5 * time.Second)
	for len(payloads) < 2 {
		select {
		case p := <-got:
			payloads = append(payloads, p)
		case <-deadline:
			t.Fatalf("两条流只等到 %d 条(第二条被门禁误拦?)", len(payloads))
		}
	}
	sort.Strings(payloads)
	if payloads[0] != "one" || payloads[1] != "two" {
		t.Fatalf("流数据不符: 得到 %v, 期望 [one two](token 行被吞/门禁误拦都会在此现形)", payloads)
	}
}

// ---------------------------------------------------------------------------
// (c) token 错 → 流被拒(连接死亡),Handler 未被调
// ---------------------------------------------------------------------------

// TestServeListenerWrongTokenKillsConnNotHandler 错 token:整条会话被
// CloseWithError 拒死(客户端侧迟早观察到失败),且 Handler 一次都不能被调
// ——门禁拒的是会话,不存在「拒了首流再放行后续流」的口子。
func TestServeListenerWrongTokenKillsConnNotHandler(t *testing.T) {
	handlerCalled := make(chan net.Conn, 1)
	addr, cfg, _ := punchListenerEnv(t, "right-token", 5*time.Second, func(c net.Conn) {
		handlerCalled <- c
	})

	conn := punchTestDial(t, addr, ClientTLSConfig(cfg))
	stream := punchTestOpenStream(t, conn)
	if _, err := stream.Write([]byte("wrong-token\n")); err != nil {
		t.Fatalf("客户端写错 token 失败: %v", err)
	}
	punchClientMustSeeFailure(t, conn)
	select {
	case c := <-handlerCalled:
		t.Fatalf("错 token 的会话不应把流交付 Handler,却收到来自 %v 的连接", c.RemoteAddr())
	case <-time.After(200 * time.Millisecond):
	}
}

// ---------------------------------------------------------------------------
// (d) TTL 到点 → done 关闭、后续 Dial 失败
// ---------------------------------------------------------------------------

// TestServeListenerTTLTeardownClosesDone TTL(此处压到 300ms)到点:done
// 关闭、Transport 收摊——此后对同一地址的拨号必须失败(服务端已无人应答
// 握手),这是「收摊=终局」的直接见证。
func TestServeListenerTTLTeardownClosesDone(t *testing.T) {
	addr, cfg, done := punchListenerEnv(t, "punch-test-token", 300*time.Millisecond, func(net.Conn) {})
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("TTL 到点 done 未关闭(收摊失灵)")
	}

	tr := punchTestClientTransport(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if conn, err := tr.Dial(ctx, addr, ClientTLSConfig(cfg), nil); err == nil {
		_ = conn.CloseWithError(0, "")
		t.Fatal("listener 收摊后拨号必须失败,却成功了")
	}
}

// ---------------------------------------------------------------------------
// (d2) accept 报错先至(调用方提前关 conn)→ done 关闭、不挂死
// ---------------------------------------------------------------------------

// TestServeListenerCallerConnCloseClosesDone 非 TTL 收摊路径:调用方在 TTL
// 远未到时提前关 conn(所有权契约里调用方的合法动作),accept 面报错先至
// ——done 必须照样关闭,不能因收摊调度只按 timer 分支设计而永久挂死。
// 5s 护栏防测试本身挂死;TTL 给足 90s 保证唯一触发面就是关 conn。
func TestServeListenerCallerConnCloseClosesDone(t *testing.T) {
	serverCert, serverFP := testPunchCert(t, "punch-close-server")
	clientCert, clientFP := testPunchCert(t, "punch-close-client")
	cfg := PunchTLSConfig{
		ServerCert:        serverCert,
		ClientCert:        clientCert,
		ServerFingerprint: serverFP,
		ClientFingerprint: clientFP,
		ExpectedToken:     "punch-test-token",
	}
	udpConn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("起 listener UDP socket 失败: %v", err)
	}
	t.Cleanup(func() { _ = udpConn.Close() }) // 幂等兜底:主体已关,二次关只报错
	_, done, err := ServeListener(udpConn, ListenerOpts{
		TTL:     90 * time.Second,
		Token:   "punch-test-token",
		TLS:     cfg,
		Handler: func(net.Conn) {},
	})
	if err != nil {
		t.Fatalf("起 listener 失败: %v", err)
	}

	_ = udpConn.Close() // 调用方提前收 conn:accept 报错先至路径

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("调用方关 conn 后 done 未关闭(accept-error 先至路径挂死)")
	}
}

// ---------------------------------------------------------------------------
// (e) 两个并发会话各自过门互不串扰
// ---------------------------------------------------------------------------

// TestServeListenerConcurrentSessionsNoCrosstalk A1、A2 两会话同时存活、
// 各自首流过门、各自回显自己的载荷——Handler 收到的流若串了会话,回显就
// 会互换或门禁拒单,两侧对不上即现形。
func TestServeListenerConcurrentSessionsNoCrosstalk(t *testing.T) {
	addr, cfg, _ := punchListenerEnv(t, "punch-test-token", 5*time.Second, func(c net.Conn) {
		buf := make([]byte, len("payload-A1"))
		if _, err := io.ReadFull(c, buf); err != nil {
			t.Errorf("Handler 读数据失败: %v", err)
			return
		}
		if _, err := c.Write(buf); err != nil {
			t.Errorf("Handler 回显失败: %v", err)
		}
	})

	// 两会话先后拨、同时存活(punchTestDial 各带独立 Transport),载荷各自
	// 定向——串扰必然造成回显错位。
	conn1 := punchTestDial(t, addr, ClientTLSConfig(cfg))
	conn2 := punchTestDial(t, addr, ClientTLSConfig(cfg))
	s1 := punchTestOpenStream(t, conn1)
	if _, err := s1.Write([]byte("punch-test-token\npayload-A1")); err != nil {
		t.Fatalf("A1 写 token+载荷失败: %v", err)
	}
	s2 := punchTestOpenStream(t, conn2)
	if _, err := s2.Write([]byte("punch-test-token\npayload-A2")); err != nil {
		t.Fatalf("A2 写 token+载荷失败: %v", err)
	}

	echo1 := make([]byte, len("payload-A1"))
	if _, err := io.ReadFull(s1, echo1); err != nil {
		t.Fatalf("A1 读回显失败(会话被串扰/误杀?): %v", err)
	}
	echo2 := make([]byte, len("payload-A2"))
	if _, err := io.ReadFull(s2, echo2); err != nil {
		t.Fatalf("A2 读回显失败(会话被串扰/误杀?): %v", err)
	}
	if string(echo1) != "payload-A1" || string(echo2) != "payload-A2" {
		t.Fatalf("两会话回显错位: A1 得 %q, A2 得 %q", string(echo1), string(echo2))
	}
}

// ---------------------------------------------------------------------------
// (f) Handler panic 不拖垮 accept 循环
// ---------------------------------------------------------------------------

// TestServeListenerHandlerPanicIsolated 首条流的 Handler 故意 panic:隔离
// 层必须接住(进程不死),且同一会话的第二条流(免门直通)仍能被 accept
// 面交付并回显——panic 只赔本流,不赔接收面。
func TestServeListenerHandlerPanicIsolated(t *testing.T) {
	var calls int32
	firstHit := make(chan struct{}, 1)
	addr, cfg, _ := punchListenerEnv(t, "punch-test-token", 5*time.Second, func(c net.Conn) {
		if atomic.AddInt32(&calls, 1) == 1 {
			firstHit <- struct{}{}
			panic("punch test: 首条流 Handler 故意炸")
		}
		buf := make([]byte, len("after"))
		if _, err := io.ReadFull(c, buf); err != nil {
			t.Errorf("panic 后 Handler 读数据失败: %v", err)
			return
		}
		if _, err := c.Write([]byte("echo:" + string(buf))); err != nil {
			t.Errorf("panic 后 Handler 回写失败: %v", err)
		}
	})

	conn := punchTestDial(t, addr, ClientTLSConfig(cfg))
	s1 := punchTestOpenStream(t, conn)
	if _, err := s1.Write([]byte("punch-test-token\nboom")); err != nil {
		t.Fatalf("客户端写首流失败: %v", err)
	}
	select {
	case <-firstHit:
	case <-time.After(5 * time.Second):
		t.Fatal("等首条流进 Handler 超时")
	}
	// 同一会话第二条流:若 panic 拖垮了 accept 面,这条流永远无人受理。
	s2 := punchTestOpenStream(t, conn)
	if _, err := s2.Write([]byte("after")); err != nil {
		t.Fatalf("客户端写第二条流失败: %v", err)
	}
	const want = "echo:after"
	echo := make([]byte, len(want))
	if _, err := io.ReadFull(s2, echo); err != nil {
		t.Fatalf("panic 后第二条流读回显失败(accept 面被拖垮?): %v", err)
	}
	if string(echo) != want {
		t.Fatalf("回显不符: 得到 %q, 期望 %q", string(echo), want)
	}
}
