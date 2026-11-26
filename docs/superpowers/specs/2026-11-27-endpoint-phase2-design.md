# LLM Endpoint 管理平台二期设计——激活升格、AliangCode 出口与 auto 调度

- 日期：2026-11-27
- 状态：设计已与用户逐节确认（v3，含业界对照修正）；spec 评审 1 轮 12 项已全部修入，待复审
- 前置：一期（2026-11-25-llm-endpoint-manager-design.md）已合 master=ada0deda，本文件只写增量
- 对照参考：sub2api-aliang 源码（本地检出）+ one-api/new-api/LiteLLM/cc-switch/OpenRouter 调研（研究工作流 2026-11-26，结论已并入）

## 1. 目标

1. endpoint 的 `enabled` 从表单字段升格为**行上一等公民开关**（你点了激活，后续调度才把它当候选；没点=完全忽略）。
2. 新增「**AliangCode 出口 endpoint**」：把某个 anthropic endpoint 设为这台电脑 AliangCode 内核的唯一定向出口（用户配置压倒一切）。
3. 未设出口时内核走 **auto 调度**：协议过滤 → 会话黏性 → 优先级分层 → 层内最少负载，全空回落 builtin。
4. 产品收敛：agent 设置页删除「内核端点（连到哪）」卡片（agentcore_llm 硬退役）；「代理设置」改名「网络设置」；聊天页加出口状态只读指示。

## 2. 术语

| 术语 | 含义 |
|---|---|
| 激活 | enabled=1；行上开关，调度/路由的唯一候选前提 |
| 出口指定 | egress_kernel=1 的 anthropic 行，内核恒走它（至多一行） |
| auto 调度 | 无出口指定时内核的自动选择（黏性+优先级+负载） |
| 黏性 | 会话 id→endpoint 绑定（内存，TTL 1h 命中刷新） |
| 熔断 | 连续失败达阈值自动摘除（cooldown 到期懒恢复，非用户停用） |
| 限流 | 429 冷却（rate_limited_until，非用户停用） |

**展示层三词分明（新增不变量）**：停用（用户，enabled=0）/ 熔断（恢复于 HH:MM）/ 限流（至 HH:MM）——UI 与日志禁止用同一个「禁用/不可用」词混淆三者。

## 3. 范围

### 本期做
- `llm_endpoints` 加列：`egress_kernel`(0/1)、`last_used_at`、`total_requests`、`failed_requests`
- 激活开关升格（行上 toggle，PUT enabled 快速路径）
- `set-egress-kernel` REST（事务换绑 store 方法 SetEgressKernel、非 anthropic 拒设、builtin 可设）
- 内核出口解析改造（每 run 单次解析收拢 4+ 调用点 + 出口指定 → auto 调度 → builtin 兜底）+ 会话黏性 + 429 窗口解析
- **内核失败上报缝**（agentcore LoopHook OnRequestError + AIEndpointSelector.ReportFailure 扩展 + builtin 状态尽力捕获）——锁存/限流/计数的前提
- 401/402 锁存、排队超时进 failover 链、出口行尊重限流（三项必修）
- 激活迁移清零健康、黏性 TTL/重黏日志、select 跳过原因日志、计数列窄写 MarkUsed
- UI：行上激活开关/出口徽标/设-取消出口按钮、聊天页 ChatInfoDrawer 三态指示、删内核端点卡、代理→网络设置改名
- agentcore_llm 硬退役（内核不再读；开机 Warn；不变量 9 废除 + 不变量 5/6 修订）

### 本期不做
- 56432 路由层的**调度语义**不变（黏性/出口仅内核专属；路由层只吸收三项必修与 429 解析；claude/codex CLI 与网页聊天不受出口指定影响）
- per-(endpoint,model) 作用域冷却、EWMA 慢逃逸、后台定时探针、层内 weight、expiry 自动停用（研究结论：单机产品刻意不抄）
- 旧 agentcore_llm 自动迁移（UI 卡片删除后字段自然失效，日志提示）

## 4. 数据模型增量

`llm_endpoints` 新增列（AutoMigrate 自动加列）：

| 列 | 类型 | 语义 |
|---|---|---|
| egress_kernel | INTEGER 0/1 | 出口指定；全表至多一行=1（store 层事务守卫）；非 anthropic 行拒设（Validate+store 双层） |
| last_used_at | INTEGER | unix 秒；每次被内核选中打点（观测用，不参与调度） |
| total_requests / failed_requests | INTEGER | 计数器；ReportSuccess/ReportFailure 顺带 +1 |

- **`egress_kernel` 不进 `endpointConfigColumns` 白名单**（评审修正③：进白名单会让普通 PUT 绕过至多一行不变量造出双出口行；且 HandleUpdate 请求体本就没有该字段）。所有写操作走新的事务性 store 方法 `SetEgressKernel(id, set)`（`db.Transaction`：清全表→置目标行；取消=置 0）——store 目前无事务 API，本方法即首个。
- `last_used_at` 也不进白名单（陈旧快照回写风险，写缝见 5.7）。
- **enabled=0 与 egress_kernel=1 可共存**：出口「暂停保留」——内核跳过该行回 auto 调度；enabled 0→1 时出口恢复生效（用户已拍板）。

## 5. 内核出口解析（agentCoreSelectEndpoint 改造）

### 5.0 每 run 单次解析（评审修正①②：现有三解析点是 4+ 个独立 Select 调用点，必须收拢）

- **runAgentCore 在 run 开始时解析一次**（`ResolveKernelEndpoint(sessionID, model)`），返回整行（`*config.LLMEndpoint`）+ 内核侧新字段；结果随 `agentcore.Config` 传递，`agentCoreBaseURL/agentCoreAuthToken` 改为**读 Config 上已解析的行**（不再各自调 Select）。`agentCoreEnsureUpstream` 接收已解析行作参数。
- 已知的第 4 个调用点 `agentCoreMemoryExtractAsync`（agent_ai_agentcore_memory.go:558，无 session id）：黏性键用 `projectPath`（同项目黏住同一 endpoint，语义合理）；compact 路径用 `session.id`。
- **Acquire 计数作用域 = 每 run 一次**：选中后 Acquire，release 挂 run 终结（成功/失败/取消/panic 全路径，defer 语义）。run 内的多次 LLM 请求共享这一个槽位（in-flight 语义=「占着这个 endpoint 的 run 数」）——避免 3×Acquire 自膨胀污染层内负载信号。Acquire 满/超时时在解析层重选（exclude 该行）而非 run 内等待。

```
ResolveKernelEndpoint(sessionID, model):
  1. 出口指定行(egress_kernel=1) 存在:
     a. enabled=0 → 出口暂停,落 2(auto 调度),打 Info 日志「出口 {id} 停用中,回退 auto」
     b. enabled=1 → 恒返回它(压过健康/模型/并发;但见 5.4 限流例外)
  2. auto 调度:
     候选 = auto_group=1 ∧ enabled ∧ protocol_anthropic ∧ 健康(ok∨熔断到期) ∧ 限流到期 ∧ (models=[]∨model命中)
     a. 黏性优先: sessionID 绑定行仍满足候选闸 → 直接用(重刷 TTL)
     b. 否则: 按 priority 分层,层内选 in-flight 最少者(并列随机),选中并重黏
     c. 全空 → builtin 兜底(照旧,必打日志)
  3. 手机 model 照传不改写;出口/调度层不匹配 → 上游报错(用户配置优先)
```

**黏性 map 归属（评审修正⑦）**：services 包新增内核侧解析器（如 `kernelEndpointResolver`，独立小文件），持有 sticky map + mu，包装 `EndpointService.Select`——**不进 EndpointService**（Select 与路由层共享，路由层无黏性，不能被污染）。

**model 时机（评审修正⑪）**：`agentCoreEnsureUpstream` 在模型抽取之前跑（runAgentCore :1355 探测 → :1364 才有 model）——解析时 model 允许为空（跳过模型过滤），探测语义不受影响；解析函数签名带 model 但空值合法。

### 5.1 黏性（内存 map）
- `map[sessionID]{endpointID, lastHit}`；TTL 3600s 命中刷新；读时懒清除过期项；map>4096 全量清扫。
- 黏住的行不满足候选闸（enabled=0/熔断/限流）→ 不清绑定、重黏次优并覆写，**打 Info 日志（会话→新行+原因）**。
- 进程重启丢 map（重新选择自然重黏）。

### 5.2 负载
层内选 in-flight 最少者：复用 service `Inflight()`；**内核选中后统一走 `Acquire`** 计数（否则 in-flight 不是真数）。Acquire 满/超时 → exclude 该行进 failover 链（不计失败不动健康）。

### 5.3 必修：401/402 锁存
- ReportFailure 收到 401/402 → 首次即 `health_status='disabled'`、`cooldown_until=0`（**无冷却、不自愈**，锁存）、last_error 记「认证失败/欠费」。
- **锁存绕过池模式门控（§9.1 不变量 6 的显式豁免项）**：401/402 是确定性凭据错误，与「暂时性失败要不要自动摘除」的策略选择正交——池模式下同样锁存（门控的只有熔断迁移与冷却写库，不含锁存）。死 key 行留在池里只会吃 failover 税，池模式的本意是「不记暂时性失败」而非「记不住坏凭据」。
- 恢复路径：用户换 key 后「重置健康」按钮，或一次真实成功（ReportSuccess 全清）。
- 出口指定行被 401/402 锁存 → 仍按出口语义走（用户配置压倒一切，错误透传给用户）。
- 400/413/422 仍忽略；403 归计数路径（语义歧义按普通失败计，池模式下不落库）。

### 5.3a 内核侧失败上报缝（评审修正①：一期未建，必建，否则内核流量的锁存/限流/计数永远不触发）

现状事实（评审实证）：ReportFailure 只被 56432 路由层调用；内核 BYO 直连绕过 56432；builtin 路径 `forwardAIBuiltinAttempt` 不回传上游状态（一律当成功）；内核 client 的 `httpStatusError.retryAfter` 是非导出字段且只在重试帧出现（401/402 不可重试故从不通知）。

- **新缝 `OnRequestError`**：`agentcore` 内核加 LoopHook（如 `Config.OnRequestError func(status int, headers http.Header)`），终端失败（含不可重试错误）时回调，携带状态码+响应头。宿主侧在 runAgentCore 把它接到 resolver：按选中 endpoint id 调 ReportFailure（429 时用解析出的 until，见 5.5）。
- **路由层 builtin 状态可见化**：`forwardAIBuiltinAttempt` 补上游状态捕获（watcher/relay 层如无法观测则至少把 TCP/协议层失败作为 status≤0 上报；上游 4xx/5xx 语义在 builtin 路径标注为「尽力而为」，不承诺精确）——builtin 行为部分锁存是尽力语义，spec 明示。
- `AIEndpointSelector.ReportFailure` 接口扩展为携带 `until int64`（预解析冷却截止）可选参数，adapter 透传。

### 5.4 必修：出口行尊重限流
出口指定行的 `rate_limited_until` 到点前：内核**不发请求**，直接返回本地明确错误「出口限流中，恢复于 HH:MM（可稍后重试）」——不 failover、不硬打（避免每 30s 戳 5h 限额窗）。熔断（连续失败 disabled）仍压过照走（用户配置优先）。

### 5.5 必修：429 窗口解析
收到 429 → 解析 `Retry-After`（秒/HTTP-date）与 `anthropic-ratelimit-unified-reset` 头，clamp 60s..21600s；无头回落组策略 `RateLimitCooldownSec`（默认 60s）。解析发生在**能拿到响应头的两条缝**：路由层（endpoint_router 有响应头）与新内核缝 5.3a 的 `OnRequestError(status, headers)`；解析出的 `until` 随 ReportFailure 写库。

### 5.6 激活迁移清零
服务层 enabled 0→1 迁移时执行 ResetHealth 同款清零（状态 ok、计数 0、两路冷却 0、last_error 清）——消灭「打开了还是不用」。

### 5.7 计数列写缝（评审修正⑩）
`HealthUpdate` 扩展 counter 字段（total/failed/last_used_at 增量），`UpdateHealth` 列映射覆盖；新增专用窄写 `MarkUsed(id)`（select 命中打点，不碰其它列）。`last_used_at` **不进** `endpointConfigColumns` 白名单（避免 GET-merge-PUT 陈旧快照回写）。

## 6. 56432 路由层增量（小）

- Acquire `ErrQueueTimeout` → excludeID 进 attempt 循环（切下一候选）。**明确语义**：attempt 上限仍是 2（首次+一次重试），队满场景=排除忙行后再试至多一个候选；≥3 行且前两行全忙时第三行本轮不试（下一请求自然轮到，不为队满扩 attempt——忙不是病，不值得为它加循环）。**explicitID + 该行忙 → 立即 503**（显式点名不重选，重选违背点名语义且会再烧一个 30s 等待）。
- 429 解析（5.5）同样作用于路由层 ReportFailure。
- 401/402 锁存（5.3）同样作用于路由层。
- 其余行为不变；路由层不做黏性/出口（仅内核专属）。

## 7. UI

### 7.1 EndpointManagerSettings
- **行上激活开关**（升格）：列表行第一个操作位；点击 = PUT enabled（快速路径，不进弹窗）；builtin 行开关照常可用（停 builtin=只停兜底行本身）。
- **出口徽标 + 「设为 AliangCode 出口」按钮**：anthropic 行显示；当前出口行按钮变「取消出口」；非 anthropic 行不显示。换绑即事务生效。
- 健康徽标三词化：停用（灰）/ 熔断（红，恢复于 HH:MM tooltip）/ 限流（蓝灰，至 HH:MM tooltip）/ 正常（绿）。
- 行尾新增 `total/failed` 计数与 `last_used_at`（相对时间）。
- 表单内 enabled 勾选移除（行上开关取代）；编辑弹窗保留其余字段。

### 7.2 聊天页（评审修正⑫：挂载点定案）
挂 **ChatInfoDrawer.vue**（会话信息抽屉，ChatPage.vue:104 常驻挂载；比 ChatSettingsBar 更合适——抽屉本就有「运行态/守卫」区块，出口指示是同类信息）。数据源：GET /api/endpoints 过滤 egress_kernel=1 的行（取 name + health_status + §8 新增的 cooldown_until/rate_limited_until）。三态指示：`出口：{endpoint 名}`（生效）/ `出口：{名}（已停用，auto 调度中）` / `出口：auto 调度`（未指定）。不可在聊天页切换（切换只在管理页）。

### 7.3 agent 设置页
- 删 AgentCoreSettings 的「内核端点（连到哪）」半卡（enable/base_url/api_key 及相关 i18n 保留键删除）。
- 守卫阈值（max_identical_tool_turns）保留，卡片改名「内核调优」。
- `normalizeAgentCoreLLMConfig`/`applyAgentCoreLLMClearing` 前端纯函数退役删除（SettingsPage 归一链同步摘除 agentcore_llm 键；后端 POST 白名单保留接受避免 403，值被忽略）。

### 7.4 改名
「代理设置」→「网络设置」：settings_tabSettings、settings_subProxy、分区副标题、及其它引用文案（egress_guardProxyMissing 等提示语）同步；i18n zh/en 成对。

## 8. REST 增量

| 路由 | 说明 |
|---|---|
| POST /api/endpoints/{id}/set-egress-kernel | body `{set:true|false}`；set=true：非 anthropic→400、事务换绑；builtin 行允许 |
| PUT /api/endpoints/{id} | enabled 迁移走服务层（激活清零健康逻辑在此路径触发） |

GET 列表/单条视图带 `egress_kernel/last_used_at/total_requests/failed_requests`，**并加 `cooldown_until/rate_limited_until`（原 json:"-" 不出 REST 面，评审修正⑧：三词徽标的「恢复于 HH:MM」tooltip 需要它们；加进 endpointView，store 结构体 tag 不动）**。

## 9. agentcore_llm 硬退役

- 内核解析点删除 EffectiveAgentCoreLLM 早返回（base/auth/ensureUpstream 全部只认 registry 出口解析）。
- **对应测试反转**（评审修正⑨）：agent_ai_agentcore_endpoint_test.go 的三个不变量 9 用例（TestAgentCoreBaseURLOldFieldBeatsRegistry / AuthToken... / EnsureUpstream...）反转为「旧字段被忽略」断言；`setTestGlobalConfigAgentCoreLLM` helper 被 agent_ai_agentcore_run_test.go 共用——改造 helper 而非删除。
- 开机检测 `customer.agentcore_llm.enabled=true` → 打一条 Warn「旧内核端点配置已忽略，出网走 endpoint 管理」。
- **清除可达性（评审修正⑥）**：删除 UI 卡片后唯一能写空值的 `applyAgentCoreLLMClearing` 也退役，旧用户的 Warn 将永远打——接受并文档化（字段无行为影响，仅日志噪音；在改名的「内核调优」卡底部加一行灰字提示「检测到旧版内核端点配置，已忽略」即可让用户知情）。前端 normalize 链（SettingsPage normalizeCustomerConfig 的 agentcore_llm 键）同步摘除。
- spec 一期 §17 不变量 9 废除（新不变量 17：旧字段永不驱动出网）。

### 9.1 一期 §17 不变量 5/6 修订（评审修正⑤）

- 不变量 5 修订：4xx 不计数 → **401/402 例外**（首次锁存摘除）、403 归计数路径，其余 4xx 仍忽略。
- 不变量 6 修订（池模式语义收窄）：池模式（failure_disable=0）**只门控 disabled 迁移与冷却写库**；计数列（total/failed/last_used_at）与 last_error **照记**（行业共识「指标永远记，只门控动作」——new-api auto_ban=0 只跳过 DisableChannel 照采指标）。黏性/调度/出口均不受池模式影响（内核调度是出口解析层，与组策略的健康摘除门控正交）。

## 10. 测试与文档

- store：egress 换绑事务唯一性、非 anthropic 拒、enabled 共存、计数列
- service：出口解析三态（生效/暂停/无）、auto 调度（黏性/TTL/层内负载/重黏日志）、401/402 锁存与双恢复路径、429 头解析 clamp、激活清零、排队 failover
- 内核：三解析点接新选择器（含旧字段忽略日志）
- handler/UI：set-egress-kernel、行开关快速路径、三态指示、i18n parity
- rollout 文档更新：不变量 9 废除 + 激活/出口使用说明

## 11. 不变量（§17 增补）

11. enabled=0 的行对一切调度与路由完全不可见（用户停用是最高优先级）。
12. 出口指定是用户配置：压过健康/模型/并发，但尊重限流窗口（5.4）与用户自己停用（暂停保留）。
13. 黏性是偏好不是钉子：不满足候选闸即重黏并留日志。
14. 401/402 锁存不自愈（确定性错误，且绕过池模式门控，见 §5.3）；恢复只有用户动作或真实成功。
15. 「忙」不是病：排队超时进 failover，不计失败不动健康。
16. 三词分明：停用/熔断/限流在 UI 与日志中不混用。
17. 旧 agentcore_llm 永不驱动出网。