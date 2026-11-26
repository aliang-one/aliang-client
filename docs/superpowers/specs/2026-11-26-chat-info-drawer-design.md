# 聊天页会话信息抽屉(右侧滑出)设计 + 实施记录

> **状态(2026-11-26 凌晨)**:已实现,全绿,未提交。前端 vitest 91 文件/1068 用例全绿(含本批新增 23 用例),vite build 绿。改动清单见 §7。

## 1. 背景与需求

用户需求(2026-11-25/26 口头):网页端 aliangcode 聊天页要有**右侧、点击后侧滑出来**的信息侧栏,内容对齐手机端——「比如 subagent 的状态,一些上下文的消息等等」。

现状考古:
- 2026-10-29 曾实现右侧会话抽屉(`c36f7864`/`ef997bb6`,`fixed right-0 w-80`,设备会话 attach 接管),**无滑出动画**;2026-10-31 聊天页大重构(`f06161f1`)把设备会话并进左侧 ChatSidebar,右侧抽屉消失,`sessionDrawerOpen` 全仓再未出现。
- 网页端右侧现存唯一面板 = SubagentPanel(桌面 36rem 右贴边,**瞬时挂载无动画**)。
- 手机端(AliangVibeCodingPhone)的对标物 = `DiagnosticsPanel.tsx`(右侧滑入:上下文用量/运行状态/无进展守卫/失败原因四卡)+ `BackgroundTasksPanel.tsx`(子agent聚合列表,行点击开 SubagentDrawer)。
- 数据链路现成:宿主 `ai.run.progress` 心跳自诊断面板批(2026-10-28)起携带逐回合 `diagnostics` 快照,网页端此前**无消费者**(帧路由无 case,直接丢弃)。

## 2. 设计定案

**一个抽屉、五个区块、双通道数据**,滑出范式镜像 ChatSidebar(全仓唯一真滑出实现):

| 区块 | 内容 | 数据源(网页通道) |
|---|---|---|
| ① 子agent状态 | 行=phase 图标+名称+当前步骤+耗时/tokens;行点击/「查看详情」→ 开 SubagentPanel 看深度详情 | `useAliangChat.activeSubagents`(ai.subagent → reducer,已有) |
| ② 上下文用量 | 大字 `已用 / 窗口` + 进度条 + 剩余可用 + 回合数 + compact 提示 | `ai.run.progress.diagnostics`(实时)+ `detail.diagnostics`(journal end 终态回放) |
| ③ 运行状态 | 触碰文件 / Git 变更 / 运行耗时 / 停止原因 | 同上(progress 心跳的 files_touched_count/git_changed_count + elapsed_ms/stop_reason) |
| ④ 无进展守卫 | dup/err/trunc 三条连击进度条(阈值 3/5/2,越界封顶超载呈现) | `diagnostics.{dup,err,trunc}_streak` |
| ⑤ 失败原因 | 终止真因(reason)+ 结束状态(status)+ 错误文案(error);仅失败态渲染 | `detail.diagnostics`(journal end 终态,实时心跳不带终态字段) |

**交互形态**:
- 桌面(≥sm):`fixed right-0` 宽 24rem,`translate-x-full ↔ translate-x-0` + `transition-transform duration-200` 侧滑;移动端(<sm)全宽滑入。
- 面板**常驻挂载**(v-if 只挂遮罩)——这是滑出动画成立的前提(裸 v-if 挂载即瞬时显隐,SubagentPanel 无动画的根因)。
- 入口:ChatHeader `info` 图标按钮(有活跃会话即可见,双端一致),`data-testid=chat-info-entry`。
- z 序:抽屉 920/930 < SubagentPanel 940/950——抽屉里点「查看详情」,面板盖在抽屉上。
- Esc 层序(useEscStack,后激活在顶):`chat-info-drawer` 先注册、`chat-subagent-panel` 后注册 → 抽屉+面板同开时 Esc 先收面板、再收抽屉、最后退页面;与 z 序一致。
- 切会话即收抽屉(D2 同口径,防旧会话快照错位呈现);抽屉打开瞬间拉一次详情补终态字段。

## 3. 数据面契约(全部现成,零 Go 端改动)

实时通道 `ai.run.progress`(宿主 agent_ai_agentcore.go:1327 `diag.payload()`):
```
diagnostics: { turns, context_tokens, context_window, dup_streak, err_streak,
               trunc_streak, elapsed_ms, stop_reason?, terminal?, reason?, status?, error? }
+ files_touched_count, git_changed_count(心跳既有字段)
```
详情通道 `getAgentSessionDetail(...).diagnostics`(models.AgentVibeDiagnostics,**json tag 与实时通道同一套键**,手机端两路同源消费——网页端直接对齐)。尚无回合时 `diagnostics=nil`,空心跳安全丢弃。

## 4. 实现(与代码库既有惯用法逐点对齐)

- **纯链**(`useAliangChat.js`,同 subagentFrameToReducerMsg/consumeSubagentFrame 惯用法):`diagnosticsFrameToState`(蛇形→驼峰,数值归一,未知键不透传)/`consumeProgressFrame`(按 session_id 键整条覆盖;键取键存,无跨会话污染面,不设归属守卫——与 ai.subagent 的 M2 守卫不同源的理由:诊断快照挂在键上,展示层只读当前会话键)/`ingestDetailDiagnostics`/`refreshDiagnosticsFromDetail`(in-flight 去重+失败静默,同 refreshSubagentsFromDetail)。
- **路由**:帧路由新增 `case 'ai.run.progress':`,`return` 不落 localStorage(瞬态展示数据,同 ai.subagent 口径)。
- **composable 暴露**:`activeDiagnostics`(computed,按当前会话 aiSessionId 取键)+ `refetchDiagnosticsForSession`。
- **组件** `chat/ChatInfoDrawer.vue`:纯渲染+轻派生;数字格式化复用 `utils/subagentPanel` 的 formatSubagentDuration/Tokens(与子agent面板同一套口径);展示数学(阈值/百分比)收 `utils/chatDiagnostics.js`(GUARD_MAXES 与手机端 DiagnosticsPanel 同源 3/5/2;guardPct 显式判 null/undefined——`Number(null)===0` 会把「无数据」洗成 0%)。
- **i18n**:zh/en 成对新增 `chat_info*` 21 键,文案口径对齐手机端 vibecoding locale 的 diagnostics 文案;i18n.test.js 键集守卫绿。

## 5. 测试(TDD,23 用例)

| 文件 | 覆盖 |
|---|---|
| `utils/chatDiagnostics.test.js`(7) | 阈值常量同源断言/百分比取整/封顶/空值判缺(先红后绿的边界) |
| `useAliangChat.diagnostics.test.js`(10) | 蛇形归一/空心跳拒收/终态字段透传/按键覆盖无污染面/详情回填/静默重拉+in-flight 去重 |
| `chat/ChatInfoDrawer.test.js`(9) | 滑出范式(常驻挂载+translate 切换+aria-hidden)/五区块渲染门/空态/失败态/事件上抛 |
| `ChatPage.infodrawer.test.js`(4) | 入口开抽屉+拉详情/查看详情开面板(双开)/Esc 三层序/切会话收抽屉 |

## 6. 遗留与拍板项(明早评审点)

1. **「上一回合 输入/输出(+缓存读)」细分未做**:手机端取 `ai.usage` 结构化事件,网页端 WS 帧路由无 `ai.usage` 通道——需要 Go 宿主向网页 WS 下发 usage(slim envelope 手机同款)或复用 delta 内嵌 usage。P2 顺延,当前上下文卡已含「本轮输入含缓存」主值。
2. **入口形态**:现在是顶栏 info 图标;若嫌顶栏拥挤可改「运行状态气泡旁指纹图标」(手机端同位),一行改动。
3. **SubagentPanel 本体仍无滑出动画**(本批不动它,层序上被抽屉压住);若要全端统一手感,给其外包 `<Transition>`+translate 即可(与抽屉同范式)。
4. 抽屉打开后**不自动刷新**:运行中靠心跳推帧自动更新;终态字段(status/reason/error)在打开瞬间+切回该会话再开时经详情回填;若要「run 终态自动补拉」,在 ai.done/ai.error 处理处加一次 refetch 即可(未做,避免终态高频重拉)。

## 7. 改动清单(全部未提交)

```
新增 app/website/src/utils/chatDiagnostics.js(+test)
新增 app/website/src/composables/useAliangChat.diagnostics.test.js
新增 app/website/src/components/chat/ChatInfoDrawer.vue(+test)
新增 app/website/src/components/ChatPage.infodrawer.test.js
修改 app/website/src/composables/useAliangChat.js(诊断 WS 消费纯链+case+refs+暴露)
修改 app/website/src/components/chat/ChatHeader.vue(info 入口+emit)
修改 app/website/src/components/ChatPage.vue(接线:模板/状态/Esc 层序/切会话收口)
修改 app/website/src/i18n/zh.js、en.js(chat_info* 21 键)
```

验证:`npx vitest run` 91 文件/1068 用例全绿;`npx vite build` 绿。
