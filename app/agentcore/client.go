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
// 不发 Authorization：56432 平台代理在 relay 层注入 Authorization-Inner。这与
// claude-code / pi 走同一条路径，凭据永远不经过本进程。

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
)

// Client 是 Messages API 的流式客户端。零值不可用，用 NewClient 构造。
type Client struct {
	baseURL string
	http    *http.Client
	// apiKey 仅用于自配远端端点（非 56432 代理）的直连场景。走平台代理时
	// 必须留空——代理会注入自己的凭据，多带一个 Authorization 反而会让
	// 上游拒绝。
	apiKey string
}

// NewClient 构造客户端。baseURL 形如 http://127.0.0.1:56432（不带 /v1）。
func NewClient(baseURL string) *Client {
	return &Client{
		baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
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

// WithAPIKey 给自配远端端点用。走平台代理时不要调用。
func (c *Client) WithAPIKey(key string) *Client {
	c.apiKey = strings.TrimSpace(key)
	return c
}

// StreamEvent 是内核从 SSE 流里提取出来的、与传输层解耦的事件。
//
// 内核只关心这几类，其余（ping 等）直接丢弃。
type StreamEvent struct {
	// Kind: "start" | "text" | "tool_use_start" | "tool_input" | "block_stop"
	//       | "stop" | "error"
	Kind string

	// start
	MessageID string
	Model     string

	// text
	Text string

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

// isRetryable 判定错误是否值得重试：连接类错误、429、5xx。
func isRetryable(err error) bool {
	var statusErr *httpStatusError
	if errors.As(err, &statusErr) {
		return statusErr.status == http.StatusTooManyRequests || statusErr.status >= 500
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
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return &httpStatusError{
			status:     resp.StatusCode,
			body:       string(raw),
			retryAfter: parseRetryAfter(resp.Header.Get("retry-after")),
		}
	}

	return parseSSE(ctx, resp.Body, onEvent)
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
func parseSSE(ctx context.Context, r io.Reader, onEvent func(StreamEvent) error) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), sseMaxLineBytes)

	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		line := strings.TrimRight(scanner.Text(), "\r")

		// 空行是事件分隔符；注释行（以 ':' 开头）是保活心跳。
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
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
	}
	if err := scanner.Err(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
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
