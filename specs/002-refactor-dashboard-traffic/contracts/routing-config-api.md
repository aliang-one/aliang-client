# API 契约: 路由配置管理

**端点**: `/api/config/routing`
**版本**: v1
**协议**: HTTP/JSON

---

## GET /api/config/routing

获取当前的路由规则配置

### 请求

**方法**: `GET`
**路径**: `/api/config/routing`
**认证**: 需要（管理员权限）

**请求头**:
```
Authorization: Bearer {token}
Accept: application/json
```

**查询参数**: 无

### 响应

**成功响应 (200 OK)**:

```json
{
  "success": true,
  "data": {
    "to_door": {
      "rules": [
        {
          "id": "rule_domain_1734428800",
          "type": "domain",
          "condition": "*.google.com",
          "enabled": true,
          "created_at": "2025-12-17T08:00:00Z"
        }
      ]
    },
    "black_list": {
      "rules": [
        {
          "id": "rule_ip_1734429000",
          "type": "ip",
          "condition": "192.168.0.0/16",
          "enabled": true,
          "created_at": "2025-12-17T08:03:20Z"
        }
      ]
    },
    "none_lane": {
      "rules": []
    },
    "settings": {
      "geoip_enabled": true,
      "none_lane_enabled": false
    }
  }
}
```

**错误响应**:

- **401 Unauthorized**: 未授权
```json
{
  "success": false,
  "error": {
    "code": "UNAUTHORIZED",
    "message": "Authentication required"
  }
}
```

- **500 Internal Server Error**: 服务器错误
```json
{
  "success": false,
  "error": {
    "code": "INTERNAL_ERROR",
    "message": "Failed to load configuration from Nacos"
  }
}
```

---

## POST /api/config/routing

更新路由规则配置

### 请求

**方法**: `POST`
**路径**: `/api/config/routing`
**认证**: 需要（管理员权限）

**请求头**:
```
Authorization: Bearer {token}
Content-Type: application/json
```

**请求体**:

```json
{
  "to_door": {
    "rules": [
      {
        "id": "rule_domain_1734428800",
        "type": "domain",
        "condition": "*.google.com",
        "enabled": true,
        "created_at": "2025-12-17T08:00:00Z"
      }
    ]
  },
  "black_list": {
    "rules": [
      {
        "id": "rule_ip_1734429000",
        "type": "ip",
        "condition": "192.168.0.0/16",
        "enabled": true,
        "created_at": "2025-12-17T08:03:20Z"
      }
    ]
  },
  "none_lane": {
    "rules": []
  },
  "settings": {
    "geoip_enabled": true,
    "none_lane_enabled": false
  }
}
```

**字段验证**:
- `to_door`, `black_list`, `none_lane`: 必须存在
- `settings`: 必须存在
- 每条规则的`id`必须唯一
- `type`必须是`domain`, `ip`, `geoip`之一
- `condition`格式必须与`type`匹配

### 响应

**成功响应 (200 OK)**:

```json
{
  "success": true,
  "message": "Configuration updated successfully",
  "data": {
    "applied_at": "2025-12-17T10:30:45Z"
  }
}
```

**错误响应**:

- **400 Bad Request**: 验证失败
```json
{
  "success": false,
  "error": {
    "code": "VALIDATION_ERROR",
    "message": "Invalid rule condition",
    "details": {
      "field": "to_door.rules[0].condition",
      "issue": "Invalid domain pattern: must match *.example.com or example.com"
    }
  }
}
```

- **401 Unauthorized**: 未授权
- **500 Internal Server Error**: 保存失败

---

## PUT /api/config/routing/rules/{ruleId}/toggle

切换单条规则的启用/禁用状态

### 请求

**方法**: `PUT`
**路径**: `/api/config/routing/rules/{ruleId}/toggle`
**认证**: 需要（管理员权限）

**路径参数**:
- `ruleId`: 规则ID (例如: `rule_domain_1734428800`)

**请求头**:
```
Authorization: Bearer {token}
Content-Type: application/json
```

**请求体**:
```json
{
  "enabled": false
}
```

### 响应

**成功响应 (200 OK)**:

```json
{
  "success": true,
  "message": "Rule toggled successfully",
  "data": {
    "rule_id": "rule_domain_1734428800",
    "enabled": false
  }
}
```

**错误响应**:

- **404 Not Found**: 规则不存在
```json
{
  "success": false,
  "error": {
    "code": "RULE_NOT_FOUND",
    "message": "Rule with id 'rule_domain_1734428800' not found"
  }
}
```

---

## 数据类型定义

### RoutingRulesConfig

| 字段 | 类型 | 必填 | 描述 |
|------|------|------|------|
| to_door | RoutingRuleSet | 是 | To Door代理规则集 |
| black_list | RoutingRuleSet | 是 | 黑名单规则集 |
| none_lane | RoutingRuleSet | 是 | NoneLane代理规则集 |
| settings | RulesSettings | 是 | 全局设置 |

### RoutingRuleSet

| 字段 | 类型 | 必填 | 描述 |
|------|------|------|------|
| rules | []RoutingRule | 否 | 规则列表(可为空) |

### RoutingRule

| 字段 | 类型 | 必填 | 描述 |
|------|------|------|------|
| id | string | 是 | 规则唯一ID |
| type | string | 是 | 规则类型: "domain", "ip", "geoip" |
| condition | string | 是 | 匹配条件 |
| enabled | bool | 是 | 是否启用 |
| created_at | string | 是 | 创建时间(ISO8601) |

### RulesSettings

| 字段 | 类型 | 必填 | 描述 |
|------|------|------|------|
| geoip_enabled | bool | 是 | 是否启用GeoIP判断 |
| none_lane_enabled | bool | 是 | 是否启用NoneLane代理 |

---

## 错误码参考

| 错误码 | HTTP状态码 | 描述 |
|--------|------------|------|
| UNAUTHORIZED | 401 | 未授权访问 |
| FORBIDDEN | 403 | 无权限 |
| VALIDATION_ERROR | 400 | 请求数据验证失败 |
| RULE_NOT_FOUND | 404 | 规则不存在 |
| INTERNAL_ERROR | 500 | 服务器内部错误 |
| NACOS_ERROR | 500 | Nacos配置中心错误 |
