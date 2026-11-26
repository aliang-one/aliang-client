package services

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"aliang.one/nursorgate/common/logger"
)

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
// v2 新增 Header/Summary/Version 均可选：旧 JSON 无这些字段零影响。

type agentAIOptionRequest struct {
	ID          string                `json:"option_id,omitempty"`
	SessionID   string                `json:"session_id,omitempty"`
	MessageID   string                `json:"message_id,omitempty"`
	Provider    string                `json:"provider,omitempty"`
	Title       string                `json:"title,omitempty"`
	Header      string                `json:"header,omitempty"`
	Summary     string                `json:"summary,omitempty"`
	Version     int                   `json:"v,omitempty"`
	Options     []agentAIOptionChoice `json:"options"`
	AllowCustom bool                  `json:"allow_custom"`
	Multi       bool                  `json:"multi"`
}

type agentAIOptionChoice struct {
	ID          string   `json:"id"`
	Label       string   `json:"label"`
	Description string   `json:"description,omitempty"`
	Response    string   `json:"response,omitempty"`
	Previews    []string `json:"previews,omitempty"`
}

// aliangOptionsBlockRe 匹配 ```aliang-options\n<json>\n``` 围栏块（DOTALL）。

var aliangOptionsBlockRe = regexp.MustCompile("(?s)```aliang-options\\s*\\n(.*?)\\n```")

// agentAIOptionIDRe v2 id 硬规则：≤48 字符 ASCII slug（大小写均可，模型常写
// BlueGreen 之类混合形式，一律重造反而制造 churn）。

var agentAIOptionIDRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,47}$`)

const agentAIOptionMaxCount = 8

// normalizeAgentAIOptionRequest 对模型产出的块做硬规范化（prompt 只能劝，这里必须拦）：
// 空 label 选项丢弃；id 缺失/非法/重复 → 重造 option-N（撞已有合法 id 时顺延）；
// 清洗后再截到 8——先截后丢会白丢名额。重造后的 id 会随 ai.option.request
// 下发、作答原样回传，全链自洽。

func normalizeAgentAIOptionRequest(req *agentAIOptionRequest) {
	seen := make(map[string]bool, len(req.Options))
	kept := make([]agentAIOptionChoice, 0, agentAIOptionMaxCount)
	for _, opt := range req.Options {
		if strings.TrimSpace(opt.Label) == "" {
			continue
		}
		if !agentAIOptionIDRe.MatchString(opt.ID) || seen[opt.ID] {
			opt.ID = fmt.Sprintf("option-%d", len(kept)+1)
			for seen[opt.ID] {
				opt.ID = opt.ID + "x"
			}
		}
		seen[opt.ID] = true
		kept = append(kept, opt)
		if len(kept) == agentAIOptionMaxCount {
			break
		}
	}
	req.Options = kept
}

// extractAgentAIOptionBlocks 扫描 assistant 完整输出，提取所有 aliang-options 块。
// 无效 JSON 或 options 为空的块丢弃（JSON 解析失败不再静默：打 Warn 日志，
// 否则模型写坏围栏=用户什么都看不到且无迹可查）。
// 闭合围栏缺失的退化形态（GLM 偶发漏闭合行，2026-11-15 本机 journal 实锤
// 4/34≈12%：黏行开头 + JSON 写到 } 就收尾，一次被字面 "..." 顶替闭合行）
// 走 extractUnclosedAgentAIOptionBlocks 兜底。

func extractAgentAIOptionBlocks(output string) []agentAIOptionRequest {
	matches := aliangOptionsBlockRe.FindAllStringSubmatch(output, -1)
	out := make([]agentAIOptionRequest, 0, len(matches))
	for _, m := range matches {
		var req agentAIOptionRequest
		if err := json.Unmarshal([]byte(m[1]), &req); err != nil {
			logger.Warn(fmt.Sprintf("[ai.option] aliang-options block dropped: bad JSON (%d bytes): %v", len(m[1]), err))
			continue
		}
		normalizeAgentAIOptionRequest(&req)
		if len(req.Options) == 0 {
			continue
		}
		out = append(out, req)
	}
	out = append(out, extractUnclosedAgentAIOptionBlocks(output)...)
	return out
}

// aliangOptionsBalancedJSONPrefix 括号配平截取 s 中首个完整 JSON 对象
// （字符串感知：引号内的花括号/转义不参与配平）。截不出返回 false。

func aliangOptionsBalancedJSONPrefix(s string) (string, bool) {
	start := strings.IndexByte(s, '{')
	if start < 0 {
		return "", false
	}
	depth := 0
	inStr, esc := false, false
	for i := start; i < len(s); i++ {
		ch := s[i]
		if inStr {
			switch {
			case esc:
				esc = false
			case ch == '\\':
				esc = true
			case ch == '"':
				inStr = false
			}
			continue
		}
		switch ch {
		case '"':
			inStr = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start : i+1], true
			}
		}
	}
	return "", false
}

// extractUnclosedAgentAIOptionBlocks 兜底提取闭合缺失的退化围栏。只有开标签
// 之后不再出现任何闭合行（\n```）的块才走这里——有闭合行的歧义形态（标签后
// 跟正文又接普通代码块）保持旧行为丢弃，避免把无关正文误吞成卡。合法性由
// json.Unmarshal + normalize 把关，闭合行不是真闸门。

func extractUnclosedAgentAIOptionBlocks(output string) []agentAIOptionRequest {
	tag := agentAIOptionFenceTag
	var out []agentAIOptionRequest
	rest := output
	for {
		idx := strings.Index(rest, tag)
		if idx < 0 {
			return out
		}
		rest = rest[idx+len(tag):]
		if strings.Contains(rest, "\n```") {
			continue
		}
		body, ok := aliangOptionsBalancedJSONPrefix(rest)
		if !ok {
			return out
		}
		var req agentAIOptionRequest
		if err := json.Unmarshal([]byte(body), &req); err != nil {
			logger.Warn(fmt.Sprintf("[ai.option] unclosed aliang-options block dropped: bad JSON (%d bytes): %v", len(body), err))
			return out
		}
		normalizeAgentAIOptionRequest(&req)
		if len(req.Options) > 0 {
			out = append(out, req)
		}
		return out
	}
}

// agentAIOptionFenceTag 是 aliang-options 围栏开标签字面量（与
// extractUnclosedAgentAIOptionBlocks 的截断判定同源）。
const agentAIOptionFenceTag = "```aliang-options"

// agentAIOptionFencePreserveGraceRunes 围栏保活的追加预算：截断点切进
// aliang-options 围栏时，允许把完整围栏带过传输层的最大超出量。正常围栏受
// agentAIOptionSystemPrompt（≤8 选项、字段一句话）约束在千字节量级；超预算
// 的病态巨围栏回落普通截断，由渲染端的截断围栏容忍兜底（吞残块不外晒）。
const agentAIOptionFencePreserveGraceRunes = 8192

// agentAIOptionFenceBodyLooksLikeJSON 判定 tag 之后的文本形态是否像真实围栏
// 体：tag 行只含 tag（允许水平空白与 \r 收行），下一行以 { 开头（允许行首
// 空白）——对齐 agentAIOptionSystemPrompt 的 tag 行纯度约定。散文内联提及
// 标签（同行还有正文）与 tag 行后跟非 JSON 正文都不满足，不触发保活（对抗
// 评审坐实的误伤面：无关正文被当围栏残段延长带过传输）。
func agentAIOptionFenceBodyLooksLikeJSON(afterTag string) bool {
	i := 0
	for i < len(afterTag) && (afterTag[i] == ' ' || afterTag[i] == '\t' || afterTag[i] == '\r') {
		i++
	}
	if i >= len(afterTag) || afterTag[i] != '\n' {
		return false
	}
	i++
	for i < len(afterTag) && (afterTag[i] == ' ' || afterTag[i] == '\t') {
		i++
	}
	return i < len(afterTag) && afterTag[i] == '{'
}

// truncateAgentTextOptionFenceAware 在 truncateAgentText 的传输限幅上叠加
// 围栏保活：截断若切进 aliang-options 围栏（开标签已入窗、闭合 ``` 在窗外，
// 或围栏整体在窗外），把闭合前的整段一并保留——接收端的静态选项卡解析
// （围栏正则要闭合 ```、裸 JSON 兜底要配平 {}）对半截围栏双双失效
// （2026-11-25 local-mwl6wqyvv5ysrg 手机端选项卡消失事故：4565 rune 消息、
// 围栏位于 3490，4000 截断点切在 JSON 中间）。只延长不缩短：围栏完整落在
// 截断窗口内时与 truncateAgentText 逐字节一致；原文自身未闭合（GLM 偶发
// 退化）与超预算的病态巨围栏同样回落普通截断，渲染端负责兜底。
//
// 逐块扫描（对抗评审 minor 修正）：不锚最后一个 tag——尾部跟未闭合 tag 或
// 巨围栏时，真正骑跨截断点的可能是更早的完整围栏，必须逐块找到「闭合越过
// 窗口」的那一个做保活判定。
func truncateAgentTextOptionFenceAware(value string, max int) string {
	value = strings.TrimSpace(value)
	if max <= 0 || utf8.RuneCountInString(value) <= max {
		return truncateAgentText(value, max)
	}
	head := strings.TrimSpace(string([]rune(value)[:max]))
	search := 0
	for {
		rel := strings.Index(value[search:], agentAIOptionFenceTag)
		if rel < 0 {
			return head
		}
		openAbs := search + rel
		afterTag := value[openAbs+len(agentAIOptionFenceTag):]
		closeRel := strings.Index(afterTag, "\n```")
		// tag 行不纯（散文提及）或原文未闭合：不是可保活的围栏，看下一块。
		if closeRel < 0 || !agentAIOptionFenceBodyLooksLikeJSON(afterTag) {
			search = openAbs + len(agentAIOptionFenceTag)
			continue
		}
		endAbs := openAbs + len(agentAIOptionFenceTag) + closeRel + len("\n```")
		endRunes := utf8.RuneCountInString(value[:endAbs])
		if endRunes <= max {
			// 闭合在窗内：普通截断没切到它，继续找后面的。
			search = endAbs
			continue
		}
		if endRunes > max+agentAIOptionFencePreserveGraceRunes {
			// 病态巨围栏超预算：其后围栏的终点只会更远，一并回落普通截断。
			return head
		}
		return strings.TrimSpace(value[:endAbs])
	}
}

// buildAgentAIOptionFollowup 把用户的选择拼成下一轮的 user prompt。
// Claude 经 --resume 续接时，这段文本作为新的用户输入。
// v2：选项带 response（模型预写的确认文案）时优先原样回填，语义最强；
// 否则退回 label（description）旧形态。

func buildAgentAIOptionFollowup(req *agentAIOptionRequest, selected []string, custom string) string {
	var b strings.Builder
	b.WriteString("用户已在上述方案中做出选择，请据此继续，不要再重复列出方案：\n")
	choiceByID := make(map[string]agentAIOptionChoice, len(req.Options))
	for _, opt := range req.Options {
		choiceByID[opt.ID] = opt
	}
	for _, id := range selected {
		opt, ok := choiceByID[id]
		if !ok {
			b.WriteString("- 选择了未知选项 id=" + id + "\n")
			continue
		}
		if resp := strings.TrimSpace(opt.Response); resp != "" {
			b.WriteString("- 选择了：" + resp + "\n")
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
	// expiresAt 是审批等待的绝对截止（requestApproval 时刻 + agentAIApprovalTimeout）。
	// pending 帧与两端快照带 expires_at 供 UI 显示剩余时间（spec §4.2.1）。
	expiresAt time.Time
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
