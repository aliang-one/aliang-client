# 仪表盘子 agent 面板设计（2026-11-07）

## 背景与目标

agentcore（AliangCode）内核的子 agent 状态镜像已落地（ai.subagent 帧，见
2026-11-07 断链修复 ad23584：帧经 streamStructuredAIDelta 透传进下行）。本设计把
状态消费落到**网页仪表盘的 aliangcode 对话页**：

- 常驻入口（消息区底部）：汇总本会话子 agent 运行状态（N 运行中 · M 完成）
- 点击 → 子 agent 面板：列出会话内全部子 agent，可切换查看
- 单个子 agent 详情：实时状态 + 工具调用时间线 + 完整报告
- 实时会话与历史回看都支持

**明确不做**：手机端（App 已有 SubagentRows 消费链，独立演进）、PhoneServer 改动、
claude/codex 内核的时间线（仪表盘会话列表本就只认 agentcore——useDeviceSessions.js:58）。

## 现状地基（取证定案）

- WS 实时通道：`/api/agent/ai/stream`（HandleAIStream，localhost origin），ai.subagent 帧
  已透传到 `writeJSON`（=origin 写+本地 fanOut 扇出）——ad23584。
- 详情 API：`GET /api/agent/session?id=`（HandleVibeSession→VibeSessionDetail→
  findAgentVibeSessionDetail 读 journal），transcript 里 tool 行 `role:"tool"`、
  ID=`tool-%06d`，**前端映射层丢弃**、且无 task_id 关联。
- journal 行 schema：session/msg/turn/tool/end/approval（agent_ai_agentcore_journal.go）。
- 前端：ChatPage（壳）→ChatMessageList→ChatMessageBubble；WS 消费集中在
  useAliangChat.js 的 handleAIEvent switch（ai.subagent 无 case，default 丢弃）；
  会话刷新=进页拉取+WS 事件 2s 节流（useDeviceSessions）。

## 一、数据面：journal 增强

### 新行 kind=subagent（两种）

task_id 生成规则(**含回合级消歧**,评审 H1):mirror 每回合新建、seq 归零,裸
`agentcore-<名>-<seq>` 会跨回合重号(同会话两个回合各派一次同名子agent,读侧按
task_id 分组会把不同回合实例合并成一张卡)。定案:`task_id = agentcore-<run.messageID>-<名>-<seq>`
——run.messageID 是回合唯一锚(mirror 已持有),WS 帧与 journal 行同款,跨回合唯一。

```jsonl
{"kind":"subagent","event":"start","task_id":"agentcore-msg7-researcher-1","name":"researcher","description":"加密解密调研","ts":"..."}
{"kind":"subagent","event":"finish","task_id":"agentcore-msg7-researcher-1","status":"completed","summary":"<≤200 rune>","result":"<完整报告,≤64KB,rune安全截断>","total_tokens":8123,"duration_ms":45230,"ts":"..."}
```

- start 行由 mirror.start 落（即时持久化→进行中状态历史可见）；finish 行由 mirror.finish 落。
- **落行 gating（评审 L1）**：registry 拒收（cap 64 满，tracked=false）时 start/finish/工具行
  三类 journal 行一律不落（与 ai.subagent 帧同 gate）——否则面板出现永久 running 僵尸条目。
- result 封顶 64KB，**rune 安全截断**（字节截断会劈多字节字符出 U+FFFD，不用
  truncateAgentCoreMCPOutput 的字节劈法）；summary ≤200 rune。
- journal 行 16MiB 单行上限（agentVibeJSONLMaxLineBytes）远高于 64KB，无压力。

### tool 行加关联字段

子 run 工具行的 tool 行新增可选 `subagent_task_id` 字段（主 run 工具行不带）。写点：
agentCoreSubagentTools 的组合钩子里，tracked 时调
`m.coreJournal.appendSubagentTool(sessionID, taskID, tool, summary, isErr)`（新方法，
行内带 subagent_task_id）；未 tracked 回落原 appendTool。

### mirror 接线

agentCoreSubagentMirror 增加 `journal *agentCoreJournal` 字段（runAgentCore 注入
m.coreJournal；nil=不落盘，兼容既有测试），start/progress/finish 方法内同步写行。
run.subagents==nil 的退化路径 mirror=nil，行为不变。

## 二、详情 API：服务端组装 subagents[]

`AgentVibeSession`（models/agent.go）新增：

```go
Subagents []AgentVibeSubagent `json:"subagents,omitempty"`
type AgentVibeSubagent struct {
    TaskID, Name, Description, Status, Summary, Result string
    TotalTokens int64
    DurationMs int64
    StartedAt, EndedAt string // RFC3339
    Tools []AgentVibeSubagentTool // {Tool, Summary, IsError, Ts}
}
```

读侧（agent_inventory_agentcore.go 行解析 switch 加 case）：
- subagent start 行 → 建 entry（Status=started）
- subagent finish 行 → 补 Status/Summary/Result/TotalTokens/DurationMs/EndedAt
- tool 行带 subagent_task_id → 归入对应 entry.Tools（时间序）
- 组装后按 StartedAt 排序输出；无 subagent 行的旧 journal → 空（omitempty 不下发）

向后兼容：未知 kind 的旧行维持现行为（跳过）；tool 行无 subagent_task_id 维持现行为。

**列表/同步载荷剥离（评审 H2）**：读侧组装点被列表与详情共用，但清单出口
（`summarizeAgentVibeSessions`→`/api/agent/sessions` 与云端同步快照 VibeSessions 封顶 200 条）
必须 `session.Subagents = nil`——否则每条 ≤64KB 的 result+tools 无声放大清单带宽与解析成本，
且 PhoneServer zod 到手即剥、纯浪费。**详情 API 契约 ≠ 清单契约**，补契约测试（清单响应不得含
subagents 键）。subagent 行在 truncateBefore 区间归属与 healMergeJournal 搬迁中随宿主 msg 行
区间处理，与 tool/turn/end 同规。

## 三、WS 实时消费（前端）

- 重连预算归零集合（useAliangChat.js AGENT_AI_RECONNECT_RESET_TYPES，:37-41）加
  `ai.subagent`（子agent活动证明管道活，重连预算随之归零——有利副作用）；消费点=
  handleAIEvent switch 加 case（现 default 直接 return 丢弃）。
- 新增纯函数 reducer `applySubagentEvent(state, msg)`（可单测）：按 task_id upsert——
  started→新增条目（name/description/status）；progress→更新 current_step/last_tool/
  tool_uses；completed|failed→status+summary 定稿。
- **会话归属规则（评审 M2）**：仅当帧 `session_id` 等于当前打开会话 id 才消费；
  不命中（含他端/他会话帧落进 fallback 挂活跃会话的兜底链，:2141-2143）一律丢弃——
  防止他会话子agent帧污染当前面板。
- **时间线单真源（评审 L4）**：WS 只维护状态/当前步骤/summary，时间线一律以详情
  journal 为准——省掉 progress 累积与权威时间线的合并去重语义。
- **断线自愈（评审 H3，替代原「随快照自愈」的不实论断）**：快照读侧
  （readAgentCoreJournalTranscriptFrom）不解析 subagent 行，断连窗口内的事件不经快照恢复。
  定案：WS 重连成功（既有 800ms 重连+重订阅）后，对当前打开会话触发一次**仅消费
  subagents[] 的详情刷新**（journal 是持久真值，补齐断连窗口丢失的状态与终态）。
- **报告获取（评审 M1）**：completed 到达且用户正在看该子 agent 详情时，静默重拉详情，
  **只消费返回的 subagents[]，转录一律不落壳**（详情装载是整体替换 messages，全量重拉会
  把正在流式输出的直播气泡顶掉）。不在看则下次打开面板时拉。

## 四、前端 UI（app/website，跟随 ChatPage 风格）

### 入口条（SubagentStatusBar.vue）

UI 文案一律走 i18n 键（仪表盘 useI18n 惯例）；e2e 断言中文须 locale zh-CN（既有坑）。

- 位置：ChatPage 消息区底部固定条；仅 agentcore 会话且 subagents 非空时渲染。
- 内容：`子agent ● N 运行中 · M 完成`（运行中带 spinner）；点击开面板。

### 面板（SubagentPanel.vue）

- 桌面：右侧滑出 drawer；移动（<768px）：全屏遮罩。跟随仪表盘既有弹窗响应式约定
  （flex-col+min-h-0 内滚/dvh/触控≥40px）。
- 左列表：每行=名称/描述、状态徽标（spinner/✓/✗）、耗时、token 数；点击切换右侧详情。
- 右详情（SubagentDetail.vue）：状态头（实时字段）→ 工具时间线（时间序，is_error 红）→
  完整报告（markdown 渲染+复制按钮）；运行中显示当前步骤与占位。

### 数据装配

- 打开会话（实时或历史）：详情 API 的 subagents[] 初始化面板状态。
- 会话进行中：WS ai.subagent 增量叠加（reducer）。
- 既有 tool 行丢弃逻辑不变（时间线由 subagents[].tools 承载，不往气泡流塞 tool 气泡）。

## 五、边界与错误处理

- 旧 journal（无 subagent 行）→ 空数组，入口条不渲染，零回归面。
- registry 满载（cap 64）fail-soft：丢事件不影响任务（既有语义），面板可能少个别条目——
  可接受。
- result 超 64KB 截断（写侧已保证）；summary 200 rune。
- WS 断线：既有 800ms 重连+快照对账收敛，面板状态随快照/详情刷新自愈。
- 权限：零新端点；WS/详情 API 沿用既有会话边界（localhost origin/订阅只读）。

## 六、测试策略

- 后端：mirror 落行单测（start/finish/工具行关联字段）；读侧组装单测（新 journal→
  subagents[] 完整、旧 journal→空、tool 归组排序）；详情 API 契约。
- 前端：reducer 纯函数 vitest（started/progress/completed/乱序/多 task）；入口条条件渲染；
  面板列表切换/详情渲染（报告 markdown/is_error 红）；历史打开装配。
- e2e：模拟 mirror 事件流→入口条出现→面板切换→详情字段齐全（含 total_tokens/duration）。

## 七、交付切分

1. 后端（journal 写+读侧组装+API 字段）——单仓独立可交付
2. 前端（WS 消费+入口条+面板）——依赖 1
3. e2e——依赖 1+2
