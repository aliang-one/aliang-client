package services

// AliangCode（agentcore）MCP 客户端：原始配置采集 + 连接编排 + 工具桥。
// 设计文档 docs/superpowers/specs/2026-10-26-agentcore-mcp-design.md。
//
// 安全红线：server 配置里的 env/headers 值只进 transport 构造，永不进
// 日志/journal/审批卡。

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"aliang.one/nursorgate/app/agentcore"
	"aliang.one/nursorgate/common/logger"
	"github.com/modelcontextprotocol/go-sdk/mcp"
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
func (b *agentCoreMCPBridge) Execute(ctx context.Context, input map[string]interface{}, _ *agentcore.ToolEnv) (result agentcore.ToolResult) {
	summary := summarizeAgentCoreMCPInput(input)
	panicked := true
	defer func() {
		if panicked {
			result = agentcore.ToolResult{Content: "MCP 工具执行崩溃（已兜底）", IsError: true}
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
	panicked = false
	if b.onCall != nil {
		func() {
			defer func() { _ = recover() }()
			b.onCall(b.name, summary, isErr)
		}()
	}
	return agentcore.ToolResult{Content: text, IsError: isErr}
}

// agentCoreMCPResultText 拼接 content blocks 的文本（非 Text 块跳过）。
// CallToolResult.IsError=true 是「工具报错但协议成功」，语义上等价工具失败。
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
// 集中截断）——MCP 桥必须自截，否则异常 server 的巨量输出直接打进模型上下文。
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
// ⚠️ raw.Headers 暂未注入：go-sdk v1.8.0 两个 HTTP transport 均无 Headers
// 字段（需自 RoundTripper）；带非空 Headers 的条目由 agentCoreMCPServerToolDefs
// 预判跳过，别白烧超时预算 dial 一个注定 401 的连接。
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

// agentCoreMCPBridgeName 组装桥工具名，锁两条硬约束：
//  1. 总长 ≤128：Anthropic API 工具名 ^[a-zA-Z0-9_-]{1,128}$，理论最长
//     5+64+2+64=135 会整轮请求 400——server 段按 tool 段剩余预算收缩。
//  2. 全局唯一：清洗把 a.b/a:b 压成同段，Registry.Register 后注册者胜，
//     重名会静默遮蔽——冲突追加 -2/-3 序号，序号占长前先重截 base。
//
// seen 由调用方跨 server 持有（整个 run 一张表）。
func agentCoreMCPBridgeName(serverName, toolName string, seen map[string]bool) string {
	toolSeg := sanitizeAgentCoreMCPSegment(toolName)
	serverMax := 128 - len("mcp__") - len("__") - len(toolSeg)
	if serverMax < 1 {
		serverMax = 1 // 防御：tool 段 64 封顶下实际到不了这里
	}
	serverSeg := sanitizeAgentCoreMCPSegment(serverName)
	if len(serverSeg) > serverMax {
		serverSeg = serverSeg[:serverMax]
	}
	base := "mcp__" + serverSeg + "__" + toolSeg
	name := base
	for n := 2; seen[name]; n++ {
		suffix := "-" + strconv.Itoa(n)
		room := 128 - len(suffix)
		if len(base) > room {
			base = base[:room] // 序号占用长度，重截后再拼
		}
		name = base + suffix
	}
	seen[name] = true
	return name
}

// agentCoreMCPServerToolDefs 连接全部 raw server、拉工具清单并包成桥。
// 返回桥列表（已截 128）与统一关闭函数。单个 server 失败跳过 + Warn；
// 全部失败返回空桥列表（run 照常，纯内置工具）。
// 超时按 server 计（agentCoreMCPInitTimeout 各自一份）——共享预算会让排后
// 的 server 趋零被误杀；connect ctx 只裁握手（transport 连接不绑 ctx，
// cancel 后 session 仍可 CallTool）。
func (m *agentAIManager) agentCoreMCPServerToolDefs(ctx context.Context, raws []agentCoreMCPRawServer, onCall func(tool, summary string, isErr bool)) ([]agentcore.Tool, func()) {
	var closes []func()
	bridges := make([]agentcore.Tool, 0, agentCoreMCPToolCap)
	seen := map[string]bool{}
	closeAll := func() {
		for _, c := range closes {
			c()
		}
	}
	fail := func(server string, err error) {
		logger.Warn(fmt.Sprintf("agentcore-mcp: server 跳过 server=%s err=%v", sanitizeAgentCoreMCPSegment(server), err))
	}
	for _, raw := range raws {
		isHTTP := strings.EqualFold(raw.Type, "http") || strings.EqualFold(raw.Type, "sse")
		if isHTTP && len(raw.Headers) > 0 {
			logger.Warn(fmt.Sprintf("agentcore-mcp: server 跳过 server=%s reason=headers 暂不支持（v1 限制）", sanitizeAgentCoreMCPSegment(raw.Name)))
			continue
		}
		sctx, cancel := context.WithTimeout(ctx, agentCoreMCPInitTimeout)
		session, closeFn, err := agentCoreMCPDial(sctx, raw)
		if err != nil {
			fail(raw.Name, err)
			cancel()
			continue
		}
		closes = append(closes, closeFn)
		tools, err := listAgentCoreMCPTools(sctx, session)
		if err != nil {
			fail(raw.Name, err)
			cancel()
			continue
		}
		cancel() // 该 server 处理完（成功路径）；session 交给 closeAll 关
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
				name:        agentCoreMCPBridgeName(raw.Name, tool.Name, seen),
				description: desc, schema: schema, onCall: onCall,
			})
		}
	}
	return bridges, closeAll
}
