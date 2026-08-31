package tunnel

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func signTicketWithExpiry(t *testing.T, privateKey ed25519.PrivateKey, exp time.Time) string {
	t.Helper()
	now := time.Now()
	claims := routeClaims{
		MappingID: "pm_test", DeviceID: "dev_test", TargetHost: "127.0.0.1",
		TargetPort: 8080, UpstreamScheme: "http", HostPolicy: "rewrite",
	}
	claims.RegisteredClaims = jwt.RegisteredClaims{
		Issuer:    routeTicketIssuer,
		Subject:   "pm_test",
		Audience:  jwt.ClaimStrings{routeTicketAudience},
		IssuedAt:  jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(exp),
	}
	raw, err := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims).SignedString(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestVerifierAcceptsModerateClockSkew(t *testing.T) {
	t.Parallel()
	publicKey, privateKey, _ := ed25519.GenerateKey(rand.Reader)
	verifier, err := newRouteVerifier(base64.StdEncoding.EncodeToString(publicKey))
	if err != nil {
		t.Fatal(err)
	}
	// Expired 20s ago on the signer's clock — must stay valid within the 30s
	// leeway so drifting agent laptops keep working.
	ticket := signTicketWithExpiry(t, privateKey, time.Now().Add(-20*time.Second))
	if _, err := verifier.verify(ticket); err != nil {
		t.Fatalf("expected skewed ticket to verify, got %v", err)
	}
}

func TestVerifierRejectsStaleTicketBeyondLeeway(t *testing.T) {
	t.Parallel()
	publicKey, privateKey, _ := ed25519.GenerateKey(rand.Reader)
	verifier, _ := newRouteVerifier(base64.StdEncoding.EncodeToString(publicKey))
	ticket := signTicketWithExpiry(t, privateKey, time.Now().Add(-routeTicketLeeway-time.Second))
	if _, err := verifier.verify(ticket); err == nil {
		t.Fatal("expected ticket expired beyond leeway to be rejected")
	}
}
