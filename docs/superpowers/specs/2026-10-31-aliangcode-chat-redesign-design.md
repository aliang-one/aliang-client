# AliangCode 聊天页大重构设计(撤回重发 / effort / steer / 交互审批 / 视觉现代化)

日期:2026-10-31(v3,吸收 spec 评审迭代 1 的 11 条 + 迭代 2 的 6 条意见,迭代 3 终审 Approved)
状态:方向性设计已获用户批准;本版为评审修订稿
范围:本仓(alianggate)前端 `app/website` + Go 宿主两处小改;不涉及 PhoneServer / 手机 App 代码改动。

---

## 1. 背景与目标

仪表盘 AliangCode 聊天页(`app/website/src/components/ChatPage.vue`,2120 行)当前是功能性原型:纯文本气泡、单行 input、无消息级操作、`ai.option.request` 帧被静默丢弃、steer 以流外轻量条呈现、无停止按钮、审批等待期不拦发送。本次重构目标:

1. **转录级真撤回重发**:从任意历史 user 消息截断上下文并重发,跨 agent 重启持久、手机端一致。
2. **AI 气泡 markdown 渲染**:标题/列表/表格/代码块(语言标签+复制),流式容忍。
3. **消息级操作补全**:停止按钮(ai.stop)、复制+时间戳、steer 融入消息流、审批等待期硬拦发送、可交互结构化审批(ai.option)。
4. **视觉现代化**:品牌绿(#21C45D)基因下的全面精致化。
5. **组件化拆分**:2120 行单文件拆为 `components/chat/` 模块树。

## 2. 链路事实(摸底结论,实现必须遵守)

### 2.1 内核(agentcore)与宿主

- 内核逐 run 无状态:上下文 = 每次 `agentcore.Run` 收到的 messages(`app/agentcore/loop.go:295`);`agentCoreMessages` 把 session.history 快照成内核 messages(`app/http/services/agent_ai_agentcore.go:472-540`)。
- `ai.session.create` 带 transcript 会整体覆盖内存 history,但**仅当 transcript 非空**(`agent_ai.go:1745-1747`);lazy-create 时 transcript 非空则跳过 journal 读回(`agent_ai.go:1904-1918`,条件在 :1906)。
- **详情转录出口对 assistant 内容做 120 rune 摘要截断**(`agent_service.go:387` → `summarizeVibeTranscriptForDisplay`,`agent_display.go:8-29`)——详情接口内容**严禁**用于任何上下文重建/喂入;快照同样有传输层截断(200 行/4000 rune,`agent_ai_sockets.go:39`,注释明确截断只发生在快照传输层)。
- journal 落盘 `<agentHome()>/.aliang/ai-sessions/<safeID>.jsonl`,append-only(`agent_ai_agentcore_journal.go:317-358`);首行 `kind=session` 头行;行型 kind=msg(带 message_id)/end/turn/tool/approval(**关联行均不带 message_id**,:220-310);唯一改写先例 `prependHeaderLocked`(:148-172,锁内全读→重写→rename)。无任何按消息截断能力。
- journal 读回重建 `readAgentCoreJournalHistoryFrom`(`agent_ai_journal_history.go:29-75`)与 lazy-create 同路径,是 journal→history 的权威解析器。
- effort:per-session 持久 + per-run 快照;生效通道 = create 重发或 `ai.run.start` 直带(**直接赋值,空串会清档**,`agent_ai.go:1729` 无条件赋值);内核 `NormalizeEffortLevel` 全集:low/medium/high/xhigh/max 透传、ultracode→max、minimal/none→low、未识别静默丢弃(`loop.go:256-269`)。`ai.message` 契约不含 effort(`app/http/models/agent_protocol.go:306`)。
- steer:宿主内存队列;`deliver`→queued,内核每轮 LLM 调用前经 `Hooks.GetSteeringMessages` 取走→applied;run 结束未取走→`not_running(turn_ended)`。result 全集 queued/applied/not_running/error/unsupported(`agent_ai_agentcore.go:1738-1934`)。steer 同时写 session.history(`agent_ai.go:2643-2649`)与 journal(`agent_ai.go:2658-2661`,kind=msg 带 message_id)。
- 交互审批协议(本仓已有、仪表盘未渲染):
  - `ai.option.request`:session_id/message_id/option_id/title/header/summary/v/options[{id,label,description,response,previews}]/allow_custom/multi/provider(`agent_ai.go:638-656`);agentcore 从模型输出的 ` ```aliang-options ` 围栏提取,run 结束发出(`agent_ai.go:3741-3788`),session 记 `pendingOption`(:3761)。
  - `ai.option.response`:session_id/option_id/selected[]/custom_text(`agent_ai.go:3038+`,读取在 :3050-3051);作答刷新 originSocket 并续接新 run。
  - `ai.option.cancelled`:双向作废帧。
  - fanOut 白名单已含 option request(`agent_ai_sockets.go:345`)。
- WS 链路:core 56431 是 TCP 裸泵(`ai_stream_proxy.go`),**一切新帧语义只加在 agent 进程 `DispatchLocalAI`**(`agent_remote_ws.go:1063-1121`;unknown 帧型回 `ai.error 'unsupported local AI event type'`,文案在 :1134)。
- `ai.stop` 后端已实现(`agent_ai.go:3292`):stop 本身只发 `ai.status "stopping"` + approval.cancelled,**终态帧(done/error)由被取消 run 收敛**——对 agentcore 成立(内核 ctx 取消仍发 result 帧),前端必须等终态帧而非把 stop 当终态。前端 0 接线。

### 2.2 前端底座与测试约束

- Vue 3.5 + Vite 5 + Tailwind 3.4,darkMode=class(`useTheme.js:69`);主色 `--color-primary-rgb: 33 196 93`(assets/styles.css 为 minified 单行,变量在第 1 行);**borderRadius 全局覆写**:DEFAULT=2px/lg=4px/xl=8px/full=12px(非圆!),`rounded-2xl` 未覆写=16px;字体 Inter + Material Symbols 本地 TTF。
- 无 markdown/高亮/sanitize/动画库;有 codemirror 6(仅编辑器用)。
- **双轨 Tailwind 配置**:`tailwind.config.js` 与 `index.html:17-30` 内联 tailwind-config(CDN 运行时)必须同步;本设计不动全局 token,规避漂移。
- e2e 钉死的 data-testid(口径:**聊天页内 13 个** + 仪表盘侧栏入口 `aliang-chat-entry-sidebar`,共 14,枚举如下):
  - 聊天页内:chat-settings-directory(160 只读 span / 170 按钮,`chat-settings.spec.js:113` 用 `span[data-testid=...]` **元素形态断言**,锁定态必须是 span)、chat-settings-model、chat-settings-effort、chat-settings-error、session-source-badge、device-session-running-dot、device-session-load-error、chat-run-notice 与 chat-approval-cancelled-note(按 msg.note 动态二选一)、chat-steer-item、chat-steer-status、chat-steer-blocked-hint、approval-expires。
  - 侧栏入口:aliang-chat-entry-sidebar(DashboardPage.vue:166,三份 chat spec 以此进聊天)。
  - `window.__CHAT_IDLE_TIMEOUT_MS__` 注入点必须在 app 代码运行前求值(ChatPage.vue:522 保留 globalThis 读取)。帧契约单测:`chatSettings.test.js` 钉死 create 载荷字段集「不得多出空键」(:71-81 全字段 toEqual、:88-92 resume 键、:157-168 transcript 键)——**truncate 键只允许出现在撤回路径**。
- i18n chat_* 70 键 zh/en 成对且有平权单测(`i18n.test.js:30`);新增键必须成对。
- 移动端既定约定:md: 还原桌面、dvh、min-h-0 内滚、触控 ≥40px(`min-h-[40px] md:min-h-0` 定式)。
- ChatPage 是 `fixed inset-0 z-[900]` 覆盖层;聊天内新增浮层 z 序 ≤999(Dashboard 弹窗层 z-1000 及以上不可越过)。
- 手机端参照(RN 仓,AliangVibeCodingPhone):自研 markdown 解析器(1690 行,GLM 中文容错+自动折叠+信封标签)、长按菜单=复制/编辑并重发(**仅回填输入框,不删历史**——仪表盘本次做的是超出手机端的真撤回)、ToolsMenu 的 effort chips +「当前有效/下轮配置」双态、审批等待期全 provider 硬拦发送(`sessionPhase.ts:372-409`)、composer 运行中发送/停止键交替占槽。

## 3. 已拍板决策

| 决策点 | 拍板 |
|---|---|
| 撤回深度 | 转录级真撤回(内核 journal 截断 + create truncate 标记) |
| 气泡渲染 | markdown-it + DOMPurify + 轻量高亮(新依赖) |
| 消息操作 | 停止按钮 + 复制+时间戳 + steer 融入消息流 + 审批硬拦 + 可交互 approval |
| 视觉方向 | 品牌绿现代化(不动全局 token,组件级实现) |

## 4. 设计

### A. 撤回重发(转录级真撤回)

**核心机制(评审迭代 1 定稿):撤回 create 帧「不带 transcript」,宿主截断 journal 后自行全文重建 history。** 理由:①详情/快照出口都有展示级截断,前端组装的 transcript 必然污染上下文(120-rune 降级);②宿主经 `readAgentCoreJournalHistoryFrom`(journal 全文解析器)重建,保真且天然处理「撤回到空」;③免掉前端 `deviceTranscriptSent` 一次性消费的旁路。

**内核改动(仅两处):**

1. `app/http/services/agent_ai_agentcore_journal.go` 新增 `truncateAgentCoreJournalBefore(sessionID, beforeMessageID string) error`:
   - **文件位置语义**(journal 关联行不带 message_id,按 ID 数值比较不可实现):保留首行(`kind=session` 头行);保留**文件位置上先于 beforeMessageID 所在 msg 行**的全部 msg 行(含 steer 的 kind=msg 行,按位置自然归属);end/turn/tool/approval 等非 msg 行归属其前最近的 msg 行所在区间(随宿主行保留/丢弃);首个保留 msg 行之前的孤儿非 msg 行丢弃;
   - 实现:锁内全读 → 临时文件写 → `rename` 原子替换(prependHeaderLocked 同款套路);目标 msg 行不存在或文件缺失时幂等成功(仅日志;「已截断过」与「beforeMessageID 不匹配」两成因不可区分,接受此模糊,终态展示以 journal 刷新为准)——**同一 beforeMessageID 重复调用幂等**,支持撤回重试;
   - prune/append 既有并发路径不变。
2. `app/http/models/agent_protocol.go` 契约表更新:`ai.session.create` Optional 清单(:304)补 `truncate_before_message_id`;`app/http/services/agent_ai.go` `create()` 消费该字段:
   - **仅对 provider=agentcore 生效**;其他 provider 收到该字段时忽略并回显 `truncate_applied=false`(journal 只存在于 agentcore);
   - 语义:①调用 1 的 journal 重写;②history 由截断后的 journal 经 `readAgentCoreJournalHistoryFrom` **全文重建**(与 lazy-create 同一路径;空 journal=空 history,天然覆盖「撤回到空」——不依赖既有 `len>0` 才覆盖的条件);**读回失败(非 ErrNotExist 的 IO 错)必须回 ai.error 拒绝本次撤回,不得静默空 history——truncate 幂等,重试即收敛**;该语义须同时覆盖 create 的两条代码分支(existing 分支 `agent_ai.go:1728+` 与不在内存的新建/lazy 分支 ≈`:1904`,agent 重启后撤回走后者);③`ai.session.created` ack 增加可选回显 `truncate_applied`(bool,仅记录不阻断);
   - **时序安全**:create 携带 truncate 字段时,宿主先校验会话无活跃 run(撤回流程在前端已保证 stop 终态后才发),带 run 活跃时拒绝并回 ai.error(防 rename 后僵尸 run 终态写复活被撤行);「无活跃 run」校验以 `session.cancel == nil` 为准,其充分性依赖 runAgentCore 的 **defer LIFO 顺序**(cancel → appendEndDiagnostics → markRunGuardDone → clearRunning,end 行先于 cancel 清位落盘)——实现时不得重排该 defer 链,否则会出现「终态帧已发但 end 行未落」的截断窗口;
   - 不携带该字段时全部行为零变化(旧前端/手机端零影响)。

**前端交互流:**

- **provider 门控**:撤回入口仅对 agentcore 会话提供(本地 agentcore 草稿 + agentcore 设备壳);claudecode/codex 会话 hover 只显示复制。
- user 气泡 hover 操作菜单:**复制 / 撤回重发**;本地草稿发送路径生成的 message_id 须写入 user 气泡的 `messageId` 字段(`generateId` 同值,与 ai.message 帧一致——journal user 行的锚点即来自该 id;存量旧消息不迁移、只显示复制)。设备壳消息 id 用详情行 ID;快照行 message_id 系 journal 同源 id(快照截断只切内容不切 id),允许作为展示/撤回锚点透传——**快照内容仍绝不作喂入**(撤回路径本就无 transcript 喂入;计划评审迭代 2 修订)。
- 点「撤回重发」→ 会话进入**撤回编辑态**(状态机见下)。
- 确认发送:
  1. 若会话 running:发 `ai.stop{session_id}` 并**等待终态帧**(done/error;10s 超时只结束等待,**中止撤回**——保留编辑态 + 提示「停止超时,请重试」,绝不携带 truncate 字段发 create);
  2. 发 `create(session_id, project_path, model, effort, truncate_before_message_id=<被撤消息id>)`——**不带 transcript**,走既有确认门;宿主侧完成 journal 截断 + history 全文重建;
  3. ack `created` 后发 `ai.message`(内容=编辑后文本,**新 message_id**);
  4. 前端消息数组立即截断到保留区间 + 追加新 user 气泡;设备壳展示流随后由快照/详情自然刷新(journal 是唯一权威,无喂入旁路)。
- effort 载荷:撤回 create 沿用既有 `sessionCreatePayload` 构造(effort 键原样携带)。「create 空 effort 清档」是**既有全局语义**(仪表盘每次发消息都重发 create,本就如此),本重构不扩大也不修复,见 §6 风险清单。

**撤回编辑态状态机:**

- 进入:输入框回填被撤消息原文;顶部常驻 `chat-recall-banner` 提示条「将截断「<标题切片>」及之后的全部对话,发送后生效」;banner 态拦截普通发送(发送键变为「确认重发」文案,同键位);slash 补全在编辑态**禁用**(原文以 `/` 开头也不激活,防误触)。
- 退出:banner 取消按钮或 Esc → 恢复进入前草稿原文,回到普通态。
- 切换:编辑态中点另一条消息的「撤回重发」→ 直接切换目标(重置 banner 文案与回填文本),不打断。
- 失败回滚:create 超时/被拒 → **不发 ai.message**;编辑态保留 + 提示条「撤回未确认,可重试」;由于 journal 截断按 beforeMessageID 位置语义**幂等**,直接重发同一 create 即安全收敛;终态展示以刷新后的 journal 详情为准(重开会话重拉)。

### B. effort / 会话设置呈现

- 设置条保留单行形态(锁定态 `chat-settings-directory` 仍为 span),视觉重设计;effort 当前值 chip 常驻。
- 改动 effort/模型后 chip 显示「下轮生效」小角标,下一个 `ai.run.started` 到达该会话时清除(对齐手机端「当前有效/下轮配置」)。
- 硬规则沿用:running 禁改;设置同步不发空 effort 新语义(维持既有 create 重发通道与载荷构造)。

### C. steer 融入消息流 + 停止 + 硬拦

- steer 条改为 **user 气泡形态按 ts 归并进消息流**(右侧绿描边 + ⚡徽标 + 状态 chip 六态:已发送/已受理/已送达/回合已结束/失败/未知中性);仍为瞬态不落 localStorage(重载消失,与现状一致)。
- **停止按钮**:会话 running 时输入区发送键位切换为停止键,点击发 `ai.stop{session_id}`(本地会话用 aiSessionId;设备壳用 managed_conversation_id ?? entry.id,与既有 steer 三分流同构);等待终态帧收尾(stop 帧本身不是终态,见 §2.1)。
- **审批等待期硬拦**:`activeApprovals` 非空时输入禁用 + 提示条「有待审批请求,请先处理后再发送」(对齐手机端防顶翻);仅拦活跃会话自身的审批,壳路由到他卡的会话不受影响。
- steerBlocked 提示条保留。

### D. 可交互 approval

- 新增 `ai.option.request` 渲染(组件 `ChatOptionCard.vue`):
  - 结构:标题/header/summary + 选项行(单选=radio 语义/多选=checkbox 语义)+ previews 折叠块 + 自定义回复输入(allow_custom);
  - **两步制作答**:点选项行仅本地选中高亮,底部确认按钮才发 `ai.option.response{session_id, option_id, selected, custom_text}`;发送成功 → 卡片折叠为「已选择:…」终态;失败 → 恢复可选 + 错误文案;
  - `ai.option.cancelled` 到达 → 清卡(带系统提示条);
  - 走既有确认门之外的裸 sendAI 通道(与审批卡同构)。
- 命令审批卡(`ChatApprovalCard.vue`)重设计:四决策(accept_for_session/accept/decline/cancel)按钮语义不变;盾徽 + 状态 chip + expires 倒计时保留(`approval-expires` testid 不变)。
- markdown 渲染层:`aliang-options` 围栏识别为静态只读卡(实施修正:交互卡是主路径,静态卡不再做折叠交互——原文「折叠卡」措辞作废);同会话存在同 option_id 的交互卡时静态卡隐藏(option_id 对齐,防双份)。

### E. markdown 渲染管线

- 新依赖(仅前端):`markdown-it`、`dompurify`、`highlight.js`(core + 常用语言子集:js/ts/go/json/bash/yaml/python/sql/html/css/diff)。
- `ChatMarkdown.vue`:content → markdown-it(html:false,linkify:true)→ 代码块语言标注与高亮 → **DOMPurify 消毒** → v-html;流式渲染 debounce ~80ms;未闭合围栏由 markdown-it 天然按代码处理。
- 代码块包装:语言标签 + 复制按钮 + 超过 20 行折叠(展开按钮)。
- GLM 中文容错预处理(markdown-it 前置 normalize,参照手机端规则第一批):行首 `#`/`-` 后无空格补空格。
- **user 气泡保持纯文本**(whitespace-pre-wrap,防注入);链接渲染 `target=_blank rel=noopener`。

### F. 视觉系统(品牌绿现代化)

- 消息列居中 `max-w-3xl mx-auto`;上下留白加大。
- user 气泡:品牌绿实底白字,`rounded-2xl`(真 16px),右对齐。
- assistant 气泡:白/dark 卡片 + 细边框 + `shadow-sm`,左对齐,配 24px 品牌渐变圆头像(`bg-gradient-to-br from-primary to-emerald-500`)。
- eyebrow 行:角色名 + 相对时间(hover 显绝对时间)+ assistant 状态(对话中/已完成/错误)。
- 流式:文本尾部闪烁光标;thinking 三点动画保留升级。
- 滚动:距底 <120px 时自动跟随,上翻即停;右下浮动「回到底部」按钮(带未读消息数);**替换现有无条件 scrollToBottom**。
- 输入区:自动增高 textarea(max 6 行;Enter 发送 / Shift+Enter 换行;IME 组合期 Enter 不发送——沿用 isComposingEvent);胶囊容器;running 时停止键占发送键位;撤回编辑态时确认重发占位。
- 空状态:品牌 hero(渐变 logo 圆 +「开始新对话」)。
- 侧栏:条目圆角化(`rounded-lg` hover 块 + active 左侧品牌竖条 + 品牌淡底),分组标题/角标/状态点样式微调。
- 深色模式全量适配;触控 ≥40px(`min-h-[40px] md:min-h-0` 定式);移动端 md: 还原;系统条=居中弱化胶囊。
- 不新增/不修改全局 tailwind token 与 index.html 内联配置。

### G. 组件化拆分

```
app/website/src/components/chat/
  ChatSidebar.vue        # 会话清单(分组/角标/重命名/状态点)
  ChatHeader.vue         # 顶栏(标题/重命名/running/删除/关闭)
  ChatSettingsBar.vue    # 目录/模型/effort + 目录浮层
  ChatMessageList.vue    # 归并时间线(messages+steerItems) + 滚动管理
  ChatMessageBubble.vue  # 单气泡(复制/撤回入口/时间戳/markdown 分流)
  ChatMarkdown.vue       # markdown 管线
  ChatApprovalCard.vue   # 命令审批卡
  ChatOptionCard.vue     # 结构化方案卡(ai.option)
  ChatComposer.vue       # textarea+发送/停止+撤回 banner(slash 补全保留在非编辑态)
ChatPage.vue             # 编排壳:WS/状态/watchdog/事件路由(收进 composables/useAliangChat.js)
```

**硬约束(违反即返工):**

1. data-testid 全保留且标签形态不变,枚举(14):聊天页内 13 个——chat-settings-directory(锁定态必须是 `span`,160/170 双形态都保)/chat-settings-model/chat-settings-effort/chat-settings-error/session-source-badge/device-session-running-dot/device-session-load-error/chat-run-notice/chat-approval-cancelled-note(动态二选一机制保留)/chat-steer-item/chat-steer-status/chat-steer-blocked-hint/approval-expires;侧栏入口 aliang-chat-entry-sidebar。
2. `window.__CHAT_IDLE_TIMEOUT_MS__` 在组件外层模块求值点保留(globalThis 读取)。
3. 帧契约不破:ai.session.create 载荷字段集不得多出空键(chatSettings.test.js 钉死);resume 字段仅首次 create;steer 目标三分流(managed id → agentcore 裸 id 回退 → claude_/codex_ 阻断)不变。
4. **truncate 字段只允许出现在撤回路径**的 create 帧;正常发送/设置同步路径的载荷与现状逐字节同构(truncate 键不常驻)。
5. i18n 新增键 zh/en 成对;i18n.test.js 平权单测保持绿。
6. 聊天内新增浮层 z 序 ≤999。
7. 既有 4 份 e2e spec 全绿;`__aiFrames` 帧捕获契约保留。
8. WS 帧语义只加在 agent 进程 DispatchLocalAI;core 裸泵零改动。

## 5. 测试策略

- **内核单测(Go)**:
  - journal truncate:头行保留/前缀保留/**关联行位置归属(含 steer kind=msg 行)**/空截断/目标行不存在幂等/文件缺失幂等/rename 原子;
  - create truncate 字段:history 全文重建(空 journal=空 history)/`truncate_applied` 回显/**非 agentcore provider 忽略**/不带字段行为不变/带活跃 run 拒绝;
  - **验收不变量**:撤回后下一回合上下文与保留区间语义一致(user+assistant 完整、无 120-rune 降级)——以「截断→读回重建→断言 assistant 全文」用例钉住(若发现重建解析器对 assistant 行缺位,以该用例暴露并在实现层补齐解析,不降级验收)。
- **前端单测(vitest)**:撤回时间线切片(按 messageId,含首条撤回;锚定本地草稿 user 气泡 messageId 写入前提)、create truncate 载荷组装(仅撤回路径含键)、markdown 消毒(脚本注入用例)、中文容错 normalize、代码块折叠阈值、option 卡状态机(选中/两步提交/取消/失败恢复)、steer 归并排序函数。
- **e2e(新增)**:
  - 撤回全链:running 会话 stop→终态等待→truncate create(帧断言:**含 truncate_before_message_id 且无 transcript 键**)→message 帧序(`__aiFrames`);
  - 撤回失败路径:超时中止(无 truncate create 帧)+ 编辑态保留重试;
  - 停止按钮帧;审批硬拦(输入禁用断言);option 卡两步作答帧;markdown 代码块渲染;
  - B「下轮生效」角标出现与 `ai.run.started` 清除;C steer 归并时间线六态 chip + 瞬态不落盘;D aliang-options 静态卡防双份;E 折叠与语言标签;撤回 banner 取消路径。
- **回归**:chat-settings / chat-unify / chat-device-sessions / mobile-responsive 全绿。

## 6. 风险与边界

- **撤回时序**:journal 截断仅在宿主确认无活跃 run 时执行(带 run 拒绝);前端 stop 超时绝不携带 truncate 发 create——防止 rename 后僵尸 run 终态写把被撤行复活进新文件。truncate 按 beforeMessageID 位置幂等,重试安全。
- **effort 清档既有语义**:create 对 effort 无条件赋值(agent_ai.go:1729),仪表盘每次发消息重发 create 本就会按本地值覆盖(含空串清档);撤回路径沿用同一载荷构造,不扩大也不修复,留档为独立后续项。
- 手机端感知:截断后手机 UI 在下一次快照/详情拉取时收敛,无主动推送(现状架构如此,不扩面)。
- 存量本地草稿消息无 messageId → 不提供撤回入口(仅复制),不做数据迁移。
- 撤回编辑态展示原文用**本地消息**;不依赖详情端点(其 assistant 120-rune 截断内容严禁进入任何上下文路径)。
- 撤回重建的边界语义(均为既有设计,留档不修):①journal 不存 system 行,create existing 分支对 initialContext 无条件赋值(`agent_ai.go:1736`)——truncate create 不带 initial_context 即清档,与既有 create 重发语义一致,与 effort 同款「不扩大也不修复」;②停止/失败 run 落 `-partial` 后缀的 assistant msg 行(`agent_ai_agentcore.go:1077-1092`,后缀防撞去重键),重建把半截输出收为独立 assistant 消息——与既有 lazy-create 读回一致,刻意设计。
- **设备壳撤回收窄(计划评审迭代 2 定案)**:壳续聊会话的 journal 键是新建的 `local-dev-*`(与原 `entry.id` journal 分叉),壳消息锚点(id 来自原 journal)truncate 未命中为 **no-op 非报错** → 撤回会「UI 成功、上下文静默分叉/喂入转录丢失」。故**设备壳会话撤回整体禁用**(仅本地会话可撤);`applyDevice*Transcript` 的 messageId 接线保留(id 展示+未来服务端跨 journal 锚点解析落地后启用);该限制待服务端侧支持后解锁。
- 明确不做(本批):信封标签折叠(thinking/command-output)、shiki 高亮、手机端/PhoneServer 代码改动、消息级「编辑不改历史」、effort 清档语义修复。

## 7. 交付物

1. Go:journal truncate 函数 + create truncate 字段 + **agent_protocol.go 契约表更新**(`ai.session.create` Optional 清单补 `truncate_before_message_id`)(含单测)。
2. 前端:chat/ 组件树 + useAliangChat composable + markdown 管线 + 新依赖(含单测/e2e)。
3. i18n zh/en 新键;e2e 新 spec;全部既有测试绿。
