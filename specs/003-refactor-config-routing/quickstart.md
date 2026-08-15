# 快速开始指南: 配置系统重构与路由引擎迁移

**Feature**: 003-refactor-config-routing
**Phase**: 1 - Design
**Date**: 2025-12-17

## 概述

本指南为开发者提供快速入门路径，详细展示如何理解、集成和测试新的路由配置系统。

---

## 第1步：理解数据模型

### 关键概念（5分钟）

新的配置系统围绕 **RoutingRulesConfig** 展开，包含三大组件：

#### 1. RoutingRule（单条路由规则）
```json
{
  "id": "rule_domain_1703001234",      // 唯一标识
  "type": "domain",                     // domain | ip | geoip
  "condition": "*.google.com",          // 匹配条件
  "enabled": true,                      // 启用/禁用开关
  "created_at": "2025-12-17T10:00:00Z",
  "updated_at": "2025-12-17T10:00:00Z",
  "description": "Google services"       // 可选说明
}
```

**支持的规则类型**：
- **domain**: 域名匹配（支持通配符 `*.example.com`）
- **ip**: IP段匹配（CIDR格式 `192.168.0.0/16`）
- **geoip**: 地理位置匹配（ISO 3166-1 alpha-2代码 `US`, `CN`）

#### 2. RoutingRuleSet（规则集合）
```json
{
  "set_type": "to_door",        // to_door | black_list | none_lane
  "rules": [...],               // RoutingRule 数组
  "count": 2,                   // 规则数量（自动计算）
  "updated_at": "2025-12-17T10:30:00Z"
}
```

三个规则集的用途：
- **to_door**: Door代理规则（命中则走Door）
- **none_lane**: NoneLane规则（命中则走NoneLane）
- **black_list**: 黑名单规则（保留，当前未使用）

#### 3. RulesSettings（全局开关）
```json
{
  "none_lane_enabled": true,    // NoneLane规则是否启用
  "door_enabled": true,         // Door规则是否启用
  "geoip_enabled": false,       // GeoIP规则是否启用
  "auto_update": true,          // Nacos自动同步开关（关键！）
  "updated_at": "2025-12-17T10:30:00Z",
  "last_nacos_sync": "2025-12-17T10:30:00Z"
}
```

**最重要的字段：auto_update**
- `true`: 本地配置与Nacos自动同步
- `false`: 忽略Nacos更新，保持本地配置（用户修改后自动设置）

#### 4. RoutingRulesConfig（完整配置）
```json
{
  "to_door": {...},             // Door规则集
  "black_list": {...},          // 黑名单规则集
  "none_lane": {...},           // NoneLane规则集
  "settings": {...},            // 全局开关
  "version": 5,                 // 配置版本号
  "created_at": "2025-12-17T08:00:00Z",
  "updated_at": "2025-12-17T10:30:00Z"
}
```

---

## 第2步：API端点快速参考

### 配置管理 (config-api)

**获取当前配置**：
```bash
GET /api/v1/config/routing

响应：RoutingRulesConfig
```

**保存修改配置**：
```bash
POST /api/v1/config/routing
Content-Type: application/json

请求体：修改后的 RoutingRulesConfig

响应：
{
  "status": "success",
  "auto_update": false,          // 自动设置为 false!
  "saved_at": "2025-12-17T10:30:00Z"
}
```

**启用自动同步**（手动恢复Nacos同步）：
```bash
PUT /api/v1/config/routing/auto-update

响应：
{
  "status": "success",
  "auto_update": true,
  "synced_at": "2025-12-17T10:35:00Z",
  "config": {...}               // 从Nacos拉取的最新配置
}
```

### 规则引擎 (rules-api)

**查询规则引擎状态**：
```bash
GET /api/v1/rules/engine/status

响应：引擎健康状态、开关状态、缓存统计
```

**切换单个规则启用/禁用**：
```bash
PUT /api/v1/config/routing/rules/{ruleId}/toggle

响应：更新后的 RoutingRule
```

**查询GeoIP位置**（用于测试）：
```bash
POST /api/v1/rules/geoip/lookup
Content-Type: application/json

{
  "ip": "203.0.113.42"
}

响应：
{
  "ip": "203.0.113.42",
  "country_code": "US",
  "country_name": "United States",
  "cached": false,
  "lookup_time_ms": 0.5
}
```

### Nacos集成 (nacos-api)

**检查Nacos连接**：
```bash
GET /api/v1/nacos/health

响应：连接状态、延迟、错误信息
```

**查看同步状态**：
```bash
GET /api/v1/nacos/sync/status

响应：
{
  "auto_update_enabled": true,
  "local_version": 5,
  "nacos_version": 6,
  "in_sync": false,
  "sync_needed": true
}
```

**手动同步**（一次性从Nacos拉取）：
```bash
POST /api/v1/nacos/sync/manual

响应：
{
  "status": "success",
  "synced_at": "2025-12-17T11:00:00Z",
  "version": 6,
  "changes_applied": true
}
```

---

## 第3步：路由决策引擎工作原理

### 优先级逻辑

路由引擎按以下优先级顺序检查规则，**首次匹配即返回**：

```
1. NoneLane 规则 (none_lane_enabled=true)
   ├─ 检查Domain规则
   └─ 匹配 → 返回 NoneLane

2. Door 规则 (door_enabled=true)
   ├─ 检查Domain规则
   ├─ 检查IP规则
   └─ 匹配 → 返回 Door

3. GeoIP 规则 (geoip_enabled=true)
   ├─ 查询请求IP的地理位置
   └─ 匹配 → 返回 Door

4. 默认路由
   └─ 都不匹配 → 返回 Direct
```

### 具体例子

**配置**：
```json
{
  "none_lane": {
    "rules": [
      { "id": "r1", "type": "domain", "condition": "*.internal.com", "enabled": true }
    ]
  },
  "to_door": {
    "rules": [
      { "id": "r2", "type": "domain", "condition": "*.google.com", "enabled": true },
      { "id": "r3", "type": "ip", "condition": "10.0.0.0/8", "enabled": true }
    ]
  },
  "settings": {
    "none_lane_enabled": true,
    "door_enabled": true,
    "geoip_enabled": false
  }
}
```

**请求场景**：
- 请求 `api.internal.com` → 匹配r1 → **NoneLane** ✓
- 请求 `api.google.com` → 不匹配r1 → 匹配r2 → **Door** ✓
- 请求 `192.168.1.1` （来自10.0.0.5） → 不匹配r1、r2 → 匹配r3 → **Door** ✓
- 请求 `example.com` → 都不匹配 → **Direct** ✓

### 全局开关的影响

**如果 none_lane_enabled=false**：
```
跳过步骤1，直接从步骤2开始检查Door规则
```

**如果 geoip_enabled=false**：
```
跳过步骤3，不进行GeoIP查询
```

**如果 door_enabled=false + none_lane_enabled=false**：
```
跳过步骤1和2，直接返回 Direct（所有流量绕过代理）
```

---

## 第4步：Nacos自动同步机制

### auto_update 标志的两个状态

| 状态 | auto_update | 行为 | 触发条件 |
|------|-------------|------|----------|
| **同步模式** | true | 监听Nacos配置变更，自动应用到本地 | 启动时默认 |
| **本地编辑模式** | false | 忽略Nacos更新，保持本地配置 | 用户通过API修改配置后自动设置 |

### 完整的工作流

#### 场景1：正常启动（auto_update=true）
```
1. 应用启动
   ↓
2. 读取 Config 中的 NacosServer 信息
   ↓
3. 初始化 Nacos 客户端
   ↓
4. GetConfig(dataId, group) → 获取 RoutingRulesConfig
   ↓
5. 启动监听器 ListenConfig(dataId, group)
   ↓
6. Nacos服务器配置变更
   ↓
7. 监听回调被触发，自动更新本地配置 ✓
```

#### 场景2：用户修改配置（自动设置 auto_update=false）
```
1. 用户通过Web UI修改规则
   ↓
2. 前端调用 POST /api/v1/config/routing
   ↓
3. 后端自动：
   - 保存修改到本地文件
   - 设置 auto_update = false
   - 停止Nacos监听（可选，回调中会检查auto_update）
   ↓
4. 返回成功响应，UI显示"本地编辑模式"
   ↓
5. 即使Nacos服务器配置变更，本地也不同步
   ↓
6. 用户本地修改被永久保留 ✓
```

#### 场景3：用户手动启用自动同步（auto_update=false → true）
```
1. 用户在UI上点击"启用自动同步"按钮
   ↓
2. 前端调用 PUT /api/v1/config/routing/auto-update
   ↓
3. 后端：
   - GetConfig(dataId, group) → 从Nacos拉取最新配置
   - 覆盖本地配置为Nacos版本
   - 设置 auto_update = true
   - 恢复Nacos监听器
   ↓
4. 返回成功响应 + 新配置内容
   ↓
5. 本地编辑被丢弃，Nacos配置生效
   ↓
6. 恢复自动同步模式 ✓
```

#### 场景4：Nacos故障然后恢复
```
Nacos故障期间：
  - auto_update=true: 继续使用本地缓存配置（无影响）
  - auto_update=false: 继续使用本地配置（预期行为）

Nacos恢复后：
  - 如果auto_update=true: 立即同步，覆盖本地为Nacos版本 ✓
  - 如果auto_update=false: 不同步，保持本地配置不变 ✓
```

---

## 第5步：开发和测试

### 本地测试流程

#### 1. 测试路由决策引擎

```bash
# 获取当前配置
curl http://localhost:56431/api/v1/config/routing | jq

# 响应应该包含三个规则集和全局开关
```

#### 2. 测试修改配置（自动停止Nacos同步）

```bash
# 获取当前配置
CONFIG=$(curl -s http://localhost:56431/api/v1/config/routing)

# 修改某个规则（例如启用GeoIP）
MODIFIED=$(echo "$CONFIG" | jq '.settings.geoip_enabled = true')

# 保存修改
curl -X POST http://localhost:56431/api/v1/config/routing \
  -H "Content-Type: application/json" \
  -d "$MODIFIED" | jq

# 检查 auto_update 是否变为 false
curl http://localhost:56431/api/v1/nacos/sync/status | jq '.auto_update_enabled'
# 预期输出: false
```

#### 3. 测试GeoIP查询

```bash
# 查询某个IP的地理位置
curl -X POST http://localhost:56431/api/v1/rules/geoip/lookup \
  -H "Content-Type: application/json" \
  -d '{"ip":"203.0.113.42"}' | jq

# 响应应该包含 country_code 和 cached 状态
```

#### 4. 测试手动启用自动同步

```bash
# 当前处于本地编辑模式 (auto_update=false)

# 手动启用自动同步
curl -X PUT http://localhost:56431/api/v1/config/routing/auto-update | jq

# 响应应该：
# 1. auto_update = true
# 2. config = Nacos中的最新配置（可能不同于本地编辑版本）
# 3. synced_at = 当前时间戳
```

#### 5. 测试规则启用/禁用

```bash
# 禁用特定规则（不删除，只是禁用）
RULE_ID="rule_domain_1703001234"

curl -X PUT http://localhost:56431/api/v1/config/routing/rules/$RULE_ID/toggle | jq

# 该规则的 enabled 字段应该从 true 变为 false
```

#### 6. 测试Nacos连接状态

```bash
# 检查Nacos健康状态
curl http://localhost:56431/api/v1/nacos/health | jq

# 检查同步状态
curl http://localhost:56431/api/v1/nacos/sync/status | jq

# 查看监听器状态
curl http://localhost:56431/api/v1/nacos/listener/status | jq
```

### 单元测试重点

基于 spec.md 中的测试策略，重点测试以下场景：

#### 路由决策逻辑测试
```go
// 示例：NoneLane规则优先级测试
func TestRouterDecision_NoneLaneHighestPriority(t *testing.T) {
    config := &RoutingRulesConfig{
        NoneLane: {
            Rules: []RoutingRule{
                {ID: "r1", Type: "domain", Condition: "*.internal.com", Enabled: true},
            },
        },
        ToDoor: {
            Rules: []RoutingRule{
                {ID: "r2", Type: "domain", Condition: "*.internal.com", Enabled: true},
            },
        },
        Settings: RulesSettings{NoneLaneEnabled: true, DoorEnabled: true},
    }

    decision := DecideRoute("api.internal.com", "10.0.0.1", config)

    // NoneLane规则应该优先匹配，即使Door规则也匹配
    assert.Equal(t, RouteToALiang, decision) // NoneLane
}
```

#### 全局开关测试
```go
// 示例：禁用NoneLane开关时应该跳过NoneLane检查
func TestGlobalSwitch_NoneLaneDisabled(t *testing.T) {
    config := &RoutingRulesConfig{
        NoneLane: {
            Rules: []RoutingRule{
                {ID: "r1", Type: "domain", Condition: "*.internal.com", Enabled: true},
            },
        },
        Settings: RulesSettings{NoneLaneEnabled: false, DoorEnabled: false},
    }

    decision := DecideRoute("api.internal.com", "10.0.0.1", config)

    // 即使NoneLane规则匹配，也应该返回Direct（因为NoneLane被禁用）
    assert.Equal(t, RouteDirect, decision)
}
```

#### auto_update状态机测试
```go
// 示例：API调用后auto_update自动设置为false
func TestAutoUpdate_DisabledAfterAPIModification(t *testing.T) {
    config := GetRoutingConfig() // 初始 auto_update=true
    config.Settings.GeoIPEnabled = true

    SaveRoutingConfig(config)

    updatedConfig := GetRoutingConfig()

    // 保存后auto_update应该自动变为false
    assert.Equal(t, false, updatedConfig.Settings.AutoUpdate)
}
```

### 集成测试重点

#### Nacos监听器生命周期测试
```go
// 示例：验证监听器正确处理auto_update切换
func TestNacosListener_RespectAutoUpdateFlag(t *testing.T) {
    // 1. 启动监听器，auto_update=true
    StartListener()
    assert.True(t, IsListenerRunning())

    // 2. 修改配置，auto_update自动变为false
    SaveModifiedConfig(...)

    // 3. Nacos配置变更，监听器回调被触发
    // 4. 回调检查auto_update标志，发现为false，忽略更新
    TriggerNacosConfigChange()

    // 本地配置应该保持不变
    assert.Equal(t, "old_config", GetLocalConfig())
}
```

---

## 第6步：常见问题

### Q: auto_update 标志保存在哪里？
**A**: auto_update 是本地状态（不存储在Nacos中）。存储在本地配置文件（通常是 `~/.nursorgate/routing_config_local.json`）。Nacos只存储规则数据。

### Q: 修改配置后无法恢复Nacos同步怎么办？
**A**: 调用 `PUT /api/v1/config/routing/auto-update` 端点重新启用自动同步。这会从Nacos拉取最新配置并覆盖本地版本。

### Q: GeoIP查询性能如何保证<10ms？
**A**:
- 本地GeoLite2数据库查询：<1ms
- LRU缓存（10,000条）：<0.1ms (命中率通常 70-80%)
- 整体路由决策：2-5ms（包含所有优先级检查）

### Q: 如果Nacos长期不可用会怎样？
**A**: 系统使用本地缓存配置继续运行，路由决策不受影响。当Nacos恢复后：
- 若 auto_update=true：自动同步Nacos最新配置
- 若 auto_update=false：保持本地配置不变

### Q: 能否手动删除规则而不是禁用？
**A**: 可以。通过配置管理API修改规则数组，直接从 to_door/none_lane/black_list 中删除规则对象即可。禁用（enabled=false）是为了临时关闭规则但保留历史记录。

### Q: Domain规则如何支持通配符？
**A**: 使用 `*.example.com` 格式。引擎会自动检测前缀为 `*.` 的域名，执行后缀匹配（suffix matching）。

- `*.google.com` 匹配 `api.google.com`, `www.google.com` 等
- `google.com` 只匹配精确的 `google.com`

---

## 第7步：下一步行动

### 为Phase 2（实现）做准备

**后端开发**：
1. 在 `processor/config/types.go` 中删除旧的 RoutingRules 字段
2. 在 `processor/routing/` 创建决策引擎（decision_engine.go, matcher.go）
3. 在 `processor/nacos/` 创建Nacos集成模块（manager.go, listener.go）
4. 在 `processor/api/` 增强配置处理器（config_handler.go）
5. 更新 `cmd/main.go` 启动流程

**测试开发**：
1. 为路由决策引擎编写单元测试（>90% 覆盖率）
2. 为全局开关编写集成测试
3. 为auto_update状态机编写测试
4. 为Nacos监听器生命周期编写测试

**前端验证**（Phase 2已完成）：
- ✅ 规则管理UI已实现
- ✅ API集成已完成
- ✅ 配置加载和保存已实现

### 参考文档

- **data-model.md**: 数据模型详细定义
- **research.md**: 技术研究和最佳实践
- **spec.md**: 完整功能规范
- **plan.md**: 实施计划和工作分解

### 联系方式

遇到问题？查看：
1. **API 文档**: 检查 `/contracts/*.openapi.yaml`
2. **数据模型**: 参考 `data-model.md`
3. **规范**: 阅读 `spec.md` 中的设计决策部分

---

**快速开始版本**: 1.0
**最后更新**: 2025-12-17
**适用对象**: 后端开发者、测试工程师、运维人员
