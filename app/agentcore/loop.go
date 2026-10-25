package agentcore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

// maxIterations 是单次 run 的硬上限。防的是模型陷入「调工具 → 失败 → 再调」
// 的死循环，那会把 token 烧穿。
const maxIterations = 50

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
}

// logf 走宿主的日志出口；没配就静默。
func (r *runner) logf(format string, args ...interface{}) {
	if r.cfg.Logf != nil {
		r.cfg.Logf(format, args...)
	}
}

// loop 是主循环本体。
func (r *runner) loop(ctx context.Context) (*RunResult, error) {
	writer := r.writer
	if writer != nil {
		if err := writer.Init(); err != nil {
			return nil, err
		}
	}

	for turn := 0; turn < maxIterations; turn++ {
		// 进程内内核没有子进程可杀，取消只能靠自己轮询。
		if err := ctx.Err(); err != nil {
			return r.finishWithErr(ctx, "", err)
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

		req := r.buildRequest()
		if r.cfg.Hooks.PrepareRequest != nil {
			r.cfg.Hooks.PrepareRequest(req)
		}

		assistantBlocks, stopReason, err := r.streamTurn(ctx, req, writer)
		if err != nil {
			// 流式失败同样可能是取消导致的（SSE 读循环被 ctx 打断），
			// 交给 finishWithErr 分辨。
			return r.finishWithErr(ctx, "", err)
		}
		r.stopLast = stopReason

		// 把 assistant 输出写回历史（下一轮请求要带上）。
		r.history = append(r.history, BlocksMessage("assistant", assistantBlocks))

		toolUses := collectToolUses(assistantBlocks)
		if len(toolUses) == 0 {
			// 无工具调用 = 回合收尾。
			r.fireFinishTurn(stopReason)
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

		// 执行工具。
		results := r.executeTools(ctx, toolUses, writer)
		r.history = append(r.history, BlocksMessage("user", results))

		if writer != nil {
			if err := writer.ToolResults(results); err != nil {
				return nil, err
			}
		}
	}

	// 撞上 maxIterations。
	return r.finish(ctx, ResultSubtypeErrorMaxTurns,
		fmt.Sprintf("达到最大回合数 %d，已停止", maxIterations), true, nil)
}

// buildRequest 组装一次 Messages API 请求。
func (r *runner) buildRequest() *MessagesRequest {
	req := &MessagesRequest{
		Model:     r.cfg.Model,
		MaxTokens: r.cfg.MaxTokens,
		System:    r.cfg.System,
		Messages:  r.history,
	}
	if r.cfg.Tools != nil {
		req.Tools = r.cfg.Tools.Defs()
	}
	return req
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
			}

		case EventError:
			return ev.Err
		}
		return nil
	})
	if err != nil {
		return nil, "", err
	}

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
			Content:   result.Content,
			IsError:   result.IsError,
		})
	}
	return results
}

// executeOne 执行单个工具调用，串起审批 → 围栏 → 执行。
func (r *runner) executeOne(ctx context.Context, tu ContentBlock) ToolResult {
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
			if text != "" {
				text += "\n\n"
			}
			text += errText
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
