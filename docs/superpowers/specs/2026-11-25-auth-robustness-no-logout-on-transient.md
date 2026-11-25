# 认证链路鲁棒性增强设计（服务端异常不触发登出）

- 日期：2026-11-25
- 范围：alianggate 本地网关/仪表盘的认证身份链路（Go 后端 processor/auth + app/http + 前端 website）
- 目标：服务端网络抖动/5xx/超时/传输错误不得被当做登出触发因子；只有服务端**明确**的身份失效状态码（401 且携带明确拒绝语义，或 403/吊销）才落 HardInvalid/登出。自动刷新轮换身份继续运行，错过窗口重试而非团灭。

## 1. 问题陈述

当前链路把「任何 HTTP 401 / 传输失败 / profile 401」都等价为「身份已失效 → 清本地会话 → HardInvalid → 登出」。在网络不稳定的环境下，一次抖动就团灭所有设备的本地会话，且恢复需要重新登录。具体触发点：

1. `processor/auth/errors.go:classifyRefreshSessionFailure`：任何 401（含无法解析的 body）直接返回 `ErrRefreshTokenInvalid`，进而 `clearLocalSessionAfterInvalidRefreshToken` → `clearLocalSessionAfterExpiration` → `NotifyRefreshFailed(true)` → HardInvalid → 清 sqlite。
2. `processor/auth/errors.go:classifyAccessTokenFailure`：任何 401（含未知 JSON、空 body、不可解析 body）兜底返回 `ErrSessionExpired`，`verifyCurrentSessionWithAuthMe` → `clearLocalSessionAfterExpiredAccessToken` → `RecoverOrExpireLocalSession` → SoftExpired；失败时升级 HardInvalid。
3. `processor/auth/token_activate.go:RefreshSession`：profile 端点 401 → `clearLocalSessionAfterExpiration`（无恢复）。
4. `processor/auth/token_activate.go:RefreshSession`：transport 错误（`client.Do` 失败）当前只返回包装错误，但上层 `refreshUserInfo`/`RestoreSession`/`RecoverOrExpireLocalSession` 路径会把 SoftExpired 持续重试；问题在于一旦 refresh endpoint 返回 401，立刻 HardInvalid。
5. 前端 `app/website/src/services/authFailure.js:isAuthenticationFailure`：`responseStatus === 401` 即视为身份失效，调用 `syncUnauthenticatedAuthState` → `restoreAuthSession`，再配合后端 HardInvalid 快照把 UI 翻成未登录。
6. agent 侧：`doCallAgentServer` 仅把 `401` 当 `agentUserAuthRejectedError`，但 transport 错误/5xx 走 `classifyRegisterFailure` 已正确保留注册态；然而 `recoverOrExpireAfterRegisterAuthRejection` 在 401 时无条件触发 `RecoverOrExpireLocalSession`，把一次 401（可能是网关抖动返回的 401）升级为 SoftExpired→HardInvalid。

## 2. 设计原则

- **明确性原则**：登出/HardInvalid 只能由服务端**明确**的身份失效信号触发。明确信号**逐 classifier 枚举**（前端/后端共用同一份清单，避免实现歧义）：
  - **refresh 端点 401 明确拒绝短语**（`classifyRefreshSessionFailure` 匹配，body 的 `reason`/`code`/`message`/`error` 字段，大小写不敏感）：
    - `reason` 或 `code` == `REFRESH_TOKEN_INVALID`
    - `message`/`error` 含：`invalid refresh token`、`refresh token is no longer valid`、`refresh token expired`、`refresh token has been revoked`、`local session is no longer valid`
    - 403 → `ErrRefreshTokenInvalid`（权限撤销，refresh 端点 403 视为拒绝）
  - **access 端点 401 明确拒绝语义**（`classifyAccessTokenFailure` 匹配，body 的 `code`/`reason`/`message` 字段）：
    - `code` 或 `reason` == `TOKEN_REVOKED` / `ACCESS_TOKEN_INVALID` / `TOKEN_EXPIRED`
    - `message` 含：`token has been revoked`、`invalid access token`、`token expired`、`expired token`、`invalid authentication`
    - 403 **不**在 access classifier 处理（access 端点 403 视为瞬时/无权限，保留会话重试；权限撤销通过 refresh 端点 403 或 snapshot `hard_invalid` 表达）
  - **snapshot `outcome == session_invalid`**（`hard_invalid`/`unauthenticated` state，由 `BuildSessionSnapshotPayload` 产出，前端 reducer 据此清身份）——这是前端「明确失效」的唯一权威来源。
- **传输/网络/5xx/408/429/425/空 body/不可解析 body/未知 JSON 401**：视为瞬时故障，进入 SoftExpired 恢复链（退避重试），**不**升级 HardInvalid，**不**清 sqlite，**不**团灭。
- **恢复链**：SoftExpired 状态下 `runSoftExpiryRecovery` 已实现指数退避重试（0/5s/15s，生产无限重试），仅在 refresh 真正被永久拒绝时升级 HardInvalid。保持该语义，仅把「升级 HardInvalid 的门槛」收严。
- **前端**：`isAuthenticationFailure` 不再把裸 401 当身份失效；改为只在 401 且 body 命中上述 access 拒绝语义清单，或 snapshot state 为 `hard_invalid`/`unauthenticated` 时才触发 `syncUnauthenticatedAuthState`。传输失败/超时/5xx 不触发。
- **agent 侧**：401 仍走恢复链（SoftExpired），但 transport/5xx 不得触发 `RecoverOrExpireLocalSession`（已有 `classifyRegisterFailure` 保留注册态，保持）；仅当服务端明确拒绝才升级。

## 3. 改动点（按层）

### 3.1 后端 classifier 收严（processor/auth/errors.go）

- `classifyRefreshSessionFailure`：
  - 非 401（含 5xx/408/429/transport nil）→ `nil`（已是）。
  - 401 但 body 不可解析/空/未知 JSON → **不再**默认 `ErrRefreshTokenInvalid`；改为返回 `nil`（视为瞬时/不确定），让上层进入 SoftExpired 重试而非 HardInvalid。仅当 body 命中 §2 refresh 明确拒绝短语清单时返回 `ErrRefreshTokenInvalid`。
  - `refreshRejectionPhrases` 追加 `local session is no longer valid`（此前仅靠 fallback 命中），并保留 `reason`/`code` == `REFRESH_TOKEN_INVALID` 匹配。
  - 403 → `ErrRefreshTokenInvalid`（权限撤销，refresh 端点 403 视为明确拒绝）。
- `classifyAccessTokenFailure`：
  - 非 401 → `nil`（已是）。
  - 401 但 body 不可解析/空/未知 → **不再**默认 `ErrSessionExpired`；返回 `nil`（瞬时），上层保留会话并重试。
  - 仅命中 §2 access 明确拒绝语义清单时返回 `ErrSessionExpired`。
  - 403 **不**在本 classifier 处理（返回 `nil`），保留会话重试；权限撤销通过 refresh 端点 403 或 snapshot `hard_invalid` 表达。

### 3.2 RefreshSession 路径（processor/auth/token_activate.go）

- refresh endpoint 401：仅在 `classifyRefreshSessionFailure` 返回非 nil（即命中 §2 refresh 明确拒绝清单）时调用 `clearLocalSessionAfterInvalidRefreshToken`（HardInvalid）；否则保持会话，返回**非终态**包装错误 `fmt.Errorf("refresh session inconclusive: %w", ErrSessionRecovering)`（不得是 `ErrSessionExpired`/`ErrRefreshTokenInvalid`，否则 `RestoreSession` 的 `isTerminalSessionError` 会误判终态中止 SoftExpired 恢复链），由 SoftExpired 恢复链重试。
- transport 错误（`client.Do` 失败）：返回包装错误，**不**清会话，**不**升级。上层 SoftExpired 重试。
- profile 端点 401：当前 `clearLocalSessionAfterExpiration` 无条件 wipe。改为：仅当 `classifyAccessTokenFailure` 返回 `ErrSessionExpired`（明确拒绝）时 wipe；否则保留会话并返回 `fmt.Errorf("profile sync after refresh inconclusive: %w", ErrSessionRecovering)`（非终态），由 SoftExpired 重试。传输/5xx 不 wipe。

### 3.3 clearLocalSessionAfterExpiredAccessToken / verifyCurrentSessionWithAuthMe

- `verifyCurrentSessionWithAuthMe`：`/auth/me` 401 时先 refresh 探活（已有 `recoverSessionAfter401` 模式）；refresh 成功 → 保留会话；refresh 明确拒绝（`classifyRefreshSessionFailure` 命中清单）→ `clearLocalSessionAfterInvalidRefreshToken`（立即 HardInvalid）；refresh 传输失败/不确定 → 保留会话重试。当前 `clearLocalSessionAfterExpiredAccessToken` → `RecoverOrExpireLocalSession` 已是 SoftExpired；保持——它本身不 wipe，`runSoftExpiryRecovery` 仅在 refresh 永久拒绝时升级 HardInvalid。本点仅澄清：`verifyCurrentSessionWithAuthMe` 里「明确拒绝」指 refresh 探活返回明确拒绝，而非 access 401 本身；access 401 一律先走 SoftExpired 探活。

### 3.4 前端 authFailure.js

- `isAuthenticationFailure`：移除「裸 401 即失效」。改为：
  - `responseStatus === 401` 且 body 命中 §2 **access** 拒绝语义清单（status unauthenticated / error session_expired|token_expired / code token_expired / message 含 local session is no longer valid / invalid authentication / token has been revoked / invalid access token / token expired）→ true。
  - `responseStatus === 403` → false（403 不在前端触发失效；由后端 refresh 端点 403 或 snapshot 表达）。
  - snapshot `outcome === session_invalid`（reducer 层，`hard_invalid`/`unauthenticated`）→ 清身份（已是）。
  - 传输失败（fetch throw）不调用本函数（调用方 catch），不触发。
- `handleAuthenticationFailure` 调用点保持，但 5xx/超时不会进入（response.ok false 但 status 非 401 → false）。
- 测试 `authFailure.test.js` 首个「recognizes HTTP 401」用例的 envelope 必须带明确拒绝 message（裸 401 不再合格）。

### 3.5 前端 auth store

- `applySessionReadFailure`：transport 失败时 `reduceSessionReadFailure` 已保留 isAuthenticated/user（测试钉死）。保持。仅当 snapshot 明确 `hard_invalid`/`unauthenticated` 才清身份（reducer 已如此）。
- `restoreAuthSession` catch：transport 失败时 `applySessionReadFailure` 设置 status server_error/transport_unavailable，isAuthenticated 保留。保持。

### 3.6 agent 侧

- `recoverOrExpireAfterRegisterAuthRejection`：仅在 401 明确拒绝时调用 `RecoverOrExpireLocalSession`；transport/5xx 不调用（`classifyRegisterFailure` 已返回 true 保留注册态）。保持现有 `isAuthRejectedErr` 门（401 typed）。但需确保 `agentUserAuthRejectedError` 仅在服务端明确 401 时产生（已是：`resp.StatusCode == 401`）。保持。
- `handleHandshakeUnauthorized`：401 时走 `remoteWSAuthRecovery`（SoftExpired 恢复）。保持，但 transport 错误不触发（已是：仅 `resp.StatusCode == 401`）。

### 3.7 测试

- 更新 `errors_test.go`：不可解析/空/未知 JSON 的 401 不再返回 ErrRefreshTokenInvalid/ErrSessionExpired（返回 nil）。新增用例钉死。现有「unknown 401 is terminal」「invalid JSON 401 is terminal」「local session is no longer valid」用例更新为期望 nil（local session 短语改为显式加入 `refreshRejectionPhrases` 后仍 terminal，单独钉死）。
- `user_center_test.go:215` 的 `clearLocalSessionAfterExpiredAccessToken()` 预期更新（保持 SoftExpired 语义）。
- `token_activate_test.go` profile-reject 用例：profile 401 明确拒绝仍 HardInvalid；新增 profile 401 不确定不 wipe 用例。
- 前端 `authFailure.test.js` 裸 401 不触发（更新首个用例 envelope）。

## 4. 不做的事

- 不加「永不登出」UI 开关（用户已选 A）。
- 不改 24h 滑动会话寿命与客户端 2h lead 窗口。
- 不改 `RevokeAllSessions` 服务端 rotation 逻辑（服务端明确拒绝时的团灭是安全边界，保留）。
- 不改 agent 注册态保留逻辑（已正确）。

## 5. 验收

- 断网/5xx/超时场景下本地会话保持，进入 SoftExpired 重试，不登出、不清 sqlite。
- 服务端明确 401（拒绝语义）时仍 HardInvalid + 清会话 + 登出。
- 现有 auth 测试全绿；新增「传输/未知 401 不登出」用例全绿。
- 前端：feature API 5xx/超时不再把 UI 翻成未登录。
