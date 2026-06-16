package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"aliang.one/nursorgate/app/http/common"
	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/app/http/services"

	"github.com/gorilla/websocket"
)

type AgentHandler struct {
	service *services.AgentService
	client  *http.Client
}

func NewAgentHandler(service *services.AgentService) *AgentHandler {
	if service == nil {
		service = services.GetSharedAgentService()
	}
	return &AgentHandler{
		service: service,
		client:  &http.Client{Timeout: 12 * time.Second},
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

func (h *AgentHandler) proxyToUserAgent(w http.ResponseWriter, r *http.Request) error {
	target := services.UserAgentBaseURL() + r.URL.RequestURI()
	req, err := http.NewRequestWithContext(r.Context(), r.Method, target, r.Body)
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

	resp, err := h.client.Do(req)
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
