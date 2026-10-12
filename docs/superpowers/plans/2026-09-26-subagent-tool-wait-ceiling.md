# 工具/子agent 等待上限（Phase 0 防卡死）Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 给 AI run 的连续工具/子 agent 等待加专属上限（默认 1h，审批时间不计入），消除"子 agent 僵死让 run 卡到 48h 硬顶、心跳替僵死撒谎"的漏洞。

**Architecture:** 在 `agentAIActivity` 上增加连续等待时钟（0↔非0 跳变驱动），watchdog 新增一条在 idlePaused 期间也会触发的分支，终态复用现有 `timeout` 状态族使服务端零改动。全部改动集中在 alianggate 两个文件 + 两个测试文件。

**Tech Stack:** Go（atomic.Int64 时钟、context cancel），表驱动测试，无新依赖。

**设计文档:** `docs/superpowers/specs/2026-09-26-subagent-visibility-design.md`

**工作区:** 本计划的实施在 git worktree 分支上进行（当前 worktree 即可），提交信息用全中文「新增：/修复：」前缀，结尾附：

```
🤖 Generated with [Claude Code](https://claude.com/claude-code)

Co-Authored-By: Claude Code <noreply@anthropic.com>
```

**回归注意:** 全仓 `go test ./...` 有预存 flake（agent_ai 子进程 2s/5s 硬超时类，高并行偶败）。验证以 `go test ./app/http/services/ -count=1` 为准；若偶败，单包复跑确认。

**背景知识（实施者必读）:**

- `agentAIActivity`（agent_ai.go:928）是 run 存活跟踪器。`pendingToolUses > 0` 或 `awaitingApproval` 时 `idlePaused()` 为 true，idle(10min) 与 no-progress(60min) 看门狗都被暂停——这是为了不误杀合法的长工具等待。
- 漏洞：僵死的子 agent 让 pendingToolUses 永不归零，而 `ai.run.progress` 心跳（10s ticker，agent_ai.go:4919 与 3948）照常发给服务端，服务端静默清扫（10min）永不触发，只剩服务端 6h 绝对封顶 / gate 48h 硬顶。
- 审批等待已有独立上限 `agentAIApprovalTimeout`（默认 24h）。本计划的工具等待上限必须与它正交：**审批挂起期间工具等待时钟清零**，审批解除后重新起表。
- 终态状态值 `timeout` 已在两侧的终态列表里（agent_ai.go:401 的映射 + 服务端 `isTerminalAgentRunEvent`），所以新 kill reason 映射到 `"timeout"` 后服务端零改动。

---

### Task 1: agentAIActivity 增加工具等待时钟

**Files:**
- Modify: `app/http/services/agent_ai.go`（struct 约 928-941 行；`beginToolUseWait`/`endToolUseWait` 约 1005-1027 行；`setAwaitingApproval` 约 986-993 行）
- Test: `app/http/services/agent_ai_watchdog_test.go`

- [ ] **Step 1: 写失败测试**

在 `agent_ai_watchdog_test.go` 追加（紧跟 `TestAgentAIActivityLifecycle` 之后，风格对齐现有测试）：

```go
func TestAgentAIActivityToolWaitClock(t *testing.T) {
	a := newAgentAIActivity()

	// 未等待时时钟为 0。
	if got := a.toolWaitFor(); got != 0 {
		t.Fatalf("fresh activity toolWaitFor = %v, want 0", got)
	}

	// 首次 begin 起表。
	a.beginToolUseWait()
	if got := a.toolWaitFor(); got <= 0 {
		t.Fatalf("after beginToolUseWait toolWaitFor = %v, want > 0", got)
	}

	// 嵌套等待：第二个 begin 不得重置时钟（连续等待语义）。
	first := a.toolWaitFor()
	time.Sleep(5 * time.Millisecond)
	a.beginToolUseWait()
	if got := a.toolWaitFor(); got < first {
		t.Fatalf("nested beginToolUseWait reset the clock: %v < %v", got, first)
	}

	// 审批挂起：时钟清零（人的决策时间不计入）。
	a.setAwaitingApproval(true)
	if got := a.toolWaitFor(); got != 0 {
		t.Fatalf("approval should pause tool-wait clock, got %v", got)
	}

	// 审批解除且仍有 pending：重新起表。
	a.setAwaitingApproval(false)
	if got := a.toolWaitFor(); got <= 0 {
		t.Fatalf("approval resolve with pending tools should re-arm clock, got %v", got)
	}

	// 归零清表：最后一个 tool_result 到达后时钟回 0。
	a.endToolUseWait()
	a.endToolUseWait()
	if got := a.toolWaitFor(); got != 0 {
		t.Fatalf("after all tools resolved toolWaitFor = %v, want 0", got)
	}
	if a.idlePaused() {
		t.Fatal("idle watchdog should be unpaused once all tools resolved")
	}

	// 审批解除但无 pending 时不误起表。
	a.setAwaitingApproval(true)
	a.setAwaitingApproval(false)
	if got := a.toolWaitFor(); got != 0 {
		t.Fatalf("approval resolve with no pending tools should leave clock at 0, got %v", got)
	}
}
```

- [ ] **Step 2: 运行确认失败**

```bash
go test ./app/http/services/ -run 'TestAgentAIActivityToolWaitClock' -count=1
```

预期：编译失败，`a.toolWaitFor undefined`。

- [ ] **Step 3: 最小实现**

`agent_ai.go` struct（`pendingToolUses` 字段后）加：

```go
	// toolWaitStartedAt is the UnixNano instant the current CONTIGUOUS
	// tool/subagent wait began (0 = not waiting). The tool-wait watchdog uses
	// it to bound hung tools/subagents that would otherwise survive until the
	// hard ceiling while the progress heartbeat keeps telling the server the
	// run is alive. Cleared while a human approval is pending so approval
	// time never counts against the ceiling (approvals are bounded separately
	// by agentAIApprovalTimeout).
	toolWaitStartedAt atomic.Int64
```

`beginToolUseWait` 改为（保留原 nil 检查与 bump）：

```go
func (a *agentAIActivity) beginToolUseWait() {
	if a == nil {
		return
	}
	if a.pendingToolUses.Add(1) == 1 {
		a.toolWaitStartedAt.Store(time.Now().UnixNano())
	}
	a.bump()
}
```

`endToolUseWait` 的 CAS 成功分支内，归零时清时钟：

```go
		if a.pendingToolUses.CompareAndSwap(current, current-1) {
			if current-1 == 0 {
				a.toolWaitStartedAt.Store(0)
			}
			break
		}
```

`setAwaitingApproval` 改为：

```go
func (a *agentAIActivity) setAwaitingApproval(v bool) {
	if a == nil {
		return
	}
	a.awaitingApproval.Store(v)
	if v {
		// Approval time is human time, not tool time: pause the tool-wait
		// clock so a slow decision never trips the tool-wait ceiling.
		a.toolWaitStartedAt.Store(0)
	} else if a.pendingToolUses.Load() > 0 {
		// Decision resolved but the tool is still in flight: re-arm with a
		// fresh ceiling window for the remaining execution.
		a.toolWaitStartedAt.Store(time.Now().UnixNano())
	}
	a.bump()
}
```

新增访问器（放在 `awaiting()` 之后）：

```go
// toolWaitFor reports how long the current contiguous tool/subagent wait has
// been in flight, excluding approval-pending time. 0 when not waiting.
func (a *agentAIActivity) toolWaitFor() time.Duration {
	if a == nil {
		return 0
	}
	ts := a.toolWaitStartedAt.Load()
	if ts == 0 {
		return 0
	}
	return time.Since(time.Unix(0, ts))
}
```

- [ ] **Step 4: 运行确认通过**

```bash
go test ./app/http/services/ -run 'TestAgentAIActivity' -count=1
```

预期：PASS（含既有 `TestAgentAIActivityLifecycle`）。

- [ ] **Step 5: 提交**

```bash
git add app/http/services/agent_ai.go app/http/services/agent_ai_watchdog_test.go
git commit -m "$(cat <<'EOF'
新增：AI run 工具等待时钟（连续 tool/subagent 等待计时，审批时间不计入）

🤖 Generated with [Claude Code](https://claude.com/claude-code)

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

---

### Task 2: 常量 agentAIToolWaitCeiling

（先于 watchdog 任务：生产调用点在 Task 3 引用本常量，顺序保证每个任务结束时代码可编译。）

**Files:**
- Modify: `app/http/services/agent_execution_guard.go`（var 块约 101-117 行）
- Test: `app/http/services/agent_execution_guard_test.go`

- [ ] **Step 1: 写失败测试**

在 `TestAgentAITimeoutDefaults`（guard_test.go:39 起）内追加。沿用该测试的 env-skip 风格，在函数开头补一个 skip：

```go
	if value := os.Getenv("ALIANG_AI_TOOL_WAIT_CEILING"); value != "" {
		t.Skipf("ALIANG_AI_TOOL_WAIT_CEILING=%s set at start; skipping default assertion", value)
	}
```

函数体末尾追加断言：

```go
	if agentAIToolWaitCeiling != time.Hour {
		t.Fatalf("default agentAIToolWaitCeiling = %s, want 1h", agentAIToolWaitCeiling)
	}
	if agentAIToolWaitCeiling >= agentAIHardCeiling {
		t.Fatalf("agentAIToolWaitCeiling (%s) must be less than agentAIHardCeiling (%s) to fire first", agentAIToolWaitCeiling, agentAIHardCeiling)
	}
```

- [ ] **Step 2: 运行确认失败**

```bash
go test ./app/http/services/ -run 'TestAgentAITimeoutDefaults' -count=1
```

预期：编译失败，`agentAIToolWaitCeiling undefined`。

- [ ] **Step 3: 最小实现**

`agent_execution_guard.go` 的 var 块（`agentAINoProgressWindow` 之后）追加：

```go
	// agentAIToolWaitCeiling bounds how long a CONTIGUOUS tool/subagent wait
	// (approval-pending time excluded) may run before the run is cancelled as
	// hung. It is the only guard that fires during such a wait: the idle and
	// no-progress watchdogs pause while a tool result is pending, and the
	// ai.run.progress heartbeat keeps feeding the server-side stale-run
	// sweeper — without this bound a hung subagent keeps the phone showing
	// "running" until the hard ceiling. Env:
	//
	//	ALIANG_AI_TOOL_WAIT_CEILING  (time.ParseDuration, e.g. "90m"). Default 1h.
	//
	// Explicitly set it to "0s" to disable. Blank/unparseable/negative values
	// fall back to the default (see resolveEnvDurationAllowZero).
	agentAIToolWaitCeiling = resolveEnvDurationAllowZero("ALIANG_AI_TOOL_WAIT_CEILING", time.Hour)
```

- [ ] **Step 4: 运行确认通过**

```bash
go test ./app/http/services/ -run 'TestAgentAITimeoutDefaults|TestResolveEnvDuration' -count=1
```

预期：PASS。

- [ ] **Step 5: 提交**

```bash
git add app/http/services/agent_execution_guard.go app/http/services/agent_execution_guard_test.go
git commit -m "$(cat <<'EOF'
新增：ALIANG_AI_TOOL_WAIT_CEILING 环境变量（工具等待上限，默认 1h，0 禁用）

🤖 Generated with [Claude Code](https://claude.com/claude-code)

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

---

### Task 3: watchdog 增加 toolWaitCeiling 分支

**Files:**
- Modify: `app/http/services/agent_ai.go`（`agentAIWatchdogLoop` 约 1082-1113 行；生产调用点约 1077 行）
- Test: `app/http/services/agent_ai_watchdog_test.go`
- Modify（Step 6 文档润色）: `app/http/services/agent_execution_guard.go`（常量注释）

- [ ] **Step 1: 更新既有测试调用点 + 写失败测试**

签名从 `agentAIWatchdogLoop(ctx, activity, cancel, idleWindow, hardCeiling, noProgressWindow, interval)` 变为在 `interval` 前插入 `toolWaitCeiling`。既有 5 处测试调用（实测在 165、236、265、327、362 行；以 `rg -n "agentAIWatchdogLoop" app/http/services/agent_ai_watchdog_test.go` 实际结果为准）在 `interval` 前补 `0,`（禁用，保持原语义）。

追加三个新测试：

```go
func TestAgentAIWatchdogLoopToolWaitCeilingKills(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	activity := newAgentAIActivity()
	activity.beginToolUseWait()
	done := make(chan struct{})
	go func() {
		// idle 窗口巨大：确保唯一可能触发的是 tool-wait 上限。
		agentAIWatchdogLoop(ctx, activity, cancel, time.Hour, 0, 0, 40*time.Millisecond, 5*time.Millisecond)
		close(done)
	}()

	select {
	case <-done:
		if reason := activity.killReasonOr(""); reason != "tool_wait_timeout" {
			t.Fatalf("kill reason = %q, want tool_wait_timeout", reason)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("watchdog did not cancel a hung tool wait past the ceiling")
	}
}

func TestAgentAIWatchdogLoopApprovalPausesToolWaitClock(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	activity := newAgentAIActivity()
	activity.beginToolUseWait()
	activity.setAwaitingApproval(true) // 时钟清零 → 上限不得触发
	done := make(chan struct{})
	go func() {
		agentAIWatchdogLoop(ctx, activity, cancel, time.Hour, 0, 0, 40*time.Millisecond, 5*time.Millisecond)
		close(done)
	}()

	select {
	case <-done:
		t.Fatalf("watchdog cancelled during approval wait, reason=%q", activity.killReasonOr(""))
	case <-time.After(200 * time.Millisecond):
	}
}

func TestAgentAIWatchdogLoopApprovalResolveRearmsToolWaitClock(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	activity := newAgentAIActivity()
	activity.beginToolUseWait()
	activity.setAwaitingApproval(true)
	done := make(chan struct{})
	go func() {
		agentAIWatchdogLoop(ctx, activity, cancel, time.Hour, 0, 0, 40*time.Millisecond, 5*time.Millisecond)
		close(done)
	}()
	time.Sleep(80 * time.Millisecond) // 在审批中度过整个上限窗口
	activity.setAwaitingApproval(false) // 重新起表 → 一个新窗口后必须触发

	select {
	case <-done:
		if reason := activity.killReasonOr(""); reason != "tool_wait_timeout" {
			t.Fatalf("kill reason = %q, want tool_wait_timeout", reason)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("watchdog did not cancel after approval resolved and tool stayed pending")
	}
}
```

- [ ] **Step 2: 运行确认失败**

```bash
go test ./app/http/services/ -run 'TestAgentAIWatchdogLoop' -count=1
```

预期：整个包编译失败（既有 5 处调用与新测试都是 8 参，实现还是 7 参）——红灯阶段就是编译失败，属正常 TDD 红灯。

- [ ] **Step 3: 最小实现**

`agentAIWatchdogLoop` 签名插入 `toolWaitCeiling time.Duration`（`interval` 之前），在 no-progress 检查之后、hard-ceiling 检查之前插入分支：

```go
			// Tool-wait ceiling: a contiguous tool/subagent wait (approval
			// time excluded — the clock is cleared while a human decision is
			// pending) that outlives the ceiling means the tool or subagent
			// is hung. Every other guard is paused during such a wait by
			// design, and the ai.run.progress heartbeat keeps feeding the
			// server-side stale-run sweeper, so without this bound a hung
			// subagent pins the run until the hard ceiling while the phone
			// shows "running". The awaiting() re-check hardens against a
			// theoretical stale-armed clock (a scheduler preemption inside
			// beginToolUseWait's Add→Store window landing exactly as an
			// approval hook fires): approval waits are bounded separately by
			// agentAIApprovalTimeout, so skipping the kill while a human
			// decision is pending is always safe.
			if toolWaitCeiling > 0 && !activity.awaiting() && activity.toolWaitFor() > toolWaitCeiling {
				activity.setKillReason("tool_wait_timeout")
				cancel()
				return
			}
```

生产调用点（`rg -n "agentAIWatchdogLoop(ctx, activity, cancel, agentAIIdleWindow" app/http/services/agent_ai.go` 定位）在 `agentAINoProgressWindow` 后补传 `agentAIToolWaitCeiling`（Task 2 已创建，可直接引用）。

**watchdog 分支必须包含 `!activity.awaiting()` 守卫**（Task 1 质量评审的行动项：防理论上的 stale-arm 交错——`beginToolUseWait` 的 Add→Store 窗口内被调度器抢占、恰好撞上审批 hook 触发时，时钟可能在审批挂起期间带着旧值在跑；审批等待由 `agentAIApprovalTimeout` 单独约束，挂起期间跳过终止永远安全）。分支条件为 `toolWaitCeiling > 0 && !activity.awaiting() && activity.toolWaitFor() > toolWaitCeiling`，注释文本用下方 Step 3 代码块里的版本。

- [ ] **Step 4: 运行确认通过**

```bash
go test ./app/http/services/ -run 'TestAgentAIWatchdogLoop|TestAgentAIActivity' -count=1
```

预期：PASS。

- [ ] **Step 5: 提交**

```bash
git add app/http/services/agent_ai.go app/http/services/agent_ai_watchdog_test.go
git commit -m "$(cat <<'EOF'
新增：watchdog 工具等待上限分支（僵死 tool/subagent 等待超限即终止，审批等待豁免）

🤖 Generated with [Claude Code](https://claude.com/claude-code)

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

- [ ] **Step 6: 常量注释补充约束说明（Task 2 质量评审 Minor 项落地，独立提交）**

`app/http/services/agent_execution_guard.go` 的 `agentAIToolWaitCeiling` 注释末尾（"fall back to the default (see resolveEnvDurationAllowZero)." 之后）追加两句：

```go
	// Keep it below agentAIHardCeiling — that backstop fires on total active
	// time, so a value at or above it neutralizes this guard. Approval waits
	// never count against this ceiling (the clock is paused while a human
	// decision is pending) and remain bounded by agentAIApprovalTimeout.
```

运行 `gofmt -l app/http/services/` 确认干净后提交：

```bash
git add app/http/services/agent_execution_guard.go
git commit -m "$(cat <<'EOF'
文档：工具等待上限注释补充硬顶排序约束与审批豁免交叉引用

🤖 Generated with [Claude Code](https://claude.com/claude-code)

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

---

### Task 4: 终态映射 tool_wait_timeout → timeout

**Files:**
- Modify: `app/http/services/agent_ai.go`（`agentAIRunStoppedStatus` 约 1118-1134 行；Step 6 另含 `startAIWatchdog` 注释约 1104 行）
- Test: `app/http/services/agent_ai_watchdog_test.go`
- Modify（Step 6 文档修正）: `app/http/services/agent_execution_guard.go`（常量注释）

- [ ] **Step 1: 写失败测试**

先确认没有既有测试：`rg -n "agentAIRunStoppedStatus" app/http/services/*_test.go`。没有就新建：

```go
func TestAgentAIRunStoppedStatusToolWaitTimeout(t *testing.T) {
	activity := newAgentAIActivity()
	activity.setKillReason("tool_wait_timeout")
	status, msg := agentAIRunStoppedStatus(activity, nil)
	if status != "timeout" {
		t.Fatalf("status = %q, want timeout (server terminal-status family)", status)
	}
	if msg == "" {
		t.Fatal("expected a non-empty human-readable reason")
	}
}
```

- [ ] **Step 2: 运行确认失败**

```bash
go test ./app/http/services/ -run 'TestAgentAIRunStoppedStatusToolWaitTimeout' -count=1
```

预期：FAIL，status 实际为 `"stopped"`（落入兜底分支）。

- [ ] **Step 3: 最小实现**

`agentAIRunStoppedStatus` 的 switch 在 `case "hard_ceiling":` 之前插入：

```go
	case "tool_wait_timeout":
		return "timeout", fmt.Sprintf("AI run tool/subagent wait exceeded %s without a tool result (suspected hung tool) and was stopped", agentAIToolWaitCeiling)
```

- [ ] **Step 4: 运行确认通过**

```bash
go test ./app/http/services/ -run 'TestAgentAIRunStoppedStatus' -count=1
```

预期：PASS。

- [ ] **Step 5: 提交**

```bash
git add app/http/services/agent_ai.go app/http/services/agent_ai_watchdog_test.go
git commit -m "$(cat <<'EOF'
新增：tool_wait_timeout 终态映射到 timeout 状态族（服务端零改动）

🤖 Generated with [Claude Code](https://claude.com/claude-code)

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

- [ ] **Step 6: 注释精度修正（Task 3 质量评审 Minor 项落地，独立提交）**

两处文档修正：

1. `app/http/services/agent_execution_guard.go` 的 `agentAIToolWaitCeiling` 注释：
   - "that backstop fires on total active time" → **"that backstop fires on total elapsed run time"**（hardCeiling 计的是 `time.Since(runStart)`，包含审批与空闲时间，并非只算活跃时间）
   - "so a value at or above it neutralizes this guard" → **"so a value above it neutralizes this guard"**（相等时 tool-wait 分支先检查、仍会以 tool_wait_timeout 触发，只有严格大于才使本守卫失效）
2. `app/http/services/agent_ai.go` 的 `startAIWatchdog` doc 注释（约 1104 行）整段替换为：

```go
// startAIWatchdog cancels ctx when the run goes idle (no output for
// agentAIIdleWindow and not awaiting approval/tool results), produces no
// meaningful progress for agentAINoProgressWindow, a contiguous tool/subagent
// wait outlives agentAIToolWaitCeiling, or the run exceeds agentAIHardCeiling.
// It exits as soon as ctx is done, so it never outlives the run.
```

运行 `gofmt -l app/http/services/` 确认干净、`go build ./app/http/services/` 通过后提交：

```bash
git add app/http/services/agent_execution_guard.go app/http/services/agent_ai.go
git commit -m "$(cat <<'EOF'
文档：watchdog 注释精度修正（硬顶计时口径、相等阈值语义、startAIWatchdog 全量守卫清单）

🤖 Generated with [Claude Code](https://claude.com/claude-code)

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

---

### Task 5: 回归与收尾

- [ ] **Step 1: 包级全量回归**

```bash
go build ./... && go test ./app/http/services/ -count=1
```

预期：`ok`。若出现 agent_ai 子进程超时类偶败（预存 flake），单独复跑失败的用例确认转绿。

- [ ] **Step 2: 核对无遗漏调用点**

```bash
rg -n "agentAIWatchdogLoop\(" app/http/ | rg -v "_test.go|toolWaitCeiling|agentAIToolWaitCeiling"
rg -n "tool_wait_timeout" app/http/ | rg -v "_test.go"
```

预期：第一条无输出（生产调用点都已带 ceiling 参数）；第二条恰好两处——watchdog 分支（`setKillReason`）与终态映射（switch case）。

- [ ] **Step 3: 更新设计文档状态**

`docs/superpowers/specs/2026-09-26-subagent-visibility-design.md` 的 Phase 0 标注"已实施（本分支）"。

- [ ] **Step 4: 提交**

```bash
git add docs/superpowers/specs/2026-09-26-subagent-visibility-design.md
git commit -m "$(cat <<'EOF'
新增：子 agent 感知设计 spec（Phase 0 已实施标注）

🤖 Generated with [Claude Code](https://claude.com/claude-code)

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```
