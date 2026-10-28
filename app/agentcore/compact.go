package agentcore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// 上下文压缩。
//
// 内核自己管理上下文窗口：手机选定的窗口大小（Config.ContextWindow）既是
// 预算上限，也是自动压缩的触发依据。两件事共用同一套逻辑：
//
//   - 自动压缩：每轮 LLM 调用前检查历史规模，超过窗口的 75% 就把较早的消息
//     换成一段模型生成的摘要，只保留最近的一部分原文；
//   - 手动压缩（/compact）：宿主直接调 Compact()，把当前会话历史压一次。
//
// 压缩后的历史形状：一条「摘要」user 消息 + 最近的消息原文。切分点只落在
// assistant 消息或人类 user 消息上，保证 tool_use / tool_result 永远成对——
// 从一条 tool_result 切开会让上游看到一个没有 tool_use 的孤儿结果，直接 400。

const (
	// DefaultContextWindow 是手机没给窗口大小时的兜底（与 PhoneServer 的
	// DEFAULT_CONTEXT_WINDOW 一致）。
	DefaultContextWindow = 200_000

	// 自动压缩阈值：历史估算达到窗口的 3/4 就压缩。留 1/4 余量给下一轮的
	// 输出与工具结果——压到 100% 才动手的话，触发时请求已经因超限被上游
	// 拒绝了。
	autoCompactRatioNum = 3
	autoCompactRatioDen = 4

	// compactTailRatioDen 决定保留多少 recent 原文：窗口的 1/4。
	compactTailRatioDen = 4

	// compactTranscriptRatioDen 决定摘要请求最多带多少旧上下文：窗口的 1/2
	// （剩下一半留给摘要输出与 system prompt）。
	compactTranscriptRatioDen = 2

	// compactSummaryMaxTokens 是摘要回复的输出上限。4096 对长会话太紧：早
	// 期上下文（文件路径、已排除的方案、用户明确要求）一多，摘要在中途就
	// 被截断，丢掉的正是压缩最该保住的部分。8192 覆盖正常摘要长度；遇到
	// 输出上限更低的模型，客户端会按上游 400 自动降级重试。
	compactSummaryMaxTokens = 8192

	// maxAutoCompactsPerRun 防止「刚压完又超限」的抖动在单次 run 里反复烧
	// 摘要请求。4 次对 200 回合的长任务太少了：一次压缩只腾出约 3/4 窗口，
	// 长任务很快又填满，撞到上限后就不再压缩、直到被上游拒绝。10 次仍能
	// 兜住抖动，同时让长任务走完全程。
	maxAutoCompactsPerRun = 10

	// compactFailureCooldownTurns 是一次自动压缩失败后跳过自动压缩判定的
	// 回合数。摘要请求要把全窗口输入重烧一遍，失败后每回合原样重试就是
	// 「压缩失败熔炉」：历史仍超阈值 → 每个回合间再压一次 → 每回合都垫上
	// 一次全窗口摘要请求的钱与延迟。冷却 3 个回合再试，给瞬态故障留恢复
	// 窗口。
	compactFailureCooldownTurns = 3

	// compactFailureLimit 是本 run 内**连续**自动压缩失败多少次后彻底禁用
	// 自动压缩。连续 3 次失败说明不是抖动（上游限流、网络中断），继续按节奏
	// 重试只会每 4 个回合烧一次摘要请求；禁用后带着长上下文继续跑，真被上游
	// 以 prompt too long 拒绝时仍有 noteContextOverflow 的降级压缩路径兜底
	// （那条路不经本判定，不受禁用影响）。
	compactFailureLimit = 3

	// compactRenderEntryMaxChars 限制单条消息进摘要请求的长度：一条巨大的
	// 工具输出不该吃掉整个摘要预算。
	compactRenderEntryMaxChars = 8000
)

// ErrNothingToCompact 表示历史还没有多到值得压缩。
var ErrNothingToCompact = errors.New("agentcore: 没有可压缩的历史消息")

// CompactInfo 描述一次完成的压缩。
type CompactInfo struct {
	// Trigger 是触发方式："auto"（自动阈值）或 "manual"（/compact）。
	Trigger string
	// PreTokens / PostTokens 是压缩前后的估算 token 数。
	PreTokens  int
	PostTokens int
	// Removed 是被摘要替换掉的消息条数。
	Removed int
}

// EstimateTokens 粗略估算一组消息的 token 数。
//
// 不追求与某个具体 tokenizer 对齐：压缩只需要一个稳定、偏保守的量级判断。
// 经验规则：ASCII 约 4 字符/token；非 ASCII（中文、emoji）按 1 rune/token
// 计——中文在 UTF-8 里 3 字节/字，按字节除 4 会低估近一个数量级，而低估的
// 后果是压缩触发太晚，直接撞上上游的 prompt too long。
func EstimateTokens(messages []Message) int {
	total := 0
	for _, m := range messages {
		total += estimateMessageTokens(m)
	}
	return total
}

func estimateTextTokens(text string) int {
	if text == "" {
		return 0
	}
	ascii, wide := 0, 0
	for _, r := range text {
		if r < utf8.RuneSelf {
			ascii++
		} else {
			wide++
		}
	}
	return (ascii+3)/4 + wide
}

func estimateMessageTokens(m Message) int {
	// 每条消息的角色/分隔符开销。
	return 4 + estimateTextTokens(m.Role) + estimateContentTokens(m.Content)
}

func estimateContentTokens(content interface{}) int {
	switch c := content.(type) {
	case nil:
		return 0
	case string:
		return estimateTextTokens(c)
	case []ContentBlock:
		total := 0
		for _, b := range c {
			total += estimateBlockTokens(b)
		}
		return total
	default:
		raw, err := json.Marshal(content)
		if err != nil {
			return 0
		}
		return estimateTextTokens(string(raw))
	}
}

func estimateBlockTokens(b ContentBlock) int {
	switch b.Type {
	case BlockTypeText, BlockTypeThinking:
		return estimateTextTokens(b.Text)
	case BlockTypeToolUse:
		raw, _ := json.Marshal(b.Input)
		return estimateTextTokens(b.Name) + estimateTextTokens(string(raw))
	case BlockTypeToolResult:
		return estimateContentTokens(b.Content)
	case BlockTypeImage:
		// 图片按上游常见的「一张图 ≈ 1600 tokens」粗估。宁可高估：低估会让
		// 带图会话压不动、最终被上游拒绝。
		return 1600
	default:
		return 0
	}
}

// Compact 把较早的历史消息替换成一段模型生成的摘要。
//
// 返回的新消息列表可以直接作为下一轮 Run 的输入，也可以由宿主落库成新历史。
// trigger 只用于 CompactInfo（"auto" / "manual"），不影响压缩行为。
func Compact(ctx context.Context, cfg *Config, messages []Message, trigger string) ([]Message, CompactInfo, error) {
	if cfg == nil || cfg.Client == nil {
		return nil, CompactInfo{}, errors.New("agentcore: 未配置 Client")
	}
	if strings.TrimSpace(cfg.Model) == "" {
		return nil, CompactInfo{}, errors.New("agentcore: 未配置 Model")
	}
	window := cfg.ContextWindow
	if window <= 0 {
		window = DefaultContextWindow
	}

	preTokens := EstimateTokens(messages)
	split := compactSplitIndex(messages, window)
	if split <= 0 || split >= len(messages) {
		return nil, CompactInfo{}, ErrNothingToCompact
	}

	summary, err := compactSummarize(ctx, cfg, messages[:split], window)
	if err != nil {
		return nil, CompactInfo{}, err
	}
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return nil, CompactInfo{}, errors.New("agentcore: 压缩摘要为空")
	}

	out := make([]Message, 0, len(messages)-split+1)
	out = append(out, TextMessage("user", compactSummaryHeader+summary))
	out = append(out, messages[split:]...)
	return out, CompactInfo{
		Trigger:    trigger,
		PreTokens:  preTokens,
		PostTokens: EstimateTokens(out),
		Removed:    split,
	}, nil
}

// compactSplitIndex 选一个切分点：messages[:split] 拿去摘要，messages[split:]
// 保留原文。返回 0 表示没有值得压缩的旧消息。
func compactSplitIndex(messages []Message, window int) int {
	tailBudget := window / compactTailRatioDen
	used := 0
	i := len(messages)
	for i > 0 {
		t := estimateMessageTokens(messages[i-1])
		if used+t > tailBudget && used > 0 {
			break
		}
		used += t
		i--
	}
	if i <= 0 {
		// 全部 recent 消息都在尾部预算内，没什么可压的。
		return 0
	}
	if !isCompactTailBoundary(messages[i]) {
		// 候选点落在 tool_result 上：往前退到最近的合法边界（通常是它的
		// assistant tool_use）。宁可多保留一条原文，也不制造孤儿结果。
		j := i - 1
		for j > 0 && !isCompactTailBoundary(messages[j]) {
			j--
		}
		if j <= 0 {
			return 0
		}
		i = j
	}
	return i
}

// isCompactTailBoundary 判断一条消息能否作为「保留原文」的第一条。
//
// assistant 消息可以（它的 tool_use 会把后续 tool_result 一起带进尾部）；
// 人类 user 消息可以；tool_result 构成的 user 消息不可以——它必须和前面的
// assistant tool_use 在一起。
func isCompactTailBoundary(m Message) bool {
	role := strings.ToLower(strings.TrimSpace(m.Role))
	switch role {
	case "assistant":
		return true
	case "user":
	default:
		return false
	}
	switch c := m.Content.(type) {
	case nil:
		return false
	case string:
		return strings.TrimSpace(c) != ""
	case []ContentBlock:
		for _, b := range c {
			if b.Type == BlockTypeToolResult {
				return false
			}
		}
		return true
	default:
		return true
	}
}

const compactSummaryHeader = "以下是本会话此前内容的压缩摘要（由系统自动生成，用于延续对话）：\n\n"

const compactSummarySystem = `你是上下文压缩器，为一个正在进行的编程助手会话生成可以继续工作的压缩摘要。

要求：
- 保留完成任务必需的信息：用户目标与约束、已确认的方案与决策、改动过的文件路径及其状态、执行过的关键命令与结果、未解决的问题与 TODO。
- 丢弃寒暄、重复内容和无关细节。
- 用要点列表，紧凑、信息密度高。
- 用会话原文的语言书写。
- 不要输出任何前言、解释或元评论，直接给摘要正文。`

const compactSummaryInstruction = "请为上面这段对话生成压缩摘要。\n\n===== 对话开始 =====\n"

// compactSummarize 用一次不带工具的 Messages 调用生成前缀摘要。
//
// 整个调用带**最多一次**安全重试。为什么这里重试安全而回合主流不行（Stream
// 的 delivered 守卫一刀切禁止已下发事件后的重试）：压缩的全部状态都悬在本次
// 闭包里——builder 是局部变量，出错即弃，重试从零开始，不会像回合主流那样把
// 第二次尝试的 delta 缝在半截文本后面；压缩期间内核不向宿主发任何事件帧，
// 重试也不会让宿主/手机看到重复事件。回合主流恰恰相反：文本累积与已下发帧
// 都挂在调用方身上，重试必然重复内容。ctx 取消/超时不重试——那不是上游的
// 瞬态故障，重试只会拖住取消路径。
func compactSummarize(ctx context.Context, cfg *Config, prefix []Message, window int) (string, error) {
	transcript := renderCompactTranscript(prefix, window/compactTranscriptRatioDen)
	if strings.TrimSpace(transcript) == "" {
		return "", ErrNothingToCompact
	}
	req := &MessagesRequest{
		Model:     cfg.Model,
		MaxTokens: compactSummaryMaxTokens,
		System:    compactSummarySystem,
		Messages:  []Message{TextMessage("user", compactSummaryInstruction+transcript)},
	}
	attempt := func() (string, error) {
		var b strings.Builder
		err := cfg.Client.Stream(ctx, req, func(ev StreamEvent) error {
			switch ev.Kind {
			case EventText:
				b.WriteString(ev.Text)
			case EventError:
				return ev.Err
			}
			return nil
		})
		if err != nil {
			return "", fmt.Errorf("agentcore: 压缩摘要请求失败: %w", err)
		}
		return b.String(), nil
	}
	summary, err := attempt()
	if err == nil {
		return summary, nil
	}
	if ctx.Err() != nil ||
		errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) {
		return "", err
	}
	retried, retryErr := attempt()
	if retryErr == nil {
		return retried, nil
	}
	return "", fmt.Errorf("agentcore: 压缩摘要重试仍失败: %w（首次失败: %v）", retryErr, err)
}

// renderCompactTranscript 把前缀消息渲染成纯文本转录，带 token 预算。
//
// 预算不够时从最早的消息开始丢：靠近现在的旧上下文（用户最近在做什么）比
// 会话开头的寒暄更有价值。
func renderCompactTranscript(messages []Message, maxTokens int) string {
	if maxTokens <= 0 {
		maxTokens = DefaultContextWindow / compactTranscriptRatioDen
	}
	kept := make([]string, 0, len(messages))
	used := 0
	for i := len(messages) - 1; i >= 0; i-- {
		line := renderCompactMessage(messages[i])
		if line == "" {
			continue
		}
		t := estimateTextTokens(line)
		if used+t > maxTokens {
			break
		}
		used += t
		kept = append(kept, line)
	}
	var b strings.Builder
	for i := len(kept) - 1; i >= 0; i-- {
		b.WriteString(kept[i])
		b.WriteString("\n\n")
	}
	return strings.TrimSpace(b.String())
}

func renderCompactMessage(m Message) string {
	text := strings.TrimSpace(renderCompactContent(m.Content))
	if text == "" {
		return ""
	}
	return compactRoleLabel(m.Role) + text
}

func compactRoleLabel(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "user":
		return "用户: "
	case "assistant":
		return "助手: "
	default:
		return role + ": "
	}
}

func renderCompactContent(content interface{}) string {
	switch c := content.(type) {
	case nil:
		return ""
	case string:
		return truncateCompactText(c)
	case []ContentBlock:
		parts := make([]string, 0, len(c))
		for _, b := range c {
			switch b.Type {
			case BlockTypeText, BlockTypeThinking:
				if s := strings.TrimSpace(b.Text); s != "" {
					parts = append(parts, truncateCompactText(s))
				}
			case BlockTypeToolUse:
				raw, _ := json.Marshal(b.Input)
				parts = append(parts, fmt.Sprintf("[调用工具 %s %s]", b.Name, truncateCompactText(string(raw))))
			case BlockTypeToolResult:
				s := strings.TrimSpace(renderCompactContent(b.Content))
				if b.IsError {
					s = "错误: " + s
				}
				parts = append(parts, "[工具结果] "+truncateCompactText(s))
			case BlockTypeImage:
				parts = append(parts, "[图片]")
			}
		}
		return strings.Join(parts, "\n")
	default:
		raw, err := json.Marshal(content)
		if err != nil {
			return ""
		}
		return truncateCompactText(string(raw))
	}
}

func truncateCompactText(s string) string {
	if len(s) <= compactRenderEntryMaxChars {
		return s
	}
	runes := []rune(s)
	if len(runes) > compactRenderEntryMaxChars {
		runes = runes[:compactRenderEntryMaxChars]
	}
	return string(runes) + "…"
}
