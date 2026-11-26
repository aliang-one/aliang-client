package ipc

import (
	"encoding/json"
	"errors"
	"fmt"
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

// ---- 2026-11-26 IPC 客户端加固批:请求级互斥 + 响应 ID 校验 + 可注入读
// 超时。托盘面板上线后点击会经同一 Client 触发 IPC,与 2s 轮询 goroutine
// 并发;无互斥时响应串位、无 deadline 时永久阻塞、无 ID 校验时错帧冒领。

// asyncWriteConn 模拟真实 unix socket 的写语义:写完成 ≠ 服务端已消费
// (内核缓冲区解耦)。net.Pipe 的写是同步的(对端读走才算写完),叠加
// 服务端单连接串行处理会形成严格配对纪律——并发请求结构性不可能串位,
// 测试会假绿。本 shim 把客户端侧写改成「入队即返回」,flusher goroutine
// 异步转发,还原生产环境「两个请求同时在途、先到读的 goroutine 拿走最早
// 入队响应」的串位触发条件。其余方法全部透传内嵌 net.Conn。
type asyncWriteConn struct {
	net.Conn
	writes chan []byte
	done   chan struct{}
	once   sync.Once
}

func newAsyncWriteConn(c net.Conn) *asyncWriteConn {
	a := &asyncWriteConn{Conn: c, writes: make(chan []byte, 64), done: make(chan struct{})}
	go func() {
		for {
			select {
			case b := <-a.writes:
				if _, err := c.Write(b); err != nil {
					return
				}
			case <-a.done:
				return
			}
		}
	}()
	return a
}

func (a *asyncWriteConn) Write(b []byte) (int, error) {
	select {
	case a.writes <- b:
		return len(b), nil
	case <-a.done:
		return 0, net.ErrClosed
	}
}

func (a *asyncWriteConn) Close() error {
	a.once.Do(func() { close(a.done) })
	return a.Conn.Close()
}

// asyncWriteTransport 是包着 pipeTransport 的客户端写解耦变体,服务端侧
// 保持原样(串行处理语义不变)。
type asyncWriteTransport struct {
	inner *pipeTransport
}

func (t *asyncWriteTransport) Listen() (net.Listener, error) { return t.inner.Listen() }
func (t *asyncWriteTransport) SocketPath() string            { return t.inner.SocketPath() }

func (t *asyncWriteTransport) Dial() (net.Conn, error) {
	c, err := t.inner.Dial()
	if err != nil {
		return nil, err
	}
	return newAsyncWriteConn(c), nil
}

// TestClientConcurrentSendsAreIsolated:并发 Send 各自拿到自己的响应。
// 串位 = 拿到别的在途请求的 token。服务端对单连接是串行「读一帧回一帧」,
// 响应顺序严格跟随请求顺序,所以一旦数据对不上必然是客户端读写交错所致。
func TestClientConcurrentSendsAreIsolated(t *testing.T) {
	transport := &asyncWriteTransport{inner: &pipeTransport{}}
	server := NewServerWithTransport(transport)
	// echo handler:把请求里的 token 原样返回,串位即可观测。
	server.Register("echo", func(args json.RawMessage) (interface{}, error) {
		var payload struct {
			Token string `json:"token"`
		}
		if err := json.Unmarshal(args, &payload); err != nil {
			return nil, err
		}
		return payload.Token, nil
	})
	if err := server.Start(); err != nil {
		t.Fatalf("server start: %v", err)
	}
	t.Cleanup(func() { _ = server.Stop() })

	client := NewClientWithTransport(transport, "http://127.0.0.1:1")
	if err := client.Connect(); err != nil {
		t.Fatalf("connect: %v", err)
	}

	const workers = 16
	const rounds = 10
	for round := 0; round < rounds; round++ {
		start := make(chan struct{})
		var wg sync.WaitGroup
		errs := make([]error, workers)
		tokens := make([]string, workers)
		for i := range tokens {
			tokens[i] = fmt.Sprintf("w%d-r%d", i, round)
		}
		for i := 0; i < workers; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start // 栅栏:全部 goroutine 就位后同时发,最大化交错
				resp, err := client.Send("echo", map[string]string{"token": tokens[i]})
				if err != nil {
					errs[i] = err
					return
				}
				if got, _ := resp.Data.(string); got != tokens[i] {
					errs[i] = fmt.Errorf("响应串位: 期望 token %q, 实际 %q", tokens[i], got)
				}
			}(i)
		}
		close(start)

		// 看门狗:无互斥的实现会让并发 Decoder 争抢同一连接,帧被吞进
		// 别人的缓冲区后部分 goroutine 读不到帧而挂死——用有界失败代替
		// 测试永挂。
		done := make(chan struct{})
		go func() {
			wg.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatalf("第 %d 轮并发 Send 挂死——请求级互斥缺失的典型症状", round)
		}
		for i, err := range errs {
			if err != nil {
				t.Fatalf("第 %d 轮 worker %d: %v", round, i, err)
			}
		}
	}
}

// TestClientSendTimesOutWhenServerHangs:服务端吞掉请求不回包时,Send 必须
// 在注入的 300ms deadline 内带错返回。全局 requestTimeout 一行不动,只改
// 本实例字段,验证注入通道有效。
func TestClientSendTimesOutWhenServerHangs(t *testing.T) {
	transport := &pipeTransport{}
	listener, err := transport.Listen()
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	client := NewClientWithTransport(transport, "http://127.0.0.1:1")
	client.readTimeout = 300 * time.Millisecond // 注入小 deadline
	if err := client.Connect(); err != nil {
		t.Fatalf("connect: %v", err)
	}

	// 收走服务端连接:只吞请求,永不回包 = 服务端挂死。
	serverEnd, err := listener.Accept()
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	release := make(chan struct{})
	t.Cleanup(func() {
		close(release)
		_ = serverEnd.Close()
	})
	go func() {
		var req Request
		_ = json.NewDecoder(serverEnd).Decode(&req) // 吞请求
		<-release
	}()

	type sendResult struct {
		err     error
		elapsed time.Duration
	}
	resCh := make(chan sendResult, 1)
	go func() {
		s := time.Now()
		_, err := client.Send(ActionPing, nil)
		resCh <- sendResult{err: err, elapsed: time.Since(s)}
	}()

	select {
	case res := <-resCh:
		if res.err == nil {
			t.Fatal("服务端不回包时 Send 应超时报错,而不是成功返回")
		}
		if res.elapsed > 2*time.Second {
			t.Fatalf("Send 耗时 %v,注入的 300ms deadline 未生效", res.elapsed)
		}
		t.Logf("Send 在 %v 内按注入 deadline 失败: %v", res.elapsed, res.err)
	case <-time.After(3 * time.Second):
		t.Fatal("Send 超过 3s 未返回——读 deadline 未生效,永久阻塞复现")
	}
}

// TestClientSkipsMismatchedResponseID:服务端先回错帧(ID 不属于任何在途
// 请求,如上一轮漏读的迟到响应)再回对帧,Send 必须等到 ID 匹配的那帧,
// 错帧不得被当成本次结果。
func TestClientSkipsMismatchedResponseID(t *testing.T) {
	transport := &pipeTransport{}
	listener, err := transport.Listen()
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	client := NewClientWithTransport(transport, "http://127.0.0.1:1")
	if err := client.Connect(); err != nil {
		t.Fatalf("connect: %v", err)
	}

	serverEnd, err := listener.Accept()
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	t.Cleanup(func() { _ = serverEnd.Close() })

	type sendResult struct {
		resp *Response
		err  error
	}
	resCh := make(chan sendResult, 1)
	go func() {
		resp, err := client.Send(ActionPing, nil)
		resCh <- sendResult{resp: resp, err: err}
	}()

	// 服务端侧:读请求 → 先回错帧(陈旧 ID)→ 再回对帧(匹配 ID)。整体放
	// goroutine:net.Pipe 的写是同步的(对端读走才算写完),红灯态客户端
	// 读走错帧即退场,对帧将无人读而永久阻塞——若留在测试主 goroutine,
	// 会卡死在写帧上、永远到不了看门狗 select。主 goroutine 只守看门狗。
	go func() {
		var req Request
		if err := json.NewDecoder(serverEnd).Decode(&req); err != nil {
			return // 客户端没发出请求:看门狗兜底收尾
		}
		enc := json.NewEncoder(serverEnd)
		if err := enc.Encode(Response{ID: "stale-frame-leftover", OK: true, Data: "wrong"}); err != nil {
			return
		}
		_ = enc.Encode(Response{ID: req.ID, OK: true, Data: "right"})
	}()

	select {
	case res := <-resCh:
		if res.err != nil {
			t.Fatalf("Send failed: %v", res.err)
		}
		if got, _ := res.resp.Data.(string); got != "right" {
			t.Fatalf("Send 应返回 ID 匹配帧的数据,实际 data=%v——错帧被当成了结果", res.resp.Data)
		}
		if !res.resp.OK {
			t.Fatalf("expected ok=true, got error: %s", res.resp.Error)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Send 未在 3s 内返回——错帧过滤逻辑卡死")
	}
}
