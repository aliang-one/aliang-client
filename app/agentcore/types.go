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
	Model     string    `json:"model"`
	MaxTokens int       `json:"max_tokens"`
	System    string    `json:"system,omitempty"`
	Messages  []Message `json:"messages"`
	Tools     []ToolDef `json:"tools,omitempty"`
	Stream    bool      `json:"stream"`

	// Temperature 等采样参数首期不下发：不同上游模型对取值范围的容忍度不
	// 一致，交给服务端默认值更稳。
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
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"input_schema"`
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
//	input_json_delta  → PartialJSON
//	message_delta     → StopReason
type streamDelta struct {
	Type        string `json:"type"`
	Text        string `json:"text,omitempty"`
	PartialJSON string `json:"partial_json,omitempty"`
	StopReason  string `json:"stop_reason,omitempty"`
}

// streamError 是流内 error 事件。
type streamError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}
