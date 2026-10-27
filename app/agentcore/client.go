package agentcore

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// 客户端对 /v1/messages 的一次流式调用。
//
// 两条头都要，各管各的：
//   - Authorization-Inner 由 56432 relay 注入，用来标识「哪个用户/设备」；
//   - Authorization 由调用方通过 WithAPIKey 显式带上，是用户自己的 API key，
//     网关据此走 user-api-key 通道，不做分组门禁。
//
// 第二点是实测结论：网关的 key 解析（group_api_key_resolver.ResolveAPIKeyForRequest）
// 先看请求自带的 candidate key，命中用户的 key 就直接用；没有 candidate 才落到
// anthropic-duration 分组的兜底 key——不在该组的新账号会收到
// `group "anthropic-duration" not available for platform "anthropic"`。
// CLI provider 一直自带 key（quick-setup 把 ANTHROPIC_AUTH_TOKEN 写成用户自己的
// {{api_key}}），所以从不出这个问题。

const (
	// anthropicVersion 是 Messages API 的版本头。上游要求必填。
	anthropicVersion = "2023-06-01"

	// maxRetries 是瞬态失败的重试次数。仅覆盖连接错误 / 429 / 5xx。
	maxRetries = 3

	// baseRetryDelay 是指数退避的起点。
	baseRetryDelay = 500 * time.Millisecond

	// maxRetryDelay 是退避上限（同时兜住 Retry-After 的离谱值）。
	maxRetryDelay = 30 * time.Second

	// defaultMaxTokens 是请求体没指定时的上限。
	defaultMaxTokens = 8192

	// sseMaxLineBytes 是单行 SSE 的上限。上游的 input_json_delta 可能很长
	// （大文件的 tool_use 参数），给足余量。
	sseMaxLineBytes = 8 << 20

	// jsonErrorBodyMaxBytes 是「裸 JSON 错误体」的识别上限。真实网关的错误
	// 信封只有一两百字节；设上限只是为了别把一段异常的巨型响应攒进内存。
	jsonErrorBodyMaxBytes = 4096
)

// Client 是 Messages API 的流式客户端。零值不可用，用 NewClient 构造。
type Client struct {
	baseURL string
	http    *http.Client
	// apiKey 是用户自己的平台 API key（sk-...）。走 56432 平台代理时同样要带：
	// 网关用它识别「用哪把 key」，不带就会落到分组兜底、对非该组的账号 403。
	// 留空只在没有可用 key 时发生——此时行为退回到「由代理决定用哪把」。
	apiKey string
	// streamIdle 是 SSE 块间空闲窗口；0 = 禁用（测试用）。由 NewClient 从
	// streamIdleTimeout 环境变量解析结果播种。
	streamIdle time.Duration
}

// NewClient 构造客户端。baseURL 形如 http://127.0.0.1:56432（不带 /v1）。
func NewClient(baseURL string) *Client {
	return &Client{
		baseURL:    strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		streamIdle: streamIdleTimeout,
		http: &http.Client{
			// 不设整体 Timeout：流式响应会持续很久，整体超时会误杀长回合。
			// 连接建立与首字节由 Transport 的 dial/response header 超时兜住。
			Transport: &http.Transport{
				Proxy:                 http.ProxyFromEnvironment,
				MaxIdleConns:          4,
				IdleConnTimeout:       90 * time.Second,
				ResponseHeaderTimeout: 120 * time.Second,
			},
		},
	}
}

// WithAPIKey 设置用户自己的平台 API key。走 56432 平台代理时也要调用——
// 网关靠这个 candidate key 走 user-api-key 通道，不做分组门禁。
// 传空串等于不带，行为退回「由代理按分组兜底决定用哪把 key」。
func (c *Client) WithAPIKey(key string) *Client {
	c.apiKey = strings.TrimSpace(key)
	return c
}

// WithStreamIdleTimeout 覆盖 SSE 块间空闲窗口（0=禁用）。测试用。
func (c *Client) WithStreamIdleTimeout(d time.Duration) *Client {
	c.streamIdle = d
	return c
}

// StreamEvent 是内核从 SSE 流里提取出来的、与传输层解耦的事件。
//
// 内核只关心这几类，其余（ping 等）直接丢弃。
type StreamEvent struct {
	// Kind: "start" | "text" | "image" | "tool_use_start" | "tool_input"
	//       | "block_stop" | "stop" | "error"
	Kind string

	// start
	MessageID string
	Model     string

	// text
	Text string

	// image
	Image *ImageSource

	// tool_use_start / tool_input / block_stop
	Index int
	ID    string
	Name  string

	// tool_input
	PartialJSON string

	// stop
	StopReason string
	Usage      *Usage

	// error
	Err error
}

// 事件种类常量。
const (
	EventStart        = "start"
	EventText         = "text"
	EventImage        = "image"
	EventToolUseStart = "tool_use_start"
	EventToolInput    = "tool_input"
	EventBlockStop    = "block_stop"
	EventStop         = "stop"
	EventError        = "error"
)

// Stream 发起一次流式调用，把事件逐个回调给 onEvent。
//
// onEvent 返回错误即中止本次调用（用于把工具执行/取消信号传回传输层）。
// 返回的 error 是传输层或上游错误；onEvent 自身的错误会原样返回。
func (c *Client) Stream(ctx context.Context, req *MessagesRequest, onEvent func(StreamEvent) error) error {
	if c.baseURL == "" {
		return errors.New("agentcore: base URL 为空")
	}
	if req == nil {
		return errors.New("agentcore: 请求为空")
	}
	if req.MaxTokens <= 0 {
		req.MaxTokens = defaultMaxTokens
	}
	req.Stream = true

	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if attempt > 0 {
			delay := retryDelay(attempt, lastErr)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
			}
		}
		err := c.streamOnce(ctx, req, onEvent)
		if err == nil {
			return nil
		}
		// onEvent 的错误不是传输错误，不重试。
		var abort *eventAbortError
		if errors.As(err, &abort) {
			return abort.cause
		}
		if !isRetryable(err) {
			return err
		}
		lastErr = err
	}
	return fmt.Errorf("agentcore: 重试 %d 次后仍失败: %w", maxRetries, lastErr)
}

// eventAbortError 包住 onEvent 返回的错误，用于把它与传输错误区分开。
type eventAbortError struct{ cause error }

func (e *eventAbortError) Error() string { return e.cause.Error() }
func (e *eventAbortError) Unwrap() error { return e.cause }

// httpStatusError 是上游返回的非 2xx。
type httpStatusError struct {
	status     int
	body       string
	retryAfter time.Duration
}

func (e *httpStatusError) Error() string {
	body := strings.TrimSpace(e.body)
	if len(body) > 512 {
		body = body[:512] + "…"
	}
	if body == "" {
		return fmt.Sprintf("agentcore: 上游返回 HTTP %d", e.status)
	}
	return fmt.Sprintf("agentcore: 上游返回 HTTP %d: %s", e.status, body)
}

// upstreamError 是上游把错误塞进 2xx 响应体里返回的情况。
//
// 实测（2026-10-25，经 56432 本地代理，model=glm-5.3-flash[1m]）：网关限流时
// 回的是 **HTTP 200 + 裸 JSON**：
//
//	{"error":{"message":"Upstream rate limit exceeded, please retry later",
//	          "type":"rate_limit_error"},"type":"error"}
//
// 它既不是非 2xx（状态码检查抓不到），也不是 SSE（每一行都没有 data: 前缀，
// parseSSE 会把整段忽略掉）。不单独识别的话，这条错误就退化成「空流」，
// 用户只拿到一句笼统的空回复，真正的原因（限流）根本传不到手机上。
type upstreamError struct {
	kind    string // 上游 error.type，如 rate_limit_error
	message string
	body    string
}

func (e *upstreamError) Error() string {
	if msg := strings.TrimSpace(e.message); msg != "" {
		if kind := strings.TrimSpace(e.kind); kind != "" {
			return fmt.Sprintf("agentcore: 上游错误(%s): %s", kind, msg)
		}
		return "agentcore: 上游错误: " + msg
	}
	body := strings.TrimSpace(e.body)
	if len(body) > 512 {
		body = body[:512] + "…"
	}
	return "agentcore: 上游返回错误响应: " + body
}

// retryable 判定这个 2xx 内错误值不值得重试。限流与过载是容量问题，
// 退避后重试有意义；其余（如 invalid_request_error）重试只会重复失败。
func (e *upstreamError) retryable() bool {
	switch strings.TrimSpace(e.kind) {
	case "rate_limit_error", "overloaded_error", "api_error":
		return true
	}
	return false
}

// jsonErrorEnvelope 是裸 JSON 错误响应的外壳。
type jsonErrorEnvelope struct {
	Type  string       `json:"type"`
	Error *streamError `json:"error"`
}

// upstreamErrorFromJSON 尝试把一段裸 JSON 响应体解析成 upstreamError。
// 返回 nil 表示「这段还不是完整的错误信封」——可能只是 pretty-print 的中间
// 片段，调用方应继续累积。
func upstreamErrorFromJSON(body string) *upstreamError {
	var env jsonErrorEnvelope
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		return nil
	}
	if env.Error == nil {
		return nil
	}
	if strings.TrimSpace(env.Error.Message) == "" && strings.TrimSpace(env.Error.Type) == "" {
		return nil
	}
	return &upstreamError{kind: env.Error.Type, message: env.Error.Message, body: body}
}

// isRetryable 判定错误是否值得重试：连接类错误、429、5xx、2xx 内的限流/过载。
func isRetryable(err error) bool {
	// 块间停滞不是瞬态：整条流已经断在半开状态，重试只会把本尝试已经下发的
	// start/text 事件帧重复推给手机。
	var idleErr *streamIdleError
	if errors.As(err, &idleErr) {
		return false
	}
	var statusErr *httpStatusError
	if errors.As(err, &statusErr) {
		return statusErr.status == http.StatusTooManyRequests || statusErr.status >= 500
	}
	var upErr *upstreamError
	if errors.As(err, &upErr) {
		return upErr.retryable()
	}
	// 连接重置 / EOF / 超时都属于瞬态。
	return true
}

// retryDelay 计算退避时长，优先尊重上游的 Retry-After。
func retryDelay(attempt int, lastErr error) time.Duration {
	var statusErr *httpStatusError
	if errors.As(lastErr, &statusErr) && statusErr.retryAfter > 0 {
		if statusErr.retryAfter > maxRetryDelay {
			return maxRetryDelay
		}
		return statusErr.retryAfter
	}
	delay := baseRetryDelay << (attempt - 1)
	if delay > maxRetryDelay {
		return maxRetryDelay
	}
	return delay
}

// streamOnce 是单次尝试。
func (c *Client) streamOnce(ctx context.Context, req *MessagesRequest, onEvent func(StreamEvent) error) error {
	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("agentcore: 序列化请求失败: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("agentcore: 构造请求失败: %w", err)
	}
	httpReq.Header.Set("content-type", "application/json")
	httpReq.Header.Set("anthropic-version", anthropicVersion)
	httpReq.Header.Set("accept", "text/event-stream")
	if c.apiKey != "" {
		httpReq.Header.Set("authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return fmt.Errorf("agentcore: 请求上游失败: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return &httpStatusError{
			status:     resp.StatusCode,
			body:       string(raw),
			retryAfter: parseRetryAfter(resp.Header.Get("retry-after")),
		}
	}

	// 成功响应才装块间空闲闸：错误体是一次性读完的，不需要。
	reader := newIdleTimeoutReader(resp.Body, c.streamIdle)
	defer reader.Close()
	return parseSSE(ctx, reader, onEvent)
}

// parseRetryAfter 解析 Retry-After 头（秒数形式；HTTP-date 形式忽略）。
func parseRetryAfter(raw string) time.Duration {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	secs, err := strconv.Atoi(raw)
	if err != nil || secs <= 0 {
		return 0
	}
	return time.Duration(secs) * time.Second
}

// parseSSE 逐行读 SSE，把 data 负载解析成 sseEvent 再翻译成 StreamEvent。
//
// 除了正常 SSE，这里还负责识别一种真实网关会返回的异常形状：HTTP 200 +
// 一段裸 JSON 错误信封（见 upstreamError）。SSE 的合法行只有 event: / data: /
// id: / retry: / 注释，绝不会以 '{' 开头，所以按这个特征做判别不会误伤正常流。
func parseSSE(ctx context.Context, r io.Reader, onEvent func(StreamEvent) error) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), sseMaxLineBytes)

	// 裸 JSON 体的累积缓冲（pretty-print 的 JSON 会跨多行）。
	var jsonBuf strings.Builder
	jsonOverflow := false

	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		line := strings.TrimRight(scanner.Text(), "\r")

		// 空行是事件分隔符；注释行（以 ':' 开头）是保活心跳。
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}

		trimmed := strings.TrimSpace(line)

		// data: 行优先判定。裸 JSON 缓冲里可能还留着上一段内容（例如网关先
		// 回了一个非错误的 JSON 对象），若让它抢在 data: 前面，正常帧会被
		// 拼进缓冲里一起丢掉。
		if strings.HasPrefix(line, "data:") {
			jsonBuf.Reset()
			payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if payload == "" || payload == "[DONE]" {
				continue
			}

			var ev sseEvent
			if err := json.Unmarshal([]byte(payload), &ev); err != nil {
				// 单帧解析失败不该整轮失败：上游偶发会插入非 JSON 保活帧。
				continue
			}

			events, err := translateEvent(&ev)
			if err != nil {
				return err
			}
			for _, out := range events {
				if err := onEvent(out); err != nil {
					return &eventAbortError{cause: err}
				}
			}
			// message_stop 是协议的终止帧。此时整轮内容已经收全，若上游把数据
			// 发完却不关连接（半开最常见的一种形态），继续读只会被块间空闲闸
			// 判成停滞，把一个成功的回合误报成失败；而上游若一直补 ping 保活，
			// ping 会不断续命、永远等不到 EOF。收到终点帧即视为流结束。
			if ev.Type == "message_stop" {
				return nil
			}
			continue
		}

		// 裸 JSON 错误体：以 '{' 开头，后续行是它的续行。
		if jsonBuf.Len() > 0 || strings.HasPrefix(trimmed, "{") {
			if jsonOverflow {
				continue
			}
			if jsonBuf.Len()+len(trimmed) > jsonErrorBodyMaxBytes {
				// 不像一个错误信封，放弃识别，退回「忽略未知行」。
				jsonOverflow = true
				jsonBuf.Reset()
				continue
			}
			jsonBuf.WriteString(trimmed)
			if upErr := upstreamErrorFromJSON(jsonBuf.String()); upErr != nil {
				return upErr
			}
			continue
		}
	}
	if err := scanner.Err(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		// 停滞错误原样上抛：包一层会让 isRetryable 的 errors.As 之外的多处
		// 判定多绕一环，且错误文案会变成「读取流失败: ...」掩盖真因。
		var idleErr *streamIdleError
		if errors.As(err, &idleErr) {
			return idleErr
		}
		return fmt.Errorf("agentcore: 读取流失败: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

// translateEvent 把一个上游 SSE 事件翻译成零到多个内核事件。
func translateEvent(ev *sseEvent) ([]StreamEvent, error) {
	switch ev.Type {
	case "message_start":
		out := StreamEvent{Kind: EventStart}
		if ev.Message != nil {
			out.MessageID = ev.Message.ID
			out.Model = ev.Message.Model
			out.Usage = ev.Message.Usage
		}
		return []StreamEvent{out}, nil

	case "content_block_start":
		if ev.ContentBlock == nil {
			return nil, nil
		}
		switch ev.ContentBlock.Type {
		case BlockTypeText, BlockTypeThinking:
			// 文本块起始本身不带内容，等 delta。
			return nil, nil
		case BlockTypeImage:
			// 图片块是**完整**的（source 在 start 里一次给全，没有 delta），
			// 与文本相反：这里就是它唯一的内容来源，丢掉就没了。
			if ev.ContentBlock.Source == nil {
				return nil, nil
			}
			return []StreamEvent{{
				Kind:  EventImage,
				Index: ev.Index,
				Image: ev.ContentBlock.Source,
			}}, nil
		case BlockTypeToolUse:
			return []StreamEvent{{
				Kind:  EventToolUseStart,
				Index: ev.Index,
				ID:    ev.ContentBlock.ID,
				Name:  ev.ContentBlock.Name,
			}}, nil
		}
		return nil, nil

	case "content_block_delta":
		if ev.Delta == nil {
			return nil, nil
		}
		switch ev.Delta.Type {
		case "text_delta":
			if ev.Delta.Text == "" {
				return nil, nil
			}
			return []StreamEvent{{Kind: EventText, Index: ev.Index, Text: ev.Delta.Text}}, nil
		case "input_json_delta":
			return []StreamEvent{{
				Kind:        EventToolInput,
				Index:       ev.Index,
				PartialJSON: ev.Delta.PartialJSON,
			}}, nil
		}
		// thinking_delta / signature_delta 等首期不消费。
		return nil, nil

	case "content_block_stop":
		return []StreamEvent{{Kind: EventBlockStop, Index: ev.Index}}, nil

	case "message_delta":
		out := StreamEvent{Kind: EventStop, Usage: ev.Usage}
		if ev.Delta != nil {
			out.StopReason = ev.Delta.StopReason
		}
		return []StreamEvent{out}, nil

	case "message_stop":
		// message_delta 已经发过 stop，这里不重复。
		return nil, nil

	case "error":
		msg := "上游返回 error 事件"
		if ev.Error != nil && strings.TrimSpace(ev.Error.Message) != "" {
			msg = ev.Error.Message
		}
		return []StreamEvent{{Kind: EventError, Err: errors.New(msg)}}, nil

	case "ping":
		return nil, nil
	}
	return nil, nil
}
