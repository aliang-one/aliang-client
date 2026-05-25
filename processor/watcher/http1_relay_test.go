package tls

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"

	"aliang.one/nursorgate/processor/config"
)

func http1DropBoolPtr(value bool) *bool {
	return &value
}

func resetHTTP1DropConfigForTest(t *testing.T, cfg *config.HTTP1DropConfig) {
	t.Helper()
	config.ResetGlobalConfigForTest()
	config.SetGlobalConfig(&config.Config{
		Core: &config.CoreConfig{APIServer: "https://api.aliang.one"},
		Customer: &config.CustomerConfig{
			HTTP1Drop: cfg,
		},
	})
	t.Cleanup(config.ResetGlobalConfigForTest)
}

func TestRelayHTTP1_DropsMetricPathAndKeepsConnectionAligned(t *testing.T) {
	resetHTTP1DropConfigForTest(t, nil)

	clientConn, proxyClientConn := net.Pipe()
	defer clientConn.Close()
	defer proxyClientConn.Close()

	proxyRemoteConn, remoteConn := net.Pipe()
	defer proxyRemoteConn.Close()
	defer remoteConn.Close()

	relayDone := make(chan error, 1)
	go func() {
		_, err := RelayHTTP1(t.Context(), proxyClientConn, proxyRemoteConn)
		relayDone <- err
	}()

	remoteReqCh := make(chan string, 1)
	go func() {
		req, err := http.ReadRequest(bufio.NewReader(remoteConn))
		if err != nil {
			remoteReqCh <- "read error: " + err.Error()
			return
		}
		body, err := io.ReadAll(req.Body)
		if err != nil {
			remoteReqCh <- "body error: " + err.Error()
			return
		}
		_ = req.Body.Close()
		if req.URL.Path != "/v1/chat/completions" {
			remoteReqCh <- "unexpected path: " + req.URL.Path
			return
		}
		if string(body) != `{"ok":true}` {
			remoteReqCh <- "unexpected body: " + string(body)
			return
		}

		_, _ = io.WriteString(remoteConn, ""+
			"HTTP/1.1 200 OK\r\n"+
			"Content-Length: 2\r\n"+
			"Connection: close\r\n"+
			"\r\n"+
			"OK")
		remoteReqCh <- "ok"
	}()

	clientWriteDone := make(chan error, 1)
	go func() {
		_, err := io.WriteString(clientConn, ""+
			"POST /metrics/upload HTTP/1.1\r\n"+
			"Host: example.com\r\n"+
			"Content-Length: 7\r\n"+
			"\r\n"+
			"ignored"+
			"POST /v1/chat/completions HTTP/1.1\r\n"+
			"Host: example.com\r\n"+
			"Content-Length: 11\r\n"+
			"Connection: close\r\n"+
			"\r\n"+
			`{"ok":true}`)
		clientWriteDone <- err
	}()

	reader := bufio.NewReader(clientConn)
	droppedResp, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatalf("ReadResponse(dropped) error = %v", err)
	}
	if droppedResp.StatusCode != http.StatusNoContent {
		t.Fatalf("dropped response status = %d, want 204", droppedResp.StatusCode)
	}
	if got := droppedResp.Header.Get("X-Aliang-Dropped"); got != "http1-path" {
		t.Fatalf("X-Aliang-Dropped = %q, want http1-path", got)
	}
	_ = droppedResp.Body.Close()

	forwardedResp, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatalf("ReadResponse(forwarded) error = %v", err)
	}
	body, err := io.ReadAll(forwardedResp.Body)
	if err != nil {
		t.Fatalf("read forwarded body error = %v", err)
	}
	_ = forwardedResp.Body.Close()
	if forwardedResp.StatusCode != http.StatusOK || string(body) != "OK" {
		t.Fatalf("forwarded response = status %d body %q, want 200 OK", forwardedResp.StatusCode, string(body))
	}

	if got := <-remoteReqCh; got != "ok" {
		t.Fatalf("remote request result = %q", got)
	}
	if err := <-clientWriteDone; err != nil {
		t.Fatalf("client write error = %v", err)
	}
	if err := <-relayDone; err != nil {
		t.Fatalf("RelayHTTP1 error = %v", err)
	}
}

func TestRelayHTTP1_DropCanBeDisabled(t *testing.T) {
	resetHTTP1DropConfigForTest(t, &config.HTTP1DropConfig{
		Enabled: http1DropBoolPtr(false),
	})

	clientConn, proxyClientConn := net.Pipe()
	defer clientConn.Close()
	defer proxyClientConn.Close()

	proxyRemoteConn, remoteConn := net.Pipe()
	defer proxyRemoteConn.Close()
	defer remoteConn.Close()

	relayDone := make(chan error, 1)
	go func() {
		_, err := RelayHTTP1(t.Context(), proxyClientConn, proxyRemoteConn)
		relayDone <- err
	}()

	remoteReqCh := make(chan string, 1)
	go func() {
		req, err := http.ReadRequest(bufio.NewReader(remoteConn))
		if err != nil {
			remoteReqCh <- "read error: " + err.Error()
			return
		}
		_, _ = io.Copy(io.Discard, req.Body)
		_ = req.Body.Close()
		remoteReqCh <- req.URL.Path
		_, _ = io.WriteString(remoteConn, ""+
			"HTTP/1.1 200 OK\r\n"+
			"Content-Length: 0\r\n"+
			"Connection: close\r\n"+
			"\r\n")
	}()

	_, err := io.WriteString(clientConn, ""+
		"GET /metrics HTTP/1.1\r\n"+
		"Host: example.com\r\n"+
		"Connection: close\r\n"+
		"\r\n")
	if err != nil {
		t.Fatalf("client write error = %v", err)
	}

	resp, err := http.ReadResponse(bufio.NewReader(clientConn), nil)
	if err != nil {
		t.Fatalf("ReadResponse error = %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("response status = %d, want 200", resp.StatusCode)
	}
	if got := <-remoteReqCh; got != "/metrics" {
		t.Fatalf("remote path = %q, want /metrics", got)
	}
	if err := <-relayDone; err != nil {
		t.Fatalf("RelayHTTP1 error = %v", err)
	}
}

func TestRelayHTTP1_CustomDropPathContains(t *testing.T) {
	resetHTTP1DropConfigForTest(t, &config.HTTP1DropConfig{
		Enabled:      http1DropBoolPtr(true),
		PathContains: []string{"telemetry"},
	})

	req, err := http.NewRequest(http.MethodPost, "http://example.com/v1/telemetry/events", strings.NewReader("body"))
	if err != nil {
		t.Fatalf("NewRequest error = %v", err)
	}
	if !shouldDropHTTP1Request(req) {
		t.Fatal("shouldDropHTTP1Request() = false, want true")
	}

	metricReq, err := http.NewRequest(http.MethodPost, "http://example.com/v1/metrics", strings.NewReader("body"))
	if err != nil {
		t.Fatalf("NewRequest metric error = %v", err)
	}
	if shouldDropHTTP1Request(metricReq) {
		t.Fatal("metric request unexpectedly dropped after custom path_contains")
	}
}

func TestShouldCloseAfterDroppedHTTP1Request_ClosesOnStreamingBodies(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "http://example.com/metrics", io.NopCloser(strings.NewReader("body")))
	if err != nil {
		t.Fatalf("NewRequest error = %v", err)
	}
	req.ContentLength = http1UnknownRequestBodySize
	if !shouldCloseAfterDroppedHTTP1Request(req) {
		t.Fatal("unknown-length dropped body should close connection")
	}

	smallReq, err := http.NewRequest(http.MethodPost, "http://example.com/metrics", strings.NewReader("body"))
	if err != nil {
		t.Fatalf("NewRequest small error = %v", err)
	}
	if shouldCloseAfterDroppedHTTP1Request(smallReq) {
		t.Fatal("small known-length dropped body should keep connection open")
	}
}
