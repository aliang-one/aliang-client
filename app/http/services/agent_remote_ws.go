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
		return errors.New("device token is not available; pair this device first")
	}
	if !enabled {
		return errors.New("agent mode is disabled")
	}

	s.wsMu.Lock()
	if s.wsConnected || s.wsConnecting {
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
			s.setRemoteConnectionState(false, "offline", "")
			return
		}

		wsURL, err := currentAgentWebSocketURL(token)
		if err != nil {
			s.setRemoteConnectionState(false, "connect_failed", err.Error())
			time.Sleep(2 * time.Second)
			continue
		}

		conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
		if err != nil {
			s.setRemoteConnectionState(false, "connect_failed", err.Error())
			time.Sleep(2 * time.Second)
			continue
		}

		s.wsMu.Lock()
		s.wsConnected = true
		s.wsMu.Unlock()
		s.setRemoteConnectionState(true, "online", "")

		if err := s.runRemoteAgentSession(conn); err != nil {
			s.setRemoteConnectionState(false, "disconnected", err.Error())
		} else {
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

	if err := writeJSON(s.agentHelloPayload()); err != nil {
		return err
	}

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
		s.handleRemoteAgentMessage(msg)
	}
}

func (s *AgentService) handleRemoteAgentMessage(msg map[string]interface{}) {
	msgType := strings.TrimSpace(fmt.Sprint(msg["type"]))
	switch msgType {
	case "agent.registered":
		deviceID := strings.TrimSpace(fmt.Sprint(msg["device_id"]))
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
	case "terminal.create", "terminal.input", "terminal.resize", "terminal.close", "ai.session.create", "ai.message", "ai.stop":
		s.setRemoteConnectionState(true, "online", "Remote command handling is not implemented in this desktop agent build yet.")
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
	return map[string]interface{}{
		"type":          "agent.hello",
		"device_id":     s.currentDeviceID(),
		"device_name":   defaultAgentDeviceName(),
		"platform":      agentPlatform(),
		"agent_version": version.String(),
		"capabilities":  agentCapabilities(),
	}
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
