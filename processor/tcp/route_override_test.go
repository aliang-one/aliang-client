package tcp

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	M "aliang.one/nursorgate/inbound/tun/metadata"
	"aliang.one/nursorgate/outbound"
	outboundproxy "aliang.one/nursorgate/outbound/proxy"
	"aliang.one/nursorgate/outbound/proxy/proto"
	"aliang.one/nursorgate/processor/config"
	"aliang.one/nursorgate/processor/mirror"
	"aliang.one/nursorgate/processor/statistic"
	"golang.org/x/net/http2"
)

type fakeConn struct {
	reader *bytes.Reader
}

func newFakeConn(payload []byte) *fakeConn {
	return &fakeConn{reader: bytes.NewReader(payload)}
}

func (c *fakeConn) Read(p []byte) (int, error)  { return c.reader.Read(p) }
func (c *fakeConn) Write(p []byte) (int, error) { return len(p), nil }
func (c *fakeConn) Close() error                { return nil }
func (c *fakeConn) LocalAddr() net.Addr {
	return &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 56432}
}
func (c *fakeConn) RemoteAddr() net.Addr {
	return &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 41000}
}
func (c *fakeConn) SetDeadline(time.Time) error      { return nil }
func (c *fakeConn) SetReadDeadline(time.Time) error  { return nil }
func (c *fakeConn) SetWriteDeadline(time.Time) error { return nil }
func (c *fakeConn) CloseRead() error                 { return nil }
func (c *fakeConn) CloseWrite() error                { return nil }

type fakeAliangProxy struct{}

func (p *fakeAliangProxy) DialContext(context.Context, *M.Metadata) (net.Conn, error) {
	return newFakeConn(nil), nil
}

func (p *fakeAliangProxy) DialUDP(*M.Metadata) (net.PacketConn, error) {
	return nil, io.EOF
}

func (p *fakeAliangProxy) Addr() string {
	return "fake-aliang"
}

func (p *fakeAliangProxy) Proto() proto.Proto {
	return proto.Aliang
}

var _ outboundproxy.Proxy = (*fakeAliangProxy)(nil)

type fakeMITMTLSHandler struct {
	mitmPayload []byte
	mitmHost    string
	extractSNI  string
	extractBuf  []byte
}

func (h *fakeMITMTLSHandler) ExtractSNI(context.Context, net.Conn) (string, []byte, error) {
	return h.extractSNI, append([]byte(nil), h.extractBuf...), nil
}

func (h *fakeMITMTLSHandler) PerformMITM(_ context.Context, _ net.Conn, serverName string) (net.Conn, error) {
	h.mitmHost = serverName
	return newFakeConn(h.mitmPayload), nil
}

func (h *fakeMITMTLSHandler) DetermineRoute(string) ProxyRoute {
	return RouteDirect
}

func (h *fakeMITMTLSHandler) DetermineRouteWithContext(*M.Metadata) (ProxyRoute, bool) {
	return RouteDirect, false
}

type mirrorAwareRelayManager struct {
	mu            sync.Mutex
	originWrapped bool
}

func (r *mirrorAwareRelayManager) Relay(ctx context.Context, originConn, remoteConn net.Conn, metadata *M.Metadata) (*RelayStats, error) {
	r.mu.Lock()
	r.originWrapped = strings.Contains(fmt.Sprintf("%T", originConn), "mirror.mirrorConn")
	r.mu.Unlock()

	payload, err := io.ReadAll(originConn)
	if err != nil {
		return nil, err
	}

	_ = remoteConn.Close()
	now := time.Now()
	return &RelayStats{
		StartedAt:          now,
		FirstResponseAt:    now,
		CompletedAt:        now,
		ClientToServerByte: int64(len(payload)),
		RequestPayload:     payload,
	}, nil
}

func TestDetermineRouteWithContext_ForcesAliangForLocalHTTPProxyPort(t *testing.T) {
	handler := NewDefaultTLSHandler()
	metadata := &M.Metadata{
		Network: M.TCP,
		DstIP:   netip.MustParseAddr("127.0.0.1"),
		DstPort: uint16(config.DefaultHTTPProxyPort),
	}

	route, requiresSNI := handler.DetermineRouteWithContext(metadata)
	if route != RouteToALiang {
		t.Fatalf("unexpected route: got %v want %v", route, RouteToALiang)
	}
	if requiresSNI {
		t.Fatal("expected local proxy override to not require SNI")
	}
}

func TestHandleNonTLS_DoesNotShortCircuitLocalHTTPProxyPortToDirect(t *testing.T) {
	config.ResetRoutingApplyStoreForTest()

	registry := outbound.GetRegistry()
	registry.Clear()
	defer registry.Clear()
	if err := registry.Register("aliang", &fakeAliangProxy{}); err != nil {
		t.Fatalf("register fake aliang proxy failed: %v", err)
	}

	handler := NewTCPConnectionHandler(NewDefaultProtocolDetector(), NewDefaultTLSHandler(), nil, nil)
	metadata := &M.Metadata{
		Network: M.TCP,
		DstIP:   netip.MustParseAddr("127.0.0.1"),
		DstPort: uint16(config.DefaultHTTPProxyPort),
	}

	remote, _, err := handler.handleNonTLS(context.Background(), newFakeConn(nil), metadata)
	if err != nil {
		t.Fatalf("handleNonTLS failed: %v", err)
	}
	if remote == nil {
		t.Fatal("expected remote conn to be created via aliang override")
	}
	if got, want := metadata.Route, "RouteToALiang"; got != want {
		t.Fatalf("unexpected metadata route: got %q want %q", got, want)
	}
}

func TestHandle_MirrorsConfiguredTrafficEvenWhenRouteIsDirect(t *testing.T) {
	config.ResetGlobalConfigForTest()
	defer func() {
		config.SetGlobalConfig(nil)
		mirror.InitGlobalForwarder()
	}()

	var (
		mu     sync.Mutex
		bodies [][]byte
		recvCh = make(chan []byte, 8)
		server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			_ = r.Body.Close()
			copyBody := append([]byte(nil), body...)
			mu.Lock()
			bodies = append(bodies, copyBody)
			mu.Unlock()
			recvCh <- copyBody
			w.WriteHeader(http.StatusOK)
		}))
	)
	defer server.Close()

	config.SetGlobalConfig(&config.Config{
		Customer: &config.CustomerConfig{
			TrafficMirror: &config.TrafficMirrorConfig{
				Enabled: true,
				Target:  server.URL,
				Domains: []string{"*.cursor.sh"},
			},
		},
	})
	mirror.InitGlobalForwarder()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen failed: %v", err)
	}
	defer ln.Close()

	accepted := make(chan net.Conn, 1)
	go func() {
		conn, acceptErr := ln.Accept()
		if acceptErr == nil {
			accepted <- conn
		}
	}()

	relay := &mirrorAwareRelayManager{}
	handler := NewTCPConnectionHandler(NewDefaultProtocolDetector(), NewDefaultTLSHandler(), relay, statistic.DefaultManager)
	metadata := &M.Metadata{
		Network:  M.TCP,
		SrcIP:    netip.MustParseAddr("127.0.0.1"),
		SrcPort:  41000,
		DstIP:    netip.MustParseAddr("127.0.0.1"),
		DstPort:  uint16(ln.Addr().(*net.TCPAddr).Port),
		HostName: "api.cursor.sh",
	}

	originConn := newFakeConn([]byte("hello mirror"))
	if err := handler.Handle(context.Background(), originConn, metadata); err != nil {
		t.Fatalf("Handle failed: %v", err)
	}

	if got, want := metadata.Route, "RouteDirect"; got != want {
		t.Fatalf("unexpected metadata route: got %q want %q", got, want)
	}

	relay.mu.Lock()
	originWrapped := relay.originWrapped
	relay.mu.Unlock()
	if !originWrapped {
		t.Fatal("expected configured mirror flow to wrap direct-route origin connection")
	}

	select {
	case conn := <-accepted:
		_ = conn.Close()
	case <-time.After(2 * time.Second):
		t.Fatal("expected direct dial to reach local listener")
	}

	deadline := time.After(2 * time.Second)
	for {
		mu.Lock()
		gotStart := false
		gotChunk := false
		for _, body := range bodies {
			s := string(body)
			if strings.Contains(s, `"event_type":"flow_start"`) {
				gotStart = true
			}
			if strings.Contains(s, `"direction":"request"`) {
				gotChunk = true
			}
		}
		mu.Unlock()
		if gotStart && gotChunk {
			break
		}
		select {
		case body := <-recvCh:
			_ = body
		case <-deadline:
			t.Fatalf("expected mirrored flow_start and request chunk to be forwarded, got bodies=%d", len(bodies))
		}
	}
}

func TestShouldMITMForMirror_OnlyMatchesNonAliangMirroredHost(t *testing.T) {
	config.ResetGlobalConfigForTest()
	defer config.ResetGlobalConfigForTest()

	config.SetGlobalConfig(&config.Config{
		Customer: &config.CustomerConfig{
			TrafficMirror: &config.TrafficMirrorConfig{
				Enabled: true,
				Target:  "http://127.0.0.1:1/mirror",
				Domains: []string{"*.cursor.sh"},
			},
		},
	})

	metadata := &M.Metadata{HostName: "api.cursor.sh"}
	if !shouldMITMForMirror(metadata, RouteDirect) {
		t.Fatal("expected direct mirrored host to use monitor MITM")
	}
	if !shouldMITMForMirror(metadata, RouteToLocalProxy) {
		t.Fatal("expected local proxy mirrored host to use monitor MITM")
	}
	if shouldMITMForMirror(metadata, RouteToALiang) {
		t.Fatal("expected toAliang route to keep existing MITM path")
	}
	if shouldMITMForMirror(&M.Metadata{HostName: "api.openai.com"}, RouteDirect) {
		t.Fatal("expected unmatched host to skip monitor MITM")
	}
}

func TestMirrorUpstreamNextProtos_PreservesClientProtocol(t *testing.T) {
	if got := mirrorUpstreamNextProtos(http2.NextProtoTLS, AppProtoHTTP1); len(got) != 1 || got[0] != http2.NextProtoTLS {
		t.Fatalf("h2 client next protos = %v, want [h2]", got)
	}
	if got := mirrorUpstreamNextProtos("http/1.1", AppProtoHTTP2); len(got) != 1 || got[0] != "http/1.1" {
		t.Fatalf("http/1.1 client next protos = %v, want [http/1.1]", got)
	}
	if got := mirrorUpstreamNextProtos("", AppProtoHTTP2); len(got) != 1 || got[0] != http2.NextProtoTLS {
		t.Fatalf("http2 app next protos = %v, want [h2]", got)
	}
	if got := mirrorUpstreamNextProtos("", AppProtoUnknown); len(got) != 2 || got[0] != http2.NextProtoTLS || got[1] != "http/1.1" {
		t.Fatalf("unknown app next protos = %v, want [h2 http/1.1]", got)
	}
}

func TestDetectApplicationProtocolWithALPN_FallsBackWhenPrefetchIncomplete(t *testing.T) {
	if got := detectApplicationProtocolWithALPN([]byte("PRI * HTTP/2.0\r\n"), http2.NextProtoTLS); got != AppProtoHTTP2 {
		t.Fatalf("incomplete h2 preface with h2 ALPN = %q, want %q", got, AppProtoHTTP2)
	}
	if got := detectApplicationProtocolWithALPN(nil, "http/1.1"); got != AppProtoHTTP1 {
		t.Fatalf("empty prefetch with http/1.1 ALPN = %q, want %q", got, AppProtoHTTP1)
	}
	if got := detectApplicationProtocolWithALPN([]byte("GET / HTTP/1.1\r\n"), http2.NextProtoTLS); got != AppProtoHTTP1 {
		t.Fatalf("sniffed HTTP/1 should win over ALPN fallback, got %q", got)
	}
	if got := detectApplicationProtocolWithALPN(nil, ""); got != AppProtoUnknown {
		t.Fatalf("empty prefetch without ALPN = %q, want %q", got, AppProtoUnknown)
	}
}

func TestValidateMirrorUpstreamALPN_RejectsHTTP2Downgrade(t *testing.T) {
	if err := validateMirrorUpstreamALPN(http2.NextProtoTLS, AppProtoHTTP2, ""); err == nil {
		t.Fatal("expected h2 client without upstream h2 to fail")
	}
	if err := validateMirrorUpstreamALPN("", AppProtoHTTP2, "http/1.1"); err == nil {
		t.Fatal("expected h2 app without upstream h2 to fail")
	}
	if err := validateMirrorUpstreamALPN("http/1.1", AppProtoHTTP1, ""); err != nil {
		t.Fatalf("expected http/1.1 client with empty upstream ALPN to pass, got %v", err)
	}
	if err := validateMirrorUpstreamALPN(http2.NextProtoTLS, AppProtoHTTP2, http2.NextProtoTLS); err != nil {
		t.Fatalf("expected matching h2 ALPN to pass, got %v", err)
	}
}

func TestResolveTLSMirrorRoute_RewrapsDirectRouteAndPreservesPlaintextForMirror(t *testing.T) {
	config.ResetGlobalConfigForTest()
	defer config.ResetGlobalConfigForTest()

	var (
		gotServerName string
		gotNextProtos []string
	)
	previousEstablish := establishMirrorUpstreamTLSConn
	establishMirrorUpstreamTLSConn = func(_ context.Context, raw net.Conn, serverName string, nextProtos []string, _ string, _ string) (net.Conn, error) {
		gotServerName = serverName
		gotNextProtos = append([]string(nil), nextProtos...)
		return raw, nil
	}
	defer func() {
		establishMirrorUpstreamTLSConn = previousEstablish
	}()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen failed: %v", err)
	}
	defer ln.Close()

	accepted := make(chan net.Conn, 1)
	go func() {
		conn, acceptErr := ln.Accept()
		if acceptErr == nil {
			accepted <- conn
		}
	}()

	tlsHandler := &fakeMITMTLSHandler{
		mitmPayload: []byte("GET / HTTP/1.1\r\nHost: 127.0.0.1\r\n\r\n"),
	}
	handler := NewTCPConnectionHandler(NewDefaultProtocolDetector(), tlsHandler, nil, statistic.DefaultManager)
	metadata := &M.Metadata{
		Network:  M.TCP,
		DstIP:    netip.MustParseAddr("127.0.0.1"),
		DstPort:  uint16(ln.Addr().(*net.TCPAddr).Port),
		HostName: "127.0.0.1",
	}

	remote, origin, err := handler.resolveTLSMirrorRoute(context.Background(), newFakeConn(nil), metadata, RouteDirect, nil, "")
	if err != nil {
		t.Fatalf("resolveTLSMirrorRoute failed: %v", err)
	}
	defer remote.Close()
	defer origin.Close()

	if tlsHandler.mitmHost != "127.0.0.1" {
		t.Fatalf("MITM host = %q, want 127.0.0.1", tlsHandler.mitmHost)
	}
	if gotServerName != "127.0.0.1" {
		t.Fatalf("upstream TLS server name = %q, want 127.0.0.1", gotServerName)
	}
	if len(gotNextProtos) != 1 || gotNextProtos[0] != "http/1.1" {
		t.Fatalf("upstream next protos = %v, want [http/1.1]", gotNextProtos)
	}
	if metadata.AppProto != AppProtoHTTP1 {
		t.Fatalf("metadata AppProto = %q, want %q", metadata.AppProto, AppProtoHTTP1)
	}
	if metadata.Route != "RouteDirect" {
		t.Fatalf("metadata Route = %q, want RouteDirect", metadata.Route)
	}

	plaintext, err := io.ReadAll(origin)
	if err != nil {
		t.Fatalf("read mirrored plaintext failed: %v", err)
	}
	if !strings.HasPrefix(string(plaintext), "GET / HTTP/1.1") {
		t.Fatalf("origin plaintext = %q, want HTTP/1.1 request", string(plaintext))
	}

	select {
	case conn := <-accepted:
		_ = conn.Close()
	case <-time.After(2 * time.Second):
		t.Fatal("expected direct dial to reach local listener")
	}
}

func TestHandleTLS_DoHProviderRoutesDirectAndPreservesClientHello(t *testing.T) {
	config.ResetGlobalConfigForTest()
	defer config.ResetGlobalConfigForTest()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen failed: %v", err)
	}
	defer ln.Close()

	accepted := make(chan net.Conn, 1)
	go func() {
		conn, acceptErr := ln.Accept()
		if acceptErr == nil {
			accepted <- conn
		}
	}()

	clientHello := []byte("client-hello")
	tlsHandler := &fakeMITMTLSHandler{
		extractSNI: "dns.google",
		extractBuf: clientHello,
	}
	handler := NewTCPConnectionHandler(NewDefaultProtocolDetector(), tlsHandler, nil, statistic.DefaultManager)
	metadata := &M.Metadata{
		Network: M.TCP,
		DstIP:   netip.MustParseAddr("127.0.0.1"),
		DstPort: uint16(ln.Addr().(*net.TCPAddr).Port),
	}

	remote, origin, err := handler.handleTLS(context.Background(), context.Background(), newFakeConn(nil), metadata)
	if err != nil {
		t.Fatalf("handleTLS failed: %v", err)
	}
	if remote == nil {
		t.Fatal("remote connection is nil")
	}
	defer remote.Close()
	if origin == nil {
		t.Fatal("origin connection is nil")
	}
	defer origin.Close()

	if metadata.HostName != "dns.google" {
		t.Fatalf("metadata HostName = %q, want dns.google", metadata.HostName)
	}
	if metadata.Route != "RouteDirect" {
		t.Fatalf("metadata Route = %q, want RouteDirect", metadata.Route)
	}

	buf := make([]byte, len(clientHello))
	if _, err := io.ReadFull(origin, buf); err != nil {
		t.Fatalf("failed to read preserved ClientHello: %v", err)
	}
	if !bytes.Equal(buf, clientHello) {
		t.Fatalf("preserved ClientHello = %q, want %q", string(buf), string(clientHello))
	}

	select {
	case conn := <-accepted:
		_ = conn.Close()
	case <-time.After(2 * time.Second):
		t.Fatal("expected DoH direct route to reach local listener")
	}
}
