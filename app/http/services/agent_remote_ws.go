package services

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"runtime"
	"strings"
	"sync"
	"time"

	"aliang.one/nursorgate/common/logger"
	"aliang.one/nursorgate/common/version"
	"aliang.one/nursorgate/processor/config"
	"github.com/gorilla/websocket"
)

func (s *AgentService) EnsureRemoteConnection() error {
	s.mu.Lock()
	s.ensureDeviceIdentityLocked()
	token := strings.TrimSpace(s.state.DeviceToken)
	enabled := s.state.Enabled
	s.mu.Unlock()
	if token == "" {
		logger.Info("[AGENT-BOOT] remote_connection skipped reason=no_device_token")
		return errors.New("device token is not available; pair this device first")
	}
	if !enabled {
		logger.Info("[AGENT-BOOT] remote_connection skipped reason=agent_disabled")
		return errors.New("agent mode is disabled")
	}

	s.wsMu.Lock()
	if s.wsConnected || s.wsConnecting {
		logger.Info(fmt.Sprintf("[AGENT-BOOT] remote_connection already_active connected=%t connecting=%t", s.wsConnected, s.wsConnecting))
		s.wsMu.Unlock()
		return nil
	}
	s.wsConnecting = true
	s.wsMu.Unlock()

	go s.remoteConnectionLoop()
	return nil
}

func (s *AgentService) remoteConnectionLoop() {
	defer func() {
		s.wsMu.Lock()
		s.wsConnecting = false
		s.wsMu.Unlock()
	}()

	for {
		token, shouldRun := s.remoteConnectionSnapshot()
		if !shouldRun {
			logger.Info("[AGENT-BOOT] remote_connection loop_stop reason=disabled_or_missing_token")
			s.setRemoteConnectionState(false, "offline", "")
			return
		}

		wsURL, err := currentAgentWebSocketURL(token)
		if err != nil {
			logger.Warn(fmt.Sprintf("[AGENT-BOOT] remote_connection ws_url_failed error=%v", err))
			s.setRemoteConnectionState(false, "connect_failed", err.Error())
			time.Sleep(2 * time.Second)
			continue
		}

		logger.Info(fmt.Sprintf("[AGENT-BOOT] remote_connection dialing endpoint=%s", sanitizeAgentEndpoint(wsURL)))
		conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
		if err != nil {
			logger.Warn(fmt.Sprintf("[AGENT-BOOT] remote_connection dial_failed endpoint=%s error=%v", sanitizeAgentEndpoint(wsURL), err))
			s.setRemoteConnectionState(false, "connect_failed", err.Error())
			time.Sleep(2 * time.Second)
			continue
		}

		s.wsMu.Lock()
		s.wsConnected = true
		s.wsMu.Unlock()
		s.setRemoteConnectionState(true, "online", "")
		logger.Info(fmt.Sprintf("[AGENT-BOOT] remote_connection connected endpoint=%s", sanitizeAgentEndpoint(wsURL)))

		if err := s.runRemoteAgentSession(conn); err != nil {
			logger.Warn(fmt.Sprintf("[AGENT-BOOT] remote_connection session_ended status=error error=%v", err))
			s.setRemoteConnectionState(false, "disconnected", err.Error())
		} else {
			logger.Info("[AGENT-BOOT] remote_connection session_ended status=clean")
			s.setRemoteConnectionState(false, "offline", "")
		}

		s.wsMu.Lock()
		if s.wsConnected {
			s.wsConnected = false
		}
		s.wsMu.Unlock()
		_ = conn.Close()
		time.Sleep(2 * time.Second)
	}
}

func (s *AgentService) runRemoteAgentSession(conn *websocket.Conn) error {
	var writeMu sync.Mutex
	writeJSON := func(payload interface{}) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return conn.WriteJSON(payload)
	}

	hello := s.agentHelloPayload()
	logger.Info(fmt.Sprintf("[AGENT-BOOT] remote_connection sending_hello device_id=%s platform=%s", hello["device_id"], hello["platform"]))
	if err := writeJSON(hello); err != nil {
		return err
	}
	logger.Info("[AGENT-BOOT] remote_connection hello_sent")

	defer s.terminal.closeAll()
	defer s.ai.closeAll()

	done := make(chan struct{})
	defer close(done)
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				_ = writeJSON(map[string]interface{}{
					"type":      "agent.heartbeat",
					"device_id": s.currentDeviceID(),
					"ts":        time.Now().UnixMilli(),
				})
			case <-done:
				return
			}
		}
	}()

	for {
		var msg map[string]interface{}
		if err := conn.ReadJSON(&msg); err != nil {
			return err
		}
		s.handleRemoteAgentMessage(msg, writeJSON)
	}
}

func (s *AgentService) handleRemoteAgentMessage(msg map[string]interface{}, writeJSON func(interface{}) error) {
	msgType := strings.TrimSpace(fmt.Sprint(msg["type"]))
	switch msgType {
	case "agent.registered":
		deviceID := strings.TrimSpace(fmt.Sprint(msg["device_id"]))
		logger.Info(fmt.Sprintf("[AGENT-BOOT] remote_connection registered_ack device_id=%s", deviceID))
		s.mu.Lock()
		if deviceID != "" {
			s.state.DeviceID = deviceID
			if s.state.Device != nil {
				s.state.Device.ID = deviceID
				s.state.Device.DeviceID = deviceID
			}
		}
		s.setRemoteConnectionStateLocked(true, "online", "")
		_ = s.saveStateLocked()
		s.mu.Unlock()
	case "agent.heartbeat.ack":
		s.setRemoteConnectionState(true, "online", "")
	case "device.unbound":
		logger.Warn("[AGENT-BOOT] remote_connection device_unbound")
		s.mu.Lock()
		s.state.Enabled = false
		s.state.Registered = false
		s.state.RemoteConnected = false
		s.state.Device = nil
		s.state.DeviceToken = ""
		s.state.LastSyncStatus = "unbound"
		s.state.LastSyncMessage = "Device was unbound by the agent server."
		_ = s.saveStateLocked()
		s.mu.Unlock()
	case "device.settings.updated":
		logger.Info("[AGENT-BOOT] remote_connection settings_updated")
		s.applyRemoteDeviceSettings(msg)
	case "terminal.create":
		s.setRemoteConnectionState(true, "online", "")
		if !s.remoteTerminalEnabled() {
			_ = writeJSON(agentTerminalErrorPayload(remoteString(msg, "session_id"), errors.New("remote terminal is disabled for this device")))
			return
		}
		s.terminal.create(msg, writeJSON)
	case "terminal.input":
		s.setRemoteConnectionState(true, "online", "")
		if !s.remoteTerminalEnabled() {
			_ = writeJSON(agentTerminalErrorPayload(remoteString(msg, "session_id"), errors.New("remote terminal is disabled for this device")))
			return
		}
		s.terminal.write(msg, writeJSON)
	case "terminal.resize":
		s.setRemoteConnectionState(true, "online", "")
		if !s.remoteTerminalEnabled() {
			_ = writeJSON(agentTerminalErrorPayload(remoteString(msg, "session_id"), errors.New("remote terminal is disabled for this device")))
			return
		}
		s.terminal.resize(msg, writeJSON)
	case "terminal.close":
		s.setRemoteConnectionState(true, "online", "")
		s.terminal.close(msg, writeJSON)
	case "ai.session.create", "ai.message", "ai.stop":
		s.setRemoteConnectionState(true, "online", "")
		switch msgType {
		case "ai.session.create":
			if !s.aiControlEnabled() {
				_ = writeJSON(agentAIErrorPayload(remoteString(msg, "session_id"), "", errors.New("AI control is disabled for this device")))
				return
			}
			s.ai.create(msg, writeJSON)
		case "ai.message":
			if !s.aiControlEnabled() {
				_ = writeJSON(agentAIErrorPayload(remoteString(msg, "session_id"), remoteString(msg, "message_id"), errors.New("AI control is disabled for this device")))
				return
			}
			s.ai.message(msg, writeJSON)
		case "ai.stop":
			s.ai.stop(msg, writeJSON)
		}
	}
}

func (s *AgentService) remoteConnectionSnapshot() (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	token := strings.TrimSpace(s.state.DeviceToken)
	return token, token != "" && s.state.Enabled
}

func (s *AgentService) setRemoteConnectionState(connected bool, status string, message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.setRemoteConnectionStateLocked(connected, status, message)
	_ = s.saveStateLocked()
}

func (s *AgentService) setRemoteConnectionStateLocked(connected bool, status string, message string) {
	s.state.RemoteConnected = connected
	s.state.Registered = connected || strings.TrimSpace(s.state.DeviceToken) != ""
	s.state.LastSyncAt = time.Now().UTC().Format(time.RFC3339)
	s.state.LastSyncStatus = status
	s.state.LastSyncMessage = message
	s.syncRuntimeDeviceStatusLocked()
	if s.state.Device != nil {
		if connected {
			s.state.Device.Status = "online"
		} else {
			s.state.Device.Status = "offline"
		}
		s.state.Device.LastSeenAt = s.state.LastSyncAt
	}
}

func (s *AgentService) agentHelloPayload() map[string]interface{} {
	s.mu.Lock()
	s.ensureDeviceIdentityLocked()
	deviceID := s.state.DeviceID
	uniqueCode := s.state.UniqueCode
	s.mu.Unlock()

	return map[string]interface{}{
		"type":          "agent.hello",
		"device_id":     deviceID,
		"unique_code":   uniqueCode,
		"device_name":   defaultAgentDeviceName(),
		"platform":      agentPlatform(),
		"agent_version": version.String(),
		"capabilities":  agentCapabilities(),
		"tools":         detectAgentTools(),
		"history":       collectAgentHistoryRoots(),
		"started_at":    time.Now().UTC().Format(time.RFC3339),
	}
}

func (s *AgentService) remoteTerminalEnabled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.Device == nil {
		return true
	}
	return s.state.Device.RemoteTerminalEnabled
}

func (s *AgentService) aiControlEnabled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.Device == nil {
		return true
	}
	return s.state.Device.AIControlEnabled
}

func (s *AgentService) currentDeviceID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureDeviceIdentityLocked()
	return s.state.DeviceID
}

func currentAgentWebSocketURL(token string) (string, error) {
	cfg := config.GetGlobalConfig()
	if cfg == nil || strings.TrimSpace(cfg.AgentBaseURL()) == "" {
		return "", errors.New("agent server is not configured")
	}
	parsed, err := url.Parse(cfg.AgentBaseURL())
	if err != nil {
		return "", err
	}
	switch parsed.Scheme {
	case "http":
		parsed.Scheme = "ws"
	case "https":
		parsed.Scheme = "wss"
	case "ws", "wss":
	default:
		return "", fmt.Errorf("unsupported agent server scheme: %s", parsed.Scheme)
	}
	if (parsed.Hostname() == "localhost" || parsed.Hostname() == "127.0.0.1") && parsed.Port() == "5174" {
		parsed.Host = parsed.Hostname() + ":4000"
	}
	parsed.Path = "/ws/agent"
	parsed.RawQuery = ""
	values := parsed.Query()
	values.Set("token", token)
	parsed.RawQuery = values.Encode()
	return parsed.String(), nil
}

func agentPlatform() string {
	return runtime.GOOS + "-" + runtime.GOARCH
}

func agentCapabilities() []string {
	return []string{"terminal", "ai_chat", "vibe_session", "file_read", "file_diff", "command_launch"}
}

func agentMessageJSON(payload interface{}) string {
	raw, err := json.Marshal(payload)
	if err != nil {
		return "{}"
	}
	return string(raw)
}
