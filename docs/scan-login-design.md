# 扫码登录（Scan-to-Login）— 客户端接入设计

## 目标

alianggate（桌面网关客户端）支持扫码登录，且**扫码登录后的效果与账号密码登录完全一致**
（同一用户态、同一刷新机制、同一 AI 代理通行令牌），用户无需在客户端手输账号密码。

## 核心结论：令牌模型与「等价性」

alianggate 密码登录从 official-website `/api/v1/auth/login` 拿到的并非 sub2api 原始令牌，而是
official-website **改写后的令牌对**（见 `backend/internal/httpapi/routes.go`
`injectLocalSessionIntoAuthResponse`）：

- `access_token` = 本地会话令牌 `st_`（official-voice 用 `st_` 覆盖了 sub2api access_token）；
- `refresh_token` = 该用户的 **sub2api refresh_token**（原样保留）。

alianggate 把 `st_` 同时用作：① `/api/v1/*` 的 `Authorization: Bearer`；② AI 代理的
`Authorization-Inner`（见 `processor/auth/user.go:GetCurrentAuthorizationHeader`）。official-website
的 `sub2api.Gateway.ReplaceAuthHeader` 会把 `st_`→sub2api 再转发。

因此**扫码登录要达到「与密码登录等价」，只需让扫码 also 交付同一个令牌对（`st_` + sub2api
refresh_token）**。两者落地后 alianggate 的本地态逐字段相同，下游（`/me`、刷新器、
Authorization-Inner）行为完全一致。

### `st_` 的刷新（纠正一个常见误解）

`st_` 本身不轮换，但它**可被续命**：alianggate 刷新器用 sub2api refresh_token 打
`/api/v1/auth/refresh`，official-website 在该路径上除了轮换 sub2api 令牌（`captureSub2APITokens`），
**还会 `extendLocalSessionExpiry` 把本地 `st_` 的 expires_at 续 +24h**（`routes.go:1992`）。
所以只要 alianggate 持有有效 sub2api refresh_token，`st_` 就一直活着——**无需为扫码新建任何
official-website 续期接口**。

## 跨仓库改动

### 1. official-website（分支 `feature/qr-scan-login`）

让扫码 `Status` 的 `authorized` 响应**在 `st_` 之外一并返回 sub2api refresh_token**：

- `internal/sub2apiauth/service.go`：新增 `GetRefreshTokenByUserID`（列可空，缺省归
  `ErrTokenNotFound`）。
- `internal/sub2api/gateway.go`：新增 `UpstreamRefreshToken(ctx, userID) (string, bool, error)`，
  nil-safe，结构化满足下面的 resolver。
- `internal/scanlogin/service.go`：新增可选依赖 `RefreshTokenResolver`（接口，由 `*sub2api.Gateway`
  结构化实现）；`StatusResult` 增 `RefreshToken` 字段；`Status()` 在 `authorized` 时调用 resolver
  填充，best-effort（未注入/无 upstream 令牌/取数失败均静默省略，不阻断状态机）。
- `internal/httpapi/routes.go`：把 sub2api Gateway 抽成局部变量，同时作为 passthrough 入口与
  scanlogin 的 `RefreshTokenResolver` 注入。
- 测试：`scanlogin` 三个用例（暴露/缺省/未注入 resolver）+ `sub2apiauth` `GetRefreshTokenByUserID`。

> 安全：sub2api refresh_token 是长效凭据，经 status 短轮询通道下发，但只有持有 `device_code`
> （PC 密钥，从不进二维码）的 PC 能取到，且 scan_codes 行 ~5min TTL——与密码登录在 HTTPS 上直接
> 回传 refresh_token 的安全等级相当。

### 2. alianggate（分支 `codex/user-agent-mode`）

**后端（Go）**

- `processor/config/{types,url_builder}.go`：新增 `GetAuthScanInitURL/GetAuthScanStatusURL`
  （`APIBaseURL + /auth/scan/{init,status}`，注意 official-website 扫码路由不带 `/api/v1` 前缀）。
- `processor/auth/scan_login.go`（新）：`ScanInit()` / `ScanStatus(deviceCode)` 上游调用 + 结果结构体 +
  `ErrScanCodeNotFound`。
- `processor/auth/token_activate.go`：抽出 `finalizeAuthenticatedSession(...)` 共用收尾；
  新增 `ActivateWithTokens(st_, refresh_token)`——与 `LoginWithPassword` 走同一条收尾，故扫码后
  本地态与密码登录等价。`ExpiresIn` 取 `scanAccessTokenTTLSeconds=3600` 仅驱动刷新节奏。
- `app/http/services/auth_service.go`：`ScanInit/ScanStatus/ActivateScanLogin`（后者返回与 `Login`
  同构：`data=UserInfoResponse` + `agent_sync`，reason=`scan_login`）。
- `app/http/handlers/auth_handler.go`：`HandleScanInit/HandleScanStatus/HandleScanActivate`。
- `app/http/routes/routes.go`：注册 `POST /api/auth/scan/init`、`GET /api/auth/scan/status`、
  `POST /api/auth/scan/activate`。
- `app/http/middleware/startup_status.go` + `app/http/handlers/startup_handler.go`：三条 scan 路由
  登录前可用 + 帮助文案。
- `app/http/models/auth.go`：`ScanActivateRequest{session_token, refresh_token}`。

**前端（Vue）**

- `services/authApi.js`：`scanInit/scanStatus/activateScanLogin`。
- `stores/auth.js`：`completeScanLogin({session_token, refresh_token})`（与 `loginWithPassword` 同构
  应用认证态；成功后既有 `watch(isAuthenticated)` 触发数据加载）。
- `components/settings/ScanLoginPanel.vue`（新）：初始化→渲染二维码（`qrcode` 库）→按 interval 短轮询
  →`authorized` 调激活→`expired/denied` 自动重生码；卸载 `stopped` 守卫 + 清理定时器。
- `components/settings/UserInfoSettings.vue`：登录区加「密码登录 / 扫码登录」Tab，扫码态渲染
  `ScanLoginPanel`。
- `i18n/{zh,en}.js`：`scan_*` 文案。
- 依赖：新增 `qrcode@1.5.4`。

## 端到端流程

```
PC(alianggate)             official-website              App(已登录)
   │  POST /api/auth/scan/init ──► /auth/scan/init
   │ ◄── device_code + qr_payload(scan_code)
   │  渲染二维码(scan_code)
   │                                                        │ 扫码 → /auth/scan/scan(App Bearer)
   │  GET /api/auth/scan/status?device_code ──►            │ → scanned
   │ ◄── status: scanned
   │                                                        │ 确认 → /auth/scan/confirm
   │                                                          · MintSessionForUser → st_
   │                                                          · (本改动) 查 sub2api refresh_token
   │ ◄── status: authorized, session_token=st_, refresh_token=sub2api, user
   │  POST /api/auth/scan/activate {session_token, refresh_token}
   │     └─ ActivateWithTokens → finalizeAuthenticatedSession
   │        · GetUserProfileWithToken(st_) → /api/v1/user/profile(ReplaceAuthHeader st_→sub2api)
   │        · SaveUserInfo(st_, sub2api_refresh)
   │        · startTokenRefresh + READY + agent sync
   │ ◄── data: UserInfoResponse  （与密码登录 /api/auth/login 同构）
```

之后刷新器每 ~1h 用 sub2api refresh_token 打 `/api/v1/auth/refresh`，顺带 `extendLocalSessionExpiry`
续 `st_`——长期挂机不掉线。

## 验证

- 后端：`go build ./cmd/... ./app/... ./processor/...` 通过；`go test ./processor/auth/...` 通过。
- official-website：`go build ./...` + `go test ./internal/scanlogin/... ./internal/sub2apiauth/...
  ./internal/sub2api/... ./internal/httpapi/...`（Scan 用例）均通过。
- 前端：`cd app/website && npm run build` 通过。
- 手测要点：① 扫码 Tab 出二维码；② App 扫码+确认后客户端进入已登录态（与密码登录界面一致）；
③ 重启客户端会话可恢复（`/api/auth/session`）；④ 等待 ~1h 或手动触发刷新确认 `st_` 续命。

## 部署依赖

**official-website 的 status-refresh_token 扩展必须先上线**，否则 alianggate 扫码拿不到
refresh_token（降级：仅 `st_`、不可刷新、24h 失效）。App 端的扫码/确认 UI 由 App 团队对接（后端
`/auth/scan/scan|confirm|deny` 接口在 official-website 既有交付中已就绪）。
