package handlers

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"aliang.one/nursorgate/app/http/common"
	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/app/http/services"
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
	common.Success(w, h.service.Disable())
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
