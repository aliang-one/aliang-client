# 任务清单: 仪表板重构与流量监控优化

**功能分支**: `002-refactor-dashboard-traffic`
**创建时间**: 2025-12-17
**规范**: [spec.md](./spec.md) | **计划**: [plan.md](./plan.md)

---

## 执行概览

**总任务数**: 52个任务
**用户故事**: 3个(P1/P2/P3)
**预期时间**: 7-10天(并行执行)

| 阶段 | 任务数 | 范围 | 优先级 |
|------|--------|------|--------|
| **1. 设置** | 3 | 项目初始化 | 必须 |
| **2. 基础** | 4 | 共享基础设施 | 必须 |
| **3. P1** | 18 | 统一操作面板(前端) | P1 |
| **4. P2** | 18 | 规则引擎配置(前后端) | P2 |
| **5. P3** | 6 | 流量统计(后端) | P3 |
| **6. 完善** | 3 | 文档和优化 | 可选 |

---

## 🎯 用户故事优先级

### US1 (P1) - 统一操作面板
**优先级**: P1 (MVP核心)
**范围**: 前端页面合并和布局优化
**预期**: 2-3天
**独立测试**: 合并页面功能完整、布局符合30%-50%-20%比例、页面响应式正常

### US2 (P2) - 统一规则引擎配置
**优先级**: P2
**范围**: 前后端配置模型和UI
**预期**: 3-4天
**依赖**: 基础阶段完成
**独立测试**: 配置加载/保存、Nacos持久化、规则编辑UI功能完整

### US3 (P3) - 后端流量统计
**优先级**: P3
**范围**: 后端统计收集和前端展示
**预期**: 2-3天
**依赖**: 基础阶段完成
**独立测试**: 数据收集准确、缓存容量限制、API响应正确、前端实时刷新

---

## 并行执行机会

### P1 和 P2 可部分并行
- P1前端页面合并可与P2配置模型独立进行
- 推荐先完成P1(2-3天)再开始P2(3-4天)

### P1 和 P3 可部分并行
- P3后端统计可与P1前端页面合并同时进行
- 但P3前端展示需要等待P1页面优化完成

### P2 和 P3 完全可并行
- P2配置与P3统计功能完全独立
- 可以同时进行开发和测试

**建议执行顺序**: P1 → (P2 || P3) → 完善

---

## 依赖关系图

```
阶段1: 设置
  ↓
阶段2: 基础 (包括API路由注册)
  ├─→ 阶段3: P1 (2-3天) ──→ US1完成 ✓
  └─→ 阶段4: P2 (3-4天, 可与P1并行后期) ──→ US2完成 ✓
      └─→ 阶段5: P3 (2-3天, 可与P2并行) ──→ US3完成 ✓
          └─→ 阶段6: 完善 ──→ 全部完成 ✅

关键路径: 设置 → 基础 → P1 → (P2 + P3) → 完善
总时间: 约 10-14天顺序 / 7-10天并行
```

---

# 任务详细清单

## 阶段 1: 设置(项目初始化)

**目标**: 为所有后续工作创建基础环境

- [ ] T001 验证项目环境并创建功能特定的目录结构
  - 验证Go 1.25.1和依赖可用
  - 创建 `common/model/` 目录(如不存在)
  - 创建 `processor/stats/` 目录(如不存在)
  - 验证 `app/website/` 目录结构正确

- [ ] T002 创建feature分支上下文和开发指南
  - 确认当前分支为 `002-refactor-dashboard-traffic`
  - 阅读 `specs/002-refactor-dashboard-traffic/quickstart.md`
  - 准备本地开发环境(Go mod, 前端依赖)

- [ ] T003 设置Nacos本地测试环境(如需要)
  - 验证Nacos服务可用或启动Docker容器
  - 测试Nacos连接配置
  - 创建 `routing-rules` 配置项模板

---

## 阶段 2: 基础(共享阻塞先决条件)

**目标**: 为所有用户故事提供基础支撑

### 后端基础设施

- [ ] T004 [P] 在 `common/model/routing_config.go` 中定义统一的路由规则配置模型
  - 定义 `RoutingRulesConfig` 结构体(包含to_door, black_list, none_lane, settings)
  - 定义 `RoutingRuleSet` 结构体
  - 定义 `RoutingRule` 结构体(ID, Type, Condition, Enabled, CreatedAt)
  - 定义 `RulesSettings` 结构体(GeoIPEnabled, NoneLaneEnabled)
  - 实现 `Validate()` 方法验证所有规则
  - 实现 `ToJSON()` 和 `NewRoutingRulesConfigFromJSON()` 方法
  - 参考: `specs/002-refactor-dashboard-traffic/data-model.md`

- [ ] T005 [P] 创建 `cmd/nursorgate/handlers/` 目录和基础路由处理器
  - 创建 `cmd/nursorgate/handlers/base.go` 定义基础处理器类(包含auth、logger)
  - 实现通用的错误响应格式
  - 实现通用的成功响应格式
  - 注册路由到主应用(在main.go中)

- [ ] T006 [P] 在main.go中注册API路由前缀
  - 添加 `/api/config` 路由前缀处理
  - 添加 `/api/stats` 路由前缀处理
  - 确保所有API端点都在 `/api` 路径下

- [ ] T007 创建前端基础页面导航机制
  - 在 `app/website/assets/app.js` 中创建页面管理系统
  - 实现 `showPage(pageId)` 函数
  - 实现 `hidePage(pageId)` 函数
  - 创建页面路由表(将页面ID映射到HTML元素ID)
  - 添加菜单导航事件处理

---

## 阶段 3: P1 - 统一操作面板(前端)

**目标**: 合并"代理管理"和"运行控制"页面，优化仪表板布局

**验收标准**:
- ✅ 新合并页面完全正常工作
- ✅ 标签页切换功能正常
- ✅ 仪表板布局符合30%-50%-20%的比例
- ✅ 页面在不同屏幕尺寸下响应式正常

### 前端页面合并

- [ ] T008 [US1] [P] 在 `app/website/index.html` 中创建合并的"代理管理与运行控制"页面
  - 新增 `<div id="proxy-control-page">` 页面容器
  - 创建Bootstrap标签页结构(Nav tabs + Tab content)
  - 第一个标签页: "代理管理"(从现有代理管理页复制内容)
  - 第二个标签页: "运行控制"(从现有运行控制页复制内容)
  - 删除原有的单独代理管理和运行控制页面定义

- [ ] T009 [US1] 更新导航菜单链接指向新的合并页面
  - 在 `app/website/index.html` 中更新菜单项
  - 将"代理管理"菜单项指向新的合并页面 ID
  - 删除单独的"运行控制"菜单项(合并到代理管理)
  - 确保菜单显示顺序逻辑正确

- [ ] T010 [US1] [P] 在 `app/website/assets/app.js` 中更新页面导航逻辑
  - 更新页面导航函数识别合并后的页面
  - 添加标签页切换事件监听
  - 确保从菜单点击可以正确显示合并页面

- [ ] T011 [US1] [P] 验证合并页面中的所有代理和运行控制功能完整
  - 测试代理列表显示
  - 测试代理切换功能
  - 测试运行状态显示
  - 测试所有控制按钮(启动、停止、证书等)
  - 在浏览器控制台检查是否有JavaScript错误

### 仪表板布局优化

- [ ] T012 [US1] [P] 重新设计 `app/website/index.html` 中的Dashboard页面布局结构
  - 修改Dashboard容器为Flexbox布局(flex-direction: column, height: 100%)
  - 创建三个区间容器: metrics-section, traffic-section, other-section
  - metrics-section: flex: 0 0 30% (关键指标区)
  - traffic-section: flex: 1 1 50% (流量监控区)
  - other-section: flex: 0 0 20% (其他内容区)
  - 参考快速开始中的HTML示例代码

- [ ] T013 [US1] [P] 在 `app/website/assets/styles.css` 中添加仪表板布局样式
  - 添加 `.dashboard` 高度100%和flexbox样式
  - 添加 `.metrics-section` 布局和边框样式
  - 添加 `.traffic-section` 布局和溢出处理
  - 添加 `.other-section` 布局和边框样式
  - 优化指标卡片紧凑显示(padding: 8-10px)
  - 添加响应式媒体查询(在小屏幕上调整比例)

- [ ] T014 [US1] [P] 创建8个关键指标卡片UI在Dashboard关键指标区
  - 在metrics-section中创建8个卡片(运行状态、当前代理、运行模式、规则状态、总上传、总下载、活跃连接、总流量)
  - 使用Bootstrap grid系统(col-lg-3 col-md-6)
  - 每个卡片包含: card-title、card-text(用于显示值)
  - 添加卡片ID便于后续JavaScript更新(例如: #status-value)

- [ ] T015 [US1] [P] 在Dashboard流量监控区中创建实时流量图表容器
  - 在traffic-section中创建card结构
  - 添加时间尺度选择按钮组(1秒、5秒、15秒)
  - 添加 `<canvas id="traffic-chart">` 容器(用于Chart.js)
  - 添加容器ID便于后续JavaScript初始化

- [ ] T016 [US1] 在 `app/website/assets/app.js` 中添加Dashboard指标更新函数
  - 创建 `updateDashboardMetrics(data)` 函数
  - 接收指标数据对象
  - 更新每个指标卡片的值(使用querySelector定位元素)
  - 保留现有获取指标数据的逻辑，只修改显示方式

- [ ] T017 [US1] [P] 验证仪表板布局响应式正确且指标显示
  - 在PC浏览器上测试布局(30%-50%-20%比例)
  - 在平板和手机上测试响应式
  - 验证指标卡片正常显示和更新
  - 使用浏览器开发者工具验证Flexbox布局正确

- [ ] T018 [US1] 在 `app/website/index.html` 中引入Chart.js库(如未引入)
  - 在HTML head中添加Chart.js CDN链接
  - 验证库加载正确(检查浏览器console)

### P1验收检查点

- [ ] T019 [US1] P1 阶段完成检查
  - ✅ 合并页面创建完成
  - ✅ 标签页切换正常
  - ✅ 仪表板布局符合要求
  - ✅ 所有功能完整且无错误
  - ✅ 页面响应式正常

---

## 阶段 4: P2 - 统一规则引擎配置(前后端)

**目标**: 创建统一的配置模型，实现API端点和前端UI

**验收标准**:
- ✅ GET /api/config/routing 返回完整配置
- ✅ POST /api/config/routing 正确保存到Nacos
- ✅ 前端配置加载和编辑UI完整
- ✅ 配置持久化和实时同步正常

### 后端配置API

- [ ] T020 [US2] [P] 创建 `cmd/nursorgate/handlers/config.go` 实现配置API处理器
  - 创建 `ConfigHandler` 结构体(包含nacosClient、logger等)
  - 实现 `GetRoutingConfig()` 处理器函数
    - 从Nacos读取 "routing-rules" 配置
    - 反序列化为RoutingRulesConfig对象
    - 返回JSON响应(参考contracts/routing-config-api.md)
  - 实现 `UpdateRoutingConfig()` 处理器函数
    - 接收RoutingRulesConfig JSON
    - 调用Validate()方法验证
    - 序列化后写入Nacos "routing-rules" 配置
    - 返回成功/错误响应
  - 实现 `ToggleRuleStatus()` 处理器函数
    - 接收规则ID和启用状态
    - 从Nacos读取配置，查找并更新规则
    - 写入回Nacos，返回更新后的规则

- [ ] T021 [US2] [P] 在 `cmd/nursorgate/main.go` 中注册配置API路由
  - 添加GET /api/config/routing 路由
  - 添加POST /api/config/routing 路由
  - 添加PUT /api/config/routing/rules/{ruleId}/toggle 路由
  - 确保所有路由都经过认证中间件

- [ ] T022 [US2] 在 `processor/config/` 中创建配置管理服务(可选，如不使用Nacos直接访问)
  - 创建 `config_service.go` 文件
  - 实现 `LoadRoutingConfig()` 函数(从Nacos读取)
  - 实现 `SaveRoutingConfig()` 函数(写入Nacos)
  - 实现缓存层(减少Nacos访问)

### 前端配置UI

- [ ] T023 [US2] [P] 在 `app/website/index.html` 中创建规则引擎配置页面结构
  - 新增 `<div id="rules-engine-page">` 页面容器
  - 创建全局设置卡片(GeoIP和NoneLane启用/禁用复选框)
  - 创建三个标签页: "To Door规则"、"黑名单"、"NoneLane规则"
  - 每个标签页包含规则列表和编辑表单

- [ ] T024 [US2] [P] 在 `app/website/assets/app.js` 中实现配置加载函数
  - 创建 `loadRoutingConfig()` 异步函数
    - 调用GET /api/config/routing
    - 获取RoutingRulesConfig JSON
    - 调用 `populateRulesUI()` 填充前端表单
  - 创建 `populateRulesUI(config)` 函数
    - 显示全局设置(GeoIP和NoneLane的启用状态)
    - 为每个规则集的规则列表创建表格行
    - 为每行添加编辑和删除按钮

- [ ] T025 [US2] [P] 在 `app/website/assets/app.js` 中实现规则编辑函数
  - 创建 `addRule(ruleSet)` 函数(打开新规则表单)
  - 创建 `editRule(ruleId, ruleSet)` 函数(加载现有规则到表单)
  - 创建 `deleteRule(ruleId, ruleSet)` 函数(从规则集删除规则)
  - 创建 `generateRuleId(type, timestamp)` 函数(生成规则ID)

- [ ] T026 [US2] [P] 在 `app/website/assets/app.js` 中实现配置保存函数
  - 创建 `saveRoutingConfig()` 异步函数
    - 从UI表单收集配置数据
    - 构建RoutingRulesConfig对象
    - 调用POST /api/config/routing
    - 显示成功/错误消息
    - 重新加载配置(调用loadRoutingConfig)
  - 创建 `extractConfigFromUI()` 函数(从表单提取数据)

- [ ] T027 [US2] [P] 在 `app/website/assets/app.js` 中实现规则验证函数
  - 创建 `validateRule(rule)` 函数
    - 检查condition字段非空
    - 根据type验证condition格式:
      - domain: 合法域名或通配符模式
      - ip: 合法CIDR表示法
      - geoip: ISO 3166-1 alpha-2国家代码
    - 返回验证结果和错误消息

- [ ] T028 [US2] [P] 在 `app/website/assets/styles.css` 中添加配置页面样式
  - 添加规则编辑表单样式
  - 添加规则列表表格样式
  - 添加验证错误提示样式
  - 添加按钮和输入框样式

### 前端配置UI详细实现

- [ ] T029 [US2] 在规则编辑表单中添加规则类型选择(Domain/IP/GeoIP)
  - 创建select下拉框列出三种类型
  - 根据类型变化显示不同的帮助文本:
    - Domain: "输入域名模式，如: *.google.com 或 example.com"
    - IP: "输入CIDR表示法，如: 192.168.0.0/16"
    - GeoIP: "输入ISO 3166-1国家代码，如: US, CN"
  - 添加客户端验证

- [ ] T030 [US2] 创建规则编辑模态框UI
  - 为添加/编辑规则创建Bootstrap模态框
  - 包含字段: 类型(select)、条件(text input)、启用(checkbox)
  - 底部: 保存和取消按钮
  - 提交时触发验证和保存

- [ ] T031 [US2] [P] 在配置页面添加全局设置开关UI
  - GeoIP启用/禁用复选框
  - NoneLane代理启用/禁用复选框
  - 在保存时一并提交

### P2验收检查点

- [ ] T032 [US2] P2 阶段完成检查
  - ✅ 配置模型定义完整
  - ✅ 后端API端点实现正确
  - ✅ Nacos读写功能正常
  - ✅ 前端UI加载和显示配置
  - ✅ 前端编辑和保存配置
  - ✅ 规则验证正常
  - ✅ 配置持久化到Nacos
  - ✅ 页面刷新后配置保留

---

## 阶段 5: P3 - 后端流量统计(后端)

**目标**: 实现多时间尺度流量统计，提供API端点，前端展示实时数据

**验收标准**:
- ✅ 后端收集流量统计数据
- ✅ 缓存正确存储300条记录
- ✅ GET /api/stats/{timescale} 返回正确数据
- ✅ 前端每秒刷新并显示实时流量
- ✅ 时间尺度切换正常

### 后端统计收集

- [ ] T033 [US3] [P] 创建 `processor/stats/types.go` 定义统计数据类型
  - 定义 `TrafficStats` 结构体(Timestamp, ActiveConnections, UploadBytes, DownloadBytes)
  - 定义 `StatsSnapshot` 结构体(Timescale, ActiveConnections, Stats数组)
  - 定义 `RingBuffer` 结构体(用于缓存)

- [ ] T034 [US3] [P] 创建 `processor/stats/cache.go` 实现环形缓冲区
  - 实现 `RingBuffer[T]` 泛型(或使用interface{})
  - 实现 `Push(item)` 方法(FIFO, 容量限制为300)
  - 实现 `GetAll()` 方法(返回所有项的切片，按时间顺序)
  - 使用sync.RWMutex确保并发安全

- [ ] T035 [US3] [P] 创建 `processor/stats/collector.go` 实现统计收集器
  - 定义 `StatsCollector` 结构体
    - cache1s, cache5s, cache15s: 三个RingBuffer
    - trafficData: 当前流量数据源(可从现有代理服务读取)
    - mu: sync.RWMutex
  - 实现 `NewStatsCollector()` 构造函数
  - 实现 `Start()` 方法启动后台任务
  - 实现 `collectEvery1Second()` 方法
    - 每秒从流量数据源读取当前连接数和流量
    - 创建TrafficStats对象(timestamp=当前秒)
    - Push到cache1s
    - 同时Push到cache5s(每5个数据点一次)
    - 同时Push到cache15s(每15个数据点一次)

- [ ] T036 [US3] 实现与现有代理服务的集成
  - 在StatsCollector中添加接口与现有流量统计服务通信
  - 读取当前活跃连接数(ActiveConnections)
  - 读取总上传流量(UploadBytes)
  - 读取总下载流量(DownloadBytes)
  - 注意: 这可能需要与现有代码的集成点协商

### 后端统计API

- [ ] T037 [US3] [P] 创建 `cmd/nursorgate/handlers/stats.go` 实现统计API处理器
  - 创建 `StatsHandler` 结构体(包含collector等)
  - 实现 `GetStats(timescale)` 处理器函数
    - 验证timescale参数(1s/5s/15s)
    - 调用collector.GetStats(timescale)获取StatsSnapshot
    - 返回JSON响应(参考contracts/traffic-stats-api.md)
  - 实现 `GetCurrentStats()` 处理器函数
    - 返回最新的流量快照(当前连接数和速率)

- [ ] T038 [US3] [P] 在 `cmd/nursorgate/main.go` 中注册统计API路由和启动收集器
  - 实例化StatsCollector
  - 调用collector.Start()启动后台任务
  - 添加GET /api/stats/{timescale} 路由
  - 添加GET /api/stats/current 路由
  - 在应用关闭时优雅停止collector

### 前端流量监控

- [ ] T039 [US3] [P] 在 `app/website/assets/app.js` 中实现流量数据刷新函数
  - 创建 `currentTimescale` 变量(默认"1s")
  - 创建 `refreshTrafficStats()` 异步函数
    - 调用GET /api/stats/{currentTimescale}
    - 获取StatsSnapshot JSON
    - 调用 `renderChart(data.stats)` 渲染图表
    - 调用 `updateConnectionInfo(data.active_connections)` 更新连接数显示
  - 在页面加载时调用一次
  - 使用setInterval(refreshTrafficStats, 1000)每秒刷新一次

- [ ] T040 [US3] [P] 在 `app/website/assets/app.js` 中实现Chart.js图表初始化和更新
  - 创建 `renderChart(statsArray)` 函数
    - 初始化Chart.js图表(如果首次调用)
    - 或更新现有图表数据
    - X轴: 时间戳(Unix时间或格式化时间)
    - Y轴左: 流量(Mbps或字节)
    - Y轴右: 活跃连接数
    - 两条线: 上传流量和下载流量
    - 柱状图: 活跃连接数

- [ ] T041 [US3] [P] 在 `app/website/assets/app.js` 中实现时间尺度切换
  - 为时间尺度按钮(1秒、5秒、15秒)添加点击事件
  - 点击时更新 `currentTimescale` 变量
  - 立即调用refreshTrafficStats()获取新数据
  - 更新按钮的视觉反馈(active/highlight)

- [ ] T042 [US3] [P] 在 `app/website/assets/app.js` 中实现活跃连接数显示
  - 创建 `updateConnectionInfo(connectionCount)` 函数
    - 显示当前活跃连接数
    - 可选: 显示平均连接数、峰值连接数

- [ ] T043 [US3] [P] 在 `app/website/assets/styles.css` 中添加流量监控样式
  - 添加图表容器样式
  - 添加时间尺度按钮组样式
  - 添加连接数显示样式
  - 添加图表响应式样式

- [ ] T044 [US3] 优化流量图表展示
  - 配置Chart.js选项(响应式、动画)
  - 设置图表刷新时避免闪烁(使用update()而非销毁重建)
  - 添加图表缩放/平移功能(可选)

### P3验收检查点

- [ ] T045 [US3] P3 阶段完成检查
  - ✅ 后端统计收集器正常运行
  - ✅ 三个时间维度的缓存正确工作
  - ✅ API返回正确的统计数据
  - ✅ 前端每秒刷新一次
  - ✅ 图表显示流量数据
  - ✅ 连接数显示正确
  - ✅ 时间尺度切换正常
  - ✅ 无性能问题或内存泄漏

---

## 阶段 6: 完善与文档

**目标**: 最终优化、文档、测试和部署准备

- [ ] T046 [P] 生成API文档
  - 确保 `contracts/routing-config-api.md` 和 `contracts/traffic-stats-api.md` 完整
  - 生成OpenAPI/Swagger规范文件(可选)
  - 创建API使用示例文档

- [ ] T047 [P] 创建配置迁移指南
  - 记录从旧配置格式到新RoutingRulesConfig的迁移步骤
  - 提供迁移脚本(如有必要)
  - 编写迁移测试用例

- [ ] T048 执行完整功能集成测试
  - 测试P1 + P2 + P3的完整工作流
  - 测试配置保存后是否影响路由判断
  - 测试流量统计是否准确反映代理活动
  - 验证前后端交互的完整性

---

# 独立测试计划

## US1 (P1) - 统一操作面板 独立测试

**何时测试**: 完成T019后

**测试步骤**:
1. 访问仪表板主页
2. 验证关键指标正常显示(8个指标卡片)
3. 验证指标占用空间≤30%
4. 验证流量监控区域占用≥50%
5. 点击左侧菜单的"代理管理"
6. 验证合并页面加载正确
7. 在标签页之间切换(代理管理 ↔ 运行控制)
8. 验证每个标签页内容完整且功能正常
9. 在代理管理标签页中切换一个代理
10. 返回仪表板，验证"当前代理"指标已更新

**预期结果**: ✅ 所有功能正常，页面响应式无问题

## US2 (P2) - 统一规则引擎配置 独立测试

**何时测试**: 完成T032后

**测试步骤**:
1. 访问规则引擎配置页面
2. 验证全局设置显示(GeoIP和NoneLane开关)
3. 验证三个规则集标签页加载
4. 在每个标签页中查看现有规则
5. 创建一个新的Domain规则(输入: *.test.com)
6. 验证规则ID自动生成
7. 保存配置
8. 刷新页面
9. 验证新规则仍然存在(从Nacos读取)
10. 编辑之前创建的规则
11. 删除该规则
12. 保存配置
13. 验证规则已删除且持久化

**预期结果**: ✅ 配置加载、编辑、保存、持久化全部正常

## US3 (P3) - 后端流量统计 独立测试

**何时测试**: 完成T045后

**测试步骤**:
1. 启动代理服务并产生一些流量
2. 访问仪表板
3. 观察流量监控图表加载
4. 验证图表显示上传和下载流量数据
5. 验证活跃连接数显示
6. 等待5秒，观察图表是否每秒更新一次(非闪烁)
7. 点击时间尺度按钮"5秒"
8. 验证图表数据切换到5秒维度
9. 点击时间尺度按钮"15秒"
10. 验证图表数据切换到15秒维度
11. 返回"1秒"维度
12. 停止代理流量
13. 观察图表在无流量时是否正确显示零值

**预期结果**: ✅ 数据采集准确，刷新正常，时间尺度切换成功

---

# 并行执行示例

## 推荐执行顺序(最优路径)

```
Week 1:
  Day 1-2: T001 → T002 → T003 (设置)
  Day 3-4: T004, T005, T006, T007 (并行基础任务)
  Day 5-6: T008-T019 (P1前端页面合并, 独立完成)

Week 2:
  Day 1-2: (P1独立测试) → T020-T032 (P2配置)
  Day 2-3: T033-T045 (P3统计, 可与P2并行)
  Day 4: T046-T048 (完善和集成测试)
```

## 并行执行场景A (完全并行P2和P3)

```
Day 1-2: T001-T003 (设置)
Day 3-4: T004-T007 (基础)
Day 5-6: T008-T019 (P1)
Day 7-10: (并行执行)
         分支A: T020-T032 (P2配置, 开发者A)
         分支B: T033-T045 (P3统计, 开发者B)
Day 11: T046-T048 (完善, 合并验证)
```

## 并行执行场景B (P1和P2部分并行)

```
Day 1-2: T001-T003 (设置)
Day 3-4: T004-T007 (基础)
Day 5-6: T008-T019 (P1)
Day 7: T020-T021 (P2后端API基础)
Day 8-9: (并行执行)
        分支A: T022-T031 (P2前端UI, 开发者A)
        分支B: T033-T044 (P3统计, 开发者B)
Day 10: T032, T045 (两个故事的验收)
Day 11: T046-T048 (完善)
```

---

# 实现策略

## MVP (最小可行产品)

**范围**: 仅US1 (P1 - 统一操作面板)
**任务**: T001-T019
**时间**: 2-3天
**交付价值**:
- 用户可以在单一页面中管理代理和运行控制
- 仪表板布局优化，流量监控获得更多空间
- 改善了用户体验，减少了页面跳转

**何时选择MVP**:
- 如果需要快速交付某个版本
- 如果UI优化是最高优先级
- 用于验证用户反馈

## 增量交付

**第一版(T001-T019)**: P1完成
- 发布并获取用户反馈
- 修复任何UI问题
- 优化性能

**第二版(T020-T032)**: P1 + P2完成
- 用户可以编辑路由规则
- 配置持久化到Nacos
- 提高了系统灵活性

**第三版(T033-T045)**: P1 + P2 + P3完成
- 完整的流量监控功能
- 实时数据采集和展示
- 生产就绪

**第四版(T046-T048)**: 完善和文档
- 完整的API文档
- 迁移指南
- 性能和安全优化

---

# 依赖矩阵

| 任务ID | 依赖任务 | 是否关键 | 备注 |
|--------|----------|----------|------|
| T002 | T001 | 否 | 可与T001并行 |
| T003 | T001 | 否 | Nacos可选，可与T001-T002并行 |
| T004-T007 | T001, T002, T003 | 是 | 基础任务，所有故事都依赖 |
| T008-T019 | T004-T007 | 否 | P1可部分独立(但需要基础路由) |
| T020-T032 | T004-T007 | 是 | P2需要RoutingRulesConfig模型 |
| T033-T045 | T004-T007 | 是 | P3需要基础API路由 |
| T046-T048 | 所有故事 | 否 | 完善任务，可在最后进行 |

---

# 成功指标

| 指标 | 目标 | 验证方式 |
|------|------|----------|
| 页面响应时间 | <500ms | 浏览器开发工具 |
| API响应时间 | <50ms | curl或Postman测试 |
| 缓存内存使用 | <50MB(300条 × 3维度) | Go pprof |
| 流量统计延迟 | <2秒 | 观察图表更新 |
| 规则判断延迟 | <200ms | 后端日志 |
| 无JavaScript错误 | 100% | 浏览器console |
| 配置持久化成功率 | 100% | 保存后刷新验证 |

---

**生成时间**: 2025-12-17
**任务管理**: 使用此清单逐个完成任务，每个完成后标记 [x]
**问题追踪**: 遇到阻塞时参考 `specs/002-refactor-dashboard-traffic/quickstart.md` 中的常见问题部分
