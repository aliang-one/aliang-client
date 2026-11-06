# AliangCode（agentcore）MCP 客户端 实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** AliangCode（agentcore）会话可发现并调用 user 域 MCP server（`~/.claude.json` + 已启用插件）的工具：默认逐调用手机审批，journal v2 记工具行使回放可见调用轨迹。

**Architecture:** 内核零改动——MCP 工具包成 `agentcore.Tool`（`mcp__<server>__<tool>`，段白名单清洗）经 `Registry.Register` 注入；原始配置采集复用清单层的 `readMCPServersFile`/`readPluginMCPDeclaration`；per-run 建连（go-sdk v1.8.0 client，stdio/http/sse），run 结束 defer 全关；审批在 `agentCoreApprove` 加 `mcp__` 前缀默认 require 规则（显式设备规则优先）；journal 新增 `kind:"tool"` 行，reader 渲染进 Transcript 但不计 DisplayMessageCount。

**Tech Stack:** Go；`github.com/modelcontextprotocol/go-sdk v1.8.0`（已在 go.mod，client 端首次使用）；测试用 `mcp.NewInMemoryTransports()` + `mcp.NewServer` + `AddTool` 起内存 MCP server（零进程 spawn）。

**Spec:** `docs/superpowers/specs/2026-10-26-agentcore-mcp-design.md`（已评审通过）

---

## 实施纪律（开工前必读）

1. **基线**：master `290fe3d`（journal 已合并）。worktree 实施（`git worktree add`），
   新 worktree 先 `cp -R <主仓>/app/website/dist <worktree>/app/website/dist`
   （`//go:embed` 坑）；只跑 `go test ./app/http/services/` 不受影响。
2. **提交**：CLAUDE.md 规定提交须用户明示同意；批准执行本计划即视为一并授权
   各任务 commit。标题全中文「新增：/修复：」，尾行
   `Co-Authored-By: Claude Code <noreply@anthropic.com>`。
3. **测试环境**：触盘测试 `t.Setenv("HOME", t.TempDir())`；MCP 交互测试一律走
   in-memory transport（不 spawn 真进程）。
4. **go-sdk 签名锚点**（v1.8.0，写码时如不符以此为准）：
   `mcp.NewClient(*Implementation, *ClientOptions)`（client.go:51）、
   `Client.Connect(ctx, Transport, *ClientSessionOptions)`（client.go:308）、
   `ClientSession.ListTools/CallTool`（client.go:1263/1284）、
   `CommandTransport{Command *exec.Cmd}`（cmd.go:20）、
   `StreamableClientTransport`（streamable.go:1966）、`SSEClientTransport`（sse.go:393）、
   `Tool{...; InputSchema any}`（protocol.go:1921）、`ListToolsResult{Tools, NextCursor}`（protocol.go:1326）、
   `CallToolParams{Name, Arguments any}`（protocol.go:219）、`CallToolResult{Content, IsError}`（protocol.go:266）、
   `ToolHandler func(ctx, *CallToolRequest) (*CallToolResult, error)`（tool.go:30）、
   `NewInMemoryTransports()`（transport.go:183）、`NewServer(impl, opts)`（server.go:211）、`Server.AddTool(*Tool, ToolHandler)`（server.go:315）。
5. **安全红线**：MCP server 配置里的 env/headers 值只进 `exec.Cmd.Env` 与
   transport 构造，**永不**进日志/journal/审批卡。

## 文件结构

| 文件 | 动作 | 职责 |
|---|---|---|
| `app/http/services/agent_ai_agentcore_mcp.go` | 新增 | 原始配置采集、连接编排、mcpBridgeTool |
| `app/http/services/agent_ai_agentcore_mcp_test.go` | 新增 | 全部 MCP 侧测试（in-memory server 基建在此） |
| `app/http/services/agent_ai_agentcore.go` | 修改 | Config 组装接入（`cfg.Tools`）+ 审批默认规则 |
| `app/http/services/agent_ai_agentcore_journal.go` | 修改 | `appendTool`（绕过去重） |
| `app/http/services/agent_inventory_agentcore.go` | 修改 | reader 解析 `kind:"tool"` 行 |
| `app/http/services/agent_ai_agentcore_journal_test.go` / `agent_inventory_agentcore_test.go` | 修改 | journal v2 / reader 测试 |

---

### Task 1: 原始配置采集

**Files:**
- Create: `app/http/services/agent_ai_agentcore_mcp.go`
- Test: `app/http/services/agent_ai_agentcore_mcp_test.go`

- [ ] **Step 1: 写失败测试**

`agent_ai_agentcore_mcp_test.go`：

```go
package services

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTestHomeFile(t *testing.T, home, rel, body string) {
	t.Helper()
	path := filepath.Join(home, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir 失败: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("写文件失败: %v", err)
	}
}

func TestAgentCoreMCPRawServersIn(t *testing.T) {
	home := t.TempDir()
	writeTestHomeFile(t, home, ".claude.json", `{
		"mcpServers": {
			"github": {"command": "npx", "args": ["-y", "gh-mcp"], "env": {"GH_TOKEN": "secret"}},
			"docs": {"type": "http", "url": "https://mcp.example.com/sse"},
			"off": {"command": "nope", "disabled": true},
			"broken": {"args": ["no-command"]}
		}
	}`)
	got := agentCoreMCPRawServersIn(home)
	if len(got) != 2 {
		t.Fatalf("条目数 = %d, want 2（disabled 与畸形跳过）: %+v", len(got), got)
	}
	byName := map[string]agentCoreMCPRawServer{}
	for _, s := range got {
		byName[s.Name] = s
	}
	if s := byName["github"]; s.Command != "npx" || len(s.Args) != 2 || s.Env["GH_TOKEN"] != "secret" {
		t.Fatalf("stdio 条目解析错误: %+v", s)
	}
	if s := byName["docs"]; s.URL != "https://mcp.example.com/sse" {
		t.Fatalf("http 条目解析错误: %+v", s)
	}
}

func TestAgentCoreMCPRawServersInPlugins(t *testing.T) {
	home := t.TempDir()
	writeTestHomeFile(t, home, ".claude/plugins/installed_plugins.json",
		`{"plugins": {"reader@market": [{"scope": "user", "installPath": "`+home+`/.claude/plugins/cache/reader"}]}}`)
	writeTestHomeFile(t, home, ".claude/settings.json", `{"enabledPlugins": {"reader@market": true}}`)
	writeTestHomeFile(t, home, ".claude/plugins/cache/reader/.mcp.json",
		`{"mcpServers": {"lookup": {"command": "reader-mcp"}}}`)
	got := agentCoreMCPRawServersIn(home)
	if len(got) != 1 || got[0].Name != "plugin:reader:lookup" || got[0].Command != "reader-mcp" {
		t.Fatalf("插件源解析错误: %+v", got)
	}
}

func TestAgentCoreMCPSanitizeSegment(t *testing.T) {
	cases := map[string]string{
		"plugin:reader:lookup": "plugin_reader_lookup",
		"glm-camp":             "glm-camp",
		"a.b/c":                "a_b_c",
		"":                     "srv",
	}
	for in, want := range cases {
		if got := sanitizeAgentCoreMCPSegment(in); got != want {
			t.Fatalf("sanitize(%q) = %q, want %q", in, got, want)
		}
	}
	if got := sanitizeAgentCoreMCPSegment(strings.Repeat("x", 100)); len(got) != 64 {
		t.Fatalf("长段应截 64: %d", len(got))
	}
}
```

（import 补 `strings`。）

- [ ] **Step 2: 运行确认失败**

Run: `go test ./app/http/services/ -run 'TestAgentCoreMCP' -v`
Expected: 编译失败（`undefined: agentCoreMCPRawServersIn` 等）

- [ ] **Step 3: 实现采集层**

`agent_ai_agentcore_mcp.go`：

```go
package services

// AliangCode（agentcore）MCP 客户端：原始配置采集 + 连接编排 + 工具桥。
// 设计文档 docs/superpowers/specs/2026-10-26-agentcore-mcp-design.md。
//
// 安全红线：server 配置里的 env/headers 值只进 transport 构造，永不进
// 日志/journal/审批卡。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"aliang.one/nursorgate/internal/logger"
)

const (
	// 与设备清单同口径的 server 上限；工具总数上限防 prompt 工具表爆炸。
	agentCoreMCPServerCap   = 24
	agentCoreMCPToolCap     = 128
	agentCoreMCPInitTimeout = 10 * time.Second
	agentCoreMCPDescCap     = 500
)

// agentCoreMCPRawServer 是一条未脱敏的 server 配置（连接用）。
type agentCoreMCPRawServer struct {
	Name    string            // 配置原始键（user 域为 server 名；插件域为 plugin:<p>:<s>）
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
	Type    string            `json:"type"` // ""|stdio / http / sse
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
}

type agentCoreMCPRawEntry struct {
	Command  string            `json:"command"`
	Args     []string          `json:"args"`
	Env      map[string]string `json:"env"`
	Type     string            `json:"type"`
	URL      string            `json:"url"`
	Headers  map[string]string `json:"headers"`
	Disabled bool              `json:"disabled"`
}

// sanitizeAgentCoreMCPSegment 工具名段白名单化。Anthropic API 工具名必须
// ^[a-zA-Z0-9_-]{1,128}$；插件复合键带冒号，直接用必整轮请求 400。
func sanitizeAgentCoreMCPSegment(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := b.String()
	if len(out) > 64 {
		out = out[:64]
	}
	if out == "" {
		out = "srv"
	}
	return out
}

// agentCoreMCPRawServersIn 采集 user 域原始 MCP 配置（未脱敏，连接专用）。
// 数据源与清单层同源：~/.claude.json 顶层 mcpServers + 已启用插件 .mcp.json，
// 解析直接复用清单层的 readMCPServersFile / readPluginMCPDeclaration（它们
// 填充的是未脱敏原始 map，脱敏发生在更下游的 FromClaudeRaw，我们绕开它）。
// 畸形/disabled 条目跳过；上限 24，按键排序保证截断确定性。
func agentCoreMCPRawServersIn(home string) []agentCoreMCPRawServer {
	if strings.TrimSpace(home) == "" {
		return nil
	}
	raw := map[string]interface{}{}
	_ = readMCPServersFile(filepath.Join(home, ".claude.json"), raw)
	for _, inst := range enabledUserScopePluginInstallsIn(home) {
		readPluginMCPDeclaration(inst.installPath, inst.pluginName, raw)
	}
	keys := make([]string, 0, len(raw))
	for k := range raw {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []agentCoreMCPRawServer
	for _, key := range keys {
		if len(out) >= agentCoreMCPServerCap {
			logger.Warn(fmt.Sprintf("agentcore-mcp: server 超上限截断 cap=%d", agentCoreMCPServerCap))
			break
		}
		body, err := json.Marshal(raw[key])
		if err != nil {
			continue
		}
		var entry agentCoreMCPRawEntry
		if err := json.Unmarshal(body, &entry); err != nil {
			logger.Warn(fmt.Sprintf("agentcore-mcp: 畸形条目跳过 key=%s err=%v", key, err))
			continue
		}
		if entry.Disabled {
			continue
		}
		isHTTP := strings.EqualFold(entry.Type, "http") || strings.EqualFold(entry.Type, "sse")
		if !isHTTP && strings.TrimSpace(entry.Command) == "" {
			logger.Warn(fmt.Sprintf("agentcore-mcp: 缺 command 的条目跳过 key=%s", key))
			continue
		}
		if isHTTP && strings.TrimSpace(entry.URL) == "" {
			logger.Warn(fmt.Sprintf("agentcore-mcp: 缺 url 的条目跳过 key=%s", key))
			continue
		}
		out = append(out, agentCoreMCPRawServer{
			Name: key, Command: entry.Command, Args: entry.Args, Env: entry.Env,
			Type: entry.Type, URL: entry.URL, Headers: entry.Headers,
		})
	}
	return out
}
```

- [ ] **Step 4: 运行确认通过**

Run: `go test ./app/http/services/ -run 'TestAgentCoreMCP' -v`
Expected: 3 个测试 PASS（若 `readMCPServersFile`/`readPluginMCPDeclaration` 的实际
键值语义与测试假设有出入，以实现为准修测试——这两个函数是既有已测原语）

- [ ] **Step 5: 提交**

```bash
git add app/http/services/agent_ai_agentcore_mcp.go app/http/services/agent_ai_agentcore_mcp_test.go
git commit -m "$(cat <<'EOF'
新增:agentcore MCP 原始配置采集(复用清单层解析,未脱敏连接专用,白名单清洗)

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

---

### Task 2: 连接编排 + 工具桥

**Files:**
- Modify: `app/http/services/agent_ai_agentcore_mcp.go`
- Test: `app/http/services/agent_ai_agentcore_mcp_test.go`

- [ ] **Step 1: 写失败测试（in-memory MCP server 基建 + 桥行为）**

`agent_ai_agentcore_mcp_test.go` 追加：

```go
import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"aliang.one/nursorgate/app/agentcore"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// startTestMCPSession 起一个内存 MCP server（零进程）并返回已连接的 client
// session。handler 返回 (text, isError)。
func startTestMCPSession(t *testing.T, toolName string, handler func(args map[string]interface{}) (string, bool)) *mcp.ClientSession {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "test-srv", Version: "0"}, nil)
	server.AddTool(&mcp.Tool{Name: toolName, Description: "测试工具", InputSchema: map[string]any{
		"type": "object", "properties": map[string]any{"q": map[string]any{"type": "string"}},
	}}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args, _ := req.Params.Arguments.(map[string]interface{})
		text, isErr := handler(args)
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}, IsError: isErr}, nil
	})
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	if _, err := server.Connect(context.Background(), serverTransport, nil); err != nil {
		t.Fatalf("server 连接失败: %v", err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test-cli", Version: "0"}, nil)
	session, err := client.Connect(context.Background(), clientTransport, nil)
	if err != nil {
		t.Fatalf("client 连接失败: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func TestAgentCoreMCPBridgeExecute(t *testing.T) {
	session := startTestMCPSession(t, "lookup", func(args map[string]interface{}) (string, bool) {
		if args["q"] == "boom" {
			return "查无此人", true
		}
		return "结果:" + args["q"].(string), false
	})
	var journaled []string
	bridge := &agentCoreMCPBridge{
		session: session, rawTool: "lookup", name: "mcp__test-srv__lookup",
		description: "测试工具",
		schema:      map[string]interface{}{"type": "object"},
		onCall:      func(tool, summary string, isErr bool) { journaled = append(journaled, tool+"|"+summary) },
	}
	res := bridge.Execute(context.Background(), map[string]interface{}{"q": "hi"}, nil)
	if res.IsError || !strings.Contains(res.Content, "结果:hi") {
		t.Fatalf("执行结果错误: %+v", res)
	}
	res = bridge.Execute(context.Background(), map[string]interface{}{"q": "boom"}, nil)
	if !res.IsError || !strings.Contains(res.Content, "查无此人") {
		t.Fatalf("IsError 语义错误: %+v", res)
	}
	if len(journaled) != 2 || !strings.HasPrefix(journaled[0], "mcp__test-srv__lookup|") {
		t.Fatalf("journal 回调缺失: %v", journaled)
	}
}

func TestAgentCoreMCPBridgeRecoversPanic(t *testing.T) {
	session := startTestMCPSession(t, "evil", func(map[string]interface{}) (string, bool) {
		return "", false
	})
	bridge := &agentCoreMCPBridge{session: session, rawTool: "evil", name: "mcp__s__evil"}
	// onCall 里故意 panic：Execute 必须兜住不炸整轮（纵深防御，内核已有
	// executeOne recover，这里双保险）。
	bridge.onCall = func(string, string, bool) { panic("journal boom") }
	res := bridge.Execute(context.Background(), map[string]interface{}{}, nil)
	if res.IsError {
		t.Fatalf("onCall panic 不应影响已成功的调用结果: %+v", res)
	}
}

func TestAgentCoreMCPListToolsFollowsCursor(t *testing.T) {
	session := startTestMCPSession(t, "t1", func(map[string]interface{}) (string, bool) { return "ok", false })
	tools, err := listAgentCoreMCPTools(context.Background(), session)
	if err != nil {
		t.Fatalf("ListTools 失败: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "t1" {
		t.Fatalf("工具列表错误: %+v", tools)
	}
}
```

（`startTestMCPSession` 的 `InputSchema` 用 `map[string]any` 对齐 SDK 的 `any` 类型。）

- [ ] **Step 2: 运行确认失败**

Run: `go test ./app/http/services/ -run 'TestAgentCoreMCPBridge|TestAgentCoreMCPListTools' -v`
Expected: 编译失败（`undefined: agentCoreMCPBridge` 等）

- [ ] **Step 3: 实现桥与连接编排**

`agent_ai_agentcore_mcp.go` 追加：

```go
// agentCoreMCPBridge 把一个 MCP 工具包成内核 agentcore.Tool。
type agentCoreMCPBridge struct {
	session     *mcp.ClientSession
	rawTool     string // server 侧原始工具名
	name        string // mcp__<server>__<tool>，段已白名单清洗
	description string
	schema      map[string]interface{}
	// onCall：journal v2 工具行回调（宿主注入；nil=不记）。
	onCall func(tool, summary string, isErr bool)
}

func (b *agentCoreMCPBridge) Name() string                        { return b.name }
func (b *agentCoreMCPBridge) Description() string                 { return b.description }
func (b *agentCoreMCPBridge) InputSchema() map[string]interface{} { return b.schema }

// Execute：CallTool + journal 回调。内核 executeOne 已有全量 recover（loop.go
// d2da2b5 修复），这里的 recover 是纵深防御——journal 回调在宿主层，不允许
// 它把工具结果搅了。
//
// ⚠️ 2026-10-26 执行期评审修正：初稿 defer 缺 recover()（Go 语义下 panic 穿透
// defer 继续上抛，兜底实为死代码）。正确形态见下——recover 非 nil 即信号，
// panicked 标志不需要。
func (b *agentCoreMCPBridge) Execute(ctx context.Context, input map[string]interface{}, _ *agentcore.ToolEnv) (result agentcore.ToolResult) {
	summary := summarizeAgentCoreMCPInput(input)
	defer func() {
		if rec := recover(); rec != nil {
			result = agentcore.ToolResult{Content: fmt.Sprintf("MCP 工具执行崩溃（已兜底）: %v", rec), IsError: true}
			if b.onCall != nil {
				func() {
					defer func() { _ = recover() }() // journal 回调的 panic 不许二次炸
					b.onCall(b.name, summary, true)
				}()
			}
		}
	}()
	res, err := b.session.CallTool(ctx, &mcp.CallToolParams{Name: b.rawTool, Arguments: input})
	var text string
	isErr := false
	if err != nil {
		isErr, text = true, "MCP 调用失败: "+err.Error()
	} else {
		text, isErr = agentCoreMCPResultText(res)
	}
	text = truncateAgentCoreMCPOutput(text)
	if b.onCall != nil {
		func() {
			defer func() { _ = recover() }()
			b.onCall(b.name, summary, isErr)
		}()
	}
	return agentcore.ToolResult{Content: text, IsError: isErr}
}

// agentCoreMCPResultText 拼接 content blocks 的文本（与 TextContent 形态解耦：
// 非 Text 块跳过）。CallToolResult.IsError=true 是「工具报错但协议成功」，
// 语义上等价工具失败。
func agentCoreMCPResultText(res *mcp.CallToolResult) (string, bool) {
	if res == nil {
		return "", false
	}
	var b strings.Builder
	for _, block := range res.Content {
		if tc, ok := block.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
			b.WriteString("\n")
		}
	}
	return strings.TrimRight(b.String(), "\n"), res.IsError
}

// truncateAgentCoreMCPOutput 桥自实现 64KB 字节上限。⚠️ 内核的
// truncateToolOutput 未导出、且只在各内置工具 Execute 内部自调（loop.go 无
// 集中截断）——MCP 桥必须自截，否则异常 server 的巨量输出直接打进模型上下文
// （spec §6/§11）。超限在行边界切并注明原始字节数（与内置工具同风格）。
func truncateAgentCoreMCPOutput(s string) string {
	const capBytes = 64 * 1024
	if len(s) <= capBytes {
		return s
	}
	cut := s[:capBytes]
	if i := strings.LastIndexByte(cut, '\n'); i > 0 {
		cut = cut[:i]
	}
	return fmt.Sprintf("%s\n…[原始 %d 字节，仅显示前 %d 字节]", cut, len(s), len(cut))
}

// summarizeAgentCoreMCPInput 稳定摘要：key=value 按 key 排序拼接，截 200。
func summarizeAgentCoreMCPInput(input map[string]interface{}) string {
	keys := make([]string, 0, len(input))
	for k := range input {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		if b.Len() > 0 {
			b.WriteString(" ")
		}
		v, _ := json.Marshal(input[k])
		b.WriteString(k + "=" + string(v))
		if b.Len() >= 200 {
			break
		}
	}
	out := b.String()
	if len(out) > 200 {
		out = out[:200]
	}
	return out
}

// listAgentCoreMCPTools 跟随 nextCursor 分页取尽工具（部分 server 启用分页，
// 只取第一页会静默丢工具）。
func listAgentCoreMCPTools(ctx context.Context, session *mcp.ClientSession) ([]*mcp.Tool, error) {
	var out []*mcp.Tool
	cursor := ""
	for {
		res, err := session.ListTools(ctx, &mcp.ListToolsParams{Cursor: cursor})
		if err != nil {
			return nil, err
		}
		out = append(out, res.Tools...)
		if res.NextCursor == "" || len(out) >= agentCoreMCPToolCap {
			break
		}
		cursor = res.NextCursor
	}
	return out, nil
}

// agentCoreMCPDial 按 raw 配置建一条连接。transport 选择：stdio→CommandTransport；
// type=http→Streamable；type=sse→SSE。返回 session 与关闭函数。
func agentCoreMCPDial(ctx context.Context, raw agentCoreMCPRawServer) (*mcp.ClientSession, func(), error) {
	var transport mcp.Transport
	switch {
	case strings.EqualFold(raw.Type, "http"):
		transport = &mcp.StreamableClientTransport{Endpoint: raw.URL}
	case strings.EqualFold(raw.Type, "sse"):
		transport = &mcp.SSEClientTransport{Endpoint: raw.URL}
	default:
		cmd := exec.Command(raw.Command, raw.Args...)
		cmd.Env = os.Environ()
		for k, v := range raw.Env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
		transport = &mcp.CommandTransport{Command: cmd}
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "aliang-agentcore", Version: agentVersion()}, nil)
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return nil, nil, err
	}
	return session, func() { _ = session.Close() }, nil
}

// agentCoreMCPServerToolDefs 连接全部 raw server、拉工具清单并包成桥。
// 返回按序的桥列表（已截 128）与统一关闭函数。单个 server 失败跳过 + Warn；
// 全部失败返回空桥列表（run 照常，纯内置工具）。
func (m *agentAIManager) agentCoreMCPServerToolDefs(ctx context.Context, raws []agentCoreMCPRawServer, onCall func(tool, summary string, isErr bool)) ([]agentcore.Tool, func()) {
	ctx, cancel := context.WithTimeout(ctx, agentCoreMCPInitTimeout)
	defer cancel() // 防 go vet lostcancel
	var closes []func()
	bridges := make([]agentcore.Tool, 0, agentCoreMCPToolCap)
	closeAll := func() {
		for _, c := range closes {
			c()
		}
	}
	fail := func(server string, err error) {
		logger.Warn(fmt.Sprintf("agentcore-mcp: server 跳过 server=%s err=%v", sanitizeAgentCoreMCPSegment(server), err))
	}
	for _, raw := range raws {
		session, closeFn, err := agentCoreMCPDial(ctx, raw)
		if err != nil {
			fail(raw.Name, err)
			continue
		}
		closes = append(closes, closeFn)
		tools, err := listAgentCoreMCPTools(ctx, session)
		if err != nil {
			fail(raw.Name, err)
			continue
		}
		serverSeg := sanitizeAgentCoreMCPSegment(raw.Name)
		for _, tool := range tools {
			if len(bridges) >= agentCoreMCPToolCap {
				logger.Warn(fmt.Sprintf("agentcore-mcp: 工具超上限截断 cap=%d", agentCoreMCPToolCap))
				return bridges, closeAll
			}
			schema, _ := tool.InputSchema.(map[string]interface{})
			desc := tool.Description
			if len(desc) > agentCoreMCPDescCap {
				desc = desc[:agentCoreMCPDescCap]
			}
			bridges = append(bridges, &agentCoreMCPBridge{
				session: session, rawTool: tool.Name,
				name:        "mcp__" + serverSeg + "__" + sanitizeAgentCoreMCPSegment(tool.Name),
				description: desc, schema: schema, onCall: onCall,
			})
		}
	}
	return bridges, closeAll
}
```

import 追加：`"context"`、`"os"`、`"os/exec"`、`"aliang.one/nursorgate/app/agentcore"`、`"github.com/modelcontextprotocol/go-sdk/mcp"`（`agentVersion()` 为 services 现有函数，若名不符以实际为准）。

- [ ] **Step 4: 运行确认通过**

Run: `go test ./app/http/services/ -run 'TestAgentCoreMCP' -v`
Expected: 全部 PASS（分页用例在 in-memory server 单工具下验证的是「不崩溃且取尽」语义）

- [ ] **Step 5: 提交**

```bash
git add app/http/services/agent_ai_agentcore_mcp.go app/http/services/agent_ai_agentcore_mcp_test.go
git commit -m "$(cat <<'EOF'
新增:agentcore MCP 连接编排与工具桥(in-memory 可测,分页取尽,panic 兜底)

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

---

### Task 3: journal v2 工具行 + reader

**Files:**
- Modify: `app/http/services/agent_ai_agentcore_journal.go`（appendTool）
- Modify: `app/http/services/agent_inventory_agentcore.go`（reader `case "tool"`）
- Test: `agent_ai_agentcore_journal_test.go` / `agent_inventory_agentcore_test.go`

- [ ] **Step 1: 写失败测试**

`agent_ai_agentcore_journal_test.go` 追加：

```go
func TestAgentCoreJournalAppendTool(t *testing.T) {
	dir := t.TempDir()
	j := newAgentCoreJournal(dir)
	j.ensureSessionHeader("conv-tool", "/tmp/p", "glm-5.2", "2026-10-26T01:00:00Z")
	j.appendTool("conv-tool", "mcp__s__lookup", "q=hi", false)
	j.appendTool("conv-tool", "mcp__s__lookup", "q=boom", true)
	rows := readJournalLines(t, dir, "conv-tool")
	if len(rows) != 3 { // 头 + 2 tool 行（tool 行不去重、无 message_id）
		t.Fatalf("行数 = %d, want 3: %v", len(rows), rows)
	}
	if rows[1]["kind"] != "tool" || rows[1]["tool"] != "mcp__s__lookup" || rows[1]["is_error"] != false {
		t.Fatalf("tool 行错误: %v", rows[1])
	}
	if rows[2]["is_error"] != true {
		t.Fatalf("is_error 语义错误: %v", rows[2])
	}
}
```

（`newAgentCoreJournal(dir)` / 3 参 `readJournalLines(t, dir, sessionID)` 与既有
测试同款；`ensureSessionHeader` 第 4 参 createdAt 形态以现有签名为准。）

`agent_inventory_agentcore_test.go` 追加：

```go
// writeCoreJournalRawFixture 自写原始 JSONL 行（既有 writeAgentCoreJournalFixture
// 内容固定不支持自定义行，这里新建一个小 helper）。
func writeCoreJournalRawFixture(t *testing.T, sessionID string, lines ...string) string {
	t.Helper()
	dir := agentCoreJournalDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir 失败: %v", err)
	}
	path := filepath.Join(dir, sessionID+".jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("写 fixture 失败: %v", err)
	}
	return path
}

func TestReadAgentCoreSessionMetaToolRows(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path := writeCoreJournalRawFixture(t, "sess-tools",
		`{"kind":"session","session_id":"sess-tools","project_path":"/tmp/p","model":"m","created_at":"2026-10-26T01:00:00Z"}`,
		`{"kind":"msg","role":"user","message_id":"u1","text":"查一下","ts":"2026-10-26T01:00:05Z"}`,
		`{"kind":"tool","tool":"mcp__s__lookup","summary":"q=hi","is_error":false,"ts":"2026-10-26T01:00:06Z"}`,
		`{"kind":"msg","role":"assistant","message_id":"a1","text":"结果","ts":"2026-10-26T01:00:09Z"}`,
	)
	session := readAgentCoreSessionMetaWithOptions(path, agentVibeSessionReadOptions{Limit: agentVibeTranscriptMaxMessages, IncludePageMeta: true})
	if session.MessageCount != 2 || session.DisplayMessageCount == nil || *session.DisplayMessageCount != 2 {
		t.Fatalf("tool 行不得计入对话计数: %d %v", session.MessageCount, session.DisplayMessageCount)
	}
	toolRows := 0
	for _, msg := range session.Transcript {
		if msg.Role == "tool" {
			toolRows++
			if msg.Content != "mcp__s__lookup q=hi" {
				t.Fatalf("tool 行渲染错误: %+v", msg)
			}
		}
	}
	if toolRows != 1 {
		t.Fatalf("Transcript 应含 1 条 tool 行, got %d", toolRows)
	}
}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./app/http/services/ -run 'TestAgentCoreJournalAppendTool|TestReadAgentCoreSessionMetaToolRows' -v`
Expected: FAIL/编译失败

- [ ] **Step 3: 实现**

(a) `agent_ai_agentcore_journal.go` 在 `appendEnd` 之后追加（与 appendMsg 同构但
**绕过 message_id 去重**——每次工具调用都留一行）：

```go
// appendTool 落 journal v2 工具行（kind=tool）。不参与 message_id 去重：
// 每次调用一行，是轨迹不是消息。写失败 Warn 吞掉。守卫与 appendMsg 家族
// 同款（nil/dir 空/sessionID 空直接返回）。
func (j *agentCoreJournal) appendTool(sessionID, tool, summary string, isError bool) {
	if j == nil || j.dir == "" || strings.TrimSpace(sessionID) == "" {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	j.appendLineLocked(sessionID, map[string]interface{}{
		"kind":     "tool",
		"tool":     tool,
		"summary":  summary,
		"is_error": isError,
		"ts":       time.Now().UTC().Format(time.RFC3339),
	})
}
```

（`map[string]interface{}` 形态与该实现现有的行构造一致；若实现已换成 struct
行类型则照抄该形态。）

(b) `agent_inventory_agentcore.go` reader 的 `switch row.Kind` 加：

```go
		case "tool":
			if session.ID == "" {
				return true
			}
			ts := normalizeAgentTime(row.Ts)
			// 合成 ID：tool 行无 message_id，用序号保证分页游标可用。
			toolCount++
			window.add(models.AgentVibeMessage{
				ID:        fmt.Sprintf("tool-%06d", toolCount),
				Role:      "tool",
				Content:   strings.TrimSpace(row.Tool + " " + row.Summary),
				Timestamp: ts,
				Index:     messageCount + toolCount - 1,
			})
```

（`row` 若是 struct 行类型需补 `Tool`/`Summary`/`IsError` 字段；`toolCount` 在
switch 外声明。`Index` 取「全部转录行」的序号，与 msg 行共用一条递增序列——
若现有实现 msg 行 Index=messageCount-1，则改成统一 counter 更干净，实现时
二选一并让既有测试作准。）

(c) reader 的窗口 `page(...)` 调用与 TotalCount 口径：tool 行进窗口但
`DisplayMessageCount`/`MessageCount` 仍只数 msg 行（既有分支不动）。

- [ ] **Step 4: 运行确认通过 + 回归**

Run: `go test ./app/http/services/ -run 'TestAgentCoreJournal|TestReadAgentCoreSessionMeta|TestCollectAgentCore|TestFindAgentVibeSessionDetail' -v`
Expected: 全部 PASS（含 journal 既有测试向后兼容）

- [ ] **Step 5: 提交**

```bash
git add app/http/services/agent_ai_agentcore_journal.go app/http/services/agent_inventory_agentcore.go app/http/services/agent_ai_agentcore_journal_test.go app/http/services/agent_inventory_agentcore_test.go
git commit -m "$(cat <<'EOF'
新增:journal v2 工具行(回放可见 MCP 调用轨迹,不计对话计数)

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

---

### Task 4: 审批默认规则

**Files:**
- Modify: `app/http/services/agent_ai_agentcore.go`（`agentCoreApprove`，~1028）
- Test: `app/http/services/agent_ai_agentcore_mcp_test.go`

- [ ] **Step 1: 写失败测试**

现有审批测试基建（`TestRunAgentCoreApprovalDeclineKeepsTurnAlive` 一族）已在
`agent_ai_agentcore_run_test.go`——参照其「策略短路」用例的写法追加：

```go
// 放在 agent_ai_agentcore_mcp_test.go（同包可用 run 测试基建）。
// 断言三点：①无策略规则时 mcp__* 走 require（requestApproval 被调）；
// ②显式 auto_approve 规则命中 mcp 工具时短路放行；③allow_all 策略下
// mcp__* 仍需批（默认规则优先于 DefaultDecision），非 mcp 工具不受影响。
func TestAgentCoreMCPApprovalDefaultRequiresApproval(t *testing.T) {
	// 具体断言形态照抄既有 approval 策略测试：注册带 mcp 工具的内核 run、
	// 记录 requestApproval 触发次数。三个子场景各跑一遍。
	t.Skip("实现时按既有 approval 测试基建补全——此处锁定意图，防略过")
}
```

> 实现注：不要带着 Skip 落库——先看
> `TestRunAgentCoreApprovalDeclineKeepsTurnAlive` 与策略短路用例怎么伪造
> approvalService/规则，三场景各写一个子测试再删 Skip。若既有基建只支持
> 「策略对象直评」，就补一条对 `evaluateApprovalPolicy` 输入前缀规则的
> 直评测试 + 一条 run 级测试。

- [ ] **Step 2: 运行确认失败**

Run: `go test ./app/http/services/ -run 'TestAgentCoreMCPApproval' -v`
Expected: SKIP（占位期）；随后按参照补全真实测试体后再进 Step 3——**禁止带着
Skip 落库**

- [ ] **Step 3: 实现默认规则**

`agentCoreApprove` 内，现码是 `switch decision, matchedID := svc.evaluateApprovalDecision(...); decision {`——
**求值必须提出 switch init**（否则 matchedID 作用域锁死、默认规则插不进），
改为：

```go
			decision, matchedID := svc.evaluateApprovalDecision(call.ToolName, toolInput, run.projectPath)
			// MCP 默认门（spec §7）：没有显式设备规则命中（matchedID 为空 =
			// 落到策略 DefaultDecision）的 mcp__* 工具一律要求手机审批——
			// 显式规则（含放行）优先于本默认。
			if matchedID == "" && strings.HasPrefix(call.ToolName, "mcp__") {
				decision = decisionRequireApproval
			}
			switch decision {
			case decisionAutoApprove:
			// ……以下原 switch 体不动
```

（`svc == nil` 分支不受影响：无审批服务时本来就走 `requestApproval` 全量审批。）

- [ ] **Step 4: 运行确认通过 + 回归**

Run: `go test ./app/http/services/ -run 'TestAgentCoreMCPApproval|TestRunAgentCoreApproval' -v`
Expected: 全部 PASS

- [ ] **Step 5: 提交**

```bash
git add app/http/services/agent_ai_agentcore.go app/http/services/agent_ai_agentcore_mcp_test.go
git commit -m "$(cat <<'EOF'
新增:agentcore MCP 工具默认需批(mcp__前缀无显式规则走手机审批)

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

---

### Task 5: runAgentCore 集成 + 端到端

**Files:**
- Modify: `app/http/services/agent_ai_agentcore.go`（Config 组装，~738）
- Test: `app/http/services/agent_ai_agentcore_mcp_test.go`

- [ ] **Step 1: 写失败测试（run 级 e2e）**

用 manager 的可注入 dial 跑一条完整链：设备策略显式放行 mcp 工具 → run 调用 →
journal 出齐 session/user/tool/assistant 四类行。测试基建：给 manager 加
`coreMCPDialForTest` 注入点（见 Step 3(c)），dial 返回 in-memory session：

```go
func TestRunAgentCoreMCPEndToEnd(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	// ① 内存 MCP server：echo 工具。
	session := startTestMCPSession(t, "echo", func(args map[string]interface{}) (string, bool) {
		return "echo:" + args["q"].(string), false
	})
	// ② run 文本脚本：先调 mcp__srv__echo 再给最终回答（照抄
	// agentCoreUpstream 的 tool-use 回合写法——见既有 tool 活动测试）。
	// ③ manager：dial 注入 + 设备策略 auto_approve mcp__srv__echo。
	// ④ 断言：run 成功；journal 文件四类行齐；tool 行 summary=q=hi；
	//    无 ai.error。
	//
	// 具体组装完全镜像 TestRunAgentCoreApprovalDeclineKeepsTurnAlive +
	// TestRunAgentCoreJournalsTranscript 的现成写法（同包可引用），不重复
	// 贴代码——实现者先读这两个测试再落笔。
	t.Skip("实现时按上述两个既有测试的组装方式补全")
}
```

> 同样不许带 Skip 落库。

- [ ] **Step 2: 运行确认失败**

Run: `go test ./app/http/services/ -run 'TestRunAgentCoreMCPEndToEnd' -v`
Expected: SKIP（占位期）；随后镜像两个参照测试补全真实测试体——**禁止带着
Skip 落库**

- [ ] **Step 3: 集成**

(a) manager 加注入点（`agent_ai_agentcore.go` manager struct 附近）：

```go
	// coreMCPDial 允许测试注入内存 MCP 会话；nil = 用真实 agentCoreMCPDial。
	coreMCPDialForTest func(ctx context.Context, raw agentCoreMCPRawServer) (*mcp.ClientSession, func(), error)
```

(b) `agentCoreMCPServerToolDefs` 的 dial 调用处改为优先用注入（nil 判别）。

(c) `runAgentCore` Config 组装（`cfg := &agentcore.Config{` 处，`Tools:` 行）改为：

```go
	// MCP 工具注入（spec §3）：采集→建连→包桥→注册。全部失败也只是空桥，
	// run 照常（纯内置 8 工具）。defer 全关防 stdio 子进程泄漏。
	mcpTools, closeMCP := m.agentCoreMCPServerToolDefs(ctx, agentCoreMCPRawServersIn(agentHome()), func(tool, summary string, isErr bool) {
		m.coreJournal.appendTool(run.sessionID, tool, summary, isErr)
	})
	defer closeMCP()
	registry := agentcore.DefaultRegistry()
	for _, tool := range mcpTools {
		registry.Register(tool)
	}
```

`cfg.Tools = registry`。（`onCall` 闭包捕获的 journal 为 nil 时安全：agentcore
journal 对空 dir 已是 no-op——若 `m.coreJournal` 可能为 nil，闭包里先判空。）

- [ ] **Step 4: 运行确认通过 + 全包回归**

Run: `go test ./app/http/services/ -run 'TestRunAgentCore' -v && go test ./app/http/services/ -count=1`
Expected: 全部 PASS

- [ ] **Step 5: go vet + 构建**

Run: `go vet ./app/http/services/ && go build ./app/...`
Expected: 无输出（worktree 缺 dist 先按实施纪律第 1 条拷贝）

- [ ] **Step 6: 提交**

```bash
git add app/http/services/agent_ai_agentcore.go app/http/services/agent_ai_agentcore_mcp_test.go
git commit -m "$(cat <<'EOF'
新增:agentcore run 接入 MCP 工具注入(采集-建连-注册-全关,端到端测试锁定)

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

---

### Task 6: 全量回归与真机冒烟清单

- [ ] **Step 1**: `go test ./app/http/services/ -count=1` 全绿（预存 flake 对照
      仓库记忆：单包复跑即可，勿当回归）
- [ ] **Step 2**: `go vet ./app/http/services/`
- [ ] **Step 3**: 真机冒烟清单（记入交接说明）：给 `~/.claude.json` 配一个真实
      MCP server（如 npx 形态）→ AliangCode 会话里让它调用 → 手机弹审批 →
      放行后出结果 → 回放可见 tool 行；拔掉 server 进程（kill）→ run 不挂。
- [ ] **Step 4**: 有修正才提交。

---

## 风险与回滚

- stdio 子进程泄漏 → defer closeAll + run ctx 取消联动（CommandTransport.Close
  自带 SIGTERM 兜底，TerminateDuration 默认 5s）；
- 恶意 server 输出爆炸 → 桥内 `truncateAgentCoreMCPOutput` 64KB 自截（内核
  truncateToolOutput 未导出且为各内置工具自调，无集中截断——桥必须自带）；
- 审批绕过 → Task 4 三场景测试锁定（显式规则优先/默认需批/allow_all 仍需批）；
- 回滚 = revert 各 commit；journal tool 行残留对旧 reader 静默跳过，无害。
