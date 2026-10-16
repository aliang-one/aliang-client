# Upstream API Dependencies

本文档记录 AliangGate 作为客户端调用的所有外部上游 API。

## 概览

| 服务 | Base URL | 认证方式 | 用途 |
|------|----------|----------|------|
| Aliang API | `{core.api_server}` | Bearer Token / mTLS | 认证、用户、订阅、Key、兑换 |
| Software Update | `https://www.aliang.one` | 无 | 版本检查 |
| GeoIP Database | `https://aliang-1305838434.cos.ap-nanjing.myqcloud.com` | 无 | GeoLite2 国家数据库下载（env `ALIANG_GEOIP_DOWNLOAD_URL` 可覆盖） |

`core.api_server` 来自配置文件，典型值：`https://api.aliang.one`

---

## 1. Authentication

**Base URL:** `{core.api_server}`
**Source:** `processor/auth/token_activate.go`

### POST /api/v1/auth/login

用户登录认证。

**Request:**

```json
{
  "email": "user@example.com",
  "password": "password123",
  "turnstile_token": "optional-captcha-token"
}
```

**Response:**

```json
{
  "data": {
    "access_token": "eyJhbGci...",
    "refresh_token": "dGhpcyBpcy...",
    "expires_in": 3600,
    "token_type": "Bearer"
  },
  "message": "success"
}
```

### POST /api/v1/auth/refresh

刷新 access token。

**Request:**

```json
{
  "refresh_token": "dGhpcyBpcy..."
}
```

**Response:** 同 login。

### POST /api/v1/auth/logout

登出，需携带 `Authorization: Bearer {access_token}`。

**Request:**

```json
{
  "refresh_token": "dGhpcyBpcy..."
}
```

---

## 2. User Profile

**Base URL:** `{core.api_server}`
**Headers:** `Authorization: Bearer {access_token}`
**Source:** `processor/auth/user_center.go`

### GET /api/v1/user/profile

获取用户资料。

**Response:**

```json
{
  "data": {
    "id": 1,
    "email": "user@example.com",
    "username": "user",
    "role": "user",
    "balance": 10.5,
    "concurrency": 5,
    "status": "active",
    "allowed_groups": [1, 2],
    "created_at": "2024-01-01T00:00:00Z",
    "updated_at": "2024-01-01T00:00:00Z"
  },
  "message": "success"
}
```

### PUT /api/v1/user

更新用户资料。

**Request:**

```json
{
  "username": "new-username"
}
```

**Response:** 同 GET /api/v1/user/profile。

---

## 3. Subscriptions

**Base URL:** `{core.api_server}`
**Headers:** `Authorization: Bearer {access_token}`
**Source:** `processor/auth/user_center.go`

### GET /api/v1/subscriptions/summary

获取订阅用量摘要。

**Response:**

```json
{
  "data": {
    "active_count": 2,
    "total_used_usd": 15.5,
    "subscriptions": []
  },
  "message": "success"
}
```

### GET /api/v1/subscriptions/progress

获取订阅用量进度。

**Response:**

```json
{
  "data": [],
  "message": "success"
}
```

---

## 4. API Keys

**Base URL:** `{core.api_server}`
**Headers:** `Authorization: Bearer {access_token}`
**Source:** `processor/auth/user_center.go`

### GET /api/v1/keys

获取 API Key 列表（分页）。

**Query Parameters:**

| Param | Type | Required | Description |
|-------|------|----------|-------------|
| page | int | No | 页码（默认 1） |
| page_size | int | No | 每页条数（默认 200） |
| timezone | string | No | 时区（默认 Asia/Shanghai） |

**Response:**

```json
{
  "code": 0,
  "message": "success",
  "data": {
    "items": [
      {
        "id": 1,
        "key": "sk-ant-...",
        "name": "My Key",
        "group_id": 1,
        "status": "active",
        "masked": true,
        "secret_available": false,
        "group": {
          "id": 1,
          "name": "Claude Pro",
          "description": "Group desc",
          "platform": "anthropic",
          "rate_multiplier": 1.0,
          "claude_code_only": false,
          "allow_messages_dispatch": true
        }
      }
    ],
    "total": 10,
    "page": 1,
    "page_size": 200
  }
}
```

### GET /api/v1/groups/available

获取可用的 API Key 分组。

**Response:**

```json
{
  "data": [
    {
      "id": 1,
      "name": "Claude Pro",
      "description": "Group desc",
      "platform": "anthropic",
      "rate_multiplier": 1.0,
      "claude_code_only": false,
      "allow_messages_dispatch": true
    }
  ],
  "message": "success"
}
```

---

## 5. Redeem

**Base URL:** `{core.api_server}`
**Headers:** `Authorization: Bearer {access_token}`
**Source:** `processor/auth/user_center.go`

### POST /api/v1/redeem

兑换码。

**Request:**

```json
{
  "code": "redeem-code"
}
```

**Response:**

```json
{
  "data": {},
  "message": "success"
}
```

---

## 6. Token Activation

**Base URL:** `{core.api_server}`
**Headers:** `Authorization: Bearer {access_token}`
**Source:** `processor/auth/token_activate.go`

### POST /api/user/auth/new/activate

激活 token。

**Request:**

```json
{
  "token": "aliang-token-value"
}
```

**Response:** UserInfo 结构体。

---

## 7. Plan Status

**Base URL:** `{core.api_server}`
**Headers:** `Authorization: Bearer {access_token}`
**Source:** `processor/auth/user_center.go`

### GET /api/user/auth/info/plan/info

获取套餐状态。

---

## 8. Inbounds

**Base URL:** `{core.api_server}`
**Headers:** `Authorization: Bearer {access_token}`
**Source:** `processor/auth/user_center.go`

### GET /api/production/prod/sui/user/sui/inbounds

获取入站配置。

---

## 9. AI Chat Completions (mTLS Gateway)

**URL:** `{core.api_server}/v1/chat/completions`
**Protocol:** mTLS (HTTP/2)
**Source:** `app/http/handlers/chat_handler.go`

通过 mTLS 客户端转发到上游 AI Gateway，请求中注入 `Authorization-Inner` 头传递用户认证信息。

**Request Headers:**

```
Content-Type: application/json
Authorization-Inner: Bearer {current_user_access_token}
```

**Request Body (OpenAI 格式):**

```json
{
  "model": "gpt-4o-mini",
  "messages": [
    { "role": "user", "content": "Hello" },
    { "role": "assistant", "content": "Hi there!" },
    { "role": "user", "content": "How are you?" }
  ]
}
```

**Response (OpenAI 格式):**

```json
{
  "choices": [
    {
      "message": {
        "content": "I'm doing well, how can I help you?"
      }
    }
  ]
}
```

---

## 10. Software Update Check

**URL:** `https://www.aliang.one/api/public/downloads/check`
**Method:** GET
**Auth:** 无
**Source:** `app/http/services/software_update_service.go`

**Query Parameters:**

| Param | Type | Required | Description |
|-------|------|----------|-------------|
| platform | string | Yes | 运行平台：darwin / windows / linux |
| version | string | Yes | 当前版本号 |

**Response:**

```json
{
  "code": 0,
  "msg": "success",
  "data": {
    "software_name": "alianggate",
    "platform": "darwin",
    "current_version": "1.0.0",
    "latest_version": "1.1.0",
    "download_url": "https://example.com/download",
    "file_type": "dmg",
    "force_update": false,
    "needs_update": true,
    "changelog": "- Bug fixes\n- New features"
  }
}
```

---

## 11. GeoIP Database Download

**URL:** `https://aliang-1305838434.cos.ap-nanjing.myqcloud.com/static/GeoLite2-Country.mmdb`（用户 COS 分发；环境变量 `ALIANG_GEOIP_DOWNLOAD_URL` 可覆盖）
**Method:** GET
**Auth:** 无
**Source:** `processor/geoip/service.go`

下载 MaxMind GeoLite2 Country 数据库（二进制 MMDB 文件，约 3.3 MB）。启动时（start / tray / core 三条路径）本地缺失则后台异步下载（不阻塞启动；客户端超时 5 分钟，随机名临时文件写入、rename 前 geoip2 试开校验、成功后原子重命名；重定向仅允许 https 且至多 3 跳，体积上限 64MiB）；下载失败仅记日志、下次启动重试。下载完成装载后 geoip 规则自动生效；就绪前 geoip 条目一律不命中。数据库不做自动刷新，COS 副本建议每月刷新一次。

---

## 12. Local Gateway Routing Config API（本机 API）

**Base URL:** `http://127.0.0.1:56432`（本机网关自身 API；本节为本机契约，非上游外部 API）
**Source:** `app/http/handlers/config_handler.go`、`processor/config/routing_apply_state.go`

### GET /api/config/routing

返回当前生效的 canonical 路由配置（routing apply store 的活跃 canonical）。store 为空时返回全 direct 的空壳默认视图（`toAliang/toSocks` 均 false），不代表用户开关已关闭——以 POST 提交后的回读为准。

**Response：**

```json
{
  "data": {
    "version": 1,
    "ingress": {"mode": "tun"},
    "egress": {
      "direct": {"enabled": true},
      "toAliang": {"enabled": true},
      "toSocks": {"enabled": false, "upstream": {"type": "socks"}}
    },
    "routing": {
      "rules": [
        {"id": "ai_allowlist_0", "type": "domain", "condition": "api.openai.com", "enabled": true, "target": "toAliang"}
      ],
      "default_egress": "direct"
    }
  }
}
```

### POST /api/config/routing

提交完整 canonical 路由配置（整体替换，非增量 patch）。三段形态：

| 段 | 字段 | 约束 |
|----|------|------|
| `ingress` | `mode` | `tun` \| `http`（缺省补 `tun`） |
| `egress.direct` | `enabled` | 必须为 `true`（direct 分支不可关闭） |
| `egress.toAliang` | `enabled` | AI 加速总开关 |
| `egress.toSocks` | `enabled` + `upstream.type` | 客户代理开关；`enabled=true` 时 `upstream.type` 必须为 `socks` \| `http` |
| `routing.rules[]` | `id/type/condition/enabled/target` | `target` ∈ `direct` \| `toAliang` \| `toSocks`；target 指向已关闭分支会被拒绝 |
| `routing.default_egress` | 可选 | 同 target 枚举；不可指向已关闭分支 |

未知字段拒绝（`DisallowUnknownFields`）；仅接受 canonical 三段形态，legacy 键一律报错并提示迁移。

**Response（成功）：**

```json
{
  "data": {
    "message": "Configuration updated successfully",
    "source": "canonical",
    "version": 3,
    "hash": "sha256:..."
  }
}
```

**Response（校验失败，400）：** `Configuration validation failed` + 具体字段错误；内存态保持上次成功配置（LKG）不变。

**开关重启保持语义：** Apply 成功后 canonical JSON 原子落盘 `<state>/routing.json`（tmp+rename；`<state>` 即 geoip 数据库所在状态目录）。进程启动时（start / tray / core 三条路径，早于任何连接处理）文件存在则装载恢复用户 egress 开关状态；文件缺失/损坏则回退空 store，由既有 bootstrap 推导（客户代理已配置时 toSocks 默认开启）。`egress.toSocks.enabled=false` 时即使客户代理仍配置，toSocks 分支也被拒绝并降级 direct 直连（不断链）；customer 配置保存（POST `/api/config/customer`）不会重置该开关。

---

