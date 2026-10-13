package services

// agent_mcp_inventory.go 采集设备级 MCP 服务器清单(claude/codex),脱敏后
// 挂到 AgentTool.MCPServers 随 tools[] 上报(spec:
// docs/superpowers/specs/2026-10-13-mcp-inventory-report-design.md)。
// env 值在任何路径都不进入返回结果;解析失败 fail-soft 返回 nil。

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"aliang.one/nursorgate/app/http/models"
)

const (
	mcpNameMaxLen           = 64
	mcpCommandMaxLen        = 512
	mcpURLMaxLen            = 512
	mcpServerMaxCount       = 24
	mcpTokenRedactMinLen    = 32 // argv token 超过此长度且无空白 → 打码
	mcpURLQueryRedactMinLen = 24 // URL query 值超过此长度 → 打码
)

// redactMCPToken 把疑似密钥形态的 argv token 打码——宁可错杀:MCP args 里
// 带 API key 是常态,只有「短」或「含空白(自然语句)」才放行。
func redactMCPToken(token string) string {
	if len(token) > mcpTokenRedactMinLen && !strings.ContainsAny(token, " \t") {
		return fmt.Sprintf("<redacted:%d>", len(token))
	}
	return token
}

// redactMCPCommand 把 argv join 成命令行,逐 token 脱敏,整体截 512(按字符)。
func redactMCPCommand(argv []string) string {
	parts := make([]string, 0, len(argv))
	for _, arg := range argv {
		parts = append(parts, redactMCPToken(arg))
	}
	return truncateAgentText(strings.Join(parts, " "), mcpCommandMaxLen)
}

// redactMCPURL 打码 query 中超过阈值长度的值(键保留);userinfo 一律打码、
// fragment 一律丢弃——非 query 形态的凭据同样不得外泄;整体截 512(按字符)。
func redactMCPURL(raw string) string {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || (u.RawQuery == "" && u.User == nil && u.Fragment == "") {
		return truncateAgentText(raw, mcpURLMaxLen)
	}
	if u.RawQuery != "" {
		q := u.Query()
		for key, vals := range q {
			for i, v := range vals {
				if len(v) > mcpURLQueryRedactMinLen {
					vals[i] = "<redacted>"
				}
			}
			q[key] = vals
		}
		u.RawQuery = q.Encode()
	}
	if u.User != nil {
		u.User = url.User("<redacted>")
	}
	u.Fragment = ""
	u.RawFragment = ""
	return truncateAgentText(u.String(), mcpURLMaxLen)
}

// truncateMCPName 把 MCP 服务器名称截到 64 字符(rune 安全,复用 truncateAgentText)。
func truncateMCPName(name string) string {
	return truncateAgentText(name, mcpNameMaxLen)
}

// agentMCPServersFromClaudeRaw 把 claude 形态的原始 mcpServers 条目转成
// 脱敏摘要。名字按字典序输出(确定性);非法/缺字段的条目逐条丢弃;
// 未知 transport 丢弃(fail-closed);数量截 mcpServerMaxCount。
func agentMCPServersFromClaudeRaw(named map[string]interface{}, source string) []models.AgentMCPServer {
	names := make([]string, 0, len(named))
	for name := range named {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]models.AgentMCPServer, 0, len(names))
	for _, name := range names {
		if len(out) >= mcpServerMaxCount {
			break
		}
		v, ok := named[name].(map[string]interface{})
		if !ok {
			continue
		}
		nm := truncateMCPName(name)
		if nm == "" {
			continue // 全空白长名经 TrimSpace 截断后为空:跳过(服务端 schema min(1) 会拒掉整个条目)
		}
		typ, _ := v["type"].(string)
		if typ == "" {
			typ = "stdio"
		}
		switch typ {
		case "stdio":
			command, _ := v["command"].(string)
			if command == "" {
				continue
			}
			argv := []string{command}
			if rawArgs, ok := v["args"].([]interface{}); ok {
				for _, a := range rawArgs {
					if s, ok := a.(string); ok {
						argv = append(argv, s)
					}
				}
			}
			out = append(out, models.AgentMCPServer{
				Name: nm, Transport: "stdio",
				Command: redactMCPCommand(argv), Source: source,
			})
		case "http", "sse":
			u, _ := v["url"].(string)
			if u == "" {
				continue
			}
			out = append(out, models.AgentMCPServer{
				Name: nm, Transport: typ,
				URL: redactMCPURL(u), Source: source,
			})
		default:
			continue // 未知 transport:宁可少报(fail-closed)
		}
	}
	return out
}

// agentMCPFileStamp 是缓存失效判定的文件指纹。零值 comparable,直接 == 比较。
// (Task 6 将在其上扩展清单缓存;此处先落地最小形态。)
type agentMCPFileStamp struct {
	path    string
	exists  bool
	modTime time.Time
	size    int64
}

// stampAgentMCPFile 采集单个文件的指纹;缺失文件的 stamp 仅含 path(exists=false)。
func stampAgentMCPFile(path string) agentMCPFileStamp {
	if st, err := os.Stat(path); err == nil {
		return agentMCPFileStamp{path: path, exists: true, modTime: st.ModTime(), size: st.Size()}
	}
	return agentMCPFileStamp{path: path}
}

// claudeMCPServerInventoryIn 采集 <home> 下的 Claude 设备级 MCP 清单:
// <home>/.claude.json 顶层 mcpServers(source=user)+ 已启用插件 MCP
// (source=plugin)。项目级 .mcp.json 不采集(随 cwd 变化,非设备能力)。
// 所有读取以传入 home 为根,不回落 agentHome()。
//
// 两态返回契约:stamps 恒非 nil(= 本次读过/统计过的全部文件指纹,
// 任一文件从缺失变存在或内容变更即缓存失效);清单为空时 out 为 nil
// 但 stamps 照交——否则新机器(检测到 claude 却从未跑过)会把空清单
// 永久缓存,用户后来创建 ~/.claude.json 也刷不出来。nil stamps 仅可能
// 出现在上游拦截路径(home 为空时本函数不应被调用)。
func claudeMCPServerInventoryIn(home string) ([]models.AgentMCPServer, []agentMCPFileStamp) {
	claudeJSON := filepath.Join(home, ".claude.json")
	settingsJSON := filepath.Join(home, ".claude", "settings.json")
	registryJSON := filepath.Join(home, ".claude", "plugins", "installed_plugins.json")
	claudeStamp := stampAgentMCPFile(claudeJSON)
	settingsStamp := stampAgentMCPFile(settingsJSON)
	registryStamp := stampAgentMCPFile(registryJSON)
	stamps := []agentMCPFileStamp{claudeStamp, settingsStamp, registryStamp}
	// 无任何 claude 配置:清单为空,但 stamps 必须照交——缓存据此感知
	// 「配置后来出现」(任一文件从缺失变存在即失效)。
	if !claudeStamp.exists && !registryStamp.exists {
		return nil, stamps
	}
	userRaw := map[string]interface{}{}
	_ = readMCPServersFile(claudeJSON, userRaw)
	pluginRaw := map[string]interface{}{}
	for _, inst := range enabledUserScopePluginInstallsIn(home) {
		declPath := filepath.Join(inst.installPath, ".mcp.json")
		stamps = append(stamps, stampAgentMCPFile(declPath))
		readPluginMCPDeclaration(inst.installPath, inst.pluginName, pluginRaw)
	}
	out := agentMCPServersFromClaudeRaw(userRaw, "user")
	out = append(out, agentMCPServersFromClaudeRaw(pluginRaw, "plugin")...)
	if len(out) > mcpServerMaxCount {
		out = out[:mcpServerMaxCount]
	}
	if len(out) == 0 {
		return nil, stamps
	}
	return out, stamps
}
