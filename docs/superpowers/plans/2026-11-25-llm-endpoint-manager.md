# LLM Endpoint 管理平台实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把 56432 本地代理改造成多 endpoint 聚合路由层：独立 SQLite registry + CRUD REST + 明文 AI 路径路由 + 严格优先级 failover + 健康摘除 + per-endpoint 并发排队 + 仪表盘管理子页 + 内核接入 + quick-setup 哨兵 key。

**Architecture:** 新增 `llm_endpoints`/`endpoint_group_policy` 两张表（aliang.data，复用 getAuthSessionDB 的 gorm 句柄）；core 进程内 `EndpointService` 单例持有 registry+信号量+健康状态；56432 明文 HTTP 分支（HandleHttpConnection）对 `/v1/messages`、`/v1/chat/completions`、`/v1/models` 三个 path **全权处理**（选择→转发→失败重试→回落内置→503）；内置 aliang 走 `tcp.GetHandler()` 现有 RouteToALiang 链，BYO 直连；56431 管理面新增 `/api/endpoints/*`；仪表盘 `EndpointManagerSettings.vue` 子页；内核三解析点改读 registry；quick-setup 哨兵 `sk-aliang-local`。

**Tech Stack:** Go 1.23+, gorm/sqlite (`aliang.data`), Vue 3 Options API, vitest。

**Import 环纪律（关键）：** `app/http/services` 已 import `inbound/http`（run.go:16），所以 `inbound/http` **禁止** import `app/http/services`。路由层通过包内接口 + 包级 var 注入（同 dispatch.go:41 `proxyFaceSourceAllowed` 模式），由 core 启动链赋值。

---

## 0. 文件结构（锁定）

**新建**
- `processor/config/endpoint.go` — `LLMEndpoint` / `EndpointGroupPolicy` struct + Validate
- `processor/storage/endpoint_store.go` — gorm CRUD（复用共享句柄 / 测试独立句柄）
- `processor/storage/endpoint_store_test.go`
- `app/http/services/endpoint_service.go` — 选择算法/健康/并发信号量/测试/拉模型 + 单例构造
- `app/http/services/endpoint_service_test.go`
- `inbound/http/endpoint_router.go` — AI 路径路由层（接口注入，不含业务依赖）
- `inbound/http/endpoint_router_test.go`
- `app/http/handlers/endpoint_handler.go` — REST handler
- `app/website/src/utils/endpointSettings.js` + `.test.js`
- `app/website/src/components/settings/EndpointManagerSettings.vue`

**修改**
- `processor/auth/user_info.go:229` — AutoMigrate 追加两表
- `app/http/routes/routes.go` — RegisterRoutes 注册 `/api/endpoints/*`（**仅 core 侧单注册**，不走 agentruntime registerAgentRoutes 双注册——那是 proxy/known-hosts 的跨进程特例，routes.go:134 注释说明）
- `inbound/http/handler.go:23` — HandleHttpConnection 内 AI path 分流
- `app/http/server.go` 或 StartUnifiedIngress 调用链 — 路由层注入接线
- `app/http/services/agent_ai_agentcore.go:131,216,297` — 三解析点
- `app/website/src/utils/quickSetupState.js:42`（renderComboContent）+ `quickSetupState.test.js` — 哨兵
- `app/website/src/components/SettingsPage.vue` — 子 tab + 挂载 + Esc
- `app/website/src/i18n/zh.js`、`en.js`
- 升级说明文档一节（§13 手动重建提示）

---

## Task 1: 数据层 — struct + store + migration

**Files:**
- Create: `processor/config/endpoint.go`
- Create: `processor/storage/endpoint_store.go`
- Create: `processor/storage/endpoint_store_test.go`
- Modify: `processor/auth/user_info.go:229`（AutoMigrate 追加）

- [ ] **Step 1: 写失败测试**（`:memory:` 独立句柄，测试内自跑 AutoMigrate）

```go
package storage

import (
	"testing"

	"aliang.one/nursorgate/processor/config"
)

func newTestStore(t *testing.T) *EndpointStore {
	t.Helper()
	s, err := NewEndpointStoreForTest()
	if err != nil { t.Fatal(err) }
	t.Cleanup(func() { s.Close() })
	return s
}

func TestEndpointStore_SeedBuiltin(t *testing.T) {
	s := newTestStore(t)
	es, err := s.List()
	if err != nil { t.Fatal(err) }
	if len(es) != 1 || es[0].ID != "builtin-aliang" { t.Fatalf("want builtin-aliang, got %+v", es) }
	if !es[0].ProtocolAnthropic || !es[0].ProtocolOpenAI { t.Fatal("builtin must support both protocols") }
	if es[0].BaseURL != "" || !es[0].Builtin || es[0].AutoGroup != true { t.Fatal("builtin shape wrong") }
}

func TestEndpointStore_DeleteBuiltinRejected(t *testing.T) {
	s := newTestStore(t)
	if err := s.Delete("builtin-aliang"); err == nil { t.Fatal("want error deleting builtin") }
}

func TestEndpointStore_CRUD(t *testing.T) {
	s := newTestStore(t)
	e := &config.LLMEndpoint{ID: "e1", Name: "prov", BaseURL: "https://x.example.com", APIKey: "sk-1",
		ProtocolAnthropic: true, AutoGroup: true, Priority: 10, Enabled: true}
	if err := s.Create(e); err != nil { t.Fatal(err) }
	got, err := s.Get("e1"); if err != nil { t.Fatal(err) }
	if got.APIKey != "sk-1" { t.Fatal("key roundtrip failed") }
	got.Priority = 5
	if err := s.Update(got); err != nil { t.Fatal(err) }
	if err := s.Delete("e1"); err != nil { t.Fatal(err) }
}

func TestEndpointStore_Policy(t *testing.T) {
	s := newTestStore(t)
	p, err := s.GetPolicy(); if err != nil { t.Fatal(err) }
	if !p.FailureDisable || p.FailureThreshold != 3 { t.Fatalf("default policy wrong: %+v", p) }
	p.FailureThreshold = 5
	if err := s.SetPolicy(p); err != nil { t.Fatal(err) }
	if p2, _ := s.GetPolicy(); p2.FailureThreshold != 5 { t.Fatal("policy not persisted") }
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./processor/storage/... -v`
Expected: FAIL（包不存在）

- [ ] **Step 3: 实现 struct**（`processor/config/endpoint.go`，imports: fmt/net/url/strings）

```go
package config

type LLMEndpoint struct {
	ID                 string `gorm:"primaryKey" json:"id"`
	Name               string `json:"name"`
	BaseURL            string `json:"base_url"`
	APIKey             string `json:"-"`
	ProtocolAnthropic  bool   `json:"protocol_anthropic"`
	ProtocolOpenAI     bool   `json:"protocol_openai"`
	AutoGroup          bool   `json:"auto_group"`
	Priority           int    `json:"priority"`
	MaxConcurrency     int    `json:"max_concurrency"`
	Models             string `json:"models"` // JSON array string; [] = 不限制
	Enabled            bool   `json:"enabled"`
	Builtin            bool   `json:"builtin"`
	HealthStatus       string `json:"health_status"` // ok/disabled/cooldown
	ConsecutiveFailures int   `json:"-"`
	CooldownUntil      int64  `json:"-"`
	RateLimitedUntil   int64  `json:"-"`
	LastError          string `json:"last_error"`
	CreatedAt          int64  `json:"created_at"`
	UpdatedAt          int64  `json:"updated_at"`
}

func (e *LLMEndpoint) Validate() error {
	if strings.TrimSpace(e.Name) == "" { return fmt.Errorf("name required") }
	if !e.ProtocolAnthropic && !e.ProtocolOpenAI { return fmt.Errorf("at least one protocol required") }
	if strings.TrimSpace(e.BaseURL) != "" {
		u, err := url.Parse(strings.TrimSpace(e.BaseURL))
		if err != nil || u.Scheme == "" || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			return fmt.Errorf("invalid base_url")
		}
		if !isLoopbackHostName(u.Hostname()) && strings.TrimSpace(e.APIKey) == "" {
			return fmt.Errorf("api_key required for non-loopback")
		}
	}
	if e.Priority < 0 || e.MaxConcurrency < 0 { return fmt.Errorf("priority/max_concurrency must be >=0") }
	if e.Builtin && e.BaseURL != "" { return fmt.Errorf("builtin base_url must be empty") }
	return nil
}

type EndpointGroupPolicy struct {
	Group                string `gorm:"primaryKey" json:"group"` // 预留多组；本期恒 "auto"
	FailureDisable       bool   `json:"failure_disable"`
	FailureThreshold     int    `json:"failure_threshold"`
	CooldownSec          int    `json:"cooldown_sec"`
	RateLimitCooldownSec int    `json:"rate_limit_cooldown_sec"`
}
```

（`isLoopbackHostName` 已存在于 types.go:1463 同包，直接用。）

- [ ] **Step 4: 实现 store**（`processor/storage/endpoint_store.go`）

- `NewEndpointStoreFromDB(db *gorm.DB)` — **生产构造**：复用共享句柄（不二次打开 aliang.data）。**接线步骤**：`processor/auth/user_info.go` 新增导出包装 `SharedAuthSessionDB() (*gorm.DB, error)`（内部复用 getAuthSessionDB 单例，:206——该函数未导出，storage/services 无法直取）；`GetEndpointService()` 首次调用时 `NewEndpointStoreFromDB(auth.SharedAuthSessionDB())`；AutoMigrate 已在 DB 打开链上（Task 1 Step 5），时序安全
- `NewEndpointStoreForTest()` — 测试构造：`:memory:` + 自己 AutoMigrate
- `SeedBuiltin()`：INSERT OR IGNORE `builtin-aliang`（anthropic+openai 都开，priority 0，auto，enabled，builtin）
- `List/Get/Create/Update/Delete`（Delete builtin → ErrBuiltinImmutable）
- `GetPolicy/SetPolicy`（默认行 `group="auto"`: failure_disable=true, threshold=3, cooldown=300, rate_limit=60）

- [ ] **Step 5: migration** — `processor/auth/user_info.go:229` 行追加：

```go
if err := db.AutoMigrate(&authTokenRecord{}, &authProfileRecord{},
	&config.LLMEndpoint{}, &config.EndpointGroupPolicy{}); err != nil {
```

- [ ] **Step 6: 跑测试** — `go test ./processor/storage/... ./processor/config/... -v`，PASS
- [ ] **Step 7: 提交**

```bash
git add processor/config/endpoint.go processor/storage/ processor/auth/user_info.go
git commit -m "feat(endpoint): LLM endpoint registry 数据层 + 内置 aliang seed"
```

---

## Task 2: 选择算法 + 健康 + 并发（service 层）

**Files:**
- Create: `app/http/services/endpoint_service.go`
- Create: `app/http/services/endpoint_service_test.go`

- [ ] **Step 1: 写失败测试**，覆盖：
  1. Select 协议过滤/优先级排序/模型过滤/禁用过滤/429 冷却过滤
  2. **回落**：无候选 → builtin 健康 → 返回 builtin；builtin 也 disabled/cooldown → `ErrNoEndpoint`
  3. ReportFailure：5xx 计数→threshold 摘除（cooldown_until）；429 → rate_limited_until 不摘；4xx 不计数；成功清零
  4. 池模式（policy.FailureDisable=false）：失败不落任何状态
  5. Acquire：max_concurrency 排队；超 32 队列或 30s → ErrQueueTimeout；0=不限
  6. 懒恢复：cooldown 到期后 Select 返回该 endpoint 且清零计数
  7. Inflight() 返回 map[id]int（供 REST 展示）
  8. ResetHealth 立即清零

- [ ] **Step 2: 跑测试确认失败**
- [ ] **Step 3: 实现**（要点）：

```go
type EndpointService struct {
	store   *storage.EndpointStore
	mu      sync.Mutex
	sems    map[string]chan struct{}
	inflight map[string]int
	policy  config.EndpointGroupPolicy
}
var endpointServiceOnce sync.Once; var sharedEndpointService *EndpointService
func GetEndpointService() *EndpointService  // 单例；首次调用时 SeedBuiltin
```

- `Select(protocol, model, explicitID)`：explicitID 非空直取（协议/启用校验）；否则 candidates=enabled ∧ protocol toggle ∧ (ok ∨ cooldown 到期) ∧ (rate_limited 到期) ∧ (models 空 ∨ model 空 ∨ model∈models) ∧ auto_group；按 priority,created_at 排序取首；**空候选 → builtin 健康则回落（打日志 `endpoint: fallback to builtin`）→ 否则 ErrNoEndpoint**
- `Acquire`：sems map 惰性建（cap=max_concurrency，0 跳过）；select 等待 30s；队列深度用 len(已等待) 计数 ≤32
- `ReportFailure(ep, status, err)`：按 §6.2 表格（429 不计数只冷却；5xx/conn 计数；达 threshold 且 policy.FailureDisable → disabled+cooldown_until；4xx 忽略）；每次 ReportFailure/ReportSuccess/回落打结构化日志（endpoint id/protocol/status/计数）
- `FetchModels(id)`：GET `<base_url>/v1/models`（2s 超时；带 Bearer key），解析 OpenAI 或 Anthropic 风格响应为 []string，**不自动写库**，返回给 handler 由 PUT 确认落库
- `Test(id)`：TCP 拨测 + GET /v1/models smoke，返回 (ok, modelCount, errSummary)
- `ModelsUnion()`：enabled ∧ 对应协议 toggle 开的 endpoint models **并集去重**；models=[]（不限制）与 builtin 不贡献条目（Task 3 的 AIEndpointSelector 接口必需此方法，供 /v1/models 聚合）

- [ ] **Step 4: 跑测试** — `go test ./app/http/services/ -run TestEndpointService -v`，PASS
- [ ] **Step 5: 提交**

---

## Task 3: 路由层（56432 明文 AI 路径，全权处理）

**Files:**
- Create: `inbound/http/endpoint_router.go`
- Create: `inbound/http/endpoint_router_test.go`
- Modify: `inbound/http/handler.go:23`（HandleHttpConnection 分流）
- Modify: `app/http/server.go`（或 StartUnifiedIngress 调用链）接线

**Import 环解法：** `inbound/http` 定义窄接口 + 包级 var：

```go
// endpoint_router.go
type AIEndpointSelector interface {
	Select(protocol, model, explicitID string) (id string, baseURL string, apiKey string, builtin bool, err error)
	Acquire(id string, maxConcurrency int) (release func(), err error)
	ReportSuccess(id string)
	ReportFailure(id string, status int, errMsg string)
	ModelsUnion() []map[string]any // /v1/models 聚合数据
}
var aiSelector AIEndpointSelector // nil = 路由层不激活，AI path 走原透明代理（测试/降级安全）
func SetAIEndpointSelector(s AIEndpointSelector) { aiSelector = s }
```

（`app/http/services` 的 EndpointService 适配此接口；**不**注入 Authorization-Inner——watcher relay 已无条件注入，不变量 3。）

- [ ] **Step 1: 写失败测试**（fake 上游 httptest.Server），用例：
  1. `/v1/messages` → 选中 BYO → 转发：断言上游收到 `Authorization: Bearer <key>`、**无** `Authorization-Inner`、Host=上游 host、body 原样
  2. `/v1/chat/completions` → 同上（openai 族）
  3. 失败重试：候选1 5xx → 切候选2 成功（响应头未发出前）
  4. **不变量 4**：候选1 返回 200 + 首个 SSE 事件后断连 → **不**切候选2，客户端收到错误
  5. 无候选 + builtin 健康 → 走 builtin（转发到 aliang metadata 链，用 fake tcpHandler 缝验证）
  6. 无候选 + builtin 不健康 → 503 JSON `no_endpoint_available`
  7. `GET /v1/models` 聚合：多 endpoint models 并集去重；models=[] 与 builtin 不贡献；响应形状 `{object:"list",data:[{id,object:"model",owned_by:...}]}`
  8. `X-Endpoint-Id` 显式指定：命中 auto=0 行；协议不匹配 → 404
  9. aiSelector=nil → AI path 落回原透明代理行为（降级安全）
  10. 非 AI path（如 `/foo`）不进路由层

- [ ] **Step 2: 跑测试确认失败**
- [ ] **Step 3: 实现**：

`HandleHttpConnection` 在 `extractMetadataFromHTTP` 后、`newHTTPRequestReplayConn` 前插入：

```go
if aiSelector != nil && isAIPath(req.URL.Path) {
	handleAIRequest(conn, reader, req) // 全权处理，必 return；永不回落透明代理
	return
}
```

`handleAIRequest`（要点）：
- 读 body 到内存（**cap 8MB**，超限 413）——重试需要按 attempt 重建 `*http.Request`（复用 body bytes）
- Select → Acquire → 构造 attempt 请求（内置：metadata 改写为 aliang 目标后委托 `tcp.GetHandler().Handle`；BYO：`http.Client{Timeout:0}` 直连 base_url，`Authorization: Bearer <key>`，Host 覆盖为上游 host，透传其余头与 body）
- 流式判断：读到响应头未发出前失败 → ReportFailure + 取下一候选重试一次；**响应已开始写客户端 → 只 ReportFailure + 断开**（不变量 4）
- 429 → ReportFailure(429) + 重试一次；其它 4xx → 透传不重试不计数
- 结束 ReportSuccess/ReportFailure + release()
- 全程结构化日志（select/forward/fail/retry/fallback，§15）

`/v1/models`：直接 `aiSelector.ModelsUnion()` 序列化返回，不转发。

- [ ] **Step 4: 接线** — core 启动链（app/http/server.go StartUnifiedIngress 调用前）：`inbound/http.SetAIEndpointSelector(adapter(services.GetEndpointService()))`
- [ ] **Step 5: 跑测试** — `go test ./inbound/http/... -v`，PASS；`go build ./...` 全仓编译过（验证无 import 环）
- [ ] **Step 6: 提交**

---

## Task 4: REST API（56431 管理面，core-only 单注册）

**Files:**
- Create: `app/http/handlers/endpoint_handler.go`
- Create: `app/http/handlers/endpoint_handler_test.go`
- Modify: `app/http/routes/routes.go`（RegisterRoutes 内 register）

- [ ] **Step 1: 写 handler 测试**（CRUD/掩码/409/校验 400/test/fetch-models/reset-health/policy/inflight）
- [ ] **Step 2: 实现**：
  - GET /api/endpoints：列表；`api_key` 掩码（前4+***+后4，长度≤8 时全掩）；附 `inflight` 字段（来自 service.Inflight()）
  - POST /api/endpoints：Validate → Create
  - PUT /api/endpoints/{id}：**api_key 空值 = 不改**（保留旧值）；builtin 禁改 base_url/auto_group
  - DELETE /api/endpoints/{id}：builtin → 409
  - POST /api/endpoints/{id}/test | /fetch-models | /reset-health
  - GET/PUT /api/endpoint-groups/auto/policy
- [ ] **Step 3: 注册路由**（`register("/api/endpoints", ..., http.MethodGet, http.MethodPost)` 等 7 条；**不加 agentruntime 侧**——core-only，见 routes.go:134 注释的特例说明）
- [ ] **Step 4: 跑测试**，PASS
- [ ] **Step 5: 提交**

---

## Task 5: 内核接入（三解析点）

**Files:**
- Modify: `app/http/services/agent_ai_agentcore.go:131,216,297`
- Test: 已有 agentcore 相关测试跑绿

- [ ] **Step 1: `agentCoreBaseURL()`** — EffectiveAgentCoreLLM 非 nil 仍最高优先（不变量 9）；否则 `GetEndpointService().Select("anthropic","", "")`，builtin（base_url 空）→ 返回 `agentCoreDefaultBaseURL`；BYO → 其 base_url
- [ ] **Step 2: `agentCoreAuthToken()`** — EffectiveAgentCoreLLM.APIKey 最优先（:133 早返回保留）；否则选中 endpoint 的 api_key；builtin 空 key → 现有链（env → settings → user-center 缓存）
- [ ] **Step 3: `agentCoreEnsureUpstream()`** — BYO endpoint：HTTP 探测 GET /v1/models 2s（200/401 都算可达）；builtin：维持 MeshDelegateActive 跳过 + POST /api/run/start 自愈逻辑
- [ ] **Step 4: 跑测试** — `go test ./app/http/services/ -run 'AgentCore|Endpoint' -v`，PASS
- [ ] **Step 5: 提交**

---

## Task 6: quick-setup 哨兵 key

**Files:**
- Modify: `app/website/src/utils/quickSetupState.js:42`（renderComboContent）
- Modify: `app/website/src/utils/quickSetupState.test.js`
- Modify: `app/http/services/quick_setup_support.go`（新增导出常量供文档对齐）

- [ ] **Step 1: 写失败测试**：renderComboContent 对 `{{api_key}}` 占位符输出哨兵 `sk-aliang-local` 而非真实 key
- [ ] **Step 2: 实现**：`renderComboContent` 中 api_key 变量值替换为哨兵常量（`ANTHROPIC_AUTH_TOKEN` 是 claude-code 模板内 `{{api_key}}` 的落点，替换占位符值即可覆盖）
- [ ] **Step 3: 跑 vitest** — `cd app/website && npx vitest run src/utils/quickSetupState.test.js`，PASS
- [ ] **Step 4: 提交**

---

## Task 7: 仪表盘 UI

**Files:**
- Create: `app/website/src/utils/endpointSettings.js` + `.test.js`
- Create: `app/website/src/components/settings/EndpointManagerSettings.vue`
- Modify: `app/website/src/components/SettingsPage.vue`（proxySubTabs 数据 :671 + v-if 挂载 :140-165 + useEscLayer :737）
- Modify: `app/website/src/i18n/zh.js`、`en.js`

- [ ] **Step 1: endpointSettings.js**（validate/normalize，与 Go Validate 同源；掩码显示规则；models 文本域↔JSON array 转换）
- [ ] **Step 2: vitest**（与 Go Validate 同源用例：双协议全 0 拒、非回环缺 key 拒、builtin base_url 拒、掩码）
- [ ] **Step 3: Vue 组件**（AgentMCPRegistrySettings.vue:85 modal 模板 + ListenerSettings.vue:204 5s 轮询 STATUS_TONES 徽标模式）：列表（name/base_url/协议徽标/auto/priority/并发 inflight/max/健康徽标/最近错误）+ 弹窗 + 行按钮（测试/拉模型/重置健康/删除）+ 组策略卡
- [ ] **Step 4: SettingsPage 子 tab + Esc 折叠**
- [ ] **Step 5: i18n**（`endpointManager_*` 前缀，zh/en 全角标点「」规范）
- [ ] **Step 6: 跑 vitest** — `cd app/website && npx vitest run`，PASS
- [ ] **Step 7: 提交**

---

## Task 8: 端到端验证 + 收尾

- [ ] **Step 1:** `gofmt -l processor/storage app/http inbound/http`（应为空）+ `go vet ./...`
- [ ] **Step 2:** `go build ./...`
- [ ] **Step 3:** `go test ./processor/... ./app/http/... ./inbound/http/... -race`
- [ ] **Step 4:** `cd app/website && npx vitest run`
- [ ] **Step 5:** 升级文档一节：旧 `customer.agentcore_llm` 不自动迁移，升级后在新 UI 手动重建；想完全切 registry 需清空旧字段
- [ ] **Step 6:** 手动冒烟：内置 endpoint 走 aliang 隧道（现有行为不变）；BYO 新增→test→fetch-models→选中→转发；3 次失败→摘除→冷却到期懒恢复；池模式开关不落状态
- [ ] **Step 7:** 提交收尾

---

## 备注

- CONNECT 路径（inbound/http/tunnel.go）零改动；非 AI path 透明代理零改动。
- `aiSelector=nil`（测试/降级）时 AI path 走原透明代理行为——降级安全，但生产接线必须在启动链完成。
- **api_retry 帧通知（spec §6.2）**：本期**实现为日志级**（ReportFailure 结构化日志），不接入 `system/api_retry` 帧通道——那需要打通路由层（inbound/http）到内核帧通道（app/agentcore/stream.go）的跨层通知，属独立接线；已在 §18 开放问题登记，不在本计划范围。若实施中发现低成本接缝（如 services 层适配器内已有帧通道引用），可顺带实现并在提交信息注明。
- X-Endpoint-Id 供管理面测试与内核使用；auto=0 行不进自动调度。