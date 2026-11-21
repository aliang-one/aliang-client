package handlers

import (
	"bufio"
	"bytes"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// 起一个回声 agent：连上后把收到的二进制帧原样发回。
func startEchoAgent(t *testing.T) *httptest.Server {
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			mt, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if err := conn.WriteMessage(mt, data); err != nil {
				return
			}
		}
	}))
}

// drainBuffered 把 gorilla Upgrade 缓冲里滞留的早期帧字节写给 dst，
// 再交回裸 io.Copy——不排干会静默吞帧（回环下真实发生过）。
func TestDrainBufferedWritesResidualBytes(t *testing.T) {
	var out bytes.Buffer
	rw := bufio.NewReadWriter(bufio.NewReader(strings.NewReader("residual-bytes")), bufio.NewWriter(&out))
	// bufio 是惰性填充：不先触发一次读，缓冲里没有任何字节，排干就退化为空转。
	// Peek(1) 迫使 fill 把底层内容拉进缓冲，制造「握手期早期帧已滞留在 bufio
	// 缓冲」的前置状态（生产里对应 Hijack 时内核已送达、已被读进缓冲的字节）。
	if _, err := rw.Reader.Peek(1); err != nil {
		t.Fatalf("prime bufio buffer: %v", err)
	}
	if err := drainBuffered(&out, rw.Reader); err != nil {
		t.Fatalf("drainBuffered: %v", err)
	}
	if out.String() != "residual-bytes" {
		t.Fatalf("got %q", out.String())
	}
	// 排干后再次调用是 no-op
	if err := drainBuffered(&out, rw.Reader); err != nil {
		t.Fatalf("second drain: %v", err)
	}
}

func TestProxyAIStreamPassthroughEcho(t *testing.T) {
	agentSrv := startEchoAgent(t)
	defer agentSrv.Close()

	origTarget := aiStreamTargetHost
	aiStreamTargetHost = func() string { return strings.TrimPrefix(agentSrv.URL, "http://") }
	defer func() { aiStreamTargetHost = origTarget }()

	coreSrv := httptest.NewServer(http.HandlerFunc(proxyAIStream))
	defer coreSrv.Close()
	wsURL := "ws" + strings.TrimPrefix(coreSrv.URL, "http") + "/api/agent/ai/stream"

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial through proxy: %v", err)
	}
	defer conn.Close()
	if err := conn.WriteMessage(websocket.TextMessage, []byte("ping")); err != nil {
		t.Fatalf("write: %v", err)
	}
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if string(data) != "ping" {
		t.Fatalf("echo = %q, want ping", data)
	}
	// 第二帧：证明桥接在首次往返之后仍然存活，不是「一发一收即死」。
	if err := conn.WriteMessage(websocket.TextMessage, []byte("pong")); err != nil {
		t.Fatalf("write second frame: %v", err)
	}
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, data, err = conn.ReadMessage()
	if err != nil {
		t.Fatalf("read second echo: %v", err)
	}
	if string(data) != "pong" {
		t.Fatalf("second echo = %q, want pong", data)
	}
	// 8KB 帧跨过 4096 的 bufio 缓冲边界，验证裸泵不分帧不截断。
	big := bytes.Repeat([]byte("x"), 8*1024)
	if err := conn.WriteMessage(websocket.BinaryMessage, big); err != nil {
		t.Fatalf("write 8KB frame: %v", err)
	}
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, data, err = conn.ReadMessage()
	if err != nil {
		t.Fatalf("read 8KB echo: %v", err)
	}
	if !bytes.Equal(data, big) {
		t.Fatalf("8KB echo: got %d bytes, want %d", len(data), len(big))
	}
}

func TestProxyAIStreamHostPolicyRejectsNonLocalhost(t *testing.T) {
	// 指向回声 agent：若 host 策略被绕过，透传会真的建立（测试才算有区分度）。
	agentSrv := startEchoAgent(t)
	defer agentSrv.Close()

	origTarget := aiStreamTargetHost
	aiStreamTargetHost = func() string { return strings.TrimPrefix(agentSrv.URL, "http://") }
	defer func() { aiStreamTargetHost = origTarget }()

	// gorilla Dial 的 Host 取自 URL，没法直连又带非 localhost Host；在服务端
	// mux 里覆写 r.Host 来模拟「经 172.16.222.1:56431 访问仪表盘」。无 Origin
	// 的非 loopback Host（curl/脚本直连）仍按既有底线拒绝。
	coreSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Host = "172.16.222.1:56431"
		proxyAIStream(w, r)
	}))
	defer coreSrv.Close()
	wsURL := "ws" + strings.TrimPrefix(coreSrv.URL, "http") + "/api/agent/ai/stream"

	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err == nil {
		conn.Close()
		t.Fatal("expected non-localhost Host (no Origin) to be rejected, got connection")
	}
	if resp == nil {
		t.Fatal("expected 403 HTTP response, got nil")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
}

// TestProxyAIStreamSameOriginLANAllowed 是 2026-11-19 生产假断线的回归测试：
// 浏览器经 LAN IP 打开仪表盘（Host=172.16.222.1:56431），aiSocketUrl() 发出
// 同源 Origin，聊天 WS 必须照常握手透传——旧 localhost-only 白名单在这里
// 恒 403，每条对话被前端画成「与设备的连接已中断」。
func TestProxyAIStreamSameOriginLANAllowed(t *testing.T) {
	agentSrv := startEchoAgent(t)
	defer agentSrv.Close()

	origTarget := aiStreamTargetHost
	aiStreamTargetHost = func() string { return strings.TrimPrefix(agentSrv.URL, "http://") }
	defer func() { aiStreamTargetHost = origTarget }()

	coreSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Host = "172.16.222.1:56431"
		proxyAIStream(w, r)
	}))
	defer coreSrv.Close()
	wsURL := "ws" + strings.TrimPrefix(coreSrv.URL, "http") + "/api/agent/ai/stream"

	hdr := http.Header{"Origin": []string{"http://172.16.222.1:56431"}}
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, hdr)
	if err != nil {
		t.Fatalf("same-origin LAN dial rejected (resp=%v): %v", resp, err)
	}
	defer conn.Close()
	if err := conn.WriteMessage(websocket.TextMessage, []byte("ping")); err != nil {
		t.Fatalf("write: %v", err)
	}
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if string(data) != "ping" {
		t.Fatalf("echo = %q, want ping", data)
	}
}

// TestProxyAIStreamCrossOriginRejected：第三方页面开着 ws://设备IP 的跨站
// 连接（Origin=攻击页 ≠ 本机 Host）必须拒绝——真同源闸的核心价值。
func TestProxyAIStreamCrossOriginRejected(t *testing.T) {
	agentSrv := startEchoAgent(t)
	defer agentSrv.Close()

	origTarget := aiStreamTargetHost
	aiStreamTargetHost = func() string { return strings.TrimPrefix(agentSrv.URL, "http://") }
	defer func() { aiStreamTargetHost = origTarget }()

	coreSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Host = "172.16.222.1:56431"
		proxyAIStream(w, r)
	}))
	defer coreSrv.Close()
	wsURL := "ws" + strings.TrimPrefix(coreSrv.URL, "http") + "/api/agent/ai/stream"

	hdr := http.Header{"Origin": []string{"http://evil.example"}}
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, hdr)
	if err == nil {
		conn.Close()
		t.Fatal("expected cross-origin dial to be rejected, got connection")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403, got resp=%v err=%v", resp, err)
	}
	defer func() {
		if resp != nil {
			resp.Body.Close()
		}
	}()
}

// TestAIStreamHostAllowedTable：aiStreamHostAllowed 的策略表——同源/跨源/
// 无 Origin×loopback/无 Origin×LAN/Origin=null 逐格钉死。
func TestAIStreamHostAllowedTable(t *testing.T) {
	cases := []struct {
		name   string
		host   string
		origin string
		want   bool
	}{
		{"same-origin lan", "172.16.222.1:56431", "http://172.16.222.1:56431", true},
		{"same-origin port differs", "172.16.222.1:56431", "http://172.16.222.1:9999", false},
		{"same-origin host differs", "172.16.222.1:56431", "http://10.0.0.5:56431", false},
		{"cross-origin", "172.16.222.1:56431", "http://evil.example", false},
		{"loopback localhost no origin", "localhost:56431", "", true},
		{"loopback 127 no origin", "127.0.0.1:56433", "", true},
		{"lan no origin", "172.16.222.1:56431", "", false},
		{"origin null", "172.16.222.1:56431", "null", false},
		{"origin garbage", "172.16.222.1:56431", "::::", false},
	}
	for _, tc := range cases {
		r := httptest.NewRequest(http.MethodGet, "http://listener/api/agent/ai/stream", nil)
		r.Host = tc.host
		if tc.origin != "" {
			r.Header.Set("Origin", tc.origin)
		}
		if got := aiStreamHostAllowed(r); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestProxyAIStreamAgentOfflineCloses1011(t *testing.T) {
	origTarget := aiStreamTargetHost
	aiStreamTargetHost = func() string { return "127.0.0.1:1" } // 必然拒绝
	defer func() { aiStreamTargetHost = origTarget }()

	coreSrv := httptest.NewServer(http.HandlerFunc(proxyAIStream))
	defer coreSrv.Close()
	wsURL := "ws" + strings.TrimPrefix(coreSrv.URL, "http") + "/api/agent/ai/stream"

	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		// 升级失败也算可达性错误可见，但本实现承诺先 Upgrade 再发 close 帧；
		// 走到 err 分支即说明实现用了裸 4xx/5xx，不合格。
		t.Fatalf("expected upgrade+close(1011), got dial error: %v (resp=%v)", err, resp)
	}
	defer conn.Close()
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, _, err = conn.ReadMessage() // gorilla 把 close 帧转成 *websocket.CloseError 返回
	if err != nil {
		var ce *websocket.CloseError
		if errors.As(err, &ce) {
			if ce.Code != websocket.CloseInternalServerErr {
				t.Fatalf("close code = %d, want 1011", ce.Code)
			}
			if !strings.Contains(ce.Text, "device agent offline") {
				t.Fatalf("close reason = %q", ce.Text)
			}
			return
		}
		t.Fatalf("read close: %v", err)
	}
	t.Fatal("expected close frame, got normal message")
}

// 透传腿 Cookie 转发（2026-11-21 审计四批评审 blocker 修订钉）：同用户单
// 二进制部署下 core 与 agent 共享 session 存储——浏览器 Cookie 必须原样进
// handshakeAgent 自造请求，agent 侧握手门才能验签；分路径部署走 loopback
// 豁免不受影响。两种形态都钉住。
func TestHandshakeAgentForwardsBrowserCookie(t *testing.T) {
	var seenCookie string
	up := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool {
		seenCookie = r.Header.Get("Cookie")
		return true
	}}
	agentSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		conn.Close()
	}))
	defer agentSrv.Close()

	origTarget := aiStreamTargetHost
	aiStreamTargetHost = func() string { return strings.TrimPrefix(agentSrv.URL, "http://") }
	defer func() { aiStreamTargetHost = origTarget }()

	agentConn, err := net.DialTimeout("tcp", aiStreamTargetHost(), aiStreamProxyDialTimeout)
	if err != nil {
		t.Fatalf("dial agent: %v", err)
	}
	defer agentConn.Close()

	if _, err := handshakeAgent(agentConn, aiStreamTargetHost(), "/api/agent/ai/stream", "aliang_dashboard_session=abc"); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	if seenCookie != "aliang_dashboard_session=abc" {
		t.Fatalf("agent 侧未收到转发 Cookie: %q", seenCookie)
	}

	// 空 cookie：不带 Cookie 头（分路径部署形态不泄漏空/错值）
	agentConn2, err := net.DialTimeout("tcp", aiStreamTargetHost(), aiStreamProxyDialTimeout)
	if err != nil {
		t.Fatalf("dial agent 2: %v", err)
	}
	defer agentConn2.Close()
	seenCookie = ""
	if _, err := handshakeAgent(agentConn2, aiStreamTargetHost(), "/api/agent/ai/stream", ""); err != nil {
		t.Fatalf("handshake 2: %v", err)
	}
	if seenCookie != "" {
		t.Fatalf("空 cookie 不应带 Cookie 头: %q", seenCookie)
	}
}
