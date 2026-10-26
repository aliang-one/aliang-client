package services

// AliangCode（agentcore）MCP 客户端：原始配置采集 + 连接编排 + 工具桥。
// 设计文档 docs/superpowers/specs/2026-10-26-agentcore-mcp-design.md。
//
// 安全红线：server 配置里的 env/headers 值只进 transport 构造，永不进
// 日志/journal/审批卡。

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"aliang.one/nursorgate/common/logger"
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
	Name    string // 配置原始键（user 域为 server 名；插件域为 plugin:<p>:<s>）
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
