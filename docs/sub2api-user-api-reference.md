# Sub2API 用户侧接口文档（微服务调用版）

## 约���

| 项目 | 说明 |
|------|------|
| **Base URL** | `http://<sub2api-host>:8080` |
| **认证方式** | `Authorization: Bearer <jwt_token>`（透传用户 JWT） |
| **共享依赖** | 同一 PostgreSQL + 同一 Redis + 同一 JWT Secret |
| **响应格式** | JSON，统一包装 `{ "data": ..., "message": "success" }` |

---

## 一、认证 Auth

### 1.1 注册

```
POST /api/v1/auth/register
```

**认证**: 无需

**Request Body**:
```json
{
  "email": "user@example.com",
  "password": "123456",
  "verify_code": "123456",
  "turnstile_token": "xxx",
  "promo_code": "WELCOME",
  "invitation_code": "INV123"
}
```

**Response** (`200`):
```json
{
  "data": {
    "access_token": "eyJhbG...",
    "refresh_token": "rt_xxx",
    "expires_in": 1800,
    "token_type": "Bearer",
    "user": { "id": 1, "email": "user@example.com", "role": "user" }
  }
}
```

---

### 1.2 登录

```
POST /api/v1/auth/login
```

**认证**: 无需

**Request Body**:
```json
{
  "email": "user@example.com",
  "password": "123456",
  "turnstile_token": "xxx"
}
```

**Response** (`200`):
```json
{
  "data": {
    "access_token": "eyJhbG...",
    "refresh_token": "rt_xxx",
    "expires_in": 1800,
    "token_type": "Bearer",
    "user": { "id": 1, "email": "user@example.com", "role": "user" }
  }
}
```

> 若用户开启了 TOTP 二步验证，登录会返回特殊状态码，需再调用 `/auth/login/2fa`。

---

### 1.3 登录（二步验证）

```
POST /api/v1/auth/login/2fa
```

**认证**: 无需

**Request Body**:
```json
{
  "email": "user@example.com",
  "password": "123456",
  "totp_code": "123456"
}
```

---

### 1.4 刷新 Token

```
POST /api/v1/auth/refresh
```

**认证**: 无需（携带 refresh_token）

**Request Body**:
```json
{
  "refresh_token": "rt_xxx"
}
```

---

### 1.5 登出

```
POST /api/v1/auth/logout
```

**认证**: 无需

**Request Body**:
```json
{
  "refresh_token": "rt_xxx"
}
```

---

### 1.6 撤销所有会话

```
POST /api/v1/auth/revoke-all-sessions
```

**认证**: 需要 JWT

---

### 1.7 忘记密码（发送重置邮件）

```
POST /api/v1/auth/forgot-password
```

**认证**: 无需

**Request Body**:
```json
{ "email": "user@example.com" }
```

---

### 1.8 重置密码

```
POST /api/v1/auth/reset-password
```

**认证**: 无需

**Request Body**:
```json
{
  "email": "user@example.com",
  "code": "123456",
  "new_password": "newpass123"
}
```

---

### 1.9 发送邮件验证码

```
POST /api/v1/auth/send-verify-code
```

**认证**: 无需

**Request Body**:
```json
{ "email": "user@example.com", "turnstile_token": "xxx" }
```

---

### 1.10 获取当前用户信息

```
GET /api/v1/auth/me
```

**认证**: 需要 JWT

---

## 二、用户 User

### 2.1 获取用户资料（含余额）

```
GET /api/v1/user/profile
```

**认证**: 需要 JWT

**Response**:
```json
{
  "data": {
    "id": 1,
    "email": "user@example.com",
    "username": "张三",
    "role": "user",
    "balance": 99.50000000,
    "concurrency": 5,
    "status": "active",
    "allowed_groups": [1, 3],
    "created_at": "2025-01-01T00:00:00Z",
    "updated_at": "2025-03-20T00:00:00Z"
  }
}
```

> **`balance`** 即为账户余额（单位 USD）。

---

### 2.2 更新个人资料

```
PUT /api/v1/user
```

**认证**: 需要 JWT

**Request Body**:
```json
{ "username": "新名字" }
```

---

### 2.3 修改密码

```
PUT /api/v1/user/password
```

**认证**: 需要 JWT

**Request Body**:
```json
{
  "old_password": "oldpass",
  "new_password": "newpass123"
}
```

> 注意：修改密码后所有已签发的 JWT 立即失效（TokenVersion 递增机制）。

---

### 2.4 TOTP 二步验证

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/api/v1/user/totp/status` | 查询 TOTP 状态 |
| GET | `/api/v1/user/totp/verification-method` | 获取当前验证方式 |
| POST | `/api/v1/user/totp/send-code` | 发送备用验证码 |
| POST | `/api/v1/user/totp/setup` | 发起 TOTP 绑定 |
| POST | `/api/v1/user/totp/enable` | 启用 TOTP |
| POST | `/api/v1/user/totp/disable` | 禁用 TOTP |

---

## 三、API Key 管理

### 3.1 获取可用分组列表

```
GET /api/v1/groups/available
```

**认证**: 需要 JWT

**Response**:
```json
{
  "data": [
    {
      "id": 3,
      "name": "Claude 基础组",
      "platform": "anthropic",
      "rate_multiplier": 1.0,
      "is_exclusive": false,
      "status": "active",
      "subscription_type": "",
      "daily_limit_usd": null,
      "weekly_limit_usd": null,
      "monthly_limit_usd": null
    }
  ]
}
```

---

### 3.2 查询分组倍率

```
GET /api/v1/groups/rates
```

**认证**: 需要 JWT

---

### 3.3 创建 API Key（指定分组）

```
POST /api/v1/api-keys
```

**认证**: 需要 JWT

**Request Body**:
```json
{
  "name": "我的 Claude Key",
  "group_id": 3,
  "custom_key": "sk-my-custom-key",
  "quota": 100.0,
  "expires_in_days": 30,
  "ip_whitelist": ["1.2.3.4"],
  "ip_blacklist": [],
  "rate_limit_5h": 50.0,
  "rate_limit_1d": 100.0,
  "rate_limit_7d": 500.0
}
```

**Response** (`200`):
```json
{
  "data": {
    "id": 10,
    "key": "sk-xxx...yyy",
    "name": "我的 Claude Key",
    "group_id": 3,
    "status": "active",
    "quota": 100.0,
    "quota_used": 0,
    "expires_at": "2025-04-22T00:00:00Z",
    "rate_limit_5h": 50.0,
    "rate_limit_1d": 100.0,
    "rate_limit_7d": 500.0,
    "created_at": "2025-03-23T00:00:00Z"
  }
}
```

> **`key` 明文仅在创建时返回一次**，之后不可再查看。

---

### 3.4 列出我的所有 API Key

```
GET /api/v1/api-keys?page=1&per_page=20&status=active&group_id=3&search=claude
```

**认证**: 需要 JWT

**Query 参数**:

| 参数 | 类型 | 说明 |
|------|------|------|
| `page` | int | 页码，默认 1 |
| `per_page` | int | 每页条数，默认 20 |
| `status` | string | 筛选状态：`active` / `inactive` |
| `group_id` | int | 按分组筛选 |
| `search` | string | 按 key 名称模糊搜索 |

---

### 3.5 获取单个 API Key 详情

```
GET /api/v1/api-keys/:id
```

**认证**: 需要 JWT

---

### 3.6 更新 API Key

```
PUT /api/v1/api-keys/:id
```

**认证**: 需要 JWT

**Request Body**:
```json
{
  "name": "新名称",
  "group_id": 5,
  "status": "inactive",
  "quota": 200.0,
  "reset_quota": true,
  "expires_at": "2025-12-31T23:59:59Z",
  "rate_limit_1d": 200.0
}
```

> 通过将 `status` 设为 `"inactive"` 可**禁用**某个 API Key。

---

### 3.7 删除 API Key

```
DELETE /api/v1/api-keys/:id
```

**认证**: 需要 JWT

---

## 四、使用统计与查询

### 4.1 仪表盘概览

```
GET /api/v1/usage/dashboard/stats
```

**认证**: 需要 JWT

**Response**:
```json
{
  "data": {
    "total_api_keys": 5,
    "active_api_keys": 3,
    "total_requests": 12000,
    "total_input_tokens": 5000000,
    "total_output_tokens": 2000000,
    "total_tokens": 7000000,
    "total_cost": 150.50,
    "total_actual_cost": 145.20,
    "today_requests": 300,
    "today_input_tokens": 120000,
    "today_output_tokens": 50000,
    "today_tokens": 170000,
    "today_cost": 3.75,
    "today_actual_cost": 3.60,
    "average_duration_ms": 1250.5,
    "rpm": 12,
    "tpm": 6800
  }
}
```

---

### 4.2 使用趋势（不同时间粒度）

```
GET /api/v1/usage/dashboard/trend?start_date=2025-03-01&end_date=2025-03-23&granularity=day
```

**认证**: 需要 JWT

**Query 参数**:

| 参数 | 类型 | 说明 |
|------|------|------|
| `start_date` | string | 起始日期 `YYYY-MM-DD`，默认 7 天前 |
| `end_date` | string | 结束日期 `YYYY-MM-DD`，默认今��� |
| `granularity` | string | 聚合粒度：`day`（默认）/ `week` / `month` |
| `timezone` | string | 时区，如 `Asia/Shanghai` |

**Response**:
```json
{
  "data": {
    "trend": [
      {
        "date": "2025-03-01",
        "requests": 450,
        "input_tokens": 180000,
        "output_tokens": 72000,
        "cache_creation_tokens": 5000,
        "cache_read_tokens": 12000,
        "total_tokens": 269000,
        "cost": 5.62,
        "actual_cost": 5.40
      },
      { "date": "2025-03-02", "..." : "..." }
    ],
    "start_date": "2025-03-01",
    "end_date": "2025-03-23",
    "granularity": "day"
  }
}
```

---

### 4.3 模型使用分布

```
GET /api/v1/usage/dashboard/models?start_date=2025-03-01&end_date=2025-03-23
```

**认证**: 需要 JWT

**Response**:
```json
{
  "data": {
    "models": [
      {
        "model": "claude-sonnet-4-20250514",
        "requests": 5000,
        "input_tokens": 2000000,
        "output_tokens": 800000,
        "total_tokens": 2800000,
        "cost": 60.00,
        "actual_cost": 57.50
      },
      {
        "model": "gpt-4o",
        "requests": 3000,
        "input_tokens": 1200000,
        "output_tokens": 500000,
        "total_tokens": 1700000,
        "cost": 45.00,
        "actual_cost": 43.00
      }
    ],
    "start_date": "2025-03-01",
    "end_date": "2025-03-23"
  }
}
```

---

### 4.4 按 API Key 批量查询用量

```
POST /api/v1/usage/dashboard/api-keys-usage
```

**认证**: 需要 JWT

**Request Body**:
```json
{
  "api_key_ids": [10, 11, 12]
}
```

**Response**:
```json
{
  "data": {
    "stats": {
      "10": { "api_key_id": 10, "today_actual_cost": 1.50, "total_actual_cost": 25.00 },
      "11": { "api_key_id": 11, "today_actual_cost": 0.80, "total_actual_cost": 12.00 }
    }
  }
}
```

---

### 4.5 使用统计汇总（支持时间范围）

```
GET /api/v1/usage/stats?period=month&api_key_id=10
GET /api/v1/usage/stats?start_date=2025-03-01&end_date=2025-03-23
```

**认证**: 需要 JWT

**Query 参数**:

| 参数 | 类型 | 说明 |
|------|------|------|
| `period` | string | `today` / `week` / `month`（与 start_date/end_date 二选一） |
| `start_date` | string | 起始日期 `YYYY-MM-DD` |
| `end_date` | string | 结束日期 `YYYY-MM-DD` |
| `api_key_id` | int | 按 API Key 过滤 |
| `timezone` | string | 时区 |

**Response**:
```json
{
  "data": {
    "total_requests": 5000,
    "total_input_tokens": 2000000,
    "total_output_tokens": 800000,
    "total_cache_tokens": 15000,
    "total_tokens": 2815000,
    "total_cost": 60.00,
    "total_actual_cost": 57.50,
    "average_duration_ms": 1200.0,
    "endpoints": [
      { "endpoint": "/v1/messages", "requests": 3000, "total_tokens": 1500000, "cost": 30.00, "actual_cost": 28.50 },
      { "endpoint": "/v1/chat/completions", "requests": 2000, "total_tokens": 1315000, "cost": 30.00, "actual_cost": 29.00 }
    ],
    "upstream_endpoints": []
  }
}
```

---

### 4.6 使用记录列表

```
GET /api/v1/usage?page=1&per_page=20&model=claude-sonnet-4-20250514&start_date=2025-03-01&end_date=2025-03-23&api_key_id=10&request_type=chat&stream=true
```

**认证**: 需要 JWT

**Query 参数**:

| 参数 | 类型 | 说明 |
|------|------|------|
| `page` | int | 页码，默认 1 |
| `per_page` | int | 每页条数，默认 20 |
| `model` | string | 按模型筛选 |
| `api_key_id` | int | 按 API Key 筛选 |
| `start_date` | string | 起始日期 |
| `end_date` | string | 结束日期 |
| `request_type` | string | 请求类型：`chat` / `image` / `sora` 等 |
| `stream` | bool | 按流式筛选 |
| `billing_type` | int | 按计费类型筛选 |
| `timezone` | string | 时区 |

**Response** (数组中每条记录):
```json
{
  "data": [
    {
      "id": 1001,
      "user_id": 1,
      "api_key_id": 10,
      "request_id": "req_xxx",
      "model": "claude-sonnet-4-20250514",
      "inbound_endpoint": "/v1/messages",
      "group_id": 3,
      "subscription_id": 5,
      "input_tokens": 1500,
      "output_tokens": 800,
      "cache_creation_tokens": 0,
      "cache_read_tokens": 200,
      "total_cost": 0.03,
      "actual_cost": 0.0285,
      "rate_multiplier": 1.0,
      "request_type": "chat",
      "stream": true,
      "duration_ms": 3200,
      "first_token_ms": 450,
      "created_at": "2025-03-20T10:30:00Z",
      "user": { "id": 1, "email": "user@example.com" },
      "api_key": { "id": 10, "name": "我的 Claude Key" },
      "group": { "id": 3, "name": "Claude 基础组" }
    }
  ]
}
```

---

### 4.7 单条使用记录详情

```
GET /api/v1/usage/:id
```

**认证**: 需要 JWT

---

## 五、充值与余额

### 5.1 卡密兑换（充值）

```
POST /api/v1/redeem
```

**认证**: 需要 JWT

**Request Body**:
```json
{ "code": "REDEEM-XXXX-YYYY" }
```

**Response**:
```json
{
  "data": {
    "id": 50,
    "code": "REDEEM-XXXX-YYYY",
    "type": "admin_balance",
    "value": 50.00,
    "status": "used",
    "used_by": 1,
    "used_at": "2025-03-23T10:00:00Z"
  }
}
```

> 卡密类型 `type` 包括：
> - `admin_balance` — 充值余额
> - `admin_concurrency` — 增加并发
> - `subscription` — 获得订阅（含 `group_id` 和 `validity_days`）

---

### 5.2 充值历史

```
GET /api/v1/redeem/history
```

**认证**: 需要 JWT

**Response**: 最近 25 条兑换记录

---

### 5.3 余额查询

余额包含在 `/api/v1/user/profile` 的 `balance` 字段中，参见 **2.1**。

---

## 六、订阅管理（用户侧）

### 6.1 订阅列表（全部）

```
GET /api/v1/subscriptions
```

**认证**: 需要 JWT

**Response**:
```json
{
  "data": [
    {
      "id": 5,
      "user_id": 1,
      "group_id": 3,
      "starts_at": "2025-03-01T00:00:00Z",
      "expires_at": "2025-04-01T00:00:00Z",
      "status": "active",
      "daily_usage_usd": 2.50,
      "weekly_usage_usd": 15.00,
      "monthly_usage_usd": 45.00,
      "created_at": "2025-03-01T00:00:00Z",
      "group": {
        "id": 3,
        "name": "Claude 基础组",
        "daily_limit_usd": 5.0,
        "weekly_limit_usd": 30.0,
        "monthly_limit_usd": 100.0
      }
    }
  ]
}
```

---

### 6.2 活跃订阅列表

```
GET /api/v1/subscriptions/active
```

**认证**: 需要 JWT

---

### 6.3 订阅进度（用量/限额对比）

```
GET /api/v1/subscriptions/progress
```

**认证**: 需要 JWT

**Response**:
```json
{
  "data": [
    {
      "subscription": { "id": 5, "group_id": 3, "status": "active" },
      "progress": {
        "daily": { "used": 2.50, "limit": 5.00, "percentage": 50.0 },
        "weekly": { "used": 15.00, "limit": 30.00, "percentage": 50.0 },
        "monthly": { "used": 45.00, "limit": 100.00, "percentage": 45.0 }
      }
    }
  ]
}
```

---

### 6.4 订阅汇总

```
GET /api/v1/subscriptions/summary
```

**认证**: 需要 JWT

**Response**:
```json
{
  "data": {
    "active_count": 2,
    "total_used_usd": 60.00,
    "subscriptions": [
      {
        "id": 5,
        "group_id": 3,
        "group_name": "Claude 基础组",
        "status": "active",
        "daily_used_usd": 2.50,
        "daily_limit_usd": 5.00,
        "weekly_used_usd": 15.00,
        "weekly_limit_usd": 30.00,
        "monthly_used_usd": 45.00,
        "monthly_limit_usd": 100.00,
        "expires_at": "2025-04-01T00:00:00Z"
      }
    ]
  }
}
```

---

### 6.5 加入订阅（通过卡密兑换）

用户侧没有直接"加入订阅"的接口。订阅通过以下方式获得：

1. **卡密兑换** `POST /api/v1/redeem` — 卡密类型为 `subscription` 时自动获得订阅
2. **管理员分配** — 通过 Admin API `POST /api/v1/admin/subscriptions/assign`

如果你的微服务需要为用户分配订阅，使用 Admin API Key 调用：

```
POST /api/v1/admin/subscriptions/assign
x-api-key: <admin-key>
```

---

### 6.6 取消订阅

用户侧没有直接取消订阅的接口。取消需要管理员操作：

```
DELETE /api/v1/admin/subscriptions/:id
x-api-key: <admin-key>
```

---

## 七、Admin 管理接口

> 以下接口均需通过 **Admin API Key** 认证：在请求头中携带 `x-api-key: <admin-key>`。
> Admin API Key 在管理后台「系统设置 -> Admin API Key」中生成。

### 7.1 查询所有订阅套餐（分组列表）

```
GET /api/v1/admin/groups/all
```

**认证**: `x-api-key: <admin-key>`

**Query 参数**:

| 参数 | 类型 | 说明 |
|------|------|------|
| `platform` | string | 按平台筛选（可选），如 `anthropic`、`openai`、`gemini` |

**Response**:
```json
{
  "data": [
    {
      "id": 3,
      "name": "Claude 基础组",
      "platform": "anthropic",
      "rate_multiplier": 1.0,
      "is_exclusive": false,
      "status": "active",
      "subscription_type": "",
      "daily_limit_usd": 5.0,
      "weekly_limit_usd": 30.0,
      "monthly_limit_usd": 100.0,
      "account_count": 10,
      "active_account_count": 8
    },
    {
      "id": 5,
      "name": "GPT-4o 订阅组",
      "platform": "openai",
      "rate_multiplier": 1.5,
      "is_exclusive": true,
      "status": "active",
      "subscription_type": "monthly",
      "daily_limit_usd": null,
      "weekly_limit_usd": null,
      "monthly_limit_usd": 200.0,
      "account_count": 5,
      "active_account_count": 5
    }
  ]
}
```

> 此接口返回所有分组，无分页限制。在 Sub2API 中，**分组（Group）即订阅套餐**，`subscription_type` 字段标识是否为订阅制分组（空字符串表示非订阅制），`daily_limit_usd` / `weekly_limit_usd` / `monthly_limit_usd` 为用量限额。

---

### 7.2 为指定用户分配订阅

```
POST /api/v1/admin/subscriptions/assign
```

**认证**: `x-api-key: <admin-key>`

**Request Body**:
```json
{
  "user_id": 1,
  "group_id": 3,
  "validity_days": 30,
  "notes": "手动分配"
}
```

| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `user_id` | int64 | 是 | 目标用户 ID |
| `group_id` | int64 | 是 | 目标分组（订阅套餐）ID |
| `validity_days` | int | 否 | 有效天数（最大 36500，默认使用分组配置） |
| `notes` | string | 否 | 备注信息 |

**Response** (`200`):
```json
{
  "data": {
    "id": 10,
    "user_id": 1,
    "group_id": 3,
    "starts_at": "2025-03-24T00:00:00Z",
    "expires_at": "2025-04-23T00:00:00Z",
    "status": "active",
    "assigned_by": 0,
    "notes": "手动分配",
    "created_at": "2025-03-24T00:00:00Z"
  }
}
```

---

### 7.3 批量分配订阅

```
POST /api/v1/admin/subscriptions/bulk-assign
```

**认证**: `x-api-key: <admin-key>`

**Request Body**:
```json
{
  "user_ids": [1, 2, 3],
  "group_id": 3,
  "validity_days": 30,
  "notes": "批量分配"
}
```

| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `user_ids` | int64[] | 是 | 目标用户 ID 列表（至少 1 个） |
| `group_id` | int64 | 是 | 目标分组（订阅套餐）ID |
| `validity_days` | int | 否 | 有效天数 |
| `notes` | string | 否 | 备注信息 |

---

### 7.4 延长/缩短订阅有效期

```
POST /api/v1/admin/subscriptions/:id/extend
```

**认证**: `x-api-key: <admin-key>`

**Request Body**:
```json
{ "days": 15 }
```

| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `days` | int | 是 | 调整天数。正数延长，负数缩短。范围：`-36500 ~ 36500` |

---

### 7.5 重置订阅配额

```
POST /api/v1/admin/subscriptions/:id/reset-quota
```

**认证**: `x-api-key: <admin-key>`

**Request Body**:
```json
{
  "daily": true,
  "weekly": false,
  "monthly": false
}
```

> 至少设置一个为 `true`。重置后对应周期的用量归零。

---

### 7.6 撤销订阅

```
DELETE /api/v1/admin/subscriptions/:id
```

**认证**: `x-api-key: <admin-key>`

**Response**:
```json
{
  "data": { "message": "Subscription revoked successfully" }
}
```

---

### 7.7 查询指定用户的订阅列表

```
GET /api/v1/admin/users/:id/subscriptions
```

**认证**: `x-api-key: <admin-key>`

**Response**:
```json
{
  "data": [
    {
      "id": 10,
      "user_id": 1,
      "group_id": 3,
      "starts_at": "2025-03-01T00:00:00Z",
      "expires_at": "2025-04-01T00:00:00Z",
      "status": "active",
      "daily_usage_usd": 2.50,
      "weekly_usage_usd": 15.00,
      "monthly_usage_usd": 45.00,
      "created_at": "2025-03-01T00:00:00Z"
    }
  ]
}
```

---

### 7.8 查询指定分组的订阅列表

```
GET /api/v1/admin/groups/:id/subscriptions?page=1&per_page=20
```

**认证**: `x-api-key: <admin-key>`

---

### 7.9 查询订阅详情

```
GET /api/v1/admin/subscriptions/:id
```

**认证**: `x-api-key: <admin-key>`

---

### 7.10 查询订阅进度

```
GET /api/v1/admin/subscriptions/:id/progress
```

**认证**: `x-api-key: <admin-key>`

---

### 7.11 为指定用户充值余额

```
POST /api/v1/admin/users/:id/balance
```

**认证**: `x-api-key: <admin-key>`

**Request Body**:
```json
{
  "balance": 50.00,
  "operation": "add",
  "notes": "充值"
}
```

| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `balance` | float64 | 是 | 金额（必须 > 0） |
| `operation` | string | 是 | 操作类型：`set`（直接设定）/ `add`（增加）/ `subtract`（扣减） |
| `notes` | string | 否 | 备注信息 |

**示例**：

- 充值 50 USD：`{ "balance": 50, "operation": "add" }`
- 设置余额为 100 USD：`{ "balance": 100, "operation": "set" }`
- 扣减 10 USD：`{ "balance": 10, "operation": "subtract" }`

**Response** (`200`):
```json
{
  "data": {
    "id": 1,
    "email": "user@example.com",
    "username": "张三",
    "role": "user",
    "balance": 149.50000000,
    "concurrency": 5,
    "status": "active"
  }
}
```

> 此接口支持幂等性，重复请求不会重复扣减/增加。

---

### 7.12 查询用户余额变动历史

```
GET /api/v1/admin/users/:id/balance-history?page=1&per_page=20&type=balance
```

**认证**: `x-api-key: <admin-key>`

**Query 参数**:

| 参数 | 类型 | 说明 |
|------|------|------|
| `page` | int | 页码 |
| `per_page` | int | 每页条数 |
| `type` | string | 记录类型：`balance` / `admin_balance` / `concurrency` / `admin_concurrency` / `subscription` |

**Response**:
```json
{
  "data": {
    "items": [
      {
        "id": 100,
        "code": "ADMIN-xxx",
        "type": "admin_balance",
        "value": 50.00,
        "status": "used",
        "used_by": 1,
        "used_at": "2025-03-24T10:00:00Z",
        "notes": "充值"
      }
    ],
    "total": 15,
    "page": 1,
    "page_size": 20,
    "pages": 1,
    "total_recharged": 500.00
  }
}
```

---

### 7.13 为指定用户创建 API Key

```
POST /api/v1/api-keys
```

**认证**: `Authorization: Bearer <jwt_token>`（用户自身的 JWT）

> 创建 API Key 使用的是**用户侧接口**（第三章 3.3），不是 Admin 接口。
> 你的微服务只需透传该用户的 JWT 即可为其创建 Key。

**Request Body**:
```json
{
  "name": "Key 名称",
  "group_id": 3,
  "custom_key": "sk-custom-xxx",
  "quota": 100.0,
  "expires_in_days": 30,
  "ip_whitelist": ["1.2.3.4"],
  "rate_limit_5h": 50.0,
  "rate_limit_1d": 100.0,
  "rate_limit_7d": 500.0
}
```

> 详细字段说明参见 **3.3**。

---

### 7.14 禁用指定用户的 API Key

```
PUT /api/v1/api-keys/:id
```

**认证**: `Authorization: Bearer <jwt_token>`（用户自身的 JWT）

> 禁用 API Key 使用的是**用户侧接口**（第三章 3.6），不是 Admin 接口。
> 你的微服务只需透传该用户的 JWT 即可。

**Request Body**:
```json
{ "status": "inactive" }
```

---

### 7.15 查询指定用户的所有 API Key（Admin 视角）

```
GET /api/v1/admin/users/:id/api-keys?page=1&per_page=20
```

**认证**: `x-api-key: <admin-key>`

**Response**:
```json
{
  "data": [
    {
      "id": 10,
      "name": "我的 Claude Key",
      "key": "sk-xxx...***",
      "group_id": 3,
      "status": "active",
      "quota": 100.0,
      "quota_used": 25.50,
      "expires_at": "2025-04-22T00:00:00Z",
      "created_at": "2025-03-23T00:00:00Z"
    }
  ],
  "pagination": { "total": 3, "page": 1, "page_size": 20, "pages": 1 }
}
```

> Admin 视角可以看到 key 的掩码（非明文），适合管理端查看用户的所有 key。
> 如果用户自己查，使用 `GET /api/v1/api-keys`（**3.4**）。

---

### 7.16 查询指定用户的使用统计

```
GET /api/v1/admin/users/:id/usage?period=month
```

**认证**: `x-api-key: <admin-key>`

**Query 参数**:

| 参数 | 类型 | 说明 |
|------|------|------|
| `period` | string | 统计周期：`today` / `week` / `month`（默认 `month`） |

---

## 八、公开设置

```
GET /api/v1/settings/public
```

**认证**: 无需

返回注册是否开放、邮件验证是否开启等公开配置。

---

## 附录 A：需求与接口对照表

| 需求 | 接口 | 认证方式 | 状态 |
|------|------|----------|------|
| 注册 | `POST /api/v1/auth/register` | 无 | 支持 |
| 登录 | `POST /api/v1/auth/login` | 无 | 支持 |
| 忘记密码 | `POST /api/v1/auth/forgot-password` + `reset-password` | 无 | 支持 |
| 刷新 Token | `POST /api/v1/auth/refresh` | 无 | 支持 |
| 登出 | `POST /api/v1/auth/logout` | 无 | 支持 |
| 个人信息查看（含余额） | `GET /api/v1/user/profile` | JWT | 支持 |
| 在分组下创建 Key | `POST /api/v1/api-keys` | JWT | 支持 |
| 禁用 API Key | `PUT /api/v1/api-keys/:id` (`status: inactive`) | JWT | 支持 |
| 查询用户所有 API Key | `GET /api/v1/api-keys` | JWT | 支持 |
| Admin 查询用户所有 API Key | `GET /api/v1/admin/users/:id/api-keys` | Admin API Key | 支持 |
| API 调用次数统计 | `GET /api/v1/usage/dashboard/stats` | JWT | 支持 |
| Token 统计（日/周/月） | `GET /api/v1/usage/dashboard/trend` | JWT | 支持 |
| 模型使用分布 | `GET /api/v1/usage/dashboard/models` | JWT | 支持 |
| 按 Key 查用量 | `POST /api/v1/usage/dashboard/api-keys-usage` | JWT | 支持 |
| 统计汇总（含端点分布） | `GET /api/v1/usage/stats` | JWT | 支持 |
| 使用记录列表 | `GET /api/v1/usage` | JWT | 支持 |
| 单条记录详情 | `GET /api/v1/usage/:id` | JWT | 支持 |
| 订阅使用情况 | `GET /api/v1/subscriptions/progress` | JWT | 支持 |
| 订阅汇总 | `GET /api/v1/subscriptions/summary` | JWT | 支持 |
| 充值（卡密兑换） | `POST /api/v1/redeem` | JWT | 支持 |
| 余额查询 | `GET /api/v1/user/profile` -> `balance` | JWT | 支持 |
| 加入订阅 | Admin: `POST /api/v1/admin/subscriptions/assign` | Admin API Key | 支持 |
| 批量加入订阅 | Admin: `POST /api/v1/admin/subscriptions/bulk-assign` | Admin API Key | 支持 |
| 取消订阅 | Admin: `DELETE /api/v1/admin/subscriptions/:id` | Admin API Key | 支持 |
| 查询所有订阅套餐 | `GET /api/v1/admin/groups/all` | Admin API Key | 支持 |
| 为用户充值余额 | `POST /api/v1/admin/users/:id/balance` | Admin API Key | 支持 |
| 查询用户余额变动历史 | `GET /api/v1/admin/users/:id/balance-history` | Admin API Key | 支持 |
| 查询指定用户订阅 | `GET /api/v1/admin/users/:id/subscriptions` | Admin API Key | 支持 |
| Admin 查询用户使用统计 | `GET /api/v1/admin/users/:id/usage` | Admin API Key | 支持 |
| 延长/缩短订阅 | `POST /api/v1/admin/subscriptions/:id/extend` | Admin API Key | 支持 |
| 重置订阅配额 | `POST /api/v1/admin/subscriptions/:id/reset-quota` | Admin API Key | 支持 |

---

## 附录 B：认证方式说明

### 用户 JWT（透传方式）

适用于用户侧操作（注册、登录、查询个人数据、创建 Key、查看统计等）：

```
Authorization: Bearer <jwt_token>
```

你的微服务在用户登录后获得 JWT，后续请求透传此 Token 即可。

### Admin API Key

适用于管理操作（分配订阅、充值余额、查询所有套餐、管理用户 Key 等）：

```
x-api-key: <admin-key>
```

Admin API Key 在 Sub2API 管理后台「系统设置」中生成，是一个长期有效的密钥，适合服务端对服务端调用。
