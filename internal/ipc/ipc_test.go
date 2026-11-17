package ipc

import (
	"encoding/json"
	"errors"
	"net"
	"sync"
	"testing"
	"time"
)

// pipeTransport 是内存双端 Transport:Listen 建一个基于 channel 的
// net.Listener(net.Pipe 对),供 server/client 双端测试共用,零文件系统依赖。
type pipeTransport struct {
	mu       sync.Mutex
	listener *pipeListener
	closed   bool
}

func (t *pipeTransport) Listen() (net.Listener, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	l := newPipeListener()
	t.listener = l
	return l, nil
}

func (t *pipeTransport) Dial() (net.Conn, error) {
	t.mu.Lock()
	closed, l := t.closed, t.listener
	t.mu.Unlock()
	if closed {
		return nil, errors.New("transport closed (core down)")
	}
	if l == nil {
		return nil, errors.New("no listener")
	}
	server, client := net.Pipe()
	select {
	case l.conns <- server:
		return client, nil
	case <-l.done:
		return nil, errors.New("listener closed")
	}
}

func (t *pipeTransport) SocketPath() string { return "pipe-test" }

func (t *pipeTransport) setClosed(closed bool) {
	t.mu.Lock()
	t.closed = closed
	t.mu.Unlock()
}

type pipeListener struct {
	conns chan net.Conn
	done  chan struct{}
	once  sync.Once
}

func newPipeListener() *pipeListener {
	return &pipeListener{conns: make(chan net.Conn, 16), done: make(chan struct{})}
}

func (l *pipeListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *pipeListener) Close() error {
	l.once.Do(func() { close(l.done) })
	return nil
}

func (l *pipeListener) Addr() net.Addr { return pipeAddr{} }

type pipeAddr struct{}

func (pipeAddr) Network() string { return "pipe" }
func (pipeAddr) String() string  { return "pipe-test" }

func startTestServer(t *testing.T) (*Server, *pipeTransport) {
	t.Helper()
	transport := &pipeTransport{}
	server := NewServerWithTransport(transport)
	server.Register(ActionPing, func(args json.RawMessage) (interface{}, error) { return "pong", nil })
	server.Register("get_status", func(args json.RawMessage) (interface{}, error) { return map[string]string{"ok": "yes"}, nil })
	if err := server.Start(); err != nil {
		t.Fatalf("server start: %v", err)
	}
	t.Cleanup(func() { _ = server.Stop() })
	return server, transport
}

// TestStopClosesLiveConnections 是 2026-11-16 生产挂死的回归护栏:托盘
// companion 持常驻连接(读无 deadline 的 handleConn)时,Stop 必须主动断开
// 存量连接并在有界时间内返回,否则 SCM 停止永远卡 StopPending。
func TestStopClosesLiveConnections(t *testing.T) {
	server, transport := startTestServer(t)

	clientConn, err := transport.Dial()
	if err != nil {
		t.Fatalf("client dial: %v", err)
	}

	stopped := make(chan error, 1)
	go func() { stopped <- server.Stop() }()

	select {
	case err := <-stopped:
		if err != nil {
			t.Fatalf("stop returned error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Stop did not return within 3s with a live idle connection — StopPending hang regression")
	}

	// 服务端断开后,客户端侧应立即看到 EOF/错误,而不是无限等待。
	_ = clientConn.SetReadDeadline(time.Now().Add(1 * time.Second))
	buf := make([]byte, 16)
	if _, err := clientConn.Read(buf); err == nil {
		t.Fatal("client connection should observe EOF after server stop")
	}
}

// TestClientSendRedialsAfterDisconnect 覆盖「core 换代后 IPC-only 动作死透」
// 的修复:断线状态下 Send 先单次重拨,拨通即恢复 IPC 通道。
func TestClientSendRedialsAfterDisconnect(t *testing.T) {
	transport := &pipeTransport{}
	server := NewServerWithTransport(transport)
	server.Register(ActionPing, func(args json.RawMessage) (interface{}, error) {
		return "pong", nil
	})
	// server 尚未 Start,transport 处于「core 不在」形态。
	transport.setClosed(true)
	t.Cleanup(func() { _ = server.Stop() })

	client := NewClientWithTransport(transport, "http://127.0.0.1:1")

	// core 不在:重拨失败,落 HTTP 回退也失败(127.0.0.1:1 拒连),不挂死。
	if _, err := client.Send(ActionPing, nil); err == nil {
		t.Fatal("send while core down should fail via HTTP fallback")
	}
	if client.IsConnected() {
		t.Fatal("failed dial must not mark client connected")
	}

	// core 上线:下一次 Send 单次重拨成功,IPC 动作恢复。
	if err := server.Start(); err != nil {
		t.Fatalf("server start: %v", err)
	}
	transport.setClosed(false)

	resp, err := client.Send(ActionPing, nil)
	if err != nil {
		t.Fatalf("send after core recovery: %v", err)
	}
	if !resp.OK || resp.Data != "pong" {
		t.Fatalf("unexpected response: ok=%v data=%v", resp.OK, resp.Data)
	}
	if !client.IsConnected() {
		t.Fatal("client should stay connected after successful IPC round-trip")
	}
}
