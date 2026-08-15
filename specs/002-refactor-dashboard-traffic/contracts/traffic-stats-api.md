# API 契约: 流量统计

**端点**: `/api/stats`
**版本**: v1
**协议**: HTTP/JSON

---

## GET /api/stats/{timescale}

获取指定时间尺度的流量统计数据

### 请求

**方法**: `GET`
**路径**: `/api/stats/{timescale}`
**认证**: 需要（用户权限）

**路径参数**:
- `timescale`: 时间尺度，枚举值: `1s`, `5s`, `15s`

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
    "timescale": "1s",
    "active_connections": 125,
    "stats": [
      {
        "timestamp": 1734428800,
        "active_connections": 120,
        "upload_bytes": 15360000,
        "download_bytes": 76800000
      },
      {
        "timestamp": 1734428801,
        "active_connections": 123,
        "upload_bytes": 15420000,
        "download_bytes": 77000000
      },
      {
        "timestamp": 1734428802,
        "active_connections": 125,
        "upload_bytes": 15500000,
        "download_bytes": 77200000
      }
    ]
  }
}
```

**字段说明**:
- `timescale`: 请求的时间尺度
- `active_connections`: 当前实时活跃连接数
- `stats`: 统计数据数组，按时间戳升序排列，最多300条

**错误响应**:

- **400 Bad Request**: 无效的时间尺度
```json
{
  "success": false,
  "error": {
    "code": "INVALID_TIMESCALE",
    "message": "Invalid timescale parameter. Must be one of: 1s, 5s, 15s"
  }
}
```

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

- **503 Service Unavailable**: 统计服务不可用
```json
{
  "success": false,
  "error": {
    "code": "STATS_UNAVAILABLE",
    "message": "Traffic statistics service is temporarily unavailable"
  }
}
```

---

## GET /api/stats/current

获取当前实时流量信息（简化版本，不含历史数据）

### 请求

**方法**: `GET`
**路径**: `/api/stats/current`
**认证**: 需要（用户权限）

**请求头**:
```
Authorization: Bearer {token}
Accept: application/json
```

### 响应

**成功响应 (200 OK)**:

```json
{
  "success": true,
  "data": {
    "timestamp": 1734428802,
    "active_connections": 125,
    "upload_bytes": 15500000,
    "download_bytes": 77200000,
    "upload_rate": 52000,
    "download_rate": 256000
  }
}
```

**字段说明**:
- `timestamp`: 当前时间戳
- `active_connections`: 活跃连接数
- `upload_bytes`: 总上传流量(字节)
- `download_bytes`: 总下载流量(字节)
- `upload_rate`: 当前上传速率(字节/秒)
- `download_rate`: 当前下载速率(字节/秒)

---

## 数据类型定义

### StatsSnapshot

| 字段 | 类型 | 必填 | 描述 |
|------|------|------|------|
| timescale | string | 是 | 时间尺度: "1s", "5s", "15s" |
| active_connections | int32 | 是 | 当前活跃连接数 |
| stats | []TrafficStats | 是 | 统计数据数组(最多300条) |

### TrafficStats

| 字段 | 类型 | 必填 | 描述 |
|------|------|------|------|
| timestamp | int64 | 是 | Unix时间戳(秒) |
| active_connections | int32 | 是 | 活跃连接数 |
| upload_bytes | uint64 | 是 | 上传流量(字节) |
| download_bytes | uint64 | 是 | 下载流量(字节) |

### CurrentStats

| 字段 | 类型 | 必填 | 描述 |
|------|------|------|------|
| timestamp | int64 | 是 | Unix时间戳(秒) |
| active_connections | int32 | 是 | 活跃连接数 |
| upload_bytes | uint64 | 是 | 总上传流量(字节) |
| download_bytes | uint64 | 是 | 总下载流量(字节) |
| upload_rate | uint64 | 是 | 上传速率(字节/秒) |
| download_rate | uint64 | 是 | 下载速率(字节/秒) |

---

## 使用说明

### 前端实时监控实现

**推荐模式**: 每秒轮询

```javascript
// 每秒刷新流量统计
async function refreshTrafficStats() {
    const timescale = getCurrentTimescale(); // "1s" | "5s" | "15s"
    const response = await fetch(`/api/stats/${timescale}`, {
        headers: {
            'Authorization': `Bearer ${token}`,
            'Accept': 'application/json'
        }
    });

    if (response.ok) {
        const result = await response.json();
        updateChart(result.data.stats);
        updateConnections(result.data.active_connections);
    }
}

setInterval(refreshTrafficStats, 1000); // 每秒刷新一次
```

### 时间尺度切换

用户在前端切换时间尺度时，只需改变API请求的timescale参数，无需额外逻辑：

```javascript
function switchTimescale(newTimescale) {
    currentTimescale = newTimescale; // "1s" | "5s" | "15s"
    // 下一次定时器触发时会自动使用新的timescale
}
```

---

## 性能特性

- **响应时间**: < 50ms (后端内存缓存)
- **数据延迟**: 最多T+2秒 (统计收集延迟)
- **缓存大小**: 每个时间尺度最多300条记录
- **刷新频率**: 建议每秒1次，避免过度刷新

---

## 错误码参考

| 错误码 | HTTP状态码 | 描述 |
|--------|------------|------|
| UNAUTHORIZED | 401 | 未授权访问 |
| INVALID_TIMESCALE | 400 | 无效的时间尺度参数 |
| STATS_UNAVAILABLE | 503 | 统计服务暂时不可用 |
| INTERNAL_ERROR | 500 | 服务器内部错误 |
