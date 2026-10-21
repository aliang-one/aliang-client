package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"aliang.one/nursorgate/app/http/common"
	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/app/http/services"

	"github.com/gorilla/websocket"
)

// agentProxyDataTimeout 是仪表盘数据面代理（会话列表/转录/终端回放）的期限。
// 这些读取在 runtime 侧可能触发冷转录扫描（liang-dev 实测 30s+），走 12s
// 控制面 client 会被掐断并退化成「空列表 + 200」（2026-10-21 事故），所以
// 单独走无硬超时的 client，由这里的 context 期限兜底。
const agentProxyDataTimeout = 45 * time.Second

type AgentHandler struct {
	service *services.AgentService
	client  *http.Client
	// dataClient 不设 Timeout：期限由 agentProxyDataTimeout 的 context 控制。
	dataClient *http.Client
}

func NewAgentHandler(service *services.AgentService) *AgentHandler {
	if service == nil {
		service = services.GetSharedAgentService()
	}
	return &AgentHandler{
		service:    service,
		client:     &http.Client{Timeout: 12 * time.Second},
		dataClient: &http.Client{},
	}
}

// aiStreamUpgrader upgrades the in-app chat WebSocket. Same localhost-only
// origin policy as the log stream so the bundled web UI can connect directly
// and drive the local Claude Code / Codex headless run.
var aiStreamUpgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin: func(r *http.Request) bool {
		host := strings.Split(r.Host, ":")[0]
		return host == "localhost" || host == "127.0.0.1"
	},
}

// HandleAIStream is the local chat WebSocket endpoint (/api/agent/ai/stream).
// It bridges the web UI to the agent AI manager: each inbound JSON message is
// dispatched locally (ai.session.create / ai.message / ai.stop / ai.session.close)
// and every event the manager emits (ai.run.started, ai.delta, ai.done, ai.error)
// is written straight back over the same socket, giving the page a live,
// token-by-token stream plus the running -> done status transitions.
func (h *AgentHandler) HandleAIStream(w http.ResponseWriter, r *http.Request) {
	conn, err := aiStreamUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	var writeMu sync.Mutex
	writeJSON := func(payload interface{}) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return conn.WriteJSON(payload)
	}

	for {
		var msg map[string]interface{}
		if err := conn.ReadJSON(&msg); err != nil {
			return
		}
		h.service.DispatchLocalAI(msg, writeJSON)
	}
}

// HandleToolAskMCP 自有交互工具 MCP 端点(tool_ask bridge):协议层交官方
// SDK,鉴权/关联由 bridge 的 per-run token 把关(见 services.agentToolAskBridge)。
func (h *AgentHandler) HandleToolAskMCP(w http.ResponseWriter, r *http.Request) {
	h.service.HandleToolAskMCP(w, r)
}

func (h *AgentHandler) HandleAIApprovalHook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		common.Error(w, http.StatusMethodNotAllowed, "Method not allowed", nil)
		return
	}
	defer r.Body.Close()
	var payload map[string]interface{}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&payload); err != nil {
		common.Error(w, http.StatusBadRequest, "Invalid approval hook payload", nil)
		return
	}
	response, _ := h.service.HandleAIApprovalHook(
		r.Context(),
		r.URL.Query().Get("session_id"),
		r.URL.Query().Get("message_id"),
		r.URL.Query().Get("token"),
		payload,
	)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(response)
}

func (h *AgentHandler) HandleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		common.Error(w, http.StatusMethodNotAllowed, "Method not allowed", nil)
		return
	}
	if h.proxyIfNeeded(w, r) {
		return
	}
	common.Success(w, h.service.Status())
}

func (h *AgentHandler) HandleEnable(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		common.Error(w, http.StatusMethodNotAllowed, "Method not allowed", nil)
		return
	}
	if h.proxyIfNeeded(w, r) {
		return
	}
	resp, err := h.service.EnableWithUserContext(r.Header.Get(services.AgentForwardedAuthorizationHeader), r.Header.Get(services.AgentForwardedUserKeyHeader))
	if err != nil {
		writeAgentServiceError(w, "Failed to enable agent", err)
		return
	}
	common.Success(w, resp)
}

// HandleAuthRecover re-enables an agent that was terminal-disabled by an
// auth_expired session, but only if a fresh valid token is now available. It is
// self-gating (RecoverIfAuthExpired is a no-op otherwise), so it is safe to
// nudge after every successful token refresh. Proxied to the user-agent server
// when this process isn't the runtime that owns agent state.
func (h *AgentHandler) HandleAuthRecover(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		common.Error(w, http.StatusMethodNotAllowed, "Method not allowed", nil)
		return
	}
	if h.proxyIfNeeded(w, r) {
		return
	}
	resp, err := h.service.RecoverIfAuthExpired()
	if err != nil {
		writeAgentServiceError(w, "Failed to recover agent", err)
		return
	}
	common.Success(w, resp)
}

// HandleReconnect (re)establishes the remote agent link. Idempotent: a no-op
// when already connected/connecting, or when no device_token / agent disabled.
// Proxied to the user-agent server when this process isn't the runtime that
// owns the WS connection. Used to bring the link back once the user session is
// restored (login or a successful token refresh). Note a session expiry does
// deregister: a WS-handshake 401 terminally disables the agent
// (disableWithReasonMessage clears Device/Registered), and registration only
// comes back through the recovery chain — auth rejection is forwarded to the
// session owner, whose refresh either re-enables and re-registers the device
// (RecoverIfAuthExpired) or lands in the refresh_invalid terminal state.
func (h *AgentHandler) HandleReconnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		common.Error(w, http.StatusMethodNotAllowed, "Method not allowed", nil)
		return
	}
	if h.proxyIfNeeded(w, r) {
		return
	}
	if err := h.service.EnsureRemoteConnection(); err != nil {
		writeAgentServiceError(w, "Failed to reconnect agent", err)
		return
	}
	common.Success(w, h.service.Status())
}

func (h *AgentHandler) HandleDisable(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		common.Error(w, http.StatusMethodNotAllowed, "Method not allowed", nil)
		return
	}
	if h.proxyIfNeeded(w, r) {
		return
	}
	reason := strings.TrimSpace(r.URL.Query().Get("reason"))
	if r.Body != nil {
		defer r.Body.Close()
		var req models.AgentDisableRequest
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err == nil && strings.TrimSpace(req.Reason) != "" {
			reason = req.Reason
		}
	}
	common.Success(w, h.service.DisableWithReason(reason))
}

func (h *AgentHandler) HandleSync(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		common.Error(w, http.StatusMethodNotAllowed, "Method not allowed", nil)
		return
	}
	if h.proxyIfNeeded(w, r) {
		return
	}
	if err := h.service.SyncNowWithUserContext(r.Header.Get(services.AgentForwardedAuthorizationHeader), r.Header.Get(services.AgentForwardedUserKeyHeader)); err != nil {
		writeAgentServiceError(w, "Failed to sync agent device", err)
		return
	}
	common.Success(w, h.service.Status())
}

func (h *AgentHandler) HandleTools(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		common.Error(w, http.StatusMethodNotAllowed, "Method not allowed", nil)
		return
	}
	if h.proxyIfNeeded(w, r) {
		return
	}
	common.Success(w, map[string]interface{}{"tools": h.service.Tools()})
}

func (h *AgentHandler) HandleProtocol(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		common.Error(w, http.StatusMethodNotAllowed, "Method not allowed", nil)
		return
	}
	common.Success(w, models.DefaultAgentProtocolContract())
}

func (h *AgentHandler) HandleLaunch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		common.Error(w, http.StatusMethodNotAllowed, "Method not allowed", nil)
		return
	}
	if h.proxyIfNeeded(w, r) {
		return
	}
	var req models.AgentLaunchRequest
	if err := common.DecodeRequest(r, &req); err != nil {
		common.ErrorBadRequest(w, "Invalid request body", map[string]interface{}{"error": err.Error()})
		return
	}
	resp, err := h.service.Launch(req)
	if err != nil {
		common.ErrorBadRequest(w, err.Error(), nil)
		return
	}
	common.Success(w, resp)
}

// HandleVibeSessions returns local AI coding sessions as summaries. When this
// process is not the user-agent runtime and the runtime is unreachable, an
// empty list is returned so the page can show an empty state instead of an error.
func (h *AgentHandler) HandleVibeSessions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		common.Error(w, http.StatusMethodNotAllowed, "Method not allowed", nil)
		return
	}
	if services.IsUserAgentRuntime() {
		common.Success(w, map[string]interface{}{"sessions": h.service.VibeSessions()})
		return
	}
	// 数据面代理：runtime 侧冷扫描可能远超 12s 控制面超时，12s 掐断后这里的
	// 兜底空列表会把「运行中的 agent」误报成离线（2026-10-21 liang-dev 实测）。
	// 改走 dataClient + agentProxyDataTimeout 期限，兜底语义保持不变。
	ctx, cancel := context.WithTimeout(r.Context(), agentProxyDataTimeout)
	defer cancel()
	if err := h.proxyToUserAgentClient(ctx, w, r, h.dataClient); err != nil {
		common.Success(w, map[string]interface{}{"sessions": []interface{}{}})
		return
	}
}

// HandleRuntimeSessions returns only sessions currently executing in the
// user-agent process. The main process proxies this request to that runtime.
func (h *AgentHandler) HandleRuntimeSessions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		common.Error(w, http.StatusMethodNotAllowed, "Method not allowed", nil)
		return
	}
	if services.IsUserAgentRuntime() {
		common.Success(w, h.service.RuntimeSessions())
		return
	}
	// 数据面代理：与 HandleVibeSessions 同理，走 dataClient + 更长期限。
	ctx, cancel := context.WithTimeout(r.Context(), agentProxyDataTimeout)
	defer cancel()
	if err := h.proxyToUserAgentClient(ctx, w, r, h.dataClient); err != nil {
		common.Success(w, models.AgentRuntimeSnapshot{
			AIConversations: []models.AgentVibeSession{},
			Terminals:       []models.AgentTerminalRuntime{},
			CollectedAt:     time.Now().UTC().Format(time.RFC3339),
		})
	}
}

// HandleVibeSession returns one session's transcript page (?id=&limit=&before=).
func (h *AgentHandler) HandleVibeSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		common.Error(w, http.StatusMethodNotAllowed, "Method not allowed", nil)
		return
	}
	if h.proxyDataIfNeeded(w, r) {
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		common.ErrorBadRequest(w, "id is required", nil)
		return
	}
	limit := 0
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	session, err := h.service.VibeSessionDetail(id, limit, r.URL.Query().Get("before"))
	if err != nil {
		common.ErrorNotFound(w, err.Error())
		return
	}
	common.Success(w, session)
}

// HandleTerminalScrollback returns a terminal session's recent output
// (?limit= bytes, default 64KiB, cap 512KiB). Mirrors HandleRuntimeSessions:
// the terminal manager lives in the user-agent runtime, so non-runtime
// processes proxy there via proxyDataIfNeeded（数据面期限，见 agentProxyDataTimeout）.
func (h *AgentHandler) HandleTerminalScrollback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		// common.Error 收业务码（100-299），直传 http.StatusMethodNotAllowed 会被
		// 映射成 500；此处须真实 405，参照 session_event_broker 的 http.Error 惯例。
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	sessionID := strings.TrimSpace(r.PathValue("sessionId"))
	if sessionID == "" {
		common.ErrorBadRequest(w, "sessionId is required", nil)
		return
	}
	if h.proxyDataIfNeeded(w, r) {
		return
	}
	limit := 0
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	resp, ok := h.service.TerminalScrollback(sessionID, limit)
	if !ok {
		common.ErrorNotFound(w, "terminal session not found")
		return
	}
	common.Success(w, resp)
}

// HandleScanDirectories GET 读取 / POST 设置扫描目录过滤（经 proxyIfNeeded 代理到 runtime，
// runtime 直接读写本地 state 并持久化）。
func (h *AgentHandler) HandleScanDirectories(w http.ResponseWriter, r *http.Request) {
	if h.proxyIfNeeded(w, r) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		dirs, enabled := h.service.ScanDirectoriesConfig()
		common.Success(w, map[string]interface{}{"enabled": enabled, "directories": dirs})
	case http.MethodPost:
		var req struct {
			Enabled     bool     `json:"enabled"`
			Directories []string `json:"directories"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
			common.ErrorBadRequest(w, "Invalid request body", nil)
			return
		}
		dirs, enabled, err := h.service.SetScanDirectoriesConfig(req.Directories, req.Enabled)
		if err != nil {
			common.ErrorInternalServer(w, "Failed to save scan directories", map[string]interface{}{"error": err.Error()})
			return
		}
		common.Success(w, map[string]interface{}{"enabled": enabled, "directories": dirs})
	default:
		common.Error(w, http.StatusMethodNotAllowed, "Method not allowed", nil)
	}
}

func (h *AgentHandler) proxyIfNeeded(w http.ResponseWriter, r *http.Request) bool {
	if services.IsUserAgentRuntime() {
		return false
	}

	if err := h.proxyToUserAgent(w, r); err != nil {
		if r.URL.Path == "/api/agent/status" {
			common.Success(w, services.UserAgentOfflineStatus(err))
			return true
		}
		common.Error(w, common.CodeServiceUnavailable, "User agent process is not running", map[string]interface{}{
			"error": err.Error(),
			"url":   services.UserAgentBaseURL(),
		})
		return true
	}
	return true
}

// proxyDataIfNeeded 是数据面端点（会话列表/转录/终端回放）专用的代理入口：
// 与 proxyIfNeeded 行为一致，但走无硬超时的 dataClient 并施加
// agentProxyDataTimeout 期限，避免 12s 控制面 client 把冷扫描掐成空列表。
func (h *AgentHandler) proxyDataIfNeeded(w http.ResponseWriter, r *http.Request) bool {
	if services.IsUserAgentRuntime() {
		return false
	}

	ctx, cancel := context.WithTimeout(r.Context(), agentProxyDataTimeout)
	defer cancel()
	if err := h.proxyToUserAgentClient(ctx, w, r, h.dataClient); err != nil {
		common.Error(w, common.CodeServiceUnavailable, "User agent process is not running", map[string]interface{}{
			"error": err.Error(),
			"url":   services.UserAgentBaseURL(),
		})
		return true
	}
	return true
}

func (h *AgentHandler) proxyToUserAgent(w http.ResponseWriter, r *http.Request) error {
	return h.proxyToUserAgentClient(r.Context(), w, r, h.client)
}

func (h *AgentHandler) proxyToUserAgentClient(ctx context.Context, w http.ResponseWriter, r *http.Request, client *http.Client) error {
	target := services.UserAgentBaseURL() + r.URL.RequestURI()
	req, err := http.NewRequestWithContext(ctx, r.Method, target, r.Body)
	if err != nil {
		return err
	}
	copyAgentProxyHeaders(req.Header, r.Header)
	req.Header.Set("X-Aliang-Agent-Proxy", "dashboard")
	req.Header.Del(services.AgentForwardedAuthorizationHeader)
	req.Header.Del(services.AgentForwardedUserKeyHeader)
	if authHeader := services.CurrentAgentRegisterAuthorizationHeader(); authHeader != "" {
		req.Header.Set(services.AgentForwardedAuthorizationHeader, authHeader)
	}
	if userKey := services.CurrentAgentRegisterUserKey(); userKey != "" {
		req.Header.Set(services.AgentForwardedUserKeyHeader, userKey)
	}

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	copyAgentProxyHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	if _, err := io.Copy(w, resp.Body); err != nil {
		return fmt.Errorf("copy user agent response: %w", err)
	}
	return nil
}

func copyAgentProxyHeaders(dst http.Header, src http.Header) {
	for key, values := range src {
		if strings.EqualFold(key, "Host") {
			continue
		}
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

func writeAgentServiceError(w http.ResponseWriter, message string, err error) {
	details := map[string]interface{}{"error": err.Error()}
	errText := strings.ToLower(err.Error())
	switch {
	case strings.Contains(errText, "returned 401") || strings.Contains(errText, "missing_bearer_token") || strings.Contains(errText, "log in before"):
		common.Error(w, common.CodeUnauthorized, message, details)
	case strings.Contains(errText, "returned 403"):
		common.Error(w, common.CodeForbidden, message, details)
	case strings.Contains(errText, "returned 404"):
		common.Error(w, common.CodeNotFound, message, details)
	case strings.Contains(errText, "returned 409") || strings.Contains(errText, "already_bound"):
		common.Error(w, common.CodeConflict, message, details)
	default:
		common.ErrorInternalServer(w, message, details)
	}
}

// HandleSessionEvent applies a forwarded session transition (from the dashboard
// authority) to the user-agent connection lifecycle.
// POST /api/agent/session-event {to, reason}
func (h *AgentHandler) HandleSessionEvent(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		common.Error(w, http.StatusMethodNotAllowed, "Method not allowed", nil)
		return
	}
	var req struct {
		To     string `json:"to"`
		Reason string `json:"reason"`
	}
	if err := common.DecodeRequest(r, &req); err != nil {
		common.ErrorBadRequest(w, "Invalid request format", nil)
		return
	}
	h.service.ApplySessionEvent(req.To, req.Reason)
	common.Success(w, map[string]interface{}{"status": "ok"})
}
