# 手机消息路由到活着的 TUI — 设计规格

> 日期：2026-10-20 · 状态：设计已评审（用户三项决策已拍板）· 实现计划见 `../plans/2026-10-20-tui-executed-phone-turns.md`

## 1. 问题

手机对一个**本地已存在的 Claude 会话**发起聊天时，若该会话正被一个**空闲的 TUI**（用户在终端里 `claude` 打开的窗口）持有，当前行为是：

- agent 自己 spawn `claude -p --resume <nativeId>` 执行该回合；
- TUI 屏幕上**看不到**这个回合（直到用户手动刷新历史）；
- TUI 随后若发起自己的回合，两个 writer 交替写同一份 native transcript。

用户要的是：**TUI 开着且空闲时，手机消息直接送进那个 TUI 执行**。这样 TUI 与手机天然共享同一份上下文，TUI 自动更新，两边看到同一次对话。

## 2. 现状核查（六个断点，全部有代码证据）

| # | 断点 | 证据 |
|---|---|---|
| ① | 无路由决策：`message() → runUserMessage → runCLIPass` 链上没有「TUI 空闲则改走注入」的分支 | `agent_ai.go:1371` → `:2139` → `:4922` |
| ② | 注入管道只服务摘要通知，不服务对话：`ccPeerInject` 全仓唯一调用方是 `tuiSync` 的 digest 路径 | `cc_peer_messaging.go:401`（`ccPeerSyncFireWith` 内） |
| ③ | 注入回合无 run 生命周期：`ai.run.started / ai.delta / ai.done` 在 spawn 模式下由 `agentAIRunEmitter` 产出，注入模式一个都发不出 | `agent_ai.go:5067` / `:6540` / `:5260` |
| ④ | 无流式桥：注入后输出只落在 TUI 屏幕 + jsonl，手机收不到增量 | 无任何 jsonl tail 代码 |
| ⑤ | 注入回合无完成检测/停止/审批关联 | `stop()` 的 SIGINT 分支（`agent_external_interrupt.go:120`）与回合无关联 |
| ⑥ | 能力前提未做门槛：messaging UDS 需 CC ≥ 2.1.224 且 pid record 带 `messagingSocketPath`；Windows named pipe 是 v2 未实现 | `cc_peer_messaging.go:307` 注释 |

**已有的可复用资产（无需重写）：**

| 资产 | 位置 |
|---|---|
| TUI 存活检测 + pid 身份验证（防 pid 复用） | `liveClaudeTUIRecord` / `liveClaudeTUIRecordWith`，`agent_external_interrupt.go:94` |
| busy/idle 状态判定 | `externalTUIBusy`，`agent_external_interrupt.go:167` |
| peer 注入帧构造（auth + user 帧，`msgV/msg_id/priority/fromMode`） | `ccPeerUserLine` / `ccPeerInject`，`cc_peer_messaging.go:98` `:156` |
| permission class 探测（bypass/prompting） | `ccPeerTUIPermissionClass`，`cc_peer_messaging.go:181` |
| held（被扣留）检测 | `ccPeerWatchHeld`，`cc_peer_messaging.go:228` |
| transcript 定位（跨 project 目录，mtime 择优） | `ccPeerSessionJSONL`，`cc_peer_messaging.go:73` |
| 外部 SIGINT 停止 + 3s 去抖 | `interruptExternalClaudeTurn`，`agent_external_interrupt.go:120` |
| spawn 前 TUI busy 拒绝（`tui_busy` 错误码） | `guardExternalTUIClaudeSpawn`，`agent_external_interrupt.go:181` |
| run 生命周期事件 + 落盘 + 断线补推 | `agentAIRunEmitter`，`agent_ai.go:375` |

## 3. 用户决策（三项，已拍板）

1. **审批语义**：注入回合的工具审批**在 TUI 内完成**。手机端不做审批转发；手机 UI 需能表达「此回合由 TUI 执行，审批请在终端处理」。v1 不改审批协议。
2. **账号/通道**：注入回合**有意跑在 TUI 自己的环境**上（TUI 的账号、模型、权限模式——即个人通道），这是「天然就是 TUI 上下文」的应有之义。与 [[ai-run-dual-channel-design]] 的平台通道隔离**方向相反但是有意为之**，需在文档与 UI 上明示。
3. **流式**：手机端 `ai.delta` **由 tail jsonl 合成**，不做 v1 延后。

## 4. 设计

### 4.1 路由点

**唯一插点：`runUserMessage` 的最前端**（`agent_ai.go:2139`，在 `guardExternalTUIClaudeSpawn` 之后、任何 run 状态变更之前）。

覆盖面验证：`message()`（`agent_ai.go:1554`）与 `runStart()`（`agent_ai.go:1635` → `m.message()`）两条入口最终都汇入 `runUserMessage`，所以一处路由即全覆盖。

### 4.2 路由条件（全部满足才注入）

记 `nativeID = session.resumeSessionID`：

1. `provider ∈ {claude, claudecode, auto}`（auto 是 imported session 的默认值，解析到 claude）；
2. `nativeID != ""`（fresh spawn 无 native id，不可注入）；
3. `!run.readOnly && len(run.goalIdentity) == 0`（goal/fork 回合有沙箱与报告契约，**一律不进 TUI 路径**）；
4. `liveClaudeTUIRecord(home, nativeID)` 返回存活记录；
5. `record.Status == "idle"`（busy → 不注入，交给现有 spawn guard 的 `tui_busy` 拒绝路径）；
6. `record.MessagingSocketPath != ""` 且以 `/` 开头（unix UDS；Windows named pipe 是 v2，明确不支持）；
7. jsonl 可定位（`ccPeerSessionJSONL` 非空）——tail 桥的前提。

任一不满足 → 落到现有 spawn 路径，**行为与今天完全一致**。

### 4.3 执行器形态

新增文件 `app/http/services/agent_tui_turn.go`，承担「注入 + tail + 生命周期」三件事；路由判定抽成纯函数便于单测。

```
runUserMessage
  ├─ tuiTurnDecide(input) *tuiTurnPlan          // 纯函数：条件 1-7 判定 + 快照注入所需字段
  │
  ├─ plan == nil  → 现有 spawn 路径（不变）
  │
  └─ plan != nil  → go m.runTUITurn(ctx, session, run, content, plan, emitter)
```

`runTUITurn` 时序：

1. **登记**：`session.cancel = cancel`、`session.activeWriter = emitter.emit`、`session.runSeq++`、`session.activeRunID = runID`（与 spawn 路径同一组字段，使 `stop()` / 审批通道 / LRU 全部无需改动即可工作）。
2. **`ai.run.started`**：复用 spawn 路径的 payload 形状，附加 `"executor": "tui"`、`"state": "running"`。
3. **用户消息入历史**：与 spawn 路径一致地 append 到 `session.history`（手机侧列表即时显示）。
4. **注入**：`ccPeerInject(socket, loadClaudePeerToken(home,pid), content + agentAIAttachmentPromptSuffix(run.attachments, true), fromMode)`，`fromMode` 来自 `ccPeerTUIPermissionClass(jsonl)`。注入前记录 jsonl 大小 `offset`。
   ⚠️ 必须是**原始用户 content**，不能用 `run.prompt`——后者是 `buildAgentAIPrompt` 包装后的文本（带 "You are continuing an Aliang remote agent AI chat session" 前缀 + 全量历史，`agent_ai.go:6748`）；imported 会话碰巧有 resume id 才让 `prompt = content` 碰巧成立。附件清单也必须在这里拼：spawn 路径由 `withAgentAIAttachments`（`agent_ai.go:7050`）代劳，注入路径没有。
5. **tail 桥**：轮询（间隔 200ms，可经变量覆写）jsonl 从 `offset` 起的新行：
   - `type == "user"` 且非 `isMeta`、非 `tool_result`、非 `<command-name>`/`<local-command` 前缀：**回显注入的用户消息**（`ai.delta` + `channel:"user"`），用于手机确认「消息已进 TUI」。
   - `type == "assistant"` 且 `isSidechain == false`：按 `message.content[]` 的 block 派发，字段契约见 `app/http/models/agent_protocol.go:219-224`——
     - `text` → `ai.delta` + `channel:"assistant"`
     - `thinking` → **独立事件 `ai.thinking`**（带 `item_id`，见 `agent_ai.go:6282`），与 spawn 路径一致，不进正文通道
     - `tool_use` → `ai.command`，**必填 `item_id` + `status:"started"`**（缺了服务端判畸形），`item_id` 取 `"tb" + apiBlockIndex``
   - **粒度说明**：transcript 是**块级整行写入、uuid 唯一、不重写**（2026-10-20 在本机 2.1.286 实测：`apiBlockIndex` 递增、`duplicate_uuids = 0`、`ends_with_newline = true`）。故 `ai.delta` 是**块级增量**而非 token 级——块内文字一次性到达。这是 v1 的既定取舍。
   同时起 10 秒 `ai.run.progress` 心跳（`agentAIRunProgressInterval`，`agent_execution_guard.go:75`，与 spawn 两条路径同间隔）——TUI 长时间思考时手机需要「仍活着」的信号，否则服务端 10 分钟安静 sweeper 会把回合打翻成 timed out。
6. **完成判定**（满足其一即结束并进 7）：
   - 观察到**新的 user 类型行**（`tool_result` 除外）之后，出现**连续两个轮询周期无新 assistant 行**且 pid record 转 `idle`/记录消失 → 回合完成；
   - 或全局回合超时（复用 watchdog 常量）；
   - 或 `session.cancel` 被 `ai.stop` 触发。
   超时值可经包级变量覆写（测试用）。
7. **`ai.done`**：经 `emitter.emit` 发出，携带 `files_touched_count`（从本回合 observed tool_use 的文件路径去重计）与 `git_changed_count`（复用 `countGitChanged`）、`"executor": "tui"`。经 emitter 发出即自动获得 run_id/event_seq、终态落盘、断线补推——**与 spawn 路径完全同构**。
8. **清理**：`session.cancel = nil` 等（与 spawn 路径的 `defer` 清理对齐）。

### 4.4 停止

`ai.stop` 无需改代码：`stop()` 里 `session.cancel()` 会 cancel 执行器 ctx；执行器 defer 里调 `interruptExternalClaudeTurn(home, nativeID)` 对 TUI pid 发**一次 SIGINT**（= 一次 Esc，TUI 保持打开），随后按现有 `ai.status(status=stopping)` 回复。3 秒去抖沿用现成常量。

### 4.5 失败与回退

| 失败点 | 行为 |
|---|---|
| 注入前判定中途失效（TUI 退出） | 直接发 `ai.error`（含 `tui_unavailable` 码），**不自动回退 spawn**（避免同一回合被双跑）；服务端可重试 |
| `ccPeerInject` 拨号/写入失败 | 发 `ai.error`（`tui_inject_failed`），同上不回退 |
| 注入后被 CC held（bypass 门） | `ccPeerWatchHeld` 检出 → `ai.error`（`tui_held`），提示去终端处理；**不**更新 baseline |
| tail 期间 jsonl 消失 | 立即收尾为 `ai.error`（`tui_transcript_lost`） |
| TUI 忙碌翻转（用户在 TUI 里同时开始回合） | tail 继续；新 assistant 行仍归入本回合。停止仍走 SIGINT（一次 Esc） |

**关键安全属性：一次手机回合，要么注入要么 spawn，永不双跑。** 这是路由判定必须发生在任何 run 状态变更之前、且注入失败不回退 spawn 的原因。

**已知限制（v1，实现期实测确认）：**

1. **流式粒度是块级**，非 token 级（transcript 块级整行写入，见 §4.3 粒度说明）。
2. **provider=auto 且存在空闲 claude TUI 时回合固定走 TUI**；用户显式选 codex 则不注入。
3. **停止响应最多延迟约 3 秒**：注入后 `ccPeerWatchHeld` 同步观测 held 窗口，期间收到的 stop 在窗口结束后才生效。且该提前 return 必须补发 `ai.status(stopping)`，否则手机 UI 卡在「执行中」。
4. **Windows named pipe 注入未实现**（v2），Windows 上自动回退 spawn。

### 4.6 与既有机制的交互

- **`guardExternalTUIClaudeSpawn`**：注入回合结束时 TUI 已 idle，下一个手机回合会再次被路由到注入（而非 spawn）——这是期望行为。若用户同时手动在 TUI 发起回合使其 busy，则后续手机回合被 `tui_busy` 拒绝，与今天一致。
- **inventory / 手机会话列表**：注入回合天然写同一份 jsonl，扫描与 status overlay（`applyClaudeRenameNames`，`agent_inventory.go:1117`）零改动即正确显示 running/idle。
- **binding / resume id**：注入不改变 native id，`m.bindingForConversation` 与 `ai.done` 的 `claude_session_id` 回显逻辑不变。
- **双 poller 与 rename**：不受影响（本功能不产生新标题写入路径）。

### 4.7 明确不做（v1 范围外）

- 手机侧审批转发（决策 1：TUI 内审批）。
- Windows named pipe 注入（v2）。
- 非 claude provider（codex/opencode）的 TUI 注入。
- goal / fork / readOnly 回合的 TUI 路由（4.2 条件 3）。
- busy TUI 的消息排队（现有 guard 拒绝语义不变）。
- token 级流式（4.3 块级取舍）。

## 5. 测试策略

新文件 `app/http/services/agent_tui_turn_test.go`（纯函数 + 执行器行为，全部用 fake home / 临时 socket / 覆写轮询间隔，**不依赖真实 CC 进程**）：

| 测试 | 断言 |
|---|---|
| 路由判定：全部条件满足 | 返回 plan |
| 路由判定：provider 非 claude | nil |
| 路由判定：无 native id（fresh） | nil |
| 路由判定：readOnly / goalIdentity | nil（两条） |
| 路由判定：TUI busy | nil |
| 路由判定：无 messaging socket | nil |
| 路由判定：record 不存在 / pid 死 | nil |
| 执行器 happy path（fake UDS + 手工 jsonl） | 帧内容正确（**原始 content、无 buildAgentAIPrompt 污染**）；`ai.run.started`→`ai.delta`(channel=user)→`ai.delta`(channel=assistant)→`ai.command`(item_id+status)→`ai.done` 顺序与 run_id/event_seq 正确 |
| 执行器注入附件 | 注入帧含 `agentAIAttachmentPromptSuffix` 清单 |
| 执行器：tool_use → `ai.command` | 事件类型正确 |
| 执行器：`ai.stop` → SIGINT 一次 + `ai.status(stopping)` | `externalInterruptFunc` 捕获 pid 恰好一次 |
| 执行器：注入失败 → `ai.error(tui_inject_failed)` 且不 spawn | 错误码正确；无任何子进程启动 |
| 执行器：held → `ai.error(tui_held)` | 错误码正确 |
| 执行器：transcript 丢失 → `ai.error` | 错误码正确 |
| 回归：路由不命中时 spawn 路径原样工作 | 现有测试全绿 |

命名守则（见 [[go-implicit-goos-suffix-test-trap]]）：测试函数名不得以 `_windows` / `_linux` / `_darwin` 结尾，新测试先用 `go test -run <Name> -v` 确认真实执行。

## 6. 发布说明素材

- 手机消息在目标会话的 TUI 空闲时，将直接送入该 TUI 执行；TUI 与手机同步显示，上下文天然连续。
- 该回合的工具审批在终端内完成（手机不弹审批）；该回合使用 TUI 自身的账号与模型。
- TUI 正忙时保持现有行为：手机回合被拒（可在回合结束后重试）。
- 需要 Claude Code ≥ 2.1.224 且 TUI 记录了消息 socket；不满足时自动回退原有的无头执行方式（行为不变）。
