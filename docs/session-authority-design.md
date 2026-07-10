# 用户身份权威与会话状态机设计（SessionAuthority）

> 状态：**设计草案，待评审**。本文件固化 Phase 2 架构方向，供评审状态机、`device_token` 策略与 `SoftExpired` 语义后再动结构代码。
>
> 关联：Phase 1 已先交付 `StopIngressIfActive`（缺口 A 的直接修复，见文末「与 Phase 1 的关系」）。

---

## 1. 背景与问题

认证失效后的清理由多个子系统各自响应：清本地凭证、UI 登出、停代理、agent 下线。**当前没有一个统一的"身份状态权威"**，每个子系统持有自己的状态并各自凭自觉响应，导致一类一致性 bug。

### 1.1 触发事件的真实缺陷（来自 `~/.aliang/logs/aliang_core.log`）

- **缺口 A（已修，Phase 1）**：`handleAuthExpired` 用 `runService.IsRunning()` 内存标志位门控停代理。该标志在模式切换 / daemon 重启 / 启动回滚后会与真实监听器（56432）脱节。脱节时认证失效**不停代理** → 56432 继续拿死 token 转发 → 云端回 401。日志铁证：同一触发（invalid refresh token）有时停有时不停。
- **缺口 B（待修）**：`processor/auth/errors.go:142` `RecoverOrExpireLocalSession` 在"云端拒认 access_token（agent server rejected user authorization）但 refresh 自愈成功"时**主动放弃 wipe、不停代理**。但云端仍在拒认那个 access_token → 代理继续转发 → 401 刷屏。日志：`session retained after recovery refresh ... no auth_expired`。
- **缺口 C（待修）**：`authExpirationHandler` 是**单回调**（`errors.go:19-23`，单槽覆盖写），且 `notifyAuthExpirationHandler()` 只调本进程。跨进程（core-root / user-agent）的扇出靠 `RequestUserAgentDisableAfterLogout` / `EnsureConnection` / `SyncAfterAuth` 临时桥接，**没有统一事件通道**。

### 1.2 设计层面的根因

> **"云端拒认 token（401）" 与 "停本地代理 / 下线 agent" 没有直接耦合。** 停代理只绑定在「本地会话 wipe」这一条线上；而 wipe 又被可脱节的标志位门控、且在"自愈成功"时被跳过。子系统状态（`currentUserInfo` / `fetchSuccess` / `isRunning` / `agent_state`）各自独立维护、互相不派生。

---

## 2. 目标与非目标

**目标**
1. 身份状态**单一权威**，子系统状态由它派生 / 被它通知，不再各自为政。
2. 所有"身份感知点"（定时刷新、业务 401、agent 服务端拒认、主动登出）统一汇入。
3. 状态转移**幂等**，重复信号不产生重复副作用。
4. 区分**软过期**（可自愈）与**硬失效**（不可恢复），分别扇出，既修缺口 B 又不回归 v1.0.95 的"登录着却掉线"。
5. 扇出**可扩展**：加消费者不改核心（单回调 → 订阅列表）。
6. 跨进程统一事件通道（收敛现有临时桥接）。

**非目标**
- 不改变认证数据存储（仍是 `~/.aliang/aliang.data` 单文件，内存优先）。
- 不改后端（official-website）契约；本设计仅限 agent 客户端内部。
- UI 侧身份事件本期升级为 SSE 推送（见 §10 Step 4）；run/agent/startup 状态仍走现有 `/api/startup/status` 轮询（独立关注点，后续再统一）。

---

## 3. 状态机

### 3.1 状态图

```
                         login / activate ok
            ┌────────────────────────────────────────┐
            │                                        ▼
     ┌──────────────┐  access 401 / 云端拒认   ┌─────────────┐
     │Unauthenticated│ ──────────────────────► │ SoftExpired │
     └──────┬───────┘                          └──┬───┬──────┘
            ▲  cleanup                            │   │ refresh ok
            │                                     │   ▼
            │                            ┌────────┘   │
            │  logout / refresh死 / 吊销 │  timeout   │
            │              ┌─────────────▼─┐         │
            │              │  HardInvalid  │ ◄───────┘
            │              └──────┬────────┘
            └──────────────────────┘
```

### 3.2 状态语义与各子系统行为

| 状态 | 含义 | 本地凭证 | 代理（56432） | agent / device_token | UI |
|---|---|---|---|---|---|
| **Unauthenticated** | 无身份（初始 / 清理后） | 无 | 停 | 视来源：logout→已 drop；expiry→保留 | 登录视图 |
| **Active** | 有效 token，已校验 | 有 | 可运行（用户启动后） | 保留，WS 在线 | 已登录 |
| **SoftExpired** | access 被云端拒，refresh 仍活（可自愈） | 保留 | **暂停转发**（不拿死 token 刷 401） | 保留，WS 掉等恢复 | 已登录（降级提示） |
| **HardInvalid** | refresh 死 / 主动登出 / 吊销（不可恢复） | 清 | 停 | logout→drop；expiry→仅断 WS 保留 token | 登录视图 |

> **`SoftExpired` 是本设计的核心新增态** —— 缺口 B 的结构性解：云端拒认时不再闷头转发刷 401，而是降级、激进自愈，超时才升级硬失效。

### 3.3 转移表

| from → to | 触发 | 生产者 |
|---|---|---|
| `Unauthenticated → Active` | 登录 / 扫码激活成功 | `Login` / `ActivateWithTokens` |
| `Active → SoftExpired` | access_token 401 / agent 服务端拒认 | `verifyCurrentSessionWithAuthMe` / `agent_service.go:1557` |
| `SoftExpired → Active` | refresh 成功（自愈） | `RefreshSession` 成功分支 |
| `SoftExpired → HardInvalid` | refresh 失败（refresh_token 被拒）/ 超时 | `RefreshSession` ErrRefreshTokenInvalid / SoftExpired 定时器 |
| `Active → HardInvalid` | refresh_token 直接被拒（无 SoftExpired 中转） | `RefreshSession` ErrRefreshTokenInvalid |
| `* → HardInvalid` | 主动登出 | `LogoutUser` |
| `HardInvalid → Unauthenticated` | 清理完成（幂等收口） | 权威内部 |

转移函数**幂等**：已在目标态则 no-op，不重复扇出。

---

## 4. 权威 API 与生产者

权威定义在 `processor/auth`（与现有认证逻辑同包），为持有 auth 的进程提供进程内单例。

```go
// processor/auth/session_authority.go（新建）

type SessionState int
const (
    StateUnauthenticated SessionState = iota
    StateActive
    StateSoftExpired
    StateHardInvalid
)

// SessionReason 结构化来源，供订阅者按来源分叉（如 device_token 策略，见 §7）。
type SessionReason string

const (
    ReasonLogin             SessionReason = "login"               // 登录/激活成功 →Active
    ReasonRefreshed         SessionReason = "refreshed"           // 刷新成功 →Active
    ReasonAccessRejected    SessionReason = "access_rejected"     // 云端拒认 access →SoftExpired
    ReasonRefreshInvalid    SessionReason = "refresh_invalid"     // refresh_token 永久被拒 →HardInvalid（即时）
    ReasonSoftExpiryTimeout SessionReason = "soft_expiry_timeout" // SoftExpired 30s 未恢复 →HardInvalid
    ReasonRevoked           SessionReason = "revoked"             // 云端吊销 →HardInvalid（保留 device_token）
    ReasonLogout            SessionReason = "logout"              // 主动登出 →HardInvalid（drop device_token）
)

// SessionEvent 是权威向订阅者广播的转移事件。
type SessionEvent struct {
    From   SessionState
    To     SessionState
    Reason SessionReason
    User   *UserInfo // Active 转移时携带最新身份快照（可空）
}

type SessionListener func(SessionEvent)

type SessionAuthority struct {
    mu        sync.RWMutex
    state     SessionState
    listeners []SessionListener
}

// 生产者入口（幂等）：
func (a *SessionAuthority) NotifyLoggedIn(*UserInfo)       // →Active
func (a *SessionAuthority) NotifyRefreshed(*UserInfo)      // →Active
func (a *SessionAuthority) NotifyAccessRejected(reason)    // →SoftExpired（内部先试一次自愈）
func (a *SessionAuthority) NotifyRefreshFailed(permanent bool) // permanent→HardInvalid，否则→SoftExpired
func (a *SessionAuthority) NotifyLoggedOut()               // →HardInvalid(logout)

// 消费者订阅：
func (a *SessionAuthority) Subscribe(SessionListener)
func (a *SessionAuthority) State() SessionState
```

**生产者接入点**（把现有散落调用改成 `Notify*`）：

| 现有调用点 | 改为 |
|---|---|
| `clearLocalSessionAfterExpiration`（`errors.go:168`） | `authority.NotifyRefreshFailed(true)` |
| `RecoverOrExpireLocalSession` 自愈成功分支（`errors.go:147`） | `authority.NotifyAccessRejected(...)` → 内部试 refresh |
| `RecoverOrExpireLocalSession` 自愈失败分支 | `authority.NotifyRefreshFailed(true)` |
| `RefreshSession` 成功（`token_activate.go:338`） | `authority.NotifyRefreshed(userInfo)` |
| `finalizeAuthenticatedSession`（登录/激活收尾） | `authority.NotifyLoggedIn(userInfo)` |
| `AuthService.LogoutUser`（`auth_service.go:295`） | `authority.NotifyLoggedOut()` |

---

## 5. 订阅者与扇出

`processor/auth/errors.go` 现有单回调 `authExpirationHandler func()`（单槽）升级为 `[]SessionListener`。每个子系统注册自己的监听器，核心不再 fat-handler：

| 订阅者 | 注册位置 | 订阅的转移 | 动作 |
|---|---|---|---|
| `localDataCleaner` | `processor/auth` | `→HardInvalid` | `DeleteUserInfo()` |
| `startupStateSyncer` | `app/http/services` | `→HardInvalid` / `→Active` | `fetch_success=false/true`、`status=UNCONFIGURED/READY` |
| `proxyController` | `app/http/services` | `→HardInvalid` | `runService.StopIngressIfActive()`（Phase 1） |
| | | `→SoftExpired` | **暂停转发**（新，见 §6） |
| | | `→Active`（且用户已启动） | 恢复转发 |
| `agentController` | `app/http/services` | `→HardInvalid(logout)` | `RequestUserAgentDisableAfterLogout("logout")`（drop device_token） |
| | | `→HardInvalid(expiry)` | 保留 device_token，仅断 WS（v1.0.95 决策保留） |
| | | `→Active` | `PushSessionRefresh` + `RequestUserAgentEnsureConnection` |
| `notifier` | `app/http/services` | `→HardInvalid` | 桌面通知「认证已过期，请重新登录」 |

> 这样 `handleAuthExpired` / `handleAuthRefreshed` 的函数体被拆解进各订阅者，核心只负责"发事件"，加消费者只需新增一个 listener。

---

## 6. SoftExpired 的代理语义（**已定：方案 A 完全暂停**）

进入 `SoftExpired` 时，`proxyController` 执行**方案 A**：停止 `Accept` 新连接，在途连接放完；回到 `Active` 后恢复 `Accept`。彻底不再拿死 token 转发 → 闭合缺口 B。

### 6.1 超时 = 30s 的取舍（已分析）

- 单次 refresh 的 `apiTimeout = 10s`（`processor/auth/token_activate.go:20`）。后端 arbiter 命中缓存 <1s，需轮换时一次 upstream 往返。
- **区分两类失败**（`classifyRefreshSessionFailure` 已能识别）：
  - **永久**（`ErrRefreshTokenInvalid`，`ReasonRefreshInvalid`）：**立刻升级 HardInvalid，不烧 30s**。
  - **瞬时**（网络 / 5xx / 未识别 401）：30s 内带退避重试。
- **进入 SoftExpired 立即触发一次 refresh**（不等 1-min `TokenRefresher` ticker），退避如 `0s / 5s / 15s`，30s 内约 2–3 次。
- **常见情形**（瞬时 blip 首次重试即恢复）：暂停 <1s，用户几乎无感。**30s 只是持续失败时的最坏天花板**，而非每次都要等满。
- 结论：**30s 合理**，但必须配「永久即时升级」+「立即刷新 + 退避」。可配 env（如 `ALIANG_SESSION_SOFT_EXPIRY_TIMEOUT`，对齐 `ALIANG_AI_APPROVAL_TIMEOUT` 风格），默认 30s。若想更保守可 20s（仍够 2 次），但 30s 对瞬时网络更宽裕 → **推荐 30s**。

---

## 7. device_token 策略（**已定**）

v1.0.95 的正确决策：**会话过期 ≠ 设备注销**。本设计显式建模之，集中裁决（按结构化 `SessionReason` 分叉）：

| 转移 / Reason | device_token | 理由 |
|---|---|---|
| `→HardInvalid` & `ReasonLogout` | **drop** | 用户主动登出，干净注销 |
| `→HardInvalid` & `ReasonRefreshInvalid` / `ReasonSoftExpiryTimeout` | **保留** | 重新登录秒恢复，避免"登录着却掉线"回归 |
| `→HardInvalid` & `ReasonRevoked`（云端吊销） | **保留** | 同上；后端将来若给"吊销=踢设备"明确信号再单列 |
| `→SoftExpired` / `→Active` | 保留 | 仅断/重连 WS |

---

## 8. 跨进程事件通道

权威住在**持有 auth 的进程**（**已确认**）：macOS = tray/dashboard（用户态）；Linux = core（root）。跨用户（root core 读不到用户 `~/.aliang`）为已知约束，**接受**。其它进程（user-agent 子进程）订阅统一 IPC。

把现有三个临时桥接收敛为**一条** `SessionEvent` 流：

```
现有（散落）：                       收敛后：
  RequestUserAgentDisableAfterLogout   ┐
  RequestUserAgentEnsureConnection     ├─► POST /api/agent/session-event
  RequestUserAgentSyncAfterAuth        ┘    {type, from, to, reason}
```

user-agent 子进程侧注册一个 `SessionListener`，收到事件按 `To` 态执行（断 WS / 重连 / drop device_token）。这样"哪个进程感知到身份变化"不再影响扇出完整性（消除缺口 C）。

---

## 9. 与 Phase 1 的关系

Phase 1 已交付 `runService.StopIngressIfActive()`（`app/http/services/run.go`）：按真实监听器状态停代理，不受 `isRunning` 脱节影响。`handleAuthExpired` 已改用它。

本设计 §5 的 `proxyController` 订阅 `→HardInvalid` 时**直接复用** `StopIngressIfActive`——Phase 1 是它的前置依赖与兜底，无需重做。

---

## 10. 迁移路径（每步可独立测试、行为保持）

- [x] **Step 0**：`StopIngressIfActive` 修复缺口 A（`app/http/services/run.go`）。
- [x] **Step 1：薄封装权威**。`processor/auth/session_authority.go` 落地状态机 + `Notify*` + `Subscribe`（幂等转移）。生产点 additive 接入：登录/刷新成功 → `NotifyLoggedIn/NotifyRefreshed`；wipe → `NotifyRefreshFailed(true,...)`；登出 → `NotifyLoggedOut`。
- [x] **Step 2：单回调 → 多订阅扇出**。移除 `authExpirationHandler` 单回调；`onSessionEvent` 订阅者按 `To` 态扇出（SoftExpired/Active/HardInvalid）。`notifyAuthSuccessHandler`（每刷新 JWT 推送）保留——它是 per-refresh 信号，非转移。
- [x] **Step 3：引入 SoftExpired（闭合缺口 B）**。`processor/auth/soft_expiry.go` 恢复协调器（立即刷新 + `0s/5s/15s` 退避，30s 超时，永久即时升级，env `ALIANG_SESSION_SOFT_EXPIRY_TIMEOUT` 可配，单飞）。`RecoverOrExpireLocalSession` → `NotifyAccessRejected` + 直接启动协调器；`onSessionEvent(SoftExpired)` 暂停代理（方案 A，`StopIngressIfActive`），`→Active` 恢复。
- [x] **Step 4：前端 SSE 身份事件推送**。`GET /api/session/events`（`session_event_broker.go`，SSE，连上先推快照）；前端 `stores/auth.js` `connectSessionEvents()` 订阅，转移即时（≤5s→即时）。
- [x] **Step 5：跨进程收敛**。`POST /api/agent/session-event`（agent server）+ dashboard `ForwardSessionEventToUserAgent` 订阅者（best-effort，user-agent runtime 内自跳过）。agent 侧 `ApplySessionEvent` 按结构化 `Reason` 分叉（Active→reconnect、logout→disable、expiry→保留 device_token）。

### 10.1 实现期的一个加固（force-fire）

落地 Step 2 时发现：teardown 若只在 `→HardInvalid` **转移**时触发，则"重复 wipe"或"跨测试单例残留 HardInvalid"时不会重跑清理（代理可能没停）。加固：`NotifyRefreshFailed(permanent=true)` **强制触发** HardInvalid 事件（即使已是 HardInvalid），让幂等清理（`StopIngressIfActive` 等）每次 wipe 都重跑；重复桌面通知被 `StopIngressIfActive` 的返回值天然抑制（代理已停则不通知）。这是对 §5 "转移触发"模型的细化：**转移类事件**（broker/forwarder/SoftExpired/Active）保持转移触发；**显式 teardown（wipe）**force-fire。

### 10.2 仍未收敛（后续）

`RequestUserAgentSyncAfterAuth`（每刷新推 JWT + sync）与 `handleAuthRefreshed` 的 per-refresh 重连保留——它们是 **per-refresh 信号**而非转移，不经过权威。完整退役需要把"每刷新"也建模为权威信号（signal listener，区别于 transition listener），属独立后续项，不在本期。当前 `POST /api/agent/session-event` 已承载所有**转移**的跨进程扇出。

每步独立 PR、独立测试、独立回滚。

---

## 11. 已定决策（评审结论）

1. **SoftExpired 代理语义**：方案 A 完全暂停（§6）。接受自愈期（通常 <1s，最坏 30s）新连接短暂不可用。
2. **SoftExpired 超时**：默认 **30s**，可配 env（`ALIANG_SESSION_SOFT_EXPIRY_TIMEOUT`）；永久拒绝即时升级，瞬时失败带退避重试（§6.1）。
3. **device_token 在云端吊销时**：**保留**（`ReasonRevoked`，与 expiry 同），仅 `ReasonLogout` 才 drop（§7）。
4. **权威进程归属**：macOS=tray/dashboard、Linux=core，**确认**；跨用户读不到 `~/.aliang` 约束**接受**（§8）。
5. **前端身份同步**：升级为 **SSE 事件推送**（`/api/session/events`，§10 Step 4），身份转移即时反映；run/agent 状态本轮仍走轮询。理由：本地 poll 负载可忽略，真正收益是**延迟（≤5s → 即时）**与**去耦合**（取消 权威→`fetch_success`→poll→UI 的迂回）。
6. **`HardInvalid` 来源字段**：**结构化** `SessionReason`（§4），订阅者按来源分叉（device_token 策略等）。

---

## 12. 附录：关键代码坐标

| 关注点 | 位置 |
|---|---|
| 现有汇流点 | `processor/auth/errors.go:168` `clearLocalSessionAfterExpiration` |
| 自愈保留（缺口 B） | `processor/auth/errors.go:142` `RecoverOrExpireLocalSession` |
| 单回调 handler | `processor/auth/errors.go:19-23,193,219` |
| 刷新成功/失败 | `processor/auth/token_activate.go:281,338` |
| 本地凭证存取 | `processor/auth/user_info.go:39,112,160` |
| agent 服务端拒认入口 | `app/http/services/agent_service.go:1557` |
| 设备 disable / 跨进程桥接 | `app/http/services/agent_service.go:612,634,654` |
| 代理停（Phase 1 修复） | `app/http/services/run.go` `StopIngressIfActive` |
| 真实监听器探针 | `inbound/http/server.go:118` `IsHttpRunning` |
| UI 状态 | `processor/runtime/startup_state.go:24` `fetchSuccess` |
