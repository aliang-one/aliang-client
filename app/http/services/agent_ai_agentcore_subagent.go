package services

// AliangCode（agentcore）子 agent：定义采集（~/.claude/agents，CC 格式）+ 桥。
// 设计文档 docs/superpowers/specs/2026-10-27-agentcore-subagents-design.md。
//
// 分层与 MCP 桥同款：内核零改动，宿主把每个定义包成 agentcore.Tool，
// Execute 递归 agentcore.Run 子循环（depth=1：子 registry 不含子 agent 桥）。

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"aliang.one/nursorgate/app/agentcore"
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

// agentCoreSubagentBridge 把一个子 agent 定义包成内核 agentcore.Tool。
type agentCoreSubagentBridge struct {
	client         *agentcore.Client
	mainModel      string // 主 run 模型（兜底）
	defModel       string // frontmatter model（策略见 agentCoreSubagentModel）
	name           string // 已消解工具名
	description    string
	body           string // 定义正文（子 run 指令）
	projectPath    string
	toolsWhitelist []string
	env            *agentcore.ToolEnv // 主 run 的 ToolEnv（围栏/shell 策略共用）
	approve        func(context.Context, agentcore.ApprovalRequest) (agentcore.ApprovalResponse, error)
	logf           func(string, ...interface{})
	// mcpTools：主 run 已建连的 mcp__ 桥工厂（子 run 继承同一 session，零重复建连）。
	// nil=无 MCP。
	mcpTools func() []agentcore.Tool
	// onToolCall：journal 回调——Task 调用本身与（经装饰器的）子 run 内置工具
	// 调用都会触发；nil=不记。
	onToolCall func(tool, summary string, isErr bool)
}

func (b *agentCoreSubagentBridge) Name() string        { return b.name }
func (b *agentCoreSubagentBridge) Description() string { return b.description }
func (b *agentCoreSubagentBridge) InputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"task": map[string]interface{}{"type": "string", "description": "交给该子 agent 的任务描述"},
		},
		"required": []interface{}{"task"},
	}
}

// agentCoreSubagentModel 模型策略（spec §6）：定义 model 空/等于主模型 → 沿用；
// glm-* → 采用；其余（claude 系）→ Warn 并回落主模型（网关只路由 glm-*）。
//
// 前缀判断用精确小写字面：网关只认小写 glm-*，"GLM-5.2" 原样发上游只会换来
// model_not_found，不如回落主模型。Warn 频次=每次 Task 调用一条（模型策略在
// Execute 里逐次求值），非每 run 一次。
func agentCoreSubagentModel(defModel, mainModel string) string {
	defModel = strings.TrimSpace(defModel)
	if defModel == "" || defModel == mainModel {
		return mainModel
	}
	if strings.HasPrefix(defModel, "glm-") {
		return defModel
	}
	logger.Warn(fmt.Sprintf("agentcore-subagent: 定义 model %q 非 glm-*，忽略回落主模型 %s", defModel, mainModel))
	return mainModel
}

// subRegistry 组装子 run 工具表：内置 8（frontmatter 白名单生效，大写规范名
// 精确匹配，白名单未知名忽略+Warn）+ MCP 桥（自带 onCall 无需装饰）；
// **不含任何子 agent 桥（depth=1 结构保证）**。
func (b *agentCoreSubagentBridge) subRegistry(mcpTools []agentcore.Tool, journal bool) *agentcore.Registry {
	base := agentcore.DefaultRegistry()
	registry := agentcore.NewRegistry()
	kernelNames := []string{"Read", "Write", "Edit", "Bash", "PowerShell", "Grep", "Glob", "LS"}
	for _, name := range kernelNames {
		tool, ok := base.Get(name)
		if !ok {
			continue
		}
		if len(b.toolsWhitelist) > 0 && !containsString(b.toolsWhitelist, name) {
			continue
		}
		if journal && b.onToolCall != nil {
			tool = &agentCoreJournalToolDecorator{inner: tool, onCall: b.onToolCall}
		}
		registry.Register(tool)
	}
	for _, w := range b.toolsWhitelist {
		if !containsString(kernelNames, w) {
			logger.Warn(fmt.Sprintf("agentcore-subagent: 白名单未知名忽略 name=%s", w))
		}
	}
	for _, tool := range mcpTools {
		registry.Register(tool)
	}
	return registry
}

// agentCoreJournalToolDecorator 包装内核工具，把每次调用记 journal tool 行
// （子 run 内置工具的轨迹；MCP 桥自带 onCall 不需要装饰）。
type agentCoreJournalToolDecorator struct {
	inner  agentcore.Tool
	onCall func(tool, summary string, isErr bool)
}

func (d *agentCoreJournalToolDecorator) Name() string        { return d.inner.Name() }
func (d *agentCoreJournalToolDecorator) Description() string { return d.inner.Description() }
func (d *agentCoreJournalToolDecorator) InputSchema() map[string]interface{} {
	return d.inner.InputSchema()
}
func (d *agentCoreJournalToolDecorator) Execute(ctx context.Context, input map[string]interface{}, env *agentcore.ToolEnv) (result agentcore.ToolResult) {
	summary := summarizeAgentCoreMCPInput(input)
	defer func() {
		if rec := recover(); rec != nil {
			result = agentcore.ToolResult{Content: fmt.Sprintf("工具执行崩溃（已兜底）: %v", rec), IsError: true}
			safeOnToolCall(d.onCall, d.inner.Name(), summary, true)
		}
	}()
	res := d.inner.Execute(ctx, input, env)
	safeOnToolCall(d.onCall, d.inner.Name(), summary, res.IsError)
	return res
}

// safeOnToolCall journal 回调的 panic 不许二次炸、也不许搅了已定的工具结果
// （与 MCP 桥 safeOnCall 同款兜底）；Task 行与装饰器路径统一走它。
func safeOnToolCall(onCall func(tool, summary string, isErr bool), tool, summary string, isErr bool) {
	if onCall == nil {
		return
	}
	defer func() { _ = recover() }()
	onCall(tool, summary, isErr)
}

// Execute：递归子 run。ctx 继承（内核单工具 10 分钟上限罩住整个子循环）。
// 子 Config 显式置零：Emit=nil（子 run 静默，防帧交错进父管道）、Hooks=nil
// （防窃主 run 插话队列）——spec §3 钉死，勿照抄父 Config。
func (b *agentCoreSubagentBridge) Execute(ctx context.Context, input map[string]interface{}, _ *agentcore.ToolEnv) (result agentcore.ToolResult) {
	task, _ := input["task"].(string)
	if strings.TrimSpace(task) == "" {
		return agentcore.ToolResult{Content: "task 参数必填", IsError: true}
	}
	var mcpTools []agentcore.Tool
	if b.mcpTools != nil {
		mcpTools = b.mcpTools()
	}
	cfg := &agentcore.Config{
		Client:  b.client,
		Model:   agentCoreSubagentModel(b.defModel, b.mainModel),
		System:  agentCoreSystemPrompt(b.projectPath) + "\n\n" + b.body,
		Tools:   b.subRegistry(mcpTools, true),
		Env:     b.env,
		Approve: b.approve,
		Logf:    b.logf,
		// Emit/Hooks 刻意 nil（spec §3）。
	}
	res, err := agentcore.Run(ctx, cfg, []agentcore.Message{{Role: "user", Content: task}})
	isErr := false
	text := ""
	if err != nil {
		isErr, text = true, "子 agent 执行失败: "+err.Error()
	} else if res != nil && res.IsError {
		isErr, text = true, "子 agent 内部错误: "+res.Text
	} else if res != nil {
		text = res.Text
	}
	// Task 调用本身也记 tool 行（spec §7：Task 调用与子 run 内部调用都记）。
	// 时序：这行在子 run 结束后才落（完成时刻），子 run 内部工具行（装饰器）
	// 在先——与 MCP 桥同一机制，journal 里父行永远后于子行。
	safeOnToolCall(b.onToolCall, b.name, summarizeAgentCoreMCPInput(map[string]interface{}{"task": task}), isErr)
	return agentcore.ToolResult{Content: truncateAgentCoreMCPOutput(text), IsError: isErr}
}
