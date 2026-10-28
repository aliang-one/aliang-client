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

	// maxRetries 是瞬态失败的重试次数。仅覆盖连接错误 / 429 / 5xx。注意口径：
	// 重试 3 次 = 全程最多 4 次尝试；按 ResponseHeaderTimeout 120s 逐次烧满的
	// 话，最坏 4×120s + 退避 ≈ 8min 才见分晓——若上游带 Retry-After（429/5xx
	// 优先采用、封顶 30s），最坏 4×120s + 3×30s ≈ 9.5min。服务层的 stale 扫描器
	// （5min）靠 api_retry 重试帧刷新活性才扛得住这段静默，见 Stream 里
	// notifyRetry。
	maxRetries = 3

	// baseRetryDelay 是指数退避的起点。
	baseRetryDelay = 500 * time.Millisecond

	// maxRetryDelay 是退避上限（同时兜住 Retry-After 的离谱值）。
	maxRetryDelay = 30 * time.Second

	// defaultMaxTokens 是请求体没指定时的输出上限。
	//
	// 宿主从不设置 MaxTokens，所以这个默认值就是线上真实生效的值。曾经是
	// 8192：一次写大文件（几百行）的 tool_use 会被 in-band 截断成
	// stop_reason=max_tokens，而内核见到被截断的 tool_use 会把**整批**工具
	// 调用丢掉（宁可重试也不执行半个参数），用户看到的是「AI 说写好了」但
	// 文件没变、或者干脆一轮白跑。32000 覆盖主流模型的输出上限，仍低于最
	// 保守的 4k/8k 档，后者由 maxTokensFallback 兜底降级。
	defaultMaxTokens = 32000

	// minMaxTokens 是降级重试的下限：老模型/小模型常见的输出上限。
	minMaxTokens = 8192

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
	// streamIdleEnvKey 环境变量解析结果每次播种（见 currentStreamIdleWindow）。
	streamIdle time.Duration
}

// NewClient 构造客户端。baseURL 形如 http://127.0.0.1:56432（不带 /v1）。
func NewClient(baseURL string) *Client {
	return &Client{
		baseURL:    strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		streamIdle: currentStreamIdleWindow(),
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
	// Kind: "start" | "text" | "thinking" | "image" | "tool_use_start"
	//       | "tool_input" | "block_stop" | "stop" | "error"
	Kind string

	// start
	MessageID string
	Model     string

	// text / thinking
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
	EventThinking     = "thinking"
	EventImage        = "image"
	EventToolUseStart = "tool_use_start"
	EventToolInput    = "tool_input"
	EventBlockStop    = "block_stop"
	EventStop         = "stop"
	EventError        = "error"
)

// RetryNotice 描述一次「确定要重试」的上游瞬态失败。Stream 在退避等待**前**
// 通过 WithRetryNotice 注册的观察者发出它；最后一次失败直接返回错误，不发。
type RetryNotice struct {
	// Attempt 是即将进行的第几次重试，从 1 起（首尝试失败后的第一次重试 = 1）。
	Attempt int
	// Delay 是本次重试前的退避时长（尊重 Retry-After 后的最终值）。
	Delay time.Duration
	// ErrorStatus 是触发重试的上游 HTTP 状态码；连接类失败为 0。
	ErrorStatus int
	// ErrorKind 是原因类别，取值对齐真 claude-code 的 api_retry 帧里 error
	// 字段：server_error / rate_limit_error / invalid_request_error /
	// overloaded_error / api_error / connection_error。
	ErrorKind string
}

// streamOptions 收拢 Stream 的可选参数。零值即默认行为。
type streamOptions struct {
	onRetry func(RetryNotice)
}

// StreamOption 是 Stream 的可选参数。变参是为了既有调用点（含全部单测的
// 三参形态）零改动——重试可观测是内核对宿主的增量能力，不是新契约。
type StreamOption func(*streamOptions)

// WithRetryNotice 注册重试观察者。内核在重试循环里「确定要重试」时（退避
// 等待前）回调一次；没有可重试的失败（如 400 非降档）不会回调。
func WithRetryNotice(fn func(RetryNotice)) StreamOption {
	return func(o *streamOptions) { o.onRetry = fn }
}

// Stream 发起一次流式调用，把事件逐个回调给 onEvent。
//
// onEvent 返回错误即中止本次调用（用于把工具执行/取消信号传回传输层）。
// 返回的 error 是传输层或上游错误；onEvent 自身的错误会原样返回。
func (c *Client) Stream(ctx context.Context, req *MessagesRequest, onEvent func(StreamEvent) error, opts ...StreamOption) error {
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

	var so streamOptions
	for _, opt := range opts {
		if opt != nil {
			opt(&so)
		}
	}
	// notifyRetry 把「确定要重试」告知观察者。nextAttempt 是即将进行的重试
	// 序号；loop 顶的退避等待发生在下一轮迭代开始时，所以这里先发帧再睡——
	// 宿主拿到的是「要等多久、为什么等」，而不是等完之后的马后炮。
	// nextAttempt > maxRetries 意味着循环条件即将自然终止（最后一次失败），
	// 并没有真的重试，不发帧。
	notifyRetry := func(nextAttempt int, err error) {
		if so.onRetry == nil || nextAttempt > maxRetries {
			return
		}
		status, kind := retryNoticeFields(err)
		so.onRetry(RetryNotice{
			Attempt:     nextAttempt,
			Delay:       retryDelay(nextAttempt, err),
			ErrorStatus: status,
			ErrorKind:   kind,
		})
	}

	// delivered 记录本调用是否已向调用方下发过任何事件。它是**跨尝试**的，
	// 不随单次尝试重置：调用方（streamTurn）的内容累积与宿主已收到的帧都挂
	// 在 onEvent 上，重试边界之外的状态不会归零。
	delivered := false
	emit := func(ev StreamEvent) error {
		delivered = true
		return onEvent(ev)
	}

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
		err := c.streamOnce(ctx, req, emit)
		if err == nil {
			return nil
		}
		// onEvent 的错误不是传输错误，不重试。必须先于 delivered 判定：它的
		// 返回值是包内层 cause，走 delivered 分支会把 eventAbortError 壳一起
		// 带出去，调用方的 errors.Is 语义就断了。
		var abort *eventAbortError
		if errors.As(err, &abort) {
			return abort.cause
		}
		// 已经向调用方下发过任何事件之后，一切错误原样返回、绝不重试。
		//
		// streamIdleError 注释里那条理由对**一切**已下发事件后的错误都成立：
		// 重试会从零重发 start/text 等事件帧，而调用方第一次尝试的半截内容
		// 还在累积——第二次尝试的 delta 会拼接在半截文本后面，缝出一段从未
		// 被模型完整说过的「缝合怪」，发往手机、进历史、毒化后续上下文。
		// 连接中途重置/EOF/超时这些「瞬态」错误因此在此时不再瞬态。首字节
		// 之前的失败不受影响，仍按下面的 isRetryable 正常重试。
		//
		// 刻意放在 reduceMaxTokens 之前：400 降档发生在响应检查阶段，此刻
		// 必然还没有下发过任何事件，不受影响；而 2xx 体内的错误信封
		// （upstreamError）可能出现在事件中途——那种情况下重试同样会缝内容，
		// delivered 判定先拦住是对的。
		if delivered {
			return err
		}
		// 各模型的输出上限不同：defaultMaxTokens（32000）遇到只支持 8k/16k
		// 的模型会被直接 400。与其整轮失败，不如降到保守值重试——线上用户
		// 要的是回复，不关心单次输出上限的具体数字。
		if reduced, ok := reduceMaxTokens(err, req.MaxTokens); ok {
			req.MaxTokens = reduced
			notifyRetry(attempt+1, err)
			lastErr = err
			continue
		}
		if !isRetryable(err) {
			return err
		}
		notifyRetry(attempt+1, err)
		lastErr = err
	}
	return fmt.Errorf("agentcore: 重试 %d 次后仍失败: %w", maxRetries, lastErr)
}

// retryNoticeFields 把一次失败归类成 api_retry 帧里的 error_status / error
// 类别。类别名对齐宿主在 CLI 路径见到的真 claude-code 取值（app/http/services/
// agent_ai_retry_test.go 里的逐字样例：502 → "server_error"）。
func retryNoticeFields(err error) (status int, kind string) {
	var statusErr *httpStatusError
	if errors.As(err, &statusErr) {
		switch {
		case statusErr.status == http.StatusTooManyRequests:
			return statusErr.status, "rate_limit_error"
		case statusErr.status >= 500:
			return statusErr.status, "server_error"
		default:
			return statusErr.status, "invalid_request_error"
		}
	}
	var upErr *upstreamError
	if errors.As(err, &upErr) && strings.TrimSpace(upErr.kind) != "" {
		return 0, strings.TrimSpace(upErr.kind)
	}
	// 连接被拒/重置、EOF、首字节前断开等都归到 connection_error。
	return 0, "connection_error"
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
//
// 注意它只看错误本身的性质；「本调用已经下发过事件」这层守卫在 Stream 的
// 重试循环里（delivered），两者是互补关系——这里放行、那里拦截的情形由
// delivered 兜底。
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

// reduceMaxTokens 判断这次失败是否为「max_tokens 超出模型输出上限」，是则
// 返回下一个可试的保守值。逐次减半、不低于 minMaxTokens，降到下限后不再
// 提供新的值（ok=false，让上层的失败路径收尾，避免死循环重试同一个 400）。
func reduceMaxTokens(err error, current int) (int, bool) {
	if current <= minMaxTokens {
		return 0, false
	}
	// 限流 / 过载也会提到 token，但那是容量问题，降上限没用。
	if !mentionsMaxTokens(err) {
		return 0, false
	}
	next := current / 2
	if next < minMaxTokens {
		next = minMaxTokens
	}
	return next, true
}

// mentionsMaxTokens 只看 4xx（以及 2xx 内信封）里明确指向输出上限的错误文本。
func mentionsMaxTokens(err error) bool {
	var statusErr *httpStatusError
	if errors.As(err, &statusErr) {
		if statusErr.status != http.StatusBadRequest &&
			statusErr.status != http.StatusUnprocessableEntity {
			return false
		}
		return maxTokensPhrase(statusErr.body)
	}
	var upErr *upstreamError
	if errors.As(err, &upErr) {
		switch strings.TrimSpace(upErr.kind) {
		case "rate_limit_error", "overloaded_error":
			return false
		}
		return maxTokensPhrase(upErr.message + " " + upErr.body)
	}
	return false
}

func maxTokensPhrase(s string) bool {
	m := strings.ToLower(s)
	for _, needle := range []string{
		"max_tokens",
		"max tokens",
		"max output",
		"max_output",
		"maximum output",
		"output token",
		"output len",
		"最大输出",
		"输出上限",
		"输出长度",
	} {
		if strings.Contains(m, needle) {
			return true
		}
	}
	return false
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

// streamUnterminatedError 表示上游把流「优雅地」断在了终点帧之前：连接正常
// 关闭（EOF），但全程既没见到 message_stop，也没见到携带非空 stop_reason 的
// message_delta。内容只收到一半；与 streamIdleError 的区别在于停滞是窗口内
// 零字节，这里是流自己「说完就挂断」——逐字节都到了，唯独缺终点帧。不把它
// 与正常收尾区分开，半截回答就会带着 IsError=false 静默落库。
type streamUnterminatedError struct{}

func (e *streamUnterminatedError) Error() string {
	return "agentcore: 上游流未正常终止（连接在 message_stop/stop_reason 之前关闭，回答可能被截断）"
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

	// terminated 记录流是否给出过「终点信号」：message_stop 帧、携带非空
	// stop_reason 的 message_delta，或 error 事件。三者任一出现，流的结局就
	// 已经定论；干净 EOF 时若三者都没见过，说明上游把流断在了终点之前。
	terminated := false

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
				terminated = true
				return nil
			}
			// message_delta 携带非空 stop_reason 与 message_stop 同属终点信号，
			// 且以此处为终止判据收口、不等 message_stop。协议顺序上 message_delta
			// 在所有内容块之后（tool_use 的 input_json_delta、thinking 的
			// signature_delta 都已在各自的 content_block_stop 前发完），此刻内容
			// 已经 100% 收全。半开的另一种形态正是：上游发完终点信号后连接停滞、
			// 既不发 message_stop 也不断开——继续读只会被块间空闲闸在 90s 后判成
			// 停滞，把用户已经看到完整回答的回合误杀成红色错误。
			if ev.Type == "message_delta" && ev.Delta != nil && strings.TrimSpace(ev.Delta.StopReason) != "" {
				terminated = true
				return nil
			}
			// error 事件是显式失败的终局：结局已经告知调用方，EOF 不该再往
			// 上面叠一层「未终止」。
			if ev.Type == "error" {
				terminated = true
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
	// 干净 EOF（连接优雅关闭）但全程没有任何终点信号：上游把回答断在了
	// 半截。不能与「见到 message_stop 的正常收尾」混为一谈——否则半截文本
	// 会以成功收尾静默落库。作为普通错误返回后，重试语义交给 Stream：
	// 零下发场景照常瞬态重试，已下发场景由 delivered 守卫拦住不重试。
	if !terminated {
		return &streamUnterminatedError{}
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
			// 文本/思考块起始本身不带内容，等 delta。
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
		case "thinking_delta":
			if ev.Delta.Thinking == "" {
				return nil, nil
			}
			return []StreamEvent{{Kind: EventThinking, Index: ev.Index, Text: ev.Delta.Thinking}}, nil
		case "input_json_delta":
			return []StreamEvent{{
				Kind:        EventToolInput,
				Index:       ev.Index,
				PartialJSON: ev.Delta.PartialJSON,
			}}, nil
		case "signature_delta":
			// 思考块的签名字段：不是用户可见内容，内核也不做上游签名校验
			// （思考块从不回传给上游——回传缺签名的思考块才会被拒），所以
			// 没有需要保存的字节。丢掉。
			return nil, nil
		}
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
