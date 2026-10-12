# 子 agent 感知与防卡死 — 全链路设计

日期：2026-09-26
状态：已评审（会话内三轮收敛）
范围：alianggate（agent 客户端，Go）+ AliangPhoneServer（服务端，Node/TS）+ AliangVibeCodingPhone-cosdl（手机端，React Native）

## 背景与实证

Claude Code 派发子 agent 时落盘形态（2026-09-25 现场实验实锤）：

- 子 agent transcript 实时写在 `~/.claude/projects/<项目>/<父sessionId>/subagents/agent-<agentId>.jsonl`，**派发即创建**
- 每行带 `sessionId`（=父会话 id）、`agentId`、`isSidechain: true`；首行 user=派发 prompt，末行 assistant=产出
- 父会话 jsonl 里对应 `name=="Agent"` 的 tool_use（input 含 `subagent_type`/`description`/`prompt`），结束时补 tool_result
- 工具名已从 `Task` 改为 `Agent`
- 手机端发起的 run 走 `claude --print --verbose --output-format stream-json`（agent_ai.go:6757），工具全集开放，**子 agent 派发会发生**
- codex 无对应机制，明确排除

## 问题：边界情况核验（2026-09-26 逐条验证）

| 场景 | 现状 | 结论 |
|---|---|---|
| claude 进程死亡/流断开/用户停止 | `runCLI` 入口 `defer m.clearRunning(...)`（agent_ai.go:3203）无条件清 running；服务端 agent 掉线 15s 宽限后 legacy run 置 idle+failed | ✅ 不卡 |
| gate 进程重启 | run 为内存态随进程消失；服务端 sweeper：静默 10min→timed_out、绝对 6h 封顶（sweepers.ts） | ✅ 有兜底 |
| 手机断连重连 | RealtimeBroker 重连缓存 + REST 补拉 | ✅ 已覆盖 |
| **子 agent 僵死** | `pendingToolUses>0` → `idlePaused()` → idle(10min)/no-progress(60min) 看门狗全暂停；同时 `ai.run.progress` 心跳（10s 一次，agent_ai.go:4919）照发，服务端静默清扫永不触发 | ❌ **卡最长 6h**（服务端绝对封顶），心跳在替僵死撒谎 |

**核心洞**：防误杀机制（看门狗暂停 + 心跳保活）叠加后，僵死检测被完全屏蔽。审批等待已有独立上限 `agentAIApprovalTimeout`（默认 24h，execution_guard.go），与之正交。

## 目标

1. 僵死的工具/子 agent 等待有专属上限，不再依赖 48h 硬顶兜底（审批时间不计入）
2. run 内子 agent 派发/完成对手机可见（消除长 run 黑盒静默）
3. 子 agent 内部过程透出 + 僵死可见化（先提示疑似卡死，后终止）
4. TUI 会话卡片显示"子agent运行中(N)"，消除"会话半天没动静"误判

## 非目标

- 不做子 agent 独立实体的全链路协议（独立消息类型/独立寻址/独立状态机）——子 agent 是随父 run 生灭的瞬态资源，正确形态是挂在父实体下的子资源，独立实体会引入生命周期对齐/幽灵状态问题
- codex 侧不做（无对应机制）
- 手机端审批不做（headless 模式权限在 run 启动时已定，子 agent 继承）

## 分阶段设计

### Phase 0：堵洞（只改 alianggate，不动协议）— 已实施（本分支）

`agentAIActivity` 增加连续工具等待时钟 `toolWaitStartedAt`：

- `beginToolUseWait`：pendingToolUses 0→1 跳变时记录起始时刻
- `endToolUseWait`：归零时清时钟
- `setAwaitingApproval(true)`：清时钟（**人的决策时间不计入**——审批已有 24h 上限管着）
- `setAwaitingApproval(false)` 且仍有 pending：重新起表

watchdog（`agentAIWatchdogLoop`）新增分支：时钟连续超过 `agentAIToolWaitCeiling`（默认 1h，env `ALIANG_AI_TOOL_WAIT_CEILING` 可调，0 禁用）→ killReason=`tool_wait_timeout` → 终态映射复用现有 `timeout` 状态族 → **服务端零改动**。

新增常量默认约束：1h < 48h hardCeiling（保证先触发）。

### Phase 1：run 内派发可见（三端协议）

新事件类型 `ai.subagent`（started/completed 两态，payload：session_id/message_id/item_id/status/description/subagent_type）：

| 仓库 | 改动点 |
|---|---|
| alianggate | `claudeToolUseEvents` switch 加 `case "Agent"`（agent_ai.go:5851 区域）：tool_use→started、tool_result→completed |
| AliangPhoneServer | `STRUCTURED_AI_EVENT_TYPES` 白名单（structuredEvents.ts:4-8）、`parseStructuredEvent`（:98-124）、`mergeStructuredEvent`（:180-213）、`slimEnvelope`（:216-223）、保活名单（sessionStatus.ts:77-86）。DB 列为 TEXT，无需迁移 |
| AliangVibeCodingPhone | 管道 10 处：platformTransport 联合+toEvent 分支、aiStreamBatching 白名单、platformModels 联合+envelopeToActivity、structuredSlice、activitySummary/grouping/renderMemo、ActivityBlock 新 SubagentRow（仿 CommandRow 展开）、i18n、测试 |

兼容性：服务端白名单不认识新类型时静默丢弃不崩 → 天然支持服务端先部署灰度。

### Phase 2：子 agent 内部过程透出 + 僵死可见化

- gate 在 run 存续期 tail `<父会话>/subagents/agent-*.jsonl`（复用 bufio 逐行模式），子 agent 工具活动节流后以 `ai.subagent{status:progress}` 推出，与 `ai.run.progress` 共用 10s 节拍器
- **僵死检测闭环**：子 agent 文件停更 → progress 停 → Phase 0 时钟照跑 → 手机先见"疑似卡死"提示（progress 停更 + 超过 T1 阈值时由 gate 发提示事件），超过 T2 被 Phase 0 终止
- 手机端 SubagentRow 扩展展示每个子 agent 的最近活动行

### Phase 3：TUI 会话卡片"子agent运行中(N)"

- inventory 不再 skip `subagents/`（agent_inventory.go:739），新增轻量 meta 读取器（只读首尾行），按父目录名映射父会话
- 父会话 `Status=running` 判定复用 `claudeStatusFreshnessWindow`(3min) 模式；`UpdatedAt` 取 max(自身消息, 子 agent mtime)
- **扩 digest**（agent_vibe_digest.go 目前不 hash subagents 目录，不扩则要等 1 分钟兜底 tick）
- `AgentVibeSession` 加平铺字段（如 `subagents_running`、`subagent_activity`），hello 快照全量 upsert 天然透传（需一次联调确认服务端不剥字段）
- 手机端会话卡片加徽标

## 部署顺序

1. Phase 0 独立先行（单仓、零协议变更）
2. Phase 1 三端联动，**服务端先上**
3. Phase 2 依赖 Phase 1 的类型
4. Phase 3 独立主题，可与 2 并行

## 已知存量问题备忘

- 子 agent transcript 被双重排除：walk 层 skip（agent_inventory.go:739）+ 解析层 isSidechain 丢弃（agent_inventory.go:1086-1088）
- digest 不含 subagents 目录 mtime（agent_vibe_digest.go:26-108）
- 全仓 go test 有预存 flake（agent_ai 子进程超时类，单包复跑即绿），回归时以单包为准
