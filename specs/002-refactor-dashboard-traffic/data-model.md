# 数据模型: 仪表板重构与流量监控优化

**日期**: 2025-12-17
**功能**: 仪表板重构与流量监控优化 (`002-refactor-dashboard-traffic`)
**状态**: 设计完成

---

## 核心实体

### 1. RoutingRulesConfig (路由规则配置)

**用途**: 统一的代理路由规则配置模型，用于Nacos存储、前端展示和后端判断

**字段**:

| 字段名 | 类型 | 必填 | 描述 | 验证规则 |
|--------|------|------|------|----------|
| to_door | RoutingRuleSet | 是 | To Door代理规则集 | - |
| black_list | RoutingRuleSet | 是 | 黑名单规则集(内网IP段) | - |
| none_lane | RoutingRuleSet | 是 | NoneLane代理规则集 | - |
| settings | RulesSettings | 是 | 全局设置(启用/禁用选项) | - |

**关系**:
- 包含3个`RoutingRuleSet`(聚合关系)
- 包含1个`RulesSettings`

**状态转换**: 无(配置对象)

---

### 2. RoutingRuleSet (路由规则集)

**用途**: 某一类别的多条路由规则的集合

**字段**:

| 字段名 | 类型 | 必填 | 描述 | 验证规则 |
|--------|------|------|------|----------|
| rules | []RoutingRule | 否 | 规则列表 | - |

**关系**:
- 包含多条`RoutingRule`(聚合关系)

---

### 3. RoutingRule (单条路由规则)

**用途**: 单条代理路由判断规则

**字段**:

| 字段名 | 类型 | 必填 | 描述 | 验证规则 |
|--------|------|------|------|----------|
| id | string | 是 | 规则唯一标识 | 非空，格式: `rule_{type}_{timestamp}` |
| type | RuleType | 是 | 规则类型 | 枚举: "domain", "ip", "geoip" |
| condition | string | 是 | 匹配条件 | 非空，格式取决于type |
| enabled | bool | 是 | 是否启用 | 默认true |
| created_at | timestamp | 是 | 创建时间 | ISO8601格式 |

**类型说明**:
- `type="domain"`: condition为域名通配符，如`*.example.com`、`example.com`
- `type="ip"`: condition为CIDR表示法，如`192.168.0.0/16`、`10.0.0.0/8`
- `type="geoip"`: condition为国家代码，如`CN`、`US`

**验证规则**:
- domain类型：condition必须是合法的域名或通配符
- ip类型：condition必须是合法的CIDR格式
- geoip类型：condition必须是ISO 3166-1 alpha-2国家代码(2个字符)

**状态转换**:
```
[新建] -> enabled=true (默认启用)
enabled=true <-> enabled=false (管理员切换)
```

---

### 4. RulesSettings (规则全局设置)

**用途**: 控制全局规则引擎的启用/禁用选项

**字段**:

| 字段名 | 类型 | 必填 | 描述 | 验证规则 |
|--------|------|------|------|----------|
| geoip_enabled | bool | 是 | 是否启用GeoIP判断 | 默认true |
| none_lane_enabled | bool | 是 | 是否启用NoneLane代理 | 默认false |

**说明**:
- `geoip_enabled=false`: 所有type="geoip"的规则不会被执行
- `none_lane_enabled=false`: none_lane规则集中的所有规则不会被执行

---

### 5. TrafficStats (流量统计数据)

**用途**: 单个时间点的流量统计快照

**字段**:

| 字段名 | 类型 | 必填 | 描述 | 验证规则 |
|--------|------|------|------|----------|
| timestamp | int64 | 是 | Unix时间戳(秒) | 必须>0 |
| active_connections | int32 | 是 | 当前活跃连接数 | >=0 |
| upload_bytes | uint64 | 是 | 上传流量(字节) | >=0 |
| download_bytes | uint64 | 是 | 下载流量(字节) | >=0 |

**关系**:
- 隶属于某个时间维度(1s/5s/15s)的缓存列表

**状态转换**: 无(只读数据)

---

### 6. StatsSnapshot (统计数据快照响应)

**用途**: API返回的统计数据响应对象

**字段**:

| 字段名 | 类型 | 必填 | 描述 | 验证规则 |
|--------|------|------|------|----------|
| timescale | string | 是 | 时间尺度 | 枚举: "1s", "5s", "15s" |
| stats | []TrafficStats | 是 | 统计数据列表 | 长度<=300 |
| active_connections | int32 | 是 | 当前活跃连接数 | >=0 |

**说明**:
- `stats`数组包含最近300条统计记录
- `active_connections`为实时连接数(与最后一条stats.active_connections一致)

---

## 数据流示意图

### 配置管理流

```
用户(前端)
  -> GET /api/config/routing
  -> 后端读取Nacos
  -> 返回RoutingRulesConfig JSON

用户修改配置
  -> POST /api/config/routing + JSON
  -> 后端验证RoutingRulesConfig
  -> 写入Nacos
  -> 返回成功/错误
```

### 流量统计流

```
后端后台goroutine:
  每1秒收集一次 -> cache1s.Push(TrafficStats)
  每5秒收集一次 -> cache5s.Push(TrafficStats)
  每15秒收集一次 -> cache15s.Push(TrafficStats)

前端:
  每秒调用 GET /api/stats/{timescale}
  -> 后端返回StatsSnapshot{stats: [...300条]}
  -> 前端chart.js渲染
```

---

## 配置示例 (JSON)

### 完整的RoutingRulesConfig示例

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
      },
      {
        "id": "rule_geoip_1734428900",
        "type": "geoip",
        "condition": "US",
        "enabled": true,
        "created_at": "2025-12-17T08:01:40Z"
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
      },
      {
        "id": "rule_ip_1734429100",
        "type": "ip",
        "condition": "10.0.0.0/8",
        "enabled": true,
        "created_at": "2025-12-17T08:05:00Z"
      }
    ]
  },
  "none_lane": {
    "rules": [
      {
        "id": "rule_domain_1734429200",
        "type": "domain",
        "condition": "*.nonelane.example.com",
        "enabled": true,
        "created_at": "2025-12-17T08:06:40Z"
      }
    ]
  },
  "settings": {
    "geoip_enabled": true,
    "none_lane_enabled": false
  }
}
```

### StatsSnapshot API响应示例

```json
{
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
    // ... 最多300条
  ]
}
```

---

## 迁移与兼容性

### 从旧配置迁移到新模型

**旧配置问题**:
- `processor/config/types.go`中的`RoutingRulesConfig`可能与Nacos格式不一致
- 缺少统一的启用/禁用开关
- 缺少规则ID和创建时间

**迁移策略**:
1. 编写迁移脚本读取旧配置
2. 为每条规则生成唯一ID(`rule_{type}_{timestamp}`)
3. 添加`created_at`字段(使用当前时间)
4. 添加`settings`字段(默认值: `geoip_enabled=true`, `none_lane_enabled=false`)
5. 验证新配置并写入Nacos

**向后兼容**:
- 保留旧配置文件备份
- 实施期间同时支持新旧两种格式
- 在FR-010要求下最终移除旧代码

---

## 索引与查询

### RoutingRulesConfig

无需数据库索引（存储在Nacos配置中）

**查询方式**:
- 通过Nacos SDK读取完整配置
- 在内存中根据type、enabled过滤规则

### TrafficStats

**缓存结构**:
- 使用环形缓冲区(Ring Buffer)，固定大小300
- FIFO策略，新数据自动覆盖最旧数据

**查询方式**:
- GET /api/stats/1s → 返回cache1s的所有数据
- GET /api/stats/5s → 返回cache5s的所有数据
- GET /api/stats/15s → 返回cache15s的所有数据

---

## 数据约束总结

| 实体 | 约束 |
|------|------|
| RoutingRulesConfig | to_door, black_list, none_lane, settings均不能为null |
| RoutingRule | id唯一; type必须是domain/ip/geoip之一; condition格式取决于type |
| TrafficStats | timestamp > 0; active_connections >= 0; upload/download_bytes >= 0 |
| StatsSnapshot | timescale必须是1s/5s/15s之一; stats长度<=300 |
