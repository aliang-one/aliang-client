package models

type AgentStatusResponse struct {
	Status          string             `json:"status"`
	Enabled         bool               `json:"enabled"`
	Bound           bool               `json:"bound"`
	Registered      bool               `json:"registered"`
	BindingRequired bool               `json:"binding_required"`
	Platform        string             `json:"platform"`
	AgentServer     string             `json:"agent_server,omitempty"`
	Runtime         *AgentRuntime      `json:"runtime,omitempty"`
	Device          *AgentDevice       `json:"device,omitempty"`
	PendingBind     *AgentBindSession  `json:"pending_bind,omitempty"`
	Tools           []AgentTool        `json:"tools"`
	History         []AgentHistoryRoot `json:"history"`
	LastSyncAt      string             `json:"last_sync_at,omitempty"`
	SyncStatus      string             `json:"sync_status,omitempty"`
	SyncMessage     string             `json:"sync_message,omitempty"`
	Message         string             `json:"message,omitempty"`
}

type AgentRuntime struct {
	Online bool   `json:"online"`
	Kind   string `json:"kind"`
	URL    string `json:"url,omitempty"`
	PID    int    `json:"pid,omitempty"`
}

type AgentDevice struct {
	ID                    string   `json:"id"`
	DeviceID              string   `json:"device_id,omitempty"`
	UniqueCode            string   `json:"unique_code,omitempty"`
	Name                  string   `json:"name"`
	Platform              string   `json:"platform"`
	AgentVersion          string   `json:"agent_version,omitempty"`
	Status                string   `json:"status,omitempty"`
	Capabilities          []string `json:"capabilities,omitempty"`
	LastSeenAt            string   `json:"last_seen_at,omitempty"`
	RemoteTerminalEnabled bool     `json:"remote_terminal_enabled"`
	AIControlEnabled      bool     `json:"ai_control_enabled"`
	CreatedAt             string   `json:"created_at,omitempty"`
	PairedAt              string   `json:"paired_at,omitempty"`
	BoundAt               string   `json:"bound_at"`
}

type AgentBindStartResponse struct {
	SessionID   string `json:"session_id"`
	PairingCode string `json:"pairing_code,omitempty"`
	QRPayload   string `json:"qr_payload"`
	QRDataURL   string `json:"qr_data_url"`
	ExpiresAt   string `json:"expires_at"`
	Status      string `json:"status"`
	Message     string `json:"message"`
}

type AgentBindStatusResponse struct {
	SessionID   string       `json:"session_id"`
	PairingCode string       `json:"pairing_code,omitempty"`
	Status      string       `json:"status"`
	Bound       bool         `json:"bound"`
	Device      *AgentDevice `json:"device,omitempty"`
	Message     string       `json:"message,omitempty"`
	ExpiresAt   string       `json:"expires_at,omitempty"`
}

type AgentBindSession struct {
	SessionID   string `json:"session_id"`
	PairingCode string `json:"pairing_code,omitempty"`
	ExpiresAt   string `json:"expires_at"`
	Status      string `json:"status"`
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
