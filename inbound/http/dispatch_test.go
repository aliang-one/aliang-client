// dispatch_test.go — 统一入口分派(spec §4/§6.2):嗅探→移交。本层只验
// 「交对了人 + 交的是完整字节流」;各协议内部语义(http CONNECT、socks 协商)
// 由各自包/既有测试覆盖。
package http

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	"aliang.one/nursorgate/inbound/socks5"
	M "aliang.one/nursorgate/inbound/tun/metadata"
	auth "aliang.one/nursorgate/processor/auth"
	tcphandler "aliang.one/nursorgate/processor/tcp"
)

// dispatchPipe 起一对 net.Pipe 供 dispatchConn 消费,写端写完即关。
func dispatchPipe(t *testing.T, payload string) net.Conn {
	t.Helper()
	client, server := net.Pipe()
	go func() {
		_, _ = io.WriteString(client, payload)
		_ = client.Close()
	}()
	t.Cleanup(func() { _ = server.Close() })
	return server
}

// metadataRecorder 只记录管道收到的 metadata(不读 conn——CONNECT 隧道里
// 已无 HTTP 请求,读请求会失败)。
type metadataRecorder struct {
	got chan *M.Metadata
}

func newMetadataRecorder() *metadataRecorder {
	return &metadataRecorder{got: make(chan *M.Metadata, 1)}
}

func (r *metadataRecorder) Handle(_ context.Context, conn net.Conn, md *M.Metadata) error {
	select {
	case r.got <- md:
	default:
	}
	_, _ = io.Copy(io.Discard, conn)
	return nil
}

// waitConnClosed 断言 conn 已被对端关闭(读到 EOF/关闭类错误),带短窗口防挂死。
func waitConnClosed(t *testing.T, conn net.Conn) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Read(make([]byte, 1)); err == nil {
		t.Fatal("expected conn to be closed")
	}
}

func TestDispatchConnHTTPReachesCONNECTPath(t *testing.T) {
	// CONNECT 分支带会话租约门禁(与合并前一致):无活跃会话会 503,故先登录。
	authority := auth.ResetSessionAuthorityForTest()
	authority.NotifyLoggedIn(&auth.UserInfo{ID: 1})
	t.Cleanup(func() { auth.ResetSessionAuthorityForTest() })

	orig := tcphandler.GetHandler()
	rec := newMetadataRecorder()
	tcphandler.SetHandler(rec)
	t.Cleanup(func() { tcphandler.SetHandler(orig) })

	// CONNECT 的 metadata 提取要求回环目标(与既有 CONNECT 用例同口径)。
	// net.Pipe 是同步管道:服务端写 "200 Connection Established" 必须有人读,
	// 否则 dispatchConn 卡在写应答、永远到不了管线——故客户端侧起读排空协程。
	client, server := net.Pipe()
	go func() {
		_, _ = io.WriteString(client, "CONNECT 127.0.0.1:443 HTTP/1.1\r\nHost: 127.0.0.1:443\r\n\r\n")
		_, _ = io.Copy(io.Discard, client)
		_ = client.Close()
	}()
	t.Cleanup(func() {
		_ = client.Close()
		_ = server.Close()
	})

	done := make(chan struct{})
	go func() {
		dispatchConn(server)
		close(done)
	}()

	// CONNECT 成功后是长隧道(handler 阻塞在 relay),故只断言「metadata 到达
	// 管线」;dispatchConn 的返回由 t.Cleanup 关连接触发,不作时序断言。
	select {
	case md := <-rec.got:
		if got, want := md.DstPort, uint16(443); got != want {
			t.Fatalf("metadata dst port = %d, want %d", got, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("CONNECT must reach the TCP pipeline with metadata")
	}
	_ = client.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("dispatchConn did not return after client closed")
	}
}

func TestDispatchConnUnknownBytesGoesToHTTP(t *testing.T) {
	// 未知字节流兜底 HTTP:ReadRequest 失败即断连,不 panic、不误派 mesh/ssh。
	conn := dispatchPipe(t, "\xFF\xFE\x00\x01")
	done := make(chan struct{})
	go func() {
		dispatchConn(conn)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("dispatchConn did not return for unknown stream")
	}
}

// TestDispatchConnSocksEndToEnd dispatch 喂 socks 样本 → 真实 socks5 握手
// → 管线缝收到正确 Metadata(no-auth 协商+CONNECT 全程过,门禁需登录)。
//
// net.Pipe 同步管道下的两处互锁必须避开:①嗅探 Peek(4) 要求首块写 ≥4 字节
// (greeting 只有 3 字节),且服务端首次应答前要读满「嗅探 4+协商 3」= 7
// 字节,故首块=greeting+请求头;②成功应答 10 字节要等客户端消费,故客户端
// 末尾 ReadAll 兜住应答后服务端才走到管线缝。请求段由 socks5 解析器自身
// 消费,管线侧断言对象是 Metadata 而非字节回放。
func TestDispatchConnSocksEndToEnd(t *testing.T) {
	authority := auth.ResetSessionAuthorityForTest()
	authority.NotifyLoggedIn(&auth.UserInfo{ID: 1})
	t.Cleanup(func() { auth.ResetSessionAuthorityForTest() })

	origTCP := socks5.TCPHandler
	t.Cleanup(func() { socks5.TCPHandler = origTCP })
	mdCh := make(chan *M.Metadata, 1)
	socks5.TCPHandler = func(_ context.Context, conn net.Conn, md *M.Metadata) error {
		select {
		case mdCh <- md:
		default:
		}
		_, _ = io.Copy(io.Discard, conn)
		_ = conn.Close()
		return nil
	}

	greeting := []byte{0x05, 0x01, 0x00}            // VER NMETHODS=1 no-auth
	requestHead := []byte{0x05, 0x01, 0x00, 0x01}   // CONNECT + ATYP=IPv4
	requestRest := []byte{127, 0, 0, 1, 0x01, 0xBB} // 127.0.0.1:443

	client, server := net.Pipe()
	t.Cleanup(func() {
		_ = client.Close()
		_ = server.Close()
	})
	_ = client.SetDeadline(time.Now().Add(5 * time.Second))

	done := make(chan struct{})
	go func() {
		defer close(done)
		dispatchConn(server)
	}()

	go func() {
		_, _ = client.Write(append(append([]byte{}, greeting...), requestHead...))
		method := make([]byte, 2)
		if _, err := io.ReadFull(client, method); err != nil {
			return
		}
		_, _ = client.Write(requestRest)
		_, _ = io.ReadAll(client) // 兜住成功应答;服务端应答完成后才进管线缝
	}()

	select {
	case md := <-mdCh:
		if md.DstPort != 443 || !md.DstIP.IsValid() || md.DstIP.String() != "127.0.0.1" {
			t.Fatalf("metadata = %s:%d, want 127.0.0.1:443", md.DstIP, md.DstPort)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("socks5 handler never invoked")
	}
	_ = client.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("dispatchConn did not return")
	}
}
