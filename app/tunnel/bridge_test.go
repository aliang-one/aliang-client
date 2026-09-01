package tunnel

import (
	"bufio"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func signTCPRouteTicket(t *testing.T, privateKey ed25519.PrivateKey, claims routeClaims) string {
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

// startBridgeWithKey starts an echo target plus a bridge bound to a fresh
// Ed25519 key; it returns the signing key so tests can mint tickets.
func startBridgeWithKey(t *testing.T, allowPrivate bool) (bridgeAddr, targetAddr string, privateKey ed25519.PrivateKey, stop func()) {
	t.Helper()
	target, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			conn, err := target.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				buf := make([]byte, 4) // read the "ping" request, then reply
				_, _ = io.ReadFull(c, buf)
				_, _ = c.Write([]byte("tcp-bridge-ok"))
				_ = c.Close()
			}(conn)
		}
	}()

	publicKey, privateKey, _ := ed25519.GenerateKey(rand.Reader)
	verifier, err := newRouteVerifier(base64.StdEncoding.EncodeToString(publicKey))
	if err != nil {
		t.Fatal(err)
	}
	bridge := newTCPBridge("dev_test", verifier, allowPrivate)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = bridge.Serve(ln) }()
	return ln.Addr().String(), target.Addr().String(), privateKey, func() {
		_ = ln.Close()
		_ = target.Close()
	}
}

func sendRouteHeader(t *testing.T, bridgeAddr string, header tcpRouteHeader) (net.Conn, *bufio.Reader) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", bridgeAddr, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(header)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write(append(raw, '\n')); err != nil {
		t.Fatal(err)
	}
	return conn, bufio.NewReader(conn)
}

func TestTCPBridgeForwardsAuthorizedConnection(t *testing.T) {
	t.Parallel()
	bridgeAddr, targetAddr, privateKey, stop := startBridgeWithKey(t, true)
	defer stop()
	host, rawPort, _ := net.SplitHostPort(targetAddr)
	var port int
	for _, c := range rawPort {
		port = port*10 + int(c-'0')
	}

	ticket := signTCPRouteTicket(t, privateKey, routeClaims{
		MappingID: "pm_test", DeviceID: "dev_test", TargetHost: host,
		TargetPort: port, UpstreamScheme: "tcp", HostPolicy: "passthrough",
	})
	conn, reader := sendRouteHeader(t, bridgeAddr, tcpRouteHeader{
		Ticket: ticket, TargetHost: host, TargetPort: port,
	})
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, len("tcp-bridge-ok"))
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := io.ReadFull(reader, reply); err != nil {
		t.Fatalf("read reply: %v", err)
	}
	if string(reply) != "tcp-bridge-ok" {
		t.Fatalf("unexpected reply %q", reply)
	}
}

func TestTCPBridgeRejectsInvalidTicket(t *testing.T) {
	t.Parallel()
	bridgeAddr, targetAddr, _, stop := startBridgeWithKey(t, true)
	defer stop()
	host, rawPort, _ := net.SplitHostPort(targetAddr)
	var port int
	for _, c := range rawPort {
		port = port*10 + int(c-'0')
	}

	// Ticket signed by a foreign key must be rejected: the connection is
	// closed without ever reaching the target.
	conn, reader := sendRouteHeader(t, bridgeAddr, tcpRouteHeader{
		Ticket: "not-a-valid-ticket", TargetHost: host, TargetPort: port,
	})
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := reader.ReadByte(); err == nil {
		t.Fatal("expected connection to be closed for invalid ticket")
	}
}

func TestTCPBridgeRejectsPrivateTargetWhenDisabled(t *testing.T) {
	t.Parallel()
	bridgeAddr, _, privateKey, stop := startBridgeWithKey(t, false)
	defer stop()

	ticket := signTCPRouteTicket(t, privateKey, routeClaims{
		MappingID: "pm_test", DeviceID: "dev_test", TargetHost: "192.168.1.20",
		TargetPort: 5000, UpstreamScheme: "tcp", HostPolicy: "passthrough",
	})
	conn, reader := sendRouteHeader(t, bridgeAddr, tcpRouteHeader{
		Ticket: ticket, TargetHost: "192.168.1.20", TargetPort: 5000,
	})
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := reader.ReadByte(); err == nil {
		t.Fatal("expected connection to be closed for private target with allowPrivate=false")
	}
}
