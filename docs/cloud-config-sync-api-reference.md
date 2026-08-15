# Aliang Cloud Config Sync API

Base URL: `{cloud_base_url}/api/v1`

Auth: `Authorization: Bearer {user_access_token}` (与 Sub2API 复用同一套 token)

---

## 1. 推送配置（批量）

`POST /api/v1/configs/sync`

客户端将本地配置批量推送到云端，云端按 UUID 做冲突处理。

### Request

```json
{
  "configs": [
    {
      "uuid": "550e8400-e29b-41d4-a716-446655440000",
      "software": "opencode",
      "name": "Home Desktop Config",
      "file_path": "~/.config/opencode/config.json",
      "version": "v1",
      "in_use": true,
      "selected": true,
      "format": "json",
      "content": "{\"proxy\":{\"type\":\"socks5\",\"server\":\"127.0.0.1:1080\"}}",
      "created_at": "2026-03-26T10:00:00Z",
      "updated_at": "2026-03-26T12:00:00Z"
    }
  ]
}
```

### Response

```json
{
  "code": 0,
  "msg": "ok",
  "data": {
    "result": [
      {
        "uuid": "550e8400-e29b-41d4-a716-446655440000",
        "action": "updated",
        "reason": "cloud_newer_replaced"
      }
    ],
    "synced_count": 1,
    "server_time": "2026-03-26T12:01:00Z"
  }
}
```

**action 枚举值**: `created` | `updated` | `skipped` (云端更新时间戳相同)

---

## 2. 拉取配置（按软件过滤）

`GET /api/v1/configs/sync?software=opencode&updated_after=2026-03-20T00:00:00Z`

客户端拉取云端配置，支持增量拉取。

### Response

```json
{
  "code": 0,
  "msg": "ok",
  "data": {
    "configs": [
      {
        "uuid": "550e8400-e29b-41d4-a716-446655440000",
        "software": "opencode",
        "name": "Home Desktop Config",
        "file_path": "~/.config/opencode/config.json",
        "version": "v1",
        "in_use": false,
        "selected": true,
        "format": "json",
        "content": "{\"proxy\":{\"type\":\"socks5\",\"server\":\"127.0.0.1:1080\"}}",
        "created_at": "2026-03-26T10:00:00Z",
        "updated_at": "2026-03-26T12:00:00Z"
      }
    ],
    "total": 1,
    "has_more": false
  }
}
```

**Query 参数**:

| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `software` | string | 否 | 按软件名过滤 |
| `updated_after` | string | 否 | ISO 8601 时间戳，只返回此时间之后更新的配置 |
| `page` | int | 否 | 分页，默认 1 |
| `page_size` | int | 否 | 默认 50，最大 200 |

---

## 3. 对比本地与云端差异

`POST /api/v1/configs/compare`

客户端提交本地配置的 UUID + updated_at 列表，云端返回差异。

### Request

```json
{
  "items": [
    {
      "uuid": "550e8400-e29b-41d4-a716-446655440000",
      "local_updated_at": "2026-03-26T12:00:00Z"
    },
    {
      "uuid": "660e8400-e29b-41d4-a716-446655440001",
      "local_updated_at": "2026-03-25T10:00:00Z"
    }
  ]
}
```

### Response

```json
{
  "code": 0,
  "msg": "ok",
  "data": {
    "items": [
      {
        "uuid": "550e8400-e29b-41d4-a716-446655440000",
        "software": "opencode",
        "name": "Home Desktop Config",
        "local_updated_at": "2026-03-26T12:00:00Z",
        "cloud_updated_at": "2026-03-26T11:00:00Z",
        "status": "local_newer"
      },
      {
        "uuid": "660e8400-e29b-41d4-a716-446655440001",
        "software": "claude",
        "name": "Claude Settings",
        "local_updated_at": "2026-03-25T10:00:00Z",
        "cloud_updated_at": "2026-03-26T15:00:00Z",
        "status": "cloud_newer"
      }
    ]
  }
}
```

**status 枚举值**:

| 值 | 说明 |
|------|------|
| `local_newer` | 本地配置更新，需要 push |
| `cloud_newer` | 云端配置更新，需要 pull |
| `same` | 时间戳一致，无需同步 |
| `local_only` | 云端不存在此 UUID |
| `cloud_only` | 返回不在请求列表中的云端配置 |

---

## 4. 删除配置（云端）

`DELETE /api/v1/configs/sync/{uuid}`

客户端通知云端删除指定配置。

### Response

```json
{
  "code": 0,
  "msg": "ok",
  "data": {
    "deleted": true,
    "uuid": "550e8400-e29b-41d4-a716-446655440000"
  }
}
```

---

## 5. 获取所有软件列表

`GET /api/v1/configs/software-list`

返回当前用户在云端存储的所有软件名。

### Response

```json
{
  "code": 0,
  "msg": "ok",
  "data": {
    "software_list": ["opencode", "claude", "cursor", "openai"]
  }
}
```

---

## 6. 获取同步状态概览

`GET /api/v1/configs/sync/status`

返回用户配置同步的汇总信息。

### Response

```json
{
  "code": 0,
  "msg": "ok",
  "data": {
    "total_configs": 12,
    "by_software": {
      "opencode": 3,
      "claude": 5,
      "cursor": 2,
      "openai": 2
    },
    "last_push_at": "2026-03-26T12:00:00Z",
    "last_pull_at": "2026-03-26T11:30:00Z"
  }
}
```

---

## 通用错误格式

所有接口统一使用：

```json
{
  "code": 40001,
  "msg": "具体错误描述",
  "data": null
}
```

**code 值**:

| code | 说明 |
|------|------|
| `0` | 成功 |
| `40001` | 参数校验失败 |
| `40101` | token 无效或过期 |
| `40301` | 权限不足 |
| `40401` | 资源不存在 |
| `40901` | 冲突（如并发写入） |
| `50001` | 服务端内部错误 |

---

## 数据模型

### SoftwareConfig（客户端与云端共享）

| 字段 | 类型 | 说明 |
|------|------|------|
| `uuid` | string(64) | 配置唯一标识，客户端生成 |
| `software` | string(128) | 所属软件名 (opencode/claude/cursor...) |
| `name` | string(255) | 配置名称（用户可读） |
| `file_path` | string | 本地磁盘写入路径 |
| `version` | string(128) | 配置版本 (如 v1) |
| `in_use` | bool | 是否已激活/正在使用 |
| `selected` | bool | 是否被用户选中 |
| `format` | string(16) | 格式: `json` 或 `yaml` |
| `content` | text | 配置内容 (JSON/YAML 文本) |
| `created_at` | timestamp | 创建时间 (ISO 8601) |
| `updated_at` | timestamp | 更新时间 (ISO 8601) |

---

## 设计要点

1. **复用 Sub2API 的 user token**: 不需要单独的认证体系，用现有的 `access_token` 鉴权
2. **增量同步**: 通过 `updated_after` 参数支持增量拉取，避免每次全量传输
3. **冲突感知**: `compare` 接口让客户端在 push/pull 之前先了解差异，避免盲目覆盖
4. **按 UUID 去重**: 每个 `SoftwareConfig` 有全局唯一 UUID，跨设备同步以此为 key
5. **时间戳驱动合并**: 客户端已有 `MergeByLatest` 逻辑（`updated_at` 更新的胜出），云端配合此策略
6. **用户隔离**: 云端按 `user_id`（从 token 中解析）隔离不同用户的配置数据
