# Upstream API Dependencies

本文档记录 AliangGate 作为客户端调用的所有外部上游 API。

## 概览

| 服务 | Base URL | 认证方式 | 用途 |
|------|----------|----------|------|
| Aliang API | `{core.api_server}` | Bearer Token / mTLS | 认证、用户、订阅、Key、兑换 |
| Software Update | `https://www.aliang.one` | 无 | 版本检查 |
| GeoIP Database | `https://git.io` | 无 | MaxMind 数据库下载 |

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

**URL:** `https://git.io/GeoLite2-Country.mmdb`
**Method:** GET
**Auth:** 无
**Source:** `processor/geoip/service.go`

下载 MaxMind GeoLite2 Country 数据库（二进制 MMDB 文件）。客户端超时 5 分钟。

---
