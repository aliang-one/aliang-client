# 异常必有状态返回 · 内核 P0 实现计划(时钟统一 + 断连守卫冻结)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 内核侧所有「活动/空闲/等待」判定统一走单调钟(免疫 Mac 睡眠),断连期间守卫冻结——根治「墙钟 2h9m vs 单调 18m」误杀与断连期误判。

**Architecture:** ①`agentAIActivity` 的 8 个 `time.Now().UnixNano()` 墙钟戳改为 `atomic.Value` 存 `time.Time`(带单调读数),读侧 `time.Since()` 自动走单调(macOS 上单调钟不含睡眠时间)——外部方法签名全部不变,改动封闭在 agentAIActivity 内部;②ws 瞬断(`setRemoteConnectionState(false)`)时给守卫挂「暂停」标志,watchdog 循环与 stale 扫描器跳过判定,重连恢复——断连期间杀 run 也通知不出去,不如冻结。

**Tech Stack:** Go,单一大平包 `app/http/services`,定向 `go test -run` + `go build ./...`。

**工作方式:** 主 checkout 有 59 文件并行 WIP **勿动**。新建 worktree(`git worktree add ../../alianggate-excstatus -b fix/activity-monotonic-clock`,基 master@6dfdc26)。TDD,commit message 用中文。

**前置事实(已侦查核实,勿再考证):**
- 墙钟写入点:`agent_ai.go:1227(runStart 例外保留),1237,1249,1283,1304,1355,1430,1477`
- 墙钟读取点:`agent_ai.go:1258-1266(progressIdleFor),1487-1495(idleFor),1315-1323(approvalWaitFor),1335-1343(toolWaitFor),1461-1465(subagentWaitFor)`
- 墙钟消费(误杀点):`agent_ai.go:1552-1557(idle_timeout),1565-1567(no_progress),1584-1587(tool_wait),1593-1596(subagent_wait)`;`agent_ai_run_guard.go:540-553` → `staleDeathReason`(`agent_ai.go:1397-1418`)
- 单调已正确处:`runStart time.Time`(1221)+ `time.Since(runStart)`;engineActive ticker 累加(1542)
- 断连:`agent_remote_ws.go:389-394`(读窗 90s);`485-495`(瞬断不杀进程=正确,保留);`1162-1181` `setRemoteConnectionStateLocked`(只改状态,不碰守卫)
- 24h 审批 waiter:`agent_execution_guard.go:122`;`agent_ai.go:3811-3867`。ctx 走 Go timer(单调)睡眠自动顺延,**本计划不动它**
- 测试基座:`agent_ai_watchdog_test.go`/`agent_ai_run_guard_test.go` 既有家族;TestMain 用 `agentAIStaleScannerEnabled=false`(`agent_ai_run_guard.go:503-507` 注释)

---

### Task 1: agentAIActivity 时间戳统一单调钟

**Files:**
- Modify: `app/http/services/agent_ai.go:1185-1266` 及 1304/1355/1430/1477 各 setter、1315-1343/1461-1495 各 getter
- Modify(测试树适配,直接 `Store`/`Load` 原子字段的文件): `app/http/services/agent_ai_run_guard_test.go`(232-233, 260, 279, 289)、`agent_ai_agentcore_steer_test.go`(13, 36, 48)、`agent_ai_agentcore_thinking_test.go`(43-44, 192, 199)、`agent_ai_subagent_test.go`、`agent_tui_turn_test.go`、`agent_execution_guard_test.go`(若 `TestAgentAITimeoutDefaults` 断言存储形状)
- Test: `app/http/services/agent_ai_watchdog_test.go`(追加用例)

**实现要点:**

1. 结构体字段类型改 `atomic.Value`(存 `time.Time`;zero 用 `time.Time{}` 判空,不用 0 值哨兵):

```go
type agentAIActivity struct {
	lastActivityAt        atomic.Value // time.Time(带单调读数)
	lastProgressAt        atomic.Value
	approvalWaitStartedAt atomic.Value
	toolWaitStartedAt     atomic.Value
	subagentWaitStartedAt atomic.Value
	pendingApprovals      atomic.Int64
	pendingToolUses       atomic.Int64
	activeSubagents       atomic.Int64
	runStart              time.Time // 不变(已单调)
	...
}
```

(以现结构为准逐字段对齐,勿增删语义字段;`pendingApprovals` 等计数器不动。)

2. 写入 helper(替代散落的 `Store(int64)`;`time.Now()` 自带单调读数,无需额外包装):

```go
func (a *agentAIActivity) storeTS(field *atomic.Value) { field.Store(time.Now()) }
func loadTS(field *atomic.Value) (time.Time, bool) {
	t, ok := field.Load().(time.Time)
	return t, ok && !t.IsZero()
}
```

3. 读取统一:

```go
func (a *agentAIActivity) progressIdleFor() time.Duration {
	last, ok := loadTS(&a.lastProgressAt)
	if !ok { return 0 }
	return time.Since(last) // time.Time 含单调读数 → Since 走单调,睡眠不计
}
```

idleFor/approvalWaitFor/toolWaitFor/subagentWaitFor 同型替换。`time.Unix(0, ts)` 反解全部消失。

4. 对外导出/序列化点若需要墙钟(如日志、快照),在 getter 处 `time.Now().Add(-d)` 派生,**判定不依赖它**。

- [ ] **Step 1: 按**新 API**写测试(红灯 = 编译失败)**

> **为什么红灯是编译失败而非运行时失败:** `time.Time` 的单调读数与墙钟值绑定在同一个值里,测试无法构造「墙钟跳 2h 但单调只走 50ms」的状态——`Store(time.Now().Add(-2h))` 会把单调读数一起回拨,新旧实现都无法据此区分。睡眠免疫是**结构性保证**(time.Since 优先单调读数 + macOS 单调钟不含睡眠),其行为正确性由「连续性测试(语义不变)+ 全量回归(全部守卫用例仍绿)」锁定,并配代码注释说明。这是本任务刻意选择的 TDD 形态。

`agent_ai_watchdog_test.go` 追加连续性用例(按新 API 编写,旧代码下编译不过 = 红灯):

```go
func TestAgentAIActivityTimestampSemanticsUnchanged(t *testing.T) {
	a := newAgentAIActivity() // 按现有构造器对齐(注意:构造器构造时即以 runStart 填充时钟,
	                          // 见 agent_ai.go:1228-1229,故新建 activity 的 idle 是微小非零值)
	// 断言用既有生命周期测试的容差写法(对齐 agent_ai_watchdog_test.go:16-18),
	// 勿断言 ==0(构造即填充,必然非零);也勿改构造器留零值——那是「idle 窗口
	// 从 run 构建变到首次 bump」的行为变更,现有容差测试抓不住。
	if d := a.progressIdleFor(); d > time.Second {
		t.Fatalf("新建 activity 的 idle 应 ≈0(容差内), got %s", d)
	}
	a.markProgress() // 语义:写入即清零 idle
	if d := a.progressIdleFor(); d > time.Second {
		t.Fatalf("刚写入的进度 idle 应 ≈0, got %s", d)
	}
	// 新 API 直接以 time.Time 伪造「过去的进度」:
	a.lastProgressAt.Store(time.Now().Add(-30 * time.Minute))
	if d := a.progressIdleFor(); d < 29*time.Minute {
		t.Fatalf("30 分钟前的进度应测得 ≈30m idle, got %s", d)
	}
}
```

- [ ] **Step 2: 确认红灯**

Run: `go test ./app/http/services/ -run 'TestAgentAIActivityTimestampSemanticsUnchanged' -count=1`
Expected: **编译失败**(旧字段是 `atomic.Int64`,不存在 `Store(time.Time)` 可行路径/类型不匹配)

- [ ] **Step 3: 实现替换**(按上述 1-4;同步把 Files 清单里 5+1 个测试文件的 `Store(int64)`/`Load` 用法改为 `Store(time.Time)`/`loadTS`,逐文件 grep 核对:

```bash
grep -n 'lastActivityAt\|lastProgressAt\|approvalWaitStartedAt\|toolWaitStartedAt\|subagentWaitStartedAt' app/http/services/*_test.go
```

否则 `loadTS` 的类型断言会 panic。)

- [ ] **Step 4: 定向回归**

Run: `go test ./app/http/services/ -run 'AgentAIActivity|AgentAIWatchdogLoop|StaleDeath|AwaitingApproval|AgentAITimeoutDefaults|AgentAIRunStoppedStatus|RunGuardKill|StaleScanner|Steer|Thinking|Subagent|TuiTurn' -count=1`
Expected: 全 PASS。特别注意 `TestAgentAITimeoutDefaults` 位于 `agent_execution_guard_test.go:39`,若断言 UnixNano 存储形状,按新形状更新。

- [ ] **Step 5: Commit**

```bash
git add app/http/services/agent_ai.go app/http/services/agent_ai_watchdog_test.go \
  app/http/services/agent_ai_run_guard_test.go app/http/services/agent_ai_agentcore_steer_test.go \
  app/http/services/agent_ai_agentcore_thinking_test.go app/http/services/agent_ai_subagent_test.go \
  app/http/services/agent_tui_turn_test.go app/http/services/agent_execution_guard_test.go
git commit -m "修复:内核活动/等待时钟统一单调钟——Mac睡眠不再虚增idle被守卫误杀"
```

---

### Task 2: 断连期间守卫冻结

**Files:**
- Modify: `app/http/services/agent_ai.go`(agentAIActivity 加 paused 标志与守卫判定闸)
- Modify: `app/http/services/agent_remote_ws.go:1162-1181`(`setRemoteConnectionStateLocked` 挂/摘标志)
- Test: `app/http/services/agent_ai_run_guard_test.go`(追加)

**语义:** `RemoteConnected=false`(瞬断/重拨中)→ 守卫判定全部跳过(idle_timeout/no_progress/tool_wait/subagent_wait/stale 扫描);重连成功恢复。**审批 waiter 24h ctx 不动**(人工上限,单调 timer 睡眠顺延,语义正确)。**永久拆线路径(closeAllWithReason/forceDisconnectRemote)不受影响**——它们本来就直接收帧。

- [ ] **Step 1: 写失败测试**

```go
func TestGuardFrozenWhileRemoteDisconnected(t *testing.T) {
	// 构造:run 活跃 + lastProgressAt 停在远超 noProgressWindow 之前
	// (沿用 TestStaleScannerKillsNonWaitingRunEvenWithHeartbeat(:218) 的构造手法)
	// 测试针对管理器级方法(setRemoteConnectionState 对应的 s.ai 遍历入口,
	// 见 Step 3)驱动 paused 标志,不构建完整 AgentService。
	// 置断连 → 守卫一轮不 kill;置回连 → 下一轮 kill 照常
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./app/http/services/ -run 'TestGuardFrozenWhileRemoteDisconnected' -count=1`
Expected: FAIL

- [ ] **Step 3: 实现**

`agentAIActivity` 加 `guardPaused atomic.Bool`;watchdog 判定四处(agent_ai.go:1552-1596)与 `staleDeathReason`(1397-1418)入口处 `if a.guardPaused.Load() { continue }`(staleDeathReason 返回 `("", false)`)。挂接路径:`setRemoteConnectionStateLocked` 是 `*AgentService` 方法,经 `s.ai`(agent_service.go:154,`ai *agentAIManager`)遍历会话 activity 置/清(对齐 `scanStaleRuns` 的遍历手法);**`attachRunGuard` 创建 activity 时以当前连接状态初始化 guardPaused**——否则断连窗口内新启动的 run 会绕过冻结(正是本修复要灭的 bug 类)。

- [ ] **Step 4: 定向回归**

Run: `go test ./app/http/services/ -run 'Guard|Stale|Watchdog|Disconnect|Teardown|RemoteLoop' -count=1 && go build ./...`
Expected: 全 PASS + 编译零错

- [ ] **Step 5: Commit**

```bash
git add app/http/services/agent_ai.go app/http/services/agent_remote_ws.go app/http/services/agent_ai_run_guard_test.go
git commit -m "修复:断连期间冻结运行守卫——通知不出去的判死只会制造僵尸,重连后再判"
```

---

### Task 3: 全量验证与出包

- [ ] **Step 1:** `go vet ./app/http/services/` → 0;`go build ./...` → 0
- [ ] **Step 2:** 定向回归(两 Task 的 -run 合集)→ 全绿;基线红清单单独记录(已知 `TestAgentCoreSubagentBridgeDepthOne` 为基线红,勿当回归)
- [ ] **Step 3:** 出 release 分支 `release/v1.1.94`(master 线,worktree 内 `git checkout -b`),按既有 pkg 流程出 `Aliang-v1.1.94-arm64.pkg`;**sudo 装机 + 重启 Aliang.app = 用户动作,装机前不动 master 引用**
- [ ] **Step 4:** 装机验证后,`git merge release/v1.1.94` 快进 master(遵守「等 WIP 收敛后纯快进」惯例)

## 验收(对照事故)

10-31 两连死场景:Mac 睡眠 2h 唤醒 → 守卫不再因墙钟虚增误杀(Task 1);relay 瞬断重拨 90s 窗口 → 守卫冻结不产生「判死但通知不出去」的僵尸(Task 2)。配合 server 侧 P0(决议慢档常驻 + 命令 watchdog),「卡死无状态」五层盲区全部闭合。
