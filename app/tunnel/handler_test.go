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
	h, err := newHandler("dev_test", verifier)
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
	h, _ := newHandler("dev_a", verifier)
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
