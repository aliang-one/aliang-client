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
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/common/logger"
	"aliang.one/nursorgate/internal/tccguard"
	"aliang.one/nursorgate/processor/config"
	"github.com/modelcontextprotocol/go-sdk/mcp"
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
	// coreJournal 是 agentcore（AliangCode）调用记录落盘器；dir 为空时内部
	// 全 no-op。构造时解析一次 agentHome()。
	coreJournal *agentCoreJournal
	// —— 跨端会话统一（2026-10-28）——
	// aiSockets 是出站 socket 注册表；aiSocketSeq 发号。originSocket 不在
	// manager：随消息 __origin_socket 字段走，落到 session.originSocket（后续任务）。
	aiSockets   map[string]*agentAISocket
	aiSocketSeq int
	// coreMCPDialForTest 允许测试注入内存 MCP 会话（忽略 raw 配置直接返回
	// session）；nil = 用真实 agentCoreMCPDial。仅测试可写。
	coreMCPDialForTest func(ctx context.Context, raw agentCoreMCPRawServer) (*mcp.ClientSession, func(), error)
	// toolAsk 自有 MCP 交互工具桥(2026-10-16 方案):one-shot --print 下承载
	// 手机阻塞交互,懒初始化见 ensureToolAskBridge。
	toolAsk *agentToolAskBridge
	// service is the owning AgentService (set when the service creates this
	// manager), used to evaluate the device approval policy. nil for standalone
	// test managers, which fall back to GetSharedAgentService().
	service *AgentService
	// scanOnce 保证 stale 扫描器每个 manager 只启动一条。
	scanOnce sync.Once
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
			if title == "" && strings.EqualFold(msg.Role, "user") && !isJunkAgentTitle(msg.Content) {
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
	return out
}

// agentAIToolDiagnosticArgs 渲染 ai.run.cli 日志的 argv 诊断串。prompt 走
// stdin 时 args 末位是普通 flag 值，不得把它伪装成 prompt——改为在尾部追加
// 一个 stdin prompt 长度标注；prompt 在 argv 时保持原掩码语义。
func agentAIToolDiagnosticArgs(tool *agentAITool) []string {
	if tool == nil {
		return nil
	}
	out := agentAIDiagnosticArgs(tool.args)
	if tool.stdinPrompt != "" {
		return append(out, fmt.Sprintf("<prompt:%d chars via stdin>", len(tool.stdinPrompt)))
	}
	if len(out) > 0 && len(tool.args) > 0 {
		out[len(out)-1] = fmt.Sprintf("<prompt:%d chars>", len(tool.args[len(tool.args)-1]))
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
	// Windows 上合并注册表最新用户 PATH：agent 快照 PATH 缺后装 CLI 的目录，
	// npm 的 claude.cmd shim 还要靠 PATH 解析 node——不合并则面板探测判定可用
	// 而 spawn 必败（"node 不是内部或外部命令"）。非 Windows 原样返回。
	base := augmentEnvWithWindowsUserPath(os.Environ())
	// macOS/Linux：注入登录 shell PATH 快照，让 AI 子进程看到用户终端同款
	// 工具链（node/npm 等）。Windows 探针恒失败，走上面的注册表合并通道。
	base = augmentEnvWithLoginShellPath(base)
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

func agentChildProcessEnvValue(key string) (string, bool) {
	for _, entry := range agentChildProcessEnv() {
		if index := strings.IndexByte(entry, '='); index >= 0 && entry[:index] == key {
			return entry[index+1:], true
		}
	}
	return "", false
}

// agentAIContextWindowMin/Max bound the auto-compact window the phone may set.
// The claude CLI accepts 100k–1M for `--autocompact`, and the server enforces
// the same range (AliangPhoneServer server/src/modelConfig.ts), so an
// out-of-range value can only come from a stale/misbehaving client. Clamp
// instead of failing the turn.
const (
	agentAIContextWindowMin     = 100_000
	agentAIContextWindowMax     = 1_000_000
	agentAIContextWindowDefault = 200_000
)

// clampAgentAIContextWindow normalizes a client-supplied context window.
// 0/negative means "unset" and is preserved so callers can tell "older server
// sent nothing" (CLI default) apart from an explicit choice.
func clampAgentAIContextWindow(v int) int {
	if v <= 0 {
		return 0
	}
	if v < agentAIContextWindowMin {
		return agentAIContextWindowMin
	}
	if v > agentAIContextWindowMax {
		return agentAIContextWindowMax
	}
	return v
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
	effort string
	// contextWindow is the model context size (tokens) chosen on the phone.
	// For claude it is passed to the CLI as `--autocompact <tokens>` so the
	// auto-compaction trigger matches the real window instead of whatever the
	// model name implies; for agentcore it drives the kernel's own compaction
	// threshold. 0 = unset (older server) → CLI/kernel defaults.
	contextWindow           int
	resumeSessionID         string
	reservedNativeSessionID string
	bindingVersion          int
	initialContext          string
	// isGoalFork marks a fork (re-planning exploration) session. #5: its runs are
	// forced read-only (run.readOnly) so exploration can't mutate the workspace —
	// real provider-level enforcement, not just a prompt.
	isGoalFork    bool
	cancel        context.CancelFunc
	activeWriter  agentTerminalWriter
	approvalToken string
	activity      *agentAIActivity
	// guard 是本回合的收尾守卫（kill 观测 + 宽限强拆）。clearRunning 清空。
	guard *agentAIRunGuard
	// finalizedRunSeq 是「该 runSeq 已被强拆收尾」的显式标记（0 = 无）。
	// 非帧副作用守卫靠它识别僵尸迟返回；不用 cancel == nil 判别，是因为
	// 单测/TUI 路径注册的会话本来就没有 cancel，会把合法回合一起挡掉
	// （会打红 6 个既有用例）。按 runSeq 记账，下一回合 runSeq 推进后自然失效，
	// 不需要在起跑路径手工清零。
	finalizedRunSeq     int
	runSeq              int
	activeRunID         string
	goalIdentity        map[string]interface{}
	goalAllowedRoots    []string
	goalAllowedCommands []string
	// lastActiveAt drives LRU eviction (evictOldestIdleAISessionLocked): the
	// non-running session with the oldest stamp is dropped when a new session
	// would exceed agentAISessionResidentCap. Stamped on create and bumped on
	// every inbound ai.message so actively-used conversations survive.
	lastActiveAt  time.Time
	history       []agentAIMessage
	pendingOption *agentAIOptionRequest // run 结束检测到方案块时置位，等 ai.option.response
	codexSteer    *agentAICodexSteerControl
	// claudeSteer 是 Claude headless（--input-format stream-json）run 的
	// steer 通道；与 codexSteer 二选一，同一条 run 上不会同时存在。
	claudeSteer *agentAIClaudeSteerControl
	// coreSteer 是内置内核（provider=="agentcore"）run 的 steer 通道。内核
	// 没有 stdin 可写，插话只能排队、由下一轮 LLM 调用前取走，所以它与
	// claudeSteer 的 deliver 语义不同（永远 queued，drain 时才 applied）。
	coreSteer *agentAICoreSteerControl
	// tuiTurn 是当前 TUI 注入回合的路由计划（UDS socket / transcript 路径 /
	// 权限类）。TUI 回合没有 stdin 可写（claudeSteer 恒 nil），UDS 注入是唯一
	// 插话通道——CC 把回合中收到的注入按「排队消息」处理，当前回合结束后自动
	// 接续执行，正是 steer 的语义。runTUITurn 置位、clearTUITurnSessionState
	// 清除；steer() 据此分流（2026-10-24 前 TUI 回合的 steer 一律 unsupported，
	// 内容被静默丢弃）。
	tuiTurn      *tuiTurnPlan
	claudePolicy agentAIClaudeRemotePolicy
	claudeCaps   agentAIClaudeCapabilities
	// surface 是本会话最近一个手机回合的驱动面标记（"phone" 或 ""）。
	// 只增不降：ai.session.create 等旧通道不带该字段时不清掉已置的 phone。
	surface string
	// originSocket 是本会话最近一个 run 的发起 socket 注册表 id（"remote" 或
	// "aisock-N"）。由入站消息的 __origin_socket 标记落（message/create/runStart），
	// 构造 run 时带进 run.originSocketID 供 fan-out 去重与审批 ack 双写（Task 7）。
	originSocket string
}

type agentAISteerMessage struct {
	MessageID string
	Content   string
	CreatedAt time.Time
	// ImageBlocks 是随插话一起发过去的图片（已 base64 编码成 provider block）。
	// 刻意在 steer 入口就编码完：steer 可能排队到 control.started 之后才写出，
	// 若携带临时文件路径，排队期间清理就会把文件删掉。
	ImageBlocks []map[string]interface{}
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
	sessionID string
	runID     string
	messageID string
	// surface 是本回合的驱动面标记（agentAISurfacePhone 或 ""），来自会话
	// 状态；claudeApprovalHookSettings 据此决定是否硬禁终端交互构件。
	surface string
	// providerMsgID is the provider's native assistant message id for the
	// CURRENT turn (claude msg_*, captured from message_start / finalized
	// assistant events). Streaming events (ai.delta / ai.thinking /
	// ai.command / ai.file_change) carry it as message_id so each assistant
	// turn becomes a distinct transcript message + distinct structured events
	// — matching what the server rebuilds from the native session log on
	// refresh. Empty when not yet captured; streamingMessageID falls back to
	// the run-derived id.
	providerMsgID string
	runSeq        int
	mode          string
	projectPath   string
	provider      string
	model         string
	effort        string
	// contextWindow mirrors the session field for this run (claude:
	// --autocompact tokens; agentcore: kernel compaction budget).
	contextWindow           int
	resumeSessionID         string
	reservedNativeSessionID string
	bindingVersion          int
	prompt                  string
	freshPrompt             string
	attachments             []agentAIAttachment
	// 服务端下发的附件下载白名单（SSRF 围栏），空则回落到环境变量。
	attachmentAllowedHosts []string
	cancel                 context.CancelFunc
	approvalToken          string
	activity               *agentAIActivity
	// guard 是本回合的收尾守卫指针，也是跨 /clear 唯一可靠的回合身份：
	// /clear 把 runSeq 归零后会被新回合复用，只比 runSeq 无法区分「同一个
	// runSeq 的不同回合」。未挂载（TUI / 单测路径）时为 nil。
	guard *agentAIRunGuard
	// subagents 跟踪本 run 的子 agent 生命周期（claude task_* 帧 / 兜底登记 /
	// TUI 桥的 sidechain 目录 watch）。nil 安全（codex 路径不建）。
	subagents    *agentAISubagentRegistry
	claudePolicy agentAIClaudeRemotePolicy
	onClaudeInit func([]string, string)
	// Policy context for an escalated approval: which rule triggered it and the
	// policy version that decided. Set by the approval hooks before escalation.
	matchedRuleID string
	policyVersion int
	goalIdentity  map[string]interface{}
	// goalRequiredCheckCount is the number of required verification checks on the
	// current task, parsed from goal_context. Drives whether a missing marker
	// may be optimistically treated as task_completed (only safe with checks).
	goalRequiredCheckCount int
	// goalAllowedRoots/Commands 来自 goal_context.task，approval hook 围栏校验用。
	goalAllowedRoots    []string
	goalAllowedCommands []string
	nativeGoal          map[string]interface{}
	readOnly            bool
	// claudeSteer 是本次 run 的 Claude steer 通道（仅 stream-json 输入模式）。
	// 放在 run 上而不是加参数：流循环与 run 同生命周期，且避免改动既有
	// streamAgentAIStdout/streamStructuredAIDelta 的签名与全部测试调用点。
	claudeSteer *agentAIClaudeSteerControl
	// originSocketID 是本回合发起 socket 的注册表 id（"remote" 或 "aisock-N"）。
	// 由消息分发路径盖的 __origin_socket 标记经 message()/runStart() 落入
	//（Task 5 起，由 message 的 messageRun/runStart 保留绑定错误发射器/
	// optionResponse 续接/runUserMessage 执行 run 四路填充；create() 只落
	// session.originSocket，从不构造 run）；空串=旧调用方（TUI/测试内部
	// 路径），fan-out 无 origin 去重。
	originSocketID string
}

// agentAIAttachment 是一条随消息下发的素材。线上是 JSON，但解析走 remoteString
// 手工取值（历史原因，结构体没有 json tag），所以加字段必须同时改
// resolveAgentAIAttachments，光加字段不会生效。
//
// Type: "image" | "text"。text 用于手绘/ASCII 这类纯文本素材——它们不需要下载、
// 不占视觉 token，直接内联进 prompt 即可。
type agentAIAttachment struct {
	Type string
	Name string
	Path string
	URL  string
	Text string // 仅 Type=="text"
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
	// goalRequiredCheckCount mirrors run.goalRequiredCheckCount; read by
	// attachGoalReport to decide whether optimistic completion is safe.
	goalRequiredCheckCount int
	// goalAllowedRoots/Commands mirrors run.*; approval hook 读。
	goalAllowedRoots    []string
	goalAllowedCommands []string
}

func newAgentAIRunEmitter(run agentAIRun, write agentTerminalWriter) *agentAIRunEmitter {
	runID := strings.TrimSpace(run.runID)
	if runID == "" {
		// Compatibility for older servers: message ids are unique per user turn,
		// so they are a stable fallback run identity until the server sends run_id.
		runID = run.messageID
	}
	return &agentAIRunEmitter{
		runID:                  runID,
		write:                  write,
		goalIdentity:           cloneGoalIdentity(run.goalIdentity),
		goalRequiredCheckCount: run.goalRequiredCheckCount,
		goalAllowedRoots:       run.goalAllowedRoots,
		goalAllowedCommands:    run.goalAllowedCommands,
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
	if payload["type"] == models.AgentEventAIError {
		e.appendGoalOutput(remoteString(payload, "error"))
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
	// stdinPrompt 非空时经子进程 stdin 传递而非 argv：Windows 批处理 shim
	// (.cmd/.bat) 的命令行经 cmd.exe 解析，引号翻转重切分、%VAR% 展开、& | ^
	// 元字符——prompt 是不可信自由文本，绝不走 argv（见 claudePromptViaStdinForOS）。
	stdinPrompt string
	// stdinStreamJSON 表示该 run 走 `--input-format stream-json`：prompt 与
	// 后续 steer 都以 JSON 行写入一条常驻 stdin 管道，进程在 turn 之间保持
	// 存活。为 true 时 stdinPrompt 仍保存原始 prompt 文本（供附件/只读等
	// 包装器拼接），真正的管道由 runCLIPass 建立并交给 steer 通道。
	stdinStreamJSON bool
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
	permissionAsk           []string
	// trustTier is the effective trust tier ("isolated"|"sanitized"|"full").
	// It supersedes projectSkillTrusted/projectCapabilityMode when the server
	// sends trust_level; legacy fields map onto it for old servers.
	trustTier string
	// projectMCPTrusted opts the project's .mcp.json into the sanitized-tier
	// explicit MCP merge (spec §5).
	projectMCPTrusted bool
	// policyNotice carries a structured downgrade/fallback reason surfaced on
	// ai.run.started as policy_notice. Nil when the tier applied as requested.
	policyNotice map[string]interface{}
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
	// Tier resolution (2026-10-16 默认档 sanitized——产品语义「settings.json
	// 优先,平台 env 兜底」的最严实现):
	// 1) valid trust_level wins(服务端显式下发仍可 isolated/full);
	// 2) present-but-invalid trust_level → isolated (fail-closed, 服务端明
	//    显发了无法识别的值属配置事故);
	// 3) no trust_level(含全部 legacy 字段形态) → sanitized:加载 user
	//    scope(用户 settings.json 的 provider/skills/ask 规则生效,与 TUI
	//    一致),但**不加载 project scope**——项目 .claude/settings.json 的
	//    hooks/env/allow 在 -p 模式零审批执行且可被远程克隆的仓库投毒
	//    (R1,实验复现),user scope 的同名内容是设备主人自己的文件,无此面;
	// 4) absent claude_remote_policy → 机制关闭:withClaudeApprovalHook 保
	//    持历史行为 --setting-sources "",settings 全不加载(旧服务端设备的
	//    provider 仍只能走平台 env 或 claudeAuthEnvOverlay 兜底)。
	if level := strings.TrimSpace(remoteString(raw, "trust_level")); level != "" {
		if tier, valid := normalizeClaudeTrustTier(level); valid {
			policy.trustTier = tier
		} else {
			policy.trustTier = "isolated"
		}
	} else {
		policy.trustTier = "sanitized"
	}
	policy.projectMCPTrusted = remoteBool(raw, "project_mcp_trusted", false)
	// The tier table owns --setting-sources (see claudeTierSettingSources);
	// the message-level setting_sources field is legacy and dropped.
	policy.settingSources = nil
	// settings.disableSkillShellExecution 为 write-only 遗留死字段:agent 侧
	// 从未消费(写入端恒 true),服务端继续下发以兼容旧版本,此处不再解析。
	if settings, ok := raw["settings"].(map[string]interface{}); ok {
		if permissions, ok := settings["permissions"].(map[string]interface{}); ok {
			policy.permissionAsk = remoteStringSlice(permissions, "ask")
		}
	}
	// Full tier must not add ask rules: blanket ask outranks allow and would
	// suppress the user's own allow rules (= local approval volume, spec §3).
	if len(policy.permissionAsk) == 0 && policy.trustTier != "full" {
		policy.permissionAsk = []string{"Bash", "Edit", "Write", "NotebookEdit", "mcp__*"}
	}
	return policy
}

func cloneAgentAIClaudeRemotePolicy(policy agentAIClaudeRemotePolicy) agentAIClaudeRemotePolicy {
	policy.settingSources = append([]string(nil), policy.settingSources...)
	policy.permissionAsk = append([]string(nil), policy.permissionAsk...)
	return policy
}

type claudeCodeVersionProbe struct {
	raw string
	ok  bool
}

var claudeCodeVersionProbeCache sync.Map

// probeClaudeRawVersion returns the raw `claude --version` output for the
// executable, cached per executable content. ok=false when the probe fails
// (missing or non-claude binary) — failures are NOT cached (retried each
// pass, bounded by the 2s timeout); successes are cached until the binary
// content changes. Both consumers (hook strategy + trust-tier guard) parse
// their own derivatives from this single source.
func probeClaudeRawVersion(toolPath string) (string, bool) {
	toolPath = strings.TrimSpace(toolPath)
	if toolPath == "" {
		return "", false
	}
	cacheKey := executableProbeCacheKey(toolPath)
	if cached, ok := claudeCodeVersionProbeCache.Load(cacheKey); ok {
		probe, _ := cached.(claudeCodeVersionProbe)
		return probe.raw, probe.ok
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := newBackgroundCommandContext(ctx, toolPath, "--version").CombinedOutput()
	if err != nil {
		return "", false
	}
	claudeCodeVersionProbeCache.Store(cacheKey, claudeCodeVersionProbe{raw: string(out), ok: true})
	return string(out), true
}

// probeClaudeCodeVersion returns the parsed version of the claude executable,
// cached per executable content on success only (via probeClaudeRawVersion).
// ok=false when the probe fails or the raw output is unparsable — callers
// treat that as fail-closed.
func probeClaudeCodeVersion(toolPath string) (claudeCodeVersion, bool) {
	raw, ok := probeClaudeRawVersion(toolPath)
	if !ok {
		return claudeCodeVersion{}, false
	}
	version, ok := parseClaudeCodeVersion(raw)
	if !ok {
		return claudeCodeVersion{}, false
	}
	return version, true
}

// applyClaudeTierVersionGuard downgrades non-isolated tiers to isolated when
// the installed claude predates 2.1.268: those builds never fire
// PermissionRequest hooks in headless --print mode (fixed upstream in
// 2.1.268, CHANGELOG), and user ask rules suppress PreToolUse approvals, so
// loading user/project settings there would break the approval bridge
// (spec §3 guard). The downgrade is tier-wide: sources back to "", no plugin.
// Note: a full-tier policy downgraded here keeps its (intentionally empty)
// ask list — the <2.1.268 PreToolUse strategy never injects permissions.ask, so
// behavior matches legacy isolated runs without refilling the default list.
func applyClaudeTierVersionGuard(tool *agentAITool, policy agentAIClaudeRemotePolicy, sessionID string) agentAIClaudeRemotePolicy {
	if !policy.enabled || policy.trustTier == "" || policy.trustTier == "isolated" {
		return policy
	}
	version, ok := probeClaudeCodeVersion(tool.path)
	if ok && version.atLeast(claudeTierMinMajor, claudeTierMinMinor, claudeTierMinPatch) {
		if !version.atLeast(claudeKnownGoodMajor, claudeKnownGoodMinor, claudeKnownGoodPatch) {
			logger.Warn(fmt.Sprintf("claude-policy: %s is in the 2.1.268-2.1.288 band with known approval-bypass defects fixed in 2.1.289; upgrading claude is recommended session=%s", tool.path, sessionID))
		}
		return policy
	}
	logger.Warn(fmt.Sprintf("claude-policy: %s predates the tier minimum 2.1.268, downgrading trust tier %s to isolated session=%s", tool.path, policy.trustTier, sessionID))
	downgraded := policy
	downgraded.trustTier = "isolated"
	// reason 字面量为历史契约(实际阈值已是 2.1.268),服务端存储透传不改名。
	downgraded.policyNotice = map[string]interface{}{
		"effective": "isolated",
		"requested": policy.trustTier,
		"reason":    "claude_version_below_2_2",
	}
	return downgraded
}

// applyClaudePolicyNotice folds a degrade notice back into the effective
// policy: a degraded run IS an isolated run, so the tier must match the
// notice — otherwise the approval hook would re-open the sources the
// degrade just closed (spec §4.1 fail-closed).
func applyClaudePolicyNotice(policy agentAIClaudeRemotePolicy, notice map[string]interface{}) agentAIClaudeRemotePolicy {
	if notice == nil {
		return policy
	}
	policy.policyNotice = notice
	// notice.effective 是权威终态：既有降级路径为 isolated（sanitize/mcp 失败），
	// 平台 env 缺失回退路径为 sanitized（见 withClaudeRemotePolicy）。缺省保持
	// 旧行为（按 isolated 处理），approval hook 会按折叠后的 tier 重写 sources。
	if tier, valid := normalizeClaudeTrustTier(remoteString(notice, "effective")); valid {
		policy.trustTier = tier
	} else {
		policy.trustTier = "isolated"
	}
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
		coreJournal:        newAgentCoreJournal(agentCoreJournalDir()),
		aiSockets:          make(map[string]*agentAISocket),
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
			// One line per run terminal. Its absence is how the 2026-10-21
			// incident stayed invisible: the run died with no trace of whether a
			// terminal was ever produced, journaled, or merely failed to send.
			logger.Info(fmt.Sprintf("[ai.run.terminal] status=%s type=%s session=%s run=%s seq=%v",
				remoteString(payload, "status"), remoteString(payload, "type"), run.sessionID, run.runID, payload["event_seq"]))
			if err := m.rememberPendingTerminal(payload); err != nil {
				return err
			}
			return m.completeProcessedRun(run.runID, payload)
		}
	}
	// 跨端同态（spec §4.1）：origin 仍内联，事件副本扇出到订阅 socket。
	// originSocketID 从消息分发路径盖的 __origin_socket 来（Task 5 起
	// messageRun/保留绑定错误发射器/optionResponse/runUserMessage 四路
	// 填充）；恒空串=无 origin 去重，扇出与 origin 内联并存（仅测试/内部
	// 路径）。
	e.write = m.fanOutWrite(run.originSocketID, run.sessionID, e.write)
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
	lastActivityAt atomic.Int64
	// lastProgressAt tracks MEANINGFUL progress (assistant text/thinking via
	// emitAIDelta, file changes via fileSink, a completed Codex work item) —
	// NOT raw output. A degenerate model loop (e.g. filler `echo` tool calls
	// every few seconds for hours) keeps lastActivityAt fresh, so only the
	// progress clock can distinguish "alive" from "getting anywhere".
	lastProgressAt atomic.Int64
	// pendingApprovals 是并发的审批请求数（0=无人在等）。用计数而不是 bool，
	// 是因为一个回合可能有多个审批重叠：A、B 同时在等时 A 先结束，B 仍需要
	// 保住审批钟与 idlePaused——bool 会被 A 的 false 提前清掉，随后
	// pendingToolUses>0 又把工具钟臂起，合法的人工等待会被工具天花板误杀。
	pendingApprovals atomic.Int64
	pendingToolUses  atomic.Int64
	// approvalWaitStartedAt 是当前这段「等人工决策」的开始时刻（0=不在等）。
	// 计数版下它不保证与 toolWaitStartedAt 互斥：第一个审批开启时清工具钟，
	// 但期间若工具重新臂起（或第二个重叠审批不清工具钟），两钟可同时非零；
	// 这不误判，因为 staleDeathReason 的 awaiting 分支优先于工具钟分支。
	// 审批等待由 agentAIApprovalTimeout 单独约束，stale 扫描器也要能读到它。
	approvalWaitStartedAt atomic.Int64
	// toolWaitStartedAt is the UnixNano instant the current CONTIGUOUS
	// tool/subagent wait began (0 = not waiting). The tool-wait watchdog uses
	// it to bound hung tools/subagents that would otherwise pin the run
	// indefinitely (the hard ceiling no longer counts wait ticks) while the
	// progress heartbeat keeps telling the server the run is alive. Cleared
	// while a human approval is pending so approval time never counts against
	// the ceiling (approvals are bounded separately by
	// agentAIApprovalTimeout).
	toolWaitStartedAt atomic.Int64
	// activeSubagents counts in-flight subagent records (task_started …
	// terminal frame / teardown). While >0 the idle and no-progress guards are
	// paused (idlePaused) and agentAISubagentWaitCeiling bounds the contiguous
	// stretch instead — subagent work is legitimately silent on the parent's
	// stdout (sync mode: no frames between launch and completion).
	activeSubagents       atomic.Int64
	subagentWaitStartedAt atomic.Int64
	runStart              time.Time
	killReasonMu          sync.RWMutex
	killReason            string
}

func newAgentAIActivity() *agentAIActivity {
	a := &agentAIActivity{runStart: time.Now()}
	a.lastActivityAt.Store(a.runStart.UnixNano())
	a.lastProgressAt.Store(a.runStart.UnixNano())
	return a
}

func (a *agentAIActivity) bump() {
	if a == nil {
		return
	}
	a.lastActivityAt.Store(time.Now().UnixNano())
}

// markProgress records meaningful progress and counts as activity. Callers:
// emitAIDelta (assistant text/thinking), the run's fileSink (file written or
// edited), and the Codex item/completed handler (one work unit finished).
func (a *agentAIActivity) markProgress() {
	if a == nil {
		return
	}
	now := time.Now().UnixNano()
	a.lastActivityAt.Store(now)
	a.lastProgressAt.Store(now)
}

// progressIdleFor is how long the run has produced NO meaningful progress.
// Unlike idleFor it is NOT reset by raw output — a run can be loudly alive
// while going nowhere.
func (a *agentAIActivity) progressIdleFor() time.Duration {
	if a == nil {
		return 0
	}
	last := a.lastProgressAt.Load()
	if last == 0 {
		return 0
	}
	return time.Since(time.Unix(0, last))
}

// setAwaitingApproval 登记/解除一个审批等待，并 bump 活动时间，让审批结束时
// 拿到一个新的 idle 窗口。
//
// 用计数而非 bool：一个回合可能同时有多个审批（A、B 重叠）。只有第一个审批
// 才起审批钟/清工具钟（后续重叠审批不能把人工等待上限往后推）；只有最后一个
// 审批结束才清钟，并在工具仍在飞时重新臂起工具钟。
func (a *agentAIActivity) setAwaitingApproval(v bool) {
	if a == nil {
		return
	}
	if v {
		if a.pendingApprovals.Add(1) == 1 {
			// Approval time is human time, not tool time: pause the tool-wait
			// clock so a slow decision never trips the tool-wait ceiling.
			a.approvalWaitStartedAt.Store(time.Now().UnixNano())
			a.toolWaitStartedAt.Store(0)
		}
	} else {
		// CAS 递减且不允许减到负数：多余的 false（重复解除）在 0 处直接 break。
		for {
			current := a.pendingApprovals.Load()
			if current <= 0 {
				// 多余 false 本身无害，但计数失衡（例如漏了 true 却发 false）
				// 会让 awaiting() 长期为真，把该 run 拖到 24h 审批天花板，
				// 所以要留痕。agentAIActivity 无会话身份，日志不带 id。
				logger.Warn("[ai.activity] approval_release_without_pending")
				break
			}
			if a.pendingApprovals.CompareAndSwap(current, current-1) {
				if current-1 == 0 {
					a.approvalWaitStartedAt.Store(0)
					if a.pendingToolUses.Load() > 0 {
						// Decision resolved but the tool is still in flight:
						// re-arm with a fresh ceiling window for the remaining
						// execution.
						a.toolWaitStartedAt.Store(time.Now().UnixNano())
					}
				}
				break
			}
		}
	}
	a.bump()
}

// approvalWaitFor 报告当前这段审批等待已持续多久，0 = 不在等待。
func (a *agentAIActivity) approvalWaitFor() time.Duration {
	if a == nil {
		return 0
	}
	ts := a.approvalWaitStartedAt.Load()
	if ts == 0 {
		return 0
	}
	return time.Since(time.Unix(0, ts))
}

func (a *agentAIActivity) awaiting() bool {
	if a == nil {
		return false
	}
	return a.pendingApprovals.Load() > 0
}

// toolWaitFor reports how long the current contiguous tool/subagent wait has
// been in flight, excluding approval-pending time. 0 when not waiting.
func (a *agentAIActivity) toolWaitFor() time.Duration {
	if a == nil {
		return 0
	}
	ts := a.toolWaitStartedAt.Load()
	if ts == 0 {
		return 0
	}
	return time.Since(time.Unix(0, ts))
}

// beginToolUseWait marks that Claude has emitted a tool_use (Task/subagent,
// Bash, etc.) and the run is legitimately silent until a matching tool_result
// arrives. It bumps activity so the run gets a fresh idle window once the tool
// completes.
func (a *agentAIActivity) beginToolUseWait() {
	if a == nil {
		return
	}
	if a.pendingToolUses.Add(1) == 1 {
		a.toolWaitStartedAt.Store(time.Now().UnixNano())
	}
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
			if current-1 == 0 {
				a.toolWaitStartedAt.Store(0)
			}
			break
		}
	}
	a.bump()
}

func (a *agentAIActivity) idlePaused() bool {
	if a == nil {
		return false
	}
	return a.awaiting() || a.pendingToolUses.Load() > 0 || a.activeSubagents.Load() > 0
}

// staleDeathReason 判定这个 run 是否已经「无响应死」，返回原因码与结论。
//
// 口径（spec §3.3.1/§3.3.2）：
//   - 审批等待态：只服 approvalTimeout，超过即判死。工具/子代理天花板在这里
//     不参与评估——进入审批时工具钟已被清零（审批是人的时间，不是工具时间），
//     所以「审批 + 子代理同时活跃」的收敛上界是 approvalTimeout。
//   - 工具/子代理等待态（非审批）：不按 stale 阈值判死，只服各自天花板——
//     toolCeiling、subagentCeiling。
//   - 非等待态：lastActivityAt 距今超过 staleThreshold → stalled。activity 只由
//     真实事件帧/审批帧/steer ack 刷新，ai.run.progress 心跳不参与（否则心跳
//     会把一切判死判据击穿）。
func (a *agentAIActivity) staleDeathReason(staleThreshold, approvalTimeout, toolCeiling, subagentCeiling time.Duration) (string, bool) {
	if a == nil {
		return "", false
	}
	if a.awaiting() {
		if approvalTimeout > 0 && a.approvalWaitFor() > approvalTimeout {
			return "approval_timeout", true
		}
		return "", false
	}
	if a.pendingToolUses.Load() > 0 || a.activeSubagents.Load() > 0 {
		if toolCeiling > 0 && a.toolWaitFor() > toolCeiling {
			return "tool_wait_timeout", true
		}
		if subagentCeiling > 0 && a.subagentWaitFor() > subagentCeiling {
			return "subagent_wait_timeout", true
		}
		return "", false
	}
	if staleThreshold > 0 && a.idleFor() > staleThreshold {
		return "stalled", true
	}
	return "", false
}

// beginSubagent marks that a subagent record went active (task_started). While
// the count is >0 the idle/no-progress guards are paused and the subagent-wait
// ceiling clock is armed.
func (a *agentAIActivity) beginSubagent() {
	if a == nil {
		return
	}
	if a.activeSubagents.Add(1) == 1 {
		a.subagentWaitStartedAt.Store(time.Now().UnixNano())
	}
	a.bump()
}

// endSubagent marks a subagent record terminal. The wait clock clears when the
// last active record closes.
func (a *agentAIActivity) endSubagent() {
	if a == nil {
		return
	}
	for {
		current := a.activeSubagents.Load()
		if current <= 0 {
			break
		}
		if a.activeSubagents.CompareAndSwap(current, current-1) {
			if current-1 == 0 {
				a.subagentWaitStartedAt.Store(0)
			}
			break
		}
	}
	a.bump()
}

// subagentWaitFor reports how long >=1 subagent has been continuously active.
func (a *agentAIActivity) subagentWaitFor() time.Duration {
	if a == nil {
		return 0
	}
	ts := a.subagentWaitStartedAt.Load()
	if ts == 0 {
		return 0
	}
	return time.Since(time.Unix(0, ts))
}

// subagentProgress feeds every watchdog clock from a task_progress frame: raw
// activity (idle guard), meaningful progress (no-progress guard), and — the
// mis-kill fix — a RESET of the contiguous tool-wait clock while a tool wait is
// pending. On the sync phone path the parent Agent tool_result only arrives at
// completion, so pendingToolUses stays armed for the whole subagent run and
// without this reset a >1h subagent dies as "suspected hung tool" despite
// emitting regular progress.
func (a *agentAIActivity) subagentProgress() {
	if a == nil {
		return
	}
	now := time.Now().UnixNano()
	a.lastActivityAt.Store(now)
	a.lastProgressAt.Store(now)
	if a.pendingToolUses.Load() > 0 {
		a.toolWaitStartedAt.Store(now)
	}
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
// agentAIIdleWindow and not awaiting approval/tool results), produces no
// meaningful progress for agentAINoProgressWindow, a contiguous tool/subagent
// wait outlives agentAIToolWaitCeiling, or its engine-active time exceeds
// agentAIHardCeiling. It exits as soon as ctx is done, so it never outlives
// the run.
func (m *agentAIManager) startAIWatchdog(ctx context.Context, activity *agentAIActivity, cancel context.CancelFunc) {
	if activity == nil || cancel == nil {
		return
	}
	go agentAIWatchdogLoop(ctx, activity, m.wrapKillCancel(activity, cancel), agentAIIdleWindow, agentAIHardCeiling, agentAINoProgressWindow, agentAIToolWaitCeiling, agentAISubagentWaitCeiling, agentAIIdleCheckInterval)
}

// agentAIWatchdogLoop is the parameterized watchdog body, split out so tests can
// drive it with tiny windows instead of the multi-minute production defaults.
func agentAIWatchdogLoop(ctx context.Context, activity *agentAIActivity, cancel context.CancelFunc, idleWindow, hardCeiling, noProgressWindow, toolWaitCeiling, subagentCeiling, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	// 引擎活跃时钟：逐拍累加，但处于任一合法等待态（审批/工具/子代理）的拍
	// 不计入。口径见 spec §3.2 第 4 条——等待态各自有独立天花板，硬顶只管真实
	// 干活时长，否则 3h 的子代理批处理会被 2h 硬顶误杀。按 ticker 粒度采样，
	// 误差以一拍为界（生产 agentAIIdleCheckInterval 为 30s）。
	lastTick := time.Now()
	var engineActive time.Duration
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			delta := now.Sub(lastTick)
			lastTick = now
			if !activity.idlePaused() {
				engineActive += delta
			}
			if !activity.idlePaused() && activity.idleFor() > idleWindow {
				activity.setKillReason("idle_timeout")
				cancel()
				return
			}
			// No-progress kill: the run keeps emitting output (so the idle
			// watchdog above never fires) but has produced no assistant text,
			// no file changes, and no completed work item for the whole window
			// — the observed degenerate-loop failure mode. Skipped while a
			// tool/approval wait is pending: a single long-lived Task/subagent
			// or human decision is work in flight, not looping.
			if noProgressWindow > 0 && !activity.idlePaused() && activity.progressIdleFor() > noProgressWindow {
				activity.setKillReason("no_progress")
				cancel()
				return
			}
			// Tool-wait ceiling: a contiguous tool/subagent wait (approval
			// time excluded — the clock is cleared while a human decision is
			// pending) that outlives the ceiling means the tool or subagent
			// is hung. Every other guard is paused during such a wait by
			// design, and the ai.run.progress heartbeat keeps feeding the
			// server-side stale-run sweeper, so without this bound a hung
			// subagent pins the run indefinitely while the phone
			// shows "running". The awaiting() re-check hardens against a
			// theoretical stale-armed clock (a scheduler preemption inside
			// beginToolUseWait's Add→Store window landing exactly as an
			// approval hook fires): approval waits are bounded separately by
			// agentAIApprovalTimeout, so skipping the kill while a human
			// decision is pending is always safe.
			if toolWaitCeiling > 0 && !activity.awaiting() && activity.toolWaitFor() > toolWaitCeiling {
				activity.setKillReason("tool_wait_timeout")
				cancel()
				return
			}
			// Subagent-wait ceiling: a contiguous stretch with >=1 active
			// subagent that outlives this bound means runaway subagent work.
			// task_progress resets the tool-wait clock above (progress = not
			// hung) but never this one, so an endlessly-progressing subagent is
			// still bounded. Old CC (no task_* frames) leaves the count at zero
			// and this guard stays inert.
			if subagentCeiling > 0 && activity.subagentWaitFor() > subagentCeiling {
				activity.setKillReason("subagent_wait_timeout")
				cancel()
				return
			}
			if hardCeiling > 0 && engineActive > hardCeiling {
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
	case "no_progress":
		// Same server-mapped status as idle_timeout (→ timed_out / error) so
		// the backend treats the run as terminally dead; only the message
		// differs, naming the real cause.
		return "idle_timeout", fmt.Sprintf("AI run produced no meaningful progress (no text or file changes) for %s and was stopped", agentAINoProgressWindow)
	case "tool_wait_timeout":
		return "timeout", fmt.Sprintf("AI run tool/subagent wait exceeded %s without a tool result (suspected hung tool) and was stopped", agentAIToolWaitCeiling)
	case "subagent_wait_timeout":
		return "timeout", fmt.Sprintf("AI run subagent work exceeded %s without completing (suspected runaway subagent) and was stopped", agentAISubagentWaitCeiling)
	case "hard_ceiling":
		return "hard_timeout", fmt.Sprintf("AI run exceeded the maximum engine-active runtime %s (approval, tool and subagent waits are excluded)", agentAIHardCeiling)
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
	contextWindow := clampAgentAIContextWindow(remoteInt(msg, "context_window", 0))
	resumeSessionID := firstNonEmpty(remoteString(msg, "resume_session_id"), remoteString(msg, "source_session_id"))
	reservedNativeSessionID := strings.TrimSpace(remoteString(msg, "reserved_native_session_id"))
	bindingVersion := remoteInt(msg, "binding_version", 0)
	initialContext := strings.TrimSpace(remoteString(msg, "initial_context"))
	isGoalFork, _ := msg["is_goal_fork"].(bool) // #5: server flags fork sessions
	surface := normalizeAgentAISurface(remoteString(msg, "surface"))
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
		if contextWindow > 0 {
			existing.contextWindow = contextWindow
		}
		existing.resumeSessionID = resumeSessionID
		existing.reservedNativeSessionID = reservedNativeSessionID
		existing.bindingVersion = bindingVersion
		existing.initialContext = initialContext
		existing.isGoalFork = isGoalFork
		if surface != "" {
			existing.surface = surface
		}
		if claudePolicy.enabled {
			existing.claudePolicy = cloneAgentAIClaudeRemotePolicy(claudePolicy)
			existing.claudeCaps = agentAIClaudeCapabilities{}
		}
		if len(history) > 0 {
			existing.history = trimAgentAIHistoryForSession(existing, history)
		}
		if v := strings.TrimSpace(remoteString(msg, agentAIOriginSocketField)); v != "" {
			existing.originSocket = v
		}
		m.mu.Unlock()
		_ = writeJSON(agentAISessionCreatedPayload(existing))
		return
	}
	// NOTE: no count cap here. The real turn runs on ai.message, whose lazy-create
	// path (see message()) registers the session unconditionally — so a cap only
	// in create() was a dead check that emitted a misleading "limit reached"
	// error while the conversation still ran. Bound via eviction, not a gate.
	newHistoryMessages, newHistoryChars := agentAIHistoryLimits(provider, contextWindow)
	session := &agentAISession{
		id:                      sessionID,
		mode:                    mode,
		projectPath:             projectPath,
		provider:                provider,
		model:                   model,
		effort:                  effort,
		contextWindow:           contextWindow,
		resumeSessionID:         resumeSessionID,
		reservedNativeSessionID: reservedNativeSessionID,
		bindingVersion:          bindingVersion,
		initialContext:          initialContext,
		isGoalFork:              isGoalFork,
		surface:                 surface,
		originSocket:            strings.TrimSpace(remoteString(msg, agentAIOriginSocketField)),
		claudePolicy:            cloneAgentAIClaudeRemotePolicy(claudePolicy),
		history:                 trimAgentAIHistoryWithLimits(history, newHistoryMessages, newHistoryChars),
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
	// CRITICAL: inject structured goal_context into the prompt so the AI knows
	// the task details, required checks, file requirements, etc. Without this,
	// Claude only sees the task title in `content` and has no idea what files
	// to create or what verification will check — causing repeated failures.
	if gc := serializeGoalContext(msg["goal_context"]); gc != "" {
		content = gc + "\n\n" + content
	}
	if sessionID == "" {
		_ = writeJSON(agentAIErrorPayload("", messageID, errors.New("ai.message missing session_id")))
		return
	}
	if messageID == "" {
		messageID = sessionID
	}
	// #5: a fork (re-planning exploration) session runs READ-ONLY so the AI can't
	// mutate the workspace during exploration. Read the flag from the payload
	// here (the session object is resolved only later); the atomic run.start path
	// carries is_goal_fork, and lazy-create persists it for subsequent turns.
	messageIsGoalFork, _ := msg["is_goal_fork"].(bool)
	messageRun := agentAIRun{
		sessionID: sessionID,
		runID:     remoteString(msg, "run_id"),
		messageID: messageID,
		// origin 必须在本条消息自带标记上取：emitter 在 session 解析（含
		// lazy-create）之前就要建好，读 session.originSocket 只能拿到上一回合
		// 的旧值——换端发起的回合会被误判成旧端 origin，去重失效变成双投。
		originSocketID:         strings.TrimSpace(remoteString(msg, agentAIOriginSocketField)),
		goalIdentity:           goalRunIdentityFromMessage(msg),
		goalRequiredCheckCount: goalRequiredCheckCountFromContext(msg["goal_context"]),
		goalAllowedRoots:       goalAllowedRootsFromContext(msg["goal_context"]),
		goalAllowedCommands:    goalAllowedCommandsFromContext(msg["goal_context"]),
		nativeGoal:             cloneAgentAIMap(mapIf(msg["native_goal"])),
		readOnly:               messageIsGoalFork,
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

	if err := m.preemptForGoalRun(
		sessionID,
		messageRun.runID,
		messageRun.goalIdentity,
		10*time.Second,
	); err != nil {
		_ = runWrite(agentAIErrorPayload(sessionID, messageID, err))
		return
	}

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
		// P0-1 root cause: mirror create()'s initial_context handling. ai.run.start
		// (atomic v3) carries initial_context (the fork snapshot / goal context),
		// but the v1 lazy-create dropped it → a fork session born via run.start
		// never saw the goal snapshot / ALIANG_GOAL_REPLAN_DELTA contract. The
		// server's proactive ai.session.create only covers the online happy path;
		// persisting it here is the robust fix (offline / reconnect / lost create
		// all still converge here on first run.start → message lazy-create).
		lazyInitialContext := strings.TrimSpace(remoteString(msg, "initial_context"))
		lazyIsGoalFork, _ := msg["is_goal_fork"].(bool) // #5: ai.run.start lazy-create
		lazyHistory := remoteAgentAIHistory(msg)
		if lazyInitialContext != "" {
			lazyHistory = append([]agentAIMessage{{
				Role:      "system",
				MessageID: "initial_context",
				Content:   lazyInitialContext,
				CreatedAt: time.Now().UTC(),
			}}, lazyHistory...)
		}
		lazyContextWindow := clampAgentAIContextWindow(remoteInt(msg, "context_window", 0))
		lazyHistoryMessages, lazyHistoryChars := agentAIHistoryLimits(lazyProvider, lazyContextWindow)
		session = &agentAISession{
			id:                      sessionID,
			mode:                    lazyMode,
			projectPath:             projectPath,
			provider:                lazyProvider,
			model:                   strings.TrimSpace(remoteString(msg, "model")),
			effort:                  strings.TrimSpace(remoteString(msg, "effort")),
			contextWindow:           lazyContextWindow,
			resumeSessionID:         firstNonEmpty(remoteString(msg, "resume_session_id"), remoteString(msg, "source_session_id")),
			reservedNativeSessionID: strings.TrimSpace(remoteString(msg, "reserved_native_session_id")),
			bindingVersion:          remoteInt(msg, "binding_version", 0),
			claudePolicy:            cloneAgentAIClaudeRemotePolicy(parseAgentAIClaudeRemotePolicy(msg)),
			initialContext:          lazyInitialContext,
			isGoalFork:              lazyIsGoalFork,
			surface:                 normalizeAgentAISurface(remoteString(msg, "surface")),
			history:                 trimAgentAIHistoryWithLimits(lazyHistory, lazyHistoryMessages, lazyHistoryChars),
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
	if surface := normalizeAgentAISurface(remoteString(msg, "surface")); surface != "" {
		session.surface = surface
	}
	if cw := clampAgentAIContextWindow(remoteInt(msg, "context_window", 0)); cw > 0 {
		session.contextWindow = cw
	}
	if v := strings.TrimSpace(remoteString(msg, agentAIOriginSocketField)); v != "" {
		session.originSocket = v
	}
	session.lastActiveAt = time.Now().UTC() // LRU recency: this conversation is active
	if session.cancel != nil {
		m.mu.Unlock()
		_ = runWrite(agentAIErrorPayload(sessionID, messageID, fmt.Errorf("ai session is already running: %s", sessionID)))
		return
	}
	m.mu.Unlock()

	// Phone-driven runs can arrive while the Mac is unattended. The project may
	// live under a macOS privacy-protected folder (Desktop/Documents/Downloads/
	// iCloud/external volume): touching it would raise a "Files & Folders" (TCC)
	// prompt nobody is there to answer. tccguard refuses with an actionable
	// consent error before the first disk access (goal capture, attachments and
	// provider spawn all touch the project), and probes the category once when a
	// human IS at the Mac, remembering the grant.
	if guardErr := tccguard.GuardPath(session.projectPath); guardErr != nil {
		_ = runWrite(agentAIErrorPayload(sessionID, messageID, guardErr))
		return
	}

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
	// operations (/clear) or honest status replies (/model /help /cost /compact).
	// Prompt-style commands (/review, custom .claude/commands) and unknown /xxx
	// fall through to the normal model turn below.
	if m.handleLocalSlashCommand(session, messageID, content, provider, runWrite) {
		return
	}

	if err := m.runUserMessage(session, remoteString(msg, "run_id"), messageID, content, provider, attachments, remoteStringSlice(msg, "attachment_allowed_hosts"), emitter, messageRun.nativeGoal); err != nil {
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
			// 错误回包也要带 origin：fan-out 去重靠它跳过发起通道，Task 7 的
			// 审批 ack 双写同样按 origin 寻址。
			originSocketID: strings.TrimSpace(remoteString(msg, agentAIOriginSocketField)),
			goalIdentity:   goalRunIdentityFromMessage(msg),
		}, writeJSON)
		_ = emitter.emit(agentAIErrorPayload(sessionID, messageID, err))
		return
	}
	m.mu.Lock()
	if persisted := m.bindings[sessionID]; persisted.NativeSessionID != "" {
		if persisted.State == "confirmed" {
			resumeID = persisted.NativeSessionID
			reservedID = ""
			// 绑定守卫（2026-10-27 native session id mismatch 事故根治的 agent
			// 端半边）：绑定已 confirmed 时，resume id 归 persisted.Provider 所有。
			// 消息里带来的 provider（服务端设备推断翻转 / 客户端误带 / 老服务端）
			// 若与绑定不同族，以绑定为权威强制改写 msg——否则 CLI 拿内核的原生 id
			// 去 --resume，confirmBinding 必然 mismatch，会话从这一回合起永久报错。
			// 缺省也强制：provider 缺省但 tool 是外来值时同样会走错 CLI。
			// claude/claudecode 经 providersEquivalent 视为同族，不触发强制。
			if requested := firstNonEmpty(remoteString(msg, "provider"), remoteString(msg, "tool")); !providersEquivalent(requested, persisted.Provider) {
				msg["provider"] = persisted.Provider
				msg["tool"] = persisted.Provider
			}
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
		if cw := clampAgentAIContextWindow(remoteInt(msg, "context_window", 0)); cw > 0 {
			session.contextWindow = cw
		}
		session.resumeSessionID = resumeID
		session.reservedNativeSessionID = reservedID
		session.bindingVersion = remoteInt(msg, "binding_version", session.bindingVersion)
		if v := strings.TrimSpace(remoteString(msg, agentAIOriginSocketField)); v != "" {
			session.originSocket = v
		}
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
	if name == "compact" {
		switch provider {
		case "codex":
			m.handleCodexCompactCommand(session, messageID, writeJSON)
			return true
		case "agentcore":
			m.handleAgentCoreCompactCommand(session, messageID, writeJSON)
			return true
		case "claude", "claudecode":
			m.mu.Lock()
			resumeID := strings.TrimSpace(session.resumeSessionID)
			m.mu.Unlock()
			if resumeID == "" {
				m.replyLocalSlashCommand(session, messageID, provider, writeJSON,
					"当前还没有可压缩的原生上下文。先对话几轮，再发送 /compact。")
				return true
			}
			// Claude Code 的 headless CLI 自己实现了 /compact（带 --resume 时
			// 压缩原生会话并接着用压缩后的上下文）。这里不能本地应答，必须让
			// 命令原样落成一个模型回合——runUserMessage 识别该形态后跳过手机
			// 契约包装与 TUI 路由，否则命令文本会变成普通用户消息。
			return false
		}
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

// replyLocalSlashCommand settles a slash command with a single local status
// line (run.started → ai.delta → ai.done) without spawning a model turn.
func (m *agentAIManager) replyLocalSlashCommand(session *agentAISession, messageID, provider string, writeJSON agentTerminalWriter, line string) {
	if writeJSON == nil {
		return
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
		session.finalizedRunSeq = 0
		m.mu.Unlock()
		return fmt.Sprintf("✓ /clear 已清空。下一轮将开启全新的 %s 会话。", agentAIProviderDisplayName(provider))
	case "model":
		newModel := strings.TrimSpace(args)
		m.mu.Lock()
		cur := session.model
		m.mu.Unlock()
		if newModel != "" {
			return "远程 /model 不会修改服务器会话配置。请在手机会话工具中切换模型，修改将在下一轮生效。"
		}
		if cur == "" {
			return "当前未指定模型(使用服务器或 CLI 默认)。请在手机会话工具中切换。"
		}
		return fmt.Sprintf("当前模型: %s。请在手机会话工具中切换。", cur)
	case "help":
		return "本机命令(remote=local): /clear  /model  /help  /cost  /compact\n注: 模型切换请使用手机会话设置；/compact 会对已连接的 Claude/Codex/内置内核执行真实压缩。"
	case "cost":
		// Per-turn usage is emitted as ai.usage but not accumulated on the
		// session, so there is no running total to report here.
		return "⚠ headless agent 未累计会话用量。请在桌面端查看 token / 费用统计。"
	case "compact":
		return fmt.Sprintf("⚠ 无法确定 %s 的原生会话类型，/compact 未执行。请先在会话设置里固定 provider。", agentAIProviderDisplayName(provider))
	}
	return ""
}

// agentAINativeCompactTurn reports whether this turn is a raw /compact passed
// through to the Claude CLI instead of being answered locally.
//
// Claude Code's headless binary implements /compact itself: with --resume it
// summarizes the native session and continues from the compacted context.
// Agent-side wrapping (prompt builder, phone contract, history append) would
// turn the command text into an ordinary user message and the CLI would never
// see the slash command, so this turn runs bare. Only claude/claudecode
// qualify — codex has its own compact path, and agentcore compaction is managed
// by the kernel.
func agentAINativeCompactTurn(content, provider, resumeSessionID string) bool {
	if strings.TrimSpace(resumeSessionID) == "" {
		return false
	}
	switch provider {
	case "claude", "claudecode":
	default:
		return false
	}
	name, _, ok := parseLocalSlashCommand(content)
	return ok && name == "compact"
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
	// 手机驱动插话的契约只在「注入正在运行的 TUI」时随文本走（下面两处外部
	// TUI 分支与 session.tuiTurn 分支）。headless 通道（claude stream-json /
	// codex / agentcore）不注入文本契约，见 agent_ai_phone_contract.go 头注释。
	steerContent := content
	if normalizeAgentAISurface(remoteString(msg, "surface")) == agentAISurfacePhone {
		steerContent = wrapAgentAIPhoneContract(content)
	}

	m.mu.Lock()
	session := m.sessions[sessionID]
	if session == nil {
		m.mu.Unlock()
		// 导入/外部 TUI 会话不在注册表(手机侧 tuiDrivenRunning)。若 server
		// 随 ai.steer 附了 source_session_id,且该 native 会话有 live 的 busy
		// pid 记录并带跨会话 messaging UDS socket(CC >= 2.1.224),插话经
		// CC 收件箱排队——与 stop 的外部 SIGINT 同一份注册表、与 TUI 注入
		// 回合(steerTUITurn)同语义。TUI 插话只有文本帧,附件先拒。
		if sourceSessionID := strings.TrimSpace(remoteString(msg, "source_session_id")); sourceSessionID != "" {
			if arr, ok := msg["attachments"].([]interface{}); ok && len(arr) > 0 {
				emitAgentAISteerAck(writeJSON, sessionID, messageID, "error", "外部 TUI 执行中的插话暂不支持附件，请等回合结束后再发", "unsupported_attachment")
				return
			}
			if result, errMsg := externalSteerAttempt(externalTUIHome(), sourceSessionID, steerContent); result != "not_running" {
				code := ""
				if result == "error" {
					code = "send_failed"
				}
				emitAgentAISteerAck(writeJSON, sessionID, messageID, result, errMsg, code)
				return
			}
		}
		emitAgentAISteerAck(writeJSON, sessionID, messageID, "not_running", fmt.Sprintf("ai session not found: %s", sessionID), "")
		return
	}
	// surface 先落到会话上：下面 TUI 分支按 session.surface 决定是否注入契约。
	if surface := normalizeAgentAISurface(remoteString(msg, "surface")); surface != "" {
		session.surface = surface
	}
	if session.cancel == nil {
		m.mu.Unlock()
		// 注册表里在、但当前没有活跃回合:该会话的回合可能正由外部 TUI 驱动
		// (与 stop 的 idle 分支对称的 best-effort)。尝试同一外部通道。
		if sourceSessionID := strings.TrimSpace(remoteString(msg, "source_session_id")); sourceSessionID != "" {
			if arr, ok := msg["attachments"].([]interface{}); ok && len(arr) > 0 {
				emitAgentAISteerAck(writeJSON, sessionID, messageID, "error", "外部 TUI 执行中的插话暂不支持附件，请等回合结束后再发", "unsupported_attachment")
				return
			}
			if result, errMsg := externalSteerAttempt(externalTUIHome(), sourceSessionID, steerContent); result != "not_running" {
				code := ""
				if result == "error" {
					code = "send_failed"
				}
				emitAgentAISteerAck(writeJSON, sessionID, messageID, result, errMsg, code)
				return
			}
		}
		emitAgentAISteerAck(writeJSON, sessionID, messageID, "not_running", fmt.Sprintf("ai session is not running: %s", sessionID), "")
		return
	}
	// TUI 注入回合：没有 stdin 可写，但 UDS 注入通道就是插话通道——CC 把回合中
	// 收到的注入按排队消息处理，当前回合结束后自动接续（与 codex enqueue 的
	// "queued" 语义一致）。分流给专用执行器，不走下面的 stdin 路径。
	if tuiPlan := session.tuiTurn; tuiPlan != nil {
		m.mu.Unlock()
		// 契约由 steerTUITurn 按 session.surface 追加到注入帧；history 存原文。
		m.steerTUITurn(session, *tuiPlan, msg, sessionID, messageID, content, writeJSON)
		return
	}
	control := session.codexSteer
	claudeControl := session.claudeSteer
	coreControl := session.coreSteer
	if control == nil && claudeControl == nil && coreControl == nil {
		m.mu.Unlock()
		emitAgentAISteerAck(writeJSON, sessionID, messageID, "unsupported", "ai.steer is only supported by active Codex app-server, Claude stream-json or Aliang Agent runs", "unsupported_provider")
		return
	}
	projectPath := session.projectPath
	m.mu.Unlock()

	// 附件在写 history 之前物化：校验失败不该留下一条「用户说过但没发出去」
	// 的历史（下一轮 prompt 会把它当上下文带上）。
	attachments, err := resolveAgentAIAttachments(msg["attachments"], projectPath)
	if err != nil {
		emitAgentAISteerAck(writeJSON, sessionID, messageID, "error", err.Error(), "bad_request")
		return
	}
	steer := agentAISteerMessage{
		MessageID: messageID,
		Content:   content,
		CreatedAt: time.Now().UTC(),
	}
	if len(attachments) > 0 {
		// 文本素材并入正文；图片编码成 block（base64 内联，不落临时文件，
		// 因此可以安全排队——见 agentAISteerMessage.ImageBlocks 注释）。
		if suffix := agentAIAttachmentPromptSuffix(textOnlyAttachments(attachments), false); suffix != "" {
			steer.Content += suffix
		}
		if hasImageAttachments(attachments) {
			blocks, buildErr := buildSteerImageBlocks(context.Background(), attachments, remoteStringSlice(msg, "attachment_allowed_hosts"))
			if buildErr != nil {
				emitAgentAISteerAck(writeJSON, sessionID, messageID, "error", buildErr.Error(), "send_failed")
				return
			}
			steer.ImageBlocks = blocks
		}
	}

	m.mu.Lock()
	session.history = append(session.history, agentAIMessage{
		Role:      "user",
		MessageID: messageID,
		Content:   content,
		CreatedAt: steer.CreatedAt,
	})
	m.mu.Unlock()

	var result string
	switch {
	case control != nil:
		result, err = control.enqueue(steer)
	case claudeControl != nil:
		result, err = claudeControl.deliver(steer)
	default:
		result, err = coreControl.deliver(steer)
		if err == nil {
			// agentcore 专属：插话投递成功才留痕（deliver 失败时消息没进内核）。
			// 落 content（原文）而非 steer.Content——后者此时已拼上附件后缀，
			// journal 按 spec §5 只存真实用户 content。
			m.journalSteerUserMessage(sessionID, steer, content)
		}
	}
	if err != nil {
		code := "send_failed"
		if result == "not_running" {
			code = "turn_ended"
		}
		emitAgentAISteerAck(writeJSON, sessionID, messageID, result, err.Error(), code)
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

// steerTUITurn 把插话注入执行中的 TUI。TUI 回合没有 stdin（claudeSteer 恒
// nil），UDS 注入是唯一通道：CC 把回合中收到的注入按排队消息处理，当前回合
// 结束后自动接续执行——正是 steer 的语义（ccPeerInject 注释已实证 busy TUI
// 对注入排队）。插话回显由进行中的 tail 桥送回手机（transcriptUserEcho →
// ai.delta channel=user），不在这里重复推流。
//
// v1 只支持文本：注入帧是纯文本，附件要先落 TUI 侧本地缓存并让模型 Read，
// 排队场景下缓存生命周期复杂（回合可能随时结束），先明确拒绝而不是静默丢弃。
func (m *agentAIManager) steerTUITurn(session *agentAISession, plan tuiTurnPlan, msg map[string]interface{}, sessionID, messageID, content string, writeJSON agentTerminalWriter) {
	if arr, ok := msg["attachments"].([]interface{}); ok && len(arr) > 0 {
		emitAgentAISteerAck(writeJSON, sessionID, messageID, "error", "TUI 执行中的插话暂不支持附件，请等回合结束后再发", "unsupported_attachment")
		return
	}
	// held 观测以「当前 transcript 末尾」为起点：只盯注入之后新写的字节，
	// held 标记（若有）一定落在其中；从回合起点的旧偏移开始会把历史字节
	// 重复扫一遍。
	offset := int64(0)
	if info, err := os.Stat(plan.JSONLPath); err == nil {
		offset = info.Size()
	}
	// 手机行为契约只加在注入帧上；下面 history 仍落原文（契约不进上下文）。
	injectText := wrapAgentAIPhoneContractForSurface(session.surface, content)
	if err := ccPeerDialInject(plan.SocketPath, plan.PeerToken, injectText, plan.FromMode); err != nil {
		emitAgentAISteerAck(writeJSON, sessionID, messageID, "error", "无法把插话送进终端会话", "send_failed")
		return
	}
	if ccPeerWatchHeld(plan.JSONLPath, offset, ccPeerHeldWatchWindow) {
		emitAgentAISteerAck(writeJSON, sessionID, messageID, "error", "终端会话拒绝了这条插话，请在终端中处理", "held")
		return
	}
	m.mu.Lock()
	session.history = append(session.history, agentAIMessage{
		Role: "user", MessageID: messageID, Content: content, CreatedAt: time.Now().UTC(),
	})
	m.mu.Unlock()
	emitAgentAISteerAck(writeJSON, sessionID, messageID, "queued", "", "")
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
			"input":               codexSteerInput(msg),
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
func (m *agentAIManager) runUserMessage(session *agentAISession, runID, messageID, content, provider string, attachments []agentAIAttachment, attachmentAllowedHosts []string, emitter *agentAIRunEmitter, nativeGoal map[string]interface{}) error {
	// Spawn precheck (imported Claude sessions): refuse to --resume a native
	// session whose interactive TUI is mid-turn — two writers on one native
	// conversation would interleave transcripts. Earliest abortable point: no
	// run state below has been mutated yet, so returning here surfaces as a
	// plain ai.error and the server can retry after the TUI turn finishes.
	if err := guardExternalTUIClaudeSpawn(externalTUIHome(), provider, session.resumeSessionID); err != nil {
		return err
	}
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
	session.activeRunID = runID
	session.goalIdentity = cloneGoalIdentity(emitter.goalIdentity)
	session.goalAllowedRoots = emitter.goalAllowedRoots
	session.goalAllowedCommands = emitter.goalAllowedCommands
	resumeSessionID := strings.TrimSpace(session.resumeSessionID)
	reservedNativeSessionID := strings.TrimSpace(session.reservedNativeSessionID)
	// A raw /compact for claude must reach the CLI unchanged: the local command
	// is the compaction trigger, and any agent-side wrapping turns it into an
	// ordinary user message (the CLI then answers text instead of compacting).
	nativeCompact := agentAINativeCompactTurn(content, provider, resumeSessionID)
	logger.Info(fmt.Sprintf("ai.run: session=%s runSeq=%d resumeSessionID=%q (empty=fresh claude run this turn; non-empty=--resume)", session.id, session.runSeq, resumeSessionID))
	if !nativeCompact {
		session.history = append(session.history, agentAIMessage{
			Role:      "user",
			MessageID: messageID,
			Content:   content,
			CreatedAt: now,
		})
	}
	freshPrompt := content
	if !nativeCompact {
		freshPrompt = buildAgentAIPrompt(session, content)
	}
	// headless 通道（claude -p / codex / agentcore）不带手机行为契约：那是
	// 宿主拉起的进程，约束走 settings/permissions 与 append-system-prompt，
	// 见 agent_ai_phone_contract.go 头注释。契约只在 TUI 注入路由里加。
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
		contextWindow:           session.contextWindow,
		resumeSessionID:         resumeSessionID,
		reservedNativeSessionID: reservedNativeSessionID,
		bindingVersion:          session.bindingVersion,
		prompt:                  prompt,
		freshPrompt:             freshPrompt,
		attachments:             append([]agentAIAttachment(nil), attachments...),
		attachmentAllowedHosts:  append([]string(nil), attachmentAllowedHosts...),
		cancel:                  cancel,
		approvalToken:           approvalToken,
		activity:                activity,
		subagents:               newAgentAISubagentRegistry(),
		claudePolicy:            cloneAgentAIClaudeRemotePolicy(session.claudePolicy),
		goalIdentity:            cloneGoalIdentity(emitter.goalIdentity),
		goalAllowedRoots:        emitter.goalAllowedRoots,
		goalAllowedCommands:     emitter.goalAllowedCommands,
		nativeGoal:              cloneAgentAIMap(nativeGoal),
		// #5 (v2): enforce read-only on the EXECUTION run for fork sessions.
		// v1 set this only on the temporary messageRun, which is discarded before
		// the real run is built here — so it never reached runCLIPass/codex.
		readOnly: session.isGoalFork,
		surface:  session.surface,
		// 执行 run 也带 origin（fan-out 去重走 emitter 侧的 messageRun 值，
		// 这份供审批 ack 双写等 run 级消费方用；临界区内读，与 session 快照同源）。
		originSocketID: session.originSocket,
	}
	run.onClaudeInit = func(commands []string, version string) {
		m.recordClaudeCapabilities(run.sessionID, run.projectPath, commands, version)
	}
	m.mu.Unlock()

	// TUI 执行路由（spec §4.1）：原生 TUI 开着且空闲时，把这一回合送进那个 TUI
	// 执行——TUI 与手机天然共享上下文、同看一次对话，TUI 自动更新。
	//
	// 三个不变量：
	//   ① 判定发生在任何 provider 进程/spawn 状态之前；
	//   ② 注入失败不回退 spawn（spec §4.5）——一次回合只走一条路；
	//   ③ 释放本函数前段为 spawn 准备的 ctx，避免悬挂 goroutine。
	//
	// 位置刻意选在 ensurePolicyBeforeRun / startAIWatchdog / runCLI 之前：前者是
	// spawn 路径的策略同步（注入回合用 TUI 自己的权限模式），中者的 watchdog 会因
	// TUI 回合没有子进程 stdout 而误判 idle 超时。
	if plan := tuiTurnDecide(tuiTurnInput{
		Home:            externalTUIHome(),
		Provider:        provider,
		NativeSessionID: run.resumeSessionID,
		ReadOnly:        run.readOnly,
		HasGoalIdentity: len(run.goalIdentity) > 0,
		JSONLPath:       ccPeerSessionJSONL(externalTUIHome(), run.resumeSessionID),
	}); plan != nil && !nativeCompact {
		// 释放 spawn 路径预建的 ctx（执行器自建 runCtx，不再用这个）。
		cancel()
		// 传原始 content 而非 run.prompt（后者被 buildAgentAIPrompt 包装过）；
		// 附件走 run.attachments，由执行器拼进注入文本。手机行为契约也在执行器
		// 里按 run.surface 追加到注入文本——history 与注入文本因此各归各位：
		// 历史只存原文，契约只存在于 TUI 收到的那一帧。
		go m.runTUITurn(context.Background(), session, run, content, plan, emitter)
		return nil
	}

	// Best-effort sync of the device approval policy before the run starts, so a
	// just-changed policy (or a fresh process with only the built-in default)
	// picks up the current rules. Never blocks beyond a short timeout or errors
	// the turn; failures keep the cached/built-in policy.
	if svc := m.approvalService(); svc != nil {
		svc.ensurePolicyBeforeRun(ctx, session.projectPath)
	}
	run.guard = m.attachRunGuard(run, emitter.emit)
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
	// 跨端同态（Task 7 前置）：option 作答同样刷新 session.originSocket——
	// option 续接的执行 run 与其后续审批 ack 都要写回「作答这条 socket」，
	// 不刷新的话执行 run 还带着上一回合的旧 origin，ack 双写会写到旧通道。
	// 空标记（TUI/测试内部旧调用方）不动。
	if session != nil {
		if v := strings.TrimSpace(remoteString(msg, agentAIOriginSocketField)); v != "" {
			session.originSocket = v
		}
	}
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

	// option 续接回合同样盖 origin：不补的话，发起 socket 已注册时会出现
	// 「内联 + 扇出」双投（无去重扇出）。
	run := agentAIRun{
		sessionID:      sessionID,
		runID:          remoteString(msg, "run_id"),
		messageID:      messageID,
		originSocketID: strings.TrimSpace(remoteString(msg, agentAIOriginSocketField)),
	}
	emitter := m.runEmitter(run, writeJSON)
	if err := m.runUserMessage(session, remoteString(msg, "run_id"), messageID, content, provider, nil, nil, emitter, nil); err != nil {
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
	if session.contextWindow > 0 {
		payload["context_window"] = session.contextWindow
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
		content := stripAgentAIPhoneContract(strings.TrimSpace(remoteString(row, "content")))
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
	// Attachment validation is the first place a message touches the project
	// directory. Behind tccguard, an unattended Mac refuses protected folders
	// instead of leaving an unanswerable "Files & Folders" prompt on screen.
	if err := tccguard.GuardPath(projectPath); err != nil {
		return nil, err
	}
	projectRoot, err := cleanExistingAgentDirectory(projectPath)
	if err != nil {
		return nil, fmt.Errorf("resolve attachment project directory: %w", err)
	}
	if err := tccguard.GuardPath(projectRoot); err != nil {
		return nil, err
	}
	const maxAttachments = 8
	const maxAttachmentBytes = 20 * 1024 * 1024
	const maxTotalBytes = 50 * 1024 * 1024
	// 文本素材的上限按 rune 计（不是字节）：中文/emoji 的字节数是 rune 的数倍，
	// 按字节卡会把一幅正常大小的 ASCII 图误杀。服务端 schema 按字符卡同一上限，
	// 这里只做防线，放宽方向是安全的（不会拒掉服务端放行的载荷）。
	const maxAttachmentTextRunes = 32 * 1024
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
			// 正文不 TrimSpace：ASCII 图的缩进就是语义，首尾空格删掉图就错位了。
			Text: remoteString(item, "text"),
		}
		if attachment.Type == "" && attachment.Text != "" {
			attachment.Type = "text"
		}
		if attachment.Type == "text" && attachment.Text != "" {
			// 内联文本素材：既没有文件也没有 URL，不走下面任何下载/围栏分支。
			// 注意 Type=="text" 还有另一种历史含义——「一个文本文件」，那种
			// 带 Path 但没有 Text，继续走下面的文件校验。
			if strings.TrimSpace(attachment.Text) == "" {
				return nil, errors.New("text attachment requires non-empty text")
			}
			if utf8.RuneCountInString(attachment.Text) > maxAttachmentTextRunes {
				return nil, fmt.Errorf("text attachment exceeds %d characters", maxAttachmentTextRunes)
			}
			if attachment.Name == "" {
				attachment.Name = "attachment.txt"
			}
			attachments = append(attachments, attachment)
			continue
		}
		if attachment.Type == "text" && attachment.Path == "" && attachment.URL == "" {
			return nil, errors.New("text attachment requires non-empty text")
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
			// Absolute attachment paths may point at another account's home or a
			// different privacy category than the project; guard before resolving
			// symlinks (EvalSymlinks itself stats every component).
			if err := tccguard.GuardPath(absolute); err != nil {
				return nil, err
			}
			if resolved, resolveErr := filepath.EvalSymlinks(absolute); resolveErr == nil {
				absolute = resolved
				if err := tccguard.GuardPath(absolute); err != nil {
					return nil, err
				}
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
	sourceSessionID := strings.TrimSpace(remoteString(msg, "source_session_id"))

	m.mu.Lock()
	session := m.sessions[sessionID]
	if session == nil {
		m.mu.Unlock()
		// Imported sessions are conversations Claude Code spawned outside this
		// agent (nothing in m.sessions, no cancel func). The turn may be
		// running in a live TUI process on this machine: try the external
		// interrupt (SIGINT = one Esc press) when the stop carries the native
		// session id, and fall back to the legacy "stopped" reply otherwise.
		interrupted := false
		if sourceSessionID != "" {
			_, interrupted = interruptExternalClaudeTurn(externalTUIHome(), sourceSessionID)
		}
		_ = writeJSON(externalStopReply(sessionID, interrupted))
		return
	}
	requestedRunID := strings.TrimSpace(remoteString(msg, "run_id"))
	if requestedRunID != "" && session.activeRunID != "" && requestedRunID != session.activeRunID {
		activeRunID := session.activeRunID
		m.mu.Unlock()
		logger.Info(fmt.Sprintf("ai.stop: ignored stale run session=%s requested=%s active=%s", sessionID, requestedRunID, activeRunID))
		return
	}
	cancel := session.cancel
	runWrite := session.activeWriter
	cancelledApprovals := m.clearPendingApprovalsLocked(
		sessionID,
		session.runSeq,
		models.AgentAIApprovalDecisionCancel,
	)
	m.mu.Unlock()
	if runWrite == nil {
		runWrite = writeJSON
	}
	// TODO(ai-run-resilience): 此处的 cancel 不经过 agentAIRunGuard——卡死回合
	// 上用户主动停止时没有 [ai.run.kill] 轨迹、也没有 30s 强拆兜底。见
	// docs/superpowers/plans/2026-10-27-ai-run-resilience.md（计划未覆盖）。
	if cancel != nil {
		cancel()
	} else if sourceSessionID != "" {
		// Registered but idle: the previous agent-spawned turn already
		// finished, so the turn being stopped may belong to the external TUI.
		// Best-effort interrupt; the reply below is unchanged either way.
		interruptExternalClaudeTurn(externalTUIHome(), sourceSessionID)
	}
	m.emitApprovalCancelled(runWrite, sessionID, cancelledApprovals, "run_cancelled")
	_ = runWrite(map[string]interface{}{
		"type":       models.AgentEventAIStatus,
		"session_id": sessionID,
		"status":     "stopping",
	})
}

// preemptForGoalRun makes a newer server-authoritative Goal run replace a
// locally-stuck run in the same conversation. Ordinary chat messages retain the
// existing single-run guard. The run_id fence prevents a redelivered copy of the
// same Goal run from cancelling itself.
func (m *agentAIManager) preemptForGoalRun(
	sessionID string,
	incomingRunID string,
	goalIdentity map[string]interface{},
	timeout time.Duration,
) error {
	if len(goalIdentity) == 0 || strings.TrimSpace(incomingRunID) == "" {
		return nil
	}

	m.mu.Lock()
	session := m.sessions[sessionID]
	if session == nil || session.cancel == nil {
		m.mu.Unlock()
		return nil
	}
	if session.activeRunID == incomingRunID {
		m.mu.Unlock()
		return fmt.Errorf("ai session is already running: %s", sessionID)
	}
	cancel := session.cancel
	runWrite := session.activeWriter
	cancelledApprovals := m.clearPendingApprovalsLocked(
		sessionID,
		session.runSeq,
		models.AgentAIApprovalDecisionCancel,
	)
	m.mu.Unlock()

	// TODO(ai-run-resilience): 此处的 cancel 不经过 agentAIRunGuard——卡死回合
	// 上用户主动停止时没有 [ai.run.kill] 轨迹、也没有 30s 强拆兜底。见
	// docs/superpowers/plans/2026-10-27-ai-run-resilience.md（计划未覆盖）。
	cancel()
	m.emitApprovalCancelled(runWrite, sessionID, cancelledApprovals, "goal_run_preempted")

	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		m.mu.Lock()
		current := m.sessions[sessionID]
		idle := current == nil || current.cancel == nil
		m.mu.Unlock()
		if idle {
			return nil
		}
		select {
		case <-deadline.C:
			return fmt.Errorf("goal run preemption timed out: %s", sessionID)
		case <-ticker.C:
		}
	}
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
	//
	// 跨端同态（spec §4.2.2）：ack 双写——决策到达的 socket（现有语义，重投
	// 引擎的对端确认）+ run 发起的 socket（修「仪表盘抢答手机回合时 PhoneServer
	// 收不到 ack、已生效决策被重投至 10 次」）。runSocket 在闭包外一次取好
	// （session.originSocket，Task 5 落）；同 socket 只写一次；run socket 已
	// 换道/已死/未注册时尽力而为跳过（服务端宽限+离线撤单兜底）。duplicate /
	// no_match / 坏决策各 return 路径都经这一个闭包，全覆盖。
	originSocket := strings.TrimSpace(remoteString(msg, agentAIOriginSocketField))
	runSocket := ""
	m.mu.Lock()
	if s := m.sessions[sessionID]; s != nil {
		runSocket = s.originSocket
	}
	m.mu.Unlock()
	ack := func(result string) {
		payload := map[string]interface{}{
			"type":        models.AgentEventAIApprovalAck,
			"session_id":  sessionID,
			"approval_id": approvalID,
			"result":      result,
		}
		if deliveryID != "" {
			payload["delivery_id"] = deliveryID
		}
		if writeJSON != nil {
			_ = writeJSON(payload)
		}
		if runSocket != "" && runSocket != originSocket {
			m.mu.Lock()
			sock := m.aiSockets[runSocket]
			m.mu.Unlock()
			if sock != nil && sock.alive() {
				sock.enqueue(payload)
			}
		}
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

// agentAITeardownStatus 是「run 的所有者被拆掉」时发布的终态。服务端把
// stopped|cancelled|interrupted 映射成 run_state=cancelled（生命周期收敛），
// 这才是「回合被打断」的诚实结论。什么都不发则会留下一个 live run，被服务端
// 的 stale sweeper 打翻成 timed_out —— 手机上一张红色失败卡片（2026-10-21 事故）。
const agentAITeardownStatus = "cancelled"

func agentAITeardownMessage(reason string) string {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "agent_shutdown"
	}
	return fmt.Sprintf("AI run cancelled by the agent (%s); the local process may keep running, but its result is no longer reported", reason)
}

// closeAll 拆掉本机所有常驻 AI 会话（进程内调用点没有具体原因时的默认入口）。
func (m *agentAIManager) closeAll() {
	m.closeAllWithReason("agent_shutdown")
}

// closeAllWithReason 拆掉本机所有常驻 AI 会话。在途 run **先收敛后取消**：通过该
// run 自己的 emitter 发布终态 ai.status —— emitter 会先把终态写进持久 journal
// (rememberPendingTerminal) 并关闭 terminal 屏障，之后才 cancel ctx。
//
// 顺序在这里有两重意义：
//  1. journal 必须在 run 身份还在手上时落盘，进程随后崩掉也还能在下次
//     agent.registered 时由 replayPendingTerminals 重放；
//  2. terminal 屏障让 run goroutine 的 ctx.Err() 分支无法再塞一个自相矛盾的终态。
//
// 旧实现先把 m.sessions 整个换掉再 cancel，run goroutine 醒来时既找不到 session
// 也没有终态可发，服务端就一直挂着一个 live run，直到 sweeper 把它画成
// timed_out/失败（2026-10-21 事故：回合其实已经跑完，卡片却是红的）。
func (m *agentAIManager) closeAllWithReason(reason string) {
	type inflightRun struct {
		sessionID string
		runID     string
		write     agentTerminalWriter
	}

	m.mu.Lock()
	cancels := make([]context.CancelFunc, 0, len(m.sessions))
	runs := make([]inflightRun, 0, len(m.sessions))
	for _, session := range m.sessions {
		if session.cancel != nil {
			cancels = append(cancels, session.cancel)
			if session.activeWriter != nil {
				runs = append(runs, inflightRun{
					sessionID: session.id,
					runID:     session.activeRunID,
					write:     session.activeWriter,
				})
			}
		}
		// Drop pendingOption on shutdown (no writer -> rely on server offline-grace).
		session.pendingOption = nil
		m.clearPendingApprovalsLocked(session.id, session.runSeq, models.AgentAIApprovalDecisionCancel)
	}
	m.sessions = make(map[string]*agentAISession)
	m.mu.Unlock()

	message := agentAITeardownMessage(reason)
	for _, run := range runs {
		err := run.write(map[string]interface{}{
			"type":       models.AgentEventAIStatus,
			"session_id": run.sessionID,
			"status":     agentAITeardownStatus,
			"error":      message,
		})
		// emitter 先 journal 后写线，所以这里报错只代表「还没上线路」，终态本身
		// 已经持久化，重连后会被 replayPendingTerminals 补发。
		logger.Info(fmt.Sprintf("[ai.run.terminal] status=%s session=%s run=%s reason=%s delivered=%t",
			agentAITeardownStatus, run.sessionID, run.runID, reason, err == nil))
		if err != nil {
			logger.Warn(fmt.Sprintf("[ai.run.terminal] teardown delivery deferred session=%s run=%s error=%v", run.sessionID, run.runID, err))
		}
	}
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

	run.activity.setAwaitingApproval(true)
	defer run.activity.setAwaitingApproval(false)
	logger.Info(fmt.Sprintf("approval-hook: requestApproval SENDING ai.approval.request to cloud session=%s approval_id=%s kind=%s — awaiting user decision (≤%s)", run.sessionID, req.ID, req.Kind, agentAIApprovalTimeout))
	if err := writeAgentAIApprovalRequest(ctx, writeJSON, agentAIApprovalRequestPayload(req), 5, time.Second); err != nil {
		m.mu.Lock()
		if waiter := m.approvals[approvalKey]; waiter != nil && waiter.sessionID == run.sessionID && waiter.runSeq == run.runSeq {
			delete(m.approvals, approvalKey)
		}
		m.mu.Unlock()
		return agentAIApprovalResponse{}, fmt.Errorf("send approval request: %w", err)
	}

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

func writeAgentAIApprovalRequest(
	ctx context.Context,
	writeJSON agentTerminalWriter,
	payload map[string]interface{},
	maxAttempts int,
	retryDelay time.Duration,
) error {
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if err := writeJSON(payload); err == nil {
			return nil
		} else {
			lastErr = err
			logger.Warn(fmt.Sprintf("approval-hook: request send failed attempt=%d/%d error=%v", attempt, maxAttempts, err))
		}
		if attempt == maxAttempts {
			break
		}
		timer := time.NewTimer(retryDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return lastErr
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
		sessionID:           session.id,
		messageID:           firstNonEmpty(messageID, session.id),
		runSeq:              session.runSeq,
		mode:                session.mode,
		projectPath:         session.projectPath,
		provider:            session.provider,
		model:               session.model,
		cancel:              session.cancel,
		approvalToken:       session.approvalToken,
		activity:            session.activity,
		goalIdentity:        cloneGoalIdentity(session.goalIdentity),
		goalAllowedRoots:    session.goalAllowedRoots,
		goalAllowedCommands: session.goalAllowedCommands,
	}
	writeJSON := session.activeWriter
	m.mu.Unlock()

	// 自有交互工具短路:ask_user 等白名单工具就是"审批"本身,再弹审批卡等于
	// 双重交互(非 full 档注入的 permissions.ask 含 mcp__* 会把它压进 ask 路径)。
	// 白名单是精确名集合——未知 mcp__aliang__*(项目 .mcp.json 同名 server 注入)
	// 落回原路径。
	if toolName := firstNonEmpty(remoteString(raw, "tool_name"), remoteString(raw, "toolName")); models.IsAgentToolAskTool(toolName) {
		logger.Info(fmt.Sprintf("approval-hook: AUTO-APPROVE (aliang interaction tool) tool=%s session=%s", toolName, sessionID))
		return claudeApprovalHookDecision(hookEventName, true, "auto-approved: aliang interaction tool"), nil
	}

	// Policy short-circuit: evaluate the device approval policy locally before
	// hitting the cloud. Auto-approved/denied tools resolve here with no
	// round-trip; only require_approval falls through to requestApproval.
	toolName := firstNonEmpty(remoteString(raw, "tool_name"), remoteString(raw, "toolName"))
	toolInput := marshalAgentAIRaw(raw["tool_input"])
	if len(toolInput) == 0 {
		toolInput = marshalAgentAIRaw(raw["toolInput"])
	}
	// Goal execution override: the plan was pre-approved by the user (with
	// allowed_roots + allowed_commands). Auto-approve every non-dangerous tool
	// without a cloud round-trip — goal runs must NOT stall on manual approval
	// requests that the phone may not surface in time. Only block commands the
	// policy explicitly flags as dangerous (decisionAutoDeny).
	if len(run.goalIdentity) > 0 {
		// Gap B: allowed_scope 执行边界。越界（路径/命令）直接 deny，claude 收到自纠。
		// agent 级强制，非 OS 级——见 docs/superpowers/specs/2026-08-08-goal-allowed-scope-enforcement-design.md §6。
		// 注意：m.mu.Unlock() 已在此前执行，此处只能读局部 run（run 在锁内从 session 拷贝）。
		if ok, reason := goalScopeAllowsToolCall(toolName, raw, run.projectPath, run.goalAllowedRoots, run.goalAllowedCommands); !ok {
			logger.Info(fmt.Sprintf("approval-hook: SCOPE-DENY (goal run) tool=%s reason=%s session=%s", toolName, reason, sessionID))
			return claudeApprovalHookDecision(hookEventName, false, "blocked: "+reason), nil
		}
		if svc := m.approvalService(); svc != nil {
			if decision, _ := svc.evaluateApprovalDecision(toolName, toolInput, run.projectPath); decision == decisionAutoDeny {
				logger.Info(fmt.Sprintf("approval-hook: AUTO-DENY (dangerous, goal run) tool=%s session=%s", toolName, sessionID))
				return claudeApprovalHookDecision(hookEventName, false, "blocked: dangerous command in goal execution"), nil
			}
		}
		logger.Info(fmt.Sprintf("approval-hook: AUTO-APPROVE (goal run, pre-approved plan) tool=%s session=%s", toolName, sessionID))
		return claudeApprovalHookDecision(hookEventName, true, "auto-approved: goal execution within pre-approved plan"), nil
	}
	if svc := m.approvalService(); svc != nil {
		// raw 传入边界判定:Claude auto-memory carve-out 需要 transcript_path
		// 在 slug 编码漂移时精确锁定本会话的 memory 目录。
		switch decision, matchedID := svc.evaluateApprovalDecisionForHook(toolName, toolInput, run.projectPath, raw); decision {
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

// clearRunning 复位会话运行态。expectGuard 是可选的 guard 身份校验：给了就
// 必须仍是该会话当前的 guard 才执行复位——/clear 会把 runSeq 归零后被新回合
// 复用，只比 runSeq 会让迟到的强拆拆掉新回合（见 finalizeStalledGuard）。
// 不传 expectGuard 的调用方行为与旧版逐字等价（guardOwns 恒真）。
func (m *agentAIManager) clearRunning(sessionID string, runSeq int, writeJSON agentTerminalWriter, expectGuard ...*agentAIRunGuard) {
	m.mu.Lock()
	session := m.sessions[sessionID]
	// 身份校验必须与复位在同一个临界区：独立的事前检查会留下「检查通过 →
	// 新回合挂上 guard → 复位」的窗口，正是要消灭的竞态。
	guardOwns := len(expectGuard) == 0 || expectGuard[0] == nil ||
		(session != nil && session.guard == expectGuard[0])
	var steerControl *agentAICodexSteerControl
	var claudeSteerControl *agentAIClaudeSteerControl
	var coreSteerControl *agentAICoreSteerControl
	if guardOwns && session != nil && session.runSeq == runSeq {
		steerControl = session.codexSteer
		claudeSteerControl = session.claudeSteer
		coreSteerControl = session.coreSteer
		session.cancel = nil
		session.activeWriter = nil
		session.approvalToken = ""
		session.activity = nil
		if session.guard != nil {
			// 正常收尾也必须关闭 done。guard 挂在所有 provider 的回合上，而
			// markRunGuardDone 只有 agentcore 路径调用；缺了这一步，codex/claude
			// 回合被合法 kill 后 30s 还会再跑一次多余的强拆（终态被 emitter 闸
			// 丢弃，但会多一条 force_finalize 日志 + 一个多余 goroutine）。
			session.guard.markDone()
			session.guard = nil
		}
		session.codexSteer = nil
		session.claudeSteer = nil
		session.coreSteer = nil
		session.activeRunID = ""
		session.goalIdentity = nil
		session.history = trimAgentAIHistoryForSession(session, session.history)
	}
	var cancelled []string
	if guardOwns {
		cancelled = m.clearPendingApprovalsLocked(sessionID, runSeq, models.AgentAIApprovalDecisionCancel)
	}
	m.mu.Unlock()
	if steerControl != nil {
		steerControl.close("not_running", "codex turn ended before steer was applied", "turn_ended")
	}
	if claudeSteerControl != nil {
		claudeSteerControl.close("not_running", "claude turn ended before steer was applied", "turn_ended")
	}
	if coreSteerControl != nil {
		coreSteerControl.close("not_running", "agentcore turn ended before steer was applied", "turn_ended")
	}
	m.emitApprovalCancelled(writeJSON, sessionID, cancelled, "run_ended")
}

// attachClaudeSteer 把 Claude steer 通道挂到会话上，供 ai.steer 路由查找。
// 只在 runSeq 仍匹配时挂载，避免迟到/串号的 run 覆盖当前通道。
func (m *agentAIManager) attachClaudeSteer(sessionID string, runSeq int, control *agentAIClaudeSteerControl) {
	if control == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if session := m.sessions[sessionID]; session != nil && session.runSeq == runSeq {
		session.claudeSteer = control
	}
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
	session.history = trimAgentAIHistoryForSession(session, session.history)
}

// lastUserHistoryMessage 返回会话 history 里最后一条 user 消息（m.mu 内读取）。
// journal 用它拿「触发本轮的真实用户 content」——不取 run.prompt（拼接产物）。
func (m *agentAIManager) lastUserHistoryMessage(sessionID string) (agentAIMessage, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	session := m.sessions[sessionID]
	if session == nil {
		return agentAIMessage{}, false
	}
	for i := len(session.history) - 1; i >= 0; i-- {
		if strings.EqualFold(session.history[i].Role, "user") {
			return session.history[i], true
		}
	}
	return agentAIMessage{}, false
}

// journalSteerUserMessage 把一条运行中插话落成 journal 的 user 行。
// content 必须是用户原话：steer.Content 在投递前会被拼上附件后缀与手机契约
// 包装（见 ai.steer 投递分支），journal 按 spec §5 取真实用户 content，与
// session.history 的 user 行同口径。
// steer 可能先于 run 的任何写入到达（目录/文件都还没有）——appendLineLocked
// 内部有目录懒创建，这里只管追加。steer 先建的文件无头行：ProjectPath/Model
// 留空是该会话的既定降级（reader ID 兜底取文件名），不要试图补写头行
// （ensureSessionHeader 见文件存在即跳过，永远轮不到补）。
func (m *agentAIManager) journalSteerUserMessage(sessionID string, msg agentAISteerMessage, content string) {
	if m == nil || m.coreJournal == nil {
		return
	}
	ts := time.Now().UTC().Format(time.RFC3339)
	if !msg.CreatedAt.IsZero() {
		ts = msg.CreatedAt.UTC().Format(time.RFC3339)
	}
	m.coreJournal.appendUser(sessionID, msg.MessageID, content, ts)
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
	// agentAIRunEffortRejected means the CLI hard-rejected the requested
	// --effort value at argument validation (older claude builds exit 1 and
	// print the accepted levels). The pass emitted no assistant output; the
	// caller should clamp the effort to a listed level and retry once.
	agentAIRunEffortRejected
)

type agentAIRunOutcome int

func (m *agentAIManager) runCLI(ctx context.Context, run agentAIRun, writeJSON agentTerminalWriter) {
	// 必须带 guard：内核路径是在同一 goroutine 内联调用 runAgentCore 的，僵尸
	// 迟返回时 agentcore 内部两个带 guard 的 defer 会正确跳过，控制权却回到这里。
	// 若只比 runSeq，/clear 归零后新回合复用同一个值，就会把新回合的
	// cancel/activity/guard/steer 与待审批一并拆掉。正常路径 session.guard
	// 就是 run.guard，行为不变。
	defer m.clearRunning(run.sessionID, run.runSeq, writeJSON, run.guard)
	if run.cancel != nil {
		defer run.cancel()
	}

	// 附件下载放在 run 的最外层：一次 run 内可能重试多个 pass（resume-missing
	// / effort-rejected），下载一次、整轮复用，退出时统一删临时目录。失败按
	// 硬错误处理——宁可不跑，也不让模型拿到一个「有附件清单但没有文件」的
	// prompt（那会变成一轮看似正常、实则瞎猜的回复）。
	attachments, cleanupAttachments, err := downloadAgentAIAttachments(ctx, run.attachments, run.attachmentAllowedHosts)
	if err != nil {
		_ = writeJSON(agentAIErrorPayload(run.sessionID, run.messageID, err))
		return
	}
	defer cleanupAttachments()
	run.attachments = attachments

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

	// 内置内核：进程内跑 agent loop，不 spawn 任何外部 CLI，因此没有
	// resume-missing / effort-rejected 这两类自愈重试——内核自己就是权威，
	// 会话 id 由我们自己发。
	//
	// fallback 命中时把 run.provider 改写为 "agentcore"：auto 只是「由你决定」
	// 的解析指令，不是真实 provider。CLI 路径也是这么做的——confirmBinding 落
	// 的是 tool.id（解析结果）而不是 "auto"，否则绑定记录的 provider 会和实际
	// 跑的 provider 对不上，providersEquivalent 一比对就分叉。
	if agentAIUseAgentCore(run.provider) || agentAIUseAgentCoreFallback(run) {
		run.provider = "agentcore"
		m.runAgentCore(ctx, run, writeJSON)
		return
	}

	// Try to resume the prior CLI session when one is referenced. If that
	// session does not exist locally (an imported/foreign session, or one filed
	// under a different project path than this run's cwd), retry fresh so the
	// conversation still streams instead of surfacing a hard error.
	if strings.TrimSpace(run.resumeSessionID) != "" {
		outcome, rej := m.runCLIPass(ctx, run, writeJSON, true)
		if outcome != agentAIRunResumeMissing {
			m.retryAfterEffortRejected(ctx, run, writeJSON, outcome, rej, true)
			return
		}
	}
	outcome, rej := m.runCLIPass(ctx, run, writeJSON, false)
	m.retryAfterEffortRejected(ctx, run, writeJSON, outcome, rej, false)
}

// retryAfterEffortRejected 在 CLI 拒绝 effort(flag 本身不存在,或硬拒值)时
// 自愈重试一次:flag 级拒绝直接去掉 --effort,值级拒绝按声明的合法清单钳制
// (与 agentAIRunResumeMissing 的自愈重试同构)。无法钳制、或重试仍被拒时补发
// ai.error,避免回合静默无响应。
func (m *agentAIManager) retryAfterEffortRejected(ctx context.Context, run agentAIRun, writeJSON agentTerminalWriter, outcome agentAIRunOutcome, rej agentAIEffortRejection, allowResume bool) {
	if outcome != agentAIRunEffortRejected || ctx.Err() != nil {
		return
	}
	requested := strings.TrimSpace(run.effort)
	plan := planAgentAIEffortRetry(requested, rej)
	if !plan.retry {
		detail := "no compatible effort level"
		if rej.flagUnsupported {
			detail = "CLI does not support --effort"
		} else if len(rej.levels) > 0 {
			detail = "supported: " + strings.Join(rej.levels, ", ")
		}
		_ = writeJSON(agentAIErrorPayload(run.sessionID, run.messageID,
			fmt.Errorf("claude CLI rejected effort %q (%s)", requested, detail)))
		return
	}
	run.effort = plan.retryEffort
	second, _ := m.runCLIPass(ctx, run, writeJSON, allowResume)
	if second == agentAIRunEffortRejected {
		_ = writeJSON(agentAIErrorPayload(run.sessionID, run.messageID,
			fmt.Errorf("claude CLI rejected effort %q even after clamping to %q", requested, plan.retryEffort)))
		return
	}
	// 去档/钳制重试撞上 resume 缺失(与 runCLI 的自愈同构):allowResume 路径
	// 的第二次 pass 可能因陈旧 resume id 返回 ResumeMissing,直接返回会让回合
	// 既无输出也无错误(2026-09-29 评审确认的静默回归)。清掉 resume 再补一发
	// fresh pass;此时 effort 已处理完毕,fresh pass 不再带被拒的档。
	if second == agentAIRunResumeMissing && allowResume && ctx.Err() == nil {
		m.clearAgentAIResumeSessionID(run.sessionID, run.runSeq, run.resumeSessionID)
		run.resumeSessionID = ""
		run.prompt = firstNonEmpty(run.freshPrompt, run.prompt)
		_, _ = m.runCLIPass(ctx, run, writeJSON, false)
	}
}

func agentAIUseCodexAppServer(provider string) bool {
	provider = strings.ToLower(strings.TrimSpace(provider))
	return (provider == "codex" || provider == "auto") && codexAppServerAvailable()
}

// agentAIUseAgentCore 判断这一轮是否走内置内核。
//
// 只认显式的 "agentcore"，**不匹配 "auto"** —— 与 agentAIUseCodexAppServer 的
// 判断刻意不同：codex 是「装了就用」，内置内核一旦纳入 auto 就会顶掉用户装了
// 的 claude/codex，那是行为倒退。用户明确选了自有入口才走这里。
func agentAIUseAgentCore(provider string) bool {
	return strings.ToLower(strings.TrimSpace(provider)) == "agentcore"
}

// agentAIUseAgentCoreFallback 是 auto 的兜底：一个外部 CLI 都没有时走内置内核。
//
// 为什么需要它：产品的核心承诺是「装好 alianggate 就能用 AI」。auto 的候选集
// 只有 codex/claude/claudecode，三者都不在 PATH 时 resolveAgentAITool 直接报
// "no approval-capable AI CLI found in PATH"，用户什么都没得用——这条兜底正是
// 那个承诺的落点。
//
// 为什么不放进 resolveAgentAITool 的候选集：那里返回的是 *agentAITool（外部
// 进程句柄：path/args/outputFormat），内核根本不消费它。让 runCLI 在解析之前
// 分流，比伪造一个假 tool 干净。
//
// 三条刻意排除：
//   - 只认 "auto"：显式选了 claude/codex 就照旧报错，不静默换 provider。
//   - goal 相关 run（readOnly / goalIdentity）不走：内核首期不注入 goal 的
//     ALIANG_GOAL_REPORT 强制段（withGoalTaskReportSystemPrompt 只能改
//     agentAITool），跑起来会让 goal 永远拿不到报告行。这类 run 继续用 CLI。
//   - codex app-server 可用时不会走到这里（runCLI 里那个分支在前）。
func agentAIUseAgentCoreFallback(run agentAIRun) bool {
	if strings.ToLower(strings.TrimSpace(run.provider)) != "auto" {
		return false
	}
	if run.readOnly || len(run.goalIdentity) > 0 {
		return false
	}
	return !agentAIExternalCLIAvailable()
}

// agentAIExternalCLIProbe 报告 auto 的候选集里是否有可用的外部 CLI。
//
// var 是单测注入点：hermetic，不依赖测试机上真的装没装 claude/codex（与
// platformProviderEnvProbe 同一手法）。
//
// 口径必须与 resolveAgentAITool 的 auto 分支一致：codex 需 app-server 才算
// 可用；claudecode 在 resolveNamedAgentAITool 里会回退去找 claude，所以任一
// 命中即算。
var agentAIExternalCLIProbe = func() bool {
	if codexAppServerAvailable() {
		return true
	}
	if _, err := lookPathCLI("claude"); err == nil {
		return true
	}
	if _, err := lookPathCLI("claudecode"); err == nil {
		return true
	}
	return false
}

// agentAIExternalCLIAvailable 是 probe 的读取入口，保留函数形状便于将来
// 加缓存而不改调用方。
func agentAIExternalCLIAvailable() bool {
	return agentAIExternalCLIProbe()
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

func codexNativeGoalAvailable() bool {
	path, err := lookPathCLI("codex")
	if err != nil || !codexAppServerAvailable() {
		return false
	}
	cacheKey := "goal:" + executableProbeCacheKey(path)
	if cached, ok := codexAppServerProbeCache.Load(cacheKey); ok {
		return cached.(bool)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	output, probeErr := newBackgroundCommandContext(ctx, path, "features", "list").CombinedOutput()
	available := probeErr == nil && codexFeatureEnabled(string(output), "goals")
	codexAppServerProbeCache.Store(cacheKey, available)
	return available
}

func codexFeatureEnabled(output, feature string) bool {
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 3 && fields[0] == feature && fields[len(fields)-1] == "true" {
			return true
		}
	}
	return false
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
	// #5: fork (re-planning exploration) sessions run in a READ-ONLY codex sandbox
	// so exploration can't mutate the workspace. v1 always used workspace-write,
	// bypassing the runCLIPass readOnly enforcement entirely on the app-server path.
	threadParams := map[string]interface{}{
		"cwd":               run.projectPath,
		"approvalPolicy":    "on-request",
		"approvalsReviewer": "user",
		"sandbox":           codexSandboxMode(run.readOnly),
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
	threadBound := false
	bindThread := func() error {
		if threadBound || threadID == "" {
			return nil
		}
		record, err := m.confirmBinding(run.sessionID, "codex", threadID, run.bindingVersion)
		if err != nil {
			return err
		}
		threadBound = true
		return writeJSON(map[string]interface{}{
			"type":              models.AgentEventAISessionBound,
			"session_id":        run.sessionID,
			"message_id":        run.messageID,
			"run_id":            run.runID,
			"provider":          "codex",
			"native_session_id": record.NativeSessionID,
			"source_session_id": record.NativeSessionID,
			"binding_version":   record.BindingVersion,
			"binding_state":     record.State,
		})
	}
	turnStarted := false
	completed := false
	resumeMissing := false
	turnStatus := ""
	fileChangesByID := map[string]json.RawMessage{}
	commandsByID := map[string]string{}
	dedup := newCodexAgentMessageDedup()
	var nativeGoalSnapshot map[string]interface{}
	nativeGoalTerminalDesired := ""
	nativeGoalSyncErr := ""
	startTurn := func() {
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
	}
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
				case "3", "4", "5":
					nativeGoalSyncErr = "Codex app-server native Goal synchronization failed: " + detail
					terminalErr = nativeGoalSyncErr
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
			case "thread/goal/updated":
				if params, ok := msg["params"].(map[string]interface{}); ok {
					nativeGoalSnapshot = codexNativeGoalSnapshot(params["goal"])
					if nativeGoalSnapshot != nil {
						_ = writeJSON(map[string]interface{}{
							"type":        models.AgentEventAIRunProgress,
							"session_id":  run.sessionID,
							"message_id":  agentAssistantMessageID(run.messageID),
							"native_goal": nativeGoalSnapshot,
						})
					}
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
						// A finished Codex work item (command/file change) is
						// real progress for the no-progress watchdog.
						run.activity.markProgress()
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
				if turnStatus == "failed" && turnErr != "" {
					terminalErr = firstNonEmpty(turnErr, "codex turn failed")
				}
				if len(run.nativeGoal) > 0 {
					if turnStatus == "failed" || turnStatus == "interrupted" {
						nativeGoalTerminalDesired = "blocked"
					} else {
						outMu.Lock()
						nativeGoalTerminalDesired = codexNativeGoalTerminalStatus(output.String())
						outMu.Unlock()
					}
					codexPhase = "thread/goal/get(final)"
					_ = send(map[string]interface{}{
						"method": "thread/goal/get",
						"id":     4,
						"params": map[string]interface{}{"threadId": threadID},
					})
					continue
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
				if err := bindThread(); err != nil {
					terminalErr = "Codex provider binding failed: " + err.Error()
					goto done
				}
				if len(run.nativeGoal) > 0 {
					params := map[string]interface{}{
						"threadId":  threadID,
						"objective": remoteString(run.nativeGoal, "objective"),
						"status":    firstNonEmpty(remoteString(run.nativeGoal, "status"), "active"),
					}
					if budget := remoteInt(run.nativeGoal, "token_budget", 0); budget > 0 {
						params["tokenBudget"] = budget
					}
					codexPhase = "thread/goal/set"
					_ = send(map[string]interface{}{"method": "thread/goal/set", "id": 3, "params": params})
				} else {
					startTurn()
				}
			} else if _, hasResult := msg["result"]; hasResult {
				terminalErr = fmt.Sprintf("Codex app-server %s returned no thread id", threadMethod)
				goto done
			}
		}
		if fmt.Sprint(msg["id"]) == "3" {
			if result, ok := msg["result"].(map[string]interface{}); ok {
				nativeGoalSnapshot = codexNativeGoalSnapshot(result["goal"])
			}
			if nativeGoalSnapshot == nil {
				nativeGoalSyncErr = "Codex app-server thread/goal/set returned no Goal snapshot"
				terminalErr = nativeGoalSyncErr
				goto done
			}
			_ = writeJSON(map[string]interface{}{
				"type":        models.AgentEventAIRunProgress,
				"session_id":  run.sessionID,
				"message_id":  agentAssistantMessageID(run.messageID),
				"native_goal": nativeGoalSnapshot,
			})
			startTurn()
		}
		if fmt.Sprint(msg["id"]) == "4" {
			if result, ok := msg["result"].(map[string]interface{}); ok {
				nativeGoalSnapshot = codexNativeGoalSnapshot(result["goal"])
			}
			if nativeGoalSnapshot == nil {
				nativeGoalSyncErr = "Codex app-server final native Goal read returned no Goal snapshot"
				terminalErr = nativeGoalSyncErr
				goto done
			}
			currentNativeStatus := remoteString(nativeGoalSnapshot, "status")
			if currentNativeStatus == "active" ||
				((turnStatus == "failed" || turnStatus == "interrupted") && currentNativeStatus != "blocked") {
				codexPhase = "thread/goal/set(final)"
				_ = send(map[string]interface{}{
					"method": "thread/goal/set",
					"id":     5,
					"params": map[string]interface{}{"threadId": threadID, "status": nativeGoalTerminalDesired},
				})
				continue
			}
			completed = true
			goto done
		}
		if fmt.Sprint(msg["id"]) == "5" {
			if result, ok := msg["result"].(map[string]interface{}); ok {
				nativeGoalSnapshot = codexNativeGoalSnapshot(result["goal"])
			}
			if nativeGoalSnapshot == nil {
				nativeGoalSyncErr = "Codex app-server final native Goal update returned no Goal snapshot"
				terminalErr = nativeGoalSyncErr
				goto done
			}
			completed = true
			goto done
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
		if len(run.goalIdentity) > 0 {
			if errMsg == "" {
				errMsg = "Codex Goal run stopped with status " + status
			}
			if codexPhase != "" {
				errMsg = fmt.Sprintf("%s while %s", errMsg, codexPhase)
			}
			if stderrText := strings.TrimSpace(stderrBuf.String()); stderrText != "" {
				errMsg = fmt.Sprintf("%s; codex stderr: %s", errMsg, truncateForCloud(stderrText))
			}
			payload := agentAIErrorPayload(run.sessionID, run.messageID, errors.New(errMsg))
			payload["native_goal"] = codexNativeGoalStaleSnapshot(nativeGoalSnapshot, errMsg)
			_ = writeJSON(payload)
			return agentAIRunDone
		}
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
		payload := agentAIErrorPayload(run.sessionID, run.messageID, errors.New(terminalErr))
		if nativeGoalSyncErr != "" {
			payload["native_goal"] = codexNativeGoalStaleSnapshot(nativeGoalSnapshot, nativeGoalSyncErr)
		} else {
			payload["native_goal"] = nativeGoalSnapshot
		}
		_ = writeJSON(payload)
		return agentAIRunDone
	}
	if !completed {
		if err := scanner.Err(); err != nil {
			payload := agentAIErrorPayload(run.sessionID, run.messageID, err)
			payload["native_goal"] = codexNativeGoalStaleSnapshot(nativeGoalSnapshot, err.Error())
			_ = writeJSON(payload)
			return agentAIRunDone
		}
		if waitErr != nil {
			message := fmt.Sprintf("Codex app-server exited before turn completed while %s: %v", codexPhase, waitErr)
			if stderrText := strings.TrimSpace(stderrBuf.String()); stderrText != "" {
				message = fmt.Sprintf("%s; codex stderr: %s", message, truncateForCloud(stderrText))
			}
			payload := agentAIErrorPayload(run.sessionID, run.messageID, errors.New(message))
			payload["native_goal"] = codexNativeGoalStaleSnapshot(nativeGoalSnapshot, message)
			_ = writeJSON(payload)
			return agentAIRunDone
		}
		if turnStarted {
			message := fmt.Sprintf("Codex app-server exited before turn completed while %s", codexPhase)
			payload := agentAIErrorPayload(run.sessionID, run.messageID, errors.New(message))
			payload["native_goal"] = codexNativeGoalStaleSnapshot(nativeGoalSnapshot, message)
			_ = writeJSON(payload)
			return agentAIRunDone
		}
	}
	if turnStatus == "failed" || turnStatus == "interrupted" {
		if turnStatus == "failed" {
			payload := agentAIErrorPayload(run.sessionID, run.messageID, errors.New("codex turn failed"))
			payload["native_goal"] = nativeGoalSnapshot
			_ = writeJSON(payload)
		} else if len(run.goalIdentity) > 0 {
			payload := agentAIErrorPayload(run.sessionID, run.messageID, errors.New("codex turn interrupted"))
			payload["native_goal"] = nativeGoalSnapshot
			_ = writeJSON(payload)
		} else {
			_ = writeJSON(map[string]interface{}{
				"type":       models.AgentEventAIStatus,
				"session_id": run.sessionID,
				"status":     "interrupted",
			})
		}
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
		"native_goal":       nativeGoalSnapshot,
	})
	return agentAIRunDone
}

// codexSteerInput 是 turn/steer 的 input 数组：文本 + 随插话一起发的图片。
// Codex app-server 接受与 turn 相同的 input 形状，所以图片直接用 localImage
// block（steer 路径的图片在入口就落成本地临时文件，见 steer()）。
func codexSteerInput(msg agentAISteerMessage) []map[string]interface{} {
	input := []map[string]interface{}{
		{"type": "text", "text": msg.Content, "text_elements": []interface{}{}},
	}
	for _, block := range msg.ImageBlocks {
		// ImageBlocks 在 steer 入口按 Claude 形状编码（base64 source）。Codex
		// app-server 的 input 认 data:image URL，所以这里转成 data URL——两边
		// 都不需要临时文件，也就没有「排队期间临时文件被清理」的生命周期问题。
		source, _ := block["source"].(map[string]interface{})
		mediaType, _ := source["media_type"].(string)
		data, _ := source["data"].(string)
		if mediaType == "" || data == "" {
			continue
		}
		input = append(input, map[string]interface{}{
			"type": "image",
			"url":  "data:" + mediaType + ";base64," + data,
		})
	}
	return input
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

// runCLIPass 跑一遍 CLI。第二个返回值仅在 agentAIRunEffortRejected 时携带
// CLI 的 effort 拒绝分类(flag 本身不受支持 / 合法清单),供调用方自愈重试。
func (m *agentAIManager) runCLIPass(ctx context.Context, run agentAIRun, writeJSON agentTerminalWriter, allowResume bool) (agentAIRunOutcome, agentAIEffortRejection) {
	resumeID := run.resumeSessionID
	newSessionID := run.reservedNativeSessionID
	if !allowResume {
		if newSessionID == "" {
			newSessionID = resumeID
			// 身份续接是有意设计(绑定/下一轮 --resume 依赖同一 native id),
			// 但副作用是一份新 jsonl 会顶着旧身份落盘——旧文件若日后回流,
			// 扫描会把两份历史折在同一个 UUID 下。这里留一行日志让该形态
			// 可诊断(2026-10-15 事故里正是这条路径造出了 28 行 stub)。
			if resumeID != "" {
				logger.Info(fmt.Sprintf(
					"ai.run.resume.weld: session=%s runSeq=%d fresh pass reuses native id %q (resume unresolvable or fresh retry)",
					run.sessionID, run.runSeq, resumeID))
			}
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
		return agentAIRunDone, agentAIEffortRejection{}
	}
	// spawn 前探测上游可达性:56432 run service 运行态不持久化,core/owner
	// 重启后可能无人拉起——不可达先自愈,仍失败则人话快速失败,替代 CLI 侧
	// 10 次重试 3 分钟后的 error_during_execution(10-13/10-15 两次事故)。
	if (tool.id == "claude" || tool.id == "claudecode") && !run.readOnly {
		if err := ensureAIUpstreamReachableForRun(ctx); err != nil {
			_ = writeJSON(agentAIErrorPayload(run.sessionID, run.messageID, err))
			return agentAIRunDone, agentAIEffortRejection{}
		}
	}
	// 该二进制已知不认 --effort(探针确证或上次运行学到):spawn 前直接去档
	// 重解析,别让回合死在参数校验上(2026-09-29 事故:~/.local/bin claude 2.1.17)。
	// 去档是静默降级,打一行日志让它可诊断(评审确认:探测抖动窗口内可能
	// 选错二进制连带走档,日志是唯一可见痕迹)。
	if err == nil {
		if dropped := effortDropForBinary(run.effort, tool.path); dropped != run.effort {
			logger.Info(fmt.Sprintf(
				"ai.effort.dropped: session=%s runSeq=%d path=%q version=%q requested=%q (binary known to lack --effort)",
				run.sessionID, run.runSeq, tool.path, cachedCLIVersion(tool.path), run.effort))
			run.effort = dropped
			if run.readOnly || len(run.goalIdentity) > 0 {
				tool, err = resolveGoalAgentAITool(run.prompt, run.provider, run.model, dropped, resumeID, newSessionID)
			} else {
				tool, err = resolveAgentAITool(run.prompt, run.provider, run.model, dropped, resumeID, newSessionID)
			}
			if err != nil {
				_ = writeJSON(agentAIErrorPayload(run.sessionID, run.messageID, err))
				return agentAIRunDone, agentAIEffortRejection{}
			}
		}
	}
	if run.readOnly {
		tool = withAgentReadOnlyPolicy(tool)
		if tool == nil {
			_ = writeJSON(agentAIErrorPayload(run.sessionID, run.messageID, errors.New("provider does not support enforced read-only mode")))
			return agentAIRunDone, agentAIEffortRejection{}
		}
	} else if len(run.goalIdentity) > 0 {
		tool = withGoalExecutionPolicy(tool)
		// 强化 goal task 的 report 输出指令（provider 经代理常漏 ALIANG_GOAL_REPORT
		// 行，导致 goal 无故失败）。仅 claude stream-json 路径下 --append-system-prompt
		// 已挂载；这里把它扩展为原值 + goal report 强制段。
		tool = withGoalTaskReportSystemPrompt(tool)
	}
	tool = withAgentAIAttachments(tool, run.attachments)
	// Claude 的图片只能走 stream-json 的 image block。老版本 CLI 没有
	// --input-format（stdinStreamJSON=false）时没有降级通道：明确报错，
	// 而不是把图片悄悄降级成「prompt 里一行 URL」（模型读不到，用户以为发了）。
	var claudeImageBlocks []map[string]interface{}
	if tool.outputFormat == agentAIOutputClaudeStreamJSON && hasImageAttachments(run.attachments) {
		if !tool.stdinStreamJSON {
			_ = writeJSON(agentAIErrorPayload(run.sessionID, run.messageID,
				errors.New("this claude CLI does not support --input-format stream-json, which is required to send images; please upgrade claude")))
			return agentAIRunDone, agentAIEffortRejection{}
		}
		blocks, err := buildClaudeImageBlocks(run.attachments)
		if err != nil {
			_ = writeJSON(agentAIErrorPayload(run.sessionID, run.messageID, err))
			return agentAIRunDone, agentAIEffortRejection{}
		}
		claudeImageBlocks = blocks
	}
	cleanupClaudePolicy := func() {}
	effectiveRun := run
	if tool.outputFormat == agentAIOutputClaudeStreamJSON && !run.readOnly {
		// 平台通道缺失的 isolated→sanitized 回退必须在版本守卫之前：守卫要对
		// 提升后的档位继续行使职权（提升不得撤销版本守门的降级）。
		effectiveRun.claudePolicy = promoteIsolatedTierForPlatformFallback(effectiveRun.claudePolicy, run.sessionID)
		effectiveRun.claudePolicy = applyClaudeTierVersionGuard(tool, effectiveRun.claudePolicy, run.sessionID)
		var policyNotice map[string]interface{}
		tool, cleanupClaudePolicy, policyNotice = withClaudeRemotePolicy(tool, effectiveRun)
		// Fold must precede withClaudeApprovalHook: the hook re-derives
		// --setting-sources from trustTier, so a skipped fold would re-open
		// the sources a degrade just closed.
		effectiveRun.claudePolicy = applyClaudePolicyNotice(effectiveRun.claudePolicy, policyNotice)
		defer cleanupClaudePolicy()
		tool = withClaudeApprovalHook(tool, effectiveRun)
		// 自有交互 MCP server(tool_ask bridge):per-run token + 合并 --mcp-config。
		// 注入失败只降级不阻断回合——aliang-options 围栏兜底仍在;readOnly/goal
		// fork 不注入(无交互)。
		if toolAskInjected, cleanupToolAsk, toolAskErr := m.withAgentToolAskBridge(tool, effectiveRun, writeJSON); toolAskErr == nil {
			tool = toolAskInjected
			innerCleanup := cleanupClaudePolicy
			cleanupClaudePolicy = func() { cleanupToolAsk(); innerCleanup() }
		} else {
			logger.Warn(fmt.Sprintf("tool-ask: bridge injection failed, falling back to fence-only interactions session=%s err=%v", run.sessionID, toolAskErr))
		}
	}

	// 上下文窗口钉死放到最后：前面任何一次 tool 重解析(capability 去档)都会
	// 换出新的 args 副本，提前挂的话可能被覆盖掉。手机没选窗口时不加参数，
	// 保持 CLI 自己的默认行为。
	tool = withClaudeAutoCompact(tool, run.contextWindow)

	cmd := newBackgroundCommandContext(ctx, tool.path, tool.args...)
	cmd.Dir = run.projectPath
	cmd.Env = agentChildProcessEnv()
	var claudeSteer *agentAIClaudeSteerControl
	if tool.stdinStreamJSON {
		// 常驻 stdin：prompt 与后续 steer 都写成 stream-json 的 user 行。
		// 管道必须在 Start 之前建好并挂到会话上，否则启动窗口内到达的
		// steer 会误判为 unsupported。
		stdinPipe, err := cmd.StdinPipe()
		if err != nil {
			_ = writeJSON(agentAIErrorPayload(run.sessionID, run.messageID, err))
			return agentAIRunDone, agentAIEffortRejection{}
		}
		claudeSteer = newAgentAIClaudeSteerControl(run, stdinPipe, writeJSON)
		m.attachClaudeSteer(run.sessionID, run.runSeq, claudeSteer)
		run.claudeSteer = claudeSteer
	} else if tool.stdinPrompt != "" {
		// Windows 批处理 shim 的 prompt 走 stdin（claudePromptViaStdinForOS）。
		cmd.Stdin = strings.NewReader(tool.stdinPrompt)
	}
	if len(tool.env) > 0 {
		cmd.Env = append(cmd.Env, tool.env...)
	}
	logger.Info(fmt.Sprintf(
		"ai.run.cli: session=%s runSeq=%d provider=%s path=%q version=%q cwd=%q allowResume=%t resume=%t model=%q effort=%q output=%s hook_base=%q args=%v env=%s",
		run.sessionID,
		run.runSeq,
		tool.id,
		tool.path,
		cachedCLIVersion(tool.path),
		run.projectPath,
		allowResume,
		strings.TrimSpace(resumeID) != "",
		normalizeAgentAIModel(run.model),
		strings.TrimSpace(run.effort),
		tool.outputFormat,
		currentAgentAIApprovalHookBaseURL(),
		agentAIToolDiagnosticArgs(tool),
		agentAIEnvDiagnostic(),
	))

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = writeJSON(agentAIErrorPayload(run.sessionID, run.messageID, err))
		return agentAIRunDone, agentAIEffortRejection{}
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = writeJSON(agentAIErrorPayload(run.sessionID, run.messageID, err))
		return agentAIRunDone, agentAIEffortRejection{}
	}
	if err := cmd.Start(); err != nil {
		if claudeSteer != nil {
			claudeSteer.close("error", err.Error(), "start_failed")
		}
		_ = writeJSON(agentAIErrorPayload(run.sessionID, run.messageID, err))
		return agentAIRunDone, agentAIEffortRejection{}
	}
	if claudeSteer != nil {
		// 首条 prompt 必须以 stream-json user 行写入；写完才放行排队中的 steer。
		claudeSteer.start(tool.stdinPrompt, claudeImageBlocks)
		// 本次 pass 结束时兜底关闸：resume-missing/effort-rejected 会在同一
		// run 里再起一次 runCLIPass，若不在返回时关闭，旧管道的写端会一直
		// 挂着，且旧通道在重试挂载新通道前仍可被 ai.steer 命中。
		defer claudeSteer.close("not_running", "claude run ended before steer was applied", "turn_ended")
	}

	started := map[string]interface{}{
		"type":         models.AgentEventAIRunStarted,
		"session_id":   run.sessionID,
		"message_id":   agentAssistantMessageID(run.messageID),
		"provider":     tool.id,
		"mode":         run.mode,
		"project_path": run.projectPath,
		"state":        "running",
	}
	if effectiveRun.claudePolicy.policyNotice != nil {
		started["policy_notice"] = effectiveRun.claudePolicy.policyNotice
	}
	_ = writeJSON(started)

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
		// A file written/edited is real progress, not just activity.
		run.activity.markProgress()
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
		return agentAIRunDone, agentAIEffortRejection{}
	}

	if ctx.Err() != nil {
		status, errMsg := agentAIRunStoppedStatus(run.activity, limiter)
		// 先收敛存活的子 agent 再发终态：emitter 是终态屏障，ai.status 之后再
		// 发的 interrupted 会被静默丢弃。
		finalizeRunSubagents(run, writeJSON)
		statusPayload := map[string]interface{}{
			"type":       models.AgentEventAIStatus,
			"session_id": run.sessionID,
			"status":     status,
		}
		if errMsg != "" {
			statusPayload["error"] = errMsg
		}
		_ = writeJSON(statusPayload)
		return agentAIRunDone, agentAIEffortRejection{}
	}
	if waitErr != nil {
		// The referenced --resume session is not resolvable in this cwd (e.g.
		// an imported session, or one created under a different project path).
		// Signal the caller to retry without --resume instead of erroring.
		if allowResume && isAgentAIResumeMissing(stderrBuf.String()) {
			return agentAIRunResumeMissing, agentAIEffortRejection{}
		}
		// CLI 在参数校验阶段拒绝了 --effort:极老世代连 flag 都没有(unknown
		// option),旧世代硬拒值并枚举合法清单。此时不可能有 assistant 输出,
		// 上报 agentAIRunEffortRejected 让调用方去 flag/按清单钳制后重试,而不是
		// 把这个可自愈的失败直接抛给用户。
		outMu.Lock()
		emittedAssistantOutput := output.Len() > 0
		outMu.Unlock()
		if rej := isAgentAIEffortRejected(stderrBuf.String()); rej.rejected && !emittedAssistantOutput {
			// 学进探测缓存:后续回合 spawn 前直接去档,不再重演第一次死亡。
			if rej.flagUnsupported {
				rememberClaudeEffortFlagUnsupported(tool.path)
			}
			return agentAIRunEffortRejected, rej
		}
		// Surface WHY the CLI exited. Prefer the structured cause derived from
		// the last api_retry (gateway status + retry count) when available; the
		// bare waitErr ("exit status 1") carries no reason on its own. The stderr
		// capture goes into "detail" (phone renders the structured cause, not the
		// raw stderr). Mirrors the codex app-server path's stderr enrichment.
		cause := claudeFailureCause(waitErr, lastRetry, stderrBuf.String())
		payload := agentAIErrorPayloadWithRetry(run.sessionID, run.messageID, errors.New(cause), lastRetry)
		if stderrText := strings.TrimSpace(stderrBuf.String()); stderrText != "" {
			payload["detail"] = truncateForCloud(stderrText)
		}
		finalizeRunSubagents(run, writeJSON)
		_ = writeJSON(payload)
		return agentAIRunDone, agentAIEffortRejection{}
	}
	outMu.Lock()
	assistantOutput := output.String()
	outMu.Unlock()
	filesMu.Lock()
	filesTouchedCount := len(filesTouched)
	filesMu.Unlock()
	m.appendAssistantHistory(run.sessionID, run.runSeq, run.messageID, assistantOutput)
	m.setAgentAIResumeSessionIDIfEmpty(run.sessionID, run.runSeq, capturedResumeSessionID)
	// 谱系标记：手机回合（sdk-cli）若分叉于交互链，追加 cli 谱系标记让下次
	// resume 直接可见（2026-09-28 3d06bb04 实测病灶；详见 cc_session_lineage.go）。
	if tool.outputFormat == agentAIOutputClaudeStreamJSON {
		if sid := strings.TrimSpace(capturedResumeSessionID); sid != "" {
			if jsonl := ccPeerSessionJSONL(externalTUIHome(), sid); jsonl != "" {
				if err := appendLineageMarker(jsonl, sid, run.projectPath, agentAILineageNotice); err != nil {
					logger.Info(fmt.Sprintf("lineage marker: session=%s error=%v", sid, err))
				}
			}
		}
	}
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
	// 正常完成也要收敛：后台子 agent（async）在 ai.done 时可能仍在跑，按
	// interrupted 终结（服务端 finalizeTurnActivity 同语义兜底）。
	finalizeRunSubagents(run, writeJSON)
	_ = writeJSON(done)
	return agentAIRunDone, agentAIEffortRejection{}
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
	// Per-message streamed-text buffer + last finalized text. Replaces the old
	// per-run `emitted` flag (which, once set by the first text_delta,永久抑制
	// 后续所有 finalized assistant 文本). Now: text_delta 积进 currentStreamed；
	// 每个 finalized assistant 事件只补发未流过的后缀并重置 currentStreamed
	// (按 message 边界去重)。result 事件对齐 lastFinalText 去重。这样工具型
	// 多段 assistant 回合里，即便某段文本只出现在 finalized 事件(未经
	// text_delta 流式)，也能实时作为 ai.delta 发出，不再被丢到结束才回拉。
	currentStreamed := ""
	lastFinalText := ""
	// pendingCommands tracks Bash tool_use_id → command so a later user-turn
	// tool_result can close it as ai.command(completed) with output + exit code.
	pendingCommands := map[string]string{}
	// claudeBlockTypes tracks content_block index → block type (thinking/text/
	// tool_use) so content_block_stop can emit an explicit ai.thinking
	// active:false when a thinking span ends — giving the thinking activity a
	// reliable stop boundary (not only the run-terminal ai.done).
	claudeBlockTypes := map[int]string{}
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
		// Capture the provider's native assistant message id for the CURRENT
		// turn (claude message_start / finalized assistant). Streaming events
		// then carry it so each turn is a distinct transcript message + distinct
		// structured events — matching the post-refresh native-log view, instead
		// of the whole run collapsing into one assistant bubble + one growing
		// thinking event.
		if format == agentAIOutputClaudeStreamJSON {
			if mid := claudeStreamMessageID(event); mid != "" && mid != run.providerMsgID {
				run.providerMsgID = mid
			}
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
			// 子 agent 生命周期帧（system/task_*）→ 注册表 + ai.subagent 事件
			// + 看门狗喂食。放在 activity 配对之后、结构化发射之前，保证同一
			// 帧上 activity 状态先于 wire 事件就绪。
			handleClaudeTaskEvents(event, run, writeJSON)
		}
		emitClaudeStructuredEvents(format, event, run, writeJSON, pendingCommands, claudeBlockTypes)
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
		// Claude steer：第一个 result 事件意味着本 turn 的产出已定稿。
		// 关闭 stdin 让 CLI 自然退出（约 0.5s）；实测「已进入管道」的 steer
		// 仍会被 CLI 读完并执行，所以这里不必等待队列排空。放在错误分支之前，
		// 失败 result 同样要关闸，否则进程会一直等输入。
		if format == agentAIOutputClaudeStreamJSON && run.claudeSteer != nil &&
			remoteString(event, "type") == "result" {
			run.claudeSteer.closeInput()
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
					cause = claudeFailureCause(nil, ri, "")
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
		parts := extractStructuredAITexts(format, event)
		for _, d := range parts.deltas {
			// 空白 delta（独立的 "\n"、独立空格）也必须原样上线：丢掉它们会让
			// 落库文本缺字节——```aliang-options 围栏标签后的换行被吃（围栏
			// 残废）、token 边界随机吃空格（2026-10-26 RCA）。emitAIDelta 对
			// 真空串自行短路；currentStreamed 继续记账，suffix 去重不受影响。
			if !emitAIDelta(d, run, "assistant", writeJSON, limiter, capture) {
				return
			}
			currentStreamed += d
		}
		if parts.finalText != "" {
			// A finalized assistant message emits only the text not already
			// streamed for this message (suffix). The run result event is the
			// LAST assistant message's text — dedup against whatever was emitted
			// for it: the streamed deltas if it never finalized, else the
			// finalized text. Either way, never silently drop finalized text
			// (the old global-flag bug) nor double-emit already-streamed text.
			reference := currentStreamed
			if parts.isResult && currentStreamed == "" {
				reference = lastFinalText
			}
			if suffix := suffixNotStreamed(reference, parts.finalText); suffix != "" {
				if !emitAIDelta(suffix, run, "assistant", writeJSON, limiter, capture) {
					return
				}
			}
			if parts.isResult {
				lastFinalText = parts.finalText
			} else {
				currentStreamed = ""
				lastFinalText = parts.finalText
			}
		}
	}
}

func updateClaudeToolUseActivity(event map[string]interface{}, activity *agentAIActivity, pending map[string]struct{}) {
	if activity == nil || pending == nil {
		return
	}
	// 子 agent 内部的 tool_use/tool_result 带 parent_tool_use_id（实测 --print
	// stream-json 默认全量下发）。它们必须在这里止步：否则内部配对会双计
	// pendingToolUses、掩盖父 Agent 等待的真实边界。子 agent 活跃度由
	// handleClaudeTaskEvents 经 beginSubagent/subagentProgress 单独喂。
	if remoteString(event, "parent_tool_use_id") != "" {
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

// aiStreamTexts describes the user-visible text carried by one structured
// stream event, split into two channels that the run loop dedups differently:
//
//   - deltas    — streaming text_delta fragments. Always emitted as they
//     arrive, and accumulated into the current message's streamed buffer.
//   - finalText — the finalized assistant message's full text (a message
//     boundary). Emitted only as the un-streamed suffix (see suffixNotStreamed)
//     so messages whose text arrived only in the finalized event — never as
//     text_delta partials — are still delivered in real time.
//
// This replaces the old global per-run `emitted`/`allowFinal` flag, which once
// the first text_delta set it permanently suppressed every later finalized
// assistant message, dropping its text on the wire (only recoverable later by
// the server's end-of-run session-detail pull). isResult marks the run's
// terminal result event (deduped against the last assistant message instead
// of resetting the per-message buffer).
type aiStreamTexts struct {
	deltas    []string
	finalText string
	isResult  bool
}

func extractStructuredAITexts(format agentAIOutputFormat, event map[string]interface{}) aiStreamTexts {
	switch format {
	case agentAIOutputClaudeStreamJSON:
		return extractClaudeStreamTexts(event)
	case agentAIOutputCodexJSON:
		return extractCodexJSONTexts(event)
	case agentAIOutputOpenCodeJSON:
		return extractOpenCodeJSONTexts(event)
	default:
		return aiStreamTexts{}
	}
}

// suffixNotStreamed returns the portion of final that was not already streamed.
//   - Empty final ⇒ nothing to emit.
//   - streamed already covers final (or more) ⇒ nothing.
//   - final extends streamed ⇒ the un-streamed tail.
//   - they diverge (neither is a prefix of the other) ⇒ final wholesale.
//   - Empty streamed ⇒ all of final (recovers finalized-only messages).
//
// 前缀比较失败时先做一次空白容忍对齐（逐 rune，双侧空白等价）：result 事件与
// finalized 文本的排版可能有空白差异——这些都不该触发整段重发。真分叉（出现
// 不同字符）仍保持旧行为整体重发，绝不静默丢字。
func suffixNotStreamed(streamed, final string) string {
	if final == "" {
		return ""
	}
	if strings.HasPrefix(streamed, final) {
		return ""
	}
	if strings.HasPrefix(final, streamed) {
		return final[len(streamed):]
	}
	streamedRunes := []rune(streamed)
	finalRunes := []rune(final)
	i, j := 0, 0
	for i < len(streamedRunes) && j < len(finalRunes) {
		switch {
		case unicode.IsSpace(streamedRunes[i]) && unicode.IsSpace(finalRunes[j]):
			for i < len(streamedRunes) && unicode.IsSpace(streamedRunes[i]) {
				i++
			}
			for j < len(finalRunes) && unicode.IsSpace(finalRunes[j]) {
				j++
			}
		case unicode.IsSpace(streamedRunes[i]):
			i++
		case unicode.IsSpace(finalRunes[j]):
			j++
		case streamedRunes[i] == finalRunes[j]:
			i++
			j++
		default:
			return final
		}
	}
	if i < len(streamedRunes) {
		// 流式侧还有最终文本没覆盖到的内容 → 真分叉，整体重发。
		return final
	}
	return string(finalRunes[j:])
}

func extractOpenCodeJSONTexts(event map[string]interface{}) aiStreamTexts {
	var out aiStreamTexts
	eventType := strings.ToLower(strings.TrimSpace(remoteString(event, "type")))
	props := openCodeEventProperties(event)
	if strings.Contains(eventType, "part.delta") {
		if text := firstNonBlankPreserveSpace(remoteString(props, "delta"), remoteString(event, "delta")); text != "" {
			out.deltas = []string{text}
			return out
		}
	}
	// Finalized (per-message) text — emitted by the loop as the un-streamed
	// suffix. Routing it here (instead of the old allowFinal-guarded return)
	// means a later opencode assistant message whose text arrives only in its
	// finalized event is no longer dropped by a global per-run flag.
	if part := firstOpenCodeMap(props["part"], event["part"]); part != nil {
		if text := openCodePartText(part); text != "" {
			out.finalText = text
			return out
		}
	}
	if message := firstOpenCodeMap(props["message"], event["message"]); message != nil {
		if role := strings.ToLower(strings.TrimSpace(remoteString(message, "role"))); role == "" || role == "assistant" {
			if texts := openCodeMessageTexts(message["parts"]); len(texts) > 0 {
				out.finalText = strings.Join(texts, "")
				return out
			}
			if text := firstNonBlankPreserveSpace(remoteString(message, "text"), remoteString(message, "content")); text != "" {
				out.finalText = text
				return out
			}
		}
	}
	if role := strings.ToLower(strings.TrimSpace(remoteString(event, "role"))); role == "assistant" {
		if texts := openCodeMessageTexts(event["parts"]); len(texts) > 0 {
			out.finalText = strings.Join(texts, "")
			return out
		}
		if text := firstNonBlankPreserveSpace(remoteString(event, "text"), remoteString(event, "content")); text != "" {
			out.finalText = text
			return out
		}
	}
	if strings.Contains(eventType, "message") || strings.Contains(eventType, "assistant") {
		if text := firstNonBlankPreserveSpace(remoteString(props, "text"), remoteString(props, "content"), remoteString(event, "text"), remoteString(event, "content")); text != "" {
			out.finalText = text
			return out
		}
	}
	return out
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

func extractClaudeStreamTexts(event map[string]interface{}) aiStreamTexts {
	var out aiStreamTexts
	// 子 agent 的内部文本（parented assistant / stream_event delta）不得混进
	// 父会话的 ai.delta——它们是子 agent 的自言自语，混进来会让用户看到两路
	// 交织的半截回复。父会话自身的帧 parent_tool_use_id 为 null，不受影响。
	if remoteString(event, "parent_tool_use_id") != "" {
		return out
	}
	if remoteString(event, "type") == "stream_event" {
		if streamEvent, ok := event["event"].(map[string]interface{}); ok && remoteString(streamEvent, "type") == "content_block_delta" {
			if delta, ok := streamEvent["delta"].(map[string]interface{}); ok && remoteString(delta, "type") == "text_delta" {
				if text := remoteString(delta, "text"); text != "" {
					out.deltas = []string{text}
				}
			}
		}
		return out
	}
	switch remoteString(event, "type") {
	case "assistant":
		if message, ok := event["message"].(map[string]interface{}); ok {
			// Claude emits a synthetic assistant message (model "<synthetic>",
			// carrying the error text) on terminal failure. Streaming it as a
			// normal reply masks the failure — skip it; the result event (or the
			// CLI exit) surfaces the failure as ai.error.
			if remoteString(message, "model") == "<synthetic>" {
				return out
			}
			if text := claudeMessageContentText(message["content"]); text != "" {
				out.finalText = text
			}
		}
	case "result":
		// A failed result (is_error / error_* subtype) must NOT be emitted as an
		// assistant delta — that masks the failure as a successful reply. The
		// streaming loop surfaces it as ai.error via claudeResultError.
		if _, isErr := claudeResultError(event); isErr {
			return out
		}
		if text := remoteString(event, "result"); text != "" {
			// 斜杠命令（/compact 等）的 headless 结果会被 CLI 包在
			// <local-command-stdout> 等标记里；标记不是给用户看的内容。
			if cleaned := stripLocalCommandMarkup(text); cleaned != "" {
				out.finalText = cleaned
				out.isResult = true
			}
		}
	}
	return out
}

// stripLocalCommandMarkup 去掉 CLI 斜杠命令输出的成对标记，保留其中的正文。
//
// headless 下 /compact 之类命令的结果会被包成
// <local-command-stdout>…</local-command-stdout>；<local-command-caveat> 块
// 是给模型的提示语，整体丢弃。标记缺失/未闭合时按容错处理，不吞正文。
func stripLocalCommandMarkup(text string) string {
	text = stripLocalCommandTag(text, "<local-command-caveat>", "</local-command-caveat>", true)
	text = stripLocalCommandTag(text, "<local-command-stdout>", "</local-command-stdout>", false)
	text = stripLocalCommandTag(text, "<local-command-stderr>", "</local-command-stderr>", false)
	return strings.TrimSpace(text)
}

func stripLocalCommandTag(text, open, close string, dropInner bool) string {
	var b strings.Builder
	rest := text
	for {
		i := strings.Index(rest, open)
		if i < 0 {
			b.WriteString(rest)
			return b.String()
		}
		j := strings.Index(rest[i+len(open):], close)
		b.WriteString(rest[:i])
		if j < 0 {
			b.WriteString(rest[i+len(open):])
			return b.String()
		}
		j += i + len(open)
		if !dropInner {
			b.WriteString(rest[i+len(open) : j])
		}
		rest = rest[j+len(close):]
	}
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
	msgID := streamingMessageID(run)
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
		case "Agent", "Task":
			// 老 CC（无 task_started 帧）兜底：仅登记不发射——实测新 CC 的
			// task_started 紧随 assistant 帧到达，此刻发射必然与真 started
			// 撞出重复行。登记后看门狗照常知情；终态由 claudeToolResultEvents
			// 在无父标记 tool_result 到达时补发，run 拆除时 finalize 兜底。
			if run.subagents != nil && id != "" {
				run.subagents.startLegacy(id, msgID, remoteString(input, "description"), remoteString(input, "subagent_type"), time.Now())
			}
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
	// pendingCommands 为空也不能早退：老 CC 兜底登记的子 agent 终态走这里
	// （无父标记 tool_result），与 Bash pending 无关。
	if len(pending) == 0 && run.subagents == nil {
		return nil
	}
	msgID := streamingMessageID(run)
	var out []map[string]interface{}
	for _, raw := range items {
		row, ok := raw.(map[string]interface{})
		if !ok || remoteString(row, "type") != "tool_result" {
			continue
		}
		id := remoteString(row, "tool_use_id")
		// 老 CC 兜底登记的子 agent：无父标记的 tool_result（"[Subagent
		// hand-back]" 或 async_launched）就是它的终态信号——此刻补发
		// completed/failed，摘要用返回文本（截断）。
		if run.subagents != nil && run.subagents.legacyStarted(id) {
			phase := "completed"
			if isErr, ok := row["is_error"].(bool); ok && isErr {
				phase = "failed"
			}
			if record, ok := run.subagents.finish(id, phase, claudeToolResultText(row["content"]), time.Now()); ok {
				run.activity.endSubagent()
				out = append(out, agentAISubagentPayload(run.sessionID, record))
			}
			continue
		}
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
// streamFloatIndex parses a claude stream-json content_block "index" (a JSON
// number, decoded by encoding/json as float64) into an int. Returns
// (0,false) if absent or not a number.
func streamFloatIndex(raw interface{}) (int, bool) {
	f, ok := raw.(float64)
	if !ok {
		return 0, false
	}
	return int(f), true
}

func emitClaudeStructuredEvents(format agentAIOutputFormat, event map[string]interface{}, run agentAIRun, writeJSON agentTerminalWriter, pending map[string]string, blockTypes map[int]string) {
	if format != agentAIOutputClaudeStreamJSON {
		return
	}
	// 子 agent 内部帧（parented）的全部结构化内容——thinking delta、tool_use、
	// usage、tool_result——都不得以父会话名义上 wire。子 agent 生命周期走
	// handleClaudeTaskEvents 的 ai.subagent 专用通道。父会话帧
	// parent_tool_use_id 为 null，不受影响。
	if remoteString(event, "parent_tool_use_id") != "" {
		return
	}
	msgID := streamingMessageID(run)

	if remoteString(event, "type") == "stream_event" {
		streamEvent := mapIf(event["event"])
		if streamEvent != nil {
			switch remoteString(streamEvent, "type") {
			case "content_block_start":
				// Record this block's type so its content_block_stop can tell
				// whether a thinking span ended.
				if blockTypes != nil {
					if idx, ok := streamFloatIndex(streamEvent["index"]); ok {
						if cb := mapIf(streamEvent["content_block"]); cb != nil {
							blockTypes[idx] = remoteString(cb, "type")
						}
					}
				}
			case "content_block_stop":
				// A thinking span ending emits an explicit active:false so the
				// activity headline can leave "思考中" the moment the model
				// moves on — not only at run-terminal ai.done. The next
				// thinking_delta re-activates it (server-side merge). item_id is
				// the block index → this thinking block's event_id
				// (type+session+message+item) is distinct per block, so multiple
				// thinking blocks (across turns or within one) don't collapse.
				if blockTypes != nil {
					if idx, ok := streamFloatIndex(streamEvent["index"]); ok {
						if blockTypes[idx] == "thinking" {
							_ = writeJSON(map[string]interface{}{
								"type":       models.AgentEventAIThinking,
								"session_id": run.sessionID,
								"message_id": msgID,
								"item_id":    fmt.Sprintf("tb%d", idx),
								"active":     false,
							})
						}
						delete(blockTypes, idx)
					}
				}
			case "content_block_delta":
				if delta := mapIf(streamEvent["delta"]); delta != nil && remoteString(delta, "type") == "thinking_delta" {
					if text := remoteString(delta, "thinking"); strings.TrimSpace(text) != "" {
						// 思考流也算实打实的进展（与 TUI 路径同一口径）。不算
						// 的话，一段超过 idle 窗口（10 分钟）的纯推理在宿主看来
						// 就是「没有任何动静」，会被当卡死杀掉——模型明明在
						// 正常思考，用户却收到中断。
						run.activity.markProgress()
						idx, _ := streamFloatIndex(streamEvent["index"])
						_ = writeJSON(map[string]interface{}{
							"type":       models.AgentEventAIThinking,
							"session_id": run.sessionID,
							"message_id": msgID,
							"item_id":    fmt.Sprintf("tb%d", idx),
							"delta":      text,
						})
					}
				}
			}
		}
		return
	}

	switch remoteString(event, "type") {
	case "system":
		// 上下文压缩完成帧：CLI 的 /compact 与自动压缩都会发这一帧。把结果翻译
		// 成一行可见提示，否则手机端 /compact 会安静地「什么都没发生」。
		if notice := claudeCompactNotice(event); notice != "" {
			_ = writeJSON(map[string]interface{}{
				"type":       models.AgentEventAIDelta,
				"session_id": run.sessionID,
				"message_id": msgID,
				"channel":    "stdout",
				"delta":      notice,
			})
		}
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

// claudeCompactNotice 把 CLI 的 system/compact_boundary 帧翻译成一行可见提示。
//
// 帧形状（2.1.x 实测 + CLI 源码常量）：type=system、subtype=compact_boundary、
// compact_metadata={trigger:"manual"|"auto", pre_tokens, post_tokens}。注意
// pre/post_tokens 在不同版本里既可能是 JSON 数字也可能是字符串，两种都认。
// 非压缩帧返回空串。
func claudeCompactNotice(event map[string]interface{}) string {
	if remoteString(event, "type") != "system" || remoteString(event, "subtype") != "compact_boundary" {
		return ""
	}
	meta := mapIf(event["compact_metadata"])
	if meta == nil {
		meta = mapIf(event["compactMetadata"])
	}
	trigger := remoteString(meta, "trigger")
	pre := claudeCompactTokenValue(meta["pre_tokens"])
	if pre <= 0 {
		pre = claudeCompactTokenValue(meta["preTokens"])
	}
	post := claudeCompactTokenValue(meta["post_tokens"])
	if post <= 0 {
		post = claudeCompactTokenValue(meta["postTokens"])
	}
	switch {
	case pre > 0 && post > 0:
		return fmt.Sprintf("✓ 上下文已压缩：约 %s → %s tokens。\n",
			agentAITokenCountLabel(pre), agentAITokenCountLabel(post))
	case pre > 0:
		return fmt.Sprintf("✓ 上下文已压缩（压缩前约 %s tokens）。\n", agentAITokenCountLabel(pre))
	case trigger == "auto":
		return "✓ 上下文已自动压缩。\n"
	default:
		return "✓ 上下文已压缩。\n"
	}
}

// claudeCompactTokenValue 解析 compact_metadata 里的 token 数（数字或字符串）。
func claudeCompactTokenValue(raw interface{}) int {
	switch v := raw.(type) {
	case float64:
		return int(v)
	case int:
		return v
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return 0
		}
		return n
	default:
		return 0
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
// for a non-git directory or any git error. Only called from the user-initiated
// project probe — never from a background tick (folder access must stay tied
// to a user action so macOS does not raise Files & Folders prompts on its own).
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
// non-git directory or any git error. Only called from the user-initiated
// project probe — never from a background tick.
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

func extractCodexJSONTexts(event map[string]interface{}) aiStreamTexts {
	var out aiStreamTexts
	eventType := remoteString(event, "type")
	if strings.Contains(eventType, "delta") {
		if text := firstNonEmpty(remoteString(event, "delta"), remoteString(event, "text")); text != "" {
			out.deltas = []string{text}
			return out
		}
		if item, ok := event["item"].(map[string]interface{}); ok && remoteString(item, "type") == "agent_message" {
			if text := firstNonEmpty(remoteString(item, "delta"), remoteString(item, "text")); text != "" {
				out.deltas = []string{text}
				return out
			}
		}
	}
	if item, ok := event["item"].(map[string]interface{}); ok && remoteString(item, "type") == "agent_message" {
		if text := remoteString(item, "text"); text != "" {
			out.finalText = text
		}
	}
	if eventType == "agent_message" {
		if text := remoteString(event, "text"); text != "" {
			out.finalText = text
		}
	}
	return out
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
	// Assistant text/thinking is real progress, not just activity.
	run.activity.markProgress()
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
		"message_id": streamingMessageID(run),
		"channel":    channel,
		"delta":      text,
	})
	if limiter != nil && limiter.Exceeded() {
		return false
	}
	return true
}

// serializeGoalContext converts the structured goal_context envelope (sent by the
// server in ai.run.start) into a readable text block for the AI prompt. Without
// this, Claude only sees the task title and has no idea what to produce or what
// checks will verify — causing repeated blind failures.
func serializeGoalContext(raw interface{}) string {
	if raw == nil {
		return ""
	}
	gc, ok := raw.(map[string]interface{})
	if !ok {
		return ""
	}
	var b strings.Builder
	b.WriteString("=== Goal Context (authoritative) ===")

	// Objective + constraints
	if goal, ok := gc["goal"].(map[string]interface{}); ok {
		if obj, ok := goal["objective"].(string); ok && strings.TrimSpace(obj) != "" {
			b.WriteString("\nObjective: ")
			b.WriteString(obj)
		}
		if rev, ok := goal["revisionNumber"]; ok {
			b.WriteString(fmt.Sprintf("\nRevision: %v", rev))
		}
		if attempt, ok := goal["taskAttempt"]; ok {
			b.WriteString(fmt.Sprintf("\nAttempt: %v", attempt))
		}
		// Constraints / non-goals are the approved contract the executor must
		// honor. The server ships them in the envelope; rendering them here stops
		// the executor from drifting past the agreed scope.
		if cons, ok := goal["constraints"].([]interface{}); ok && len(cons) > 0 {
			b.WriteString("\nConstraints:")
			for _, c := range cons {
				if s, ok := c.(string); ok && strings.TrimSpace(s) != "" {
					b.WriteString("\n  - ")
					b.WriteString(s)
				}
			}
		}
		if ngs, ok := goal["nonGoals"].([]interface{}); ok && len(ngs) > 0 {
			b.WriteString("\nNon-goals (do NOT do these):")
			for _, c := range ngs {
				if s, ok := c.(string); ok && strings.TrimSpace(s) != "" {
					b.WriteString("\n  - ")
					b.WriteString(s)
				}
			}
		}
	}

	// Task details — THE critical part
	if task, ok := gc["task"].(map[string]interface{}); ok {
		b.WriteString("\n\n--- Current Task ---")
		if key, ok := task["key"].(string); ok {
			b.WriteString("\nKey: ")
			b.WriteString(key)
		}
		if title, ok := task["title"].(string); ok {
			b.WriteString("\nTitle: ")
			b.WriteString(title)
		}
		if desc, ok := task["description"].(string); ok && strings.TrimSpace(desc) != "" {
			b.WriteString("\nDescription: ")
			b.WriteString(desc)
		}
		if roots, ok := task["allowedRoots"].([]interface{}); ok && len(roots) > 0 {
			b.WriteString("\nAllowed roots: ")
			for i, r := range roots {
				if i > 0 {
					b.WriteString(", ")
				}
				b.WriteString(fmt.Sprintf("%v", r))
			}
		}
		if cmds, ok := task["allowedCommands"].([]interface{}); ok && len(cmds) > 0 {
			b.WriteString("\nAllowed commands: ")
			for i, c := range cmds {
				if i > 0 {
					b.WriteString(", ")
				}
				b.WriteString(fmt.Sprintf("%v", c))
			}
		}
		// Required checks — tells the AI what must pass AND what to produce.
		// Render the check definition (command/path/contains), not just the
		// name, so the executor knows exactly what condition to satisfy.
		if checks, ok := task["requiredChecks"].([]interface{}); ok && len(checks) > 0 {
			b.WriteString("\n\nRequired verification checks (your work must satisfy these):")
			for _, c := range checks {
				check, ok := c.(map[string]interface{})
				if !ok {
					continue
				}
				b.WriteString("\n  - ")
				if ck, ok := check["key"].(string); ok && ck != "" {
					b.WriteString(ck)
				}
				if ct, ok := check["type"].(string); ok && ct != "" {
					b.WriteString(" (" + ct + ")")
				}
				if cmd, ok := check["command"].(string); ok && strings.TrimSpace(cmd) != "" {
					b.WriteString("\n      command: " + strings.TrimSpace(cmd))
				}
				if p, ok := check["path"].(string); ok && strings.TrimSpace(p) != "" {
					b.WriteString("\n      path: " + strings.TrimSpace(p))
				}
				if con, ok := check["contains"].(string); ok && strings.TrimSpace(con) != "" {
					b.WriteString("\n      must contain: " + strings.TrimSpace(con))
				}
			}
		}
	}

	// Queued inputs (user messages that clarify the task)
	if inputs, ok := gc["queuedInputs"].([]interface{}); ok && len(inputs) > 0 {
		b.WriteString("\n\n--- Queued Inputs ---")
		for _, inp := range inputs {
			if input, ok := inp.(map[string]interface{}); ok {
				if content, ok := input["content"].(string); ok && strings.TrimSpace(content) != "" {
					b.WriteString("\n  ")
					b.WriteString(strings.TrimSpace(content))
				}
			}
		}
	}

	// Plan overview: every task with state + deps so the executor sees the big
	// picture (what's done, what's next, what interfaces downstream tasks need).
	if plan, ok := gc["plan"].([]interface{}); ok && len(plan) > 0 {
		b.WriteString("\n\n--- Plan (big picture) ---")
		for _, p := range plan {
			pt, ok := p.(map[string]interface{})
			if !ok {
				continue
			}
			key, _ := pt["key"].(string)
			title, _ := pt["title"].(string)
			b.WriteString(fmt.Sprintf("\n  - %s %s", key, title))
			if state, _ := pt["state"].(string); state != "" {
				b.WriteString(" [" + state + "]")
			}
			if deps, ok := pt["dependsOn"].([]interface{}); ok && len(deps) > 0 {
				b.WriteString(" after:")
				for i, d := range deps {
					if i > 0 {
						b.WriteString(",")
					}
					b.WriteString(fmt.Sprintf(" %v", d))
				}
			}
			if n, ok := pt["requiredChecks"]; ok {
				b.WriteString(fmt.Sprintf(" (%v checks)", n))
			}
		}
	}
	// Goal progress: distance-to-goal + recent failures. Breaks the repeated
	// blind-failure loop (failedTaskOutputs reach the executor only here).
	if gp, ok := gc["goalProgress"].(map[string]interface{}); ok {
		b.WriteString("\n\n--- Goal Progress ---")
		b.WriteString(fmt.Sprintf("\nCompleted: %v / %v", gp["completedTasks"], gp["totalTasks"]))
		if rem, ok := gp["remainingTasks"].([]interface{}); ok && len(rem) > 0 {
			b.WriteString("\nRemaining:")
			for _, r := range rem {
				if rm, ok := r.(map[string]interface{}); ok {
					k, _ := rm["key"].(string)
					t, _ := rm["title"].(string)
					b.WriteString(fmt.Sprintf("\n  - %s %s", k, t))
				}
			}
		}
		if fails, ok := gp["recentFailures"].([]interface{}); ok && len(fails) > 0 {
			b.WriteString("\nRecent failures (do not repeat these):")
			for _, f := range fails {
				if fm, ok := f.(map[string]interface{}); ok {
					tk, _ := fm["taskKey"].(string)
					summary, _ := fm["summary"].(string)
					b.WriteString(fmt.Sprintf("\n  - %s: %s", tk, summary))
				}
			}
		}
	}
	// Budget: rendered generically (shape is opaque to the agent). Lets the
	// executor see the remaining token/time envelope and self-throttle.
	if budget, ok := gc["budget"]; ok && budget != nil {
		if bj, err := json.Marshal(budget); err == nil {
			if s := string(bj); s != "null" && s != "{}" {
				b.WriteString("\n\n--- Budget ---")
				b.WriteString("\n" + s)
			}
		}
	}

	b.WriteString("\n=== End Goal Context ===")
	return b.String()
}

func buildAgentAIPrompt(session *agentAISession, latestContent string) string {
	history := trimAgentAIHistoryForSession(session, append([]agentAIMessage(nil), session.history...))
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

// trimAgentAIHistory 是默认档：最多 16 条 / 64000 字符。
//
// 这档上限服务的是「宿主历史只用来拼初始 prompt 或生成会话摘要」的 provider
// ——claude/codex 的上下文在 CLI 自己的会话里，宿主历史短一点无所谓。
func trimAgentAIHistory(history []agentAIMessage) []agentAIMessage {
	return trimAgentAIHistoryWithLimits(history, agentAIHistoryDefaultMessages, agentAIHistoryDefaultChars)
}

const (
	agentAIHistoryDefaultMessages = 16
	agentAIHistoryDefaultChars    = 64000
)

// agentAIHistoryLimits 返回某个会话应保留的历史上限。
//
// 内置内核（agentcore）直接消费这份历史：固定 16 条 / 64k 字符会把「手机上
// 选了大窗口」变成一句空话——16 条消息远到不了 100k token，长对话在宿主这
// 一层就被截断，内核的自动压缩永远不会触发。所以内核会话按选定窗口换算，
// 其余 provider 维持原值（它们的上下文由 CLI 自己管理）。
func agentAIHistoryLimits(provider string, contextWindow int) (maxMessages, maxChars int) {
	if !agentAIUseAgentCore(provider) {
		return agentAIHistoryDefaultMessages, agentAIHistoryDefaultChars
	}
	contextWindow = clampAgentAIContextWindow(contextWindow)
	if contextWindow <= 0 {
		// 与内核 DefaultContextWindow 对齐：手机没传窗口时内核按 200k 压缩，
		// 宿主先把历史截到 64k 字符会让这个 200k 永远用不满。
		contextWindow = agentAIContextWindowDefault
	}
	// 1 token ≈ 2 字符是保守换算：CJK 约 1 字符/token，ASCII 约 4 字符/token。
	// 宁可多留一点让内核按真实估算去压，也不要在宿主层过早截断。
	maxChars = contextWindow * 2
	maxMessages = contextWindow / 100
	if maxMessages < agentAIHistoryDefaultMessages {
		maxMessages = agentAIHistoryDefaultMessages
	}
	return maxMessages, maxChars
}

// trimAgentAIHistoryForSession 按会话选定的 provider/窗口裁剪历史。
func trimAgentAIHistoryForSession(session *agentAISession, history []agentAIMessage) []agentAIMessage {
	if session == nil {
		return trimAgentAIHistory(history)
	}
	maxMessages, maxChars := agentAIHistoryLimits(session.provider, session.contextWindow)
	return trimAgentAIHistoryWithLimits(history, maxMessages, maxChars)
}

func trimAgentAIHistoryWithLimits(history []agentAIMessage, maxMessages, maxChars int) []agentAIMessage {
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
	return newClaudeCodeAIToolForOS(runtime.GOOS, id, path, prompt, model, effort, resumeSessionID, newSessionID...)
}

func newClaudeCodeAIToolForOS(goos string, id string, path string, prompt string, model string, effort string, resumeSessionID string, newSessionID ...string) *agentAITool {
	return newClaudeCodeAIToolForOSWithInput(goos, id, path, prompt, model, effort, resumeSessionID,
		claudeStreamJSONInputAvailable(path), newSessionID...)
}

// newClaudeCodeAIToolForOSWithInput 是 newClaudeCodeAIToolForOS 的可注入版本：
// streamInput 为 true 时启用 `--input-format stream-json`，prompt 改由
// runCLIPass 写入常驻 stdin 管道（这是 Claude steer 的物理前提）；为 false
// 时保持历史行为（prompt 走 argv，或 Windows .cmd shim 走一次性 stdin）。
func newClaudeCodeAIToolForOSWithInput(goos string, id string, path string, prompt string, model string, effort string, resumeSessionID string, streamInput bool, newSessionID ...string) *agentAITool {
	args := claudeCodeHeadlessSlimArgs()
	args = append(args, "--print", "--verbose", "--output-format", "stream-json", "--include-partial-messages")
	if streamInput {
		// 双向 stream-json：stdin 保持打开，后续 steer 以 JSON 行追加。
		args = append(args, "--input-format", "stream-json")
	}
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
	promptViaStdin := claudePromptViaStdinForOS(goos, path)
	if !streamInput && !promptViaStdin {
		args = append(args, prompt)
	}
	tool := &agentAITool{
		id:           id,
		path:         path,
		args:         args,
		outputFormat: agentAIOutputClaudeStreamJSON,
	}
	// stdinPrompt 在两条 stdin 路径上都保存原始 prompt：附件/只读等包装器
	// 靠它判断「末位不是位置参数」，并把附件清单拼进同一份 stdin 文本。
	if streamInput || promptViaStdin {
		tool.stdinPrompt = prompt
	}
	if streamInput {
		tool.stdinStreamJSON = true
	}
	return tool
}

var claudeAutoCompactProbeCache sync.Map

// claudeAutoCompactAvailable reports whether this claude binary understands
// `--autocompact`. Probe failure counts as unsupported: an unknown flag would
// make every turn fail to start, so an older CLI keeps its built-in window.
// The cache key includes symlink target + mtime + size
// (executableProbeCacheKey), so an upgrade invalidates it automatically.
func claudeAutoCompactAvailable(path string) bool {
	path = strings.TrimSpace(path)
	if path == "" {
		return false
	}
	cacheKey := executableProbeCacheKey(path)
	if cached, ok := claudeAutoCompactProbeCache.Load(cacheKey); ok {
		return cached.(bool)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	output, err := newBackgroundCommandContext(ctx, path, "--help").CombinedOutput()
	available := err == nil && strings.Contains(string(output), "--autocompact")
	claudeAutoCompactProbeCache.Store(cacheKey, available)
	return available
}

// withClaudeAutoCompact pins the CLI's auto-compaction window to the context
// size chosen on the phone.
//
// Without it the CLI derives the window from the model name (a "[1m]" tag
// claims 1M tokens even when the gateway window is ~262k), and compaction then
// fires too late — the request dies with "Prompt is too long" instead. The flag
// takes tokens directly (100k–1M). Old binaries silently skip it.
func withClaudeAutoCompact(tool *agentAITool, contextWindow int) *agentAITool {
	if tool == nil || len(tool.args) == 0 {
		return tool
	}
	if tool.outputFormat != agentAIOutputClaudeStreamJSON {
		return tool
	}
	contextWindow = clampAgentAIContextWindow(contextWindow)
	if contextWindow <= 0 || !claudeAutoCompactAvailable(tool.path) {
		return tool
	}
	copied := *tool
	copied.args = append([]string(nil), tool.args...)
	flags := []string{"--autocompact", strconv.Itoa(contextWindow)}
	// prompt 走 stdin 时 args 末位不是位置参数：flags 直接追加尾部。
	if copied.stdinPrompt != "" {
		copied.args = append(copied.args, flags...)
		return &copied
	}
	promptIndex := len(copied.args) - 1
	copied.args = append(copied.args[:promptIndex], append(flags, copied.args[promptIndex])...)
	return &copied
}

// claudePromptViaStdinForOS 报告该平台/路径下 prompt 是否必须走 stdin 而非
// argv。Windows 批处理 shim(.cmd/.bat)经 cmd.exe /c 调起：即使 Go 已按 cmd.exe
// 规则转义，%VAR% 展开与引号/脱字符的边缘序列仍会重切或污染自由文本参数——
// 用户 prompt 不可信，不允许进 argv。claude --print 原生支持从 stdin 读提示词，
// 语义与位置参数完全一致。
func claudePromptViaStdinForOS(goos, path string) bool {
	if goos != "windows" {
		return false
	}
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".cmd" || ext == ".bat"
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
		case agentAIOutputClaudeStreamJSON:
			// 图片走 stdin 的 image block（buildClaudeImageBlocks），不再列进
			// prompt 的附件清单——同一张图既给 block 又给路径会让模型重复解读。
			if attachment.Type == "image" && attachment.Path != "" && tool.stdinStreamJSON {
				attachedPaths[attachment.Path] = struct{}{}
			}
		}
	}
	remaining := make([]agentAIAttachment, 0, len(attachments))
	for _, attachment := range attachments {
		if _, attached := attachedPaths[attachment.Path]; !attached {
			remaining = append(remaining, attachment)
		}
	}
	// prompt 走 stdin 时附件清单必须拼进 stdinPrompt——拼到 args 末位会把
	// 附件文本叠到最后一个 flag 值上（--resume id / --effort 等）。
	if tool.stdinPrompt != "" {
		copied.stdinPrompt = tool.stdinPrompt + agentAIAttachmentPromptSuffix(remaining, true)
		copied.args = append(copied.args, extraArgs...)
		return &copied
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
	var texts []agentAIAttachment
	for _, attachment := range attachments {
		// 内联文本素材没有位置可列，正文直接内联（见下）。带 Path 的
		// Type=="text" 是「文本文件」，仍然走位置清单。
		if attachment.Text != "" {
			if strings.TrimSpace(attachment.Text) != "" {
				texts = append(texts, attachment)
			}
			continue
		}
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
	var b strings.Builder
	if len(lines) > 0 {
		b.WriteString("\n\nAttachments available for this request:\n")
		b.WriteString(strings.Join(lines, "\n"))
	}
	for _, attachment := range texts {
		name := firstNonEmpty(attachment.Name, "attachment.txt")
		// 用围栏包住正文：模型按等宽读，ASCII 图的列对齐才不会被当成普通段落
		// 重排。围栏长度取正文里最长反引号串 +1，避免正文自带 ``` 提前闭合。
		fence := strings.Repeat("`", longestBacktickRun(attachment.Text)+1)
		b.WriteString("\n\nAttachment ")
		b.WriteString(strconv.Quote(name))
		b.WriteString(" (text):\n")
		b.WriteString(fence)
		b.WriteString("\n")
		b.WriteString(attachment.Text)
		b.WriteString("\n")
		b.WriteString(fence)
	}
	return b.String()
}

// longestBacktickRun 返回 s 中最长的连续反引号长度（用于挑一个不会提前闭合的
// Markdown 围栏）。至少为 2，保证围栏永远是 ``` 起。
func longestBacktickRun(s string) int {
	longest, current := 0, 0
	for _, r := range s {
		if r == '`' {
			current++
			if current > longest {
				longest = current
			}
			continue
		}
		current = 0
	}
	if longest < 2 {
		return 2
	}
	return longest
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

// 信任档位(sanitized/full)放行的最低 claude 版本:官方在 2.1.268 修复
// headless --print 不触发 PermissionRequest 的缺陷(CHANGELOG 2.1.268)。
// 档位守卫(applyClaudeTierVersionGuard)、审批钩子策略选择(本文件)与
// slash 列表门(agent_slash_commands.go)三处必须共用此常量——2026-10-16
// 评审曾发现守卫放宽而钩子策略仍卡旧阈值的三门漂移(full 档加载用户 ask
// 规则却接 PreToolUse 桥,手机批准后仍被拒)。旧值 2.2.0 从未发布,是永假门。
const (
	claudeTierMinMajor = 2
	claudeTierMinMinor = 1
	claudeTierMinPatch = 268
	// claudeKnownGood* 及以上修复了 <2.1.289 的审批绕过缺口(复合命令嵌套/
	// env 前缀展开等),更早版本放行信任档时告警建议升级。
	claudeKnownGoodMajor = 2
	claudeKnownGoodMinor = 1
	claudeKnownGoodPatch = 289
)

func claudeApprovalHookStrategyForVersion(raw string) claudeApprovalHookStrategy {
	version, ok := parseClaudeCodeVersion(raw)
	// 2.1.268 前的构建不触发 PermissionRequest(2.1.17 验证),只响应
	// PreToolUse 命令钩子;2.1.268 起遵循现行 PermissionRequest/http 契约。
	if ok && version.atLeast(claudeTierMinMajor, claudeTierMinMinor, claudeTierMinPatch) {
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
	raw, ok := probeClaudeRawVersion(toolPath)
	if !ok {
		return claudeApprovalHookPreToolUseCommand
	}
	return claudeApprovalHookStrategyForVersion(raw)
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

// claudeAuthEnvOverlay returns the gateway auth keys headless claude runs need
// but did not inherit, sourced from the user's <agentHome>/.claude/settings.json
// env block (the canonical token location since quick_setup fa84f42). Only keys
// missing (or empty) in the effective child-process environment are returned —
// an env-provided value always wins. Isolated-tier runs pass --setting-sources ""
// so claude never loads that settings file itself; a GUI-spawned agent carries no
// shell-profile exports, so without this overlay its headless children have zero
// credentials and claude fails locally with "Please run /login". Missing or
// unparseable settings yield no overlay (tolerated, matching claudeEnabledPlugins'
// fail-closed posture); only the two auth keys are ever copied.
func claudeAuthEnvOverlay() map[string]string {
	authKeys := []string{"ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL"}
	missing := make(map[string]bool, len(authKeys))
	for _, key := range authKeys {
		if value, ok := agentChildProcessEnvValue(key); !ok || value == "" {
			missing[key] = true
		}
	}
	if len(missing) == 0 {
		return nil
	}
	home := agentHome()
	if home == "" {
		return nil
	}
	settingsPath := filepath.Join(home, ".claude", "settings.json")
	raw, err := os.ReadFile(settingsPath)
	if err != nil {
		return nil // absent/unreadable settings: nothing to fall back to (silent)
	}
	var settings struct {
		Env map[string]string `json:"env"`
	}
	if err := json.Unmarshal(raw, &settings); err != nil {
		logger.Warn(fmt.Sprintf("claude-auth: unparseable settings %s: %v", settingsPath, err))
		return nil
	}
	overlay := make(map[string]string, len(missing))
	for _, key := range authKeys {
		if !missing[key] {
			continue
		}
		if value := strings.TrimSpace(settings.Env[key]); value != "" {
			overlay[key] = value
		}
	}
	if len(overlay) == 0 {
		return nil
	}
	return overlay
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
	// Full tier runs at local parity: it keeps its own settings sources and
	// must not carry the fail-closed shell disable or ask overlay (hooks stay).
	if run.claudePolicy.enabled && run.claudePolicy.trustTier != "full" {
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
	// 手机驱动回合(surface=phone):硬禁终端交互构件,与档位无关——本地 full
	// 档的"不打扰"约束针对本地用户;手机回合不存在本地旁观者。deny 让 CC
	// 直接拒绝 AskUserQuestion 并把原因回执给模型,模型自纠(配合 surface
	// 契约文本),无头模式不再静默悬死。
	if run.surface == agentAISurfacePhone {
		permissions, _ := settings["permissions"].(map[string]interface{})
		if permissions == nil {
			permissions = map[string]interface{}{}
			settings["permissions"] = permissions
		}
		deny, _ := permissions["deny"].([]string)
		permissions["deny"] = append(deny, "AskUserQuestion")
	}
	return settings, nil
}

func claudeApprovalHookCurlCommand(hookURL string) string {
	curl := claudeHookCurlWordForOS(runtime.GOOS, exec.LookPath)
	return fmt.Sprintf("%s -sS -X POST -H %q --data-binary @- %q", curl, "Content-Type: application/json", hookURL)
}

// claudeHookCurlWordForOS 把 curl 解析成 shell 安全的命令词。Windows 上钩子经
// Git Bash 执行（无则回退 PowerShell），而 PowerShell 5.1 的裸 curl 是
// Invoke-WebRequest 别名，-H/--data-binary 非法、钩子必败——System32 自带的
// curl.exe（Win10 1803+）是稳出口，解析绝对路径绕开别名。含空格的路径
// （Program Files 下的 Git Bash curl）在 bash 与 PowerShell 的引用/调用语义
// 无法用单一字符串两全，保持裸 curl——Git Bash 场景本就正常。非 Windows 零变化。
func claudeHookCurlWordForOS(goos string, lookPath func(string) (string, error)) string {
	if goos != "windows" {
		return "curl"
	}
	p, err := lookPath("curl")
	if err != nil {
		return "curl"
	}
	p = strings.TrimSpace(p)
	if p == "" || strings.ContainsAny(p, " \t") {
		return "curl"
	}
	// 正斜杠在 Git Bash 与 PowerShell 下都可执行；反斜杠在 bash 未引用时会
	// 被当转义符吞掉。不用 filepath.ToSlash——它按构建平台的分隔符处理，
	// darwin 上对反斜杠是 no-op，而此函数单测要在任意平台跑。
	return strings.ReplaceAll(p, `\`, `/`)
}

// marshalClaudeApprovalHookSettings 是 --settings JSON 的唯一序列化出口
// （withClaudeApprovalHook 与护栏测试共用）。必须保持 encoding/json 默认
// escapeHTML=true——它把 & < > 转成 \uXXXX，是 Windows .cmd 链路上挡住
// cmd.exe 元字符的关键防线之一（% ^ 无转义，靠内容自身约束，见
// agent_approval_hook_guard_test.go 的护栏）。改成 Encoder+SetEscapeHTML(false)
// 或手拼 JSON 会让字面 & 直达 cmd.exe；guard 测试会红，红时必须显式评审。
func marshalClaudeApprovalHookSettings(settings map[string]interface{}) ([]byte, error) {
	return json.Marshal(settings)
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
	settingsRaw, err := marshalClaudeApprovalHookSettings(settings)
	if err != nil {
		return tool
	}
	copied := *tool
	// Auth fallback rides the child environment, never argv: claudeAuthEnvOverlay
	// sources the gateway keys from <agentHome>/.claude/settings.json only for
	// keys the effective child env is missing, so cmd.Env gains no duplicates and
	// the token never appears in `ps`-visible command lines or the --settings
	// blob (which stays hooks-only and log-masked). Isolation is untouched —
	// --setting-sources still blocks all settings-file loading below.
	if overlay := claudeAuthEnvOverlay(); len(overlay) > 0 {
		keys := make([]string, 0, len(overlay))
		for key, value := range overlay {
			copied.env = append(copied.env, key+"="+value)
			keys = append(keys, key)
		}
		logger.Info(fmt.Sprintf("claude-auth: env overlay applied keys=%v session=%s runSeq=%d (values stay out of argv and logs)", keys, run.sessionID, run.runSeq))
	}
	sources := ""
	if run.claudePolicy.enabled {
		// Carry the effective tier's sources instead of unconditionally
		// clobbering to "" — the hook layer previously erased whatever tier
		// withClaudeRemotePolicy had configured (spec §7). Policy-disabled
		// runs keep the historical "" (isolated) behavior.
		sources = claudeTierSettingSources(run.claudePolicy.trustTier)
	}
	copied.args = withoutCLIArgumentValue(tool.args, "--setting-sources")
	copied.args = append([]string{"--setting-sources", sources, "--permission-mode", "default", "--settings", string(settingsRaw)}, copied.args...)
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
	case "", "auto", "codex", "claude", "claude code", "claudecode", "opencode", "open-code", "open_code", "provider default", "default":
		return ""
	default:
		return model
	}
}

func agentAICapabilities() []string {
	claudePath, hasClaude := lookPathCLI("claude")
	claudeCodePath, hasClaudeCode := lookPathCLI("claudecode")
	_, hasOpenCode := lookPathCLI("opencode")
	// Claude steer 依赖 `--input-format stream-json`；老世代 CLI 没有该 flag，
	// 探测失败即不宣称能力，服务端/手机会退回「运行中不可发消息」的旧行为。
	hasClaudeSteer := (hasClaude == nil && claudeStreamJSONInputAvailable(claudePath)) ||
		(hasClaudeCode == nil && claudeStreamJSONInputAvailable(claudeCodePath))
	// 外部 TUI 插话依赖 CC >= 2.1.224 写进 ~/.claude/sessions/<pid>.json 的
	// 跨会话 messaging UDS socket(MessagingSocketPath)。按 CLI 版本探测,与
	// stream-json 探测同模式(缓存随二进制内容失效)。
	hasClaudeExternalSteer := false
	if probePath := externalSteerProbePath(hasClaude == nil, claudePath, hasClaudeCode == nil, claudeCodePath); probePath != "" {
		if v, ok := probeClaudeCodeVersion(probePath); ok {
			hasClaudeExternalSteer = v.atLeast(2, 1, 224)
		}
	}
	return agentAICapabilitiesForTools(hasClaude == nil, hasClaudeCode == nil, codexAppServerAvailable(), hasOpenCode == nil, codexNativeGoalAvailable(), hasClaudeSteer, hasClaudeExternalSteer)
}

// externalSteerProbePath 挑做版本探测的 claude 二进制:优先 claude,退 claudecode。
func externalSteerProbePath(hasClaude bool, claudePath string, hasClaudeCode bool, claudeCodePath string) string {
	if hasClaude {
		return claudePath
	}
	if hasClaudeCode {
		return claudeCodePath
	}
	return ""
}

func agentAICapabilitiesForTools(hasClaude, hasClaudeCode, hasCodexAppServer, hasOpenCode, hasCodexNativeGoal, hasClaudeSteer, hasClaudeExternalSteer bool) []string {
	caps := []string{"ai_chat", "ai_chat_context", "ai_stream", "ai_approval", "ai_steer", "vibe_session"}
	if hasClaudeSteer {
		// 与 ai_steer（Codex app-server 通道）区分：老 agent 也会无条件宣称
		// ai_steer，但只有新 agent 才能真正对 Claude run 注入。
		caps = append(caps, "ai_steer_claude")
	}
	if hasClaudeExternalSteer {
		// 外部 TUI 会话(导入/桌面终端自发生长)的插话通道:经 CC 跨会话
		// messaging UDS 收件箱排队。与 ai_steer_claude(stream-json stdin,
		// 只覆盖 agent 自 spawn 的回合)互补。
		caps = append(caps, "ai_steer_external")
	}
	if hasClaude {
		caps = append(caps, "ai_provider_claude")
	}
	if hasClaudeCode || hasClaude {
		caps = append(caps, "ai_provider_claudecode")
	}
	if hasCodexAppServer {
		caps = append(caps, "ai_provider_codex", "ai_provider_codex_app_server")
	}
	// goal_codex_native_v1 intentionally NOT advertised: the native hybrid
	// driver lacks terminal-state validation (see docs/goal-master.md §五).
	// All goals (including Codex) use the Server-driven Phase 1 path until the
	// native conformance suite passes. hasCodexNativeGoal param retained for
	// the call signature; reuse it when re-enabling native.
	_ = hasCodexNativeGoal
	if hasOpenCode {
		caps = append(caps, "ai_provider_opencode_basic")
	}
	// 内置内核恒可用：它跑在本进程里，不依赖任何外部 CLI，也没有版本探测
	// 可言。所以不设 has* 参数——加一个恒为 true 的形参只会让 7 个调用点
	// 白改一遍。
	caps = append(caps, "ai_provider_agentcore")
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

// resolveAgentAICWD resolves the vibecoding create/lazy-create project path.
// Operator policy: the discovery snapshot is a display cache, NOT an execution
// ACL — "discovered" and "authorized" are different concerns (2026-10-12 RCA:
// a project whose only transcript fell out of the agentVibeSessionFileScanLimit
// window could never start a session from the phone, and cold projects could
// never re-enter the list without manual CLI activity). Any existing directory
// the user names is creatable; existence + symlink resolution stay enforced
// via cleanExistingAgentDirectory, and an empty path falls back to the user's
// home (bc961e5 semantics). The terminal cwd keeps its own confinement
// (resolveAgentAuthorizedCWD) — that surface is a separate decision, which is
// why this function deliberately does NOT route through
// resolveAgentCWDFallbackHome: that helper degrades to an existence check only
// when zero projects are synchronized and otherwise enforces the authorized
// list — the exact ACL semantics this surface rejects (2026-10-16 merge fix:
// the terminal-cwd branch briefly routed this through the guard, breaking
// TestResolveAgentAICWDAcceptsExistingDirOutsideAuthorizedList).
func resolveAgentAICWD(raw string) (string, error) {
	cwd := strings.TrimSpace(raw)
	if cwd == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		cwd = home
	}
	// Same confinement as the other phone-driven surfaces (see
	// resolveAgentCWDWithConsent), but still without the authorized-list ACL:
	// existence + symlink resolution + TCC consent only.
	return resolveAgentCWDWithConsent(cwd)
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
// claudeFailureCause builds a concise, phone-friendly cause string for a failed
// Claude turn. When retry info is present it describes the upstream error and
// how many retries were spent (e.g. "gateway 502 (server_error); retried
// 10/10"); otherwise it falls back to the CLI exit error, enriched with the
// first meaningful stderr line when one exists (flag rejections, auth errors
// etc. would otherwise surface as a bare "exit status 1"). Verbose
// diagnostics (the raw stderr) travel separately in the ai.error "detail"
// field, which the phone does not render by default.
func claudeFailureCause(waitErr error, ri claudeRetryInfo, stderr string) string {
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
		if essence := conciseCLIStderrCause(stderr); essence != "" {
			return fmt.Sprintf("Claude CLI exited: %v — %s", waitErr, essence)
		}
		return fmt.Sprintf("Claude CLI exited: %v", waitErr)
	}
	return "claude run failed"
}

// conciseCLIStderrCause 提炼 stderr 首个非空行(截 200 字符),给手机端一个
// 可读的失败原因;空白 stderr 返回空串,由调用方退回裸格式。
func conciseCLIStderrCause(stderr string) string {
	for _, line := range strings.Split(stderr, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		runes := []rune(line)
		if len(runes) > 200 {
			runes = runes[:200]
		}
		return string(runes)
	}
	return ""
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
	// Typed refusals carry a machine-readable code (e.g. tui_busy for a spawn
	// refused by the external-TUI precheck) so the server can surface a
	// precise reason instead of string-matching the message.
	var coded interface{ ErrorCode() string }
	if err != nil && errors.As(err, &coded) {
		if code := coded.ErrorCode(); code != "" {
			payload["error_code"] = code
		}
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

// streamingMessageID returns the message_id that streaming events for the
// CURRENT provider assistant turn should carry: the provider's native msg id
// when known (one per turn → each turn is a distinct transcript message and
// distinct structured events, matching the post-refresh native-log view),
// falling back to the legacy run-derived id when the native id hasn't been
// captured yet (or for providers/paths that don't emit it).
func streamingMessageID(run agentAIRun) string {
	if id := strings.TrimSpace(run.providerMsgID); id != "" {
		return id
	}
	return agentAssistantMessageID(run.messageID)
}

// claudeStreamMessageID returns the provider native assistant message id from a
// claude stream-json event — the finalized "assistant" event's message.id, or
// the message_start stream_event's message.id (available before any
// content_block_delta, so streaming events of the turn can carry it). Empty
// when the event carries none.
func claudeStreamMessageID(event map[string]interface{}) string {
	switch remoteString(event, "type") {
	case "stream_event":
		if se := mapIf(event["event"]); se != nil && remoteString(se, "type") == "message_start" {
			if m := mapIf(se["message"]); m != nil {
				return strings.TrimSpace(remoteString(m, "id"))
			}
		}
	case "assistant":
		if m := mapIf(event["message"]); m != nil {
			return strings.TrimSpace(remoteString(m, "id"))
		}
	}
	return ""
}
