package tcp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"aliang.one/nursorgate/common/logger"
	"aliang.one/nursorgate/inbound/tun/dialer"
	M "aliang.one/nursorgate/inbound/tun/metadata"
	"aliang.one/nursorgate/outbound"
	outboundproxy "aliang.one/nursorgate/outbound/proxy"
	cachepkg "aliang.one/nursorgate/processor/cache"
	"aliang.one/nursorgate/processor/config"
	"aliang.one/nursorgate/processor/mirror"
	"aliang.one/nursorgate/processor/rules"
	"aliang.one/nursorgate/processor/statistic"
	watcher "aliang.one/nursorgate/processor/watcher"
	"golang.org/x/net/http2"
)

var reverseLookupAddr = func(ctx context.Context, addr string) ([]string, error) {
	return net.DefaultResolver.LookupAddr(ctx, addr)
}

const (
	applicationPrefetchInitialTimeout = 200 * time.Millisecond
	applicationPrefetchRetryTimeout   = 50 * time.Millisecond
	applicationPrefetchMaxBytes       = 8192
)

const (
	AppProtoUnknown = "unknown"
	AppProtoHTTP1   = "http1"
	AppProtoHTTP2   = "http2"
)

const (
	DenyReasonToAliangDisabled     = "toAliang_disabled"
	DenyReasonToAliangUnavailable  = "toAliang_unavailable"
	DenyReasonToSocksDisabled      = "toSocks_disabled"
	DenyReasonToSocksMisconfigured = "toSocks_misconfigured"
	DenyReasonToSocksUnavailable   = "toSocks_unavailable"
	DenyReasonToSocksUnsupported   = "toSocks_unsupported_upstream_type"
	toSocksUpstreamTypeSocks       = "socks"
	toSocksUpstreamTypeHTTP        = "http"
	toSocksBranchName              = "toSocks"
	toAliangBranchName             = "toAliang"
)

var tcpConnIDCounter uint64

type tcpContextConnIDKey struct{}

type BranchDenyError struct {
	Branch string
	Reason string
	Cause  error
}

func (e *BranchDenyError) Error() string {
	if e == nil {
		return "route denied"
	}
	if e.Cause == nil {
		return fmt.Sprintf("route denied: branch=%s reason=%s", e.Branch, e.Reason)
	}
	return fmt.Sprintf("route denied: branch=%s reason=%s: %v", e.Branch, e.Reason, e.Cause)
}

func (e *BranchDenyError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func newBranchDenyError(branch, reason string, cause error) error {
	return &BranchDenyError{Branch: branch, Reason: reason, Cause: cause}
}

func IsBranchDenyError(err error) bool {
	var denyErr *BranchDenyError
	return errors.As(err, &denyErr)
}

func BranchDenyReason(err error) string {
	var denyErr *BranchDenyError
	if errors.As(err, &denyErr) {
		return denyErr.Reason
	}
	return ""
}

// parseNetIPAddr converts a string IP to netip.Addr
func parseNetIPAddr(ipStr string) (netip.Addr, error) {
	return netip.ParseAddr(ipStr)
}

// TCPConnectionHandler implements the TCPConnHandler interface.
// It orchestrates TCP connection handling from protocol detection
// through routing and bidirectional data relay.
type TCPConnectionHandler struct {
	protocolDetector ProtocolDetector
	tlsHandler       TLSHandler
	relayManager     RelayManager
	statsManager     *statistic.Manager
}

// NewTCPConnectionHandler creates a new TCP handler
func NewTCPConnectionHandler(
	protocolDetector ProtocolDetector,
	tlsHandler TLSHandler,
	relayManager RelayManager,
	statsManager *statistic.Manager,
) *TCPConnectionHandler {
	return &TCPConnectionHandler{
		protocolDetector: protocolDetector,
		tlsHandler:       tlsHandler,
		relayManager:     relayManager,
		statsManager:     statsManager,
	}
}

func logObservedTLSServerName(metadata *M.Metadata, source string) {
	if metadata == nil || metadata.HostName == "" {
		return
	}

	sourceAddr := metadata.SourceAddress()
	if !metadata.SrcIP.IsValid() || metadata.SrcIP.IsUnspecified() {
		sourceAddr = fmt.Sprintf("unknown:%d", metadata.SrcPort)
	}

	destIP := metadata.DstIP.String()
	if !metadata.DstIP.IsValid() || metadata.DstIP.IsUnspecified() {
		destIP = "unknown"
	}

	logger.Debug(fmt.Sprintf(
		"[TUN TLS] conn_id=%s observed server_name=%s source=%s src=%s dst=%s:%d",
		metadata.ConnID,
		metadata.HostName,
		source,
		sourceAddr,
		destIP,
		metadata.DstPort,
	))
}

func logAliangGateProxy(metadata *M.Metadata, routeSource string) {
	if metadata == nil || metadata.HostName == "" {
		return
	}

	destIP := metadata.DstIP.String()
	if !metadata.DstIP.IsValid() || metadata.DstIP.IsUnspecified() {
		destIP = "unknown"
	}

	logger.Debug(fmt.Sprintf(
		"[AliangGate] conn_id=%s proxying server_name=%s route_source=%s app_proto=%s dst=%s:%d final_route=%s",
		metadata.ConnID,
		metadata.HostName,
		routeSource,
		metadata.AppProto,
		destIP,
		metadata.DstPort,
		metadata.Route,
	))
}

func detectApplicationProtocol(prefetched []byte) string {
	if len(prefetched) == 0 {
		return AppProtoUnknown
	}

	if len(prefetched) >= len(http2.ClientPreface) && string(prefetched[:len(http2.ClientPreface)]) == http2.ClientPreface {
		return AppProtoHTTP2
	}

	httpMethods := [][]byte{
		[]byte("GET "),
		[]byte("POST "),
		[]byte("PUT "),
		[]byte("HEAD "),
		[]byte("PATCH "),
		[]byte("DELETE "),
		[]byte("OPTIONS "),
		[]byte("CONNECT "),
		[]byte("TRACE "),
	}
	for _, method := range httpMethods {
		if len(prefetched) >= len(method) && bytes.Equal(prefetched[:len(method)], method) {
			return AppProtoHTTP1
		}
	}

	return AppProtoUnknown
}

func ensureTCPConnID(metadata *M.Metadata) string {
	if metadata == nil {
		return "unknown"
	}
	if strings.TrimSpace(metadata.ConnID) != "" {
		return metadata.ConnID
	}
	metadata.ConnID = fmt.Sprintf("tcp-%d", atomic.AddUint64(&tcpConnIDCounter, 1))
	return metadata.ConnID
}

func selectUniqueCachedDomainEntry(entries []*cachepkg.CacheEntry) (*cachepkg.CacheEntry, int) {
	if len(entries) == 0 {
		return nil, 0
	}

	unique := make(map[string]*cachepkg.CacheEntry, len(entries))
	for _, entry := range entries {
		if entry == nil || entry.Domain == "" {
			continue
		}
		if _, exists := unique[entry.Domain]; !exists {
			unique[entry.Domain] = entry
		}
	}

	if len(unique) != 1 {
		return nil, len(unique)
	}

	for _, entry := range unique {
		return entry, 1
	}

	return nil, 0
}

func setMetadataHostFromObservedSNI(metadata *M.Metadata, sni string) {
	if metadata == nil || sni == "" {
		return
	}
	metadata.SetHostName(sni, M.BindingSourceSNI, M.DefaultSNITTL)
	logObservedTLSServerName(metadata, "sni")
}

func prepareTCPConnectionContexts(ctx context.Context, connID string) (context.Context, context.Context, context.CancelFunc) {
	sessionCtx := context.WithValue(ctx, tcpContextConnIDKey{}, connID)

	timeout := time.Duration(DefaultTCPConnectTimeout) * time.Second
	if deadline, ok := sessionCtx.Deadline(); ok {
		timeout = time.Until(deadline)
	}

	connectCtx, cancel := context.WithTimeout(sessionCtx, timeout)
	return sessionCtx, connectCtx, cancel
}

// Handle processes a single TCP connection.
// It is the main orchestration entry point.
func (h *TCPConnectionHandler) Handle(ctx context.Context, originConn net.Conn, metadata *M.Metadata) error {
	originConn = wrapCloseOnceConn(originConn)

	// Ensure we close the origin connection when done
	defer originConn.Close()
	connID := ensureTCPConnID(metadata)
	sessionCtx, connectCtx, cancel := prepareTCPConnectionContexts(ctx, connID)
	defer cancel()

	// Log connection arrival at INFO level for business tracking
	logger.Info(fmt.Sprintf("[TCP] New connection from %s -> %s:%d",
		metadata.SourceAddress(), metadata.DstIP, metadata.DstPort))

	// Create timeout context (使用父 context 而非 Background)
	// Detect protocol
	protocol := h.protocolDetector.Detect(metadata.DstPort)
	logger.Debug(fmt.Sprintf("TCP: conn_id=%s handling %v connection to %s:%d src=%s",
		connID, protocol, metadata.DstIP, metadata.DstPort, metadata.SourceAddress()))

	var remoteConn net.Conn
	var newOriginConn net.Conn = originConn
	var err error

	// Route based on protocol
	switch protocol {
	case ProtocolTLS:
		remoteConn, newOriginConn, err = h.handleTLS(sessionCtx, connectCtx, originConn, metadata)
	case ProtocolHTTP:
		remoteConn, newOriginConn, err = h.handleNonTLS(connectCtx, originConn, metadata)
	default:
		remoteConn, newOriginConn, err = h.handleNonTLS(connectCtx, originConn, metadata)
	}

	if err != nil {
		msg := fmt.Sprintf("[TCP HANDLER] conn_id=%s ❌ 处理失败 - 域名:%s, 端口:%d, 错误:%v", connID, metadata.HostName, metadata.DstPort, err)
		if isExpectedClientDisconnect(err) {
			logger.Debug(msg)
		} else {
			logger.Error(msg)
		}
		return err
	}

	if remoteConn == nil {
		// Special handling (e.g., DoH)
		logger.Info(fmt.Sprintf("[TCP] Connection handled specially (no relay) for %s", metadata.HostName))
		return nil
	}

	// Update metadata MidIP/MidPort
	if localAddr, ok := remoteConn.LocalAddr().(*net.TCPAddr); ok {
		if ip, err := parseNetIPAddr(localAddr.IP.String()); err == nil {
			metadata.MidIP = ip
			metadata.MidPort = uint16(localAddr.Port)
		}
	}

	// Log successful connection establishment at INFO level
	logger.Info(fmt.Sprintf("[TCP] conn_id=%s Connection established: %s -> %s:%d via %s (proto=%s)",
		connID, metadata.SourceAddress(), metadata.DstIP, metadata.DstPort, metadata.Route, metadata.AppProto))
	statistic.GetDefaultAIActivityTracker().RecordMetadata(metadata)

	// Track statistics
	trackedConn := wrapCloseOnceConn(remoteConn)
	if metadata != nil && metadata.Route == "RouteToALiang" {
		trackedConn = wrapCloseWithPeer(trackedConn, newOriginConn)
	}
	trackedRemote := statistic.NewTCPTracker(trackedConn, metadata, h.statsManager)
	defer trackedRemote.Close()

	// Mirror wrapping is enabled whenever traffic mirror is configured,
	// regardless of the final route.
	var mirrorFlow *mirror.Flow
	if metadata != nil {
		mirrorFlow = mirror.NewMirrorFlow(metadata)
	}
	if mirrorFlow != nil {
		newOriginConn = mirrorFlow.WrapConn(newOriginConn, mirror.DirectionRequest)
		trackedRemote = mirrorFlow.WrapConn(trackedRemote, mirror.DirectionResponse)
		mirrorFlow.EmitStart()
		defer mirrorFlow.Close()
	}

	if metadata != nil && metadata.Route == "RouteToALiang" && metadata.AppProto == AppProtoHTTP1 {
		http1RelayStats, relayErr := watcher.RelayHTTP1(sessionCtx, newOriginConn, trackedRemote)
		statistic.GetDefaultAIActivityTracker().CompleteMetadata(metadata, http1RelayStats.CompletedAt)
		if relayErr == nil {
			statistic.GetDefaultHTTPStatsCollector().RecordConnection(
				metadata,
				http1RelayStats.RequestPayload,
				http1RelayStats.ResponsePayload,
				http1RelayStats.ClientToServerByte,
				http1RelayStats.ServerToClientByte,
				http1RelayStats.StartedAt,
				http1RelayStats.FirstResponseAt,
				http1RelayStats.CompletedAt,
			)
		}

		if relayErr == nil && metadata.DNSInfo != nil && metadata.DNSInfo.ShouldCache {
			engine := rules.GetEngine()
			if engine != nil {
				engine.StoreBinding(metadata)
			}
		}
		if relayErr != nil {
			logger.Debug(fmt.Sprintf("TCP: conn_id=%s http1 relay finished with err=%v", connID, relayErr))
		} else {
			logger.Debug(fmt.Sprintf("TCP: conn_id=%s http1 relay completed bytes_up=%d bytes_down=%d",
				connID, http1RelayStats.ClientToServerByte, http1RelayStats.ServerToClientByte))
		}
		if mirrorFlow != nil {
			mirrorFlow.EmitEnd(
				http1RelayStats.ClientToServerByte,
				http1RelayStats.ServerToClientByte,
				relayErr,
			)
		}
		return relayErr
	}

	// Relay data bidirectionally
	relayStats, err := h.relayManager.Relay(sessionCtx, newOriginConn, trackedRemote, metadata)
	statistic.GetDefaultAIActivityTracker().CompleteMetadata(metadata, relayStats.CompletedAt)
	if err == nil {
		// Log successful relay completion at INFO level
		logger.Info(fmt.Sprintf("[TCP] Relay completed: %s -> %s:%d (sent=%dKB, recv=%dKB)",
			metadata.SourceAddress(), metadata.DstIP, metadata.DstPort,
			relayStats.ClientToServerByte/1024, relayStats.ServerToClientByte/1024))

		statistic.GetDefaultHTTPStatsCollector().RecordConnection(
			metadata,
			relayStats.RequestPayload,
			relayStats.ResponsePayload,
			relayStats.ClientToServerByte,
			relayStats.ServerToClientByte,
			relayStats.StartedAt,
			relayStats.FirstResponseAt,
			relayStats.CompletedAt,
		)
	} else {
		// Log relay failure at WARN level with context
		logger.Warn(fmt.Sprintf("[TCP] Relay failed: %s -> %s:%d, error: %v",
			metadata.SourceAddress(), metadata.DstIP, metadata.DstPort, err))
	}
	if err != nil {
		logger.Debug(fmt.Sprintf("TCP: conn_id=%s relay finished with err=%v bytes_up=%d bytes_down=%d",
			connID, err, relayStats.ClientToServerByte, relayStats.ServerToClientByte))
	} else {
		logger.Debug(fmt.Sprintf("TCP: conn_id=%s relay completed bytes_up=%d bytes_down=%d first_response=%s",
			connID, relayStats.ClientToServerByte, relayStats.ServerToClientByte, relayStats.FirstResponseAt.Format(time.RFC3339Nano)))
	}

	if mirrorFlow != nil {
		mirrorFlow.EmitEnd(
			relayStats.ClientToServerByte,
			relayStats.ServerToClientByte,
			err,
		)
	}

	// Store DNS binding to cache after successful relay
	// This persists domain-IP relationships for future cache hits
	if err == nil && metadata.DNSInfo != nil && metadata.DNSInfo.ShouldCache {
		engine := rules.GetEngine()
		if engine != nil {
			engine.StoreBinding(metadata)
		}
	}

	return err
}

func (h *TCPConnectionHandler) handleNonTLS(
	ctx context.Context,
	originConn net.Conn,
	metadata *M.Metadata,
) (remoteConn net.Conn, newOriginConn net.Conn, err error) {
	if shouldForceAliangRoute(metadata) {
		metadata.Route = "RouteToALiang"
		remote, dialErr := h.dialByRoute(ctx, metadata, RouteToALiang)
		if dialErr != nil {
			return nil, originConn, dialErr
		}
		return remote, h.wrapAliangHTTPConnByProto(originConn, metadata.AppProto), nil
	}

	if metadata != nil && metadata.DstIP.IsValid() && !metadata.DstIP.IsUnspecified() &&
		(IsLoopbackIP(metadata.DstIP) || IsPrivateIP(metadata.DstIP)) {
		metadata.Route = "RouteDirect"
		remote, dialErr := h.dialDirect(ctx, metadata)
		return remote, originConn, dialErr
	}

	bufferedData, sniffErr := prefetchApplicationData(ctx, originConn, applicationPrefetchMaxBytes)
	if sniffErr != nil {
		logger.Debug(fmt.Sprintf("non-TLS prefetch failed: %v", sniffErr))
	}

	newOriginConn = wrapBufferedConn(originConn, bufferedData)

	host, bindingSource, isHTTP := extractHTTPRoutingHost(bufferedData)
	if isHTTP {
		metadata.AppProto = AppProtoHTTP1
		if host != "" {
			ttl := M.DefaultHTTPTTL
			if bindingSource == M.BindingSourceCONNECT {
				ttl = M.DefaultCONNECTTTL
			}
			metadata.SetHostName(host, bindingSource, ttl)
		}

		route, _ := h.tlsHandler.DetermineRouteWithContext(metadata)
		logger.Debug(fmt.Sprintf("HTTP: Route decision for host=%s ip=%s: %v", metadata.HostName, metadata.DstIP, route))
		remote, dialErr := h.dialByRoute(ctx, metadata, route)
		if dialErr != nil {
			return nil, newOriginConn, dialErr
		}
		if route == RouteToALiang {
			return remote, h.wrapAliangHTTPConnByProto(newOriginConn, metadata.AppProto), nil
		}
		return remote, newOriginConn, nil
	}

	h.enrichMetadataFromReverseLookup(ctx, metadata)
	route, _ := h.tlsHandler.DetermineRouteWithContext(metadata)
	logger.Debug(fmt.Sprintf("TCP: Route decision for host=%s ip=%s (non-HTTP payload): %v", metadata.HostName, metadata.DstIP, route))
	remote, dialErr := h.dialByRoute(ctx, metadata, route)
	return remote, newOriginConn, dialErr
}

// handleTLS processes TLS connections (port 443).
// Transparent TLS interception only trusts identifiers observed on the current
// connection. If we can't read an explicit hostname or SNI, we bypass MITM and
// route direct to avoid certificate and HTTP/2 mismatches on shared IPs.
func (h *TCPConnectionHandler) handleTLS(
	sessionCtx context.Context,
	connectCtx context.Context,
	originConn net.Conn,
	metadata *M.Metadata,
) (remoteConn net.Conn, newOriginConn net.Conn, err error) {
	var sni string
	var sniBuf []byte
	var wrapped *WrappedConn
	var cacheFallbackUsed bool

	// STEP 1: Prefer an explicit hostname (for example CONNECT), otherwise read
	// SNI from the current TLS ClientHello.
	if metadata.HostName != "" {
		sni = metadata.HostName
		logger.Info(fmt.Sprintf("[TLS] Using preset hostname: %s", sni))
		logObservedTLSServerName(metadata, "preset")
	} else {
		logger.Debug("TLS: Extracting SNI from TLS ClientHello")
		sni, sniBuf, err = h.tlsHandler.ExtractSNI(connectCtx, originConn)

		if err != nil {
			logger.Warn(fmt.Sprintf("[TLS] SNI extraction error: %v", err))
			sni = ""
		} else if sni != "" {
			// Set hostname with SNI binding source
			setMetadataHostFromObservedSNI(metadata, sni)

			// Check if this is a DoH (DNS over HTTPS) provider
			// DoH traffic should be routed directly without proxy interception
			if IsDoHProvider(sni) {
				logger.Info(fmt.Sprintf("[DoH] Detected DoH provider: %s, routing directly", sni))
				// Return nil to allow direct connection (bypass proxy)
				// This means the connection will be handled directly without MITM or proxy routing
				return nil, nil, nil
			}

			logger.Info(fmt.Sprintf("[TLS] Successfully extracted SNI: %s", sni))
		}
	}

	// Preserve the bytes consumed while reading ClientHello so direct forwarding
	// or MITM can continue from the original stream.
	if len(sniBuf) > 0 {
		wrapped = &WrappedConn{
			Conn: originConn,
			Buf:  sniBuf,
		}
	}
	logger.Debug(fmt.Sprintf(
		"[TLS DIAG] conn_id=%s post_sni origin_type=%T wrapped=%t wrapped_diag=%s sni=%q sni_buf=%d host=%s dns_source=%s route=%s",
		ensureTCPConnID(metadata),
		originConn,
		wrapped != nil,
		describeConnDiagnostics(wrapped),
		sni,
		len(sniBuf),
		metadata.HostName,
		safeBindingSource(metadata),
		metadata.Route,
	))

	// STEP 2: Without an explicit hostname or observed SNI, we only trust a
	// unique cached IP->domain binding. Shared-IP cache entries still bypass MITM.
	if metadata.HostName == "" {
		cache := rules.GetCache()
		if cache == nil {
			logger.Warn(fmt.Sprintf(
				"TLS: No SNI observed for %s:%d and DNS cache is unavailable; bypassing MITM and routing direct",
				metadata.DstIP,
				metadata.DstPort,
			))
		} else if !metadata.DstIP.IsUnspecified() {
			cacheEntries := cache.GetByIP(metadata.DstIP)
			cachedEntry, uniqueDomainCount := selectUniqueCachedDomainEntry(cacheEntries)
			if cachedEntry != nil {
				bindingSource := ""
				if len(cachedEntry.BindingSources) > 0 {
					bindingSource = string(cachedEntry.BindingSources[0])
				}
				metadata.SetHostNameFromCacheEntry(
					cachedEntry.Domain,
					M.BindingSource(bindingSource),
					cachedEntry.CreatedAt,
					cachedEntry.TimeToLive(),
				)
				cacheFallbackUsed = true
				logger.Warn(fmt.Sprintf(
					"TLS: No SNI observed for %s:%d; using unique cached domain=%s to continue TLS routing and MITM",
					metadata.DstIP,
					metadata.DstPort,
					cachedEntry.Domain,
				))
				logObservedTLSServerName(metadata, "cache")
			} else if uniqueDomainCount > 1 {
				logger.Warn(fmt.Sprintf(
					"TLS: No SNI observed for %s:%d; shared IP has %d cached domains, bypassing MITM and routing direct",
					metadata.DstIP,
					metadata.DstPort,
					uniqueDomainCount,
				))
			} else {
				logger.Warn(fmt.Sprintf(
					"TLS: No SNI observed for %s:%d and no reliable cached hostname exists; bypassing MITM and routing direct",
					metadata.DstIP,
					metadata.DstPort,
				))
			}
		} else {
			logger.Warn(fmt.Sprintf(
				"TLS: No SNI observed for destination %s:%d; bypassing MITM and routing direct",
				metadata.DstIP,
				metadata.DstPort,
			))
		}
		if metadata.HostName == "" {
			return h.resolveTLSRoute(sessionCtx, connectCtx, originConn, metadata, RouteDirect, wrapped, "")
		}
	}

	// STEP 3: Determine route based on rules engine with domain info available
	// Now that we have domain info (from cache or SNI extraction), the rules engine
	// can make a more informed routing decision
	route, _ := h.tlsHandler.DetermineRouteWithContext(metadata)

	// Log routing decision at INFO level for business tracking
	var routeSource string
	if sni != "" {
		routeSource = "SNI"
	} else if cacheFallbackUsed {
		routeSource = "cache"
	} else {
		routeSource = "preset"
	}
	logger.Info(fmt.Sprintf("[TLS] Route decision: %s matched via %s -> %v", metadata.HostName, routeSource, route))
	logger.Debug(fmt.Sprintf("TLS: Route decision for %s (%s via %s): %v", metadata.HostName, metadata.DstIP, routeSource, route))

	mitmedSNI := sni
	if mitmedSNI == "" && metadata.HostName != "" && metadata.DNSInfo != nil && metadata.DNSInfo.BindingSource == M.BindingSourceCONNECT {
		// CONNECT already provided an explicit target hostname, which is safe to reuse.
		mitmedSNI = metadata.HostName
	} else if mitmedSNI == "" && cacheFallbackUsed {
		// A unique cached domain is acceptable as a last-resort MITM name when
		// this transparent TLS flow did not expose SNI.
		mitmedSNI = metadata.HostName
	}

	return h.resolveTLSRoute(sessionCtx, connectCtx, originConn, metadata, route, wrapped, mitmedSNI)
}

// dialDirect dials a direct connection to the target using tun dialer
func (h *TCPConnectionHandler) dialDirect(ctx context.Context, metadata *M.Metadata) (net.Conn, error) {
	addr := net.JoinHostPort(metadata.DstIP.String(), fmt.Sprintf("%d", metadata.DstPort))
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		logger.Debug(fmt.Sprintf("Dial failed: %v", err))
		return nil, err
	}

	return conn, nil
}

func (h *TCPConnectionHandler) dialByRoute(ctx context.Context, metadata *M.Metadata, route ProxyRoute) (net.Conn, error) {
	switch route {
	case RouteToALiang:
		metadata.Route = "RouteToALiang"
		aliangProxy, err := h.getAliangProxyForExecution()
		if err != nil {
			return nil, err
		}
		return aliangProxy.DialContext(ctx, metadata)
	case RouteToLocalProxy:
		return h.dialViaSocksOrDirect(ctx, metadata)
	default:
		metadata.Route = "RouteDirect"
		return h.dialDirect(ctx, metadata)
	}
}

func (h *TCPConnectionHandler) resolveTLSRoute(
	sessionCtx context.Context,
	connectCtx context.Context,
	originConn net.Conn,
	metadata *M.Metadata,
	route ProxyRoute,
	wrapped net.Conn,
	mitmedSNI string,
) (net.Conn, net.Conn, error) {
	if route == RouteToALiang {
		metadata.Route = "RouteToALiang"
		mitmConn := wrapped
		// 在某些情况下会跳过sni，导致wrapped为nil，此时直接使用originConn进行MITM，避免因wrapped为nil而导致的MITM失败
		if !isUsableConn(mitmConn) {
			mitmConn = originConn
		}

		mitmed, err := h.tlsHandler.PerformMITM(connectCtx, mitmConn, mitmedSNI)
		if err != nil {
			logger.Warn(fmt.Sprintf("[TLS MITM] MITM failed for %s: %v", mitmedSNI, err))
			return nil, nil, err
		}
		logger.Info(fmt.Sprintf("[TLS MITM] MITM completed for %s", mitmedSNI))

		prefetchedData, sniffErr := prefetchApplicationData(connectCtx, mitmed, applicationPrefetchMaxBytes)
		if sniffErr != nil {
			logger.Debug(fmt.Sprintf("aliang protocol prefetch failed: %v", sniffErr))
		}
		metadata.AppProto = detectApplicationProtocol(prefetchedData)
		if metadata.AppProto == "" {
			metadata.AppProto = AppProtoUnknown
		}
		bufferedMitmed := wrapBufferedConn(mitmed, prefetchedData)

		remote, err := h.dialByRoute(connectCtx, metadata, route)
		if err != nil {
			return nil, nil, err
		}

		logAliangGateProxy(metadata, "tls")

		return remote, h.wrapAliangHTTPConnByProto(bufferedMitmed, metadata.AppProto), nil
	}

	remote, err := h.dialByRoute(connectCtx, metadata, route)
	if err != nil {
		return nil, nil, err
	}
	logger.Debug(fmt.Sprintf(
		"[TLS DIAG] conn_id=%s resolve_tls route=%s host=%s mitm_sni=%q relay_origin_type=%T relay_origin_diag=%s remote_type=%T remote_diag=%s",
		ensureTCPConnID(metadata),
		metadata.Route,
		metadata.HostName,
		mitmedSNI,
		wrapped,
		describeConnDiagnostics(wrapped),
		remote,
		describeConnDiagnostics(remote),
	))
	return remote, wrapped, nil
}

func safeBindingSource(metadata *M.Metadata) string {
	if metadata == nil || metadata.DNSInfo == nil {
		return ""
	}
	return string(metadata.DNSInfo.BindingSource)
}

func (h *TCPConnectionHandler) wrapAliangHTTPConn(conn net.Conn) net.Conn {
	if conn == nil {
		return nil
	}
	return watcher.NewWatcherWrapConn(conn)
}

func (h *TCPConnectionHandler) wrapAliangHTTPConnByProto(conn net.Conn, appProto string) net.Conn {
	if conn == nil {
		return nil
	}
	if appProto == AppProtoHTTP1 {
		return conn
	}
	return h.wrapAliangHTTPConn(conn)
}

func (h *TCPConnectionHandler) dialViaSocksOrDirect(ctx context.Context, metadata *M.Metadata) (net.Conn, error) {
	if !isCustomerProxyEnabled() {
		metadata.Route = "RouteDirect"
		return h.dialDirect(ctx, metadata)
	}

	socksProxy, upstreamType, err := h.getToSocksProxyForExecution()
	if err != nil {
		return nil, err
	}
	logger.Debug(fmt.Sprintf("toSocks execution using upstream type: %s", upstreamType))

	metadata.Route = "RouteToSocks"
	return socksProxy.DialContext(ctx, metadata)
}

func isCustomerProxyEnabled() bool {
	cfg := config.GetGlobalConfig()
	if cfg == nil || cfg.Customer == nil || cfg.Customer.Proxy == nil {
		return true
	}
	return cfg.Customer.Proxy.IsEnabled()
}

func (h *TCPConnectionHandler) enrichMetadataFromReverseLookup(ctx context.Context, metadata *M.Metadata) {
	if metadata == nil || metadata.HostName != "" || !metadata.DstIP.IsValid() || metadata.DstIP.IsUnspecified() {
		return
	}
	if IsLoopbackIP(metadata.DstIP) || IsPrivateIP(metadata.DstIP) {
		return
	}

	cache := rules.GetCache()
	if cache != nil {
		cacheEntries := cache.GetByIP(metadata.DstIP)
		if len(cacheEntries) > 0 {
			cachedEntry := cacheEntries[0]
			bindingSource := M.BindingSourceDNS
			if len(cachedEntry.BindingSources) > 0 {
				bindingSource = cachedEntry.BindingSources[0]
			}
			metadata.SetHostNameFromCacheEntry(
				cachedEntry.Domain,
				bindingSource,
				cachedEntry.CreatedAt,
				cachedEntry.TimeToLive(),
			)
			return
		}
	}

	names, err := reverseLookupAddr(ctx, metadata.DstIP.String())
	if err != nil {
		logger.Debug(fmt.Sprintf("reverse lookup failed for %s: %v", metadata.DstIP, err))
		return
	}

	for _, candidate := range names {
		host := normalizeRoutingHost(candidate)
		if host == "" {
			continue
		}
		metadata.SetHostName(host, M.BindingSourceDNS, M.DefaultDNSTTL)
		logger.Debug(fmt.Sprintf("reverse lookup matched %s -> %s", metadata.DstIP, host))
		return
	}
}

func prefetchApplicationData(ctx context.Context, conn net.Conn, maxBytes int) ([]byte, error) {
	if conn == nil || maxBytes <= 0 {
		return nil, nil
	}

	initialDeadline := time.Now().Add(applicationPrefetchInitialTimeout)
	if deadline, ok := ctx.Deadline(); ok && deadline.Before(initialDeadline) {
		initialDeadline = deadline
	}
	if err := conn.SetReadDeadline(initialDeadline); err != nil {
		return nil, err
	}
	defer conn.SetReadDeadline(time.Time{})

	buf := make([]byte, 0, 1024)
	tmp := make([]byte, 1024)

	for len(buf) < maxBytes {
		readSize := len(tmp)
		if remaining := maxBytes - len(buf); remaining < readSize {
			readSize = remaining
		}

		n, err := conn.Read(tmp[:readSize])
		if n > 0 {
			buf = append(buf, tmp[:n]...)
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return buf, nil
			}
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				return buf, nil
			}
			return buf, err
		}

		if len(buf) == 0 {
			continue
		}
		if hasCompleteHTTPHeaders(buf) || !couldStillBeHTTP(buf) {
			return buf, nil
		}

		nextDeadline := time.Now().Add(applicationPrefetchRetryTimeout)
		if deadline, ok := ctx.Deadline(); ok && deadline.Before(nextDeadline) {
			nextDeadline = deadline
		}
		if err := conn.SetReadDeadline(nextDeadline); err != nil {
			return buf, err
		}
	}

	return buf, nil
}

func wrapBufferedConn(originConn net.Conn, buf []byte) net.Conn {
	if len(buf) == 0 {
		return originConn
	}
	return &WrappedConn{
		Conn: originConn,
		Buf:  buf,
	}
}

func hasCompleteHTTPHeaders(buf []byte) bool {
	return bytes.Contains(buf, []byte("\r\n\r\n")) || bytes.Contains(buf, []byte("\n\n"))
}

func couldStillBeHTTP(buf []byte) bool {
	if len(buf) == 0 {
		return true
	}

	methods := []string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS", "CONNECT", "TRACE"}
	upperPrefix := strings.ToUpper(string(buf))
	for _, method := range methods {
		if strings.HasPrefix(method, upperPrefix) || strings.HasPrefix(upperPrefix, method+" ") {
			return true
		}
	}
	return false
}

func extractHTTPRoutingHost(buf []byte) (host string, source M.BindingSource, isHTTP bool) {
	if len(buf) == 0 {
		return "", "", false
	}

	headerSlice := buf
	if idx := bytes.Index(buf, []byte("\r\n\r\n")); idx >= 0 {
		headerSlice = buf[:idx]
	} else if idx := bytes.Index(buf, []byte("\n\n")); idx >= 0 {
		headerSlice = buf[:idx]
	}

	headerText := strings.ReplaceAll(string(headerSlice), "\r\n", "\n")
	lines := strings.Split(headerText, "\n")
	if len(lines) == 0 {
		return "", "", false
	}

	requestLine := strings.TrimSpace(lines[0])
	parts := strings.Fields(requestLine)
	if len(parts) < 2 {
		return "", "", false
	}

	method := strings.ToUpper(strings.TrimSpace(parts[0]))
	if !isSupportedHTTPMethod(method) {
		return "", "", false
	}

	isHTTP = true
	if method == "CONNECT" {
		return normalizeRoutingHost(parts[1]), M.BindingSourceCONNECT, true
	}

	for _, line := range lines[1:] {
		line = strings.TrimSpace(line)
		if line == "" {
			break
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(key), "host") {
			return normalizeRoutingHost(value), M.BindingSourceHTTP, true
		}
	}

	target := strings.TrimSpace(parts[1])
	if parsedURL, err := url.Parse(target); err == nil && parsedURL.Host != "" {
		return normalizeRoutingHost(parsedURL.Host), M.BindingSourceHTTP, true
	}

	return "", M.BindingSourceHTTP, true
}

func isSupportedHTTPMethod(method string) bool {
	switch method {
	case "GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS", "CONNECT", "TRACE":
		return true
	default:
		return false
	}
}

func normalizeRoutingHost(raw string) string {
	value := strings.ToLower(strings.TrimSpace(raw))
	value = strings.TrimSuffix(value, ".")
	if value == "" {
		return ""
	}

	if parsedURL, err := url.Parse(value); err == nil && parsedURL.Host != "" {
		value = parsedURL.Host
	}

	if host, port, err := net.SplitHostPort(value); err == nil {
		_ = port
		value = host
	} else if strings.HasPrefix(value, "[") && strings.Contains(value, "]") {
		value = strings.TrimPrefix(strings.SplitN(value, "]", 2)[0], "[")
	} else if host, port, ok := strings.Cut(value, ":"); ok && port != "" && isAllDigits(port) {
		value = host
	}

	value = strings.Trim(value, "[]")
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}

	if ip, err := netip.ParseAddr(value); err == nil && ip.IsValid() {
		return ""
	}

	if !IsValidHostname(value) {
		return ""
	}

	return value
}

func isAllDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func (h *TCPConnectionHandler) getAliangProxyForExecution() (outboundproxy.Proxy, error) {
	canonical := config.GetRoutingApplyStore().ActiveCanonicalSchema()
	if canonical != nil && !canonical.Egress.ToAliang.Enabled {
		return nil, newBranchDenyError(toAliangBranchName, DenyReasonToAliangDisabled, nil)
	}

	aliangProxy, err := outbound.GetRegistry().GetAliang()
	if err != nil {
		return nil, newBranchDenyError(toAliangBranchName, DenyReasonToAliangUnavailable, err)
	}

	return aliangProxy, nil
}

func (h *TCPConnectionHandler) getToSocksProxyForExecution() (outboundproxy.Proxy, string, error) {
	canonical := config.GetRoutingApplyStore().ActiveCanonicalSchema()
	upstreamType := toSocksUpstreamTypeSocks

	if canonical != nil {
		if !canonical.Egress.ToSocks.Enabled {
			return nil, "", newBranchDenyError(toSocksBranchName, DenyReasonToSocksDisabled, nil)
		}

		upstreamType = canonical.Egress.ToSocks.Upstream.Type
		if upstreamType == "" {
			return nil, "", newBranchDenyError(toSocksBranchName, DenyReasonToSocksMisconfigured, nil)
		}
	}

	proxyName := ""
	switch upstreamType {
	case toSocksUpstreamTypeSocks:
		proxyName = "socks"
	case toSocksUpstreamTypeHTTP:
		proxyName = "http"
	default:
		return nil, "", newBranchDenyError(
			toSocksBranchName,
			DenyReasonToSocksUnsupported,
			fmt.Errorf("upstream.type=%s", upstreamType),
		)
	}

	socksProxy, err := outbound.GetRegistry().Get(proxyName)
	if err != nil {
		return nil, "", newBranchDenyError(toSocksBranchName, DenyReasonToSocksUnavailable, err)
	}

	return socksProxy, upstreamType, nil
}
