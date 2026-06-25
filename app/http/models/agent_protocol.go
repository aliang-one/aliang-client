package models

const (
	AgentProtocolVersion = "2026-06-12"

	AgentHTTPRegisterEndpoint   = "/api/devices/register"
	AgentHTTPStatusSyncEndpoint = "/api/agent/status"
	AgentWSEndpoint             = "/ws/agent"

	AgentEventHello                   = "agent.hello"
	AgentEventHeartbeat               = "agent.heartbeat"
	AgentEventRegistered              = "agent.registered"
	AgentEventHeartbeatAck            = "agent.heartbeat.ack"
	AgentEventSessionRefresh          = "agent.session.refresh"
	AgentEventSessionRefreshAck       = "agent.session.refresh.ack"
	AgentEventError                   = "agent.error"
	AgentEventDeviceUnbound           = "device.unbound"
	AgentEventDeviceSettings          = "device.settings.updated"
	AgentEventProjectSettings         = "project.settings.updated"
	AgentEventTerminalCreate          = "terminal.create"
	AgentEventTerminalCreated         = "terminal.created"
	AgentEventTerminalInput           = "terminal.input"
	AgentEventTerminalOutput          = "terminal.output"
	AgentEventTerminalResize          = "terminal.resize"
	AgentEventTerminalResized         = "terminal.resized"
	AgentEventTerminalClose           = "terminal.close"
	AgentEventTerminalExit            = "terminal.exit"
	AgentEventTerminalError           = "terminal.error"
	AgentEventAISessionCreate         = "ai.session.create"
	AgentEventAISessionCreated        = "ai.session.created"
	AgentEventAISessionClose          = "ai.session.close"
	AgentEventAISessionClosed         = "ai.session.closed"
	AgentEventAIMessage               = "ai.message"
	AgentEventAIMessageReceived       = "ai.message.received" // agent→cloud receipt ack; drives the admin per-turn pipeline's "Agent 已收到" confirmed node
	AgentEventAISteer                 = "ai.steer"            // cloud→agent: append user input to the active Codex app-server turn
	AgentEventAISteerAck              = "ai.steer.ack"        // agent→cloud: non-terminal steer delivery/result ack
	AgentEventAIRunStarted            = "ai.run.started"
	AgentEventAIDelta                 = "ai.delta"
	AgentEventAIDone                  = "ai.done"
	AgentEventAIRunProgress           = "ai.run.progress" // server→mobile: live per-run files_touched_count + git_changed_count
	AgentEventAIStop                  = "ai.stop"
	AgentEventAIStatus                = "ai.status"
	AgentEventAIError                 = "ai.error"
	AgentEventAIApprovalRequest       = "ai.approval.request"
	AgentEventAIApprovalResponse      = "ai.approval.response"
	AgentEventAIApprovalRequestAck    = "ai.approval.request.ack" // server→client: request received/stored
	AgentEventAIApprovalAck           = "ai.approval.ack"         // client→server: receipt+result of a decision (observed delivery)
	AgentEventAIApprovalSync          = "ai.approval.sync"        // client→server: list still-pending approvals (reconcile + liveness)
	AgentEventAIApprovalCancelled     = "ai.approval.cancelled"   // bidirectional: dialogue went inactive, approvals dropped
	AgentEventAIApprovalState         = "ai.approval.state"       // server→client: server-side status of an approval (e.g. still pending)
	AgentEventAIOptionRequest         = "ai.option.request"       // agent→server: 请求用户在多个方案中选择
	AgentEventAIOptionResponse        = "ai.option.response"      // server→agent: 用户的选择结果
	AgentEventAIOptionCancelled       = "ai.option.cancelled"     // 双向: 选择对话失活，作废待选
	AgentEventAICommand               = "ai.command"              // agent→cloud: structured command execution (Bash / codex commandExecution) for activity-feed rendering
	AgentEventAIFileChange            = "ai.file_change"          // agent→cloud: structured file edit (Write/Edit/MultiEdit / codex fileChange) with ±lines + diff
	AgentEventAIThinking              = "ai.thinking"             // agent→cloud: streamed model reasoning (claude thinking_delta / codex reasoning) kept off the prose channel
	AgentEventAIUsage                 = "ai.usage"                // agent→cloud: per-turn token usage surfaced from the provider
	AgentEventAITask                  = "ai.task"                 // agent→cloud: task/todo list snapshot (claude TodoWrite)
	AgentEventFileList                = "file.list"
	AgentEventFileListResult          = "file.list.result"
	AgentEventFileRead                = "file.read"
	AgentEventFileReadResult          = "file.read.result"
	AgentEventFileError               = "file.error"
	AgentEventProjectDetail           = "project.detail"
	AgentEventProjectDetailResult     = "project.detail.result"
	AgentEventAISessionDetail         = "ai.session.detail"
	AgentEventAISessionDetailResult   = "ai.session.detail.result"
	AgentEventSlashCommandsList       = "slash.commands.list"
	AgentEventSlashCommandsListResult = "slash.commands.list.result"
	AgentEventSlashCommandsListError  = "slash.commands.list.error"
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
				Notes:          "Sent immediately after device registration and on explicit sync so the cloud can list local projects and vibecoding sessions before websocket hello succeeds. A 401 response means the device token is invalid; the local agent must clear the device token and close its websocket.",
			},
			{
				Name:           "disable_local_agent",
				Method:         "POST",
				Path:           "/api/agent/disable",
				Auth:           "local dashboard or local auth hook",
				RequestFields:  []string{"reason?"},
				ResponseFields: []string{"status", "enabled", "registered", "remote_connected", "sync_status", "sync_message"},
				Notes:          "Local-only control endpoint. Supported reasons include manual, logout, auth_expired, device_token_invalid, and device_unbound; disabling actively closes the remote websocket and local terminal/AI sessions.",
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
				{Type: AgentEventSessionRefresh, Required: []string{"type", "access_token"}},
				{Type: AgentEventTerminalCreated, Required: []string{"type", "session_id", "shell", "cwd", "pty", "rows", "cols"}},
				{Type: AgentEventTerminalOutput, Required: []string{"type", "session_id", "encoding", "data"}},
				{Type: AgentEventTerminalResized, Required: []string{"type", "session_id", "rows", "cols", "pty"}},
				{Type: AgentEventTerminalExit, Required: []string{"type", "session_id", "exit_code"}},
				{Type: AgentEventTerminalError, Required: []string{"type", "session_id", "error"}},
				{Type: AgentEventAISessionCreated, Required: []string{"type", "session_id", "mode", "project_path", "provider", "state"}, Optional: []string{"state=idle"}},
				{Type: AgentEventAIRunStarted, Required: []string{"type", "session_id", "message_id", "provider", "mode", "project_path"}, Optional: []string{"state=running"}},
				{Type: AgentEventAISteerAck, Required: []string{"type", "session_id", "message_id", "result"}, Optional: []string{"error", "code", "acked_at"}},
				{Type: AgentEventAIDelta, Required: []string{"type", "session_id", "message_id", "channel", "delta"}},
				{Type: AgentEventAIDone, Required: []string{"type", "session_id", "message_id"}},
				{Type: AgentEventAIRunProgress, Required: []string{"type", "session_id"}, Optional: []string{"message_id", "files_touched_count", "git_changed_count", "retry_active", "retry_attempt", "retry_max", "retry_delay_ms", "error_status", "error_type"}},
				{Type: AgentEventAICommand, Required: []string{"type", "session_id", "message_id", "item_id", "status"}, Optional: []string{"command", "cwd", "exit_code", "output"}},
				{Type: AgentEventAIFileChange, Required: []string{"type", "session_id", "message_id", "item_id"}, Optional: []string{"path", "kind", "added", "removed", "diff", "changes"}},
				{Type: AgentEventAIThinking, Required: []string{"type", "session_id", "message_id", "delta"}},
				{Type: AgentEventAIUsage, Required: []string{"type", "session_id"}, Optional: []string{"message_id", "input_tokens", "output_tokens", "cache_read_tokens", "model"}},
				{Type: AgentEventAITask, Required: []string{"type", "session_id", "message_id", "tasks"}},
				{Type: AgentEventAIStatus, Required: []string{"type", "session_id", "status"}},
				{Type: AgentEventAIError, Required: []string{"type", "session_id", "error"}, Optional: []string{"message_id", "error_status", "error_type", "retry_attempt", "retry_max", "detail"}},
				{Type: AgentEventAIApprovalRequest, Required: []string{"type", "session_id", "message_id", "approval_id", "provider", "kind", "status"}, Optional: []string{"title", "reason", "command", "cwd", "tool_name", "tool_input", "file_changes", "available_decisions", "decision", "raw", "matched_rule_id", "policy_version"}},
				{Type: AgentEventAIApprovalAck, Required: []string{"type", "session_id", "approval_id", "result"}, Optional: []string{"delivery_id"}},
				{Type: AgentEventAIApprovalSync, Required: []string{"type"}, Optional: []string{"pending"}},
				{Type: AgentEventAIApprovalCancelled, Required: []string{"type", "session_id"}, Optional: []string{"approval_ids", "reason"}},
				{Type: AgentEventAISessionClosed, Required: []string{"type", "session_id"}},
				{Type: AgentEventFileListResult, Required: []string{"type", "request_id", "path", "entries"}},
				{Type: AgentEventFileReadResult, Required: []string{"type", "request_id", "path", "encoding", "content"}},
				{Type: AgentEventProjectDetailResult, Required: []string{"type", "request_id", "project"}},
				{Type: AgentEventAISessionDetailResult, Required: []string{"type", "request_id", "session"}},
				{Type: AgentEventSlashCommandsListResult, Required: []string{"type", "request_id", "project_path", "commands"}, Optional: []string{"generated_at"}},
				{Type: AgentEventSlashCommandsListError, Required: []string{"type", "request_id", "error"}},
				{Type: AgentEventFileError, Required: []string{"type", "request_id", "error"}},
				{Type: AgentEventError, Required: []string{"type", "error"}},
				{Type: AgentEventAIOptionRequest, Required: []string{"type", "session_id", "option_id", "options"}, Optional: []string{"message_id", "title", "allow_custom", "multi", "provider"}},
			},
			ServerSends: []AgentProtocolEvent{
				{Type: AgentEventRegistered, Optional: []string{"device_id"}},
				{Type: AgentEventHeartbeatAck},
				{Type: AgentEventSessionRefreshAck, Optional: []string{"ok", "auth_expires_at"}},
				{Type: AgentEventDeviceUnbound},
				{Type: AgentEventDeviceSettings, Required: []string{"device"}},
				{Type: AgentEventProjectSettings, Required: []string{"path"}, Optional: []string{"project_id", "approval_policy"}},
				{Type: AgentEventTerminalCreate, Required: []string{"type", "session_id"}, Optional: []string{"shell", "cwd", "rows", "cols"}, Emits: []string{AgentEventTerminalCreated, AgentEventTerminalOutput, AgentEventTerminalExit, AgentEventTerminalError}},
				{Type: AgentEventTerminalInput, Required: []string{"type", "session_id", "data"}, Emits: []string{AgentEventTerminalOutput, AgentEventTerminalError}},
				{Type: AgentEventTerminalResize, Required: []string{"type", "session_id", "rows", "cols"}, Emits: []string{AgentEventTerminalResized, AgentEventTerminalError}},
				{Type: AgentEventTerminalClose, Required: []string{"type", "session_id"}, Emits: []string{AgentEventTerminalExit}},
				{Type: AgentEventAISessionCreate, Required: []string{"type", "session_id"}, Optional: []string{"project_path", "mode", "provider", "tool", "model", "source_session_id", "resume_session_id", "initial_context", "transcript"}, Emits: []string{AgentEventAISessionCreated, AgentEventAIError}},
				{Type: AgentEventAIMessage, Required: []string{"type", "session_id", "message_id", "content"}, Optional: []string{"attachments", "provider", "tool", "model", "project_path", "source_session_id", "resume_session_id"}, Emits: []string{AgentEventAIMessageReceived, AgentEventAIRunStarted, AgentEventAIDelta, AgentEventAIThinking, AgentEventAICommand, AgentEventAIFileChange, AgentEventAIUsage, AgentEventAITask, AgentEventAIApprovalRequest, AgentEventAIRunProgress, AgentEventAIDone, AgentEventAIError}},
				{Type: AgentEventAISteer, Required: []string{"type", "session_id", "message_id", "content"}, Optional: []string{"mode"}, Emits: []string{AgentEventAISteerAck}},
				{Type: AgentEventAIMessageReceived, Required: []string{"type", "session_id", "message_id"}, Optional: []string{"received_at"}},
				{Type: AgentEventAIApprovalResponse, Required: []string{"type", "session_id", "approval_id", "decision"}, Optional: []string{"message_id", "scope", "raw", "delivery_id", "attempt"}, Emits: []string{AgentEventAIApprovalAck, AgentEventAIStatus, AgentEventAIError}},
				{Type: AgentEventAIApprovalState, Required: []string{"type", "approval_id", "status"}, Optional: []string{"session_id"}},
				{Type: AgentEventAIStop, Required: []string{"type", "session_id"}, Emits: []string{AgentEventAIStatus}},
				{Type: AgentEventAISessionClose, Required: []string{"type", "session_id"}, Emits: []string{AgentEventAISessionClosed}},
				{Type: AgentEventFileList, Required: []string{"type", "request_id", "project_path", "path"}, Optional: []string{"max_entries"}, Emits: []string{AgentEventFileListResult, AgentEventFileError}},
				{Type: AgentEventFileRead, Required: []string{"type", "request_id", "project_path", "path"}, Optional: []string{"max_bytes"}, Emits: []string{AgentEventFileReadResult, AgentEventFileError}},
				{Type: AgentEventProjectDetail, Required: []string{"type", "request_id", "project_id", "project_path"}, Emits: []string{AgentEventProjectDetailResult, AgentEventFileError}},
				{Type: AgentEventAISessionDetail, Required: []string{"type", "request_id"}, Optional: []string{"session_id", "source_session_id", "project_path", "limit", "before_message_id", "before_timestamp"}, Emits: []string{AgentEventAISessionDetailResult, AgentEventFileError}},
				{Type: AgentEventSlashCommandsList, Required: []string{"type", "request_id", "project_path"}, Optional: []string{"provider", "include_user_level", "include_plugins"}, Emits: []string{AgentEventSlashCommandsListResult, AgentEventSlashCommandsListError}},
				{Type: AgentEventAIOptionResponse, Required: []string{"type", "session_id", "option_id", "selected"}, Optional: []string{"message_id", "custom_text", "decision", "delivery_id"}, Emits: []string{AgentEventAIRunStarted, AgentEventAIDelta, AgentEventAIRunProgress, AgentEventAIDone, AgentEventAIError, AgentEventAIOptionRequest}},
				{Type: AgentEventAIOptionCancelled, Required: []string{"type", "session_id"}, Optional: []string{"option_ids", "reason"}},
			},
		},
		Streams: []AgentProtocolStream{
			{Name: "terminal", Event: AgentEventTerminalOutput, Required: []string{"session_id", "encoding", "data"}, Description: "PTY bytes are forwarded as text chunks in arrival order."},
			{Name: "ai_chat", Event: AgentEventAIDelta, Required: []string{"session_id", "message_id", "channel", "delta"}, Optional: []string{"provider"}, Description: "Headless AI CLI stdout/stderr is streamed as soon as bytes arrive."},
			{Name: "ai_approval", Event: AgentEventAIApprovalRequest, Required: []string{"session_id", "message_id", "approval_id", "provider", "kind", "status"}, Optional: []string{"title", "reason", "command", "cwd", "tool_name", "tool_input", "file_changes", "available_decisions", "decision", "matched_rule_id", "policy_version"}, Description: "Headless AI CLI permission requests are surfaced to the user and resumed only after an ai.approval.response decision. matched_rule_id/policy_version carry the approval-policy context that triggered the escalation (optional; absent for auto-approved tools, which never reach the cloud)."},
		},
	}
}
