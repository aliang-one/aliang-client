package services

// AliangCode（agentcore）子 agent：定义采集（~/.claude/agents，CC 格式）+ 桥。
// 设计文档 docs/superpowers/specs/2026-10-27-agentcore-subagents-design.md。
//
// 分层与 MCP 桥同款：内核零改动，宿主把每个定义包成 agentcore.Tool，
// Execute 递归 agentcore.Run 子循环（depth=1：子 registry 不含子 agent 桥）。

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"aliang.one/nursorgate/common/logger"
)

const (
	agentCoreSubagentCap     = 24
	agentCoreSubagentDescCap = 500
	agentCoreSubagentBodyCap = 8 * 1024
)

// agentCoreSubagentDef 是一条子 agent 定义。
type agentCoreSubagentDef struct {
	Name           string
	Description    string
	ToolsWhitelist []string // 空=全量
	Model          string
	Body           string // frontmatter 之后的正文（子 run 指令），已截 8KB
}

// agentCoreSubagentFrontmatter 解析单文件。name 缺省回落文件名（经段白名单
// 清洗）；无 frontmatter 的文件不跳过（沿用设备清单语义），全文即指令正文。
func agentCoreSubagentFrontmatter(path, content string) (agentCoreSubagentDef, bool) {
	def := agentCoreSubagentDef{
		Name: sanitizeAgentCoreMCPSegment(strings.TrimSuffix(filepath.Base(path), ".md")),
	}
	hasFrontmatter := false
	rest, ok := strings.CutPrefix(content, "---")
	if ok {
		block, tail, found := strings.Cut(rest, "\n---")
		if found {
			hasFrontmatter = true
			toolsDeclared := false
			for _, line := range strings.Split(block, "\n") {
				key, value, has := strings.Cut(strings.TrimSpace(line), ":")
				if !has {
					continue
				}
				key = strings.TrimSpace(key)
				v := strings.Trim(strings.TrimSpace(value), `"'`)
				if key == "tools" {
					toolsDeclared = true
				}
				if v == "" {
					continue
				}
				switch key {
				case "name":
					def.Name = sanitizeAgentCoreMCPSegment(v)
				case "description":
					def.Description = truncateAgentText(v, agentCoreSubagentDescCap)
				case "model":
					def.Model = v
				case "tools":
					for _, t := range strings.Split(v, ",") {
						if t = strings.TrimSpace(t); t != "" {
							def.ToolsWhitelist = append(def.ToolsWhitelist, t)
						}
					}
				}
			}
			// 行式解析只支持逗号标量（tools: Read, Grep）。YAML 块列表
			// （tools:\n  - Read）与流式（tools: [Read]）会解析出空白名单，
			// 此时语义是「全量放行」——用户写限制却拿到无限制，必须留痕。
			if toolsDeclared && len(def.ToolsWhitelist) == 0 {
				logger.Warn(fmt.Sprintf(
					"agentcore-subagent: tools 字段未解析出白名单(仅支持逗号标量格式),按全量放行 name=%s file=%s",
					def.Name, filepath.Base(path)))
			}
			def.Body = strings.TrimSpace(tail)
		}
	}
	// 只有「确认无 frontmatter」才全文回落。有 frontmatter 但正文为空时
	// Body 保持空串——否则整块 frontmatter 会泄漏进指令文本。
	if !hasFrontmatter {
		def.Body = strings.TrimSpace(content)
	}
	// 字节截断：会在 UTF-8 多字节字符中间劈开（已知限制，与
	// truncateAgentTextBytes 全仓语义一致），Body 下游只做拼接不再解析。
	def.Body = truncateAgentTextBytes(def.Body, agentCoreSubagentBodyCap)
	// sanitize 兜底保证非空（空串回落 "srv"），此守卫实际不可达，防御性保留。
	if strings.TrimSpace(def.Name) == "" {
		return agentCoreSubagentDef{}, false
	}
	return def, true
}

// agentCoreSubagentDefsIn 采集 user 域定义（不递归、cap 24、按文件名排序、
// 畸形跳过）。目录缺失返回 nil。
func agentCoreSubagentDefsIn(home string) []agentCoreSubagentDef {
	if strings.TrimSpace(home) == "" {
		return nil
	}
	dir := filepath.Join(home, ".claude", "agents")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	var out []agentCoreSubagentDef
	for _, name := range names {
		if len(out) >= agentCoreSubagentCap {
			logger.Warn(fmt.Sprintf("agentcore-subagent: 定义超上限截断 cap=%d", agentCoreSubagentCap))
			break
		}
		path := filepath.Join(dir, name)
		content, err := os.ReadFile(path)
		if err != nil {
			// 留痕：用户会拿「为什么我的 agent 没出现」来问，静默跳过无法排查。
			logger.Warn(fmt.Sprintf("agentcore-subagent: 定义读取失败跳过 file=%s err=%v", name, err))
			continue
		}
		if def, ok := agentCoreSubagentFrontmatter(path, string(content)); ok {
			out = append(out, def)
		}
	}
	return out
}

// agentCoreSubagentBridgeName 工具名消解：避开 reserved（内核 8 规范名）与
// seen（已用名），重名追加 -2/-3。字符清洗复用 MCP 段白名单。
//
// 长度预算：段白名单截 64 + 序号后缀最多 3 字符（"-99"）= 67 < 128（API
// 工具名上限），无需再截。seen 表必须与 MCP 桥命名共用同一张：否则子
// agent 桥名可能遮蔽/撞上 mcp__xxx 桥名，T2 接线时注意传同一 map。
func agentCoreSubagentBridgeName(raw string, reserved, seen map[string]bool) string {
	base := sanitizeAgentCoreMCPSegment(raw)
	candidate := base
	for i := 2; reserved[candidate] || seen[candidate]; i++ {
		candidate = fmt.Sprintf("%s-%d", base, i)
	}
	seen[candidate] = true
	return candidate
}
