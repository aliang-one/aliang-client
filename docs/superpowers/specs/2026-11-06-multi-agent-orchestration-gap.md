# 多 agent 编排引擎：能力缺口定案（2026-11-06）

> 来源：2026-11-05 九 agent 代码取证（master d4ed54a，33 论断对抗复核定案，workflow wf_e812c6a1-2a8）+ 审计次日（2026-11-06）完成的五盲点修复（分支 fix/agentcore-subagent-blindspots）。用户拍板：编排引擎是我们缺失的功能，立档跟进。

## 1. 现状定案：支持到哪一层

| 层 | 状态 | 说明 |
|---|---|---|
| 同机会话内子 agent | ✅ | 三内核都有：agentcore（`~/.claude/agents` 每定义一工具，递归子 run，depth=1）、claudecode（原生 Task 透传 + `ai.subagent` 事件镜像 + 看门狗）、codex（不封不观测） |
| 同回合并行子 agent | ✅ | 内核 `Parallelizable` 批并发（上限 4），结果/钩子按调用序回填；审批闭包互斥进闸 |
| 跨设备 agent 协同 | ✅ | peer MCP 五件套（exec/fetch_file/device_list/device_info/device_note），三注入面，`customer.peer_mcp` 门控 |
| **高层编排引擎** | ❌ **缺失** | 无 team/crew/swarm/graph/planner-of-agents：全仓 crew/swarm/multi-agent/graph 关键词 0 命中；唯一 planner 是 goal 的单 agent 有界规划循环（`agent_goal.go runGoalPlanLoop`） |

## 2. 缺口定义

现在的「多 agent」是**模型自发的、单回合内的、无状态的**：内核/CLI 决定派几个子 agent，跑完即散，宿主没有编排话语权。缺失的是**宿主侧的编排能力**——由产品（而非模型即兴）定义多 agent 的拓扑、分工、聚合与生命周期：

1. **计划性多 agent**：把一个 goal 拆给 N 个子 agent 并行/流水线执行，聚合各自产出（goal 现在是单 agent 多轮循环）。
2. **持久团队**：跨回合存活的命名 agent 组（角色/工具面/预算各自配置），而不是每回合从 `~/.claude/agents` 现拼。
3. **拓扑控制**：子 agent 深度、扇出、依赖关系由配置表达（现在是硬编码 depth=1 + 模型即兴）。
4. **聚合与汇合点**：多子 agent 结果的合并、冲突消解、投票/评审机制（现在只有「各自回最终文本」）。
5. **跨设备编排**：peer MCP 只给了「单点远程执行」原语；没有「把任务编排到多台设备的多 agent 上」的调度层（服务端已 hello 上报画像可供选型）。

## 3. 约束（设计时必须尊重的既有事实）

- **审批一致性**：任何编排层派生的子 agent，其工具审批必须仍走宿主同一闭包（手机审批卡/accept_for_session/mcp__ 默认门）。审批串行门已上移到 `agentCoreApprovalGate`（runAgentCore 对 kernelApprove 统一 wrap，主层+子层共用一道门）；门是单卡语义，信号量持有期含人的等待（SLA 24h）——**并行排队者的有效审批窗口 = min(自身工具窗口, 前序审批等待时长)**，编排层放大并发后此约束要进预算模型。
- **journal 单会话语义**：子 agent 轨迹落同一 session journal（kind=tool 行，父行恒后于子行）。编排层若引入跨会话 agent，需要新的轨迹归属模型。
- **深度=1 是结构保证**：子 registry 不含子 agent 桥（`TestAgentCoreSubagentBridgeDepthOne` 锁死）。放开深度=编排引擎的第一等设计题（防递归失控 + 审批/预算传播），不是把桥塞回子 registry 就完事。
- **超时口径已统一**：子 agent 窗口 = `ALIANG_AI_SUBAGENT_WAIT_CEILING`（默认 6h，与 claude 侧看门狗同旋钮）；编排层的预算必须叠加在这之上而不是绕过。
- **provider 分流**：agentcore 只在显式 provider 或 auto 兜底时可达；goal 回合一律走 CLI 内核。编排引擎选内核时要处理这个不对称。
- **codex 无观测面**：`ai.subagent` 生命周期通道是 claude 专属；编排引擎若以事件驱动，codex 子任务是不可观测的黑盒。

## 4. 候选演进路线（按性价比排序，未拍板）

- **R1 goal 多执行器**（近期最实）：goal 的 task 执行层允许单 task 派生 N 个并行子任务（复用现有 goal.turnCount 账本 + 内核并行批），先做「一个 task 内的 fan-out/fan-in」，不引入持久团队。
- **R2 编排 profile**：`~/.claude/agents` 之上加一层编排定义（团队/流水线的声明式描述），宿主装配时按 profile 注入协作指令与聚合钩子——复用「宿主包桥」哲学，内核零改动。
- **R3 深度=2 受控嵌套**：给编排 profile 显式开深度=2（孙 agent），预算/审批/超时逐层传播；需要先解 journal 归属与 `ai.subagent` 事件树。
- **R4 跨设备编排**：服务端按 hello 画像做任务→设备→agent 的调度，peer MCP 原语之上加「远程子 agent 会话」语义（需要 PhoneServer 侧配套）。

## 5. 已修复的周边缺陷（同分支，2026-11-06）

1. 子 run 墙钟连坐：内核 `TimeoutHint` 可选接口，子 agent 桥 6h（`ALIANG_AI_SUBAGENT_WAIT_CEILING`，与 claude 侧同旋钮）。
2. 子 agent 串行：内核 `Parallelizable` 批并发（上限 4），顺序/钩子/toolCalls 单线程回填；子 agent 审批闭包互斥。
3. 技能清单不进子 run：`skillsBlock` 装配期求值注入子 run System。
4. peer 工具取消传播：`tools.HandlerFunc` 带 ctx，`RunWithGuards` 护栏窗口变 guardCtx（超时=取消信号，WS 会话退出级联取消）；peer_exec 桥补 `TimeoutHint`=630s。
5. task 无上限：`truncateAgentCoreSubagentTask` 64KB rune 安全封顶 + 截断标记。

关键文件：`app/agentcore/tools.go`（接口+常量）、`app/agentcore/loop.go`（executeTools 批并发/executeOneInner 窗口）、`app/http/services/agent_ai_agentcore_subagent.go`（桥三件+task 封顶+审批互斥）、`app/tools/dispatch.go`（ctx 贯穿）、`agent_remote_ws.go`（sessionCtx）。
