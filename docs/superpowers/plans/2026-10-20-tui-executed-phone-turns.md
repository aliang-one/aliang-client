# 手机消息路由到活着的 TUI — 实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 手机对某会话发消息时，若该会话的原生 TUI 正开着且空闲，消息改经 Claude Code 的 peer inbox UDS 注入该 TUI 执行；agent 通过 tail transcript 合成 `ai.run.started/ai.delta/ai.done` 回传手机，使 TUI 与手机同看一次对话、上下文天然连续。

**Architecture:** 唯一路由点插在 `runUserMessage`（`app/http/services/agent_ai.go:2139`）最前端——覆盖 `ai.message` 与 `ai.run.start` 两条入口（后者经 `m.message()` 汇入）。新文件 `agent_tui_turn.go` 承担「路由判定（纯函数）+ 注入 + tail 桥 + 生命周期合成」。复用全部既有资产：`liveClaudeTUIRecord`、`ccPeerDialInject`（注入旋钮）、`ccPeerTUIPermissionClass`、`ccPeerWatchHeld`、`ccPeerSessionJSONL`、`interruptExternalClaudeTurn`、`agentAIRunEmitter`。注入失败不回退 spawn——一次回合只走一条路。

**Tech Stack:** Go 1.25、标准库 `testing`、`net`（unix socket）、`os`/`bufio`（transcript tail）。

**语义权威：** spec `docs/superpowers/specs/2026-10-20-tui-executed-phone-turns-design.md`（三项用户决策：TUI 内审批 / 注入回合用 TUI 自身环境 / tail jsonl 合成 delta）。

**约定（本仓强制）：**
- 全中文提交标题「新增：/修复：/重构：」，页脚 `Co-Authored-By: Claude Code <noreply@anthropic.com>`；**未经用户明确要求不得 commit**（本计划 Task 0 由用户批准后建 worktree，各 Task 的 commit 步骤需用户点头才执行——若用户只要求实现不要求提交，则跳过 commit 步骤保留工作区）。
- 标准库 testing；`go test ./...` 有预存 flake（services 包 `TestSessionAuthorityProxyAdmissionLinearizesWithDemotion` 基线 5/10、3 个子进程超时测试），**单包复跑判定，勿当回归**。
- **测试命名守则**：函数名不得以 `_windows`/`_linux`/`_darwin` 结尾（隐式 GOOS 跳过陷阱）；新测试先 `go test -run <Name> -v` 确认真实执行。
- `Date.now`/随机源在测试中一律用固定值或 `t.TempDir()`，不依赖真实 `~/.claude`。

**已核实的关键事实（写代码前必须知道）：**
1. transcript 是**块级整行写入**：`apiBlockIndex` 递增、uuid 唯一不重写、文件以 `\n` 结尾（2026-10-20 本机 CC 2.1.286 实测 `duplicate_uuids = 0`）→ tail 桥产出的是**块级 delta**，非 token 级。spec §4.3 既定取舍。
2. transcript 的 `user` 行有噪声：`isMeta:true`（本地命令回显）、`<command-name>` 开头、`tool_result` block——tail 桥必须过滤这三类，否则会把 TUI 的本地命令当成对话内容回显。
3. `runStart()`（`agent_ai.go:1635`）末尾 `m.message(msg, writeJSON)`，故路由点只需一处。
4. `agentAIRunEmitter.emit()` 自动追加 `run_id`/`event_seq`、终态落盘（`rememberPendingTerminal` + `completeProcessedRun`）——注入路径只要经 emitter 发事件就自动获得断线补推，**无需自己写 journal**。
5. **⚠️ 注入必须用原始 `content`，不能用 `run.prompt`**：`runUserMessage` 里 `prompt = freshPrompt`（= `buildAgentAIPrompt`，agent_ai.go:6748，会包上 "You are continuing an Aliang remote agent AI chat session..." 前缀 + 全量历史），仅当 `resumeSessionID != ""` 时才 `prompt = content`。imported 会话碰巧有 resume id 所以碰巧对——但这是巧合不是设计，goal/fresh 场景会注入包装文本。**实现时 TUI 路径必须显式传 `content`**（`runUserMessage` 的入参，需透传到 `agentAIRun` 或 `runTUITurn`）。
6. **⚠️ 附件清单会丢**：spawn 路径靠 `withAgentAIAttachments`（agent_ai.go:7050）把附件路径拼进 prompt；注入路径必须自己拼 `agentAIAttachmentPromptSuffix(attachments, true)`，否则手机发图片/文件时 TUI 看不到。
7. **⚠️ 事件字段契约**（`app/http/models/agent_protocol.go:219-224` 必填项，服务端会校验）：
   - `ai.command` 必填 `item_id` + `status`（只发 `command` 会被判畸形）→ 用 `item_id = "tb<apiBlockIndex>"` 或 block uuid 前缀、`status = "started"`
   - `ai.thinking` 必填 `type/session_id/message_id/delta`；claude 路径的 thinking 走**独立事件 `ai.thinking`**（带 `item_id`，见 agent_ai.go:6282），**不走 ai.delta**
   - `ai.delta` 必填 `type/session_id/message_id/channel/delta`——字段名是 **`channel`**（spawn 路径用字面量 `"assistant"`，agent_ai.go:5355），**没有 `role` 字段**
   - `ai.error` 的错误码字段名是 `error_code`（`agentAIErrorPayload` 用 `errors.As` 拾取 `ErrorCode()`，agent_ai.go:7613-7617）
8. **⚠️ 测试构造三坑（已实测确认）**：
   - manager 必须用 `newAgentAIManager()`（`agent_ai.go:879`）。`&agentAIManager{}` 的 `m.sessions` 是 nil map，`registerAISessionLocked`（`agent_ai.go:919`）直接 panic「assignment to entry in nil map」。
   - unix socket 路径必须用现成的 `ccPeerShortSocketPath(t)`（`cc_peer_messaging_test.go:95`）：macOS `sun_path` 上限 103 字节，`t.TempDir()` 嵌了完整测试名，长测试名必爆 `bind: invalid argument`。
   - tail 循环必须有全局上限（`tuiTurnCeiling`），否则注入永不回显时 goroutine 永久占住 `session.cancel`，该会话再无法发起回合。
9. **⚠️ 必须补 `ai.run.progress` 心跳**：spawn 两条路径都有 10 秒心跳（`agentAIRunProgressInterval = 10s`，agent_execution_guard.go:75；codex 侧 agent_ai.go:4068、claude 侧 agent_ai.go:5102），否则 TUI 长时间思考时服务端的 10 分钟安静 sweeper 会把回合打翻成 timed out。注入回合在 tail 循环里必须同步起同间隔心跳。

---

### Task 0: worktree 环境准备

**Files:** 无代码改动

- [ ] **Step 1: 创建 worktree 分支**（主工作区执行）

```bash
git stash -u   # ⚠️ 先确认无并行会话同时 stash
git worktree add ../alianggate-tui-turn -b feat/tui-executed-phone-turns
git stash pop
```

若分支已存在：停止执行并报告，勿强推勿删除。

- [ ] **Step 2: 拷贝 spec 与计划并提交**

```bash
mkdir -p ../alianggate-tui-turn/docs/superpowers/specs ../alianggate-tui-turn/docs/superpowers/plans
cp docs/superpowers/specs/2026-10-20-tui-executed-phone-turns-design.md ../alianggate-tui-turn/docs/superpowers/specs/
cp docs/superpowers/plans/2026-10-20-tui-executed-phone-turns.md ../alianggate-tui-turn/docs/superpowers/plans/
cd ../alianggate-tui-turn
git add docs/superpowers/
git commit -m "$(cat <<'EOF'
新增：手机消息路由到活着的 TUI 设计文档与实现计划

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

---

### Task 1: 错误码常量与 payload 助手

**Files:**
- Create: `app/http/services/agent_tui_turn.go`（先只放错误类型）
- Test: `app/http/services/agent_tui_turn_test.go`

- [ ] **Step 1: 写失败测试**

```go
func TestTUITurnErrorCodes(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{newAgentTUITurnError(agentTUIErrUnavailable, "gone"), "tui_unavailable"},
		{newAgentTUITurnError(agentTUIErrInjectFailed, "dial"), "tui_inject_failed"},
		{newAgentTUITurnError(agentTUIErrHeld, "held"), "tui_held"},
		{newAgentTUITurnError(agentTUIErrTranscriptLost, "gone"), "tui_transcript_lost"},
	}
	for _, c := range cases {
		coder, ok := c.err.(interface{ ErrorCode() string })
		if !ok {
			t.Fatalf("%T 未实现 ErrorCode()", c.err)
		}
		if got := coder.ErrorCode(); got != c.want {
			t.Fatalf("ErrorCode() = %q, want %q", got, c.want)
		}
		if c.err.Error() == "" {
			t.Fatalf("Error() 不应为空")
		}
	}
}
```

- [ ] **Step 2: 运行测试验证失败**

Run: `cd ../alianggate-tui-turn && go test ./app/http/services/ -run TestTUITurnErrorCodes -v`
Expected: FAIL（`undefined: newAgentTUITurnError`）

- [ ] **Step 3: 实现错误类型**

```go
package services

import "fmt"

// TUI 执行回合的机器可读错误码。与 agentExternalTUIBusyError 同构：实现
// ErrorCode() interface，由 agentAIErrorPayload 自动拾取挂到 ai.error 上
// （见 services/agent_ai_errors.go 的 payload 构造），服务端与手机据此分支，
// 无需对错误字符串做脆弱匹配。
const (
	agentTUIErrUnavailable    = "tui_unavailable"     // 注入前判定中途失效（TUI 退出/记录消失）
	agentTUIErrInjectFailed   = "tui_inject_failed"   // 拨号或写入 UDS 失败
	agentTUIErrHeld           = "tui_held"            // 接收方（bypass 门）静默扣留注入消息
	agentTUIErrTranscriptLost = "tui_transcript_lost" // tail 期间 transcript 消失
)

// agentTUITurnError 是注入 TUI 回合的结构化失败。msg 面向人（日志/手机提示），
// code 面向程序。
type agentTUITurnError struct {
	code string
	msg  string
}

func (e agentTUITurnError) Error() string { return e.msg }
func (e agentTUITurnError) ErrorCode() string { return e.code }

func newAgentTUITurnError(code, msg string) error {
	return agentTUITurnError{code: code, msg: msg}
}
```

已核实（`agent_ai.go:7613-7617`）：`agentAIErrorPayload` 用 `errors.As` 拾取 `ErrorCode() interface` 并写入 `error_code` 字段——无需改 payload 构造。

- [ ] **Step 4: 运行测试验证通过**

Run: `go test ./app/http/services/ -run TestTUITurnErrorCodes -v`
Expected: PASS

- [ ] **Step 5: Commit**（需用户批准）

---

### Task 2: 路由判定纯函数 `tuiTurnDecide`

**Files:**
- Modify: `app/http/services/agent_tui_turn.go`
- Test: `app/http/services/agent_tui_turn_test.go`

- [ ] **Step 1: 写失败测试**

```go
func TestTUITurnDecideGates(t *testing.T) {
	lastPid := stubExternalInterrupt(t, nil) // 身份门 + 信号 stub + 去抖表清空
	pid := int(lastStubbedPid(lastPid))       // = os.Getpid()，真实存活（isPidAlive 不可覆写）
	home := t.TempDir()
	writeClaudePidRecord(t, home, "sess-1", pid, "idle", "/tmp/fake-inbox.sock")
	base := tuiTurnInput{
		Home: home, Provider: "claude", NativeSessionID: "sess-1",
		JSONLPath: writeTranscriptFixture(t, home, "sess-1"),
	}
	// 全部满足 → 有 plan
	if plan := tuiTurnDecide(base); plan == nil {
		t.Fatal("全部条件满足时应返回 plan")
	}
	// provider 非 claude
	p := base; p.Provider = "codex"
	if plan := tuiTurnDecide(p); plan != nil { t.Fatal("codex 不应注入") }
	// auto 允许（imported session 默认值）
	p = base; p.Provider = "auto"
	if plan := tuiTurnDecide(p); plan == nil { t.Fatal("auto 应允许") }
	// 无 native id
	p = base; p.NativeSessionID = ""
	if plan := tuiTurnDecide(p); plan != nil { t.Fatal("fresh spawn 不应注入") }
	// readOnly
	p = base; p.ReadOnly = true
	if plan := tuiTurnDecide(p); plan != nil { t.Fatal("readOnly 不应注入") }
	// goal 回合
	p = base; p.HasGoalIdentity = true
	if plan := tuiTurnDecide(p); plan != nil { t.Fatal("goal 回合不应注入") }
	// busy
	writeClaudePidRecord(t, home, "sess-1", pid, "busy", "/tmp/fake-inbox.sock")
	if plan := tuiTurnDecide(base); plan != nil { t.Fatal("busy 不应注入") }
	// 无 socket
	writeClaudePidRecord(t, home, "sess-1", pid, "idle", "")
	if plan := tuiTurnDecide(base); plan != nil { t.Fatal("无 socket 不应注入") }
	// record 不存在
	if plan := tuiTurnDecide(tuiTurnInput{Home: home, Provider: "claude", NativeSessionID: "nope", JSONLPath: base.JSONLPath}); plan != nil {
		t.Fatal("无记录不应注入")
	}
}
```

配套 fixture（同文件）：

```go
// writeClaudePidRecord 写 ~/.claude/sessions/<pid>.json，形状对齐真实 CC 记录
// （loadClaudeRenameRecords 只 glob "*.json"，键名必须与之一致）。
func writeClaudePidRecord(t *testing.T, home, sessionID string, pid int, status, socket string) {
	t.Helper()
	dir := filepath.Join(home, ".claude", "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil { t.Fatal(err) }
	row := map[string]any{"sessionId": sessionID, "pid": pid, "status": status, "messagingSocketPath": socket}
	b, _ := json.Marshal(row)
	if err := os.WriteFile(filepath.Join(dir, strconv.Itoa(pid)+".json"), b, 0o600); err != nil { t.Fatal(err) }
}

// transcriptFixtureDir 是 fixture 统一使用的 project 目录名。fake TUI 追加行时
// 必须用同一个目录，否则 appendLine 会写进一个不存在的目录。
const transcriptFixtureDir = "-tmp-proj"

// transcriptFixturePath 返回 ~/.claude/projects/<transcriptFixtureDir>/<id>.jsonl。
func transcriptFixturePath(home, sessionID string) string {
	return filepath.Join(home, ".claude", "projects", transcriptFixtureDir, sessionID+".jsonl")
}

// writeTranscriptFixture 写 fixture transcript，含一条 user 行让 tail 桥有起点
// （ccPeerSessionJSONL 依赖该布局）。
func writeTranscriptFixture(t *testing.T, home, sessionID string) string {
	t.Helper()
	path := transcriptFixturePath(home, sessionID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { t.Fatal(err) }
	row := `{"type":"user","uuid":"u-1","isSidechain":false,"isMeta":false,"message":{"role":"user","content":"hi"}}` + "\n"
	if err := os.WriteFile(path, []byte(row), 0o600); err != nil { t.Fatal(err) }
	return path
}

// appendTranscriptLine 追加一行到 fixture transcript。**禁止 t.Fatal**：它会被
// fake TUI 的 accept goroutine 调用，在非测试 goroutine 里 Fatal 是 Go testing
// 违规（直接让整个进程退出，且不报告是哪个用例失败）。错误只记日志。
func appendTranscriptLine(t *testing.T, path, line string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		logger.Info(fmt.Sprintf("test fixture append failed path=%s error=%v", path, err))
		return
	}
	defer f.Close()
	if _, err := f.WriteString(line + "\n"); err != nil {
		logger.Info(fmt.Sprintf("test fixture write failed path=%s error=%v", path, err))
	}
}
```

**⚠️ pid 必须真实存活，且必须复用现成的 stub 助手。** `liveClaudeTUIRecord` 走两道门：`isPidAlive(pid)`（`syscall.Kill(pid,0)`）与 `externalInterruptTargetMatches(pid)`。前者是**普通函数、不可覆写**（`agent_rename_liveness_unix.go:11`），所以测试里的 pid **必须用 `os.Getpid()`**——用一个死 pid（如 4242）会让所有用例恒判 not-live。后者是可变旋钮。

包内已有现成助手（`agent_external_interrupt_test.go:38` `stubExternalInterrupt`）同时 stub 了信号与匹配并清空去抖表，**直接复用，不要另写**：

```go
func TestTUITurnDecideGates(t *testing.T) {
	lastPid := stubExternalInterrupt(t, nil) // 内含 externalInterruptTargetMatches = func(int) bool { return true }
	pid := int(lastStubbedPid(lastPid))       // = os.Getpid()，真实存活
	home := t.TempDir()
	writeClaudePidRecord(t, home, "sess-1", pid, "idle", "/tmp/fake-inbox.sock")
	...
}
```

> 因此本计划所有测试里的 pid 一律取 `os.Getpid()`（经 `stubExternalInterrupt`/`lastStubbedPid` 取到），**不得写死常量**。Task 5/7/8 的示例代码同样按此修正。

- [ ] **Step 2: 运行测试验证失败**

Run: `go test ./app/http/services/ -run TestTUITurnDecideGates -v`
Expected: FAIL（`undefined: tuiTurnDecide`）

- [ ] **Step 3: 实现路由判定**

```go
// tuiTurnInput 是路由判定的输入快照。全部字段在调度前一次性取到，避免判定
// 过程中读可变状态。
type tuiTurnInput struct {
	Home             string
	Provider         string
	NativeSessionID  string
	ReadOnly         bool
	HasGoalIdentity  bool
	JSONLPath        string // 调用方经 ccPeerSessionJSONL 解析，空则由本函数自行解析
}

// tuiTurnPlan 描述一次「注入 TUI」的执行计划。
type tuiTurnPlan struct {
	Home            string
	NativeSessionID string
	SocketPath      string
	PID             int
	JSONLPath       string
	PeerToken       string
	FromMode        string
	JSONLOffset     int64
}

// tuiTurnDecide 实现 spec §4.2 的七道门。任一不满足返回 nil（调用方落回
// spawn 路径）。这是纯判定：不做任何 I/O 之外的副作用，便于单测全覆盖。
func tuiTurnDecide(in tuiTurnInput) *tuiTurnPlan {
	// ① provider：claude/claudecode/auto。入参已被 normalizeAgentAIProvider
	// （agent_execution_guard.go:438）归一化成 auto|codex|claude|claudecode|
	// opencode 小写，这里再做一次 Trim/ToLower 是防御拷贝。
	// ⚠️ 已知限制：auto 在 spawn 路径可能解析到 codex，但注入路径只会命中
	// claude 的 pid 记录——即「auto + 存在空闲 claude TUI」时回合固定走 TUI。
	// 这是有意的（用户决策 2：注入回合用 TUI 自身环境），但若用户显式选了
	// codex，服务端下发的 provider 就是 codex，本函数直接返回 nil 走 spawn。
	switch strings.TrimSpace(strings.ToLower(in.Provider)) {
	case "claude", "claudecode", "auto":
	default:
		return nil
	}
	// ② 必须有 native id：fresh spawn 没有可注入的对话
	nativeID := strings.TrimSpace(in.NativeSessionID)
	if nativeID == "" { return nil }
	// ③ goal/fork 回合有沙箱与报告契约，一律不进 TUI 路径
	if in.ReadOnly || in.HasGoalIdentity { return nil }
	home := strings.TrimSpace(in.Home)
	if home == "" { return nil }
	// ④⑤ live 记录 + idle（busy 交给 guardExternalTUIClaudeSpawn 的 tui_busy）
	record, live := liveClaudeTUIRecord(home, nativeID)
	if !live { return nil }
	if !strings.EqualFold(strings.TrimSpace(record.Status), "idle") { return nil }
	// ⑥ unix UDS 能力（Windows named pipe 是 v2，明确不支持）
	socketPath := strings.TrimSpace(record.MessagingSocketPath)
	if socketPath == "" || !strings.HasPrefix(socketPath, "/") { return nil }
	// ⑦ transcript 可定位：tail 桥的前提
	jsonlPath := strings.TrimSpace(in.JSONLPath)
	if jsonlPath == "" { jsonlPath = ccPeerSessionJSONL(home, nativeID) }
	if jsonlPath == "" { return nil }
	info, err := os.Stat(jsonlPath)
	if err != nil || info.Size() <= 0 { return nil }
	return &tuiTurnPlan{
		Home: home, NativeSessionID: nativeID, SocketPath: socketPath,
		PID: record.PID, JSONLPath: jsonlPath,
		PeerToken: loadClaudePeerToken(home, record.PID),
		FromMode:  ccPeerTUIPermissionClass(jsonlPath),
		JSONLOffset: info.Size(),
	}
}
```

- [ ] **Step 4: 运行测试验证通过**

Run: `go test ./app/http/services/ -run TestTUITurnDecideGates -v`
Expected: PASS

- [ ] **Step 5: Commit**（需用户批准）

---

### Task 3: transcript 增量分类器（纯函数 + 测试先行）

**Files:**
- Modify: `app/http/services/agent_tui_turn.go`
- Test: `app/http/services/agent_tui_turn_test.go`

- [ ] **Step 1: 写失败测试**

```go
func TestClassifyTranscriptLine(t *testing.T) {
	cases := []struct {
		name string
		line string
		want transcriptLineKind
	}{
		{"assistant text", `{"type":"assistant","uuid":"a1","isSidechain":false,"apiBlockIndex":1,"message":{"role":"assistant","content":[{"type":"text","text":"你好"}]}}`, transcriptAssistant},
		{"assistant thinking", `{"type":"assistant","uuid":"a2","isSidechain":false,"message":{"content":[{"type":"thinking","thinking":"想"}]}}`, transcriptAssistant},
		{"assistant tool_use", `{"type":"assistant","uuid":"a3","isSidechain":false,"message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"ls"}}]}}`, transcriptAssistant},
		{"sidechain 忽略", `{"type":"assistant","uuid":"a4","isSidechain":true,"message":{"content":[{"type":"text","text":"子代理"}]}}`, transcriptIgnore},
		{"真实 user 回显", `{"type":"user","uuid":"u1","isMeta":false,"message":{"role":"user","content":"问题"}}`, transcriptUserEcho},
		{"tool_result 忽略", `{"type":"user","uuid":"u2","message":{"role":"user","content":[{"type":"tool_result","content":"ok"}]}}`, transcriptIgnore},
		{"isMeta 忽略", `{"type":"user","uuid":"u3","isMeta":true,"message":{"role":"user","content":"<command-name>x</command-name>"}}`, transcriptIgnore},
		{"本地命令回显忽略", `{"type":"user","uuid":"u4","message":{"role":"user","content":"<command-name>/effort</command-name>"}}`, transcriptIgnore},
		{"空行", ``, transcriptIgnore},
		{"坏 JSON", `{not json`, transcriptIgnore},
		{"metadata 行", `{"type":"mode","mode":"normal","sessionId":"s"}`, transcriptIgnore},
	}
	for _, c := range cases {
		if got := classifyTranscriptLine([]byte(c.line)); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}
```

- [ ] **Step 2: 运行测试验证失败**

Run: `go test ./app/http/services/ -run TestClassifyTranscriptLine -v`
Expected: FAIL（undefined）

- [ ] **Step 3: 实现分类器**

```go
// transcriptLineKind 是 tail 桥对 transcript 一行的分类。
type transcriptLineKind int

const (
	transcriptIgnore transcriptLineKind = iota // 元数据/噪声/空/坏行
	transcriptUserEcho                         // 真实用户消息（含我们注入的那条）
	transcriptAssistant                        // 主链 assistant 块
)

// classifyTranscriptLine 按 spec §4.3-5 过滤。transcript 的 user 行有三类噪声
// （2026-10-20 实测）：isMeta 本地命令回显、<command-name> 开头内容、
// tool_result block——不过滤就会把 TUI 的本地命令当成对话回显到手机。
func classifyTranscriptLine(line []byte) transcriptLineKind {
	if len(bytes.TrimSpace(line)) == 0 { return transcriptIgnore }
	var row struct {
		Type       string `json:"type"`
		UUID       string `json:"uuid"`
		IsSidechain bool  `json:"isSidechain"`
		IsMeta     bool   `json:"isMeta"`
		Message    struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(line, &row); err != nil { return transcriptIgnore }
	switch row.Type {
	case "user":
		if row.IsMeta { return transcriptIgnore }
		if row.IsSidechain { return transcriptIgnore }
		// content 为字符串时按前缀过滤本地命令回显；为数组时只接受 text block。
		var text string
		if err := json.Unmarshal(row.Message.Content, &text); err == nil {
			if strings.HasPrefix(strings.TrimSpace(text), "<command-name>") ||
				strings.HasPrefix(strings.TrimSpace(text), "<local-command") ||
				strings.HasPrefix(strings.TrimSpace(text), "<local-command-stdout>") {
				return transcriptIgnore
			}
			return transcriptUserEcho
		}
		var blocks []struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(row.Message.Content, &blocks); err != nil { return transcriptIgnore }
		for _, b := range blocks {
			if b.Type == "tool_result" { return transcriptIgnore }
		}
		// 非 tool_result 的数组内容（罕见）按真实用户消息处理
		return transcriptUserEcho
	case "assistant":
		if row.IsSidechain { return transcriptIgnore }
		if strings.TrimSpace(row.UUID) == "" { return transcriptIgnore }
		return transcriptAssistant
	default:
		return transcriptIgnore
	}
}
```

- [ ] **Step 4: 运行测试验证通过**

Run: `go test ./app/http/services/ -run TestClassifyTranscriptLine -v`
Expected: PASS（含 sidechain/tool_result/isMeta 全部分支）

- [ ] **Step 5: Commit**（需用户批准）

---

### Task 4: transcript 块提取（assistant content → 事件载荷）

**Files:**
- Modify: `app/http/services/agent_tui_turn.go`
- Test: `app/http/services/agent_tui_turn_test.go`

- [ ] **Step 1: 写失败测试**

```go
func TestExtractTranscriptBlocks(t *testing.T) {
	line := []byte(`{"type":"assistant","uuid":"a1","apiBlockIndex":2,"message":{"role":"assistant","content":[{"type":"thinking","thinking":"思考"},{"type":"text","text":"答案"},{"type":"tool_use","name":"Bash","input":{"command":"ls"}}]}}`)
	blocks := extractTranscriptBlocks(line)
	if len(blocks) != 3 { t.Fatalf("got %d blocks, want 3", len(blocks)) }
	if blocks[0].Kind != "thinking" || blocks[0].Text != "思考" { t.Errorf("block0 = %+v", blocks[0]) }
	if blocks[1].Kind != "text" || blocks[1].Text != "答案" { t.Errorf("block1 = %+v", blocks[1]) }
	if blocks[2].Kind != "tool_use" || blocks[2].ToolName != "Bash" { t.Errorf("block2 = %+v", blocks[2]) }
	// 非 assistant 行 → 无块
	if got := extractTranscriptBlocks([]byte(`{"type":"user","message":{"content":"x"}}`)); got != nil {
		t.Errorf("user 行应无块, got %+v", got)
	}
}

func TestToolUseTouchedPaths(t *testing.T) {
	blocks := []transcriptBlock{
		{Kind: "tool_use", ToolName: "Write", ToolInput: json.RawMessage(`{"file_path":"/a/b.go"}`)},
		{Kind: "tool_use", ToolName: "Edit", ToolInput: json.RawMessage(`{"file_path":"/a/b.go"}`)},
		{Kind: "tool_use", ToolName: "Bash", ToolInput: json.RawMessage(`{"command":"ls"}`)},
	}
	paths := toolUseTouchedPaths(blocks)
	if len(paths) != 1 || paths[0] != "/a/b.go" {
		t.Fatalf("paths = %v, want [/a/b.go]", paths)
	}
}
```

- [ ] **Step 2: 运行测试验证失败**

Run: `go test ./app/http/services/ -run 'TestExtractTranscriptBlocks|TestToolUseTouchedPaths' -v`
Expected: FAIL（undefined）

- [ ] **Step 3: 实现**

```go
// transcriptBlock 是一条 assistant 行解出的一个内容块。transcript 是块级整行
// 写入（spec §4.3 已核实），故这是 delta 的天然粒度。
type transcriptBlock struct {
	Kind      string // "thinking" | "text" | "tool_use"
	Text      string
	ToolName  string
	ToolInput json.RawMessage
}

func extractTranscriptBlocks(line []byte) []transcriptBlock {
	if classifyTranscriptLine(line) != transcriptAssistant { return nil }
	var row struct {
		Message struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(line, &row); err != nil { return nil }
	var raw []struct {
		Type     string          `json:"type"`
		Text     string          `json:"text"`
		Thinking string          `json:"thinking"`
		Name     string          `json:"name"`
		Input    json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(row.Message.Content, &raw); err != nil { return nil }
	out := make([]transcriptBlock, 0, len(raw))
	for _, b := range raw {
		switch b.Type {
		case "thinking":
			out = append(out, transcriptBlock{Kind: "thinking", Text: b.Thinking})
		case "text":
			out = append(out, transcriptBlock{Kind: "text", Text: b.Text})
		case "tool_use":
			out = append(out, transcriptBlock{Kind: "tool_use", ToolName: b.Name, ToolInput: b.Input})
		}
	}
	return out
}

// toolUseTouchedPaths 从 tool_use 块提取被改文件路径（去重），供 ai.done 的
// files_touched_count 使用。键名与 spawn 路径的 fileSink 语义对齐。
func toolUseTouchedPaths(blocks []transcriptBlock) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, b := range blocks {
		if b.Kind != "tool_use" { continue }
		var in struct {
			FilePath string `json:"file_path"`
			Path     string `json:"path"`
			Notebook string `json:"notebook_path"`
		}
		if err := json.Unmarshal(b.ToolInput, &in); err != nil { continue }
		p := firstNonEmpty(in.FilePath, in.Path, in.Notebook)
		if p == "" { continue }
		if _, dup := seen[p]; dup { continue }
		seen[p] = struct{}{}
		out = append(out, p)
	}
	return out
}
```

先看 spawn 路径 `fileSink` 用了哪些键（`agent_ai.go` 的 `filesTouched` 喂入处），**保持一致**；若还有 `filename`/`filePath` 变体，一并加入。

- [ ] **Step 4: 运行测试验证通过**

Run: `go test ./app/http/services/ -run 'TestExtractTranscriptBlocks|TestToolUseTouchedPaths' -v`
Expected: PASS

- [ ] **Step 5: Commit**（需用户批准）

---

### Task 5: TUI 执行器主体（注入 + tail + 生命周期）

**Files:**
- Modify: `app/http/services/agent_tui_turn.go`
- Modify: `app/http/services/agent_ai.go`（`runUserMessage` 最前端插路由，约 12 行）
- Test: `app/http/services/agent_tui_turn_test.go`

- [ ] **Step 1: 写失败测试（happy path：帧内容 + 事件序列 + run_id/event_seq）**

```go
```go
func TestRunTUITurnHappyPath(t *testing.T) {
	origMatch := externalInterruptTargetMatches
	externalInterruptTargetMatches = func(int) bool { return true }
	defer func() { externalInterruptTargetMatches = origMatch }()
	// 心跳是 10s 间隔，测试里不必等它；但把轮询间隔压到最短加快用例。
	origPoll := tuiTurnPollInterval
	tuiTurnPollInterval = 20 * time.Millisecond
	defer func() { tuiTurnPollInterval = origPoll }()

	home := t.TempDir()
	sock := ccPeerShortSocketPath(t) // macOS sun_path 103 字节上限，t.TempDir()+测试名必爆
	// fake TUI：收帧后向 transcript 追加 user 回显 + text 块 + tool_use 块
	frames := make(chan string, 8)
	startFakeTUI(t, sock, frames, func(content string) {
		path := transcriptFixturePath(home, "sess-1")
		appendTranscriptLine(t, path, `{"type":"user","uuid":"echo","isMeta":false,"message":{"role":"user","content":`+jsonString(content)+`}}`)
		appendTranscriptLine(t, path, `{"type":"assistant","uuid":"a1","apiBlockIndex":0,"message":{"content":[{"type":"text","text":"收到："+`+jsonString(content)+`}]}}`)
		appendTranscriptLine(t, path, `{"type":"assistant","uuid":"a2","apiBlockIndex":1,"message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"echo hi","cwd":"/tmp"}}]}}`)
	})
	writeClaudePidRecord(t, home, "sess-1", pid, "idle", sock)
	jsonl := writeTranscriptFixture(t, home, "sess-1")

	m := newAgentAIManager() // agent_ai.go:879。⚠️ 不能用 &agentAIManager{}：其 m.sessions 是 nil map，registerAISessionLocked 会 panic
	var mu sync.Mutex
	var events []map[string]any
	write := func(v any) error {
		mu.Lock()
		defer mu.Unlock()
		if p, ok := v.(map[string]any); ok { events = append(events, p) }
		return nil
	}
	session := &agentAISession{id: "sess-1", projectPath: t.TempDir(), provider: "claude", resumeSessionID: "sess-1"}
	m.registerAISessionLocked(session)
	emitter := m.runEmitter(agentAIRun{runID: "run-1", sessionID: "sess-1", messageID: "m-1"}, write)

	m.runTUITurn(context.Background(), session, agentAIRun{
		runID: "run-1", sessionID: "sess-1", messageID: "m-1",
		projectPath: session.projectPath, provider: "claude",
		resumeSessionID: "sess-1",
	}, "手机消息", &tuiTurnPlan{
		Home: home, NativeSessionID: "sess-1", SocketPath: sock, PID: pid,
		JSONLPath: jsonl, JSONLOffset: mustSize(t, jsonl),
	}, emitter)

	mu.Lock()
	defer mu.Unlock()
	// 帧内容：注入的是原始 content，不是 buildAgentAIPrompt 包装文本
	if len(frames) == 0 { t.Fatal("未注入任何帧") }
	frame := <-frames
	if !strings.Contains(frame, "手机消息") { t.Fatalf("注入帧缺少用户内容: %s", frame) }
	if strings.Contains(frame, "You are continuing an Aliang") {
		t.Fatalf("注入帧被 buildAgentAIPrompt 污染: %s", frame)
	}
	// 事件序列
	var seq []string
	for _, e := range events {
		seq = append(seq, fmt.Sprint(e["type"]))
		if e["run_id"] != "run-1" { t.Errorf("事件缺 run_id: %v", e) }
	}
	// ⚠️ 该期望序列依赖 fake TUI 的写入顺序：user 回显 → text 块 → tool_use 块。
	// 顺序写成 text→tool_use→text 会让断言失败（块按 transcript 出现顺序派发），
	// 那是 fixture 写错了，不是实现错了。
	want := []string{"ai.run.started", "ai.delta", "ai.delta", "ai.command", "ai.done"}
	if strings.Join(seq, ",") != strings.Join(want, ",") {
		t.Fatalf("事件序列 = %v, want %v", seq, want)
	}
	// user 回显的 delta 走 channel=user；assistant 走 channel=assistant
	if events[1]["channel"] != "user" { t.Errorf("user 回显 channel = %v, want user", events[1]["channel"]) }
	if events[2]["channel"] != "assistant" { t.Errorf("assistant channel = %v, want assistant", events[2]["channel"]) }
	// ai.command 必填 item_id + status
	if events[3]["item_id"] == nil || events[3]["status"] != "started" {
		t.Errorf("ai.command 缺 item_id/status: %v", events[3])
	}
	if events[3]["command"] != "Bash" { t.Errorf("ai.command command = %v", events[3]["command"]) }
	// 终态带 executor 标记与统计
	done := events[len(events)-1]
	if done["executor"] != "tui" { t.Errorf("ai.done 缺 executor=tui: %v", done) }
	if done["files_touched_count"] == nil { t.Errorf("ai.done 缺 files_touched_count") }
}

// TestRunTUITurnInjectsAttachmentSuffix 锁死事实 6：附件清单必须拼进注入文本。
func TestRunTUITurnInjectsAttachmentSuffix(t *testing.T) {
	origMatch := externalInterruptTargetMatches
	externalInterruptTargetMatches = func(int) bool { return true }
	defer func() { externalInterruptTargetMatches = origMatch }()
	origPoll := tuiTurnPollInterval
	tuiTurnPollInterval = 20 * time.Millisecond
	defer func() { tuiTurnPollInterval = origPoll }()

	home := t.TempDir()
	sock := ccPeerShortSocketPath(t) // macOS sun_path 103 字节上限，t.TempDir()+测试名必爆
	frames := make(chan string, 4)
	startFakeTUI(t, sock, frames, func(string) {
		path := transcriptFixturePath(home, "sess-1")
		appendTranscriptLine(t, path, `{"type":"user","uuid":"echo","isMeta":false,"message":{"role":"user","content":"看图"}}`)
		appendTranscriptLine(t, path, `{"type":"assistant","uuid":"a1","apiBlockIndex":0,"message":{"content":[{"type":"text","text":"好"}]}}`)
	})
	writeClaudePidRecord(t, home, "sess-1", pid, "idle", sock)
	jsonl := writeTranscriptFixture(t, home, "sess-1")

	m := newAgentAIManager() // agent_ai.go:879（nil map 安全）
	session := &agentAISession{id: "sess-1", projectPath: t.TempDir(), provider: "claude", resumeSessionID: "sess-1"}
	m.registerAISessionLocked(session)
	emitter := m.runEmitter(agentAIRun{runID: "r", sessionID: "sess-1", messageID: "m"}, func(any) error { return nil })

	m.runTUITurn(context.Background(), session, agentAIRun{
		runID: "r", sessionID: "sess-1", messageID: "m",
		projectPath: session.projectPath, provider: "claude", resumeSessionID: "sess-1",
		attachments: []agentAIAttachment{{Type: "image", Path: "/tmp/pic.png", Name: "pic.png"}},
	}, "看图", &tuiTurnPlan{Home: home, NativeSessionID: "sess-1", SocketPath: sock, PID: pid, JSONLPath: jsonl, JSONLOffset: mustSize(t, jsonl)}, emitter)

	frame := <-frames
	if !strings.Contains(frame, "看图") || !strings.Contains(frame, "/tmp/pic.png") {
		t.Fatalf("注入帧缺附件清单: %s", frame)
	}
}
```

配套 fixture：

```go
// startFakeTUI 起一个 unix listener，收到帧后回调 onContent(content)。
func startFakeTUI(t *testing.T, sock string, frames chan<- string, onContent func(string)) {
	t.Helper()
	_ = os.Remove(sock)
	ln, err := net.Listen("unix", sock)
	if err != nil { t.Fatal(err) }
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil { return }
			go func(c net.Conn) {
				defer c.Close()
				sc := bufio.NewScanner(c)
				for sc.Scan() {
					line := sc.Text()
					frames <- line
					var f struct {
						Type    string `json:"type"`
						Message struct{ Content string } `json:"message"`
					}
					if json.Unmarshal([]byte(line), &f) == nil && f.Type == "user" {
						onContent(f.Message.Content)
					}
				}
			}(c)
		}
	}()
}

// jsonString 把任意字符串安全编码为 JSON 字符串字面量（含引号），供测试里
// 手工拼 transcript 行。
func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// mustSize 返回文件当前字节数，用于构造 tail 起点。
func mustSize(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil { t.Fatal(err) }
	return info.Size()
}

// waitFor 轮询等待条件成立（测试里替代裸 sleep）。
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() { return }
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met within 5s")
}
```

> **测试文件 import 清单**（`agent_tui_turn_test.go`，避免漏 import 导致编译失败）：
> `bufio`、`bytes`、`context`、`encoding/json`、`fmt`、`io`、`net`、`os`、`path/filepath`、`strconv`、`strings`、`sync`、`testing`、`time`。若某个 import 最终未用到，`go vet` 会报——按实际使用增删。

配套 fixture：

```go
// startFakeTUI 起一个 unix listener，收到帧后回调 onContent(content)。
func startFakeTUI(t *testing.T, sock string, frames chan<- string, onContent func(string)) {
	t.Helper()
	_ = os.Remove(sock)
	ln, err := net.Listen("unix", sock)
	if err != nil { t.Fatal(err) }
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil { return }
			go func(c net.Conn) {
				defer c.Close()
				sc := bufio.NewScanner(c)
				for sc.Scan() {
					line := sc.Text()
					frames <- line
					var f struct {
						Type    string `json:"type"`
						Message struct{ Content string } `json:"message"`
					}
					if json.Unmarshal([]byte(line), &f) == nil && f.Type == "user" {
						onContent(f.Message.Content)
					}
				}
			}(c)
		}
	}()
}

```

- [ ] **Step 2: 运行测试验证失败**

Run: `go test ./app/http/services/ -run TestRunTUITurnHappyPath -v`
Expected: FAIL（`undefined: m.runTUITurn`）

- [ ] **Step 3: 实现执行器**

> ⚠️ 本节代码已按「已核实的关键事实」5/6/7/8 修正：注入用**原始 content**（非 `run.prompt`）、拼附件清单、`ai.delta` 用 `channel` 字段、thinking 走 `ai.thinking`、`ai.command` 带 `item_id`+`status`、补 10 秒 `ai.run.progress` 心跳。

```go
// tuiTurnPollInterval 是 tail 桥的轮询间隔。可经测试覆写。
var tuiTurnPollInterval = 200 * time.Millisecond

// tuiTurnQuietRounds 是判定「回合已安静结束」所需的连续无新 assistant 行的
// 轮询次数。CC 写完最后一个块到把 pid record 翻成 idle 之间有短暂窗口。
var tuiTurnQuietRounds = 3

// tuiTurnCeiling 是注入回合的全局上限。没有它，一个永不回显也永不结束的
// 注入（TUI 卡死 / transcript 不写 / 判定条件永不满足）会让这个 goroutine
// 永久占着 session.cancel，该会话此后再也无法发起回合。兜底必须有。
var tuiTurnCeiling = 30 * time.Minute

// runTUITurn 把一个手机回合注入目标 TUI 执行，并 tail transcript 合成 run
// 生命周期回传手机（spec §4.3）。ctx 取消 = ai.stop。
//
// content 是用户的原始文本（不是 run.prompt——那是 buildAgentAIPrompt 包装过
// 的；见「已核实的关键事实」5）。附件从 run.attachments 取（该字段 agentAIRun
// 已自带，由 message() 的 resolveAgentAIAttachments 填好；spawn 路径靠
// withAgentAIAttachments 拼进 prompt，注入路径必须自己拼——见事实 6）。
func (m *agentAIManager) runTUITurn(ctx context.Context, session *agentAISession, run agentAIRun, content string, plan *tuiTurnPlan, emitter *agentAIRunEmitter) {
	write := agentTerminalWriter(emitter.emit)
	fail := func(code, msg string) {
		logger.Info(fmt.Sprintf("ai.tui.turn: %s session=%s native=%s", code, session.id, plan.NativeSessionID))
		_ = write(agentAIErrorPayload(session.id, run.messageID, newAgentTUITurnError(code, msg)))
	}
	assistantMsgID := agentAssistantMessageID(run.messageID)

	// 1) 登记：与 spawn 路径同一组字段，使 stop()/审批/LRU 无需改动即可工作。
	runCtx, cancel := context.WithCancel(ctx)
	activity := newAgentAIActivity()
	m.mu.Lock()
	session.cancel = cancel
	session.activeWriter = write
	session.activity = activity
	session.runSeq++
	session.activeRunID = run.runID
	run.runSeq = session.runSeq
	session.history = append(session.history, agentAIMessage{
		Role: "user", MessageID: run.messageID, Content: content, CreatedAt: time.Now().UTC(),
	})
	m.mu.Unlock()
	defer m.clearTUITurnSessionState(session)
	// 停止语义：ctx 取消时对 TUI pid 发一次 SIGINT（=一次 Esc，TUI 保持打开）。
	defer func() {
		if runCtx.Err() != nil {
			interruptExternalClaudeTurn(plan.Home, plan.NativeSessionID)
		}
	}()

	// 2) ai.run.started（executor=tui 让手机 UI 区分执行通道）
	_ = write(map[string]any{
		"type": models.AgentEventAIRunStarted, "session_id": session.id,
		"message_id": assistantMsgID, "provider": run.provider,
		"mode": run.mode, "project_path": run.projectPath,
		"state": "running", "executor": "tui",
	})

	// 3) 注入。内容 = 原始用户文本 + 附件清单（与 spawn 路径等价）。
	// 失败不回退 spawn（spec §4.5：一次回合只走一条路）。
	// 走 ccPeerDialInject 旋钮（与 tuiSync 一致），测试才能拦截注入动作。
	injectText := content + agentAIAttachmentPromptSuffix(run.attachments, true)
	if err := ccPeerDialInject(plan.SocketPath, plan.PeerToken, injectText, plan.FromMode); err != nil {
		fail(agentTUIErrInjectFailed, "无法把消息送进终端会话")
		return
	}
	// held 检测：bypass 门会静默扣留且不弹任何提示，transcript 是唯一观测点。
	if ccPeerWatchHeld(plan.JSONLPath, plan.JSONLOffset, ccPeerHeldWatchWindow) {
		fail(agentTUIErrHeld, "终端会话拒绝了这条消息，请在终端中处理")
		return
	}
	if err := runCtx.Err(); err != nil { return } // 注入期间被 stop

	// 4) tail 桥 + 完成判定。
	m.startAIWatchdog(runCtx, activity, cancel)
	// 进度心跳：TUI 长时间思考时手机需要「仍活着」的信号，否则服务端 10 分钟
	// 安静 sweeper 会把回合打翻成 timed out（与 spawn 两条路径同间隔）。
	progressStop := make(chan struct{})
	defer close(progressStop)
	go func() {
		ticker := time.NewTicker(agentAIRunProgressInterval)
		defer ticker.Stop()
		for {
			select {
			case <-progressStop:
				return
			case <-ticker.C:
				_ = write(map[string]any{
					"type":              models.AgentEventAIRunProgress,
					"session_id":        session.id,
					"message_id":        assistantMsgID,
					"git_changed_count": countGitChanged(session.projectPath),
				})
			}
		}
	}()
	emittedText := map[string]struct{}{} // 已发过的行 uuid，防重
	touched := map[string]struct{}{}
	quiet := 0
	sawUserEcho := false
	offset := plan.JSONLOffset
	ceiling := time.After(tuiTurnCeiling)
	for {
		select {
		case <-runCtx.Done():
			_ = write(map[string]any{"type": models.AgentEventAIStatus, "session_id": session.id, "status": "stopping"})
			return
		case <-ceiling:
			// 兜底：注入回合超上限。按 error 收尾（不是 done）——手机必须知道
			// 这一回合没有正常完成，而不是拿到一个假的成功。
			fail(agentTUIErrUnavailable, "终端会话执行超时")
			return
		default:
		}
		if runCtx.Err() != nil {
			_ = write(map[string]any{"type": models.AgentEventAIStatus, "session_id": session.id, "status": "stopping"})
			return
		}
		lines, newOffset, err := readTranscriptFrom(plan.JSONLPath, offset)
		if err != nil {
			fail(agentTUIErrTranscriptLost, "终端会话记录消失")
			return
		}
		offset = newOffset
		grew := false
		for _, line := range lines {
			switch classifyTranscriptLine(line) {
			case transcriptUserEcho:
				// 回显注入的消息（含 TUI 侧真实用户输入），手机确认「已进终端」。
				// channel 用 "user" 区分于 spawn 路径的 "assistant"（事实 7）。
				sawUserEcho = true
				if text := transcriptUserText(line); text != "" {
					_ = write(map[string]any{
						"type": models.AgentEventAIDelta, "session_id": session.id,
						"message_id": assistantMsgID,
						"channel": "user", "delta": text,
					})
				}
				quiet = 0
			case transcriptAssistant:
				uuid := transcriptLineUUID(line)
				if _, dup := emittedText[uuid]; dup { continue }
				emittedText[uuid] = struct{}{}
				grew = true
				itemID := "tb" + transcriptBlockIndex(line) // ai.command/ai.thinking 的 item_id
				for _, b := range extractTranscriptBlocks(line) {
					switch b.Kind {
					case "text":
						_ = write(map[string]any{
							"type": models.AgentEventAIDelta, "session_id": session.id,
							"message_id": assistantMsgID,
							"channel": "assistant", "delta": b.Text,
						})
					case "thinking":
						// claude 路径的 thinking 走独立事件（事实 7），不进正文通道。
						_ = write(map[string]any{
							"type": models.AgentEventAIThinking, "session_id": session.id,
							"message_id": assistantMsgID, "item_id": itemID, "delta": b.Text,
						})
					case "tool_use":
						for _, p := range toolUseTouchedPaths([]transcriptBlock{b}) {
							touched[p] = struct{}{}
						}
						// ai.command 必填 item_id + status（事实 7），缺了服务端判畸形。
						payload := map[string]any{
							"type": models.AgentEventAICommand, "session_id": session.id,
							"message_id": assistantMsgID, "item_id": itemID,
							"status": "started", "command": b.ToolName,
						}
						if cwd := toolUseCwd(b.ToolInput); cwd != "" {
							payload["cwd"] = cwd
						}
						_ = write(payload)
					}
				}
			}
		}
		if grew { quiet = 0 } else if sawUserEcho { quiet++ }
		// 完成：注入已回显 + 连续安静 + TUI 不再 busy（或记录消失）。
		if sawUserEcho && quiet >= tuiTurnQuietRounds {
			if rec, live := liveClaudeTUIRecord(plan.Home, plan.NativeSessionID); !live ||
				strings.EqualFold(strings.TrimSpace(rec.Status), "idle") {
				break
			}
		}
		select {
		case <-runCtx.Done():
			_ = write(map[string]any{"type": models.AgentEventAIStatus, "session_id": session.id, "status": "stopping"})
			return
		case <-time.After(tuiTurnPollInterval):
		}
	}

	// 5) ai.done（经 emitter → 自动 run_id/event_seq + 终态落盘 + 断线补推）。
	_ = write(map[string]any{
		"type": models.AgentEventAIDone, "session_id": session.id,
		"message_id": assistantMsgID,
		"files_touched_count": len(touched),
		"git_changed_count":   countGitChanged(session.projectPath),
		"executor":            "tui",
	})
}
```

配套助手（同文件）：

```go
// readTranscriptFrom 从 offset 读完整新行。返回的行均已以 \n 结尾（半行留在
// 文件里等下一轮），故不会解析到半个 JSON。
func readTranscriptFrom(path string, offset int64) ([][]byte, int64, error) {
	f, err := os.Open(path)
	if err != nil { return nil, offset, err }
	defer f.Close()
	info, err := f.Stat()
	if err != nil { return nil, offset, err }
	if info.Size() <= offset { return nil, offset, nil }
	if _, err := f.Seek(offset, io.SeekStart); err != nil { return nil, offset, err }
	buf := make([]byte, info.Size()-offset)
	if _, err := io.ReadFull(f, buf); err != nil && err != io.ErrUnexpectedEOF { return nil, offset, err }
	end := int64(len(buf))
	if idx := bytes.LastIndexByte(buf, '\n'); idx >= 0 {
		end = int64(idx) + 1 // 只消费完整行
	}
	if end <= 0 { return nil, offset, nil }
	lines := bytes.Split(bytes.TrimRight(buf[:end], "\n"), []byte("\n"))
	return lines, offset + end, nil
}

func transcriptLineUUID(line []byte) string {
	var row struct{ UUID string `json:"uuid"` }
	_ = json.Unmarshal(line, &row)
	return strings.TrimSpace(row.UUID)
}

// transcriptBlockIndex 取行的 apiBlockIndex（transcript 块级写入的序号），用作
// ai.command / ai.thinking 的 item_id。缺失时退化为 "b"。
func transcriptBlockIndex(line []byte) string {
	var row struct {
		APIBlockIndex *int `json:"apiBlockIndex"`
	}
	if json.Unmarshal(line, &row) == nil && row.APIBlockIndex != nil {
		return strconv.Itoa(*row.APIBlockIndex)
	}
	return "b"
}

// toolUseCwd 从 tool_use 的 input 里取 cwd（Bash 类工具有），供 ai.command 的
// 可选 cwd 字段使用。
func toolUseCwd(input json.RawMessage) string {
	var in struct {
		Cwd string `json:"cwd"`
	}
	if json.Unmarshal(input, &in) != nil {
		return ""
	}
	return strings.TrimSpace(in.Cwd)
}

func transcriptUserText(line []byte) string {
	var row struct {
		Message struct{ Content json.RawMessage `json:"content"` } `json:"message"`
	}
	if json.Unmarshal(line, &row) != nil { return "" }
	var text string
	if json.Unmarshal(row.Message.Content, &text) == nil { return text }
	var blocks []struct{ Type, Text string }
	if json.Unmarshal(row.Message.Content, &blocks) != nil { return "" }
	for _, b := range blocks {
		if b.Type == "text" { return b.Text }
	}
	return ""
}

// clearTUITurnSessionState 清掉本回合在 session 上的占用标记，让后续回合可跑。
func (m *agentAIManager) clearTUITurnSessionState(session *agentAISession) {
	m.mu.Lock()
	defer m.mu.Unlock()
	session.cancel = nil
	session.activeWriter = nil
	session.activity = nil
	session.activeRunID = ""
}
```

停止的 SIGINT：`ctx` 被 `ai.stop` 取消时，上面只发了 `stopping` 就返回——**TUI 里的回合仍在跑**。补上（在执行器开头 `defer` 之后追加）：

```go
	// 停止语义：ctx 取消时对 TUI pid 发一次 SIGINT（=一次 Esc，TUI 保持打开）。
	// 复用外部中断通道与 3s 去抖。
	defer func() {
		if runCtx.Err() != nil {
			interruptExternalClaudeTurn(plan.Home, plan.NativeSessionID)
		}
	}()
```

- [ ] **Step 4: 运行测试验证通过**

Run: `go test ./app/http/services/ -run TestRunTUITurnHappyPath -v`
Expected: PASS（事件序列/帧内容/executor 标记全部命中）

- [ ] **Step 5: Commit**（需用户批准）

---

### Task 6: 接入路由点

**Files:**
- Modify: `app/http/services/agent_ai.go:2137-2150`（`runUserMessage` 开头）
- Test: `app/http/services/agent_tui_turn_test.go`

- [ ] **Step 1: 写失败测试（路由不命中时 spawn 路径原样工作）**

```go
func TestRunUserMessageFallsBackToSpawnWithoutTUI(t *testing.T) {
	// 无 pid record → 路由返回 nil → 必须走 spawn 路径（现有测试已覆盖 spawn
	// 行为，这里断言的是「路由没拦截」：用一个必然失败的 spawn 二进制即可。
	// 做法：provider=claude、resumeSessionID 有值、home 指向空 TempDir。
	home := t.TempDir()
	t.Setenv("HOME", home) // agentHome() 读 HOME
	...
	// 断言：错误来自 spawn 层（AI CLI not found / cwd 等），而非 tui_* 错误码。
	// 具体断言以现有 agent_ai_test.go 的 spawn 失败测试形态为准。
}
```

> 这一步的真正验证在 Step 4 的全包回归：本任务改的是 12 行接线，测试重点是「不破坏既有 spawn 测试」。

- [ ] **Step 2: 运行测试验证失败**（此步无新失败测试可跑，改为确认基线）

Run: `go test ./app/http/services/ -run 'TestRunUserMessage|TestMessage' -v`
Expected: 现有测试 PASS（基线记录）

- [ ] **Step 3: 实现接线**

接线点选在 `runUserMessage` 内 `go m.runCLI(ctx, run, emitter.emit)` 那一行**之前**。理由：此处 `run` 已构造完（含 prompt/provider/projectPath/resumeSessionID/readOnly/goalIdentity），而 `ctx`/`cancel`/`activity`/watchdog/policy-sync 都还没派上用场——TUI 分支可以干净地释放它们，不留悬挂 goroutine。

```go
	// TUI 执行路由（spec §4.1）：原生 TUI 开着且空闲时，把这一回合送进那个
	// TUI 执行——TUI 与手机天然共享上下文、同看一次对话。
	// 三个不变量：
	//  ① 判定发生在任何 provider 进程/spawn 状态之前；
	//  ② 注入失败不回退 spawn（spec §4.5）——一次回合只走一条路；
	//  ③ 释放本函数前段为 spawn 准备的 ctx/watchdog，避免悬挂 goroutine。
	if plan := tuiTurnDecide(tuiTurnInput{
		Home:            externalTUIHome(),
		Provider:        provider,
		NativeSessionID: run.resumeSessionID,
		ReadOnly:        run.readOnly,
		HasGoalIdentity: len(run.goalIdentity) > 0,
		JSONLPath:       ccPeerSessionJSONL(externalTUIHome(), run.resumeSessionID),
	}); plan != nil {
		// 释放 spawn 路径预建的 ctx（执行器自建 runCtx，不再用这个）。
		cancel()
		// 注意传的是原始 content，不是 run.prompt（事实 5）；附件走 run.attachments。
		go m.runTUITurn(context.Background(), session, run, content, plan, emitter)
		return nil
	}
	go m.runCLI(ctx, run, emitter.emit)
	return nil
```

放置位置的精确要求（实现时逐一核对）：

| 前置条件 | 原因 |
|---|---|
| 在 `m.startAIWatchdog(ctx, activity, cancel)` **之前** | watchdog 会因 TUI 回合无子进程 stdout 而误判 idle 超时 |
| 在 `svc.ensurePolicyBeforeRun(ctx, session.projectPath)` **之前** | 那是 spawn 路径的策略同步，注入回合用 TUI 自己的权限模式（用户决策 2） |
| 在 `go m.runCLI(...)` 之前 | 否则两个执行器会同时跑 |

若这三行在 `agent_ai.go` 中的实际顺序与上表冲突，**以「TUI 分支不触发 watchdog / policy-sync / spawn」为准**调整插入点；必要时把判定提前到 `m.mu.Unlock()` 之后立即做（此时 `run` 尚未构造，可先算 `plan`，等 `run` 构造完再进执行器——用局部变量暂存 plan）。

- [ ] **Step 4: 运行测试验证通过（含全包回归）**

Run:
```bash
go test ./app/http/services/ -run 'TestRunTUITurn|TestTUITurn|TestClassifyTranscript|TestExtractTranscript|TestToolUse|TestRunUserMessage|TestMessage|TestClaude' -v
go test ./app/http/services/ 2>&1 | tail -20
```
Expected: 新增测试全 PASS；全包结果与基线一致（预存 flake 见「约定」，单包复跑判定）

- [ ] **Step 5: Commit**（需用户批准）

---

### Task 7: 停止路径接线（SIGINT 关联）+ 测试

**Files:**
- Modify: `app/http/services/agent_tui_turn.go`（Task 5 已含 defer，本任务补单测）
- Test: `app/http/services/agent_tui_turn_test.go`

- [ ] **Step 1: 写失败测试**

```go
func TestRunTUITurnStopSignalsTUIOnce(t *testing.T) {
	// 复用现成 stub：同时 stub 信号与身份门，并清空去抖表。pid 取 os.Getpid()
	// —— isPidAlive 不可覆写，死 pid 会让 liveClaudeTUIRecord 恒判 not-live。
	lastPid := stubExternalInterrupt(t, nil)
	pid := int(lastStubbedPid(lastPid))

	home := t.TempDir()
	sock := ccPeerShortSocketPath(t) // macOS sun_path 103 字节上限，t.TempDir()+测试名必爆
	startFakeTUI(t, sock, make(chan string, 4), func(string) {}) // 只收帧，不写 transcript
	writeClaudePidRecord(t, home, "sess-1", pid, "idle", sock)
	jsonl := writeTranscriptFixture(t, home, "sess-1")

	m := newAgentAIManager() // agent_ai.go:879；&agentAIManager{} 会让 registerAISessionLocked 写 nil map 而 panic
	var mu sync.Mutex
	var events []map[string]any
	write := func(v any) error {
		mu.Lock()
		defer mu.Unlock()
		if p, ok := v.(map[string]any); ok { events = append(events, p) }
		return nil
	}
	session := &agentAISession{id: "sess-1", projectPath: t.TempDir(), provider: "claude", resumeSessionID: "sess-1"}
	m.registerAISessionLocked(session)
	emitter := m.runEmitter(agentAIRun{runID: "r", sessionID: "sess-1", messageID: "m"}, write)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		m.runTUITurn(ctx, session, agentAIRun{
			runID: "r", sessionID: "sess-1", messageID: "m",
			projectPath: session.projectPath, provider: "claude", resumeSessionID: "sess-1",
		}, "x", &tuiTurnPlan{Home: home, NativeSessionID: "sess-1", SocketPath: sock, PID: pid, JSONLPath: jsonl, JSONLOffset: mustSize(t, jsonl)}, emitter)
	}()
	// 等到 started 发出后停止（events 有 mutex 保护，安全跨 goroutine 读）
	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(events) > 0
	})
	cancel()
	<-done

	// 恰好一次 SIGINT，且打在我们记录的 TUI pid 上
	mu.Lock()
	defer mu.Unlock()
	if got := lastStubbedPid(lastPid); got != pid {
		t.Fatalf("SIGINT pid = %d, want %d（恰好一次，且是 TUI 的 pid）", got, pid)
	}
	sawStopping := false
	for _, e := range events {
		if e["type"] == "ai.status" && e["status"] == "stopping" { sawStopping = true }
	}
	if !sawStopping { t.Fatal("未发出 ai.status(stopping)") }
	// session 占用已清理，后续回合可跑
	m.mu.Lock()
	busy := session.cancel != nil
	m.mu.Unlock()
	if busy { t.Fatal("session.cancel 未清理") }
}
```

- [ ] **Step 2: 运行测试验证失败**

Run: `go test ./app/http/services/ -run TestRunTUITurnStopSignalsTUIOnce -v`
Expected: FAIL（`pids` 为空或 session 未清理）

- [ ] **Step 3: 修正实现**（Task 5 的 defer 已含 SIGINT 与状态清理；本步只按失败信息修）

- [ ] **Step 4: 运行测试验证通过**

Run: `go test ./app/http/services/ -run TestRunTUITurnStopSignalsTUIOnce -v`
Expected: PASS

- [ ] **Step 5: Commit**（需用户批准）

---

### Task 8: 失败路径（注入失败 / held / transcript 丢失）+ 测试

**Files:**
- Modify: `app/http/services/agent_tui_turn.go`
- Test: `app/http/services/agent_tui_turn_test.go`

- [ ] **Step 1: 写失败测试**

```go
func TestRunTUITurnFailures(t *testing.T) {
	// pid 必须真实存活（isPidAlive 不可覆写）：经现成 stub 取 os.Getpid()。
	lastPid := stubExternalInterrupt(t, nil)
	pid := int(lastStubbedPid(lastPid))

	// setup 返回「可直接跑的上下文」，home/jsonl/sock 一并带回，免去臆造 helper。
	setup := func(t *testing.T, socketUp, held bool) (*agentAIManager, *agentAISession, *agentAIRunEmitter, string /*home*/, string /*jsonl*/, func() []map[string]any) {
		home := t.TempDir()
		sock := ccPeerShortSocketPath(t) // macOS sun_path 103 字节上限，t.TempDir()+测试名必爆
		if socketUp {
			onContent := func(string) {}
			if held {
				// 形态对齐 cc_peer_messaging_test.go:387 的既有 fixture：CC 在
				// transcript 尾部追加一条含 "Held peer message" 的 system 行。
				onContent = func(string) {
					appendTranscriptLine(t, transcriptFixturePath(home, "sess-1"),
						`{"type":"system","subtype":"informational","content":"Held peer message — not delivered to Claude"}`)
				}
			}
			startFakeTUI(t, sock, make(chan string, 4), onContent)
		}
		writeClaudePidRecord(t, home, "sess-1", pid, "idle", sock)
		jsonl := writeTranscriptFixture(t, home, "sess-1")
		m := newAgentAIManager()
		var mu sync.Mutex
		var events []map[string]any
		write := func(v any) error {
			mu.Lock()
			defer mu.Unlock()
			if p, ok := v.(map[string]any); ok { events = append(events, p) }
			return nil
		}
		session := &agentAISession{id: "sess-1", projectPath: t.TempDir(), provider: "claude", resumeSessionID: "sess-1"}
		m.registerAISessionLocked(session)
		emitter := m.runEmitter(agentAIRun{runID: "r", sessionID: "sess-1", messageID: "m"}, write)
		return m, session, emitter, home, jsonl, func() []map[string]any {
			mu.Lock()
			defer mu.Unlock()
			return append([]map[string]any(nil), events...)
		}
	}

	run := func(m *agentAIManager, session *agentAISession, emitter *agentAIRunEmitter, home, jsonl string) {
		m.runTUITurn(context.Background(), session, agentAIRun{
			runID: "r", sessionID: "sess-1", messageID: "m",
			projectPath: session.projectPath, provider: "claude", resumeSessionID: "sess-1",
		}, "x", &tuiTurnPlan{
			Home: home, NativeSessionID: "sess-1", SocketPath: sock,
			PID: pid, JSONLPath: jsonl, JSONLOffset: mustSize(t, jsonl),
		}, emitter)
	}

	assertLastError := func(t *testing.T, events []map[string]any, wantCode string) {
		t.Helper()
		if len(events) == 0 { t.Fatal("未发出任何事件") }
		last := events[len(events)-1]
		if last["type"] != "ai.error" {
			t.Fatalf("last type = %v, want ai.error (events=%v)", last["type"], events)
		}
		if last["error_code"] != wantCode {
			t.Fatalf("error_code = %v, want %s (last=%v)", last["error_code"], wantCode, last)
		}
	}

	t.Run("inject_failed", func(t *testing.T) {
		m, session, emitter, home, jsonl, events := setup(t, false, false) // socket 不存在
		run(m, session, emitter, home, jsonl)
		assertLastError(t, events(), "tui_inject_failed")
	})

	t.Run("held", func(t *testing.T) {
		m, session, emitter, home, jsonl, events := setup(t, true, true)
		run(m, session, emitter, home, jsonl)
		assertLastError(t, events(), "tui_held")
	})

	t.Run("transcript_lost", func(t *testing.T) {
		m, session, emitter, home, jsonl, events := setup(t, true, false)
		// fake TUI 收帧后不写任何行；这里在注入成功后删掉 transcript，让 tail
		// 首轮就失败。删除动作与 runTUITurn 同步串行，无跨 goroutine 竞争。
		started := make(chan struct{})
		origInject := ccPeerDialInject
		ccPeerDialInject = func(socketPath, token, digest, fromMode string) error {
			close(started)
			return origInject(socketPath, token, digest, fromMode)
		}
		defer func() { ccPeerDialInject = origInject }()
		go func() {
			<-started
			_ = os.Remove(jsonl)
		}()
		run(m, session, emitter, home, jsonl)
		assertLastError(t, events(), "tui_transcript_lost")
	})
}
```

> `agentAIErrorPayload` 的 `error_code` 字段名已核实（`agent_ai.go:7613-7617`），断言直接用 `error_code`。helper 命名注意：包内**已有** `waitForFile`（`agent_cli_capabilities_probe_test.go:26`）、`waitForAgentEvent`（`agent_service_test.go:4503`）——`waitFor`/`mustSize` 等新 helper 先 grep 确认无同名再定义，禁止重复定义。`newTestAgentAIManager` 不存在，统一用 `newAgentAIManager()`（agent_ai.go:879）——**`&agentAIManager{}` 不行**，其 `m.sessions` 是 nil map，`registerAISessionLocked`（agent_ai.go:919）会 panic「assignment to entry in nil map」。

- [ ] **Step 2: 运行测试验证失败**

Run: `go test ./app/http/services/ -run TestRunTUITurnFailures -v`
Expected: FAIL（错误码不符/字段不符）

- [ ] **Step 3: 修正实现与断言**（按 `agentAIErrorPayload` 的真实形状对齐）

- [ ] **Step 4: 运行测试验证通过**

Run: `go test ./app/http/services/ -run TestRunTUITurnFailures -v`
Expected: PASS（三个子测试）

- [ ] **Step 5: Commit**（需用户批准）

---

### Task 9: 文档与发布说明

**Files:**
- Modify: `docs/`（现有 agent 行为说明，若有）
- Create: `docs/superpowers/plans/2026-10-20-tui-executed-phone-turns.md` 内追加「发布说明」小节（或仓库既有的 CHANGELOG/发布说明文件——先看仓库惯例）

- [ ] **Step 1: 写发布说明**

```markdown
## 手机消息送入终端会话（TUI）执行

- 手机对某会话发消息时，若该会话的 Claude Code 终端窗口正开着且空闲，消息将直接
  送进那个终端执行：终端与手机同步显示同一次对话，上下文天然连续，无需再各写一份。
- 这一回合的工具审批在终端内完成（手机不弹审批框）；该回合使用终端自身的账号与模型。
- 终端正忙时行为不变：手机回合会被拒绝，可在终端回合结束后重试。
- 需要 Claude Code ≥ 2.1.224 且终端记录了消息 socket；不满足时自动回退原有方式
  （本机无头执行，再同步到终端），行为与之前一致。
```

- [ ] **Step 2: 确认无行为回归说明遗漏**（把 spec §6 的四条与本任务四条对齐）

- [ ] **Step 3: Commit**（需用户批准）

---

### Task 10: 端到端真机验证（手工，需用户在场）

**Files:** 无代码改动

- [ ] **Step 1: 本地构建并部署到 liang-dev（或本机）**

按 [[liang-dev-dual-unit-deploy-agent-startup-failure]] 的标准管线：本地 tar 精确文件清单 → scp → `alianggate-head` 换树 → goproxy.cn + CGO 构建 + 版本戳必打 → stop agent/core → 备份 → 换装 → enable → 先 agent 后 core。日志在 `~/aliang.log` 与 `~/aliang-agent.log`。

- [ ] **Step 2: 真机场景 A（空闲 TUI → 注入）**

1. 终端对某项目 `claude` 打开一个已有会话，停在提示符（idle）。
2. 手机对该会话发「你好，请用一句话介绍你自己」。
3. 预期：TUI 立即出现该消息并开始执行；手机收到 started/delta/done 并显示同样内容；TUI 与手机内容一致。
4. 抓日志确认：`ai.tui.turn: ... frames written` 或等价注入成功行。

- [ ] **Step 3: 真机场景 B（busy TUI → 拒绝，不双跑）**

1. TUI 里发起一个长回合（如在执行中）。
2. 手机同时发消息。
3. 预期：手机收到 `tui_busy` 错误；TUI 回合不受干扰；无第二个 claude 进程。

- [ ] **Step 4: 真机场景 C（TUI 关闭 → spawn 回退）**

1. 关掉 TUI。
2. 手机发消息。
3. 预期：走 spawn 路径（`claude -p --resume`），行为与今天完全一致。

- [ ] **Step 5: 真机场景 D（停止）**

1. 场景 A 复现，在 TUI 执行中点手机「停止」。
2. 预期：TUI 回合中断但 TUI 保持打开；手机显示 stopping。

- [ ] **Step 6: 记录验证结果**（写入计划文档末尾「验证记录」小节：日期、版本戳、四项场景结果、异常日志摘录）

---

## 附：风险与回滚

| 风险 | 缓解 |
|---|---|
| 注入把不可信文本写进 TUI stdin | 帧经 JSON 编码 + `\n` 分隔，无 shell 解析（`ccPeerInject` 已 spike 验证） |
| 一次回合双跑（注入 + spawn） | 路由判定在 spawn 之前；注入失败明确 `ai.error` 不回退 |
| tail 桥漏读导致 `ai.done` 过早 | `tuiTurnQuietRounds` + pid record idle 双重条件；可调 |
| 老版本 CC 无 socket | 条件 ⑥ 落回 spawn，行为不变 |
| Windows 无 unix socket | 条件 ⑥ 落回 spawn，行为不变 |
| 注入回合用了 TUI 的个人账号 | 用户决策 2：有意为之；UI 需标注 executor=tui |

**回滚**：整个功能可经 `tuiTurnDecide` 提前 `return nil` 一键关闭（回到今天的 spawn-only 行为）；无协议变更、无数据格式变更，回滚无需迁移。
