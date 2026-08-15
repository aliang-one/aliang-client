# 研究文档: 仪表板重构与流量监控优化

**日期**: 2025-12-17
**功能**: 仪表板重构与流量监控优化 (`002-refactor-dashboard-traffic`)
**状态**: 完成

---

## 技术决策与论证

### 1. 规则引擎配置模型设计

**决策**: 在`common/model`中使用Go结构体定义统一的RoutingRulesConfig，支持三大类规则和四种判断方式

**理由**:
- 当前系统存在Nacos和打包默认两套不兼容的配置格式，造成维护混乱
- 统一模型便于Nacos配置管理和前端展示
- Go结构体支持JSON/YAML序列化，与Nacos原生兼容

**实现方案**:
```go
// common/model/routing_config.go

type RoutingRulesConfig struct {
    ToDoor RoutingRuleSet      `json:"to_door"`
    BlackList RoutingRuleSet   `json:"black_list"`
    NoneLane RoutingRuleSet    `json:"none_lane"`
    Settings RulesSettings     `json:"settings"`
}

type RoutingRuleSet struct {
    Rules []RoutingRule `json:"rules"`
}

type RoutingRule struct {
    ID string           `json:"id"`
    Type RuleType       `json:"type"`      // Domain, IP, GeoIP
    Condition string    `json:"condition"` // 匹配条件
    Enabled bool        `json:"enabled"`   // 是否启用
    CreatedAt time.Time `json:"created_at"`
}

type RulesSettings struct {
    GeoIPEnabled bool   `json:"geoip_enabled"`
    NoneLaneEnabled bool `json:"none_lane_enabled"`
}
```

**选项评估**:
- ❌ 保持现有两套配置: 维护成本高，容易出现不一致
- ❌ 在processor/config中定义: 应当通过common/model暴露给所有组件
- ✅ 统一在common/model: 最佳实践，便于共享和维护

---

### 2. 流量统计多时间尺度缓存实现

**决策**: 在后端`processor/stats`中实现多时间尺度缓存，支持1秒、5秒、15秒三个维度，每个维度最多300条记录

**理由**:
- 后端缓存避免重复计算，减少CPU消耗
- 300条记录 × 3个维度 ≈ 900条(约100KB内存)，内存开销可控
- 三个时间尺度满足从实时到分钟级的监控需求

**实现方案**:
```go
// processor/stats/collector.go

type TrafficStats struct {
    Timestamp int64  `json:"timestamp"`
    ActiveConnections int32 `json:"active_connections"`
    UploadBytes uint64     `json:"upload_bytes"`
    DownloadBytes uint64   `json:"download_bytes"`
}

type StatsCollector struct {
    cache1s   ringbuffer.RingBuffer[TrafficStats]   // 1秒级, 容量300
    cache5s   ringbuffer.RingBuffer[TrafficStats]   // 5秒级, 容量300
    cache15s  ringbuffer.RingBuffer[TrafficStats]   // 15秒级, 容量300
    mu sync.RWMutex
}

func (c *StatsCollector) Collect(interval time.Duration) {
    // 后台goroutine，按interval定期统计
}
```

**缓存策略**:
- 使用环形缓冲区(ring buffer)实现固定大小FIFO
- 1秒级: 5分钟历史(300s)
- 5秒级: 25分钟历史(300×5s)
- 15秒级: 75分钟历史(300×15s)

**选项评估**:
- ❌ 前端实时计算: 浪费带宽，图表闪烁
- ❌ 单一时间尺度: 不能满足灵活的监控需求
- ✅ 多时间尺度后端缓存: 最优方案，兼具准确性和性能

---

### 3. 前端页面合并与布局优化

**决策**: 使用Bootstrap标签页(Tabs)或手风琴(Accordion)合并"代理管理"和"运行控制"；使用CSS Flexbox/Grid优化仪表板布局

**理由**:
- 标签页是现代Web应用的标准模式，用户习惯好
- 现有代码已使用Bootstrap 5.x，保持一致
- Flexbox/Grid支持响应式设计，适应不同屏幕

**实现方案**:
- 新增"代理管理"页面，合并当前Run Control和Proxy Management
- 修改Dashboard布局：8个关键指标占≤30%，流量监控占≥50%
- 其余空间用于其他信息或留白

**布局示例**:
```html
<!-- Dashboard -->
<div class="dashboard">
  <!-- 关键指标 (≤30%) -->
  <div class="metrics-section" style="height: 30%;">
    <!-- 8个指标卡片 -->
  </div>

  <!-- 流量监控 (≥50%) -->
  <div class="traffic-section" style="height: 50%;">
    <!-- 实时流量图表 -->
  </div>

  <!-- 其他内容 (≤20%) -->
  <div class="other-section" style="height: 20%;">
    <!-- 补充信息 -->
  </div>
</div>
```

**选项评估**:
- ❌ 打开新窗口: 影响用户体验，难以协作编辑
- ❌ 分割屏幕: 交互复杂，不适合管理界面
- ✅ 单页标签页: 简洁、高效、符合现代Web设计

---

### 4. 实时流量数据前端展示策略

**决策**: 前端每秒刷新一次，从后端获取当前时间维度的全量统计数据(300条)，直接渲染而不是追加

**理由**:
- 后端已缓存300条记录，直接返回避免前端复杂逻辑
- 一次性渲染性能优于增量追加(减少DOM操作)
- 5秒、1秒、15秒维度切换时无需额外处理，直接切换数据源

**实现方案**:
```javascript
// app/website/assets/app.js

async function refreshTrafficStats() {
    const timescale = getCurrentTimescale(); // "1s" | "5s" | "15s"
    const response = await fetch(`/api/stats/${timescale}`);
    const data = await response.json();

    // data = { stats: [...300 items], active_connections: N }
    // 直接替换图表数据，chart.js处理渲染
    renderChart(data.stats);
    updateConnectionInfo(data.active_connections);
}

setInterval(refreshTrafficStats, 1000); // 每秒刷新
```

**数据流**:
1. 前端: setInterval每秒调用一次API
2. 后端: 返回当前时间维度的缓存数据(300条)
3. 前端: chart.js重新渲染(增量更新优化由chart库处理)

**选项评估**:
- ❌ 前端维护增量追加: 数据同步复杂，容易出现偏差
- ❌ WebSocket推送: 额外的连接管理成本
- ✅ 每秒轮询获取全量数据: 简单可靠，充分满足需求

---

### 5. 规则优先级与冲突解决

**决策**: 定义明确的规则判断优先级：域名判断 > GeoIP判断；非启用的规则直接跳过

**理由**:
- 优先级决定了流量路由的确定性行为
- GeoIP判断更通用，域名判断更精确，精确优先符合直观
- 功能开关(启用/禁用)提供灵活性，关闭时零成本

**实现方案**:
```go
// processor/rules/engine.go

func (e *RulesEngine) EvaluateRoute(traffic *Traffic) ProxyType {
    // 1. 检查域名规则(D代理) - 优先级1
    if e.config.Settings.GeoIPEnabled {
        for _, rule := range e.config.ToDoor.Rules {
            if !rule.Enabled {
                continue
            }
            if rule.Type == RuleDomain && matchDomain(traffic, rule) {
                return ProxyToDoor
            }
        }
    }

    // 2. 检查GeoIP规则(D代理) - 优先级2
    if e.config.Settings.GeoIPEnabled {
        for _, rule := range e.config.ToDoor.Rules {
            if !rule.Enabled {
                continue
            }
            if rule.Type == RuleGeoIP && matchGeoIP(traffic, rule) {
                return ProxyToDoor
            }
        }
    }

    // 3. 检查黑名单规则 - 独立
    for _, rule := range e.config.BlackList.Rules {
        if !rule.Enabled {
            continue
        }
        if rule.Type == RuleIP && matchIP(traffic, rule) {
            return ProxyDirect // 黑名单直连
        }
    }

    // 4. 检查NoneLane规则 - 优先级3
    if e.config.Settings.NoneLaneEnabled {
        for _, rule := range e.config.NoneLane.Rules {
            if !rule.Enabled {
                continue
            }
            if rule.Type == RuleDomain && matchDomain(traffic, rule) {
                return ProxyNoneLane
            }
        }
    }

    return ProxyDirect // 默认直连
}
```

**冲突处理示例**:
- 流量同时匹配域名和GeoIP: 使用域名结果
- NoneLane配置为空: 使用direct代理(默认行为)
- GeoIP/NoneLane禁用: 跳过判断分支

**选项评估**:
- ❌ 随机选择: 不可预测，难以维护
- ❌ 所有规则都有相同优先级: 配置冲突难以解决
- ✅ 清晰的优先级顺序: 可预测，易于维护和测试

---

### 6. Nacos配置与业务模型的同步

**决策**: 业务模型定义在`common/model`中，Nacos存储的是该模型的JSON表示；前端展示和编辑时直接使用该模型

**理由**:
- 单一真实来源(SSOT)原则
- JSON格式与Go结构体的自动映射(encoding/json)
- 前端直接从API获取Nacos中存储的模型

**实现方案**:
```
后端流程:
1. common/model定义RoutingRulesConfig结构
2. 启动时从Nacos加载JSON配置，反序列化为RoutingRulesConfig
3. API端点返回该结构的JSON表示
4. 配置变更时，序列化后写入Nacos

前端流程:
1. GET /api/config/routing → 获取RoutingRulesConfig JSON
2. 用户编辑配置(UI组件生成JSON)
3. POST /api/config/routing + JSON → 后端写入Nacos
```

**Nacos配置示例**:
```json
{
  "to_door": {
    "rules": [
      {
        "id": "rule_1",
        "type": "domain",
        "condition": "*.example.com",
        "enabled": true,
        "created_at": "2025-12-17T00:00:00Z"
      }
    ]
  },
  "black_list": {
    "rules": [
      {
        "id": "rule_bl_1",
        "type": "ip",
        "condition": "192.168.0.0/16",
        "enabled": true,
        "created_at": "2025-12-17T00:00:00Z"
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

**选项评估**:
- ❌ 保持Nacos与前端直接通信: 架构混乱，难以集中控制
- ❌ 后端使用不同的内部模型: 增加转换成本和出错风险
- ✅ 统一模型通过API暴露: 清晰的架构、易于维护

---

## 总结与建议

### 已解决的关键问题

1. ✅ **配置模型统一**: 使用`common/model/RoutingRulesConfig`统一所有配置
2. ✅ **流量统计性能**: 后端多时间尺度缓存，前端轮询拉取
3. ✅ **前端布局**: Bootstrap标签页合并页面，Flexbox优化布局
4. ✅ **规则优先级**: 明确的判断顺序和冲突解决机制
5. ✅ **Nacos集成**: 模型驱动的配置管理

### 风险与缓解

| 风险 | 影响 | 缓解措施 |
|------|------|----------|
| 现有配置格式变更 | 用户配置不兼容 | 提供迁移脚本，记录在文档中 |
| 后端缓存内存溢出 | OOM问题 | 限制300条上限，监控内存使用 |
| 规则判断延迟> 200ms | 影响流量路由 | 优化判断逻辑，添加性能测试 |
| 前端刷新频率过高 | 页面卡顿 | 限制为每秒最多1次，使用防抖 |

### 后续实施步骤(在Plan阶段2处理)

1. 创建`common/model/routing_config.go`和模型
2. 创建`processor/stats`包实现流量统计
3. 创建后端API端点: `/api/config/routing`, `/api/stats/{timescale}`
4. 修改前端HTML/JS实现新的页面结构和实时监控
5. 编写单元测试和集成测试
6. 生成API文档和迁移指南
