package punch

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"errors"
	"io"
	"math/big"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
)

// ---------------------------------------------------------------------------
// 测试基建:自签证书 helper + loopback QUIC 服务端/客户端
// ---------------------------------------------------------------------------

// testPunchCert 测试现签一张 ECDSA P-256 自签 1h 证书,镜像 services 层
// generateAgentLanCertificate 的机器(agent_peer_lan.go:130)但零依赖
// services——punch 包依赖单向(services 依赖 punch),测试自带同构
// helper;指纹在测试里独立算(叶子 DER 的 sha256 小写 hex),不复用实现
// 里的函数,保住测试对格式契约的独立见证。
func testPunchCert(t *testing.T, commonName string) (tls.Certificate, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("生成 ECDSA 密钥失败: %v", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatalf("生成序列号失败: %v", err)
	}
	template := x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: commonName},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		// 测试证书在 (a)(d) 里同时当服务端与客户端证书用,两种 EKU 都给。
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("自签证书失败: %v", err)
	}
	sum := sha256.Sum256(der)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, hex.EncodeToString(sum[:])
}

// punchTestServer 起 loopback QUIC 服务端:自带 UDP socket + quic.Transport
// + Listener(刻意不碰 Task 3 的物理接口绑定——真绑定链路属 Task 14 集成
// 面);accept 循环把每条完成握手的连接记入 accepted 通道——(c)/(e) 用
// 它证伪「服务端把该连接交付了上层」,首流交给 handle。返回服务端地址。
func punchTestServer(t *testing.T, tlsConf *tls.Config, handle func(*quic.Stream)) (net.Addr, <-chan *quic.Conn) {
	t.Helper()
	udpConn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("起服务端 UDP socket 失败: %v", err)
	}
	tr := &quic.Transport{Conn: udpConn}
	listener, err := tr.Listen(tlsConf, nil)
	if err != nil {
		t.Fatalf("起 QUIC listener 失败: %v", err)
	}
	accepted := make(chan *quic.Conn, 4)
	t.Cleanup(func() {
		_ = listener.Close()
		_ = tr.Close()
		_ = udpConn.Close()
	})
	go func() {
		for {
			conn, err := listener.Accept(context.Background())
			if err != nil {
				return // listener 已被 Cleanup 关闭,退出 accept 循环
			}
			accepted <- conn
			go func(c *quic.Conn) {
				stream, err := c.AcceptStream(context.Background())
				if err != nil {
					return
				}
				handle(stream)
			}(conn)
		}
	}()
	return udpConn.LocalAddr(), accepted
}

// punchTestClientTransport 起 loopback 客户端 Transport,清理随测试收摊。
func punchTestClientTransport(t *testing.T) *quic.Transport {
	t.Helper()
	udpConn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("起客户端 UDP socket 失败: %v", err)
	}
	tr := &quic.Transport{Conn: udpConn}
	t.Cleanup(func() {
		_ = tr.Close()
		_ = udpConn.Close()
	})
	return tr
}

// punchTestDial 拨号;握手失败即 Fatal——(b)(c)(e) 里「拨号可能失败」
// 本身是断言对象,不走本 helper。
func punchTestDial(t *testing.T, addr net.Addr, tlsConf *tls.Config) *quic.Conn {
	t.Helper()
	tr := punchTestClientTransport(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := tr.Dial(ctx, addr, tlsConf, nil)
	if err != nil {
		t.Fatalf("客户端拨号失败: %v", err)
	}
	t.Cleanup(func() { _ = conn.CloseWithError(0, "") })
	return conn
}

// punchTestOpenStream 开首流并给一个 5s 读围栏,纯测试便利件。
func punchTestOpenStream(t *testing.T, conn *quic.Conn) *quic.Stream {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, err := conn.OpenStreamSync(ctx)
	if err != nil {
		t.Fatalf("开首流失败: %v", err)
	}
	if err := stream.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("设客户端读围栏失败: %v", err)
	}
	return stream
}

// punchAssertNotAccepted (c)/(e) 用:客户端已亲历服务端拒单(拒单引发的
// CONNECTION_CLOSE 已送达),此刻服务端的裁决是终局——accepted 通道不可
// 能再收到这条连接。小睡一拍只为通道投递留因果余量,非轮询等待。
func punchAssertNotAccepted(t *testing.T, accepted <-chan *quic.Conn) {
	t.Helper()
	select {
	case conn := <-accepted:
		t.Fatalf("服务端不应把被拒连接交付上层,却 Accept 到来自 %v 的连接", conn.RemoteAddr())
	case <-time.After(200 * time.Millisecond):
	}
}

// punchClientMustSeeFailure 断言客户端侧迟早观察到失败。TLS 1.3 下客户端
// 先收到服务端 Finished,握手可能先行成功——服务端的拒单随后必到;开流
// 读面必须以「非自身读围栏到点」的错误收场,纯 deadline 超时不算数
// (那说明拒单没送达,是挂死不是拒绝)。
func punchClientMustSeeFailure(t *testing.T, conn *quic.Conn) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, err := conn.OpenStreamSync(ctx)
	if err != nil {
		return // 开流面已失败,同为「客户端侧收到失败」的见证
	}
	if err := stream.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatalf("设客户端读围栏失败: %v", err)
	}
	buf := make([]byte, 64)
	if _, err := stream.Read(buf); err == nil {
		t.Fatal("期望客户端读到服务端拒单(连接关断),却读到了数据")
	} else if errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("客户端只等到了自己的读围栏——服务端拒单未送达: %v", err)
	}
}

// ---------------------------------------------------------------------------
// (a) 指纹互钉正确 → QUIC 握手成功 + 首流双向读写
// ---------------------------------------------------------------------------

// TestPunchTLSMutualPinHandshakeAndStream 双向钉扎全对的甜路径:QUIC 握手
// 成功(A 钉 B 对 + B 钉 A 对),首流过 TokenGate 后双向读写都在线——
// 这是 Task 6/7 listener/dialer 赖以立足的地基,必须真握手而非只测
// tls.Config 装配。
func TestPunchTLSMutualPinHandshakeAndStream(t *testing.T) {
	serverCert, serverFP := testPunchCert(t, "punch-test-server")
	clientCert, clientFP := testPunchCert(t, "punch-test-client")
	cfg := PunchTLSConfig{
		ServerCert:        serverCert,
		ClientCert:        clientCert,
		ServerFingerprint: serverFP,
		ClientFingerprint: clientFP,
		ExpectedToken:     "punch-test-token",
	}

	const ping = "ping\n"
	const pong = "pong:ping\n"
	gateEcho := make(chan string, 1)
	addr, _ := punchTestServer(t, ServerTLSConfig(cfg), func(stream *quic.Stream) {
		if err := TokenGate(stream, cfg.ExpectedToken); err != nil {
			t.Errorf("token 对却过门失败: %v", err)
			return
		}
		got := make([]byte, len(ping))
		if _, err := io.ReadFull(stream, got); err != nil {
			t.Errorf("服务端读 ping 失败: %v", err)
			return
		}
		if _, err := stream.Write([]byte(pong)); err != nil {
			t.Errorf("服务端写 pong 失败: %v", err)
			return
		}
		gateEcho <- string(got)
	})

	conn := punchTestDial(t, addr, ClientTLSConfig(cfg))
	stream := punchTestOpenStream(t, conn)
	// token 行 + 紧随的数据一次写入:顺带见证 TokenGate 逐字节读不吞后续
	// 字节(吞了 ping 就读不回 pong)。
	if _, err := stream.Write([]byte("punch-test-token\n" + ping)); err != nil {
		t.Fatalf("客户端写 token+ping 失败: %v", err)
	}
	gotPong := make([]byte, len(pong))
	if _, err := io.ReadFull(stream, gotPong); err != nil {
		t.Fatalf("客户端读 pong 失败(双向流读写未成): %v", err)
	}
	if string(gotPong) != pong {
		t.Fatalf("pong 内容不符: 得到 %q, 期望 %q", string(gotPong), pong)
	}
	select {
	case got := <-gateEcho:
		if got != ping {
			t.Fatalf("服务端收到的 ping 不符: %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("等服务端回显超时")
	}
}

// ---------------------------------------------------------------------------
// (b) ServerFingerprint 错 → 客户端握手失败
// ---------------------------------------------------------------------------

// TestPunchTLSClientRejectsWrongServerFingerprint A 钉 B 的指纹不对(拿
// 第三张证书的指纹冒充 B):客户端握手必须失败——fail-closed,无 TOFU
// 回退、绝不降级成「先连上再说」。
func TestPunchTLSClientRejectsWrongServerFingerprint(t *testing.T) {
	serverCert, _ := testPunchCert(t, "punch-test-server")
	clientCert, clientFP := testPunchCert(t, "punch-test-client")
	_, decoyFP := testPunchCert(t, "punch-test-decoy")
	serverCfg := PunchTLSConfig{
		ServerCert:        serverCert,
		ClientFingerprint: clientFP,
	}
	addr, _ := punchTestServer(t, ServerTLSConfig(serverCfg), func(stream *quic.Stream) {
		t.Error("指纹不匹配的握手不应把流交付上层")
	})

	clientCfg := ClientTLSConfig(PunchTLSConfig{
		ClientCert: clientCert,
		// A 侧钉的是 decoy 的指纹:格式合法但不是 B 的——正是钉扎要拦的
		// 「证书被换」场景。
		ServerFingerprint: decoyFP,
	})
	tr := punchTestClientTransport(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := tr.Dial(ctx, addr, clientCfg, nil); err == nil {
		t.Fatal("ServerFingerprint 钉错,客户端握手必须失败,却拨号成功")
	} else if errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("客户端失败只是握手围栏到点——钉扎没拦住: %v", err)
	}
}

// ---------------------------------------------------------------------------
// (c) ClientFingerprint 错 → 服务端拒、客户端侧收到握手失败
// ---------------------------------------------------------------------------

// TestPunchTLSServerRejectsWrongClientFingerprint B 钉 A 的指纹不对:服务端
// 必须拒(TLS 1.3 下拒单以 CONNECTION_CLOSE 送达客户端),客户端侧迟早
// 观察到失败;且被拒连接绝不经 Accept 交付上层。
func TestPunchTLSServerRejectsWrongClientFingerprint(t *testing.T) {
	serverCert, serverFP := testPunchCert(t, "punch-test-server")
	clientCert, _ := testPunchCert(t, "punch-test-client")
	_, decoyFP := testPunchCert(t, "punch-test-decoy")
	serverCfg := PunchTLSConfig{
		ServerCert: serverCert,
		// B 侧钉的是 decoy 指纹:进来的真 A 证书过不了门。
		ClientFingerprint: decoyFP,
	}
	addr, accepted := punchTestServer(t, ServerTLSConfig(serverCfg), func(stream *quic.Stream) {
		t.Error("指纹不匹配的握手不应把流交付上层")
	})

	tr := punchTestClientTransport(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, dialErr := tr.Dial(ctx, addr, ClientTLSConfig(PunchTLSConfig{
		ClientCert:        clientCert,
		ServerFingerprint: serverFP,
	}), nil)
	if dialErr != nil {
		// 握手面已失败,同样成立;继续验证服务端未交付。
		_ = conn
	} else {
		t.Cleanup(func() { _ = conn.CloseWithError(0, "") })
		punchClientMustSeeFailure(t, conn)
	}
	punchAssertNotAccepted(t, accepted)
}

// ---------------------------------------------------------------------------
// (d) TokenGate:token 对→过;token 错→error;半截行吊着→围栏到点;空期望→拒
// ---------------------------------------------------------------------------

// punchTokenGateEnv 起「指纹互钉全对」的 loopback QUIC 对端:服务端首流
// 跑 TokenGate 并把结果投回 gateCh,过门后可选继续执行 afterGate(d1 用
// 它验证围栏已清、过门后服务端读面仍活);返回服务端地址与两端共用的
// 配置(客户端据此拨号)。
func punchTokenGateEnv(t *testing.T, expected string, afterGate func(*quic.Stream)) (net.Addr, PunchTLSConfig, <-chan error) {
	t.Helper()
	serverCert, serverFP := testPunchCert(t, "punch-test-server")
	clientCert, clientFP := testPunchCert(t, "punch-test-client")
	cfg := PunchTLSConfig{
		ServerCert:        serverCert,
		ClientCert:        clientCert,
		ServerFingerprint: serverFP,
		ClientFingerprint: clientFP,
		ExpectedToken:     expected,
	}
	gated := make(chan error, 1)
	addr, _ := punchTestServer(t, ServerTLSConfig(cfg), func(stream *quic.Stream) {
		gated <- TokenGate(stream, expected)
		if afterGate != nil {
			afterGate(stream)
		}
	})
	return addr, cfg, gated
}

// punchAwaitGate 等门禁裁决落地,超时即测试失败。
func punchAwaitGate(t *testing.T, gated <-chan error) error {
	t.Helper()
	select {
	case err := <-gated:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("等 TokenGate 裁决超时")
		return nil
	}
}

// TestPunchTokenGateAcceptsCorrectToken token 正确 → nil,且过门后服务端
// 读面立即再用(读围栏已清,不是「过门即死流」)——围栏清没清只有服务端
// 的下一次读能作证:没清,下一次读 5s 后撞 deadline。
func TestPunchTokenGateAcceptsCorrectToken(t *testing.T) {
	const probe = "after-gate\n"
	addr, cfg, gated := punchTokenGateEnv(t, "punch-test-token", func(stream *quic.Stream) {
		// 过门后服务端再读一行并回显:围栏若没清,这里会卡到读 deadline。
		if err := stream.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
			t.Errorf("服务端设读围栏失败: %v", err)
			return
		}
		buf := make([]byte, len(probe))
		if _, err := io.ReadFull(stream, buf); err != nil {
			t.Errorf("过门后服务端读 probe 失败(围栏未清?): %v", err)
			return
		}
		if _, err := stream.Write(buf); err != nil {
			t.Errorf("服务端回显 probe 失败: %v", err)
		}
	})
	conn := punchTestDial(t, addr, ClientTLSConfig(cfg))
	stream := punchTestOpenStream(t, conn)
	// token 行与 probe 一次写入:顺带见证逐字节门禁不吞后续字节。
	if _, err := stream.Write([]byte("punch-test-token\n" + probe)); err != nil {
		t.Fatalf("客户端写 token+probe 失败: %v", err)
	}
	if err := punchAwaitGate(t, gated); err != nil {
		t.Fatalf("token 对却过门失败: %v", err)
	}
	echo := make([]byte, len(probe))
	if _, err := io.ReadFull(stream, echo); err != nil {
		t.Fatalf("客户端读回显失败: %v", err)
	}
	if string(echo) != probe {
		t.Fatalf("回显不符: 得到 %q, 期望 %q", string(echo), probe)
	}
}

// TestPunchTokenGateLineOverLimitFailsClosed 超长行(>punchTokenLineLimit
// 无换行)必须以超限错误 fail-closed——本文件唯一防内存轰炸的安全守卫,
// 重构循环时被无声丢掉的唯一信号在此钉住(质量审查 Minor 1 补测)。
func TestPunchTokenGateLineOverLimitFailsClosed(t *testing.T) {
	addr, cfg, gated := punchTokenGateEnv(t, "punch-test-token", nil)
	conn := punchTestDial(t, addr, ClientTLSConfig(cfg))
	stream := punchTestOpenStream(t, conn)
	huge := strings.Repeat("x", punchTokenLineLimit+1) // 无换行
	if _, err := stream.Write([]byte(huge)); err != nil {
		t.Fatalf("客户端写超长行失败: %v", err)
	}
	err := punchAwaitGate(t, gated)
	if err == nil {
		t.Fatal("超长行应 fail-closed 被拒")
	}
	if !strings.Contains(err.Error(), "token 行超限") {
		t.Fatalf("应报行超限而非其他错误: %v", err)
	}
}

// TestPunchTokenGateRejectsWrongToken token 不匹配 → error(不 panic、不
// 静默放行)。
func TestPunchTokenGateRejectsWrongToken(t *testing.T) {
	addr, cfg, gated := punchTokenGateEnv(t, "punch-test-token", nil)
	conn := punchTestDial(t, addr, ClientTLSConfig(cfg))
	stream := punchTestOpenStream(t, conn)
	if _, err := stream.Write([]byte("wrong-token\n")); err != nil {
		t.Fatalf("客户端写错 token 失败: %v", err)
	}
	err := punchAwaitGate(t, gated)
	if err == nil {
		t.Fatal("token 错,TokenGate 必须报错,却放行了")
	}
}

// TestPunchTokenGateDeadlineFencesSilentPeer 半截 token 行后闭嘴的慢速
// 占用:读围栏到点必须报错。注:开流后一字节不发对端根本看不见(QUIC
// 的空流无帧可发、AcceptStream 不醒),门禁围栏真实要拦的正是「起了个
// 头就吊着」的连接——写半截行再沉默。围栏调小到 50ms 避免真等 5s
// (包变量可调是 stun.go classifyDiscoverTimeout 的既定先例)。
func TestPunchTokenGateDeadlineFencesSilentPeer(t *testing.T) {
	old := punchTokenGateTimeout
	punchTokenGateTimeout = 50 * time.Millisecond
	t.Cleanup(func() { punchTokenGateTimeout = old })

	addr, cfg, gated := punchTokenGateEnv(t, "punch-test-token", nil)
	conn := punchTestDial(t, addr, ClientTLSConfig(cfg))
	stream := punchTestOpenStream(t, conn)
	if _, err := stream.Write([]byte("pun")); err != nil { // 半截行,无换行,随后沉默
		t.Fatalf("客户端写半截 token 失败: %v", err)
	}
	err := punchAwaitGate(t, gated)
	if err == nil {
		t.Fatal("半截行吊着不写完,必须被读围栏拦下,TokenGate 却放行了")
	}
	if !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("围栏到点应以 deadline 错误收场,得到: %v", err)
	}
}

// TestPunchTokenGateEmptyExpectedTokenFailsClosed 期望 token 为空串:空串
// 对空串在常量时间比对里是「相等」——门禁不能因此形同虚设,必须在比对
// 前就 fail-closed。
func TestPunchTokenGateEmptyExpectedTokenFailsClosed(t *testing.T) {
	addr, cfg, gated := punchTokenGateEnv(t, "", nil)
	conn := punchTestDial(t, addr, ClientTLSConfig(cfg))
	stream := punchTestOpenStream(t, conn)
	if _, err := stream.Write([]byte("\n")); err != nil {
		t.Fatalf("客户端写空 token 行失败: %v", err)
	}
	err := punchAwaitGate(t, gated)
	if err == nil {
		t.Fatal("期望 token 为空串时必须 fail-closed,却放行了")
	}
}

// ---------------------------------------------------------------------------
// (e) 无客户端证书 → 服务端拒
// ---------------------------------------------------------------------------

// TestPunchTLSServerRejectsCertlessClient 客户端不带证书(裸 InsecureSkipVerify
// 客户端):RequireAnyClientCert 必须把连接拒之门外,客户端侧观察到失败,
// 且绝不经 Accept 交付上层——双向 mTLS 的「双向」缺一不可。
func TestPunchTLSServerRejectsCertlessClient(t *testing.T) {
	serverCert, serverFP := testPunchCert(t, "punch-test-server")
	_, clientFP := testPunchCert(t, "punch-test-client")
	serverCfg := PunchTLSConfig{
		ServerCert:        serverCert,
		ClientFingerprint: clientFP,
	}
	addr, accepted := punchTestServer(t, ServerTLSConfig(serverCfg), func(stream *quic.Stream) {
		t.Error("无客户端证书的握手不应把流交付上层")
	})

	// 裸客户端:钉 B 指纹正确、ALPN 正确,唯独不出示客户端证书。
	certless := ClientTLSConfig(PunchTLSConfig{ServerFingerprint: serverFP})
	certless.Certificates = nil
	tr := punchTestClientTransport(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, dialErr := tr.Dial(ctx, addr, certless, nil)
	if dialErr != nil {
		_ = conn
	} else {
		t.Cleanup(func() { _ = conn.CloseWithError(0, "") })
		punchClientMustSeeFailure(t, conn)
	}
	punchAssertNotAccepted(t, accepted)
}

// ---------------------------------------------------------------------------
// (f) ClientFingerprint 空 → 收任意出示的客户端证书(v1 线协议约束)
// ---------------------------------------------------------------------------

// TestPunchTLSServerEmptyClientFingerprintAcceptsAnyCert 线协议 v1 的
// peer.punch.session 帧不携带 A 侧证书指纹(Task 1 契约表锁定),B 侧组装
// PunchTLSConfig 时 ClientFingerprint 只能是空串——空串必须降级为「仍要求
// 出示证书(RequireAnyClientCert 拦无证书者)、但不钉扎指纹」,否则 B 侧
// listener 会把每个真实 A 都拒之门外。B 侧访问控制由 token 首流门禁承载;
// A 钉 B(ServerFingerprint)方向保持 fail-closed 不动(下一测另证)。
func TestPunchTLSServerEmptyClientFingerprintAcceptsAnyCert(t *testing.T) {
	serverCert, serverFP := testPunchCert(t, "punch-unpinned-server")
	// B 侧配置零 ClientFingerprint——services 层(Task 10)真实现场的形态。
	serverCfg := PunchTLSConfig{ServerCert: serverCert}
	// A 侧证书与 B 完全无关联(现签另一张),指纹互不可知。
	clientCert, _ := testPunchCert(t, "punch-stranger-client")
	addr, accepted := punchTestServer(t, ServerTLSConfig(serverCfg), func(stream *quic.Stream) {
		// 握手过了就行,流面无人消费也要关掉防吊着。
		_ = stream.Close()
	})

	conn := punchTestDial(t, addr, ClientTLSConfig(PunchTLSConfig{
		ClientCert:        clientCert,
		ServerFingerprint: serverFP,
	}))
	select {
	case <-accepted:
	case <-time.After(5 * time.Second):
		t.Fatal("空 ClientFingerprint 应放行任意出示证书的客户端,连接未被 Accept")
	}
	_ = conn.CloseWithError(0, "")

	// 降级不等于放弃「双向」:无证书客户端仍必须被 RequireAnyClientCert 拒。
	certless := ClientTLSConfig(PunchTLSConfig{ServerFingerprint: serverFP})
	certless.Certificates = nil
	tr := punchTestClientTransport(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c2, dialErr := tr.Dial(ctx, addr, certless, nil)
	if dialErr != nil {
		_ = c2
	} else {
		t.Cleanup(func() { _ = c2.CloseWithError(0, "") })
		punchClientMustSeeFailure(t, c2)
	}
	punchAssertNotAccepted(t, accepted)
}

// TestPunchTLSClientEmptyServerFingerprintStillFailsClosed 对照组:A 侧
// ServerFingerprint 为空串时必须维持 fail-closed(不降级、无 TOFU)——降级
// 只发生在 B 侧(客户端证书方向),服务端指纹方向的信任件一刀不松。
func TestPunchTLSClientEmptyServerFingerprintStillFailsClosed(t *testing.T) {
	serverCert, _ := testPunchCert(t, "punch-fp-server")
	clientCert, clientFP := testPunchCert(t, "punch-fp-client")
	addr, _ := punchTestServer(t, ServerTLSConfig(PunchTLSConfig{
		ServerCert:        serverCert,
		ClientFingerprint: clientFP,
	}), func(stream *quic.Stream) {
		t.Error("空 ServerFingerprint 的握手不应把流交付上层")
	})

	tr := punchTestClientTransport(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, dialErr := tr.Dial(ctx, addr, ClientTLSConfig(PunchTLSConfig{
		ClientCert: clientCert,
		// ServerFingerprint 故意空串:A 钉 B 方向 fail-closed 不许降级。
	}), nil)
	if dialErr == nil {
		t.Cleanup(func() { _ = conn.CloseWithError(0, "") })
		punchClientMustSeeFailure(t, conn)
	} else if errors.Is(dialErr, os.ErrDeadlineExceeded) {
		t.Fatalf("拒绝只是握手围栏到点——fail-closed 没拦住: %v", dialErr)
	}
}
