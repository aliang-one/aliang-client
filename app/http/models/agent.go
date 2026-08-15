package models

type AgentStatusResponse struct {
	Status          string             `json:"status"`
	Enabled         bool               `json:"enabled"`
	Bound           bool               `json:"bound"`
	Registered      bool               `json:"registered"`
	RemoteConnected bool               `json:"remote_connected"`
	BindingRequired bool               `json:"binding_required"`
	Platform        string             `json:"platform"`
	ProtocolVersion string             `json:"protocol_version,omitempty"`
	AgentServer     string             `json:"agent_server,omitempty"`
	Runtime         *AgentRuntime      `json:"runtime,omitempty"`
	Device          *AgentDevice       `json:"device,omitempty"`
	Capabilities    []string           `json:"capabilities,omitempty"`
	Tools           []AgentTool        `json:"tools"`
	History         []AgentHistoryRoot `json:"history"`
	LastSyncAt      string             `json:"last_sync_at,omitempty"`
	SyncStatus      string             `json:"sync_status,omitempty"`
	SyncMessage     string             `json:"sync_message,omitempty"`
	Message         string             `json:"message,omitempty"`

	// Registration/connection health (derived, authoritative). These answer the
	// two questions users actually care about: "is this device correctly
	// registered with the server?" (registration_state) and "what is the live
	// link state?" (connection_state). The legacy booleans above are optimistic
	// (Registered is true even when the server has rejected the token) and kept
	// only for backward compatibility — prefer these.
	RegistrationState   string `json:"registration_state,omitempty"`
	RegistrationMessage string `json:"registration_message,omitempty"`
	ConnectionState     string `json:"connection_state,omitempty"`
	ConnectionMessage   string `json:"connection_message,omitempty"`
	ConnectedAt         string `json:"connected_at,omitempty"`
}

type AgentDisableRequest struct {
	Reason string `json:"reason,omitempty"`
}

type AgentRuntime struct {
	Online bool   `json:"online"`
	Kind   string `json:"kind"`
	URL    string `json:"url,omitempty"`
	PID    int    `json:"pid,omitempty"`
}

type AgentDevice struct {
	ID                    string             `json:"id"`
	DeviceID              string             `json:"device_id,omitempty"`
	UserID                string             `json:"user_id,omitempty"`
	User                  *AgentUserIdentity `json:"user,omitempty"`
	Name                  string             `json:"name"`
	Platform              string             `json:"platform"`
	AgentVersion          string             `json:"agent_version,omitempty"`
	Status                string             `json:"status,omitempty"`
	Capabilities          []string           `json:"capabilities,omitempty"`
	LastSeenAt            string             `json:"last_seen_at,omitempty"`
	RemoteTerminalEnabled bool               `json:"remote_terminal_enabled"`
	AIControlEnabled      bool               `json:"ai_control_enabled"`
	CreatedAt             string             `json:"created_at,omitempty"`
	PairedAt              string             `json:"paired_at,omitempty"`
	BoundAt               string             `json:"bound_at"`
}

type AgentUserIdentity struct {
	ID    string `json:"id"`
	Email string `json:"email,omitempty"`
	Name  string `json:"name,omitempty"`
	Role  string `json:"role,omitempty"`
}

type AgentTool struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Command     string `json:"command"`
	Path        string `json:"path,omitempty"`
	Available   bool   `json:"available"`
	Description string `json:"description,omitempty"`
}

type AgentHistoryRoot struct {
	Tool      string `json:"tool"`
	Path      string `json:"path"`
	Exists    bool   `json:"exists"`
	FileCount int    `json:"file_count"`
	TotalSize int64  `json:"total_size"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

type AgentProject struct {
	ID              string   `json:"id,omitempty"`
	Name            string   `json:"name"`
	Path            string   `json:"path"`
	Branch          string   `json:"branch,omitempty"`
	Language        string   `json:"language,omitempty"`
	Description     string   `json:"description,omitempty"`
	Status          string   `json:"status,omitempty"`
	PackageManager  string   `json:"package_manager,omitempty"`
	IsGitRepo       bool     `json:"is_git_repo,omitempty"`
	DetectedPorts   []int    `json:"detected_ports,omitempty"`
	Files           []string `json:"files,omitempty"`
	FileCount       int      `json:"file_count,omitempty"`
	GitChangedCount int      `json:"git_changed_count,omitempty"`
	TotalSize       int64    `json:"total_size,omitempty"`
	Readme          string   `json:"readme,omitempty"`
	LastActiveAt    string   `json:"last_active_at,omitempty"`
	SourceTools     []string `json:"source_tools,omitempty"`
	DetailUpdatedAt string   `json:"detail_updated_at,omitempty"`
}

type AgentVibeSession struct {
	ID                    string                   `json:"id"`
	Provider              string                   `json:"provider"`
	Tool                  string                   `json:"tool,omitempty"`
	SourceSessionID       string                   `json:"source_session_id,omitempty"`
	Origin                string                   `json:"origin,omitempty"`
	ManagedConversationID string                   `json:"managed_conversation_id,omitempty"`
	BindingState          string                   `json:"binding_state,omitempty"`
	BindingVersion        int                      `json:"binding_version,omitempty"`
	ProjectPath           string                   `json:"project_path,omitempty"`
	Title                 string                   `json:"title,omitempty"`
	Summary               string                   `json:"summary,omitempty"`
	Mode                  string                   `json:"mode,omitempty"`
	Status                string                   `json:"status,omitempty"`
	MessageCount          int                      `json:"message_count,omitempty"`
	Branch                string                   `json:"branch,omitempty"`
	Model                 string                   `json:"model,omitempty"`
	Transcript            []AgentVibeMessage       `json:"transcript,omitempty"`
	TranscriptPage        *AgentVibeTranscriptPage `json:"transcript_page,omitempty"`
	CreatedAt             string                   `json:"created_at,omitempty"`
	UpdatedAt             string                   `json:"updated_at,omitempty"`
}

type AgentVibeMessage struct {
	ID        string `json:"id,omitempty"`
	Role      string `json:"role"`
	Content   string `json:"content"`
	Timestamp string `json:"timestamp,omitempty"`
	Index     int    `json:"index,omitempty"`
}

type AgentVibeTranscriptPage struct {
	Limit               int    `json:"limit"`
	Count               int    `json:"count"`
	TotalCount          int    `json:"total_count,omitempty"`
	HasMore             bool   `json:"has_more"`
	NextBeforeMessageID string `json:"next_before_message_id,omitempty"`
	Order               string `json:"order,omitempty"`
}

type AgentRuntimeSnapshot struct {
	AIConversations []AgentVibeSession     `json:"ai_conversations"`
	Terminals       []AgentTerminalRuntime `json:"terminals"`
	CollectedAt     string                 `json:"collected_at"`
}

type AgentTerminalRuntime struct {
	ID           string `json:"id"`
	Shell        string `json:"shell"`
	CWD          string `json:"cwd"`
	PTY          bool   `json:"pty"`
	StartedAt    string `json:"started_at"`
	LastActiveAt string `json:"last_active_at"`
}

type AgentLaunchRequest struct {
	Tool        string   `json:"tool"`
	CommandLine string   `json:"command_line"`
	Args        []string `json:"args"`
	CWD         string   `json:"cwd"`
	Mode        string   `json:"mode"`
}

type AgentLaunchResponse struct {
	SessionID string `json:"session_id"`
	Tool      string `json:"tool"`
	Command   string `json:"command"`
	CWD       string `json:"cwd,omitempty"`
	Mode      string `json:"mode"`
	Status    string `json:"status"`
	Message   string `json:"message"`
}
