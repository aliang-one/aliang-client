package services

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"aliang.one/nursorgate/common/logger"
	auth "aliang.one/nursorgate/processor/auth"
)

// sessionEventAgentAction maps a forwarded SessionEvent to the agent-side
// connection action. Pure (unit-tested).
//
//   - active                       -> reconnect (ensure WS up; idempotent)
//   - hard_invalid (any reason)    -> disable  (drop JWT context + WS)
//   - unauthenticated + logout     -> disable  (explicit local session end)
//   - soft_expired / other unauthenticated -> none (recovery / idle handled elsewhere)
func sessionEventAgentAction(to, reason string) string {
	normalizedState := strings.ToLower(strings.TrimSpace(to))
	normalizedReason := strings.ToLower(strings.TrimSpace(reason))
	switch normalizedState {
	case "active":
		return "reconnect"
	case "hard_invalid":
		return "disable"
	case "unauthenticated":
		if normalizedReason == string(auth.ReasonLogout) {
			return "disable"
		}
	default:
		return "none"
	}
	return "none"
}

// ApplySessionEvent applies a forwarded session transition to the local
// (user-agent-process) agent connection lifecycle. Called from the
// /api/agent/session-event handler in the user-agent subprocess.
func (s *AgentService) ApplySessionEvent(to, reason string) {
	switch sessionEventAgentAction(to, reason) {
	case "reconnect":
		if err := s.EnsureRemoteConnection(); err != nil {
			logger.Warn(fmt.Sprintf("session-event reconnect failed: %v", err))
		}
	case "disable":
		s.DisableWithReason(reason)
	}
}

// ForwardSessionEventToUserAgent is the dashboard-side authority listener that
// pushes each transition to the user-agent subprocess over the unified
// /api/agent/session-event channel. Best-effort; no-op inside the user-agent
// runtime (avoids self-forwarding). This converges cross-process identity
// fan-out onto a single structured channel.
func ForwardSessionEventToUserAgent(e auth.SessionEvent) {
	if IsUserAgentRuntime() {
		return
	}
	body, err := json.Marshal(map[string]string{"to": e.To.String(), "reason": string(e.Reason)})
	if err != nil {
		return
	}
	endpoint := strings.TrimRight(localUserAgentBaseURL(), "/") + "/api/agent/session-event"
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	timeout := agentHTTPTimeout
	if sessionEventAgentAction(e.To.String(), string(e.Reason)) == "disable" {
		timeout = agentLogoutTimeout
	}
	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		logger.Debug(fmt.Sprintf("forward session-event to user-agent failed (best-effort): %v", err))
		return
	}
	_ = resp.Body.Close()
}

func init() {
	// Cross-process fan-out: every dashboard authority transition is forwarded
	// to the user-agent subprocess so its connection lifecycle follows identity.
	auth.SubscribeGlobal(ForwardSessionEventToUserAgent)
}
