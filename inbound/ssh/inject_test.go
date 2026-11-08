package ssh

import (
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"

	M "aliang.one/nursorgate/inbound/tun/metadata"
	tcphandler "aliang.one/nursorgate/processor/tcp"

	gossh "golang.org/x/crypto/ssh"
)

// stubTCPHandler 是 processor/tcp 管线的测试桩(Handle 换成注入闭包)。
type stubTCPHandler struct {
	handle func(ctx context.Context, conn net.Conn, metadata *M.Metadata) error
}

func (s *stubTCPHandler) Handle(ctx context.Context, conn net.Conn, metadata *M.Metadata) error {
	return s.handle(ctx, conn, metadata)
}

// swapTCPHandler 换入全局 TCP 管线 handler(tcphandler.GetHandler/SetHandler
// 是真实注入点)。SetHandler 影响进程级单例:本包测试不 t.Parallel,
// 串行互不踩;-count=2 同进程跑两轮,还原必须用 SetHandler(原 handler
// 快照)而非 ResetHandler,保证后续 GetHandler 稳定。
func swapTCPHandler(t *testing.T, handle func(ctx context.Context, conn net.Conn, metadata *M.Metadata) error) {
	t.Helper()
	orig := tcphandler.GetHandler()
	tcphandler.SetHandler(&stubTCPHandler{handle: handle})
	t.Cleanup(func() { tcphandler.SetHandler(orig) })
}

// openDirectTCPIP 客户端侧发起 direct-tcpip 通道(目标四元组进 payload)。
func openDirectTCPIP(t *testing.T, conn gossh.Conn, addr string, port uint32) gossh.Channel {
	t.Helper()
	payload := gossh.Marshal(directTCPIPMsg{Addr: addr, Port: port, OriginAddr: "127.0.0.1", OriginPort: 0})
	ch, _, err := conn.OpenChannel(channelDirectTCPIP, payload)
	if err != nil {
		t.Fatalf("direct-tcpip open: %v", err)
	}
	return ch
}

func TestBareStreamInjectsIntoPipeline(t *testing.T) {
	// 占一个真实本地端口当目标地址样本(管线桩直接经 channel 回显,
	// 不真正外拨——断言核心是 Metadata 的目标/源信息)。
	target, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve target port: %v", err)
	}
	defer func() { _ = target.Close() }()
	targetPort := uint32(target.Addr().(*net.TCPAddr).Port)

	metaCh := make(chan *M.Metadata, 1)
	handleDone := make(chan struct{})
	swapTCPHandler(t, func(ctx context.Context, conn net.Conn, metadata *M.Metadata) error {
		defer close(handleDone)
		metaCh <- metadata
		// 管线桩回写:channel 的读写经 channelConn 适配器直连,双向通。
		_, _ = io.Copy(conn, conn)
		return nil
	})

	humanPub, humanSigner := newTestKey(t)
	_, addr := startTestServer(t, RunConfig{AuthorizedKeys: []gossh.PublicKey{humanPub}})
	conn, err := dialIngress(t, addr, gossh.PublicKeys(humanSigner))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	ch := openDirectTCPIP(t, conn, "127.0.0.1", targetPort)

	// 端到端:客户端写入→桩回显→客户端读到(证明通道双向通)。
	msg := []byte("ping over direct-tcpip")
	if _, err := ch.Write(msg); err != nil {
		t.Fatalf("client write: %v", err)
	}
	echo := make([]byte, len(msg))
	if _, err := io.ReadFull(ch, echo); err != nil {
		t.Fatalf("client echo read: %v", err)
	}
	if string(echo) != string(msg) {
		t.Fatalf("echo = %q, want %q", echo, msg)
	}

	// 桩侧断言 Metadata:目标/源/ConnID/网络类型。
	var got *M.Metadata
	select {
	case got = <-metaCh:
	case <-time.After(3 * time.Second):
		t.Fatal("bare stream must reach the pipeline handler")
	}
	if got.HostName != "127.0.0.1" {
		t.Fatalf("HostName = %q, want 127.0.0.1", got.HostName)
	}
	if got.DstPort != uint16(targetPort) {
		t.Fatalf("DstPort = %d, want %d", got.DstPort, targetPort)
	}
	if !got.SrcIP.IsValid() || got.SrcIP != netip.MustParseAddr("127.0.0.1") {
		t.Fatalf("SrcIP = %q, want 127.0.0.1 (from ssh conn RemoteAddr)", got.SrcIP)
	}
	if got.SrcPort == 0 {
		t.Fatal("SrcPort must carry the client ephemeral port, got 0")
	}
	if !strings.HasPrefix(got.ConnID, "sshing-") {
		t.Fatalf("ConnID = %q, want prefix sshing-", got.ConnID)
	}
	if got.Network != M.TCP {
		t.Fatalf("Network = %v, want M.TCP (uint8 iota)", got.Network)
	}
	if got.DstIP.IsValid() {
		t.Fatalf("DstIP must stay unset (pipeline re-resolves at tunnel time), got %q", got.DstIP)
	}

	// 收尾:客户端半关→桩 echo copy EOF→Handle 返回→服务端关通道,
	// 客户端读到 EOF。
	if err := ch.CloseWrite(); err != nil {
		t.Fatalf("close write: %v", err)
	}
	if _, err := ch.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("expected EOF after pipeline handler closed channel, got %v", err)
	}
	select {
	case <-handleDone:
	case <-time.After(3 * time.Second):
		t.Fatal("pipeline handler must return after channel teardown")
	}
}

func TestBareStreamConnIDAndCtxCarry(t *testing.T) {
	type handleRecord struct {
		connID string
		ctxOK  bool
	}
	recCh := make(chan handleRecord, 4)
	exited := make(chan struct{}, 4)
	swapTCPHandler(t, func(ctx context.Context, conn net.Conn, metadata *M.Metadata) error {
		recCh <- handleRecord{connID: metadata.ConnID, ctxOK: ctx != nil}
		<-ctx.Done() // 感知连接级取消
		exited <- struct{}{}
		return nil
	})

	humanPub, humanSigner := newTestKey(t)
	_, addr := startTestServer(t, RunConfig{AuthorizedKeys: []gossh.PublicKey{humanPub}})
	conn, err := dialIngress(t, addr, gossh.PublicKeys(humanSigner))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	// 同一 SSH 连接开两条通道:ConnID 各自独立且前缀 sshing-。
	// (fire-and-forget:通道引用无需保留,生命周期随连接清理收口。)
	openDirectTCPIP(t, conn, "127.0.0.1", 443)
	openDirectTCPIP(t, conn, "127.0.0.1", 443)

	var recs []handleRecord
	for len(recs) < 2 {
		select {
		case r := <-recCh:
			recs = append(recs, r)
		case <-time.After(3 * time.Second):
			t.Fatal("both channels must reach the pipeline handler")
		}
	}
	for i, r := range recs {
		if !strings.HasPrefix(r.connID, "sshing-") {
			t.Fatalf("conn %d ConnID = %q, want prefix sshing-", i, r.connID)
		}
		if !r.ctxOK {
			t.Fatalf("conn %d ctx must be non-nil", i)
		}
	}
	if recs[0].connID == recs[1].connID {
		t.Fatalf("channels must get distinct ConnIDs, both %q", recs[0].connID)
	}

	// 客户端断开 SSH 连接→服务端连接拆除→连接级 ctx 取消→在途 Handle
	// 可感知并退出。
	if err := conn.Close(); err != nil {
		t.Fatalf("client close: %v", err)
	}
	for i := 0; i < 2; i++ {
		select {
		case <-exited:
		case <-time.After(3 * time.Second):
			t.Fatal("in-flight Handle must observe ctx cancellation after connection teardown")
		}
	}
}

func TestStopWithInflightChannel(t *testing.T) {
	// 质量审查点名覆盖缺口:已认证客户端持打开通道在传数据 → Stop() →
	// 在途 Handle 感知 ctx 取消;Stop 有界返回(wg.Wait 不挂);客户端
	// 通道读到 EOF/错误。
	entered := make(chan struct{}, 1)
	exited := make(chan struct{}, 1)
	swapTCPHandler(t, func(ctx context.Context, conn net.Conn, metadata *M.Metadata) error {
		entered <- struct{}{}
		<-ctx.Done()
		_ = conn.Close()
		exited <- struct{}{}
		return ctx.Err()
	})

	humanPub, humanSigner := newTestKey(t)
	s, addr := startTestServer(t, RunConfig{AuthorizedKeys: []gossh.PublicKey{humanPub}})
	conn, err := dialIngress(t, addr, gossh.PublicKeys(humanSigner))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	ch := openDirectTCPIP(t, conn, "127.0.0.1", 443)
	if _, err := ch.Write([]byte("in-flight")); err != nil {
		t.Fatalf("client write: %v", err)
	}
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("Handle must be in-flight before Stop")
	}

	// Stop 必须有界返回:注入协程不入 WaitGroup,在途 Handle 靠连接级
	// ctx 取消+连接拆除退出,不拖住 wg.Wait。
	stopDone := make(chan error, 1)
	go func() { stopDone <- s.Stop() }()
	select {
	case err := <-stopDone:
		if err != nil {
			t.Fatalf("stop: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Stop hung with in-flight channel")
	}

	select {
	case <-exited:
	case <-time.After(3 * time.Second):
		t.Fatal("in-flight Handle must observe ctx cancellation on Stop")
	}
	if _, err := ch.Read(make([]byte, 1)); err == nil {
		t.Fatal("client channel must observe EOF/error after Stop")
	}
}
