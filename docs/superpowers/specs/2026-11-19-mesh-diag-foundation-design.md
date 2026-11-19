# 组网诊断地基修复批(阶段 0)设计

日期:2026-11-19
分支:`feat/mesh-diag-foundation`
前序:「组网能力原子化暴露给 aliangcode」可行性评估(2026-11-19,四路盘点+四路评审收敛)
定位:AI 组网面的**前置地基批**——不暴露任何 MCP 工具、不改任何写路径,只修「AI 面会建立在假数据上」的四个地基缺陷。

## 1. 背景与动机

评估实锤四个地基缺陷,不修则后续 AI 组网面(阶段 1 读面工具 + runbook)全部建立在假数据/404/日志盲区上:

| # | 缺陷 | 后果 |
|---|------|------|
| D1 | `StartMeshRosterSync` 全仓零调用方(app/http/services/mesh_roster_sync.go:52 定义,注释声称 boot 两形态各调一次,实际 cmd/start.go:136、cmd/core.go:199 只接 lookup 侧 `WireMeshAutoPeerProbe`) | roster 缓存永不填充 → `MeshRosterEntryFor` 恒 miss → auto_peers 恒 fail-closed,**自动组网在当前树事实性死亡**。继 start.go 漏接线、utun7 吞广播之后第三例「本体在、接线断」 |
| D2 | `/api/mesh/status` 假绿(app/http/handlers/mesh_handler.go:88 `HandleStatus` 全部字段从配置推导,beacon 是否真在跑不可见) | AI/人把「enable=true」当「功能活着」,自信错误归因——比没有数据更糟 |
| D3 | mesh 端点单边注册(`/api/mesh/status`+`/api/mesh/lan_peers` 仅注册在 core 侧 app/http/routes/routes.go,56433 `registerAgentRoutes` 缺失);且 parity 护栏正则只扫 `api/agent|api/proxy|mcp/aliang` 前缀,**mesh 对护栏隐形** | `aliang mcp` stdio 面缺省打 56433(cmd/mcp.go:124)→ 404 → AI 把半注册误判成网络故障(2026-10-21/10-23/10-29 三次事故同族) |
| D4 | mesh 域 20 处 stdlib `log.Printf` 不进 `/api/logs` 环形 buffer(common/logger) | beacon 起停/装配失败/委托不可达等最关键行 AI 经 HTTP 查不到,旁证链断一半 |

## 2. 范围

**做**:D1 接线、D2 运行态真值、D3 双注册+护栏扩围、D4 日志收编、接线孤儿审计测试。
**不做(阶段边界)**:egress 委托计数/载体分桶/SkipMemo 读面/身份一致性(→阶段1)、MCP 工具包装(→阶段1)、runbook 技能(可与阶段1并行)、任何写面(→阶段3)。

## 3. 子项设计

### 3.1 roster sync 孤儿接线(D1)

- `cmd/start.go:133`(`StartMeshLANAssembly` 之后)与 `cmd/core.go:196`(同位)各加一行 `services.StartMeshRosterSync()`,注释对齐现有装配接线注释风格(注明第三例接线断、auto_peers 信任锚)。
- 该函数 `sync.Once` 幂等(mesh_roster_sync.go:53)且自带 `Customer.Mesh == nil` 跳过守卫(:79),两个 boot 形态各调一次零风险;启动即首同步(`MeshRosterSyncTick` 首调用)。
- **验收**:boot 后 30s 内 roster 缓存非空可查(经 3.2 的 `MeshRosterSyncStatus` 访问器);单测 `TestRosterSyncWiredInBothBootForms`(见 3.5)。

### 3.2 `/api/mesh/status` 补运行态真值(D2)

只增字段不改既有字段,向后兼容:

| 新字段 | 数据源 | 改动点 |
|---|---|---|
| `beacon_running bool` | `lanbeacon.Beacon` 既有 `started` 位(beacon.go:189,mu 保护,:322 置真/:346 置假) | Beacon 加 `Running() bool`(RLock 读 `started`) |
| `lan_running bool` | `tunnel.meshLAN.running`(mesh_assembly.go:195) | tunnel 加 `MeshLANRunning() bool`,对齐既有 `MeshLANPort()`(:497)访问器模式 |
| `roster_sync` 对象 `{synced bool, last_sync_at string, last_error string}` | mesh_roster_sync.go 既有 `meshRosterFetchedAt`(:40) | services 加 `MeshRosterSyncStatus()` 快照访问器;顺带补 `meshRosterLastError string` 记账——现状 fetch 失败静默 `return`(:157-158),「从未同步」「拉取失败」两态无法区分,补后三态齐全。`last_sync_at` 用 RFC3339 UTC 字符串(对齐 `AgentRuntimeSnapshot.CollectedAt` 既有惯例,agent_service.go:354) |

- `meshStatusResponse` 加三字段(json tag:`beacon_running`/`lan_running`/`roster_sync`);`MeshHandler` 注入缝补三个函数字段(对齐 :24-27 既有 seam 风格),nil 安全返回零值。
- 语义:`roster_sync.synced=false` = 从未同步过(D1 未接线即此态);`synced=true`+非空 `last_error` = 上一轮拉取失败(保留旧缓存);`synced=true`+空 error = 健康。
- **验收**:人为占住 beacon UDP 端口装配失败后,status 显出「enable=true 且 beacon_running=false」;单测覆盖三访问器翻转与 roster 三态。

### 3.3 mesh 端点双注册 + parity 护栏扩围(D3)

- 56433 侧 `registerAgentRoutes`(app/agentruntime/server.go:124)注册 `/api/mesh/status` 与 `/api/mesh/lan_peers`。
- **关键决策:不能裸挂 `NewMeshHandler()`**——beacon/装配住 core 进程,56433 实例会恒报 `running=false`+空候选(反向假红,违反本批原则)。写 forward-to-core 透传 handler:仅 user-agent 侧生效,GET 同路径转发 `http://127.0.0.1:<DefaultManagementPort>`(processor/config/defaults.go:13,56431),透传响应;core 侧注册形态不变(handler 直答,透传 handler 在 core 进程不装配/不会被路由到)。
- `route_parity_test.go:43-44` 两条正则各追加 `api/mesh` 前缀,护栏从此盖住 mesh 面。
- **验收**:parity 绿;worktree 内起两个 mux(httptest)验证 56433 透传拿到 core 真值。

### 3.4 mesh 域日志收编(D4)

20 处实锤清单(grep `log.Printf` 非 test):

| 文件 | 处数 | 内容 |
|---|---|---|
| app/mesh/lanbeacon/beacon.go | 1 | beacon 广播失败行 |
| app/mesh/punch/listener.go | 1 | listener 行 |
| app/tunnel/bridge.go | 5 | 桥接行(含本次基线跑出的 `[AGENT-TCP]` 族) |
| app/tunnel/manager.go | 4 | manager 行 |
| app/tunnel/mesh_assembly.go | 5 | 装配起停/失败行(诊断价值最高) |
| app/tunnel/mesh_dial.go | 2 | 拨号行 |
| app/tunnel/piko_logger.go | 2 | 已核语义(spec 评审实锤):两处均在独立函数 `reportTCPDisabled`/`reportMeshRelayDisabled` 内,非 piko 库重定向器——直接替换即可 |

- 全部改 `logger.Info(fmt.Sprintf(...))`(common/logger,进 /api/logs 1000 环形 buffer);`[MESH-*]`/`[AGENT-TCP]` 等前缀原样保留(前端与未来 runbook 依赖该 token)。
- **验收**:beacon 起停/装配失败行出现在 `/api/logs`;mesh 域 `grep log.Printf` 清零(piko_logger.go 若有意豁免则注释说明理由)。

### 3.5 接线孤儿审计测试(治本)

新增源码级审计测试(parity 测试同款风格,`app/agentruntime/route_parity_test.go:31` 的源码扫描手法):
- 断言 `StartMeshRosterSync(` 在 `cmd/start.go` 与 `cmd/core.go` 各出现一次(排除注释行,同 `extractAgentRoutePaths` 剥注释逻辑)。
- 断言 `StartMeshLANAssembly(`、`WireMeshAutoPeerProbe(` 同样各出现一次——把整个「boot mesh 装配族」纳入审计,「接线孤儿」类缺陷从此 CI 拦截。
- 落点拍板:cmd 包新测试文件 `cmd/boot_wiring_audit_test.go`——与被审计源码(start.go/core.go)同包,相对路径直读,同 route_parity_test.go 的包内相对路径惯例。

## 4. 测试与验收线

1. 三个运行态访问器单测:`Running()` 翻转、`MeshLANRunning()` 装配停后 false、`MeshRosterSyncStatus()` 三态。
2. 透传 handler 单测:httptest 模拟 core,56433 侧透传拿到真值;core 侧形态不变。
3. parity 扩围后全绿(自动覆盖 mesh 双注册)。
4. 接线孤儿审计测试绿。
5. 日志收编后:mesh 域 log.Printf 清零 + 一条集成断言(装配失败行进 buffer)。
6. **既有测试状态不变**:`TestAssemblySeamsPunchThreeStates` 为 master 预存基线失败(1b8c36b 主树同败,`装配产物应为 *ExitClient, got <nil>` 三子项),本批不修不扩波,执行后保持同态即可。

## 5. 风险与回滚

- 全批为只读/接线级:无写路径改动、无配置 schema 变更、无跨仓契约变更。
- `meshStatusResponse` 只增字段,前端判空既有守卫(peers/lan_peers 恒数组惯例)不受影响;新对象字段前端未消费前为死字段,零风险。
- 透传 handler 仅影响 56433 上两个此前 404 的路径——从 404 变为透传,不存在行为回退面。
- 回滚 = 单批 git revert。
