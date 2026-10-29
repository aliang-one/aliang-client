package models

type AgentStatusResponse struct {
	Status string `json:"status"`
	// DeviceEnabled 是 raw enabled 标志。Enabled 复合了 Registered/Device
	// （isEnabledLocked），注册态丢失时恒为 false——本机环回巡检（watchdog）
	// 需要它区分"用户开着的设备掉注册"与"显式禁用/登出"。仅存在于响应模型，
	// 不参与 agent_state.json 持久化。
	DeviceEnabled   bool               `json:"device_enabled"`
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

// AgentMCPServer 是设备上某 CLI 配置的一个 MCP 服务器摘要(脱敏后,设备级
// 能力清单)。env 值永不进入本结构;长 token/URL query 由采集层打码。
type AgentMCPServer struct {
	Name string `json:"name"`
	// Transport 是传输形态:stdio | http | sse(codex url 条目统一归 http)。
	Transport string `json:"transport"`
	// Command 是 stdio 形态的 argv join 命令行;http/sse 形态省略。
	Command string `json:"command,omitempty"`
	// URL 是 http/sse 形态的端点(可疑 query 值已打码);stdio 形态省略。
	URL string `json:"url,omitempty"`
	// Source 标记来源:claude 为 user|plugin,codex 为 config。
	Source string `json:"source,omitempty"`
}

type AgentTool struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Command     string `json:"command"`
	Path        string `json:"path,omitempty"`
	Available   bool   `json:"available"`
	Description string `json:"description,omitempty"`
	// Version 是该 CLI 的 x.y.z 版本(探测自 `--version`),空=探不到。
	Version string `json:"version,omitempty"`
	// Efforts 是该 CLI 支持的 effort 档清单(claude 运行时探针,codex/opencode
	// 静态表),nil=未知——消费方对未知不设限制。
	Efforts []string `json:"efforts,omitempty"`
	// MCPServers 是该 CLI 配置的 MCP 服务器清单(设备级,脱敏后)。nil=未
	// 采集、无配置或不适用(opencode/pi/gemini 不采集);消费方对 nil 不设限。
	MCPServers []AgentMCPServer `json:"mcp_servers,omitempty"`
}

// AgentSubagent 是设备上用户域(~/.claude/agents)的一个自定义 subagent 定义
// 摘要(name/description 截断后随 hello `subagents` 上报)。description 为
// frontmatter 首行截断,free text 不可信——消费方按展示文本对待。
type AgentSubagent struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Source      string `json:"source,omitempty"`
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
	ID                    string `json:"id"`
	Provider              string `json:"provider"`
	Tool                  string `json:"tool,omitempty"`
	SourceSessionID       string `json:"source_session_id,omitempty"`
	Origin                string `json:"origin,omitempty"`
	ManagedConversationID string `json:"managed_conversation_id,omitempty"`
	BindingState          string `json:"binding_state,omitempty"`
	BindingVersion        int    `json:"binding_version,omitempty"`
	ProjectPath           string `json:"project_path,omitempty"`
	Title                 string `json:"title,omitempty"`
	// TitleUpdatedAt is the agent-clock timestamp of the winning user rename
	// (pid record or durable rename cache). Empty when the title is merely
	// derived (summary/firstPrompt/index fallback). PhoneServer compares this
	// against its own titleUpdatedAt to implement latest-of-writer semantics
	// instead of freezing the first phone rename forever.
	TitleUpdatedAt string `json:"title_updated_at,omitempty"`
	// TitleSource carries Claude Code's pid-record nameSource for the title
	// above: user|peer|hook (an explicit rename) or derived|auto|collision
	// (Claude Code generated it). PhoneServer uses it as defence in depth: a
	// generated name must never be stamped into title_updated_at. Empty on
	// older agents/CC builds, which PhoneServer treats as user intent.
	TitleSource  string `json:"title_source,omitempty"`
	Summary      string `json:"summary,omitempty"`
	Mode         string `json:"mode,omitempty"`
	Status       string `json:"status,omitempty"`
	MessageCount int    `json:"message_count,omitempty"`
	// DisplayMessageCount is the number of messages a client can actually
	// render. MessageCount remains the STABLE-ID index allocator (command and
	// artifact rows consume index slots even though they are excluded from the
	// transcript), so it is not a display count. Nil means "this path does not
	// compute it" (e.g. the index-only channel) and the server falls back to
	// MessageCount; a non-nil 0 is a KNOWN zero (command/artifact husk).
	DisplayMessageCount *int                     `json:"display_message_count,omitempty"`
	Branch              string                   `json:"branch,omitempty"`
	Model               string                   `json:"model,omitempty"`
	Transcript          []AgentVibeMessage       `json:"transcript,omitempty"`
	TranscriptPage      *AgentVibeTranscriptPage `json:"transcript_page,omitempty"`
	CreatedAt           string                   `json:"created_at,omitempty"`
	UpdatedAt           string                   `json:"updated_at,omitempty"`
	// Diagnostics 是该会话最后一次 run 的终态诊断（journal end 行落盘，详情
	// 接口透传）。nil 表示没有带诊断块的 end 行——旧数据或非 agentcore 通道，
	// 手机端面板据此降级为「无诊断数据」而不是渲染空卡。
	Diagnostics *AgentVibeDiagnostics `json:"diagnostics,omitempty"`
}

// AgentVibeDiagnostics 是 agentcore 会话一次 run 的终态诊断快照。JSON 键与
// 实时通道（ai.run.progress 携带的 diagnostics 字段）一致，手机端两路
// （实时心跳 / 详情回放）按同一套字段消费。
type AgentVibeDiagnostics struct {
	// Turns 是最后一次 run 实际执行的回合数。
	Turns int `json:"turns"`
	// ContextTokens 是该 run 最后一次上游报告的本轮输入 token 数（含缓存），
	// 自动压缩后为压缩后的估算值。
	ContextTokens int `json:"context_tokens"`
	// ContextWindow 是该 run 的有效上下文窗口。
	ContextWindow int `json:"context_window"`
	// DupStreak / ErrStreak / TruncStreak 是三个无进展守卫终止时的连击计数。
	DupStreak   int `json:"dup_streak"`
	ErrStreak   int `json:"err_streak"`
	TruncStreak int `json:"trunc_streak"`
	// StopReason 是最近一个回合的上游停止原因。
	StopReason string `json:"stop_reason,omitempty"`
	// ElapsedMs 是该 run 的总耗时（毫秒）。
	ElapsedMs int64 `json:"elapsed_ms"`
	// Terminal 恒为 true（终态快照），保留键是为了与实时通道的字段形状对齐。
	Terminal bool `json:"terminal,omitempty"`
	// Reason 是内核给出的结构化终止真因。
	Reason string `json:"reason,omitempty"`
	// Status 是 end 行的 status ∈ success|stopped|error。
	Status string `json:"status,omitempty"`
	// Error 是宿主落进 end 行的最终错误文案（Reason 为空时的回退显示值）。
	Error string `json:"error,omitempty"`
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

type AgentTerminalScrollback struct {
	SessionID  string `json:"session_id"`
	Encoding   string `json:"encoding"`
	Status     string `json:"status"` // "live" | "exited"
	Data       string `json:"data"`
	Truncated  bool   `json:"truncated"`
	CapturedAt string `json:"captured_at"`
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
