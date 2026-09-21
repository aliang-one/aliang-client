# 设计：Agent 认证链自愈与计时锚定

- 日期：2026-09-21
- 状态：已与用户确认方案 A（客户端 + 服务端协议配合）
- 涉及仓库（行号均指写作时点 HEAD，以符号检索为准）：
  - `alianggate`（本仓库，agent 客户端，v1.1.33）
  - `aliang-official-website/backend`（账号服务，路径 `/Users/mac/MyProgram/AiProgram/aliang-official-website/backend`）
- 不涉及：PhoneServer（`aliang-phone-agent-server`，仅背景）、手机端、UI

## 1. 背景与根因（2026-09-20 事故）

事故：Mac 客户端 agent 于 21:10 起被 PhoneServer 拒绝（register 401 / WS 1008），手机端显示离线，无法自愈，直到人工重登。

根因链条（已三方取证定案：客户端日志、PhoneServer K8s 日志与 DB、账号服务代码）：

1. **计时锚定错误（根因）**：客户端刷新判据 = `UpdatedAt + expires_in − 10min`，其中 `expires_in` 是**客户端自己登记的常量 86400**（`processor/auth/token_activate.go:22-25`），不是服务端返回值。9/20 13:41:19 旧凭据被注入 root core 落盘时 `UpdatedAt` 被重置为当下，客户端因此认为"明天 13:31 才需刷新"；而凭据（上游 sub2api access JWT）真实 exp = 当晚 21:10。TokenRefresher 每分钟 tick（`processor/auth/token_refresh.go:24`），全天 tick 上千次、**零次真正发起刷新**。
2. **上游家族静默死亡**：上游（sub2api）令牌家族最后一次成功轮换 ≈ 前一日 21:10，24h 无人续期后整体过期。21:10 PhoneServer 验 JWT 失败 → 回退调账号服务 `/api/auth/me` → `/me` 透传内的 ensure-fresh 自救触发轮换 → sub2api 拒绝（家族已死）→ 账号服务清 vault 强制重认证 → 401 → PhoneServer 拒绝并踢 WS。服务端自救机制行为正确，只是客户端没有任何环节在听。
3. **agent→owner 通知断链（无自愈）**：agent 是 non-owner，register 401 后走 `delegated_to_session_owner` 仅打日志即 return（`app/http/services/agent_service.go:1621-1624`）；owner（core/tray）对"凭据已被远端拒绝"无感知，SoftExpired 恢复循环（`processor/auth/soft_expiry.go:89-127`）从未启动。agent 单方面自我禁用并落 `refresh_invalid` sticky 终态（该标签在此被挪用——真实的 refresh 请求从未发出）。

结构性事实：账号服务已有 refresh arbiter（per-user 进程内锁 + PostgreSQL advisory lock、缓存令牌对不下发、sub2api 永远看不到 refresh 复用，`internal/httpapi/routes.go:1194-1263`），因此**客户端跨进程并发刷新不会引发上游家族撤销**——客户端无需自建跨进程刷新单飞锁。

## 2. 目标

1. 消除"旧凭据注入 + 本地重置计时 → 静默过期"这一事故模式（根治）。
2. 任何形式的远端拒绝凭据后，owner 能感知并走既有 SoftExpired 恢复链自动续期/重连；只有刷新确证 401 才落终态（自愈）。
3. 诊断不再被误导：`refresh_invalid` 终态只在真实刷新被拒后出现；测试日志不污染生产日志（卫生）。
4. 两端改动相互独立、可各自发版，四个兼容象限均不劣于现状。

## 3. 非目标

- 不改 refresh arbiter 的锁、轮换、撤销语义。
- 不改 sticky 终态的存储与手机端展示语义（只改其触发条件）。
- 不做客户端跨进程刷新单飞锁（服务端 arbiter 已化解正确性问题，见 §1 结构性事实）。
- 不加功能开关（改动小、测试覆盖、兼容矩阵安全）。
- 不动 PhoneServer 与 /me 透传路径。

## 4. 设计

### 4.1 服务端：arbiter 重写响应暴露真实过期锚点

`writeLocalSessionRefresh`（`internal/httpapi/routes.go:1405`）重写后的 login/refresh 响应体新增两个字段：

- `expires_in`：上游 sub2api access JWT 剩余秒数（基于 vault 的 access token，用现成 `accessExpiryOrDefault`，routes.go:1288；vault 缺 access 时返回 `defaultAccessTTL=50min` 对应值）。
- `access_expires_at`：绝对过期 unix 秒。

约束：纯增量字段；不触碰现有字段名与结构（客户端现有解析键不变）；login/register 走的响应重写路径（routes.go:2492-2538 一带）同样补充这两个字段，保证客户端在每次取得凭据的事件上都能拿到锚点。`/me` 透传不动。

### 4.2 客户端：计时锚定采用服务端真值

`RefreshSession`（`processor/auth/token_activate.go:272-425`）响应解析处：**优先采用响应中的 `expires_in`**（秒）；字段缺失或非正数时回退现有常量 86400 逻辑。现有公式 `UpdatedAt + expires_in − 10min`（`token_refresh.go:154-164`）不变。持久化继续写 `sub2api_auth_tokens.expires_in` 列，**零 schema 迁移**。`access_expires_at` 仅供日志与未来使用，不参与判定（单一真相源）。

### 4.3 客户端：注入/restore 即刷新（本次事故的直接解药)

在会话恢复/注入路径（"从落盘/IPC 拿到已有凭据并激活会话"的函数；13:41:19 事故的注入点，实施计划阶段以 grep `startTokenRefresh` 全部调用方 + DB 写入路径精确定位）挂载一个**异步立即刷新**。**挂载范围明确限定为 restore/注入激活路径**（非本次登录 freshly-issued 的凭据）——用户刚登录拿到的凭据由登录响应字段（§4.1）直接提供锚点，不在每个 `ActivateToken` 调用点泛滥挂载：

- 不阻塞启动与 UI；
- 失败不清会话：网络错误/5xx 仅记录并留待常规 tick（沿用现有错误分类，`errors.go:53-84` 仅 401 判终态）；
- 刷新 401 → 走既有 SoftExpired → 恢复失败升级 HardInvalid 流程，把"凭据已死"提前暴露在启动时刻而非深夜静默死亡；
- 去重：同一凭据代（generation/UpdatedAt）只触发一次，避免注入与常规 tick 叠加风暴。

效果：13:41 注入 → 立即刷新 → 上游续期并取得真实锚点，事故模式被结构性消除。

### 4.4 客户端：接通 agent→owner 凭据拒绝通知链

- **agent 侧**：两处 401 处理点改为"转换沿通知"——
  1. `recoverOrExpireAfterRegisterAuthRejection` 非 owner 分支（agent_service.go:1621-1624）；
  2. WS 握手 401 的终态禁用路径（`app/http/services/agent_remote_ws.go:121-130`）。
  仅在 ok→rejected 转换沿发送一次（本地状态机幂等，防风暴），POST 至 owner 进程新增端点，携带 `{reason, device_id, observed_at, generation?}`。
- **owner 侧**：新增小端点（挂在既有 dashboard HTTP 服务，路径与 agent→owner 通道在实施计划钉死：env 传递 dashboard 地址或复用既有反代路径，二选一）。收到通知 → 调 `RecoverOrExpireLocalSession`（已有重入守卫 agent_service.go:1603-1619）→ SoftExpired 退避循环 {0,5s,15s} → 刷新成功后既有 `handleAuthRefreshed`（`app/http/services/auth_runtime_hooks.go:124-142`）链自动转发新会话给 agent re-register；刷新 401 才写 `refresh_invalid` 真终态。
- **防回退**：通知携带会话 generation，owner 用 `GenerationActive`（`processor/auth/session_authority.go:351-354`）丢弃过期通知；迟到旧通知不得把新会话误标 SoftExpired。**generation 缺失时的确定性规则**：owner 仅在自身快照为 Active 且 `observed_at` 距今 ≤5 分钟时照常应用（走 `RecoverOrExpireLocalSession`，其幂等/重入守卫保证最坏只是一次多余刷新），否则忽略并记日志。
- 通知通道不可用（owner 未监听等）时保持现状行为（agent 自禁），不阻塞。

### 4.5 卫生

- `app/http/handlers/agent_handler.go:148-152` 注释按改动 4 后的真实行为重写（消除"注释与实现矛盾"）。
- `TestMain`（`app/http/services/testmain_test.go` 等）把日志/状态目录重定向至 `t.TempDir()`，杜绝 go test 写入 `~/.aliang/logs/aliang_core.log` 与真实状态文件。

## 5. 测试计划（TDD，先失败后实现）

| 改动 | 测试 |
|---|---|
| 4.1 服务端字段 | arbiter 缓存命中/轮换两条路径的响应均含 `expires_in`/`access_expires_at` 且与 vault exp 一致；缺 access 时回退 50min；旧字段逐字节不变 |
| 4.2 客户端锚定 | 响应含 `expires_in` 时判定时刻 = 服务端真值 − 10min；缺失/非法时回退常量；持久化写入真值 |
| 4.3 restore 即刷新 | 注入路径恰好触发一次刷新；网络错误不清会话且不重试风暴；401 进入 SoftExpired；同代去重 |
| 4.4 通知链 | agent 401 → owner 恰好收到一次通知；owner 启动 SoftExpired → 刷新成功 → 转发 → agent re-register 201；风暴去重；过期 generation 被丢弃；通道不可用降级为现状 |
| 4.5 卫生 | 全量 `go test` 后生产日志/状态文件 mtime 不变；两仓库 `go test ./...` 全绿 |

## 6. 兼容矩阵与发版

| 服务端 \ 客户端 | 旧客户端 | 新客户端 |
|---|---|---|
| 旧服务端 | 现状 | 锚点缺失→回退常量 86400（现状）；4.3/4.4 仍生效 |
| 新服务端 | 忽略新字段（现状） | 完整锚定 + 自愈 |

两端独立发版，无顺序约束；新服务端先上亦无害。

## 7. 风险与回归分析（已与用户对齐的要点）

1. **restore 即刷新的启动时序**：网络未就绪时刷新失败 → 按错误分类静默跳过，退化为现状，不会误清活会话（仅 401 清，且 401 是真死亡信号）。
2. **双 owner 并发注入/刷新**：arbiter per-user 锁串行化，最坏浪费一次调用，无正确性风险。
3. **通知风暴/乱序**：转换沿幂等 + generation 丢弃 + SoftExpired 既有退避；重入守卫已有。
4. **行为变化可见性**：agent 被拒后从"立即终态禁用"变为"owner 先试恢复"，窗口内 `deriveConnectionState` 渲染为 disconnected/等待登录（现有语义，手机端 UI 零改动）。
5. **服务端字段兼容**：纯增量，旧客户端忽略；响应重写只加键不改键。

## 8. 决策记录

- 不加功能开关（理由见 §3）。
- 不改 sticky 终态存储/展示，只改触发条件。
- 不做客户端跨进程刷新单飞锁。
- 客户端改动全部落在 `processor/auth/*` 与 `app/http/services/*` 既有边界内，不引入新包。

## 9. 附录：关键代码位置（现状 HEAD，行号会漂移、以符号为准）

- 客户端：`token_refresh.go` isTokenExpired/tick；`token_activate.go` 常量与 RefreshSession；`errors.go` 错误分类与终态清理；`soft_expiry.go` 恢复循环；`session_authority.go` generation；`agent_service.go` 1610-1626 断链点 / 404-430 disable；`agent_remote_ws.go` WS 401；`auth_runtime_hooks.go` handleAuthRefreshed；`agent_handler.go:148` 漂移注释；`testmain_test.go`
- 服务端：`internal/httpapi/routes.go` writeLocalSessionRefresh(1405) / refreshEarlyRotateSkew(1194) / accessExpiryOrDefault(1288) / lockUserRefresh(1230) / handleAuthRefreshArbiter(1438) / 响应重写(2492-2538)；`internal/auth/session_tokens.go` st_ 前缀
