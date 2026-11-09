package services

// AliangCode（agentcore）子 agent：定义采集（~/.claude/agents，CC 格式）+ 桥。
// 设计文档 docs/superpowers/specs/2026-10-27-agentcore-subagents-design.md。
//
// 分层与 MCP 桥同款：内核零改动，宿主把每个定义包成 agentcore.Tool，
// Execute 递归 agentcore.Run 子循环（depth=1：子 registry 不含子 agent 桥）。

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"aliang.one/nursorgate/app/agentcore"
	"aliang.one/nursorgate/common/logger"
	"aliang.one/nursorgate/processor/config"
)

const (
	agentCoreSubagentCap     = 24
	agentCoreSubagentDescCap = 500
	agentCoreSubagentBodyCap = 8 * 1024
	// agentCoreSubagentTaskCap 是 task 入参的字节封顶（rune 安全截断）。超长
	// task 会原样进子 run 首轮请求，上游可能直接 400（2026-11-05 审计盲点 5：
	// 此前只有 TrimSpace 空校验，无上限）。64KB 与 MCP 输出截断同量级。
	agentCoreSubagentTaskCap = 64 * 1024
)

// agentCoreSubagentToolCeiling 解析子 agent 桥的内核执行窗口（TimeoutHint）。
// 与 claude 侧子 agent 看门狗共用同一把旋钮 ALIANG_AI_SUBAGENT_WAIT_CEILING
// （默认 6h）：两个内核对「子 agent 最多跑多久」应该是一个语义。显式 "0"
// （关）不产生无上限——回落默认值，内核窗口必须存在（防挂死子 run 拖死父
// run），只是放宽到 6h 档。
func agentCoreSubagentToolCeiling() time.Duration {
	if d := resolveEnvDurationAllowZero("ALIANG_AI_SUBAGENT_WAIT_CEILING", agentAISubagentWaitCeiling); d > 0 {
		return d
	}
	return agentAISubagentWaitCeiling
}

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
// 工具名上限），无需再截。
//
// 与 MCP 桥名空间的隔离：T1 曾设想 seen 表与 MCP 桥命名共用一张，接线时
// 证实不可行——agentCoreMCPServerToolDefs 在自己内部建 seen 且签名不外露。
// 实际防线是「mcp__ 前缀剥除」（见 agentCoreSubagentTools）：MCP 桥名恒为
// mcp__ 前缀，子 agent 原名剥掉 mcp__ 前缀后再消解，两个名空间不可能相交。
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
	// peerTools：设备互访工具工厂（peer.exec/peer.fetch_file/device_list/
	// device_info/device_note 桥）。nil=不注入。与 MCP 桥同款继承语义——主
	// run 能跨设备，子 agent 也应能（spec 缺口修补：此前子 registry 只装
	// 内置+MCP,主 agent 把跨设备任务 delegate 给子 agent 时子 agent 无从
	// 执行）。桥自带 journal 闭包(绑主 run session),复用实例零额外建连。
	peerTools func() []agentcore.Tool
	// onToolCall：journal 回调——Task 调用本身与（经装饰器的）子 run 内置工具
	// 调用都会触发；nil=不记。
	onToolCall func(tool, summary string, isErr bool)
	// contextWindow：主 run 的上下文窗口。子 run 与主 run 用同一模型、同一
	// 上游配额，窗口必须一致——否则子 run 按 200k 兜底压缩，大窗口会话里
	// 子 agent 反而先撞 prompt too long。
	contextWindow int
	// toolCeiling 是内核单工具窗口的覆盖值（TimeoutHint，2026-11-05 审计
	// 盲点 1）。桥的 Execute 是一整段子 run：默认 10 分钟窗口会把子 run 里
	// 等手机审批（SLA 24h）连坐掐死。取值见 agentCoreSubagentToolCeiling。
	toolCeiling time.Duration
	// skillsBlock 是主 run 同款的技能清单块（agentCoreSkillsPromptBlock 产
	// 出，装配期求值一次；2026-11-05 审计盲点 3——此前只注主 run System，
	// 子 registry 明明有 Read，渐进披露对子 agent 却失效）。空串=无副作用。
	skillsBlock string
	// mirror 是子 run 状态镜像（ai.subagent started/progress/completed|failed，
	// 2026-11-07：手机端此前对 agentcore 派发的子 agent 全程无感知）。nil=不发
	// （宿主未接线/既有测试路径），完全无感。
	mirror *agentCoreSubagentMirror
}

// TimeoutHint 覆盖内核默认单工具窗口（agentcore.TimeoutHint）。
func (b *agentCoreSubagentBridge) TimeoutHint() time.Duration { return b.toolCeiling }

// Parallelizable 声明桥可并发（agentcore.Parallelizable）：子 run 各自独立
// 组装 registry/Config，互享状态只有 client（并发安全）/env（只读）/审批闭包
// （agentCoreSubagentTools 已互斥化）/journal（内部带锁）。内核据此把同回合
// 的多个子 agent 并发跑，对齐 claude Task 原生并行。
func (b *agentCoreSubagentBridge) Parallelizable() bool { return true }

// subSystem 组装子 run System prompt：基础提示 + 技能清单 + 定义正文。
// skillsBlock 为空时与历史拼接（prompt + "\n\n" + body）逐字节等价。
func (b *agentCoreSubagentBridge) subSystem() string {
	return agentCoreSystemPrompt(b.projectPath) + b.skillsBlock + "\n\n" + b.body
}

// truncateAgentCoreSubagentTask 把 task 入参封到 maxBytes 字节内（rune 安全：
// 回退到完整 UTF-8 边界，绝不送非法字节序列上游），尾部附截断标记。

// agentCoreSubagentMirror 把 agentcore 子 run 的生命周期镜像成 ai.subagent
// 协议事件——此前该通道是 claude 专属，用户在手机上派 N 个子 agent（实测
// 2026-11-06 派 3 个调研）全程无感知。复用 claude 侧同一套基座：
// run.subagents registry（cap 64/upsert）+ activity.beginSubagent/endSubagent
// （idle 暂停+watchdog subagent ceiling 对 agentcore 子 run 同样生效）+
// agentAISubagentPayload 同款帧形（手机端 SubagentRows 零改动渲染）。帧经
// emit（newAgentCoreEmit 管道，JSON+\n 按行）进 ai 事件流，不进父 delta 流
// （子 run 仍静默 Emit=nil，防帧交错约束不变）。
//
// nil 安全：桥 mirror=nil（宿主未接线/既有测试路径）完全无感。
type agentCoreSubagentMirror struct {
	registry  *agentAISubagentRegistry
	activity  *agentAIActivity
	emit      func([]byte) error
	sessionID string
	messageID string
	seq       atomic.Int64
	// journal 是主 run 的调用记录 journal（nil=不落盘，既有测试路径）。tracked
	// 子 run 的 start/finish/工具行按 task_id 关联落盘，供读侧聚合子 agent 轨迹。
	journal *agentCoreJournal
}

// start 登记 started。tracked=false 表示 registry 拒收（cap 满），后续
// progress/finish 对该 taskID 静默降级（与 claude 侧 registry 满载 fail-soft
// 同语义——轨迹丢，任务不受影响）。
func (m *agentCoreSubagentMirror) start(bridgeName, description string) (string, bool) {
	if m == nil {
		return "", false
	}
	// taskID 含回合 messageID（spec H1）：mirror 每回合新建、seq 归零——裸 id
	// 跨回合重号会让 journal/读侧聚合串线，messageID 段消歧。
	taskID := fmt.Sprintf("agentcore-%s-%s-%d", m.messageID, bridgeName, m.seq.Add(1))
	// registry 以 toolUseID 为主键且非空必填——agentcore 侧没有 CC 的
	// tool_use_id，taskID 兼任（progress/finish 经 taskAliases 解析）。
	record, ok := m.registry.start(taskID, taskID, m.messageID, description, bridgeName, 1, "", false, time.Now())
	if !ok {
		return taskID, false
	}
	if m.journal != nil {
		m.journal.appendSubagentStart(m.sessionID, taskID, bridgeName, description)
	}
	m.activity.beginSubagent()
	m.emitRecord(record)
	return taskID, true
}

// progress 由子 run 工具调用驱动（current_step/last_tool/tool_uses 进手机
// 状态行），并对齐 claude 侧语义喂 watchdog 进度钟（subagentProgress——子
// run 静默期间主 run 无 delta，不喂钟的话 no-progress 窗口会误杀长任务）。
func (m *agentCoreSubagentMirror) progress(taskID, currentStep, lastTool string, uses int) {
	if m == nil || taskID == "" {
		return
	}
	record, ok := m.registry.progress(taskID, time.Now(), agentAISubagentProgress{
		CurrentStep: currentStep,
		LastTool:    lastTool,
		ToolUses:    uses,
	})
	if !ok {
		return
	}
	m.activity.subagentProgress()
	m.emitRecord(record)
}

// finish 登记终态（completed|failed）并配平 activity 计数。summary 截到
// 200 rune——claude 侧 Summary 是短摘要，手机 SubagentRows 只渲染两行；
// 完整最终文本已经在桥的返回值里（64KB 封顶），不重复进帧。
func (m *agentCoreSubagentMirror) finish(bridgeName, taskID, phase, summary string, duration time.Duration, totalTokens int64) {
	if m == nil || taskID == "" {
		return
	}
	record, ok := m.registry.finish(taskID, phase, truncateAgentText(summary, 200), time.Now())
	if ok {
		record.TotalTokens = totalTokens
		// 测量粒度下限：极快子 run（本地回环可亚毫秒）取整后为 0，payload
		// builder 对 0 值省键——手机端就看不到耗时字段了，标 1ms 兜底。
		ms := duration.Milliseconds()
		if ms < 1 {
			ms = 1
		}
		record.DurationMs = ms
		if record.EndedAt.IsZero() {
			record.EndedAt = time.Now()
		}
		m.emitRecord(record)
		// journal finish 行与帧同生命周期（registry ok 之内）：summary 截
		// 200 rune，result 留子 run 最终文本全文（64KB rune 安全封顶）。裸
		// 字节截断会在切点劈开多字节 rune——journal 是持久化留痕，绝不能
		// 送非法字节序列。
		if m.journal != nil {
			m.journal.appendSubagentFinish(m.sessionID, taskID, phase,
				truncateAgentText(summary, 200),
				truncateAgentCoreMCPOutputRuneSafe(summary, 64*1024),
				totalTokens, ms)
		}
	}
	m.activity.endSubagent()
}

// appendToolLine 落子 agent 的工具轨迹行（kind=tool + subagent_task_id）。
// 桥 Execute 的 toolHook 在 tracked 分支用它替代原 b.onToolCall——同一工具
// 调用只落一行（带关联键），读侧按 task_id 聚合，不与主会话工具混流。
// taskID 空串（未 tracked）守卫兜底。
func (m *agentCoreSubagentMirror) appendToolLine(taskID, tool, summary string, isErr bool) {
	if m == nil || m.journal == nil || taskID == "" {
		return
	}
	m.journal.appendSubagentTool(m.sessionID, taskID, tool, summary, isErr)
}

// emitRecord 与内核帧同构：JSON+换行（宿主按行读），session_id 由
// agentAISubagentPayload 携带。
func (m *agentCoreSubagentMirror) emitRecord(record agentAISubagent) {
	if m.emit == nil {
		return
	}
	raw, err := json.Marshal(agentAISubagentPayload(m.sessionID, record))
	if err != nil {
		return
	}
	raw = append(raw, '\n')
	_ = m.emit(raw)
}
func truncateAgentCoreSubagentTask(task string, maxBytes int) string {
	if maxBytes <= 0 || len(task) <= maxBytes {
		return task
	}
	cut := task[:maxBytes]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut + fmt.Sprintf("\n\n[系统提示：任务超长，已在传输层截断——原始 %d 字节，保留前 %d 字节。请按现有内容执行，勿复述本提示]", len(task), len(cut))
}

// truncateAgentCoreMCPOutputRuneSafe 是 truncateAgentCoreSubagentTask 的去标
// 记版：字节封顶 + 回退 UTF-8 边界（复用同一 rune 回退循环），截断尾巴不带
// 提示标记。journal 的子 agent result 落盘用它——持久化留痕绝不送非法字节
// 序列（裸字节截断会把切点劈进多字节 rune，读侧解析即 U+FFFD 垃圾）。
// maxBytes<=0 或 len(s)<=maxBytes 原样返回。
func truncateAgentCoreMCPOutputRuneSafe(s string, maxBytes int) string {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s
	}
	cut := s[:maxBytes]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut
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

// subRegistry 组装子 run 工具表：内置工具（遍历 agentcore.DefaultRegistry
// 的 Defs() 派生——内核加新工具自动跟进，frontmatter 白名单生效，规范名
// 精确匹配，白名单未知名忽略+Warn）+ MCP 桥 + peer 设备互访桥（两者自带
// onCall/journal 无需装饰、不受 frontmatter 白名单过滤——白名单语义只对
// 内核内置名生效，与 MCP 既有先例一致）；**不含任何子 agent 桥（depth=1
// 结构保证）**。toolHook 非 nil 时内置工具经装饰器回调（journal+状态镜像
// 组合钩子，由 Execute 组装）；nil=不装饰。
func (b *agentCoreSubagentBridge) subRegistry(mcpTools []agentcore.Tool, toolHook func(tool, summary string, isErr bool)) *agentcore.Registry {
	base := agentcore.DefaultRegistry()
	registry := agentcore.NewRegistry()
	kernelDefs := base.Defs()
	kernelNames := make([]string, 0, len(kernelDefs))
	for _, def := range kernelDefs {
		kernelNames = append(kernelNames, def.Name)
		tool, ok := base.Get(def.Name)
		if !ok {
			continue // 防御：Defs/Get 同源理论上不可达
		}
		if len(b.toolsWhitelist) > 0 && !containsString(b.toolsWhitelist, def.Name) {
			continue
		}
		if toolHook != nil {
			tool = &agentCoreJournalToolDecorator{inner: tool, onCall: toolHook}
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
	if b.peerTools != nil {
		for _, tool := range b.peerTools() {
			registry.Register(tool)
		}
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

// Execute：递归子 run。ctx 继承（内核单工具窗口由 TimeoutHint 覆盖为
// toolCeiling，默认 6h——不再是 10 分钟，见 agentCoreSubagentToolCeiling）。
// 子 Config 显式置零：Emit=nil（子 run 静默，防帧交错进父管道）、Hooks=nil
// （防窃主 run 插话队列）——spec §3 钉死，勿照抄父 Config。
func (b *agentCoreSubagentBridge) Execute(ctx context.Context, input map[string]interface{}, _ *agentcore.ToolEnv) (result agentcore.ToolResult) {
	task, _ := input["task"].(string)
	if strings.TrimSpace(task) == "" {
		return agentcore.ToolResult{Content: "task 参数必填", IsError: true}
	}
	task = truncateAgentCoreSubagentTask(task, agentCoreSubagentTaskCap)
	var mcpTools []agentcore.Tool
	if b.mcpTools != nil {
		mcpTools = b.mcpTools()
	}
	// 状态镜像：started → progress（子 run 工具调用驱动）→ completed|failed。
	// 终态在 defer 里发（text/isErr 就绪后）。defer 只在 tracked 时挂——
	// registry 拒收（cap 满/nil）时不 finish：begin/end 配平靠 start 的
	// tracked 短路保证，孤儿 finish 行还会骗读侧「有终态无起点」。
	// journal 闭包与 progress 组成同一装饰钩子——tracked 时子 run 内置工具
	// 改走 appendToolLine（带 subagent_task_id 关联键），未 tracked 保持
	// b.onToolCall 原样（主会话普通 tool 行）。
	startedAt := time.Now()
	isErr := false
	text := ""
	var totalTokens int64
	subTaskID, tracked := b.mirror.start(b.name, b.description)
	var toolUses atomic.Int64
	if tracked {
		defer func() {
			// 相位判定先取消后错误（2026-11-09 审计）：主回合停止/超时时
			// agentcore.Run 返回的 err 就是 ctx 错，isErr 会被置 true——若先判
			// isErr 会把取消错标 failed，与 claude 侧 finalizeRunSubagents 的
			// live interrupted 口径分裂（面板历史卡 vs 实时卡语义不一致）。
			phase := "completed"
			if ctx.Err() != nil {
				phase = "interrupted"
			} else if isErr {
				phase = "failed"
			}
			b.mirror.finish(b.name, subTaskID, phase, text, time.Since(startedAt), totalTokens)
		}()
	}
	var toolHook func(tool, summary string, isErr bool)
	if tracked {
		toolHook = func(tool, summary string, isErr bool) {
			b.mirror.appendToolLine(subTaskID, tool, summary, isErr)
			n := int(toolUses.Add(1))
			b.mirror.progress(subTaskID, fmt.Sprintf("工具 #%d: %s", n, tool), tool, n)
		}
	} else if b.onToolCall != nil {
		toolHook = b.onToolCall
	}
	// model 覆盖（原生派发面 2026-11-09）：input 显式给 model 时优先于定义
	// frontmatter——派发者按任务选档（claude Task model 参数同语义）；策略
	// 仍走 agentCoreSubagentModel（仅 glm-* 生效，其余回落主模型）。
	defModel := b.defModel
	if v, ok := input["model"].(string); ok {
		if trimmed := strings.TrimSpace(v); trimmed != "" {
			defModel = trimmed
		}
	}
	cfg := &agentcore.Config{
		Client:  b.client,
		Model:   agentCoreSubagentModel(defModel, b.mainModel),
		System:  b.subSystem(),
		Tools:   b.subRegistry(mcpTools, toolHook),
		Env:     b.env,
		Approve: b.approve,
		Logf:    b.logf,
		// 与主 run 同窗口：子 run 的自动压缩阈值跟随会话设置。
		ContextWindow: b.contextWindow,
		// 与主 run 同阈值：子 agent 同样跑 loop，打转守卫口径必须一致
		// （仪表盘 customer.agentcore.max_identical_tool_turns；0 走内核默认）。
		MaxIdenticalToolTurns: config.GetGlobalConfig().EffectiveAgentCoreMaxIdenticalToolTurns(),
		// Emit/Hooks 刻意 nil（spec §3）。
	}
	res, err := agentcore.Run(ctx, cfg, []agentcore.Message{{Role: "user", Content: task}})
	if err != nil {
		isErr, text = true, "子 agent 执行失败: "+err.Error()
	} else if res != nil && res.IsError {
		isErr, text = true, "子 agent 内部错误: "+res.Text
	} else if res != nil {
		text = res.Text
		// 子 run token 用量（对照 claude 侧 progress usage.total_tokens）。
		totalTokens = int64(res.Usage.InputTokens + res.Usage.OutputTokens)
	}
	// Task 调用本身也记 tool 行（spec §7：Task 调用与子 run 内部调用都记）。
	// 时序：这行在子 run 结束后才落（完成时刻），子 run 内部工具行（装饰器）
	// 在先——与 MCP 桥同一机制，journal 里父行永远后于子行。
	safeOnToolCall(b.onToolCall, b.name, summarizeAgentCoreMCPInput(map[string]interface{}{"task": task}), isErr)
	return agentcore.ToolResult{Content: truncateAgentCoreMCPOutput(text), IsError: isErr}
}

// agentCoreSubagentReservedNames 是子 agent 桥名必须避开的内核规范名，
// 从 agentcore.DefaultRegistry().Defs() 派生（包初始化时构建一次）——内核
// 未来加第 9 个工具自动跟进，不会留下「手抄清单=旧 8 个」的漂移缺口，也就
// 不会让子 agent 定义悄悄遮蔽新内核工具。测试
// TestAgentCoreSubagentBridgeDepthOne 里的独立硬编码清单是有意的锁死
// （独立清单恰能发现派生源漂移），勿「顺手」统一。
var agentCoreSubagentReservedNames = func() map[string]bool {
	names := map[string]bool{}
	for _, def := range agentcore.DefaultRegistry().Defs() {
		names[def.Name] = true
	}
	return names
}()

// agentCoreSubagentTools 采集 user 域子 agent 定义并包成桥——runAgentCore 的
// 注入口（spec：2026-10-27-agentcore-subagents §3）。
//
// client/env/approve/logf 全部复用主 run 构造好的同一份（调用方 hoist，
// 见 runAgentCore）：子 run 与主 run 同一凭据出网、同一道围栏、同一个审批
// 闭包——审批因此贯穿（子 run 里的 mcp__ 调用同样过默认门）。
// mcpTools 是主 run 已建连的 MCP 桥，交给桥工厂做子 run 继承（同一批
// session，零重复 dial）。readOnly 判定在调用方（mirror MCP 决策）；
// 定义集为空返回 nil（不注入）。
func (m *agentAIManager) agentCoreSubagentTools(run agentAIRun,
	client *agentcore.Client, env *agentcore.ToolEnv,
	approve func(context.Context, agentcore.ApprovalRequest) (agentcore.ApprovalResponse, error),
	logf func(string, ...interface{}),
	mcpTools []agentcore.Tool,
	mirror *agentCoreSubagentMirror) []agentcore.Tool {
	router := m.agentCoreSubagentTaskTool(run, client, env, approve, logf, mcpTools, mirror)
	if router == nil {
		return nil
	}
	return []agentcore.Tool{router}
}

// agentCoreSubagentBuiltinDefs 内置默认定义（2026-11-09 vm459/vm22 事故根治）：
// 用户定义目录为空/缺名时兜底——此前无定义机器上桥=0、机制块不追加，模型既无
// 派发工具也无机制认知，「装了最新版还是没法派发」。Model 空=沿用主模型（内置
// 定义不绑定具体上游）；用户同名定义优先覆盖（合并语义见 agentCoreSubagentMergedDefs）。
var agentCoreSubagentBuiltinDefs = []agentCoreSubagentDef{
	{
		Name:        "researcher",
		Description: "调研任务——收集资料、对比方案、验证假设、输出结构化结论",
		Body:        "你是调研员。针对交给你的任务收集资料并验证假设,输出结构化调研报告(事实/依据/结论)。",
	},
}

// agentCoreSubagentMergedDefs 用户域定义 + 内置默认合并：同名用户定义优先
// （用户写了 researcher.md 就用用户的），内置只补缺。
func agentCoreSubagentMergedDefs() []agentCoreSubagentDef {
	defs := agentCoreSubagentDefsIn(agentHome())
	have := make(map[string]bool, len(defs))
	for _, d := range defs {
		have[d.Name] = true
	}
	for _, d := range agentCoreSubagentBuiltinDefs {
		if !have[d.Name] {
			defs = append(defs, d)
		}
	}
	return defs
}

// agentCoreSubagentTaskRouter 原生统一派发面（vm22 反馈 2026-11-09「多 agent
// 支持要原生的」）：claude Task 同款形态——一个 task 工具，subagent_type 选
// 角色、task 派任务、model 可选覆盖；替代此前「N 定义 N 零散工具」。每座桥
// 降为内部引擎（并行/镜像/journal/超时机器零改动），router 只做类型路由。
type agentCoreSubagentTaskRouter struct {
	description  string
	ceiling      time.Duration
	bridges      map[string]*agentCoreSubagentBridge
	types        []string // 稳定序类型名（机制块枚举 + 未知类型报错提示）
	descriptions map[string]string
}

func (r *agentCoreSubagentTaskRouter) Name() string        { return "task" }
func (r *agentCoreSubagentTaskRouter) Description() string { return r.description }
func (r *agentCoreSubagentTaskRouter) hasType(t string) bool {
	_, ok := r.bridges[t]
	return ok
}

// TimeoutHint/Parallelizable 必须在 router 上实现：内核执行的是注册进
// registry 的这个工具——单工具窗口（6h，子 run 等审批不被 10min 连坐）与
// 同回合并行批判定都看它，而不是内部桥。
func (r *agentCoreSubagentTaskRouter) TimeoutHint() time.Duration { return r.ceiling }
func (r *agentCoreSubagentTaskRouter) Parallelizable() bool       { return true }

func (r *agentCoreSubagentTaskRouter) InputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"subagent_type": map[string]interface{}{"type": "string", "description": "要派发的子 agent 类型名（见 system prompt <subagents> 类型清单）"},
			"task":          map[string]interface{}{"type": "string", "description": "交给子 agent 的任务描述（自包含——子 agent 看不到本对话）"},
			"model":         map[string]interface{}{"type": "string", "description": "可选：覆盖子 agent 模型（仅 glm-* 生效，其余回落主模型）"},
		},
		"required": []interface{}{"subagent_type", "task"},
	}
}

func (r *agentCoreSubagentTaskRouter) Execute(ctx context.Context, input map[string]interface{}, env *agentcore.ToolEnv) agentcore.ToolResult {
	typ, _ := input["subagent_type"].(string)
	typ = strings.TrimSpace(typ)
	if typ == "" {
		return agentcore.ToolResult{Content: "subagent_type 必填。可用类型: " + strings.Join(r.types, ", "), IsError: true}
	}
	b, ok := r.bridges[typ]
	if !ok {
		return agentcore.ToolResult{Content: fmt.Sprintf("未知子 agent 类型 %q。可用类型: %s", typ, strings.Join(r.types, ", ")), IsError: true}
	}
	// input 原样透传：桥自己校验 task 必填、读可选 model 覆盖。
	return b.Execute(ctx, input, env)
}

func (m *agentAIManager) agentCoreSubagentTaskTool(run agentAIRun,
	client *agentcore.Client, env *agentcore.ToolEnv,
	approve func(context.Context, agentcore.ApprovalRequest) (agentcore.ApprovalResponse, error),
	logf func(string, ...interface{}),
	mcpTools []agentcore.Tool,
	mirror *agentCoreSubagentMirror) *agentCoreSubagentTaskRouter {

	defs := agentCoreSubagentMergedDefs()
	if len(defs) == 0 {
		return nil
	}
	onToolCall := func(tool, summary string, isErr bool) {
		m.coreJournal.appendTool(run.sessionID, tool, summary, isErr)
	}
	// 审批串行由调用方保证：传入的 approve 必须已过 agentCoreApprovalGate
	// （runAgentCore 对 kernelApprove 统一 wrap）——主层（含对桥工具本身的
	// 审批）与子 run 内部工具审批共用同一道门，单卡语义+闭包内 run 字段
	// 无数据竞争（2026-11-06 评审 C4/C8：互斥必须在闭包源头，桥内自包会漏
	// 掉主层）。本函数不再自行包裹。
	// 技能清单块装配期求值一次（与主 run 同源同开关；空串=无副作用）。
	skillsBlock := m.agentCoreSkillsPromptBlock(run)
	var mcpFactory func() []agentcore.Tool
	if len(mcpTools) > 0 {
		mcpFactory = func() []agentcore.Tool { return mcpTools }
	}
	// peer 设备互访桥:与 MCP 桥同款工厂继承。此处自建(agentCorePeerTools 只
	// 依赖 m 与 run,调用方 runAgentCore 无需改签名)——readOnly 判定由调用方
	// 的注入闸保证(子 agent 桥只在 !run.readOnly 时注入,peer 桥随之天然只
	// 出现在非 readOnly run 的子 agent 里)。
	var peerFactory func() []agentcore.Tool
	if peerTools := m.agentCorePeerTools(run); len(peerTools) > 0 {
		peerFactory = func() []agentcore.Tool { return peerTools }
	}
	mainModel := agentCoreModel(run)
	seen := map[string]bool{}
	bridges := make(map[string]*agentCoreSubagentBridge, len(defs))
	types := make([]string, 0, len(defs))
	descriptions := make(map[string]string, len(defs))
	for _, def := range defs {
		// mcp__ 前缀剥除（名空间隔离，见 agentCoreSubagentBridgeName 注释）：
		// 剥到不含前缀为止（"mcp__mcp__x" 也压干净），剥空则由段白名单回落
		// "srv"——绝不产出可能撞 MCP 桥名的名字。
		raw := def.Name
		for strings.HasPrefix(raw, "mcp__") {
			raw = strings.TrimPrefix(raw, "mcp__")
		}
		name := agentCoreSubagentBridgeName(raw, agentCoreSubagentReservedNames, seen)
		desc := def.Description
		if strings.TrimSpace(desc) == "" {
			// 占位引用消解后的类型名（非 def.Name）：原名以 mcp__ 开头的定义
			// 经剥除后可调用名已变，占位若引原名会诱导模型调错工具。
			desc = "子 agent " + name + "（定义未写 description）"
		}
		// 桥描述不再带机制前缀：机制自述统一上收到 task 路由器描述 +
		// <subagents> 机制块（原生派发面 2026-11-09）。桥已不可直接注册，
		// 逐桥前缀只会污染机制块的类型枚举。
		bridges[name] = &agentCoreSubagentBridge{
			client:         client,
			mainModel:      mainModel,
			defModel:       def.Model,
			name:           name,
			description:    desc,
			body:           def.Body,
			projectPath:    run.projectPath,
			toolsWhitelist: def.ToolsWhitelist,
			env:            env,
			approve:        approve,
			logf:           logf,
			mcpTools:       mcpFactory,
			peerTools:      peerFactory,
			onToolCall:     onToolCall,
			contextWindow:  run.contextWindow,
			toolCeiling:    agentCoreSubagentToolCeiling(),
			skillsBlock:    skillsBlock,
			mirror:         mirror,
		}
		types = append(types, name)
		descriptions[name] = desc
	}
	router := &agentCoreSubagentTaskRouter{
		// 机制前缀上收到统一派发面（原生自述）：模型可见面上必须宣告「这是
		// 子agent」（vm459/vm22 实测：只透传定义 description 时被问「你有
		// 子 agent 功能吗」照实答没有，也不会主动派发）。
		description: "子agent（独立内部线程，完成后报告回主对话）：派发一个子 agent 执行任务并返回其完整报告。" +
			"同回合多次调用并行执行（上限 4）。subagent_type 从 <subagents> 类型清单中选。",
		ceiling:      agentCoreSubagentToolCeiling(),
		bridges:      bridges,
		types:        types,
		descriptions: descriptions,
	}
	logger.Info(fmt.Sprintf("agentcore-subagent: 注入 task 路由器（%d 个类型）", len(types)))
	return router
}

// agentCoreSubagentMechanismSuffix 返回主 run system prompt 的子 agent 机制
// 说明块。router=nil/0 类型返回空串（零副作用拼接）。工具派发面已收成 task
// 路由器（原生形态），机制块负责：宣告并行能力与生命周期归属、点明子 agent
// 看不到主对话（task 必须自包含）、共享工作目录写冲突警告（模型感知盲区，
// 审计 2026-11-09），并逐类型枚举可用角色。
func agentCoreSubagentMechanismSuffix(router *agentCoreSubagentTaskRouter) string {
	if router == nil || len(router.types) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n<subagents>\n")
	b.WriteString("You can delegate work to subagents (子agent) via the task tool. Each call spawns an ")
	b.WriteString("independent internal Aliang Agent run: a separate conversation thread with its own context ")
	b.WriteString("and tool loop. A subagent does NOT see this conversation — give each task a self-contained ")
	b.WriteString("description with enough context to work alone. Subagents dispatched in the same turn run ")
	b.WriteString("concurrently (up to 4); each subagent's final report is returned as the tool result, and its ")
	b.WriteString("lifecycle is bound to the current turn.\n")
	b.WriteString("Available subagent types (subagent_type):\n")
	for _, t := range router.types {
		fmt.Fprintf(&b, "- %s: %s\n", t, router.descriptions[t])
	}
	b.WriteString("Use subagents for parallel research, codebase exploration, or independent subtasks. ")
	b.WriteString("Parallel subagents share the same working directory — parallelize read-only work; ")
	b.WriteString("keep file-writing tasks sequential or scoped to distinct files.\n")
	b.WriteString("</subagents>")
	return b.String()
}
