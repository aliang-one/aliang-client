// Package agentcore 是 alianggate 内置的 agent 内核：自带 LLM 客户端、agent
// 循环与 pi 对等的 8 个工具，装好客户端即可进行基础 AI 对话与写代码，不依赖
// 任何外部 CLI。
//
// 包边界是刻意的：本包不得 import app/http/services。与宿主（services 侧）的
// 唯一耦合是 Deps 里的几个函数字段，由宿主注入。这样内核可以单独测试，也能
// 在将来被别的宿主复用。
//
// 出网走本机 56432 平台代理（与 claude-code / pi 完全同一条路径）：agent 子
// 进程不是会话权威，不持有 token；56432 的 shouldForceAliangRoute 会把任何
// 回环:56432 的连接强制路由到 aliang 隧道，由 relay 层注入 Authorization-Inner。
// 因此本包不做任何凭据管理。
package agentcore

import "encoding/base64"

// Anthropic Messages API 的请求/响应形状。
//
// 只覆盖内核实际用到的字段：流式、工具调用、多轮 tool_result。刻意不追求
// 完整覆盖 Messages API——字段越少，与上游形状漂移时的暴露面越小。

// MessagesRequest 是一次 /v1/messages 调用的请求体。
type MessagesRequest struct {
	Model     string `json:"model"`
	MaxTokens int    `json:"max_tokens"`
	// System 既可以是纯字符串，也可以是 []SystemBlock。
	//
	// 用 interface{} 而不是直接定成块数组：绝大多数调用点只想塞一段文本，
	// 强制造块数组等于给每个测试和子调用加一层噪音。挂 prompt cache 断点时
	// 由 applyCacheBreakpoints 就地升级成块形态（见 cache.go）。
	System   interface{} `json:"system,omitempty"`
	Messages []Message   `json:"messages"`
	Tools    []ToolDef   `json:"tools,omitempty"`
	Stream   bool        `json:"stream"`
	// OutputConfig 承载 output_config 请求参数（Claude Messages API 语义：
	// effort 挂在 output_config 里，不是顶层字段）。nil = 不下发。
	OutputConfig *OutputConfig `json:"output_config,omitempty"`

	// Temperature 等采样参数首期不下发：不同上游模型对取值范围的容忍度不
	// 一致，交给服务端默认值更稳。
}

// OutputConfig 是 Messages API 的 output_config 参数。目前只下发 effort：
// 推理努力档位，与 claude CLI 的 --effort、codex 的模型名后缀殊途同归——
// 上游网关对同一字段消费（claude_code 路径即由 CLI 把 effort 放进
// output_config 下发，网关/GLM 侧已验证接受）。
type OutputConfig struct {
	Effort string `json:"effort,omitempty"`
}

// Message 是一条对话消息。Content 既可以是纯字符串，也可以是内容块数组；
// 用 interface{} 承载是为了让 tool_use / tool_result 能原样往返。
type Message struct {
	Role    string      `json:"role"`
	Content interface{} `json:"content"`
}

// TextMessage 构造一条纯文本消息。
func TextMessage(role, text string) Message {
	return Message{Role: role, Content: text}
}

// BlocksMessage 构造一条内容块消息（tool_use / tool_result 用）。
func BlocksMessage(role string, blocks []ContentBlock) Message {
	return Message{Role: role, Content: blocks}
}

// ContentBlock 是内容块。Type 决定哪些字段有效：
//
//	"text"        → Text
//	"image"       → Source
//	"tool_use"    → ID, Name, Input
//	"tool_result" → ToolUseID, Content, IsError
type ContentBlock struct {
	Type string `json:"type"`

	// text
	Text string `json:"text,omitempty"`

	// image
	Source *ImageSource `json:"source,omitempty"`

	// tool_use
	ID    string                 `json:"id,omitempty"`
	Name  string                 `json:"name,omitempty"`
	Input map[string]interface{} `json:"input,omitempty"`

	// tool_result
	ToolUseID string      `json:"tool_use_id,omitempty"`
	Content   interface{} `json:"content,omitempty"`
	IsError   bool        `json:"is_error,omitempty"`

	// cache_control 只在下发方向有意义：请求尾部块打一个断点，让上游把整段
	// 前缀缓存起来。响应解析时上游不会回填这个字段，留 nil 即可。
	CacheControl *CacheControl `json:"cache_control,omitempty"`
}

// ImageSource 是 image 块的来源。
//
// 只支持 base64 与 url 两种。base64 是主路径：手机上传的附件在宿主侧已经落
// 到本地临时文件，读成字节后直接内联，不依赖上游能不能回连我们的 COS 预签名
// URL（预签名 600 秒就过期，而历史消息可能活很久）。
//
//	Type == "base64" → MediaType + Data
//	Type == "url"    → URL
type ImageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type,omitempty"`
	Data      string `json:"data,omitempty"`
	URL       string `json:"url,omitempty"`
}

// ImageBlock 构造一个 base64 image 块。
func ImageBlock(mediaType string, data []byte) ContentBlock {
	return ContentBlock{
		Type: BlockTypeImage,
		Source: &ImageSource{
			Type:      "base64",
			MediaType: mediaType,
			Data:      base64.StdEncoding.EncodeToString(data),
		},
	}
}

// ImageURLBlock 构造一个 url image 块（上游自行拉取）。
func ImageURLBlock(url, mediaType string) ContentBlock {
	return ContentBlock{
		Type:   BlockTypeImage,
		Source: &ImageSource{Type: "url", URL: url, MediaType: mediaType},
	}
}

// ToolDef 是下发给模型的一个工具定义。
type ToolDef struct {
	Name         string                 `json:"name"`
	Description  string                 `json:"description"`
	InputSchema  map[string]interface{} `json:"input_schema"`
	CacheControl *CacheControl          `json:"cache_control,omitempty"`
}

// 停止原因。只有 max_tokens 会触发内核的截断守卫。
const (
	StopReasonEndTurn   = "end_turn"
	StopReasonToolUse   = "tool_use"
	StopReasonMaxTokens = "max_tokens"
)

// 内容块类型常量。
const (
	BlockTypeText       = "text"
	BlockTypeImage      = "image"
	BlockTypeToolUse    = "tool_use"
	BlockTypeToolResult = "tool_result"
	BlockTypeThinking   = "thinking"
)

// Usage 是上游返回的 token 用量。
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`

	// 下面两个是 prompt cache 的记账字段（Anthropic Messages API 语义）。
	// 不解析它们，缓存到底有没有命中就永远是瞎的——线上「慢」的排查会退化成
	// 靠字节账本反推（2026-10-27 的 30 分钟跑就是这么查出来的）。
	//
	// CacheCreationInputTokens：本轮写进缓存的 token（首次或前缀变化时非 0）。
	// CacheReadInputTokens：本轮从缓存读回的 token（命中时接近整个前缀）。
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`
}

// CacheControl 是内容块上的缓存断点标记。
//
// 目前只有 "ephemeral"（5 分钟 TTL，读写都按上游定价打折）一种取值。
type CacheControl struct {
	Type string `json:"type"`
}

// SystemBlock 是 system 字段的块形态。挂 cache_control 必须走块数组——
// 纯字符串形态没有办法承载断点。
type SystemBlock struct {
	Type         string        `json:"type"`
	Text         string        `json:"text"`
	CacheControl *CacheControl `json:"cache_control,omitempty"`
}

// sseEvent 是 SSE 流里的一个事件（已解析掉 "data: " 前缀）。
type sseEvent struct {
	Type string `json:"type"`

	// message_start
	Message *streamMessage `json:"message,omitempty"`

	// content_block_start
	Index        int           `json:"index,omitempty"`
	ContentBlock *ContentBlock `json:"content_block,omitempty"`

	// content_block_delta
	Delta *streamDelta `json:"delta,omitempty"`

	// message_delta
	Usage *Usage `json:"usage,omitempty"`

	// error
	Error *streamError `json:"error,omitempty"`
}

// streamMessage 是 message_start 事件里的 message 对象。
type streamMessage struct {
	ID    string `json:"id"`
	Model string `json:"model"`
	Role  string `json:"role"`
	Usage *Usage `json:"usage,omitempty"`
}

// streamDelta 是各类 delta 的并集。
//
//	text_delta        → Text
//	thinking_delta    → Thinking（Anthropic 语义的载荷键就是 thinking，不是 text；
//	                  个别网关走 text，translateEvent 两个都收）
//	input_json_delta  → PartialJSON
//	message_delta     → StopReason
type streamDelta struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
	// thinking_delta 的思考文本。流式转发给宿主（writer 帧 → ai.thinking →
	// 手机渲染思考 + 服务层 markProgress 喂活性），并落 journal transcript；
	// 绝不进对话历史与结果正文（回传无签名思考块会被上游拒）。
	Thinking    string `json:"thinking,omitempty"`
	PartialJSON string `json:"partial_json,omitempty"`
	StopReason  string `json:"stop_reason,omitempty"`
}

// streamError 是流内 error 事件。
type streamError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}
