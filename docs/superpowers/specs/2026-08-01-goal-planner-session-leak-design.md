# Goal 规划内部 CLI session 泄漏到 VibeCoding 列表 — 修复设计

- 日期：2026-08-01
- 状态：Draft（待 spec-review）
- 范围：仅 Go agent（`alianggate`）。单仓改动。
- 受影响仓库：`~/MyProgram/GoProgram/nursor/alianggate`（Go agent）。**不**改 server / 不改契约 / 不改手机 / 不改 TS `local-agent.ts`。

---

## 1. 背景（问题现象）

手机 VibeCoding 列表里会莫名其妙出现一些用户从未创建过的对话。复现规律：一旦使用 Goal 功能（plan / replan / 规划重试），列表里就会出现「创建 plan」之类的陌生对话。这些对话其实是 Goal 规划器为了生成计划而临时驱动的 Codex / Claude CLI 在本机磁盘留下的真实 session 历史，被库存扫描器当成普通外部 CLI 对话导入了。

## 2. 根因（精确机制）

1. Server 创建 Goal 时正常建一个 `purpose: 'goal'` 的主会话（`AliangPhoneServer/.../routes/goals.ts`），这是**应当**展示的会话。
2. Server 随后发独立的 `goal.plan` RPC（`AliangPhoneServer/.../goal/agentPlanner.ts`），载荷含 `goal_id` + `planning_attempt_id`，**不**带主会话 id，**不**声明内部调用。
3. Go agent 处理 `goal.plan`：在 `app/http/services/agent_goal.go` 的 `runGoalPlanCLIPass` 里以 `plannerInternal: true`（`agent_goal.go:753`）跑 CLI。Provider（Codex/Claude）在本机磁盘留下真实 session（`~/.codex/sessions/*.jsonl`、`~/.claude/projects/*/*.jsonl`）。
4. 所有 CLI 运行捕获 native session id 都走同一回调 `onNativeSessionID`（`app/http/services/agent_ai.go:4663`）。普通运行在此调 `confirmBinding` 把 native id 绑成会话 → server 端 `phoneSessionOwnsCliSource`（`AliangPhoneServer/.../agent/reported.ts`）后续去重，不会重复导入。**但 `plannerInternal` 分支（`agent_ai.go:4664-4670`）直接 `return`——native id 已知却被丢弃，不绑定。**
5. agent 的库存扫描器（`app/http/services/agent_inventory.go`）周期性无差别读取磁盘上的 Codex/Claude session，输出 `codex_<id>` / `claude_<id>`。
6. Server 收到扫描结果，对没有绑定主会话的记录视为「CLI 启动的外部会话」，创建 `ai_import_<hash>`。`phoneSessionOwnsCliSource` 因 planner 未绑定而**不**命中。
7. 列表 API 只按 用户/设备/`hiddenFromMobileAt` 过滤（`AliangPhoneServer/.../shared/lists.ts`），不区分内部 planner session。手机直接展示。

> 关键纠偏：Go agent 里 `provider_run_id` 在 `agent_goal.go:1093` 被直接设成 RPC `requestID`，**不是** CLI native session id，无法用于关联/隐藏——印证它从不被用来定位 CLI session。

每次 plan / replan / 规划重试都可能留下新的底层 native session，随后以陌生对话形式出现在列表。

## 3. 范围确认（哪些内部 spawn 会泄漏）

- ✅ **泄漏**：Goal **规划**（plan / replan / 重试的 explore + emit pass，全部走 `plannerInternal: true`）。这是本设计覆盖的全部对象。
- ❌ **不泄漏，不在范围内**：
  - Goal **任务执行**走普通 run 路径，会 `confirmBinding` 绑到主 goal 会话（`purpose: 'goal'`，本就该显示）。
  - Goal **verify** 跑的是 shell 检查命令（`file_exists` / `file_contains` / `command`），不产生 CLI chat session。

## 4. 目标 / 非目标

**目标**

- 让 Goal 规划产生的 native CLI session 不再被库存扫描器上报，从而不再生成 `ai_import_`。
- 覆盖 Codex **和** Claude 两条 provider 路径。
- 单仓（Go agent）止血；持久化跨 agent 重启生效。

**非目标（明确不做）**

- 不改 server / 契约 / 手机。
- 不清理已泄漏的历史 `ai_import_` 记录（用户手动删；本设计只「止血」）。
- 不给 TS `local-agent.ts` 扫描器做 parity（它是 dev 桩，自身不 spawn 规划）。
- 不修「CLI 先落盘 session、后回报 SID」的固有小竞态（见 §8 风险，作为已知项接受）。

## 5. 设计

总思路：在已有的 `plannerInternal` 闸口，把规划运行捕获到的 native session id 记进一个**持久化的内部 SID 集合**；库存扫描器构建候选后，剔除 id ∈ 集合的条目。镜像已有的「绑定即去重」哲学，区别仅在于 planner 不绑定、改为进排除集合。

### 5.1 组件 1 — 内部 SID 集合（数据模型 + 持久化）

存结构化记录，**不**存拼接字符串：

```go
type rememberedInternalSession struct {
    Provider        string    // "codex" | "claude"  (canonical)
    NativeSessionID string    // 原始 native id（无前缀）
    CapturedAt      time.Time
}
```

**归一化（canonical inventory id）helper**——与扫描器输出格式严格对齐：

```text
claude / claudecode -> "claude_<id>"
codex              -> "codex_<id>"
```

> 为什么不用 `tool.id + "_" + nativeID`：`claudecode` 的 `tool.id` 会拼出 `claudecode_<id>`，而扫描器输出的是 `claude_<id>`，两者不匹配会导致过滤失效。必须用上面的归一化 helper，以 canonical inventory id 作为内存集合的 key。

**剪枝策略**：

- **删除原设计的 14 天 TTL。** 扫描器按**数量**（`agentVibeSessionFileScanLimit = 120` 文件、`agentVibeSummaryMaxSessions = 200`）扫描，而非时间。低活跃机器上 15 天前的 session 仍在扫描窗口内，TTL 到期会让问题复发。
- **按 provider 分桶，每桶最多 512 条，无时间过期。** 分桶是必须的——否则大量 Codex 记录会挤掉仍在 Claude 扫描窗口内的标记。512 远大于扫描器的 120 文件窗口，留足余量；插入时若该 provider 桶超限，按 `CapturedAt` 最旧的优先淘汰。

**持久化**：

- 复用 agent 现有 `persistIdentityState()`（`app/http/services/agent_ai_identity.go:81`，已是原子写）。
- 在 identity state 结构里新增字段承载上述记录（带 provider 分桶）。
- 启动时加载进内存集合。
- **旧 JSON 兼容**：加载时对缺失新字段的旧 state 文件视作空集合（不报错、不丢弃既有 binding 等其它字段）。

### 5.2 组件 2 — 在 chokepoint 捕获（两条 provider 路径都要补）

新增统一入口（命名建议）：

```go
func (m *agentAIManager) rememberPlannerNativeSession(provider, nativeID string)
```

功能：归一化为 canonical inventory id → 插入内存集合（幂等）→ 仅在**首次插入**（集合确实新增了一条）时同步落盘。

**捕获点 A — Claude / OpenCode**（已有回调，补一行）：

`agent_ai.go:4663` 的 `onNativeSessionID`，在现有 `plannerInternal` 分支 `return` 之前，调用 `rememberPlannerNativeSession`。注意：这个回调只被 Claude/OpenCode 触发。

**捕获点 B — Codex**（当前完全没进回调，必须新增）：

Codex `exec --json` 不进入 `onNativeSessionID`。Codex 的 native thread id 在 `agent_ai.go` 的 Codex 事件解析处捕获，有**两个赋值点**：`threadID = value`（@ ~3742，`turn/started` 路径）和 `threadID = remoteString(thread, "id")`（@ ~3925，`thread.create` 响应路径，当 `threadID == ""` 时触发），外加 `setAgentAIResumeSessionIDIfEmpty`（@ ~3635）把 threadID 钉到 session。

实现时**不要**只埋在 `turn/started` 路径：应在 `run.plannerInternal` 为真、且 threadID **首次变为非空**时（覆盖上述两个赋值点中先命中的那个）调用 `rememberPlannerNativeSession("codex", threadID)`。最稳妥是放在「threadID 刚被确认为非空、即将用于 resume/steer」的汇聚点上，而非某个具体 case 分支里。

> 不补 B，方案只修好 Claude，Codex 规划仍泄漏。

### 5.3 组件 3 — 持久化语义（同步、失败容忍）

- **同步持久化，取消原设计的 debounce。** `persistIdentityState()` 本就是原子写；普通 binding 捕获也是同步调它，planner 捕获保持一致即可。
- 仅在**首次插入**新条目时落盘（已存在的 canonical id 不重复写盘）。
- **不持锁做磁盘 I/O**：在锁内判定「是否新增」并拷贝待写数据，锁外调 `persistIdentityState()`。
- **保存失败只告警**（log warn），内存集合标记继续生效（下一次新增时再尝试落盘）。保存失败**不得**中断 planner 回合。
- 因此**不需要** `removeGoalPlanningSession` / `closeAll` 的 flush 生命周期钩子——集合的存活与 planner session 的存活解耦（planner session 是瞬态的，但「这个 native id 是内部的」这一事实需要活得比 planner session 久，直到扫描器不再扫到它）。

### 5.4 组件 4 — 扫描器排除（过滤位置是本设计最易出错点）

**关键事实（纠偏）**：`collectAgentSyncSnapshot` 在 `agent_inventory.go:76` 用 `collectAgentProjects(vibeSessions)` **直接从 vibe 候选列表**派生 `Projects`，:77 `agentProjectPaths(projects)` 派生 `AuthorizedDirectories`——**不是**独立扫描。因此过滤不能在 `collectAgentProjects` 消费候选之前就把 planner session 删掉，否则会撤掉其 `projectPath` 对应的项目授权。

**分流设计（同时满足「internal 不占名额」与「项目授权从未过滤派生」）**：

把候选构建拆成 `collectRawAgentVibeSessions(scanDirs)`（codex+claude 全量、按 `UpdatedAt` 排序、**不截断不过滤**），再在消费侧分流。核心是纯函数 `partitionAgentVibeSessions(full, internal)`（`full` 已排序），返回两份：

- `projectsSource` = `full` 的前 `agentVibeSummaryMaxSessions=200`（**含** internal）→ 供 `collectAgentProjects` / `agentProjectPaths` 派生项目与授权目录。
- `vibe` = `excludeInternalVibeSessions(full, internal)` 后再取前 200（**不含** internal）→ 作 `VibeSessions` 输出。

这样：vibe 路径是「先过滤后截断」（internal 不占名额）；projects 路径看到含 internal 的未过滤候选（授权不丢）。两份独立截断，互不影响。

**覆盖所有消费方**：`collectAgentSyncSnapshot`（库存同步，`agent_inventory.go:73`）走上面的分流——projects 用 `projectsSource`、`VibeSessions` 用 `vibe`。`AgentService.VibeSessions()`（`agent_service.go:231`，只要可见列表、不要 projects）调用 `collectRawAgentVibeSessions` + `excludeInternalVibeSessions` + 截断 200。两路都过滤，不漏。`overlayActiveAgentVibeSessions`（`agent_inventory.go:95`）在已过滤的 `snapshot.VibeSessions` 之上叠加 active runs，其内部 :129 的截断保持不变（叠加后总量再 cap 200）。

**集合传递**：内部 SID 集合由 `agentAIManager` 持有，`(m).internalNativeSessionSnapshot()` 返回拷贝；`AgentService` 经 `s.ai` 取快照传入上述函数。**禁止 package global。**

## 6. 数据流（修复后）

1. goal 规划跑 CLI（`plannerInternal: true`）。
2. provider 回报 native id：
   - Claude/OpenCode → `onNativeSessionID` → 调 `rememberPlannerNativeSession("claude", id)`。
   - Codex → threadID 捕获处 → 调 `rememberPlannerNativeSession("codex", threadID)`。
3. 集合新增 → 同步 `persistIdentityState()`（首次插入）。
4. 周期库存扫描：构建未截断候选 → 过滤内部 SID → 截断 200；`Projects`/`AuthorizedDirectories` 从未过滤候选派生。
5. 上报给 server 的 `vibe_sessions` 不含 planner session → 不再产生 `ai_import_`。

## 7. 修改清单（实现时细化，spec 级别）

- `app/http/services/agent_ai_identity.go`：identity state 增字段 + load/save 旧 JSON 兼容。
- `app/http/services/agent_ai.go`：
  - 新增 `rememberPlannerNativeSession` + 内存集合 + 归一化 helper + 分桶剪枝（max 512/provider）。
  - `onNativeSessionID` plannerInternal 分支：补捕获（点 A）。
  - Codex threadID 捕获处：`plannerInternal` 时补捕获（点 B）。
- `app/http/services/agent_inventory.go`：拆出 `collectRawAgentVibeSessions`（不截断不过滤）+ 纯函数 `partitionAgentVibeSessions(full, internal)`（projects 取未过滤前 200、vibe 取过滤后前 200）+ `excludeInternalVibeSessions`；`collectAgentSyncSnapshot` 用 `partition` 分流；集合以参数快照传入。
- `app/http/services/agent_service.go`：`VibeSessions()`（:231）改用 `collectRawAgentVibeSessions` + 过滤 + 截断；`collectAgentSyncSnapshotWithActiveRuns`（:1999）把 `s.ai.internalNativeSessionSnapshot()` 传入 `collectAgentSyncSnapshot`。两个消费方都过滤，不漏。
- 测试（见 §9）。

## 8. 风险

- **已知竞态（接受）**：CLI 可能**先**在磁盘创建 session 文件，**再**通过 stdout 回报 SID；这几毫秒内若刚好发生 inventory sync，仍可能泄漏。作为单仓止血可接受，但**必须写进风险项并在真机验证时覆盖**：触发规划后强制 agent 重连，并等待至少一次 1 分钟周期上报，确认最终列表无陌生 `ai_import_`。
- **provider 偶发不回报 native id**（某些失败路径）：无 id 可捕获 → 该 session 可能仍泄漏。可接受（罕见；失败往往也不留持久 session）。
- **agent 重装 / state 文件丢失**：集合清空 → 扫描窗口内残留的旧 planner session 可能泄漏一次。可接受（YAGNI，不做种子恢复）。
- **emit(resume) pass 复用 explore 的同一 native id**：幂等插入，无副作用。

## 9. 测试（Go）

- Codex SID 提取：构造 Codex `thread.started`/threadID 事件流，`plannerInternal` 时 `rememberPlannerNativeSession("codex", ...)` 被调用且 id 正确。
- Claude/OpenCode 路径：`onNativeSessionID` 在 `plannerInternal` 时进集合、**不**触发 `confirmBinding`；非 planner 时仍正常绑定。
- 归一化：`claude`/`claudecode` → `claude_<id>`；`codex` → `codex_<id>`。
- 旧 JSON 兼容 + 重启恢复：缺失新字段的旧 state 文件加载为空集合且不丢其它字段；写入后重启能读回。
- 剪枝：单 provider 超 512 淘汰最旧；Codex 桶满不挤占 Claude 桶。
- **过滤发生在 200 截断前**：`partitionAgentVibeSessions` 构造候选含 ≥200 真实 + 若干 internal，断言 `vibe` 被过滤的是 internal 且真实对话未被挤掉（先过滤后截断）。
- **项目授权从未过滤派生**：同一构造里，`projectsSource`（前 200）**仍含** internal session 的 `project_path`——隐藏 planner 对话不撤授权。
- **两个调用方都过滤**：`collectAgentVibeSessions` 的两个消费点（库存同步路径 + `AgentService.VibeSessions()`）输出都不含 internal session。
- **隐藏 session 不影响项目授权**：含 internal 的候选，过滤后 `Projects`/`AuthorizedDirectories` 仍包含其 `project_path`。
- 持久化失败容忍：模拟写盘失败 → 只告警、不 panic、不中断 planner。
- 上述相关测试 `go test -race` 通过。

## 10. 真机验证

1. `go build ./...`、`go vet ./...`、相关 `go test`（含 `-race`）全绿。
2. 部署新 agent 二进制。
3. 触发一次 Goal 规划（含至少一次 replan / 重试）。
4. **强制 agent 重连**，等待至少一次 1 分钟周期 inventory 上报。
5. 确认手机 VibeCoding 列表**不新增**陌生 `ai_import_` 对话（覆盖 §8 竞态窗口）。
6. 确认 Goal 主会话（`purpose: 'goal'`）仍正常显示。
7. 确认相关项目的授权目录未受影响。

## 11. 明确不做（重申）

- server / 契约 / 手机：零改动。
- TS `local-agent.ts` 扫描器 parity：不做。
- 历史 `ai_import_` 清理：用户手动删。
