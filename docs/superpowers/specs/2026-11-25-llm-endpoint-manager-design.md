# LLM Endpoint 管理平台设计（56432 多 endpoint 聚合路由）

- 日期：2026-11-25
- 状态：设计已与用户逐节确认，待 spec 评审
- 相关代码基线：master @ f9693b3a

## 1. 背景与目标

AliangCode 内核（provider="agentcore"）与 quick-setup 装机的 CLI 客户端（claude-code / codex / opencode / pi）今天都把 `http://127.0.0.1:56432` 当作 LLM 网关，56432 收到回环 AI 流量后由 `shouldForceAliangRoute`（processor/tcp/route_override.go:11）**无条件**强制走 aliang mTLS 隧道出网。用户订购多个上游 endpoint（多家、多协议）后，需要：

1. 一个 endpoint 统一管理工具（列表、增删改查），类似 ccswitch / one-api 的形态；
2. 新增 endpoint 时填完 base_url + api_key 后自动探测上游模型列表（GET /v1/models），可手动修改；
3. 每个 endpoint 支持优先级、并发上限、Anthropic/OpenAI 双协议 toggle（可自动探测）；
4. auto 分组：开启后按优先级统一调度（严格优先级 failover），失败自动摘除 + 冷却自愈（参考 sub2api 语义）；
5. 56432 从「写死走 aliang 隧道」变成「按 endpoint 配置选链路」，aliang 隧道只是候选之一。

**一句话**：把 56432 本地代理改造成 endpoint 路由层；endpoint registry 独立存储；仪表盘设置页新增管理子页。

## 2. 术语

| 术语 | 含义 |
|---|---|
| endpoint | 一个上游 LLM 服务记录（base_url + api_key + 协议 + 调度参数） |
| 内置 aliang endpoint | registry 第一行，代表现有 aliang mTLS 隧道链路（core.aliangServer / ai-gateway.aliang.one），不可删 |
| 协议族 | anthropic（POST /v1/messages）或 openai（POST /v1/chat/completions），按请求 path 判定 |
| auto 组 | 参与优先级统一调度的 endpoint 集合（auto_group=1） |
| 摘除 | 连续失败达阈值后 endpoint 退出调度池，冷却到期后自动回池 |
| 池模式 | auto 组关闭「失败摘除」开关时的语义：不记健康状态，失败照样调 |

## 3. 范围

### 本期做

- endpoint registry（SQLite 表 + REST CRUD + 组策略）
- 56432 明文 HTTP 路由层：`/v1/messages`、`/v1/chat/completions`、`/v1/models`
- 严格优先级 failover、endpoint 级健康摘除（auto 组开关控制）、per-endpoint 并发排队
- 内置 aliang endpoint（第一行，不可删）+ BYO endpoint 直连
- 仪表盘设置页 `EndpointManagerSettings.vue` 管理子页（列表/弹窗/测试/拉模型/健康徽标/组策略）
- 内核接入：agentCoreBaseURL/AuthToken/EnsureUpstream 改读 registry
- quick-setup 哨兵 key 改造
- 失败切下一 endpoint 重试一次（流未开始前）

### 本期不做（明确排除）

- OpenAI↔Anthropic 协议翻译（按协议匹配路由，不翻译；不匹配的请求 404/直接透传见 §7.4）
- per-endpoint 配额/计费/spend 统计
- 跨进程 registry 同步（单 core 进程持有）
- sub2api 账号 CRUD（只借鉴调度语义，不接 sub2api 写接口）
- CONNECT + MITM 路径的 AI 路由（CONNECT 隧道保持现状 byte 透传）
- 旧 `customer.agentcore_llm` 自动迁移（手动重建，见 §13）

## 4. 架构总览

```
客户端 (claude-code / codex / AliangCode 内核 / 第三方)
   │ 明文 HTTP: POST /v1/messages | /v1/chat/completions | GET /v1/models
   ▼
127.0.0.1:56432  unified ingress (inbound/http, startUnifiedIngress)
   │ dispatchConn 嗅探 → KindHTTP → HandleHttpConnection
   ▼
┌─────────────────────────────────────────────────────────────┐
│ NEW: endpoint router (inbound/http 明文分支内, tcp handler 之前) │
│  1. path → 协议族                                          │
│  2. registry 过滤: enabled + protocol toggle + 健康 + 模型    │
│  3. auto=1 → 优先级 failover + 并发排队 + 失败摘除/重试       │
│     auto=0 → 仅显式指定 endpoint id 时可用(本期仅内部使用)    │
│  4. 重写: Host / Authorization(和内置的 Authorization-Inner)  │
│  5. 内置 endpoint → RouteToALiang(现有 mTLS 隧道)           │
│     BYO endpoint   → 直连 base_url(现有 dialByRoute 直连路径) │
└─────────────────────────────────────────────────────────────┘
```

关键接线事实（侦察确认）：

- 56432 = `config.DefaultHTTPProxyPort`（processor/config/defaults.go:16），监听在 `inbound/http/server.go:63 startUnifiedIngress`，协议分派在 `inbound/http/dispatch.go dispatchConn`。
- 路由层插在 HandleHttpConnection 内、交给 tcp handler 之前，替换「无条件强制」为「按 endpoint 选择」——**仅对 AI 路径**（§7.1 三个 path）成立；非 AI 的 56432 明文流量仍落入 `processor/tcp/handler.go:495` 的 `shouldForceAliangRoute` → `RouteToALiang`，机制不变。
- CONNECT 路径（inbound/http/tunnel.go HandleRawConnect）**完全不动**，保持 byte 透传 + 现有 MITM 行为。
- aliang 隧道出网、watcher relay 注入（processor/watcher/http1_relay.go:398 injectHTTP1AuthorizationHeader、http2_wrapper.go:390）**保留且保持无条件注入**（watcher 在 tcp 处理链下游封装 conn，无 endpoint 上下文，无法按 endpoint 区分；且 `Authorization-Inner` 对 BYO 流量发到 aliang 网关也无害——该头只在 aliang 网关侧被消费）。路由层**不自行注入** `Authorization-Inner`；BYO 凭据只走 `Authorization` 头。
- 仪表盘管理面在 56431（独立进程/端口），与 56432 数据面同进程（core），registry 读写不跨进程。

## 5. 数据模型

### 5.1 表 `llm_endpoints`（aliang.data 同库新表，common/cache/datafile.go:10）

| 字段 | 类型 | 约束/默认 | 说明 |
|---|---|---|---|
| id | TEXT PK | uuid | |
| name | TEXT | NOT NULL | 显示名 |
| base_url | TEXT | 默认 '' | 空 = 内置 aliang 隧道默认链路 |
| api_key | TEXT | 默认 '' | BYO 非回环必填；内置可空（用登录态） |
| protocol_anthropic | INTEGER | 0/1 | Anthropic 格式 toggle |
| protocol_openai | INTEGER | 0/1 | OpenAI 格式 toggle |
| auto_group | INTEGER | 0/1 | 是否进 auto 池 |
| priority | INTEGER | 默认 50 | 越小越优先（对齐 sub2api 语义） |
| max_concurrency | INTEGER | 默认 0 | 0 = 不限 |
| models | TEXT | JSON array，默认 [] | 探测/手填的模型 id 列表；[] = 不限制 |
| enabled | INTEGER | 默认 1 | 用户手动启/停 |
| builtin | INTEGER | 默认 0 | 1 = 内置 aliang（仅 id='builtin-aliang'） |
| health_status | TEXT | 默认 'ok' | ok / disabled / cooldown |
| consecutive_failures | INTEGER | 默认 0 | |
| cooldown_until | INTEGER | 默认 0 | unix 秒；disabled 状态到期懒恢复 |
| rate_limited_until | INTEGER | 默认 0 | 429 专用冷却 |
| last_error | TEXT | 默认 '' | 最近一次失败摘要（展示用） |
| created_at / updated_at | INTEGER | unix 秒 | |

约束：

- 至少各有一个协议 toggle 为 1（校验拒绝双 0）。
- `builtin=1` 的行：DELETE 返回 409；base_url 不可改（空）；protocol_anthropic 默认 1、protocol_openai 默认 0（可改）；auto_group 不可关（恒 1）。
- 唯一约束：无（允许同 base_url 多条，用户自己负责）。

### 5.2 组策略 `endpoint_group_policy`（组少，单行/组）

| 字段 | 默认 | 说明 |
|---|---|---|
| failure_disable | 1 | 「失败摘除」总开关（auto 组级；关 = 池模式） |
| failure_threshold | 3 | 连续失败 N 次摘除 |
| cooldown_sec | 300 | 摘除冷却时长 |
| rate_limit_cooldown_sec | 60 | 429 冷却时长 |

本期只有一个 auto 组，单行即可；预留 group 字段以便将来多组。

### 5.3 内置行

`id='builtin-aliang'`：name「Aliang 隧道（内置）」，base_url=''，api_key 可空，protocol_anthropic=1，protocol_openai=1（两个协议 toggle 都开——56432 承载全部 AI 流量，OpenAI 请求也必须能落到内置链路），auto_group=1，priority=0，enabled=1，builtin=1。首次启动 seed（INSERT OR IGNORE）。

## 6. 路由算法

### 6.1 选择（selectEndpoint）

```
输入: protocol(anthropic|openai), requestedModel, explicitID(可空)
1. explicitID 非空 → 取该行；协议不匹配/未启用 → 404 endpoint_not_available
2. candidates = llm_endpoints
     WHERE enabled=1 AND protocol_<protocol>=1
       AND (health_status='ok' OR (health_status='disabled' AND cooldown_until<=now))
       AND (rate_limited_until<=now)
       AND (models=[] OR requestedModel='' OR requestedModel ∈ models)
3. auto 池: auto_group=1（auto=0 的行不参与自动调度，仅 explicitID 显式指定时可用——显式指定走 `X-Endpoint-Id: <id>` 请求头）
4. ORDER BY priority ASC, created_at ASC
5. 取第一个
```

- requestedModel 为空（如 /v1/models 聚合、探针）时跳过模型过滤。
- models=[] 视为「全部模型可用」。
- 选中后 acquire 并发信号量（§6.3）。

### 6.2 失败处理与重试

| 结果 | 计数 | 状态变更 | 重试 |
|---|---|---|---|
| 连接失败 / 超时 / 5xx | consecutive_failures+1（含重试后的第二次失败，同样计数） | 达 threshold 且组 failure_disable=1 → health_status='disabled', cooldown_until=now+cooldown_sec；否则仅计数 | 切下一候选重试一次（仅当响应头未发出） |
| 429 | 不计数（重试后的 429 也不计） | rate_limited_until=now+rate_limit_cooldown_sec | 切下一候选重试一次 |
| 其它 4xx | 不计数 | 无 | 不重试，直接透传 |
| 成功 | 计数清零 | consecutive_failures=0 | — |

- 重试仅一次；第二次失败把错误返回客户端。
- **流已开始输出（SSE 已写出首个事件）后绝不切换**，直接中断返回错误——避免损坏回合。
- 重试发生时打结构化日志（endpoint id、protocol、status、attempt）；本期**不**接 `system/api_retry` 帧通道（跨层接线延后，见 §18）。
- 池模式（组 failure_disable=0）：不写健康状态、不摘除，失败只透传（重试仍按上表执行一次，但不落状态）。

### 6.3 并发

- 每 endpoint 一个进程内信号量（map[id]chan struct{}，cap=max_concurrency；0=不限不建）。
- 超限排队：队列上限 32，等待上限 30s，超时返回 503 `endpoint_queue_timeout`。
- core 进程单例持有；仪表盘读用量 = len(inflight map)。

### 6.4 恢复

- disabled/cooldown 到期**懒恢复**：下次 select 时 `cooldown_until<=now` 即回 ok，清零计数。不跑后台定时器。
- 手动「重置健康」按钮（REST）立即清零。

## 7. 协议族与入口

### 7.1 新增明文入口（inbound/http 明文分支）

| Path | 协议族 | 说明 |
|---|---|---|
| POST /v1/messages | anthropic | body 透传；anthropic-version 头透传 |
| POST /v1/chat/completions | openai | body 透传 |
| GET /v1/models | —（聚合） | 所有 enabled 且对应 toggle 开启的 endpoint 的 models 并集去重；OpenAI 风格响应（`{object:"list", data:[{id, object:"model", owned_by:<endpoint id>}]}`）；models=[]（不限制）的 endpoint 不贡献条目；内置 aliang endpoint 的 models 为空时同样不贡献（其模型列表由手机侧 MODEL_PRESETS 维护，不在本接口聚合） |

### 7.2 重写规则

- `Authorization`：BYO endpoint → `Bearer <api_key>`（OpenAI 与 Anthropic 上游同用 Bearer；若某上游要求 `x-api-key`，后续版本再加 per-endpoint auth_style，本期不做）。
- 内置 aliang endpoint → 维持现有 relay 注入 `Authorization-Inner`（登录态），客户端自带的 Authorization 原样透传（哨兵/旧真实 key 均不透明接受，见 §10）。
- `Host`：BYO → endpoint base_url 的 host；内置 → 现有 aliang host 重写链（processor/watcher/aliang_http_host_rewrite.go）不动。
- body 的 model 字段**不改写**。

### 7.3 客户端 base_url 形态

- claude-code / pi：`http://127.0.0.1:56432`（自行追加 /v1/messages）
- codex / opencode：`http://127.0.0.1:56432/v1`（quick-setup 现有预设，quick_setup_support.go:18-33 不变）

### 7.4 非 AI 路径

56432 上非上述三个 path 的明文请求，走现有透明代理行为（零变化）。CONNECT 隧道零变化。

## 8. 内核（AliangCode）接入变更

`app/http/services/agent_ai_agentcore.go` 三个解析点改读 registry（core 进程内 service 调用）：

- `agentCoreBaseURL()`（:216）：EffectiveAgentCoreLLM 显式配置**保留最高优先级**（兼容旧行为，未配置时）→ registry 选中的 auto anthropic endpoint 的 base_url；内置行 base_url 空时返回 `agentCoreDefaultBaseURL`（:41）。
- `agentCoreAuthToken()`（:131）：EffectiveAgentCoreLLM.APIKey 仍最优先（:133 早返回保留）→ 选中 endpoint 的 api_key；内置行为空 → 现有链（ANTHROPIC_AUTH_TOKEN → user-center key 缓存）。
- `agentCoreEnsureUpstream()`（:297）：对选中 endpoint 做 HTTP 探测（GET /v1/models，2s 超时，只看可达+200/401 都算可达——401 说明链路通、key 问题留给请求本身报）；内置行维持现有自愈（POST /api/run/start 拉起 run service）+ MeshDelegateActive 跳过逻辑。
- 模型权威：手机下发的 model 仍是唯一输入；内核不新增拒绝逻辑；endpoint.models 仅作候选校验供 UI 展示，手机 model 不在任何列表时打 warn 不阻断。**路由层兜底**：registry 为空、全部 endpoint 不健康、或 requestedModel 不匹配任何 endpoint 的 models 时，回落到内置 aliang endpoint（若其健康）直发；内置也不可用才返回 503 `no_endpoint_available`。这样内核「不阻断」与路由层「过滤」口径统一。

## 9. quick-setup 变更（哨兵 key）

- 哨兵 key 改造：新增常量 `sk-aliang-local`（services 包），quick-setup 渲染/写入层（app/website/src/utils/quickSetupState.js renderComboContent、QuickSetupConfigurePanel.vue 的 {{api_key}} 替换点）把写入客户端配置文件的 `api_key`/`ANTHROPIC_AUTH_TOKEN` 值替换为哨兵；56432 路由层对客户端 Authorization **不透明接受**：哨兵、旧真实 key、任意非空串均放行；真实鉴权靠来源闸（回环）+ 内置 endpoint 的 relay 注入。
- 目的：密钥轮换不再传播到装机机器；真实密钥只存 endpoint registry。
- quick-setup catalog 的 api_keys 列表展示不变（用户仍能看到/管理平台 key），仅写入值换哨兵；quick_setup_support.go 的 base_url 预设不变。

## 10. 管理 UI（仪表盘设置页新子页）

`app/website/src/components/settings/EndpointManagerSettings.vue`，挂 SettingsPage 新子 tab（proxySubTabs 同机制：SettingsPage.vue:671 数据、140-165 v-if 挂载分支、:737 useEscLayer 折叠）：

- **列表**：name / base_url / 协议徽标(A/O) / auto / priority / 并发用量(inflight/max) / 健康徽标(ok 绿 / cooldown 琥珀 / disabled 红 / 429 蓝灰) / 最近错误。5s 轮询（ListenerSettings.vue:204 模式）。
- **新增/编辑弹窗**（AgentMCPRegistrySettings.vue:85 modal 模板）：name、base_url（内置行禁用）、api_key（显隐切换；编辑时留空=不改）、protocol toggles、auto toggle（内置行禁用）、priority、max_concurrency、models 文本域（换行/逗号分隔）。
- **行按钮**：测试连接、拉取模型、重置健康、删除（内置行禁用删除）。
- **组策略卡**：失败摘除开关 + threshold + cooldown + 429 冷却。
- 校验纯逻辑放 `app/website/src/utils/endpointSettings.js`，与 Go 侧 Validate 同源（现有 agentCoreSettings.js 模式）。

## 11. REST API（56431 管理面）

| 路由 | 说明 |
|---|---|
| GET /api/endpoints | 列表（api_key 掩码：前4+***+后4） |
| POST /api/endpoints | 创建 |
| GET/PUT/DELETE /api/endpoints/{id} | 单条（DELETE builtin → 409） |
| POST /api/endpoints/{id}/test | TCP 拨测 + GET /v1/models smoke（2s 超时），返回可达/模型数/错误摘要 |
| POST /api/endpoints/{id}/fetch-models | 拉 /v1/models 写入 models 字段，返回列表供确认 |
| POST /api/endpoints/{id}/reset-health | 清零健康状态 |
| GET/PUT /api/endpoint-groups/auto/policy | 组策略 |

注册方式沿用 `RegisterRoutes` 内 `register()` 助手模式（app/http/routes/routes.go:115-120）；新建 `app/http/handlers/endpoint_handler.go`（今天不存在），在 `RegisterRoutes` 中注册上述路由。

## 12. 存储与进程归属

- SQLite：aliang.data（common/cache/datafile.go:10 UnifiedDataDBFile，processor/auth/user_info.go getAuthSessionDB 打开），core 进程单例持有 registry service + 健康状态 + 并发信号量；56431 管理面与 56432 数据面同进程，无跨进程同步问题。写路径：CRUD/健康变更直写 SQLite；读路径每次 select 查库（endpoint 数量小，无缓存必要）。迁移：在 DB 打开链上补 `llm_endpoints` + `endpoint_group_policy` 的迁移注册（现有只对 authTokenRecord/authProfileRecord 做 AutoMigrate）。

## 13. 迁移与兼容

- **不自动迁移**旧 `customer.agentcore_llm`：升级后该字段保留在 config（POST patch 仍接受，deep-merge 不删键），`EffectiveAgentCoreLLM` 读取优先级保留（仍最高优先，§8），registry 只在其未配置时生效；用户在 UI 手动重建后想完全切到 registry，需清空旧字段（前端 applyAgentCoreLLMClearing 已有清空路径）。文档写明升级后需手动重建一次。
- 内置 aliang 行 seed 保证升级后开箱即用（等价今天的行为）。
- 旧客户端配置文件里的真实 key 不动（不透明接受，§9），无需重跑 quick-setup；新装机才写哨兵。

## 14. 安全与来源闸

- CRUD 走 56431 管理面，仅靠现有会话鉴权（56431 是独立管理服务器，`proxyFaceSourceAllowed`/`serviceBoundToLAN()` 只作用于 56432 代理面，不覆盖 56431；本期不在 56431 加新来源闸）。
- 数据面 56432 的 AI path 沿用 proxyFaceSourceAllowed 来源闸（回环/显式 --host 放行）。
- api_key 明文存 SQLite（与现状 config.json 明文同等级，不升级加密）；GET 返回掩码；PUT 空值=不改。
- BYO endpoint 的 base_url 校验：仅 http/https；非回环必须 api_key（沿用 AgentCoreLLMConfig.Validate 规则，types.go:695 同源）。

## 15. 可观测性

- 每次 select/转发/失败/摘除/恢复打结构化日志（logger.Info，含 endpoint id、protocol、model、耗时、状态）。
- 失败摘除与恢复必须留日志（用户自查入口）。
- 空 registry / 全摘除 / 模型不匹配的回落内置 endpoint 行为同样打日志（回落是静默降级，必须可查）。
- 本期不做 metrics 端点（后续可加 /api/endpoints/stats）。

## 16. 测试策略

- Go 单测：选择算法（优先级/健康/模型过滤/协议过滤）、失败状态机（计数/摘除/冷却/懒恢复/429）、并发排队（超限排队/超时拒绝）、内置行 seed 与 409、Validate 规则。
- 路由层集成测试：明文 /v1/messages 与 /v1/chat/completions 经 fake 上游的转发/重写/重试/404。
- 前端 vitest：endpointSettings.js 校验/归一（与 Go 同源用例对齐）、列表 CRUD、掩码显示。
- 回归：CONNECT 路径零变化（现有 inbound/http 测试全绿）；quick-setup 快照测试更新哨兵期望值。

## 17. 重构必带不变量

1. CONNECT 隧道与 MITM 路径零变化。
2. 非 AI path 的透明代理行为零变化。
3. 内置 aliang endpoint 的 relay 注入（Authorization-Inner 无条件注入）语义不变。
4. 流式响应一旦开始输出，绝不跨 endpoint 切换。
5. 失败摘除只由转发失败计数驱动；429 只冷却不摘除；4xx 不计数。
6. 池模式（failure_disable=0）不写任何健康状态。
7. api_key 掩码永不回显完整值；PUT 空值=不改。
8. builtin 行不可删、base_url 不可改、auto 不可关。
9. 旧 agentcore_llm 字段读优先级保留（EffectiveAgentCoreLLM 仍最高优先，兼容），registry 只在其未配置时驱动出网。
10. 哨兵 key 不透明接受：路由层不因客户端 key 值拒绝请求。

## 18. 开放问题（实现期确认）

- BYO 上游若要求 `x-api-key`（部分 Anthropic 兼容网关）而非 Bearer：本期统一 Bearer，收到用户反馈再加 auth_style 字段。
- /v1/models 聚合响应是否需要按协议分两个端点（/v1/models 返回 OpenAI 风格、Anthropic 风格无标准 models 端点）：本期单端点 OpenAI 风格。
- 多 auto 组：本期单组；表结构预留 group 字段。
- api_retry 帧通知本期降为日志级（ReportFailure 结构化日志），接入 `system/api_retry` 帧通道的跨层接线延后。