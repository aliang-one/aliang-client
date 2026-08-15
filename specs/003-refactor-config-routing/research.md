# 技术研究: 配置系统重构与路由引擎迁移

**Feature**: 003-refactor-config-routing
**Phase**: 0 - Research
**Date**: 2025-12-17

## 研究目标

基于 `plan.md` 中的技术背景，本研究旨在：

1. 验证 Nacos SDK (nacos-sdk-go v1.1.6) 的配置监听最佳实践
2. 评估 Go 语言生态中的 GeoIP 库选项
3. 研究路由决策引擎的设计模式
4. 分析配置同步与状态管理策略

## 技术未知项

从 `plan.md` 技术背景分析，当前没有标记为 NEEDS CLARIFICATION 的项目。所有关键技术点已确定：

- ✅ 语言/版本: Go 1.19+
- ✅ 主要依赖: Nacos SDK v1.1.6, GeoIP 库, Bootstrap 5
- ✅ 存储: 本地文件 + Nacos远程配置
- ✅ 测试: Go testing framework
- ✅ 性能目标: 已明确量化

**研究重点**: 聚焦于**实现最佳实践**而非技术选型。

---

## 研究任务

### 1. Nacos SDK 配置监听模式

#### 研究问题
- Nacos SDK (nacos-sdk-go v1.1.6) 的配置监听器生命周期管理最佳实践
- 监听回调中如何安全处理配置更新
- 如何优雅地启动/停止监听器以支持 auto_update 开关

#### 现有知识
- Nacos 提供 ConfigClient 接口，支持 `ListenConfig` 方法注册监听器
- 回调函数接收配置变更事件（dataId, group, content）
- 需要处理并发安全问题（配置更新与业务逻辑访问配置的竞争）

#### 研究成果

**最佳实践模式**:

```go
// processor/nacos/manager.go 设计方案
type ConfigManager struct {
    client      config_client.IConfigClient
    listener    *ConfigListener
    autoUpdate  bool            // auto_update 开关
    mu          sync.RWMutex    // 保护 autoUpdate 和配置读写
    stopCh      chan struct{}   // 停止信号通道
}

// 启动监听
func (m *ConfigManager) StartListening(dataId, group string) error {
    m.mu.Lock()
    defer m.mu.Unlock()

    if !m.autoUpdate {
        return fmt.Errorf("auto_update is disabled")
    }

    m.listener = &ConfigListener{
        onConfigChange: m.handleConfigChange,
    }

    return m.client.ListenConfig(config_client.ListenConfigParam{
        DataId:   dataId,
        Group:    group,
        OnChange: m.listener.OnChange,
    })
}

// 停止监听
func (m *ConfigManager) StopListening(dataId, group string) error {
    m.mu.Lock()
    defer m.mu.Unlock()

    return m.client.CancelListenConfig(config_client.CancelListenConfigParam{
        DataId: dataId,
        Group:  group,
    })
}

// 处理配置变更（线程安全）
func (m *ConfigManager) handleConfigChange(namespace, group, dataId, data string) {
    m.mu.RLock()
    if !m.autoUpdate {
        m.mu.RUnlock()
        log.Info("auto_update is disabled, ignoring Nacos config change")
        return
    }
    m.mu.RUnlock()

    // 解析新配置
    var newConfig routing_config.RoutingRulesConfig
    if err := json.Unmarshal([]byte(data), &newConfig); err != nil {
        log.Errorf("Failed to parse Nacos config: %v", err)
        return
    }

    // 原子更新全局配置（使用 sync/atomic 或写锁）
    updateGlobalConfig(&newConfig)
}
```

**决策理由**:
- 使用 `sync.RWMutex` 保护 auto_update 标志和配置读写，避免竞态条件
- 回调函数内部检查 auto_update 状态，停止监听但保持回调注册（简化生命周期）
- 使用通道 `stopCh` 实现优雅关闭，配合 context.Context 支持超时

**替代方案（已拒绝）**:
- ❌ **动态注册/取消监听器**: 每次 auto_update 切换时调用 ListenConfig/CancelListenConfig
  - 拒绝原因: Nacos SDK 的 CancelListenConfig 可能不稳定，频繁注册/取消增加复杂度
- ❌ **使用两套配置存储**: local 和 remote 分离
  - 拒绝原因: 增加状态管理复杂度，auto_update 标志已足够简洁

---

### 2. GeoIP 库选型

#### 研究问题
- Go 语言生态中哪个 GeoIP 库最适合本项目需求
- 是否使用本地数据库（MaxMind GeoLite2）还是第三方 API 服务
- 如何实现 GeoIP 查询结果缓存以满足 <10ms 性能要求

#### 评估选项

| 库/服务 | 优点 | 缺点 | 适用性 |
|---------|------|------|--------|
| **oschwald/geoip2-golang** (MaxMind GeoIP2) | 本地查询快（<1ms），无网络依赖，免费 GeoLite2 数据库 | 需定期更新数据库，初始下载约 30MB | ✅ **推荐** |
| **ip2location/ip2location-go** | 数据精度高，支持更多字段（ISP, 域名） | 商业授权费用，数据库文件更大 | ❌ 成本过高 |
| **ipinfo.io API** | 零维护，数据实时更新 | 网络延迟（50-200ms），API 调用限额 | ❌ 性能不满足 |
| **ipstack.com API** | 数据全面，支持批量查询 | 付费服务，网络依赖 | ❌ 性能不满足 |

#### 研究成果

**选择**: `oschwald/geoip2-golang` + MaxMind GeoLite2 数据库

**实现方案**:

```go
// processor/routing/geoip.go 设计方案
type GeoIPCache struct {
    db    *geoip2.Reader
    cache *lru.Cache      // LRU 缓存，最大 10000 条
    mu    sync.RWMutex
}

func NewGeoIPCache(dbPath string) (*GeoIPCache, error) {
    db, err := geoip2.Open(dbPath)
    if err != nil {
        return nil, err
    }

    cache, _ := lru.New(10000) // 10000 entries LRU cache

    return &GeoIPCache{
        db:    db,
        cache: cache,
    }, nil
}

func (g *GeoIPCache) Lookup(ip string) (string, error) {
    // 先查缓存
    if cached, ok := g.cache.Get(ip); ok {
        return cached.(string), nil
    }

    // 查询数据库
    parsedIP := net.ParseIP(ip)
    record, err := g.db.Country(parsedIP)
    if err != nil {
        return "", err
    }

    countryCode := record.Country.IsoCode
    g.cache.Add(ip, countryCode)

    return countryCode, nil
}
```

**性能测试目标**:
- 缓存命中: <0.1ms（内存查找）
- 缓存未命中: <1ms（本地数据库查询）
- 总体平均: <10ms（包含路由决策全流程）

**数据库更新策略**:
- 每月自动下载 GeoLite2-Country.mmdb（通过 cron 任务或启动检查）
- 提供手动更新 API 端点 `POST /geoip/update`

**决策理由**:
- 本地查询满足 <10ms 性能要求，网络 API 无法保证
- GeoLite2 免费且精度满足国家/地区级判断需求
- LRU 缓存进一步降低延迟，覆盖热点 IP

**替代方案（已拒绝）**:
- ❌ **Redis 外部缓存**: 拒绝原因：增加架构复杂度，本地 LRU 已足够
- ❌ **第三方 API**: 拒绝原因：网络延迟不可控，无法满足性能目标

---

### 3. 路由决策引擎设计模式

#### 研究问题
- 如何优雅实现优先级路由决策（NoneLane → Door → GeoIP → Direct）
- 规则匹配器（domain/ip/geoip）的抽象设计
- 如何支持规则启用/禁用而不删除规则

#### 设计模式研究

**Chain of Responsibility (责任链模式)** vs **Strategy Pattern (策略模式)**

| 模式 | 适用场景 | 优点 | 缺点 |
|------|----------|------|------|
| **责任链** | 多个处理器按顺序尝试处理请求 | 灵活扩展、解耦处理器 | 性能略低（链式调用） |
| **策略** | 根据条件选择一种处理策略 | 性能高、策略明确 | 新增策略需修改选择逻辑 |

#### 研究成果

**选择**: **简化责任链模式** + **First-Match-Wins 策略**

**实现方案**:

```go
// processor/routing/decision_engine.go 设计方案

type RouteDecision int

const (
    RouteToALiang  RouteDecision = iota  // NoneLane
    RouteToDoor                          // Door
    RouteDirect                          // Direct
)

type MatchContext struct {
    Domain    string
    IP        string
    Request   *http.Request
}

type RuleMatcher interface {
    Match(ctx *MatchContext, rule routing_config.RoutingRule) (bool, error)
}

// 核心决策引擎
func DecideRoute(ctx *MatchContext, config *routing_config.RoutingRulesConfig) RouteDecision {
    // 1. 检查 NoneLane 规则（优先级最高）
    if config.Settings.NoneLaneEnabled {
        for _, rule := range config.NoneLane.Rules {
            if !rule.Enabled {
                continue // 跳过禁用规则
            }
            matched, _ := matchRule(ctx, rule)
            if matched {
                return RouteToALiang
            }
        }
    }

    // 2. 检查 Door 规则
    if config.Settings.DoorEnabled {
        for _, rule := range config.ToDoor.Rules {
            if !rule.Enabled {
                continue
            }
            matched, _ := matchRule(ctx, rule)
            if matched {
                return RouteToDoor
            }
        }
    }

    // 3. 检查 GeoIP 规则（若启用）
    if config.Settings.GeoIPEnabled {
        countryCode, err := geoipCache.Lookup(ctx.IP)
        if err == nil {
            for _, rule := range config.ToDoor.Rules {
                if !rule.Enabled || rule.Type != "geoip" {
                    continue
                }
                if rule.Condition == countryCode {
                    return RouteToDoor
                }
            }
        }
    }

    // 4. 默认 Direct
    return RouteDirect
}

// 规则匹配实现
func matchRule(ctx *MatchContext, rule routing_config.RoutingRule) (bool, error) {
    switch rule.Type {
    case "domain":
        return matchDomain(ctx.Domain, rule.Condition), nil
    case "ip":
        return matchIP(ctx.IP, rule.Condition), nil
    case "geoip":
        // GeoIP 在 DecideRoute 中特殊处理
        return false, nil
    default:
        return false, fmt.Errorf("unknown rule type: %s", rule.Type)
    }
}

// Domain 匹配（支持通配符 *.google.com）
func matchDomain(domain, pattern string) bool {
    if strings.HasPrefix(pattern, "*.") {
        suffix := pattern[2:]
        return strings.HasSuffix(domain, suffix)
    }
    return domain == pattern
}

// IP 匹配（CIDR 格式）
func matchIP(ip, cidr string) bool {
    _, ipNet, err := net.ParseCIDR(cidr)
    if err != nil {
        return false
    }
    parsedIP := net.ParseIP(ip)
    return ipNet.Contains(parsedIP)
}
```

**设计亮点**:
- **首次匹配优先**: 循环遇到第一个匹配规则立即返回，避免冲突
- **规则启用/禁用支持**: 通过 `rule.Enabled` 字段控制，无需删除规则
- **全局开关分层控制**: Settings 开关在外层检查，规则启用在内层检查
- **性能优化**: 避免不必要的 GeoIP 查询（仅在 GeoIPEnabled 时调用）

**替代方案（已拒绝）**:
- ❌ **完整责任链模式**: 拒绝原因：过度抽象，增加接口复杂度，性能略低
- ❌ **优先级数值排序**: 拒绝原因：固定优先级顺序已满足需求，数值排序增加配置复杂度

---

### 4. 配置同步与状态管理

#### 研究问题
- 如何简洁实现 API 触发式修改检测
- auto_update 标志的持久化存储方案
- Nacos 故障与用户修改的统一处理机制

#### 研究成果

**状态管理架构**:

```
┌──────────────────────────────────────────────────────────┐
│                    ConfigState                           │
│  ┌────────────────────────────────────────────────────┐  │
│  │ RoutingRulesConfig (from Nacos or local)           │  │
│  │  - to_door: RuleSet                                │  │
│  │  - black_list: RuleSet                             │  │
│  │  - none_lane: RuleSet                              │  │
│  │  - settings: RulesSettings                         │  │
│  │    - geoip_enabled: bool                           │  │
│  │    - none_lane_enabled: bool                       │  │
│  │    - auto_update: bool  ◄──────┐                   │  │
│  └────────────────────────────────────────────────────┘  │
│                                                           │
│  Modification Detection:                                 │
│   - API Trigger: POST /config/routing → auto_update=false│
│   - Manual Reset: PUT /config/routing/auto-update        │
│                                                           │
│  Nacos Sync Logic:                                       │
│   - if auto_update=true:  Nacos change → update local   │
│   - if auto_update=false: Nacos change → ignore         │
└──────────────────────────────────────────────────────────┘
```

**实现方案**:

```go
// processor/api/config_handler.go 设计方案

// 保存路由配置（API 触发点）
func (h *ConfigHandler) SaveRoutingConfig(c *gin.Context) {
    var newConfig routing_config.RoutingRulesConfig
    if err := c.BindJSON(&newConfig); err != nil {
        c.JSON(400, gin.H{"error": err.Error()})
        return
    }

    // 关键步骤: 自动设置 auto_update = false
    newConfig.Settings.AutoUpdate = false

    // 保存到本地
    if err := saveToLocal(&newConfig); err != nil {
        c.JSON(500, gin.H{"error": err.Error()})
        return
    }

    // 停止 Nacos 监听（可选，回调中检查 auto_update 已足够）
    // nacosManager.StopListening(dataId, group)

    c.JSON(200, gin.H{"status": "success", "auto_update": false})
}

// 手动启用 auto_update
func (h *ConfigHandler) EnableAutoUpdate(c *gin.Context) {
    // 1. 从 Nacos 拉取最新配置
    latestConfig, err := nacosManager.GetConfig(dataId, group)
    if err != nil {
        c.JSON(500, gin.H{"error": err.Error()})
        return
    }

    // 2. 覆盖本地配置
    latestConfig.Settings.AutoUpdate = true
    if err := saveToLocal(&latestConfig); err != nil {
        c.JSON(500, gin.H{"error": err.Error()})
        return
    }

    // 3. 重新启动监听（可选）
    // nacosManager.StartListening(dataId, group)

    c.JSON(200, gin.H{"status": "success", "auto_update": true})
}
```

**auto_update 持久化方案**:
- 将 auto_update 作为 RoutingRulesConfig.Settings 的一部分持久化到本地文件
- 本地文件路径: `~/.nonelane/routing_config_local.json`
- Nacos 配置不包含 auto_update 字段（仅存储规则，auto_update 是本地状态）

**决策理由**:
- **API 触发检测简化**: 无需文件监控或哈希比较，任何 POST /config/routing 调用即触发
- **auto_update 作为本地状态**: Nacos 配置只存储规则数据，auto_update 标志是客户端的同步控制状态
- **统一故障与修改处理**: 无论 Nacos 故障还是用户修改，都通过 auto_update 标志决定是否同步

**替代方案（已拒绝）**:
- ❌ **文件哈希比较**: 拒绝原因：增加复杂度，API 触发已足够准确
- ❌ **时间戳优先**: 拒绝原因：需要维护本地和远程时间戳，auto_update 布尔标志更简洁
- ❌ **三态标志（auto/manual/hybrid）**: 拒绝原因：两态（true/false）已覆盖所有场景

---

## 技术栈最终确认

| 组件 | 技术选型 | 版本 | 理由 |
|------|----------|------|------|
| **配置中心** | Nacos (nacos-sdk-go) | v1.1.6 | 已在项目中使用，稳定可靠 |
| **GeoIP 库** | oschwald/geoip2-golang | v1.9.0+ | 本地查询快（<1ms），免费 GeoLite2 数据库 |
| **LRU 缓存** | hashicorp/golang-lru | v2.0.0+ | 成熟的 LRU 实现，线程安全 |
| **HTTP 路由** | gin-gonic/gin | v1.9.0+ | 项目现有框架，性能优秀 |
| **JSON 解析** | encoding/json | 标准库 | 满足需求，无需第三方库 |
| **测试框架** | testing + testify | 标准库 + v1.8.0+ | 断言库增强可读性 |

---

## 性能预估

基于研究成果和设计方案，预估性能指标：

| 指标 | 目标 | 预估 | 达成信心 |
|------|------|------|----------|
| **路由决策延迟** | <10ms (99th) | 2-5ms | ✅ 高 |
| **GeoIP 查询（缓存命中）** | N/A | <0.1ms | ✅ 高 |
| **GeoIP 查询（缓存未命中）** | N/A | <1ms | ✅ 高 |
| **Nacos 监听初始化** | <5s | 1-3s | ✅ 高 |
| **配置修改 API 响应** | <500ms | 50-200ms | ✅ 高 |

**性能瓶颈分析**:
- **GeoIP 数据库加载**: 30MB 文件初始加载约 100-200ms（启动时一次性）
- **规则匹配循环**: 最坏情况遍历所有规则（预计 10-100 条），正则匹配约 0.01ms/条
- **Nacos 网络延迟**: 初始化连接依赖 Nacos 服务器响应时间（1-3s）

**优化建议**:
- 使用 goroutine 并发初始化 GeoIP 和 Nacos 监听器
- 规则数量超过 1000 条时考虑索引优化（Trie 树或哈希表）

---

## 风险与缓解措施

| 风险 | 影响 | 缓解措施 |
|------|------|----------|
| **GeoLite2 数据库过期** | GeoIP 判断不准确 | 每月自动更新数据库，提供手动更新 API |
| **Nacos SDK 版本不兼容** | 监听器失败 | 单元测试覆盖监听器生命周期，提供降级方案（仅使用本地配置） |
| **auto_update 标志持久化失败** | 配置同步混乱 | 写入失败时立即返回错误，事务性保存（原子写入临时文件 + 重命名） |
| **规则数量过多导致性能下降** | 路由决策超时 | 限制规则数量上限（每类 1000 条），提供性能监控指标 |

---

## 下一步行动

1. ✅ **研究阶段完成** - 所有技术选型和设计模式已确认
2. ➡️ **进入 Phase 1**: 生成 data-model.md 和 API contracts
3. 📋 **待办任务**:
   - data-model.md: 详细定义 5 个核心实体的字段、关系、验证规则
   - contracts/: 生成 OpenAPI 规范文件（config-api, rules-api, nacos-api）
   - quickstart.md: 开发者快速上手指南

---

**研究负责人**: Claude Code Agent
**审核状态**: 待用户确认
**参考文档**: [spec.md](./spec.md), [plan.md](./plan.md)
