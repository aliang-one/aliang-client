# 功能规范: 配置系统重构与路由引擎迁移

**功能分支**: `003-refactor-config-routing`
**创建时间**: 2025-12-17
**状态**: 草稿
**输入**: 用户描述: 配置系统逻辑梳理、RoutingRules删除、规则引擎迁移、Nacos同步管理

## 澄清记录

### Session 2025-12-17

通过澄清流程确认的设计决策：

- Q1: 配置迁移策略 → A: 直接替换，不保留向后兼容性
- Q2: 本地修改检测机制 → A: API触发即视为用户修改
- Q3: 路由决策优先级冲突解决 → A: 严格按顺序检查，首次匹配即返回
- **Q4: Nacos故障恢复策略 → A: auto_update开关是唯一决策点，与故障状态无关。用户有修改则不同步，无修改则恢复后同步**

## 用户场景与测试 *(必填)*

### 用户故事 1 - 清理配置结构，删除过时的RoutingRules (优先级: P1)

作为系统维护人员，我希望能够删除processor/config中的过时RoutingRules配置选项，以简化配置结构，使代码更加清晰易维护。

**优先级原因**: 这是架构清理的基础，为后续路由引擎迁移奠定基础。删除过时代码能降低系统复杂性，减少维护成本。

**独立测试**: 可以通过验证Config结构不再包含RoutingRules字段，且编译成功，所有引用该字段的代码已删除，实现代码简化。

**验收场景**:

1. **给定** processor/config/types.go中Config结构包含RoutingRules字段, **当** 删除该字段和相关逻辑, **那么** 编译成功且无编译错误
2. **给定** 项目中存在使用RoutingRules的代码, **当** 搜索项目查找所有引用, **那么** 发现并移除所有使用该字段的逻辑
3. **给定** 删除后的新配置, **当** 启动应用程序, **那么** 应用正常运行，不依赖于已删除的RoutingRules

---

### 用户故事 2 - 迁移路由判断逻辑到新的RoutingRulesConfig模型 (优先级: P1)

作为开发者，我希望能够将所有路由判断逻辑从旧的Config结构迁移到新的common/model/routing_config.go中的RoutingRulesConfig，实现规则引擎的统一管理。

**优先级原因**: 这是整个功能的核心，规则引擎是路由决策的中心。此功能实现后才能支持后续的Nacos配置管理和用户配置编辑。

**独立测试**: 可以通过创建单元测试验证路由判断逻辑的正确性，测试包括：
- NoneLane域名规则匹配
- Door域名规则匹配
- GeoIP地理位置匹配
- 全局开关对判断结果的影响

**验收场景**:

1. **给定** 请求域名为"api.nonelane.com"，非LaneEnabled开关打开, **当** 执行路由判断, **那么** 返回NoneLane路由结果
2. **给定** 请求域名为"api.google.com"，Door开关打开，GeoIP为"US", **当** 执行路由判断, **那么** 返回Door路由结果
3. **给定** 非LaneEnabled和Door开关都关闭, **当** 执行任何路由判断, **那么** 所有流量走Direct路由
4. **给定** 对应规则disabled为true, **当** 执行路由判断, **那么** 不匹配该规则，继续检查下一个规则

---

### 用户故事 3 - 实现全局开关控制NoneLane和Door路由 (优先级: P1)

作为系统管理员，我希望能够通过全局开关快速启用或禁用NoneLane和Door路由功能，以便在不修改具体规则的情况下灵活调控流量分配。

**优先级原因**: 全局开关是风险控制的关键机制，允许管理员快速回滚整个路由功能而无需逐一删除规则。

**独立测试**: 可以通过修改配置中的geoip_enabled和none_lane_enabled标志，验证路由逻辑的相应变化。

**验收场景**:

1. **给定** geoip_enabled设为false, **当** 执行路由判断, **那么** 跳过GeoIP判断步骤
2. **给定** none_lane_enabled设为false, **当** 执行路由判断, **那么** 所有NoneLane规则都被跳过
3. **给定** 两个开关都设为false, **当** 执行路由判断, **那么** 所有流量走Direct路由
4. **给定** 修改开关状态后, **当** 重新加载配置, **那么** 新的开关状态立即生效

---

### 用户故事 4 - 实现Nacos配置自动同步和手动开关 (优先级: P2)

作为系统管理员，我希望能够在Nacos配置自动同步和本地编辑之间灵活切换，确保配置管理过程中不会因为Nacos更新而覆盖我的本地修改。

**优先级原因**: Nacos同步是配置持久化的关键，但需要在自动更新和本地编辑之间找到平衡，防止用户修改被无意覆盖。

**独立测试**: 可以通过模拟Nacos服务器配置更改，验证在启用/禁用自动更新时的行为差异。

**验收场景**:

1. **给定** 用户未修改Nacos配置，自动更新为启用, **当** Nacos服务器配置变化, **那么** 本地配置自动更新为最新值
2. **给定** 用户手动修改了本地配置, **当** 保存修改, **那么** 自动更新开关自动关闭，Nacos监听器暂停
3. **给定** 自动更新开关处于关闭状态, **当** Nacos服务器配置变化, **那么** 本地配置保持不变
4. **给定** 自动更新处于关闭状态, **当** 用户手动启用开关, **那么** Nacos立即同步，覆盖本地配置，监听器恢复

---

### 用户故事 5 - 启动流程中集成Nacos配置监听 (优先级: P2)

作为开发者，我希望能够在应用启动后，自动初始化Nacos配置监听器，从而支持配置的实时更新和热加载。

**优先级原因**: 这是Nacos集成的运行时支撑，没有这个功能，应用启动后无法接收来自Nacos的配置更新。

**独立测试**: 可以通过启动应用，验证Nacos监听器是否成功初始化，以及是否能收到来自Nacos的配置变更通知。

**验收场景**:

1. **给定** 应用启动时配置中包含有效的NacosServer地址, **当** 应用初始化, **那么** Nacos配置监听器成功启动
2. **给定** 监听器启动后, **当** Nacos服务器上的配置变化, **那么** 本地应用收到变更通知
3. **给定** APIServer和NacosServer都已配置, **当** 启动应用, **那么** 先解析这两个字段，然后初始化Nacos监听

---

### 用户故事 6 - 架构改进：全局初始化Rule Engine和GeoIP (优先级: P1) ✅ 已完成

作为系统架构师，我希望Rule Engine和GeoIP数据库在应用启动时统一初始化一次，避免在HTTP和TUN模式重复初始化，以确保单例模式正确性和代码可维护性。

**优先级原因**: 这是架构层面的关键改进，解决了两个严重问题：GeoIP数据库从未被加载导致功能失效，以及Rule Engine重复初始化违反单例模式。这些问题会导致核心路由功能不可用和潜在的状态不一致。

**独立测试**: 可以通过验证应用启动日志、检查GeoIP功能是否正常工作、确认只有一个Rule Engine初始化调用来验证。

**验收场景**:

1. **给定** 应用启动且GeoIPEnabled=false, **当** InitializeGlobalRuleEngine()被调用, **那么** Rule Engine只初始化一次，GeoIP数据库不加载
2. **给定** 应用启动且GeoIPEnabled=true, **当** InitializeGlobalRuleEngine()被调用, **那么** GeoIP数据库成功从~/.nonelane/GeoLite2-Country.mmdb加载，IsEnabled()返回true
3. **给定** GeoIP数据库文件不存在, **当** 应用启动, **那么** 系统自动从https://git.io/GeoLite2-Country.mmdb下载数据库
4. **给定** GeoIP加载失败（网络不可用或文件损坏）, **当** 应用继续启动, **那么** GeoIP自动禁用但应用正常运行，其他路由规则继续生效
5. **给定** HTTP和TUN模式同时运行, **当** 检查Rule Engine初始化次数, **那么** Initialize()方法只被调用一次
6. **给定** 删除app/http/server.go和inbound/tun/runner/start.go中的重复代码, **当** 编译项目, **那么** 编译成功且代码行数减少54行

---

### 边界情况

- 当Nacos服务不可用时会发生什么？系统应该使用本地缓存的配置继续运行，路由决策不受影响。
- Nacos故障恢复时会发生什么？auto_update开关是唯一决策点：如果auto_update=true则立即同步覆盖本地配置；如果auto_update=false则保持本地配置不变。
- 用户在修改配置后立即重启应用时会发生什么？本地修改应该被保留（auto_update已自动关闭），不被Nacos覆盖。
- 当GeoIP规则和非LaneRules同时匹配时会发生什么？非Lane规则优先级更高，应该首先进行非Lane匹配判断。
- 如果配置中不存在任何匹配规则时会发生什么？应该回退到Direct路由。
- 当自动更新开关从关闭切换为启用时，本地未保存的修改会被覆盖吗？是的，这是预期行为。
- **✅ 新增（架构改进）**: 当GeoIP数据库加载失败时会发生什么？系统会记录警告日志但继续启动，自动禁用GeoIP路由（设置`geoip_enabled=false`），其他路由规则（NoneLane、Door）仍然有效。这样可以避免因为GeoIP文件不存在导致整个应用启动失败。
- **✅ 新增（架构改进）**: GeoIP数据库文件不存在时会发生什么？系统会自动从 `https://git.io/GeoLite2-Country.mmdb` 下载到 `~/.nonelane/GeoLite2-Country.mmdb`（约6MB，需要1-2分钟）。如果网络不可用或下载失败，则按照上条规则优雅降级。

## 需求 *(必填)*

### 功能需求

- **FR-001**: 系统必须从processor/config/types.go中完全删除RoutingRules字段及所有相关逻辑
- **FR-002**: 系统必须实现路由决策引擎，优先级为：NoneLane域名 → Door域名 → GeoIP → Direct
  - **域名匹配算法**: 支持精确匹配和通配符匹配
    - 精确匹配：`example.com` 只匹配 `example.com`
    - 通配符匹配：`*.example.com` 匹配所有子域名（如 `api.aliang.one`、`www.example.com`）
    - 通配符仅支持单级前缀：`*.*.example.com` 不支持
  - **IP匹配算法**: 支持CIDR格式（如 `192.168.0.0/16`）和单IP精确匹配
  - **GeoIP匹配**: 基于MaxMind GeoLite2数据库，支持国家代码匹配（如 `CN`、`US`）
- **FR-003**: 系统必须提供geoip_enabled全局开关，当禁用时跳过所有GeoIP判断
- **FR-004**: 系统必须提供none_lane_enabled全局开关，当禁用时跳过所有NoneLane判断
- **FR-005**: 系统必须实现规则启用/禁用控制，允许单个规则被临时禁用而无需删除
- **FR-006**: 系统必须实现Nacos配置监听，自动接收来自服务器的配置更新
  - **不可用检测机制**: Nacos连接超时阈值为5秒，连续3次重试失败后判定为不可用
  - **重试策略**: 指数退避重试，初始间隔1秒，最大间隔30秒，最多重试3次
  - **降级行为**: Nacos不可用时，系统使用本地缓存配置继续运行，每60秒尝试一次重连
  - **恢复策略**: Nacos恢复后，根据auto_update标志决定是否同步（true则同步，false则保持本地配置）
- **FR-007**: 系统必须检测本地配置修改，自动停止Nacos监听和自动更新
- **FR-008**: 系统必须提供auto_update开关，允许用户手动恢复Nacos自动更新
- **FR-009**: 当auto_update从禁用切换为启用时，系统必须使用Nacos最新配置覆盖本地配置
- **FR-010**: 系统必须在应用启动后自动初始化Nacos监听器，基于Config中的APIServer和NacosServer字段
- **✅ FR-011（架构改进）**: 系统必须在应用启动时统一初始化Rule Engine和GeoIP数据库，而非在HTTP/TUN模式分别初始化
  - 唯一的初始化入口点：`cmd/start.go` 中的 `InitializeGlobalRuleEngine()`
  - 该函数必须检查 `GeoIPEnabled` 标志，如果为true则加载GeoIP数据库
  - GeoIP数据库路径：`~/.nonelane/GeoLite2-Country.mmdb`，文件不存在时自动下载
  - 如果GeoIP加载失败，必须优雅降级（禁用GeoIP但不阻止应用启动）
- **✅ FR-012（架构改进）**: 系统必须删除重复的初始化代码，从以下位置移除Rule Engine初始化：
  - `app/http/server.go` 中的 `initializeRuleEngine()` 函数
  - `inbound/tun/runner/start.go` 中的 `initializeRuleEngineForTUN()` 函数

### 关键实体 *(如果功能涉及数据则包含)*

- **RoutingRule**: 单条路由规则，包含id(唯一标识), type(domain/ip/geoip), condition(匹配条件), enabled(启用标志), created_at(创建时间)
- **RoutingRuleSet**: 规则集合，包含rules数组，对应to_door、black_list、none_lane三个分类
- **RoutingRulesConfig**: 完整的路由配置，包含三个RuleSet和Settings
- **RulesSettings**: 全局设置，包含geoip_enabled(GeoIP启用标志)、none_lane_enabled(NoneLane启用标志)、auto_update(自动更新标志)
- **Config**: 应用主配置，仍保有APIServer和NacosServer字段用于启动时初始化

## 成功标准 *(必填)*

### 可衡量的结果

- **SC-001**: processor/config/types.go中的Config结构不包含RoutingRules字段，代码编译成功
- **SC-002**: 路由决策逻辑覆盖所有优先级场景（NoneLane→Door→GeoIP），单元测试通过率100%
- **SC-003**: 全局开关能够有效控制NoneLane和Door功能的启用/禁用，经过集成测试验证
- **SC-004**: Nacos配置监听在应用启动5秒内成功初始化，并能接收配置变更通知
- **SC-005**: 用户通过API修改配置后，Nacos监听立即停止，auto_update开关自动关闭，经过集成测试验证
- **SC-006**: 应用在没有任何匹配规则时默认使用Direct路由，不出现空指针异常或其他崩溃
- **SC-007**: 删除RoutingRules后，所有单元测试和集成测试仍然通过，无遗留引用
- **SC-008**: 路由决策采用"首次匹配优先"策略，按NoneLane→Door→GeoIP顺序检查，第一个匹配的规则立即决定路由
- **✅ SC-009（架构改进）**: Rule Engine在应用启动时只初始化一次，所有HTTP/TUN模式共享同一个全局单例，无重复初始化
- **✅ SC-010（架构改进）**: GeoIP数据库在应用启动时自动加载（如果`GeoIPEnabled=true`），GeoIP路由功能正常工作
- **✅ SC-011（架构改进）**: 当GeoIP数据库加载失败时，应用仍能启动，自动禁用GeoIP路由，其他路由规则继续生效
- **✅ SC-012（架构改进）**: GeoIP初始化代码行数减少54行（消除重复），编译后代码大小减少

---

## 设计决策 *(澄清后补充)*

### 配置迁移策略
- **决策**: 直接替换，不保留向后兼容性
- **理由**: 简化代码逻辑，降低维护成本。旧配置需要用户手动迁移或系统提供迁移工具
- **影响**: 系统升级后，Nacos中必须使用新的RoutingRulesConfig格式

### 本地修改检测机制
- **决策**: API触发即视为用户修改
- **实现原理**: 用户仅通过网页UI的API来修改配置，任何API调用都表示用户主动修改
- **流程**: 当用户调用配置修改API时，系统自动：
  1. 保存修改到本地存储
  2. 停止Nacos监听器
  3. 将auto_update开关设为关闭
  4. 记录修改时间戳
- **影响**: 无需额外的文件监控或哈希比较，简化实现

### Nacos故障恢复策略
- **决策**: auto_update开关是唯一决策点，与Nacos故障状态无关
- **实现原理**:
  - 如果auto_update=false（用户已修改配置），Nacos无论何时恢复都不同步，本地配置保持不变
  - 如果auto_update=true（用户未修改配置），Nacos恢复后立即自动同步，覆盖本地配置为Nacos最新版本
- **行为**:
  ```
  场景1: Nacos故障时
  - 无论auto_update状态如何，系统使用本地缓存配置继续运行
  - 路由决策不受影响，继续使用当前本地配置

  场景2: Nacos恢复时
  - 如果auto_update=true: 立即同步，覆盖本地配置为Nacos版本
  - 如果auto_update=false: 不同步，保持本地配置不变

  场景3: 用户修改后Nacos故障再恢复
  - auto_update已被置为false（修改时自动设置）
  - Nacos恢复后仍不同步，本地修改被永久保留
  ```
- **影响**: 统一了故障和修改两种状态的处理，通过auto_update标志统一管理配置同步

### 路由决策优先级
- **决策**: 严格按顺序检查，首次匹配即返回结果
- **优先级顺序**: NoneLane规则 > Door规则 > GeoIP规则 > Direct（默认）
- **实现逻辑**:
  ```
  1. 如果none_lane_enabled为true:
     - 检查请求是否匹配any NoneLane rule
     - 若匹配，返回NoneLane路由，停止检查

  2. 如果door_enabled为true:
     - 检查请求是否匹配any Door rule
     - 若匹配，返回Door路由，停止检查

  3. 检查GeoIP规则（若geoip_enabled为true）
     - 检查请求IP的地理位置是否匹配any GeoIP rule
     - 若匹配，返回Door路由，停止检查

  4. 都不匹配，返回Direct路由
  ```
- **影响**: 简化决策逻辑，避免冲突处理的复杂性

### Rule Engine 全局初始化策略（架构改进 2025-12-19）
- **决策**: Rule Engine 和 GeoIP 在应用启动时统一初始化，而非在 HTTP/TUN 模式各自初始化
- **问题背景**:
  - **问题1**: GeoIP 数据库从未被加载，导致 GeoIP 路由功能完全失效
    - 症状：GeoIP Service 单例存在，但 `LoadDatabase()` 从未被调用
    - 影响：`checkGeoIP()` 永远返回 nil，GeoIP 路由功能不工作
  - **问题2**: Rule Engine 在 HTTP 模式（`app/http/server.go`）和 TUN 模式（`inbound/tun/runner/start.go`）各有一份重复的初始化代码（54行重复）
    - 症状：同一个单例在两个地方分别初始化
    - 影响：如果同时启动 HTTP 和 TUN，`Initialize()` 会被调用两次，违反单例模式
- **解决方案**:
  - 在 `cmd/start.go` 中创建唯一的全局初始化函数 `InitializeGlobalRuleEngine()`
  - 该函数负责：
    1. 初始化 Rule Engine 单例（仅一次）
    2. 加载 GeoIP 数据库（如果 `GeoIPEnabled=true`）✅ 修复 GeoIP 不加载问题
    3. 预加载 Nacos 配置
  - 从 HTTP 和 TUN 模式删除重复的初始化代码
- **GeoIP 数据库加载机制**:
  - 默认路径: `~/.nonelane/GeoLite2-Country.mmdb`
  - 如果文件不存在，自动从 `https://git.io/GeoLite2-Country.mmdb` 下载（约 6MB，需要 1-2 分钟）
  - 如果加载失败，优雅降级（禁用 GeoIP 但不影响应用启动）
- **启动流程**:
  ```
  程序启动 (cmd/start.go)
    ↓
  InitializeUser()
    ↓
  InitializeGlobalRuleEngine() ← 唯一的初始化入口点
    ├─ Step 1: 创建默认配置
    ├─ Step 2: 初始化 Rule Engine（单例，仅一次）
    ├─ Step 3: 加载 GeoIP 数据库（如果 GeoIPEnabled=true）
    └─ Step 4: 预加载 Nacos 配置
    ↓
  初始化 Nacos Configuration Manager
    ↓
  启动 HTTP/TUN 服务器（使用已初始化的 engine）
  ```
- **影响**:
  - ✅ 消除 54 行重复代码
  - ✅ 修复 GeoIP 数据库未加载问题
  - ✅ 确保 Rule Engine 单例模式正确性
  - ✅ 初始化点从 2 个（HTTP + TUN）减少到 1 个（全局）
  - ✅ 代码重复比例从 100% 降低到 0%
  - ✅ 启动流程更清晰，便于维护
- **详细文档**: 参见 `architecture-fixes.md`（包含完整的问题分析、代码证据和修复验证）

---

## 实现概述 *(补充)*

### 后端架构变更

#### 1. Config结构清理（P1）
- 从`processor/config/types.go`移除RoutingRules字段
- 删除相关的rule engine加载和初始化代码
- 更新Config验证逻辑

#### 2. 路由决策引擎（P1）
- 在`processor/routing`目录创建新的decision engine
- 实现路由决策函数：`func DecideRoute(req *Request, config *RoutingRulesConfig) RouteType`
- 按照优先级逻辑实现四层判断
- 实现GeoIP查询缓存机制

#### 3. API处理器增强（P1）
- 在config_handler中实现修改检测逻辑
- 当POST /config/routing被调用时：
  1. 保存新配置到本地
  2. 通知Nacos listener停止
  3. 更新auto_update标志为false

#### 4. Nacos集成（P2）
- 在processor中创建nacos_manager模块
- 实现配置监听器初始化
- 实现auto_update开关管理
- 当auto_update启用时，自动从Nacos拉取配置

#### 5. 启动流程（P2） - 已改进，实现全局初始化
- 在main函数中（`cmd/start.go`）：
  1. 初始化用户信息
  2. **✅ 统一初始化 Rule Engine 和 GeoIP**（通过 `InitializeGlobalRuleEngine()`）
     - 创建默认路由配置
     - 初始化 Rule Engine 单例（仅一次）
     - 加载 GeoIP 数据库（如果启用）✅ 修复架构问题
     - 预加载 Nacos 配置
  3. 初始化 Nacos Configuration Manager（加载配置、启动监听器）
  4. 启动 HTTP 服务器
  5. 启动 TUN 服务器（可选）
- **✅ 已删除重复初始化代码**：
  - 从 `app/http/server.go` 移除了 `initializeRuleEngine()` 函数
  - 从 `inbound/tun/runner/start.go` 移除了 `initializeRuleEngineForTUN()` 函数

### 前端交互（已在Phase 2完成）

#### 1. 规则管理UI
- 显示三个规则集合的管理界面
- 支持CRUD操作
- 展示global settings开关

#### 2. Nacos同步状态显示
- 显示当前auto_update状态
- 显示最后一次同步时间
- 显示Nacos连接状态

#### 3. API集成
- POST /config/routing: 保存修改（触发本地修改标记）
- GET /config/routing: 获取当前配置
- PUT /config/routing/rules/{ruleId}/toggle: 启用/禁用单个规则

---

## 实现阶段 *(补充)*

### Phase 3.1：P1任务（配置系统清理和路由引擎）
**预期工作量**: 3-4个工作日

1. **Config结构清理**
   - 删除RoutingRules字段
   - 删除相关的初始化代码
   - 更新所有引用该字段的代码
   - 编译验证无错误

2. **路由决策引擎实现**
   - 创建decision_engine.go
   - 实现DecideRoute函数
   - 实现三种规则匹配函数（domain, ip, geoip）
   - 编写单元测试（覆盖所有优先级场景）

3. **全局开关集成**
   - 更新RoutingRulesConfig模型
   - 在决策引擎中集成开关检查
   - 在前端UI中实现开关控制

4. **API处理器更新**
   - 添加修改检测逻辑
   - 实现Nacos listener通知机制
   - 更新auto_update标志管理

### Phase 3.2：P2任务（Nacos集成）
**预期工作量**: 3-4个工作日

1. **Nacos监听器实现**
   - 初始化Nacos客户端
   - 实现配置监听回调
   - 处理配置变更通知

2. **Auto-update管理**
   - 实现auto_update开关持久化
   - 实现监听器启动/停止逻辑
   - 实现配置覆盖机制

3. **启动流程集成**
   - 更新main函数初始化流程
   - 集成Nacos监听器启动
   - 添加优雅关闭机制

4. **集成测试**
   - Nacos配置同步测试
   - 本地修改后监听停止测试
   - Auto-update手动开启测试

---

## 测试策略 *(补充)*

### 单元测试（P1）

#### 配置清理验证
- 验证RoutingRules字段已删除
- 验证编译成功
- 验证Config的Validate方法正常工作

#### 路由决策逻辑
```
测试场景1: NoneLane规则匹配
- 给定: none_lane_enabled=true, domain="*.nonelane.example.com"
- 当: 请求域名匹配NoneLane规则
- 那么: 返回NoneLane路由类型

测试场景2: Door规则匹配（NoneLane未启用）
- 给定: none_lane_enabled=false, door_enabled=true
- 当: 请求域名匹配Door规则
- 那么: 返回Door路由类型

测试场景3: GeoIP规则匹配（NoneLane和Door都未启用）
- 给定: none_lane_enabled=false, door_enabled=false, geoip_enabled=true
- 当: 请求IP的地理位置匹配GeoIP规则
- 那么: 返回Door路由类型

测试场景4: 全部开关关闭
- 给定: none_lane_enabled=false, door_enabled=false
- 当: 任何请求
- 那么: 返回Direct路由类型

测试场景5: 优先级测试（NoneLane和Door同时启用）
- 给定: none_lane_enabled=true, door_enabled=true
- 当: 请求同时匹配NoneLane和Door规则
- 那么: 返回NoneLane路由类型（NoneLane优先级更高）
```

#### 全局开关测试
- 验证geoip_enabled=false时跳过GeoIP检查
- 验证none_lane_enabled=false时跳过NoneLane检查
- 验证door_enabled=false时跳过Door检查

### 集成测试（P2）

#### 本地修改检测
```
测试场景: API调用后监听停止
- 给定: Nacos监听器已启动，auto_update=true
- 当: 调用POST /config/routing修改配置
- 那么:
  1. 本地配置已更新
  2. Nacos监听器已停止
  3. auto_update标志已设为false
  4. 修改时间戳已记录
```

#### Nacos同步
```
测试场景: 配置自动同步
- 给定: Nacos监听器已启动，auto_update=true，本地配置与Nacos同步
- 当: Nacos服务器上的配置变化
- 那么: 本地应用收到变更通知并更新配置

测试场景: 手动恢复同步
- 给定: auto_update=false（用户修改了配置）
- 当: 用户在UI上手动启用auto_update开关
- 那么:
  1. Nacos监听器重新启动
  2. 本地配置立即被Nacos最新配置覆盖
  3. auto_update标志设为true
```

#### 端到端测试
```
完整流程测试:
1. 启动应用，Nacos监听器自动初始化
2. 通过网页UI修改规则（调用API）
3. 验证Nacos监听停止
4. Nacos服务器配置变更，但本地不应更新
5. 用户手动启用auto_update
6. 本地配置被Nacos配置覆盖
7. Nacos继续监听新的配置变更
```

### 性能测试

- 路由决策延迟 < 10ms（99百分位）
- Nacos监听初始化 < 5秒
- 配置修改API响应 < 500ms

### 验收测试

- 用户可以成功删除RoutingRules字段后启动应用
- 用户可以通过UI管理三种规则集合
- 用户可以看到Nacos同步状态
- 用户可以手动控制auto-update开关
- 系统按照正确的优先级进行路由决策

---

## 依赖关系和假设 *(补充)*

### 依赖关系

- **P1必须在P2之前**: 路由引擎必须先实现，Nacos集成才能工作
- **Config清理必须先完成**: 删除RoutingRules是基础，后续工作都依赖此
- **API处理器更新**: 必须在修改检测之前完成

### 假设

1. Nacos SDK（nacos-sdk-go v1.1.6）已在项目中正确配置
2. 现有的Config结构除了RoutingRules外，其他部分都是有效的
3. APIServer和NacosServer字段在启动时就可用
4. 前端UI（Phase 2）已完成，API端点都已实现
5. **✅ 已优化**: GeoIP数据库会自动下载到 `~/.nonelane/GeoLite2-Country.mmdb`，如加载失败则优雅降级
6. 用户浏览器支持标准的ES6+ JavaScript
7. **✅ 新增（架构改进）**: Rule Engine 和 GeoIP 在应用启动时全局初始化一次（cmd/start.go），不再在 HTTP/TUN 模式分别初始化

---

## 风险分析 *(补充)*

### 高风险

| 风险 | 影响 | 概率 | 缓解方案 |
|------|------|------|---------|
| RoutingRules删除导致编译失败 | 构建中断 | 中 | 创建脚本自动检查所有引用；分阶段删除 |
| 路由决策逻辑有bug导致流量误导 | 严重业务影响 | 低 | 完整的单元测试覆盖；灰度发布 |
| Nacos监听器未正确停止导致资源泄露 | 内存泄漏 | 中 | 实现监听器生命周期管理；定期健康检查 |

### 中等风险

| 风险 | 影响 | 概率 | 缓解方案 |
|------|------|------|---------|
| 本地修改检测不准确 | 配置同步混乱 | 低 | 严格通过API调用检测；日志记录所有修改 |
| GeoIP查询性能下降 | 路由延迟增加 | 中 | 实现GeoIP结果缓存；异步处理 |
| 配置格式变更导致前端兼容问题 | UI显示错误 | 低 | 版本控制；前端适配多版本格式 |
| **✅ 已缓解**: GeoIP数据库未初始化导致功能失效 | GeoIP路由不工作 | ~~中~~ → 低 | **已修复**: 全局初始化时自动加载，失败时优雅降级 |
| **✅ 已缓解**: Rule Engine重复初始化导致状态不一致 | 路由决策混乱 | ~~中~~ → 极低 | **已修复**: 仅在cmd/start.go中初始化一次，消除重复代码 |

### 低风险

| 风险 | 影响 | 概率 | 缓解方案 |
|------|------|------|---------|
| 规则验证逻辑遗漏某些格式 | 规则创建失败 | 低 | 完整的正则表达式验证；用户反馈机制 |
| 文档与实现不同步 | 维护困难 | 中 | 代码审查时同步更新；自动文档生成 |

---

## 交付成果 *(补充)*

### Phase 3.1（P1）
- ✅ 更新后的processor/config/types.go（无RoutingRules）
- ✅ 新的路由决策引擎实现
- ✅ 单元测试代码（>90% 覆盖率）
- ✅ 更新的API处理器
- ✅ 修改检测和Nacos listener通知机制
- ✅ P1任务的集成测试用例

### Phase 3.2（P2）
- ✅ Nacos管理器模块
- ✅ Auto-update开关实现
- ✅ 启动流程集成代码
- ✅ P2任务的集成测试
- ✅ 端到端测试验证
- ✅ 部署和迁移指南
- ✅ 运维文档

### 文档交付
- ✅ 架构设计文档
- ✅ API文档更新
- ✅ 部署指南
- ✅ 故障排查指南
- ✅ 配置迁移指南

