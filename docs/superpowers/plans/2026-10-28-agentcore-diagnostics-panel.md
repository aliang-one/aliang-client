# 聊天页诊断面板（上下文 / 状态参数 / 失败原因）端到端

> **实施状态（2026-10-28）**：L1–L4 全部实现，未提交。分支/worktree：
> - alianggate `.worktrees/diagnostics-panel` @ `feature/agentcore-diagnostics-panel`（内核 OnTurn+TurnSnapshot、宿主 ai.run.progress.diagnostics、journal end 行诊断块、reader case "end"、models.AgentVibeDiagnostics；agentcore/services 包测试绿，仅剩 2 个 HEAD 预存失败：TestAgentCoreSubagentBridgeDepthOne darwin PowerShell 断言 + flake 家族）
> - AliangPhoneServer `.worktrees/diagnostics-panel` @ `feature/run-diagnostics-panel`（AiRunDiagnostics 类型、aiRun.ts 归一化入库、messages.ts 下发、db 列+迁移 sqlite/pg；tsc 绿、vitest 3/3）
> - AliangVibeCodingPhone `.worktrees/diagnostics-panel` @ `feature/run-diagnostics-panel`（VibeRunDiagnostics、api/sessions+internals 映射+merge 保留行、DiagnosticsPanel.tsx 右滑面板、状态气泡旁入口按钮、i18n zh/en；tsc 绿（余 2 个 HEAD 预存 internals.ts TS2352）、jest 绿（余 2 个 HEAD 预存 agentTranscript））
> - 键形契约：agent 线上 snake_case → 服务端归一化 camelCase（落库+下发），手机直用 camelCase。
> - 手机面板消费 ai.session.updated 快照里的 diagnostics（服务端每次 progress 都重发快照），终态真因来自内核 OnTurn 终态快照（同时升级 journal end 行的 error 文案）。


## 目标
在聊天页顶部 in-progress 状态气泡旁加一个入口按钮，点开从右侧滑出一个全高诊断面板，展示当前对话的：
1. **上下文用量**（细致）：当前已用 tokens / 窗口 / 百分比、本轮输入输出、缓存命中、累计、压缩历史。
2. **状态参数**：回合数、无进展守卫（重复工具/连续错误/截断重试计数）、停止原因、阶段、耗时。
3. **失败原因**：最近一次失败的结构化原因（status/type/重试 N/M）+ 错误文案。

数据流打通：agentcore（内核+宿主产出）→ PhoneServer（接收/落库/下发）→ 手机（获取/展示）。

## 现状（已核查，避免重复造）
- 内核 `runner` 已持有 `turns / contextTokens / dupStreak / errStreak / truncStreak / effectiveContextWindow()`，但**只在 run 结束**经 `RunResult` 暴露，无逐回合对外通道。
- 宿主 `ai.run.progress` 心跳**只发** `files_touched_count / git_changed_count`；`ai.error`（含结构化真因文案）已到手机；`ai.usage` 已作为结构化事件到手机。
- journal `end` 行写了 `{status, error}`，但详情 reader `readAgentCoreSessionMetaWithOptions` 的 `switch row.Kind` **只处理 session/msg/tool，忽略 end** → 重开旧会话看不到失败原因。
- 手机 `VibeCodingRun` 已有 `contextWindow / structuredEvents(usage) / retryAttempt / lastErrorStatus/Type / phase / runState`。
- PhoneServer `AgentSession` 已有 `filesTouchedCount / gitChangedCount / contextWindow / lastError*`；`messages.ts` 已把它们序列化下发；`aiRun.ts` progress 分支把字段存到 session；结构化 usage 走 slim envelope。

**结论**：错误文案、usage、窗口、重试大多已在链路上。真正缺的是「逐回合的权威上下文 token 数 + 回合数 + 守卫计数 + 停止原因」的实时通道，以及「重开旧会话仍能看到终态诊断」。

## L1 — 内核（alianggate `app/agentcore/loop.go`）
新增逐回合快照钩子（非破坏，字段可 nil）：
```go
// LoopHooks 增加：
OnTurn func(TurnSnapshot) // 每个回合开始（发请求前）触发一次

type TurnSnapshot struct {
    Turns         int
    ContextTokens int   // 上一回合上游报告的本轮输入 token（含 cache）
    ContextWindow int   // effectiveContextWindow()
    DupStreak     int
    ErrStreak     int
    TruncStreak   int
    StopReason    string
    ElapsedMs     int64
}
```
调用点：`for turn ...` 内，`r.turns = turn + 1` 之后、`maybeAutoCompact` 之前触发 `OnTurn`（此时 contextTokens 为上一回合值，够用）。守卫命中（dupStreak/errStreak/truncStreak 越界、maxIterations 兜底）处也补发一次带最终计数的快照，便于面板看到「为什么停」。

## L2 — 宿主（alianggate `app/http/services/agent_ai_agentcore.go` + journal + inventory）
1. **共享快照**：run 内建 `diag := &agentCoreDiagnostics{}`（`sync.Mutex` 保护 `last TurnSnapshot`）。`agentCoreLoopHooks(m, run)` 增加返回 `OnTurn` → 写入 `diag`。
2. **心跳扩字段**：progress ticker 的 `emit()` 在 payload 加 `diagnostics: {turns, context_tokens, context_window, dup_streak, err_streak, trunc_streak, stop_reason, elapsed_ms}`。为让面板及时，`OnTurn` 里可主动触发一次 emit（与 ticker 共用闭包）。
3. **落库终态诊断**：`journal.appendEnd` 增 `diagnostics` 字段（turns/contextTokens/contextWindow/stopReason/streaks），把终态快照随 end 行落盘。
4. **reader 补 end 分支**：`agent_inventory_agentcore.go` 的 row struct 增 `Status / Error / Diagnostics`；`case "end"` 把 `session.Status`、终态错误与 `Diagnostics` 填进 `models.AgentVibeSession`。新增字段 `AgentVibeSession.Diagnostics *AgentVibeDiagnostics`（`app/http/models/agent.go`），随 `ai.session.detail.result` 下发。
5. 保持 `ai.error` 现有真因文案不变（手机失败卡片继续用）。

## L3 — PhoneServer（Node/TS）
1. `server/src/types.ts` `AgentSession` 增 `diagnostics?: RunDiagnostics`（turns/contextTokens/contextWindow/dupStreak/errStreak/truncStreak/stopReason/elapsedMs/updatedAt）。
2. `handlers/aiRun.ts` progress 分支：`if (message.diagnostics) session.diagnostics = message.diagnostics`。
3. `modules/ai/messages.ts` 快照增 `diagnostics: session.diagnostics`。
4. 持久化：`db/aiSessions.ts` 增一列（JSON，nullable）`diagnostics`，读写随 files_touched 同段；`AiSession` row 映射回填。保证重开/重启后仍在。
5. `sessionDetail` 若走 journal/ai.session.detail 通道，则透传 agent 侧 `Diagnostics`（与 L2.4 对齐）。

## L4 — 手机（AliangVibeCodingPhone）
1. **数据**：`VibeCodingRun`（`src/data/platformModels.ts`）增 `diagnostics`；`platformTransport.ts` 在 `ai.run.progress` 透传 `diagnostics`；reducer（`src/store/slices/structuredSlice.ts` 或 aiRun.progress 分支）存到 run；`ai.session.detail` 打开旧会话时也回填。
2. **入口按钮**：`VibeCodingSessionScreen.tsx` 的 `TopAppBar rightAction` → `styles.topStatusCluster`（≈2600-2622，`StatusChip` 旁）加一个 icon 按钮，`onPress` 切换 `diagnosticsOpen`。
3. **右侧滑出面板**：新建 `src/components/vibecoding/DiagnosticsPanel.tsx`，复用 `BottomSheet.tsx` 的 reanimated 结构，`translateY`→`translateX`（`SCREEN_WIDTH→0`），贴右、全高、左侧圆角。`{diagnosticsOpen && <DiagnosticsPanel run={session}/>}`。
4. **内容分区**（细致展示）：
   - 上下文卡：`RingMeter`/`ProgressBar` 显示 used/window 与百分比；细分 input/output/cache_read/累计（取 `structuredEvents` usage 汇总 + `diagnostics.contextTokens`）。
   - 状态卡：回合数、dup/err/trunc 三个守卫计数（带阈值 N/M 进度）、`phase`、`stopReason`、耗时。
   - 失败卡：`lastErrorStatus/lastErrorType`（+retryAttempt/retryMax）、错误文案（run 上的 error / detail）。
5. **工具**：复用 `utils/contextWindow.ts`（formatContextWindow/contextWindowFraction）、`activitySummary.ts`（usageTokens）。
6. **i18n**：`src/i18n/locales/vibecoding/{en,zh}.json` 增 `session.diagnostics.*`。

## 交付顺序 / 验证
1. 内核 OnTurn + 宿主 diagnostics 通道 → 单测：构造小 run，断言 progress payload 含 turns/context_tokens 且随回合增长；`go test ./app/agentcore/... ./app/http/services/...`（注意内存 flake 基线，单包复跑）。
2. journal end 落 diagnostics + reader case end → 单测：写一条 end 行，断言 detail 能读到 status/error/diagnostics。
3. PhoneServer：vitest 覆盖 progress 存 session.diagnostics + messages 序列化。
4. 手机：TS 类型 + 面板渲染；本地 build 校验。

## 非目标 / 顺延
- 不改动既有 `ai.error` 文案口径。
- `maxIterations` 提升 + 自动续跑（上轮已批准、当前顺延的 fix #1）不在本计划内，待本特性完成后单独落地。
- 每个仓库各自用 worktree 分支开发，不直接改 master/main；提交需用户显式同意。
