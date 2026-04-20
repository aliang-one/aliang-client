package tcp

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"reflect"
	"strings"
	"syscall"
	"time"

	"aliang.one/nursorgate/common/logger"
	M "aliang.one/nursorgate/inbound/tun/metadata"
	cert_client "aliang.one/nursorgate/processor/cert/client"
	"aliang.one/nursorgate/processor/config"
	"aliang.one/nursorgate/processor/routing"
	tls_helper "aliang.one/nursorgate/processor/tls"
	watcher "aliang.one/nursorgate/processor/watcher"
)

// DefaultTLSHandler implements the TLSHandler interface.
// It handles SNI extraction, MITM certificate generation, and domain routing.
type DefaultTLSHandler struct {
	// Whether to enable cursor proxy (for request interception)
	cursorProxyEnabled bool
}

func applyContextReadDeadline(ctx context.Context, conn net.Conn) func() {
	if ctx == nil || !isUsableConn(conn) {
		return func() {}
	}

	deadline, ok := ctx.Deadline()
	if !ok {
		return func() {}
	}

	if err := conn.SetReadDeadline(deadline); err != nil {
		logger.Debug(fmt.Sprintf("failed to set read deadline from context: %v", err))
		return func() {}
	}

	return func() {
		if err := conn.SetReadDeadline(time.Time{}); err != nil && !isExpectedClientDisconnect(err) {
			logger.Debug(fmt.Sprintf("failed to clear read deadline from context: %v", err))
		}
	}
}

func isUsableConn(conn net.Conn) bool {
	if conn == nil {
		return false
	}

	value := reflect.ValueOf(conn)
	if value.Kind() == reflect.Pointer && value.IsNil() {
		return false
	}

	switch typed := conn.(type) {
	case *WrappedConn:
		return typed != nil && isUsableConn(typed.Conn)
	case *closeOnceConn:
		return typed != nil && isUsableConn(typed.Conn)
	case *WrappedConnWithTLS:
		return typed != nil && isUsableConn(typed.Conn)
	default:
		return true
	}
}

// NewDefaultTLSHandler creates a new TLS handler
func NewDefaultTLSHandler() *DefaultTLSHandler {
	return &DefaultTLSHandler{
		cursorProxyEnabled: watcher.IsCursorProxyEnabled,
	}
}

// ExtractSNI extracts the Server Name Indication from a TLS ClientHello.
// It delegates to processor/tls/tls_sni_helper.go which has the low-level parsing.
func (h *DefaultTLSHandler) ExtractSNI(ctx context.Context, conn net.Conn) (string, []byte, error) {
	clearDeadline := applyContextReadDeadline(ctx, conn)
	defer clearDeadline()

	// Use the existing SNI extraction from processor/tls
	serverName, buffer, err := tls_helper.ExtractSNI(conn)
	if err != nil {
		logger.Debug("SNI extraction failed: " + err.Error())
		return "", buffer, err
	}

	logger.Debug("Extracted SNI: " + serverName)
	return serverName, buffer, nil
}

// PerformMITM creates and performs a TLS handshake with intercepted certificate.
// Steps:
// 1. Generate/retrieve certificate for serverName
// 2. Create TLS config with certificate
// 3. Perform TLS handshake as server
// 4. Return the TLS connection
func (h *DefaultTLSHandler) PerformMITM(ctx context.Context, originConn net.Conn, serverName string) (net.Conn, error) {
	// Generate TLS config for MITM
	tlsConfig := cert_client.CreateTlsConfigForHost(serverName)
	if tlsConfig == nil {
		return nil, fmt.Errorf("failed to create TLS config for host: %s", serverName)
	}

	// Create TLS server connection
	tlsConn := tls.Server(originConn, tlsConfig)

	clearDeadline := applyContextReadDeadline(ctx, originConn)
	defer clearDeadline()

	// Perform TLS handshake
	if err := tlsConn.Handshake(); err != nil {
		localAddr := "unknown"
		if addr := originConn.LocalAddr(); addr != nil {
			localAddr = addr.String()
		}
		remoteAddr := "unknown"
		if addr := originConn.RemoteAddr(); addr != nil {
			remoteAddr = addr.String()
		}
		connID := "unknown"
		if metadataConn, ok := ctx.Value(tcpContextConnIDKey{}).(string); ok && strings.TrimSpace(metadataConn) != "" {
			connID = metadataConn
		}
		msg := fmt.Sprintf(
			"TLS MITM handshake with client failed conn_id=%s for %s: local=%s remote=%s err=%v",
			connID,
			serverName,
			localAddr,
			remoteAddr,
			err,
		)
		if isExpectedClientDisconnect(err) {
			logger.Debug(msg)
		} else {
			logger.Error(msg)
		}
		return nil, err
	}

	// Log successful handshake
	state := tlsConn.ConnectionState()
	connID := "unknown"
	if metadataConn, ok := ctx.Value(tcpContextConnIDKey{}).(string); ok && strings.TrimSpace(metadataConn) != "" {
		connID = metadataConn
	}
	logger.Debug(fmt.Sprintf("TLS handshake successful conn_id=%s for %s. Protocol: %s, Version: 0x%04x",
		connID, serverName, state.NegotiatedProtocol, state.Version))

	return tlsConn, nil
}

func isExpectedClientDisconnect(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
		return true
	}

	var opErr *net.OpError
	if errors.As(err, &opErr) {
		if isExpectedClientDisconnect(opErr.Err) {
			return true
		}
	}

	if errors.Is(err, os.ErrClosed) ||
		errors.Is(err, syscall.EPIPE) ||
		errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.ECONNABORTED) {
		return true
	}

	errText := strings.ToLower(err.Error())
	return strings.Contains(errText, "broken pipe") ||
		strings.Contains(errText, "connection reset by peer") ||
		strings.Contains(errText, "use of closed network connection")
}

func (h *DefaultTLSHandler) DetermineRoute(serverName string) ProxyRoute {
	metadata := &M.Metadata{HostName: serverName, DstPort: 443}
	route, _ := h.DetermineRouteWithContext(metadata)
	return route
}

// IsDoHProvider checks if a domain is a DNS-over-HTTPS provider.
// DoH providers typically offer DNS resolution over HTTPS which should not be intercepted.
func IsDoHProvider(domain string) bool {
	if domain == "" {
		return false
	}

	domain = strings.ToLower(domain)

	// List of known DoH providers
	dohProviders := []string{
		DoHProviderGoogle,
		DoHProviderCloudflare,
		DoHProviderOpenDNS,
		DoHProviderQuad9,
		DoHProviderCleanBrowse,
		DoHProviderGoogle8,
		DoHProviderGoogle9,
		DoHProviderCloudflare1,
		DoHProviderCloudflare2,
		DoHProviderQuad9ip,
	}

	for _, provider := range dohProviders {
		if strings.Contains(domain, provider) {
			return true
		}
	}

	return false
}

// DetectDoH detects if an established TLS connection is being used for DNS-over-HTTPS.
// This is useful because some applications establish HTTPS connections that are actually
// used for DNS queries. The HTTP/2 PRI frame or HTTP request will contain "/dns-query" or "/resolve".
func DetectDoH(tlsConn net.Conn) bool {
	// Try to peek at the first few bytes to detect HTTP request
	// This is a simple heuristic check
	if tc, ok := tlsConn.(interface{ Peek(int) ([]byte, error) }); ok {
		data, err := tc.Peek(256)
		if err == nil {
			dataStr := string(data)
			if strings.Contains(dataStr, "/dns-query") || strings.Contains(dataStr, "/resolve") {
				return true
			}
		}
	}

	return false
}

// TLSConnectionInfo contains information about an established TLS connection
type TLSConnectionInfo struct {
	ServerName        string
	Protocol          string
	Version           uint16
	CipherSuite       uint16
	IsResumed         bool
	VerificationError error
}

// GetTLSConnectionInfo extracts information from an established TLS connection
func GetTLSConnectionInfo(tlsConn *tls.Conn) *TLSConnectionInfo {
	state := tlsConn.ConnectionState()
	return &TLSConnectionInfo{
		ServerName:  state.ServerName,
		Protocol:    state.NegotiatedProtocol,
		Version:     state.Version,
		CipherSuite: state.CipherSuite,
		IsResumed:   state.DidResume,
	}
}

// WrappedConnWithTLS combines an origin connection with TLS metadata
// for easier handling of SNI-extracted connections
type WrappedConnWithTLS struct {
	net.Conn
	SNI    string // Extracted Server Name Indication
	Buffer []byte // Preserved TLS ClientHello
}

func (h *DefaultTLSHandler) DetermineRouteWithContext(metadata *M.Metadata) (ProxyRoute, bool) {
	if metadata == nil {
		return h.defaultFallbackRoute(), false
	}

	if shouldForceAliangRoute(metadata) {
		logger.Debug(fmt.Sprintf("Route override: forcing aliang for local proxy target %s", metadata.DestinationAddress()))
		return RouteToALiang, false
	}

	route := h.decideRouteWithRoutingEngine(metadata)
	requiresSNI := metadata.DstPort == 443 && metadata.HostName == ""

	logger.Debug(fmt.Sprintf("Route decision: %v (requiresSNI: %v)", route, requiresSNI))

	return route, requiresSNI
}

func (h *DefaultTLSHandler) decideRouteWithRoutingEngine(metadata *M.Metadata) ProxyRoute {
	if metadata == nil {
		return h.defaultFallbackRoute()
	}

	cfg := config.GetGlobalConfig()
	if cfg == nil {
		return h.defaultFallbackRoute()
	}

	switchStatus := routing.GetSwitchManager().GetStatus()
	snapshot, err := routing.CompileRuntimeSnapshotFromRuntimeInputs(cfg, switchStatus)
	if err != nil {
		logger.Warn(fmt.Sprintf("CompileRuntimeSnapshotFromRuntimeInputs failed, fallback route used: %v", err))
		return h.defaultFallbackRoute()
	}

	routeCtx := &routing.MatchContext{
		Domain: strings.ToLower(strings.TrimSpace(metadata.HostName)),
	}
	if metadata.DstIP.IsValid() && !metadata.DstIP.IsUnspecified() {
		routeCtx.IP = metadata.DstIP.String()
	}

	decision, err := routing.DecideRouteFromSnapshot(snapshot, routeCtx)
	if err != nil {
		logger.Warn(fmt.Sprintf("routing.DecideRouteFromSnapshot failed, fallback route used: %v", err))
		return h.defaultFallbackRoute()
	}

	switch decision {
	case routing.RouteToAliang:
		return RouteToALiang
	case routing.RouteToSocks:
		return RouteToLocalProxy
	default:
		return h.defaultFallbackRoute()
	}
}

func (h *DefaultTLSHandler) defaultFallbackRoute() ProxyRoute {
	cfg := config.GetGlobalConfig()
	switchStatus := routing.GetSwitchManager().GetStatus()
	if cfg != nil && cfg.EffectiveDefaultProxy() == "socks" && switchStatus.SocksEnabled {
		return RouteToLocalProxy
	}
	return RouteDirect
}
