# AI 方案选择（ai.option.*）Agent 端实现 Plan — 方案 B

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让 Agent（alianggate 设备端）在 Claude/Codex AI 对话中，能识别 AI 输出的"多方案选择"结构化块，向服务端发 `ai.option.request`；收到用户在手机端的选择（`ai.option.response`）后，把选择续接成新一轮 AI run（`--resume`），从而实现"AI 出方案 → 手机端选 → AI 继续"。

**Architecture:** 方案 B（多轮对话 + 结构化方案块）。不引入自定义 MCP 工具，复用现有 `claude --print --output-format stream-json --resume` 单次管道模型：
- AI 一轮输出里带 ```` ```aliang-options {...}``` ```` 结构化块 → run 自然 `ai.done` → agent 扫描完整输出提取块 → 发 `ai.option.request` 并在 session 记 `pendingOption`；
- 收到 `ai.option.response` → 用选择拼成新 user prompt → 启动新一轮 run（复用 `message()` 的 run 启动逻辑，抽成 `runUserMessage`）。
- 这是 **run 之间**的交互（非 run 中途阻塞），因此比 approval 的 waiter/channel 机制更简单：不需要阻塞 goroutine，只需 session 上记一个 pending 请求。

**Tech Stack:** Go（alianggate `app/http/services/agent_ai.go` 等）、`encoding/json`、`regexp`、现有 `agentAIManager`/`agentAISession`、gorilla WebSocket 出站 `writeJSON`。

**前置事实（已通过读码确认）：**
- Claude 走 `--print --verbose --output-format stream-json --include-partial-messages`（`newClaudeCodeAITool` agent_ai.go:1894），输出含完整 assistant message。
- `runCLIPass`（agent_ai.go:1426）在 run 结束时有完整累积输出 `assistantOutput`（:1505），其后调 `appendAssistantHistory`（:1509）并发 `ai.done`（:1511）。方案块提取点就在这里。
- `message()`（agent_ai.go:368）负责"启动一轮 run"：锁内查 session/检查未在跑/normalize provider/生成 token+ctx/更新 session.cancel+activeWriter+activity+runSeq+history，锁外 `go m.runCLI`。要抽出可复用段。
- approval 的可靠投递（ack/sync/cancelled）已在 Phase 2 落地（`agent_ai_approval_proto_test.go`），option 的 cancelled 复用同一套 close 清理模式。
- `agentAISession`（agent_ai.go:29-43）字段已知；`agentAIManager`（:22-27）有 `sessions/approvals/completedApprovals`。
- Claude `--append-system-prompt "<text>"` 在 print 模式可用，用于引导 AI 输出 `aliang-options` 块。

**⚠️ Commit Policy（项目 CLAUDE.md）：禁止自动 commit。** 本 plan 每个任务末尾的 commit 步骤仅为结构占位，**执行时不得自动提交**；需在用户明确说"commit/提交"后，按项目约定（HEREDOC + `🤖 Generated with [Claude Code]` + Co-Authored-By 行）由人工触发。测试步骤照常运行。

**范围（Scope）：** 本 plan 只覆盖 **agent 端（alianggate）**，是独立可测单元（mock WS writer 单测协议/解析/续接）。服务端（AliangPhoneServer，TS）与手机端见文末"契约附录"，作为后续 plan。

---

## 文件结构

| 文件 | 职责 | 动作 |
|---|---|---|
| `app/http/models/agent_protocol.go` | `ai.option.*` 事件常量 + 协议契约条目 | 修改 |
| `app/http/services/agent_ai.go` | option 数据结构、方案块解析、run 结束提取发 request、`runUserMessage` 抽取、`optionResponse`、close 清理、Claude system prompt 引导 | 修改（核心） |
| `app/http/services/agent_remote_ws.go` | `handleRemoteAgentMessage` 增加 `ai.option.response` 分支 | 修改 |
| `app/http/services/agent_ai_option_test.go` | option 解析 / request 发送 / response 续接 / close cancelled 的单测 | 新建 |

设计原则：option 逻辑尽量与 approval 并列、复用同一套 close/取消模式；不阻塞 goroutine（run 间交互）；新代码集中在 agent_ai.go 的 option 段 + 一个独立测试文件。

---

## 协议设计

### 事件常量（`app/http/models/agent_protocol.go`）

```go
AgentEventAIOptionRequest   = "ai.option.request"   // agent→server：请求用户在多方案中选择
AgentEventAIOptionResponse  = "ai.option.response"  // server→agent：用户的选择结果
AgentEventAIOptionCancelled = "ai.option.cancelled" // 双向：选择对话失活，作废待选
```

契约条目加入 `DefaultAgentProtocolContract().WebSocket`：
- `ClientSends` 增 `ai.option.request`（Required: `type,session_id,option_id,options`；Optional: `message_id,title,allow_custom,multi,provider`）
- `ServerSends` 增 `ai.option.response`（Required: `type,session_id,option_id,selected`；Optional: `message_id,custom_text,decision,delivery_id`，Emits: `ai.run.started,ai.delta,ai.done,ai.error,ai.option.request`）、`ai.option.cancelled`。

### `ai.option.request`（agent→server）

```jsonc
{
  "type": "ai.option.request",
  "session_id": "s1", "message_id": "m_asst_1", "option_id": "opt_s1_1",
  "title": "选择实现方案",
  "options": [
    {"id":"a","label":"方案A：用 Redis 缓存","description":"快但多一个依赖"},
    {"id":"b","label":"方案B：本地内存缓存","description":"零依赖"}
  ],
  "allow_custom": true, "multi": false, "provider": "claude"
}
```

### `ai.option.response`（server→agent）

```jsonc
{
  "type": "ai.option.response", "session_id": "s1", "option_id": "opt_s1_1",
  "selected": ["b"], "custom_text": "", "decision": "submitted", "delivery_id": "dv_3"
}
```

### 方案块格式（AI 输出，靠 system prompt 引导产出）

````
我准备了两个实现方案，请你选择：

```aliang-options
{"title":"选择实现方案","options":[{"id":"a","label":"方案A：用 Redis 缓存","description":"快但多一个依赖"},{"id":"b","label":"方案B：本地内存缓存","description":"零依赖"}],"allow_custom":true,"multi":false}
```
````

agent 用正则 `` (?s)```aliang-options\s*\n(.*?)\n``` `` 提取块内 JSON。一次 run 取**第一个**有效块（MVP）。

---

## Task 1：协议常量 + 契约条目

**Files:**
- Modify: `app/http/models/agent_protocol.go`

- [ ] **Step 1：加事件常量**

在 `AgentEventAIApprovalState`/`AgentEventAIApprovalCancelled` 一组之后（约 agent_protocol.go:45 附近）追加：

```go
AgentEventAIOptionRequest   = "ai.option.request"   // agent→server: 请求用户在多个方案中选择
AgentEventAIOptionResponse  = "ai.option.response"  // server→agent: 用户的选择结果
AgentEventAIOptionCancelled = "ai.option.cancelled" // 双向: 选择对话失活，作废待选
```

- [ ] **Step 2：补协议契约条目**

在 `DefaultAgentProtocolContract()` 的 `WebSocket.ClientSends` 末尾加：

```go
{Type: AgentEventAIOptionRequest, Required: []string{"type", "session_id", "option_id", "options"}, Optional: []string{"message_id", "title", "allow_custom", "multi", "provider"}},
```

在 `WebSocket.ServerSends` 末尾加：

```go
{Type: AgentEventAIOptionResponse, Required: []string{"type", "session_id", "option_id", "selected"}, Optional: []string{"message_id", "custom_text", "decision", "delivery_id"}, Emits: []string{AgentEventAIRunStarted, AgentEventAIDelta, AgentEventAIDone, AgentEventAIError, AgentEventAIOptionRequest}},
{Type: AgentEventAIOptionCancelled, Required: []string{"type", "session_id"}, Optional: []string{"option_id", "reason"}},
```

- [ ] **Step 3：编译**

Run: `go build ./app/http/models/...`
Expected: 编译通过（无报错）。

- [ ] **Step 4：commit（⚠️ 需用户确认，禁止自动提交）**

```bash
git add app/http/models/agent_protocol.go
git commit -m "feat(agent): add ai.option.* protocol events for multi-option selection"
```

---

## Task 2：option 数据结构 + 方案块解析（纯函数）

**Files:**
- Modify: `app/http/services/agent_ai.go`（在 approval 相关结构附近新增 option 结构与解析函数）
- Test: `app/http/services/agent_ai_option_test.go`（新建）

- [ ] **Step 1：先写失败测试（新建测试文件）**

`app/http/services/agent_ai_option_test.go`：

```go
package services

import (
	"strings"
	"testing"
)

func TestExtractAgentAIOptionBlocks(t *testing.T) {
	output := "我准备了两个方案：\n\n" +
		"```aliang-options\n" +
		`{"title":"选择方案","options":[{"id":"a","label":"方案A"},{"id":"b","label":"方案B","description":"更稳"}],"allow_custom":true,"multi":false}` + "\n```\n"
	got := extractAgentAIOptionBlocks(output)
	if len(got) != 1 {
		t.Fatalf("blocks = %d, want 1", len(got))
	}
	if got[0].Title != "选择方案" || !got[0].AllowCustom || len(got[0].Options) != 2 {
		t.Fatalf("parsed = %+v", got[0])
	}
	if got[0].Options[1].Description != "更稳" {
		t.Fatalf("opt[1].Description = %q", got[0].Options[1].Description)
	}
}

func TestExtractAgentAIOptionBlocksIgnoresMalformedAndEmpty(t *testing.T) {
	// 无效 JSON + 空 options 都应被丢弃；不报错。
	output := "```aliang-options\n{not json}\n```\n```aliang-options\n{\"options\":[]}\n```\n"
	if got := extractAgentAIOptionBlocks(output); len(got) != 0 {
		t.Fatalf("blocks = %d, want 0", len(got))
	}
	// 普通代码块不应误命中
	if got := extractAgentAIOptionBlocks("```go\nfmt.Println()\n```\n"); len(got) != 0 {
		t.Fatalf("plain code block should not match, got %d", len(got))
	}
}

func TestBuildAgentAIOptionFollowup(t *testing.T) {
	req := &agentAIOptionRequest{Title: "x", Options: []agentAIOptionChoice{
		{ID: "a", Label: "方案A"},
		{ID: "b", Label: "方案B", Description: "更稳"},
	}}
	got := buildAgentAIOptionFollowup(req, []string{"b"}, "顺便加日志")
	if !strings.Contains(got, "方案B") || !strings.Contains(got, "更稳") || !strings.Contains(got, "顺便加日志") {
		t.Fatalf("followup = %q", got)
	}
}
```

- [ ] **Step 2：运行测试确认失败**

Run: `go test ./app/http/services/ -run 'TestExtractAgentAIOptionBlocks|TestBuildAgentAIOptionFollowup' -v`
Expected: FAIL（`undefined: extractAgentAIOptionBlocks` 等）。

- [ ] **Step 3：实现数据结构 + 解析/拼装函数**

在 `agent_ai.go`（approval 结构定义之后，例如 `agentAIApprovalResponse` 结构之后）新增：

```go
// agentAIOptionRequest 描述一次"多方案选择"请求（agent→server）。
type agentAIOptionRequest struct {
	ID          string                `json:"option_id,omitempty"`
	SessionID   string                `json:"session_id,omitempty"`
	MessageID   string                `json:"message_id,omitempty"`
	Provider    string                `json:"provider,omitempty"`
	Title       string                `json:"title,omitempty"`
	Options     []agentAIOptionChoice `json:"options"`
	AllowCustom bool                  `json:"allow_custom"`
	Multi       bool                  `json:"multi"`
}

type agentAIOptionChoice struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// aliangOptionsBlockRe 匹配 ```aliang-options\n<json>\n``` 围栏块（DOTALL）。
var aliangOptionsBlockRe = regexp.MustCompile("(?s)```aliang-options\\s*\\n(.*?)\\n```")

// extractAgentAIOptionBlocks 扫描 assistant 完整输出，提取所有 aliang-options 块。
// 无效 JSON 或 options 为空的块被静默丢弃。
func extractAgentAIOptionBlocks(output string) []agentAIOptionRequest {
	matches := aliangOptionsBlockRe.FindAllStringSubmatch(output, -1)
	out := make([]agentAIOptionRequest, 0, len(matches))
	for _, m := range matches {
		var req agentAIOptionRequest
		if err := json.Unmarshal([]byte(m[1]), &req); err != nil {
			continue
		}
		if len(req.Options) == 0 {
			continue
		}
		out = append(out, req)
	}
	return out
}

// buildAgentAIOptionFollowup 把用户的选择拼成下一轮的 user prompt。
// Claude 经 --resume 续接时，这段文本作为新的用户输入。
func buildAgentAIOptionFollowup(req *agentAIOptionRequest, selected []string, custom string) string {
	var b strings.Builder
	b.WriteString("用户已在上述方案中做出选择，请据此继续，不要再重复列出方案：\n")
	labelByID := make(map[string]agentAIOptionChoice, len(req.Options))
	for _, opt := range req.Options {
		labelByID[opt.ID] = opt
	}
	for _, id := range selected {
		opt, ok := labelByID[id]
		if !ok {
			b.WriteString("- 选择了未知选项 id=" + id + "\n")
			continue
		}
		b.WriteString("- 选择了：" + opt.Label)
		if strings.TrimSpace(opt.Description) != "" {
			b.WriteString("（" + opt.Description + "）")
		}
		b.WriteString("\n")
	}
	if strings.TrimSpace(custom) != "" {
		b.WriteString("- 用户自定义补充方案：" + strings.TrimSpace(custom) + "\n")
	}
	return b.String()
}
```

> **必须新增 import**：`regexp` 当前未被 agent_ai.go 引用（已核实 import 块），不补会编译失败。把 `"regexp"` 加入 agent_ai.go 的 import 块。

- [ ] **Step 4：运行测试确认通过**

Run: `go test ./app/http/services/ -run 'TestExtractAgentAIOptionBlocks|TestBuildAgentAIOptionFollowup' -v`
Expected: PASS。

- [ ] **Step 5：commit（⚠️ 需用户确认）**

```bash
git add app/http/services/agent_ai.go app/http/services/agent_ai_option_test.go
git commit -m "feat(agent): parse aliang-options blocks and build option followup prompt"
```

---

## Task 3：session 记 pendingOption + run 结束提取并发 request

**Files:**
- Modify: `app/http/services/agent_ai.go`（`agentAISession` 结构、`runCLIPass`、新增 `emitOptionRequest`）
- Test: `app/http/services/agent_ai_option_test.go`

- [ ] **Step 1：`agentAISession` 增字段**

在 `agentAISession`（agent_ai.go:29-43）末尾加：

```go
	pendingOption   *agentAIOptionRequest // run 结束检测到方案块时置位，等 ai.option.response
```

- [ ] **Step 2：先写失败测试**

测试文件追加：

```go
func TestEmitOptionRequestSendsEventAndStashesPending(t *testing.T) {
	manager := newAgentAIManager()
	defer manager.closeAll()
	mu, events, writer := captureAIWriter(t)

	// 种一个 session + activity，runSeq=2（模拟一轮 run 刚结束）。
	manager.mu.Lock()
	manager.sessions["s1"] = &agentAISession{
		id: "s1", runSeq: 2, provider: "claude", activity: newAgentAIActivity(),
	}
	manager.mu.Unlock()

	run := agentAIRun{sessionID: "s1", runSeq: 2, messageID: "m1", provider: "claude", activity: manager.sessions["s1"].activity}
	blocks := []agentAIOptionRequest{
		{Title: "选方案", Options: []agentAIOptionChoice{{ID: "a", Label: "A"}, {ID: "b", Label: "B"}}, AllowCustom: true},
	}
	manager.emitOptionRequest(run, writer, blocks)

	req := lastAIEvent(mu, events, "ai.option.request")
	if req == nil {
		t.Fatal("expected ai.option.request emitted")
	}
	if req["session_id"] != "s1" || req["option_id"] == nil || req["option_id"] == "" {
		t.Fatalf("request = %+v", req)
	}
	opts, _ := req["options"].([]agentAIOptionChoice)
	// options 经 struct 序列化到 map：直接断言长度与 title。
	if req["title"] != "选方案" || req["allow_custom"] != true {
		t.Fatalf("request fields = %+v", req)
	}
	// pending 已落到 session。
	manager.mu.Lock()
	pending := manager.sessions["s1"].pendingOption
	manager.mu.Unlock()
	if pending == nil || pending.Title != "选方案" {
		t.Fatalf("pendingOption = %+v", pending)
	}
}

func TestEmitOptionRequestSkipsStaleRunSeq(t *testing.T) {
	manager := newAgentAIManager()
	defer manager.closeAll()
	mu, events, writer := captureAIWriter(t)
	manager.mu.Lock()
	manager.sessions["s1"] = &agentAISession{id: "s1", runSeq: 9, activity: newAgentAIActivity()}
	manager.mu.Unlock()
	// runSeq 不匹配（run 已被后续覆盖）→ 不发、不记。
	run := agentAIRun{sessionID: "s1", runSeq: 2, messageID: "m1", activity: manager.sessions["s1"].activity}
	manager.emitOptionRequest(run, writer, []agentAIOptionRequest{
		{Options: []agentAIOptionChoice{{ID: "a", Label: "A"}}},
	})
	if got := findAIEvents(mu, events, "ai.option.request"); len(got) != 0 {
		t.Fatalf("expected no event for stale runSeq")
	}
}
```

- [ ] **Step 3：运行测试确认失败**

Run: `go test ./app/http/services/ -run 'TestEmitOptionRequest' -v`
Expected: FAIL（`undefined: emitOptionRequest`）。

- [ ] **Step 4：实现 `emitOptionRequest`**

在 agent_ai.go（approval 的 requestApproval 附近）新增：

```go
// emitOptionRequest 把 run 结束时提取到的方案块，发 ai.option.request 并在 session 上记 pending。
// MVP：一次 run 只处理第一个有效块。runSeq 不匹配（已被覆盖）时静默跳过，防止串台。
func (m *agentAIManager) emitOptionRequest(run agentAIRun, writeJSON agentTerminalWriter, blocks []agentAIOptionRequest) {
	if writeJSON == nil || len(blocks) == 0 {
		return
	}
	req := blocks[0]
	if strings.TrimSpace(req.ID) == "" {
		req.ID = newAgentAIApprovalID(run.sessionID, run.runSeq) // 复用 id 生成器
	}
	req.SessionID = run.sessionID
	req.MessageID = agentAssistantMessageID(run.messageID)
	req.Provider = firstNonEmpty(req.Provider, run.provider)

	m.mu.Lock()
	session := m.sessions[run.sessionID]
	if session == nil || session.runSeq != run.runSeq {
		m.mu.Unlock()
		return
	}
	session.pendingOption = &req
	m.mu.Unlock()

	_ = writeJSON(map[string]interface{}{
		"type":         models.AgentEventAIOptionRequest,
		"session_id":   run.sessionID,
		"message_id":   req.MessageID,
		"option_id":    req.ID,
		"title":        req.Title,
		"options":      req.Options,
		"allow_custom": req.AllowCustom,
		"multi":        req.Multi,
		"provider":     req.Provider,
	})
}
```

- [ ] **Step 5：在 `runCLIPass` 成功路径接入提取**

定位 `runCLIPass` 成功结尾（**按符号定位，勿依赖行号**：在 `m.appendAssistantHistory(run.sessionID, run.runSeq, run.messageID, assistantOutput)` 调用之后、发 `models.AgentEventAIDone` 的 `writeJSON` 之前；实际约 :1521-1524）。在 `appendAssistantHistory(...)` 调用之后插入：

```go
	if blocks := extractAgentAIOptionBlocks(assistantOutput); len(blocks) > 0 {
		m.emitOptionRequest(run, writeJSON, blocks)
	}
```

> 注意：方案块文本也会随 `ai.delta` 流发到手机端（MVP 不做 delta 过滤）。手机端 UI 可隐藏 `aliang-options` 代码块；delta 过滤列为后续优化（见文末）。

- [ ] **Step 6：运行测试确认通过**

Run: `go test ./app/http/services/ -run 'TestEmitOptionRequest' -v`
Expected: PASS。

- [ ] **Step 7：commit（⚠️ 需用户确认）**

```bash
git add app/http/services/agent_ai.go app/http/services/agent_ai_option_test.go
git commit -m "feat(agent): emit ai.option.request when a run output contains option blocks"
```

---

## Task 4：抽 `runUserMessage` + `optionResponse` 续接 + WS 入口分支

> 这是本 plan 最敏感的一步（重构 `message()`）。目标：把 `message()` 里"在 session 上启动一轮 run"的核心抽成 `runUserMessage`，`message()` 与 `optionResponse()` 共用，**不改变现有 approval/正常聊天行为**。

**Files:**
- Modify: `app/http/services/agent_ai.go`（`message` 重构、新增 `runUserMessage` 与 `optionResponse`）
- Modify: `app/http/services/agent_remote_ws.go`（`handleRemoteAgentMessage` 增分支）
- Test: `app/http/services/agent_ai_option_test.go`

- [ ] **Step 1：先写 optionResponse 的失败测试**

测试文件追加（不真正起 CLI，只断言：pending 被消费 + 触发了新 run 的 `ai.run.started`/错误事件；用桩 runCLI）。由于真实 `runCLI` 会起子进程，测试用**已注册的 pendingOption + 一个会快速失败的 run**（空 prompt / 找不到 CLI 时 runCLIPass 会发 `ai.error`）。更稳妥：断言 `pendingOption` 被清空 + session.runSeq 自增（说明新 run 已派发）。

```go
func TestOptionResponseClearsPendingAndDispatchesRun(t *testing.T) {
	manager := newAgentAIManager()
	defer manager.closeAll()
	mu, events, writer := captureAIWriter(t)

	// 种 session：已结束一轮(runSeq=2)，挂一个 pendingOption。
	manager.mu.Lock()
	manager.sessions["s1"] = &agentAISession{
		id: "s1", runSeq: 2, provider: "claude", mode: "vibe",
		projectPath: t.TempDir(), activity: newAgentAIActivity(),
		pendingOption: &agentAIOptionRequest{
			ID: "opt_s1_2", Options: []agentAIOptionChoice{{ID: "a", Label: "A"}, {ID: "b", Label: "B"}},
		},
	}
	manager.mu.Unlock()

	manager.optionResponse(map[string]interface{}{
		"type": "ai.option.response", "session_id": "s1", "option_id": "opt_s1_2",
		"selected": []interface{}{"b"}, "custom_text": "",
	}, writer)

	// pending 被消费。
	manager.mu.Lock()
	pending := manager.sessions["s1"].pendingOption
	runSeq := manager.sessions["s1"].runSeq
	manager.mu.Unlock()
	if pending != nil {
		t.Fatalf("pendingOption not cleared, got %+v", pending)
	}
	if runSeq != 3 {
		t.Fatalf("runSeq = %d, want 3 (new run dispatched)", runSeq)
	}
	// 新 run 派发后，要么 ai.run.started，要么因环境无 CLI 而 ai.error；二者至少有一个。
	started := lastAIEvent(mu, events, "ai.run.started")
	errEv := lastAIEvent(mu, events, "ai.error")
	if started == nil && errEv == nil {
		t.Fatal("expected either ai.run.started or ai.error after option followup dispatch")
	}
}

func TestOptionResponseNoMatchIsHarmless(t *testing.T) {
	manager := newAgentAIManager()
	defer manager.closeAll()
	mu, events, writer := captureAIWriter(t)
	manager.mu.Lock()
	manager.sessions["s1"] = &agentAISession{id: "s1", runSeq: 2}
	manager.mu.Unlock()
	// option_id 不匹配 / 无 pending → 不派发、不发 run。
	manager.optionResponse(map[string]interface{}{
		"type": "ai.option.response", "session_id": "s1", "option_id": "opt_unknown",
		"selected": []interface{}{"a"},
	}, writer)
	if got := findAIEvents(mu, events, "ai.run.started"); len(got) != 0 {
		t.Fatalf("expected no run dispatched for unmatched option")
	}
}
```

- [ ] **Step 2：运行测试确认失败**

Run: `go test ./app/http/services/ -run 'TestOptionResponse' -v`
Expected: FAIL（`undefined: optionResponse`）。

- [ ] **Step 3：重构 `message()` 抽出 `runUserMessage`**

把 `message()`（agent_ai.go:368-452）中"normalize provider 成功之后 → `go m.runCLI` 之前"这段（锁内更新 session + 构造 run + 锁外 startWatchdog/runCLI）抽成方法。重构后 `message()` 只负责：参数校验、查 session、检查未在跑、normalize provider，然后委托 `runUserMessage`。

新方法（放在 `message()` 之后）：

```go
// runUserMessage 在 session 上派发一轮新的 AI run。message()（用户消息）与
// optionResponse()（用户方案选择续接）共用。调用者须已确认 session 存在且当前未在跑。
// 返回 error 时由调用方发 ai.error。
func (m *agentAIManager) runUserMessage(session *agentAISession, messageID, content, provider string, writeJSON agentTerminalWriter) error {
	approvalToken, err := newAgentAIApprovalToken()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	activity := newAgentAIActivity()
	now := time.Now().UTC()

	m.mu.Lock()
	session.cancel = cancel
	session.activeWriter = writeJSON
	session.approvalToken = approvalToken
	session.activity = activity
	session.runSeq++
	resumeSessionID := strings.TrimSpace(session.resumeSessionID)
	session.history = append(session.history, agentAIMessage{
		Role: "user", MessageID: messageID, Content: content, CreatedAt: now,
	})
	prompt := buildAgentAIPrompt(session, content)
	if resumeSessionID != "" {
		prompt = content
	}
	run := agentAIRun{
		sessionID: session.id, messageID: messageID, runSeq: session.runSeq,
		mode: session.mode, projectPath: session.projectPath, provider: provider, model: session.model,
		resumeSessionID: resumeSessionID, prompt: prompt, cancel: cancel,
		approvalToken: approvalToken, activity: activity,
	}
	m.mu.Unlock()

	m.startAIWatchdog(ctx, activity, cancel)
	go m.runCLI(ctx, run, writeJSON)
	return nil
}
```

`message()` 重构后（保留原有校验，锁内段精简为"查 session + 未在跑检查 + normalize"，随后调 `runUserMessage`）：

```go
func (m *agentAIManager) message(msg map[string]interface{}, writeJSON agentTerminalWriter) {
	if writeJSON == nil {
		return
	}
	sessionID := remoteString(msg, "session_id")
	messageID := remoteString(msg, "message_id")
	content := strings.TrimSpace(remoteString(msg, "content"))
	if sessionID == "" {
		_ = writeJSON(agentAIErrorPayload("", messageID, errors.New("ai.message missing session_id")))
		return
	}
	if messageID == "" {
		messageID = sessionID
	}
	if content == "" {
		_ = writeJSON(agentAIErrorPayload(sessionID, messageID, errors.New("ai.message content is empty")))
		return
	}
	if len(content) > agentAIMessageLimitBytes {
		_ = writeJSON(agentAIErrorPayload(sessionID, messageID, fmt.Errorf("ai.message exceeds %d bytes", agentAIMessageLimitBytes)))
		return
	}

	m.mu.Lock()
	session := m.sessions[sessionID]
	if session == nil {
		m.mu.Unlock()
		_ = writeJSON(agentAIErrorPayload(sessionID, messageID, fmt.Errorf("ai session not found: %s", sessionID)))
		return
	}
	if session.cancel != nil {
		m.mu.Unlock()
		_ = writeJSON(agentAIErrorPayload(sessionID, messageID, fmt.Errorf("ai session is already running: %s", sessionID)))
		return
	}
	m.mu.Unlock()

	provider, err := normalizeAgentAIProvider(firstNonEmpty(strings.TrimSpace(remoteString(msg, "provider")), strings.TrimSpace(remoteString(msg, "tool")), session.provider))
	if err != nil {
		_ = writeJSON(agentAIErrorPayload(sessionID, messageID, err))
		return
	}

	if err := m.runUserMessage(session, messageID, content, provider, writeJSON); err != nil {
		_ = writeJSON(agentAIErrorPayload(sessionID, messageID, err))
	}
}
```

> ⚠️ 重构后必须跑现有 AI 测试（`go test ./app/http/services/ -run AgentAI`）确保 approval/正常聊天零回归。`runUserMessage` 的逻辑与原 `message()` 锁内段逐行等价（仅把 `prompt=content` 的 resume 分支也并入），审批 token/ctx/activity/history/runSeq 更新完全一致。

- [ ] **Step 4：实现 `optionResponse`**

新增（放在 `optionResponse` 与 `approval` 同段）：

```go
// optionResponse 处理 server→agent 的 ai.option.response：匹配 session 上的 pendingOption，
// 把用户选择拼成新 user prompt，续接一轮 run（--resume）。option_id 不匹配/无 pending 时静默。
func (m *agentAIManager) optionResponse(msg map[string]interface{}, writeJSON agentTerminalWriter) {
	if writeJSON == nil {
		return
	}
	sessionID := remoteString(msg, "session_id")
	optionID := strings.TrimSpace(remoteString(msg, "option_id"))
	if sessionID == "" || optionID == "" {
		_ = writeJSON(agentAIErrorPayload(sessionID, remoteString(msg, "message_id"), errors.New("ai.option.response missing session_id or option_id")))
		return
	}

	m.mu.Lock()
	session := m.sessions[sessionID]
	if session == nil || session.cancel != nil || session.pendingOption == nil || session.pendingOption.ID != optionID {
		m.mu.Unlock()
		// 无匹配 pending（可能 run 已被覆盖/重复投递）→ 安全忽略。
		return
	}
	pending := session.pendingOption
	session.pendingOption = nil
	provider := session.provider
	m.mu.Unlock()

	selected := remoteStringSlice(msg, "selected")
	custom := strings.TrimSpace(remoteString(msg, "custom_text"))
	content := buildAgentAIOptionFollowup(pending, selected, custom)
	messageID := pending.MessageID + ".option" // pending.MessageID 已是 assistant id（emitOptionRequest 设过 agentAssistantMessageID）

	if err := m.runUserMessage(session, messageID, content, provider, writeJSON); err != nil {
		_ = writeJSON(agentAIErrorPayload(sessionID, messageID, err))
	}
}
```

需要的小工具 `remoteStringSlice`（若不存在则新增，放在 `remoteString` 附近）：

```go
func remoteStringSlice(msg map[string]interface{}, key string) []string {
	raw, ok := msg[key].([]interface{})
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		if s := strings.TrimSpace(fmt.Sprint(item)); s != "" {
			out = append(out, s)
		}
	}
	return out
}
```

- [ ] **Step 5：WS 入口加分支（外层 + 内层两处都要改，否则外层 fall-through 到 default 报错）**

`app/http/services/agent_remote_ws.go` 的 `handleRemoteAgentMessage`（外层 switch 从 :305 开始）。注意 `ai.approval.response` 这类"用户响应"事件走的是**嵌套结构**：外层一个合并 case（:355 `case models.AgentEventAISessionCreate, models.AgentEventAIMessage, models.AgentEventAIApprovalResponse, models.AgentEventAIStop, models.AgentEventAISessionClose:`）进入后，里面还有一个 `switch msgType` 内层分发（:357-380）。因此必须改三处：

(a) **外层 case 列表**（:355 那一行）追加 `models.AgentEventAIOptionResponse` —— 否则外层 switch 命中末尾 `default`，发 `"unsupported remote agent event type"` 错误。

(b) **内层 switch**（与 `case models.AgentEventAIApprovalResponse:` 并列）增加：

```go
case models.AgentEventAIOptionResponse:
	if !s.aiControlEnabled() {
		_ = writeJSON(agentAIErrorPayload(remoteString(msg, "session_id"), remoteString(msg, "message_id"), errors.New("AI control is disabled for this device")))
		return
	}
	s.ai.optionResponse(msg, writeJSON)
```

(c) 把 `models.AgentEventAIOptionResponse` 加入 `remoteAgentMessageRequiresEnabledDevice`（:398）的 switch（option 续接需要 enabled device）。

- [ ] **Step 6：运行测试确认通过 + 回归**

Run: `go test ./app/http/services/ -run 'TestOptionResponse' -v`
Expected: PASS。
Run: `go test ./app/http/services/ -run AgentAI -v`
Expected: PASS（approval / watchdog / writer 等现有测试零回归）。

- [ ] **Step 7：commit（⚠️ 需用户确认）**

```bash
git add app/http/services/agent_ai.go app/http/services/agent_remote_ws.go app/http/services/agent_ai_option_test.go
git commit -m "feat(agent): resume run on ai.option.response via shared runUserMessage"
```

---

## Task 5：close / 失活清理 pendingOption（cancelled 通知）

**Files:**
- Modify: `app/http/services/agent_ai.go`（`close`、`clearRunning`、新增 `emitOptionCancelledLocked`）
- Test: `app/http/services/agent_ai_option_test.go`

- [ ] **Step 1：先写失败测试**

```go
func TestOptionCancelledEmittedOnClose(t *testing.T) {
	manager := newAgentAIManager()
	defer manager.closeAll()
	mu, events, writer := captureAIWriter(t)

	manager.mu.Lock()
	manager.sessions["s5"] = &agentAISession{id: "s5", runSeq: 1, pendingOption: &agentAIOptionRequest{ID: "opt_c"}}
	manager.mu.Unlock()

	manager.close(map[string]interface{}{"type": "ai.session.close", "session_id": "s5"}, writer)

	cancelled := lastAIEvent(mu, events, "ai.option.cancelled")
	if cancelled == nil || cancelled["session_id"] != "s5" {
		t.Fatalf("expected ai.option.cancelled on close, got %+v", cancelled)
	}
	ids, _ := cancelled["option_ids"].([]string)
	if len(ids) != 1 || ids[0] != "opt_c" {
		t.Fatalf("cancelled option_ids = %v, want [opt_c]", ids)
	}
	manager.mu.Lock()
	pendingAfter := manager.sessions["s5"].pendingOption
	manager.mu.Unlock()
	if pendingAfter != nil {
		t.Fatal("pendingOption was not cleared on close")
	}
}
```

- [ ] **Step 2：运行确认失败**

Run: `go test ./app/http/services/ -run 'TestOptionCancelledEmittedOnClose' -v`
Expected: FAIL（无 cancelled 事件）。

- [ ] **Step 3：实现清理 + 通知**

参照现有 `clearPendingApprovalsLocked`（返回被取消的 approval id）+ `close` 发 `ai.approval.cancelled` 的模式（见 `agent_ai_approval_proto_test.go` 的 TestAgentAIApprovalCancelledEmittedOnClose）。新增：

```go
// emitOptionCancelled 在 session 失活（close/run 结束被丢弃）时，best-effort 通知服务端作废待选。
func (m *agentAIManager) emitOptionCancelled(writeJSON agentTerminalWriter, sessionID string, optionIDs []string, reason string) {
	if writeJSON == nil || len(optionIDs) == 0 {
		return
	}
	_ = writeJSON(map[string]interface{}{
		"type":        models.AgentEventAIOptionCancelled,
		"session_id":  sessionID,
		"option_ids":  optionIDs,
		"reason":      reason,
	})
}
```

在 `close()`（session 关闭路径）里，清 session 前收集 `pendingOption.ID`，清空 `session.pendingOption`，随后 `emitOptionCancelled`（与现有 `emitApprovalCancelled` 并列调用）。同样在 `clearRunning()`（run 结束但未匹配到 option response 的兜底，例如下一轮启动覆盖前）里，若仍有未消费的 pendingOption，按 "superseded" 作废。

> 实现时先读 `close()` 与 `clearRunning()` 当前实现（用 find_symbol），在其"清理 pending approvals"的同一处并列处理 pendingOption，保持锁语义一致（收集在锁内、发送在锁外）。

- [ ] **Step 4：运行确认通过 + 回归**

Run: `go test ./app/http/services/ -run 'TestOptionCancelledEmittedOnClose|AgentAI' -v`
Expected: PASS（含现有 approval cancelled 回归）。

- [ ] **Step 5：commit（⚠️ 需用户确认）**

```bash
git add app/http/services/agent_ai.go app/http/services/agent_ai_option_test.go
git commit -m "feat(agent): emit ai.option.cancelled and clear pendingOption on session close"
```

---

## Task 6：Claude system prompt 引导（输出 aliang-options 块）

**Files:**
- Modify: `app/http/services/agent_ai.go`（`newClaudeCodeAITool`）

- [ ] **Step 1：定义引导常量**

在 agent_ai.go 顶部常量区新增：

```go
// agentAIOptionSystemPrompt 引导 Claude 在需要用户多方案抉择时，输出结构化 aliang-options 块，
// 以便 agent 提取并发 ai.option.request。仅在 Claude 路径注入。
const agentAIOptionSystemPrompt = `When you have multiple viable approaches/options and the best choice depends on user preference, you MUST let the user choose instead of deciding for them. Present options using a fenced code block with language "aliang-options" containing one JSON object on its own line, shaped exactly: {"title":string,"options":[{"id":string,"label":string,"description":string}],"allow_custom":bool,"multi":bool}. Keep "id" short stable slugs. After the block, stop and wait for the user's choice in the next message; do not proceed on assumption.`
```

- [ ] **Step 2：在 `newClaudeCodeAITool` 注入**

`newClaudeCodeAITool`（agent_ai.go:1892）的 `args` 构造里，在 `--include-partial-messages` 之后、`--resume`/`--model`/prompt 之前追加：

```go
	args = append(args, "--append-system-prompt", agentAIOptionSystemPrompt)
```

> 仅 Claude 分支（`newClaudeCodeAITool`）。Codex 路径（`resolveNamedAgentAITool` 的 codex 分支）本 plan 不动，留作后续。

- [ ] **Step 3：编译 + 现有测试回归**

Run: `go build ./app/http/services/... && go test ./app/http/services/ -run AgentAI`
Expected: 编译通过；现有测试 PASS。

- [ ] **Step 4：commit（⚠️ 需用户确认）**

```bash
git add app/http/services/agent_ai.go
git commit -m "feat(agent): inject system prompt so Claude emits aliang-options blocks"
```

---

## Task 7：集成验证

- [ ] **Step 1：全量编译 + vet**

Run: `go build ./cmd/... ./app/... ./processor/...`
Expected: 通过。
Run: `go vet ./app/http/services/... ./app/http/models/...`
Expected: 通过。

- [ ] **Step 2：option 专项 + services 全量测试**

Run: `go test ./app/http/services/ -run 'Option|AgentAI' -v`
Expected: PASS。
Run: `go test ./app/http/services/`
Expected: 除既有无关失败 `TestQuickSetupService_Render_MultiProvider` 外全绿（与设计文档基线一致）。

- [ ] **Step 3：手工联调（可选，需 Claude CLI）**

在本机起 agent，发一条会被 AI 拆成多方案的 prompt（如"给我两种缓存实现方案，让我选"），观察 agent 日志是否发出 `ai.option.request`；构造一条 `ai.option.response`（可用 wscat 连本地 /ws/agent 或单元桩）验证续接成新一轮 run。

- [ ] **Step 4：commit（⚠️ 需用户确认）**

无代码改动则跳过；若联调发现小修，单独提交。

---

## 契约附录：服务端（AliangPhoneServer）与手机端（后续 plan）

**AliangPhoneServer（TS，`/Users/mac/MyProgram/AiProgram/vibe_on_phone/AliangPhoneServer`）：**
- inbound：`ai.option.request`（落库 + 推移动端，可套现有 `AIApprovalDelivery` 引擎：以 `option_id` 为 key，带 `delivery_id` 重投/ack）；`ai.option.cancelled`。
- respond 端点：`ai.option.response`（用户决策 → 带 `delivery_id` 投给目标设备 agent）。
- 落库：扩展或新增 option 表（`status/decided_by/decided_at/selected[]/custom_text/delivery_id/ack_status/...`）。

**手机端：**
- 收 `ai.option.request` → 渲染选择 UI（选项卡片列表 + 可选自定义输入框 + 多选开关 + submit）。
- submit → 调 respond 端点发 `ai.option.response`。
- （可选）聊天流里隐藏 `aliang-options` 代码块，只展示其前的方案描述文字。

---

## 未做 / 已知限制（后续）

1. **delta 过滤**：方案块文本当前会随 `ai.delta` 流到手机端（用户可能看到 JSON 块）。后续可在 `streamStructuredAIDelta`/`emitAIDelta` 加状态机，缓冲并剥离 ```` ```aliang-options ```` 围栏段。
2. **可靠投递 ack/sync**：MVP 未做 `ai.option.ack/sync`（重连对账/重投），先靠 server 单向重投 + agent `option_id` 匹配幂等。待产品验证后再补，复用 approval 的 ack/sync 骨架。
3. **Codex 路径**：本 plan 只引导 Claude（`--append-system-prompt`）。Codex 的方案选择可复用其 `availableDecisions`/`requestApproval`，留作后续。
4. **一次 run 单选**：MVP 取第一个有效块。多块（连续多道选择题）后续再支持。

---

## 验证基线（来自既有 ai-approval-reliability-design.md）

- `go build ./cmd/... ./app/... ./processor/...` 通过。
- `go vet ./app/http/services/... ./app/http/models/...` 通过。
- `go test ./app/http/services/ -run AgentAI` 全绿。
- 全量 services 测试除既有 `TestQuickSetupService_Render_MultiProvider`（无关）外全绿。
