package tunnel

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"
)

func TestHandlerForwardsAuthorizedRequestAndRewritesHost(t *testing.T) {
	t.Parallel()
	received := make(chan *http.Request, 1)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received <- r.Clone(r.Context())
		_, _ = w.Write([]byte("ok"))
	}))
	defer target.Close()
	host, rawPort, _ := net.SplitHostPort(strings.TrimPrefix(target.URL, "http://"))
	port, _ := strconv.Atoi(rawPort)

	publicKey, privateKey, _ := ed25519.GenerateKey(rand.Reader)
	verifier, err := newRouteVerifier(base64.StdEncoding.EncodeToString(publicKey))
	if err != nil {
		t.Fatal(err)
	}
	h, err := newHandler("dev_test", verifier, true)
	if err != nil {
		t.Fatal(err)
	}
	ticket := signRouteTicket(t, privateKey, routeClaims{
		MappingID: "pm_test", DeviceID: "dev_test", TargetHost: host,
		TargetPort: port, UpstreamScheme: "http", HostPolicy: "rewrite",
	})

	request := httptest.NewRequest(http.MethodGet, "https://public.example/hello", nil)
	request.Header.Set("X-Aliang-Route", ticket)
	request.Header.Set("X-Piko-Endpoint", "dev_test")
	request.Header.Set("Origin", "https://public.example")
	recorder := httptest.NewRecorder()
	h.ServeHTTP(recorder, request)
	response := recorder.Result()
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || string(body) != "ok" {
		t.Fatalf("unexpected response: %d %q", response.StatusCode, body)
	}

	forwarded := <-received
	if forwarded.Host != net.JoinHostPort(host, rawPort) {
		t.Fatalf("target received Host %q", forwarded.Host)
	}
	if forwarded.Header.Get("Origin") != "http://"+net.JoinHostPort(host, rawPort) {
		t.Fatalf("target received Origin %q", forwarded.Header.Get("Origin"))
	}
	if forwarded.Header.Get("X-Aliang-Route") != "" || forwarded.Header.Get("X-Piko-Endpoint") != "" {
		t.Fatal("internal routing headers reached the target")
	}
}

func TestHandlerRejectsTicketForAnotherDevice(t *testing.T) {
	t.Parallel()
	publicKey, privateKey, _ := ed25519.GenerateKey(rand.Reader)
	verifier, _ := newRouteVerifier(base64.StdEncoding.EncodeToString(publicKey))
	h, _ := newHandler("dev_a", verifier, true)
	ticket := signRouteTicket(t, privateKey, routeClaims{
		MappingID: "pm_test", DeviceID: "dev_b", TargetHost: "127.0.0.1",
		TargetPort: 3000, UpstreamScheme: "http", HostPolicy: "rewrite",
	})
	request := httptest.NewRequest(http.MethodGet, "https://public.example/", nil)
	request.Header.Set("X-Aliang-Route", ticket)
	recorder := httptest.NewRecorder()
	h.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", recorder.Code)
	}
}

func TestHandlerPassesThroughWebSocketUpgrade(t *testing.T) {
	t.Parallel()
	upgrader := websocket.Upgrader{}
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			messageType, message, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if err := conn.WriteMessage(messageType, append([]byte("echo:"), message...)); err != nil {
				return
			}
		}
	}))
	defer target.Close()
	host, rawPort, _ := net.SplitHostPort(strings.TrimPrefix(target.URL, "http://"))
	port, _ := strconv.Atoi(rawPort)

	publicKey, privateKey, _ := ed25519.GenerateKey(rand.Reader)
	verifier, err := newRouteVerifier(base64.StdEncoding.EncodeToString(publicKey))
	if err != nil {
		t.Fatal(err)
	}
	h, err := newHandler("dev_test", verifier, true)
	if err != nil {
		t.Fatal(err)
	}
	ticket := signRouteTicket(t, privateKey, routeClaims{
		MappingID: "pm_test", DeviceID: "dev_test", TargetHost: host,
		TargetPort: port, UpstreamScheme: "http", HostPolicy: "rewrite",
	})

	// A live listener is mandatory: hijacked upgrade streams cannot run
	// through httptest.NewRecorder, and end-to-end byte pumping is exactly
	// what this test exists to prove.
	public := httptest.NewServer(h)
	defer public.Close()

	header := http.Header{}
	header.Set("X-Aliang-Route", ticket)
	header.Set("X-Piko-Endpoint", "dev_test")
	dialer := websocket.Dialer{HandshakeTimeout: 5 * time.Second}
	conn, response, err := dialer.Dial("ws"+strings.TrimPrefix(public.URL, "http")+"/ws", header)
	if err != nil {
		t.Fatalf("websocket dial: %v (status %v)", err, response)
	}
	defer conn.Close()
	if err := conn.WriteMessage(websocket.TextMessage, []byte("ping")); err != nil {
		t.Fatal(err)
	}
	messageType, message, err := conn.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if messageType != websocket.TextMessage || string(message) != "echo:ping" {
		t.Fatalf("unexpected echo: type=%d body=%q", messageType, message)
	}
}

func signRouteTicket(t *testing.T, privateKey ed25519.PrivateKey, claims routeClaims) string {
	t.Helper()
	now := time.Now()
	claims.RegisteredClaims = jwt.RegisteredClaims{
		Issuer:    routeTicketIssuer,
		Subject:   claims.MappingID,
		Audience:  jwt.ClaimStrings{routeTicketAudience},
		IssuedAt:  jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(time.Minute)),
	}
	raw, err := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims).SignedString(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestHandlerRejectsPrivateTargetWhenDisabled(t *testing.T) {
	t.Parallel()
	publicKey, privateKey, _ := ed25519.GenerateKey(rand.Reader)
	verifier, _ := newRouteVerifier(base64.StdEncoding.EncodeToString(publicKey))
	h, err := newHandler("dev_test", verifier, false)
	if err != nil {
		t.Fatal(err)
	}
	ticket := signRouteTicket(t, privateKey, routeClaims{
		MappingID: "pm_test", DeviceID: "dev_test", TargetHost: "192.168.1.20",
		TargetPort: 5000, UpstreamScheme: "http", HostPolicy: "rewrite",
	})
	request := httptest.NewRequest(http.MethodGet, "https://public.example/", nil)
	request.Header.Set("X-Aliang-Route", ticket)
	recorder := httptest.NewRecorder()
	h.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for private target with allowPrivate=false, got %d", recorder.Code)
	}
}
