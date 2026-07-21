package services

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/common/logger"
	"aliang.one/nursorgate/processor/config"
)

type agentAIManager struct {
	mu                 sync.Mutex
	identityPersistMu  sync.Mutex
	sessions           map[string]*agentAISession
	approvals          map[string]*agentAIApprovalWaiter
	completedApprovals map[string]*agentAICompletedApproval
	codexInputs        map[string]*agentAICodexInputWaiter
	pendingTerminals   map[string]map[string]interface{}
	runJournalEnabled  bool
	bindings           map[string]agentAIBindingRecord
	processedRuns      map[string]agentAIProcessedRun
	// service is the owning AgentService (set when the service creates this
	// manager), used to evaluate the device approval policy. nil for standalone
	// test managers, which fall back to GetSharedAgentService().
	service *AgentService
}

func (m *agentAIManager) activeVibeSessionsSnapshot() []models.AgentVibeSession {
	if m == nil {
		return nil
	}
	now := time.Now().UTC().Format(time.RFC3339)
	m.mu.Lock()
	defer m.mu.Unlock()
	sessions := make([]models.AgentVibeSession, 0, len(m.sessions))
	for _, session := range m.sessions {
		if session == nil || session.cancel == nil {
			continue
		}
		provider := firstNonEmpty(session.provider, "auto")
		binding := m.bindings[session.id]
		sourceSessionID := firstNonEmpty(binding.NativeSessionID, session.resumeSessionID, session.reservedNativeSessionID)
		bindingState := binding.State
		bindingVersion := binding.BindingVersion
		if bindingState == "" && sourceSessionID != "" {
			bindingState = "reserved"
		}
		if bindingVersion == 0 {
			bindingVersion = session.bindingVersion
		}
		createdAt := now
		title := ""
		for _, msg := range session.history {
			if createdAt == now && !msg.CreatedAt.IsZero() {
				createdAt = msg.CreatedAt.UTC().Format(time.RFC3339)
			}
			if title == "" && strings.EqualFold(msg.Role, "user") {
				title = msg.Content
			}
		}
		title = firstNonEmpty(title, session.initialContext, agentProjectName(session.projectPath))
		sessions = append(sessions, models.AgentVibeSession{
			ID:                    session.id,
			Provider:              provider,
			Tool:                  provider,
			SourceSessionID:       sourceSessionID,
			Origin:                "managed",
			ManagedConversationID: session.id,
			BindingState:          bindingState,
			BindingVersion:        bindingVersion,
			ProjectPath:           session.projectPath,
			Title:                 truncateAgentText(title, 200),
			Mode:                  firstNonEmpty(session.mode, "vibe"),
			Status:                "running",
			MessageCount:          len(session.history),
			Model:                 session.model,
			CreatedAt:             createdAt,
			UpdatedAt:             now,
		})
	}
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].UpdatedAt > sessions[j].UpdatedAt
	})
	return sessions
}

func agentAIDiagnosticArgs(args []string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case "--settings":
			out = append(out, "--settings", "<json>")
			i++
		case "--append-system-prompt":
			out = append(out, "--append-system-prompt", "<prompt>")
			i++
		default:
			out = append(out, arg)
		}
	}
	if len(out) > 0 {
		out[len(out)-1] = fmt.Sprintf("<prompt:%d chars>", len(args[len(args)-1]))
	}
	return out
}

func agentAIEnvDiagnostic() string {
	keys := []string{
		"ANTHROPIC_BASE_URL",
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC",
		"CLAUDE_CODE_SSE_PORT",
		"HTTP_PROXY",
		"HTTPS_PROXY",
		"ALL_PROXY",
		"NO_PROXY",
		"ALIANG_USER_AGENT_ADDR",
		"ALIANG_CLAUDE_APPROVAL_HOOK",
		"ALIANG_CLAUDE_HEADLESS_SLIM",
		"ALIANG_CLAUDE_HEADLESS_TOOLS",
		"ALIANG_CLAUDE_HEADLESS_ENABLE_MCP",
	}
	parts := make([]string, 0, len(keys)+1)
	for _, key := range keys {
		value, ok := os.LookupEnv(key)
		if !ok {
			parts = append(parts, key+"=<unset>")
			continue
		}
		parts = append(parts, fmt.Sprintf("%s=%q", key, value))
	}
	if token := strings.TrimSpace(os.Getenv("ANTHROPIC_AUTH_TOKEN")); token != "" {
		parts = append(parts, fmt.Sprintf("ANTHROPIC_AUTH_TOKEN=<set:%d chars>", len(token)))
	} else {
		parts = append(parts, "ANTHROPIC_AUTH_TOKEN=<unset>")
	}
	if cfg := config.GetGlobalConfig(); cfg != nil {
		if applied := len(cfg.EffectiveCustomEnvVars()); applied > 0 {
			parts = append(parts, fmt.Sprintf("CUSTOM_ENV_OVERRIDES=<applied:%d>", applied))
		}
	}
	return strings.Join(parts, " ")
}

// mergeEnvOverriding returns base with any entry whose key matches an overrides
// key removed, then appends "KEY=VALUE" for each override. The result has at most
// one entry per key, with overrides winning over the inherited values — this is
// what makes a custom env var reliably replace a same-named system variable for the
// child process (a plain append would leave both present and the winner would then
// depend on the child runtime's envp parsing).
func mergeEnvOverriding(base []string, overrides map[string]string) []string {
	if len(overrides) == 0 {
		return base
	}
	out := make([]string, 0, len(base)+len(overrides))
	for _, kv := range base {
		key := kv
		if i := strings.IndexByte(kv, '='); i >= 0 {
			key = kv[:i]
		}
		if _, drop := overrides[key]; drop {
			continue
		}
		out = append(out, kv)
	}
	for key, value := range overrides {
		out = append(out, key+"="+value)
	}
	return out
}

// agentChildProcessEnv returns the environment to pass to an AI CLI child process
// (claude/codex): the agent's inherited environment with custom env vars from the
// customer config overriding any same-named entries. It does NOT mutate the agent
// process environment (no os.Setenv), so goroutines spawning children concurrently
// stay isolated. Callers may still append tool-specific vars afterwards.
func agentChildProcessEnv() []string {
	base := os.Environ()
	cfg := config.GetGlobalConfig()
	if cfg == nil {
		return base
	}
	overrides := cfg.EffectiveCustomEnvVars()
	if len(overrides) == 0 {
		return base
	}
	return mergeEnvOverriding(base, overrides)
}

type agentAISession struct {
	id          string
	mode        string
	projectPath string
	provider    string
	model       string
	// effort is the provider-specific reasoning effort (codex:
	// none/minimal/low/medium/high/xhigh; claude: none/low/medium/high/max).
	// Applied to the codex CLI as a `<base>-<effort>` model-name suffix that the
	// downstream gateway derives reasoning effort from. Empty = no override.
	effort                  string
	resumeSessionID         string
	reservedNativeSessionID string
	bindingVersion          int
	initialContext          string
	cancel                  context.CancelFunc
	activeWriter            agentTerminalWriter
	approvalToken           string
	activity                *agentAIActivity
	runSeq                  int
	// lastActiveAt drives LRU eviction (evictOldestIdleAISessionLocked): the
	// non-running session with the oldest stamp is dropped when a new session
	// would exceed agentAISessionResidentCap. Stamped on create and bumped on
	// every inbound ai.message so actively-used conversations survive.
	lastActiveAt  time.Time
	history       []agentAIMessage
	pendingOption *agentAIOptionRequest // run 结束检测到方案块时置位，等 ai.option.response
	codexSteer    *agentAICodexSteerControl
	claudePolicy  agentAIClaudeRemotePolicy
	claudeCaps    agentAIClaudeCapabilities
}

type agentAISteerMessage struct {
	MessageID string
	Content   string
	CreatedAt time.Time
}

type agentAICodexSteerControl struct {
	mu        sync.Mutex
	sessionID string
	runSeq    int
	threadID  string
	turnID    string
	closed    bool
	send      func(map[string]interface{}) error
	write     agentTerminalWriter
	nextID    int64
	pending   map[string]agentAISteerMessage
	queue     []agentAISteerMessage
}

func newAgentAICodexSteerControl(run agentAIRun, send func(map[string]interface{}) error, write agentTerminalWriter) *agentAICodexSteerControl {
	return &agentAICodexSteerControl{
		sessionID: run.sessionID,
		runSeq:    run.runSeq,
		send:      send,
		write:     write,
		nextID:    100,
		pending:   make(map[string]agentAISteerMessage),
	}
}

type agentAIMessage struct {
	Role      string
	MessageID string
	Content   string
	CreatedAt time.Time
}

type agentAIRun struct {
	sessionID               string
	runID                   string
	messageID               string
	runSeq                  int
	mode                    string
	projectPath             string
	provider                string
	model                   string
	effort                  string
	resumeSessionID         string
	reservedNativeSessionID string
	bindingVersion          int
	prompt                  string
	freshPrompt             string
	attachments             []agentAIAttachment
	cancel                  context.CancelFunc
	approvalToken           string
	activity                *agentAIActivity
	claudePolicy            agentAIClaudeRemotePolicy
	onClaudeInit            func([]string, string)
	// Policy context for an escalated approval: which rule triggered it and the
	// policy version that decided. Set by the approval hooks before escalation.
	matchedRuleID string
	policyVersion int
	goalIdentity  map[string]interface{}
	readOnly      bool
}

type agentAIAttachment struct {
	Type string
	Name string
	Path string
	URL  string
}

// agentAIRunEmitter serializes every event emitted by one run. Besides adding
// the v2 run identity/order fields, it is a terminal barrier: once a terminal
// event has been accepted, no concurrently-ticking progress goroutine can emit
// after it. Holding the mutex through write preserves event_seq wire order.
type agentAIRunEmitter struct {
	mu                  sync.Mutex
	runID               string
	nextSeq             int64
	terminal            bool
	write               agentTerminalWriter
	onTerminal          func(map[string]interface{}) error
	goalIdentity        map[string]interface{}
	goalOutput          strings.Builder
	goalProjectPath     string
	goalWorkspaceBefore string
}

func newAgentAIRunEmitter(run agentAIRun, write agentTerminalWriter) *agentAIRunEmitter {
	runID := strings.TrimSpace(run.runID)
	if runID == "" {
		// Compatibility for older servers: message ids are unique per user turn,
		// so they are a stable fallback run identity until the server sends run_id.
		runID = run.messageID
	}
	return &agentAIRunEmitter{
		runID:        runID,
		write:        write,
		goalIdentity: cloneGoalIdentity(run.goalIdentity),
	}
}

func agentAIRunEventTerminal(payload map[string]interface{}) bool {
	typeName := strings.TrimSpace(fmt.Sprint(payload["type"]))
	if typeName == models.AgentEventAIDone || typeName == models.AgentEventAIError || typeName == models.AgentEventAISessionClosed {
		if payload["retry_active"] == true || strings.Contains(strings.ToLower(fmt.Sprint(payload["error"])), "reconnecting") {
			return false
		}
		return true
	}
	if typeName != models.AgentEventAIStatus {
		return false
	}
	switch strings.TrimSpace(fmt.Sprint(payload["status"])) {
	case "stopped", "cancelled", "interrupted", "timeout", "idle_timeout", "hard_timeout", "output_limited":
		return true
	default:
		return false
	}
}

func (e *agentAIRunEmitter) emit(value interface{}) error {
	payload, ok := value.(map[string]interface{})
	if !ok {
		return e.write(value)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.terminal {
		return nil
	}
	e.nextSeq++
	payload["run_id"] = e.runID
	payload["event_seq"] = e.nextSeq
	for key, identityValue := range e.goalIdentity {
		payload[key] = identityValue
	}
	if payload["type"] == models.AgentEventAIDelta {
		e.appendGoalOutput(remoteString(payload, "delta"))
	}
	terminal := agentAIRunEventTerminal(payload)
	if terminal {
		e.attachGoalReport(payload)
		// Close before the write. A concurrent heartbeat blocks on the mutex and
		// observes terminal=true after this write completes, so done->progress is
		// impossible even at the ticker boundary.
		e.terminal = true
		if e.onTerminal != nil {
			if err := e.onTerminal(payload); err != nil {
				return err
			}
		}
	}
	return e.write(payload)
}

type agentAITool struct {
	id           string
	path         string
	args         []string
	env          []string
	outputFormat agentAIOutputFormat
}

type agentAIApprovalRequest struct {
	ID                 string
	SessionID          string
	MessageID          string
	Provider           string
	Kind               string
	Title              string
	Reason             string
	Command            string
	CWD                string
	ToolName           string
	ToolInput          json.RawMessage
	FileChanges        json.RawMessage
	AvailableDecisions []string
	Raw                json.RawMessage
	// Policy context surfaced to the approver (which rule + which policy version).
	MatchedRuleID string
	PolicyVersion int
	respond       chan agentAIApprovalResponse
}

type agentAIApprovalResponse struct {
	Decision string
	Scope    string
	Raw      json.RawMessage
}

// agentAIOptionRequest 描述一次"多方案选择"请求（agent→server）。
type agentAIOptionRequest struct {
	ID          string                `json:"option_id,omitempty"`
	SessionID   string                `json:"session_id,omitempty"`
	MessageID   string                `json:"message_id,omitempty"`
	Provider    string                `json:"provider,omitempty"`
	Title       string                `json:"title,omitempty"`
	Options     []agentAIOptionChoice `json:"options"`
	AllowCustom bool                  `json:"allow_custom"`
	Multi       bool                  `json:"multi"`
}

type agentAIOptionChoice struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// aliangOptionsBlockRe 匹配 ```aliang-options\n<json>\n``` 围栏块（DOTALL）。
var aliangOptionsBlockRe = regexp.MustCompile("(?s)```aliang-options\\s*\\n(.*?)\\n```")

// extractAgentAIOptionBlocks 扫描 assistant 完整输出，提取所有 aliang-options 块。
// 无效 JSON 或 options 为空的块被静默丢弃。
func extractAgentAIOptionBlocks(output string) []agentAIOptionRequest {
	matches := aliangOptionsBlockRe.FindAllStringSubmatch(output, -1)
	out := make([]agentAIOptionRequest, 0, len(matches))
	for _, m := range matches {
		var req agentAIOptionRequest
		if err := json.Unmarshal([]byte(m[1]), &req); err != nil {
			continue
		}
		if len(req.Options) == 0 {
			continue
		}
		out = append(out, req)
	}
	return out
}

// buildAgentAIOptionFollowup 把用户的选择拼成下一轮的 user prompt。
// Claude 经 --resume 续接时，这段文本作为新的用户输入。
func buildAgentAIOptionFollowup(req *agentAIOptionRequest, selected []string, custom string) string {
	var b strings.Builder
	b.WriteString("用户已在上述方案中做出选择，请据此继续，不要再重复列出方案：\n")
	labelByID := make(map[string]agentAIOptionChoice, len(req.Options))
	for _, opt := range req.Options {
		labelByID[opt.ID] = opt
	}
	for _, id := range selected {
		opt, ok := labelByID[id]
		if !ok {
			b.WriteString("- 选择了未知选项 id=" + id + "\n")
			continue
		}
		b.WriteString("- 选择了：" + opt.Label)
		if strings.TrimSpace(opt.Description) != "" {
			b.WriteString("（" + opt.Description + "）")
		}
		b.WriteString("\n")
	}
	if strings.TrimSpace(custom) != "" {
		b.WriteString("- 用户自定义补充方案：" + strings.TrimSpace(custom) + "\n")
	}
	return b.String()
}

type agentAIApprovalWaiter struct {
	sessionID string
	runSeq    int
	request   agentAIApprovalRequest
}

type agentAICompletedApproval struct {
	sessionID string
	runSeq    int
	response  agentAIApprovalResponse
	createdAt time.Time
}

type agentAICodexInputWaiter struct {
	sessionID string
	runSeq    int
	optionID  string
	respond   chan agentAICodexInputAnswer
}

type agentAICodexInputAnswer struct {
	selected []string
	custom   string
}

var (
	agentAIApprovalHookBaseURLMu sync.RWMutex
	agentAIApprovalHookBaseURL   = UserAgentBaseURL()
	claudeApprovalHookCache      sync.Map // map[executable fingerprint]claudeApprovalHookStrategy
)

type claudeApprovalHookStrategy string

const (
	claudeApprovalHookPreToolUseCommand     claudeApprovalHookStrategy = "pretool_command"
	claudeApprovalHookPermissionRequestHTTP claudeApprovalHookStrategy = "permission_request_http"
)

type agentAIClaudeRemotePolicy struct {
	enabled                 bool
	requireInitVerification bool
	projectSkillTrusted     bool
	projectCapabilityMode   string
	settingSources          []string
	disableSkillShell       bool
	permissionAsk           []string
}

type agentAIClaudeCapabilities struct {
	verified    bool
	projectPath string
	version     string
	generation  string
	commands    map[string]struct{}
}

func parseAgentAIClaudeRemotePolicy(msg map[string]interface{}) agentAIClaudeRemotePolicy {
	raw, ok := msg["claude_remote_policy"].(map[string]interface{})
	if !ok || raw == nil {
		return agentAIClaudeRemotePolicy{}
	}
	policy := agentAIClaudeRemotePolicy{
		enabled:                 true,
		requireInitVerification: remoteBool(raw, "require_system_init_verification", true),
		projectSkillTrusted:     remoteBool(raw, "project_skill_trusted", false),
		projectCapabilityMode:   remoteString(raw, "project_capability_mode"),
		settingSources:          remoteStringSlice(raw, "setting_sources"),
	}
	if policy.projectCapabilityMode != "sanitized_plugin" {
		policy.projectCapabilityMode = "disabled"
	}
	// Remote mode never reads filesystem settings. User/project/local permission
	// arrays merge across scopes in Claude Code, so even a higher-precedence empty
	// list cannot neutralize a stale user permissions.ask rule. Trusted project
	// capabilities are loaded separately through a sanitized temporary plugin.
	policy.settingSources = nil
	if settings, ok := raw["settings"].(map[string]interface{}); ok {
		policy.disableSkillShell = remoteBool(settings, "disableSkillShellExecution", true)
		if permissions, ok := settings["permissions"].(map[string]interface{}); ok {
			policy.permissionAsk = remoteStringSlice(permissions, "ask")
		}
	}
	if len(policy.permissionAsk) == 0 {
		policy.permissionAsk = []string{"Bash", "Edit", "Write", "NotebookEdit", "mcp__*"}
	}
	return policy
}

func cloneAgentAIClaudeRemotePolicy(policy agentAIClaudeRemotePolicy) agentAIClaudeRemotePolicy {
	policy.settingSources = append([]string(nil), policy.settingSources...)
	policy.permissionAsk = append([]string(nil), policy.permissionAsk...)
	return policy
}

func normalizeClaudeCapabilityName(name string) string {
	return strings.TrimPrefix(strings.TrimSpace(name), "/")
}

func claudeCapabilityProjectPath(path string) string {
	path = cleanAgentProjectPath(path)
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return filepath.Clean(resolved)
	}
	return path
}

func (m *agentAIManager) recordClaudeCapabilities(sessionID, projectPath string, names []string, version string) {
	if m == nil || strings.TrimSpace(sessionID) == "" {
		return
	}
	commands := make(map[string]struct{}, len(names))
	ordered := make([]string, 0, len(names))
	for _, name := range names {
		name = normalizeClaudeCapabilityName(name)
		if name == "" {
			continue
		}
		if _, exists := commands[name]; exists {
			continue
		}
		commands[name] = struct{}{}
		ordered = append(ordered, name)
	}
	sort.Strings(ordered)
	digest := sha256.Sum256([]byte(strings.Join(append([]string{strings.TrimSpace(version)}, ordered...), "\x00")))
	caps := agentAIClaudeCapabilities{
		verified:    true,
		projectPath: claudeCapabilityProjectPath(projectPath),
		version:     strings.TrimSpace(version),
		generation:  hex.EncodeToString(digest[:8]),
		commands:    commands,
	}
	m.mu.Lock()
	if session := m.sessions[sessionID]; session != nil && claudeCapabilityProjectPath(session.projectPath) == caps.projectPath {
		session.claudeCaps = caps
	}
	m.mu.Unlock()
}

func (m *agentAIManager) claudeCapabilities(sessionID, projectPath string) (agentAIClaudeCapabilities, bool) {
	if m == nil || strings.TrimSpace(sessionID) == "" {
		return agentAIClaudeCapabilities{}, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	session := m.sessions[sessionID]
	if session == nil || !session.claudeCaps.verified ||
		claudeCapabilityProjectPath(projectPath) != session.claudeCaps.projectPath {
		return agentAIClaudeCapabilities{}, false
	}
	caps := session.claudeCaps
	caps.commands = make(map[string]struct{}, len(session.claudeCaps.commands))
	for name := range session.claudeCaps.commands {
		caps.commands[name] = struct{}{}
	}
	return caps, true
}

var claudeCodeVersionRe = regexp.MustCompile(`\b(\d+)\.(\d+)\.(\d+)\b`)

type agentAIOutputFormat string

const (
	agentAIOutputText             agentAIOutputFormat = "text"
	agentAIOutputCodexJSON        agentAIOutputFormat = "codex_json"
	agentAIOutputClaudeStreamJSON agentAIOutputFormat = "claude_stream_json"
	agentAIOutputOpenCodeJSON     agentAIOutputFormat = "opencode_json"
)

// agentAIOptionSystemPrompt 引导 Claude 在需要用户多方案抉择时，输出结构化 aliang-options 块，
// 以便 agent 提取并发 ai.option.request。仅在 Claude 路径注入。
const agentAIOptionSystemPrompt = `When you have multiple viable approaches/options and the best choice depends on user preference, you MUST let the user choose instead of deciding for them. Present options using a fenced code block with language "aliang-options" containing one JSON object on its own line, shaped exactly: {"title":string,"options":[{"id":string,"label":string,"description":string}],"allow_custom":bool,"multi":bool}. Keep "id" short stable slugs. After the block, stop and wait for the user's choice in the next message; do not proceed on assumption.`

func newAgentAIManager() *agentAIManager {
	m := &agentAIManager{
		sessions:           make(map[string]*agentAISession),
		approvals:          make(map[string]*agentAIApprovalWaiter),
		completedApprovals: make(map[string]*agentAICompletedApproval),
		codexInputs:        make(map[string]*agentAICodexInputWaiter),
		pendingTerminals:   make(map[string]map[string]interface{}),
		bindings:           make(map[string]agentAIBindingRecord),
		processedRuns:      make(map[string]agentAIProcessedRun),
	}
	return m
}

func (m *agentAIManager) runEmitter(run agentAIRun, write agentTerminalWriter) *agentAIRunEmitter {
	e := newAgentAIRunEmitter(run, write)
	// Only protocol-v2 cloud runs have a Server ACK peer. Local in-app chat and
	// legacy servers omit run_id; journaling those terminals would leak forever.
	if strings.TrimSpace(run.runID) != "" {
		e.onTerminal = func(payload map[string]interface{}) error {
			if binding, ok := m.bindingForConversation(run.sessionID); ok {
				payload["native_session_id"] = binding.NativeSessionID
				payload["source_session_id"] = binding.NativeSessionID
				payload["binding_version"] = binding.BindingVersion
			}
			if err := m.rememberPendingTerminal(payload); err != nil {
				return err
			}
			return m.completeProcessedRun(run.runID, payload)
		}
	}
	return e
}

// registerAISessionLocked stores a new session, evicting the oldest idle
// session first if the resident map is at capacity. The session must NOT
// already be present — both call sites (create + message lazy-create) resolve
// or update an existing session before reaching here. MUST be called with m.mu
// held.
func (m *agentAIManager) registerAISessionLocked(session *agentAISession) {
	m.evictOldestIdleAISessionLocked()
	m.sessions[session.id] = session
}

// evictOldestIdleAISessionLocked drops the non-running session (cancel == nil)
// with the oldest lastActiveAt when the resident map is at/over
// agentAISessionResidentCap, bounding memory (each session holds up to
// agentAIHistoryCaptureMaxBytes). It never evicts a running turn; if every
// resident session is mid-turn it is a no-op and the caller adds anyway
// (temporary overshoot, re-bounded as soon as a turn settles). Eviction is
// silent to the server: an evicted conversation rebuilds on its next ai.message
// (lazy-create) and resumes from disk via resume_session_id. MUST be called
// with m.mu held.
func (m *agentAIManager) evictOldestIdleAISessionLocked() {
	if len(m.sessions) < agentAISessionResidentCap {
		return
	}
	var victim *agentAISession
	for _, s := range m.sessions {
		if s.cancel != nil {
			continue // never evict a running turn
		}
		if victim == nil || s.lastActiveAt.Before(victim.lastActiveAt) {
			victim = s
		}
	}
	if victim == nil {
		return // every resident session is running — allow temporary overshoot
	}
	m.clearPendingApprovalsLocked(victim.id, victim.runSeq, models.AgentAIApprovalDecisionCancel)
	victim.pendingOption = nil
	delete(m.sessions, victim.id)
	logger.Info(fmt.Sprintf("ai.session: evicted idle session %s to bound resident set (cap=%d, now=%d)",
		victim.id, agentAISessionResidentCap, len(m.sessions)))
}

// approvalService returns the AgentService that owns this manager (used to
// evaluate the device approval policy), falling back to the process-wide
// shared service for standalone test managers that have no owner.
func (m *agentAIManager) approvalService() *AgentService {
	if m.service != nil {
		return m.service
	}
	return GetSharedAgentService()
}

// agentAIActivity tracks whether an AI run is alive. A run is considered active
// while it emits output (bump), while it is blocked awaiting a human approval
// decision, or while Claude has dispatched tool/subagent work and is waiting for
// the corresponding tool_result. The watchdog cancels only when the run goes
// silent for longer than agentAIIdleWindow without one of those waits in flight;
// agentAIHardCeiling is a runaway backstop. nil-safe so call sites need no guards.
type agentAIActivity struct {
	lastActivityAt   atomic.Int64
	awaitingApproval atomic.Bool
	pendingToolUses  atomic.Int64
	runStart         time.Time
	killReasonMu     sync.RWMutex
	killReason       string
}

func newAgentAIActivity() *agentAIActivity {
	a := &agentAIActivity{runStart: time.Now()}
	a.lastActivityAt.Store(a.runStart.UnixNano())
	return a
}

func (a *agentAIActivity) bump() {
	if a == nil {
		return
	}
	a.lastActivityAt.Store(time.Now().UnixNano())
}

// setAwaitingApproval marks the run as blocked on a human decision (which pauses
// the idle watchdog) and bumps activity so resolving a decision grants a fresh
// idle window before the next output arrives.
func (a *agentAIActivity) setAwaitingApproval(v bool) {
	if a == nil {
		return
	}
	a.awaitingApproval.Store(v)
	a.bump()
}

func (a *agentAIActivity) awaiting() bool {
	if a == nil {
		return false
	}
	return a.awaitingApproval.Load()
}

// beginToolUseWait marks that Claude has emitted a tool_use (Task/subagent,
// Bash, etc.) and the run is legitimately silent until a matching tool_result
// arrives. It bumps activity so the run gets a fresh idle window once the tool
// completes.
func (a *agentAIActivity) beginToolUseWait() {
	if a == nil {
		return
	}
	a.pendingToolUses.Add(1)
	a.bump()
}

func (a *agentAIActivity) endToolUseWait() {
	if a == nil {
		return
	}
	for {
		current := a.pendingToolUses.Load()
		if current <= 0 {
			break
		}
		if a.pendingToolUses.CompareAndSwap(current, current-1) {
			break
		}
	}
	a.bump()
}

func (a *agentAIActivity) idlePaused() bool {
	if a == nil {
		return false
	}
	return a.awaitingApproval.Load() || a.pendingToolUses.Load() > 0
}

func (a *agentAIActivity) idleFor() time.Duration {
	if a == nil {
		return 0
	}
	last := a.lastActivityAt.Load()
	if last == 0 {
		return 0
	}
	return time.Since(time.Unix(0, last))
}

func (a *agentAIActivity) setKillReason(reason string) {
	if a == nil {
		return
	}
	a.killReasonMu.Lock()
	a.killReason = reason
	a.killReasonMu.Unlock()
}

func (a *agentAIActivity) killReasonOr(fallback string) string {
	if a == nil {
		return fallback
	}
	a.killReasonMu.RLock()
	reason := a.killReason
	a.killReasonMu.RUnlock()
	if reason != "" {
		return reason
	}
	return fallback
}

// startAIWatchdog cancels ctx when the run goes idle (no output for
// agentAIIdleWindow and not awaiting approval/tool results) or exceeds
// agentAIHardCeiling.
// It exits as soon as ctx is done, so it never outlives the run.
func (m *agentAIManager) startAIWatchdog(ctx context.Context, activity *agentAIActivity, cancel context.CancelFunc) {
	if activity == nil || cancel == nil {
		return
	}
	go agentAIWatchdogLoop(ctx, activity, cancel, agentAIIdleWindow, agentAIHardCeiling, agentAIIdleCheckInterval)
}

// agentAIWatchdogLoop is the parameterized watchdog body, split out so tests can
// drive it with tiny windows instead of the multi-minute production defaults.
func agentAIWatchdogLoop(ctx context.Context, activity *agentAIActivity, cancel context.CancelFunc, idleWindow, hardCeiling, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !activity.idlePaused() && activity.idleFor() > idleWindow {
				activity.setKillReason("idle_timeout")
				cancel()
				return
			}
			if hardCeiling > 0 && time.Since(activity.runStart) > hardCeiling {
				activity.setKillReason("hard_ceiling")
				cancel()
				return
			}
		}
	}
}

// agentAIRunStoppedStatus derives the terminal status/error for a run whose ctx
// was cancelled, from the watchdog kill reason (falling back to the output
// limiter or a plain stop).
func agentAIRunStoppedStatus(activity *agentAIActivity, limiter *agentAIOutputLimiter) (status string, errMsg string) {
	switch activity.killReasonOr("") {
	case "idle_timeout":
		return "idle_timeout", fmt.Sprintf("AI run went idle (no output for %s) and was stopped", agentAIIdleWindow)
	case "hard_ceiling":
		return "hard_timeout", fmt.Sprintf("AI run exceeded the maximum runtime %s", agentAIHardCeiling)
	}
	if limiter != nil && limiter.Exceeded() {
		return "output_limited", fmt.Sprintf("AI output exceeded rate limit (%d bytes per %s) or lifetime cap (%d bytes)", agentAIOutputRateBytes, agentAIOutputRateWindow, agentAIOutputCapBytes)
	}
	return "stopped", ""
}

func (s *AgentService) HandleAIApprovalHook(ctx context.Context, sessionID string, messageID string, token string, payload map[string]interface{}) (map[string]interface{}, error) {
	if s == nil || s.ai == nil {
		return claudeApprovalHookDecision(claudeApprovalHookEventName(payload), false, "Aliang AI approval service is not ready."), errors.New("AI approval service is not ready")
	}
	return s.ai.handleClaudeApprovalHook(ctx, sessionID, messageID, token, payload)
}

func SetAgentAIApprovalHookBaseURL(raw string) {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" {
		return
	}
	agentAIApprovalHookBaseURLMu.Lock()
	agentAIApprovalHookBaseURL = raw
	agentAIApprovalHookBaseURLMu.Unlock()
}

func agentAIApprovalHookURL(sessionID string, messageID string, token string) string {
	agentAIApprovalHookBaseURLMu.RLock()
	base := agentAIApprovalHookBaseURL
	agentAIApprovalHookBaseURLMu.RUnlock()
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		base = UserAgentBaseURL()
	}
	values := url.Values{}
	values.Set("session_id", sessionID)
	values.Set("message_id", messageID)
	values.Set("token", token)
	return base + "/api/agent/ai/approval-hook?" + values.Encode()
}

func currentAgentAIApprovalHookBaseURL() string {
	agentAIApprovalHookBaseURLMu.RLock()
	base := agentAIApprovalHookBaseURL
	agentAIApprovalHookBaseURLMu.RUnlock()
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		base = UserAgentBaseURL()
	}
	return base
}

func (m *agentAIManager) create(msg map[string]interface{}, writeJSON agentTerminalWriter) {
	if writeJSON == nil {
		return
	}
	sessionID := remoteString(msg, "session_id")
	if sessionID == "" {
		_ = writeJSON(agentAIErrorPayload("", "", errors.New("ai.session.create missing session_id")))
		return
	}
	projectPath, err := resolveAgentAICWD(remoteString(msg, "project_path"))
	if err != nil {
		_ = writeJSON(agentAIErrorPayload(sessionID, "", err))
		return
	}
	mode := remoteString(msg, "mode")
	if mode == "" {
		mode = "vibe"
	}
	provider := strings.TrimSpace(remoteString(msg, "provider"))
	if provider == "" {
		provider = strings.TrimSpace(remoteString(msg, "tool"))
	}
	provider, err = normalizeAgentAIProvider(provider)
	if err != nil {
		_ = writeJSON(agentAIErrorPayload(sessionID, "", err))
		return
	}
	model := strings.TrimSpace(remoteString(msg, "model"))
	effort := strings.TrimSpace(remoteString(msg, "effort"))
	resumeSessionID := firstNonEmpty(remoteString(msg, "resume_session_id"), remoteString(msg, "source_session_id"))
	reservedNativeSessionID := strings.TrimSpace(remoteString(msg, "reserved_native_session_id"))
	bindingVersion := remoteInt(msg, "binding_version", 0)
	initialContext := strings.TrimSpace(remoteString(msg, "initial_context"))
	claudePolicy := parseAgentAIClaudeRemotePolicy(msg)
	history := remoteAgentAIHistory(msg)
	if initialContext != "" {
		history = append([]agentAIMessage{{
			Role:      "system",
			MessageID: "initial_context",
			Content:   initialContext,
			CreatedAt: time.Now().UTC(),
		}}, history...)
	}

	m.mu.Lock()
	if existing := m.sessions[sessionID]; existing != nil {
		existing.mode = mode
		existing.projectPath = projectPath
		existing.provider = provider
		existing.model = model
		existing.effort = effort
		existing.resumeSessionID = resumeSessionID
		existing.reservedNativeSessionID = reservedNativeSessionID
		existing.bindingVersion = bindingVersion
		existing.initialContext = initialContext
		if claudePolicy.enabled {
			existing.claudePolicy = cloneAgentAIClaudeRemotePolicy(claudePolicy)
			existing.claudeCaps = agentAIClaudeCapabilities{}
		}
		if len(history) > 0 {
			existing.history = trimAgentAIHistory(history)
		}
		m.mu.Unlock()
		_ = writeJSON(agentAISessionCreatedPayload(existing))
		return
	}
	// NOTE: no count cap here. The real turn runs on ai.message, whose lazy-create
	// path (see message()) registers the session unconditionally — so a cap only
	// in create() was a dead check that emitted a misleading "limit reached"
	// error while the conversation still ran. Bound via eviction, not a gate.
	session := &agentAISession{
		id:                      sessionID,
		mode:                    mode,
		projectPath:             projectPath,
		provider:                provider,
		model:                   model,
		effort:                  effort,
		resumeSessionID:         resumeSessionID,
		reservedNativeSessionID: reservedNativeSessionID,
		bindingVersion:          bindingVersion,
		initialContext:          initialContext,
		claudePolicy:            cloneAgentAIClaudeRemotePolicy(claudePolicy),
		history:                 trimAgentAIHistory(history),
		lastActiveAt:            time.Now().UTC(),
	}
	m.registerAISessionLocked(session)
	m.mu.Unlock()

	_ = writeJSON(agentAISessionCreatedPayload(session))
}

func (m *agentAIManager) message(msg map[string]interface{}, writeJSON agentTerminalWriter) {
	if writeJSON == nil {
		return
	}
	sessionID := remoteString(msg, "session_id")
	messageID := remoteString(msg, "message_id")
	content := strings.TrimSpace(remoteString(msg, "content"))
	if sessionID == "" {
		_ = writeJSON(agentAIErrorPayload("", messageID, errors.New("ai.message missing session_id")))
		return
	}
	if messageID == "" {
		messageID = sessionID
	}
	messageRun := agentAIRun{
		sessionID:    sessionID,
		runID:        remoteString(msg, "run_id"),
		messageID:    messageID,
		goalIdentity: goalRunIdentityFromMessage(msg),
	}
	emitter := m.runEmitter(messageRun, writeJSON)
	runWrite := agentTerminalWriter(emitter.emit)
	if content == "" {
		_ = runWrite(agentAIErrorPayload(sessionID, messageID, errors.New("ai.message content is empty")))
		return
	}
	if len(content) > agentAIMessageLimitBytes {
		_ = runWrite(agentAIErrorPayload(sessionID, messageID, fmt.Errorf("ai.message exceeds %d bytes", agentAIMessageLimitBytes)))
		return
	}

	// Acknowledge receipt immediately, before any processing, so the cloud
	// admin's per-turn pipeline can mark "Agent 已收到" (confirmed) distinctly
	// from the server's own "送达 Agent" (dispatched). Mirrors
	// scripts/local-agent.ts. Optional for older agents — the server falls back
	// to "已转发·待确认" when this is absent.
	_ = runWrite(map[string]interface{}{
		"type":        models.AgentEventAIMessageReceived,
		"session_id":  sessionID,
		"message_id":  messageID,
		"received_at": time.Now().UTC().Format(time.RFC3339Nano),
	})

	m.mu.Lock()
	session := m.sessions[sessionID]
	m.mu.Unlock()
	if session == nil {
		// Lazy creation: a session is born on its first ai.message. The prior
		// ai.session.create may have errored (the server validates project_path
		// is AUTHORIZED but not that it EXISTS on disk — a path that's
		// authorized-but-missing fails the agent's create), been dropped /
		// reordered across a flaky link, or never sent. The old behavior — a
		// generic "ai session not found" — ended the turn immediately AND masked
		// the real cause. Register on demand from the message's own fields; if a
		// field is invalid, surface THAT clear error instead. Validation runs
		// outside the manager lock (filesystem ops).
		projectPath, cwdErr := resolveAgentAICWD(remoteString(msg, "project_path"))
		if cwdErr != nil {
			_ = runWrite(agentAIErrorPayload(sessionID, messageID, cwdErr))
			return
		}
		lazyProvider, providerErr := normalizeAgentAIProvider(firstNonEmpty(
			strings.TrimSpace(remoteString(msg, "provider")),
			strings.TrimSpace(remoteString(msg, "tool")),
		))
		if providerErr != nil {
			_ = runWrite(agentAIErrorPayload(sessionID, messageID, providerErr))
			return
		}
		lazyMode := remoteString(msg, "mode")
		if lazyMode == "" {
			lazyMode = "vibe"
		}
		session = &agentAISession{
			id:                      sessionID,
			mode:                    lazyMode,
			projectPath:             projectPath,
			provider:                lazyProvider,
			model:                   strings.TrimSpace(remoteString(msg, "model")),
			effort:                  strings.TrimSpace(remoteString(msg, "effort")),
			resumeSessionID:         firstNonEmpty(remoteString(msg, "resume_session_id"), remoteString(msg, "source_session_id")),
			reservedNativeSessionID: strings.TrimSpace(remoteString(msg, "reserved_native_session_id")),
			bindingVersion:          remoteInt(msg, "binding_version", 0),
			claudePolicy:            cloneAgentAIClaudeRemotePolicy(parseAgentAIClaudeRemotePolicy(msg)),
			lastActiveAt:            time.Now().UTC(),
		}
		m.mu.Lock()
		if existing := m.sessions[sessionID]; existing != nil {
			session = existing // a concurrent create/message registered it first
		} else {
			m.registerAISessionLocked(session)
		}
		m.mu.Unlock()
	}
	m.mu.Lock()
	if policy := parseAgentAIClaudeRemotePolicy(msg); policy.enabled {
		session.claudePolicy = cloneAgentAIClaudeRemotePolicy(policy)
		session.claudeCaps = agentAIClaudeCapabilities{}
	}
	session.lastActiveAt = time.Now().UTC() // LRU recency: this conversation is active
	if session.cancel != nil {
		m.mu.Unlock()
		_ = runWrite(agentAIErrorPayload(sessionID, messageID, fmt.Errorf("ai session is already running: %s", sessionID)))
		return
	}
	m.mu.Unlock()

	provider, err := normalizeAgentAIProvider(firstNonEmpty(strings.TrimSpace(remoteString(msg, "provider")), strings.TrimSpace(remoteString(msg, "tool")), session.provider))
	if err != nil {
		_ = runWrite(agentAIErrorPayload(sessionID, messageID, err))
		return
	}
	attachments, err := resolveAgentAIAttachments(msg["attachments"], session.projectPath)
	if err != nil {
		_ = runWrite(agentAIErrorPayload(sessionID, messageID, err))
		return
	}
	if len(messageRun.goalIdentity) > 0 {
		if err := emitter.captureGoalWorkspace(session.projectPath); err != nil {
			_ = runWrite(agentAIErrorPayload(sessionID, messageID, fmt.Errorf("goal workspace fingerprint failed: %w", err)))
			return
		}
	}

	// Slash-command dispatcher: agent-local builtins (/clear /model /help /cost
	// /compact) run WITHOUT a model turn. The Go agent drives the CLI headlessly
	// (claude --print --resume / codex app-server --stdio), so there is no live
	// REPL to receive interactive slash commands — these are direct session
	// operations (/clear /model) or honest status replies (/help /cost /compact).
	// Prompt-style commands (/review, custom .claude/commands) and unknown /xxx
	// fall through to the normal model turn below.
	if m.handleLocalSlashCommand(session, messageID, content, provider, runWrite) {
		return
	}

	if err := m.runUserMessage(session, remoteString(msg, "run_id"), messageID, content, provider, attachments, emitter); err != nil {
		_ = runWrite(agentAIErrorPayload(sessionID, messageID, err))
	}
}

// runStart is the protocol-v3 atomic create+message command. The server may
// redeliver it from its durable outbox; replayProcessedRun guarantees the same
// run never launches a second provider process.
func (m *agentAIManager) runStart(msg map[string]interface{}, writeJSON agentTerminalWriter) {
	runID := strings.TrimSpace(remoteString(msg, "run_id"))
	sessionID := strings.TrimSpace(remoteString(msg, "session_id"))
	messageID := strings.TrimSpace(remoteString(msg, "message_id"))
	if runID == "" || sessionID == "" || messageID == "" {
		_ = writeJSON(agentAIErrorPayload(sessionID, messageID, errors.New("ai.run.start missing session_id, run_id, or message_id")))
		return
	}
	if m.replayProcessedRun(runID, writeJSON) {
		return
	}
	claimed, claimErr := m.claimProcessedRun(
		runID,
		sessionID,
		messageID,
		goalRunIdentityFromMessage(msg),
	)
	if claimErr != nil {
		// Do not launch without a durable claim and do not emit a terminal run
		// event: the server outbox must remain pending and retry after storage
		// recovers.
		logger.Warn(fmt.Sprintf("ai.run.start durable claim failed run=%s: %v", runID, claimErr))
		return
	}
	if !claimed {
		m.replayProcessedRun(runID, writeJSON)
		return
	}

	reservedID := strings.TrimSpace(remoteString(msg, "reserved_native_session_id"))
	resumeID := firstNonEmpty(
		remoteString(msg, "resume_session_id"),
		remoteString(msg, "source_session_id"),
		remoteString(msg, "native_session_id"),
	)
	provider := firstNonEmpty(remoteString(msg, "provider"), remoteString(msg, "tool"), "auto")
	bindingVersion := remoteInt(msg, "binding_version", 1)
	if err := m.reserveBinding(sessionID, provider, reservedID, bindingVersion); err != nil {
		// The run was already durably claimed. Emit through the normal v2 terminal
		// barrier so the cloud can settle it and a redelivery replays this exact
		// terminal instead of leaving a permanently "received" processed-run row.
		emitter := m.runEmitter(agentAIRun{
			runID: runID, sessionID: sessionID, messageID: messageID,
			goalIdentity: goalRunIdentityFromMessage(msg),
		}, writeJSON)
		_ = emitter.emit(agentAIErrorPayload(sessionID, messageID, err))
		return
	}
	m.mu.Lock()
	if persisted := m.bindings[sessionID]; persisted.NativeSessionID != "" {
		if persisted.State == "confirmed" {
			resumeID = persisted.NativeSessionID
			reservedID = ""
		} else if reservedID == "" {
			reservedID = persisted.NativeSessionID
		}
	}
	if session := m.sessions[sessionID]; session != nil {
		session.mode = firstNonEmpty(remoteString(msg, "mode"), session.mode, "vibe")
		session.projectPath = firstNonEmpty(remoteString(msg, "project_path"), session.projectPath)
		session.provider = firstNonEmpty(remoteString(msg, "provider"), remoteString(msg, "tool"), session.provider)
		session.model = strings.TrimSpace(remoteString(msg, "model"))
		session.effort = strings.TrimSpace(remoteString(msg, "effort"))
		session.resumeSessionID = resumeID
		session.reservedNativeSessionID = reservedID
		session.bindingVersion = remoteInt(msg, "binding_version", session.bindingVersion)
		if policy := parseAgentAIClaudeRemotePolicy(msg); policy.enabled {
			session.claudePolicy = cloneAgentAIClaudeRemotePolicy(policy)
			session.claudeCaps = agentAIClaudeCapabilities{}
		}
	}
	m.mu.Unlock()

	msg["resume_session_id"] = resumeID
	msg["reserved_native_session_id"] = reservedID
	m.message(msg, writeJSON)
}

// parseLocalSlashCommand recognizes a leading "/<name>" with optional args.
// Returns ok=false when content is not a slash command or the name is empty.
// Callers further filter to the agent-local builtin set; anything else falls
// through to a normal model turn.
func parseLocalSlashCommand(content string) (name, args string, ok bool) {
	if !strings.HasPrefix(content, "/") {
		return "", "", false
	}
	rest := content[1:]
	if rest == "" {
		return "", "", false
	}
	for i := 0; i < len(rest); i++ {
		c := rest[i]
		if c == ' ' || c == '\t' {
			return strings.ToLower(rest[:i]), strings.TrimSpace(rest[i:]), true
		}
	}
	return strings.ToLower(rest), "", true
}

// localSlashBuiltins is the set of agent-local slash commands handled without a
// model turn. Keep in sync with the catalogs' remote='local' entries
// (AliangPhoneServer server/src/agentCommands.ts, the phone's
// src/utils/agentCommands.ts, and scripts/local-agent.ts BUILTIN_COMMANDS).
var localSlashBuiltins = map[string]struct{}{
	"clear":   {},
	"model":   {},
	"help":    {},
	"cost":    {},
	"compact": {},
}

// handleLocalSlashCommand intercepts agent-local slash commands and runs them
// without spawning a model turn, replying with ai.run.started + ai.delta(status)
// + ai.done so the phone settles with a clear outcome. Returns true when handled
// (the caller then skips runUserMessage).
func (m *agentAIManager) handleLocalSlashCommand(session *agentAISession, messageID, content, provider string, writeJSON agentTerminalWriter) bool {
	name, args, ok := parseLocalSlashCommand(content)
	if !ok {
		return false
	}
	if _, isLocal := localSlashBuiltins[name]; !isLocal {
		return false // prompt-style (/review, custom .claude/commands) or unknown → normal turn
	}
	if name == "compact" && provider == "codex" {
		m.handleCodexCompactCommand(session, messageID, writeJSON)
		return true
	}

	assistantID := agentAssistantMessageID(messageID)
	_ = writeJSON(map[string]interface{}{
		"type":         models.AgentEventAIRunStarted,
		"session_id":   session.id,
		"message_id":   assistantID,
		"provider":     provider,
		"mode":         session.mode,
		"project_path": session.projectPath,
		"state":        "running",
	})

	line := m.applyLocalSlashCommand(session, name, args, provider)

	_ = writeJSON(map[string]interface{}{
		"type":       models.AgentEventAIDelta,
		"session_id": session.id,
		"message_id": assistantID,
		"channel":    "stdout",
		"delta":      line + "\n",
	})
	_ = writeJSON(map[string]interface{}{
		"type":       models.AgentEventAIDone,
		"session_id": session.id,
		"message_id": assistantID,
	})
	return true
}

// applyLocalSlashCommand performs the side effect (if any) for a local slash
// command and returns a single status line streamed back as the reply.
func (m *agentAIManager) applyLocalSlashCommand(session *agentAISession, name, args, provider string) string {
	switch name {
	case "clear":
		// Detach from the current Claude session so the next turn starts FRESH
		// (no --resume). resumeSessionID may have been set at ai.session.create
		// or recaptured post-run (setAgentAIResumeSessionIDIfEmpty); clearing it
		// drops that binding. history is cleared too so the non-resume fallback
		// prompt (buildAgentAIPrompt) starts clean. The next turn spawns a fresh
		// claude run that captures a NEW session id, and continuity from there
		// is owned by Claude's own session manager (not agent-side history).
		m.mu.Lock()
		session.resumeSessionID = ""
		session.history = nil
		session.runSeq = 0
		m.mu.Unlock()
		return fmt.Sprintf("✓ /clear 已清空。下一轮将开启全新的 %s 会话。", agentAIProviderDisplayName(provider))
	case "model":
		newModel := strings.TrimSpace(args)
		if newModel == "" {
			m.mu.Lock()
			cur := session.model
			m.mu.Unlock()
			if cur == "" {
				return "当前未指定模型(使用 CLI 默认)。用法:/model <name>"
			}
			return fmt.Sprintf("当前模型: %s。用法:/model <name> 切换。", cur)
		}
		m.mu.Lock()
		session.model = newModel
		m.mu.Unlock()
		return fmt.Sprintf("✓ /model 已切换为 %s(下一轮生效)。", newModel)
	case "help":
		return "本机命令(remote=local): /clear  /model <name>  /help  /cost  /compact\n注: /compact 在 headless 模式不支持真实压缩。"
	case "cost":
		// Per-turn usage is emitted as ai.usage but not accumulated on the
		// session, so there is no running total to report here.
		return "⚠ headless agent 未累计会话用量。请在桌面端查看 token / 费用统计。"
	case "compact":
		return fmt.Sprintf("⚠ /compact 暂不支持 %s 的 headless 模式。建议用 /clear 开启新会话。", agentAIProviderDisplayName(provider))
	}
	return ""
}

func agentAIProviderDisplayName(provider string) string {
	switch provider {
	case "codex":
		return "Codex"
	case "opencode":
		return "OpenCode"
	case "claude", "claudecode":
		return "Claude Code"
	default:
		return "AI"
	}
}

func (m *agentAIManager) handleCodexCompactCommand(session *agentAISession, messageID string, writeJSON agentTerminalWriter) {
	m.mu.Lock()
	threadID := strings.TrimSpace(session.resumeSessionID)
	projectPath := session.projectPath
	mode := session.mode
	m.mu.Unlock()
	assistantID := agentAssistantMessageID(messageID)
	_ = writeJSON(map[string]interface{}{
		"type":         models.AgentEventAIRunStarted,
		"session_id":   session.id,
		"message_id":   assistantID,
		"provider":     "codex",
		"mode":         mode,
		"project_path": projectPath,
		"state":        "running",
	})
	if threadID == "" {
		_ = writeJSON(map[string]interface{}{
			"type": models.AgentEventAIDelta, "session_id": session.id, "message_id": assistantID,
			"channel": "stdout", "delta": "当前 Codex 会话还没有可压缩的原生线程。\n",
		})
		_ = writeJSON(map[string]interface{}{"type": models.AgentEventAIDone, "session_id": session.id, "message_id": assistantID})
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := compactCodexThread(ctx, projectPath, threadID); err != nil {
		_ = writeJSON(agentAIErrorPayload(session.id, messageID, err))
		return
	}
	_ = writeJSON(map[string]interface{}{
		"type": models.AgentEventAIDelta, "session_id": session.id, "message_id": assistantID,
		"channel": "stdout", "delta": "✓ Codex 原生线程已压缩。\n",
	})
	_ = writeJSON(map[string]interface{}{
		"type": models.AgentEventAIDone, "session_id": session.id, "message_id": assistantID,
		"codex_thread_id": threadID, "source_session_id": threadID,
	})
}

func compactCodexThread(ctx context.Context, projectPath, threadID string) error {
	path, err := lookPathCLI("codex")
	if err != nil {
		return err
	}
	if !codexAppServerAvailable() {
		return errors.New("Codex app-server is unavailable; cannot compact this thread")
	}
	cmd := newBackgroundCommandContext(ctx, path, "app-server", "--stdio")
	cmd.Dir = projectPath
	cmd.Env = agentChildProcessEnv()
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	var stderrBuf strings.Builder
	var stderrWG sync.WaitGroup
	stderrWG.Add(1)
	go func() {
		defer stderrWG.Done()
		captureAgentAIStderr(stderr, &stderrBuf)
	}()
	defer func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
		stderrWG.Wait()
	}()
	send := func(payload map[string]interface{}) error {
		raw, marshalErr := json.Marshal(payload)
		if marshalErr != nil {
			return marshalErr
		}
		_, writeErr := stdin.Write(append(raw, '\n'))
		return writeErr
	}
	if err := send(map[string]interface{}{
		"method": "initialize", "id": 0,
		"params": map[string]interface{}{"clientInfo": map[string]interface{}{"name": "alianggate", "title": "Aliang Agent", "version": "0.1.0"}},
	}); err != nil {
		return err
	}
	if err := send(map[string]interface{}{"method": "initialized", "params": map[string]interface{}{}}); err != nil {
		return err
	}
	if err := send(map[string]interface{}{"method": "thread/compact/start", "id": 1, "params": map[string]interface{}{"threadId": threadID}}); err != nil {
		return err
	}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		var message map[string]interface{}
		if json.Unmarshal(scanner.Bytes(), &message) != nil || fmt.Sprint(message["id"]) != "1" {
			continue
		}
		if detail, failed := codexAppServerResponseError(message); failed {
			return fmt.Errorf("Codex thread compact failed: %s", detail)
		}
		return nil
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if detail := strings.TrimSpace(stderrBuf.String()); detail != "" {
		return fmt.Errorf("Codex app-server exited during compact: %s", truncateForCloud(detail))
	}
	return errors.New("Codex app-server exited before compact completed")
}

func (m *agentAIManager) steer(msg map[string]interface{}, writeJSON agentTerminalWriter) {
	if writeJSON == nil {
		return
	}
	sessionID := remoteString(msg, "session_id")
	messageID := remoteString(msg, "message_id")
	content := strings.TrimSpace(remoteString(msg, "content"))
	if sessionID == "" {
		emitAgentAISteerAck(writeJSON, "", messageID, "error", "ai.steer missing session_id", "bad_request")
		return
	}
	if messageID == "" {
		messageID = sessionID
	}
	if content == "" {
		emitAgentAISteerAck(writeJSON, sessionID, messageID, "error", "ai.steer content is empty", "bad_request")
		return
	}
	if len(content) > agentAIMessageLimitBytes {
		emitAgentAISteerAck(writeJSON, sessionID, messageID, "error", fmt.Sprintf("ai.steer exceeds %d bytes", agentAIMessageLimitBytes), "bad_request")
		return
	}

	m.mu.Lock()
	session := m.sessions[sessionID]
	if session == nil {
		m.mu.Unlock()
		emitAgentAISteerAck(writeJSON, sessionID, messageID, "not_running", fmt.Sprintf("ai session not found: %s", sessionID), "")
		return
	}
	if session.cancel == nil {
		m.mu.Unlock()
		emitAgentAISteerAck(writeJSON, sessionID, messageID, "not_running", fmt.Sprintf("ai session is not running: %s", sessionID), "")
		return
	}
	control := session.codexSteer
	if control == nil {
		m.mu.Unlock()
		emitAgentAISteerAck(writeJSON, sessionID, messageID, "unsupported", "ai.steer is only supported by active Codex app-server runs", "unsupported_provider")
		return
	}
	now := time.Now().UTC()
	session.history = append(session.history, agentAIMessage{
		Role:      "user",
		MessageID: messageID,
		Content:   content,
		CreatedAt: now,
	})
	m.mu.Unlock()

	result, err := control.enqueue(agentAISteerMessage{
		MessageID: messageID,
		Content:   content,
		CreatedAt: now,
	})
	if err != nil {
		emitAgentAISteerAck(writeJSON, sessionID, messageID, "error", err.Error(), "send_failed")
		return
	}
	emitAgentAISteerAck(writeJSON, sessionID, messageID, result, "", "")
}

func emitAgentAISteerAck(writeJSON agentTerminalWriter, sessionID, messageID, result, errMsg, code string) {
	if writeJSON == nil {
		return
	}
	payload := map[string]interface{}{
		"type":       models.AgentEventAISteerAck,
		"session_id": sessionID,
		"message_id": messageID,
		"result":     result,
		"acked_at":   time.Now().UTC().Format(time.RFC3339Nano),
	}
	if errMsg != "" {
		payload["error"] = errMsg
	}
	if code != "" {
		payload["code"] = code
	}
	_ = writeJSON(payload)
}

func (c *agentAICodexSteerControl) enqueue(msg agentAISteerMessage) (string, error) {
	if c == nil {
		return "", errors.New("codex steer control is unavailable")
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return "", errors.New("codex turn is no longer running")
	}
	if c.threadID == "" || c.turnID == "" {
		c.queue = append(c.queue, msg)
		c.mu.Unlock()
		return "queued", nil
	}
	id, payload := c.nextSteerPayloadLocked(msg)
	send := c.send
	c.mu.Unlock()
	if err := send(payload); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return "", err
	}
	return "queued", nil
}

func (c *agentAICodexSteerControl) markReady(threadID, turnID string) {
	if c == nil {
		return
	}
	type pendingSend struct {
		id      string
		payload map[string]interface{}
	}
	var sends []pendingSend
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	if strings.TrimSpace(threadID) != "" {
		c.threadID = strings.TrimSpace(threadID)
	}
	if strings.TrimSpace(turnID) != "" {
		c.turnID = strings.TrimSpace(turnID)
	}
	if c.threadID != "" && c.turnID != "" && len(c.queue) > 0 {
		queued := c.queue
		c.queue = nil
		sends = make([]pendingSend, 0, len(queued))
		for _, item := range queued {
			id, payload := c.nextSteerPayloadLocked(item)
			sends = append(sends, pendingSend{id: id, payload: payload})
		}
	}
	send := c.send
	c.mu.Unlock()

	for _, item := range sends {
		if err := send(item.payload); err != nil {
			c.completePending(item.id, "error", err.Error(), "send_failed")
		}
	}
}

func (c *agentAICodexSteerControl) nextSteerPayloadLocked(msg agentAISteerMessage) (string, map[string]interface{}) {
	c.nextID++
	id := "aliang_steer_" + strconv.FormatInt(c.nextID, 10)
	c.pending[id] = msg
	return id, map[string]interface{}{
		"method": "turn/steer",
		"id":     id,
		"params": map[string]interface{}{
			"threadId":            c.threadID,
			"expectedTurnId":      c.turnID,
			"clientUserMessageId": msg.MessageID,
			"input": []map[string]interface{}{
				{"type": "text", "text": msg.Content, "text_elements": []interface{}{}},
			},
		},
	}
}

func (c *agentAICodexSteerControl) handleResponse(msg map[string]interface{}) bool {
	if c == nil {
		return false
	}
	id := fmt.Sprint(msg["id"])
	if strings.TrimSpace(id) == "" || id == "<nil>" {
		return false
	}
	c.mu.Lock()
	steer, ok := c.pending[id]
	if ok {
		delete(c.pending, id)
	}
	c.mu.Unlock()
	if !ok {
		return false
	}
	if errObj, ok := msg["error"].(map[string]interface{}); ok {
		message := firstNonEmpty(remoteString(errObj, "message"), remoteString(errObj, "additionalDetails"), "codex turn/steer failed")
		code := remoteString(errObj, "code")
		if info, ok := errObj["codexErrorInfo"].(map[string]interface{}); ok {
			code = firstNonEmpty(remoteString(info, "code"), code)
		}
		emitAgentAISteerAck(c.write, c.sessionID, steer.MessageID, "error", message, code)
		return true
	}
	emitAgentAISteerAck(c.write, c.sessionID, steer.MessageID, "applied", "", "")
	return true
}

func (c *agentAICodexSteerControl) completePending(id, result, errMsg, code string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	steer, ok := c.pending[id]
	if ok {
		delete(c.pending, id)
	}
	c.mu.Unlock()
	if ok {
		emitAgentAISteerAck(c.write, c.sessionID, steer.MessageID, result, errMsg, code)
	}
}

func (c *agentAICodexSteerControl) close(result, errMsg, code string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	items := make([]agentAISteerMessage, 0, len(c.queue)+len(c.pending))
	items = append(items, c.queue...)
	for _, item := range c.pending {
		items = append(items, item)
	}
	c.queue = nil
	c.pending = make(map[string]agentAISteerMessage)
	write := c.write
	c.mu.Unlock()

	for _, item := range items {
		emitAgentAISteerAck(write, c.sessionID, item.MessageID, result, errMsg, code)
	}
}

func (m *agentAIManager) setCodexSteerControl(sessionID string, runSeq int, control *agentAICodexSteerControl) {
	m.mu.Lock()
	if session := m.sessions[sessionID]; session != nil && session.runSeq == runSeq {
		session.codexSteer = control
	}
	m.mu.Unlock()
}

// runUserMessage 在 session 上派发一轮新的 AI run。message()（用户消息）与
// optionResponse()（用户方案选择续接）共用。调用者须已确认 session 存在且当前未在跑。
func (m *agentAIManager) runUserMessage(session *agentAISession, runID, messageID, content, provider string, attachments []agentAIAttachment, emitter *agentAIRunEmitter) error {
	writeJSON := agentTerminalWriter(emitter.emit)
	approvalToken, err := newAgentAIApprovalToken()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	activity := newAgentAIActivity()
	now := time.Now().UTC()

	m.mu.Lock()
	session.cancel = cancel
	session.activeWriter = writeJSON
	session.approvalToken = approvalToken
	session.activity = activity
	session.runSeq++
	resumeSessionID := strings.TrimSpace(session.resumeSessionID)
	reservedNativeSessionID := strings.TrimSpace(session.reservedNativeSessionID)
	logger.Info(fmt.Sprintf("ai.run: session=%s runSeq=%d resumeSessionID=%q (empty=fresh claude run this turn; non-empty=--resume)", session.id, session.runSeq, resumeSessionID))
	session.history = append(session.history, agentAIMessage{
		Role:      "user",
		MessageID: messageID,
		Content:   content,
		CreatedAt: now,
	})
	freshPrompt := buildAgentAIPrompt(session, content)
	prompt := freshPrompt
	if resumeSessionID != "" {
		prompt = content
	}
	run := agentAIRun{
		sessionID:               session.id,
		runID:                   runID,
		messageID:               messageID,
		runSeq:                  session.runSeq,
		mode:                    session.mode,
		projectPath:             session.projectPath,
		provider:                provider,
		model:                   session.model,
		effort:                  session.effort,
		resumeSessionID:         resumeSessionID,
		reservedNativeSessionID: reservedNativeSessionID,
		bindingVersion:          session.bindingVersion,
		prompt:                  prompt,
		freshPrompt:             freshPrompt,
		attachments:             append([]agentAIAttachment(nil), attachments...),
		cancel:                  cancel,
		approvalToken:           approvalToken,
		activity:                activity,
		claudePolicy:            cloneAgentAIClaudeRemotePolicy(session.claudePolicy),
		goalIdentity:            cloneGoalIdentity(emitter.goalIdentity),
	}
	run.onClaudeInit = func(commands []string, version string) {
		m.recordClaudeCapabilities(run.sessionID, run.projectPath, commands, version)
	}
	m.mu.Unlock()

	// Best-effort sync of the device approval policy before the run starts, so a
	// just-changed policy (or a fresh process with only the built-in default)
	// picks up the current rules. Never blocks beyond a short timeout or errors
	// the turn; failures keep the cached/built-in policy.
	if svc := m.approvalService(); svc != nil {
		svc.ensurePolicyBeforeRun(ctx, session.projectPath)
	}
	m.startAIWatchdog(ctx, activity, cancel)
	m.mu.Lock()
	if current := m.sessions[run.sessionID]; current != nil && current.runSeq == run.runSeq {
		// Approval HTTP hooks emit asynchronously through activeWriter. Point them
		// at the same per-run emitter so they share run_id/event_seq and cannot
		// escape the terminal barrier through the original socket writer.
		current.activeWriter = emitter.emit
	}
	m.mu.Unlock()
	go m.runCLI(ctx, run, emitter.emit)
	return nil
}

// optionResponse 处理用户对 ai.option.request 的选择：清除 pendingOption 并按
// 选择结果续接一轮新的 AI run。
func (m *agentAIManager) optionResponse(msg map[string]interface{}, writeJSON agentTerminalWriter) {
	if writeJSON == nil {
		return
	}
	sessionID := remoteString(msg, "session_id")
	optionID := strings.TrimSpace(remoteString(msg, "option_id"))
	if sessionID == "" || optionID == "" {
		_ = writeJSON(agentAIErrorPayload(sessionID, remoteString(msg, "message_id"), errors.New("ai.option.response missing session_id or option_id")))
		return
	}
	selected := remoteStringSlice(msg, "selected")
	custom := strings.TrimSpace(remoteString(msg, "custom_text"))

	m.mu.Lock()
	inputKey := agentAICodexInputMapKey(sessionID, optionID)
	if waiter := m.codexInputs[inputKey]; waiter != nil && waiter.sessionID == sessionID {
		delete(m.codexInputs, inputKey)
		m.mu.Unlock()
		waiter.respond <- agentAICodexInputAnswer{selected: selected, custom: custom}
		return
	}
	session := m.sessions[sessionID]
	if session == nil || session.cancel != nil || session.pendingOption == nil || session.pendingOption.ID != optionID {
		m.mu.Unlock()
		return
	}
	pending := session.pendingOption
	session.pendingOption = nil
	provider := session.provider
	m.mu.Unlock()

	content := buildAgentAIOptionFollowup(pending, selected, custom)
	messageID := pending.MessageID + ".option"

	run := agentAIRun{sessionID: sessionID, runID: remoteString(msg, "run_id"), messageID: messageID}
	emitter := m.runEmitter(run, writeJSON)
	if err := m.runUserMessage(session, remoteString(msg, "run_id"), messageID, content, provider, nil, emitter); err != nil {
		_ = emitter.emit(agentAIErrorPayload(sessionID, messageID, err))
	}
}

func agentAICodexInputMapKey(sessionID, optionID string) string {
	return sessionID + "\x00" + optionID
}

func agentAISessionCreatedPayload(session *agentAISession) map[string]interface{} {
	payload := map[string]interface{}{
		"type":         models.AgentEventAISessionCreated,
		"session_id":   session.id,
		"mode":         session.mode,
		"project_path": session.projectPath,
		"provider":     session.provider,
		"model":        session.model,
		"effort":       session.effort,
		"state":        "idle",
	}
	if session.resumeSessionID != "" {
		payload["resume_session_id"] = session.resumeSessionID
	}
	return payload
}

func remoteAgentAIHistory(msg map[string]interface{}) []agentAIMessage {
	raw, ok := msg["transcript"].([]interface{})
	if !ok || len(raw) == 0 {
		return nil
	}
	history := make([]agentAIMessage, 0, len(raw))
	for _, item := range raw {
		row, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		content := strings.TrimSpace(remoteString(row, "content"))
		if content == "" {
			continue
		}
		history = append(history, agentAIMessage{
			Role:      remoteString(row, "role"),
			MessageID: remoteString(row, "id"),
			Content:   content,
			CreatedAt: time.Now().UTC(),
		})
	}
	return history
}

func resolveAgentAIAttachments(raw interface{}, projectPath string) ([]agentAIAttachment, error) {
	items, _ := raw.([]interface{})
	if len(items) == 0 {
		return nil, nil
	}
	projectRoot, err := cleanExistingAgentDirectory(projectPath)
	if err != nil {
		return nil, fmt.Errorf("resolve attachment project directory: %w", err)
	}
	const maxAttachments = 8
	const maxAttachmentBytes = 20 * 1024 * 1024
	const maxTotalBytes = 50 * 1024 * 1024
	if len(items) > maxAttachments {
		return nil, fmt.Errorf("ai.message attachments exceed %d files", maxAttachments)
	}
	attachments := make([]agentAIAttachment, 0, len(items))
	var totalBytes int64
	for _, rawItem := range items {
		item := mapIf(rawItem)
		if item == nil {
			return nil, errors.New("ai.message attachment must be an object")
		}
		attachment := agentAIAttachment{
			Type: strings.ToLower(strings.TrimSpace(remoteString(item, "type"))),
			Name: strings.TrimSpace(remoteString(item, "name")),
			Path: strings.TrimSpace(firstNonEmpty(remoteString(item, "path"), remoteString(item, "local_path"))),
			URL:  strings.TrimSpace(remoteString(item, "url")),
		}
		if attachment.Path != "" {
			path := attachment.Path
			if !filepath.IsAbs(path) {
				path = filepath.Join(projectPath, path)
			}
			absolute, err := filepath.Abs(path)
			if err != nil {
				return nil, err
			}
			if resolved, resolveErr := filepath.EvalSymlinks(absolute); resolveErr == nil {
				absolute = resolved
			}
			if !agentPathInsideDirectory(absolute, projectRoot) {
				return nil, fmt.Errorf("attachment path is outside the project directory: %s", absolute)
			}
			stat, err := os.Stat(absolute)
			if err != nil {
				return nil, err
			}
			if !stat.Mode().IsRegular() {
				return nil, fmt.Errorf("attachment is not a regular file: %s", absolute)
			}
			if stat.Size() > maxAttachmentBytes {
				return nil, fmt.Errorf("attachment exceeds %d MiB: %s", maxAttachmentBytes/(1024*1024), absolute)
			}
			totalBytes += stat.Size()
			if totalBytes > maxTotalBytes {
				return nil, fmt.Errorf("attachments exceed %d MiB in total", maxTotalBytes/(1024*1024))
			}
			attachment.Path = absolute
			if attachment.Name == "" {
				attachment.Name = filepath.Base(absolute)
			}
			if attachment.Type == "" && isImageAttachmentPath(absolute) {
				attachment.Type = "image"
			}
		}
		if attachment.URL != "" {
			parsed, err := url.Parse(attachment.URL)
			if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "data") {
				return nil, errors.New("attachment URL must use https or data scheme")
			}
			if parsed.Scheme == "data" && !strings.HasPrefix(strings.ToLower(attachment.URL), "data:image/") {
				return nil, errors.New("only image data URLs are supported")
			}
			if attachment.Type == "" {
				attachment.Type = "image"
			}
		}
		if attachment.Path == "" && attachment.URL == "" {
			return nil, errors.New("attachment requires path or url")
		}
		attachments = append(attachments, attachment)
	}
	return attachments, nil
}

func isImageAttachmentPath(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp":
		return true
	default:
		return false
	}
}

func (m *agentAIManager) stop(msg map[string]interface{}, writeJSON agentTerminalWriter) {
	if writeJSON == nil {
		return
	}
	sessionID := remoteString(msg, "session_id")
	if sessionID == "" {
		_ = writeJSON(agentAIErrorPayload("", "", errors.New("ai.stop missing session_id")))
		return
	}

	m.mu.Lock()
	session := m.sessions[sessionID]
	if session == nil {
		m.mu.Unlock()
		_ = writeJSON(map[string]interface{}{
			"type":       models.AgentEventAIStatus,
			"session_id": sessionID,
			"status":     "stopped",
		})
		return
	}
	cancel := session.cancel
	runWrite := session.activeWriter
	m.mu.Unlock()
	if runWrite == nil {
		runWrite = writeJSON
	}
	if cancel != nil {
		cancel()
	}
	_ = runWrite(map[string]interface{}{
		"type":       models.AgentEventAIStatus,
		"session_id": sessionID,
		"status":     "stopping",
	})
}

func (m *agentAIManager) approval(msg map[string]interface{}, writeJSON agentTerminalWriter) {
	sessionID := remoteString(msg, "session_id")
	approvalID := remoteString(msg, "approval_id")
	decision := normalizeAgentAIApprovalDecision(remoteString(msg, "decision"))
	deliveryID := strings.TrimSpace(remoteString(msg, "delivery_id"))
	if sessionID == "" || approvalID == "" {
		if writeJSON != nil {
			_ = writeJSON(agentAIErrorPayload(sessionID, remoteString(msg, "message_id"), errors.New("ai.approval.response missing session_id or approval_id")))
		}
		return
	}
	// Always ACK a delivery (even on duplicate / no_match / bad decision) so the
	// server stops retrying it. result tells the server whether the decision
	// actually took effect on a live waiter.
	ack := func(result string) {
		if writeJSON == nil {
			return
		}
		payload := map[string]interface{}{
			"type":        models.AgentEventAIApprovalAck,
			"session_id":  sessionID,
			"approval_id": approvalID,
			"result":      result,
		}
		if deliveryID != "" {
			payload["delivery_id"] = deliveryID
		}
		_ = writeJSON(payload)
	}
	if decision == "" {
		ack("no_match")
		if writeJSON != nil {
			_ = writeJSON(agentAIErrorPayload(sessionID, remoteString(msg, "message_id"), fmt.Errorf("unsupported approval decision: %s", remoteString(msg, "decision"))))
		}
		return
	}

	raw := marshalAgentAIRaw(msg["raw"])
	response := agentAIApprovalResponse{
		Decision: decision,
		Scope:    normalizeAgentAIApprovalScope(remoteString(msg, "scope")),
		Raw:      raw,
	}

	approvalKey := agentAIApprovalMapKey(sessionID, approvalID)
	m.mu.Lock()
	if completed := m.completedApprovals[approvalKey]; completed != nil && completed.sessionID == sessionID {
		m.mu.Unlock()
		ack("duplicate")
		return
	}
	waiter := m.approvals[approvalKey]
	if waiter != nil && waiter.sessionID == sessionID {
		delete(m.approvals, approvalKey)
		m.rememberCompletedApprovalLocked(approvalID, sessionID, waiter.runSeq, response)
	}
	m.mu.Unlock()
	if waiter == nil || waiter.sessionID != sessionID {
		ack("no_match")
		if writeJSON != nil {
			_ = writeJSON(map[string]interface{}{
				"type":       models.AgentEventAIStatus,
				"session_id": sessionID,
				"status":     "approval_not_found",
			})
		}
		return
	}

	waiter.request.respond <- response
	ack("applied")
}

// pendingApprovalsSnapshot returns the approvals this client is still waiting
// on, for ai.approval.sync (reconnect reconcile + liveness heartbeat).
func (m *agentAIManager) pendingApprovalsSnapshot() []map[string]interface{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]map[string]interface{}, 0, len(m.approvals))
	for _, waiter := range m.approvals {
		if waiter == nil {
			continue
		}
		entry := map[string]interface{}{
			"session_id":  waiter.sessionID,
			"approval_id": waiter.request.ID,
			"kind":        waiter.request.Kind,
			"provider":    waiter.request.Provider,
		}
		if waiter.request.Command != "" {
			entry["command"] = waiter.request.Command
		}
		if waiter.request.ToolName != "" {
			entry["tool_name"] = waiter.request.ToolName
		}
		out = append(out, entry)
	}
	return out
}

// emitApprovalSync sends ai.approval.sync for all still-pending approvals (a
// no-op when there are none). Used after reconnect and on a periodic ticker.
func (m *agentAIManager) emitApprovalSync(writeJSON agentTerminalWriter) {
	if writeJSON == nil {
		return
	}
	pending := m.pendingApprovalsSnapshot()
	if len(pending) == 0 {
		return
	}
	_ = writeJSON(map[string]interface{}{
		"type":    models.AgentEventAIApprovalSync,
		"pending": pending,
	})
}

// emitApprovalCancelled notifies the server that a dialogue went inactive and
// its pending approvals were dropped, so the server stops retrying them.
// Best-effort: callers without a writer (closeAll on shutdown) rely on the
// server's offline-grace cancellation instead.
func (m *agentAIManager) emitApprovalCancelled(writeJSON agentTerminalWriter, sessionID string, approvalIDs []string, reason string) {
	if writeJSON == nil || sessionID == "" || len(approvalIDs) == 0 {
		return
	}
	payload := map[string]interface{}{
		"type":         models.AgentEventAIApprovalCancelled,
		"session_id":   sessionID,
		"approval_ids": approvalIDs,
	}
	if reason != "" {
		payload["reason"] = reason
	}
	_ = writeJSON(payload)
}

// emitOptionCancelled 在 session 失活（close 等）时，best-effort 通知服务端作废待选。
// 与 emitApprovalCancelled 同理：closeAll 等无 writer 的场景依赖服务端 offline-grace。
func (m *agentAIManager) emitOptionCancelled(writeJSON agentTerminalWriter, sessionID string, optionIDs []string, reason string) {
	if writeJSON == nil || sessionID == "" || len(optionIDs) == 0 {
		return
	}
	payload := map[string]interface{}{
		"type":       models.AgentEventAIOptionCancelled,
		"session_id": sessionID,
		"option_ids": optionIDs,
	}
	if reason != "" {
		payload["reason"] = reason
	}
	_ = writeJSON(payload)
}

// cancelOptions 处理 server→agent 的 ai.option.cancelled：清匹配的 pendingOption，
// 避免后续把已作废的选项误续接成新 run。
func (m *agentAIManager) cancelOptions(msg map[string]interface{}) {
	sessionID := remoteString(msg, "session_id")
	if sessionID == "" {
		return
	}
	m.mu.Lock()
	if session := m.sessions[sessionID]; session != nil {
		session.pendingOption = nil
	}
	m.mu.Unlock()
}

func (m *agentAIManager) close(msg map[string]interface{}, writeJSON agentTerminalWriter) {
	if writeJSON == nil {
		return
	}
	sessionID := remoteString(msg, "session_id")
	if sessionID == "" {
		_ = writeJSON(agentAIErrorPayload("", "", errors.New("ai.session.close missing session_id")))
		return
	}

	m.mu.Lock()
	session := m.sessions[sessionID]
	var cancelled []string
	var cancelledOptions []string
	if session != nil {
		// Collect pendingOption id BEFORE deleting the session so the server can
		// be told to drop it. Mirrors the approval-cancelled cleanup.
		if session.pendingOption != nil {
			cancelledOptions = []string{session.pendingOption.ID}
			session.pendingOption = nil
		}
		delete(m.sessions, sessionID)
		cancelled = m.clearPendingApprovalsLocked(sessionID, session.runSeq, models.AgentAIApprovalDecisionCancel)
	}
	var cancel context.CancelFunc
	var runWrite agentTerminalWriter
	if session != nil {
		cancel = session.cancel
		runWrite = session.activeWriter
	}
	m.mu.Unlock()
	m.emitApprovalCancelled(writeJSON, sessionID, cancelled, "session_closed")
	m.emitOptionCancelled(writeJSON, sessionID, cancelledOptions, "session_closed")

	if runWrite == nil {
		runWrite = writeJSON
	}
	if err := runWrite(map[string]interface{}{
		"type":       models.AgentEventAISessionClosed,
		"session_id": sessionID,
	}); err != nil {
		logger.Warn(fmt.Sprintf("ai.session.close: terminal delivery deferred session=%s error=%v", sessionID, err))
	}
	if cancel != nil {
		cancel()
	}
}

func (m *agentAIManager) closeAll() {
	m.mu.Lock()
	cancels := make([]context.CancelFunc, 0, len(m.sessions))
	for _, session := range m.sessions {
		if session.cancel != nil {
			cancels = append(cancels, session.cancel)
		}
		// Drop pendingOption on shutdown (no writer -> rely on server offline-grace).
		session.pendingOption = nil
		m.clearPendingApprovalsLocked(session.id, session.runSeq, models.AgentAIApprovalDecisionCancel)
	}
	m.sessions = make(map[string]*agentAISession)
	m.mu.Unlock()

	for _, cancel := range cancels {
		cancel()
	}
}

// emitOptionRequest 把 run 结束时提取到的方案块，发 ai.option.request 并在 session 上记 pending。
// MVP：一次 run 只处理第一个有效块。runSeq 不匹配（已被覆盖）时静默跳过，防止串台。
func (m *agentAIManager) emitOptionRequest(run agentAIRun, writeJSON agentTerminalWriter, blocks []agentAIOptionRequest) {
	if writeJSON == nil || len(blocks) == 0 {
		return
	}
	req := blocks[0]
	if strings.TrimSpace(req.ID) == "" {
		req.ID = newAgentAIApprovalID(run.sessionID, run.runSeq)
	}
	req.SessionID = run.sessionID
	req.MessageID = agentAssistantMessageID(run.messageID)
	req.Provider = firstNonEmpty(req.Provider, run.provider)

	m.mu.Lock()
	session := m.sessions[run.sessionID]
	if session == nil || session.runSeq != run.runSeq {
		m.mu.Unlock()
		return
	}
	session.pendingOption = &req
	m.mu.Unlock()

	_ = writeJSON(map[string]interface{}{
		"type":         models.AgentEventAIOptionRequest,
		"session_id":   run.sessionID,
		"message_id":   req.MessageID,
		"option_id":    req.ID,
		"title":        req.Title,
		"options":      req.Options,
		"allow_custom": req.AllowCustom,
		"multi":        req.Multi,
		"provider":     req.Provider,
	})
}

func (m *agentAIManager) requestApproval(ctx context.Context, run agentAIRun, writeJSON agentTerminalWriter, req agentAIApprovalRequest) (agentAIApprovalResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if writeJSON == nil {
		return agentAIApprovalResponse{}, errors.New("approval writer is unavailable")
	}
	req.SessionID = run.sessionID
	req.MessageID = agentAssistantMessageID(run.messageID)
	req.Provider = firstNonEmpty(req.Provider, run.provider)
	if req.ID == "" {
		req.ID = newAgentAIApprovalID(run.sessionID, run.runSeq)
	}
	if req.Kind == "" {
		req.Kind = models.AgentAIApprovalKindTool
	}
	if len(req.AvailableDecisions) == 0 {
		req.AvailableDecisions = []string{
			models.AgentAIApprovalDecisionAccept,
			models.AgentAIApprovalDecisionDecline,
			models.AgentAIApprovalDecisionCancel,
		}
	}
	req.respond = make(chan agentAIApprovalResponse, 1)

	m.mu.Lock()
	if m.approvals == nil {
		m.approvals = make(map[string]*agentAIApprovalWaiter)
	}
	approvalKey := agentAIApprovalMapKey(run.sessionID, req.ID)
	m.approvals[approvalKey] = &agentAIApprovalWaiter{
		sessionID: run.sessionID,
		runSeq:    run.runSeq,
		request:   req,
	}
	m.mu.Unlock()

	logger.Info(fmt.Sprintf("approval-hook: requestApproval SENDING ai.approval.request to cloud session=%s approval_id=%s kind=%s — awaiting user decision (≤%s)", run.sessionID, req.ID, req.Kind, agentAIApprovalTimeout))
	_ = writeJSON(agentAIApprovalRequestPayload(req))

	run.activity.setAwaitingApproval(true)
	defer run.activity.setAwaitingApproval(false)

	select {
	case response := <-req.respond:
		_ = writeJSON(map[string]interface{}{
			"type":        models.AgentEventAIApprovalRequest,
			"session_id":  run.sessionID,
			"message_id":  agentAssistantMessageID(run.messageID),
			"approval_id": req.ID,
			"provider":    req.Provider,
			"kind":        req.Kind,
			"status":      "resolved",
			"decision":    response.Decision,
		})
		return response, nil
	case <-ctx.Done():
		cancelled := false
		m.mu.Lock()
		if waiter := m.approvals[approvalKey]; waiter != nil && waiter.sessionID == run.sessionID && waiter.runSeq == run.runSeq {
			delete(m.approvals, approvalKey)
			m.rememberCompletedApprovalLocked(req.ID, run.sessionID, run.runSeq, agentAIApprovalResponse{Decision: models.AgentAIApprovalDecisionCancel})
			cancelled = true
		}
		m.mu.Unlock()
		if cancelled {
			reason := "run_cancelled"
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				reason = "approval_timeout"
			}
			m.emitApprovalCancelled(writeJSON, run.sessionID, []string{req.ID}, reason)
		}
		return agentAIApprovalResponse{}, ctx.Err()
	}
}

func (m *agentAIManager) handleClaudeApprovalHook(ctx context.Context, sessionID string, messageID string, token string, raw map[string]interface{}) (map[string]interface{}, error) {
	sessionID = strings.TrimSpace(sessionID)
	messageID = strings.TrimSpace(messageID)
	token = strings.TrimSpace(token)
	hookEventName := claudeApprovalHookEventName(raw)
	logger.Info(fmt.Sprintf("approval-hook: INVOKED by claude session=%s messageID=%s tokenLen=%d tool_name=%s", sessionID, messageID, len(token), firstNonEmpty(remoteString(raw, "tool_name"), remoteString(raw, "toolName"))))
	if sessionID == "" || token == "" {
		logger.Warn(fmt.Sprintf("approval-hook: deny (missing session or token) session=%q tokenLen=%d", sessionID, len(token)))
		return claudeApprovalHookDecision(hookEventName, false, "Aliang approval hook is missing session or token."), errors.New("approval hook missing session or token")
	}

	m.mu.Lock()
	session := m.sessions[sessionID]
	// Diagnose which guard fails (each maps to a distinct fix).
	switch {
	case session == nil:
		logger.Warn(fmt.Sprintf("approval-hook: deny (no live session in map) session=%s — run already ended or never started", sessionID))
	case session.approvalToken == "":
		logger.Warn(fmt.Sprintf("approval-hook: deny (session.approvalToken empty) session=%s — run ended, token cleared", sessionID))
	case session.approvalToken != token:
		logger.Warn(fmt.Sprintf("approval-hook: deny (token mismatch) session=%s", sessionID))
	case session.activeWriter == nil:
		logger.Warn(fmt.Sprintf("approval-hook: deny (activeWriter nil) session=%s", sessionID))
	case session.cancel == nil:
		logger.Warn(fmt.Sprintf("approval-hook: deny (cancel nil) session=%s", sessionID))
	}
	if session == nil || session.approvalToken == "" || session.approvalToken != token || session.activeWriter == nil || session.cancel == nil {
		m.mu.Unlock()
		return claudeApprovalHookDecision(hookEventName, false, "Aliang could not match this permission request to a running AI session."), fmt.Errorf("approval hook session mismatch: %s", sessionID)
	}
	run := agentAIRun{
		sessionID:     session.id,
		messageID:     firstNonEmpty(messageID, session.id),
		runSeq:        session.runSeq,
		mode:          session.mode,
		projectPath:   session.projectPath,
		provider:      session.provider,
		model:         session.model,
		cancel:        session.cancel,
		approvalToken: session.approvalToken,
		activity:      session.activity,
	}
	writeJSON := session.activeWriter
	m.mu.Unlock()

	// Policy short-circuit: evaluate the device approval policy locally before
	// hitting the cloud. Auto-approved/denied tools resolve here with no
	// round-trip; only require_approval falls through to requestApproval.
	toolName := firstNonEmpty(remoteString(raw, "tool_name"), remoteString(raw, "toolName"))
	toolInput := marshalAgentAIRaw(raw["tool_input"])
	if len(toolInput) == 0 {
		toolInput = marshalAgentAIRaw(raw["toolInput"])
	}
	if svc := m.approvalService(); svc != nil {
		switch decision, matchedID := svc.evaluateApprovalDecision(toolName, toolInput, run.projectPath); decision {
		case decisionAutoApprove:
			logger.Info(fmt.Sprintf("approval-hook: AUTO-APPROVE by policy rule=%s tool=%s session=%s (no cloud round-trip)", matchedID, toolName, sessionID))
			return claudeApprovalHookDecision(hookEventName, true, "auto-approved by policy: "+matchedID), nil
		case decisionAutoDeny:
			logger.Info(fmt.Sprintf("approval-hook: AUTO-DENY by policy rule=%s tool=%s session=%s", matchedID, toolName, sessionID))
			return claudeApprovalHookDecision(hookEventName, false, "denied by policy: "+matchedID), nil
		case decisionRequireApproval:
			// Attach policy context so the approver sees why it escalated.
			run.matchedRuleID = matchedID
			run.policyVersion = svc.effectiveApprovalPolicyForPath(run.projectPath).Version
		}
	}

	req := buildClaudeApprovalRequest(run, raw)
	approvalCtx, cancel := context.WithTimeout(ctx, agentAIApprovalTimeout)
	defer cancel()
	response, err := m.requestApproval(approvalCtx, run, writeJSON, req)
	if err != nil {
		return claudeApprovalHookDecision(hookEventName, false, "Aliang approval timed out or was cancelled."), err
	}
	switch response.Decision {
	case models.AgentAIApprovalDecisionAccept, models.AgentAIApprovalDecisionAcceptForSession:
		return claudeApprovalHookDecision(hookEventName, true, "Approved in Aliang."), nil
	default:
		return claudeApprovalHookDecision(hookEventName, false, "Denied in Aliang."), nil
	}
}

func buildClaudeApprovalRequest(run agentAIRun, raw map[string]interface{}) agentAIApprovalRequest {
	rawJSON := marshalAgentAIRaw(raw)
	toolName := firstNonEmpty(remoteString(raw, "tool_name"), remoteString(raw, "toolName"))
	toolInput := marshalAgentAIRaw(raw["tool_input"])
	if len(toolInput) == 0 {
		toolInput = marshalAgentAIRaw(raw["toolInput"])
	}
	command := claudeApprovalCommand(raw)
	kind := models.AgentAIApprovalKindTool
	if strings.EqualFold(toolName, "bash") || command != "" {
		kind = models.AgentAIApprovalKindCommand
	}
	reason := firstNonEmpty(
		remoteString(raw, "permission_prompt"),
		remoteString(raw, "permissionPrompt"),
		remoteString(raw, "reason"),
	)
	title := "Approve Claude Code tool use"
	if kind == models.AgentAIApprovalKindCommand {
		title = "Approve Claude Code command"
	}
	return agentAIApprovalRequest{
		ID:        newAgentAIApprovalID(run.sessionID, run.runSeq),
		Provider:  run.provider,
		Kind:      kind,
		Title:     title,
		Reason:    reason,
		Command:   command,
		CWD:       firstNonEmpty(remoteString(raw, "cwd"), run.projectPath),
		ToolName:  toolName,
		ToolInput: toolInput,
		Raw:       rawJSON,
		AvailableDecisions: []string{
			models.AgentAIApprovalDecisionAccept,
			models.AgentAIApprovalDecisionDecline,
			models.AgentAIApprovalDecisionCancel,
		},
		MatchedRuleID: run.matchedRuleID,
		PolicyVersion: run.policyVersion,
	}
}

func claudeApprovalCommand(raw map[string]interface{}) string {
	for _, key := range []string{"command", "cmd"} {
		if value := strings.TrimSpace(remoteString(raw, key)); value != "" {
			return value
		}
	}
	for _, key := range []string{"tool_input", "toolInput", "input"} {
		row, ok := raw[key].(map[string]interface{})
		if !ok {
			continue
		}
		if value := firstNonEmpty(remoteString(row, "command"), remoteString(row, "cmd")); value != "" {
			return value
		}
	}
	return ""
}

func claudeApprovalHookEventName(raw map[string]interface{}) string {
	if raw == nil {
		return "PermissionRequest"
	}
	switch firstNonEmpty(
		remoteString(raw, "hook_event_name"),
		remoteString(raw, "hookEventName"),
		remoteString(raw, "hook_event"),
		remoteString(raw, "hookEvent"),
		remoteString(raw, "event"),
	) {
	case "PreToolUse":
		return "PreToolUse"
	case "PermissionRequest":
		return "PermissionRequest"
	default:
		return "PermissionRequest"
	}
}

func claudeApprovalHookDecision(hookEventName string, allow bool, reason string) map[string]interface{} {
	decision := "deny"
	if allow {
		decision = "allow"
	}
	if hookEventName == "PreToolUse" {
		return map[string]interface{}{
			"hookSpecificOutput": map[string]interface{}{
				"hookEventName":             "PreToolUse",
				"permissionDecision":        decision,
				"permissionDecisionReason":  reason,
				"suppressOutputForApprover": false,
			},
		}
	}
	return map[string]interface{}{
		"hookSpecificOutput": map[string]interface{}{
			"hookEventName": "PermissionRequest",
			"decision": map[string]interface{}{
				"behavior": decision,
				"message":  reason,
			},
		},
	}
}

func (m *agentAIManager) clearPendingApprovalsLocked(sessionID string, runSeq int, decision string) []string {
	var cancelled []string
	for approvalKey, waiter := range m.approvals {
		if waiter == nil || waiter.sessionID != sessionID || waiter.runSeq != runSeq {
			continue
		}
		delete(m.approvals, approvalKey)
		response := agentAIApprovalResponse{Decision: decision}
		m.rememberCompletedApprovalLocked(waiter.request.ID, sessionID, runSeq, response)
		waiter.request.respond <- response
		cancelled = append(cancelled, waiter.request.ID)
	}
	return cancelled
}

// cancelApprovals drops the waiters for the given approval ids (server told us
// they were cancelled, e.g. the device was offline past grace). Each cancelled
// waiter resolves with a "cancel" decision so the running CLI denies the tool.
func (m *agentAIManager) cancelApprovals(msg map[string]interface{}) {
	rawIDs, _ := msg["approval_ids"].([]interface{})
	if len(rawIDs) == 0 {
		return
	}
	want := make(map[string]bool, len(rawIDs))
	for _, raw := range rawIDs {
		if id, ok := raw.(string); ok && id != "" {
			want[id] = true
		}
	}
	if len(want) == 0 {
		return
	}
	m.mu.Lock()
	for approvalKey, waiter := range m.approvals {
		if waiter == nil || !want[waiter.request.ID] {
			continue
		}
		delete(m.approvals, approvalKey)
		response := agentAIApprovalResponse{Decision: models.AgentAIApprovalDecisionCancel}
		m.rememberCompletedApprovalLocked(waiter.request.ID, waiter.sessionID, waiter.runSeq, response)
		waiter.request.respond <- response
	}
	m.mu.Unlock()
}

func (m *agentAIManager) rememberCompletedApprovalLocked(approvalID string, sessionID string, runSeq int, response agentAIApprovalResponse) {
	if approvalID == "" || sessionID == "" {
		return
	}
	if m.completedApprovals == nil {
		m.completedApprovals = make(map[string]*agentAICompletedApproval)
	}
	if len(m.completedApprovals) > 1024 {
		m.completedApprovals = make(map[string]*agentAICompletedApproval)
	}
	m.completedApprovals[agentAIApprovalMapKey(sessionID, approvalID)] = &agentAICompletedApproval{
		sessionID: sessionID,
		runSeq:    runSeq,
		response:  response,
		createdAt: time.Now().UTC(),
	}
}

func agentAIApprovalMapKey(sessionID string, approvalID string) string {
	return sessionID + "\x00" + approvalID
}

func (m *agentAIManager) clearRunning(sessionID string, runSeq int, writeJSON agentTerminalWriter) {
	m.mu.Lock()
	session := m.sessions[sessionID]
	var steerControl *agentAICodexSteerControl
	if session != nil && session.runSeq == runSeq {
		steerControl = session.codexSteer
		session.cancel = nil
		session.activeWriter = nil
		session.approvalToken = ""
		session.activity = nil
		session.codexSteer = nil
		session.history = trimAgentAIHistory(session.history)
	}
	cancelled := m.clearPendingApprovalsLocked(sessionID, runSeq, models.AgentAIApprovalDecisionCancel)
	m.mu.Unlock()
	if steerControl != nil {
		steerControl.close("not_running", "codex turn ended before steer was applied", "turn_ended")
	}
	m.emitApprovalCancelled(writeJSON, sessionID, cancelled, "run_ended")
}

func (m *agentAIManager) appendAssistantHistory(sessionID string, runSeq int, messageID string, output string) {
	output = strings.TrimSpace(output)
	if output == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	session := m.sessions[sessionID]
	if session == nil || session.runSeq != runSeq {
		return
	}
	session.history = append(session.history, agentAIMessage{
		Role:      "assistant",
		MessageID: agentAssistantMessageID(messageID),
		Content:   output,
		CreatedAt: time.Now().UTC(),
	})
	session.history = trimAgentAIHistory(session.history)
}

// setAgentAIResumeSessionIDIfEmpty pins a CLI-assigned session id so the NEXT
// turn resumes it instead of starting a fresh process-local conversation. No-op
// when no id was captured, the session is gone, the run is stale, or the session
// already has a resume id (imported sessions carry one, or it was captured on an
// earlier turn). Codex app-server threads are persisted by Codex and use this too,
// even though Aliang starts a fresh app-server transport process for each turn.
func (m *agentAIManager) setAgentAIResumeSessionIDIfEmpty(sessionID string, runSeq int, resumeSessionID string) {
	resumeSessionID = strings.TrimSpace(resumeSessionID)
	if resumeSessionID == "" {
		logger.Info(fmt.Sprintf("resume-set: skip, no captured id (session %s runSeq %d)", sessionID, runSeq))
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	session := m.sessions[sessionID]
	if session == nil || session.runSeq != runSeq {
		logger.Info(fmt.Sprintf("resume-set: skip, session gone/stale (session %s runSeq %d)", sessionID, runSeq))
		return
	}
	if session.resumeSessionID == "" {
		session.resumeSessionID = resumeSessionID
		logger.Info(fmt.Sprintf("resume-set: PINNED CLI resume id %s on session %s", resumeSessionID, sessionID))
	} else {
		logger.Info(fmt.Sprintf("resume-set: already set %s (session %s)", session.resumeSessionID, sessionID))
	}
}

func (m *agentAIManager) clearAgentAIResumeSessionID(sessionID string, runSeq int, expected string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	session := m.sessions[sessionID]
	if session == nil || session.runSeq != runSeq {
		return
	}
	if expected == "" || session.resumeSessionID == expected {
		session.resumeSessionID = ""
	}
}

// agentAIRunOutcome reports how a single CLI pass finished.
const (
	agentAIRunDone agentAIRunOutcome = iota
	// agentAIRunResumeMissing means the CLI could not find the requested
	// --resume session locally (it is absent, or filed under a different
	// project path than the run's cwd). The pass emitted no assistant output;
	// the caller should retry the run fresh, without --resume.
	agentAIRunResumeMissing
)

type agentAIRunOutcome int

func (m *agentAIManager) runCLI(ctx context.Context, run agentAIRun, writeJSON agentTerminalWriter) {
	defer m.clearRunning(run.sessionID, run.runSeq, writeJSON)
	if run.cancel != nil {
		defer run.cancel()
	}

	if agentAIUseCodexAppServer(run.provider) {
		outcome := m.runCodexAppServer(ctx, run, writeJSON)
		if outcome == agentAIRunResumeMissing && ctx.Err() == nil {
			m.clearAgentAIResumeSessionID(run.sessionID, run.runSeq, run.resumeSessionID)
			run.resumeSessionID = ""
			run.prompt = firstNonEmpty(run.freshPrompt, run.prompt)
			_ = m.runCodexAppServer(ctx, run, writeJSON)
		}
		return
	}

	// Try to resume the prior CLI session when one is referenced. If that
	// session does not exist locally (an imported/foreign session, or one filed
	// under a different project path than this run's cwd), retry fresh so the
	// conversation still streams instead of surfacing a hard error.
	if strings.TrimSpace(run.resumeSessionID) != "" {
		if m.runCLIPass(ctx, run, writeJSON, true) != agentAIRunResumeMissing {
			return
		}
	}
	_ = m.runCLIPass(ctx, run, writeJSON, false)
}

func agentAIUseCodexAppServer(provider string) bool {
	provider = strings.ToLower(strings.TrimSpace(provider))
	return (provider == "codex" || provider == "auto") && codexAppServerAvailable()
}

var codexAppServerProbeCache sync.Map

func executableProbeCacheKey(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	key := path
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		key += ":" + resolved
	}
	if stat, err := os.Stat(path); err == nil {
		key += fmt.Sprintf(":%d:%d", stat.ModTime().UnixNano(), stat.Size())
	}
	return key
}

func codexAppServerAvailable() bool {
	path, err := lookPathCLI("codex")
	if err != nil {
		return false
	}
	cacheKey := executableProbeCacheKey(path)
	if cached, ok := codexAppServerProbeCache.Load(cacheKey); ok {
		return cached.(bool)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	output, probeErr := newBackgroundCommandContext(ctx, path, "app-server", "--help").CombinedOutput()
	available := probeErr == nil && strings.Contains(strings.ToLower(string(output)), "app-server")
	codexAppServerProbeCache.Store(cacheKey, available)
	return available
}

// codexTurnResult inspects a codex `turn/completed` notification params payload
// and reports the terminal status the agent should surface, plus any error
// message carried by a failed turn. Status is "completed", "interrupted",
// "failed", or "" when the payload is missing/unknown. Per the app-server
// protocol, turn/completed carries { turn: { status, error?: { message,
// codexErrorInfo?, additionalDetails? } } }; a failed or interrupted turn must
// NOT be treated as a successful completion, so the caller gates ai.done on it.
func codexTurnResult(params map[string]interface{}) (status string, errMsg string) {
	if params == nil {
		return "", ""
	}
	turn, ok := params["turn"].(map[string]interface{})
	if !ok {
		return "", ""
	}
	status = strings.ToLower(strings.TrimSpace(remoteString(turn, "status")))
	if errObj, ok := turn["error"].(map[string]interface{}); ok {
		errMsg = strings.TrimSpace(firstNonEmpty(remoteString(errObj, "message"), remoteString(errObj, "additionalDetails")))
	}
	return status, errMsg
}

// extractCodexFileChangeItem reads an item/started or item/completed
// notification and, when the carried item is a fileChange, returns its itemId
// and the marshalled `changes` array. Returns ("", nil) for non-fileChange
// items. The fileChange approval request itself carries no diff (per the
// app-server protocol); the proposed edits arrive in this prior item event, so
// the caller keys them by itemId and attaches them to the approval request.
func extractCodexFileChangeItem(msg map[string]interface{}) (string, json.RawMessage) {
	params, _ := msg["params"].(map[string]interface{})
	item, _ := params["item"].(map[string]interface{})
	if remoteString(item, "type") != "fileChange" {
		return "", nil
	}
	itemID := remoteString(item, "id")
	if itemID == "" {
		return "", nil
	}
	if changes, ok := item["changes"]; ok && changes != nil {
		return itemID, marshalAgentAIRaw(changes)
	}
	return itemID, nil
}

// summarizeFileDiff counts added/removed lines in a unified diff for the
// activity feed ("Update(file) +N -M"). File headers (+++/---), hunk headers
// (@@) and the "\ No newline at end of file" marker are not counted.
func summarizeFileDiff(diff string) (added, removed int) {
	if diff == "" {
		return 0, 0
	}
	for _, line := range strings.Split(diff, "\n") {
		if line == "" {
			continue
		}
		switch line[0] {
		case '@', '\\', ' ':
			continue
		case '+':
			if !strings.HasPrefix(line, "+++") {
				added++
			}
		case '-':
			if !strings.HasPrefix(line, "---") {
				removed++
			}
		}
	}
	return added, removed
}

func codexAppServerResponseID(msg map[string]interface{}) string {
	if _, ok := msg["id"]; !ok {
		return ""
	}
	return fmt.Sprint(msg["id"])
}

func codexAppServerResponseError(msg map[string]interface{}) (string, bool) {
	errObj, ok := msg["error"].(map[string]interface{})
	if !ok || errObj == nil {
		return "", false
	}
	message := firstNonEmpty(remoteString(errObj, "message"), remoteString(errObj, "error"))
	if info, ok := errObj["codexErrorInfo"].(map[string]interface{}); ok {
		message = firstNonEmpty(message, remoteString(info, "message"), remoteString(info, "code"))
	}
	code := remoteString(errObj, "code")
	if message == "" {
		message = "unknown error"
	}
	if code != "" {
		return fmt.Sprintf("%s (%s)", message, code), true
	}
	return message, true
}

// extractCodexCommandItem pulls the lifecycle fields of a codex
// commandExecution item (item/started carries command+cwd; item/completed
// carries exitCode+stdout+stderr). ok is true only for commandExecution items;
// exitCode is non-nil only when the completed item carried one.
func extractCodexCommandItem(msg map[string]interface{}) (itemID, command, cwd string, exitCode *int, stdout, stderr string, ok bool) {
	params, _ := msg["params"].(map[string]interface{})
	item, _ := params["item"].(map[string]interface{})
	if remoteString(item, "type") != "commandExecution" {
		return "", "", "", nil, "", "", false
	}
	itemID = remoteString(item, "id")
	command = codexJoinCommand(item["command"])
	cwd = remoteString(item, "cwd")
	exitCode = remoteIntPointer(item, "exitCode", "exit_code")
	stdout = remoteString(item, "stdout")
	stderr = remoteString(item, "stderr")
	return itemID, command, cwd, exitCode, stdout, stderr, true
}

// codexJoinCommand renders a codex command value — either a shell string or an
// argv array — as a single display string.
func codexJoinCommand(raw interface{}) string {
	switch v := raw.(type) {
	case string:
		return strings.TrimSpace(v)
	case []interface{}:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if part := strings.TrimSpace(fmt.Sprint(item)); part != "" {
				out = append(out, part)
			}
		}
		return strings.Join(out, " ")
	}
	return ""
}

// remoteIntPointer returns the first present integer-valued field among keys,
// or nil when none of them hold a number. JSON numbers arrive as float64.
func remoteIntPointer(fields map[string]interface{}, keys ...string) *int {
	for _, key := range keys {
		raw, present := fields[key]
		if !present || raw == nil {
			continue
		}
		if v, ok := toIntValue(raw); ok {
			return &v
		}
	}
	return nil
}

func toIntValue(raw interface{}) (int, bool) {
	switch v := raw.(type) {
	case float64:
		return int(v), true
	case int:
		return v, true
	case int64:
		return int(v), true
	}
	return 0, false
}

// agentAICloudFieldCap bounds a single structured-event text field (command
// output, diff) so one verbose command/edit cannot blow up a websocket frame.
const agentAICloudFieldCap = 8 * 1024

func truncateForCloud(value string) string {
	if len(value) <= agentAICloudFieldCap {
		return value
	}
	return value[:agentAICloudFieldCap] + "…<truncated>"
}

// emitCodexFileChangeEvents fans a codex fileChange item's changes out as one
// ai.file_change event per changed path, each carrying ±line counts (from the
// diff) and the raw diff for the cloud to render.
func emitCodexFileChangeEvents(run agentAIRun, writeJSON agentTerminalWriter, itemID string, changes json.RawMessage) {
	var parsed []map[string]interface{}
	if err := json.Unmarshal(changes, &parsed); err != nil || len(parsed) == 0 {
		return
	}
	for _, ch := range parsed {
		diff := firstNonEmpty(remoteString(ch, "diff"), remoteString(ch, "change"))
		added, removed := summarizeFileDiff(diff)
		payload := map[string]interface{}{
			"type":       models.AgentEventAIFileChange,
			"session_id": run.sessionID,
			"message_id": agentAssistantMessageID(run.messageID),
			"item_id":    itemID,
			"path":       remoteString(ch, "path"),
			"kind":       remoteString(ch, "kind"),
			"added":      added,
			"removed":    removed,
		}
		if diff != "" {
			payload["diff"] = truncateForCloud(diff)
		}
		_ = writeJSON(payload)
	}
}

// emitCodexUsageIfPresent is a best-effort hook: codex surfaces per-turn token
// usage on turn/completed, but the exact field shape varies across codex
// versions. It probes the most likely locations and emits a single ai.usage
// event when one is found; otherwise it is a no-op. Verify/adjust the shape
// against a real codex transcript at runtime.
func emitCodexUsageIfPresent(run agentAIRun, writeJSON agentTerminalWriter, params map[string]interface{}) {
	if params == nil {
		return
	}
	sources := []map[string]interface{}{
		mapIf(params["usage"]),
		mapIf(params["turn"]),
		mapIf(params["tokenUsage"]),
	}
	for _, src := range sources {
		if src == nil {
			continue
		}
		usage := src
		if nested := mapIf(src["usage"]); nested != nil {
			usage = nested
		}
		payload := map[string]interface{}{
			"type":       models.AgentEventAIUsage,
			"session_id": run.sessionID,
			"message_id": agentAssistantMessageID(run.messageID),
		}
		hit := false
		for _, key := range []string{"input_tokens", "inputTokens"} {
			if v := remoteIntValue(usage[key]); v >= 0 {
				payload["input_tokens"] = v
				hit = true
				break
			}
		}
		for _, key := range []string{"output_tokens", "outputTokens"} {
			if v := remoteIntValue(usage[key]); v >= 0 {
				payload["output_tokens"] = v
				hit = true
				break
			}
		}
		for _, key := range []string{"cache_read_tokens", "cacheReadInputTokens"} {
			if v := remoteIntValue(usage[key]); v >= 0 {
				payload["cache_read_tokens"] = v
				hit = true
				break
			}
		}
		if hit {
			_ = writeJSON(payload)
			return
		}
	}
}

func emitCodexThreadUsage(run agentAIRun, writeJSON agentTerminalWriter, params map[string]interface{}) {
	tokenUsage := mapIf(params["tokenUsage"])
	if tokenUsage == nil {
		return
	}
	usage := mapIf(tokenUsage["last"])
	if usage == nil {
		usage = mapIf(tokenUsage["total"])
	}
	if usage == nil {
		return
	}
	payload := map[string]interface{}{
		"type":       models.AgentEventAIUsage,
		"session_id": run.sessionID,
		"message_id": agentAssistantMessageID(run.messageID),
	}
	hit := false
	for source, target := range map[string]string{
		"inputTokens":           "input_tokens",
		"outputTokens":          "output_tokens",
		"cachedInputTokens":     "cache_read_tokens",
		"reasoningOutputTokens": "reasoning_output_tokens",
		"totalTokens":           "total_tokens",
	} {
		if value := remoteIntValue(usage[source]); value >= 0 {
			payload[target] = value
			hit = true
		}
	}
	if contextWindow := remoteIntValue(tokenUsage["modelContextWindow"]); contextWindow >= 0 {
		payload["model_context_window"] = contextWindow
		hit = true
	}
	if hit {
		_ = writeJSON(payload)
	}
}

func emitCodexPlan(run agentAIRun, writeJSON agentTerminalWriter, params map[string]interface{}) {
	rawPlan, _ := params["plan"].([]interface{})
	if len(rawPlan) == 0 {
		return
	}
	tasks := make([]map[string]interface{}, 0, len(rawPlan))
	for _, rawStep := range rawPlan {
		step := mapIf(rawStep)
		if step == nil {
			continue
		}
		subject := strings.TrimSpace(remoteString(step, "step"))
		if subject == "" {
			continue
		}
		status := remoteString(step, "status")
		if status == "inProgress" {
			status = "in_progress"
		}
		tasks = append(tasks, map[string]interface{}{"subject": subject, "status": status})
	}
	if len(tasks) == 0 {
		return
	}
	payload := map[string]interface{}{
		"type":       models.AgentEventAITask,
		"session_id": run.sessionID,
		"message_id": agentAssistantMessageID(run.messageID),
		"tasks":      tasks,
	}
	if explanation := strings.TrimSpace(remoteString(params, "explanation")); explanation != "" {
		payload["explanation"] = explanation
	}
	_ = writeJSON(payload)
}

func mapIf(raw interface{}) map[string]interface{} {
	if m, ok := raw.(map[string]interface{}); ok {
		return m
	}
	return nil
}

func remoteIntValue(raw interface{}) int {
	if v, ok := toIntValue(raw); ok {
		return v
	}
	return -1
}

// emitCodexThinkingIfPresent is a best-effort hook for codex reasoning: when an
// item carries reasoning text (type reasoning/agentReasoning/reasoningSummary
// with a text/summary field), it is streamed as ai.thinking. Codex's reasoning
// event shape is version-dependent; verify against a real transcript.
func emitCodexThinkingIfPresent(run agentAIRun, writeJSON agentTerminalWriter, item map[string]interface{}) {
	if item == nil {
		return
	}
	switch remoteString(item, "type") {
	case "reasoning", "agentReasoning", "reasoningSummary":
	default:
		return
	}
	text := firstNonEmpty(remoteString(item, "text"), remoteString(item, "summary"), remoteString(item, "delta"))
	if strings.TrimSpace(text) == "" {
		return
	}
	_ = writeJSON(map[string]interface{}{
		"type":       models.AgentEventAIThinking,
		"session_id": run.sessionID,
		"message_id": agentAssistantMessageID(run.messageID),
		"delta":      text,
	})
}

// codexAgentMessageDedup suppresses exact replays of the previous completed
// agent message within a single codex turn. The upstream model/proxy has been
// observed re-streaming an identical generation (same content, sometimes the
// same message id); without dedup the backend renders the reply twice. The
// first message of a turn streams through verbatim. For each subsequent
// message, deltas that reproduce the previous message's text are held back;
// once the new message completes that exact replay (dropped) or diverges / adds
// a tail, only the genuinely new portion is emitted, so no real content is
// lost. It assumes codex replays under a fresh item id (the protocol's item
// lifecycle is item/started → deltas → item/completed); when item ids are
// absent it degrades to passthrough rather than risk dropping content.
type codexAgentMessageDedup struct {
	lastFull string // raw text of the previous completed message
	curID    string // item id of the message currently streaming
	buf      string // accumulated raw text of the current message
	emitted  string // text emitted for the current message so far
	live     bool   // once true, deltas pass through verbatim
}

func newCodexAgentMessageDedup() *codexAgentMessageDedup {
	return &codexAgentMessageDedup{}
}

func (d *codexAgentMessageDedup) start(itemID string) {
	d.lastFull = d.buf
	d.curID = itemID
	d.buf = ""
	d.emitted = ""
	d.live = false
}

func (d *codexAgentMessageDedup) process(itemID, delta string) string {
	if itemID != d.curID {
		d.start(itemID)
	}
	d.buf += delta
	if d.live || d.lastFull == "" {
		// First message of the turn, or already past the replay window: stream
		// verbatim.
		d.emitted += delta
		return delta
	}
	var want string
	switch {
	case isStringPrefix(d.buf, d.lastFull):
		// Still reproducing the previous message; hold everything back.
		want = ""
	case isStringPrefix(d.lastFull, d.buf):
		// Replay plus a new tail: emit only the tail. Everything after this
		// lands beyond lastFull, so go live.
		d.live = true
		want = d.buf[len(d.lastFull):]
	default:
		// Diverged from the previous message: genuinely new content. Flush the
		// whole message and stream the rest live.
		d.live = true
		want = d.buf
	}
	if want == d.emitted {
		return ""
	}
	out := want[len(d.emitted):]
	d.emitted = want
	return out
}

// isStringPrefix reports whether s is a prefix of full (s == full[:len(s)]),
// including the case s == full.
func isStringPrefix(s, full string) bool {
	return len(s) <= len(full) && full[:len(s)] == s
}

func (m *agentAIManager) runCodexAppServer(ctx context.Context, run agentAIRun, writeJSON agentTerminalWriter) agentAIRunOutcome {
	path, err := lookPathCLI("codex")
	if err != nil {
		_ = writeJSON(agentAIErrorPayload(run.sessionID, run.messageID, fmt.Errorf("AI CLI %q was not found in PATH", "codex")))
		return agentAIRunDone
	}
	cmd := newBackgroundCommandContext(ctx, path, "app-server", "--stdio")
	cmd.Dir = run.projectPath
	cmd.Env = agentChildProcessEnv()
	stdin, err := cmd.StdinPipe()
	if err != nil {
		_ = writeJSON(agentAIErrorPayload(run.sessionID, run.messageID, err))
		return agentAIRunDone
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = writeJSON(agentAIErrorPayload(run.sessionID, run.messageID, err))
		return agentAIRunDone
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = writeJSON(agentAIErrorPayload(run.sessionID, run.messageID, err))
		return agentAIRunDone
	}
	if err := cmd.Start(); err != nil {
		_ = writeJSON(agentAIErrorPayload(run.sessionID, run.messageID, err))
		return agentAIRunDone
	}

	var stderrBuf strings.Builder
	var stderrWG sync.WaitGroup
	stderrWG.Add(1)
	go func() {
		defer stderrWG.Done()
		captureAgentAIStderr(stderr, &stderrBuf)
	}()

	var writeMu sync.Mutex
	send := func(payload map[string]interface{}) error {
		raw, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		writeMu.Lock()
		defer writeMu.Unlock()
		if _, err := stdin.Write(append(raw, '\n')); err != nil {
			return err
		}
		return nil
	}
	steerControl := newAgentAICodexSteerControl(run, send, writeJSON)
	m.setCodexSteerControl(run.sessionID, run.runSeq, steerControl)

	model := normalizeAgentAIModel(run.model)
	effort := strings.TrimSpace(run.effort)
	codexPhase := "initializing"
	terminalErr := ""
	runStartedEmitted := false
	emitRunStarted := func() {
		if runStartedEmitted {
			return
		}
		runStartedEmitted = true
		_ = writeJSON(map[string]interface{}{
			"type":         models.AgentEventAIRunStarted,
			"session_id":   run.sessionID,
			"message_id":   agentAssistantMessageID(run.messageID),
			"provider":     "codex",
			"mode":         run.mode,
			"project_path": run.projectPath,
			"state":        "running",
		})
	}

	_ = send(map[string]interface{}{
		"method": "initialize",
		"id":     0,
		"params": map[string]interface{}{
			"clientInfo": map[string]interface{}{
				"name":    "alianggate",
				"title":   "Aliang Agent",
				"version": "0.1.0",
			},
			"capabilities": nil,
		},
	})
	_ = send(map[string]interface{}{"method": "initialized", "params": map[string]interface{}{}})
	threadParams := map[string]interface{}{
		"cwd":               run.projectPath,
		"approvalPolicy":    "on-request",
		"approvalsReviewer": "user",
		"sandbox":           "workspace-write",
	}
	threadMethod := "thread/start"
	if strings.TrimSpace(run.resumeSessionID) != "" {
		threadMethod = "thread/resume"
		threadParams["threadId"] = strings.TrimSpace(run.resumeSessionID)
	}
	if model != "" {
		threadParams["model"] = model
	}
	codexPhase = threadMethod
	_ = send(map[string]interface{}{"method": threadMethod, "id": 1, "params": threadParams})

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	limiter := &agentAIOutputLimiter{meter: newOutputMeter(agentAIOutputRateWindow, agentAIOutputRateBytes, int64(agentAIOutputCapBytes))}
	var outMu sync.Mutex
	var output strings.Builder
	capture := func(text string) {
		outMu.Lock()
		appendAgentAIHistoryCapture(&output, text)
		outMu.Unlock()
	}
	threadID := ""
	turnID := ""
	turnStarted := false
	completed := false
	resumeMissing := false
	turnStatus := ""
	fileChangesByID := map[string]json.RawMessage{}
	commandsByID := map[string]string{}
	dedup := newCodexAgentMessageDedup()
	// Progress heartbeat. Within a codex turn the app-server emits nothing
	// during quiet gaps (long tool runs, model thinking, upstream retries), and
	// unlike the Claude path there is no per-token stream — so without a
	// heartbeat the phone receives no "still alive" signal and can demote a
	// still-running turn mid-run. Tick ai.run.progress on the same interval as
	// runCLIPass so the run stays running across silent gaps. progressStop is
	// closed when runCodexAppServer returns (the scan loop below has exited).
	progressStop := make(chan struct{})
	defer close(progressStop)
	go func() {
		ticker := time.NewTicker(agentAIRunProgressInterval)
		defer ticker.Stop()
		for {
			select {
			case <-progressStop:
				return
			case <-ticker.C:
				_ = writeJSON(map[string]interface{}{
					"type":              models.AgentEventAIRunProgress,
					"session_id":        run.sessionID,
					"message_id":        agentAssistantMessageID(run.messageID),
					"git_changed_count": countGitChanged(run.projectPath),
				})
			}
		}
	}()
	for scanner.Scan() {
		run.activity.bump()
		var msg map[string]interface{}
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			continue
		}
		if steerControl.handleResponse(msg) {
			continue
		}
		if responseID := codexAppServerResponseID(msg); responseID != "" {
			if detail, ok := codexAppServerResponseError(msg); ok {
				switch responseID {
				case "0":
					terminalErr = "Codex app-server initialize failed: " + detail
					goto done
				case "1":
					if threadMethod == "thread/resume" && isAgentAIResumeMissing(detail) {
						resumeMissing = true
						goto done
					}
					terminalErr = fmt.Sprintf("Codex app-server %s failed: %s", threadMethod, detail)
					goto done
				case "2":
					terminalErr = "Codex app-server turn/start failed: " + detail
					goto done
				}
			}
		}
		if method := remoteString(msg, "method"); method != "" {
			if _, hasID := msg["id"]; hasID {
				id := msg["id"]
				params, _ := msg["params"].(map[string]interface{})
				var fileChanges json.RawMessage
				if method == "item/fileChange/requestApproval" {
					fileChanges = fileChangesByID[remoteString(params, "itemId")]
				}
				result, errorCode, err := m.handleCodexAppServerRequest(ctx, run, writeJSON, method, params, fileChanges)
				if err != nil {
					if errorCode == 0 {
						errorCode = -32000
					}
					_ = send(map[string]interface{}{
						"id":    id,
						"error": map[string]interface{}{"code": errorCode, "message": err.Error()},
					})
				} else {
					_ = send(map[string]interface{}{"id": id, "result": result})
				}
				continue
			}
			switch method {
			case "turn/started":
				if params, ok := msg["params"].(map[string]interface{}); ok {
					if value := remoteString(params, "threadId"); value != "" {
						threadID = value
					}
					if turn, ok := params["turn"].(map[string]interface{}); ok {
						turnID = remoteString(turn, "id")
					}
					if turnID == "" {
						turnID = remoteString(params, "turnId")
					}
					if turnID != "" {
						codexPhase = "running"
						steerControl.markReady(threadID, turnID)
					}
				}
			case "item/agentMessage/delta":
				if params, ok := msg["params"].(map[string]interface{}); ok {
					out := dedup.process(remoteString(params, "itemId"), remoteString(params, "delta"))
					// Suppressed replays are neither emitted nor captured, so they
					// never reach the backend or the stored assistant history.
					if out != "" && !emitAIDelta(out, run, "assistant", writeJSON, limiter, capture) {
						if run.cancel != nil {
							run.cancel()
						}
					}
				}
			case "item/reasoning/textDelta", "item/reasoning/summaryTextDelta":
				if params, ok := msg["params"].(map[string]interface{}); ok {
					if delta := remoteString(params, "delta"); strings.TrimSpace(delta) != "" {
						_ = writeJSON(map[string]interface{}{
							"type":       models.AgentEventAIThinking,
							"session_id": run.sessionID,
							"message_id": agentAssistantMessageID(run.messageID),
							"item_id":    remoteString(params, "itemId"),
							"delta":      delta,
						})
					}
				}
			case "thread/tokenUsage/updated":
				if params, ok := msg["params"].(map[string]interface{}); ok {
					emitCodexThreadUsage(run, writeJSON, params)
				}
			case "turn/plan/updated":
				if params, ok := msg["params"].(map[string]interface{}); ok {
					emitCodexPlan(run, writeJSON, params)
				}
			case "item/plan/delta":
				if params, ok := msg["params"].(map[string]interface{}); ok {
					if delta := strings.TrimSpace(remoteString(params, "delta")); delta != "" {
						_ = writeJSON(map[string]interface{}{
							"type":       models.AgentEventAITask,
							"session_id": run.sessionID,
							"message_id": agentAssistantMessageID(run.messageID),
							"item_id":    remoteString(params, "itemId"),
							"tasks": []map[string]interface{}{
								{"subject": delta, "status": "in_progress"},
							},
						})
					}
				}
			case "item/commandExecution/outputDelta":
				if params, ok := msg["params"].(map[string]interface{}); ok {
					itemID := remoteString(params, "itemId")
					_ = writeJSON(map[string]interface{}{
						"type":         models.AgentEventAICommand,
						"session_id":   run.sessionID,
						"message_id":   agentAssistantMessageID(run.messageID),
						"item_id":      itemID,
						"status":       "running",
						"command":      commandsByID[itemID],
						"output_delta": truncateForCloud(remoteString(params, "delta")),
					})
				}
			case "turn/diff/updated":
				if params, ok := msg["params"].(map[string]interface{}); ok {
					if diff := remoteString(params, "diff"); diff != "" {
						_ = writeJSON(map[string]interface{}{
							"type":       models.AgentEventAIFileChange,
							"session_id": run.sessionID,
							"message_id": agentAssistantMessageID(run.messageID),
							"item_id":    "turn_diff",
							"kind":       "diff",
							"diff":       truncateForCloud(diff),
						})
					}
				}
			case "item/started", "item/completed":
				isCompleted := method == "item/completed"
				// commandExecution lifecycle → ai.command (started: command+cwd;
				// completed: exit_code + truncated output). The completed item
				// often omits the command, so remember it from started by item id.
				if cmdID, cmd, cmdCwd, exitCode, stdout, stderr, ok := extractCodexCommandItem(msg); ok && cmdID != "" {
					if cmd != "" {
						commandsByID[cmdID] = cmd
					}
					command := cmd
					if command == "" {
						command = commandsByID[cmdID]
					}
					payload := map[string]interface{}{
						"type":       models.AgentEventAICommand,
						"session_id": run.sessionID,
						"message_id": agentAssistantMessageID(run.messageID),
						"item_id":    cmdID,
						"status":     "started",
						"command":    command,
					}
					if cmdCwd != "" {
						payload["cwd"] = cmdCwd
					}
					if isCompleted {
						payload["status"] = "completed"
						if exitCode != nil {
							payload["exit_code"] = *exitCode
						}
						if output := firstNonEmpty(stdout, stderr); output != "" {
							payload["output"] = truncateForCloud(output)
						}
					}
					_ = writeJSON(payload)
				}
				// Best-effort reasoning streaming (codex reasoning items), kept off
				// the prose channel.
				if params, ok := msg["params"].(map[string]interface{}); ok {
					if item, ok := params["item"].(map[string]interface{}); ok {
						emitCodexThinkingIfPresent(run, writeJSON, item)
					}
				}
				// fileChange: seed the approval map (started) and emit ai.file_change
				// once the change is actually applied (completed).
				if fcItemID, fcChanges := extractCodexFileChangeItem(msg); fcItemID != "" && fcChanges != nil {
					fileChangesByID[fcItemID] = fcChanges
					if isCompleted {
						emitCodexFileChangeEvents(run, writeJSON, fcItemID, fcChanges)
					}
				}
			case "turn/completed":
				completedParams, _ := msg["params"].(map[string]interface{})
				var turnErr string
				turnStatus, turnErr = codexTurnResult(completedParams)
				emitCodexUsageIfPresent(run, writeJSON, completedParams)
				if turnStatus == "failed" {
					// A failed turn (ContextWindowExceeded, upstream 5xx, etc.) must
					// surface as an error, never as a successful ai.done. The error
					// notification may or may not have preceded this; either way the
					// terminal state here is failure.
					_ = writeJSON(agentAIErrorPayload(run.sessionID, run.messageID, errors.New(firstNonEmpty(turnErr, "codex turn failed"))))
				} else if turnStatus == "interrupted" {
					_ = writeJSON(map[string]interface{}{
						"type":       models.AgentEventAIStatus,
						"session_id": run.sessionID,
						"status":     "interrupted",
					})
				}
				completed = true
				goto done
			case "error":
				if params, ok := msg["params"].(map[string]interface{}); ok {
					terminalErr = "Codex app-server error: " + firstNonEmpty(remoteString(params, "message"), remoteString(params, "error"), "unknown error")
					goto done
				}
			}
			continue
		}
		if fmt.Sprint(msg["id"]) == "1" && threadID == "" {
			if result, ok := msg["result"].(map[string]interface{}); ok {
				if thread, ok := result["thread"].(map[string]interface{}); ok {
					threadID = remoteString(thread, "id")
				}
			}
			if threadID != "" {
				m.setAgentAIResumeSessionIDIfEmpty(run.sessionID, run.runSeq, threadID)
				turnStarted = true
				emitRunStarted()
				turnParams := map[string]interface{}{
					"threadId":            threadID,
					"clientUserMessageId": run.messageID,
					"input":               codexTurnInput(run),
					"approvalPolicy":      "on-request",
					"approvalsReviewer":   "user",
				}
				if model != "" {
					turnParams["model"] = model
				}
				if effort != "" {
					turnParams["effort"] = effort
				}
				codexPhase = "turn/start"
				_ = send(map[string]interface{}{
					"method": "turn/start",
					"id":     2,
					"params": turnParams,
				})
			} else if _, hasResult := msg["result"]; hasResult {
				terminalErr = fmt.Sprintf("Codex app-server %s returned no thread id", threadMethod)
				goto done
			}
		}
		if fmt.Sprint(msg["id"]) == "2" && turnID == "" {
			if result, ok := msg["result"].(map[string]interface{}); ok {
				if turn, ok := result["turn"].(map[string]interface{}); ok {
					turnID = remoteString(turn, "id")
				}
			}
			if turnID != "" {
				codexPhase = "running"
				steerControl.markReady(threadID, turnID)
			}
		}
	}

done:
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	stderrWG.Wait()
	if resumeMissing {
		return agentAIRunResumeMissing
	}
	if ctx.Err() != nil {
		status, errMsg := agentAIRunStoppedStatus(run.activity, limiter)
		statusPayload := map[string]interface{}{
			"type":       models.AgentEventAIStatus,
			"session_id": run.sessionID,
			"status":     status,
		}
		if errMsg != "" {
			if codexPhase != "" {
				errMsg = fmt.Sprintf("%s while %s", errMsg, codexPhase)
			}
			if stderrText := strings.TrimSpace(stderrBuf.String()); stderrText != "" {
				errMsg = fmt.Sprintf("%s; codex stderr: %s", errMsg, truncateForCloud(stderrText))
			}
			statusPayload["error"] = errMsg
		}
		_ = writeJSON(statusPayload)
		return agentAIRunDone
	}
	if terminalErr != "" {
		if stderrText := strings.TrimSpace(stderrBuf.String()); stderrText != "" {
			terminalErr = fmt.Sprintf("%s; codex stderr: %s", terminalErr, truncateForCloud(stderrText))
		}
		_ = writeJSON(agentAIErrorPayload(run.sessionID, run.messageID, errors.New(terminalErr)))
		return agentAIRunDone
	}
	if !completed {
		if err := scanner.Err(); err != nil {
			_ = writeJSON(agentAIErrorPayload(run.sessionID, run.messageID, err))
			return agentAIRunDone
		}
		if waitErr != nil {
			message := fmt.Sprintf("Codex app-server exited before turn completed while %s: %v", codexPhase, waitErr)
			if stderrText := strings.TrimSpace(stderrBuf.String()); stderrText != "" {
				message = fmt.Sprintf("%s; codex stderr: %s", message, truncateForCloud(stderrText))
			}
			_ = writeJSON(agentAIErrorPayload(run.sessionID, run.messageID, errors.New(message)))
			return agentAIRunDone
		}
		if turnStarted {
			_ = writeJSON(agentAIErrorPayload(run.sessionID, run.messageID, fmt.Errorf("Codex app-server exited before turn completed while %s", codexPhase)))
			return agentAIRunDone
		}
	}
	if turnStatus == "failed" || turnStatus == "interrupted" {
		// Terminal event already emitted above (ai.error / ai.status). Skip the
		// success path so a failed turn is not stored as a completed assistant
		// message and does not emit ai.done.
		return agentAIRunDone
	}
	outMu.Lock()
	assistantOutput := output.String()
	outMu.Unlock()
	m.appendAssistantHistory(run.sessionID, run.runSeq, run.messageID, assistantOutput)
	_ = writeJSON(map[string]interface{}{
		"type":              models.AgentEventAIDone,
		"session_id":        run.sessionID,
		"message_id":        agentAssistantMessageID(run.messageID),
		"codex_thread_id":   threadID,
		"source_session_id": threadID,
	})
	return agentAIRunDone
}

func codexTurnInput(run agentAIRun) []map[string]interface{} {
	text := run.prompt + agentAIAttachmentPromptSuffix(run.attachments, false)
	input := []map[string]interface{}{{"type": "text", "text": text, "text_elements": []interface{}{}}}
	for _, attachment := range run.attachments {
		if attachment.Type != "image" {
			continue
		}
		if attachment.URL != "" {
			input = append(input, map[string]interface{}{"type": "image", "url": attachment.URL})
		} else if attachment.Path != "" {
			input = append(input, map[string]interface{}{"type": "localImage", "path": attachment.Path})
		}
	}
	return input
}

func (m *agentAIManager) handleCodexAppServerRequest(ctx context.Context, run agentAIRun, writeJSON agentTerminalWriter, method string, params map[string]interface{}, fileChanges json.RawMessage) (map[string]interface{}, int, error) {
	if isCodexApprovalMethod(method) {
		result, err := m.codexAppServerApprovalResult(ctx, run, writeJSON, method, params, fileChanges)
		return result, -32000, err
	}
	if method == "item/tool/requestUserInput" {
		result, err := m.codexAppServerUserInputResult(ctx, run, writeJSON, params)
		return result, -32000, err
	}
	return nil, -32601, fmt.Errorf("unsupported Codex app-server request: %s", method)
}

func isCodexApprovalMethod(method string) bool {
	switch method {
	case "item/commandExecution/requestApproval", "item/fileChange/requestApproval", "item/permissions/requestApproval", "execCommandApproval", "applyPatchApproval":
		return true
	default:
		return false
	}
}

func (m *agentAIManager) codexAppServerUserInputResult(ctx context.Context, run agentAIRun, writeJSON agentTerminalWriter, params map[string]interface{}) (map[string]interface{}, error) {
	rawQuestions, _ := params["questions"].([]interface{})
	if len(rawQuestions) == 0 {
		return nil, errors.New("Codex requestUserInput contained no questions")
	}
	answers := make(map[string]interface{}, len(rawQuestions))
	for index, rawQuestion := range rawQuestions {
		question := mapIf(rawQuestion)
		if question == nil {
			continue
		}
		questionID := firstNonEmpty(remoteString(question, "id"), fmt.Sprintf("question_%d", index+1))
		optionID := newAgentAIApprovalID(run.sessionID, run.runSeq)
		choices, labelByID := codexUserInputChoices(question["options"])
		allowCustom := len(choices) == 0 || remoteBool(question, "isOther", false)
		if len(choices) == 0 {
			choices = []agentAIOptionChoice{{ID: "custom", Label: "自定义回答"}}
		}
		waiter := &agentAICodexInputWaiter{
			sessionID: run.sessionID,
			runSeq:    run.runSeq,
			optionID:  optionID,
			respond:   make(chan agentAICodexInputAnswer, 1),
		}
		key := agentAICodexInputMapKey(run.sessionID, optionID)
		m.mu.Lock()
		m.codexInputs[key] = waiter
		m.mu.Unlock()
		cleanup := func() {
			m.mu.Lock()
			if current := m.codexInputs[key]; current == waiter {
				delete(m.codexInputs, key)
			}
			m.mu.Unlock()
		}
		if run.activity != nil {
			run.activity.setAwaitingApproval(true)
		}
		err := writeJSON(map[string]interface{}{
			"type":         models.AgentEventAIOptionRequest,
			"session_id":   run.sessionID,
			"message_id":   agentAssistantMessageID(run.messageID),
			"option_id":    optionID,
			"title":        firstNonEmpty(remoteString(question, "header"), remoteString(question, "question")),
			"question":     remoteString(question, "question"),
			"options":      choices,
			"allow_custom": allowCustom,
			"multi":        false,
			"provider":     "codex",
			"question_id":  questionID,
		})
		if err != nil {
			cleanup()
			if run.activity != nil {
				run.activity.setAwaitingApproval(false)
			}
			return nil, err
		}
		var answer agentAICodexInputAnswer
		select {
		case <-ctx.Done():
			cleanup()
			if run.activity != nil {
				run.activity.setAwaitingApproval(false)
			}
			return nil, ctx.Err()
		case answer = <-waiter.respond:
			cleanup()
		}
		if run.activity != nil {
			run.activity.setAwaitingApproval(false)
		}
		values := make([]string, 0, len(answer.selected)+1)
		for _, selected := range answer.selected {
			if label := labelByID[selected]; label != "" {
				values = append(values, label)
			} else if selected != "custom" && strings.TrimSpace(selected) != "" {
				values = append(values, selected)
			}
		}
		if custom := strings.TrimSpace(answer.custom); custom != "" {
			values = append(values, custom)
		}
		if len(values) == 0 {
			return nil, fmt.Errorf("Codex requestUserInput question %s received no answer", questionID)
		}
		answers[questionID] = map[string]interface{}{"answers": values}
	}
	if len(answers) == 0 {
		return nil, errors.New("Codex requestUserInput contained no valid questions")
	}
	return map[string]interface{}{"answers": answers}, nil
}

func codexUserInputChoices(raw interface{}) ([]agentAIOptionChoice, map[string]string) {
	items, _ := raw.([]interface{})
	choices := make([]agentAIOptionChoice, 0, len(items))
	labels := make(map[string]string, len(items))
	for index, rawItem := range items {
		item := mapIf(rawItem)
		if item == nil {
			continue
		}
		label := strings.TrimSpace(remoteString(item, "label"))
		if label == "" {
			continue
		}
		id := fmt.Sprintf("option_%d", index+1)
		choices = append(choices, agentAIOptionChoice{ID: id, Label: label, Description: remoteString(item, "description")})
		labels[id] = label
	}
	return choices, labels
}

func (m *agentAIManager) codexAppServerApprovalResult(ctx context.Context, run agentAIRun, writeJSON agentTerminalWriter, method string, params map[string]interface{}, fileChanges json.RawMessage) (map[string]interface{}, error) {
	// Policy short-circuit (mirrors the Claude hook): auto-approve/deny locally
	// when the device policy says so, with no cloud round-trip. Only
	// require_approval falls through to requestApproval.
	if svc := m.approvalService(); svc != nil {
		toolName, toolInput := codexPolicyToolHint(method, params, fileChanges)
		switch decision, matchedID := svc.evaluateApprovalDecision(toolName, toolInput, run.projectPath); decision {
		case decisionAutoApprove:
			logger.Info(fmt.Sprintf("approval-hook: AUTO-APPROVE by policy rule=%s method=%s session=%s (codex, no cloud round-trip)", matchedID, method, run.sessionID))
			return codexApprovalResponseResult(method, params, agentAIApprovalResponse{Decision: models.AgentAIApprovalDecisionAccept})
		case decisionAutoDeny:
			logger.Info(fmt.Sprintf("approval-hook: AUTO-DENY by policy rule=%s method=%s session=%s (codex)", matchedID, method, run.sessionID))
			return codexApprovalResponseResult(method, params, agentAIApprovalResponse{Decision: models.AgentAIApprovalDecisionDecline})
		case decisionRequireApproval:
			run.matchedRuleID = matchedID
			run.policyVersion = svc.effectiveApprovalPolicyForPath(run.projectPath).Version
		}
	}
	req := buildCodexApprovalRequest(run, method, params, fileChanges)
	// Use the env-configurable approval timeout (ALIANG_AI_APPROVAL_TIMEOUT,
	// default 24h) — same source as the Claude hook path (see handleClaudeApprovalHook).
	// Previously this was a hardcoded 30m that clipped long approval waits even when
	// the operator had raised the configured ceiling.
	approvalCtx, cancel := context.WithTimeout(ctx, agentAIApprovalTimeout)
	defer cancel()
	response, err := m.requestApproval(approvalCtx, run, writeJSON, req)
	if err != nil {
		return nil, err
	}
	return codexApprovalResponseResult(method, params, response)
}

func buildCodexApprovalRequest(run agentAIRun, method string, params map[string]interface{}, fileChanges json.RawMessage) agentAIApprovalRequest {
	rawJSON := marshalAgentAIRaw(params)
	req := agentAIApprovalRequest{
		ID:       firstNonEmpty(remoteString(params, "approvalId"), remoteString(params, "itemId"), remoteString(params, "callId"), newAgentAIApprovalID(run.sessionID, run.runSeq)),
		Provider: "codex",
		Kind:     models.AgentAIApprovalKindTool,
		Title:    "Approve Codex action",
		Reason:   remoteString(params, "reason"),
		CWD:      firstNonEmpty(remoteString(params, "cwd"), run.projectPath),
		Raw:      rawJSON,
		AvailableDecisions: []string{
			models.AgentAIApprovalDecisionAccept,
			models.AgentAIApprovalDecisionAcceptForSession,
			models.AgentAIApprovalDecisionDecline,
			models.AgentAIApprovalDecisionCancel,
		},
	}
	switch method {
	case "item/commandExecution/requestApproval", "execCommandApproval":
		req.Kind = models.AgentAIApprovalKindCommand
		req.Title = "Approve Codex command"
		req.Command = codexApprovalCommand(params)
		req.ToolName = "command"
	case "item/fileChange/requestApproval", "applyPatchApproval":
		req.Kind = models.AgentAIApprovalKindFileChange
		req.Title = "Approve Codex file change"
		// The fileChange approval request carries no diff; the proposed edits
		// arrive in the prior item/started fileChange item, tracked by the caller
		// and passed in as fileChanges. Fall back to the legacy params shape for
		// older codex that inlined them.
		if fileChanges != nil {
			req.FileChanges = fileChanges
		} else {
			req.FileChanges = marshalAgentAIRaw(firstNonNil(params["fileChanges"], params["file_changes"]))
		}
	case "item/permissions/requestApproval":
		req.Kind = models.AgentAIApprovalKindPermissions
		req.Title = "Approve Codex permissions"
		req.ToolName = "permissions"
		req.ToolInput = marshalAgentAIRaw(params["permissions"])
	default:
		req.ToolName = method
	}
	if decisions := codexAvailableDecisions(params["availableDecisions"]); len(decisions) > 0 {
		req.AvailableDecisions = decisions
	}
	req.MatchedRuleID = run.matchedRuleID
	req.PolicyVersion = run.policyVersion
	return req
}

func codexApprovalCommand(params map[string]interface{}) string {
	if value := strings.TrimSpace(remoteString(params, "command")); value != "" {
		return value
	}
	if raw, ok := params["command"].([]interface{}); ok {
		parts := make([]string, 0, len(raw))
		for _, item := range raw {
			part := strings.TrimSpace(fmt.Sprint(item))
			if part != "" {
				parts = append(parts, part)
			}
		}
		return strings.Join(parts, " ")
	}
	return ""
}

// codexPolicyToolHint maps a Codex approval method+params to a (toolName,
// toolInput) hint for device-policy evaluation. Command methods map to Bash +
// the command string; file-change methods map to a file-mutation tool name;
// permissions and anything unknown map to "" so the policy's default applies
// (fail-safe under balanced, approve-all under allow_all).
func codexPolicyToolHint(method string, params map[string]interface{}, fileChanges json.RawMessage) (string, json.RawMessage) {
	switch method {
	case "item/commandExecution/requestApproval", "execCommandApproval":
		return "Bash", marshalAgentAIRaw(map[string]interface{}{"command": codexApprovalCommand(params)})
	case "item/fileChange/requestApproval", "applyPatchApproval":
		if len(fileChanges) == 0 {
			fileChanges = marshalAgentAIRaw(firstNonNil(params["fileChanges"], params["file_changes"]))
		}
		var changes []map[string]interface{}
		_ = json.Unmarshal(fileChanges, &changes)
		paths := make([]string, 0, len(changes))
		for _, change := range changes {
			if path := strings.TrimSpace(remoteString(change, "path")); path != "" {
				paths = append(paths, path)
			}
		}
		return "Edit", marshalAgentAIRaw(map[string]interface{}{"paths": paths})
	default:
		return "", nil
	}
}

func codexAvailableDecisions(raw interface{}) []string {
	items, ok := raw.([]interface{})
	if !ok {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		switch value := item.(type) {
		case string:
			if decision := normalizeAgentAIApprovalDecision(value); decision != "" {
				out = append(out, decision)
			}
		case map[string]interface{}:
			for key := range value {
				if decision := normalizeAgentAIApprovalDecision(key); decision != "" {
					out = append(out, decision)
				}
			}
		}
	}
	return out
}

func codexApprovalResponseResult(method string, params map[string]interface{}, response agentAIApprovalResponse) (map[string]interface{}, error) {
	switch method {
	case "execCommandApproval", "applyPatchApproval":
		return map[string]interface{}{"decision": codexLegacyReviewDecision(response.Decision)}, nil
	case "item/commandExecution/requestApproval":
		return map[string]interface{}{"decision": codexCommandDecision(response.Decision)}, nil
	case "item/fileChange/requestApproval":
		return map[string]interface{}{"decision": codexFileChangeDecision(response.Decision)}, nil
	case "item/permissions/requestApproval":
		switch response.Decision {
		case models.AgentAIApprovalDecisionAccept, models.AgentAIApprovalDecisionAcceptForSession:
			permissions, _ := params["permissions"].(map[string]interface{})
			if permissions == nil {
				permissions = map[string]interface{}{}
			}
			scope := normalizeAgentAIApprovalScope(response.Scope)
			if response.Decision == models.AgentAIApprovalDecisionAcceptForSession {
				scope = "session"
			}
			return map[string]interface{}{"permissions": permissions, "scope": scope}, nil
		default:
			return nil, errors.New("permission request denied by user")
		}
	default:
		if response.Decision == models.AgentAIApprovalDecisionAccept || response.Decision == models.AgentAIApprovalDecisionAcceptForSession {
			return map[string]interface{}{}, nil
		}
		return nil, errors.New("approval denied by user")
	}
}

func codexLegacyReviewDecision(decision string) interface{} {
	switch decision {
	case models.AgentAIApprovalDecisionAccept:
		return "approved"
	case models.AgentAIApprovalDecisionAcceptForSession:
		return "approved_for_session"
	case models.AgentAIApprovalDecisionCancel:
		return "abort"
	default:
		return "denied"
	}
}

func codexCommandDecision(decision string) interface{} {
	switch decision {
	case models.AgentAIApprovalDecisionAccept:
		return "accept"
	case models.AgentAIApprovalDecisionAcceptForSession:
		return "acceptForSession"
	case models.AgentAIApprovalDecisionCancel:
		return "cancel"
	default:
		return "decline"
	}
}

func codexFileChangeDecision(decision string) string {
	switch decision {
	case models.AgentAIApprovalDecisionAccept:
		return "accept"
	case models.AgentAIApprovalDecisionAcceptForSession:
		return "acceptForSession"
	case models.AgentAIApprovalDecisionCancel:
		return "cancel"
	default:
		return "decline"
	}
}

func firstNonNil(values ...interface{}) interface{} {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func (m *agentAIManager) runCLIPass(ctx context.Context, run agentAIRun, writeJSON agentTerminalWriter, allowResume bool) agentAIRunOutcome {
	resumeID := run.resumeSessionID
	newSessionID := run.reservedNativeSessionID
	if !allowResume {
		if newSessionID == "" {
			newSessionID = resumeID
		}
		resumeID = ""
	}

	var tool *agentAITool
	var err error
	if run.readOnly || len(run.goalIdentity) > 0 {
		tool, err = resolveGoalAgentAITool(run.prompt, run.provider, run.model, run.effort, resumeID, newSessionID)
	} else {
		tool, err = resolveAgentAITool(run.prompt, run.provider, run.model, run.effort, resumeID, newSessionID)
	}
	if err != nil {
		_ = writeJSON(agentAIErrorPayload(run.sessionID, run.messageID, err))
		return agentAIRunDone
	}
	if run.readOnly {
		tool = withGoalPlanningReadOnly(tool)
		if tool == nil {
			_ = writeJSON(agentAIErrorPayload(run.sessionID, run.messageID, errors.New("provider does not support enforced read-only planning")))
			return agentAIRunDone
		}
	} else if len(run.goalIdentity) > 0 {
		tool = withGoalExecutionPolicy(tool)
	}
	tool = withAgentAIAttachments(tool, run.attachments)
	cleanupClaudePolicy := func() {}
	if tool.outputFormat == agentAIOutputClaudeStreamJSON && !run.readOnly {
		var policyErr error
		tool, cleanupClaudePolicy, policyErr = withClaudeRemotePolicy(tool, run)
		if policyErr != nil {
			_ = writeJSON(agentAIErrorPayload(run.sessionID, run.messageID, policyErr))
			return agentAIRunDone
		}
		defer cleanupClaudePolicy()
		tool = withClaudeApprovalHook(tool, run)
	}

	cmd := newBackgroundCommandContext(ctx, tool.path, tool.args...)
	cmd.Dir = run.projectPath
	cmd.Env = agentChildProcessEnv()
	if len(tool.env) > 0 {
		cmd.Env = append(cmd.Env, tool.env...)
	}
	logger.Info(fmt.Sprintf(
		"ai.run.cli: session=%s runSeq=%d provider=%s path=%q cwd=%q allowResume=%t resume=%t model=%q effort=%q output=%s hook_base=%q args=%v env=%s",
		run.sessionID,
		run.runSeq,
		tool.id,
		tool.path,
		run.projectPath,
		allowResume,
		strings.TrimSpace(resumeID) != "",
		normalizeAgentAIModel(run.model),
		strings.TrimSpace(run.effort),
		tool.outputFormat,
		currentAgentAIApprovalHookBaseURL(),
		agentAIDiagnosticArgs(tool.args),
		agentAIEnvDiagnostic(),
	))

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = writeJSON(agentAIErrorPayload(run.sessionID, run.messageID, err))
		return agentAIRunDone
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = writeJSON(agentAIErrorPayload(run.sessionID, run.messageID, err))
		return agentAIRunDone
	}
	if err := cmd.Start(); err != nil {
		_ = writeJSON(agentAIErrorPayload(run.sessionID, run.messageID, err))
		return agentAIRunDone
	}

	_ = writeJSON(map[string]interface{}{
		"type":         models.AgentEventAIRunStarted,
		"session_id":   run.sessionID,
		"message_id":   agentAssistantMessageID(run.messageID),
		"provider":     tool.id,
		"mode":         run.mode,
		"project_path": run.projectPath,
		"state":        "running",
	})

	var wg sync.WaitGroup
	var outMu sync.Mutex
	var output strings.Builder
	var stderrBuf strings.Builder
	// Live "files touched this run" tracking: the stdout stream goroutine feeds
	// file-mutating tool_use paths into filesTouched via fileSink; a progress
	// ticker reads it. Locals (not on the by-value run) so the mutex is not copied.
	var filesMu sync.Mutex
	filesTouched := make(map[string]struct{})
	fileSink := func(fp string) {
		if fp = strings.TrimSpace(fp); fp == "" {
			return
		}
		filesMu.Lock()
		filesTouched[fp] = struct{}{}
		filesMu.Unlock()
	}
	progressStop := make(chan struct{})
	defer close(progressStop)
	go func() {
		ticker := time.NewTicker(agentAIRunProgressInterval)
		defer ticker.Stop()
		emit := func() {
			filesMu.Lock()
			touched := len(filesTouched)
			filesMu.Unlock()
			_ = writeJSON(map[string]interface{}{
				"type":                models.AgentEventAIRunProgress,
				"session_id":          run.sessionID,
				"message_id":          agentAssistantMessageID(run.messageID),
				"files_touched_count": touched,
				"git_changed_count":   countGitChanged(run.projectPath),
			})
		}
		for {
			select {
			case <-progressStop:
				return
			case <-ticker.C:
				emit()
			}
		}
	}()
	limiter := &agentAIOutputLimiter{meter: newOutputMeter(agentAIOutputRateWindow, agentAIOutputRateBytes, int64(agentAIOutputCapBytes))}
	// lastRetry is written by the stdout goroutine (on each Claude api_retry
	// event) and read here after wg.Wait(), so the terminal ai.error can carry a
	// structured cause. wg.Wait() provides the happens-before edge.
	var lastRetry claudeRetryInfo
	// capturedResumeSessionID is written by the stdout goroutine (from the
	// provider's structured events) and read after wg.Wait(), which gives the
	// happens-before edge — same pattern as lastRetry. Pinned on the session
	// post-run so the next CLI turn resumes instead of starting fresh.
	var capturedResumeSessionID string
	var bindingOnce sync.Once
	var bindingErr error
	onNativeSessionID := func(nativeSessionID string) {
		bindingOnce.Do(func() {
			record, err := m.confirmBinding(
				run.sessionID,
				tool.id,
				nativeSessionID,
				run.bindingVersion,
			)
			if err != nil {
				bindingErr = err
				_ = writeJSON(agentAIErrorPayload(run.sessionID, run.messageID, err))
				if run.cancel != nil {
					run.cancel()
				}
				return
			}
			_ = writeJSON(map[string]interface{}{
				"type":              models.AgentEventAISessionBound,
				"session_id":        run.sessionID,
				"message_id":        run.messageID,
				"provider":          tool.id,
				"native_session_id": record.NativeSessionID,
				"source_session_id": record.NativeSessionID,
				"binding_version":   record.BindingVersion,
				"binding_state":     record.State,
			})
		})
	}
	wg.Add(2)
	go func() {
		defer wg.Done()
		streamAgentAIStdout(stdout, tool.outputFormat, run, writeJSON, limiter, func(text string) {
			outMu.Lock()
			appendAgentAIHistoryCapture(&output, text)
			outMu.Unlock()
		}, fileSink, &lastRetry, &capturedResumeSessionID, onNativeSessionID)
	}()
	go func() {
		defer wg.Done()
		// Capture the head of stderr (capped) so a stale/missing --resume
		// session can be detected and the run retried fresh; the remainder is
		// drained to keep memory bounded.
		captureAgentAIStderr(stderr, &stderrBuf)
	}()
	wg.Wait()
	// StdoutPipe/StderrPipe must be fully drained before Wait. Calling Wait first
	// lets a short-lived CLI close the pipes while the reader goroutines still
	// have buffered JSON events, which intermittently dropped final deltas.
	waitErr := cmd.Wait()
	if bindingErr != nil {
		return agentAIRunDone
	}

	if ctx.Err() != nil {
		status, errMsg := agentAIRunStoppedStatus(run.activity, limiter)
		statusPayload := map[string]interface{}{
			"type":       models.AgentEventAIStatus,
			"session_id": run.sessionID,
			"status":     status,
		}
		if errMsg != "" {
			statusPayload["error"] = errMsg
		}
		_ = writeJSON(statusPayload)
		return agentAIRunDone
	}
	if waitErr != nil {
		// The referenced --resume session is not resolvable in this cwd (e.g.
		// an imported session, or one created under a different project path).
		// Signal the caller to retry without --resume instead of erroring.
		if allowResume && isAgentAIResumeMissing(stderrBuf.String()) {
			return agentAIRunResumeMissing
		}
		// Surface WHY the CLI exited. Prefer the structured cause derived from
		// the last api_retry (gateway status + retry count) when available; the
		// bare waitErr ("exit status 1") carries no reason on its own. The stderr
		// capture goes into "detail" (phone renders the structured cause, not the
		// raw stderr). Mirrors the codex app-server path's stderr enrichment.
		cause := claudeFailureCause(waitErr, lastRetry)
		payload := agentAIErrorPayloadWithRetry(run.sessionID, run.messageID, errors.New(cause), lastRetry)
		if stderrText := strings.TrimSpace(stderrBuf.String()); stderrText != "" {
			payload["detail"] = truncateForCloud(stderrText)
		}
		_ = writeJSON(payload)
		return agentAIRunDone
	}
	outMu.Lock()
	assistantOutput := output.String()
	outMu.Unlock()
	filesMu.Lock()
	filesTouchedCount := len(filesTouched)
	filesMu.Unlock()
	m.appendAssistantHistory(run.sessionID, run.runSeq, run.messageID, assistantOutput)
	m.setAgentAIResumeSessionIDIfEmpty(run.sessionID, run.runSeq, capturedResumeSessionID)
	if blocks := extractAgentAIOptionBlocks(assistantOutput); len(blocks) > 0 {
		m.emitOptionRequest(run, writeJSON, blocks)
	}
	done := map[string]interface{}{
		"type":                models.AgentEventAIDone,
		"session_id":          run.sessionID,
		"message_id":          agentAssistantMessageID(run.messageID),
		"files_touched_count": filesTouchedCount,
		"git_changed_count":   countGitChanged(run.projectPath),
	}
	// Echo provider-specific CLI session ids so the server can persist them
	// (sourceSessionId) and feed them back as resume_session_id after an agent
	// restart — keeping the conversation on one continuous local session.
	if sid := strings.TrimSpace(capturedResumeSessionID); sid != "" && tool.outputFormat == agentAIOutputClaudeStreamJSON {
		done["claude_session_id"] = sid
	}
	if sid := strings.TrimSpace(capturedResumeSessionID); sid != "" && tool.outputFormat == agentAIOutputOpenCodeJSON {
		done["opencode_session_id"] = sid
		done["source_session_id"] = sid
	}
	_ = writeJSON(done)
	return agentAIRunDone
}

func isAgentAIResumeMissing(stderr string) bool {
	lower := strings.ToLower(stderr)
	if strings.Contains(stderr, "No conversation found with session ID") {
		return true
	}
	mentionsSession := strings.Contains(lower, "session") || strings.Contains(lower, "thread") || strings.Contains(lower, "rollout")
	missing := strings.Contains(lower, "not found") || strings.Contains(lower, "does not exist") || strings.Contains(lower, "no rollout")
	return mentionsSession && missing
}

// captureAgentAIStderr reads everything from reader, storing the first cap
// bytes into buf and discarding the rest. It never blocks the run on stderr
// volume and bounds memory usage.
func captureAgentAIStderr(reader io.Reader, buf *strings.Builder) {
	const cap = 8 * 1024
	b := make([]byte, 4096)
	for {
		n, err := reader.Read(b)
		if n > 0 && buf.Len() < cap {
			room := cap - buf.Len()
			if room > n {
				room = n
			}
			buf.Write(b[:room])
		}
		if err != nil {
			return
		}
	}
}

// agentAIOutputLimiter bounds AI streamed output with the shared outputMeter.
// Reserve keeps the original (n int) int signature so the streaming call sites
// are unchanged: it returns n when the window has not tripped (emit the full
// chunk) and 0 once a sustained burst exceeds the rate window or the lifetime
// cap, which the caller treats as "stop the run" (it then calls run.cancel()).
type agentAIOutputLimiter struct {
	mu       sync.Mutex
	meter    *outputMeter
	exceeded bool
}

func (l *agentAIOutputLimiter) Reserve(n int) int {
	if l == nil {
		return n
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.exceeded {
		return 0
	}
	if l.meter == nil {
		return n
	}
	if l.meter.add(n, time.Now()) {
		l.exceeded = true
		return 0
	}
	return n
}

func (l *agentAIOutputLimiter) Exceeded() bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.exceeded
}

func streamAgentAIStdout(reader io.Reader, format agentAIOutputFormat, run agentAIRun, writeJSON agentTerminalWriter, limiter *agentAIOutputLimiter, capture func(string), fileSink func(string), lastRetry *claudeRetryInfo, resumeSessionID *string, onSessionID ...func(string)) {
	switch format {
	case agentAIOutputCodexJSON, agentAIOutputClaudeStreamJSON, agentAIOutputOpenCodeJSON:
		streamStructuredAIDelta(reader, format, run, writeJSON, limiter, capture, fileSink, lastRetry, resumeSessionID, onSessionID...)
	default:
		streamAIDelta(reader, run, "assistant", writeJSON, limiter, capture)
	}
}

func streamStructuredAIDelta(reader io.Reader, format agentAIOutputFormat, run agentAIRun, writeJSON agentTerminalWriter, limiter *agentAIOutputLimiter, capture func(string), fileSink func(string), lastRetry *claudeRetryInfo, resumeSessionID *string, onSessionID ...func(string)) {
	decoder := json.NewDecoder(reader)
	emitted := false
	// pendingCommands tracks Bash tool_use_id → command so a later user-turn
	// tool_result can close it as ai.command(completed) with output + exit code.
	pendingCommands := map[string]string{}
	// pendingClaudeToolUseIDs tracks every Claude tool_use whose tool_result has
	// not arrived yet. This keeps the idle watchdog from killing a quiet but
	// legitimate Task/subagent or long-running tool wait.
	pendingClaudeToolUseIDs := map[string]struct{}{}
	for {
		var event map[string]interface{}
		if err := decoder.Decode(&event); err != nil {
			return
		}
		// Capture the provider's resumable session id from whichever structured
		// event first carries it. Pinned after a successful run so the next turn
		// resumes one continuous local session instead of N fresh runs.
		if format == agentAIOutputClaudeStreamJSON && resumeSessionID != nil && *resumeSessionID == "" {
			if sid := strings.TrimSpace(remoteString(event, "session_id")); sid != "" {
				*resumeSessionID = sid
				logger.Info(fmt.Sprintf("claude session_id captured for run %s: %s (from %s event)", run.sessionID, sid, remoteString(event, "type")))
				if len(onSessionID) > 0 && onSessionID[0] != nil {
					onSessionID[0](sid)
				}
			}
		}
		if format == agentAIOutputClaudeStreamJSON &&
			remoteString(event, "type") == "system" &&
			remoteString(event, "subtype") == "init" &&
			run.onClaudeInit != nil {
			run.onClaudeInit(
				remoteStringSlice(event, "slash_commands"),
				firstNonEmpty(remoteString(event, "claude_code_version"), remoteString(event, "version")),
			)
		}
		if format == agentAIOutputOpenCodeJSON && resumeSessionID != nil && *resumeSessionID == "" {
			if sid := openCodeSessionID(event); sid != "" {
				*resumeSessionID = sid
				logger.Info(fmt.Sprintf("opencode session id captured for run %s: %s (from %s event)", run.sessionID, sid, remoteString(event, "type")))
				if len(onSessionID) > 0 && onSessionID[0] != nil {
					onSessionID[0](sid)
				}
			}
		}
		if fileSink != nil {
			for _, fp := range extractToolUseFilePaths(format, event) {
				fileSink(fp)
			}
		}
		if format == agentAIOutputClaudeStreamJSON {
			updateClaudeToolUseActivity(event, run.activity, pendingClaudeToolUseIDs)
		}
		emitClaudeStructuredEvents(format, event, run, writeJSON, pendingCommands)
		emitOpenCodeStructuredEvents(format, event, run, writeJSON)
		if format == agentAIOutputOpenCodeJSON {
			if reason, isErr := openCodeEventError(event); isErr {
				_ = writeJSON(agentAIErrorPayload(run.sessionID, run.messageID, errors.New(reason)))
				if run.cancel != nil {
					run.cancel()
				}
				return
			}
		}
		// Claude surfaces each upstream retry as a structured system event:
		// {"type":"system","subtype":"api_retry", attempt, max_retries,
		// retry_delay_ms, error_status, error}. Forward it as ai.run.progress so
		// the phone can show "重试 2/10 · 网关 502" instead of a silent gap, and
		// remember the last one to enrich the terminal ai.error with a structured
		// cause. The CLI's own "Retrying..." text never appears in stream-json, so
		// this event is the only retry signal available.
		if format == agentAIOutputClaudeStreamJSON &&
			remoteString(event, "type") == "system" &&
			remoteString(event, "subtype") == "api_retry" {
			ri := claudeRetryInfo{has: true}
			if n, ok := event["attempt"].(float64); ok {
				ri.attempt = int(n)
			}
			if n, ok := event["max_retries"].(float64); ok {
				ri.max = int(n)
			}
			if n, ok := event["retry_delay_ms"].(float64); ok {
				ri.delayMs = n
			}
			if n, ok := event["error_status"].(float64); ok {
				ri.errorStatus = int(n)
			}
			ri.errorType = remoteString(event, "error")
			if lastRetry != nil {
				*lastRetry = ri
			}
			logger.Warn(fmt.Sprintf(
				"ai.run.claude_retry: session=%s runSeq=%d attempt=%d max=%d delay_ms=%.0f status=%d error=%q",
				run.sessionID,
				run.runSeq,
				ri.attempt,
				ri.max,
				ri.delayMs,
				ri.errorStatus,
				ri.errorType,
			))
			progress := map[string]interface{}{
				"type":         models.AgentEventAIRunProgress,
				"session_id":   run.sessionID,
				"retry_active": true,
			}
			if ri.attempt > 0 {
				progress["retry_attempt"] = ri.attempt
			}
			if ri.max > 0 {
				progress["retry_max"] = ri.max
			}
			if ri.delayMs > 0 {
				progress["retry_delay_ms"] = ri.delayMs
			}
			if ri.errorStatus > 0 {
				progress["error_status"] = ri.errorStatus
			}
			if ri.errorType != "" {
				progress["error_type"] = ri.errorType
			}
			_ = writeJSON(progress)
			continue
		}
		// A Claude "result" event that failed (is_error / error_* subtype) must
		// surface as ai.error with a structured cause, not be masked as an
		// assistant delta (which would make the turn look successful). Stop the
		// stream. The raw result text is kept as debug "detail" only — the phone
		// renders the structured cause derived from the last api_retry.
		if format == agentAIOutputClaudeStreamJSON {
			if reason, isErr := claudeResultError(event); isErr {
				ri := claudeRetryInfo{}
				if lastRetry != nil {
					ri = *lastRetry
				}
				var cause, detail string
				if ri.has && ri.errorStatus > 0 {
					cause = claudeFailureCause(nil, ri)
					detail = reason
				} else if reason != "" {
					cause = reason
				} else {
					cause = "claude run failed"
				}
				payload := agentAIErrorPayloadWithRetry(run.sessionID, run.messageID, errors.New(cause), ri)
				if detail != "" {
					payload["detail"] = truncateForCloud(detail)
				}
				_ = writeJSON(payload)
				return
			}
			// (claude session_id capture moved to the top of the decode loop —
			// grabs the first non-empty session_id from any event, not just the
			// result event, since system/init always carries it.)
		}
		for _, text := range extractStructuredAITexts(format, event, !emitted) {
			if strings.TrimSpace(text) == "" {
				continue
			}
			if !emitAIDelta(text, run, "assistant", writeJSON, limiter, capture) {
				return
			}
			emitted = true
		}
	}
}

func updateClaudeToolUseActivity(event map[string]interface{}, activity *agentAIActivity, pending map[string]struct{}) {
	if activity == nil || pending == nil {
		return
	}
	message := mapIf(event["message"])
	if message == nil {
		return
	}
	content, _ := message["content"].([]interface{})
	if len(content) == 0 {
		return
	}
	switch remoteString(event, "type") {
	case "assistant":
		for _, raw := range content {
			row := mapIf(raw)
			if row == nil || remoteString(row, "type") != "tool_use" {
				continue
			}
			id := remoteString(row, "id")
			if id == "" {
				continue
			}
			if _, exists := pending[id]; exists {
				continue
			}
			pending[id] = struct{}{}
			activity.beginToolUseWait()
		}
	case "user":
		for _, raw := range content {
			row := mapIf(raw)
			if row == nil || remoteString(row, "type") != "tool_result" {
				continue
			}
			id := remoteString(row, "tool_use_id")
			if id == "" {
				continue
			}
			if _, exists := pending[id]; !exists {
				continue
			}
			delete(pending, id)
			activity.endToolUseWait()
		}
	}
}

func extractStructuredAITexts(format agentAIOutputFormat, event map[string]interface{}, allowFinal bool) []string {
	switch format {
	case agentAIOutputClaudeStreamJSON:
		return extractClaudeStreamTexts(event, allowFinal)
	case agentAIOutputCodexJSON:
		return extractCodexJSONTexts(event, allowFinal)
	case agentAIOutputOpenCodeJSON:
		return extractOpenCodeJSONTexts(event, allowFinal)
	default:
		return nil
	}
}

func extractOpenCodeJSONTexts(event map[string]interface{}, allowFinal bool) []string {
	eventType := strings.ToLower(strings.TrimSpace(remoteString(event, "type")))
	props := openCodeEventProperties(event)
	if strings.Contains(eventType, "part.delta") {
		if text := firstNonBlankPreserveSpace(remoteString(props, "delta"), remoteString(event, "delta")); text != "" {
			return []string{text}
		}
	}
	if !allowFinal {
		return nil
	}
	if part := firstOpenCodeMap(props["part"], event["part"]); part != nil {
		if text := openCodePartText(part); text != "" {
			return []string{text}
		}
	}
	if message := firstOpenCodeMap(props["message"], event["message"]); message != nil {
		if role := strings.ToLower(strings.TrimSpace(remoteString(message, "role"))); role == "" || role == "assistant" {
			if texts := openCodeMessageTexts(message["parts"]); len(texts) > 0 {
				return texts
			}
			if text := firstNonBlankPreserveSpace(remoteString(message, "text"), remoteString(message, "content")); text != "" {
				return []string{text}
			}
		}
	}
	if role := strings.ToLower(strings.TrimSpace(remoteString(event, "role"))); role == "assistant" {
		if texts := openCodeMessageTexts(event["parts"]); len(texts) > 0 {
			return texts
		}
		if text := firstNonBlankPreserveSpace(remoteString(event, "text"), remoteString(event, "content")); text != "" {
			return []string{text}
		}
	}
	if strings.Contains(eventType, "message") || strings.Contains(eventType, "assistant") {
		if text := firstNonBlankPreserveSpace(remoteString(props, "text"), remoteString(props, "content"), remoteString(event, "text"), remoteString(event, "content")); text != "" {
			return []string{text}
		}
	}
	return nil
}

func openCodeEventProperties(event map[string]interface{}) map[string]interface{} {
	if props := mapIf(event["properties"]); props != nil {
		return props
	}
	if data := mapIf(event["data"]); data != nil {
		return data
	}
	return event
}

func openCodeSessionID(event map[string]interface{}) string {
	props := openCodeEventProperties(event)
	if sid := strings.TrimSpace(firstNonEmpty(
		remoteString(event, "sessionID"),
		remoteString(event, "sessionId"),
		remoteString(event, "session_id"),
		remoteString(props, "sessionID"),
		remoteString(props, "sessionId"),
		remoteString(props, "session_id"),
	)); sid != "" {
		return sid
	}
	for _, key := range []string{"session", "info"} {
		if nested := mapIf(props[key]); nested != nil {
			if sid := strings.TrimSpace(firstNonEmpty(remoteString(nested, "id"), remoteString(nested, "sessionID"), remoteString(nested, "session_id"))); sid != "" {
				return sid
			}
		}
	}
	return ""
}

func firstOpenCodeMap(values ...interface{}) map[string]interface{} {
	for _, value := range values {
		if row := mapIf(value); row != nil {
			return row
		}
	}
	return nil
}

func firstNonBlankPreserveSpace(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func openCodeMessageTexts(raw interface{}) []string {
	parts, ok := raw.([]interface{})
	if !ok {
		return nil
	}
	var out []string
	for _, rawPart := range parts {
		part := mapIf(rawPart)
		if part == nil {
			continue
		}
		if text := openCodePartText(part); text != "" {
			out = append(out, text)
		}
	}
	return out
}

func openCodePartText(part map[string]interface{}) string {
	partType := strings.ToLower(strings.TrimSpace(remoteString(part, "type")))
	if partType != "" {
		for _, blocked := range []string{"tool", "bash", "command", "permission", "reasoning", "thinking"} {
			if strings.Contains(partType, blocked) {
				return ""
			}
		}
	}
	if text := firstNonBlankPreserveSpace(remoteString(part, "text"), remoteString(part, "content"), remoteString(part, "delta")); text != "" {
		return text
	}
	if content := agentContentText(part["content"]); strings.TrimSpace(content) != "" {
		return strings.TrimSpace(content)
	}
	return ""
}

func openCodeEventError(event map[string]interface{}) (string, bool) {
	eventType := strings.ToLower(strings.TrimSpace(remoteString(event, "type")))
	if !strings.Contains(eventType, "error") && event["error"] == nil {
		return "", false
	}
	props := openCodeEventProperties(event)
	for _, raw := range []interface{}{props["error"], event["error"], props, event} {
		if row := mapIf(raw); row != nil {
			if message := strings.TrimSpace(firstNonEmpty(
				remoteString(row, "message"),
				remoteString(row, "detail"),
				remoteString(row, "name"),
			)); message != "" {
				return message, true
			}
		}
		if message, ok := raw.(string); ok && strings.TrimSpace(message) != "" {
			return strings.TrimSpace(message), true
		}
	}
	return firstNonEmpty(eventType, "opencode run failed"), true
}

func extractClaudeStreamTexts(event map[string]interface{}, allowFinal bool) []string {
	if remoteString(event, "type") == "stream_event" {
		if streamEvent, ok := event["event"].(map[string]interface{}); ok && remoteString(streamEvent, "type") == "content_block_delta" {
			if delta, ok := streamEvent["delta"].(map[string]interface{}); ok && remoteString(delta, "type") == "text_delta" {
				if text := remoteString(delta, "text"); text != "" {
					return []string{text}
				}
			}
		}
	}
	if !allowFinal {
		return nil
	}
	switch remoteString(event, "type") {
	case "assistant":
		if message, ok := event["message"].(map[string]interface{}); ok {
			// Claude emits a synthetic assistant message (model "<synthetic>",
			// carrying the error text) on terminal failure. Streaming it as a
			// normal reply masks the failure — skip it; the result event (or the
			// CLI exit) surfaces the failure as ai.error.
			if remoteString(message, "model") == "<synthetic>" {
				return nil
			}
			if text := claudeMessageContentText(message["content"]); text != "" {
				return []string{text}
			}
		}
	case "result":
		// A failed result (is_error / error_* subtype) must NOT be emitted as an
		// assistant delta — that masks the failure as a successful reply. The
		// streaming loop surfaces it as ai.error via claudeResultError.
		if _, isErr := claudeResultError(event); isErr {
			return nil
		}
		if text := remoteString(event, "result"); text != "" {
			return []string{text}
		}
	}
	return nil
}

// claudeResultError reports whether a Claude stream-json "result" event is a
// failure (is_error == true, or an error_* subtype such as
// error_during_execution / error_max_tokens) and, if so, returns a human
// reason — the result text, falling back to the subtype. Non-result / success
// events return ("", false). Used by the streaming loop to surface the failure
// as ai.error instead of letting it be masked as an assistant delta.
func claudeResultError(event map[string]interface{}) (string, bool) {
	if remoteString(event, "type") != "result" {
		return "", false
	}
	isErr, _ := event["is_error"].(bool)
	subtype := remoteString(event, "subtype")
	if !isErr && !strings.HasPrefix(subtype, "error_") {
		return "", false
	}
	reason := strings.TrimSpace(remoteString(event, "result"))
	if reason == "" {
		reason = subtype
	}
	if reason == "" {
		reason = "claude run failed"
	}
	return reason, true
}

func claudeMessageContentText(raw interface{}) string {
	items, ok := raw.([]interface{})
	if !ok || len(items) == 0 {
		return ""
	}
	var builder strings.Builder
	for _, item := range items {
		row, ok := item.(map[string]interface{})
		if !ok || remoteString(row, "type") != "text" {
			continue
		}
		builder.WriteString(remoteString(row, "text"))
	}
	return builder.String()
}

// claudeMessageToolUseFilePaths collects distinct target paths of file-mutating
// tool_use blocks (Write/Edit/MultiEdit) from a finalized Claude assistant
// message's content array. Read-only tools and notebook ops are ignored — these
// are the files the run actually changed, surfaced to the mobile dashboard.
func claudeMessageToolUseFilePaths(raw interface{}) []string {
	items, ok := raw.([]interface{})
	if !ok {
		return nil
	}
	var paths []string
	for _, item := range items {
		row, ok := item.(map[string]interface{})
		if !ok || remoteString(row, "type") != "tool_use" {
			continue
		}
		switch remoteString(row, "name") {
		case "Write", "Edit", "MultiEdit":
		default:
			continue
		}
		input, ok := row["input"].(map[string]interface{})
		if !ok {
			continue
		}
		if fp := strings.TrimSpace(remoteString(input, "file_path")); fp != "" {
			paths = append(paths, fp)
		}
	}
	return paths
}

// extractToolUseFilePaths pulls file-mutating tool_use target paths from a
// structured AI stream event. For Claude stream-json the finalized "assistant"
// message event carries complete tool_use blocks with their inputs (the
// per-token input_json_delta fragments are intentionally not reconstructed).
// Codex file changes are approval-routed and left as a follow-up.
func extractToolUseFilePaths(format agentAIOutputFormat, event map[string]interface{}) []string {
	if format == agentAIOutputClaudeStreamJSON && remoteString(event, "type") == "assistant" {
		if message, ok := event["message"].(map[string]interface{}); ok {
			return claudeMessageToolUseFilePaths(message["content"])
		}
	}
	return nil
}

// countTextLines reports the number of lines in s for ±line accounting: empty
// text is 0; otherwise newline count, bumped by one when there is no trailing
// newline (so "a\nb" and "a\nb\n" are both 2).
func countTextLines(s string) int {
	if s == "" {
		return 0
	}
	n := strings.Count(s, "\n")
	if !strings.HasSuffix(s, "\n") {
		n++
	}
	return n
}

// claudeEditLineDelta estimates added/removed lines for a file-mutating Claude
// tool_use. Write contributes its content as additions; Edit/MultiEdit compare
// new_string vs old_string. Exact removed counts would need the baseline file;
// this is a faithful approximation for the activity feed, and the raw edit is
// not what we forward (only the counts + path).
func claudeEditLineDelta(toolName string, input map[string]interface{}) (added, removed int) {
	switch toolName {
	case "Write":
		return countTextLines(remoteString(input, "content")), 0
	case "Edit":
		return countTextLines(remoteString(input, "new_string")), countTextLines(remoteString(input, "old_string"))
	case "MultiEdit":
		if edits, ok := input["edits"].([]interface{}); ok {
			for _, raw := range edits {
				edit, ok := raw.(map[string]interface{})
				if !ok {
					continue
				}
				added += countTextLines(remoteString(edit, "new_string"))
				removed += countTextLines(remoteString(edit, "old_string"))
			}
		}
	}
	return added, removed
}

// claudeToolUseEvents maps a finalized Claude assistant message's tool_use
// blocks to structured activity events. Bash → ai.command(started) and its
// tool_use_id→command mapping is recorded in pending so a later tool_result can
// close it as completed. Write/Edit/MultiEdit → ai.file_change; TodoWrite →
// ai.task. Read-only and other tools are ignored (write/exec surface only).
func claudeToolUseEvents(content []interface{}, run agentAIRun, pending map[string]string) []map[string]interface{} {
	items := content
	msgID := agentAssistantMessageID(run.messageID)
	var out []map[string]interface{}
	for _, raw := range items {
		row, ok := raw.(map[string]interface{})
		if !ok || remoteString(row, "type") != "tool_use" {
			continue
		}
		name := remoteString(row, "name")
		id := remoteString(row, "id")
		input, _ := row["input"].(map[string]interface{})
		switch name {
		case "Bash":
			command := remoteString(input, "command")
			if id != "" && command != "" && pending != nil {
				pending[id] = command
			}
			payload := map[string]interface{}{
				"type":       models.AgentEventAICommand,
				"session_id": run.sessionID,
				"message_id": msgID,
				"item_id":    id,
				"status":     "started",
				"command":    command,
			}
			if cwd := remoteString(input, "cwd"); cwd != "" {
				payload["cwd"] = cwd
			}
			out = append(out, payload)
		case "Write", "Edit", "MultiEdit":
			added, removed := claudeEditLineDelta(name, input)
			kind := "edit"
			if name == "Write" {
				kind = "create"
			}
			out = append(out, map[string]interface{}{
				"type":       models.AgentEventAIFileChange,
				"session_id": run.sessionID,
				"message_id": msgID,
				"item_id":    id,
				"path":       remoteString(input, "file_path"),
				"kind":       kind,
				"added":      added,
				"removed":    removed,
			})
		case "TodoWrite":
			out = append(out, map[string]interface{}{
				"type":       models.AgentEventAITask,
				"session_id": run.sessionID,
				"message_id": msgID,
				"item_id":    id,
				"tasks":      claudeTodosToTasks(input["todos"]),
			})
		}
	}
	return out
}

// claudeTodosToTasks maps a TodoWrite todos array to the ai.task tasks payload:
// content→subject, status passthrough, activeForm→active_form.
func claudeTodosToTasks(raw interface{}) []map[string]interface{} {
	items, ok := raw.([]interface{})
	if !ok {
		return nil
	}
	out := make([]map[string]interface{}, 0, len(items))
	for _, item := range items {
		todo, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		entry := map[string]interface{}{
			"subject": remoteString(todo, "content"),
			"status":  remoteString(todo, "status"),
		}
		if af := remoteString(todo, "activeForm"); af != "" {
			entry["active_form"] = af
		}
		out = append(out, entry)
	}
	return out
}

// claudeToolResultEvents closes Bash commands whose tool_result arrives in a
// later "user" turn. A matching tool_use_id emits ai.command(completed) with the
// result text (truncated) and exit_code 1 for is_error, else 0; the pending
// entry is consumed. Unknown ids are ignored.
func claudeToolResultEvents(content []interface{}, run agentAIRun, pending map[string]string) []map[string]interface{} {
	items := content
	if len(pending) == 0 {
		return nil
	}
	msgID := agentAssistantMessageID(run.messageID)
	var out []map[string]interface{}
	for _, raw := range items {
		row, ok := raw.(map[string]interface{})
		if !ok || remoteString(row, "type") != "tool_result" {
			continue
		}
		id := remoteString(row, "tool_use_id")
		command, matched := pending[id]
		if !matched {
			continue
		}
		exitCode := 0
		if isErr, ok := row["is_error"].(bool); ok && isErr {
			exitCode = 1
		}
		output := truncateForCloud(claudeToolResultText(row["content"]))
		payload := map[string]interface{}{
			"type":       models.AgentEventAICommand,
			"session_id": run.sessionID,
			"message_id": msgID,
			"item_id":    id,
			"status":     "completed",
			"command":    command,
		}
		if output != "" {
			payload["output"] = output
		}
		payload["exit_code"] = exitCode
		out = append(out, payload)
		delete(pending, id)
	}
	return out
}

// claudeToolResultText flattens a tool_result content value — a plain string or
// an array of {type:text,text:...} blocks — into a single display string.
func claudeToolResultText(raw interface{}) string {
	if s, ok := raw.(string); ok {
		return s
	}
	items, ok := raw.([]interface{})
	if !ok {
		return ""
	}
	var b strings.Builder
	for _, item := range items {
		row, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		if remoteString(row, "type") == "text" {
			b.WriteString(remoteString(row, "text"))
		}
	}
	return b.String()
}

// claudeUsageEvent builds an ai.usage payload from a finalized assistant
// message's usage block, or returns nil when no token counts are present. The
// caller stamps session_id/message_id (run context is not available here).
func claudeUsageEvent(message map[string]interface{}) map[string]interface{} {
	usage := mapIf(message["usage"])
	if usage == nil {
		return nil
	}
	payload := map[string]interface{}{"type": models.AgentEventAIUsage}
	hit := false
	for field, key := range map[string]string{
		"input_tokens":      "input_tokens",
		"output_tokens":     "output_tokens",
		"cache_read_tokens": "cache_read_input_tokens",
	} {
		if v := remoteIntValue(usage[key]); v >= 0 {
			payload[field] = v
			hit = true
		}
	}
	if !hit {
		return nil
	}
	return payload
}

// emitClaudeStructuredEvents turns Claude stream-json events into structured
// activity events: thinking_delta→ai.thinking (streamed), the finalized
// assistant message's tool_use blocks→ai.command/ai.file_change/ai.task plus
// ai.usage, and a later user-turn tool_result closes a pending Bash command as
// ai.command(completed). Only write/exec tools surface; reads are ignored.
func emitClaudeStructuredEvents(format agentAIOutputFormat, event map[string]interface{}, run agentAIRun, writeJSON agentTerminalWriter, pending map[string]string) {
	if format != agentAIOutputClaudeStreamJSON {
		return
	}
	msgID := agentAssistantMessageID(run.messageID)

	if remoteString(event, "type") == "stream_event" {
		if streamEvent := mapIf(event["event"]); streamEvent != nil && remoteString(streamEvent, "type") == "content_block_delta" {
			if delta := mapIf(streamEvent["delta"]); delta != nil && remoteString(delta, "type") == "thinking_delta" {
				if text := remoteString(delta, "thinking"); strings.TrimSpace(text) != "" {
					_ = writeJSON(map[string]interface{}{
						"type":       models.AgentEventAIThinking,
						"session_id": run.sessionID,
						"message_id": msgID,
						"delta":      text,
					})
				}
			}
		}
	}

	switch remoteString(event, "type") {
	case "assistant":
		message := mapIf(event["message"])
		if message == nil {
			return
		}
		content, _ := message["content"].([]interface{})
		for _, ev := range claudeToolUseEvents(content, run, pending) {
			_ = writeJSON(ev)
		}
		if usage := claudeUsageEvent(message); usage != nil {
			usage["session_id"] = run.sessionID
			usage["message_id"] = msgID
			_ = writeJSON(usage)
		}
	case "user":
		message := mapIf(event["message"])
		if message == nil {
			return
		}
		content, _ := message["content"].([]interface{})
		for _, ev := range claudeToolResultEvents(content, run, pending) {
			_ = writeJSON(ev)
		}
	}
}

// emitOpenCodeStructuredEvents maps OpenCode's event-bus JSON into the same
// activity protocol used by Codex and Claude. OpenCode versions have moved
// fields between properties/data and the event root, so the mapper accepts all
// three layouts and only emits when a recognizable structured part exists.
func emitOpenCodeStructuredEvents(format agentAIOutputFormat, event map[string]interface{}, run agentAIRun, writeJSON agentTerminalWriter) {
	if format != agentAIOutputOpenCodeJSON {
		return
	}
	props := openCodeEventProperties(event)
	part := firstOpenCodeMap(props["part"], event["part"])
	msgID := agentAssistantMessageID(run.messageID)
	if part != nil {
		partType := strings.ToLower(strings.TrimSpace(remoteString(part, "type")))
		if partType == "reasoning" || partType == "thinking" {
			if delta := firstNonBlankPreserveSpace(remoteString(props, "delta"), remoteString(part, "delta"), remoteString(part, "text")); delta != "" {
				_ = writeJSON(map[string]interface{}{
					"type": models.AgentEventAIThinking, "session_id": run.sessionID,
					"message_id": msgID, "delta": delta,
				})
			}
		}
		if partType == "tool" || strings.Contains(partType, "command") || partType == "bash" {
			emitOpenCodeToolEvent(part, run, writeJSON)
		}
	}

	message := firstOpenCodeMap(props["message"], event["message"], props["info"], event["info"])
	if message == nil {
		return
	}
	usage := firstOpenCodeMap(message["tokens"], message["usage"])
	if usage == nil {
		return
	}
	payload := map[string]interface{}{
		"type": models.AgentEventAIUsage, "session_id": run.sessionID, "message_id": msgID,
	}
	hit := false
	for outputKey, inputKey := range map[string]string{
		"input_tokens": "input", "output_tokens": "output", "reasoning_tokens": "reasoning",
	} {
		if value := remoteIntValue(usage[inputKey]); value >= 0 {
			payload[outputKey] = value
			hit = true
		}
	}
	if cache := mapIf(usage["cache"]); cache != nil {
		if value := remoteIntValue(cache["read"]); value >= 0 {
			payload["cache_read_tokens"] = value
			hit = true
		}
	}
	if hit {
		_ = writeJSON(payload)
	}
}

func emitOpenCodeToolEvent(part map[string]interface{}, run agentAIRun, writeJSON agentTerminalWriter) {
	state := mapIf(part["state"])
	if state == nil {
		state = part
	}
	toolName := strings.ToLower(strings.TrimSpace(firstNonEmpty(remoteString(part, "tool"), remoteString(part, "name"))))
	itemID := firstNonEmpty(remoteString(part, "callID"), remoteString(part, "call_id"), remoteString(part, "id"))
	status := strings.ToLower(strings.TrimSpace(firstNonEmpty(remoteString(state, "status"), "started")))
	switch status {
	case "pending", "running":
		status = "started"
	case "success", "completed":
		status = "completed"
	case "failed", "error":
		status = "failed"
	}
	input := mapIf(state["input"])
	if input == nil {
		input = mapIf(part["input"])
	}
	if strings.Contains(toolName, "write") || strings.Contains(toolName, "edit") || strings.Contains(toolName, "patch") {
		path := firstNonEmpty(remoteString(input, "file_path"), remoteString(input, "path"))
		_ = writeJSON(map[string]interface{}{
			"type": models.AgentEventAIFileChange, "session_id": run.sessionID,
			"message_id": agentAssistantMessageID(run.messageID), "item_id": itemID,
			"path": path, "kind": "edit", "status": status,
		})
		return
	}
	if toolName == "bash" || toolName == "shell" || strings.Contains(toolName, "command") || strings.Contains(toolName, "terminal") {
		payload := map[string]interface{}{
			"type": models.AgentEventAICommand, "session_id": run.sessionID,
			"message_id": agentAssistantMessageID(run.messageID), "item_id": itemID,
			"status": status, "command": firstNonEmpty(remoteString(input, "command"), remoteString(input, "cmd")),
		}
		if output := firstNonEmpty(remoteString(state, "output"), remoteString(state, "error")); output != "" {
			payload["output"] = output
		}
		_ = writeJSON(payload)
	}
}

// countGitChanged returns the number of working-tree entries reported by
// `git -C dir status --porcelain` (modified/added/deleted/untracked). Returns 0
// for a non-git directory or any git error. Cheap enough to run on a ~10s tick.
func countGitChanged(dir string) int {
	out, err := newBackgroundCommand("git", "-C", dir, "status", "--porcelain").Output()
	if err != nil {
		return 0
	}
	trimmed := strings.TrimSpace(string(out))
	if trimmed == "" {
		return 0
	}
	return strings.Count(trimmed, "\n") + 1
}

// countGitTrackedFiles returns the number of tracked files reported by
// `git -C dir ls-files` (committed + staged). A fast, stable proxy for "project
// file count" used by the mobile dashboard's Files metric; returns 0 for a
// non-git directory or any git error. Run on the ~1/min inventory tick.
func countGitTrackedFiles(dir string) int {
	out, err := newBackgroundCommand("git", "-C", dir, "ls-files").Output()
	if err != nil {
		return 0
	}
	trimmed := strings.TrimSpace(string(out))
	if trimmed == "" {
		return 0
	}
	return strings.Count(trimmed, "\n") + 1
}

func extractCodexJSONTexts(event map[string]interface{}, allowFinal bool) []string {
	eventType := remoteString(event, "type")
	if strings.Contains(eventType, "delta") {
		if text := firstNonEmpty(remoteString(event, "delta"), remoteString(event, "text")); text != "" {
			return []string{text}
		}
		if item, ok := event["item"].(map[string]interface{}); ok && remoteString(item, "type") == "agent_message" {
			if text := firstNonEmpty(remoteString(item, "delta"), remoteString(item, "text")); text != "" {
				return []string{text}
			}
		}
	}
	if !allowFinal {
		return nil
	}
	if item, ok := event["item"].(map[string]interface{}); ok && remoteString(item, "type") == "agent_message" {
		if text := remoteString(item, "text"); text != "" {
			return []string{text}
		}
	}
	if eventType == "agent_message" {
		if text := remoteString(event, "text"); text != "" {
			return []string{text}
		}
	}
	return nil
}

func streamAIDelta(reader io.Reader, run agentAIRun, channel string, writeJSON agentTerminalWriter, limiter *agentAIOutputLimiter, capture func(string)) {
	buf := make([]byte, 4096)
	for {
		n, err := reader.Read(buf)
		if n > 0 {
			allowed := n
			if limiter != nil {
				allowed = limiter.Reserve(n)
			}
			if allowed <= 0 {
				if run.cancel != nil {
					run.cancel()
				}
				return
			}
			text := string(buf[:allowed])
			emitAIDelta(text, run, channel, writeJSON, nil, capture)
			if allowed < n {
				if run.cancel != nil {
					run.cancel()
				}
				return
			}
		}
		if err != nil {
			return
		}
	}
}

// captureAgentAIStderr (defined above runCLIPass) is used for AI runs so a
// stale --resume session ("No conversation found with session ID") can be
// detected and the run retried fresh.
func emitAIDelta(text string, run agentAIRun, channel string, writeJSON agentTerminalWriter, limiter *agentAIOutputLimiter, capture func(string)) bool {
	run.activity.bump()
	if text == "" {
		return true
	}
	if limiter != nil {
		allowed := limiter.Reserve(len(text))
		if allowed <= 0 {
			if run.cancel != nil {
				run.cancel()
			}
			return false
		}
		if allowed < len(text) {
			text = text[:allowed]
			if run.cancel != nil {
				run.cancel()
			}
		}
	}
	if capture != nil {
		capture(text)
	}
	_ = writeJSON(map[string]interface{}{
		"type":       models.AgentEventAIDelta,
		"session_id": run.sessionID,
		"message_id": agentAssistantMessageID(run.messageID),
		"channel":    channel,
		"delta":      text,
	})
	if limiter != nil && limiter.Exceeded() {
		return false
	}
	return true
}

func buildAgentAIPrompt(session *agentAISession, latestContent string) string {
	history := trimAgentAIHistory(append([]agentAIMessage(nil), session.history...))
	var builder strings.Builder
	builder.WriteString("You are continuing an Aliang remote agent AI chat session.\n")
	builder.WriteString("Use the existing conversation as context and answer the latest user message.\n")
	builder.WriteString("Project path: ")
	builder.WriteString(session.projectPath)
	builder.WriteString("\nMode: ")
	builder.WriteString(session.mode)
	builder.WriteString("\n\nConversation:\n")
	for _, item := range history {
		builder.WriteString(agentAIRoleLabel(item.Role))
		builder.WriteString(": ")
		builder.WriteString(strings.TrimSpace(item.Content))
		builder.WriteString("\n\n")
	}
	if len(history) == 0 {
		builder.WriteString("User: ")
		builder.WriteString(strings.TrimSpace(latestContent))
		builder.WriteString("\n\n")
	}
	builder.WriteString("Assistant:")
	return builder.String()
}

func agentAIRoleLabel(role string) string {
	role = strings.ToLower(strings.TrimSpace(role))
	switch role {
	case "user":
		return "User"
	case "assistant":
		return "Assistant"
	case "system":
		return "System"
	case "":
		return "Message"
	default:
		return strings.ToUpper(role[:1]) + role[1:]
	}
}

func trimAgentAIHistory(history []agentAIMessage) []agentAIMessage {
	const maxMessages = 16
	const maxChars = 64000
	if len(history) > maxMessages {
		history = history[len(history)-maxMessages:]
	}
	total := 0
	start := len(history)
	for i := len(history) - 1; i >= 0; i-- {
		total += len(history[i].Content)
		if total > maxChars {
			break
		}
		start = i
	}
	if start > 0 && start < len(history) {
		history = history[start:]
	}
	return history
}

func resolveAgentAITool(prompt string, preferred string, model string, effort string, resumeSessionID string, newSessionID ...string) (*agentAITool, error) {
	reservedID := ""
	if len(newSessionID) > 0 {
		reservedID = strings.TrimSpace(newSessionID[0])
	}
	preferred, err := normalizeAgentAIProvider(preferred)
	if err != nil {
		return nil, err
	}
	if preferred != "auto" {
		if err := validateApprovalCapableProvider(preferred, codexAppServerAvailable()); err != nil {
			return nil, err
		}
		return resolveNamedAgentAITool(preferred, prompt, model, effort, resumeSessionID, reservedID)
	}
	candidates := []string{"claude", "claudecode"}
	if codexAppServerAvailable() {
		candidates = append([]string{"codex"}, candidates...)
	}
	for _, candidate := range candidates {
		if tool, err := resolveNamedAgentAITool(candidate, prompt, model, effort, resumeSessionID, reservedID); err == nil {
			return tool, nil
		}
	}
	return nil, errors.New("no approval-capable AI CLI found in PATH: Codex app-server, Claude, or Claude Code")
}

func resolveGoalAgentAITool(prompt string, preferred string, model string, effort string, resumeSessionID string, newSessionID ...string) (*agentAITool, error) {
	preferred, err := normalizeAgentAIProvider(preferred)
	if err != nil {
		return nil, err
	}
	if preferred == "auto" {
		return nil, errors.New("Goal execution requires an explicit AI provider")
	}
	return resolveNamedAgentAITool(preferred, prompt, model, effort, resumeSessionID, newSessionID...)
}

func validateApprovalCapableProvider(provider string, hasCodexAppServer bool) error {
	switch provider {
	case "codex":
		if !hasCodexAppServer {
			return errors.New("Codex app-server is required for remote approval support; update Codex before using it from Aliang")
		}
	case "opencode":
		return errors.New("OpenCode is unavailable for remote execution because it does not provide an Aliang approval bridge")
	}
	return nil
}

func resolveNamedAgentAITool(name string, prompt string, model string, effort string, resumeSessionID string, newSessionID ...string) (*agentAITool, error) {
	reservedID := ""
	if len(newSessionID) > 0 {
		reservedID = strings.TrimSpace(newSessionID[0])
	}
	model = normalizeAgentAIModel(model)
	effort = strings.TrimSpace(effort)
	resumeSessionID = strings.TrimSpace(resumeSessionID)
	switch name {
	case "codex":
		if path, err := lookPathCLI("codex"); err == nil {
			// codex/OpenAI path: reasoning effort is conveyed as a `<base>-<effort>`
			// model-name suffix — the downstream gateway derives reasoning_effort
			// from it (gpt-5.4-xhigh → reasoning_effort=xhigh). Applied AFTER model
			// normalization so the normalizer doesn't strip it. Skipped when no base
			// model is set (nothing to suffix) or the model already carries a suffix.
			codexModel := applyCodexEffortSuffix(model, effort)
			args := []string{"exec"}
			if resumeSessionID != "" {
				args = append(args, "resume")
			}
			args = append(args, "--json", "--skip-git-repo-check")
			if codexModel != "" {
				args = append(args, "--model", codexModel)
			}
			if resumeSessionID != "" {
				args = append(args, resumeSessionID)
			}
			args = append(args, prompt)
			return &agentAITool{
				id:           "codex",
				path:         path,
				args:         args,
				outputFormat: agentAIOutputCodexJSON,
			}, nil
		}
	case "claude":
		if path, err := lookPathCLI("claude"); err == nil {
			return newClaudeCodeAITool("claude", path, prompt, model, effort, resumeSessionID, reservedID), nil
		}
	case "claudecode":
		if path, err := lookPathCLI("claudecode"); err == nil {
			return newClaudeCodeAITool("claudecode", path, prompt, model, effort, resumeSessionID, reservedID), nil
		}
		if path, err := lookPathCLI("claude"); err == nil {
			return newClaudeCodeAITool("claudecode", path, prompt, model, effort, resumeSessionID, reservedID), nil
		}
	case "opencode":
		if path, err := lookPathCLI("opencode"); err == nil {
			return newOpenCodeAITool(path, prompt, model, effort, resumeSessionID), nil
		}
	default:
		return nil, fmt.Errorf("unsupported AI provider: %s", name)
	}
	return nil, fmt.Errorf("AI CLI %q was not found in PATH", name)
}

// applyCodexEffortSuffix appends the reasoning effort as a model-name suffix for
// the codex/OpenAI path: the downstream gateway derives reasoning_effort from a
// `<base>-<effort>` model name (e.g. gpt-5.4-xhigh → reasoning_effort=xhigh).
// Skipped when model is empty (no base to suffix), effort is empty, or the model
// already carries a recognized effort suffix (avoids gpt-5.4-xhigh-xhigh).
//
// Claude Code path does NOT use a model-name suffix: the gateway does not parse
// a suffixed claude model name and the upstream call would break. Claude effort
// is instead conveyed via the CLI's `--effort <level>` flag (see
// newClaudeCodeAITool), supported since Claude Code 2.1.x. The two providers
// therefore carry effort through DIFFERENT channels: codex via model-name
// suffix, claude via --effort. Both read the same session.effort field.
func applyCodexEffortSuffix(model, effort string) string {
	model = strings.TrimSpace(model)
	effort = strings.TrimSpace(effort)
	if model == "" || effort == "" {
		return model
	}
	if endsWithKnownAgentEffortSuffix(model) {
		return model
	}
	return model + "-" + effort
}

// endsWithKnownAgentEffortSuffix reports whether the model name already ends with
// a reasoning-effort suffix (none/minimal/low/medium/high/xhigh/extrahigh/max),
// so we don't double-append.
func endsWithKnownAgentEffortSuffix(model string) bool {
	lower := strings.ToLower(strings.TrimSpace(model))
	for _, suffix := range []string{"-none", "-minimal", "-low", "-medium", "-high", "-xhigh", "-extrahigh", "-max"} {
		if strings.HasSuffix(lower, suffix) {
			return true
		}
	}
	return false
}

func newClaudeCodeAITool(id string, path string, prompt string, model string, effort string, resumeSessionID string, newSessionID ...string) *agentAITool {
	args := claudeCodeHeadlessSlimArgs()
	args = append(args, "--print", "--verbose", "--output-format", "stream-json", "--include-partial-messages")
	args = append(args, "--append-system-prompt", agentAIOptionSystemPrompt)
	if resumeSessionID != "" {
		args = append(args, "--resume", resumeSessionID)
	} else if len(newSessionID) > 0 && strings.TrimSpace(newSessionID[0]) != "" {
		args = append(args, "--session-id", strings.TrimSpace(newSessionID[0]))
	}
	if model != "" {
		args = append(args, "--model", model)
	}
	if effort = strings.TrimSpace(effort); effort != "" {
		args = append(args, "--effort", effort)
	}
	args = append(args, prompt)
	return &agentAITool{
		id:           id,
		path:         path,
		args:         args,
		outputFormat: agentAIOutputClaudeStreamJSON,
	}
}

func newOpenCodeAITool(path string, prompt string, model string, effort string, resumeSessionID string) *agentAITool {
	args := []string{"run", "--format", "json"}
	if resumeSessionID != "" {
		args = append(args, "--session", resumeSessionID)
	}
	if model != "" {
		args = append(args, "--model", model)
	}
	if effort = strings.TrimSpace(effort); effort != "" {
		args = append(args, "--variant", effort)
	}
	args = append(args, prompt)
	return &agentAITool{
		id:           "opencode",
		path:         path,
		args:         args,
		outputFormat: agentAIOutputOpenCodeJSON,
	}
}

func withAgentAIAttachments(tool *agentAITool, attachments []agentAIAttachment) *agentAITool {
	if tool == nil || len(attachments) == 0 || len(tool.args) == 0 {
		return tool
	}
	copied := *tool
	copied.args = append([]string(nil), tool.args...)
	promptIndex := len(copied.args) - 1
	prompt := copied.args[promptIndex]
	extraArgs := make([]string, 0, len(attachments)*2)
	attachedPaths := make(map[string]struct{})
	for _, attachment := range attachments {
		switch tool.outputFormat {
		case agentAIOutputCodexJSON:
			if attachment.Type == "image" && attachment.Path != "" {
				extraArgs = append(extraArgs, "--image", attachment.Path)
				attachedPaths[attachment.Path] = struct{}{}
			}
		case agentAIOutputOpenCodeJSON:
			if attachment.Path != "" {
				extraArgs = append(extraArgs, "--file", attachment.Path)
				attachedPaths[attachment.Path] = struct{}{}
			}
		}
	}
	remaining := make([]agentAIAttachment, 0, len(attachments))
	for _, attachment := range attachments {
		if _, attached := attachedPaths[attachment.Path]; !attached || attachment.URL != "" {
			remaining = append(remaining, attachment)
		}
	}
	prompt += agentAIAttachmentPromptSuffix(remaining, true)
	copied.args = append(copied.args[:promptIndex], append(extraArgs, prompt)...)
	return &copied
}

func agentAIAttachmentPromptSuffix(attachments []agentAIAttachment, includeImages bool) string {
	if len(attachments) == 0 {
		return ""
	}
	var lines []string
	for _, attachment := range attachments {
		if !includeImages && attachment.Type == "image" {
			continue
		}
		location := firstNonEmpty(attachment.Path, attachment.URL)
		if location == "" {
			continue
		}
		name := firstNonEmpty(attachment.Name, filepath.Base(attachment.Path), "attachment")
		lines = append(lines, fmt.Sprintf("- %s: %s", name, location))
	}
	if len(lines) == 0 {
		return ""
	}
	return "\n\nAttachments available for this request:\n" + strings.Join(lines, "\n")
}

const (
	claudeCodeHeadlessDefaultTools = "Bash,Edit,Glob,Grep,Read,Write"
	claudeCodeHeadlessEmptyMCP     = `{"mcpServers":{}}`
)

func claudeCodeHeadlessSlimArgs() []string {
	if !claudeCodeHeadlessSlimEnabled() {
		return nil
	}
	tools := strings.TrimSpace(os.Getenv("ALIANG_CLAUDE_HEADLESS_TOOLS"))
	if tools == "" {
		tools = claudeCodeHeadlessDefaultTools
	}
	args := []string{"--tools", tools}
	if truthyEnv("ALIANG_CLAUDE_HEADLESS_ENABLE_MCP") {
		return args
	}
	return append(args, "--strict-mcp-config", "--mcp-config", claudeCodeHeadlessEmptyMCP)
}

// claudeCodeHeadlessSlimEnabled reports whether headless "slim" mode is enabled.
//
// 永久停用: slim 会用 --tools 把 built-in tools 砍到 6 个 + 用
// --strict-mcp-config --mcp-config {} 清空 MCP, 导致 headless 启动的 tools
// 集合远少于交互式 TUI(实测 6 vs 24+), 被下游网关判定为"非标准 Claude
// Code"(openclaw)。已验证停用后 tools 恢复到完整 built-in 集。故无视
// ALIANG_CLAUDE_HEADLESS_SLIM, 一律不启用。若将来确需精简模式, 恢复下方
// 读 env 的 switch 即可。
func claudeCodeHeadlessSlimEnabled() bool {
	return false
}

func truthyEnv(key string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "1", "true", "yes", "on", "enable", "enabled":
		return true
	default:
		return false
	}
}

type claudeCodeVersion struct {
	major int
	minor int
	patch int
}

func (v claudeCodeVersion) atLeast(major, minor, patch int) bool {
	if v.major != major {
		return v.major > major
	}
	if v.minor != minor {
		return v.minor > minor
	}
	return v.patch >= patch
}

func parseClaudeCodeVersion(raw string) (claudeCodeVersion, bool) {
	matches := claudeCodeVersionRe.FindStringSubmatch(raw)
	if len(matches) != 4 {
		return claudeCodeVersion{}, false
	}
	major, errMajor := strconv.Atoi(matches[1])
	minor, errMinor := strconv.Atoi(matches[2])
	patch, errPatch := strconv.Atoi(matches[3])
	if errMajor != nil || errMinor != nil || errPatch != nil {
		return claudeCodeVersion{}, false
	}
	return claudeCodeVersion{major: major, minor: minor, patch: patch}, true
}

func claudeApprovalHookStrategyForVersion(raw string) claudeApprovalHookStrategy {
	version, ok := parseClaudeCodeVersion(raw)
	// Claude Code 2.1.x (verified with 2.1.17) does not invoke PermissionRequest
	// for --print Bash permission denials, but it does invoke PreToolUse command
	// hooks. Newer builds follow the current PermissionRequest/http contract.
	if ok && version.atLeast(2, 2, 0) {
		return claudeApprovalHookPermissionRequestHTTP
	}
	return claudeApprovalHookPreToolUseCommand
}

func claudeApprovalHookStrategyOverride(raw string) (claudeApprovalHookStrategy, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "pretool", "pretooluse", "pretool_command", "pre_tool_use_command", "legacy":
		return claudeApprovalHookPreToolUseCommand, true
	case "permissionrequest", "permission_request", "permission_request_http", "http", "modern":
		return claudeApprovalHookPermissionRequestHTTP, true
	default:
		return "", false
	}
}

func detectClaudeApprovalHookStrategy(toolPath string) claudeApprovalHookStrategy {
	if strategy, ok := claudeApprovalHookStrategyOverride(os.Getenv("ALIANG_CLAUDE_APPROVAL_HOOK")); ok {
		return strategy
	}
	toolPath = strings.TrimSpace(toolPath)
	if toolPath == "" {
		return claudeApprovalHookPreToolUseCommand
	}
	cacheKey := executableProbeCacheKey(toolPath)
	if cached, ok := claudeApprovalHookCache.Load(cacheKey); ok {
		if strategy, ok := cached.(claudeApprovalHookStrategy); ok {
			return strategy
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := newBackgroundCommandContext(ctx, toolPath, "--version").CombinedOutput()
	strategy := claudeApprovalHookPreToolUseCommand
	if err == nil {
		strategy = claudeApprovalHookStrategyForVersion(string(out))
	}
	claudeApprovalHookCache.Store(cacheKey, strategy)
	return strategy
}

const claudeApprovalHookTimeoutGrace = 30 * time.Second

func claudeApprovalHookTimeoutSeconds(approvalTimeout time.Duration) int64 {
	if approvalTimeout <= 0 {
		approvalTimeout = time.Second
	}
	seconds := int64(approvalTimeout / time.Second)
	if approvalTimeout%time.Second != 0 {
		seconds++
	}
	return seconds + int64(claudeApprovalHookTimeoutGrace/time.Second)
}

func claudeApprovalHookSettings(strategy claudeApprovalHookStrategy, run agentAIRun) (map[string]interface{}, error) {
	hookURL := agentAIApprovalHookURL(run.sessionID, agentAssistantMessageID(run.messageID), run.approvalToken)
	hookTimeout := claudeApprovalHookTimeoutSeconds(agentAIApprovalTimeout)
	var settings map[string]interface{}
	switch strategy {
	case claudeApprovalHookPermissionRequestHTTP:
		settings = map[string]interface{}{
			"hooks": map[string]interface{}{
				"PermissionRequest": []interface{}{
					map[string]interface{}{
						"matcher": "*",
						"hooks": []interface{}{
							map[string]interface{}{
								"type":    "http",
								"url":     hookURL,
								"timeout": hookTimeout,
							},
						},
					},
				},
			},
		}
	default:
		settings = map[string]interface{}{
			"hooks": map[string]interface{}{
				"PreToolUse": []interface{}{
					map[string]interface{}{
						"matcher": "*",
						"hooks": []interface{}{
							map[string]interface{}{
								"type":    "command",
								"command": claudeApprovalHookCurlCommand(hookURL),
								"timeout": hookTimeout,
							},
						},
					},
				},
			},
		}
	}
	if run.claudePolicy.enabled {
		settings["disableSkillShellExecution"] = true
		// Claude Code 2.1.x never invokes PermissionRequest hooks in headless
		// --print mode. Its explicit ask rules also take precedence over an
		// allowing PreToolUse result, so combining the two makes tools such as
		// Edit fail even after our policy hook auto-approves them. Legacy runs
		// enforce the same policy entirely in PreToolUse; modern runs keep the
		// fail-closed ask layer and resolve it through PermissionRequest.
		if strategy == claudeApprovalHookPermissionRequestHTTP {
			settings["permissions"] = map[string]interface{}{
				"ask": append([]string(nil), run.claudePolicy.permissionAsk...),
			}
		}
	}
	return settings, nil
}

func claudeApprovalHookCurlCommand(hookURL string) string {
	return fmt.Sprintf("curl -sS -X POST -H %q --data-binary @- %q", "Content-Type: application/json", hookURL)
}

func withClaudeApprovalHook(tool *agentAITool, run agentAIRun) *agentAITool {
	if tool == nil {
		logger.Warn(fmt.Sprintf("approval-hook: withClaudeApprovalHook skipped (nil tool) session=%s", run.sessionID))
		return tool
	}
	if strings.TrimSpace(run.approvalToken) == "" {
		logger.Warn(fmt.Sprintf("approval-hook: withClaudeApprovalHook SKIPPED (empty approvalToken) session=%s runSeq=%d — claude runs with NO permission hook, commands needing approval auto-deny", run.sessionID, run.runSeq))
		return tool
	}
	strategy := detectClaudeApprovalHookStrategy(tool.path)
	logger.Info(fmt.Sprintf("approval-hook: wiring Claude hook strategy=%s session=%s runSeq=%d", strategy, run.sessionID, run.runSeq))
	settings, err := claudeApprovalHookSettings(strategy, run)
	if err != nil {
		return tool
	}
	settingsRaw, err := json.Marshal(settings)
	if err != nil {
		return tool
	}
	copied := *tool
	copied.args = withoutCLIArgumentValue(tool.args, "--setting-sources")
	copied.args = append([]string{"--setting-sources", "", "--permission-mode", "default", "--settings", string(settingsRaw)}, copied.args...)
	return &copied
}

func withoutCLIArgumentValue(args []string, flag string) []string {
	out := make([]string, 0, len(args))
	for index := 0; index < len(args); index++ {
		if args[index] == flag {
			if index+1 < len(args) {
				index++
			}
			continue
		}
		out = append(out, args[index])
	}
	return out
}

func normalizeAgentAIModel(model string) string {
	model = strings.TrimSpace(model)
	switch strings.ToLower(model) {
	case "", "auto", "codex", "claude", "claudecode", "opencode", "open-code", "open_code":
		return ""
	default:
		return model
	}
}

func agentAICapabilities() []string {
	_, hasClaude := lookPathCLI("claude")
	_, hasClaudeCode := lookPathCLI("claudecode")
	_, hasOpenCode := lookPathCLI("opencode")
	return agentAICapabilitiesForTools(hasClaude == nil, hasClaudeCode == nil, codexAppServerAvailable(), hasOpenCode == nil)
}

func agentAICapabilitiesForTools(hasClaude, hasClaudeCode, hasCodexAppServer, hasOpenCode bool) []string {
	caps := []string{"ai_chat", "ai_chat_context", "ai_stream", "ai_approval", "ai_steer", "vibe_session"}
	if hasClaude {
		caps = append(caps, "ai_provider_claude")
	}
	if hasClaudeCode || hasClaude {
		caps = append(caps, "ai_provider_claudecode")
	}
	if hasCodexAppServer {
		caps = append(caps, "ai_provider_codex", "ai_provider_codex_app_server")
	}
	if hasOpenCode {
		caps = append(caps, "ai_provider_opencode_basic")
	}
	return caps
}

func agentAIStringSliceContains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func resolveAgentAICWD(raw string) (string, error) {
	return resolveAgentAuthorizedCWD(raw, "project path")
}

// claudeRetryInfo captures the most-recent Claude stream-json
// {"type":"system","subtype":"api_retry", attempt, max_retries, retry_delay_ms,
// error_status, error} event for a run. It is threaded out of the stdout
// streaming goroutine so the run loop can (a) surface retries live as
// ai.run.progress and (b) enrich the terminal ai.error with a structured cause
// (error_status/error_type + retry counts) instead of a bare "exit status 1".
type claudeRetryInfo struct {
	has         bool
	attempt     int
	max         int
	delayMs     float64
	errorStatus int // upstream HTTP status that triggered the retry (e.g. 502)
	errorType   string
}

// claudeFailureCause builds a concise, phone-friendly cause string for a failed
// Claude turn. When retry info is present it describes the upstream error and
// how many retries were spent (e.g. "gateway 502 (server_error); retried
// 10/10"); otherwise it falls back to the CLI exit error. Verbose diagnostics
// (stderr, the raw result text) travel separately in the ai.error "detail"
// field, which the phone does not render by default.
func claudeFailureCause(waitErr error, ri claudeRetryInfo) string {
	if ri.has && ri.errorStatus > 0 {
		s := fmt.Sprintf("gateway %d", ri.errorStatus)
		if ri.errorType != "" {
			s = fmt.Sprintf("%s (%s)", s, ri.errorType)
		}
		if ri.max > 0 {
			s = fmt.Sprintf("%s; retried %d/%d", s, ri.attempt, ri.max)
		}
		return s
	}
	if waitErr != nil {
		return fmt.Sprintf("Claude CLI exited: %v", waitErr)
	}
	return "claude run failed"
}

// agentAIErrorPayloadWithRetry is agentAIErrorPayload + structured retry/error
// fields so the phone can render "会话失败 · 网关 502 (重试 10/10)" instead of a
// bare exit message. Fields attach only when retry info is available, so
// non-retry failures (and codex, which emits no api_retry) are unaffected.
func agentAIErrorPayloadWithRetry(sessionID string, messageID string, err error, ri claudeRetryInfo) map[string]interface{} {
	payload := agentAIErrorPayload(sessionID, messageID, err)
	if !ri.has {
		return payload
	}
	if ri.errorStatus > 0 {
		payload["error_status"] = ri.errorStatus
	}
	if ri.errorType != "" {
		payload["error_type"] = ri.errorType
	}
	if ri.attempt > 0 {
		payload["retry_attempt"] = ri.attempt
	}
	if ri.max > 0 {
		payload["retry_max"] = ri.max
	}
	return payload
}

func agentAIErrorPayload(sessionID string, messageID string, err error) map[string]interface{} {
	message := "ai error"
	if err != nil {
		message = err.Error()
	}
	payload := map[string]interface{}{
		"type":       models.AgentEventAIError,
		"session_id": sessionID,
		"error":      message,
	}
	if messageID != "" {
		payload["message_id"] = agentAssistantMessageID(messageID)
	}
	return payload
}

func agentAIApprovalRequestPayload(req agentAIApprovalRequest) map[string]interface{} {
	payload := map[string]interface{}{
		"type":        models.AgentEventAIApprovalRequest,
		"session_id":  req.SessionID,
		"message_id":  req.MessageID,
		"approval_id": req.ID,
		"provider":    req.Provider,
		"kind":        req.Kind,
		"status":      "pending",
	}
	if req.Title != "" {
		payload["title"] = req.Title
	}
	if req.Reason != "" {
		payload["reason"] = req.Reason
	}
	if req.Command != "" {
		payload["command"] = req.Command
	}
	if req.CWD != "" {
		payload["cwd"] = req.CWD
	}
	if req.ToolName != "" {
		payload["tool_name"] = req.ToolName
	}
	if len(req.ToolInput) > 0 {
		payload["tool_input"] = json.RawMessage(req.ToolInput)
	}
	if len(req.FileChanges) > 0 {
		payload["file_changes"] = json.RawMessage(req.FileChanges)
	}
	if len(req.AvailableDecisions) > 0 {
		payload["available_decisions"] = req.AvailableDecisions
	}
	if len(req.Raw) > 0 {
		payload["raw"] = json.RawMessage(req.Raw)
	}
	if req.MatchedRuleID != "" {
		payload["matched_rule_id"] = req.MatchedRuleID
	}
	if req.PolicyVersion > 0 {
		payload["policy_version"] = req.PolicyVersion
	}
	return payload
}

func normalizeAgentAIApprovalDecision(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "accept", "accepted", "approve", "approved", "allow", "allowed", "yes":
		return models.AgentAIApprovalDecisionAccept
	case "accept_for_session", "acceptforsession", "approved_for_session", "approve_for_session", "allow_for_session":
		return models.AgentAIApprovalDecisionAcceptForSession
	case "decline", "declined", "deny", "denied", "reject", "rejected", "no":
		return models.AgentAIApprovalDecisionDecline
	case "cancel", "abort", "timed_out", "timeout":
		return models.AgentAIApprovalDecisionCancel
	default:
		return ""
	}
}

func normalizeAgentAIApprovalScope(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "session":
		return "session"
	default:
		return "turn"
	}
}

func newAgentAIApprovalID(sessionID string, runSeq int) string {
	token, err := newAgentAIApprovalToken()
	if err != nil {
		return fmt.Sprintf("%s-%d-%d", strings.TrimSpace(sessionID), runSeq, time.Now().UnixNano())
	}
	return token
}

func newAgentAIApprovalToken() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate approval token: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

func marshalAgentAIRaw(raw interface{}) json.RawMessage {
	if raw == nil {
		return nil
	}
	if msg, ok := raw.(json.RawMessage); ok {
		return msg
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	return encoded
}

func agentAssistantMessageID(messageID string) string {
	messageID = strings.TrimSpace(messageID)
	if strings.HasPrefix(messageID, "assistant_") {
		return messageID
	}
	return "assistant_" + messageID
}
