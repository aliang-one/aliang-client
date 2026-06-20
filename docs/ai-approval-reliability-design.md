# Agent AI 审批可靠性（活跃度看门狗 + 可观测送达）落地设计

## 目标

解决 agent 运行 headless AI 对话时的两个可靠性问题：

1. **误杀长任务**：原实现用 flat 30min 超时硬墙，会把合法的长编码任务、或在等人类审批的任务
   直接杀掉。正确做法是按**对话是否活跃**判活——有输出或在等审批即活，只有「无输出且非审批
   等待」才判死。
2. **批准丢失/不可观测**：用户在服务端点了批准，送达客户端后服务端无感知，网络抖动或断线就丢，
   客户端最终被超时判 deny。正确做法是**显式可观测送达**——客户端 ACK 收到+生效结果，服务端
   未收 ACK 即重投，客户端周期 sync 兜底重连/丢包。

## 参数（已与用户确认锁定）

| 参数 | 值 | 说明 |
|---|---|---|
| `agentAIIdleWindow` | 10min | 无输出且非审批等待 → 判死 |
| `agentAIHardCeiling` | 4h | 失控兜底（无论是否活跃） |
| `agentAIIdleCheckInterval` | 30s | 看门狗 tick |
| 审批决策上限 | **无定时器** | 仅由对话失活（停止/关闭/run 结束/agent 离线）取消 |
| 服务端决策落库 | **必须** | 用户所有操作 + 审批状态持久化 |

## 分期与现状

### Phase 1（纯客户端）— ✅ 已完成并测试

**1a. 活跃度看门狗**（`app/http/services/agent_ai.go`、`agent_execution_guard.go`）

- 移除 flat `agentAIRunTimeout=30min` 的 `WithTimeout`，改为 `WithCancel` + 看门狗 goroutine。
- 新增 `agentAIActivity`（`lastActivityAt`/`awaitingApproval`/`runStart`/`killReason`，nil-safe）。
- `emitAIDelta`（所有输出路径收口）与 codex 扫描循环 `bump()` 刷新活跃时间；`requestApproval`
  等待期 `setAwaitingApproval(true)`（Claude hook 与 Codex RPC 共用此函数，一处覆盖两路径），
  审批豁免 idle。
- 看门狗：`idle && !awaitingApproval` → `idle_timeout`；`runDuration > 4h` → `hard_ceiling`。
- 终态回执经 `agentAIRunStoppedStatus` 统一：`idle_timeout`/`hard_timeout`/`output_limited`/`stopped`。
- 抽出 `agentAIWatchdogLoop(...)` 带参数体，便于测试用极小窗口驱动。

**1b. writer 解耦（重连不断流）**（`agent_service.go`、`agent_remote_ws.go`）

- `AgentService.remoteWriter atomic.Pointer[agentTerminalWriter]` + `setCurrentRemoteWriter`/
  `currentRemoteWriter`/`clearCurrentRemoteWriter`。
- `runRemoteAgentSession` 发布当前 conn 的 writer，所有 AI/terminal 写经 shim 间接层 → 始终写
  **当前**活 conn；重连后运行中的 run 自动接到新 socket。
- **transient 断线不再 `ai.closeAll()`**（保留 `terminal.closeAll()`）；真正停连
  （`remoteConnectionLoop` 退出）与 `forceDisconnectRemote` 才清理 AI 会话。

**测试**：`agent_ai_watchdog_test.go`（idle/豁免/活动重置/hard 四分支 + activity 生命周期 + nil 安全）、
`agent_remote_writer_test.go`（发布/观察/清除/重连重绑）。全量 `go test ./app/http/services/` 除一条
**既有**失败（`TestQuickSetupService_Render_MultiProvider`，与本改动无关）外全绿。

### Phase 2（客户端 + 协议增量）— ✅ 已完成并测试

新增/变更事件（`app/http/models/agent_protocol.go`，含契约条目）：

| 事件 | 方向 | 作用 |
|---|---|---|
| `ai.approval.ack` | client→server | 显式观察：`result=applied\|no_match\|duplicate`，带 `delivery_id` |
| `ai.approval.sync` | client→server | 列出仍 pending 的审批；重连对账 + 活性心跳 |
| `ai.approval.cancelled` | 双向 | 对话失活，审批被丢弃（带 `approval_ids`/`reason`） |
| `ai.approval.state` | server→client | 服务端审批状态（如仍 pending），回复 sync |
| `ai.approval.response` | server→client | 新增 `delivery_id`/`attempt`（可重投） |

客户端行为（`agent_ai.go`、`agent_remote_ws.go`）：

- `approval()` 收决策后**必发 ack**（applied/duplicate/no_match），让服务端停手。
- `pendingApprovalsSnapshot()`/`emitApprovalSync()`：列出 pending。
- 失活取消通知：`clearPendingApprovalsLocked` 返回取消的 id；`close()`(session_closed)/
  `clearRunning()`(run_ended) 经 `emitApprovalCancelled` best-effort 通知服务端。
- **重连对账**：`sendAgentHello("connect")` 后立即 `emitApprovalSync`。
- **周期提醒+活性心跳**：心跳 goroutine 新增 60s ticker，周期 `emitApprovalSync`（无 pending 时不发）。
- `ai.approval.state`（server→client）dispatch 仅触活性、忽略 payload（前向兼容）。

**向后兼容**：客户端对新字段（`delivery_id`/`attempt`）幂等处理；新发事件（ack/sync/cancelled）
旧服务端不识别也无害。**Phase 1+2 可先于 Phase 3 安全上线**。

**测试**：`agent_ai_approval_proto_test.go`（ack applied/duplicate/no_match、sync 列表、close 发 cancelled）。

### Phase 3（PhoneServer 服务端）— ✅ 已实现并 typecheck 通过

服务端位于 **AliangPhoneServer**（`/Users/mac/MyProgram/AiProgram/vibe_on_phone/AliangPhoneServer`，
TypeScript）。实现方式：**加法式 `ai.approval.*` 路径**，不动既有 `approval.*` 流（两套并存，
respond 按 `aiApprovalIds` 集合分流）。

- 新增 `server/src/ai_approval_delivery.ts`：`AIApprovalDelivery` 送达引擎——
  `deliver()`（带 `delivery_id`/`attempt` 投递）+ ack 截止重投（退避 2/4/8/16/30s，最多 10 次）+
  `handleAck`（任一 result 停手）+ `handleSync`（重投客户端仍等待的已决策）+ `onAgentConnect`
（重连重投未 ACK 决策 + 清宽限）+ `onAgentDisconnect`（3min 离线宽限 → 取消该设备**未决策**
审批，已决策 delivery 保留待重连重投）。
- `server/src/index.ts`：实例化引擎（send/onOfflineGrace/notifyCancelled）；
  inbound 新增 `ai.approval.request`（采纳客户端 approval_id 入库+通知移动端）、`ai.approval.ack`、
  `ai.approval.sync`；respond 端点对 `ai.approval.*` 审批走 `deliver()`（approved→accept /
  denied→decline），跳过 fire-and-forget publish；`agents.set` 后 `onAgentConnect`，agent close
  后 `onAgentDisconnect`。
- 客户端闭环：alianggate dispatch 新增 `ai.approval.cancelled`（→ `cancelApprovals` 按 id 取消
  本地 waiter）、`ai.approval.request.ack`/`.state`（触活性忽略）。

**类型对齐**：以客户端契约 `ai.approval.*` 为准（与 `ai.message`/`ai.stop` 一致），PhoneServer
新增 `ai.approval.*` 处理而非改名旧流，零回归。`npm run typecheck`（server）通过。
`go build`/`go test -run AgentAI`（client）通过。

#### PhoneServer 需实现（Phase 3 契约）

1. **`delivery_id` + ack 驱动重投**
   - 用户决策后生成 `delivery_id`，发 `ai.approval.response{delivery_id, attempt}` 给目标设备 agent。
   - 起 ack 计时（默认 10s）：收到 `ai.approval.ack{result}` → 标记送达、停手（`applied`/`duplicate`/
     `no_match` 任一即停）；超时 → 新 `delivery_id` 重投，退避 2/4/8/15/30s…，最多 ~10 次（≈5min）。
2. **重连重投**：设备 agent (re)connect（`agent.hello`）→ 把该设备所有「未 ACK 的决策」立即重投。
3. **响应 `ai.approval.sync`**：对客户端报上来的 `pending[]`——有决策则重投；无决策回
   `ai.approval.state{approval_id, status:"pending"}`。
4. **离线宽限取消（C4）**：设备 WS 断开 > grace（如 3min，且无 sync 心跳）→ 取消该设备挂起审批，
   落 DB（`cancelled`+reason），下次重连下发 `ai.approval.cancelled`。
5. **对话结束取消（C5）**：服务端判定对话结束（如 App 关闭对话）→ 取消并落库。
6. **DB 持久化 + 审计**：扩展审批表（`status`/`decided_by`/`decided_at`/`cancel_reason`/
   `cancelled_at`/`delivery_id`/`ack_status`/`ack_result`/`acked_at`/`attempts`/`last_attempt_at`）；
   用户每次 approve/deny/cancel 写审计事件表。

#### 类型对齐决策（⚠️ 必须先定）

读码发现 **alianggate 客户端**用 `ai.approval.request`/`ai.approval.response`/`ai.approval.ack`/
`ai.approval.sync`/`ai.approval.cancelled`（与 `ai.message`/`ai.stop`/`ai.delta` 一致的 `ai.` 前缀），
而 **PhoneServer `index.ts` 现用** `approval.request`/`approval.response`/`approval.request.ack`/
`approval.requested`（**无 `ai.` 前缀**，且 `ApprovalRequest` 字段为
`userId/deviceId/projectId/terminalId/kind/title/summary/command/files/risk/status`，与客户端
`session_id/message_id/approval_id/provider/kind/command/cwd/tool_name/tool_input` 不一一对应）。

二者当前不匹配，意味着审批中继尚未端到端打通。**实现 Phase 3 前需统一**（建议：PhoneServer 对齐到
`ai.approval.*` 命名 + 补齐 `session_id/message_id/approval_id/provider` 等字段映射，或新增显式翻译层）。
此项需客户端与服务端负责人共同拍板，不在我可单方面改动的范围内。

## 文件清单（alianggate 客户端，均已落地）

- `app/http/services/agent_ai.go`：看门狗、activity、ack/sync/cancelled 发送、approval() 发 ack、
  clearPendingApprovalsLocked 返回取消 id、状态回执统一。
- `app/http/services/agent_execution_guard.go`：超时常量重构（idle/hard/interval）。
- `app/http/services/agent_service.go`：`remoteWriter` 间接层。
- `app/http/services/agent_remote_ws.go`：shim writer、transient 断线不杀 AI、重连对账、周期 sync、
  `ai.approval.state` dispatch。
- `app/http/models/agent_protocol.go`：新事件常量 + 契约条目 + response 增 `delivery_id`/`attempt`。
- 测试：`agent_ai_watchdog_test.go`、`agent_remote_writer_test.go`、`agent_ai_approval_proto_test.go`。

## 验证

- `go build ./cmd/... ./app/... ./processor/...` 通过。
- `go vet ./app/http/services/... ./app/http/models/...` 通过。
- `go test ./app/http/services/ -run AgentAI` 全绿（含三个新测试文件）。
- 全量 services 测试除既有 `TestQuickSetupService_Render_MultiProvider`（无关）外全绿。

## 部署

- **Phase 1+2（客户端）可独立先上**——向后兼容，旧服务端不受影响。
- **Phase 3（PhoneServer）需在类型对齐后实现**，并与客户端 Phase 2 同步上线，方可获得重投/对账/
  离线取消的完整可靠性收益。
