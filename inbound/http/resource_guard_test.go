package http

// 资源防护测试:HTTP 面的请求头读取必须有期限,accept 循环对连续错误必须退避。
// 两者都是「一条/一串半开连接白占资源」的入口:嗅探清掉首段期限之后,
// ReadRequest 在无期限状态下等待——发半行请求行就停住的客户端每条连接白占
// 一个 fd + 一个 goroutine,且这一段发生在会话租约之前,任何来源都能触发。

import (
	"errors"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"aliang.one/nursorgate/processor/config"
)

func TestRawHTTPConnectionTimesOutOnStalledRequestHead(t *testing.T) {
	loginForProxyFace(t)
	origTimeout := httpReadTimeout
	httpReadTimeout = 150 * time.Millisecond
	t.Cleanup(func() { httpReadTimeout = origTimeout })

	client, server := net.Pipe()
	t.Cleanup(func() { _ = client.Close(); _ = server.Close() })
	done := make(chan struct{})
	go func() {
		defer close(done)
		handleRawConnection(server)
	}()
	go func() {
		// 只发半行请求行,然后保持连接不写也不关。
		_, _ = io.WriteString(client, "GET /heal")
		time.Sleep(2 * time.Second)
		_ = client.Close()
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("半截请求头必须受读期限约束,不能无限期占住连接")
	}
}

// errorListener 是一个 Accept 恒返回非关闭类错误的监听器(模拟 fd 耗尽等
// 瞬时故障风暴)。
type errorListener struct {
	accepts int32
	closed  chan struct{}
}

func newErrorListener() *errorListener { return &errorListener{closed: make(chan struct{})} }

func (l *errorListener) Accept() (net.Conn, error) {
	atomic.AddInt32(&l.accepts, 1)
	return nil, errors.New("accept: too many open files")
}

func (l *errorListener) Close() error {
	select {
	case <-l.closed:
	default:
		close(l.closed)
	}
	return nil
}

func (l *errorListener) Addr() net.Addr {
	return &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: config.DefaultHTTPProxyPort}
}

func TestUnifiedIngressAcceptLoopBacksOffOnErrorStorm(t *testing.T) {
	loginForProxyFace(t)
	origListen, origBackoff := listenTCP, acceptErrorBackoff
	listener := newErrorListener()
	listenTCP = func(network, addr string) (net.Listener, error) { return listener, nil }
	acceptErrorBackoff = 50 * time.Millisecond
	t.Cleanup(func() {
		listenTCP = origListen
		acceptErrorBackoff = origBackoff
	})

	returned := make(chan struct{})
	go func() {
		defer close(returned)
		startUnifiedIngress(false)
	}()
	// 等循环真的跑起来(至少吃到一次错误),再观察一个退避周期内的转速。
	deadline := time.Now().Add(2 * time.Second)
	for atomic.LoadInt32(&listener.accepts) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("accept loop never started")
		}
		time.Sleep(5 * time.Millisecond)
	}
	base := atomic.LoadInt32(&listener.accepts)
	time.Sleep(300 * time.Millisecond)
	spun := atomic.LoadInt32(&listener.accepts) - base

	// 先停循环再断言:真实监听器靠 Stop 取消代次上下文退出。
	StopHttpProxy()
	select {
	case <-returned:
	case <-time.After(2 * time.Second):
		t.Fatal("accept loop did not return after stop")
	}

	// 300ms / 50ms 退避 ≈ 6 次;给足余量到 20。裸 continue 会是数千次。
	if spun > 20 {
		t.Fatalf("accept error storm spun %d times in 300ms, backoff missing", spun)
	}
}
