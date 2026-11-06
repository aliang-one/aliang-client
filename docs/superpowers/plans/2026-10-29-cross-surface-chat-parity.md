# 跨端同权对话操作 实施计划（v2，已吸收评审）

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让 56431 的 aliangcode 聊天页能看见、接管、直播跟踪并操作手机发起的会话(发消息/插话/审批/停止),消除「手机↔网页」操作权的单向不对称。

**Architecture:** 复用同一 `agentAIManager`。三块:①watcher 注册表唯一落在 **manager**(`sessionWatchers[sessionID]`,跨 run 持久),`runEmitter` 构造时从 manager **播种拷贝**,emit 期摘除同步回删 manager,终态只清 emitter 拷贝;watcher 写走**异步扇出**(per-writer goroutine+有界 buffer,满/错即摘),绝不进 run 的锁路径;②`ai.session.create` 对已存在 session_id 变 attach(登记 watcher+回元数据,**禁止覆写属主字段**),磁盘解析**绑定优先**;③ChatPage(components/ChatPage.vue)会话抽屉+attached 模式+直播渲染+审批/停止 UI。

**Tech Stack:** Go(app/http/services、app/http/handlers)、Vue(app/website/src)、Jest。全部在 alianggate 仓,worktree 分支 `feature/cross-surface-chat-parity`(基于 master)。

**现状事实(2026-10-29 审计+计划评审双定谳):**
- DispatchLocalAI 六 op 已路由(agent_remote_ws.go:978-1010);emitter 每 run 新建(message() 经 m.runEmitter,agent_ai.go:1000-1024),单写者 e.write(agent_ai.go:532-568、1716);requestApproval=agent_ai.go:**3522**,审批帧写点 writeAgentAIApprovalRequest=**3562**,resolved=3573-3582,`ai.approval.cancelled`=3308,`ai.approval.sync`=3294;审批响应去重 `m.approvals` 按 (session_id, approval_id),首响删 waiter 写 completedApprovals,二端得 `ack:"duplicate"`(3236-3263)——跨端 first-wins 成立。
- dashboard 聊天 socket 生命周期在 **app/http/handlers/agent_handler.go `HandleAIStream`**(读循环+defer conn.Close,69-97;writeJSON 闭包 84-88 **无 WriteDeadline**);DispatchLocalAI 是逐消息分发,无生命周期钩子;「连接标识」key 需在 HandleAIStream 铸造。
- 磁盘解析:**绑定优先**——`agent_ai_identity.json`(loadIdentityState,identity:53-79;三 provider 都写 confirm:codex 4922/claude 6119/agentcore 1034;bindingForConversation 给 provider+NativeSessionID)→ 其次 ai-sessions journal 头行(**仅 agentcore 写**,文件名=id 纯函数 agentCoreJournalFileName journal:54-69,头行带 session_id/project_path/model,**无 provider 字段**)。
- create 既有分支(agent_ai.go:**1621-1648**)会用来者 payload 覆写 provider/model/effort/resumeSessionID/bindingVersion/initialContext——attach 必须绕开。
- 同会话并发 run 已有守卫(`ai session is already running`,1839/3155);teardown 经 activeWriter(=emitter.emit,2830)发布→watchers 天然收终态。
- 前端:ChatPage 实际路径 **app/website/src/components/ChatPage.vue**;localStorage `'aliang-chat-data'`;`sending` 全局单 ref(499、1309),1451 拦输入;未知帧 default(1436);fallback 归属(1344);`sessionCreatePayload`(utils/chatSettings.js:60-70);agentApi.js:55-74(列表**无分页**,HandleVibeSessions 全量返回)。

---

### Task 1: emitter watcher 异步扇出(Go)

**Files:**
- Modify: `app/http/services/agent_ai.go`(emitter 播种/扇出/摘除)
- Modify: `app/http/handlers/agent_handler.go`(HandleAIStream:铸造连接标识 + writeJSON 闭包加 5s SetWriteDeadline)
- Test: 新建 `app/http/services/agent_ai_emitter_watch_test.go`

- [ ] **Step 1: 失败测试**——①runEmitter 构造时从 manager `sessionWatchers` 播种拷贝;emit 一条 delta+终态:主写者与 watcher 各恰好收到;②**慢 watcher 不阻塞**:watcher 写挂住(通道满)时,主写者的下一条 emit 照常即时完成,慢 watcher 被摘除;③写错误 watcher 摘除且不重投;④`removeWatcher` 后不再收到;⑤终态后 emitter 拷贝清空但 **manager 注册表仍在**(下一 run 播种仍有);⑥emit 期摘除同步回删 manager 注册表。
- [ ] **Step 2: 实现**——manager `sessionWatchers[sessionID]map[connID]agentTerminalWriter`(唯一持久注册表,watch/unwatch/按连接反注销三个方法);`runEmitter` 构造播种拷贝;emit 对 watchers **异步扇出**:每 watcher 一条**常驻 goroutine + 容量 64 的 chan**(不要 per-emit 起协程——卡死写者会堆积无界 goroutine);缓冲满或写错即从 emitter 拷贝与 manager 注册表同时摘除+Warn `[ai.session.watch] dropped`,goroutine 随摘除退出;**绝不进 e.mu 主路径**;终态只清 emitter 拷贝。HandleAIStream:connID 铸造(uuid)+writeJSON 闭包 `SetWriteDeadline(5s)`(属主 socket 同获保护);读循环退出处按 connID 反注销全部 watch(本任务先落钩子,注册在 Task 2 接线)。
- [ ] **Step 3: 测试+build 绿**,提交 `新增：AI 回合 emitter 支持 watcher 异步扇出`(提交正文注明:属主 socket 获得 5s WriteDeadline 属行为变更——原无限阻塞,超时报错走 pendingTerminal 重放兜底,非回归)。

### Task 2: watch/unwatch 路由 + 审批全类帧扇出(Go)

**Files:**
- Modify: `app/http/services/agent_remote_ws.go`(DispatchLocalAI 增 `ai.session.watch`/`ai.session.unwatch`)
- Modify: `app/http/services/agent_ai.go`(watch/unwatch 处理、审批四类帧扇出)
- Modify: `app/http/models/agent_protocol.go`(登记两 op,Required=`type,session_id`)
- Test: 扩展 Task 1 测试文件

- [ ] **Step 1: 失败测试**——watch 一个 **manager 内存可解析**的会话:该会话(含后续新 run)的 delta/approval pending/**resolved/cancelled/sync**/终态全到 watcher;unwatch 后不到;watch 内存解析不到的 id → `ai.error session not found`(磁盘解析归 Task 3);审批:属主响应后,watcher 收到 resolved 帧、其卡片可据此关闭;双端双响应 second 得 duplicate(既有断言)。
- [ ] **Step 2: 实现**——watch/unwatch 处理(内存命中才收,否则 error);审批扇出范围=**pending(3562)+resolved(3573-3582)+cancelled(3308)+sync(3294)**四类,写属主后异步扇 watchers(同一异步通道,Task 1 机制)。
- [ ] **Step 3: 测试+build 绿**,提交 `新增：会话 watch/unwatch 与审批全类帧扇出`。

### Task 3: attach 语义(Go)

**Files:**
- Modify: `app/http/services/agent_ai.go`(create 分支;磁盘惰性注册)
- Test: 扩展

- [ ] **Step 1: 失败测试**——①attach 内存活跃会话:不新建不重置,**provider/model/effort/resumeSessionID/bindingVersion/initialContext 逐字段不变**,回执含元数据+`attached:true`;②attach 磁盘会话:解析序=**持久化绑定(全 provider)优先 → journal 头行(仅补 agentcore 的 model/project_path)**;壳最小安全字段集=`id/provider/resumeSessionID/bindingVersion/projectPath/model/mode(vibe)`——带齐后 confirmBinding expected-mismatch 守卫与 runStart provider 强制改写照常生效;③**journal 有而绑定无 → 拒绝**:回 `ai.error`(会话缺少持久绑定,拒绝新建防上下文分叉),**不得走新建**;④完全未知 id → 现行新建行为不变(回归钉住);⑤attach 跳过 resolveAgentAICWD 校验。
- [ ] **Step 2: 实现**——create 入口:内存命中→attach(只登记 watcher+可选更新 surface+回执,**显式不覆写六字段**,1621-1648 分支即改造对象);未命中→`bindingForConversation` 解析→journal 头行补全→惰性壳注册;绑定缺失→error。顺带收窄 Task 2 遗留:watch 的磁盘解析在此任务完成后开放(watch 处理改为「内存无则走同一路径惰性解析」)。
- [ ] **Step 3: 测试+build 绿**,提交 `新增：ai.session.create 支持附加既有会话（绑定优先，禁覆写）`。

### Task 4: 前端——会话抽屉与 attach 模式(Vue)

**Files:**
- Modify: `app/website/src/services/agentApi.js`
- Modify: `app/website/src/components/ChatPage.vue`
- Modify: `app/website/src/utils/chatSettings.js`
- Test: `app/website/src/__tests__/`

- [ ] **Step 1: agentApi 增 `attachAgentSession(id)`、`watchSession/unwatchSession`、`loadAgentTranscript(id, limit)`(loading+40s 超时+重试);会话列表无服务端分页 → 前端客户端分页,loading 盖住列表请求。**
- [ ] **Step 2: ChatPage 会话抽屉**——顶栏入口+侧滑列表(标题/provider/时间/运行中徽章=runtime-sessions overlay 合并,客户端分页);点条目→attach+watch+转录渲染→attached 模式(`aiSessionId=ai_*`);来源标记「本机/手机/桌面」;**conv 记录持久化来源标记与 aiSessionId,刷新恢复:有运行中徽章则重新 attach+watch**。
- [ ] **Step 3: 帧归位 + 卸载 unwatch**(session_id 路由到 attached 会话视图,维持 matchedById 纪律)。
- [ ] **Step 4: jest 全绿+`npm run build` 绿**,提交 `新增:聊天页会话抽屉与既有会话接管`。

### Task 5: 前端——直播帧渲染 + 操作 UI(Vue)

**Files:**
- Modify: `app/website/src/components/ChatPage.vue`
- Test: 同 Task 4

- [ ] **Step 1: 帧渲染**——switch 增 `ai.thinking`(思考气泡对齐手机)、`ai.run.progress`(重试/进度小字)、`ai.approval.request` 对 watched 会话弹审批卡(响应走既有 `ai.approval.response`;watcher 收 resolved/cancelled 据此关卡);`ai.status`/终态照旧。
- [ ] **Step 2: 操作 UI**——attached 会话运行中:插话走**独立 steer 路径**(以 attached 会话 run 状态为门,全局 `sending` 原语义不动,本地会话行为零变化);停止按钮发 `ai.stop`;审批卡三键(批准/拒绝/自定义)。
- [ ] **Step 3: jest 全绿+build 绿**,提交 `新增:attached 会话直播渲染与审批停止操作`。

### Task 6: 回归与联调清单

- [ ] `gofmt -l` 无新脏 / `go vet ./app/...` / `go test ./app/agentcore/ ./app/http/services/ -count=1` / `go build ./app/...` / `cd app/website && npm run test && npm run build`(基线 flake 清单见对话,不当回归)。
- [ ] 联调(写进提交正文):①手机发起会话→56431 抽屉「运行中」→点开见直播逐字与审批卡→页面批审批/插话/停止作用同一 run→手机同步见插话与终态;②**页面发起→手机经刷新可见、可 stop/steer;审批由发起端(网页)处理**(live 反向扇出=后续,本版不承诺);③两台浏览器 attach 同一会话不串帧。
- [ ] 完成标准:不对称清单两个「高」消除(可见+直播);「中」消除(手机会话四操作全通);②的措辞不得超额承诺。

---

## 完成标准

1. 全部任务提交且各独立可 build;整包回归绿(基线 flake 除外)。
2. 联调三条全过(需真实手机会话)。
3. 慢/挂 watcher 不得拖累 run(①类测试钉死);attach 不覆写属主六字段(逐字段断言);「journal 有绑定无」拒新建。
