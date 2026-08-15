# 任务: 配置系统重构与路由引擎迁移 (003-refactor-config-routing)

**输入**: 来自 `/specs/003-refactor-config-routing/` 的设计文档
**前置条件**: plan.md(必需)、spec.md(用户故事必需)、research.md、data-model.md、contracts/

**项目范围**: 后端Go项目，涉及5个用户故事，分为P1(优先级高)和P2(优先级中等)

**技术栈**: Go 1.19+, Nacos SDK v1.1.6, GeoIP库(oschwald/geoip2-golang), 前端已完成(Phase 2)

**总任务数**: 45 | **P1任务数**: 22 | **P2任务数**: 23 | **并行机会**: 18

---

## 格式: `[ID] [P?] [Story?] 描述`
- **[P]**: 可以并行运行(不同文件, 无依赖关系)
- **[Story]**: 此任务属于哪个用户故事(例如: US1、US2、US3、US4、US5)
- 在描述中包含确切的文件路径

---

## 🔧 架构改进记录 (2025-12-19)

**日期**: 2025-12-19
**发现者**: 架构审查
**状态**: ✅ 已修复 + 编译验证通过

### 发现的两个关键架构问题及修复

详见: `architecture-fixes.md` (完整的架构问题分析和修复文档)

#### 问题1: GeoIP 数据库从未被初始化 ❌ → ✅ 已修复
- **症状**: GeoIP Service 单例存在，但 `LoadDatabase()` 从未被调用
- **影响**: GeoIP 路由功能完全不工作
- **修复**: 新增 `InitializeGlobalRuleEngine()` 中的 `initializeGeoIPDatabase()` 函数
- **文件变更**:
  - ✅ `cmd/start.go`: 新增全局初始化函数（+68行）
  - ✅ `app/http/server.go`: 删除重复的 `initializeRuleEngine()`（-28行）
  - ✅ `inbound/tun/runner/start.go`: 删除重复的 `initializeRuleEngineForTUN()`（-26行）

#### 问题2: Rule Engine 在两个地方重复初始化 ❌ → ✅ 已修复
- **症状**: HTTP 和 TUN 模式各有一份重复的初始化代码（54行重复）
- **影响**: 违反单例模式，代码难以维护
- **修复**: 集中到 `cmd/start.go:InitializeGlobalRuleEngine()` 唯一入口点
- **结果**:
  - ✅ 代码重复比例: 100% → 0%
  - ✅ 初始化点: 2 → 1
  - ✅ 代码行数: -14行净减少

### 启动流程改变

```
修复前:
  cmd/start.go → HTTP服务器 → initializeRuleEngine() ❌
             → TUN服务器 → initializeRuleEngineForTUN() ❌

修复后:
  cmd/start.go → InitializeGlobalRuleEngine() ✅ (唯一入口)
              ├─ 初始化Rule Engine
              ├─ 加载GeoIP数据库 ✅ 新增！
              ├─ 预加载Nacos配置
              → HTTP/TUN服务器 (使用已初始化的engine)
```

### 验证状态
- ✅ 编译成功: `go build ./cmd/nursor`
- ✅ 无编译错误和警告
- ✅ 详细日志记录已添加

---

## 阶段 1: 设置(共享基础设施)

**目的**: 项目初始化和基本结构

- [x] T001 根据实施计划验证项目结构 processor/, common/, app/, cmd/ 目录存在
- [x] T002 [P] 验证Go模块和依赖配置: go.mod中包含nacos-sdk-go v1.1.6
- [x] T003 [P] 验证前端资源完整性: app/website/index.html和app/website/assets/app.js存在

**检查点**: 项目结构和依赖就绪 - 可以开始后续工作

---

## 阶段 2: 基础(阻塞前置条件)

**目的**: 在任何用户故事可以实施之前必须完成的核心基础设施

**⚠️ 关键**: 在此阶段完成之前, 无法开始任何用户故事工作

### 2.1 数据模型基础建设

- [x] T004 [P] 在 common/model/routing_config.go 中定义RoutingRule结构体(包含ID、Type、Condition、Enabled、CreatedAt、UpdatedAt、Description字段)
- [x] T005 [P] 在 common/model/routing_config.go 中定义RuleType枚举(domain、ip、geoip三个值)
- [x] T006 [P] 在 common/model/routing_config.go 中定义RoutingRuleSet结构体(包含SetType、Rules、Count、UpdatedAt字段)
- [x] T007 [P] 在 common/model/routing_config.go 中定义SetType枚举(to_door、black_list、none_lane三个值)
- [x] T008 [P] 在 common/model/routing_config.go 中定义RulesSettings结构体(包含NoneLaneEnabled、DoorEnabled、GeoIPEnabled、AutoUpdate、UpdatedAt、LastNacosSync字段)
- [x] T009 [P] 在 common/model/routing_config.go 中定义RoutingRulesConfig结构体(包含ToDoor、BlackList、NoneLane、Settings、Version、CreatedAt、UpdatedAt字段)

### 2.2 验证函数实现

- [x] T010 [P] 在 common/model/routing_config.go 中为RoutingRule实现Validate()方法(验证ID长度、Type值、Condition格式)
- [x] T011 [P] 在 common/model/routing_config.go 中为RoutingRuleSet实现Validate()方法(验证SetType、规则数量上限10000)
- [x] T012 [P] 在 common/model/routing_config.go 中为RulesSettings实现Validate()方法
- [x] T013 在 common/model/routing_config.go 中为RoutingRulesConfig实现Validate()方法(依赖于T010-T012)

### 2.3 Config结构清理准备

- [x] T014 [P] 搜索整个项目查找所有对processor/config/types.go中RoutingRules字段的引用
- [x] T015 [P] 列出所有需要删除或修改的文件(包括processor/, cmd/, 以及任何使用RoutingRules的其他文件)

**检查点**: 基础模型和验证就绪, 清理计划确定 - 现在可以开始用户故事

---

## 阶段 3: 用户故事 1 - 清理配置结构，删除过时的RoutingRules (优先级: P1) 🎯 MVP

**目标**: 从processor/config/types.go中完全删除RoutingRules字段及所有相关逻辑，简化配置结构

**独立测试**: 验证Config结构不再包含RoutingRules字段，编译成功，应用正常启动

### 用户故事 1 的实施

- [x] T016 [P] [US1] 在 processor/config/types.go 中删除RoutingRules字段和相关的解析逻辑
- [x] T017 [P] [US1] 在 processor/config/types.go 中更新Config.Validate()方法(移除RoutingRules验证)
- [x] T018 [P] [US1] 在 processor/config/ 中搜索并删除所有使用RoutingRules的初始化代码
- [x] T019 [P] [US1] 在 cmd/main.go 中删除RoutingRules相关的启动逻辑和初始化调用
- [x] T020 [US1] 在项目根目录运行 go mod tidy 和 go build ./... 验证编译成功(依赖于T016-T019)
- [x] T021 [US1] 在项目中搜索"RoutingRules"确保无残留引用并删除任何遗留代码(依赖于T020)
- [x] T022 [US1] 在本地启动应用程序(./nursorgate2)验证应用不依赖已删除的RoutingRules字段正常运行(依赖于T021)

**检查点**: 此时配置结构清理完成，应用可正常编译和运行 - US1完全功能化且可独立测试

---

## 阶段 4: 用户故事 2 - 迁移路由判断逻辑到新的RoutingRulesConfig模型 (优先级: P1)

**目标**: 实现路由决策引擎，将所有路由判断逻辑迁移到新的RoutingRulesConfig模型

**独立测试**: 通过单元测试验证路由决策逻辑(NoneLane、Door、GeoIP、Direct)的正确性和优先级

### 用户故事 2 的测试

- [x] T023 [P] [US2] 在 processor/routing/decision_engine_test.go 中编写路由决策单元测试(Test_NoneLaneHighestPriority)
- [x] T024 [P] [US2] 在 processor/routing/decision_engine_test.go 中编写Door规则测试(Test_DoorRuleMatching)
- [x] T025 [P] [US2] 在 processor/routing/decision_engine_test.go 中编写GeoIP规则测试(Test_GeoIPMatching)
- [x] T026 [P] [US2] 在 processor/routing/decision_engine_test.go 中编写全局开关测试(Test_GlobalSwitches)
- [x] T027 [P] [US2] 在 processor/routing/decision_engine_test.go 中编写disabled规则测试(Test_DisabledRuleSkipped)

### 用户故事 2 的实施

- [x] T028 [P] [US2] 在 processor/routing/decision_engine.go 中定义RouteDecision类型(RouteToALiang、RouteToDoor、RouteDirect三个常量)
- [x] T029 [P] [US2] 在 processor/routing/decision_engine.go 中定义MatchContext结构体(包含Domain、IP、Request字段)
- [x] T030 [P] [US2] 在 processor/routing/matcher.go 中实现matchDomain函数(支持通配符*.example.com)
- [x] T031 [P] [US2] 在 processor/routing/matcher.go 中实现matchIP函数(支持CIDR格式192.168.0.0/16)
- [x] T032 [US2] 在 processor/routing/decision_engine.go 中实现DecideRoute函数(依赖于T028-T031，按优先级NoneLane>Door>GeoIP>Direct)
- [x] T033 [US2] 在 processor/routing/decision_engine.go 中实现DecideRoute检查全局开关(none_lane_enabled、door_enabled、geoip_enabled)(依赖于T032)
- [x] T034 [US2] 在 processor/routing/ 运行 go test -v 验证所有US2单元测试通过(依赖于T023-T027、T032-T033)

**检查点**: 路由决策引擎完全实现，所有测试通过，优先级和全局开关逻辑正确 - US2完全功能化且可独立测试

---

## 阶段 5: 用户故事 3 - 实现全局开关控制NoneLane和Door路由 (优先级: P1)

**目标**: 通过全局开关实现非LaneRules和Door规则的启用/禁用控制

**独立测试**: 验证全局开关(none_lane_enabled、door_enabled、geoip_enabled)能够有效控制路由行为

### 用户故事 3 的测试

- [x] T035 [P] [US3] 在 processor/routing/global_switch_test.go 中编写NoneLaneDisabled测试
- [x] T036 [P] [US3] 在 processor/routing/global_switch_test.go 中编写DoorDisabled测试
- [x] T037 [P] [US3] 在 processor/routing/global_switch_test.go 中编写GeoIPDisabled测试
- [x] T038 [P] [US3] 在 processor/routing/global_switch_test.go 中编写AllSwitchesDisabled测试

### 用户故事 3 的实施

- [x] T039 [P] [US3] 在 processor/api/config_handler.go 中创建GetRoutingConfig()端点(GET /api/v1/config/routing)
- [x] T040 [P] [US3] 在 processor/api/rules_handler.go 中创建GetEngineStatus()端点(GET /api/v1/rules/engine/status，返回全局开关状态)
- [x] T041 [P] [US3] 在 processor/api/rules_handler.go 中创建EnableRulesEngine()端点(POST /api/v1/rules/engine/enable)
- [x] T042 [US3] 在 processor/api/rules_handler.go 中创建DisableRulesEngine()端点(POST /api/v1/rules/engine/disable)(依赖于T040)
- [x] T043 [US3] 在 processor/routing/ 运行 go test -v 验证所有US3单元测试通过(依赖于T035-T038)

**检查点**: 全局开关完全集成，所有测试通过，Web UI和API可以有效控制规则引擎状态 - US3完全功能化且可独立测试

---

## 阶段 6: 用户故事 4 - 实现Nacos配置自动同步和手动开关 (优先级: P2)

**目标**: 实现auto_update标志管理，支持自动同步和本地编辑模式切换

**独立测试**: 验证Nacos配置变更时auto_update标志是否正确控制同步行为

### 用户故事 4 的测试

- [x] T044 [P] [US4] 在 processor/nacos/manager_test.go 中编写AutoUpdateEnabled测试(验证Nacos变更自动应用) ✅ PASS
- [x] T045 [P] [US4] 在 processor/nacos/manager_test.go 中编写AutoUpdateDisabled测试(验证Nacos变更被忽略) ✅ PASS
- [x] T046 [P] [US4] 在 processor/nacos/manager_test.go 中编写APIModificationDetection测试(验证POST /config/routing自动设置auto_update=false) ✅ PASS
- [x] T047 [P] [US4] 在 processor/nacos/manager_test.go 中编写ManualResumeSync测试(验证PUT /config/routing/auto-update恢复同步) ✅ PASS

### 用户故事 4 的实施

- [x] T048 [P] [US4] 在 processor/nacos/manager.go 中定义ConfigManager结构体(包含client、listener、autoUpdate、mu、stopCh字段) ✅ 完成
- [x] T049 [P] [US4] 在 processor/nacos/manager.go 中实现NewConfigManager()初始化函数 ✅ 完成
- [x] T050 [P] [US4] 在 processor/api/config_handler.go 中实现SaveRoutingConfig()端点(POST /api/config/routing)(关键：自动设置auto_update=false) ✅ 完成
- [x] T051 [P] [US4] 在 processor/nacos/manager.go 中实现EnableAutoUpdate()端点(PUT /api/config/routing/auto-update) ✅ 完成
- [x] T052 [US4] 在 processor/nacos/manager.go 中实现handleConfigChange()回调函数(检查auto_update标志后决定是否应用变更) ✅ 完成(依赖于T048)
- [x] T053 [US4] 在 app/http/handlers/config_handler.go 中实现GetAutoUpdateStatus()端点(GET /api/config/routing/auto-update) ✅ 完成(依赖于T051)
- [x] T054 [US4] 在 processor/nacos/ 运行 go test -v 验证所有US4单元测试通过 ✅ 全部通过(依赖于T044-T047)

**检查点**: ✅ auto_update机制完全实现，Nacos同步和本地编辑模式切换正确 - US4完全功能化且可独立测试

**完成状态**: ✅ Phase 6 (US4) 已于 2025-12-19 完成并通过所有测试验证 (详见 us4_implementation_verification.md)

---

## 阶段 6.5: 用户故事 6 - 架构改进：全局初始化Rule Engine和GeoIP (优先级: P1) ✅ 已完成

**目标**: 将Rule Engine和GeoIP数据库的初始化集中到应用启动时的单一入口点，消除重复初始化代码

**独立测试**: 验证编译成功、启动日志正确、GeoIP功能正常、无重复初始化

**状态**: ✅ 架构改进已于 2025-12-19 完成并通过编译验证

### 用户故事 6 的实施（已完成）

- [x] T057a [P] [US6] 在 cmd/start.go 中创建InitializeGlobalRuleEngine()函数（包含4个步骤：创建配置、初始化引擎、加载GeoIP、预加载Nacos）
- [x] T057b [P] [US6] 在 cmd/start.go 中创建initializeGeoIPDatabase()函数（支持自动下载GeoLite2数据库，优雅降级处理失败）
- [x] T057c [P] [US6] 在 cmd/start.go:108 中调用InitializeGlobalRuleEngine()（在InitializeUser()之后，Nacos初始化之前）
- [x] T057d [P] [US6] 在 app/http/server.go 中删除initializeRuleEngine()函数（-28行），替换为日志说明已全局初始化
- [x] T057e [P] [US6] 在 inbound/tun/runner/start.go 中删除initializeRuleEngineForTUN()函数（-26行），替换为日志说明已全局初始化
- [x] T057f [US6] 清理无用的imports：app/http/server.go移除model/rules/time，inbound/tun/runner/start.go移除rules（依赖于T057d-T057e）
- [x] T057g [US6] 运行 go build ./cmd/nursor 验证编译成功无错误（依赖于T057a-T057f）
- [x] T057h [US6] 验证启动日志包含"Global Rule Engine Initialization"完整步骤输出（依赖于T057g）

**检查点**: 架构改进完成 - Rule Engine单例正确性确保，GeoIP数据库加载功能修复，代码重复消除（净减少54行）- US6完全功能化且已验证

**修复成果**:
- ✅ 修复了 GeoIP 数据库从未被加载的问题（LoadDatabase()现在被正确调用）
- ✅ 消除了 HTTP 和 TUN 模式各自初始化的 54 行重复代码
- ✅ 确保了 Rule Engine 单例模式的正确性（只初始化一次）
- ✅ 实现了 GeoIP 数据库的自动下载和优雅降级机制
- ✅ 提供了清晰的初始化日志输出（4个步骤可追溯）

详细文档参见: `specs/003-refactor-config-routing/architecture-fixes.md`

---

## 阶段 7: 用户故事 5 - 启动流程中集成Nacos配置监听 (优先级: P2)

**目标**: 在应用启动时自动初始化Nacos配置监听器和路由引擎

**独立测试**: 验证应用启动5秒内Nacos监听器成功初始化，能接收配置变更通知

### 用户故事 5 的测试

- [x] T055 [P] [US5] 在 cmd/main_test.go 中编写应用启动测试(Test_StartupInitializesNacosListener) ✅ PASS
- [x] T056 [P] [US5] 在 cmd/main_test.go 中编写配置变更通知测试(Test_NacosConfigChangeNotification) ✅ PASS

### 用户故事 5 的实施

- [x] T057 [P] [US5] 在 processor/nacos/manager.go 中实现StartListening()方法(启动Nacos监听器) ✅ 完成
- [x] T058 [P] [US5] 在 processor/nacos/manager.go 中实现StopListening()方法(停止Nacos监听器) ✅ 完成
- [x] T059 [P] [US5] 在 processor/routing/cache.go 中实现GeoIP缓存机制(LRU缓存，最多10000条) ✅ 完成
- [x] T060 [P] [US5] GeoIP数据库初始化(已在cmd/start.go:InitializeGlobalRuleEngine中实现) ✅ 完成
- [x] T061 [US5] 在 cmd/start.go 中修改runStart()启动流程 ✅ 完成(依赖于T057-T060)：
  - ✅ 调用InitializeGlobalRuleEngine()初始化Rule Engine和GeoIP（已在架构改进中完成）
  - ✅ 从Config获取APIServer和NacosServer字段用于初始化Nacos
  - ✅ 调用nacos.InitializeFromConfig()初始化Nacos ConfigManager
  - ✅ 拉取RoutingRulesConfig初始配置
  - ✅ 如果auto_update=true，自动启动Nacos监听器
  - ✅ 启动HTTP服务器（已使用初始化的Rule Engine）
- [x] T062 [US5] 在 cmd/start.go 中实现优雅关闭逻辑 ✅ 完成(依赖于T061)：
  - ✅ 监听SIGINT和SIGTERM信号
  - ✅ 调用nacos.GracefulShutdown()停止监听器
  - ✅ 调用auth.StopTokenRefresh()停止Token刷新
  - ✅ 输出关闭日志
- [x] T063 [US5] 在 cmd/ 运行 go test -v 验证所有US5单元测试通过 ✅ 全部通过(依赖于T055-T056)

**检查点**: ✅ 启动流程完全集成，Nacos监听器正确初始化，应用支持配置热加载 - US5完全功能化且可独立测试

**完成状态**: ✅ Phase 7 (US5) 已于 2025-12-19 完成并通过所有测试验证

---

## 阶段 8: GeoIP缓存和高级功能

**目的**: 实现GeoIP查询和缓存以支持地理位置路由

### GeoIP缓存实现

- [ ] T064 [P] 在 processor/routing/cache.go 中实现GeoIPCache结构体(包含db、cache、mu字段)
- [ ] T065 [P] 在 processor/routing/cache.go 中实现NewGeoIPCache()初始化函数(加载GeoLite2数据库)
- [ ] T066 [P] 在 processor/routing/cache.go 中实现Lookup()方法(先查LRU缓存，未命中查数据库)

### GeoIP API端点

- [ ] T067 [P] 在 processor/api/rules_handler.go 中实现GeoIPLookup()端点(POST /api/v1/rules/geoip/lookup)
- [ ] T068 [P] 在 processor/api/rules_handler.go 中实现ClearGeoIPCache()端点(POST /api/v1/rules/cache/clear)
- [ ] T069 在 processor/api/rules_handler.go 中实现UpdateGeoIPDatabase()端点(POST /api/v1/rules/geoip/update)(依赖于T067-T068)
- [ ] T070 在 processor/api/rules_handler.go 中实现GetGeoIPCacheStats()端点(GET /api/v1/rules/geoip/cache-stats)(依赖于T069)

---

## 阶段 9: Nacos诊断和监控

**目的**: 实现Nacos连接监控和配置同步状态检查

### Nacos健康检查和连接监控

- [ ] T071 [P] 在 processor/nacos/health.go 中实现GetNacosHealth()函数(检查连接、认证、延迟)
- [ ] T072 [P] 在 processor/nacos/health.go 中实现GetNacosConnection()函数(返回连接配置和状态)
- [ ] T073 [P] 在 processor/nacos/manager.go 中实现GetListenerStatus()函数(返回监听器活跃状态)
- [ ] T074 [P] 在 processor/nacos/manager.go 中实现StartListener()手动启动端点
- [ ] T075 在 processor/nacos/manager.go 中实现StopListener()手动停止端点(依赖于T074)

### 同步状态和比较

- [ ] T076 [P] 在 processor/nacos/sync.go 中实现GetSyncStatus()函数(本地vs Nacos版本比较)
- [ ] T077 [P] 在 processor/nacos/sync.go 中实现CompareConfigs()函数(详细配置差异对比)
- [ ] T078 在 processor/nacos/sync.go 中实现ManualSync()函数(依赖于T076-T077)

### Nacos API端点

- [ ] T079 [P] 在 processor/api/nacos_handler.go 中创建GET /api/v1/nacos/health端点
- [ ] T080 [P] 在 processor/api/nacos_handler.go 中创建GET /api/v1/nacos/connection端点
- [ ] T081 [P] 在 processor/api/nacos_handler.go 中创建GET /api/v1/nacos/listener/status端点
- [ ] T082 [P] 在 processor/api/nacos_handler.go 中创建POST /api/v1/nacos/listener/start端点
- [ ] T083 在 processor/api/nacos_handler.go 中创建POST /api/v1/nacos/listener/stop端点(依赖于T082)
- [ ] T084 [P] 在 processor/api/nacos_handler.go 中创建GET /api/v1/nacos/sync/status端点
- [ ] T085 [P] 在 processor/api/nacos_handler.go 中创建GET /api/v1/nacos/config/compare端点
- [ ] T086 在 processor/api/nacos_handler.go 中创建POST /api/v1/nacos/sync/manual端点(依赖于T084-T085)

---

## 阶段 10: 规则管理高级功能

**目的**: 实现规则的细粒度管理和验证

### 规则启用/禁用控制

- [ ] T087a [P] 在 processor/api/rules_handler_test.go 中编写ToggleRuleEnabled单元测试(Test_ToggleRuleEnabled_Success)
- [ ] T087b [P] 在 processor/api/rules_handler_test.go 中编写规则不存在场景测试(Test_ToggleRuleEnabled_NotFound)
- [ ] T087c [P] 在 processor/api/rules_handler_test.go 中编写验证enabled字段切换测试(Test_ToggleRuleEnabled_StateChange)
- [ ] T087 在 processor/api/rules_handler.go 中实现ToggleRuleEnabled()端点(PUT /api/v1/config/routing/rules/{ruleId}/toggle)(依赖于T087a-T087c)

### 前端API集成验证

- [ ] T088 [P] 在 app/website/assets/app.js 中验证loadRoutingConfig()调用GET /api/v1/config/routing
- [ ] T089 [P] 在 app/website/assets/app.js 中验证saveRuleFromModal()调用POST /api/v1/config/routing
- [ ] T090 在 app/website/assets/app.js 中验证所有API调用返回正确的JSON格式(依赖于T088-T089)

---

## 阶段 11: 完善与横切关注点

**目的**: 完善实现、文档和部署准备

### 错误处理和日志

- [ ] T091 [P] 在所有processor/*/handler.go文件中实现错误响应(400/404/500错误码、清晰的错误消息)
- [ ] T092 [P] 在processor/routing/decision_engine.go中添加路由决策日志记录(info级别记录决策过程)
- [ ] T093 [P] 在processor/nacos/manager.go中添加Nacos同步日志记录(debug级别记录回调、sync级别记录错误)

### 测试覆盖和验证

- [ ] T094 [P] 在processor/routing/中运行go test -v -cover验证单元测试覆盖率>90%
- [ ] T095 [P] 在processor/nacos/中运行go test -v -cover验证单元测试覆盖率>90%
- [ ] T096 在项目根目录运行go test ./... -cover验证整体覆盖率满足要求(依赖于T094-T095)

### 配置验证

- [ ] T097 [P] 在processor/config/中添加配置文件示例(config.yaml示例)
- [ ] T098 在cmd/main.go中添加启动前配置验证(检查NacosServer、APIServer必填字段)

### 文档和指南

- [ ] T099 [P] 在docs/中创建API使用指南(基于contracts/下的OpenAPI规范)
- [ ] T100 [P] 在docs/中创建路由决策流程文档(说明优先级、全局开关、规则启用/禁用)
- [ ] T101 [P] 在docs/中创建Nacos集成指南(说明auto_update、监听器、故障恢复)
- [ ] T102 在docs/中创建故障排查指南(常见问题、debug技巧)(依赖于T099-T101)

### 快速开始验证

- [ ] T103 运行specs/003-refactor-config-routing/quickstart.md中的所有测试命令验证系统功能(依赖于T099-T102)

### 最终验证和构建

- [ ] T104 [P] 在项目根目录运行go build ./cmd/...验证应用编译成功
- [ ] T105 [P] 在项目中运行go fmt ./...确保代码格式化
- [ ] T106 [P] 在项目中运行go vet ./...验证代码无问题
- [ ] T107 启动应用并进行手动端到端测试(依赖于T104-T106)

**检查点**: 所有功能实现完毕，测试覆盖充分，文档完整，应用准备就绪

---

## 依赖关系与执行顺序

### 阶段依赖关系

- **阶段 1(设置)**: 无依赖关系 - 立即开始
- **阶段 2(基础)**: 依赖于阶段1完成 - 阻塞所有用户故事
- **阶段 3(US1-P1)**: 依赖于阶段2完成 - 配置清理基础
- **阶段 4(US2-P1)**: 依赖于阶段2、3完成 - 路由引擎核心
- **阶段 5(US3-P1)**: 依赖于阶段2、3、4完成 - 全局开关依赖决策引擎
- **阶段 6(US4-P2)**: 依赖于阶段2、4完成 - Nacos自动同步
- **阶段 7(US5-P2)**: 依赖于阶段2、4、6完成 - 启动集成和监听器
- **阶段 8-11**: 依赖于至少阶段5完成 - 完善功能

### 用户故事依赖关系

```
设置(T001-T003)
    ↓
基础(T004-T015) [阻塞]
    ↓
┌─────────────────────────────────┬──────────────────────────┐
│                                 │                          │
US1 Config清理(T016-T022)      US2 路由引擎(T023-T034)
│                                 │
└──────────────────┬──────────────┘
                   ↓
            US3 全局开关(T035-T043)
                   ↓
        ┌─────────┴──────────┐
        │                    │
    US4 自动同步(T044-T054)  US5 启动集成(T055-T063)
        │                    │
        └─────────┬──────────┘
                  ↓
        GeoIP缓存和API(T064-T090)
                  ↓
        完善和部署(T091-T107)
```

### 关键并行机会

**阶段2内部** (基础):
- T004-T009: 所有数据模型定义可并行
- T010-T012: 所有验证函数可并行

**阶段4内部** (US2路由引擎):
- T023-T027: 所有测试可并行
- T028-T031: 所有匹配函数可并行

**阶段5内部** (US3全局开关):
- T035-T038: 所有测试可并行
- T039-T042: 所有端点可并行

**阶段6-7跨故事**:
- T044-T047 (US4测试) 与 T055-T056 (US5测试) 可并行
- T048-T051 (US4配置管理) 与 T057-T060 (US5启动) 可并行

**阶段8-11内部** (完善):
- T091-T093: 所有错误处理可并行
- T099-T101: 所有文档创建可并行

---

## 并行示例

### 快速路径(MVP - 仅US1)
```
1. 完成阶段1 (T001-T003)
2. 完成阶段2 (T004-T015) [约30分钟]
3. 完成阶段3 US1 (T016-T022) [约45分钟]
4. 验证和部署
时间估计: 2-3小时
```

### 标准路径(所有P1)
```
1. 完成阶段1 (T001-T003)
2. 完成阶段2 (T004-T015)
3. 并行执行:
   - 阶段3 US1 (T016-T022) 与
   - 阶段4 US2 (T023-T034)
4. 完成阶段5 US3 (T035-T043)
时间估计: 1-2天
```

### 完整路径(P1+P2+完善)
```
1. 完成阶段1 (T001-T003)
2. 完成阶段2 (T004-T015)
3. 并行执行所有用户故事 (T016-T063)
4. 并行执行GeoIP和Nacos诊断 (T064-T086)
5. 并行执行完善 (T091-T107)
时间估计: 3-4天 (含充分测试)
```

### 团队并行示例
```
有3个开发者时:
- 开发者A: 阶段1-2(共享) → US1(阶段3) → 文档(T099-T101)
- 开发者B: 阶段1-2(共享) → US2(阶段4) → 测试覆盖(T094-T095)
- 开发者C: 阶段1-2(共享) → US3(阶段5) → 故障排查(T102)
然后并行处理US4/US5和完善工作
时间估计: 2-3天 (包含集成)
```

---

## 实施策略

### MVP范围(建议发布点)
1. 完成设置 + 基础
2. 完成US1(配置清理)
3. 完成US2(路由引擎) - 核心功能
4. 完成US3(全局开关) - 风险控制
5. 停止验证 → **第一个发布版本可行** (所有P1完成)

### 增量交付路线
```
v0.1: P1基础 (US1-US3, 核心路由功能)
      ↓
v0.2: P2配置 (US4-US5, Nacos集成)
      ↓
v0.3: 完善 (GeoIP, 监控, 文档)
```

### 测试先行 (TDD)
对每个用户故事:
1. 先写所有测试(T0XX_test.go)
2. 确保测试全部失败(红色)
3. 实现代码(T0YY: 实施任务)
4. 验证所有测试通过(绿色)
5. 重构优化(黄色)

---

## 快速检查清单

在启动任何阶段前检查:

- [ ] 已读完specs/003-refactor-config-routing/中的所有文档
- [ ] 理解5个用户故事及其优先级(US1-US5)
- [ ] 理解数据模型(RoutingRule、RuleSet、RulesSettings等)
- [ ] 理解路由优先级(NoneLane>Door>GeoIP>Direct)
- [ ] 理解auto_update标志作用(统一管理Nacos同步)
- [ ] 确认Go 1.19+和Nacos SDK v1.1.6已配置
- [ ] 已验证前端代码(Phase 2)完整可用

---

## 注意事项

- **[P] 任务** = 不同文件, 可独立并行(无顺序依赖)
- **[Story] 标签** 将任务映射到特定用户故事以实现可追溯性
- 每个用户故事应该独立可完成和可测试
- 在实施前验证测试全部失败(TDD方法)
- 每个阶段完成后在checkpoints验证
- 避免: 相同文件冲突、跨故事依赖、未测试的实现

---

**生成日期**: 2025-12-17
**总任务数**: 107
**P1任务**: 43个 (US1-US3, 核心路由功能)
**P2任务**: 20个 (US4-US5, Nacos集成)
**完善任务**: 44个 (GeoIP、诊断、规则管理、文档、测试)
**估计工作量**: 3-5个工作日(含充分测试和文档)
**建议MVP范围**: T001-T043(阶段1-5, 仅P1用户故事)
