package socks5

// RFC1928 入站语义钉子(spec §6.3):仅 CONNECT、仅 no-auth,门禁=会话租约
// (与 HTTP 同档);BIND/UDP ASSOCIATE 显式 reply 0x07。喂 processor/tcp 统一
// 管线后本节点视作出口——管线语义归 tcp 包测试,此处只钉「交对了 Metadata」。

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	M "aliang.one/nursorgate/inbound/tun/metadata"
	auth "aliang.one/nursorgate/processor/auth"
)

// exchange 起一对 net.Pipe 跑 Handler,客户端按「greeting→读 2B 方法应答→
// request→读到 EOF」的顺序交互。返回:方法协商应答、其后全部字节(连接
// 应答+EOF 前残余)、管线缝收到的 Metadata。
//
// net.Pipe 是同步管道,客户端写必须有人读才能返回。拒收路径(0xFF/命令
// 不支持/畸形)服务端会先关连接,此时客户端尚未发出的后续写必然报
// closed pipe——故写操作容错(错误即服务端已关,属预期结果之一),读操作
// 权威:方法应答+尾段是断言依据,断言失败由用例自身暴露。
//
// deadline:net.Pipe 支持 SetDeadline,给整段交互 5s 上限——实现若挂死
// (如漏关连接),测试报超时而非永久阻塞。
func exchange(t *testing.T, greeting, request []byte, loggedIn bool) (methodReply, tail []byte, md *M.Metadata) {
	t.Helper()

	// 会话租约门禁状态隔离:loggedIn=false 的用例不登录(Reset 后即
	// StateRestoring,租约必拒)。
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
		_ = conn.Close() // 收到即关:客户端 ReadAll 以 EOF 收尾
		return nil
	}

	client, server := net.Pipe()
	t.Cleanup(func() {
		_ = client.Close()
		_ = server.Close()
	})
	_ = client.SetDeadline(time.Now().Add(5 * time.Second))

	done := make(chan struct{})
	go func() {
		defer close(done)
		Handler(server)
	}()

	type result struct {
		method []byte
		tail   []byte
		err    error
	}
	resCh := make(chan result, 1)
	go func() {
		var res result
		_, _ = client.Write(greeting) // 服务端拒收先关时此写可失败,容错
		res.method = make([]byte, 2)
		if _, err := io.ReadFull(client, res.method); err != nil {
			res.err = err
			resCh <- res
			return
		}
		// 仅方法协商成功才发请求:0xFF 拒收后服务端已关,再写只会 closed pipe。
		if res.method[1] == authNone && request != nil {
			_, _ = client.Write(request)
		}
		res.tail, res.err = io.ReadAll(client)
		resCh <- res
	}()

	select {
	case res := <-resCh:
		if res.err != nil {
			t.Fatalf("客户端读失败: %v(方法应答=%v 尾段=%v)", res.err, res.method, res.tail)
		}
		methodReply, tail = res.method, res.tail
	case <-time.After(8 * time.Second):
		t.Fatal("Handler 未在期限内完成(挂死/漏关连接)")
	}

	select {
	case md = <-mdCh:
	default:
	}
	<-done
	return methodReply, tail, md
}

// socksConnectRequest 构造 CONNECT 请求段(VER CMD RSV ATYP ADDR PORT)。
func socksConnectRequest(t *testing.T, cmd byte, atyp byte, addr []byte) []byte {
	t.Helper()
	req := []byte{socksVer, cmd, 0x00, atyp}
	req = append(req, addr...)
	req = append(req, 0x01, 0xBB) // port 443
	return req
}

// assertSuccessReply 断言 10 字节成功应答形态(REP=0,BND=0.0.0.0:0)。
func assertSuccessReply(t *testing.T, tail []byte) {
	t.Helper()
	if len(tail) < 10 {
		t.Fatalf("连接应答不足 10 字节: %v", tail)
	}
	if tail[0] != socksVer || tail[1] != repSuccess || tail[2] != 0x00 || tail[3] != 0x01 {
		t.Fatalf("连接应答头异常: %v", tail[:4])
	}
	if !bytesAllZero(tail[4:10]) {
		t.Fatalf("BND 应为 0.0.0.0:0: %v", tail[4:10])
	}
}

func bytesAllZero(b []byte) bool {
	for _, v := range b {
		if v != 0 {
			return false
		}
	}
	return true
}

// TestSocks5ConnectIPv4 主路径:no-auth 协商→IPv4 CONNECT→成功应答→喂管线。
func TestSocks5ConnectIPv4(t *testing.T) {
	greeting := []byte{socksVer, 0x01, authNone}
	request := socksConnectRequest(t, cmdConnect, 0x01, []byte{93, 184, 216, 34})

	methodReply, tail, md := exchange(t, greeting, request, true)

	if methodReply[0] != socksVer || methodReply[1] != authNone {
		t.Fatalf("方法应答 = %v, want [5,0]", methodReply)
	}
	assertSuccessReply(t, tail)
	if md == nil {
		t.Fatal("管线未收到 Metadata")
	}
	if md.Network != M.TCP {
		t.Fatalf("Network = %v, want tcp", md.Network)
	}
	if !md.DstIP.IsValid() || md.DstIP.String() != "93.184.216.34" {
		t.Fatalf("DstIP = %v, want 93.184.216.34", md.DstIP)
	}
	if md.DstPort != 443 {
		t.Fatalf("DstPort = %d, want 443", md.DstPort)
	}
	if md.HostName != "" {
		t.Fatalf("IP 目标不应带 HostName, got %q", md.HostName)
	}
	if len(md.ConnID) == 0 || md.ConnID[:6] != "socks-" {
		t.Fatalf("ConnID = %q, want socks-N 序号段", md.ConnID)
	}
}

// TestSocks5ConnectDomain 域名目标→HostName(DNS 解析留给拨号期,与 CONNECT
// 路径同口径;本测试不依赖真实解析)。
func TestSocks5ConnectDomain(t *testing.T) {
	greeting := []byte{socksVer, 0x02, authNone, 0x02} // 多方法含 no-auth
	domain := []byte{0x0B, 'e', 'x', 'a', 'm', 'p', 'l', 'e', '.', 'c', 'o', 'm'}
	request := socksConnectRequest(t, cmdConnect, 0x03, domain)

	methodReply, tail, md := exchange(t, greeting, request, true)

	if methodReply[1] != authNone {
		t.Fatalf("方法应答 = %v, want no-auth", methodReply)
	}
	assertSuccessReply(t, tail)
	if md == nil {
		t.Fatal("管线未收到 Metadata")
	}
	if md.HostName != "example.com" {
		t.Fatalf("HostName = %q, want example.com", md.HostName)
	}
	if md.DstPort != 443 {
		t.Fatalf("DstPort = %d, want 443", md.DstPort)
	}
}

// TestSocks5ConnectIPv6 IPv6 目标(ATYP=0x04,16 字节)。
func TestSocks5ConnectIPv6(t *testing.T) {
	greeting := []byte{socksVer, 0x01, authNone}
	ip6 := net.ParseIP("2606:2800:220:1:248:1893:25c8:1946").To16()
	request := socksConnectRequest(t, cmdConnect, 0x04, ip6)

	_, tail, md := exchange(t, greeting, request, true)

	assertSuccessReply(t, tail)
	if md == nil {
		t.Fatal("管线未收到 Metadata")
	}
	if !md.DstIP.IsValid() || md.DstIP.Is6() == false {
		t.Fatalf("DstIP = %v, want IPv6", md.DstIP)
	}
	if md.DstIP.String() != "2606:2800:220:1:248:1893:25c8:1946" {
		t.Fatalf("DstIP = %v", md.DstIP)
	}
}

// TestSocks5GreetingWithoutNoAuthRejected 客户端不含 no-auth → reply 0xFF
// 断连,不进管线。
func TestSocks5GreetingWithoutNoAuthRejected(t *testing.T) {
	greeting := []byte{socksVer, 0x02, 0x01, 0x02} // 仅 GSSAPI/用户名密码
	request := socksConnectRequest(t, cmdConnect, 0x01, []byte{1, 2, 3, 4})

	methodReply, tail, md := exchange(t, greeting, request, true)

	if methodReply[0] != socksVer || methodReply[1] != authNoAccept {
		t.Fatalf("方法应答 = %v, want [5,0xFF]", methodReply)
	}
	if len(tail) != 0 {
		t.Fatalf("拒收后不应有连接应答: %v", tail)
	}
	if md != nil {
		t.Fatal("拒收后不应喂管线")
	}
}

// TestSocks5CommandUnsupported BIND(0x02)/UDP ASSOCIATE(0x03)→ reply 0x07
// 断连。
func TestSocks5CommandUnsupported(t *testing.T) {
	cases := []struct {
		name string
		cmd  byte
	}{
		{"BIND", 0x02},
		{"UDP_ASSOCIATE", 0x03},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			greeting := []byte{socksVer, 0x01, authNone}
			request := socksConnectRequest(t, tc.cmd, 0x01, []byte{1, 2, 3, 4})

			_, tail, md := exchange(t, greeting, request, true)

			if len(tail) < 10 {
				t.Fatalf("应答不足 10 字节: %v", tail)
			}
			if tail[1] != repCmdUnsupp {
				t.Fatalf("REP = 0x%02X, want 0x07", tail[1])
			}
			if md != nil {
				t.Fatal("命令不支持不应喂管线")
			}
		})
	}
}

// TestSocks5LeaseDenied 无活跃会话 → reply 0x02(connection not allowed by
// ruleset)断连且不喂管线;成功应答只在租约通过后写(顺序钉子:REP=0x02
// 即终,同一条连接上不得再出现 REP=0x00 的二次应答)。
func TestSocks5LeaseDenied(t *testing.T) {
	greeting := []byte{socksVer, 0x01, authNone}
	request := socksConnectRequest(t, cmdConnect, 0x01, []byte{1, 2, 3, 4})

	_, tail, md := exchange(t, greeting, request, false)

	if len(tail) < 2 || tail[0] != socksVer || tail[1] != repNotAllowed {
		t.Fatalf("应答 = %v, want REP=0x02", tail)
	}
	if md != nil {
		t.Fatal("租约被拒不应喂管线")
	}
}

// TestSocks5MalformedRequestATYP 未知 ATYP → 断连,不喂管线。
func TestSocks5MalformedRequestATYP(t *testing.T) {
	greeting := []byte{socksVer, 0x01, authNone}
	request := socksConnectRequest(t, cmdConnect, 0x09, []byte{1, 2, 3, 4})

	_, tail, md := exchange(t, greeting, request, true)

	if len(tail) != 0 {
		t.Fatalf("畸形 ATYP 不应有应答, got %v", tail)
	}
	if md != nil {
		t.Fatal("畸形 ATYP 不应喂管线")
	}
}
