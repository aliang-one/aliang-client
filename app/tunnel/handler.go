package tunnel

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"
)

type handler struct {
	deviceID  string
	verifier  *routeVerifier
	transport *http.Transport
}

func newHandler(deviceID string, verifier *routeVerifier) (*handler, error) {
	if strings.TrimSpace(deviceID) == "" || verifier == nil {
		return nil, errors.New("device ID and route verifier are required")
	}
	return &handler{
		deviceID: deviceID,
		verifier: verifier,
		transport: &http.Transport{
			Proxy:                 nil,
			DialContext:           secureDialContext(10 * time.Second),
			ForceAttemptHTTP2:     false,
			MaxIdleConns:          32,
			MaxIdleConnsPerHost:   8,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: time.Second,
		},
	}, nil
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		writeTunnelError(w, http.StatusMethodNotAllowed, "CONNECT is not supported")
		return
	}
	if upgrade := r.Header.Get("Upgrade"); upgrade != "" && !strings.EqualFold(upgrade, "websocket") {
		writeTunnelError(w, http.StatusBadRequest, "unsupported protocol upgrade")
		return
	}

	claims, err := h.verifier.verify(r.Header.Get("X-Aliang-Route"))
	if err != nil {
		writeTunnelError(w, http.StatusUnauthorized, "invalid route ticket")
		return
	}
	if claims.DeviceID != h.deviceID {
		writeTunnelError(w, http.StatusForbidden, "route ticket targets another device")
		return
	}
	if claims.UpstreamScheme != "http" || claims.HostPolicy != "rewrite" {
		writeTunnelError(w, http.StatusBadRequest, "unsupported route policy")
		return
	}
	if err := validateTarget(claims.TargetHost, claims.TargetPort); err != nil {
		writeTunnelError(w, http.StatusForbidden, "target rejected")
		return
	}

	target := &url.URL{Scheme: "http", Host: targetAddress(claims.TargetHost, claims.TargetPort)}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			stripInternalHeaders(pr.Out.Header)
			pr.Out.Host = target.Host
			rewriteOrigin(pr.Out.Header, target)
		},
		Transport:     h.transport,
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			writeTunnelError(w, http.StatusBadGateway, "local target unavailable")
		},
		ModifyResponse: func(response *http.Response) error {
			stripInternalHeaders(response.Header)
			return nil
		},
	}
	proxy.ServeHTTP(w, r)
}

func rewriteOrigin(header http.Header, target *url.URL) {
	rawOrigin := header.Get("Origin")
	if rawOrigin == "" {
		return
	}
	origin, err := url.Parse(rawOrigin)
	if err != nil || origin.Scheme == "" || origin.Host == "" {
		header.Del("Origin")
		return
	}
	origin.Scheme = target.Scheme
	origin.Host = target.Host
	header.Set("Origin", origin.String())
}

func stripInternalHeaders(header http.Header) {
	for key := range header {
		lower := strings.ToLower(key)
		if strings.HasPrefix(lower, "x-piko-") || strings.HasPrefix(lower, "x-aliang-") {
			header.Del(key)
		}
	}
}

func writeTunnelError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}
