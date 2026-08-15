# RoutingRules 字段清理计划 (T014-T015 交付物)

**Date**: 2025-12-17
**Phase**: Phase 2 Foundation - Configuration Structure Cleanup
**Total Files to Modify**: 4 code files + 1 spec file
**Total Changes**: 11 discrete modifications required

---

## 执行摘要

经过全面扫描发现，项目中有 **4 个代码文件** 仍然引用已废弃的 `processor/config/types.go` 中的旧 `RoutingRulesConfig` 定义。新的 `common/model/routing_config.go` 中已实现了完整、规范的版本。需要系统地删除旧定义并重构所有依赖代码。

**好消息**：`app/http/handlers/config_handler.go` 已正确使用新的 `model.RoutingRulesConfig`，无需修改！

---

## 详细修改清单

### 1. processor/config/types.go (2 处修改)

**文件位置**: `/Users/mac/MyProgram/GoProgram/nursor/nursorgate2/processor/config/types.go`

#### 修改 1.1: 删除旧的 RoutingRulesConfig 定义
- **行号**: 76-103
- **内容**: 旧的 RoutingRulesConfig 结构体定义，包含：
  - `GeoIPConfig` (GeoIP 配置)
  - `BypassRulesConfig` (旁路规则)
  - `CacheConfig` (缓存配置)
  - `DNSPreResolutionConfig` (DNS 预解析)
  - 相关辅助函数 (GetDNSPreResolutionConfig, GetTimeout, GetMaxCacheTTL, GetPrimaryDNS, GetFallbackDNS, Validate)
- **操作**: 完全删除这些定义（66 行代码）
- **原因**: 这些功能已迁移到 common/model/routing_config.go 中的新模型
- **依赖**: 无其他代码直接调用这些辅助函数（除了初始化函数，已在计划中删除）

#### 修改 1.2: 删除 Config.RoutingRules 字段
- **行号**: 204
- **字段**: `RoutingRules *RoutingRulesConfig `json:"routingRules,omitempty"`
- **操作**: 从 Config 结构体中删除此字段
- **影响**: 4 个其他文件需要相应更新（见下文）
- **注意**: 删除后 Config 结构体仍包含其他必要字段（APIServer, NacosServer, CurrentProxy, BaseProxies, DoorProxy, DNSPreResolution）

---

### 2. cmd/config.go (2 处修改)

**文件位置**: `/Users/mac/MyProgram/GoProgram/nursor/nursorgate2/cmd/config.go`

#### 修改 2.1: 删除 initializeGeoIP() 函数调用
- **行号**: 117
- **当前代码**:
  ```go
  if err := initializeGeoIP(cfg.RoutingRules); err != nil {
      logger.Warn(fmt.Sprintf("Phase 5 - GeoIP initialization failed (non-fatal): %v", err))
  } else {
      logger.Debug("Phase 5: GeoIP service initialized")
  }
  ```
- **操作**: 删除这 6 行代码块
- **原因**: GeoIP 初始化将在路由引擎实现中处理（Phase 4 User Story 2）
- **替代**: 在 Phase 5 (User Story 3) 中通过 Nacos 监听器启动时初始化

#### 修改 2.2: 删除 initializeRuleEngine() 函数调用
- **行号**: 124
- **当前代码**:
  ```go
  if err := initializeRuleEngine(cfg.RoutingRules); err != nil {
      logger.Warn(fmt.Sprintf("Phase 6 - Rule engine initialization failed (non-fatal): %v", err))
  } else {
      logger.Debug("Phase 6: Rule engine initialized")
  }
  ```
- **操作**: 删除这 6 行代码块
- **原因**: 规则引擎初始化已移至 cmd/main.go 的启动流程（Phase 5）
- **时间**: 启动时应在加载 Nacos 配置后初始化

#### 修改 2.3: 删除 initializeGeoIP() 函数定义
- **行号**: 312-338 (约 27 行)
- **函数签名**: `func initializeGeoIP(routingRules *config.RoutingRulesConfig) error`
- **内容**:
  ```go
  func initializeGeoIP(routingRules *config.RoutingRulesConfig) error {
      if routingRules == nil || routingRules.GeoIP == nil {
          logger.Info("GeoIP routing not configured, service disabled")
          return nil
      }
      // ... 初始化逻辑 ...
  }
  ```
- **操作**: 完全删除整个函数
- **替代**: GeoIP 初始化逻辑应集成到 processor/routing/ 中的新模块（Phase 4）

#### 修改 2.4: 删除 initializeRuleEngine() 函数定义
- **行号**: 341-371 (约 31 行)
- **函数签名**: `func initializeRuleEngine(routingRules *config.RoutingRulesConfig) error`
- **内容**:
  ```go
  func initializeRuleEngine(routingRules *config.RoutingRulesConfig) error {
      if routingRules == nil {
          logger.Info("Routing rules not configured, rule engine disabled")
          return nil
      }
      // ... 初始化逻辑 ...
  }
  ```
- **操作**: 完全删除整个函数
- **替代**: 规则引擎初始化逻辑应实现在 processor/nacos/manager.go 和 processor/routing/decision_engine.go 中（Phase 4-5）

---

### 3. processor/rules/engine.go (1 处修改)

**文件位置**: `/Users/mac/MyProgram/GoProgram/nursor/nursorgate2/processor/rules/engine.go`

#### 修改 3.1: 更新 RuleEngine.Initialize() 方法签名
- **行号**: 55
- **当前签名**:
  ```go
  func (e *RuleEngine) Initialize(config *config.RoutingRulesConfig) error {
      if config == nil {
          logger.Info("Routing rules config is nil, rule engine disabled")
          return nil
      }
      // ... 实现 ...
  }
  ```
- **新签名**:
  ```go
  func (e *RuleEngine) Initialize(config *model.RoutingRulesConfig) error {
      if config == nil {
          logger.Info("Routing rules config is nil, rule engine disabled")
          return nil
      }
      // ... 实现 ...
  }
  ```
- **变更**: `config.RoutingRulesConfig` → `model.RoutingRulesConfig`
- **操作**: 更新方法接收参数类型
- **导入**: 需要添加 `import "nursor/common/model"` （如果尚未导入）
- **影响**: 此方法被 inbound/tun/runner/start.go 和新的启动逻辑调用

---

### 4. inbound/tun/runner/start.go (3 处修改)

**文件位置**: `/Users/mac/MyProgram/GoProgram/nursor/nursorgate2/inbound/tun/runner/start.go`

#### 修改 4.1: 删除 initializeRuleEngineForTUN() 中的 RoutingRules 引用
- **行号**: 150-179 (约 30 行)
- **当前代码**:
  ```go
  func initializeRuleEngineForTUN() error {
      // 从全局配置获取 routing rules
      globalCfg := config.GetGlobalConfig()
      if globalCfg == nil || globalCfg.RoutingRules == nil {
          logger.Info("TUN: Routing rules not configured, using default DNS cache")

          // 使用默认配置初始化 cache
          defaultRules := &config.RoutingRulesConfig{
              IPDomainCache: &config.CacheConfig{
                  Enabled:    true,
                  MaxEntries: 10000,
                  TTL:        "5m",
              },
          }
          // ... 继续 ...
      } else {
          ruleEngine := rules.GetEngine()
          err := ruleEngine.Initialize(globalCfg.RoutingRules)
          if err != nil {
              return fmt.Errorf("failed to initialize rule engine: %w", err)
          }
      }
  }
  ```
- **操作**: 完全重构此函数
- **新逻辑**:
  1. 删除 `globalCfg.RoutingRules` 访问
  2. 直接从全局 RoutingRulesConfig 实例获取配置（该实例由 cmd/main.go 初始化）
  3. 创建默认配置时使用 `model.NewRoutingRulesConfig()` 而非 `&config.RoutingRulesConfig{}`
  4. 调用 `ruleEngine.Initialize()` 时传递 `model.RoutingRulesConfig`
- **注意**: 这个函数涉及 TUN 网卡初始化，需要特别注意 DNS 缓存的默认行为

---

## 参考：已验证正确的文件

### ✓ app/http/handlers/config_handler.go (无需修改)

**位置**: `/Users/mac/MyProgram/GoProgram/nursor/nursorgate2/app/http/handlers/config_handler.go`

**验证结果**: 此文件已正确使用新的 `model.RoutingRulesConfig`！

**关键引用**:
- Line 66: `model.NewDefaultRoutingRulesConfig()` ✓
- Line 72: `model.NewRoutingRulesConfigFromJSON()` ✓
- Line 100: `model.RoutingRulesConfig` ✓
- Line 196: `model.NewRoutingRulesConfigFromJSON()` ✓

这表明前端配置处理器已完全迁移到新模型（可能在 Phase 2 前端实现中）。

---

## 修改优先级

**顺序** (按依赖关系):

1. **优先级 1** (必须首先完成):
   - 修改 3.1: processor/rules/engine.go - 更新方法签名
   - 修改 4.1: inbound/tun/runner/start.go - 重构函数
   - *原因*: 这些是被调用方，需要先准备好新签名

2. **优先级 2** (可并行):
   - 修改 2.1-2.4: cmd/config.go - 删除初始化代码
   - 修改 1.1-1.2: processor/config/types.go - 删除旧定义和字段
   - *原因*: 相互独立，互不依赖

---

## 验证检查清单

完成所有修改后，需要验证：

- [ ] 编译成功: `go build ./cmd/...`
- [ ] 无未使用的导入: `go mod tidy`
- [ ] 代码格式化: `go fmt ./...`
- [ ] 无编译警告: `go vet ./...`
- [ ] 所有 RoutingRules 引用已删除: `grep -r "RoutingRules" processor/ cmd/ inbound/`
- [ ] Config 结构体中无 RoutingRules 字段
- [ ] processor/rules/engine.go 中 Initialize() 使用 `model.RoutingRulesConfig`
- [ ] 应用启动成功: `./nursorgate2`

---

## 预期行为变化

**删除前**:
- Config 从 processor/config/types.go 加载 RoutingRulesConfig（旧模型）
- 启动时调用 initializeGeoIP() 和 initializeRuleEngine()
- TUN 初始化直接访问 cfg.RoutingRules

**删除后**:
- Config 不再包含 RoutingRulesConfig 字段
- RoutingRulesConfig（新模型）通过 Nacos 独立加载（Phase 5）
- 路由规则初始化延迟到 Nacos 配置准备就绪时
- TUN 初始化使用全局 RoutingRulesConfig 实例或默认配置

---

## 后续工作

这个清理计划完成后：
- ✓ 所有旧的 RoutingRulesConfig 定义被完全移除
- ✓ Config 结构简化
- ✓ 新的 common/model/routing_config.go 成为唯一的数据模型来源
- ✓ Phase 3 (US1 - Config cleanup) 完全完成
- ✓ 可以开始 Phase 4 (US2 - Routing engine implementation)

---

**文档版本**: 1.0
**最后更新**: 2025-12-17
**制作者**: Claude Code (Phase 1 Foundation)
