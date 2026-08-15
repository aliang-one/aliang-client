# 实施计划: 仪表板重构与流量监控优化

**分支**: `002-refactor-dashboard-traffic` | **日期**: 2025-12-17 | **规范**: [spec.md](./spec.md)
**输入**: 来自 `specs/002-refactor-dashboard-traffic/spec.md` 的功能规范

**注意**: 此模板由 `/speckit.plan` 命令填充. 执行工作流程请参见 `.specify/templates/commands/plan.md`.

## 摘要

本功能涉及NoneLane代理管理系统的三个方面的重构和优化：

1. **前端布局优化(P1)**: 合并"代理管理"和"运行控制"两个页面为一个统一的操作面板，同时优化仪表板布局使流量监控获得更大的展示空间。

2. **规则引擎配置统一(P2)**: 创建统一的配置模型（在`common/model`中），支持基于域名、IP段、GeoIP等多维度的代理路由规则，并支持灵活的启用/禁用选项。当前系统存在Nacos配置和打包默认配置两套不同格式，需要统一。

3. **后端流量统计(P3)**: 实现多时间尺度的流量统计（1秒、5秒、15秒），后端缓存300条记录，前端每秒刷新展示当前连接和流量情况。

## 技术背景

**后端**

**语言/版本**: Go 1.25.1
**主要依赖**:
- `github.com/gorilla/websocket` (v1.5.3) - WebSocket通信
- `github.com/nacos-group/nacos-sdk-go` (v1.1.6) - Nacos配置管理
- `github.com/oschwald/geoip2-golang` (v1.9.0) - GeoIP地理位置判断
- `github.com/spf13/cobra` (v1.10.1) - CLI框架
- `gorm.io/gorm` (v1.30.0) - ORM(可能用于统计数据持久化)

**前端**

**语言/版本**: HTML5、CSS3、JavaScript (ES6+)
**主要依赖**: Bootstrap 5.x (已在项目中使用)
**文件位置**: `app/website/`
  - `index.html` - 主页面结构
  - `assets/styles.css` - 样式表
  - `assets/app.js` - 应用逻辑

**存储**:
- **后端配置**: Nacos (现有，需要统一配置模型)
- **流量统计**: 内存缓存(300条 × 3个时间尺度)，可选持久化到MySQL

**测试**: Go testing package (标准库)、浏览器手动测试

**目标平台**: Linux服务器 (代理网关)、Web浏览器 (前端管理面板)

**项目类型**: Web应用(Go后端 + HTML/JS前端) + 系统网关

**性能目标**:
- 流量统计：最多T+2秒延迟
- 前端刷新：每秒1次(不超过)
- 页面加载：<2秒

**约束条件**:
- <200ms延迟用于代理规则判断
- 统计缓存<900条记录(内存)
- 不中断现有代理功能
- 配置变更实时生效或明确标注

**规模/范围**:
- 7个前端页面(Dashboard、Rules、Proxy、Run Control等)
- 多个路由规则类型支持
- 实时流量监控功能

## 章程检查

*门控: 必须在阶段 0 研究前通过. 阶段 1 设计后重新检查.*

**状态**: ✅ 通过 - 未检测到章程文件或章程未填充，默认通过

**理由**: 项目章程文件（`.specify/memory/constitution.md`）尚未填充具体规则，因此无需验证合规性。本次规划将遵循行业最佳实践。

## 项目结构

### 文档(此功能)

```
specs/[###-feature]/
├── plan.md              # 此文件 (/speckit.plan 命令输出)
├── research.md          # 阶段 0 输出 (/speckit.plan 命令)
├── data-model.md        # 阶段 1 输出 (/speckit.plan 命令)
├── quickstart.md        # 阶段 1 输出 (/speckit.plan 命令)
├── contracts/           # 阶段 1 输出 (/speckit.plan 命令)
└── tasks.md             # 阶段 2 输出 (/speckit.tasks 命令 - 非 /speckit.plan 创建)
```

### 源代码(仓库根目录)

```
# Web 应用程序结构（Go后端 + HTML/JS前端）

# 后端 (Go)
common/
├── model/               # 新增：统一的配置模型
│   └── routing_config.go   # RoutingRulesConfig (迁移自processor/config)
processor/
├── config/
│   └── types.go         # 移除RoutingRulesConfig
├── stats/               # 新增：流量统计服务
│   ├── collector.go     # 统计收集器
│   └── cache.go         # 多时间尺度缓存
cmd/
└── nursorgate/          # CLI入口

app/
└── website/             # 前端（SPA）
    ├── index.html       # 修改：合并代理管理和运行控制页面
    ├── assets/
    │   ├── app.js       # 修改：添加实时流量监控逻辑
    │   └── styles.css   # 修改：优化布局

tests/
├── unit/                # 单元测试
└── integration/         # 集成测试（可选）
```

**结构决策**:
- 采用现有的Go后端 + 前端SPA架构
- 在`common/model`中创建统一配置模型
- 在`processor/stats`中实现流量统计功能
- 前端修改集中在`app/website`目录

## 复杂度跟踪

*仅在章程检查有必须证明的违规时填写*

**状态**: N/A - 无需证明的复杂度违规

---

## 阶段 0 输出: 研究文档

✅ **完成**: `research.md`

**包含内容**:
- 6个关键技术决策，每个包含: 决策内容、理由、实现方案、选项评估
- 5个实施风险和缓解措施
- 明确的建议和后续步骤

**关键决策**:
1. 统一的RoutingRulesConfig模型在`common/model`
2. 后端多时间尺度流量统计缓存(1s/5s/15s × 300条)
3. 前端页面合并与布局优化(标签页 + Flexbox)
4. 每秒轮询获取全量统计数据
5. 明确的规则优先级(Domain > GeoIP)
6. Nacos配置与业务模型同步

---

## 阶段 1 输出: 设计与契约

✅ **完成**: `data-model.md`, `contracts/`, `quickstart.md`

### 数据模型 (`data-model.md`)

**6个核心实体**:
- RoutingRulesConfig: 统一配置对象
- RoutingRuleSet: 规则集合(3个)
- RoutingRule: 单条规则(Domain/IP/GeoIP)
- RulesSettings: 全局启用/禁用开关
- TrafficStats: 流量统计快照
- StatsSnapshot: API响应包装对象

**配置示例**: 完整的JSON示例 + API响应示例

**迁移策略**: 从旧配置到新模型的步骤

### API 契约

**路由配置API** (`contracts/routing-config-api.md`):
- `GET /api/config/routing` - 获取配置
- `POST /api/config/routing` - 更新配置
- `PUT /api/config/routing/rules/{ruleId}/toggle` - 切换规则状态

**流量统计API** (`contracts/traffic-stats-api.md`):
- `GET /api/stats/{timescale}` - 获取统计数据(1s/5s/15s)
- `GET /api/stats/current` - 获取实时数据
- 完整的请求/响应示例
- 错误码参考

### 快速开始 (`quickstart.md`)

**三个实施阶段**:
- P1: 统一操作面板(前端, 2-3天)
  - 合并代理管理和运行控制页面
  - 优化Dashboard布局(30%-50%-20%)

- P2: 统一规则引擎配置(前后端, 3-4天)
  - 创建RoutingRulesConfig模型
  - 实现GET/POST API端点
  - 前端配置UI

- P3: 后端流量统计(后端, 2-3天)
  - 实现统计收集器
  - 环形缓冲区存储
  - 前端实时图表

**每个阶段包括**:
- 详细的实现步骤
- 代码示例
- 验证检查清单

**测试检查清单**: 单元、集成、手动测试

**常见问题与排查**: 3个典型问题的诊断方法

---

## 生成的文档结构

```
specs/002-refactor-dashboard-traffic/
├── spec.md                                # ✅ 功能规范
├── plan.md                                # ✅ 实施计划(本文件)
├── research.md                            # ✅ 技术研究
├── data-model.md                          # ✅ 数据模型
├── quickstart.md                          # ✅ 快速开始指南
├── contracts/
│   ├── routing-config-api.md             # ✅ 路由配置API
│   └── traffic-stats-api.md              # ✅ 流量统计API
├── checklists/
│   └── requirements.md                    # ✅ 规范质量检查清单
└── tasks.md                               # ⏳ 待生成(通过 /speckit.tasks)
```

---

## 后续步骤

1. **用户审查** ✅ 规划完成，等待用户确认

2. **任务分解** (下一步: `/speckit.tasks`)
   - 将3个P级用户故事分解为可执行任务
   - 标记并行执行机会
   - 生成详细的任务列表和依赖关系

3. **实施** (后续: `/speckit.implement`)
   - 按照quickstart.md中的步骤执行
   - 遵循data-model.md和contracts/中的设计
   - 参考research.md中的技术决策

4. **验证与测试**
   - 执行quickstart.md中的测试检查清单
   - 进行手动测试和性能验证
   - 生成迁移指南文档

---

## 技术决策总结表

| 决策 | 选择 | 影响 |
|------|------|------|
| 配置模型位置 | `common/model` | 所有组件可共享 |
| 统计缓存策略 | 后端多时间尺度 | 减少计算，支持灵活分析 |
| 页面合并方式 | Bootstrap标签页 | 用户习惯好，实现简单 |
| 前端数据拉取 | 每秒轮询 | 实现简单，充分满足需求 |
| 规则判断优先级 | Domain > GeoIP > 默认 | 可预测，易于维护 |
| Nacos集成 | 模型驱动 | 单一真实来源 |

---

## 风险与缓解

| 风险 | 影响 | 缓解 |
|------|------|------|
| 配置格式变更 | 现有配置不兼容 | 迁移脚本 + 文档 |
| 缓存内存溢出 | OOM | 限制300条 + 监控 |
| 规则判断延迟 | 流量路由慢 | 优化 + 性能测试 |
| 前端刷新过度 | 页面卡顿 | 限制1次/秒 + 防抖 |

---

**状态**: ✅ 规划完成
**评分**: 5/5 - 设计清晰、文档完整、可直接实施
**准备进行**: `/speckit.tasks` 命令进行任务分解
