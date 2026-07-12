package ipc

import "encoding/json"

// Request represents an incoming IPC request.
type Request struct {
	ID     string          `json:"id"`
	Action string          `json:"action"`
	Args   json.RawMessage `json:"args,omitempty"`
}

// Response represents an outgoing IPC response.
type Response struct {
	ID    string      `json:"id"`
	OK    bool        `json:"ok"`
	Data  interface{} `json:"data,omitempty"`
	Error string      `json:"error,omitempty"`
}

// Event represents an asynchronous event sent from server to client.
type Event struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data,omitempty"`
}

// Action handlers
const (
	ActionPing          = "ping"
	ActionStartHTTP     = "start_http"
	ActionSyncAgentAuth = "sync_agent_auth"
	ActionStopHTTP      = "stop_http"
	ActionGetStatus     = "get_status"
	ActionStartProxy    = "start_proxy"
	ActionStopProxy     = "stop_proxy"
	ActionSwitchMode    = "switch_mode"
	ActionShutdown      = "shutdown"
)

// StatusResponse represents the status returned by get_status.
type StatusResponse struct {
	HTTPEnabled bool   `json:"http_enabled"`
	ProxyStatus string `json:"proxy_status"` // "running", "stopped", etc.
	CurrentMode string `json:"current_mode"` // "http", "tun", etc.
	Version     string `json:"version"`
}

// StartHTTPResponse represents the response from start_http.
type StartHTTPResponse struct {
	Port int    `json:"port"`
	URL  string `json:"url"`
}

// SwitchModeArgs represents arguments for switch_mode action.
type SwitchModeArgs struct {
	Mode string `json:"mode"` // "http" or "tun"
}

// ErrorResponse creates an error response.
func ErrorResponse(id string, err error) *Response {
	return &Response{
		ID:    id,
		OK:    false,
		Error: err.Error(),
	}
}

// OKResponse creates a success response.
func OKResponse(id string, data interface{}) *Response {
	return &Response{
		ID:   id,
		OK:   true,
		Data: data,
	}
}
