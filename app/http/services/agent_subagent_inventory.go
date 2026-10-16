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
// 上限 subagentMaxCount 截断;目录缺失/为空返回 nil(载荷键省略语义)。
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
	if len(out) == 0 {
		return nil
	}
	return out
}

// collectUserSubagents 解析 ~/.claude/agents(用户域,fail-soft)。
func collectUserSubagents() []models.AgentSubagent {
	return collectSubagentsFromDir(filepath.Join(agentHome(), ".claude", "agents"))
}
