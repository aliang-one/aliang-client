# 数据模型: 配置系统重构与路由引擎迁移

**Feature**: 003-refactor-config-routing
**Phase**: 1 - Design
**Date**: 2025-12-17

## 概述

本章节详细定义了配置系统中的核心数据实体，包括字段定义、关系、验证规则和状态转换。这些实体构成了路由决策引擎的数据基础。

---

## 核心实体

### 1. RoutingRule（单条路由规则）

**用途**: 表示一条具体的路由规则，用于判断请求是否应该走特定的代理

**字段定义**:

```go
type RoutingRule struct {
    // 唯一标识
    ID        string    `json:"id" validate:"required,max=128"`        // 例: rule_domain_1703001234

    // 规则类型和匹配条件
    Type      RuleType  `json:"type" validate:"required,oneof=domain ip geoip"` // domain|ip|geoip
    Condition string    `json:"condition" validate:"required,max=256"`  // 匹配条件内容

    // 规则状态
    Enabled   bool      `json:"enabled" default:"true"`                // 是否启用此规则

    // 元数据
    CreatedAt time.Time `json:"created_at"`                            // 创建时间 (ISO 8601)
    UpdatedAt time.Time `json:"updated_at"`                            // 最后更新时间

    // 可选字段
    Description string  `json:"description,omitempty" validate:"max=512"` // 规则描述
}

type RuleType string

const (
    RuleTypeDomain RuleType = "domain"  // 域名规则: *.google.com 或 example.com
    RuleTypeIP     RuleType = "ip"      // IP 段规则: 192.168.0.0/16 (CIDR 格式)
    RuleTypeGeoIP  RuleType = "geoip"   // 地理位置规则: US, CN (ISO 3166-1 alpha-2)
)
```

**字段详解**:

| 字段 | 类型 | 约束 | 示例 | 说明 |
|------|------|------|------|------|
| `id` | string | required, max 128 | rule_domain_1703001234 | 由客户端或服务器生成，格式 rule_{type}_{timestamp} |
| `type` | RuleType | required, enum | domain | 规则类型，决定 condition 的格式 |
| `condition` | string | required, max 256 | *.google.com | 匹配条件，格式取决于 type |
| `enabled` | bool | default true | true | false 时规则被忽略但不删除 |
| `created_at` | timestamp | readonly | 2025-12-17T10:00:00Z | UTC 时间戳，由服务器自动设置 |
| `updated_at` | timestamp | auto-update | 2025-12-17T10:30:00Z | 每次修改时更新 |
| `description` | string | optional, max 512 | Google DNS routing | 规则说明 |

**验证规则**:

```go
func (r *RoutingRule) Validate() error {
    // ID 验证
    if r.ID == "" {
        return errors.New("id is required")
    }
    if len(r.ID) > 128 {
        return errors.New("id too long (max 128)")
    }

    // Type 验证
    if r.Type != RuleTypeDomain && r.Type != RuleTypeIP && r.Type != RuleTypeGeoIP {
        return errors.New("invalid rule type")
    }

    // Condition 验证（取决于 Type）
    switch r.Type {
    case RuleTypeDomain:
        if !isValidDomain(r.Condition) {
            return errors.New("invalid domain format")
        }
    case RuleTypeIP:
        if !isValidCIDR(r.Condition) {
            return errors.New("invalid CIDR format")
        }
    case RuleTypeGeoIP:
        if !isValidCountryCode(r.Condition) {
            return errors.New("invalid country code (use ISO 3166-1 alpha-2)")
        }
    }

    // Description 验证
    if len(r.Description) > 512 {
        return errors.New("description too long (max 512)")
    }

    return nil
}

// 验证函数实现
func isValidDomain(domain string) bool {
    // 支持通配符 *.example.com 和完整域名 example.com
    if strings.HasPrefix(domain, "*.") {
        domain = domain[2:] // 移除 *.
    }
    // 基本的 DNS 域名正则（简化）
    return regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?)*$`).MatchString(domain)
}

func isValidCIDR(cidr string) bool {
    _, _, err := net.ParseCIDR(cidr)
    return err == nil
}

func isValidCountryCode(code string) bool {
    // ISO 3166-1 alpha-2 代码（2 位大写字母）
    return regexp.MustCompile(`^[A-Z]{2}$`).MatchString(code)
}
```

**状态转换**:

```
┌──────────┐
│ Created  │  新建时 enabled=true（默认启用）
└────┬─────┘
     │
     ├─► [编辑] ──► UpdatedAt 更新
     │
     └─► [启用/禁用] ──► enabled 切换，无需删除
```

**关系**:

- 一对多: `RoutingRuleSet` 包含多个 `RoutingRule`
- 独立性: 规则可在任何时刻启用或禁用，无副作用

---

### 2. RoutingRuleSet（规则集合）

**用途**: 一组相同类别的规则集合，用于分类管理不同类型的路由规则

**字段定义**:

```go
type RoutingRuleSet struct {
    // 规则集标识
    SetType SetType         `json:"set_type" validate:"required,oneof=to_door black_list none_lane"` // 规则集类型

    // 规则列表
    Rules   []RoutingRule   `json:"rules" validate:"dive"`                                          // 规则数组

    // 元数据
    Count   int             `json:"count" validate:"min=0,max=10000"`                              // 规则数量
    UpdatedAt time.Time     `json:"updated_at"`                                                    // 最后更新时间
}

type SetType string

const (
    SetTypeToDoor   SetType = "to_door"      // Door 代理规则：匹配则走 Door
    SetTypeBlacklist SetType = "black_list"   // 黑名单规则：匹配则不走 Door（保留用）
    SetTypeNoneLane SetType = "none_lane"     // NoneLane 规则：匹配则走 NoneLane
)
```

**字段详解**:

| 字段 | 类型 | 约束 | 说明 |
|------|------|------|------|
| `set_type` | SetType | required, enum | 规则集类型，决定其在路由决策中的用途 |
| `rules` | []RoutingRule | optional | 规则数组，最多 10000 条（性能限制） |
| `count` | int | readonly | 规则数量，自动计算 = len(rules) |
| `updated_at` | timestamp | auto-update | 集合内任一规则修改时更新 |

**验证规则**:

```go
func (rs *RoutingRuleSet) Validate() error {
    // SetType 验证
    if rs.SetType != SetTypeToDoor && rs.SetType != SetTypeBlacklist && rs.SetType != SetTypeNoneLane {
        return errors.New("invalid set type")
    }

    // Rules 验证
    if len(rs.Rules) > 10000 {
        return errors.New("too many rules (max 10000)")
    }

    // 逐条规则验证
    for i, rule := range rs.Rules {
        if err := rule.Validate(); err != nil {
            return fmt.Errorf("rule[%d] validation failed: %v", i, err)
        }
    }

    // Count 一致性检查
    rs.Count = len(rs.Rules)

    return nil
}
```

**关系**:

- 一对多: `RoutingRulesConfig` 包含 3 个 `RoutingRuleSet`（to_door, black_list, none_lane）
- 一对多: 每个 `RoutingRuleSet` 包含多个 `RoutingRule`

---

### 3. RulesSettings（全局设置）

**用途**: 控制路由引擎的全局行为，包括各类规则的启用/禁用和自动更新开关

**字段定义**:

```go
type RulesSettings struct {
    // 全局开关：特定规则类型是否启用
    NoneLaneEnabled  bool      `json:"none_lane_enabled" default:"true"`  // NoneLane 规则是否启用
    DoorEnabled      bool      `json:"door_enabled" default:"true"`       // Door 规则是否启用
    GeoIPEnabled     bool      `json:"geoip_enabled" default:"false"`     // GeoIP 规则是否启用

    // Nacos 同步控制（本地状态，不存储到 Nacos）
    AutoUpdate       bool      `json:"auto_update" default:"true"`        // 是否自动同步 Nacos 配置

    // 元数据
    UpdatedAt        time.Time `json:"updated_at"`                        // 最后修改时间
    LastNacosSync    time.Time `json:"last_nacos_sync,omitempty"`        // 最后一次 Nacos 同步时间
}
```

**字段详解**:

| 字段 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `none_lane_enabled` | bool | true | false 时跳过所有 NoneLane 规则检查 |
| `door_enabled` | bool | true | false 时跳过所有 Door 规则检查 |
| `geoip_enabled` | bool | false | false 时跳过 GeoIP 规则检查（可选特性） |
| `auto_update` | bool | true | **关键字段**：true 时自动从 Nacos 同步配置；false 时不同步 |
| `updated_at` | timestamp | - | UTC 时间戳，记录设置最后修改时刻 |
| `last_nacos_sync` | timestamp | - | UTC 时间戳，记录最后一次成功同步 Nacos 的时刻 |

**auto_update 状态机**:

```
┌──────────────────────────────────────────────────────────────┐
│              auto_update 状态转换                            │
├──────────────────────────────────────────────────────────────┤
│                                                              │
│  应用启动                                                    │
│     ↓                                                        │
│  auto_update = true （初始状态）                             │
│     ↓                                                        │
│  Nacos 监听启动，自动同步配置                                 │
│     ↓                                                        │
│  用户通过 API 修改配置                                       │
│     ↓                                                        │
│  auto_update = false （自动设置）                             │
│     ↓                                                        │
│  Nacos 监听仍活跃，但回调忽略变更                             │
│     ↓                                                        │
│  用户手动启用 auto_update（通过 API）                         │
│     ↓                                                        │
│  从 Nacos 拉取最新配置，覆盖本地配置                          │
│     ↓                                                        │
│  auto_update = true                                         │
│     ↓                                                        │
│  恢复自动同步                                                │
│                                                              │
└──────────────────────────────────────────────────────────────┘
```

**验证规则**:

```go
func (s *RulesSettings) Validate() error {
    // 当前无额外验证规则，所有字段都是布尔值或时间戳
    // 时间戳的有效性由 Go time.Time 保证
    return nil
}
```

**关键业务规则**:

1. **自动修改检测**: API 调用 `POST /config/routing` 时自动设置 `auto_update = false`
2. **Nacos 故障降级**: 无论 Nacos 故障状态，仅通过 `auto_update` 决定是否同步
3. **单向覆盖**: `auto_update` 从 false 切换为 true 时，Nacos 最新配置会覆盖本地配置

---

### 4. RoutingRulesConfig（完整路由配置）

**用途**: 整个路由配置的顶级容器，包含所有规则和设置

**字段定义**:

```go
type RoutingRulesConfig struct {
    // 三个规则集
    ToDoor    RoutingRuleSet `json:"to_door" validate:"required,dive"`      // Door 代理规则
    BlackList RoutingRuleSet `json:"black_list" validate:"required,dive"`   // 黑名单规则（保留）
    NoneLane  RoutingRuleSet `json:"none_lane" validate:"required,dive"`    // NoneLane 规则

    // 全局设置
    Settings  RulesSettings  `json:"settings" validate:"required"`          // 全局开关和同步控制

    // 元数据
    Version   int            `json:"version" validate:"min=1"`              // 配置版本号
    CreatedAt time.Time      `json:"created_at"`                            // 创建时间
    UpdatedAt time.Time      `json:"updated_at"`                            // 最后更新时间
}
```

**字段详解**:

| 字段 | 类型 | 说明 |
|------|------|------|
| `to_door` | RoutingRuleSet | Door 代理规则集合（必需） |
| `black_list` | RoutingRuleSet | 黑名单规则集合（保留用途，当前未使用） |
| `none_lane` | RoutingRuleSet | NoneLane 规则集合（必需） |
| `settings` | RulesSettings | 全局开关和自动更新设置 |
| `version` | int | 配置版本号，每次更新 +1（用于 Nacos 版本管理） |
| `created_at` | timestamp | 首次创建时间（不可变） |
| `updated_at` | timestamp | 最后修改时间（每次修改都更新） |

**验证规则**:

```go
func (rc *RoutingRulesConfig) Validate() error {
    // 验证三个规则集
    if err := rc.ToDoor.Validate(); err != nil {
        return fmt.Errorf("to_door validation failed: %v", err)
    }
    if err := rc.BlackList.Validate(); err != nil {
        return fmt.Errorf("black_list validation failed: %v", err)
    }
    if err := rc.NoneLane.Validate(); err != nil {
        return fmt.Errorf("none_lane validation failed: %v", err)
    }

    // 验证设置
    if err := rc.Settings.Validate(); err != nil {
        return fmt.Errorf("settings validation failed: %v", err)
    }

    // 版本号验证
    if rc.Version < 1 {
        return errors.New("version must be >= 1")
    }

    // 时间戳一致性检查
    if rc.UpdatedAt.Before(rc.CreatedAt) {
        return errors.New("updated_at cannot be before created_at")
    }

    return nil
}
```

**序列化示例**:

```json
{
  "to_door": {
    "set_type": "to_door",
    "rules": [
      {
        "id": "rule_domain_1703001234",
        "type": "domain",
        "condition": "*.google.com",
        "enabled": true,
        "created_at": "2025-12-17T10:00:00Z",
        "updated_at": "2025-12-17T10:00:00Z",
        "description": "Google services routing"
      },
      {
        "id": "rule_ip_1703001235",
        "type": "ip",
        "condition": "192.168.0.0/16",
        "enabled": false,
        "created_at": "2025-12-17T10:05:00Z",
        "updated_at": "2025-12-17T10:30:00Z"
      }
    ],
    "count": 2,
    "updated_at": "2025-12-17T10:30:00Z"
  },
  "black_list": {
    "set_type": "black_list",
    "rules": [],
    "count": 0,
    "updated_at": "2025-12-17T10:00:00Z"
  },
  "none_lane": {
    "set_type": "none_lane",
    "rules": [
      {
        "id": "rule_geoip_1703001236",
        "type": "geoip",
        "condition": "CN",
        "enabled": true,
        "created_at": "2025-12-17T10:10:00Z",
        "updated_at": "2025-12-17T10:10:00Z"
      }
    ],
    "count": 1,
    "updated_at": "2025-12-17T10:10:00Z"
  },
  "settings": {
    "none_lane_enabled": true,
    "door_enabled": true,
    "geoip_enabled": false,
    "auto_update": true,
    "updated_at": "2025-12-17T10:30:00Z",
    "last_nacos_sync": "2025-12-17T10:30:00Z"
  },
  "version": 5,
  "created_at": "2025-12-17T08:00:00Z",
  "updated_at": "2025-12-17T10:30:00Z"
}
```

---

### 5. Config（应用主配置）

**用途**: 应用启动时读取的配置，包含 API 服务器和 Nacos 服务器连接信息

**字段定义**:

```go
type Config struct {
    // API 服务器配置
    APIServer struct {
        Host    string `json:"host" default:"127.0.0.1"`       // API 监听地址
        Port    int    `json:"port" default:"56431"`          // API 监听端口
        Timeout int    `json:"timeout" default:"30"`          // 请求超时（秒）
    } `json:"api_server" validate:"required"`

    // Nacos 服务器配置
    NacosServer struct {
        Host      string `json:"host" validate:"hostname"`     // Nacos 服务器地址
        Port      int    `json:"port" default:"8848"`         // Nacos 服务器端口
        Namespace string `json:"namespace" default:""`        // Nacos 命名空间（可选）
        Group     string `json:"group" default:"DEFAULT_GROUP"` // Nacos 分组
        DataId    string `json:"data_id" validate:"required"`  // 配置 dataId
    } `json:"nacos_server" validate:"required"`

    // 日志配置
    Log struct {
        Level string `json:"level" default:"info"`    // 日志级别
        Path  string `json:"path" default:"./logs"`  // 日志路径
    } `json:"log"`

    // [注意] RoutingRules 字段已删除（Phase 3 重构）
    // RoutingRules 仅在启动时，从 Nacos 加载 RoutingRulesConfig
}
```

**字段详解**:

| 字段 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `api_server.host` | string | 127.0.0.1 | API 绑定的网络接口 |
| `api_server.port` | int | 56431 | API 监听端口 |
| `api_server.timeout` | int | 30 | 单个请求超时时间（秒） |
| `nacos_server.host` | string | - | Nacos 服务器地址（必需） |
| `nacos_server.port` | int | 8848 | Nacos 默认端口 |
| `nacos_server.namespace` | string | "" | Nacos 命名空间（空字符串表示默认命名空间） |
| `nacos_server.group` | string | DEFAULT_GROUP | Nacos 分组名称 |
| `nacos_server.data_id` | string | - | 路由配置的 dataId（必需） |
| `log.level` | string | info | 日志级别（trace/debug/info/warn/error） |
| `log.path` | string | ./logs | 日志输出目录 |

**配置示例** (config.yaml 或 config.json):

```yaml
api_server:
  host: 0.0.0.0
  port: 56431
  timeout: 30

nacos_server:
  host: localhost
  port: 8848
  namespace: ""
  group: DEFAULT_GROUP
  data_id: routing-config

log:
  level: info
  path: ./logs
```

**启动流程集成**:

```go
// cmd/main.go
func main() {
    // 1. 加载应用配置
    cfg := loadConfig("config.yaml")

    // 2. 初始化 Nacos 客户端
    nacosClient := initNacosClient(cfg.NacosServer)

    // 3. 拉取 RoutingRulesConfig（初始化）
    routingConfig := fetchRoutingConfig(nacosClient, cfg.NacosServer.DataId, cfg.NacosServer.Group)

    // 4. 启动 Nacos 监听器（前提：auto_update=true）
    if routingConfig.Settings.AutoUpdate {
        nacosManager.StartListening(cfg.NacosServer.DataId, cfg.NacosServer.Group)
    }

    // 5. 启动 API 服务器
    startAPIServer(cfg.APIServer, routingConfig)
}
```

---

## 数据关系图

```
┌──────────────────────────────────────────────────────┐
│              RoutingRulesConfig                      │
│  (Nacos 配置中心存储)                                │
├──────────────────────────────────────────────────────┤
│                                                      │
│  ├─ to_door (RoutingRuleSet)                        │
│  │  └─ [RoutingRule]* (Domain/IP 规则)              │
│  │     ├─ *.google.com                              │
│  │     └─ 10.0.0.0/8                                │
│  │                                                  │
│  ├─ black_list (RoutingRuleSet)                     │
│  │  └─ [RoutingRule]* (已禁用)                      │
│  │                                                  │
│  ├─ none_lane (RoutingRuleSet)                      │
│  │  └─ [RoutingRule]* (Domain/GeoIP 规则)           │
│  │     ├─ api.internal.com                          │
│  │     └─ CN (GeoIP)                                │
│  │                                                  │
│  └─ settings (RulesSettings)                        │
│     ├─ none_lane_enabled: true                      │
│     ├─ door_enabled: true                           │
│     ├─ geoip_enabled: false                         │
│     └─ auto_update: true ◄─── 控制 Nacos 同步      │
│                                                      │
└──────────────────────────────────────────────────────┘
         ▲
         │ (Nacos 监听/拉取)
         │
┌────────┴──────────────────────────┐
│       Config                      │
│  (应用启动时读取)                  │
├──────────────────────────────────┤
│ - APIServer (host:port)          │
│ - NacosServer (host:port)        │
│ - 用于建立 Nacos 连接            │
└──────────────────────────────────┘
```

---

## 类型定义总结

| 类型 | 位置 | 责任 | 依赖 |
|------|------|------|------|
| **RoutingRule** | common/model | 单条规则的定义和验证 | 无 |
| **RoutingRuleSet** | common/model | 同类规则的分组管理 | RoutingRule |
| **RulesSettings** | common/model | 全局开关和自动更新控制 | 无 |
| **RoutingRulesConfig** | common/model | 完整路由配置的容器 | RoutingRuleSet, RulesSettings |
| **Config** | processor/config | 应用启动配置 | 无 |

---

## 数据流转

```
┌─ 应用启动 ─────────────────────────────────┐
│                                            │
│  1. 读取 config.yaml                      │
│     ↓                                     │
│  2. 初始化 Nacos 客户端                    │
│     ↓                                     │
│  3. GetConfig(dataId, group)              │
│     ↓                                     │
│  4. 解析为 RoutingRulesConfig             │
│     ↓                                     │
│  5. 如果 auto_update=true，ListenConfig() │
│     ↓                                     │
│  6. 启动 API 服务器                       │
│                                            │
└────────────────────────────────────────────┘

┌─ 用户修改配置 ──────────────────────────────┐
│                                             │
│  1. 用户操作 Web UI                         │
│     ↓                                      │
│  2. POST /config/routing                   │
│     ↓                                      │
│  3. 服务器：验证配置                        │
│     ↓                                      │
│  4. 自动：auto_update = false              │
│     ↓                                      │
│  5. 保存到本地文件                         │
│     ↓                                      │
│  6. 返回成功响应                           │
│                                             │
└─────────────────────────────────────────────┘

┌─ Nacos 配置更新（仅当 auto_update=true） ──┐
│                                             │
│  1. Nacos 服务器配置变更                   │
│     ↓                                      │
│  2. 监听回调被触发                         │
│     ↓                                      │
│  3. 检查 auto_update 标志                  │
│     - false: 忽略更新                      │
│     - true: 解析新配置                     │
│     ↓                                      │
│  4. 更新本地 RoutingRulesConfig            │
│     ↓                                      │
│  5. 更新时间戳 last_nacos_sync             │
│                                             │
└─────────────────────────────────────────────┘
```

---

## 版本控制策略

### RoutingRulesConfig 版本

- **version** 字段存储配置版本号
- 每次配置修改时，version += 1
- Nacos 配置版本通常由 Nacos 服务器自动管理，本地版本号用于调试和审计

### 兼容性说明

- ⚠️ Phase 3 采用直接替换策略，不保持向后兼容
- 升级时需要：
  1. 备份旧的 RoutingRules 配置
  2. 手动或通过脚本迁移到新的 RoutingRulesConfig 格式
  3. 上传新配置到 Nacos
  4. 重启应用

---

## 验证与测试

### 单元测试范围

```go
// RoutingRule 验证
- TestRoutingRuleValidate_Domain_Valid
- TestRoutingRuleValidate_Domain_InvalidFormat
- TestRoutingRuleValidate_IP_Valid
- TestRoutingRuleValidate_IP_InvalidCIDR
- TestRoutingRuleValidate_GeoIP_Valid
- TestRoutingRuleValidate_GeoIP_InvalidCode

// RoutingRuleSet 验证
- TestRoutingRuleSetValidate_AllRulesValid
- TestRoutingRuleSetValidate_TooManyRules
- TestRoutingRuleSetValidate_InvalidRule

// RulesSettings 验证
- TestRulesSettingsValidate_DefaultValues
- TestRulesSettingsValidate_AutoUpdateToggle

// RoutingRulesConfig 验证
- TestRoutingRulesConfigValidate_Complete
- TestRoutingRulesConfigValidate_Serialization
```

---

**数据模型版本**: 1.0
**最后更新**: 2025-12-17
**参考文档**: [spec.md](./spec.md), [research.md](./research.md)
