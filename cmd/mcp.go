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
// Claude Code 接入：claude mcp add -s user aliang-peer -- <aliang绝对路径> mcp
// （-s user 必写：默认 local 作用域写在 .claude.json 的 per-project 条目里，
// 而内核 sanitized 档重建 mcp-config 时只合并顶层 user 作用域 mcpServers，
// local 作用域的条目在 sanitized 档 run 里不可见。）另可写入项目 .mcp.json。
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
	"aliang.one/nursorgate/internal/runtimepath"
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

// handleLine 解析并应答一行 JSON-RPC。通知（无 id）不应答；解析失败回
// -32700 parse error（保守起见 id 置 null）。
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
		s.writeResult(req.ID, peerMCPInitializeResult(proto))
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

// peerMCPInitializeResult 构造 initialize 响应。instructions 是 MCP 协议的
// 服务端级说明字段(Claude Code 会注入模型上下文):LLM 在工具列表之外由此
// 获得「本机属于设备舰队、跨设备需求先查 device_list」的全局图景——工具
// 描述只管单工具语义,路由意识靠这里(spec 2026-11-05 感知缺口修补)。
func peerMCPInitializeResult(protocolVersion string) map[string]interface{} {
	return map[string]interface{}{
		"protocolVersion": protocolVersion,
		"capabilities":    map[string]interface{}{"tools": map[string]interface{}{"listChanged": false}},
		"serverInfo":      map[string]interface{}{"name": "aliang-peer", "version": version.Version},
		"instructions": "Aliang cross-device toolset: this machine belongs to an Aliang account with other registered devices. " +
			"When the user asks to run a command on, inspect, or fetch a file from another device or machine, " +
			"call device_list FIRST to pick the device_id, then peer_exec / peer_fetch_file. " +
			"Remote targets enforce their own peer-exec gates; failures are reported per call." +
			" To drive an AI run on this machine (start / poll / approve), use ai_run_start, ai_run_status, ai_run_approve — those require the device's AI control to be enabled, and only operate on sessions they created.",
	}
}

func peerMCPToolDescriptors() []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(peerMCPTools))
	// 门控在组装时读一次：冷子进程的生命周期等于一次 CLI 会话，会话中途改
	// config.json 不会生效——这是有意的，工具面上半截变化的列表比稳定的旧
	// 列表更难排查。
	aiGateOn := agentMCPGateOn()
	for _, t := range peerMCPTools {
		// 过滤放在列表出口而不是从表里删：表是编译期常量，list 与 call 两处
		// 门控读同一张表，「谁在不在表里」才只有一种答案。
		if isAIMCPToolName(t.name) && !aiGateOn {
			continue
		}
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

// ---------------------------------------------------------------------------
// agent-as-MCP 工具组门控（customer.agentcore.ai_mcp，三态，nil=默认开）
// ---------------------------------------------------------------------------

// agentMCPGateConfigPath 是门控读配置的路径 seam。默认走 runtimepath.UserConfigPath()
// （~/.aliang/config.json）。
//
// 为什么是「包级变量」而不是直接调用：单测必须能换临时 fixture。用户主目录
// 没有 env 覆盖（UserStateDir 只认 HOME/USERPROFILE），固定路径会让 tools/call
// 的用例读到开发机上真实的 ~/.aliang/config.json —— 门控值随人而变，测试也就
// 随人红绿，更糟的是想造 gate off 就得手改真家目录。
var agentMCPGateConfigPath = func() string {
	if p, err := runtimepath.UserConfigPath(); err == nil {
		return p
	}
	return ""
}

// agentMCPGateOn 读 customer.agentcore.ai_mcp 三态：文件不存在 / 解析失败 /
// 字段缺失(nil) 一律 = 开启。
//
// 为什么直读文件而不用 config.GetGlobalConfig()：aliang mcp 是 CLI 每次拉起
// 的冷子进程，customer config 只由常驻 agent 主进程 SetGlobalConfig 装载，
// 从它等于把门控写成永久 fail-open，用户在仪表盘/AI 规则里关掉 ai_mcp 也
// 关不掉工具。
//
// 为什么读错也 fail-open（只 Warn）：与家族其它能力开关（MCPEnabled /
// SkillsEnabled / MemoryEnabled）的加法语义一致 —— 升级/半截写入/磁盘抖动
// 不得静默剥夺既有能力；要关必须在配置里写出显式的 false。
//
// 解析用局部 struct 而不引 processor/config 完整 Config：冷子进程只需要一棵
// 三字段子树，拉进完整 Config 会把整条依赖链拖进 CLI 启动路径。
func agentMCPGateOn() bool {
	path := agentMCPGateConfigPath()
	if path == "" {
		return true
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		// 文件不存在是首次安装的常态，不 Warn（否则每次启动都刷一行噪音）；
		// 其它读错（权限/IO）才是值得留痕的异常。
		if !os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "aliang mcp: read %s for ai_mcp gate: %v (defaulting to enabled)\n", path, err)
		}
		return true
	}
	var probe struct {
		Customer struct {
			AgentCore struct {
				AIMCP *bool `json:"ai_mcp"`
			} `json:"agentcore"`
		} `json:"customer"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		fmt.Fprintf(os.Stderr, "aliang mcp: parse %s for ai_mcp gate: %v (defaulting to enabled)\n", path, err)
		return true
	}
	if probe.Customer.AgentCore.AIMCP == nil {
		return true
	}
	return *probe.Customer.AgentCore.AIMCP
}

// isAIMCPToolName 判断名字是否属于 agent-as-MCP 工具组。list 与 call 两处
// 门控共用这一份名单 —— 若各写一条 || 链，加第四个工具时漏改一处就会出现
// 「列表里没有、但 tools/call 仍能打到」的静默越门（LLM 记着旧工具名就能绕）。
func isAIMCPToolName(name string) bool {
	switch name {
	case "ai_run_start", "ai_run_status", "ai_run_approve":
		return true
	}
	return false
}

// peerMCPTools 与 services 侧 LLM 工具面一一对应；后端全走 56433 本地路由，
// 复用同一套实现、脱敏与审计。
//
// 第 6 工具组（ai_run_*，agent-as-MCP）走同一张表：tools/list 与 tools/call
// 都已按表驱动，门控只需在两条出口各加一次过滤，不必为它新开一套分发。
var peerMCPTools = []peerMCPTool{
	{
		name: "peer_exec",
		description: "Run a one-shot command on another device owned by the same account and " +
			"return its exit code plus stdout/stderr. The remote device must have peer exec " +
			"enabled. Serial per target device: a busy target returns fail_reason=busy immediately. " +
			"Also serial per calling device — run peer calls one after another, never in parallel " +
			"(concurrent calls fail with fail_reason=rate_limited). On Windows targets the command " +
			"runs under Windows PowerShell 5.1: \"&&\" is unavailable (use \";\" or PS cmdlets), " +
			"external .exe may be unavailable (prefer PS built-ins like $env:COMPUTERNAME or " +
			"Get-FileHash, or absolute paths), and PS output is UTF-8 (native-exe output in a " +
			"legacy codepage may still be garbled). Cancellation/timeout only aborts the local " +
			"wait — the remote command still runs to completion on the target; do not blindly " +
			"retry side-effecting commands.",
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
			"transfer inbox. The source device must have peer fetch enabled. Returns a transfer_id; " +
			"completion is delivered via transfer progress.",
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
		description: "Cross-device directory for this Aliang account. Call this FIRST whenever the user asks to run commands on, inspect, or fetch files from another device or machine. " +
			"List every device in this account with id, name, platform, online status " +
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
	// ---- 第 6 工具组：agent-as-MCP（ai_run_*） ----
	//
	// session 契约句是描述里的承重部分而不是客套：外部 LLM 会先看到工具列表，
	// 若它以为这里能读到「用户正在手机上聊的那个会话」，就会拿 session_id 去
	// 猜、去复用 —— 服务端会一律拒（createdBy 标记三处校验），但那时工具调用
	// 已经白跑一轮。把边界写进描述，是让模型一开始就不提这个请求。
	//
	// 逐出提示同理：会话是内存态，重启/压内存就没了。不写清楚的话，模型拿到
	// 「session not found」会反复用同一个 id 重试，而不是开新会话。
	{
		name: "ai_run_start",
		description: "Start an agentcore AI run on this machine. Async: returns session_id immediately; " +
			"poll ai_run_status for progress and pending approvals. Restricted to sessions created by " +
			"this tool face; phone/desktop sessions are not accessible. " +
			"sessions may be evicted under memory pressure or after an agent restart — if ai_run_status " +
			"reports the session missing, start a new one.",
		schema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"content":      map[string]interface{}{"type": "string", "description": "Task / prompt to send to the agent"},
				"project_path": map[string]interface{}{"type": "string", "description": "Absolute path of the project the agent should work in"},
				"session_id":   map[string]interface{}{"type": "string", "description": "Continue an existing tool-face session; omit to create a new one"},
				"provider":     map[string]interface{}{"type": "string", "description": "Kernel provider hint (optional; omit for auto)"},
			},
			"required": []string{"content", "project_path"},
		},
		timeoutMs: 60_000,
		method:    http.MethodPost,
		path:      "/api/agent/ai/mcp/start",
		buildReq: func(args map[string]interface{}) interface{} {
			// 键名 snake_case 原样下发：端点侧（agent_handler.handleAIMCPOp →
			// AgentMCPStart）按 body["project_path"] 取值，驼峰化一处就是静默空参。
			req := map[string]interface{}{
				"project_path": peerMCPArgString(args, "project_path"),
				"content":      peerMCPArgString(args, "content"),
			}
			if v := peerMCPArgString(args, "provider"); v != "" {
				req["provider"] = v
			}
			if v := peerMCPArgString(args, "session_id"); v != "" {
				req["session_id"] = v
			}
			return req
		},
	},
	{
		name: "ai_run_status",
		description: "Read the state of an agentcore session (running / idle / awaiting_approval), " +
			"its pending approvals and the last assistant summary. Restricted to sessions created by " +
			"this tool face.",
		schema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"session_id": map[string]interface{}{"type": "string", "description": "Session id returned by ai_run_start"},
			},
			"required": []string{"session_id"},
		},
		timeoutMs: 30_000,
		method:    http.MethodPost,
		path:      "/api/agent/ai/mcp/status",
		buildReq: func(args map[string]interface{}) interface{} {
			return map[string]interface{}{
				"session_id": peerMCPArgString(args, "session_id"),
			}
		},
	},
	{
		name: "ai_run_approve",
		// applied 语义写进描述是必要的：服务端在「没有匹配 waiter / 已被别的
		// 路径决议」两种情况下都回 200 + applied=false（而不是报错）。没这句，
		// 模型会把 applied=false 当成成功，然后等一个永远不会前进的会话。
		description: "Submit an approval decision for a pending approval of a tool-face session. " +
			"Returns applied=true only when the decision actually landed; applied=false means it did " +
			"not (no matching waiter, or already decided).",
		schema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"session_id":  map[string]interface{}{"type": "string", "description": "Tool-face session id"},
				"approval_id": map[string]interface{}{"type": "string", "description": "Approval id from ai_run_status pending_approvals"},
				"decision":    map[string]interface{}{"type": "string", "description": "accept or deny"},
			},
			"required": []string{"session_id", "approval_id", "decision"},
		},
		timeoutMs: 30_000,
		method:    http.MethodPost,
		path:      "/api/agent/ai/mcp/approve",
		buildReq: func(args map[string]interface{}) interface{} {
			return map[string]interface{}{
				"session_id":  peerMCPArgString(args, "session_id"),
				"approval_id": peerMCPArgString(args, "approval_id"),
				"decision":    peerMCPArgString(args, "decision"),
			}
		},
	},
}

// invokeTool 执行一个 tools/call：本地路由 → {code:0,data:{result}} 解包；
// 非 2xx / code!=0 → isError 文本（服务端已脱敏）。未知工具名返回 isError。
func (s *peerMCPServer) invokeTool(name string, rawArgs json.RawMessage) (string, bool) {
	// 门控先于查表：关闸时这三个名字要落进「未知工具」分支，而不是先被表
	// 命中再被拦——否则以后有人重构错误分支，很容易把这条早退丢掉。
	//
	// 文案与 peer_mcp 关闸同款（unknown tool）是刻意的：模型对「工具不存在」
	// 的处理是明确的（换路子），对「工具被禁用」会反复重试或改写参数。
	if isAIMCPToolName(name) && !agentMCPGateOn() {
		return fmt.Sprintf("unknown tool: %s", name), true
	}
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
	// 响应体 4MiB 封顶,与 LLM 注册表面 MaxOutputBytes 同值(agent_tool_registry
	// .go peer_exec):契约合法满输出(stdout/stderr 各 256KiB 裸字节)经 JSON
	// 转义可膨胀 1.5-3 倍,卡在 1MiB 会把合法结果截断成非法 JSON 整次报废。
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
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
