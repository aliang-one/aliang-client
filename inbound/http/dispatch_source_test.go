package http

// 来源闸测试:统一入口的 HTTP/socks5 代理面只对本机开放。ssh_ingress 一开
// 56432 就绑全接口,而会话租约是全机一个标志(不看来源)——没有这道闸时,
// 局域网/隧道网里任何设备都能把网关当无鉴权代理,并借它回环打进只绑回环
// 的管理 API(2026-11-24 审计跨机实证)。SSH 族不受影响:mesh 只骑在它上面。

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	M "aliang.one/nursorgate/inbound/tun/metadata"
	auth "aliang.one/nursorgate/processor/auth"
	"aliang.one/nursorgate/processor/config"
	tcphandler "aliang.one/nursorgate/processor/tcp"
)

// loginForProxyFace 把会话置为已登录:来源闸必须在「会话租约不缺」的前提下
// 仍然拒绝,否则测的只是租约门。
func loginForProxyFace(t *testing.T) {
	t.Helper()
	authority := auth.ResetSessionAuthorityForTest()
	authority.NotifyLoggedIn(&auth.UserInfo{ID: 1})
	t.Cleanup(func() { auth.ResetSessionAuthorityForTest() })
}

// pipelineProbe 记录管线是否被触达(socks5 的惰性默认管线与 HTTP 面都汇入
// 同一个 tcphandler 单例,替换它即覆盖两条代理面)。
type pipelineProbe struct {
	hit chan struct{}
}

func (p *pipelineProbe) Handle(_ context.Context, conn net.Conn, _ *M.Metadata) error {
	select {
	case p.hit <- struct{}{}:
	default:
	}
	_, _ = io.Copy(io.Discard, conn)
	return nil
}

func stubPipeline(t *testing.T) *pipelineProbe {
	t.Helper()
	probe := &pipelineProbe{hit: make(chan struct{}, 1)}
	orig := tcphandler.GetHandler()
	tcphandler.SetHandler(probe)
	t.Cleanup(func() { tcphandler.SetHandler(orig) })
	return probe
}

const lanClientIP = "192.168.1.20"
const lanListenIP = "192.168.1.10"

// lanEnd 把管道的服务端包装成「来源是局域网地址」的连接(RemoteAddr/LocalAddr
// 覆写;deadline 透传给底层管道)。
func lanEnd(conn net.Conn) net.Conn {
	return &deadlinePassthroughConn{
		Conn: conn,
		local: &net.TCPAddr{
			IP:   net.ParseIP(lanListenIP),
			Port: config.DefaultHTTPProxyPort,
		},
		remote: &net.TCPAddr{IP: net.ParseIP(lanClientIP), Port: 41000},
	}
}

// loopbackEnd 同上,但来源是回环地址(对照组)。
func loopbackEnd(conn net.Conn) net.Conn {
	return &deadlinePassthroughConn{
		Conn: conn,
		local: &net.TCPAddr{
			IP:   net.ParseIP("127.0.0.1"),
			Port: config.DefaultHTTPProxyPort,
		},
		remote: &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 41000},
	}
}

type deadlinePassthroughConn struct {
	net.Conn
	local  net.Addr
	remote net.Addr
}

func (c *deadlinePassthroughConn) LocalAddr() net.Addr  { return c.local }
func (c *deadlinePassthroughConn) RemoteAddr() net.Addr { return c.remote }

// dispatchPipePair 起一对管道并在 goroutine 里用 end 包装服务端跑 dispatchConn,
// 返回客户端连接与结束信号。payload 由调用方经 client 写入。
func dispatchPipePair(t *testing.T, end func(net.Conn) net.Conn) (net.Conn, <-chan struct{}) {
	t.Helper()
	client, server := net.Pipe()
	t.Cleanup(func() { _ = client.Close(); _ = server.Close() })
	done := make(chan struct{})
	go func() {
		defer close(done)
		dispatchConn(end(server))
	}()
	return client, done
}

// writeThenHoldBytes 写完首段后保持连接不读也不关:被拒路径的应答由测试主
// 协程读——同步管道下同一端起两个读者会抢走应答。
func writeThenHoldBytes(client net.Conn, payload []byte) {
	_, _ = client.Write(payload)
	time.Sleep(10 * time.Second)
	_ = client.Close()
}

func waitDispatchDone(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("dispatchConn did not return")
	}
}

func assertPipelineNotReached(t *testing.T, probe *pipelineProbe) {
	t.Helper()
	select {
	case <-probe.hit:
		t.Fatal("非回环来源不得进入代理管线")
	default:
	}
}

func TestDispatchConnRejectsNonLoopbackHTTPFace(t *testing.T) {
	loginForProxyFace(t)
	probe := stubPipeline(t)

	client, done := dispatchPipePair(t, lanEnd)
	go writeThenHoldBytes(client, []byte("CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\n"))

	_ = client.SetReadDeadline(time.Now().Add(3 * time.Second))
	resp, err := http.ReadResponse(bufio.NewReader(client), &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status=%d, want 403", resp.StatusCode)
	}
	assertPipelineNotReached(t, probe)
	waitDispatchDone(t, done)
}

func TestDispatchConnRejectsNonLoopbackSocksFace(t *testing.T) {
	loginForProxyFace(t)
	probe := stubPipeline(t)

	client, done := dispatchPipePair(t, lanEnd)
	// greeting 与请求头一次写出:嗅探 Peek(4) 要求首块 ≥4 字节。
	go writeThenHoldBytes(client, []byte{0x05, 0x01, 0x00, 0x05, 0x01, 0x00, 0x01})

	_ = client.SetReadDeadline(time.Now().Add(3 * time.Second))
	reply := make([]byte, 2)
	if _, err := io.ReadFull(client, reply); err != nil {
		t.Fatalf("read method reply: %v", err)
	}
	if reply[0] != 0x05 || reply[1] != 0xFF {
		t.Fatalf("method reply=%x, want 05 FF (no acceptable methods)", reply)
	}
	assertPipelineNotReached(t, probe)
	waitDispatchDone(t, done)
}

// SSH 族是统一入口唯一面向局域网的协议面(mesh 骑在它上面,准入归公钥):
// 来源闸对它必须零影响——非回环来源的 SSH 流照常交给 SSH 入口(未配置入口
// 时由入口缝静默断连,不回显任何字节)。
func TestDispatchConnSourcePolicySkipsSSHKind(t *testing.T) {
	client, done := dispatchPipePair(t, lanEnd)
	go writeThenHoldBytes(client, []byte("SSH-2.0-OpenSSH_source-policy-test\r\n"))

	_ = client.SetReadDeadline(time.Now().Add(3 * time.Second))
	data, _ := io.ReadAll(client)
	if len(data) != 0 {
		t.Fatalf("SSH kind must not be answered by the source policy, got %q", data)
	}
	waitDispatchDone(t, done)
}

// 对照组:回环来源的代理面连接照常进入管线。
func TestDispatchConnAllowsLoopbackProxyFaceSource(t *testing.T) {
	loginForProxyFace(t)
	probe := stubPipeline(t)

	client, done := dispatchPipePair(t, loopbackEnd)
	go func() {
		_, _ = io.WriteString(client, "CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\n")
		_, _ = io.Copy(io.Discard, client)
	}()

	select {
	case <-probe.hit:
	case <-time.After(5 * time.Second):
		t.Fatal("回环来源的代理面连接必须进入管线")
	}
	_ = client.Close()
	waitDispatchDone(t, done)
}

// 运维以 --host 显式把服务绑到非回环地址 = 显式的「给局域网用」意图,代理面
// 才对非回环来源开放(ssh_ingress 把 56432 放宽到 0.0.0.0 不算)。
func TestDispatchConnAllowsNonLoopbackWhenServiceBoundToLAN(t *testing.T) {
	loginForProxyFace(t)
	probe := stubPipeline(t)
	if err := config.SetServiceBindHost("0.0.0.0"); err != nil {
		t.Fatalf("set service bind host: %v", err)
	}
	t.Cleanup(func() { _ = config.SetServiceBindHost(config.DefaultServiceBindHost) })

	client, done := dispatchPipePair(t, lanEnd)
	go func() {
		_, _ = io.WriteString(client, "CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\n")
		_, _ = io.Copy(io.Discard, client)
	}()

	select {
	case <-probe.hit:
	case <-time.After(5 * time.Second):
		t.Fatal("--host 显式绑非回环地址时,非回环来源的代理面连接必须进入管线")
	}
	_ = client.Close()
	waitDispatchDone(t, done)
}

// 代理面目标守卫(HTTP CONNECT 腿):非回环来源让网关代拨回环目标必须被拒,
// 且不进入管线。--host 显式开放代理面的部署里,这正是「借网关回环打进管理
// API」的入口。
func TestRawHTTPConnectionRejectsLoopbackTargetFromNonLoopbackClient(t *testing.T) {
	loginForProxyFace(t)
	probe := stubPipeline(t)

	client, server := net.Pipe()
	t.Cleanup(func() { _ = client.Close(); _ = server.Close() })
	done := make(chan struct{})
	go func() {
		defer close(done)
		handleRawConnection(lanEnd(server))
	}()
	go writeThenHoldBytes(client, []byte("CONNECT 127.0.0.1:56431 HTTP/1.1\r\nHost: 127.0.0.1:56431\r\n\r\n"))

	_ = client.SetReadDeadline(time.Now().Add(3 * time.Second))
	resp, err := http.ReadResponse(bufio.NewReader(client), &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status=%d, want 403", resp.StatusCode)
	}
	assertPipelineNotReached(t, probe)
	waitDispatchDone(t, done)
}

// 对照:回环来源让网关代拨回环目标照常放行(本机应用既有用法)。
func TestRawHTTPConnectionAllowsLoopbackTargetFromLoopbackClient(t *testing.T) {
	loginForProxyFace(t)
	probe := stubPipeline(t)

	client, server := net.Pipe()
	t.Cleanup(func() { _ = client.Close(); _ = server.Close() })
	done := make(chan struct{})
	go func() {
		defer close(done)
		handleRawConnection(loopbackEnd(server))
	}()
	go func() {
		_, _ = io.WriteString(client, "CONNECT 127.0.0.1:56431 HTTP/1.1\r\nHost: 127.0.0.1:56431\r\n\r\n")
		_, _ = io.Copy(io.Discard, client)
	}()

	select {
	case <-probe.hit:
	case <-time.After(5 * time.Second):
		t.Fatal("回环来源访问回环目标必须进入管线")
	}
	_ = client.Close()
	waitDispatchDone(t, done)
}
