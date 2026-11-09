// serveconn_test.go — 统一口注入缝(spec §6.4):ServeConn/HandleIngressConn。
// 语义锚点:注入连接与 listener 路径走完全同一套 handleConn(限速→握手→
// 双形态通道收敛),差别只在「谁 accept」。故此处覆盖三点:注入路径能跑通
// bare 直连全链、入口未配置时 HandleIngressConn 拒绝且关连接、入口在跑时
// 注入连接被 Stop 的快照拆除覆盖。
package ssh

import (
	"context"
	"io"
	"net"
	"path/filepath"
	"testing"
	"time"

	gossh "golang.org/x/crypto/ssh"

	M "aliang.one/nursorgate/inbound/tun/metadata"
)

// timeNowPlus 给断言侧一个短读窗口:拒绝路径的连接已被对端关闭,读应立即
// 返回错误;deadline 只防实现漏关时测试永久挂住。
func timeNowPlus() time.Time { return time.Now().Add(2 * time.Second) }

// serveConnVia 起一条测试 listener,accept 后走 ServeConn 注入,返回拨号地址。
// 这是后续 Task 7 退役 Start 的监听路径后测试将继续使用的注入范式。
func serveConnVia(t *testing.T, s *Server) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go s.ServeConn(c)
		}
	}()
	return l.Addr().String()
}

func TestServeConnInjectsBareStreamToPipeline(t *testing.T) {
	swapTCPHandler(t, func(ctx context.Context, conn net.Conn, metadata *M.Metadata) error {
		_, _ = io.Copy(io.Discard, conn)
		metadata.SetHostName("injected.test", M.BindingSourceHTTP, 0)
		return nil
	})
	humanPub, humanSigner := newTestKey(t)
	s := NewServer(RunConfig{AuthorizedKeys: []gossh.PublicKey{humanPub}}, filepath.Join(t.TempDir(), IngressHostKeyFileName))
	if err := s.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = s.Stop() })

	addr := serveConnVia(t, s)
	conn, err := dialIngress(t, addr, gossh.PublicKeys(humanSigner))
	if err != nil {
		t.Fatalf("injected serveConn must accept authenticated client: %v", err)
	}
	ch := openDirectTCPIP(t, conn, "127.0.0.1", 9)
	defer ch.Close()

	// 形态标记到达通道层 = 注入连接走了与 listener 完全同一条 handleConn。
	if got := s.observedForm(); got != FormBare {
		t.Fatalf("ingress form = %q, want %q", got, FormBare)
	}
}

func TestServeConnRejectsWhenNotRunning(t *testing.T) {
	humanPub, _ := newTestKey(t)
	s := NewServer(RunConfig{AuthorizedKeys: []gossh.PublicKey{humanPub}}, filepath.Join(t.TempDir(), IngressHostKeyFileName))
	// 未 Start:入口不在运行态。
	client, server := net.Pipe()
	defer client.Close()
	go func() { _, _ = io.Copy(io.Discard, client) }()

	done := make(chan struct{})
	go func() {
		s.ServeConn(server)
		close(done)
	}()
	<-done

	// 连接必须已被关闭:再读只会得到 EOF 类错误。
	_ = client.SetReadDeadline(timeNowPlus())
	if _, err := client.Read(make([]byte, 1)); err == nil {
		t.Fatal("ServeConn on stopped server must close the conn")
	}
}

func TestHandleIngressConnRejectsUnconfigured(t *testing.T) {
	// reconcile 期望停(未配置)时,统一口分派的 SSH 连接必须被拒且关闭,
	// 且不回显任何协议字节(spec §4 钉死的未定义分支)。
	Reconcile(nil, "", nil)
	client, server := net.Pipe()
	defer client.Close()
	go func() { _, _ = io.Copy(io.Discard, client) }()

	if HandleIngressConn(server) {
		t.Fatal("HandleIngressConn must report false when ingress unconfigured")
	}
	_ = client.SetReadDeadline(timeNowPlus())
	if _, err := client.Read(make([]byte, 1)); err == nil {
		t.Fatal("rejected conn must be closed")
	}
}

// TestStopDrainsInterleavedInjectedConns 停机交错压力钉子(评审 B1):注入
// 连接与 Stop 并发交错时,Stop 必须在有限时间返回(不被「登记晚于快照、
// ctx 无兜底」的在途连接挂死——那会连带 reconcileMu 永久持锁),且返回后
// 注入连接全部被拆除。多轮次放大交错窗口;配合 -race 覆盖 wg 记账窗口。
func TestStopDrainsInterleavedInjectedConns(t *testing.T) {
	humanPub, _ := newTestKey(t)
	for round := 0; round < 20; round++ {
		s := NewServer(RunConfig{AuthorizedKeys: []gossh.PublicKey{humanPub}}, filepath.Join(t.TempDir(), IngressHostKeyFileName))
		if err := s.Start(); err != nil {
			t.Fatalf("round %d start: %v", round, err)
		}
		for i := 0; i < 8; i++ {
			client, server := net.Pipe()
			go func() { _, _ = io.Copy(io.Discard, client) }()
			go s.ServeConn(server)
		}
		done := make(chan struct{})
		go func() {
			_ = s.Stop()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatalf("round %d: Stop must return promptly with interleaved injected conns", round)
		}
	}
}
