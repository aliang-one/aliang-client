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

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/common/logger"
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
		return errors.New("device token is not available; register this device first")
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
			if s.shouldPreserveDisabledStatus() {
				return
			}
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

		s.setActiveRemoteConnection(conn)
		s.setRemoteConnectionState(true, "online", "")
		logger.Info(fmt.Sprintf("[AGENT-BOOT] remote_connection connected endpoint=%s", sanitizeAgentEndpoint(wsURL)))

		if err := s.runRemoteAgentSession(conn); err != nil {
			logger.Warn(fmt.Sprintf("[AGENT-BOOT] remote_connection session_ended status=error error=%v", err))
			if _, shouldRun := s.remoteConnectionSnapshot(); shouldRun {
				s.setRemoteConnectionState(false, "disconnected", err.Error())
			}
		} else {
			logger.Info("[AGENT-BOOT] remote_connection session_ended status=clean")
			if _, shouldRun := s.remoteConnectionSnapshot(); shouldRun {
				s.setRemoteConnectionState(false, "offline", "")
			}
		}

		s.clearActiveRemoteConnection(conn)
		_ = conn.Close()
		time.Sleep(2 * time.Second)
	}
}

func (s *AgentService) setActiveRemoteConnection(conn *websocket.Conn) {
	s.wsMu.Lock()
	defer s.wsMu.Unlock()
	s.wsConn = conn
	s.wsConnected = true
}

func (s *AgentService) clearActiveRemoteConnection(conn *websocket.Conn) {
	s.wsMu.Lock()
	defer s.wsMu.Unlock()
	if s.wsConn == conn {
		s.wsConn = nil
	}
	s.wsConnected = false
}

func (s *AgentService) forceDisconnectRemote(reason string) {
	s.wsMu.Lock()
	conn := s.wsConn
	s.wsConn = nil
	s.wsConnected = false
	s.wsMu.Unlock()

	if conn != nil {
		logger.Info(fmt.Sprintf("[AGENT-BOOT] remote_connection force_close reason=%s", normalizeAgentDisableReason(reason)))
		_ = conn.Close()
	}
	s.terminal.closeAll()
	s.ai.closeAll()
}

func (s *AgentService) shouldPreserveDisabledStatus() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch normalizeAgentDisableReason(s.state.LastSyncStatus) {
	case "disabled", "logout", "auth_expired", "device_token_invalid", "device_unbound":
		return true
	default:
		return false
	}
}

func (s *AgentService) runRemoteAgentSession(conn *websocket.Conn) error {
	var writeMu sync.Mutex
	writeJSON := func(payload interface{}) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return conn.WriteJSON(payload)
	}

	if err := s.sendAgentHello(writeJSON, "connect"); err != nil {
		return err
	}

	defer s.terminal.closeAll()
	defer s.ai.closeAll()

	done := make(chan struct{})
	defer close(done)
	go func() {
		heartbeatTicker := time.NewTicker(10 * time.Second)
		defer heartbeatTicker.Stop()
		inventoryTicker := time.NewTicker(time.Minute)
		defer inventoryTicker.Stop()
		for {
			select {
			case <-heartbeatTicker.C:
				_ = writeJSON(map[string]interface{}{
					"type":      models.AgentEventHeartbeat,
					"device_id": s.currentDeviceID(),
					"ts":        time.Now().UnixMilli(),
					"load":      collectAgentLoadSnapshot(),
				})
			case <-inventoryTicker.C:
				if err := s.sendAgentHello(writeJSON, "periodic"); err != nil {
					logger.Warn(fmt.Sprintf("[AGENT-BOOT] remote_connection periodic_hello_failed error=%v", err))
					return
				}
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

func (s *AgentService) sendAgentHello(writeJSON func(interface{}) error, reason string) error {
	hello := s.agentHelloPayload()
	projectCount := 0
	if projects, ok := hello["projects"].([]models.AgentProject); ok {
		projectCount = len(projects)
	}
	vibeSessionCount := 0
	if sessions, ok := hello["vibe_sessions"].([]models.AgentVibeSession); ok {
		vibeSessionCount = len(sessions)
	}
	dirCount := 0
	if dirs, ok := hello["authorized_directories"].([]string); ok {
		dirCount = len(dirs)
	}
	logger.Info(fmt.Sprintf("[AGENT-BOOT] remote_connection sending_hello reason=%s device_id=%s platform=%s projects=%d vibe_sessions=%d dirs=%d",
		strings.TrimSpace(reason),
		hello["device_id"],
		hello["platform"],
		projectCount,
		vibeSessionCount,
		dirCount,
	))
	if err := writeJSON(hello); err != nil {
		return err
	}
	logger.Info(fmt.Sprintf("[AGENT-BOOT] remote_connection hello_sent reason=%s", strings.TrimSpace(reason)))
	return nil
}

func (s *AgentService) handleRemoteAgentMessage(msg map[string]interface{}, writeJSON func(interface{}) error) {
	msgType := strings.TrimSpace(fmt.Sprint(msg["type"]))
	if remoteAgentMessageRequiresEnabledDevice(msgType) && !s.remoteControlAllowed() {
		_ = writeJSON(map[string]interface{}{
			"type":  models.AgentEventError,
			"error": "agent mode is disabled or disconnected",
		})
		s.forceDisconnectRemote("disabled")
		return
	}
	switch msgType {
	case models.AgentEventRegistered:
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
	case models.AgentEventHeartbeatAck:
		s.setRemoteConnectionState(true, "online", "")
	case models.AgentEventDeviceUnbound:
		logger.Warn("[AGENT-BOOT] remote_connection device_unbound")
		s.DisableWithReason("device_unbound")
	case models.AgentEventDeviceSettings:
		logger.Info("[AGENT-BOOT] remote_connection settings_updated")
		s.applyRemoteDeviceSettings(msg)
	case models.AgentEventProjectDetail, models.AgentEventAISessionDetail, models.AgentEventFileList, models.AgentEventFileRead:
		s.setRemoteConnectionState(true, "online", "")
		go handleAgentDetailMessage(msg, writeJSON)
	case models.AgentEventTerminalCreate:
		s.setRemoteConnectionState(true, "online", "")
		if !s.remoteTerminalEnabled() {
			_ = writeJSON(agentTerminalErrorPayload(remoteString(msg, "session_id"), errors.New("remote terminal is disabled for this device")))
			return
		}
		s.terminal.create(msg, writeJSON)
	case models.AgentEventTerminalInput:
		s.setRemoteConnectionState(true, "online", "")
		if !s.remoteTerminalEnabled() {
			_ = writeJSON(agentTerminalErrorPayload(remoteString(msg, "session_id"), errors.New("remote terminal is disabled for this device")))
			return
		}
		s.terminal.write(msg, writeJSON)
	case models.AgentEventTerminalResize:
		s.setRemoteConnectionState(true, "online", "")
		if !s.remoteTerminalEnabled() {
			_ = writeJSON(agentTerminalErrorPayload(remoteString(msg, "session_id"), errors.New("remote terminal is disabled for this device")))
			return
		}
		s.terminal.resize(msg, writeJSON)
	case models.AgentEventTerminalClose:
		s.setRemoteConnectionState(true, "online", "")
		s.terminal.close(msg, writeJSON)
	case models.AgentEventAISessionCreate, models.AgentEventAIMessage, models.AgentEventAIApprovalResponse, models.AgentEventAIStop, models.AgentEventAISessionClose:
		s.setRemoteConnectionState(true, "online", "")
		switch msgType {
		case models.AgentEventAISessionCreate:
			if !s.aiControlEnabled() {
				_ = writeJSON(agentAIErrorPayload(remoteString(msg, "session_id"), "", errors.New("AI control is disabled for this device")))
				return
			}
			s.ai.create(msg, writeJSON)
		case models.AgentEventAIMessage:
			if !s.aiControlEnabled() {
				_ = writeJSON(agentAIErrorPayload(remoteString(msg, "session_id"), remoteString(msg, "message_id"), errors.New("AI control is disabled for this device")))
				return
			}
			s.ai.message(msg, writeJSON)
		case models.AgentEventAIApprovalResponse:
			if !s.aiControlEnabled() {
				_ = writeJSON(agentAIErrorPayload(remoteString(msg, "session_id"), remoteString(msg, "message_id"), errors.New("AI control is disabled for this device")))
				return
			}
			s.ai.approval(msg, writeJSON)
		case models.AgentEventAIStop:
			s.ai.stop(msg, writeJSON)
		case models.AgentEventAISessionClose:
			s.ai.close(msg, writeJSON)
		}
	default:
		_ = writeJSON(map[string]interface{}{
			"type":  models.AgentEventError,
			"error": fmt.Sprintf("unsupported remote agent event type: %s", msgType),
		})
	}
}

func remoteAgentMessageRequiresEnabledDevice(msgType string) bool {
	switch msgType {
	case models.AgentEventProjectDetail,
		models.AgentEventAISessionDetail,
		models.AgentEventFileList,
		models.AgentEventFileRead,
		models.AgentEventTerminalCreate,
		models.AgentEventTerminalInput,
		models.AgentEventTerminalResize,
		models.AgentEventTerminalClose,
		models.AgentEventAISessionCreate,
		models.AgentEventAIMessage,
		models.AgentEventAIApprovalResponse,
		models.AgentEventAIStop,
		models.AgentEventAISessionClose:
		return true
	default:
		return false
	}
}

func (s *AgentService) remoteControlAllowed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state.Enabled && strings.TrimSpace(s.state.DeviceToken) != ""
}

// DispatchLocalAI routes a single in-process AI protocol event to the agent AI
// manager. It is the local counterpart of the AI branch in
// handleRemoteAgentMessage: the in-app chat WebSocket uses it so the web UI can
// drive the local Claude Code / Codex headless run and receive streaming
// ai.run.started / ai.delta / ai.done events without round-tripping through the
// remote agent server. writeJSON receives every event the manager emits.
func (s *AgentService) DispatchLocalAI(msg map[string]interface{}, writeJSON func(interface{}) error) {
	if writeJSON == nil || msg == nil {
		return
	}
	msgType := strings.TrimSpace(fmt.Sprint(msg["type"]))
	switch msgType {
	case models.AgentEventAISessionCreate:
		s.ai.create(msg, writeJSON)
	case models.AgentEventAIMessage:
		s.ai.message(msg, writeJSON)
	case models.AgentEventAIApprovalResponse:
		s.ai.approval(msg, writeJSON)
	case models.AgentEventAIStop:
		s.ai.stop(msg, writeJSON)
	case models.AgentEventAISessionClose:
		s.ai.close(msg, writeJSON)
	default:
		_ = writeJSON(map[string]interface{}{
			"type":  models.AgentEventAIError,
			"error": fmt.Sprintf("unsupported local AI event type: %s", msgType),
		})
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

	snapshot := collectAgentSyncSnapshot()
	return map[string]interface{}{
		"type":                   models.AgentEventHello,
		"protocol_version":       models.AgentProtocolVersion,
		"device_id":              deviceID,
		"unique_code":            uniqueCode,
		"device_name":            snapshot.DeviceName,
		"platform":               snapshot.Platform,
		"agent_version":          snapshot.AgentVersion,
		"host":                   snapshot.Host,
		"capabilities":           snapshot.Capabilities,
		"tools":                  snapshot.Tools,
		"history":                snapshot.History,
		"projects":               snapshot.Projects,
		"vibe_sessions":          snapshot.VibeSessions,
		"authorized_directories": snapshot.AuthorizedDirectories,
		"collected_at":           snapshot.CollectedAt,
		"started_at":             time.Now().UTC().Format(time.RFC3339),
		"load":                   collectAgentLoadSnapshot(),
	}
}

func (s *AgentService) remoteTerminalEnabled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.state.Enabled || strings.TrimSpace(s.state.DeviceToken) == "" {
		return false
	}
	if s.state.Device == nil {
		return true
	}
	return s.state.Device.RemoteTerminalEnabled
}

func (s *AgentService) aiControlEnabled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.state.Enabled || strings.TrimSpace(s.state.DeviceToken) == "" {
		return false
	}
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
	parsed.Path = models.AgentWSEndpoint
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
	caps := []string{"terminal", "terminal_stream", "file_read", "file_diff", "command_launch"}
	if agentNativePTYSupported() {
		caps = append(caps, "terminal_pty", "terminal_resize")
	} else {
		caps = append(caps, "terminal_pipe")
	}
	caps = append(caps, agentAICapabilities()...)
	return caps
}

func agentMessageJSON(payload interface{}) string {
	raw, err := json.Marshal(payload)
	if err != nil {
		return "{}"
	}
	return string(raw)
}
