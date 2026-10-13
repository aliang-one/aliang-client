package services

// agent_mcp_inventory.go 采集设备级 MCP 服务器清单(claude/codex),脱敏后
// 挂到 AgentTool.MCPServers 随 tools[] 上报(spec:
// docs/superpowers/specs/2026-10-13-mcp-inventory-report-design.md)。
// env 值在任何路径都不进入返回结果;解析失败 fail-soft 返回 nil。

import (
	"fmt"
	"net/url"
	"strings"
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
