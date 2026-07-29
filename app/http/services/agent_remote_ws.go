package services

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
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
	enabled := s.state.Enabled
	registered := s.state.Registered
	authHeader := s.effectiveUserAuthorizationLocked("")
	s.mu.Unlock()
	if !registered {
		logger.Info("[AGENT-BOOT] remote_connection skipped reason=device_not_registered")
		return errors.New("device is not registered")
	}
	if strings.TrimSpace(authHeader) == "" {
		logger.Info("[AGENT-BOOT] remote_connection skipped reason=no_user_authorization")
		return errors.New("user authorization is not available")
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
		// The remote connection is being torn down for good (disabled / no
		// authorization): clean up AI sessions deliberately kept alive across
		// transient reconnects inside runRemoteAgentSession.
		s.ai.closeAll()
		s.wsMu.Lock()
		s.wsConnecting = false
		s.wsMu.Unlock()
	}()

	for {
		identity, shouldRun := s.remoteConnectionSnapshot()
		if !shouldRun {
			if s.shouldPreserveDisabledStatus() {
				return
			}
			logger.Info("[AGENT-BOOT] remote_connection loop_stop reason=disabled_unregistered_or_missing_auth")
			s.setRemoteConnectionState(false, "offline", "")
			return
		}

		wsURL, err := currentAgentWebSocketURL()
		if err != nil {
			logger.Warn(fmt.Sprintf("[AGENT-BOOT] remote_connection ws_url_failed error=%v", err))
			s.setRemoteConnectionState(false, "connect_failed", err.Error())
			time.Sleep(2 * time.Second)
			continue
		}

		logger.Info(fmt.Sprintf("[AGENT-BOOT] remote_connection dialing endpoint=%s", sanitizeAgentEndpoint(wsURL)))
		headers := http.Header{}
		headers.Set("Authorization", identity.authorization)
		headers.Set("X-Aliang-Device-ID", identity.deviceID)
		conn, resp, err := websocket.DefaultDialer.Dial(wsURL, headers)
		if err != nil {
			if resp != nil && resp.StatusCode == http.StatusUnauthorized {
				// The websocket is authenticated with the same user JWT as the
				// register/status calls. A handshake 401 is therefore a real auth
				// transition: stop the Agent immediately and let the session owner
				// run refresh/hard-invalid handling instead of retrying forever.
				s.disableWithReasonMessage("auth_expired", "Agent server rejected the user authorization during websocket handshake.")
				if !IsUserAgentRuntime() {
					agentAuthRejectedHandler()
				}
				return
			}
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

// setCurrentRemoteWriter publishes the live remote-connection writer so that
// AI runs (which outlive a single socket) keep streaming after a reconnect.
// Pass nil to detach (writes will fail until the next connection publishes one).
func (s *AgentService) setCurrentRemoteWriter(w agentTerminalWriter) {
	if w == nil {
		s.remoteWriter.Store(nil)
		return
	}
	s.remoteWriter.Store(&w)
}

func (s *AgentService) clearCurrentRemoteWriter() {
	s.remoteWriter.Store(nil)
}

// currentRemoteWriter returns the live remote writer, or nil when disconnected.
func (s *AgentService) currentRemoteWriter() agentTerminalWriter {
	p := s.remoteWriter.Load()
	if p == nil {
		return nil
	}
	return *p
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
	if s.tunnel != nil {
		s.tunnel.Stop()
	}
	s.terminal.closeAll()
	s.ai.closeAll()
}

func (s *AgentService) shouldPreserveDisabledStatus() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch normalizeAgentDisableReason(s.state.LastSyncStatus) {
	case "disabled", "logout", "auth_expired", "refresh_invalid", "soft_expiry_timeout", "revoked", "device_unbound":
		return true
	default:
		return false
	}
}

// Remote-WS liveness tuning. The connection rides over a NAT tunnel, so a
// dead/half-open socket can persist undetected: without an enforced deadline
// the agent believes itself online while PhoneServer sees no traffic (and vice
// versa). These keep the connection honest. Package vars so tests can shrink
// them to exercise the dead-peer path quickly.
var (
	agentRemoteHeartbeatInterval = 10 * time.Second
	agentRemotePingInterval      = 30 * time.Second
	agentRemoteReadWindow        = 90 * time.Second
	agentRemoteWriteTimeout      = 10 * time.Second
)

func (s *AgentService) runRemoteAgentSession(conn *websocket.Conn) error {
	// Write deadline: a stuck write (peer stopped reading / NAT blackhole) must
	// fail fast instead of blocking forever while holding writeMu.
	var writeMu sync.Mutex
	rawWriter := func(payload interface{}) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		_ = conn.SetWriteDeadline(time.Now().Add(agentRemoteWriteTimeout))
		return conn.WriteJSON(payload)
	}
	// Publish this connection's writer so AI runs that outlive the socket keep
	// streaming after a reconnect (they emit through currentRemoteWriter()).
	s.setCurrentRemoteWriter(rawWriter)
	defer s.clearCurrentRemoteWriter()
	// writeJSON is an indirection: it always writes to the *current* live writer,
	// not the closure captured here, so a reconnect reattaches in-flight runs.
	writeJSON := func(payload interface{}) error {
		w := s.currentRemoteWriter()
		if w == nil {
			return errors.New("remote agent writer unavailable (disconnected)")
		}
		return w(payload)
	}

	// Read liveness: enforce a read deadline that is refreshed by inbound pongs
	// (and any application message). If the peer goes silent the deadline fires
	// -> ReadJSON errors -> the session ends and remoteConnectionLoop reconnects,
	// instead of hanging on a dead socket.
	resetReadDeadline := func() {
		_ = conn.SetReadDeadline(time.Now().Add(agentRemoteReadWindow))
	}
	resetReadDeadline()
	conn.SetPongHandler(func(string) error {
		resetReadDeadline()
		return nil
	})

	if err := s.sendAgentHello(writeJSON, "connect"); err != nil {
		return err
	}
	// Re-send terminal events whose ACK was lost across a socket/process restart.
	// Server handling is idempotent by run_id + event_seq.
	s.ai.replayPendingTerminals(writeJSON)

	defer s.terminal.closeAll()
	// NOTE: AI sessions are intentionally NOT closed here. A transient WS
	// disconnect (conn read error) must not kill locally-running AI CLIs; they
	// survive and re-stream over the next connection via currentRemoteWriter().
	// True shutdown paths (remoteConnectionLoop exit, forceDisconnectRemote)
	// close AI sessions explicitly.

	// Reconcile on (re)connect: re-surface approvals we are still waiting on so
	// the server can re-deliver decided ones and confirm still-pending ones.
	s.ai.emitApprovalSync(writeJSON)

	done := make(chan struct{})
	defer close(done)

	// Ping goroutine, independent of the application ticker below: liveness
	// probes must never be starved by (or starve) heartbeats/inventory writes.
	// WriteControl is safe to call concurrently with WriteJSON and carries its
	// own deadline, so it does not contend on writeMu.
	go func() {
		pingTicker := time.NewTicker(agentRemotePingInterval)
		defer pingTicker.Stop()
		for {
			select {
			case <-pingTicker.C:
				if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(agentRemoteWriteTimeout)); err != nil {
					return
				}
			case <-done:
				return
			}
		}
	}()

	go func() {
		heartbeatTicker := time.NewTicker(agentRemoteHeartbeatInterval)
		defer heartbeatTicker.Stop()
		inventoryTicker := time.NewTicker(time.Minute)
		defer inventoryTicker.Stop()
		// Periodically re-surface still-pending approvals: a delivery retry nudge
		// and a dialogue-liveness heartbeat for the server.
		approvalSyncTicker := time.NewTicker(time.Minute)
		defer approvalSyncTicker.Stop()
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
				// Log-only on failure: a periodic hello failure must NOT tear down
				// the heartbeat/liveness stream. Returning here (the old behavior)
				// silently starved PhoneServer of heartbeats and tripped its own
				// liveness timer, dropping an otherwise-healthy connection.
				if err := s.sendAgentHello(writeJSON, "periodic"); err != nil {
					logger.Warn(fmt.Sprintf("[AGENT-BOOT] remote_connection periodic_hello_failed error=%v", err))
				}
			case <-s.sessionRefreshSig:
				// Token was refreshed (new exp). Push the current access_token so
				// PhoneServer advances this device's userTokenExp without a reconnect.
				accessToken := s.currentAccessToken()
				if accessToken == "" {
					continue
				}
				if err := writeJSON(map[string]interface{}{
					"type":         "agent.session.refresh",
					"access_token": accessToken,
				}); err != nil {
					// Log-only (see inventory): don't kill the heartbeat stream.
					logger.Warn(fmt.Sprintf("[AGENT-BOOT] remote_connection session_refresh_send_failed error=%v", err))
				}
			case <-approvalSyncTicker.C:
				s.ai.emitApprovalSync(writeJSON)
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
		// Any inbound traffic confirms the peer is alive; refresh the deadline so
		// a chatty-but-pong-less peer isn't misclassified as dead.
		resetReadDeadline()
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
		logger.Info(fmt.Sprintf("[AGENT-BOOT] remote_connection registered_ack server_device_id=%s", strings.TrimSpace(fmt.Sprint(msg["device_id"]))))
		s.mu.Lock()
		// device_id is permanent and client-owned: ignore the id the server
		// acknowledges and re-pin our own installation identity.
		s.pinPermanentDeviceIDLocked()
		s.setRemoteConnectionStateLocked(true, "online", "")
		_ = s.saveStateLocked()
		s.mu.Unlock()
	case models.AgentEventHeartbeatAck:
		s.setRemoteConnectionState(true, "online", "")
		// Retry durable terminals while the socket remains healthy. A prior
		// delivery may have reached Server while its database was unavailable;
		// waiting for another reconnect would otherwise leave the run stuck.
		s.ai.replayPendingTerminals(writeJSON)
	case models.AgentEventAIRunEventAck:
		s.setRemoteConnectionState(true, "online", "")
		s.ai.acknowledgePendingTerminal(
			remoteString(msg, "run_id"),
			int64(remoteInt(msg, "accepted_seq", 0)),
		)
	case models.AgentEventGoalRunEventAck:
		s.setRemoteConnectionState(true, "online", "")
		if remoteString(msg, "result") == "accepted" {
			s.ai.acknowledgePendingTerminal(
				remoteString(msg, "goal_run_id"),
				int64(remoteInt(msg, "accepted_seq", 0)),
			)
		}
	case models.AgentEventDeviceUnbound:
		logger.Warn("[AGENT-BOOT] remote_connection device_unbound")
		s.DisableWithReason("device_unbound")
	case models.AgentEventDeviceSettings:
		logger.Info("[AGENT-BOOT] remote_connection settings_updated")
		s.applyRemoteDeviceSettings(msg)
	case models.AgentEventProjectSettings:
		s.applyRemoteProjectSettings(msg)
	case models.AgentEventTunnelConfigure:
		s.setRemoteConnectionState(true, "online", "")
		s.configureTunnel(msg, writeJSON)
	case models.AgentEventProjectDetail, models.AgentEventAISessionDetail, models.AgentEventFileList, models.AgentEventFileRead, models.AgentEventSlashCommandsList, "file.working_tree_diff":
		s.setRemoteConnectionState(true, "online", "")
		go handleAgentDetailMessageWithAI(msg, writeJSON, s.ai)
	case models.AgentEventGitStatus, models.AgentEventEnvInfo:
		s.setRemoteConnectionState(true, "online", "")
		go handleAgentEnvToolsMessage(msg, writeJSON)
	case models.AgentEventGoalPlan, models.AgentEventGoalVerify, models.AgentEventGoalContinue:
		s.setRemoteConnectionState(true, "online", "")
		if !s.aiControlEnabled() {
			_ = writeJSON(agentGoalErrorPayload(msg, errors.New("AI control is disabled for this device")))
			return
		}
		go handleAgentGoalMessage(msg, writeJSON, s.ai)
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
	case models.AgentEventAISessionCreate, models.AgentEventAIMessage, models.AgentEventAIRunStart, models.AgentEventAISteer, models.AgentEventAIApprovalResponse, models.AgentEventAIOptionResponse, models.AgentEventAIStop, models.AgentEventAISessionClose:
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
		case models.AgentEventAIRunStart:
			if !s.aiControlEnabled() {
				_ = writeJSON(agentAIErrorPayload(remoteString(msg, "session_id"), remoteString(msg, "message_id"), errors.New("AI control is disabled for this device")))
				return
			}
			s.ai.runStart(msg, writeJSON)
		case models.AgentEventAISteer:
			if !s.aiControlEnabled() {
				emitAgentAISteerAck(writeJSON, remoteString(msg, "session_id"), remoteString(msg, "message_id"), "error", "AI control is disabled for this device", "ai_control_disabled")
				return
			}
			s.ai.steer(msg, writeJSON)
		case models.AgentEventAIApprovalResponse:
			if !s.aiControlEnabled() {
				_ = writeJSON(agentAIErrorPayload(remoteString(msg, "session_id"), remoteString(msg, "message_id"), errors.New("AI control is disabled for this device")))
				return
			}
			s.ai.approval(msg, writeJSON)
		case models.AgentEventAIOptionResponse:
			if !s.aiControlEnabled() {
				_ = writeJSON(agentAIErrorPayload(remoteString(msg, "session_id"), remoteString(msg, "message_id"), errors.New("AI control is disabled for this device")))
				return
			}
			s.ai.optionResponse(msg, writeJSON)
		case models.AgentEventAIStop:
			s.ai.stop(msg, writeJSON)
		case models.AgentEventAISessionClose:
			s.ai.close(msg, writeJSON)
		}
	case models.AgentEventAIApprovalState, models.AgentEventAIApprovalRequestAck:
		// Server-side approval status / request-received ack: informational.
		// Touch liveness; delivery/retry is driven by ai.approval.ack.
		s.setRemoteConnectionState(true, "online", "")
	case models.AgentEventAIApprovalCancelled:
		// Server cancelled approvals (e.g. device was offline past grace): drop
		// the matching local waiters so a still-running CLI is told to give up.
		s.setRemoteConnectionState(true, "online", "")
		s.ai.cancelApprovals(msg)
	case models.AgentEventAIOptionCancelled:
		// Server cancelled an option (e.g. device offline past grace): drop the
		// local pendingOption so a later stray response is ignored.
		s.setRemoteConnectionState(true, "online", "")
		s.ai.cancelOptions(msg)
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
		models.AgentEventTunnelConfigure,
		models.AgentEventAISessionDetail,
		models.AgentEventFileList,
		models.AgentEventFileRead,
		"file.working_tree_diff",
		models.AgentEventSlashCommandsList,
		models.AgentEventGitStatus,
		models.AgentEventEnvInfo,
		models.AgentEventGoalPlan,
		models.AgentEventGoalVerify,
		models.AgentEventGoalContinue,
		models.AgentEventTerminalCreate,
		models.AgentEventTerminalInput,
		models.AgentEventTerminalResize,
		models.AgentEventTerminalClose,
		models.AgentEventAISessionCreate,
		models.AgentEventAIMessage,
		models.AgentEventAIRunStart,
		models.AgentEventAISteer,
		models.AgentEventAIApprovalResponse,
		models.AgentEventAIOptionResponse,
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
	return s.state.Enabled && s.state.Registered
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
	case models.AgentEventAISteer:
		s.ai.steer(msg, writeJSON)
	case models.AgentEventAIApprovalResponse:
		s.ai.approval(msg, writeJSON)
	case models.AgentEventAIStop:
		s.ai.stop(msg, writeJSON)
	case models.AgentEventAISessionClose:
		s.ai.close(msg, writeJSON)
	case models.AgentEventSlashCommandsList:
		// Slash-command discovery shares the local chat socket so the web UI can
		// offer `/` completion. It routes through the same detail handler as the
		// remote link and is provider-aware (claude vs codex).
		handleAgentDetailMessageWithAI(msg, writeJSON, s.ai)
	default:
		_ = writeJSON(map[string]interface{}{
			"type":  models.AgentEventAIError,
			"error": fmt.Sprintf("unsupported local AI event type: %s", msgType),
		})
	}
}

type remoteConnectionIdentity struct {
	authorization string
	deviceID      string
}

func (s *AgentService) remoteConnectionSnapshot() (remoteConnectionIdentity, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	authHeader := strings.TrimSpace(s.effectiveUserAuthorizationLocked(""))
	identity := remoteConnectionIdentity{
		authorization: authHeader,
		deviceID:      strings.TrimSpace(s.state.DeviceID),
	}
	return identity, authHeader != "" && identity.deviceID != "" && s.state.Enabled && s.state.Registered
}

func (s *AgentService) setRemoteConnectionState(connected bool, status string, message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.setRemoteConnectionStateLocked(connected, status, message)
	_ = s.saveStateLocked()
}

func (s *AgentService) setRemoteConnectionStateLocked(connected bool, status string, message string) {
	s.state.RemoteConnected = connected
	if connected {
		s.state.Registered = true
	}
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
	scanDirs := activeScanDirs(s.state.ScanDirectories, s.state.ScanDirectoriesEnabled)
	s.mu.Unlock()

	snapshot := s.collectAgentSyncSnapshotWithActiveRuns(scanDirs)
	return map[string]interface{}{
		"type":                   models.AgentEventHello,
		"protocol_version":       models.AgentProtocolVersion,
		"device_id":              deviceID,
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
	if !s.state.Enabled || !s.state.Registered {
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
	if !s.state.Enabled || !s.state.Registered {
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

func currentAgentWebSocketURL() (string, error) {
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
	return parsed.String(), nil
}

func agentPlatform() string {
	return runtime.GOOS + "-" + runtime.GOARCH
}

func agentCapabilities() []string {
	caps := []string{"terminal", "terminal_stream", "file_read", "file_diff", "command_launch", "ai_run_protocol_v2", "ai_run_start_v3", "ai_provider_binding_v1"}
	if agentNativePTYSupported() {
		caps = append(caps, "terminal_pty", "terminal_resize")
	} else {
		caps = append(caps, "terminal_pipe")
	}
	caps = append(caps, agentAICapabilities()...)
	caps = append(caps,
		"http_tunnel_v1",
		"websocket_tunnel_v1",
		"goal_server_v1",
		"goal_plan_readonly_v1",
		"goal_report_v1",
		"goal_verify_v1",
		"goal_continue_v1",
		"workspace_fingerprint_v1",
	)
	return caps
}

func agentMessageJSON(payload interface{}) string {
	raw, err := json.Marshal(payload)
	if err != nil {
		return "{}"
	}
	return string(raw)
}
