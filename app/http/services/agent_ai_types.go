package services

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"aliang.one/nursorgate/app/http/models"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type agentAIManager struct {
	mu                 sync.Mutex
	identityPersistMu  sync.Mutex
	sessions           map[string]*agentAISession
	approvals          map[string]*agentAIApprovalWaiter
	completedApprovals map[string]*agentAICompletedApproval
	// sessionApprovals 是会话级批准记忆（accept_for_session，2026-10-28，见
	// agent_ai_session_approvals.go）：sessionID → {provider\x00匹配键}。内存态、
	// 随进程消失，惰性 init（与 completedApprovals 的超限全清护栏同风格）。
	sessionApprovals  map[string]map[string]struct{}
	codexInputs       map[string]*agentAICodexInputWaiter
	pendingTerminals  map[string]map[string]interface{}
	runJournalEnabled bool
	// pendingSubagentTerminals 是子 agent 终态帧断线暂存（2026-11-10 P2 #4，见
	// agent_ai_subagent_journal.go）：sessionID|task_id → 终态 payload。WS 断窗
	// 内 ai.subagent 终态帧写出失败时 remember，重连 hello 后重放、写成功即清。
	// 纯内存（进程崩溃场景=registry 同亡，靠服务端详情合成兜底），封顶丢弃最旧。
	pendingSubagentTerminals map[string]map[string]interface{}
	pendingSubagentOrder     []string
	// pendingSubagentStamp 是同键写入序号（2026-11-17 审计 P2）：重放清账按
	// 「快照时的戳 == 现在的戳」判定，窗口内被新终态覆盖的键不被旧帧清账连带
	// 删除（map 值不可比，戳是身份判定的最小实现）。
	pendingSubagentStamp map[string]uint64
	// pendingApprovalRequests 是审批请求投递 journal(L1 可靠投递,2026-11-01,
	// 见 agent_ai_approval_journal.go):approval_id → 原始 request payload。
	// 发送前 remember,服务端 ai.approval.request.ack / waiter 终结时清除,
	// 重连/心跳时重放(服务端 intake 幂等)。
	pendingApprovalRequests map[string]map[string]interface{}
	approvalJournalEnabled  bool
	bindings                map[string]agentAIBindingRecord
	processedRuns           map[string]agentAIProcessedRun
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
	// coreMemoryCallForTest 记忆提取 LLM 调用 seam(nil=走真身);测试注入假身。
	coreMemoryCallForTest agentCoreMemoryStreamFn
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

// activeVibeSessionsSnapshot 只返回活跃(运行中)会话——RuntimeSessions API 的
// 语义钉子(agent_service.go「intentionally excludes resident but idle」)。

func (m *agentAIManager) activeVibeSessionsSnapshot() []models.AgentVibeSession {
	return m.vibeSessionsSnapshot(false)
}

// vibeSessionsSnapshot 覆盖层快照。includeIdle=true 时纳管 create-only 会话
// (cancel==nil):否则桌面新建、尚未开跑的会话不进任何上报面,手机永远看不到
// (2026-11-07 S1)。hello/sync 与 VibeSessions 列表传 true;RuntimeSessions 传 false。

func (m *agentAIManager) vibeSessionsSnapshot(includeIdle bool) []models.AgentVibeSession {
	if m == nil {
		return nil
	}
	now := time.Now().UTC().Format(time.RFC3339)
	m.mu.Lock()
	defer m.mu.Unlock()
	sessions := make([]models.AgentVibeSession, 0, len(m.sessions))
	for _, session := range m.sessions {
		if session == nil {
			continue
		}
		if !includeIdle && session.cancel == nil {
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
		// origin 按发起 socket 推导(与 journal 头行同源):remote=phone、
		// aisock-*=dashboard。恒写 "managed" 会被服务端映射成 'phone',
		// 把 dashboard 会话的来源角标翻错(2026-11-07)。无 socket 标记(旧
		// 调用方/测试)推导为 unknown,服务端按外部回落 'tui'——有意变更。
		origin := agentCoreJournalOriginFor(session.originSocket, session.surface)
		status := "idle"
		if session.cancel != nil {
			status = "running"
		}
		sessions = append(sessions, models.AgentVibeSession{
			ID:                    session.id,
			Provider:              provider,
			Tool:                  provider,
			SourceSessionID:       sourceSessionID,
			Origin:                origin,
			ManagedConversationID: session.id,
			BindingState:          bindingState,
			BindingVersion:        bindingVersion,
			ProjectPath:           session.projectPath,
			Title:                 truncateAgentText(title, 200),
			Mode:                  firstNonEmpty(session.mode, "vibe"),
			Status:                status,
			MessageCount:          len(session.history),
			Model:                 session.model,
			Effort:                session.effort,
			// approval_mode 随快照上报(2026-11-09 手机同步展示补链):hello/sync
			// 与 VibeSessions 列表是手机端唯一实时回显通道,漏填则手机永远看到
			// 旧挡位。字段本身去掉了 omitempty(见 models.AgentVibeSession),
			// 空串也照常下发——消费端靠「缺键=保存量」区分不了「切回默认」与
			// 「这条上报没这个话题」,只有恒带键才传得动重置。
			ApprovalMode: session.approvalMode,
			CreatedAt:    createdAt,
			UpdatedAt:    now,
		})
	}
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].UpdatedAt > sessions[j].UpdatedAt
	})
	return sessions
}

type agentAISession struct {
	id          string
	mode        string
	projectPath string
	// createdBy 是会话创建来源标记（内存态，零序列化）：ai_mcp=agent-as-MCP
	// 工具面创建，空=既有来源（手机/桌面/恢复装载）。工具面三操作以此做
	// session 契约校验（runtime 重启随会话态消失，一致性自然成立）。
	createdBy string
	provider  string
	model     string
	// effort is the provider-specific reasoning effort (codex:
	// none/minimal/low/medium/high/xhigh; claude: none/low/medium/high/max).
	// Applied to the codex CLI as a `<base>-<effort>` model-name suffix that the
	// downstream gateway derives reasoning effort from. Empty = no override.
	effort string
	// approvalMode 会话级审批模式(2026-11-05):空=策略默认;ask_all=每次询问
	// (auto_approve 降级 require,会话记忆跳过);bypass_all=放过所有(require
	// 降级本地放行跳手机,auto_deny 恒拒)。非法值归一回落空。
	approvalMode string
	// contextWindow is the model context size (tokens) chosen on the phone.
	// For claude it is passed to the CLI as `--autocompact <tokens>` so the
	// auto-compaction trigger matches the real window instead of whatever the
	// model name implies; for agentcore it drives the kernel's own compaction
	// threshold. 0 = unset (older server) → CLI/kernel defaults.
	contextWindow int
	// maxTokens is the per-conversation output cap (tokens) chosen on the phone
	// or dashboard. 0 = unset → the agentcore kernel uses its default (64000,
	// with 400-driven halving down to 8192). Only the agentcore channel consumes
	// it; claude/codex ignore it.
	maxTokens int
	// disabledMCPServers 是本会话显式关掉的 MCP server 名（配置原始键）。
	// nil = 未下发（沿用全部启用）；空切片 = 显式「全部启用」。
	// 只在 agentcore 通道生效（claude/codex 走各自 CLI 的 MCP 配置）。
	disabledMCPServers []string
	// disabledSkills 是本会话显式关掉的技能名（与 MCP 同构：nil=未下发、
	// 空切片=显式全部启用）。只在 agentcore 通道生效。
	disabledSkills          []string
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
	// subagentControl 是本回合在跑子 agent 的控制表（L3，2026-11-09）：
	// per-task 停止与追加指令。与 coreSteer 同生命周期（runAgentCore 挂、
	// clearRunning 清）——子 agent 的生命周期严格归属主回合，回合没了控制
	// 面必须一起没，否则会留下指向已死 goroutine 的僵尸句柄。
	subagentControl *agentCoreSubagentControl
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
	// JournalAttachments 是图片的持久缓存引用（buildSteerImageBlocks 落副本
	// 时带回），journalSteerUserMessage 落 journal user 行用。
	JournalAttachments []agentAIJournalAttachment
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
	// Seq 是 journal 写侧分配的会话级单调序号（2026-11-10 聊天记录身份化）。
	// 读侧原样透传到快照/详情 wire，供前端确定性排序与幂等合并；0 = 无该
	// 字段（内存新消息、旧文件），调用方按既有数组序回落。
	Seq int64
	// Attachments 是用户消息携带的图片引用（journal msg 行透传，快照/详情
	// 序列化直接用 wire 形状）。纯图消息 Content 为空。
	Attachments []models.AgentVibeAttachment
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
	// approvalMode 镜像会话级审批模式(空=策略默认;ask_all/bypass_all 见
	// applyApprovalModeOverride)。agentCoreApprove 与审批 hook 短路径读取。
	approvalMode string
	// contextWindow mirrors the session field for this run (claude:
	// --autocompact tokens; agentcore: kernel compaction budget).
	contextWindow int
	// maxTokens mirrors the session field (agentcore: per-request max_tokens).
	maxTokens int
	// disabledMCPServers mirrors the session field: MCP server names this run
	// must NOT inject. nil = all enabled.
	disabledMCPServers []string
	// disabledSkills mirrors the session field: skill names this run must NOT
	// inject. nil = all enabled.
	disabledSkills []string
	// claudeDiag 聚合 CLI 通道的逐回合诊断快照（诊断面板数据源）。runCLIPass
	// 在起 ticker 前挂载；run 按值传递，但指针字段在所有副本里仍指向同一
	// 聚合器。nil 安全（agentcore / codex 路径不挂）。
	claudeDiag              *claudeRunDiagnostics
	resumeSessionID         string
	reservedNativeSessionID string
	bindingVersion          int
	// forkFromSessionID 非空时本趟以 thread/fork 起线程（线程 busy 询问获用户
	// 同意后的二趟）：把 fork 源线程的历史复制为一个全新 codex 线程再跑回合，
	// 源线程/写锁/原进程零接触。fork 成功后 bindThread 落新 id，ai.done 帧附
	// forked_from 供服务端把 confirmed 绑定换绑到新线程。与 resumeSessionID
	// 互斥：驱动层设 forkFromSessionID 时必须清 resumeSessionID。
	forkFromSessionID string
	// threadBusyHandled 钉死「线程 busy 询问」每回合至多一次：fork 二趟若再撞
	// writer-conflict（fork 也可能被单写者拒），直接走压平历史 fresh 兜底，
	// 绝不再弹第二次询问卡。
	threadBusyHandled bool
	prompt            string
	freshPrompt       string
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
	// PersistPath 是持久附件缓存里的相对引用（spec 2026-11-05-chat-image-
	// pipeline §3.1）：下载/上传成功落副本时置位，journal 落账与网页取图读它。
	// 空=未持久化（下载失败或 10-20MiB 降级带），只损失网页显示，不影响内核。
	PersistPath string
	// Origin 记录图片来源：phone（手机经 PhoneServer 下发，runCLI 落缓存）/
	// web（网页上传，resolve 阶段即已命中缓存根）。journal 落账透传。
	Origin string
}

// agentAIRunEmitter serializes every event emitted by one run. Besides adding
// the v2 run identity/order fields, it is a terminal barrier: once a terminal
// event has been accepted, no concurrently-ticking progress goroutine can emit
// after it. Holding the mutex through write preserves event_seq wire order.

type agentAIRunEmitter struct {
	mu         sync.Mutex
	runID      string
	nextSeq    int64
	startedAt  time.Time
	firstText  bool
	terminal   bool
	write      agentTerminalWriter
	onTerminal func(map[string]interface{}) error
	// onSubagentTerminalDrop 在子 agent 终态帧写出失败时触发（2026-11-10 P2 #4：
	// WS 断窗内 ai.subagent terminal 帧被 `_ =` 吞掉=行永卡 started/progress）。
	// 宿主接 manager 暂存表，重连 hello 后补发；非终态子 agent 帧不进暂存
	// （started/progress 丢了由服务端详情合成兜底，不值得占缓存）。
	onSubagentTerminalDrop func(map[string]interface{})
	goalIdentity           map[string]interface{}
	goalOutput             strings.Builder
	goalProjectPath        string
	goalWorkspaceBefore    string
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

// agentAIMessageHasAttachments 报告原始载荷是否带非空附件数组（纯图消息
// 放行判据，2026-11-20 审计⑦）：只看在场不做实质解析——解析/校验仍归
// resolveAgentAIAttachments，与消息侧既有的 len(arr)>0 在场判定同款。

func agentAIMessageHasAttachments(raw interface{}) bool {
	arr, ok := raw.([]interface{})
	return ok && len(arr) > 0
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
	switch payload["type"] {
	case models.AgentEventAIRunStarted:
		if e.startedAt.IsZero() {
			e.startedAt = time.Now()
			payload["agent_emitted_at_ms"] = e.startedAt.UnixMilli()
		}
	case models.AgentEventAIDelta:
		delta := remoteString(payload, "delta")
		e.appendGoalOutput(delta)
		channel := remoteString(payload, "channel")
		if !e.firstText && (channel == "" || channel == "assistant" || channel == "stdout") && strings.TrimSpace(delta) != "" {
			e.firstText = true
			emittedAt := time.Now()
			payload["agent_emitted_at_ms"] = emittedAt.UnixMilli()
			if !e.startedAt.IsZero() {
				payload["agent_started_to_first_text_ms"] = emittedAt.Sub(e.startedAt).Milliseconds()
			}
		}
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
	err := e.write(payload)
	if err != nil && e.onSubagentTerminalDrop != nil && agentAISubagentFrameTerminal(payload) {
		// 写失败（断窗/未注册 deadline）的子 agent 终态帧不落空的：暂存待重连
		// 补发。服务端按 (session,msg,task) 幂等 upsert 且终态防回退，重复投递
		// 无副作用。
		e.onSubagentTerminalDrop(payload)
	}
	return err
}

// agentAISubagentFrameTerminal 判断一帧是否子 agent 终态帧
// （type=ai.subagent 且 phase∈{completed,failed,interrupted}）。

func agentAISubagentFrameTerminal(payload map[string]interface{}) bool {
	if strings.TrimSpace(fmt.Sprint(payload["type"])) != models.AgentEventAISubagent {
		return false
	}
	switch strings.TrimSpace(fmt.Sprint(payload["phase"])) {
	case "completed", "failed", "interrupted":
		return true
	}
	return false
}
