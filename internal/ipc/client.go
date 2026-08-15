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
}

// NewClient creates a new IPC client.
func NewClient() *Client {
	return &Client{
		transport: NewTransport(),
		httpURL:   defaultHTTPBaseURL,
	}
}

// NewClientWithHTTP creates a new IPC client with custom HTTP URL.
func NewClientWithHTTP(httpURL string) *Client {
	return &Client{
		transport: NewTransport(),
		httpURL:   httpURL,
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
	// Try IPC first
	if c.IsConnected() {
		return c.sendIPC(action, args)
	}

	// Fallback to HTTP for development mode
	return c.sendHTTP(action, args)
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

	// Send request
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		c.Close()
		return nil, fmt.Errorf("[IPC] send error: %w", err)
	}

	// Read response
	var resp Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		c.Close()
		return nil, fmt.Errorf("[IPC] recv error: %w", err)
	}

	return &resp, nil
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
