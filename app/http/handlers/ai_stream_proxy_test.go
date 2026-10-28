package handlers

import (
	"bufio"
	"bytes"
	"errors"
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
	// mux 里覆写 r.Host 来模拟「经 172.16.222.1:56431 访问仪表盘」。
	coreSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Host = "172.16.222.1:56431"
		proxyAIStream(w, r)
	}))
	defer coreSrv.Close()
	wsURL := "ws" + strings.TrimPrefix(coreSrv.URL, "http") + "/api/agent/ai/stream"

	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err == nil {
		conn.Close()
		t.Fatal("expected non-localhost Host to be rejected, got connection")
	}
	if resp == nil {
		t.Fatal("expected 403 HTTP response, got nil")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
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
