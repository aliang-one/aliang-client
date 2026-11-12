// dialer_test.go 见证 A 侧拨号编排(Task 7)的六面:甜路径(打通+过门+
// 数据往返+后续流免门)/预算耗尽(无候选与对端静默两味)→ ErrPunchTimeout/
// 候选中途送达仍打通/错 token → ErrPunchGateRejected 且只拨一轮(对端握手
// 计数=1)/B 钉错 A 指纹(TLS 1.3 拒单时序不对称)最终失败非假成功/可达
// 候选排第二仍打通。全部 loopback;B 侧尽量用真 ServeListener 保契约保真,
// 证书与拨号件复用 tls_test.go 的既有 helper。
package punch

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
)

// punchDialerUDPConn 起 A 侧打洞 socket(loopback,真绑定链路属 Task 14
// 集成面),清理随测试收摊——所有权契约:dialer 失败收摊只关自建 Transport,
// conn 始终归调用方,由本 helper 关。
func punchDialerUDPConn(t *testing.T) *net.UDPConn {
	t.Helper()
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("起 A 侧打洞 socket 失败: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// punchSilentSocket 起一个收下 QUIC Initial 却永不应答的沉默候选(loopback
// 上绑好就放着):模拟「对端不存在/洞未开」——拨号方 Initial 石沉大海,
// 只能靠单次尝试围栏到点换道。
func punchSilentSocket(t *testing.T) string {
	t.Helper()
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("起沉默候选 socket 失败: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn.LocalAddr().String()
}

// punchEchoHandler 造一个「读 4 字节回显 4 字节」的 B 侧 Handler:甜路径/
// 候选晚到/轮转三用,载荷等长(4B)让首流与第二条流共用同一 Handler。
func punchEchoHandler(t *testing.T) func(net.Conn) {
	t.Helper()
	return func(c net.Conn) {
		buf := make([]byte, 4)
		if _, err := io.ReadFull(c, buf); err != nil {
			t.Errorf("B 侧读载荷失败: %v", err)
			return
		}
		if _, err := c.Write(buf); err != nil {
			t.Errorf("B 侧回显失败: %v", err)
		}
	}
}

// punchEchoRoundtrip 在流上写 4 字节载荷并读回等长回显,读围栏防测试挂死。
func punchEchoRoundtrip(t *testing.T, c net.Conn, payload string) {
	t.Helper()
	if err := c.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("设读围栏失败: %v", err)
	}
	if _, err := c.Write([]byte(payload)); err != nil {
		t.Fatalf("写载荷 %q 失败: %v", payload, err)
	}
	back := make([]byte, len(payload))
	if _, err := io.ReadFull(c, back); err != nil {
		t.Fatalf("读 %q 回显失败: %v", payload, err)
	}
	if string(back) != payload {
		t.Fatalf("回显不符: 得到 %q, 期望 %q", string(back), payload)
	}
}

// punchCountingGateServer 起「握手面全对、门禁面拒死」的 B 侧对端:每条
// 完成握手的连接计数 +1(一轮拨号=一次握手),(d) 用它断言拨号轮次;
// 首流跑 TokenGate,拒单动作与 listener.punchServeConn 同款(CloseWithError
// punchGateErrorCode)——比真 ServeListener 多暴露的只有计数。
func punchCountingGateServer(t *testing.T, expected string) (string, PunchTLSConfig, *atomic.Int32) {
	t.Helper()
	serverCert, serverFP := testPunchCert(t, "punch-gate-server")
	clientCert, clientFP := testPunchCert(t, "punch-gate-client")
	cfg := PunchTLSConfig{
		ServerCert:        serverCert,
		ClientCert:        clientCert,
		ServerFingerprint: serverFP,
		ClientFingerprint: clientFP,
		ExpectedToken:     expected,
	}
	udpConn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("起计数门禁服务端 socket 失败: %v", err)
	}
	tr := &quic.Transport{Conn: udpConn}
	ln, err := tr.Listen(ServerTLSConfig(cfg), nil)
	if err != nil {
		t.Fatalf("起计数门禁 QUIC listener 失败: %v", err)
	}
	count := &atomic.Int32{}
	t.Cleanup(func() {
		_ = ln.Close()
		_ = tr.Close()
		_ = udpConn.Close()
	})
	go func() {
		for {
			qconn, err := ln.Accept(context.Background())
			if err != nil {
				return // Cleanup 关闭 listener 后退出 accept 循环
			}
			count.Add(1)
			go func(c *quic.Conn) {
				stream, err := c.AcceptStream(context.Background())
				if err != nil {
					return
				}
				if err := TokenGate(stream, expected); err != nil {
					// 与 listener.punchServeConn 同款拒单:A 侧凭 code=1 分派拒因。
					_ = c.CloseWithError(punchGateErrorCode, "punch token gate rejected")
					return
				}
				// 测试只走拒单路径;过门成功理论不到,干净关掉收场。
				_ = c.CloseWithError(0, "")
			}(qconn)
		}
	}()
	return udpConn.LocalAddr().String(), cfg, count
}

// ---------------------------------------------------------------------------
// (a) 甜路径:单候选=B listener loopback 地址 → 打通+过门+数据往返+后续流免门
// ---------------------------------------------------------------------------

// TestDialPeerSweetPathPunchesAndStreams 单候选直指真 ServeListener:
// DialPeer 必须打通(预算内握手+过门),出让件三件齐全;首流(dialer 已写
// token 行)上数据往返可用;同会话经 Conn 开第二条流写裸数据直达 Handler
// ——免门契约的 A 侧一半由「拿到 Conn 能开流」作证。
func TestDialPeerSweetPathPunchesAndStreams(t *testing.T) {
	const token = "punch-dial-token"
	addr, cfg, _ := punchListenerEnv(t, token, 5*time.Second, punchEchoHandler(t))

	session, err := DialPeer(punchDialerUDPConn(t), DialOpts{
		Token:      token,
		TLS:        cfg,
		Candidates: func() []string { return []string{addr.String()} },
	})
	if err != nil {
		t.Fatalf("甜路径拨号失败: %v", err)
	}
	t.Cleanup(func() {
		_ = session.Conn.CloseWithError(0, "")
		_ = session.Transport.Close()
	})
	if session.Transport == nil || session.Conn == nil || session.Stream == nil {
		t.Fatal("出让件不全:Transport/Conn/Stream 有 nil(调用方没法收摊/开后续流/用流)")
	}

	// 首流:token 行已由 dialer 写毕,流面对调用方是干净的。
	punchEchoRoundtrip(t, session.Stream, "one1")

	// 同会话第二条流免门:直接开流写裸数据(无 token 行),必须直达 Handler。
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	s2, err := session.Conn.OpenStreamSync(ctx)
	if err != nil {
		t.Fatalf("开第二条流失败: %v", err)
	}
	punchEchoRoundtrip(t, &punchStreamConn{stream: s2, conn: session.Conn}, "two2")
}

// ---------------------------------------------------------------------------
// (b) 预算耗尽:无候选 / 对端静默 → ErrPunchTimeout(errors.Is 可判)
// ---------------------------------------------------------------------------

// TestDialPeerBudgetExhaustionReturnsTimeout 两味预算耗尽:无候选送达与
// 候选存在但对端永不应答。都必须以 ErrPunchTimeout 收场(errors.Is 可判,
// 上层据此落 piko 而非当错误上报),且失败收摊不动调用方的 conn(所有权
// 契约:dialer 只关自建 Transport,conn 关闭权还在调用方手里)。
func TestDialPeerBudgetExhaustionReturnsTimeout(t *testing.T) {
	t.Run("无候选送达", func(t *testing.T) {
		conn := punchDialerUDPConn(t)
		start := time.Now()
		_, err := DialPeer(conn, DialOpts{
			Token:      "punch-dial-token",
			TLS:        PunchTLSConfig{},
			Candidates: func() []string { return nil },
			Budget:     400 * time.Millisecond,
		})
		if !errors.Is(err, ErrPunchTimeout) {
			t.Fatalf("预算耗尽应以 ErrPunchTimeout 收场,得到: %v", err)
		}
		if elapsed := time.Since(start); elapsed > 1500*time.Millisecond {
			t.Fatalf("超预算迟迟不返(耗时 %v),预算语义失守", elapsed)
		}
		// 所有权契约:失败收摊后 conn 仍归调用方——这里还能关掉它;若 dialer
		// 越权关了 conn,二次 Close 会报 use of closed connection。
		if err := conn.Close(); err != nil {
			t.Fatalf("dialer 失败收摊后 conn 应仍可关(所有权被越权?),却得到: %v", err)
		}
	})
	t.Run("对端静默", func(t *testing.T) {
		silent := punchSilentSocket(t)
		_, err := DialPeer(punchDialerUDPConn(t), DialOpts{
			Token:      "punch-dial-token",
			TLS:        PunchTLSConfig{},
			Candidates: func() []string { return []string{silent} },
			Budget:     900 * time.Millisecond,
		})
		if !errors.Is(err, ErrPunchTimeout) {
			t.Fatalf("对端静默烧尽预算应以 ErrPunchTimeout 收场,得到: %v", err)
		}
	})
}

// ---------------------------------------------------------------------------
// (c) 候选中途送达:先空候选起拨,300ms 后注入真候选 → 预算内打通
// ---------------------------------------------------------------------------

// TestDialPeerLateCandidateStillPunches 信令中途送达候选:起拨时无候选,
// 300ms 后真候选注入——下一轮取数必须立即纳入并在默认预算内打通。
// Candidates 设计成取数函数正是为这条路:一次性切片会把「先到先拨」焊死。
func TestDialPeerLateCandidateStillPunches(t *testing.T) {
	const token = "punch-dial-token"
	addr, cfg, _ := punchListenerEnv(t, token, 5*time.Second, punchEchoHandler(t))

	var mu sync.Mutex
	var cands []string
	time.AfterFunc(300*time.Millisecond, func() {
		mu.Lock()
		defer mu.Unlock()
		cands = []string{addr.String()}
	})
	start := time.Now()
	session, err := DialPeer(punchDialerUDPConn(t), DialOpts{
		Token: token,
		TLS:   cfg,
		Candidates: func() []string {
			mu.Lock()
			defer mu.Unlock()
			return append([]string(nil), cands...)
		},
		// Budget 零值走默认 DialBudget(2.5s):晚到候选靠默认预算打通才是
		// 本测要锁的生产行为。
	})
	if err != nil {
		t.Fatalf("候选晚到仍应在预算内打通,却失败: %v", err)
	}
	t.Cleanup(func() {
		_ = session.Conn.CloseWithError(0, "")
		_ = session.Transport.Close()
	})
	if elapsed := time.Since(start); elapsed >= DialBudget {
		t.Fatalf("晚到候选打通耗时 %v 撞满预算 %v,轮询没把新候选纳入", elapsed, DialBudget)
	}
	punchEchoRoundtrip(t, session.Stream, "late")
}

// ---------------------------------------------------------------------------
// (d) token 错 → ErrPunchGateRejected 且只拨一轮即止(断言拨号轮次)
// ---------------------------------------------------------------------------

// TestDialPeerGateRejectedStopsAfterFirstRound B 侧门禁拒单(code=1):
// DialPeer 必须返回专用哨兵 ErrPunchGateRejected 且立即收场不重试——本
// token 永久无效,重试只会烧预算。三重断言:哨兵可判、耗时远小于预算
// (误重试会烧满 2.5s 预算)、对端握手计数恰为 1(一轮=一次握手,多等
// 600ms 超过一轮间隔确证没有第二轮拨上来)。
func TestDialPeerGateRejectedStopsAfterFirstRound(t *testing.T) {
	addr, cfg, count := punchCountingGateServer(t, "right-token")

	start := time.Now()
	_, err := DialPeer(punchDialerUDPConn(t), DialOpts{
		Token:      "wrong-token", // 与 B 期望不符:门禁拒单路径
		TLS:        cfg,
		Candidates: func() []string { return []string{addr} },
		// Budget 走默认 2.5s:若实现误把拒单当可重试,会烧满预算才返回——
		// 耗时断言在此现形。
	})
	elapsed := time.Since(start)
	if !errors.Is(err, ErrPunchGateRejected) {
		t.Fatalf("门禁拒单应以 ErrPunchGateRejected 收场,得到: %v", err)
	}
	if elapsed >= 1500*time.Millisecond {
		t.Fatalf("拒单应立即返回不重试,却耗时 %v(疑似把拒单当可重试烧预算)", elapsed)
	}
	time.Sleep(600 * time.Millisecond) // 超过一轮间隔(300ms)+握手余量
	if got := count.Load(); got != 1 {
		t.Fatalf("拒单后应只拨一轮即止,对端见证了 %d 次握手", got)
	}
}

// ---------------------------------------------------------------------------
// (e) TLS 1.3 拒单时序不对称:B 钉错 A 指纹 → 拨号最终失败(非假成功)
// ---------------------------------------------------------------------------

// TestDialPeerTLSAsymmetryNeverFakeSuccess B 用 decoy 指纹钉 A:握手可能
// 先「成功」随即死(Task 5 实锤)。DialPeer 绝不能 Dial 返回就当成功——
// 存活确认必须把假成功拦下,逐轮可重试地烧完预算,以 ErrPunchTimeout
// 收场(err 非 nil 本身就是「非假成功」的见证);被拒握手绝不经 Accept
// 交付 B 侧上层。
func TestDialPeerTLSAsymmetryNeverFakeSuccess(t *testing.T) {
	serverCert, serverFP := testPunchCert(t, "punch-asym-server")
	clientCert, _ := testPunchCert(t, "punch-asym-client")
	_, decoyFP := testPunchCert(t, "punch-asym-decoy")
	// B 侧钉的是 decoy:进来的真 A 证书过不了 B 的门(TLS 面拒单)。
	addr, accepted := punchTestServer(t, ServerTLSConfig(PunchTLSConfig{
		ServerCert:        serverCert,
		ClientFingerprint: decoyFP,
	}), func(*quic.Stream) {
		t.Error("TLS 拒单的握手不应把流交付上层")
	})

	session, err := DialPeer(punchDialerUDPConn(t), DialOpts{
		Token: "punch-dial-token",
		TLS: PunchTLSConfig{
			ClientCert:        clientCert,
			ServerFingerprint: serverFP, // A 钉 B 是对的:A 自己的握手面愿意成
		},
		Candidates: func() []string { return []string{addr.String()} },
		Budget:     900 * time.Millisecond, // 缩短预算省测试时间,烧尽路径同构
	})
	if err == nil {
		_ = session.Conn.CloseWithError(0, "")
		_ = session.Transport.Close()
		t.Fatal("B 拒 A 指纹的握手「先成功随即死」,dialer 假成功放行——存活确认失守")
	}
	if !errors.Is(err, ErrPunchTimeout) {
		t.Fatalf("TLS 拒单逐轮烧尽预算应以 ErrPunchTimeout 收场,得到: %v", err)
	}
	if errors.Is(err, ErrPunchGateRejected) {
		t.Fatalf("TLS 面拒单不是门禁拒单,不应误判成 ErrPunchGateRejected: %v", err)
	}
	punchAssertNotAccepted(t, accepted)
}

// ---------------------------------------------------------------------------
// (f) 多候选:可达候选排第二 → 仍打通(轮转覆盖)
// ---------------------------------------------------------------------------

// TestDialPeerRotatesToSecondCandidate 候选表 [沉默死洞, 真B]:每轮洗牌
// 两种顺序都会出现——可达候选无论排第一第二,同一轮内顺序遍历必须覆盖
// 到它并打通(死洞那次尝试由单次围栏到点放行换道,不锁死整轮)。
func TestDialPeerRotatesToSecondCandidate(t *testing.T) {
	const token = "punch-dial-token"
	addr, cfg, _ := punchListenerEnv(t, token, 5*time.Second, punchEchoHandler(t))
	dead := punchSilentSocket(t)

	session, err := DialPeer(punchDialerUDPConn(t), DialOpts{
		Token: token,
		TLS:   cfg,
		Candidates: func() []string {
			return []string{dead, addr.String()} // 可达候选恒排第二
		},
		// Budget 走默认 2.5s:洗牌把死洞排第一时,死尝试(750ms 围栏)+
		// 真候选握手要在一轮内走完,默认预算余量充分。
	})
	if err != nil {
		t.Fatalf("可达候选排第二仍应打通(轮转覆盖),却失败: %v", err)
	}
	t.Cleanup(func() {
		_ = session.Conn.CloseWithError(0, "")
		_ = session.Transport.Close()
	})
	punchEchoRoundtrip(t, session.Stream, "rot2")
}
