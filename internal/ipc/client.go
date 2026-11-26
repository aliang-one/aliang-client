package ipc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"aliang.one/nursorgate/common/logger"
	"aliang.one/nursorgate/processor/config"
)

const (
	defaultHTTPBaseURL = "http://" + config.DefaultManagementAddr
	requestTimeout     = 5 * time.Second
)

// Client represents the IPC client used by the Shell.
type Client struct {
	transport Transport
	conn      net.Conn
	httpURL   string
	mu        sync.Mutex
	connected bool

	// sendMu 是请求级互斥:同一连接上一次只允许一个「写请求→读响应」
	// 往返。2s 轮询 goroutine 与点击处理 goroutine 并发共用同一 Client,
	// 无互斥时两个请求同时在途,先到读的 goroutine 会拿走最早入队的
	// 响应(串位)。Send 全程持锁,重拨判断与 HTTP 回退也一并串行。
	sendMu sync.Mutex

	// readTimeout 是单次读/写帧的 deadline 时长。默认 requestTimeout;
	// 测试可注入更小值(同包测试直接改字段)验证超时语义,全局常量不动。
	readTimeout time.Duration
}

// NewClient creates a new IPC client.
func NewClient() *Client {
	return &Client{
		transport:   NewTransport(),
		httpURL:     defaultHTTPBaseURL,
		readTimeout: requestTimeout,
	}
}

// NewClientWithHTTP creates a new IPC client with custom HTTP URL.
func NewClientWithHTTP(httpURL string) *Client {
	return &Client{
		transport:   NewTransport(),
		httpURL:     httpURL,
		readTimeout: requestTimeout,
	}
}

// NewClientWithTransport creates an IPC client over the given transport
// (测试注入用)。
func NewClientWithTransport(transport Transport, httpURL string) *Client {
	return &Client{
		transport:   transport,
		httpURL:     httpURL,
		readTimeout: requestTimeout,
	}
}

// Connect establishes connection to the Core daemon via IPC.
func (c *Client) Connect() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	conn, err := c.transport.Dial()
	if err != nil {
		return fmt.Errorf("[IPC] failed to connect: %w", err)
	}

	c.conn = conn
	c.connected = true
	logger.Info(fmt.Sprintf("[IPC] Connected to Core at %s", c.transport.SocketPath()))
	return nil
}

// IsConnected returns whether the client is connected.
func (c *Client) IsConnected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.connected && c.conn != nil
}

// Close closes the IPC connection.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn != nil {
		err := c.conn.Close()
		c.conn = nil
		c.connected = false
		return err
	}
	return nil
}

// Send sends an IPC request and returns the response.
// If IPC fails, it falls back to HTTP for development mode.
func (c *Client) Send(action string, args interface{}) (*Response, error) {
	// 请求级互斥覆盖 Send 全程:重拨判断、IPC 往返、HTTP 回退都串行。
	// 没有它,两个并发 Send 的写/读会在同一连接上交错,响应串位。
	c.sendMu.Lock()
	defer c.sendMu.Unlock()

	// Try IPC first. 断线状态先拨一次:core 换代(服务重启/崩溃恢复)后旧
	// 管道已死,不重拨则 start_http/sync_agent_auth/stop_http 这些
	// IPC-only 动作在 HTTP 回退里硬报错、永远失效(companion 的
	// syncStateLoop 每 2s 一发,恰好提供自然重试节拍)。单次快速拨号、
	// 绝不循环重拨,防 core 长期不可用时的拨号风暴。
	if !c.IsConnected() {
		if err := c.Connect(); err != nil {
			return c.sendHTTP(action, args)
		}
	}
	return c.sendIPC(action, args)
}

func (c *Client) sendIPC(action string, args interface{}) (*Response, error) {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()

	if conn == nil {
		return nil, fmt.Errorf("not connected")
	}

	// Prepare request
	reqID := generateRequestID()
	reqArgs, err := json.Marshal(args)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal args: %w", err)
	}

	req := &Request{
		ID:     reqID,
		Action: action,
		Args:   reqArgs,
	}

	// 每次写/读前拨 deadline:服务端半死(收包不回包)时,Send 在
	// readTimeout 内带错返回,而不是永久阻塞读。
	if err := conn.SetDeadline(time.Now().Add(c.readTimeout)); err != nil {
		c.Close()
		return nil, fmt.Errorf("[IPC] set deadline error: %w", err)
	}

	// Send request
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		c.Close()
		return nil, fmt.Errorf("[IPC] send error: %w", err)
	}

	// 读响应循环:按 ID 过滤,跳过不属于本请求的帧(上一轮漏读的迟到
	// 响应等杂帧),只认 ID 匹配的那帧,绝不把错帧当本次结果。每次读
	// 单独拨 deadline,错帧再多也有界。
	decoder := json.NewDecoder(conn)
	for {
		if err := conn.SetDeadline(time.Now().Add(c.readTimeout)); err != nil {
			c.Close()
			return nil, fmt.Errorf("[IPC] set deadline error: %w", err)
		}
		var resp Response
		if err := decoder.Decode(&resp); err != nil {
			c.Close()
			return nil, fmt.Errorf("[IPC] recv error: %w", err)
		}
		if resp.ID == reqID {
			return &resp, nil
		}
	}
}

func (c *Client) sendHTTP(action string, args interface{}) (*Response, error) {
	httpClient := &http.Client{Timeout: requestTimeout}

	// Build URL and method based on action
	var url string
	var method string

	switch action {
	case ActionPing:
		url = c.httpURL + "/api/run/status"
		method = http.MethodGet
	case ActionStartHTTP:
		// Core doesn't have HTTP start endpoint in current implementation
		// This is handled via IPC
		return nil, fmt.Errorf("start_http must use IPC")
	case ActionSyncAgentAuth:
		return nil, fmt.Errorf("sync_agent_auth must use IPC")
	case ActionStopHTTP:
		return nil, fmt.Errorf("stop_http must use IPC")
	case ActionGetStatus:
		url = c.httpURL + "/api/run/status"
		method = http.MethodGet
	case ActionStartProxy:
		url = c.httpURL + "/api/run/start"
		method = http.MethodPost
	case ActionStopProxy:
		url = c.httpURL + "/api/run/stop"
		method = http.MethodPost
	case ActionSwitchMode:
		url = c.httpURL + "/api/run/swift"
		method = http.MethodPost
	case ActionShutdown:
		url = c.httpURL + "/api/core/shutdown"
		method = http.MethodPost
	default:
		return nil, fmt.Errorf("unknown action: %s", action)
	}

	var body *bytes.Reader
	if args != nil {
		data, err := json.Marshal(args)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal args: %w", err)
		}
		body = bytes.NewReader(data)
	} else {
		body = bytes.NewReader(nil)
	}

	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	if method != http.MethodGet {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("[HTTP] request failed: %w", err)
	}
	defer resp.Body.Close()

	var payload struct {
		Code    int                    `json:"code"`
		Msg     string                 `json:"msg"`
		Message string                 `json:"message"`
		Data    map[string]interface{} `json:"data"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("[HTTP] decode failed: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 || payload.Code != 0 {
		msg := payload.Msg
		if msg == "" {
			msg = payload.Message
		}
		if msg == "" {
			msg = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		return &Response{
			OK:    false,
			Error: msg,
		}, nil
	}

	return &Response{
		OK:   true,
		Data: payload.Data,
	}, nil
}

var requestIDCounter int64
var requestIDMu sync.Mutex

func generateRequestID() string {
	requestIDMu.Lock()
	defer requestIDMu.Unlock()
	requestIDCounter++
	return fmt.Sprintf("%d-%d", time.Now().UnixNano(), requestIDCounter)
}
