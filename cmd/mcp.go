package cmd

// aliang mcp —— 设备互访（peer-link）的 MCP stdio 面。
//
// Claude Code / Codex 等外部 CLI 无法被内核注入原生工具，经 MCP 接入同一套
// peer 工具（peer.exec / peer.fetch_file / peer.device_list / peer.device_info /
// peer.device_note）。认证三层（零新增凭据）：
//  1. stdio 子进程由 CLI 自己拉起——同用户同进程身份即信任，无凭据；
//  2. 本命令只打 user agent 的 56433 loopback 路由（loopbackOnly+零 token 是
//     agentruntime 刻意红线，只有本机同用户够得着）；
//  3. 出网凭据（用户 JWT + X-Aliang-Device-ID）由常驻 agent 进程持有，CLI/
//     LLM 全程接触不到；目标机门禁（peer_exec_enabled 默认关 + 云端 peer
//     ACL + 全量审计）对 MCP 路径同样生效。
//
// 协议：JSON-RPC 2.0 over stdio（MCP 2024-11-05）。只实现 initialize /
// notifications·initialized / tools/list / tools/call / ping；一切日志走
// stderr，绝不污染 stdout。
//
// Claude Code 接入：claude mcp add aliang-peer -- <aliang绝对路径> mcp
// Codex 接入：config.toml [mcp_servers.aliang-peer]
//
//	command = "<aliang绝对路径>"
//	args    = ["mcp"]

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"aliang.one/nursorgate/common/version"
	"aliang.one/nursorgate/processor/config"
	"github.com/spf13/cobra"
)

// peerMCPProtocolVersion 是本服务端声明并支持的 MCP 协议版本。客户端请求
// 其他版本时回退到本值（客户端可自行协商降级）。
const peerMCPProtocolVersion = "2024-11-05"

var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "Run the Aliang peer-tools MCP server over stdio (for Claude Code / Codex)",
	Long: "Expose Aliang peer tools (cross-device exec / file fetch / device directory) " +
		"as an MCP stdio server backed by the local user agent on " + config.DefaultUserAgentAddr + ".",
	RunE: func(*cobra.Command, []string) error {
		return runPeerMCPStdio(os.Stdin, os.Stdout)
	},
}

func init() {
	rootCmd.AddCommand(mcpCmd)
}

// ---------------------------------------------------------------------------
// JSON-RPC / MCP 线格式
// ---------------------------------------------------------------------------

type mcpRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  struct {
		// initialize
		ProtocolVersion string `json:"protocolVersion"`
		// tools/call
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	} `json:"params"`
}

type mcpContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type mcpToolResult struct {
	Content []mcpContentBlock `json:"content"`
	IsError bool              `json:"isError,omitempty"`
}

// ---------------------------------------------------------------------------
// 服务端主体
// ---------------------------------------------------------------------------

// peerMCPServer 是可注入的 stdio MCP 服务端：baseURL 指向本机 user agent
// （生产 http://127.0.0.1:56433，测试换 httptest）。client 刻意无全局
// Timeout——超时由每个工具调用的 ctx 期限驱动（与 LLM 工具层同款分层）。
type peerMCPServer struct {
	baseURL string
	client  *http.Client
	out     io.Writer
}

func runPeerMCPStdio(in io.Reader, out io.Writer) error {
	srv := &peerMCPServer{
		baseURL: peerMCPBaseURL(),
		client:  &http.Client{},
		out:     out,
	}
	scanner := bufio.NewScanner(in)
	// 请求体上限 1MiB（工具入参都是 KB 级；给足裕量防 scanner 短缓冲截断）。
	scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		srv.handleLine([]byte(line))
	}
	return scanner.Err()
}

// peerMCPBaseURL 解析 user agent 地址：ALIANG_USER_AGENT_ADDR 环境变量优先
// （接受 host:port 或完整 http(s) URL），缺省回 config 默认 127.0.0.1:56433。
func peerMCPBaseURL() string {
	raw := strings.TrimSpace(os.Getenv("ALIANG_USER_AGENT_ADDR"))
	if raw == "" {
		return "http://" + config.DefaultUserAgentAddr
	}
	if strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") {
		return strings.TrimRight(raw, "/")
	}
	return "http://" + strings.TrimRight(raw, "/")
}

// handleLine 解析并应答一行 JSON-RPC。通知（无 id）不应答；解析失败回 -32727
// parse error（保守起见 id 置 null）。
func (s *peerMCPServer) handleLine(line []byte) {
	var req mcpRequest
	if err := json.Unmarshal(line, &req); err != nil {
		s.writeError(nil, -32700, "parse error")
		return
	}
	hasID := len(req.ID) > 0 && string(req.ID) != "null"
	if !hasID {
		// 通知（notifications/initialized 等）：无需应答。
		return
	}
	switch req.Method {
	case "initialize":
		// 协议版本协商：客户端请求的版本原样回显（本服务端实现与
		// 2024-11-05 起的 tools 面兼容）；缺失/未知时回本服务端版本。
		proto := strings.TrimSpace(req.Params.ProtocolVersion)
		if proto == "" {
			proto = peerMCPProtocolVersion
		}
		s.writeResult(req.ID, map[string]interface{}{
			"protocolVersion": proto,
			"capabilities":    map[string]interface{}{"tools": map[string]interface{}{"listChanged": false}},
			"serverInfo":      map[string]interface{}{"name": "aliang-peer", "version": version.Version},
		})
	case "tools/list":
		s.writeResult(req.ID, map[string]interface{}{"tools": peerMCPToolDescriptors()})
	case "tools/call":
		out, isErr := s.invokeTool(req.Params.Name, req.Params.Arguments)
		s.writeResult(req.ID, mcpToolResult{
			Content: []mcpContentBlock{{Type: "text", Text: out}},
			IsError: isErr,
		})
	case "ping":
		s.writeResult(req.ID, map[string]interface{}{})
	default:
		s.writeError(req.ID, -32601, fmt.Sprintf("method not found: %s", req.Method))
	}
}

// ---------------------------------------------------------------------------
// 工具面（描述符与 LLM 注册表同口径，后端同走 56433 本地路由）
// ---------------------------------------------------------------------------

type peerMCPTool struct {
	name        string
	description string
	schema      map[string]interface{}
	// timeoutMs 是本次本地 HTTP 调用的 ctx 期限，与 LLM 工具层分层一致
	//（服务端 fence 先行裁决，这里只兜底）。
	timeoutMs int
	// method + path + 参数抽取。
	method   string
	path     string
	buildReq func(args map[string]interface{}) interface{}
}

func peerMCPToolDescriptors() []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(peerMCPTools))
	for _, t := range peerMCPTools {
		out = append(out, map[string]interface{}{
			"name":        t.name,
			"description": t.description,
			"inputSchema": t.schema,
		})
	}
	return out
}

func peerMCPArgString(args map[string]interface{}, key string) string {
	if args == nil {
		return ""
	}
	if v, ok := args[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

func peerMCPArgInt(args map[string]interface{}, key string) int {
	if args == nil {
		return 0
	}
	if v, ok := args[key].(float64); ok {
		return int(v)
	}
	return 0
}

// peerMCPTools 与 services 侧 LLM 工具面一一对应；后端全走 56433 本地路由，
// 复用同一套实现、脱敏与审计。
var peerMCPTools = []peerMCPTool{
	{
		name: "peer_exec",
		description: "Run a one-shot command on another device owned by the same account and " +
			"return its exit code plus stdout/stderr. The remote device must have peer exec " +
			"enabled. Serial per target device: a busy target returns fail_reason=busy immediately.",
		schema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"device_id":  map[string]interface{}{"type": "string", "description": "Target device id"},
				"command":    map[string]interface{}{"type": "string", "description": "Shell command to run on the target device"},
				"timeout_ms": map[string]interface{}{"type": "integer", "description": "Command timeout in ms (1000-600000, default 30000; out-of-range values are clamped)"},
				"workdir":    map[string]interface{}{"type": "string", "description": "Working directory on the target device (must exist, default home)"},
			},
			"required": []string{"device_id", "command"},
		},
		timeoutMs: 630_000,
		method:    http.MethodPost,
		path:      "/api/agent/peer/exec",
		buildReq: func(args map[string]interface{}) interface{} {
			req := map[string]interface{}{
				"device_id": peerMCPArgString(args, "device_id"),
				"command":   peerMCPArgString(args, "command"),
			}
			if v := peerMCPArgInt(args, "timeout_ms"); v != 0 {
				req["timeout_ms"] = v
			}
			if v := peerMCPArgString(args, "workdir"); v != "" {
				req["workdir"] = v
			}
			return req
		},
	},
	{
		name: "peer_fetch_file",
		description: "Queue an asynchronous file fetch from another device into this machine's " +
			"transfer inbox. Returns a transfer_id; completion is delivered via transfer progress.",
		schema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"device_id":   map[string]interface{}{"type": "string", "description": "Source device id"},
				"remote_path": map[string]interface{}{"type": "string", "description": "File path on the source device"},
			},
			"required": []string{"device_id", "remote_path"},
		},
		timeoutMs: 60_000,
		method:    http.MethodPost,
		path:      "/api/agent/peer/fetch-file",
		buildReq: func(args map[string]interface{}) interface{} {
			return map[string]interface{}{
				"device_id":   peerMCPArgString(args, "device_id"),
				"remote_path": peerMCPArgString(args, "remote_path"),
			}
		},
	},
	{
		name: "device_list",
		description: "List every device in this account with id, name, platform, online status " +
			"and whether peer exec is enabled. This is the directory to pick device_id from for " +
			"the other peer tools.",
		schema:    map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
		timeoutMs: 14_000,
		method:    http.MethodGet,
		path:      "/api/agent/peer/devices",
		buildReq:  func(map[string]interface{}) interface{} { return nil },
	},
	{
		name: "device_info",
		description: "Device profile snapshot: hostname, platform, agent version, load summary, " +
			"project count with recent project paths, authorized directory count. Omit device_id " +
			"for this machine; pass one for a remote device in the same account.",
		schema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"device_id": map[string]interface{}{"type": "string", "description": "Target device id; omit for this machine"},
			},
		},
		timeoutMs: 14_000,
		method:    http.MethodGet,
		path:      "/api/agent/peer/device-info",
		buildReq:  func(args map[string]interface{}) interface{} { return nil },
	},
	{
		name: "device_note",
		description: "Save a named note (key/value metadata) on a device. Omit device_id to note " +
			"this machine. Notes survive reconnects and are visible to every device in the account. " +
			"Key max 64 bytes, value max 4096 bytes.",
		schema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"key":       map[string]interface{}{"type": "string", "description": "Note key (max 64 bytes)"},
				"value":     map[string]interface{}{"type": "string", "description": "Note value (max 4096 bytes)"},
				"device_id": map[string]interface{}{"type": "string", "description": "Target device id; omit for this machine"},
			},
			"required": []string{"key", "value"},
		},
		timeoutMs: 30_000,
		method:    http.MethodPost,
		path:      "/api/agent/peer/note",
		buildReq: func(args map[string]interface{}) interface{} {
			return map[string]interface{}{
				"key":       peerMCPArgString(args, "key"),
				"value":     peerMCPArgString(args, "value"),
				"device_id": peerMCPArgString(args, "device_id"),
			}
		},
	},
}

// invokeTool 执行一个 tools/call：本地路由 → {code:0,data:{result}} 解包；
// 非 2xx / code!=0 → isError 文本（服务端已脱敏）。未知工具名返回 isError。
func (s *peerMCPServer) invokeTool(name string, rawArgs json.RawMessage) (string, bool) {
	var tool *peerMCPTool
	for i := range peerMCPTools {
		if peerMCPTools[i].name == name {
			tool = &peerMCPTools[i]
			break
		}
	}
	if tool == nil {
		return fmt.Sprintf("unknown tool: %s", name), true
	}
	var args map[string]interface{}
	if len(rawArgs) > 0 {
		if err := json.Unmarshal(rawArgs, &args); err != nil {
			return fmt.Sprintf("invalid arguments: %v", err), true
		}
	}
	var body io.Reader
	payload := tool.buildReq(args)
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return fmt.Sprintf("encode request: %v", err), true
		}
		body = strings.NewReader(string(raw))
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(tool.timeoutMs)*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, tool.method, s.baseURL+tool.path, body)
	if err != nil {
		return fmt.Sprintf("build request: %v", err), true
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if tool.method == http.MethodGet && tool.path == "/api/agent/peer/device-info" {
		q := url.Values{}
		if v := peerMCPArgString(args, "device_id"); v != "" {
			q.Set("device_id", v)
		}
		if len(q) > 0 {
			req.URL.RawQuery = q.Encode()
		}
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Sprintf("local agent unreachable at %s: %v (is the Aliang agent running?)", s.baseURL, err), true
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Sprintf("read local agent response: %v", err), true
	}
	var envelope struct {
		Code int             `json:"code"`
		Msg  string          `json:"msg"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return fmt.Sprintf("local agent response is not JSON (status %d)", resp.StatusCode), true
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || envelope.Code != 0 {
		msg := strings.TrimSpace(envelope.Msg)
		if msg == "" {
			msg = fmt.Sprintf("local agent returned status %d", resp.StatusCode)
		}
		return msg, true
	}
	var data struct {
		Result string `json:"result"`
	}
	if err := json.Unmarshal(envelope.Data, &data); err != nil {
		return "local agent response missing result payload", true
	}
	return data.Result, false
}

// ---------------------------------------------------------------------------
// JSON-RPC 应答
// ---------------------------------------------------------------------------

func (s *peerMCPServer) writeResult(id json.RawMessage, result interface{}) {
	s.write(map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      id,
		"result":  result,
	})
}

func (s *peerMCPServer) writeError(id json.RawMessage, code int, message string) {
	if id == nil {
		id = json.RawMessage("null")
	}
	s.write(map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      id,
		"error":   map[string]interface{}{"code": code, "message": message},
	})
}

func (s *peerMCPServer) write(v interface{}) {
	raw, err := json.Marshal(v)
	if err != nil {
		return
	}
	_, _ = s.out.Write(append(raw, '\n'))
}
