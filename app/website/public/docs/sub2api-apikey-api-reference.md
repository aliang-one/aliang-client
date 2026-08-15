# Sub2API API Key 管理文档

这份文档是用户中心内置的精简版参考，方便在查看 API Key 列表时快速对照接口。

完整仓库文档源文件:

- `docs/sub2api-apikey-api-reference.md`

## 用户中心当前使用的代理接口

用户中心页面通过本地服务暴露的代理接口读取数据:

```http
GET /api/user-center/api-keys
```

返回结构示例:

```json
{
  "data": {
    "status": "success",
    "data": {
      "items": [
        {
          "id": 10,
          "key": "sk-xxx...***",
          "name": "Claude 主力 Key",
          "group_id": 3,
          "status": "active",
          "provider": "anthropic",
          "masked": true,
          "secret_available": false,
          "group": {
            "id": 3,
            "name": "Claude 基础组",
            "description": "Claude 系列模型基础分组",
            "platform": "anthropic",
            "rate_multiplier": 1.0,
            "claude_code_only": false,
            "allow_messages_dispatch": true
          }
        }
      ]
    }
  }
}
```

## 上游用户侧接口

这些接口对应真正的 Sub2API 用户侧 API，用户中心展示的数据主要来自这里。

### 1. 获取可用分组

```http
GET /api/v1/groups/available
```

用途:

- 创建 Key 前获取可选分组
- 查看分组平台、倍率、说明

### 2. 列出我的 API Key

```http
GET /api/v1/api-keys?page=1&per_page=20&status=active&group_id=3&search=claude
```

常用字段:

- `id`: Key ID
- `key`: Key 值，通常为掩码
- `name`: Key 名称
- `group_id`: 分组 ID
- `status`: `active` / `inactive`
- `quota`: 配额上限
- `quota_used`: 已用配额
- `expires_at`: 过期时间

### 3. 查看单个 API Key 详情

```http
GET /api/v1/api-keys/:id
```

适合在管理页面中展示更详细的单条信息，例如:

- 用户信息
- 分组对象
- 详细状态

### 4. 创建 API Key

```http
POST /api/v1/api-keys
Content-Type: application/json
Authorization: Bearer <jwt_token>
```

请求示例:

```json
{
  "name": "我的 Claude Key",
  "group_id": 3,
  "quota": 100.0,
  "expires_in_days": 30,
  "rate_limit_5h": 50.0,
  "rate_limit_1d": 100.0,
  "rate_limit_7d": 500.0
}
```

说明:

- `key` 明文通常只会在创建时返回一次
- 创建完成后应立即保存明文

### 5. 启用或禁用 API Key

```http
PUT /api/v1/api-keys/:id
```

禁用:

```json
{ "status": "inactive" }
```

启用:

```json
{ "status": "active" }
```

### 6. 删除 API Key

```http
DELETE /api/v1/api-keys/:id
```

说明:

- 删除为永久操作
- 如果只是临时停用，优先使用更新接口把 `status` 设为 `inactive`

## 认证方式

用户侧接口:

```http
Authorization: Bearer <jwt_token>
```

管理侧接口:

```http
x-api-key: <admin-key>
```

## 相关说明

- 用户中心右侧展示的是当前用户已创建的 Key 列表
- 详情面板会优先展示用户中心代理接口已经返回的字段
- 如果需要更完整的配额、过期时间、限速窗口等信息，请调用上游详情接口 `GET /api/v1/api-keys/:id`
