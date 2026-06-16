package models

const (
	AgentProtocolVersion = "2026-06-12"

	AgentHTTPRegisterEndpoint   = "/api/devices/register"
	AgentHTTPStatusSyncEndpoint = "/api/agent/status"
	AgentWSEndpoint             = "/ws/agent"

	AgentEventHello                 = "agent.hello"
	AgentEventHeartbeat             = "agent.heartbeat"
	AgentEventRegistered            = "agent.registered"
	AgentEventHeartbeatAck          = "agent.heartbeat.ack"
	AgentEventError                 = "agent.error"
	AgentEventDeviceUnbound         = "device.unbound"
	AgentEventDeviceSettings        = "device.settings.updated"
	AgentEventTerminalCreate        = "terminal.create"
	AgentEventTerminalCreated       = "terminal.created"
	AgentEventTerminalInput         = "terminal.input"
	AgentEventTerminalOutput        = "terminal.output"
	AgentEventTerminalResize        = "terminal.resize"
	AgentEventTerminalResized       = "terminal.resized"
	AgentEventTerminalClose         = "terminal.close"
	AgentEventTerminalExit          = "terminal.exit"
	AgentEventTerminalError         = "terminal.error"
	AgentEventAISessionCreate       = "ai.session.create"
	AgentEventAISessionCreated      = "ai.session.created"
	AgentEventAISessionClose        = "ai.session.close"
	AgentEventAISessionClosed       = "ai.session.closed"
	AgentEventAIMessage             = "ai.message"
	AgentEventAIRunStarted          = "ai.run.started"
	AgentEventAIDelta               = "ai.delta"
	AgentEventAIDone                = "ai.done"
	AgentEventAIStop                = "ai.stop"
	AgentEventAIStatus              = "ai.status"
	AgentEventAIError               = "ai.error"
	AgentEventAIApprovalRequest     = "ai.approval.request"
	AgentEventAIApprovalResponse    = "ai.approval.response"
	AgentEventFileList              = "file.list"
	AgentEventFileListResult        = "file.list.result"
	AgentEventFileRead              = "file.read"
	AgentEventFileReadResult        = "file.read.result"
	AgentEventFileError             = "file.error"
	AgentEventProjectDetail         = "project.detail"
	AgentEventProjectDetailResult   = "project.detail.result"
	AgentEventAISessionDetail       = "ai.session.detail"
	AgentEventAISessionDetailResult = "ai.session.detail.result"
)

type AgentProtocolContract struct {
	Version   string                   `json:"version"`
	HTTP      []AgentProtocolHTTPRoute `json:"http"`
	WebSocket AgentProtocolWebSocket   `json:"websocket"`
	Streams   []AgentProtocolStream    `json:"streams"`
}

type AgentProtocolHTTPRoute struct {
	Name           string   `json:"name"`
	Method         string   `json:"method"`
	Path           string   `json:"path"`
	Auth           string   `json:"auth"`
	RequestFields  []string `json:"request_fields"`
	ResponseFields []string `json:"response_fields"`
	Notes          string   `json:"notes,omitempty"`
}

type AgentProtocolWebSocket struct {
	Path        string               `json:"path"`
	Auth        string               `json:"auth"`
	ClientSends []AgentProtocolEvent `json:"client_sends"`
	ServerSends []AgentProtocolEvent `json:"server_sends"`
	ClosePolicy string               `json:"close_policy"`
	HeartbeatMs int                  `json:"heartbeat_ms"`
}

type AgentProtocolEvent struct {
	Type     string   `json:"type"`
	Required []string `json:"required,omitempty"`
	Optional []string `json:"optional,omitempty"`
	Emits    []string `json:"emits,omitempty"`
}

type AgentProtocolStream struct {
	Name        string   `json:"name"`
	Event       string   `json:"event"`
	Required    []string `json:"required"`
	Optional    []string `json:"optional,omitempty"`
	Description string   `json:"description"`
}

func DefaultAgentProtocolContract() AgentProtocolContract {
	return AgentProtocolContract{
		Version: AgentProtocolVersion,
		HTTP: []AgentProtocolHTTPRoute{
			{
				Name:           "register_device",
				Method:         "POST",
				Path:           AgentHTTPRegisterEndpoint,
				Auth:           "Authorization: Bearer <user_access_token>",
				RequestFields:  []string{"device_id", "unique_code"},
				ResponseFields: []string{"device_token", "device_id", "device"},
				Notes:          "Registration uses the logged-in user identity. Device details are sent later over sync_agent_status and agent.hello.",
			},
			{
				Name:           "sync_agent_status",
				Method:         "POST",
				Path:           AgentHTTPStatusSyncEndpoint,
				Auth:           "Authorization: Bearer <device_token>",
				RequestFields:  []string{"device_id", "status", "unique_code", "device_name", "platform", "agent_version", "capabilities", "tools", "history", "projects", "vibe_sessions", "authorized_directories", "started_at", "collected_at", "load"},
				ResponseFields: []string{"status", "device", "settings", "project_count", "vibe_session_count"},
				Notes:          "Sent immediately after device registration and on explicit sync so the cloud can list local projects and vibecoding sessions before websocket hello succeeds.",
			},
		},
		WebSocket: AgentProtocolWebSocket{
			Path:        AgentWSEndpoint + "?token=<device_token>",
			Auth:        "device_token query parameter returned by register_device",
			ClosePolicy: "Agent closes terminal and AI sessions when the websocket disconnects.",
			HeartbeatMs: 10000,
			ClientSends: []AgentProtocolEvent{
				{Type: AgentEventHello, Required: []string{"type", "device_id", "unique_code", "protocol_version", "device_name", "platform", "agent_version", "capabilities", "tools", "history", "projects", "vibe_sessions", "started_at"}, Optional: []string{"host", "authorized_directories", "collected_at", "load"}},
				{Type: AgentEventHeartbeat, Required: []string{"type", "device_id", "ts"}, Optional: []string{"load"}},
				{Type: AgentEventTerminalCreated, Required: []string{"type", "session_id", "shell", "cwd", "pty", "rows", "cols"}},
				{Type: AgentEventTerminalOutput, Required: []string{"type", "session_id", "encoding", "data"}},
				{Type: AgentEventTerminalResized, Required: []string{"type", "session_id", "rows", "cols", "pty"}},
				{Type: AgentEventTerminalExit, Required: []string{"type", "session_id", "exit_code"}},
				{Type: AgentEventTerminalError, Required: []string{"type", "session_id", "error"}},
				{Type: AgentEventAISessionCreated, Required: []string{"type", "session_id", "mode", "project_path", "provider", "state"}, Optional: []string{"state=idle"}},
				{Type: AgentEventAIRunStarted, Required: []string{"type", "session_id", "message_id", "provider", "mode", "project_path"}, Optional: []string{"state=running"}},
				{Type: AgentEventAIDelta, Required: []string{"type", "session_id", "message_id", "channel", "delta"}},
				{Type: AgentEventAIDone, Required: []string{"type", "session_id", "message_id"}},
				{Type: AgentEventAIStatus, Required: []string{"type", "session_id", "status"}},
				{Type: AgentEventAIError, Required: []string{"type", "session_id", "error"}},
				{Type: AgentEventAIApprovalRequest, Required: []string{"type", "session_id", "message_id", "approval_id", "provider", "kind", "status"}, Optional: []string{"title", "reason", "command", "cwd", "tool_name", "tool_input", "file_changes", "available_decisions", "decision", "raw"}},
				{Type: AgentEventAISessionClosed, Required: []string{"type", "session_id"}},
				{Type: AgentEventFileListResult, Required: []string{"type", "request_id", "path", "entries"}},
				{Type: AgentEventFileReadResult, Required: []string{"type", "request_id", "path", "encoding", "content"}},
				{Type: AgentEventProjectDetailResult, Required: []string{"type", "request_id", "project"}},
				{Type: AgentEventAISessionDetailResult, Required: []string{"type", "request_id", "session"}},
				{Type: AgentEventFileError, Required: []string{"type", "request_id", "error"}},
				{Type: AgentEventError, Required: []string{"type", "error"}},
			},
			ServerSends: []AgentProtocolEvent{
				{Type: AgentEventRegistered, Optional: []string{"device_id"}},
				{Type: AgentEventHeartbeatAck},
				{Type: AgentEventDeviceUnbound},
				{Type: AgentEventDeviceSettings, Required: []string{"device"}},
				{Type: AgentEventTerminalCreate, Required: []string{"type", "session_id"}, Optional: []string{"shell", "cwd", "rows", "cols"}, Emits: []string{AgentEventTerminalCreated, AgentEventTerminalOutput, AgentEventTerminalExit, AgentEventTerminalError}},
				{Type: AgentEventTerminalInput, Required: []string{"type", "session_id", "data"}, Emits: []string{AgentEventTerminalOutput, AgentEventTerminalError}},
				{Type: AgentEventTerminalResize, Required: []string{"type", "session_id", "rows", "cols"}, Emits: []string{AgentEventTerminalResized, AgentEventTerminalError}},
				{Type: AgentEventTerminalClose, Required: []string{"type", "session_id"}, Emits: []string{AgentEventTerminalExit}},
				{Type: AgentEventAISessionCreate, Required: []string{"type", "session_id"}, Optional: []string{"project_path", "mode", "provider", "tool", "model", "source_session_id", "resume_session_id", "initial_context", "transcript"}, Emits: []string{AgentEventAISessionCreated, AgentEventAIError}},
				{Type: AgentEventAIMessage, Required: []string{"type", "session_id", "message_id", "content"}, Optional: []string{"attachments", "provider", "tool", "model", "project_path", "source_session_id", "resume_session_id"}, Emits: []string{AgentEventAIRunStarted, AgentEventAIDelta, AgentEventAIApprovalRequest, AgentEventAIDone, AgentEventAIError}},
				{Type: AgentEventAIApprovalResponse, Required: []string{"type", "session_id", "approval_id", "decision"}, Optional: []string{"message_id", "scope", "raw"}, Emits: []string{AgentEventAIStatus, AgentEventAIError}},
				{Type: AgentEventAIStop, Required: []string{"type", "session_id"}, Emits: []string{AgentEventAIStatus}},
				{Type: AgentEventAISessionClose, Required: []string{"type", "session_id"}, Emits: []string{AgentEventAISessionClosed}},
				{Type: AgentEventFileList, Required: []string{"type", "request_id", "project_path", "path"}, Optional: []string{"max_entries"}, Emits: []string{AgentEventFileListResult, AgentEventFileError}},
				{Type: AgentEventFileRead, Required: []string{"type", "request_id", "project_path", "path"}, Optional: []string{"max_bytes"}, Emits: []string{AgentEventFileReadResult, AgentEventFileError}},
				{Type: AgentEventProjectDetail, Required: []string{"type", "request_id", "project_id", "project_path"}, Emits: []string{AgentEventProjectDetailResult, AgentEventFileError}},
				{Type: AgentEventAISessionDetail, Required: []string{"type", "request_id"}, Optional: []string{"session_id", "source_session_id", "project_path", "limit", "before_message_id", "before_timestamp"}, Emits: []string{AgentEventAISessionDetailResult, AgentEventFileError}},
			},
		},
		Streams: []AgentProtocolStream{
			{Name: "terminal", Event: AgentEventTerminalOutput, Required: []string{"session_id", "encoding", "data"}, Description: "PTY bytes are forwarded as text chunks in arrival order."},
			{Name: "ai_chat", Event: AgentEventAIDelta, Required: []string{"session_id", "message_id", "channel", "delta"}, Optional: []string{"provider"}, Description: "Headless AI CLI stdout/stderr is streamed as soon as bytes arrive."},
			{Name: "ai_approval", Event: AgentEventAIApprovalRequest, Required: []string{"session_id", "message_id", "approval_id", "provider", "kind", "status"}, Optional: []string{"title", "reason", "command", "cwd", "tool_name", "tool_input", "file_changes", "available_decisions", "decision"}, Description: "Headless AI CLI permission requests are surfaced to the user and resumed only after an ai.approval.response decision."},
		},
	}
}
