package agentcore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"runtime/debug"
	"strings"
	"sync/atomic"
	"time"
)

// Agent 主循环。
//
// 一轮 run 的结构：
//
//	for turn := 0; turn < maxIterations; turn++ {
//	    拉 steering → 组请求 → 流式拿回复 → 有 tool_use 就执行并回填 → 否则收工
//	}
//
// 对齐 pi 的 5 个 hook，但首期只暴露真正用得上的三个（steering / prepare /
// finish）—— 其余两个（PrepareNextTurn / GetFollowUpMessages）在 pi 里服务于
// 它自己的扩展机制，本内核没有对应需求，等有真实用例再加，避免空接口。

// 回合预算家族。这组护栏回答的是同一个问题：手机用户按下发送就走了，run
// 必须保证有终态（否则会话永久 running，后续消息全部进死队列——比「跑太久」
// 严重得多的故障），同时死循环不能把 token 烧穿。
//
// maxIterations 只是最后的兜底，真正先触发的是三个「无进展」检测：
//
//   - maxIdenticalToolTurns：连续 N 轮发起完全相同的工具调用（名字+参数一致）
//     = 模型在原地打转；
//   - maxConsecutiveToolErrors：连续 N 个工具结果都是错误 = 越错越试的螺旋；
//   - runWallBudget：单次 run 的墙钟预算。用户对「跑了多久」的感知远强于
//     「第几轮」，30 分钟没动静就是该停了。
//
// 三者命中都不直接报错：先补一次不带工具的收尾请求让模型把进度交代出来
// （见 wrapUp），再以带说明的终止帧收场——用户至少知道干到了哪一步。
const maxIterations = 50

// maxIdenticalToolTurns 是无进展守卫的轮数阈值。
const maxIdenticalToolTurns = 3

// maxConsecutiveToolErrors 是连续工具错误的个数阈值（跨轮累计，遇成功清零）。
const maxConsecutiveToolErrors = 5

// runWallBudget 是单次 run 的墙钟预算。长活任务（探索型读代码、批量改造）
// 10 分钟是常态，40 分钟给足余量；单个 LLM 调用本身可能耗掉大半预算，
// 检查点在轮与轮之间，不在流中途。
const runWallBudget = 40 * time.Minute

// maxContextOverflowRetries 是「上游说上下文超长」后自动压缩并重试的轮数
// 上限。每次压缩都会把历史砍到窗口一半以下，两三次足够落进任何真实窗口；
// 再多说明不是上下文问题（或模型窗口小到正常对话都装不下），继续重试只是
// 反复烧摘要 token，此时把错误如实报给用户更诚实。
const maxContextOverflowRetries = 3

// minContextOverflowWindow 是降级窗口的下限。降级是按「实测 token 的一半」
// 算的，历史很小时会算到很小，窗口太小会让压缩把正常对话也折叠掉；32000
// 已经小于任何主流模型窗口，再低就没有意义了。
const minContextOverflowWindow = 32000

// maxTruncationRetries 是**连续**被 max_tokens 截断后允许重试的次数。
//
// 为什么不是一截断就终止：截断只说明「这一次的工具参数不完整」，不说明任务
// 做不了。把失败回填进历史后继续循环，模型看到「请拆小后重试」就会分块写，
// 这是 pi 的行为，也是唯一能自愈的路。但如果连续几轮还在截断，说明它在原地
// 打转（或模型输出上限本就小于它的意图），再试只是烧 token，此时才终止。
const maxTruncationRetries = 2

// LoopHooks 是主循环的外部钩子。所有字段都可以为 nil。
type LoopHooks struct {
	// GetSteeringMessages 在**每次** LLM 调用前调用，返回用户在运行中插的话。
	// 非空时作为 user 消息追加到本轮请求里，这样「边跑边插话」能生效。
	GetSteeringMessages func() []Message

	// PrepareRequest 在发出请求前调整请求体（注入 system prompt、覆盖
	// max_tokens、追加工具定义等）。
	PrepareRequest func(*MessagesRequest)

	// FinishTurn 在一个回合正常收尾（无 tool_use）时调用。
	FinishTurn func(TurnResult)

	// GetFollowUpMessages 在回合收尾（无 tool_use、FinishTurn 已发）后调用，
	// 追问宿主「还有没有必须本 run 内处理的消息」。返回非空则追加进 history
	// 再跑一轮；空则真正收工。
	//
	// 真实用例（2026-10-26 实测）：steering 在「最后一轮流式收尾」的尾声才排进
	// 队列时，已经没有任何「下一次 LLM 调用」能取走它——run 结束，插话悬在
	// 队列里，用户看到的是一条没有回复的消息。收尾追补把「来不及插进去」变成
	// 「再答一轮」。与 GetSteeringMessages 共用宿主侧的同一个 drain：先到先得，
	// 队列排空，不会重复投递。
	GetFollowUpMessages func() []Message

	// OnCompact 在自动压缩替换了历史之后调用，参数是压缩后的新消息列表。
	// 宿主据此把会话 history 落成新形状（否则下一轮 run 又从旧历史重建，
	// 压缩白做）。
	OnCompact func([]Message, CompactInfo)
}

// TurnResult 是一个回合的结果。
type TurnResult struct {
	// Text 是本回合 assistant 输出的全部文本（已拼接）。
	Text string
	// ToolCalls 是本回合执行的工具调用。
	ToolCalls []ToolCallRecord
	// StopReason 是上游返回的停止原因。
	StopReason string
	// Usage 是本回合的 token 用量。
	Usage *Usage
}

// ToolCallRecord 记录一次工具调用，供宿主做活动展示与审计。
type ToolCallRecord struct {
	ID     string
	Name   string
	Input  map[string]interface{}
	Result ToolResult
}

// Config 是一次 run 的配置。
type Config struct {
	// Client 是 Messages API 客户端，必填。
	Client *Client
	// Model 是模型名，必填。
	Model string
	// System 是 system prompt。
	System string
	// MaxTokens 是单次回复的 token 上限。0 用客户端默认值。
	MaxTokens int
	// Tools 是可用工具集。nil 表示不带工具（纯对话）。
	Tools *Registry
	// Env 是工具执行环境（项目根、shell 策略、只读围栏）。
	Env *ToolEnv
	// Hooks 是循环钩子。
	Hooks LoopHooks
	// Emit 是 Claude stream-json 帧的出口。nil 表示不产出帧（纯逻辑模式，
	// 单测用）。
	//
	// 每次调用收到**完整的一帧**：JSON 加一个 '\n'。宿主直接写进 io.Pipe
	// 即可，不需要再做分行——宿主侧 streamStructuredAIDelta 是按行扫的。
	Emit func(frame []byte) error
	// Approve 是工具执行前的审批回调。nil 表示不审批（直接执行）。
	//
	// 走 services 的 m.requestApproval；内核不做审批 UI，只等结果。
	Approve func(ctx context.Context, call ApprovalRequest) (ApprovalResponse, error)
	// Logf 是可选日志出口，nil 表示不打日志（单测、嵌入场景）。
	//
	// 内核刻意不依赖宿主的日志包（也不该反向绑死某个实现），宿主把
	// logger.Warn 包一层传进来即可。目前只用于记录 max_tokens 截断这类
	// **没有对应事件帧**的内部决策——否则事后只能靠模型的回复反推发生过什么。
	Logf func(format string, args ...interface{})
	// SessionID 是本会话的稳定 id，写进每一帧的 session_id。
	// 为空时内核自己生成一个。
	SessionID string
	// Effort 是推理努力档位（low/medium/high/xhigh/max/ultracode，随
	// ai.session.create 透传）。经 output_config.effort 下发给上游——与
	// claude CLI 的 --effort 走的是同一个网关字段；无法识别的档位静默
	// 丢弃（见 NormalizeEffortLevel），绝不为它多付一次 400 往返。
	// 空串 = 不下发。
	Effort string
	// ContextWindow 是手机为会话选定的上下文窗口（token）。0 用
	// DefaultContextWindow。它决定自动压缩的触发阈值与保留预算——内核不向
	// 上游申请窗口（Messages API 没有这个字段），它只约束自己的历史规模，
	// 换到小窗口模型时也不会撞上 "prompt is too long"。
	ContextWindow int
}

// NormalizeEffortLevel 把会话层的 effort 档位归一成上游认识的 Messages API
// 档位；无法识别的返回空串（调用方据此选择不下发，而不是赌上游宽容）。
//
// 映射规则：
//   - low/medium/high/xhigh/max 原样通过（Messages API output_config.effort
//     的合法集）；
//   - ultracode 是手机端 effort 阶梯的最高档（Claude Code 语义），上游没有
//     这个档位，映射到 max；
//   - minimal/none 视作 low；
//   - 其余（含空串）一律丢弃。
func NormalizeEffortLevel(effort string) string {
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "low":
		return "low"
	case "medium":
		return "medium"
	case "high":
		return "high"
	case "xhigh":
		return "xhigh"
	case "max", "ultracode":
		return "max"
	case "minimal", "none":
		return "low"
	default:
		return ""
	}
}

// ApprovalRequest 是一次工具审批请求。
type ApprovalRequest struct {
	ToolName  string
	Input     map[string]interface{}
	ToolUseID string
}

// ApprovalResponse 是审批结果。
type ApprovalResponse struct {
	// Approved 为 false 时工具不执行，模型收到拒绝理由。
	Approved bool
	// Reason 是拒绝理由（回填给模型）或批准备注。
	Reason string
}

// Run 跑一次完整的 run：从 messages 出发，循环到模型不再调工具为止。
//
// 返回终止结果。ctx 取消时返回 ctx.Err()，但仍会先发 result 帧，
// 让宿主能把会话正确收敛（否则手机会永远停在「进行中」）。
// 注意：若调用方的 Emit 已随 ctx 关闭而失效，该帧会被丢弃。
func Run(ctx context.Context, cfg *Config, messages []Message) (*RunResult, error) {
	if cfg == nil || cfg.Client == nil {
		return nil, errors.New("agentcore: 未配置 Client")
	}
	if strings.TrimSpace(cfg.Model) == "" {
		return nil, errors.New("agentcore: 未配置 Model")
	}

	sessionID := strings.TrimSpace(cfg.SessionID)
	if sessionID == "" {
		sessionID = newSessionID()
	}

	run := &runner{
		cfg:     cfg,
		history: append([]Message(nil), messages...),
		writer:  newStreamWriter(cfg.Emit, sessionID),
	}
	return run.loop(ctx)
}

// newSessionID 生成一个会话 id。用时间戳加计数器而不是随机数：不需要密码学
// 强度，只需要同一进程内不重复。
func newSessionID() string {
	n := atomic.AddUint64(&sessionCounter, 1)
	return fmt.Sprintf("agentcore-%d-%d", time.Now().UnixNano(), n)
}

var sessionCounter uint64

// RunResult 是一次 run 的最终结果。
type RunResult struct {
	// Text 是整轮 run 里所有 assistant 文本的拼接。
	Text string
	// Turns 是实际执行的回合数。
	Turns int
	// ToolCalls 是整轮 run 的所有工具调用。
	ToolCalls []ToolCallRecord
	// StopReason 是最后一个回合的停止原因。
	StopReason string
	// Usage 是累计 token 用量。
	Usage Usage
	// IsError 表示终止帧已经以 is_error=true 发过。
	//
	// 宿主据此决定收尾：为 true 时流式管道已把 ai.error 发出去了，宿主只需
	// 收敛状态，不能再补一条错误；为 false 才轮到宿主自己判「是不是被取消」。
	IsError bool
}

// runner 持有一次 run 的可变状态。
type runner struct {
	cfg     *Config
	writer  *streamWriter
	history []Message

	// effortLatched:上游按模型拒绝过 output_config.effort(HTTP 400/invalid_request
	// 且错误体点名 effort/output_config)。锁存后本 run 后续轮次不再下发——
	// 与 claude CLI 的 effort-rejected 自愈同语义(那个是 flag 级拒绝去
	// --effort,这里是字段级拒绝去 output_config),不然该会话每轮 run 都带
	// 同一档重发、持续 ai.error,直到用户手动改回「默认」。
	effortLatched bool
	// effortDropLogged:无法识别的档位被丢弃时只打一次日志,别每轮刷屏。
	effortDropLogged bool

	// cacheLatched:上游按模型拒绝过 cache_control(4xx 且错误体点名缓存)。
	// 锁存后本 run 后续轮次不再挂缓存断点——缓存是纯优化,不该因为它不被支持
	// 就让整轮对话失败。与 effortLatched 同一套自愈思路。
	cacheLatched bool

	// 累计
	textAll   strings.Builder
	turnText  strings.Builder
	turns     int
	toolCalls []ToolCallRecord
	usage     Usage
	stopLast  string

	// truncStreak 是连续被 max_tokens 截断的轮数。只要有一轮没截断就清零——
	// 要防的是「原地打转」，不是累计次数。
	truncStreak int

	// 无进展守卫（见文件顶部「回合预算家族」注释）。
	//
	// lastToolSignature/dupStreak：连续 N 轮工具调用签名完全一致 = 打转。
	// errStreak：连续 N 个工具结果都是错误（跨轮累计，遇成功清零）。
	// startedAt/wallBudget：墙钟预算；startedAt 在 loop 入口惰性初始化，
	// wallBudget 供测试缩短（0 = 用 runWallBudget）。
	lastToolSignature string
	dupStreak         int
	errStreak         int
	startedAt         time.Time
	wallBudget        time.Duration

	// contextTokens 是上游最近一次报告的本轮输入 token 数（message_start 的
	// usage.input_tokens）。注意 usage.InputTokens 是按轮累加的账单口径，不能
	// 拿它当「当前上下文多大」——这里单独记最近一次的实测值，供自动压缩判定。
	contextTokens int
	// autoCompacts 是本 run 内已执行的自动压缩次数，见 maxAutoCompactsPerRun。
	autoCompacts int

	// ctxOverflowWindow 是「上游实测拒绝了当前上下文」后降级出的有效窗口。
	// 配置窗口来自用户的上下文滑杆（100k~1M），它不知道模型真实上限：给
	// 200k 的模型选 1M，压缩阈值按 1M 算会一路拖到 750k，请求直接被上游拒。
	// 一旦被拒，就按实测 token 的一半重设有效窗口并在本 run 内沿用。
	ctxOverflowWindow int
	// ctxOverflows 是本 run 内为上下文超限执行的压缩重试次数。
	ctxOverflows int
}

// logf 走宿主的日志出口；没配就静默。
func (r *runner) logf(format string, args ...interface{}) {
	if r == nil || r.cfg == nil || r.cfg.Logf == nil {
		return
	}
	r.cfg.Logf(format, args...)
}

// sid 取会话 id 供日志使用。直接写 r.writer.sessionID 会在 writer 为 nil 时
// 炸在参数求值上——而日志参数是**先求值后调用**，logf 里的 nil 判断救不了它。
func (r *runner) sid() string {
	if r == nil || r.writer == nil {
		return ""
	}
	return r.writer.sessionID
}

// loop 是主循环本体。
func (r *runner) loop(ctx context.Context) (*RunResult, error) {
	writer := r.writer
	if writer != nil {
		if err := writer.Init(); err != nil {
			return nil, err
		}
	}
	// 惰性初始化：直构造 runner 的测试路径不会设 startedAt，从这里起算。
	if r.startedAt.IsZero() {
		r.startedAt = time.Now()
	}

	for turn := 0; turn < maxIterations; turn++ {
		// 进程内内核没有子进程可杀，取消只能靠自己轮询。
		if err := ctx.Err(); err != nil {
			return r.finishWithErr(ctx, "", err)
		}

		// 墙钟预算。检查点在轮与轮之间——单个流式调用本身可以耗很久，
		// 这里拦的是「下一个回合还值不值得开」。
		if elapsed := time.Since(r.startedAt); elapsed > r.budget() {
			r.logf("[agentcore] 达到单次运行时间预算 %s（已耗时 %s），收尾 session=%s",
				r.budget(), elapsed.Round(time.Second), r.sid())
			return r.stopWithSummary(ctx,
				fmt.Sprintf("本次运行已耗时 %s，达到单次运行时间预算 %s",
					elapsed.Round(time.Minute), r.budget()))
		}

		r.turns = turn + 1
		r.turnText.Reset()

		// 每轮前拉一次 steering：用户在运行中插的话要在下一次 LLM 调用前进入
		// 上下文，而不是等这一轮跑完。
		if r.cfg.Hooks.GetSteeringMessages != nil {
			if steering := r.cfg.Hooks.GetSteeringMessages(); len(steering) > 0 {
				r.history = append(r.history, steering...)
			}
		}

		// 上下文接近窗口上限时先压缩，再发请求。放在 steering 之后：刚插进来
		// 的话必须留在保留的原文尾部，而不是被折叠进摘要。
		r.maybeAutoCompact(ctx)

		req := r.buildRequest()
		if r.cfg.Hooks.PrepareRequest != nil {
			r.cfg.Hooks.PrepareRequest(req)
		}
		// 断点挂在宿主 hook 之后：hook 可能整体替换 system / tools / messages
		// （例如把 system 又塞回字符串），挂在之前会被它抹掉。
		if !r.cacheLatched {
			applyCacheBreakpoints(req)
		}

		assistantBlocks, stopReason, err := r.streamTurn(ctx, req, writer)
		if err != nil && r.noteEffortRejection(err, req) {
			// 上游按模型拒绝 effort:锁存后去掉 output_config 重试本回合
			// 一次。此时还没发过任何事件帧(400 在响应检查阶段抛出),writer
			// 状态干净,重跑安全。
			assistantBlocks, stopReason, err = r.streamTurn(ctx, r.buildRequest(), writer)
		}
		if err != nil && r.noteCacheRejection(err, req) {
			// 上游不认 cache_control:摘掉断点重试本回合一次。缓存是优化项,
			// 宁可回退成无缓存也不要整轮失败。
			assistantBlocks, stopReason, err = r.streamTurn(ctx, r.buildRequest(), writer)
		}
		if err != nil && r.noteContextOverflow(ctx, err) {
			// 上游说上下文超长：多半是用户选的窗口大于模型真实窗口。把有效
			// 窗口降到实测规模以下、就地压缩历史，然后重试本回合——压缩后
			// 请求才装得进去。（同样是响应检查阶段的 4xx，writer 干净。）
			assistantBlocks, stopReason, err = r.streamTurn(ctx, r.buildRequest(), writer)
		}
		if err != nil {
			// 流式失败同样可能是取消导致的（SSE 读循环被 ctx 打断），
			// 交给 finishWithErr 分辨。
			return r.finishWithErr(ctx, "", err)
		}
		r.stopLast = stopReason

		// 把 assistant 输出写回历史（下一轮请求要带上）。
		r.history = append(r.history, BlocksMessage("assistant", assistantBlocks))

		// 第一轮已经把用户这一轮附的图片送进模型视野了。此后每一轮都重发同一
		// 份 base64 只是白烧带宽——换成文字占位符（见 media.go）。
		r.stubHistoricalImages()

		toolUses := collectToolUses(assistantBlocks)
		if len(toolUses) == 0 {
			// 无工具调用 = 回合收尾。
			r.fireFinishTurn(stopReason)
			// 收尾追补：steering 排进队列却没赶上任何一次下一轮调用时
			// （典型：插话在最后一轮流式尾声才到），再跑一轮把它答掉，
			// 别让用户的插话悬在队列里没有回复。上限仍是 maxIterations。
			if r.cfg.Hooks.GetFollowUpMessages != nil {
				if followUps := r.cfg.Hooks.GetFollowUpMessages(); len(followUps) > 0 {
					r.history = append(r.history, followUps...)
					continue
				}
			}
			return r.finish(ctx, ResultSubtypeSuccess, "", false, nil)
		}

		// 截断守卫：stop_reason == max_tokens 时，这批 tool_use 的参数是
		// 不完整的（JSON 可能只写了一半），执行会写坏文件。pi 的做法是整批
		// 失败、一个都不执行。必须照做。
		//
		// 但「这批不执行」不等于「这一轮 run 结束」：把失败回填进历史后继续
		// 循环，模型看到「请拆小后重试」就会分块写。一截断就终止的话，回填的
		// 那句恢复提示永远没人读——它就成了死代码，用户只拿到一句「被截断」。
		// 连续 maxTruncationRetries 次仍在截断才终止，避免原地打转烧 token。
		if stopReason == StopReasonMaxTokens {
			results := failTruncatedToolUses(toolUses)
			if writer != nil {
				// 失败帧写不出去也不影响后续收敛，别把 run 拖成悬空。
				_ = writer.ToolResults(results)
			}
			r.history = append(r.history, BlocksMessage("user", results))
			for _, tu := range toolUses {
				r.toolCalls = append(r.toolCalls, ToolCallRecord{
					ID: tu.ID, Name: tu.Name, Input: tu.Input,
					Result: ErrorResult("上一条回复被 max_tokens 截断，工具参数不完整，未执行"),
				})
			}
			r.truncStreak++
			// 截断是**没有对应事件帧**的内部决策（失败回填是 tool_result，手机
			// 只看到一次工具活动），不打日志的话事后只能靠模型的回复反推。
			r.logf("[agentcore] max_tokens 截断：%d 个工具调用未执行（连续第 %d/%d 次），已回填让模型拆小重试 session=%s",
				len(toolUses), r.truncStreak, maxTruncationRetries, r.writer.sessionID)
			if r.truncStreak > maxTruncationRetries {
				r.fireFinishTurn(stopReason)
				return r.finish(ctx, ResultSubtypeErrorMaxTurns,
					fmt.Sprintf("模型连续 %d 次输出被 max_tokens 截断，工具调用未执行", r.truncStreak), true, nil)
			}
			continue
		}
		r.truncStreak = 0

		// 无进展守卫：连续 N 轮发起完全相同的工具调用（名字+参数一致）。
		// 签名取本回合全部 tool_use；任何一轮不同即重新计数。真实重复
		// （用户插话后要求再看一次同一状态）几乎总伴随参数或轮次内容变化，
		// 三轮一模一样只有打转能解释。
		if sig := toolCallSignature(toolUses); sig == r.lastToolSignature {
			r.dupStreak++
		} else {
			r.lastToolSignature = sig
			r.dupStreak = 1
		}
		if r.dupStreak >= maxIdenticalToolTurns {
			r.logf("[agentcore] 连续 %d 轮发起完全相同的工具调用，判定无进展，收尾 session=%s",
				r.dupStreak, r.sid())
			return r.stopWithSummary(ctx,
				fmt.Sprintf("模型连续 %d 轮发起完全相同的工具调用，判定为无进展", r.dupStreak))
		}

		// 执行工具。
		results := r.executeTools(ctx, toolUses, writer)
		r.history = append(r.history, BlocksMessage("user", results))

		// 连续错误守卫：跨轮累计，遇任何成功清零。防「读文件失败→重试→
		// 再失败」的螺旋——模型自己不会承认该换思路。
		for _, res := range results {
			if res.IsError {
				r.errStreak++
			} else {
				r.errStreak = 0
			}
		}
		if r.errStreak >= maxConsecutiveToolErrors {
			r.logf("[agentcore] 连续 %d 个工具结果都是错误，判定打转，收尾 session=%s",
				r.errStreak, r.sid())
			return r.stopWithSummary(ctx,
				fmt.Sprintf("工具连续 %d 次执行失败，疑似陷入重试循环", r.errStreak))
		}

		if writer != nil {
			if err := writer.ToolResults(results); err != nil {
				return nil, err
			}
		}
	}

	// 撞上 maxIterations——三个无进展守卫都没先触发时的兜底。
	//
	// 不直接报错收场：上限是防死循环的护栏，不该让用户看到一句「达到最大回合数」
	// 之后什么都没有——前面几十条命令的进展全丢在历史里。stopWithSummary 会
	// 补一次**不带工具**的请求，让模型把已完成的部分和下一步用文字交代出来。
	// 这一次不计入回合数，失败也只是没有收尾文本，不改变终止结论。
	return r.stopWithSummary(ctx,
		fmt.Sprintf("达到单次运行的最大回合数 %d", maxIterations))
}

// budget 返回本 run 的墙钟预算（测试可经 wallBudget 缩短）。
func (r *runner) budget() time.Duration {
	if r.wallBudget > 0 {
		return r.wallBudget
	}
	return runWallBudget
}

// stopWithSummary 是所有预算耗尽出口的统一收场：先补一次无工具的收尾总结
// （wrapUp 内部吞错，不影响终止结论），再以带原因的终止帧结束。
func (r *runner) stopWithSummary(ctx context.Context, reason string) (*RunResult, error) {
	r.wrapUp(ctx, reason)
	return r.finish(ctx, ResultSubtypeErrorMaxTurns, reason+"，已停止", true, nil)
}

// toolCallSignature 把一轮的全部工具调用压成可比较的签名：名字 + 参数 JSON。
// map 序列化时 Go 按键排序，同一组参数的签名天然稳定。
func toolCallSignature(uses []ContentBlock) string {
	var sb strings.Builder
	for _, tu := range uses {
		sb.WriteString(tu.Name)
		sb.WriteByte(0x1f)
		if raw, err := json.Marshal(tu.Input); err == nil {
			sb.Write(raw)
		}
		sb.WriteByte(0x1e)
	}
	return sb.String()
}

// wrapUp 在回合预算耗尽时补一次纯文本收尾。
//
// 关键手段是 req.Tools = nil：模型没有工具可调，只能输出文字，于是这一轮的
// 文本天然就是总结。放在 hook 之后再清空，防止宿主 hook 又把工具塞回来。
//
// writer 传 nil：这一轮的文本不需要再走事件帧（宿主那边会说「对话已停止」，
// 再补一串 assistant 帧反而像是还在跑）。文本经 r.turnText 由 finish 汇入结果。
func (r *runner) wrapUp(ctx context.Context, reason string) {
	if err := ctx.Err(); err != nil {
		return
	}
	req := r.buildRequest()
	if r.cfg.Hooks.PrepareRequest != nil {
		r.cfg.Hooks.PrepareRequest(req)
	}
	req.Tools = nil
	if !r.cacheLatched {
		applyCacheBreakpoints(req)
	}
	// 明确告诉模型这是一次收尾，不是又一轮可以继续干活的对话。
	req.Messages = append(append([]Message(nil), req.Messages...),
		TextMessage("user", fmt.Sprintf(
			"%s，本轮不会再执行任何工具。请用简洁的文字总结：已经完成了什么、当前状态如何、还剩哪些没做完、建议下一步怎么做。不要调用任何工具。",
			reason)))
	if _, _, err := r.streamTurn(ctx, req, nil); err != nil {
		r.logf("[agentcore] 收尾总结请求失败（不影响终止结论）: %v session=%s", err, r.writer.sessionID)
	}
}

// maybeAutoCompact 在上下文接近窗口上限时把较早的历史换成摘要。
//
// 触发依据取两者的较大值：本地估算（稳定、但对 tokenizer 只是近似）与上游
// 上一轮实测的 input_tokens（精确、但滞后一轮）。任一达到阈值的 3/4 就压缩，
// 留出余量给本轮输出与工具结果。压缩失败不终止 run：宁可带着长上下文继续
// 跑，也不要因为摘要请求偶发失败而中断用户的对话。
func (r *runner) maybeAutoCompact(ctx context.Context) {
	window := r.effectiveContextWindow()
	if window <= 0 {
		return
	}
	if r.autoCompacts >= maxAutoCompactsPerRun {
		return
	}
	threshold := window * autoCompactRatioNum / autoCompactRatioDen
	measured := EstimateTokens(r.history)
	if r.contextTokens > measured {
		measured = r.contextTokens
	}
	if measured < threshold {
		return
	}

	compacted, info, err := Compact(ctx, r.compactConfig(), r.history, "auto")
	if err != nil {
		if errors.Is(err, ErrNothingToCompact) {
			return
		}
		r.logf("agentcore: 自动压缩失败，继续使用完整上下文: %v session=%s", err, r.writer.sessionID)
		return
	}
	r.autoCompacts++
	r.history = compacted
	r.contextTokens = info.PostTokens
	r.logf("agentcore: 上下文自动压缩 %d→%d tokens，%d 条早期消息折叠为摘要（本 run 第 %d 次）session=%s",
		info.PreTokens, info.PostTokens, info.Removed, r.autoCompacts, r.writer.sessionID)
	if r.writer != nil {
		// 通知帧写不出去不拖累 run 本身；宿主会按帧给手机展示压缩提示。
		_ = r.writer.CompactBoundary(info.Trigger, info.PreTokens, info.PostTokens)
	}
	if r.cfg.Hooks.OnCompact != nil {
		r.cfg.Hooks.OnCompact(compacted, info)
	}
}

// effectiveContextWindow 返回本 run 实际可用的上下文窗口：配置值与「上游
// 实测拒绝后降级出的值」取小。手机端的滑杆不知道模型真实上限，配得再大也
// 只是让压缩阈值失效；被拒一次之后以实测为准。
func (r *runner) effectiveContextWindow() int {
	w := r.cfg.ContextWindow
	if r.ctxOverflowWindow > 0 && (w <= 0 || r.ctxOverflowWindow < w) {
		return r.ctxOverflowWindow
	}
	return w
}

// compactConfig 返回把窗口换成有效窗口的配置副本，供 Compact 计算摘要预算。
func (r *runner) compactConfig() *Config {
	cfg := *r.cfg
	cfg.ContextWindow = r.effectiveContextWindow()
	return &cfg
}

// noteContextOverflow 判定失败是否因为上下文超出模型窗口；命中就把有效
// 窗口降到实测规模以下、压缩历史并返回 true，调用方重试本回合。
//
// 只认 4xx（且是响应检查阶段抛出的 httpStatusError）——那种失败发生在任何
// 事件帧之前，writer 状态干净，重跑一整回合是安全的。2xx 信封里的错误多
// 半是限流，不能拿去压缩。
func (r *runner) noteContextOverflow(ctx context.Context, err error) bool {
	if r.ctxOverflows >= maxContextOverflowRetries {
		return false
	}
	if !isContextOverflowError(err) {
		return false
	}
	measured := EstimateTokens(r.history)
	if r.contextTokens > measured {
		measured = r.contextTokens
	}
	if measured <= 0 {
		return false
	}
	eff := measured / 2
	if eff < minContextOverflowWindow {
		eff = minContextOverflowWindow
	}
	// 每次都让窗口严格变小，避免「压缩后估算仍高于窗口 → 又一次超限 →
	// 同样大小重试」的原地打转。
	if prev := r.effectiveContextWindow(); prev > 0 && eff >= prev {
		eff = prev / 2
		if eff < minContextOverflowWindow {
			eff = minContextOverflowWindow
		}
	}
	r.ctxOverflowWindow = eff

	compacted, info, cerr := Compact(ctx, r.compactConfig(), r.history, "auto")
	if cerr != nil {
		if errors.Is(cerr, ErrNothingToCompact) {
			r.logf("agentcore: 上下文超出模型窗口，但历史已无早期消息可压缩 session=%s", r.writer.sessionID)
			return false
		}
		r.logf("agentcore: 上下文超限后压缩失败，无法自动恢复: %v session=%s", cerr, r.writer.sessionID)
		return false
	}
	r.ctxOverflows++
	r.history = compacted
	r.contextTokens = info.PostTokens
	r.logf("agentcore: 上游拒绝上下文（实测约 %d tokens），有效窗口降至 %d，压缩 %d→%d tokens 后重试（本 run 第 %d 次）session=%s",
		measured, eff, info.PreTokens, info.PostTokens, r.ctxOverflows, r.writer.sessionID)
	if r.writer != nil {
		_ = r.writer.CompactBoundary(info.Trigger, info.PreTokens, info.PostTokens)
	}
	if r.cfg.Hooks.OnCompact != nil {
		r.cfg.Hooks.OnCompact(compacted, info)
	}
	return true
}

// isContextOverflowError 判定是否为「上下文超出模型窗口」类 4xx。各家措辞
// 不同（Anthropic: prompt is too long；OpenAI 兼容: context length /
// maximum context length；GLM: input is too long），只认明确指向上下文/输入
// 的短语，避免把 max_tokens 之类的错误误判成上下文问题。
func isContextOverflowError(err error) bool {
	var statusErr *httpStatusError
	if !errors.As(err, &statusErr) {
		return false
	}
	switch statusErr.status {
	case http.StatusBadRequest, http.StatusRequestEntityTooLarge, http.StatusUnprocessableEntity:
	default:
		return false
	}
	m := strings.ToLower(statusErr.body)
	for _, needle := range []string{
		"prompt is too long",
		"context length",
		"context window",
		"maximum context",
		"too many tokens",
		"input is too long",
		"input too long",
		"exceeds the maximum context",
		"exceed context",
		"上下文",
	} {
		if strings.Contains(m, needle) {
			return true
		}
	}
	return false
}

// buildRequest 组装一次 Messages API 请求。
func (r *runner) buildRequest() *MessagesRequest {
	req := &MessagesRequest{
		Model:     r.cfg.Model,
		MaxTokens: r.cfg.MaxTokens,
		System:    r.cfg.System,
		Messages:  r.history,
	}
	if !r.effortLatched {
		if effort := NormalizeEffortLevel(r.cfg.Effort); effort != "" {
			req.OutputConfig = &OutputConfig{Effort: effort}
		} else if strings.TrimSpace(r.cfg.Effort) != "" && !r.effortDropLogged {
			// 档位无法识别被丢弃:留一条观测痕,否则 UI 永远显示已选档而
			// 实际从未下发,错配无从查起。
			r.effortDropLogged = true
			r.logf("agentcore: effort 档位 %q 无法识别,本轮请求不下发 output_config", r.cfg.Effort)
		}
	}
	if r.cfg.Tools != nil {
		req.Tools = r.cfg.Tools.Defs()
	}
	return req
}

// noteEffortRejection 判定这次流式失败是不是「上游按模型拒绝 effort」。
// 命中则锁存(r.effortLatched)并返回 true,调用方据此去 output_config 重试。
// 只认 400/invalid_request_error 且错误体点名 effort/output_config 的形态,
// 避免把无关 400 误伤成 effort 问题。
func (r *runner) noteEffortRejection(err error, req *MessagesRequest) bool {
	if r.effortLatched || req == nil || req.OutputConfig == nil {
		return false
	}
	if !isEffortRejection(err) {
		return false
	}
	r.effortLatched = true
	r.logf("agentcore: 上游拒绝 effort(output_config),本 run 后续轮次不再下发: %v", err)
	return true
}

// isEffortRejection 识别两类错误面:HTTP 4xx 携带错误体(httpStatusError),
// 与 2xx 体内嵌错误信封(upstreamError,invalid_request_error 族)。
func isEffortRejection(err error) bool {
	var httpErr *httpStatusError
	if errors.As(err, &httpErr) {
		if httpErr.status < 400 || httpErr.status >= 500 {
			return false
		}
		return mentionsEffort(httpErr.body)
	}
	var upErr *upstreamError
	if errors.As(err, &upErr) {
		if !strings.Contains(strings.ToLower(upErr.kind), "invalid_request") {
			return false
		}
		return mentionsEffort(upErr.body + " " + upErr.message)
	}
	return false
}

// mentionsEffort 判定错误文本是否点名 effort/output_config。
func mentionsEffort(text string) bool {
	t := strings.ToLower(text)
	return strings.Contains(t, "output_config") || strings.Contains(t, "effort")
}

// noteCacheRejection 判定这次流式失败是不是「上游不认 cache_control」。
// 命中则锁存(r.cacheLatched)并返回 true，调用方据此摘掉断点重试。
//
// 为什么要这一层：我们没法事先知道每个上游（GLM / deepseek / 后续接入的）
// 是否实现了 Anthropic 的缓存字段。与其加一个让用户去猜的开关，不如被拒一次
// 就自动降级——缓存是优化，不是功能前提。
func (r *runner) noteCacheRejection(err error, req *MessagesRequest) bool {
	if r.cacheLatched || req == nil {
		return false
	}
	if !isCacheRejection(err) {
		return false
	}
	r.cacheLatched = true
	r.logf("agentcore: 上游拒绝 cache_control，本 run 后续轮次不再挂缓存断点（回退为无缓存）: %v", err)
	return true
}

// streamTurn 跑一次 LLM 调用，边流边写帧，返回本回合的完整内容块。
func (r *runner) streamTurn(ctx context.Context, req *MessagesRequest, writer *streamWriter) ([]ContentBlock, string, error) {
	// 按 index 累积各内容块。
	var blocks []ContentBlock
	partialJSON := map[int]*strings.Builder{}
	// started 记录哪些 index 已经发过 content_block_start。文本块的 start
	// 要等到第一段 delta 才知道该发（上游的 content_block_start 对文本不带
	// 内容），所以不能在上面那个分支里发。
	started := map[int]bool{}
	stopReason := ""
	var messageID string
	// 本回合的用量，只用于打一行日志。累计值另有 r.usage。
	var turnIn, turnCacheRead, turnCacheWrite, turnOut int

	ensureIndex := func(idx int) {
		for len(blocks) <= idx {
			blocks = append(blocks, ContentBlock{})
		}
	}

	err := r.cfg.Client.Stream(ctx, req, func(ev StreamEvent) error {
		switch ev.Kind {
		case EventStart:
			messageID = ev.MessageID
			if ev.Usage != nil {
				r.usage.InputTokens += ev.Usage.InputTokens
				r.usage.CacheReadInputTokens += ev.Usage.CacheReadInputTokens
				r.usage.CacheCreationInputTokens += ev.Usage.CacheCreationInputTokens
				turnIn = ev.Usage.InputTokens
				turnCacheRead = ev.Usage.CacheReadInputTokens
				turnCacheWrite = ev.Usage.CacheCreationInputTokens
				// 本轮实测的上下文大小：自动压缩用它兜底估算误差。
				//
				// 必须三项相加：Anthropic 语义里 input_tokens 只算「没命中缓存
				// 的那部分」，命中的部分在 cache_read_input_tokens 里。缓存生效
				// 之后 input_tokens 会骤降，若还只取它，contextTokens 会被严重
				// 低估 → 压缩阈值永远不触发 → 请求一路涨到上游报超长。
				r.contextTokens = ev.Usage.InputTokens +
					ev.Usage.CacheReadInputTokens +
					ev.Usage.CacheCreationInputTokens
			}
			if writer != nil {
				if err := writer.TurnStart(messageID, ev.Model); err != nil {
					return err
				}
			}

		case EventText:
			ensureIndex(ev.Index)
			blocks[ev.Index].Type = BlockTypeText
			blocks[ev.Index].Text += ev.Text
			r.textAll.WriteString(ev.Text)
			r.turnText.WriteString(ev.Text)
			if writer != nil {
				if !started[ev.Index] {
					started[ev.Index] = true
					if err := writer.TextBlockStart(ev.Index); err != nil {
						return err
					}
				}
				if err := writer.TextDelta(ev.Index, ev.Text); err != nil {
					return err
				}
			}

		case EventImage:
			ensureIndex(ev.Index)
			blocks[ev.Index] = ContentBlock{Type: BlockTypeImage, Source: ev.Image}
			if writer != nil {
				// 图片块在 start 里一次给全，所以这里既发 start 也发 stop，
				// 让宿主的块序号与真实 claude-code 帧序一致。不发 delta——
				// 上游的 image 块本来就没有 delta。
				started[ev.Index] = true
				src := map[string]interface{}{"type": ev.Image.Type}
				if ev.Image.MediaType != "" {
					src["media_type"] = ev.Image.MediaType
				}
				if err := writer.ContentBlockStart(ev.Index, map[string]interface{}{
					"type":   BlockTypeImage,
					"source": src,
				}); err != nil {
					return err
				}
				if err := writer.ContentBlockStop(ev.Index); err != nil {
					return err
				}
			}

		case EventToolUseStart:
			ensureIndex(ev.Index)
			blocks[ev.Index] = ContentBlock{
				Type: BlockTypeToolUse,
				ID:   ev.ID,
				Name: ev.Name,
			}
			partialJSON[ev.Index] = &strings.Builder{}
			if writer != nil {
				started[ev.Index] = true
				if err := writer.ToolUseBlockStart(ev.Index, ev.ID, ev.Name); err != nil {
					return err
				}
			}

		case EventToolInput:
			ensureIndex(ev.Index)
			if b, ok := partialJSON[ev.Index]; ok {
				b.WriteString(ev.PartialJSON)
			}
			if writer != nil {
				if err := writer.ToolInputDelta(ev.Index, ev.PartialJSON); err != nil {
					return err
				}
			}

		case EventBlockStop:
			ensureIndex(ev.Index)
			// 参数累积完成，解析成 map。解析失败留一个空 map —— 由执行阶段
			// 报错回填，模型能自行纠错。
			if b, ok := partialJSON[ev.Index]; ok {
				raw := strings.TrimSpace(b.String())
				if raw == "" {
					blocks[ev.Index].Input = map[string]interface{}{}
				} else {
					var parsed map[string]interface{}
					if jsonErr := json.Unmarshal([]byte(raw), &parsed); jsonErr != nil {
						blocks[ev.Index].Input = map[string]interface{}{
							"__parse_error__": jsonErr.Error(),
						}
					} else {
						blocks[ev.Index].Input = parsed
					}
				}
			}
			if writer != nil {
				if err := writer.ContentBlockStop(ev.Index); err != nil {
					return err
				}
			}

		case EventStop:
			stopReason = ev.StopReason
			if ev.Usage != nil {
				r.usage.OutputTokens += ev.Usage.OutputTokens
				turnOut = ev.Usage.OutputTokens
			}

		case EventError:
			return ev.Err
		}
		return nil
	})
	if err != nil {
		return nil, "", err
	}

	// 每回合一行用量日志。这是判断「缓存到底有没有生效」的唯一地面真值：
	// 上游若支持前缀缓存，cache_read 会随轮次上升、input 会相应下降；若一直
	// 是 cache_read=0，说明这一路上游没做缓存（或者断点全被忽略），此时提速
	// 只能靠占位符化/压缩图片这类减少体积的手段。
	r.logf("[agentcore] turn usage in=%d cache_read=%d cache_write=%d out=%d ctx=%d session=%s",
		turnIn, turnCacheRead, turnCacheWrite, turnOut, r.contextTokens, r.sid())

	// 收尾：丢掉空块（上游偶尔会发 content_block_start 但没内容）。
	cleaned := make([]ContentBlock, 0, len(blocks))
	for _, b := range blocks {
		if b.Type == "" {
			continue
		}
		if b.Type == BlockTypeText && b.Text == "" {
			continue
		}
		cleaned = append(cleaned, b)
	}

	// finalized 帧 —— 工具活动追踪的唯一入口，必须发。
	if writer != nil {
		if err := writer.AssistantMessage(cleaned); err != nil {
			return nil, "", err
		}
	}

	return cleaned, stopReason, nil
}

// executeTools 逐个执行本回合的工具调用，返回 tool_result 块。
func (r *runner) executeTools(ctx context.Context, toolUses []ContentBlock, writer *streamWriter) []ContentBlock {
	results := make([]ContentBlock, 0, len(toolUses))

	for _, tu := range toolUses {
		if err := ctx.Err(); err != nil {
			results = append(results, ContentBlock{
				Type:      BlockTypeToolResult,
				ToolUseID: tu.ID,
				Content:   "运行已取消，工具未执行",
				IsError:   true,
			})
			continue
		}

		result := r.executeOne(ctx, tu)
		r.toolCalls = append(r.toolCalls, ToolCallRecord{
			ID: tu.ID, Name: tu.Name, Input: tu.Input, Result: result,
		})
		results = append(results, ContentBlock{
			Type:      BlockTypeToolResult,
			ToolUseID: tu.ID,
			Content:   result.ContentForModel(),
			IsError:   result.IsError,
		})
	}
	return results
}

// executeOne 执行单个工具调用，串起审批 → 围栏 → 执行。
//
// 内核跑在宿主进程内：审批桥、围栏、工具实现里任何一个 panic 都等于整台
// 设备下线（CLI provider 的同类崩溃只死子进程，这里死的是整个 core——
// 2026-10-26 实测：宿主审批策略对一条带注释尾段的 bash 命令 slice 越界，
// 容器整个退出）。所以这里统一 recover，把 panic 降级成失败的 tool_result：
// 模型能看到错误继续走，进程活着。堆栈进日志，摘要进工具结果。
func (r *runner) executeOne(ctx context.Context, tu ContentBlock) (result ToolResult) {
	defer func() {
		rec := recover()
		if rec == nil {
			return
		}
		r.cfg.Logf("agentcore: 工具执行 panic 已拦截 tool=%s id=%s: %v\n%s", tu.Name, tu.ID, rec, debug.Stack())
		result = ErrorResult("工具执行发生内部错误（进程已拦截，未受影响），本轮工具未执行: %v", rec)
	}()
	return r.executeOneInner(ctx, tu)
}

// executeOneInner 是 executeOne 的本体（见其注释里的 recover 兜底）。
func (r *runner) executeOneInner(ctx context.Context, tu ContentBlock) ToolResult {
	// 参数解析失败的调用直接回填错误，别拿坏参数去执行。
	if parseErr, ok := tu.Input["__parse_error__"]; ok {
		return ErrorResult("工具参数不是合法 JSON，未执行: %v", parseErr)
	}

	// 审批。只读工具通常由宿主策略放行，写/执行类需要用户确认。
	if r.cfg.Approve != nil {
		resp, err := r.cfg.Approve(ctx, ApprovalRequest{
			ToolName:  tu.Name,
			Input:     tu.Input,
			ToolUseID: tu.ID,
		})
		if err != nil {
			return ErrorResult("审批流程失败，工具未执行: %v", err)
		}
		if !resp.Approved {
			reason := strings.TrimSpace(resp.Reason)
			if reason == "" {
				reason = "用户拒绝了这次操作"
			}
			return ErrorResult("%s", reason)
		}
	}

	// 宿主注入的只读围栏（goal 模式）。
	if r.cfg.Env != nil && r.cfg.Env.ScopeCheck != nil {
		if allowed, reason := r.cfg.Env.ScopeCheck(tu.Name, tu.Input); !allowed {
			if strings.TrimSpace(reason) == "" {
				reason = "当前模式不允许该操作"
			}
			return ErrorResult("%s", reason)
		}
	}

	if r.cfg.Tools == nil {
		return ErrorResult("当前会话没有可用工具")
	}
	tool, ok := r.cfg.Tools.Get(tu.Name)
	if !ok {
		return ErrorResult("未知工具: %s", tu.Name)
	}

	// 工具自己也要能被取消打断。
	toolCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	return tool.Execute(toolCtx, tu.Input, r.cfg.Env)
}

// fireFinishTurn 调用 FinishTurn 钩子。只传本回合的文本，不是整轮累计——
// 钩子的语义是「这一回合结束了」，给整轮会让调用方重复处理前几轮的文本。
func (r *runner) fireFinishTurn(stopReason string) {
	if r.cfg.Hooks.FinishTurn == nil {
		return
	}
	r.cfg.Hooks.FinishTurn(TurnResult{
		Text:       r.turnText.String(),
		ToolCalls:  r.toolCalls,
		StopReason: stopReason,
		Usage:      &r.usage,
	})
}

// finishWithErr 按 ctx 状态选终止语义：被取消 → interrupted（非错误），
// 其余 → error_during_execution（错误）。
//
// 两者必须分开：claudeResultError（宿主 agent_ai.go:6399）只把 is_error 或
// error_* 子类型转成 ai.error。用户在手机上点「停止」时 ctx 被取消，若走错误
// 分支，手机会收到一条红色错误气泡——而正确表现是安静收敛到 stopped。
func (r *runner) finishWithErr(ctx context.Context, prefix string, runErr error) (*RunResult, error) {
	if ctx.Err() != nil {
		return r.finish(ctx, ResultSubtypeInterrupted, "", false, runErr)
	}
	msg := strings.TrimSpace(prefix)
	if msg == "" && runErr != nil {
		msg = runErr.Error()
	}
	return r.finish(ctx, ResultSubtypeErrorRun, msg, true, runErr)
}

// finish 发终止帧并返回结果。
//
// 一次 run 只能发一次 result 帧 —— 重复会让手机出现重复气泡或状态错乱。
//
// result 帧的 result 字段只放**最后一个回合**的文本，不是整轮累计。宿主
// streamStructuredAIDelta 的收尾去重（agent_ai.go suffixNotStreamed）把
// result 当「最后一条 assistant 消息的文本」，拿去和已流式发出的 delta 比对
// 后缀。若这里给整轮累计，多回合 run 的 result 会以第 1 回合文本开头，
// 与只含末回合的已流式文本比对必然分叉，宿主遂判定「整段没发过」并重发
// 全文 —— 手机上表现为整轮回复再刷一遍、落库消息双份。与 fireFinishTurn
// 的「只传本回合」是同一个理由。
func (r *runner) finish(_ context.Context, subtype, errText string, isError bool, runErr error) (*RunResult, error) {
	if r.writer != nil {
		text := r.turnText.String()
		if isError && errText != "" {
			// 错误原因必须放**最前面**：这整段文本就是宿主 ai.error 的正文，
			// 而手机的错误卡片只渲染第一段。把原因拼在模型最后一句话后面
			// （旧行为）等于把真因藏起来——2026-10-27 实锤：用户只看到模型
			// 说「接下来看看 isDraft 怎么判断」，看不到真正的
			// 「达到最大回合数 50，已停止」。
			if text != "" {
				text = errText + "\n\n" + text
			} else {
				text = errText
			}
		}
		_ = r.writer.Result(subtype, text, isError, &r.usage, r.turns)
	}

	res := &RunResult{
		Text:       r.textAll.String(),
		Turns:      r.turns,
		ToolCalls:  r.toolCalls,
		StopReason: r.stopLast,
		Usage:      r.usage,
		IsError:    isError,
	}
	return res, runErr
}

// collectToolUses 从内容块里挑出 tool_use。
func collectToolUses(blocks []ContentBlock) []ContentBlock {
	var out []ContentBlock
	for _, b := range blocks {
		if b.Type == BlockTypeToolUse {
			out = append(out, b)
		}
	}
	return out
}

// failTruncatedToolUses 给每个 tool_use 造一条失败回填。
func failTruncatedToolUses(toolUses []ContentBlock) []ContentBlock {
	out := make([]ContentBlock, 0, len(toolUses))
	for _, tu := range toolUses {
		out = append(out, ContentBlock{
			Type:      BlockTypeToolResult,
			ToolUseID: tu.ID,
			Content:   "上一条回复被 max_tokens 截断，工具参数不完整，未执行。请拆小后重试。",
			IsError:   true,
		})
	}
	return out
}
