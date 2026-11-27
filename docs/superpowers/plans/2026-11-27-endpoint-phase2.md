# LLM Endpoint 二期实施计划——激活升格、AliangCode 出口与 auto 调度

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 落地二期 spec（docs/superpowers/specs/2026-11-27-endpoint-phase2-design.md）：行上激活开关、egress_kernel 出口指定（事务换绑）、内核每 run 单次解析（出口/auto 黏性调度）、内核失败上报缝 OnRequestError、401/402 锁存、排队超时进 failover、429 窗口解析、UI 四块（行开关/出口徽标/抽屉三态/删卡改名）、agentcore_llm 硬退役。

**Architecture:** store 层加列+事务换绑 API；services 层新增 kernelEndpointResolver（黏性 map，独立于 EndpointService）；agentcore 内核加 LoopHook 失败回调；路由层吸收三项必修；UI 在既有 EndpointManagerSettings/ChatInfoDrawer/AgentCoreSettings/SettingsPage 上改造。

**Tech Stack:** Go 1.23+ (gorm/sqlite), Vue 3 Options API, vitest。

**关键文件锚点（一期已落）：**
- `processor/storage/endpoint_store.go`（HealthUpdate/UpdateHealth :248、endpointConfigColumns、事务 API 无）
- `app/http/services/endpoint_service.go`（Select 尾参 excludeIDs、ReportFailure :374（注释块 :368）、Inflight、sticky 不在此）
- `app/http/services/agent_ai_agentcore.go`（agentCoreSelectEndpoint :126、三解析点 :172/:267/:371、agentCoreMemoryExtractAsync :743→memory.go:558 第四调用点、runAgentCore :1355 探测→:1364 模型）
- `app/agentcore/client.go`（httpStatusError :336 非导出 retryAfter :563、OnRetry 只在重试时触发 :227、401/402 不可重试 :446）
- `inbound/http/endpoint_router.go`（aiMaxAttempts=2 :79、Acquire 超时 503 :170-176、excludeID 单值 :158）
- `app/http/handlers/endpoint_handler.go`（endpointView :68、HandleUpdate 请求体无 egress 字段 :230）
- 前端：`EndpointManagerSettings.vue`、`ChatInfoDrawer.vue`（ChatPage.vue:104 挂载）、`AgentCoreSettings.vue`（半卡 :13-57 守卫 :59-72）、`SettingsPage.vue`（normalize 链 agentcore_llm :494）、`agentCoreSettings.js`（applyAgentCoreLLMClearing :63）、i18n zh/en（settings_tabSettings :702）

---

## Task 1: store 层——加列、SetEgressKernel 事务 API、计数窄写

**Files:** Modify `processor/storage/endpoint_store.go`、`processor/storage/endpoint_store_test.go`、`processor/config/endpoint.go`（结构体加 4 字段+Validate 出口规则）

- [ ] Step 1 失败测试：SetEgressKernel 换绑唯一性（设 A→设 B→A=0 B=1）、取消（→全 0）、非 anthropic 行拒设（错误哨兵 ErrNotAnthropic）、builtin 行可设；MarkUsed 打点不碰其它列；HealthUpdate 扩展 counter 字段（total/failed/last_used_at）后 UpdateHealth 落列
- [ ] Step 2 红 → 实现：LLMEndpoint 加 `EgressKernel/LastUsedAt/TotalRequests/FailedRequests`（json tag 同名蛇形）；SetEgressKernel(id,set) 用 `s.db.Transaction`（set=true：UPDATE 全表置 0→UPDATE 目标行置 1，前提行存在∧ProtocolAnthropic；set=false 只置 0）；MarkUsed(id) 窄写；HealthUpdate 扩展；`egress_kernel`/`last_used_at` **不进** endpointConfigColumns（spec §4）
- [ ] Step 3 绿 + `go build ./...`；提交

## Task 2: service 层——锁存/429 窗口/池模式修订/激活清零

**Files:** Modify `app/http/services/endpoint_service.go` + test

- [ ] Step 1 失败测试：401/402 首次锁存（disabled+cooldown=0+last_error，**池模式下也锁存**——锁存绕过池模式门控，spec §5.3/§9.1 显式豁免）；403 计数（**池模式下计数列与 last_error 照记、只门控动作**——spec §9.1 修订后的不变量 6，断言 last_error 照记）；ReportFailure/ReportSuccess 计数列 +1；429 带 until 参数（**服务面行为**：until>0 覆盖组策略默认、until=0/无头回落组策略、非法值不炸——clamp 60..21600 的解析单测归 Task 3 的 ratelimitwin parser）；激活 0→1 清零（PUT 路径触发）；**双恢复路径**（ResetHealth 清零 + ReportSuccess 全清各一断言，评审 LOW#9）
- [ ] Step 2 红 → 实现：ReportFailure 签名加 `until int64`（0=按状态自行推导）；429 用 until 或组策略；401/402 分支在池模式 early-return **之前**；计数在锁存/常规/池模式三路都 +1；UpdateEnabled 迁移钩子（对比 DB 行 enabled 变化）
- [ ] Step 3 绿；提交

## Task 3: 内核失败上报缝——agentcore LoopHook + adapter 扩展 + builtin 状态尽力捕获

**Files:** Create `processor/ratelimitwin/parse.go`（**429 窗口解析器放叶子包**——inbound/http 禁 import services（endpoint_router.go:7-9 防环纪律），services 与 inbound/http 双方都要用它）+ Modify `app/agentcore/client.go`、`app/agentcore/loop.go`（LoopHook 定义处）、`inbound/http/endpoint_router.go`（builtin 捕获）、`app/http/services/endpoint_selector_adapter.go`（ReportFailure until 透传）+ 各 test

- [ ] Step 1 失败测试：agentcore 终端失败（含不可重试 401/402）触发 OnRequestError(status, headers)；`processor/ratelimitwin.Parse(status, headers)` 解析 Retry-After（秒/HTTP-date）与 anthropic-ratelimit-unified-reset → until，clamp 60..21600，无头→0；解析器单测直接覆盖
- [ ] Step 2 红 → 实现：`Config.OnRequestError func(status int, headers http.Header)`；client 终端失败路径（非重试的最终失败）回调；`forwardAIBuiltinAttempt` 至少把 TCP/协议层失败作为 status≤0 上报（上游 HTTP 状态尽力而为，注释明示）；adapter ReportFailure 加 until
- [ ] Step 3 绿（**adapter ReportFailure 加 until 必须同步改 AIEndpointSelector 接口与路由层 3 处调用点传 0**（endpoint_router.go:54/:197/:203/:220）——否则 Task 3 提交时全仓编不过；Task 5 只补头解析）；提交

## Task 4a: kernelEndpointResolver 核心（纯新文件）

**Files:** Create `app/http/services/kernel_endpoint_resolver.go` + test

- [ ] Step 1 失败测试（**纯 resolver，mock svc.Select**）：Resolve 四态（出口生效/出口暂停落 auto/无出口 auto/**出口行 rate_limited_until 未到 → 不发请求返回本地错误「出口限流中，恢复于 HH:MM」，不 failover；熔断仍照走**——spec §5.4）；黏性（同 session 命中重刷 TTL、行 enabled=0/熔断/限流→重黏+日志、TTL 过期懒清、map>4096 清扫）；层内最少 in-flight（并列随机——统计断言放宽）；model 空合法；Acquire 语义（resolver 层声明 per-run 一次，实际接线在 4b）
- [ ] Step 2 红 → 实现（**只做 resolver 本体，不碰 agent_ai_agentcore.go**——宿主接线在 4b、退役在 4c）：resolver 持 map[sessionID]{endpointID,lastHit}+mu，包 svc.Select（含 §5.4 限流出口分支：读行 rate_limited_until，未到即返回本地限流错误）；层内最少负载用 svc.Inflight()；Acquire(行, maxConcurrency) 由 resolver 对外暴露（4b 在 run 生命周期调用）；Resolve 返回整行 + 限流截止（供 4b 组装本地错误文案）
- [ ] Step 3 绿（`go test ./app/http/services/ -run 'AgentCore|Endpoint|Kernel' -race`）；提交

## Task 4b: 解析收拢接线（三解析点+第四调用点+Acquire per-run+OnRequestError 宿主接线+MarkUsed）

**Files:** Modify `app/http/services/agent_ai_agentcore.go`（三解析点改读 Config 行 + OnRequestError 宿主接线）、`agent_ai_agentcore_memory.go`（第四调用点，sticky 键=projectPath；compact=session.id）+ test

- [ ] Step 1 失败测试：runAgentCore resolve 一次→行随 Config 传递（base/auth 读行、ensureUpstream 接行参）；**OnRequestError 宿主接线**（评审 HIGH#3）：runAgentCore 设置 Config.OnRequestError → resolver 按选中行 id 调 ReportFailure(id, status, until)（until 由 ratelimitwin.Parse 算出）；Acquire per-run release run 终结 defer 全路径；MarkUsed 在 resolver 命中时调用（**不在 EndpointService.Select——路由层共享不计数**，评审 MED#6）；memory 路径 sticky 键=projectPath、compact=session.id
- [ ] Step 2 红 → 实现；Step 3 绿；提交

## Task 4c: agentcore_llm 退役

**Files:** Modify `app/http/services/agent_ai_agentcore.go`、`agent_ai_agentcore_endpoint_test.go`、`agent_ai_agentcore_run_test.go`（共用 helper）

- [ ] Step 1 失败测试：三个旧不变量 9 用例反转为「旧字段被忽略」（TestAgentCoreBaseURLOldFieldBeatsRegistry 等）；helper setTestGlobalConfigAgentCoreLLM 改造（agent_ai_agentcore_run_test.go:930 共用）；开机 Warn 断言
- [ ] Step 2 红 → 实现：EffectiveAgentCoreLLM 早返回删除 + 开机 Warn
- [ ] Step 3 绿；提交

## Task 5: 路由层三项必修

**Files:** Modify `inbound/http/endpoint_router.go` + test

- [ ] Step 1 失败测试：Acquire 队满→excludeID continue 切下一候选（不计失败）；explicitID+忙→立即 503；429 响应头解析（Retry-After 秒/HTTP-date、unified-reset）→ until 进 ReportFailure；401/402 上游响应→锁存路径（adapter 转发）
- [ ] Step 2 红 → 实现：ErrQueueTimeout 分支改 continue+exclude；429/401/402 从响应头提取；日志带 skipped 原因
- [ ] Step 3 绿（真链测试文件同步签名）；提交

## Task 6: REST + 视图字段

**Files:** Modify `app/http/handlers/endpoint_handler.go` + test、`app/http/routes/routes.go`

- [ ] Step 1 失败测试：set-egress-kernel（set=true 非 anthropic→400、事务换绑幂等、builtin 允许；set=false 取消）；GET 视图带 egress_kernel/last_used_at/total_requests/failed_requests/cooldown_until/rate_limited_until；PUT enabled 0→1 触发激活清零
- [ ] Step 2 红 → 实现 + 路由注册（`POST /api/endpoints/{id}/set-egress-kernel`）；endpointView 加字段；提交

## Task 7: 前端——行开关/出口徽标/三态徽章/抽屉指示

**Files:** Modify `EndpointManagerSettings.vue` + test、`agentApi.js`、`ChatInfoDrawer.vue`、`i18n/zh.js`/`en.js`

- [ ] Step 1 失败测试：行上激活 toggle（PUT enabled 快速路径）；设/取消出口按钮（anthropic 行显示、非 anthropic 隐藏、builtin 可设）；健康徽标三词（停用/熔断 恢复于 HH:MM/限流 至 HH:MM——用新视图字段）；**行尾 total/failed 计数 + last_used_at 相对时间展示**（§7.1）；ChatInfoDrawer 三态指示（mock egress 行）；表单 enabled 勾选移除
- [ ] Step 2 红 → 实现 + agentApi（setEndpointEgressKernel）+ i18n 成对（**本任务新增键最多，跑 i18n parity 测试**）；提交

## Task 8: 删卡改名 + 前端退役链

**Files:** Modify `AgentCoreSettings.vue`、`SettingsPage.vue`、`agentCoreSettings.js`（删 applyAgentCoreLLMClearing/normalizeAgentCoreLLMConfig）、`i18n/*`、各 test

- [ ] Step 1 失败测试：卡片只剩守卫阈值（改名内核调优）；SettingsPage normalize 链无 agentcore_llm 键；「网络设置」改名后 i18n parity + 引用文案同步（zh.js:1121 与 **en.js:1120 egress_guardProxyMissing 对句**；zh.js:708 注释是历史记录不改）；旧配置灰字提示条（检测 agentcore_llm.enabled 时显示）
- [ ] Step 2 红 → 实现（改名范围=zh.js:702-703/1121、en.js:704-705/section 副标题+注释内提及不改语义）；提交

## Task 9: 端到端验证 + rollout 增补

- [ ] `go build ./...` + gofmt/vet；`go test ./processor/storage/ ./processor/config/ ./inbound/http/ ./app/http/handlers/ ./app/http/routes/ -race`；`go test ./app/http/services/ -run 'Endpoint|AgentCore|Kernel' -race`；`cd app/website && npx vitest run`
- [ ] rollout 文档增补（不变量 11-17、三项必修语义、激活/出口使用说明、旧字段忽略提示）
- [ ] 临时真链冒烟（store→resolver→router→handler 全链假上游，跑完删）→ 提交收尾

---

## 备注

- 路由层 attempt 上限 2 不扩（spec §6：忙不是病）；explicitID+忙→503。
- 黏性只在 kernelEndpointResolver（services 新文件），EndpointService.Select 保持路由层共享无黏性。
- builtin 状态捕获为尽力而为（spec §5.3a 明示）。
- LSP 诊断对 worktree 常报陈旧未定义——一律以 `go build ./...` 实证为准。
- 前端 vitest cache 脏文件勿提交（node_modules/.vite）。