# AliangCode（agentcore）子 agent 实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** AliangCode 会话可使用 `~/.claude/agents/*.md`（CC 格式）定义的子 agent：每个定义包成宿主侧工具桥，Execute 递归 `agentcore.Run` 子循环（depth=1），轨迹全部落 journal tool 行。

**Architecture:** 内核零改动（方案 A 第四次延续）——宿主在 runAgentCore 组装期采集定义→包桥→注册；子 Config 显式 `Emit=nil`（静默子 run）/`Hooks=nil`（防窃插话）/Approve+Env+Client 沿用主 run（审批贯穿）；readOnly run 不注入；depth=1 结构性实现（子 registry 不含子 agent 桥）；子 registry 的内置工具经 journal 装饰器逐调用记 tool 行。

**Tech Stack:** Go；无新依赖（纯宿主层组装 + 既有 agentcore.Run 递归）。

**Spec:** `docs/superpowers/specs/2026-10-27-agentcore-subagents-design.md`（已评审通过）

---

## 实施纪律（开工前必读）

1. **worktree**：`.worktrees/agentcore-subagents` 新分支 `feature/agentcore-subagents`
   基于 master；新 worktree 先 `cp -R <主仓>/app/website/dist`（embed 坑；services
   测试不受影响）。
2. **提交**：批准执行本计划=授权各任务 commit；标题全中文「新增：/修复：/测试：」，
   尾行 `Co-Authored-By: Claude Code <noreply@anthropic.com>`。
3. **测试环境**：触盘测试用既有 `setTestAgentHome(t)`（勿用裸 t.Setenv HOME——
   该 helper 同时隔离 USERPROFILE）；MCP/子 run 交互测试复用 `startTestMCPSession`
   （mcp_test.go）与 `newAgentCoreUpstream`/`agentCoreTextTurn`/`agentCoreToolTurn`
   （run_test.go）——全部同包直接可用。
4. **内核事实**（写码锚点）：`agentcore.Run(ctx, *Config, []Message)
   (*RunResult, error)`（loop.go:174）；`Message{Role string; Content interface{}}`；
   Config 字段=Client/Model/System/MaxTokens/Tools/Env/Hooks/Emit/Approve/Logf/
   SessionID/Effort（**无 MaxIterations 字段**——包级常量 50 子 run 自动继承）；
   `DefaultRegistry()` 每次新建；内核工具规范名=Read/Write/Edit/Bash/PowerShell/
   Grep/Glob/LS（大写）；`Registry.Register` 后注册者胜。
5. **命名冲突红线**：子 agent 工具名必须避开 8 个内核规范名与 `mcp__` 前缀
   （Registry 后注册者胜会静默遮蔽内置工具）——清洗+保留名检查+序号消解。

## 文件结构

| 文件 | 动作 | 职责 |
|---|---|---|
| `app/http/services/agent_ai_agentcore_subagent.go` | 新增 | 定义采集（frontmatter tools/model/正文）、桥、子 Config 组装、journal 装饰器 |
| `app/http/services/agent_ai_agentcore_subagent_test.go` | 新增 | 采集/桥/depth/tools 白名单/模型策略/审批贯穿/readOnly 测试 |
| `app/http/services/agent_ai_agentcore.go` | 修改 | runAgentCore 组装注入（MCP 块之后）|
| `app/http/services/agent_ai_agentcore_mcp_test.go` | 修改 | e2e（子 agent 全链）|

---

### Task 1: 定义采集 + 桥

**Files:**
- Create: `app/http/services/agent_ai_agentcore_subagent.go`
- Test: `app/http/services/agent_ai_agentcore_subagent_test.go`

- [ ] **Step 1: 写失败测试**

```go
package services

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSubagentDef(t *testing.T, home, filename, body string) {
	t.Helper()
	dir := filepath.Join(home, ".claude", "agents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir 失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, filename), []byte(body), 0o644); err != nil {
		t.Fatalf("写定义失败: %v", err)
	}
}

func TestAgentCoreSubagentDefsIn(t *testing.T) {
	home := t.TempDir()
	writeSubagentDef(t, home, "reviewer.md",
		"---\nname: reviewer\ndescription: 代码评审\nmodel: sonnet\ntools: Read, Grep\n---\n你是评审员。")
	writeSubagentDef(t, home, "fallback.md", "没有 frontmatter 的正文。")
	writeSubagentDef(t, home, "broken.md", "---\nname: [ broken\n") // 回落名清洗
	writeSubagentDef(t, home, "notagent.txt", "忽略非 md")

	defs := agentCoreSubagentDefsIn(home)
	if len(defs) != 3 {
		t.Fatalf("条目数 = %d, want 3（非 md 忽略）: %+v", len(defs), defs)
	}
	byName := map[string]agentCoreSubagentDef{}
	for _, d := range defs {
		byName[d.Name] = d
	}
	r := byName["reviewer"]
	if r.Description != "代码评审" || len(r.ToolsWhitelist) != 2 || r.ToolsWhitelist[0] != "Read" {
		t.Fatalf("frontmatter 解析错误: %+v", r)
	}
	if !strings.Contains(r.Body, "评审员") {
		t.Fatalf("正文缺失: %+v", r)
	}
	if byName["fallback"].Body == "" {
		t.Fatalf("无 frontmatter 文件应保留回落名+正文")
	}
}

func TestAgentCoreSubagentDefsCap(t *testing.T) {
	home := t.TempDir()
	for i := 0; i < 30; i++ {
		writeSubagentDef(t, home, fmt.Sprintf("agent-%02d.md", i), "---\nname: a\n---\nx")
	}
	if got := len(agentCoreSubagentDefsIn(home)); got != 24 {
		t.Fatalf("cap = %d, want 24（确定性截断）", got)
	}
}

func TestAgentCoreSubagentBridgeName(t *testing.T) {
	reserved := map[string]bool{"Read": true, "Bash": true}
	seen := map[string]bool{}
	n1 := agentCoreSubagentBridgeName("Read", reserved, seen) // 与内核名撞 → 消解
	if n1 == "Read" || seen[n1] != true {
		t.Fatalf("内核保留名必须消解: %q", n1)
	}
	n2 := agentCoreSubagentBridgeName("Read", reserved, seen)
	if n2 == n1 {
		t.Fatalf("重名必须序号消解: %q vs %q", n1, n2)
	}
	if got := agentCoreSubagentBridgeName("a:b/c", reserved, seen); strings.ContainsAny(got, ":/") {
		t.Fatalf("清洗失效: %q", got)
	}
}
```

（import 补 `fmt`。）

- [ ] **Step 2:** `go test ./app/http/services/ -run 'TestAgentCoreSubagent' -v` 确认编译失败。

- [ ] **Step 3: 实现** `agent_ai_agentcore_subagent.go`：

```go
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

	"aliang.one/nursorgate/internal/logger"
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

// agentCoreSubagentFrontmatter 解析单文件（name 回落文件名；无 frontmatter
// 不跳过——沿用设备清单语义）。
func agentCoreSubagentFrontmatter(path, content string) (agentCoreSubagentDef, bool) {
	def := agentCoreSubagentDef{
		Name: sanitizeAgentCoreMCPSegment(strings.TrimSuffix(filepath.Base(path), ".md")),
	}
	rest, ok := strings.CutPrefix(content, "---")
	if ok {
		block, tail, found := strings.Cut(rest, "\n---")
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
			def.Body = strings.TrimSpace(tail)
		}
	}
	if def.Body == "" {
		def.Body = strings.TrimSpace(content) // 无 frontmatter：全文即指令
	}
	if len(def.Body) > agentCoreSubagentBodyCap {
		def.Body = def.Body[:agentCoreSubagentBodyCap]
	}
	if strings.TrimSpace(def.Name) == "" {
		return agentCoreSubagentDef{}, false
	}
	return def, true
}

// agentCoreSubagentDefsIn 采集 user 域定义（不递归、cap 24、按文件名排序、
// 畸形跳过）。
func agentCoreSubagentDefsIn(home string) []agentCoreSubagentDef {
	if strings.TrimSpace(home) == "" {
		return nil
	}
	entries, err := os.ReadDir(filepath.Join(home, ".claude", "agents"))
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
		content, err := os.ReadFile(filepath.Join(home, ".claude", "agents", name))
		if err != nil {
			continue
		}
		if def, ok := agentCoreSubagentFrontmatter(filepath.Join(home, ".claude", "agents", name), string(content)); ok {
			out = append(out, def)
		}
	}
	return out
}

// agentCoreSubagentBridgeName 工具名消解：避开 reserved（内核 8 规范名）与
// seen（已用名），重名追加 -2/-3。字符清洗复用 MCP 段白名单。
func agentCoreSubagentBridgeName(raw string, reserved, seen map[string]bool) string {
	base := sanitizeAgentCoreMCPSegment(raw)
	candidate := base
	for i := 2; reserved[candidate] || seen[candidate]; i++ {
		candidate = fmt.Sprintf("%s-%d", base, i)
	}
	seen[candidate] = true
	return candidate
}
```

import 校对：`internal/logger` 实为 `aliang.one/nursorgate/common/logger`（Task 1 of MCP 的教训——以仓内实际为准）。

- [ ] **Step 4:** 测试全 PASS；`gofmt -l` 两文件无输出。

- [ ] **Step 5: 提交**

```bash
git add app/http/services/agent_ai_agentcore_subagent.go app/http/services/agent_ai_agentcore_subagent_test.go
git commit -m "$(cat <<'EOF'
新增:agentcore 子 agent 定义采集(CC 格式 frontmatter,tools/model/正文,名消解)

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

---

### Task 2: 子 agent 桥（递归子 Config）

**Files:**
- Modify: `app/http/services/agent_ai_agentcore_subagent.go`
- Test: `app/http/services/agent_ai_agentcore_subagent_test.go`

- [ ] **Step 1: 写失败测试**

```go
import (
	"context"
	"aliang.one/nursorgate/app/agentcore"
)

// newSubRunUpstream 复用 run_test.go 的 newAgentCoreUpstream 起两回合脚本：
// 第一回合纯文本（子 run 的回答），第二回合不会被用到。
func TestAgentCoreSubagentBridgeExecute(t *testing.T) {
	up, srv := newAgentCoreUpstream(t, agentCoreTextTurn("评审完成：无问题", ""))
	t.Setenv("ANTHROPIC_BASE_URL", srv.URL)
	t.Setenv("ANTHROPIC_MODEL", "")
	_ = up

	client := agentcore.NewClient(agentCoreBaseURL())
	var journaled []string
	bridge := &agentCoreSubagentBridge{
		client:      client,
		model:       "glm-5.2",
		name:        "reviewer",
		description: "代码评审",
		body:        "你是评审员。",
		env: &agentcore.ToolEnv{
			ProjectPath: t.TempDir(),
			ShellPolicy: &agentCoreShellPolicy{run: agentAIRun{}},
		},
		approve: func(ctx context.Context, call agentcore.ApprovalRequest) (agentcore.ApprovalResponse, error) {
			return agentcore.ApprovalResponse{Approved: true}, nil
		},
		onToolCall: func(tool, summary string, isErr bool) { journaled = append(journaled, tool) },
		logf:       nil,
	}
	res := bridge.Execute(context.Background(), map[string]interface{}{"task": "评审 main.go"}, nil)
	if res.IsError || !strings.Contains(res.Content, "评审完成") {
		t.Fatalf("子 run 结果错误: %+v", res)
	}
}

func TestAgentCoreSubagentBridgeDepthOne(t *testing.T) {
	// depth=1 结构断言：子 Config 的 registry 不含任何子 agent 桥。
	bridge := &agentCoreSubagentBridge{}
	reg := bridge.subRegistry(nil, true) // mcpTools=nil, journal 装饰器开
	if _, ok := reg.Get("reviewer"); ok {
		t.Fatalf("子 registry 不得包含子 agent 桥（depth=1 结构保证）")
	}
	if _, ok := reg.Get("Read"); !ok {
		t.Fatalf("子 registry 应含内置 8 工具")
	}
}

func TestAgentCoreSubagentToolsWhitelist(t *testing.T) {
	bridge := &agentCoreSubagentBridge{}
	reg := bridge.subRegistry(nil, []string{"Read", "Grep"})
	for _, name := range []string{"Read", "Grep", "Write", "Bash", "LS"} {
		_, ok := reg.Get(name)
		want := name == "Read" || name == "Grep"
		if ok != want {
			t.Fatalf("白名单 %s: have=%v want=%v", name, ok, want)
		}
	}
}

func TestAgentCoreSubagentModelPolicy(t *testing.T) {
	cases := []struct {
		defModel, mainModel, want string
	}{
		{"", "glm-5.2", "glm-5.2"},
		{"glm-5.3", "glm-5.2", "glm-5.3"},
		{"sonnet", "glm-5.2", "glm-5.2"}, // claude 系忽略回落
	}
	for _, c := range cases {
		if got := agentCoreSubagentModel(c.defModel, c.mainModel); got != c.want {
			t.Fatalf("model(%q,%q)=%q want %q", c.defModel, c.mainModel, got, c.want)
		}
	}
}

// TestAgentCoreSubagentBridgeFailureTolerance 锁死失败容忍（spec §8）：
// 子 run 上游不可达 → Task 桥返回 is_error（不 panic、主 run 侧收到的
// tool_result 自决重试/放弃）。
func TestAgentCoreSubagentBridgeFailureTolerance(t *testing.T) {
	bridge := &agentCoreSubagentBridge{
		client: agentcore.NewClient("http://127.0.0.1:1"), // 不可达：client 内部重试后 err
		model:  "glm-5.2",
		name:   "reviewer",
		body:   "x",
		env: &agentcore.ToolEnv{
			ProjectPath: t.TempDir(),
			ShellPolicy: &agentCoreShellPolicy{run: agentAIRun{}},
		},
	}
	res := bridge.Execute(context.Background(), map[string]interface{}{"task": "评审"}, nil)
	if !res.IsError || !strings.Contains(res.Content, "子 agent 执行失败") {
		t.Fatalf("失败容忍语义错误: %+v", res)
	}
}
```

- [ ] **Step 2:** 跑测试确认编译失败。

- [ ] **Step 3: 实现桥**

`agent_ai_agentcore_subagent.go` 追加：

```go
// agentCoreSubagentBridge 把一个子 agent 定义包成内核 agentcore.Tool。
type agentCoreSubagentBridge struct {
	client      *agentcore.Client
	model       string // 主 run 模型（兜底）
	name        string // 已消解的工具名
	description string
	body        string // 定义正文（子 run 指令）
	env         *agentcore.ToolEnv
	approve     func(context.Context, agentcore.ApprovalRequest) (agentcore.ApprovalResponse, error)
	logf        func(string, ...interface{})
	// mcpTools：主 run 已建连的 mcp__ 桥（子 run 继承，同一 session 零重复建连）。
	// 由宿主在组装期填充。
	mcpTools func() []agentcore.Tool
	// onToolCall：子 run 内部工具调用的 journal 回调（nil=不记）。
	onToolCall func(tool, summary string, isErr bool)
}

func (b *agentCoreSubagentBridge) Name() string                        { return b.name }
func (b *agentCoreSubagentBridge) Description() string                 { return b.description }
func (b *agentCoreSubagentBridge) InputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"task": map[string]interface{}{"type": "string", "description": "交给该子 agent 的任务描述"},
		},
		"required": []string{"task"},
	}
}

// agentCoreSubagentModel 模型策略（spec §6）：定义 model 空/等于主模型 → 沿用；
// glm-* → 采用；其余（claude 系）→ 忽略回落主模型。
func agentCoreSubagentModel(defModel, mainModel string) string {
	defModel = strings.TrimSpace(defModel)
	if defModel == "" || defModel == mainModel {
		return mainModel
	}
	if strings.HasPrefix(strings.ToLower(defModel), "glm-") {
		return defModel
	}
	// spec §6：非 glm-* 忽略回落主模型（网关只路由 glm-*）。Warn 每次触发
	// 打一条（桥 per-run 构造，量级=Task 调用次数）。
	logger.Warn(fmt.Sprintf("agentcore-subagent: 定义 model %q 非 glm-*，忽略回落主模型 %s", defModel, mainModel))
	return mainModel
}

// subRegistry 组装子 run 工具表：内置 8（frontmatter 白名单生效，**大写规范名
// 精确匹配**）+ MCP 桥（mcpTools() 提供）；每个内置工具包 journal 装饰器；
// 不含任何子 agent 桥（depth=1 结构保证）。
func (b *agentCoreSubagentBridge) subRegistry(mcpTools []agentcore.Tool, journal bool) *agentcore.Registry {
	base := agentcore.DefaultRegistry()
	registry := agentcore.NewRegistry()
	kernelNames := []string{"Read", "Write", "Edit", "Bash", "PowerShell", "Grep", "Glob", "LS"}
	for _, name := range kernelNames {
		tool, ok := base.Get(name)
		if !ok {
			continue
		}
		// frontmatter 白名单：大写规范名精确匹配（spec §5）；缺省=全量。
		if len(b.whitelist()) > 0 && !containsString(b.whitelist(), name) {
			continue
		}
		if journal && b.onToolCall != nil {
			tool = &agentCoreJournalToolDecorator{inner: tool, onCall: b.onToolCall}
		}
		registry.Register(tool)
	}
	// 白名单里的未知名（与内核规范名大小写/拼写不符）忽略+Warn（spec §5）。
	for _, w := range b.whitelist() {
		if !containsString(kernelNames, w) {
			logger.Warn(fmt.Sprintf("agentcore-subagent: 白名单未知名忽略 name=%s", w))
		}
	}
	for _, tool := range mcpTools {
		registry.Register(tool) // MCP 桥自带 onCall，无需再装饰
	}
	return registry
}

func (b *agentCoreSubagentBridge) whitelist() []string { return b.toolsWhitelist }

// Execute：递归子 run。ctx 继承（内核单工具 10 分钟上限罩住整个子循环）。
func (b *agentCoreSubagentBridge) Execute(ctx context.Context, input map[string]interface{}, _ *agentcore.ToolEnv) (result agentcore.ToolResult) {
	task, _ := input["task"].(string)
	if strings.TrimSpace(task) == "" {
		return agentcore.ToolResult{Content: "task 参数必填", IsError: true}
	}
	cfg := &agentcore.Config{
		Client:  b.client,
		Model:   agentCoreSubagentModel(b.defModel, b.model),
		System:  agentCoreSystemPrompt() + "\n\n" + b.body,
		Tools:   b.subRegistry(b.mcpToolsForExecute(), true),
		Env:     b.env,
		Approve: b.approve,
		// Emit=nil：子 run 静默（防帧交错进父管道）；Hooks=nil：防窃主 run 插话。
		Logf: b.logf,
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
	if b.onToolCall != nil {
		b.onToolCall(b.name, summarizeAgentCoreMCPInput(map[string]interface{}{"task": task}), isErr)
	}
	return agentcore.ToolResult{Content: truncateAgentCoreMCPOutput(text), IsError: isErr}
}
```

配套补充（实现时钉死）：
- bridge 需补 `toolsWhitelist []string` 与 `defModel string` 字段（上面片段引用了它们）；`mcpToolsForExecute()` = 若 `b.mcpTools != nil` 返回 `b.mcpTools()` 否则 nil；
- `agentCoreJournalToolDecorator`：

```go
// agentCoreJournalToolDecorator 包装内核工具，把每次调用记 journal tool 行
// （子 run 内置工具的轨迹；MCP 桥自带 onCall 不需要装饰）。
type agentCoreJournalToolDecorator struct {
	inner  agentcore.Tool
	onCall func(tool, summary string, isErr bool)
}

func (d *agentCoreJournalToolDecorator) Name() string                        { return d.inner.Name() }
func (d *agentCoreJournalToolDecorator) Description() string                 { return d.inner.Description() }
func (d *agentCoreJournalToolDecorator) InputSchema() map[string]interface{} { return d.inner.InputSchema() }
func (d *agentCoreJournalToolDecorator) Execute(ctx context.Context, input map[string]interface{}, env *agentcore.ToolEnv) (result agentcore.ToolResult) {
	summary := summarizeAgentCoreMCPInput(input)
	defer func() {
		if rec := recover(); rec != nil {
			result = agentcore.ToolResult{Content: fmt.Sprintf("工具执行崩溃（已兜底）: %v", rec), IsError: true}
			if d.onCall != nil {
				d.onCall(d.inner.Name(), summary, true)
			}
		}
	}()
	res := d.inner.Execute(ctx, input, env)
	if d.onCall != nil {
		d.onCall(d.inner.Name(), summary, res.IsError)
	}
	return res
}
```

- `containsString` 是 agent_approval_policy.go 现成原语；`agentCoreSystemPrompt()`/`agentCoreBaseURL()` 现成（注意 master 最新 `agentCoreSystemPrompt(run.projectPath)` 带参——**以现签名为准**，桥需要 projectPath 时经 env 或新增字段传入）。
- bridge 组装入口（Task 3 用）：

```go
// agentCoreSubagentBridges 把定义集包成桥（名字消解共享 reserved=内核 8 名）。
func (m *agentAIManager) agentCoreSubagentBridges(defs []agentCoreSubagentDef, base *agentCoreSubagentBridgeConfig) []agentcore.Tool
```
（形态实现时自定，测试可达即可。）

- [ ] **Step 4:** 全部子 agent 测试 PASS + vet + gofmt。

- [ ] **Step 5: 提交**

```bash
git add app/http/services/agent_ai_agentcore_subagent.go app/http/services/agent_ai_agentcore_subagent_test.go
git commit -m "$(cat <<'EOF'
新增:agentcore 子 agent 桥(递归子 Config,Emit/Hooks 置零,depth=1,journal 装饰器)

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

---

### Task 3: runAgentCore 组装注入 + e2e

**Files:**
- Modify: `app/http/services/agent_ai_agentcore.go`（MCP 注入块之后）
- Modify: `app/http/services/agent_ai_agentcore_mcp_test.go`（e2e）

- [ ] **Step 1: 写失败测试（e2e）**

`agent_ai_agentcore_mcp_test.go` 追加（镜像 `TestRunAgentCoreMCPEndToEnd` 组装）：

```go
func TestRunAgentCoreSubagentEndToEnd(t *testing.T) {
	// ① 定义 fixture：HOME/.claude/agents/reviewer.md（tools 白名单 Read）。
	// ② 上游三回合：主 run 第一回合 tool_use 调「reviewer」{"task":"评审"}；
	//    子 run（同 upstream 脚本池）回文本「评审完成」；主 run 第二回合收尾。
	//    （子 run 与主 run 共用 newAgentCoreUpstream 的顺序脚本池——脚本顺序
	//    =先主第一回合、再子回合、再主第二回合，实现时按上游实际调用序摆放。）
	// ③ 断言：恰 1 ai.done/0 ai.error；tool_result 含「评审完成」；
	//    journal 行含 Task 行（tool=reviewer）与子 run 内部行为空（子 run 无
	//    工具调用则无内部行——本用例子 run 纯文本）；定义白名单生效（子请求
	//    的 tools 只含 Read，无 Write——从 upstream 记录的请求体断言）。
	//
	// 组装镜像 TestRunAgentCoreMCPEndToEnd；红阶段=未接线时 tool_use 得
	// 「未知工具: reviewer」。红期断言锚 tool_result 内容（非上游请求次数——
	// 未知工具时主 run 会多打一次上游导致脚本错位）。
	t.Skip("实现时按 TestRunAgentCoreMCPEndToEnd 组装补全——禁止带 Skip 落库")
}

// TestRunAgentCoreSubagentApprovalPropagation 锁死 spec §5 审批贯穿（本功能的
// 安全骨架）：子 run 内调用 mcp__ 工具 → 默认门弹手机审批；拒绝 → 子 run 收到
// 拒绝文案、主 run 存活收尾。
//
// 组装 = TestRunAgentCoreMCPEndToEnd + TestAgentCoreMCPApprovalDefaultGate 的
// NoRuleMatch 场景拼合：定义 fixture + startTestMCPSession("echo") + dial 注入 +
// 无策略规则（或 allow_all）+ 三回合脚本（主 run tool_use 调 reviewer → 子 run
// tool_use 调 mcp__srv__echo → 主 run 收尾——脚本池按实际调用序摆放）。
// 断言：审批事件 pending+resolved（tool_name=mcp__srv__echo）；decline 后子 run
// 的 tool_result 含拒绝文案；恰 1 ai.done/0 ai.error。
// 红期预期：无门时代码路径不触发审批（在 Task 4 默认门已合入的现状下，本测试
// 直接应绿——它是回归护栏而非 TDD 红；真正要防的是将来有人给子 Config 换掉
// Approve 闭包）。若写完即绿，在测试注释里写明「护栏测试」性质即可。
func TestRunAgentCoreSubagentApprovalPropagation(t *testing.T) {
	t.Skip("实现时按上述两参照组装补全——禁止带 Skip 落库")
}

// TestRunAgentCoreSubagentReadOnlyNotInjected 锁死 spec §3 readOnly 门：
// readOnly run 的上游请求 Tools 表不含子 agent 工具（mirror
// TestRunAgentCoreReadOnlySkipsMCPInjection 的断言样式）。
//
// 组装：定义 fixture（同 e2e）+ readOnly: true 的 run → 断言上游请求 Tools
// 无 reviewer、journal 无 Task 行、恰 1 ai.done。红期=未接线时本就无 reviewer
// → 写完即绿，属「接线后防回归」护栏（与 MCP readOnly 测试同性质），注释注明。
func TestRunAgentCoreSubagentReadOnlyNotInjected(t *testing.T) {
	t.Skip("实现时按 TestRunAgentCoreReadOnlySkipsMCPInjection 组装补全——禁止带 Skip 落库")
}
```

> 先读 TestRunAgentCoreMCPEndToEnd 全文再落笔；Skip 只许存在于计划，落地必须删。

- [ ] **Step 2:** 确认红（SKIP→补全后红于「未知工具」）。

- [ ] **Step 3: 集成**

runAgentCore 的 MCP 注入块（`registry := agentcore.DefaultRegistry()` 附近）之后：

```go
	// 子 agent 注入（spec §3）：readOnly run 不注入（mirror MCP 只读决策）；
	// 定义集为空不注入。
	if !run.readOnly {
		if subTools := m.agentCoreSubagentBridges(agentCoreSubagentDefsIn(agentHome()), ...); len(subTools) > 0 {
			for _, tool := range subTools {
				registry.Register(tool)
			}
		}
	}
```

（bridge 构造需要的 client/env/approve/logf 从 cfg 组装处取——实现时把 client 构造提前为局部变量共用。mcpTools 继承：桥的 `mcpTools` 字段填 `func() []agentcore.Tool { return mcpTools }`。）

- [ ] **Step 4:** 全部测试 PASS + 整包 `go test ./app/http/services/ -count=1` PASS + vet + gofmt。

- [ ] **Step 5: 提交**

```bash
git add app/http/services/agent_ai_agentcore.go app/http/services/agent_ai_agentcore_mcp_test.go
git commit -m "$(cat <<'EOF'
新增:agentcore run 接入子 agent 注入(readOnly 不注入,端到端测试锁定)

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

---

### Task 4: 全量回归 + 冒烟清单更新

- [ ] **Step 1**: `go test ./app/http/services/ -count=1` 全绿；`go vet`；gofmt 触碰文件
- [ ] **Step 2**: 冒烟清单并入 `docs/superpowers/reviews/2026-10-27-agentcore-mcp-handover.md`
      （主仓）：子 agent 项——配 `~/.claude/agents/reviewer.md` → AliangCode 会话派发 →
      journal 回放见 Task 行 → readOnly goal fork 无子 agent 工具
- [ ] **Step 3**: 有修正才提交。

---

## 风险与回滚

- 递归失控 → depth=1 结构性排除（子 registry 无子 agent 桥）；
- 审批绕过 → 同一 Approve 闭包贯穿（测试锁定）；
- 子 run 失败扩散 → is_error 隔离主 run 存活；
- 回滚 = 移除组装注入；journal tool 行残留无害。
