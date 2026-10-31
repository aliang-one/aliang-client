package services

// 「MCP 与技能」只读聚合（2026-10-30）：给仪表盘设置页提供一份设备侧
// MCP server 与 skill 的清单，并标注每一项对 **AliangCode 内核是否真的生效**。
//
// 为什么需要这层：本仓此前只有三条单向链路——①agent_mcp_inventory.go 把
// claude/codex 的 MCP 配置脱敏后随 tools[] 上报（无 HTTP 面、无页面）；
// ②agent_ai_agentcore_mcp.go 把同一批配置注入内核当工具用；③skill 只在
// claude CLI 侧有（slash 枚举）。用户看不到「我配的 MCP 到底有没有被
// AliangCode 用上」。本文件只做聚合与判定，绝不写任何配置文件。
//
// 数据源全部是「读别人的文件」：
//   - MCP: ~/.claude.json 顶层 mcpServers（source=user）、已启用插件的
//     .mcp.json（source=plugin）、~/.codex/config.toml [mcp_servers.*]
//     （source=codex）；脱敏由 agent_mcp_inventory.go 既有函数负责。
//   - Skill: ~/.claude/skills/*/SKILL.md（scope=user）、已启用插件的
//     skills/*/SKILL.md（scope=plugin）。
//
// 已知边界（写进 Notes 让用户看见，而不是静默）：
//   - 内核 MCP 上限 24 server / 128 工具；带自定义 headers 的 http/sse 条目
//     v1 直接跳过（agent_ai_agentcore_mcp.go 的同名限制）。
//   - codex 的 MCP 配置**不进内核**（内核只读 claude 侧），因此 codex 条目
//     恒为 not_collected。
//   - agentcore 内核没有 skill 能力（app/agentcore/tools.go 只注册内置 8 个
//     工具、system prompt 无 skill 清单），所以每条 skill 标 kernel_supported=false。
//   - project 域 skill 需要具体项目路径，本页无项目上下文，不列入。

import (
	"path/filepath"
	"sort"
	"strings"

	"aliang.one/nursorgate/app/http/models"

	"aliang.one/nursorgate/processor/config"
)

// MCP 内核注入态。
const (
	// agentIntegrationKernelInjected 内核会把这个 server 注入成工具。
	agentIntegrationKernelInjected = "injected"
	// agentIntegrationKernelSkippedHeaders http/sse 带自定义 headers —— v1 跳过。
	agentIntegrationKernelSkippedHeaders = "skipped_headers"
	// agentIntegrationKernelNotCollected 不在内核采集面内（如 codex 配置）。
	agentIntegrationKernelNotCollected = "not_collected"
)

// AgentIntegrationMCPServer 是一条 MCP server 的只读视图（脱敏）。
type AgentIntegrationMCPServer struct {
	Name string `json:"name"`
	// Transport 传输形态：stdio | http | sse。
	Transport string `json:"transport"`
	// Source 配置来源：user | plugin | codex（多来源合并时逗号分隔）。
	Source string `json:"source"`
	// Command stdio 形态的命令行（可疑 token 已打码）。
	Command string `json:"command,omitempty"`
	// URL http/sse 形态的端点（可疑 query 已打码）。
	URL string `json:"url,omitempty"`
	// KernelState 内核注入态：injected | skipped_headers | not_collected。
	KernelState string `json:"kernel_state"`
	// ToolPrefix 该 server 的工具名前缀（mcp__<name>__）；工具数需连上才知道，
	// 本页不拨号，故不给数字。
	ToolPrefix string `json:"tool_prefix,omitempty"`
}

// AgentIntegrationSkill 是一条 skill 的只读视图。
type AgentIntegrationSkill struct {
	Name        string `json:"name"`
	Scope       string `json:"scope"` // user | plugin
	Description string `json:"description,omitempty"`
	Source      string `json:"source,omitempty"`
	// KernelSupported 恒为 false：agentcore 内核尚无 skill 能力（路线图最后一站，
	// 未开工）。字段保留是为了将来实现后能直接翻真。
	KernelSupported bool `json:"kernel_supported"`
}

// AgentIntegrationsReport 是「MCP 与技能」页的数据源。
type AgentIntegrationsReport struct {
	MCPServers []AgentIntegrationMCPServer `json:"mcp_servers"`
	Skills     []AgentIntegrationSkill     `json:"skills"`
	// Notes 是固定边界说明（上限/跳过规则/未支持项），前端直出。
	Notes []string `json:"notes,omitempty"`
}

// collectAgentIntegrations 聚合一份报告内容。home 为待扫描的家目录
// （生产传 agentHome()，测试传 t.TempDir()）。
func collectAgentIntegrations(home string) AgentIntegrationsReport {
	home = strings.TrimSpace(home)
	report := AgentIntegrationsReport{
		MCPServers: collectAgentIntegrationMCPServers(home),
		Skills:     collectAgentIntegrationSkills(home),
		Notes: []string{
			"MCP 工具由内核按需连接：本页只列配置，工具数量需连上该 server 才知道。",
			"内核上限：24 个 MCP server / 128 个工具；带自定义 headers 的 http/sse 条目当前版本会跳过。",
			"Codex 的 MCP 配置不进 AliangCode 内核（内核只读 Claude 侧配置），故恒为「未采集」。",
			"AliangCode 内核当前不支持 skill（仅内置 8 个工具），技能列表仅供查看与后续规划。",
			"项目级技能需要具体项目上下文，本页不列入。",
		},
	}
	return report
}

// collectAgentIntegrationMCPServers 合并 claude（user+plugin）与 codex 两路清单，
// 再用内核原始配置判定注入态。
func collectAgentIntegrationMCPServers(home string) []AgentIntegrationMCPServer {
	if home == "" {
		return nil
	}
	// 内核真正会注入的 server 名单（原始配置，仅用于判态，绝不外泄内容）。
	kernel := agentCoreMCPRawServersIn(home)
	kernelByName := make(map[string]agentCoreMCPRawServer, len(kernel))
	for _, raw := range kernel {
		kernelByName[raw.Name] = raw
	}

	// 按 name 归并多来源（claude user / claude plugin / codex）；同名同源的
	// 只保留一条，来源拼成逗号串。
	merged := make(map[string]*AgentIntegrationMCPServer)
	sources := make(map[string][]string)
	add := func(servers []models.AgentMCPServer) {
		for _, srv := range servers {
			name := strings.TrimSpace(srv.Name)
			if name == "" {
				continue
			}
			entry := merged[name]
			if entry == nil {
				entry = &AgentIntegrationMCPServer{
					Name:        name,
					Transport:   srv.Transport,
					Command:     srv.Command,
					URL:         srv.URL,
					ToolPrefix:  "mcp__" + name + "__",
					KernelState: agentIntegrationKernelNotCollected,
				}
				merged[name] = entry
			}
			if src := strings.TrimSpace(srv.Source); src != "" {
				sources[name] = appendUniqueString(sources[name], src)
			}
		}
	}
	// 注意：这里刻意不用 agentMCPServersFor（它内部固定读 agentHome()），
	// 而是直接调带 home 参数的底层扫描 —— 本函数接受注入的 home，测试才能
	// 用 t.TempDir 隔离（否则会扫真机家目录）。生产入口传的就是 agentHome()，
	// 两条路径读同一批文件、共用同一份 (home+toolID) 解析缓存。
	add(cachedAgentMCPServers(home, "claude", claudeMCPServerInventoryIn))
	add(cachedAgentMCPServers(home, "codex", codexMCPServerInventoryIn))

	out := make([]AgentIntegrationMCPServer, 0, len(merged))
	for name, entry := range merged {
		entry.Source = strings.Join(sources[name], ",")
		if raw, ok := kernelByName[name]; ok {
			// 内核采集到了：http/sse 且带自定义 headers 的会被 v1 跳过
			// （agent_ai_agentcore_mcp.go 的同名限制），其余视为已注入。
			isHTTP := strings.EqualFold(strings.TrimSpace(raw.Type), "http") ||
				strings.EqualFold(strings.TrimSpace(raw.Type), "sse") ||
				strings.TrimSpace(raw.URL) != ""
			if isHTTP && len(raw.Headers) > 0 {
				entry.KernelState = agentIntegrationKernelSkippedHeaders
			} else {
				entry.KernelState = agentIntegrationKernelInjected
			}
		}
		out = append(out, *entry)
	}
	// 稳定顺序：注入的在前，其次按名称。
	sort.Slice(out, func(i, j int) bool {
		ri := kernelStateRank(out[i].KernelState)
		rj := kernelStateRank(out[j].KernelState)
		if ri != rj {
			return ri < rj
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// collectAgentIntegrationSkills 扫 user 与 plugin 两域 skill。
func collectAgentIntegrationSkills(home string) []AgentIntegrationSkill {
	if home == "" {
		return nil
	}
	homeClaude := filepath.Join(home, ".claude")
	out := make([]AgentIntegrationSkill, 0, 8)
	appendSkills := func(entries []map[string]interface{}) {
		for _, entry := range entries {
			name := strings.TrimSpace(mapString(entry, "name"))
			if name == "" {
				continue
			}
			out = append(out, AgentIntegrationSkill{
				Name:        name,
				Scope:       strings.TrimSpace(mapString(entry, "scope")),
				Description: strings.TrimSpace(mapString(entry, "description")),
				Source:      strings.TrimSpace(mapString(entry, "source")),
				// 2026-10-31：内核已支持技能注入（agent_ai_agentcore_skills.go），
				// 这里改为反映全局开关——关掉总闸时仍标「未生效」，与内核实际
				// 行为一致，避免面板说支持而内核不注入。
				KernelSupported: config.GetGlobalConfig().EffectiveAgentCoreSkillsEnabled(),
			})
		}
	}
	// user 域：~/.claude/skills/*/SKILL.md（与 collectUserSlashCommands 同源调用）
	appendSkills(scanSkillMarkdowns(filepath.Join(homeClaude, "skills"), "user", "user", homeClaude, "", "claude"))
	// plugin 域：已启用插件的 skills/*/SKILL.md
	for _, inst := range enabledUserScopePluginInstallsIn(home) {
		installPath := strings.TrimSpace(inst.installPath)
		if installPath == "" {
			continue
		}
		prefix := ""
		if strings.TrimSpace(inst.pluginName) != "" {
			prefix = inst.pluginName + ":"
		}
		appendSkills(scanSkillMarkdowns(filepath.Join(installPath, "skills"), "plugin", "plugin", installPath, prefix, "claude"))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Scope != out[j].Scope {
			return out[i].Scope < out[j].Scope
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func kernelStateRank(state string) int {
	switch state {
	case agentIntegrationKernelInjected:
		return 0
	case agentIntegrationKernelSkippedHeaders:
		return 1
	default:
		return 2
	}
}

func appendUniqueString(list []string, value string) []string {
	for _, existing := range list {
		if existing == value {
			return list
		}
	}
	return append(list, value)
}

func mapString(m map[string]interface{}, key string) string {
	if m == nil {
		return ""
	}
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

// AgentIntegrations 返回「MCP 与技能」页的只读报告（服务入口）。
func (s *AgentService) AgentIntegrations() AgentIntegrationsReport {
	return collectAgentIntegrations(agentHome())
}
