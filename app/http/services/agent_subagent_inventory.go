package services

// agent_subagent_inventory.go 采集用户域自定义 subagent 定义(~/.claude/agents
///*.md frontmatter),截断后经 hello `subagents` 上报——设备级能力清单。项目域
// .claude/agents 随 cwd 变化,与项目级 .mcp.json 同理不采集(MCP 清单归
// agent_mcp_inventory.go)。fail-soft:目录缺失/单文件读取失败逐条跳过,绝不
// 阻塞 hello;每次 hello 直读(小目录、60s 周期,不值得建缓存)。description
// 是用户手写 free text,截断按 rune 复用 truncateAgentText。

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"aliang.one/nursorgate/app/http/models"
)

const (
	subagentNameMaxLen = 64
	subagentDescMaxLen = 200
	subagentMaxCount   = 24
	subagentSource     = "user"
)

// parseSubagentFrontmatter 从 markdown frontmatter 提取 name/description 首行;
// name 缺省回落文件名(去 .md)。frontmatter 之外的正文不读。
func parseSubagentFrontmatter(path, content string) (models.AgentSubagent, bool) {
	sub := models.AgentSubagent{
		Name:   truncateAgentText(strings.TrimSuffix(filepath.Base(path), ".md"), subagentNameMaxLen),
		Source: subagentSource,
	}
	rest, ok := strings.CutPrefix(content, "---")
	if ok {
		block, _, found := strings.Cut(rest, "\n---")
		if found {
			for _, line := range strings.Split(block, "\n") {
				key, value, has := strings.Cut(strings.TrimSpace(line), ":")
				if !has {
					continue
				}
				v := strings.Trim(strings.TrimSpace(value), `"'`)
				if v == "" {
					continue
				}
				switch strings.TrimSpace(key) {
				case "name":
					sub.Name = truncateAgentText(v, subagentNameMaxLen)
				case "description":
					sub.Description = truncateAgentText(v, subagentDescMaxLen)
				}
			}
		}
	}
	if strings.TrimSpace(sub.Name) == "" {
		return models.AgentSubagent{}, false
	}
	return sub, true
}

// collectSubagentsFromDir 枚举目录下 *.md(不递归),文件名字母序确定性排序,
// 上限 subagentMaxCount 截断。三态契约:目录缺失/不可读 → nil(hello 载荷
// subagents:null,服务端视为未上报、保持旧值——非权威状态不得清空);目录
// 存在但无有效定义 → 非 nil 空切片(载荷 [],服务端清空——「清空」与
// 「未上报」由此区分,2026-10-16 服务端 normalizeAgentSubagents 同步定约)。
func collectSubagentsFromDir(dir string) []models.AgentSubagent {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	out := make([]models.AgentSubagent, 0, len(names))
	for _, name := range names {
		if len(out) >= subagentMaxCount {
			break
		}
		path := filepath.Join(dir, name)
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		sub, ok := parseSubagentFrontmatter(path, string(raw))
		if !ok {
			continue
		}
		out = append(out, sub)
	}
	return out
}

// subagentInventoryHome 是 collectUserSubagents 取 home 的注入点(测试替换)。
// 测试 swap 期间不得有后台 hello 调用方;包内测试禁 t.Parallel(同
// mcpInventoryHome 约定,见 agent_terminal_detach_test.go 说明)。
var subagentInventoryHome = agentHome

// collectUserSubagents 解析 ~/.claude/agents(用户域,fail-soft);home 不可得
// 返回 nil(载荷 null→服务端保持旧值)——否则 filepath.Join("", …) 退化为
// cwd 相对路径,进程 cwd 里恰有空 .claude/agents 目录时,非权威空清单会被
// 当「显式清空」上报、抹掉云端既有数据(对齐 MCP 侧 agentMCPServersFor 的
// 同款设防)。
func collectUserSubagents() []models.AgentSubagent {
	home := subagentInventoryHome()
	if home == "" {
		return nil
	}
	return collectSubagentsFromDir(filepath.Join(home, ".claude", "agents"))
}
