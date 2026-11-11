package tcp

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"aliang.one/nursorgate/common/logger"
	"aliang.one/nursorgate/inbound/tun/dialer"
	M "aliang.one/nursorgate/inbound/tun/metadata"
	"aliang.one/nursorgate/outbound"
	outboundproxy "aliang.one/nursorgate/outbound/proxy"
	meshproxy "aliang.one/nursorgate/outbound/proxy/mesh"
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
	toSocksUpstreamTypeSSH         = "ssh"
	toSocksBranchName              = "toSocks"
	toAliangBranchName             = "toAliang"
)

var tcpConnIDCounter uint64
var establishMirrorUpstreamTLSConn = establishMirrorUpstreamTLS

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
	// meshExits 是 mesh ExitClient 缓存(SPEC §5.1/§10.4):挂在实例上而非
	// 包级变量——包级变量跨 ResetHandler(测试)存活会串味。键与失效见
	// meshproxy.PeerSpec.CacheKey;无 TTL,键不匹配即重建(唯一重建路径)。
	meshExitsMu sync.RWMutex
	meshExits   map[string]*meshproxy.ExitClient
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
		meshExits:        make(map[string]*meshproxy.ExitClient),
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

func detectApplicationProtocolWithALPN(prefetched []byte, clientALPN string) string {
	appProto := detectApplicationProtocol(prefetched)
	if appProto != AppProtoUnknown {
		return appProto
	}

	switch clientALPN {
	case http2.NextProtoTLS:
		return AppProtoHTTP2
	case "http/1.1":
		return AppProtoHTTP1
	default:
		return AppProtoUnknown
	}
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

// setMetadataHostFromObservedSNI 将观察到的 TLS SNI 落入 metadata.HostName。
// 【加固】与 HTTP Host 路径同标准校验（normalizeRoutingHost：空标签/超长/连字符边界/
// IP 字面量等判非法）：畸形 SNI（如 "www.example.com.."）不再按原样落入 HostName——
// 畸形串永不命中域名规则，等于绕过（含镜像/AI 归因）。非法 → HostName 保持空，
// 视同无 SNI（IP/geoip 规则仍可命中），与无 SNI 特性语义自然衔接。
func setMetadataHostFromObservedSNI(metadata *M.Metadata, sni string) {
	if metadata == nil || sni == "" {
		return
	}
	host := normalizeRoutingHost(sni)
	if host == "" {
		logger.Warn(fmt.Sprintf(
			"[TUN TLS] conn_id=%s dropped malformed SNI %q; treating as no-SNI connection",
			ensureTCPConnID(metadata), sni,
		))
		return
	}
	metadata.SetHostName(host, M.BindingSourceSNI, M.DefaultSNITTL)
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
		// AI 离线改道(计划 Task 12,design §2 行11/§9.4):平台隧道不可达
		// (离线信号真)且 AI 类离线兜底勾选时,命中从 RouteToALiang 改判
		// mesh 兜底节点(专用兜底 peer,经 mesh 转发,B 侧把它当普通入站
		// 连接处理后出其自己的隧道)。在线/未勾选/总开关关 → 判定不通过,
		// 保持原路由(在线行为零变化)。连接语义:该连接是 56432 回环 AI
		// 流,继承帧头不存在(链头),mesh 出口按 56432 判据推断 Fresh
		// header Class=ai;originConn 裸透传返回——wrapAliangHTTPConnByProto
		// 的 HTTP/2 改包逻辑只适用于 aliang 隧道,mesh 直转路径不套用。
		// 兜底节点也不可达时如实失败:离线下原路由(RouteToALiang)本就
		// 拨不通,不再叠加一次注定失败的隧道拨号,错误交由 AI 客户端重试。
		if fbAddr, redirect := meshOfflineRedirectForAI(metadata); redirect {
			remote, dialErr := h.dialMeshFallbackNode(ctx, metadata, fbAddr)
			if dialErr != nil {
				return nil, originConn, fmt.Errorf("mesh: offline AI redirect to fallback node %s failed: %w", fbAddr, dialErr)
			}
			metadata.Route = "RouteMeshFallback"
			return remote, originConn, nil
		}
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
// connection. If we can't read an explicit hostname or SNI, we bypass MITM
// (certificate/HTTP-2 mismatch protection on shared IPs) but still consult the
// routing engine with the destination IP so ip/geoip rules can steer the flow;
// toAliang hits are downgraded to direct (empty-name MITM would break).
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
			// Set hostname with SNI binding source；畸形 SNI 被同标准校验拦截时
			// HostName 保持空——此处同步置空 sni，防止下方 mitmedSNI/日志继续使用畸形串
			// （视同无 SNI：走无 SNI 路径，IP/geoip 规则仍可命中）
			setMetadataHostFromObservedSNI(metadata, sni)
			if metadata.HostName == "" {
				sni = ""
			} else {
				logger.Info(fmt.Sprintf("[TLS] Successfully extracted SNI: %s", sni))
			}
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

	// DoH is normal HTTPS traffic from the browser's point of view. Bypass
	// interception, but still establish and relay the direct upstream connection.
	if metadata.HostName != "" && IsDoHProvider(metadata.HostName) {
		logger.Info(fmt.Sprintf("[DoH] Detected DoH provider: %s, routing directly", metadata.HostName))
		return h.resolveTLSRoute(sessionCtx, connectCtx, originConn, metadata, RouteDirect, wrapped, "")
	}

	// STEP 2: Without an explicit hostname or observed SNI, we only trust a
	// unique cached IP->domain binding. Shared-IP cache entries still bypass MITM.
	if metadata.HostName == "" {
		cache := rules.GetCache()
		if cache == nil {
			logger.Warn(fmt.Sprintf(
				"TLS: No SNI observed for %s:%d and DNS cache is unavailable; bypassing MITM and consulting routing engine by IP",
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
					"TLS: No SNI observed for %s:%d; shared IP has %d cached domains, bypassing MITM and consulting routing engine by IP",
					metadata.DstIP,
					metadata.DstPort,
					uniqueDomainCount,
				))
			} else {
				logger.Warn(fmt.Sprintf(
					"TLS: No SNI observed for %s:%d and no reliable cached hostname exists; bypassing MITM and consulting routing engine by IP",
					metadata.DstIP,
					metadata.DstPort,
				))
			}
		} else {
			logger.Warn(fmt.Sprintf(
				"TLS: No SNI observed for destination %s:%d; bypassing MITM and consulting routing engine by IP",
				metadata.DstIP,
				metadata.DstPort,
			))
		}
		if metadata.HostName == "" {
			// 无 SNI：不做 MITM（证书错配防护不变），但携带 IP 咨询规则引擎
			// 让 IP/geoip 规则生效（spec §3.1）；toAliang 命中降级 direct。
			route, _ := h.tlsHandler.DetermineRouteWithContext(metadata) // (ProxyRoute, requiresSNI bool)；引擎失败内部已回退 default direct（tls.go:307-322）
			route = downgradeNoSNIAliangRoute(route)
			return h.resolveTLSRoute(sessionCtx, connectCtx, originConn, metadata, route, wrapped, "")
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
	case RouteToNode:
		// mesh 转发出口(Task 7):decision 字符串(toNode:<id>[:policy])由
		// tls.go decideRouteWithRoutingEngine 寄存于 metadata.Route。
		return h.dialViaMesh(ctx, metadata)
	default:
		metadata.Route = "RouteDirect"
		return h.dialDirect(ctx, metadata)
	}
}

// downgradeNoSNIAliangRoute 无 SNI 连接命中 toAliang 时硬排除降级 direct：
// 空 HostName 会使 mitmedSNI 为空串，PerformMITM 给空名签证书必断链
// （spec: docs/superpowers/specs/2026-10-14-nosni-tls-routing-design.md §3.1）。
// shouldForceAliangRoute 特判（本地代理端口+回环）来源的命中同样经此降级并打日志。
func downgradeNoSNIAliangRoute(route ProxyRoute) ProxyRoute {
	if route == RouteToALiang {
		logger.Debug("[TLS] no-SNI connection hit toAliang rule; downgraded to direct (empty-name MITM would break)")
		return RouteDirect
	}
	return route
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

		clientALPN := negotiatedTLSProtocol(mitmed)
		prefetchedData, sniffErr := prefetchApplicationData(connectCtx, mitmed, applicationPrefetchMaxBytes)
		if sniffErr != nil {
			logger.Debug(fmt.Sprintf("aliang protocol prefetch failed: %v", sniffErr))
		}
		metadata.AppProto = detectApplicationProtocolWithALPN(prefetchedData, clientALPN)
		bufferedMitmed := wrapBufferedConn(mitmed, prefetchedData)

		remote, err := h.dialByRoute(connectCtx, metadata, route)
		if err != nil {
			return nil, nil, err
		}

		logAliangGateProxy(metadata, "tls")

		return remote, h.wrapAliangHTTPConnByProto(bufferedMitmed, metadata.AppProto), nil
	}

	if shouldMITMForMirror(metadata, route) {
		return h.resolveTLSMirrorRoute(connectCtx, originConn, metadata, route, wrapped, mitmedSNI)
	}

	remote, err := h.dialByRoute(connectCtx, metadata, route)
	if err != nil {
		return nil, nil, err
	}
	relayOrigin := net.Conn(wrapped)
	if !isUsableConn(relayOrigin) {
		relayOrigin = originConn
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
	return remote, relayOrigin, nil
}

func shouldMITMForMirror(metadata *M.Metadata, route ProxyRoute) bool {
	if route == RouteToALiang {
		return false
	}
	return mirror.ShouldMirror(metadata)
}

func (h *TCPConnectionHandler) resolveTLSMirrorRoute(
	connectCtx context.Context,
	originConn net.Conn,
	metadata *M.Metadata,
	route ProxyRoute,
	wrapped net.Conn,
	mitmedSNI string,
) (net.Conn, net.Conn, error) {
	if strings.TrimSpace(mitmedSNI) == "" {
		mitmedSNI = strings.TrimSpace(metadata.HostName)
	}
	if strings.TrimSpace(mitmedSNI) == "" {
		return nil, nil, fmt.Errorf("monitor MITM requires a server name")
	}

	mitmConn := wrapped
	if !isUsableConn(mitmConn) {
		mitmConn = originConn
	}
	if !isUsableConn(mitmConn) {
		return nil, nil, fmt.Errorf("monitor MITM requires a usable client TLS stream")
	}

	mitmed, err := h.tlsHandler.PerformMITM(connectCtx, mitmConn, mitmedSNI)
	if err != nil {
		logger.Warn(fmt.Sprintf("[TLS MIRROR MITM] MITM failed for %s: %v", mitmedSNI, err))
		return nil, nil, err
	}

	clientALPN := negotiatedTLSProtocol(mitmed)
	prefetchedData, sniffErr := prefetchApplicationData(connectCtx, mitmed, applicationPrefetchMaxBytes)
	if sniffErr != nil {
		logger.Debug(fmt.Sprintf("mirror MITM protocol prefetch failed: %v", sniffErr))
	}

	metadata.AppProto = detectApplicationProtocolWithALPN(prefetchedData, clientALPN)
	bufferedMitmed := wrapBufferedConn(mitmed, prefetchedData)

	remoteRaw, err := h.dialByRoute(connectCtx, metadata, route)
	if err != nil {
		return nil, nil, err
	}

	remoteTLS, err := establishMirrorUpstreamTLSConn(connectCtx, remoteRaw, mitmedSNI, mirrorUpstreamNextProtos(clientALPN, metadata.AppProto), clientALPN, metadata.AppProto)
	if err != nil {
		_ = remoteRaw.Close()
		return nil, nil, err
	}

	logger.Info(fmt.Sprintf("[TLS MIRROR MITM] MITM bridge established for %s via %s (proto=%s alpn=%s)",
		mitmedSNI, metadata.Route, metadata.AppProto, clientALPN))
	return remoteTLS, bufferedMitmed, nil
}

func negotiatedTLSProtocol(conn net.Conn) string {
	tlsConn, ok := conn.(*tls.Conn)
	if !ok || tlsConn == nil {
		return ""
	}
	return tlsConn.ConnectionState().NegotiatedProtocol
}

func mirrorUpstreamNextProtos(clientALPN, appProto string) []string {
	switch clientALPN {
	case http2.NextProtoTLS:
		return []string{http2.NextProtoTLS}
	case "http/1.1":
		return []string{"http/1.1"}
	}

	switch appProto {
	case AppProtoHTTP2:
		return []string{http2.NextProtoTLS}
	case AppProtoHTTP1:
		return []string{"http/1.1"}
	default:
		return []string{http2.NextProtoTLS, "http/1.1"}
	}
}

func establishMirrorUpstreamTLS(ctx context.Context, raw net.Conn, serverName string, nextProtos []string, clientALPN string, appProto string) (net.Conn, error) {
	if !isUsableConn(raw) {
		return nil, net.ErrClosed
	}

	cfg := &tls.Config{
		ServerName: strings.TrimSpace(serverName),
		NextProtos: append([]string(nil), nextProtos...),
		MinVersion: tls.VersionTLS12,
	}
	tlsConn := tls.Client(raw, cfg)

	clearDeadline := applyContextReadDeadline(ctx, raw)
	defer clearDeadline()

	if err := tlsConn.Handshake(); err != nil {
		return nil, err
	}

	upstreamALPN := tlsConn.ConnectionState().NegotiatedProtocol
	if err := validateMirrorUpstreamALPN(clientALPN, appProto, upstreamALPN); err != nil {
		_ = tlsConn.Close()
		return nil, err
	}
	return tlsConn, nil
}

func validateMirrorUpstreamALPN(clientALPN, appProto, upstreamALPN string) error {
	if clientALPN == http2.NextProtoTLS && upstreamALPN != http2.NextProtoTLS {
		return fmt.Errorf("monitor MITM upstream ALPN mismatch: client=%s upstream=%s", clientALPN, upstreamALPN)
	}
	if appProto == AppProtoHTTP2 && upstreamALPN != http2.NextProtoTLS {
		return fmt.Errorf("monitor MITM upstream does not support HTTP/2: upstream_alpn=%s", upstreamALPN)
	}
	return nil
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
		// toSocks 分支被用户关闭：拒绝走 socks 但不断链，降级 direct
		// （defaultFallbackRoute 语义）。其余 deny 原因（上游缺失/不可用）维持报错断链。
		if BranchDenyReason(err) == DenyReasonToSocksDisabled {
			logger.Debug(fmt.Sprintf("toSocks branch disabled by user, degrading to direct for %s", metadata.DestinationAddress()))
			metadata.Route = "RouteDirect"
			return h.dialDirect(ctx, metadata)
		}
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
	upstreamType, upstreamConfigured := currentCustomerProxyUpstreamType()

	if canonical != nil {
		// 用户显式关闭 toSocks（egress.toSocks.enabled=false）即拒绝走 socks 分支，
		// 与上游是否仍配置无关——上游配置存在不构成重新启用开关的理由。
		// 拒绝后由调用方降级 direct（见 dialViaSocksOrDirect），不再断链。
		if !canonical.Egress.ToSocks.Enabled {
			return nil, "", newBranchDenyError(toSocksBranchName, DenyReasonToSocksDisabled, nil)
		}

		if !upstreamConfigured {
			upstreamType = canonical.Egress.ToSocks.Upstream.Type
		}
		if upstreamType == "" {
			return nil, "", newBranchDenyError(toSocksBranchName, DenyReasonToSocksMisconfigured, nil)
		}
	}
	if upstreamType == "" {
		return nil, "", newBranchDenyError(toSocksBranchName, DenyReasonToSocksMisconfigured, nil)
	}

	proxyName := ""
	switch upstreamType {
	case toSocksUpstreamTypeSocks:
		proxyName = "socks"
	case toSocksUpstreamTypeHTTP:
		proxyName = "http"
	case toSocksUpstreamTypeSSH:
		proxyName = "ssh"
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

func currentCustomerProxyUpstreamType() (string, bool) {
	cfg := config.GetGlobalConfig()
	if cfg == nil || cfg.Customer == nil || cfg.Customer.Proxy == nil || !cfg.Customer.Proxy.IsEnabled() {
		return "", false
	}

	switch strings.ToLower(strings.TrimSpace(cfg.Customer.Proxy.Type)) {
	case "http":
		return toSocksUpstreamTypeHTTP, true
	case "socks5":
		return toSocksUpstreamTypeSocks, true
	case "ssh":
		return toSocksUpstreamTypeSSH, true
	default:
		return "", false
	}
}

// ---- mesh 拨出执行(design §3.3/§3.4/§9.4,计划 Task 7)----

// errMeshInjectedLoop mesh 注入防环(SPEC §6.2):由 mesh-exit 注入的连接
// (B 侧转发而来,Metadata.MeshInjected=true)若再命中本机 mesh 规则会
// 二次出 mesh 形成环,三处 mesh 拨出入口拨出前一律拒绝——链条由配置期
// 定死(单跳),该限制无损:需要 A→C→D 就配成对应节点。
var errMeshInjectedLoop = errors.New("mesh: injected traffic must not re-enter mesh (loop guard)")

// meshTransportDialSource 组装层注入缝,atomic.Value 保护(质量评审
// Critical 1 先例):piko 中继载体拨号在 processor/tcp 层无自然来源——生产
// 实现由 app/tunnel 封装(manager.dialMeshEndpoint,每次拨号读当前配置)。
// 生产写方 app/tunnel manager validateConfig(隧道每次续期重跑 Set)与
// mesh 拨号 goroutine 读方跨包并发,普通包级变量存在数据竞争(-race 实测,
// 见 TestMeshSeamConcurrentSetAndRead)。atomic.Value 要求 Store 的具体类型
// 恒定:缝恒为 func() meshproxy.PikoDialFunc 类型;nil 归一为空提供器
// (atomic.Value 不接受 nil 接口,归一后 Load 语义与未接线一致)。未接线
// = 中继载体不进 ExitClient 的 Transport 切片(仅 LAN,SPEC §7.3);测试
// 注入 fake 后 t.Cleanup 恢复(清空 = Set(nil))。
var meshTransportDialSource atomic.Value // func() meshproxy.PikoDialFunc

// SetMeshTransportDialSource 注入 piko 中继载体拨号提供器(生产接线:
// app/tunnel manager.validateConfig);nil = 清除。并发安全。
func SetMeshTransportDialSource(fn func() meshproxy.PikoDialFunc) {
	if fn == nil {
		fn = func() meshproxy.PikoDialFunc { return nil }
	}
	meshTransportDialSource.Store(fn)
}

func meshTransportDialFromSource() meshproxy.PikoDialFunc {
	fn, _ := meshTransportDialSource.Load().(func() meshproxy.PikoDialFunc)
	if fn == nil {
		return nil
	}
	return fn()
}

// dialViaMesh 执行 toNode:<deviceID>[:policy] 决策(错误回源语义,链头就地
// 执行,dialByRoute 的 RouteToNode 分支):
//
//	mesh 拨出成功 → 返回;失败 → 链头先兜底再失败策略
//
// 单跳模型(SPEC §6.2):中间跳续传已随帧协议退役——B 注入连接被顶部
// MeshInjected 守卫拦截,不可能走到本函数的 mesh 拨出,失败策略只属于链头。
// 链头失败路径顺序:一、离线兜底(customer.mesh.Fallback 显式使能且类别
// 勾选;兜底地址指对端 SSH 入口 56432,经 DialMeshAddr 走 SSH,LAN-only 单
// 载体);二、规则失败策略——缺省/direct 回落直连、deny 拒绝连接、
// fallback:<id2> 拨指定节点(限一次,不再链式,防策略链)。
func (h *TCPConnectionHandler) dialViaMesh(ctx context.Context, metadata *M.Metadata) (net.Conn, error) {
	if metadata.MeshInjected {
		return nil, fmt.Errorf("%w (route=%s)", errMeshInjectedLoop, metadata.Route)
	}
	deviceID, policy, ok := config.ParseMeshRuleTarget(metadata.Route)
	if !ok || deviceID == "" {
		// decision 字符串缺失/畸形:只有 RouteToNode 会进入本分支,寄存通路
		// (tls.go)只产合法形态,此处兜住直接调用 dialByRoute 的畸形输入。
		return nil, fmt.Errorf("mesh: route decision %q is not a valid toNode target", metadata.Route)
	}

	client, peerErr := h.getMeshExitForExecution(deviceID)
	if peerErr == nil {
		conn, err := client.DialContext(ctx, metadata)
		if err == nil {
			metadata.Route = "RouteToNode"
			return conn, nil
		}
		peerErr = err
	}

	logger.Warn(fmt.Sprintf("[MESH-OUT] conn_id=%s dial to %s failed, executing head failure policy (policy=%q): %v",
		ensureTCPConnID(metadata), deviceID, policy, peerErr))

	// 一、离线兜底(位于规则失败策略之前,SPEC §7.2/design §9.4 定死)。
	if fbAddr, allowed := meshFallbackAddrForClass(ctx, metadata); allowed {
		conn, err := h.dialMeshFallbackNode(ctx, metadata, fbAddr)
		if err == nil {
			metadata.Route = "RouteMeshFallback"
			return conn, nil
		}
		logger.Warn(fmt.Sprintf("[MESH-OUT] conn_id=%s mesh fallback node %s failed, continuing to failure policy: %v",
			ensureTCPConnID(metadata), fbAddr, err))
	}

	// 二、规则失败策略(缺省 = direct)。
	switch policy {
	case "", config.MeshPolicyDirect:
		metadata.Route = "RouteDirect"
		conn, directErr := h.dialDirect(ctx, metadata)
		if directErr != nil {
			// 回落 direct 也失败:终端错误,两段失败同报便于回源定位
			// (与 mesh 出口「LAN 全败+中继败」错误聚合同款口径)。
			return nil, fmt.Errorf("mesh: %s unreachable, direct fallback also failed: %w", deviceID, directErr)
		}
		return conn, nil
	case config.MeshPolicyDeny:
		return nil, fmt.Errorf("mesh: route to %s denied by failure policy: %w", deviceID, peerErr)
	default:
		// fallback:<id2>——拨指定节点,限一次:失败即终局,不再叠加策略
		// (防 fallback 链;指定节点不可达/不存在在此收口)。
		fallbackID, isFallback := strings.CutPrefix(policy, config.MeshPolicyFallback+":")
		if !isFallback || fallbackID == "" {
			return nil, fmt.Errorf("mesh: invalid failure policy %q for %s", policy, deviceID)
		}
		fbClient, fbErr := h.getMeshExitForExecution(fallbackID)
		if fbErr != nil {
			return nil, fmt.Errorf("mesh: failure policy fallback to %s unavailable: %w", fallbackID, fbErr)
		}
		conn, err := fbClient.DialContext(ctx, metadata)
		if err != nil {
			return nil, fmt.Errorf("mesh: failure policy dial to %s failed: %w", fallbackID, err)
		}
		metadata.Route = "RouteToNode"
		return conn, nil
	}
}

// getMeshExitForExecution 从 customer.mesh.peers 装配并缓存 toNode:<deviceID>
// 的 ExitClient(SPEC §5.1/§7.3)。peer 不存在/未配置/缺身份 → 错误:编译期
// meshForwardAvailable 已把关(含 SSH 依赖与钉扎/身份检查,A3),此处兜住
// 决策→拨出窗口内的配置漂移,由链头失败策略收口。
func (h *TCPConnectionHandler) getMeshExitForExecution(deviceID string) (*meshproxy.ExitClient, error) {
	cfg := config.GetGlobalConfig()
	if cfg == nil || cfg.Customer == nil || cfg.Customer.Mesh == nil || !cfg.MeshEffectivelyEnabled() {
		return nil, fmt.Errorf("mesh: customer.mesh not configured or degraded (target %s)", deviceID)
	}
	meshCfg := cfg.Customer.Mesh
	if strings.TrimSpace(meshCfg.PrivateKey) == "" {
		return nil, fmt.Errorf("mesh: customer.mesh.private_key not configured")
	}
	for i := range meshCfg.Peers {
		entry := &meshCfg.Peers[i]
		if strings.TrimSpace(entry.DeviceID) != deviceID {
			continue
		}
		spec := meshproxy.PeerSpec{
			DeviceID: strings.TrimSpace(entry.DeviceID),
			LANAddrs: entry.LANAddrs,
			HostKey:  strings.TrimSpace(entry.HostKey),
			SSHUser:  "mesh",
			Relay:    entry.Relay == nil || *entry.Relay,
		}
		var pikoDial meshproxy.PikoDialFunc
		if spec.Relay {
			if dial := meshTransportDialFromSource(); dial != nil {
				pikoDial = dial
			}
		}
		key := spec.CacheKey(meshCfg.PrivateKey, meshCfg.KeyPassphrase)
		h.meshExitsMu.RLock()
		client, ok := h.meshExits[key]
		h.meshExitsMu.RUnlock()
		if ok {
			return client, nil
		}
		client, err := meshproxy.NewExitClient(spec, meshCfg.PrivateKey, meshCfg.KeyPassphrase, pikoDial)
		if err != nil {
			return nil, err
		}
		h.meshExitsMu.Lock()
		if h.meshExits == nil {
			h.meshExits = make(map[string]*meshproxy.ExitClient) // 零值 handler 直用(测试)防御
		}
		h.meshExits[key] = client // 键不匹配即重建(唯一失效路径,SPEC §10.4)
		h.meshExitsMu.Unlock()
		return client, nil
	}
	return nil, fmt.Errorf("mesh: peer %q not found in customer.mesh.peers", deviceID)
}

// meshFallbackAddrForClass 离线兜底可用性判定(SPEC §7.2 兜底式),统一
// 抽取为共享助手 config.MeshFallbackActiveForClass(计划 Task 12 评审钉死):
// EffectiveEnabled 总开关 + Fallback.Enable + 类别勾选 + Addr 非空——总开关
// 检查是抽取时补上的穿透缺口(原实现缺此检查)。类别口径复用 mesh 出口
// 推断(56432 回环 → ai,其余 custom;self 类由 egress 发起点显式指定,
// 同一助手自动覆盖)。离线信号不在此层:本闸消费的是 dialViaMesh 链头失败
// 路径(对端不可达),与离线改道(AI 流量)的信号驱动判定互相独立。
func meshFallbackAddrForClass(ctx context.Context, md *M.Metadata) (string, bool) {
	class := meshproxy.InferClass(ctx, md)
	return config.MeshFallbackActiveForClass(config.GetGlobalConfig(), class)
}

// dialMeshFallbackNode 拨离线兜底节点:兜底地址现指对端 SSH 入口 56432
// (SPEC §7.2——旧值指向已删除的 mesh 监听口,发布说明必写),经
// DialMeshAddr 走 SSH(LAN-only 单载体)。本机身份 = customer.mesh.
// private_key(兜底路径拨号频率低,构造期解析私钥可接受,不缓存)。
// 失败返回错误,由调用方继续走规则失败策略(兜底也失败才走失败策略)。
func (h *TCPConnectionHandler) dialMeshFallbackNode(ctx context.Context, metadata *M.Metadata, addr string) (net.Conn, error) {
	if metadata.MeshInjected {
		return nil, fmt.Errorf("%w (route=%s)", errMeshInjectedLoop, metadata.Route)
	}
	cfg := config.GetGlobalConfig()
	if cfg == nil || cfg.Customer == nil || cfg.Customer.Mesh == nil {
		return nil, fmt.Errorf("mesh: customer.mesh not configured (fallback %s)", addr)
	}
	meshCfg := cfg.Customer.Mesh
	signer, err := meshproxy.SignerFromPEM(meshCfg.PrivateKey, meshCfg.KeyPassphrase)
	if err != nil {
		return nil, fmt.Errorf("mesh: fallback identity key: %w", err)
	}
	payload := meshproxy.ExitPayloadForMetadata(metadata)
	if payload == nil {
		return nil, meshproxy.ErrUnbuildableTarget()
	}
	return meshproxy.DialMeshAddr(ctx, addr, config.MeshFallbackHostKey(cfg), signer, payload)
}
