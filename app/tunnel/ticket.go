package tunnel

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	routeTicketIssuer   = "aliang-tunnel-gateway"
	routeTicketAudience = "alianggate"
	// routeTicketLeeway absorbs gateway↔agent clock skew. Tickets are minted
	// per request with a short TTL; agent laptops commonly drift tens of
	// seconds, and a rejected ticket only surfaces as opaque 401s, so prefer
	// availability here. The residual replay window stays tiny because the
	// attacker must already sit on the piko request path.
	routeTicketLeeway = 30 * time.Second
)

type routeClaims struct {
	MappingID      string `json:"mapping_id"`
	DeviceID       string `json:"device_id"`
	TargetHost     string `json:"target_host"`
	TargetPort     int    `json:"target_port"`
	UpstreamScheme string `json:"upstream_scheme"`
	HostPolicy     string `json:"host_policy"`
	jwt.RegisteredClaims
}

type routeVerifier struct {
	publicKey ed25519.PublicKey
}

func newRouteVerifier(encodedPublicKey string) (*routeVerifier, error) {
	decoded, err := base64.StdEncoding.DecodeString(encodedPublicKey)
	if err != nil {
		return nil, fmt.Errorf("decode route public key: %w", err)
	}
	if len(decoded) != ed25519.PublicKeySize {
		return nil, errors.New("invalid Ed25519 route public key length")
	}
	return &routeVerifier{publicKey: ed25519.PublicKey(decoded)}, nil
}

func (v *routeVerifier) verify(raw string) (*routeClaims, error) {
	claims := &routeClaims{}
	token, err := jwt.ParseWithClaims(
		raw,
		claims,
		func(*jwt.Token) (any, error) { return v.publicKey, nil },
		jwt.WithValidMethods([]string{jwt.SigningMethodEdDSA.Alg()}),
		jwt.WithIssuer(routeTicketIssuer),
		jwt.WithAudience(routeTicketAudience),
		jwt.WithExpirationRequired(),
		jwt.WithLeeway(routeTicketLeeway),
	)
	if err != nil || !token.Valid {
		return nil, fmt.Errorf("verify route ticket: %w", err)
	}
	if claims.MappingID == "" || claims.DeviceID == "" || claims.TargetHost == "" || claims.TargetPort == 0 {
		return nil, errors.New("route ticket is missing required claims")
	}
	if claims.Subject != claims.MappingID {
		return nil, errors.New("route ticket subject does not match mapping")
	}
	return claims, nil
}
