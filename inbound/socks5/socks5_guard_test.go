package socks5

// 来源/目标守卫测试:socks5 面是统一入口的代理面之一,非回环来源经它访问
// 回环目标必须被拒(回环目标是管理 API/agent runtime 的信任边界,见
// processor/tcp 的 CheckProxyFaceDestination)。守卫在成功应答之前——reply
// 对一条连接只能写一次。

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"

	M "aliang.one/nursorgate/inbound/tun/metadata"
	auth "aliang.one/nursorgate/processor/auth"
	"aliang.one/nursorgate/processor/config"
)

// sourceAddrConn 覆写来源地址(模拟局域网客户端),其余透传。
type sourceAddrConn struct {
	net.Conn
	remote net.Addr
}

func (c *sourceAddrConn) RemoteAddr() net.Addr { return c.remote }

func runSocksFrom(t *testing.T, sourceIP string, target net.IP, port uint16, loggedIn bool) (method []byte, tail []byte, reachedPipeline bool) {
	t.Helper()

	authority := auth.ResetSessionAuthorityForTest()
	if loggedIn {
		authority.NotifyLoggedIn(&auth.UserInfo{ID: 1})
	}
	t.Cleanup(func() { auth.ResetSessionAuthorityForTest() })

	origHandler := TCPHandler
	t.Cleanup(func() { TCPHandler = origHandler })
	mdCh := make(chan *M.Metadata, 1)
	TCPHandler = func(_ context.Context, conn net.Conn, got *M.Metadata) error {
		select {
		case mdCh <- got:
		default:
		}
		_ = conn.Close()
		return nil
	}

	client, server := net.Pipe()
	t.Cleanup(func() { _ = client.Close(); _ = server.Close() })
	_ = client.SetDeadline(time.Now().Add(5 * time.Second))

	done := make(chan struct{})
	go func() {
		defer close(done)
		Handler(&sourceAddrConn{Conn: server, remote: &net.TCPAddr{IP: net.ParseIP(sourceIP), Port: 41000}})
	}()

	type result struct {
		method, tail []byte
		err          error
	}
	resCh := make(chan result, 1)
	go func() {
		var res result
		_, _ = client.Write([]byte{socksVer, 0x01, authNone})
		res.method = make([]byte, 2)
		if _, err := io.ReadFull(client, res.method); err != nil {
			res.err = err
			resCh <- res
			return
		}
		if res.method[1] == authNone {
			req := []byte{socksVer, cmdConnect, 0x00, atypIPv4}
			req = append(req, target.To4()...)
			var portBytes [2]byte
			binary.BigEndian.PutUint16(portBytes[:], port)
			req = append(req, portBytes[:]...)
			_, _ = client.Write(req)
		}
		res.tail, res.err = io.ReadAll(client)
		resCh <- res
	}()

	select {
	case res := <-resCh:
		if res.err != nil {
			t.Fatalf("客户端读失败: %v(方法应答=%v 尾段=%v)", res.err, res.method, res.tail)
		}
		method, tail = res.method, res.tail
	case <-time.After(8 * time.Second):
		t.Fatal("Handler 未在期限内完成(挂死/漏关连接)")
	}
	select {
	case <-mdCh:
		reachedPipeline = true
	default:
	}
	<-done
	return method, tail, reachedPipeline
}

// 回环目标的连接应答(10 字节:VER REP RSV ATYP BND.ADDR BND.PORT)。
func TestSocks5RejectsLoopbackTargetFromNonLoopbackClient(t *testing.T) {
	method, tail, reached := runSocksFrom(t, "192.168.1.20", net.ParseIP("127.0.0.1"), 56431, true)
	if method[1] != authNone {
		t.Fatalf("方法协商应放行 no-auth, got %v", method)
	}
	if len(tail) < 2 || tail[0] != socksVer || tail[1] != repNotAllowed {
		t.Fatalf("非回环来源访问回环目标应答 rep 0x02, got %v", tail)
	}
	if reached {
		t.Fatal("被拒的请求不得进入管线")
	}

	// 对照:同一客户端访问公网目标照常成功。
	_, tail, reached = runSocksFrom(t, "192.168.1.20", net.ParseIP("8.8.8.8"), 443, true)
	assertSuccessReply(t, tail)
	if !reached {
		t.Fatal("公网目标的请求必须进入管线")
	}
}

// AI base-URL 形态(127.0.0.1:56432)对非回环来源同样被拒:它会带着本机登录态
// 进平台隧道。
func TestSocks5RejectsAIBaseURLFormFromNonLoopbackClient(t *testing.T) {
	_, tail, reached := runSocksFrom(t, "192.168.1.20", net.ParseIP("127.0.0.1"), uint16(config.DefaultHTTPProxyPort), true)
	if len(tail) < 2 || tail[1] != repNotAllowed {
		t.Fatalf("非回环来源访问 AI base-URL 形态应答 rep 0x02, got %v", tail)
	}
	if reached {
		t.Fatal("被拒的请求不得进入管线")
	}
}

// 对照:回环来源访问回环目标照常(本机应用既有用法)。
func TestSocks5AllowsLoopbackTargetFromLoopbackClient(t *testing.T) {
	_, tail, reached := runSocksFrom(t, "127.0.0.1", net.ParseIP("127.0.0.1"), 56431, true)
	assertSuccessReply(t, tail)
	if !reached {
		t.Fatal("回环来源访问回环目标必须进入管线")
	}
}
