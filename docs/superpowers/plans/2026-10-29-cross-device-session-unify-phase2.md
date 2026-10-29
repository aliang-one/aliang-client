# 手机↔本机会话统一 阶段2（ChatPage 前端）实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** ChatPage 以后端统一清单为会话真相：三源会话同列表（来源角标）、点开即看转录并可续聊（claude/codex 走 resume、agentcore 走 journal 直连）、run 中输入自动切 steer、审批卡补齐（cancelled 清卡/expires_at 倒计时/快照补卡）、notice 显式提示。localStorage 降级为草稿与 UI 偏好。

**Architecture:** 阶段 1 后端已全部就绪（同分支）。前端增量：①清单双源合并（后端 /api/agent/sessions 为主 + 本地草稿）；②「打开后端会话」复用现有会话壳（conv.aiSessionId 绑定策略分源）+ 详情/快照装载转录；③sendMessage 分流（idle→ai.message/create+resume；running→ai.steer）；④handleAIEvent switch 扩 5 类帧。ChatPage.vue 1561 行、`<script setup>`，改动集中在：侧栏模板三段、handleAIEvent、sendMessage、审批区、新增 ~200 行订阅/装载逻辑（抽 composable `useDeviceSessions.js` 控制体量）。

**Tech Stack:** Vue3 script setup + vitest（utils 纯函数）+ Playwright e2e（自动起 vite:4174，需本机 Chrome，headless，locale=en-US 断言中英双语正则）。

**执行约定**：与阶段 1 相同（全中文提交、Co-Authored-By、评审双轨——大任务双评审/小任务合并评审、gopls 误报无视、e2e 需 4174 端口可复用）。行号基准 = 分支 HEAD cef1961，漂移以符号 grep 为准。

## 契约速查（阶段 1 现码，写任务时照此对齐）

- `GET /api/agent/sessions`（agentApi.js:57 已有）：`{sessions:[{id,provider,tool,source_session_id,origin,managed_conversation_id,binding_state,project_path,title,summary,mode,status,message_count,model,created_at,updated_at}]}`，200 条截断；**core 角色代理失败回 200 空列表**（不可当「无会话」，也不当致命错）；ID 形态三源：agentcore/托管=裸 conversation id、claude=`claude_<nativeId>`、codex=`codex_<id>`。
- `GET /api/agent/session?id=&limit=40&before=`（agentApi.js:65 已有 getAgentSessionDetail）：AgentVibeSession+`transcript:[{id,role,content,timestamp,index}]`+`transcript_page:{has_more,next_before_message_id,...}`；未命中 404。
- `ai.session.subscribe {session_id}` → `ai.session.snapshot {session_id, status:running|idle, transcript?:[{role,text,message_id?,ts?}], pending_approvals?:[{session_id,approval_id,kind,provider,command?,tool_name?,expires_at?}]}`；转录仅 agentcore journal 源（codex/claude 订阅无 transcript 键，静默）；未知会话回 idle 不报错。
- `ai.steer {session_id,message_id,content}` → `ai.steer.ack {result:queued|applied|not_running|error|unsupported, error?, code?}`；ack 只回发起 socket。
- `ai.approval.request` pending（含 expires_at）/resolved（同型精简）；`ai.approval.cancelled {approval_ids[], reason?}`——审批族**不订阅也广播**（ChatPage 在线即收）。
- `ai.run.notice {code:resume_missing|history_restore_failed, detail?}`。
- 清单 origin 现状：running 会话="managed"；**已关闭扫描会话为空**——Task 0 后端补丁让 agentcore 条目回填 journal 头行 origin（phone/dashboard/unknown）。

---

### Task 0: 后端小补——agentcore 清单条目带 origin

**Files:** Modify `app/http/services/agent_inventory_agentcore.go`（collectAgentCoreVibeSessions 的摘要构造，~58-108）；Test 同文件 `_test.go` 追加。

- [ ] 解析器已回填 head.origin（agent_ai_journal_history.go 同源逻辑；inventory 侧 `readAgentCoreSessionMetaWithOptions` 需同步把 origin 带出——grep 其返回结构，缺字段则加）。
- [ ] 摘要条目 `Origin: head.origin`（空保空）。测试：journal 头行 origin=phone → 清单条目 Origin=="phone"；无头行 → 空。
- [ ] 跑 `go test ./app/http/services/ -run AgentCore -race -count=1`；提交 `新增:agentcore 清单条目回填 journal 头行 origin`。

### Task 1: chatSettings 扩展（payload resume + 来源角标映射）

**Files:** Modify `app/website/src/utils/chatSettings.js`；Test `chatSettings.test.js` 追加。

- [ ] `sessionCreatePayload(conv)` 增加可选字段：`conv.resumeSessionId` → `resume_session_id`；`conv.resumeProjectPath` 覆盖 `project_path`（claude resume 按 project 目录定位转录，必须回填清单原值）。未设置时字段**不出现在载荷**（向后兼容既有 e2e 断言 create 帧字段集）。
- [ ] 新增 `sessionSourceBadge(session)`：入参清单条目，返回 `'phone'|'dashboard'|'terminal'|'active'|''`——`status==='running'`→'active'；`origin==='phone'`→'phone'；`origin==='dashboard'`→'dashboard'；id 以 `claude_`/`codex_` 开头→'terminal'；否则 ''。纯函数。
- [ ] 新增 `deviceSessionToConversation(entry)`：清单条目→本地会话壳 `{id:'dev-'+entry.id(前缀防撞), title:entry.title||entry.id, provider: normalizeProvider(entry.provider), projectPath:entry.project_path||'', deviceSession:entry, aiSessionId 由 Task 3 决定}`（provider 归一：claudecode/claude→'claudecode'，codex→'codex'，agentcore→'agentcore'，其他→'claudecode'）。
- [ ] vitest 用例覆盖三函数（载荷不含未设置字段、角标四分支、provider 归一、脏输入安全）。跑 `npm test -- chatSettings`。
- [ ] 提交 `新增:chatSettings 续聊载荷与来源角标映射`。

### Task 2: 会话清单合并 + 侧栏改造

**Files:** Modify `ChatPage.vue`（侧栏模板三段 27-152 改为单段 v-for over `mergedConversations`；groupedConversations 767-791 适配）、Create `app/website/src/composables/useDeviceSessions.js`。

- [ ] composable 职责：`loadDeviceSessions()`（调 getAgentSessions，网络错/代理失败置 `available=false`——**200 空列表按「无会话」渲染是已接受的歧义**：core 代理失败也回 200 空，区分需另拉 /api/agent/status，v1 不做，注释落档）、`deviceSessions ref`、`refreshOnEvent()`（供 handleAIEvent 的 run.started/done/approval 到达时节流 2s 触发）。**不进 localStorage**（每次进页拉取，权威在后端）。
- [ ] **单次拉取复用**：设置条既有 `toggleDirectoryPanel` 的 getAgentSessions 调用（~616）改为消费本 composable 的同一次请求结果（promise 共享），消灭双拉。
- [ ] `mergedConversations` computed = 本地草稿会话（`!conv.deviceSession`）+ 设备会话壳（懒生成：`deviceSessionToConversation`，缓存于 Map 防每次渲染重建对象）。分组沿用 today/yesterday/older（按 updatedAt；设备会话用 updated_at）。
- [ ] 侧栏条目：标题 + 来源角标（sessionSourceBadge，i18n 键见 Task 6）+ running 状态点（status==='running'）。本地草稿无角标。
- [ ] 点击设备会话 → Task 3 的 `openDeviceSession(entry)`；点击本地草稿 → 原 selectConversation。删除按钮只对本地草稿显示（设备会话不可在本端删——后端无删除端点）。
- [ ] e2e 前置：现有 `openChat` 的 sessions mock 从裸 `{project_path}` 扩为带 id/provider/title/status/origin 的两三条（chat-unify.spec.js 与 chat-settings.spec.js 的 fixtures 都要适配——设置条「最近项目」逻辑读同一响应，保持去重语义不破）。跑两个既有 e2e 确认不破。
- [ ] 提交 `新增:ChatPage 会话清单合并后端三源,侧栏来源角标`。

### Task 3: 打开设备会话——转录装载 + agentcore 订阅 + 事件路由

**Files:** Modify `ChatPage.vue`、`useDeviceSessions.js`。

- [ ] `openDeviceSession(entry)`：
  1. 生成/复用会话壳（Task 1 的 deviceSessionToConversation），**aiSessionId 绑定策略（分源，钉死）**：agentcore/托管裸 id → `aiSessionId = entry.id`（直接用 conversation id）；`claude_`/`codex_` 前缀 → `aiSessionId = 'local-'+壳id`（新建托管会话），且壳补 `resumeSessionId = entry.source_session_id`、`resumeProjectPath = entry.project_path`（仅**首次 create** 使用：created ack 到达前产生的 create 载荷带 resume 字段与清单原 project_path；ack 后按既有设置条流程走，防陈旧路径压制用户的纠正）。
  2. 拉详情 `getAgentSessionDetail(entry.id)` 渲染转录进壳 messages（role/content→user/assistant 气泡；404/网络错 → 空转录+错误条，不阻断续聊输入）。**同时把详情转录存壳 `deviceTranscript`（[{role,content}...] 原始形态）**——Task 4 的 agentcore create 要用它喂上下文。
  3. `provider==='agentcore'`（裸 id 情形）→ 发 `ai.session.subscribe {session_id: entry.id}`；**`ai.session.snapshot` 帧处理在本任务接**（handleAIEvent 新 case）：transcript 非空则**覆盖**详情装载的 messages（快照更新，message_id 缺失用 ts 兜位），pending_approvals → 逐条 upsertApproval（补 status:'pending' 默认），status→壳 running 标记。`claude_`/`codex_` 不订阅（无 journal 转录，详情已够）。
  4. activeId 切到该壳；再点同一条不重复拉详情（壳带 `deviceLoadedAt` 5 分钟内跳过）。
- [ ] 切走会话时（selectConversation 到非设备会话）对 agentcore 设备会话发 `ai.session.unsubscribe`。
- [ ] **事件路由规则（钉死，修 fallback 污染）**：handleAIEvent 的未命中 fallback 链改为——①`conv.aiSessionId===session_id`（现状）；②审批族帧（request/resolved/cancelled）追加设备壳匹配：`findDeviceShellBySessionId(sessionId)`（composable 提供，查 entry.id / source_session_id / managed_conversation_id 三键），命中即挂到该壳（**不切 activeId**，侧栏角标显示待审批数）；③仍无命中 → 挂活跃会话但消息条带「来自会话 <title||id 前 8>」来源标签（接受的兜底，i18n 键 Task 6）。非审批帧维持现状 fallback。
- [ ] e2e：stub WS 回 snapshot 帧 → 断言转录渲染 + running 标记；stub 详情 404 → 断言空态可输入；stub 未知 session_id 的 approval.request → 断言挂到活跃会话且带来源标签。提交 `新增:打开设备会话,转录装载与 agentcore 订阅,事件路由分源`。

### Task 4: 续聊发送分流 + steer + agentcore 上下文喂入

**Files:** Modify `ChatPage.vue`（sendMessage 1442-1509、输入区 425-468）。

- [ ] **agentcore 上下文喂入（关键，修「create 门吞掉 journal 读回」）**：裸 id 设备会话的首条续聊消息，create 载荷带 `transcript: conv.deviceTranscript`（后端 `remoteAgentAIHistory` 在 create 时摄入，agent_ai.go ~1629 现成）——**否则 create 先注册空 history 会话，message() 的 journal 读回被跳过，续聊静默变新会话**。实现细节：先 grep `remoteAgentAIHistory` 确认 transcript 行解析形态（role/content 或 text 字段名），Task 3 存的 deviceTranscript 按该形态转换；create 成功 ack 后置 `conv.deviceTranscriptSent=true` 不再重发（resendCreate 同样带，直到 ack）。
- [ ] **分流规则（钉死）**：活跃会话 `activeRunning`（既有 computed 559）或设备会话 status==='running' → 输入提交走 `ai.steer`（sending 中允许输入、提交发 steer、按钮态变「插话中」）。**steer 目标 id（钉死）**：会话壳有 `managed_conversation_id`（清单 running 绑定态带回）→ steer 该 id（真正的在跑回合）；无 → 不发帧，输入区内联提示「该会话正在其他端运行，暂不能插话」（i18n Task 6）——**绝不做「重发为普通消息」引导**（会对同一原生转录并发第二个 CLI run）。
- [ ] steer ack 处理：`ai.steer.ack` → ack 轻量条（queued→「已受理，将在下一轮生效」；applied→「已送达」；not_running→「回合已结束，插话未生效」；error/unsupported→错误气泡带 detail；**未知 result（含 held）走默认通用渲染**）。i18n 键 Task 6。
- [ ] steer 提交不落 user 气泡为独立消息——追加「插话」样式轻量条（快照/详情装载的转录才是权威流，避免回灌重复渲染）。
- [ ] resume 会话（claude_/codex_ 壳）走原 create 确认门链：首条消息的 create 载荷带 `resume_session_id`+清单原 project_path（仅 created ack 前，Task 3 已钉）；`activeRunning` 判定对设备壳要考虑清单 status（壳字段）与本地气泡推断的并集。
- [ ] e2e：running 会话提交 → 断言发出 ai.steer（目标=managed_conversation_id）且 ack=queued 渲染提示；无 managed id 的 running → 内联提示且零帧发出；idle agentcore 会话首条 → create 帧含 transcript 字段。提交 `新增:续聊发送分流,steer 目标解析与 ack 展示,agentcore 上下文喂入`。

### Task 5: 审批卡补齐 + notice 提示

**Files:** Modify `ChatPage.vue`（审批模板 355-421、handleAIEvent 1361-1440）。

- [ ] `ai.approval.cancelled` 新 case：按 approval_ids 逐个 removeApproval + 「审批已被取消（reason）」系统条。既有 clearApprovalsForSession 的粗暴清空保留（done/error 路径），cancelled 用于**未结束回合**的精确清卡。
- [ ] **upsertApproval 存储扩展**：字段合并 map 显式加 `expiresAt`（现码 ~1207-1226 不含此键——模板倒计时依赖它，不存则永远渲染不出）。
- [ ] `ai.run.notice` 新 case：code=resume_missing → 气泡内提示条「原生会话不存在，已按新会话继续」；history_restore_failed → 「历史恢复失败，已按空会话继续」。两种都不阻断输入。
- [ ] expires_at 倒计时：审批卡头部显示剩余时间（computed 每分钟刷新，>1h 显示「X 小时」、<1h「X 分钟」、过期/缺失不显示）；resolved 帧到达由既有 upsertApproval 收卡逻辑处理（验证 resolved 无 expires_at 不影响）。
- [ ] 快照 pending_approvals 渲染（Task 3 已接）确认与 upsertApproval 字段对齐（快照行无 title/reason/status——upsert 时补 status:'pending' 默认）。
- [ ] e2e：stub pending(带 expires_at=+23h)→卡片显示剩余小时；stub cancelled→卡片消失+提示条。提交 `新增:审批卡 cancelled 清卡/expires_at 倒计时/notice 显式提示`。

### Task 6: i18n 全量 + 单测收口

**Files:** Modify `app/website/src/i18n/zh.js` + `en.js`（chat 段 211-254 后追加）。

- [ ] 新键清单（中英对照一次写齐）：来源角标 4（chat_source_phone/dashboard/terminal/active）、设备会话空态/加载失败（chat_deviceSessionsEmpty/LoadFailed）、steer 族（chat_steerPlaceholder/Queued/Applied/NotRunning/Failed）、审批倒计时（chat_approvalExpiresHours/Minutes）、notice 族（chat_noticeResumeMissing/HistoryRestoreFailed）、审批取消提示（chat_approvalCancelled）。
- [ ] 两文件键集一致断言（仿既有 i18n 测试若有；无则手动核对 + grep 键数）。
- [ ] `npm test`（vitest 全量）+ `npx playwright test tests/e2e/chat-unify.spec.js tests/e2e/chat-settings.spec.js` 全绿。提交 `新增:跨端会话 i18n 全量,单测与 e2e 收口`。

### Task 7: 阶段 2 收尾回归

- [ ] `npm test`（全量 vitest）；`npx playwright test`（全量 e2e——mobile-responsive 390/768/1280 哨兵必须仍绿：新增 UI 不得破移动布局，触控目标≥40px）。
- [ ] `go build ./...`（Task 0 后端改动）+ 全仓 `go test ./...` 一次。
- [ ] 人工验收清单写入提交信息（部署 liang-dev 后核对：手机发起会话出现在本机列表带角标、点开看转录、idle 续聊、running steer、审批双端卡同步）。
- [ ] 提交 `新增:阶段2收尾回归,部署验收清单`。

## 已知取舍（阶段 2 范围内接受）

- 会话列表不实时推送（REST + WS 事件节流刷新）；快照转录有损（200 行/4000 rune）而续聊上下文全量——两端展示口径不同属设计。
- 清单 200 条截断（后端 cap）；更老会话走「输入 id 直达」不做（YAGNI，有需求另单）。
- 设备会话删除/重命名不做（后端无端点；重命名同步属 PhoneServer 阶段 3）。
- 移动端沿用既有响应式约定（md: 还原桌面、触控≥40px）。
