package tcp

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/netip"
	"net/http"
	"net/http/httptest"
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
		mu      sync.Mutex
		bodies  [][]byte
		recvCh  = make(chan []byte, 8)
		server  = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
