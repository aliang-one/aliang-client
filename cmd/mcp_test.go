package cmd

// aliang mcp stdio 面单测：JSON-RPC 分发、tools/list 形状、tools/call 到本地
// 56433 路由的调用形状与错误语义、协议版本协商、通知不应答。

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type mcpTestRecorder struct {
	t       *testing.T
	srv     *httptest.Server
	mu      sync.Mutex
	methods []string
	paths   []string
	bodies  []string
}

func newMCPTestServer(t *testing.T, handler http.HandlerFunc) *mcpTestRecorder {
	t.Helper()
	rec := &mcpTestRecorder{t: t}
	wrapped := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		rec.mu.Lock()
		rec.methods = append(rec.methods, r.Method)
		rec.paths = append(rec.paths, r.URL.Path)
		rec.bodies = append(rec.bodies, string(raw))
		rec.mu.Unlock()
		handler(w, r)
	})
	rec.srv = httptest.NewServer(wrapped)
	t.Cleanup(rec.srv.Close)
	return rec
}

func (rec *mcpTestRecorder) last() (method, path, body string) {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.paths) == 0 {
		rec.t.Fatal("no request reached the mock local agent")
	}
	return rec.methods[len(rec.methods)-1], rec.paths[len(rec.paths)-1], rec.bodies[len(rec.bodies)-1]
}

// feed 把一行 JSON-RPC 喂给服务端并返回应答行（要求必须有一条应答）。
func feed(t *testing.T, srv *peerMCPServer, line string) map[string]interface{} {
	t.Helper()
	var out bytes.Buffer
	srv.out = &out
	srv.handleLine([]byte(line))
	if out.Len() == 0 {
		t.Fatalf("no response for %s", line)
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatalf("response is not JSON: %v (%s)", err, out.String())
	}
	return resp
}

func newMCPServerForTest(t *testing.T, rec *mcpTestRecorder) *peerMCPServer {
	t.Helper()
	return &peerMCPServer{baseURL: rec.srv.URL, client: &http.Client{}}
}

func TestPeerMCPToolsList_HasFivePeerTools(t *testing.T) {
	rec := newMCPTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("tools/list must not touch the local agent")
	})
	srv := newMCPServerForTest(t, rec)

	resp := feed(t, srv, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	if resp["error"] != nil {
		t.Fatalf("tools/list errored: %v", resp["error"])
	}
	result, _ := resp["result"].(map[string]interface{})
	tools, _ := result["tools"].([]interface{})
	if len(tools) != 5 {
		t.Fatalf("tools = %d, want 5", len(tools))
	}
	want := map[string]bool{"peer_exec": false, "peer_fetch_file": false, "device_list": false, "device_info": false, "device_note": false}
	for _, item := range tools {
		m, _ := item.(map[string]interface{})
		name, _ := m["name"].(string)
		if _, ok := want[name]; !ok {
			t.Fatalf("unexpected tool %q", name)
		}
		want[name] = true
		if m["inputSchema"] == nil || m["description"] == "" {
			t.Fatalf("tool %q missing schema/description", name)
		}
	}
	for name, seen := range want {
		if !seen {
			t.Fatalf("missing tool %q", name)
		}
	}
}

func TestPeerMCPCallExec_PostsToLocalRoute(t *testing.T) {
	rec := newMCPTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"msg":"success","data":{"result":"exit_code=0 | pong"}}`))
	})
	srv := newMCPServerForTest(t, rec)

	resp := feed(t, srv, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"peer_exec","arguments":{"device_id":"device-c","command":"echo pong","timeout_ms":5000}}}`)
	if resp["error"] != nil {
		t.Fatalf("tools/call errored: %v", resp["error"])
	}
	result, _ := resp["result"].(map[string]interface{})
	if result["isError"] == true {
		t.Fatalf("unexpected isError: %v", result)
	}
	content, _ := result["content"].([]interface{})
	if len(content) != 1 {
		t.Fatalf("content blocks = %d, want 1", len(content))
	}
	block, _ := content[0].(map[string]interface{})
	if !strings.Contains(block["text"].(string), "exit_code=0 | pong") {
		t.Fatalf("text = %v", block["text"])
	}

	method, path, body := rec.last()
	if method != http.MethodPost || path != "/api/agent/peer/exec" {
		t.Fatalf("call = %s %s", method, path)
	}
	if !strings.Contains(body, `"device_id":"device-c"`) || !strings.Contains(body, `"timeout_ms":5000`) {
		t.Fatalf("body = %s", body)
	}
}

func TestPeerMCPDeviceList_GET_NoBody(t *testing.T) {
	rec := newMCPTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":0,"msg":"success","data":{"result":"1 device(s) in this account:"}}`))
	})
	srv := newMCPServerForTest(t, rec)

	resp := feed(t, srv, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"device_list","arguments":{}}}`)
	result, _ := resp["result"].(map[string]interface{})
	if result["isError"] == true {
		t.Fatalf("device_list errored: %v", result)
	}
	method, path, body := rec.last()
	if method != http.MethodGet || path != "/api/agent/peer/devices" {
		t.Fatalf("call = %s %s", method, path)
	}
	if body != "" {
		t.Fatalf("GET must carry no body, got %s", body)
	}
}

func TestPeerMCPDeviceInfo_QueryParam(t *testing.T) {
	rec := newMCPTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("device_id") != "device-x" {
			t.Errorf("device_id query = %q", r.URL.Query().Get("device_id"))
		}
		_, _ = w.Write([]byte(`{"code":0,"msg":"success","data":{"result":"device device-x"}}`))
	})
	srv := newMCPServerForTest(t, rec)

	resp := feed(t, srv, `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"device_info","arguments":{"device_id":"device-x"}}}`)
	result, _ := resp["result"].(map[string]interface{})
	if result["isError"] == true {
		t.Fatalf("device_info errored: %v", result)
	}
	if method, path, _ := rec.last(); method != http.MethodGet || path != "/api/agent/peer/device-info" {
		t.Fatalf("call = %s %s", method, path)
	}
}

func TestPeerMCPCall_LocalAgentRefusal_IsToolError(t *testing.T) {
	// 云端拒绝经本地面回 502+Msg（服务端已脱敏）→ MCP isError 文本。
	rec := newMCPTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"code":502,"msg":"peer api status 409: not_allowed","data":null}`))
	})
	srv := newMCPServerForTest(t, rec)

	resp := feed(t, srv, `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"peer_exec","arguments":{"device_id":"device-c","command":"false"}}}`)
	result, _ := resp["result"].(map[string]interface{})
	if result["isError"] != true {
		t.Fatalf("refusal must be isError: %v", result)
	}
	content, _ := result["content"].([]interface{})
	block, _ := content[0].(map[string]interface{})
	if !strings.Contains(block["text"].(string), "not_allowed") {
		t.Fatalf("text = %v", block["text"])
	}
}

func TestPeerMCPUnknownTool_IsErrorWithoutHTTP(t *testing.T) {
	rec := newMCPTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("unknown tool must not reach the local agent")
	})
	srv := newMCPServerForTest(t, rec)

	resp := feed(t, srv, `{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"evil_tool","arguments":{}}}`)
	result, _ := resp["result"].(map[string]interface{})
	if result["isError"] != true {
		t.Fatalf("unknown tool must be isError: %v", result)
	}
}

func TestPeerMCPInitialize_EchoesProtocolVersion(t *testing.T) {
	rec := newMCPTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("initialize must not touch the local agent")
	})
	srv := newMCPServerForTest(t, rec)

	resp := feed(t, srv, `{"jsonrpc":"2.0","id":7,"method":"initialize","params":{"protocolVersion":"2024-11-05"}}`)
	result, _ := resp["result"].(map[string]interface{})
	if result["protocolVersion"] != "2024-11-05" {
		t.Fatalf("protocolVersion = %v", result["protocolVersion"])
	}
	info, _ := result["serverInfo"].(map[string]interface{})
	if info["name"] != "aliang-peer" {
		t.Fatalf("serverInfo = %v", info)
	}
}

func TestPeerMCPNotification_NoReply(t *testing.T) {
	rec := newMCPTestServer(t, func(w http.ResponseWriter, r *http.Request) { t.Fatal("must not dial") })
	srv := newMCPServerForTest(t, rec)
	var out bytes.Buffer
	srv.out = &out
	srv.handleLine([]byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`))
	if out.Len() != 0 {
		t.Fatalf("notification must not be answered: %s", out.String())
	}
}

func TestPeerMCPUnknownMethod_RPCError(t *testing.T) {
	rec := newMCPTestServer(t, func(w http.ResponseWriter, r *http.Request) { t.Fatal("must not dial") })
	srv := newMCPServerForTest(t, rec)

	resp := feed(t, srv, `{"jsonrpc":"2.0","id":8,"method":"resources/list"}`)
	errObj, _ := resp["error"].(map[string]interface{})
	if errObj == nil || errObj["code"].(float64) != -32601 {
		t.Fatalf("error = %v", resp["error"])
	}
}

func TestPeerMCPBaseURL_EnvOverrides(t *testing.T) {
	t.Setenv("ALIANG_USER_AGENT_ADDR", "")
	if got := peerMCPBaseURL(); got != "http://127.0.0.1:56433" {
		t.Fatalf("default = %s", got)
	}
	t.Setenv("ALIANG_USER_AGENT_ADDR", "127.0.0.1:9999")
	if got := peerMCPBaseURL(); got != "http://127.0.0.1:9999" {
		t.Fatalf("host:port = %s", got)
	}
	t.Setenv("ALIANG_USER_AGENT_ADDR", "http://127.0.0.1:7777/")
	if got := peerMCPBaseURL(); got != "http://127.0.0.1:7777" {
		t.Fatalf("url = %s", got)
	}
}
