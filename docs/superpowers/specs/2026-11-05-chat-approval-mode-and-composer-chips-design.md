# 设计:聊天页审批模式(每次询问/放过所有/默认推荐)+ 模型/effort chip 迁入 composer

- 日期:2026-11-05
- 状态:设计定稿(brainstorming 产出,用户已批范围 A/布局 A,"开始吧")
- 仓:alianggate(后端 + website 前端)
- 基线:master@dc2bfa0

## 1. 背景与动机

aliangcode 每次对话的工具审批只能通过手机审批卡放行,网页聊天页没有权限模式入口。用户要求:输入框旁可设「每次询问 / 放过所有 / 默认推荐」;同时把模型/effort 设置迁到输入框旁(Cursor/VSCode 式)。

现状事实:
- 顶栏 ChatSettingsBar 已有 目录/模型/effort/更多(会话设置条,pendingConfig 下轮生效管道);composer(输入框区)无任何设置入口
- 三内核共用决策点:agentcore 走 `agentCoreApprove`(内核 Config.Approve 回调),claude/codex 走审批 hook(`evaluateApprovalDecisionForHook` 调用点);策略规则 auto_approve/auto_deny/require → 会话批准记忆(accept_for_session)→ 手机云端往返
- run 级无审批模式字段(全新能力);agentAIRun 已有 session 字段镜像管道(effort/model/contextWindow/maxTokens/disabledMCPServers 同款)

## 2. 目标

1. 会话级 `approval_mode`:`""`(默认推荐=现状)/`ask_all`(每次询问)/`bypass_all`(放过所有),三内核统一生效。
2. composer 底部新增三个 chip:权限模式/模型/effort;顶栏模型/effort 选择器移除(目录+更多留顶栏),状态源唯一。

## 3. 非目标

- 手机端 UI 跟随(手机审批卡行为不变;模式跨端展示后续线)
- 设备策略编辑(策略本身不动,本设计只是 run 级覆盖)
- goal run 路径(已有 pre-approved plan 短路,本模式不接管 goal run)

## 4. 数据与内核(后端)

### 4.1 会话字段与载荷

- 会话状态新增 `approvalMode`(字符串,空=默认);create/steer 载荷可选键 `approval_mode`(白名单 `""|ask_all|bypass_all`,非法值回落空)。
- 流转:前端 composer chip → applySessionSettings 落 conv → resendCreate(ai.session.create 载荷,effort/model 同通道;steer 不带设置键) → session 字段 → 下回合 run 镜像到 `agentAIRun.approvalMode` → ai.run.started 清挂起(「下轮生效」角标沿用)。
- **journal 会话头行**(kind=session header,effort/model 同族键所在,非逐回合行)记录 approval_mode。
- **回读腿**(刷新恢复,缺了会出「chip 显示默认但后端仍是 bypass_all、create 重发悄悄重置」缺陷):inventory 解析(agent_inventory_agentcore.go 会话头 model/effort 处)→ 服务端清单下发 → `deviceSessionToConversation` 回填(chatSettings.js)→ chip 状态恢复。

### 4.2 决策短路径(三内核同语义,抽公共判定)

| 模式 | 策略 auto_deny | 策略 auto_approve | 策略 require | 会话批准记忆 | MCP 默认门 |
|---|---|---|---|---|---|
| bypass_all | **照拒** | 本地放行 | 本地放行(跳手机) | 跳过(反正放行) | 放行 |
| ask_all | **照拒** | **降级 require** | require | **跳过(显式要求每次问)** | require |
| 空(默认) | 现状 | 现状 | 现状 | 现状 | 现状 |

- **三个决策点**(各自独立实现,共用判定 helper):
  1. agentcore:`agentCoreApprove`(内核 Config.Approve 回调,含 MCP 默认门——该门仅此路径有)
  2. claude hook:`agent_ai.go` evaluateApprovalDecisionForHook 调用点(goal run 分支不接管)
  3. codex:`codexAppServerApprovalResult`(agent_ai.go ~6372,evaluateApprovalDecision)
- 共享 helper:`applyApprovalModeOverride(mode, decision)` + `normalizeAgentAIApprovalMode` + `approvalModeSkipsSessionMemory`(会话批准记忆检查点在 ask_all/bypass_all 下跳过);日志行带 mode 便于审计。

## 5. 前端(app/website)

- **ChatComposer 底部新行**(textarea 下、发送键左侧):`🛡️ {模式名} ▾`(权限模式 chip)+`{模型} ▾`(模型 chip)+`effort ▾` chip。运行中(activeRunning)禁用;「下轮生效」角标沿用 pendingConfig。
- 权限模式 chip:下拉三选;**切到 bypass_all 弹一次确认**(文案:将跳过手机审批卡);bypass 挂起/生效期间 chip 红色警示态。
- 模型 chip:点击弹 absolute 下拉面板(选项+自定义输入;复用目录面板的「挂外层容器防裁剪」手法与现有 modelCustom 逻辑),迁自 ChatSettingsBar。
- **ChatSettingsBar 移除模型/effort 两控件**(目录+更多+预算面板留顶栏);状态源单一(useAliangChat conversation 管道原样,控件搬家)。
- i18n:模式名/确认文案/漂移无——zh/en 同步。

## 6. 错误处理/边界

- 非法 `approval_mode` 值:后端归一回落空(默认);前端词表封闭。
- bypass_all 挂起未生效期间(下轮生效角标亮着):UI 明示「下轮生效」,防「以为已放过」误解。
- 运行中切换:chip 禁用(activeRunning 同源),不产生半回合生效。
- 手机端模式未知:手机照常弹卡(默认态);模式只由网页会话设置驱动,手机不读该字段(非目标)。

## 7. 测试

- 后端:模式×策略×记忆矩阵(bypass/ask_all/空 × auto_approve/auto_deny/require × 记忆命中)**三侧**(agentcore/claude hook/codex);载荷归一(非法值回落);journal 会话头行字段。
- 刷新恢复:inventory 带 approval_mode → deviceSessionToConversation 回填 → chip 状态正确、续聊 create 不重置后端值。
- 前端:chatSettings.js 纯函数用例(approval_mode 白名单);composer chips 渲染与确认流;e2e chat-settings.spec 断言随迁移更新(chips 在 composer、顶栏旧选择器消失)。
- 全量:go 相关包 + vitest + mobile e2e 哨兵。

## 8. 交付物与验收

1. composer 底部三 chip 可用;顶栏旧模型/effort 选择器消失、目录/更多不受影响。
2. bypass_all:手机不再弹卡(auto_deny 照拒);ask_all:每次都问;默认:现状。
3. 刷新页面后 chip 状态与后端一致;续聊不重置后端模式。
4. 「下轮生效」管道与角标正确;运行中不可切。
5. 切 bypass 有确认弹窗;红色警示态。
