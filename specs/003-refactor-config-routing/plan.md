# 实施计划: 配置系统重构与路由引擎迁移

**分支**: `003-refactor-config-routing` | **日期**: 2025-12-17 | **规范**: [spec.md](./spec.md)
**输入**: 来自 `/specs/003-refactor-config-routing/spec.md` 的功能规范

**注意**: 此模板由 `/speckit.plan` 命令填充. 执行工作流程请参见 `.specify/templates/commands/plan.md`.

## 摘要

本功能实现配置系统的重构，主要工作包括：

1. **配置结构清理**: 从 `processor/config/types.go` 完全删除过时的 RoutingRules 字段及相关逻辑
2. **路由引擎迁移**: 将所有路由判断逻辑迁移到新的 `common/model/routing_config.go` 中的 RoutingRulesConfig 模型
3. **优先级路由决策**: 实现四层路由判断逻辑（NoneLane域名 → Door域名 → GeoIP → Direct），采用首次匹配优先策略
4. **全局开关控制**: 提供 geoip_enabled 和 none_lane_enabled 全局开关，支持快速启用/禁用路由功能
5. **Nacos配置集成**: 实现 Nacos 配置监听、自动同步与 auto_update 开关管理，确保配置持久化与热加载

**技术方法**: 采用 API 触发式修改检测（无需文件监控或哈希比较），通过 auto_update 标志统一管理 Nacos 故障和用户修改两种状态，简化配置同步逻辑。

## 技术背景

**语言/版本**: Go (版本从现有项目继承，估计 Go 1.19+)
**主要依赖**:
- Nacos SDK (nacos-sdk-go v1.1.6) - 配置中心集成
- GeoIP 库 - 地理位置查询（第三方服务或本地数据库）
- Bootstrap 5 + Chart.js - 前端UI（已在 Phase 2 完成）

**存储**:
- 本地文件配置（processor/config/types.go 结构）
- Nacos 远程配置中心（路由规则持久化）
- 内存缓存（GeoIP 查询结果、路由决策结果）

**测试**: Go testing framework
- 单元测试：路由决策逻辑、配置验证、规则匹配
- 集成测试：Nacos 同步、API 处理器、监听器生命周期
- 端到端测试：完整配置修改流程验证

**目标平台**: 后端服务器（Linux/macOS），浏览器端 ES6+ JavaScript

**项目类型**: Web 应用（后端 Go + 前端 HTML/JS/CSS）

**性能目标**:
- 路由决策延迟 < 10ms（99百分位）
- Nacos 监听初始化 < 5秒
- 配置修改 API 响应 < 500ms
- GeoIP 查询结果缓存机制

**约束条件**:
- Nacos SDK 版本兼容性（v1.1.6）
- GeoIP 数据库可用性
- 浏览器最低要求 ES6+ JavaScript 支持
- 配置迁移策略：直接替换，不保留向后兼容性

**规模/范围**: 企业级代理配置系统
- 支持多种代理类型（Door、NoneLane、Direct）
- 三类规则集管理（to_door、black_list、none_lane）
- 预计规则数量：每类 10-100 条
- 用户：内部运维人员通过 Web UI 管理

## 章程检查

*门控: 必须在阶段 0 研究前通过. 阶段 1 设计后重新检查.*

基于项目章程，本功能需要验证以下门控条件：

### 1. 配置迁移的向后兼容性原则
**章程要求**: 系统升级应保持向后兼容性，避免破坏性变更
**本功能现状**: ❌ 采用直接替换策略，不保留向后兼容性
**是否违规**: 是
**为什么需要**: 旧的 RoutingRules 结构已过时，存在格式不一致、功能重复、难以维护的问题。保留兼容性将显著增加代码复杂度。
**拒绝更简单替代方案的原因**:
- **兼容层成本**: 需要维护双重配置格式（旧 RoutingRules + 新 RoutingRulesConfig），增加解析逻辑复杂度
- **迁移清晰度**: 直接替换迫使用户明确迁移，避免长期技术债务
- **功能完整性**: 新模型引入了 auto_update、全局开关等新特性，无法简单映射到旧结构
- **建议**: 提供迁移工具（手动或脚本），在文档中明确说明升级步骤

### 2. 代码简洁性与抽象层级原则
**章程要求**: 避免过度工程，保持代码简洁
**本功能现状**: ✅ 符合
**验证**:
- 采用 API 触发式修改检测，避免了文件监控、哈希比较等复杂机制
- 通过 auto_update 单一标志统一管理 Nacos 故障和用户修改状态
- 路由决策采用简单的顺序检查，首次匹配即返回

### 3. 性能要求
**章程要求**: 关键路径操作延迟需满足用户体验标准
**本功能现状**: ✅ 符合
**验证**:
- 路由决策 < 10ms (99百分位) - 明确量化目标
- Nacos 初始化 < 5秒 - 不阻塞主流程
- API 响应 < 500ms - 用户操作即时反馈

### 4. 测试覆盖率原则
**章程要求**: 核心功能单元测试覆盖率 > 90%
**本功能现状**: ✅ 计划符合
**验证**: 规范中明确要求单元测试覆盖路由决策、配置验证、规则匹配等核心逻辑

**总体评估**: 除配置迁移向后兼容性外，所有章程要求均已满足。向后兼容性违规已充分论证必要性，建议在阶段 1 设计时制定迁移指南作为补偿措施。

## 项目结构

### 文档(此功能)

```
specs/003-refactor-config-routing/
├── spec.md                          # 功能规范（已完成，9.7/10）
├── plan.md                          # 实施计划（此文件）
├── research.md                      # 阶段 0 研究成果（待生成）
├── data-model.md                    # 阶段 1 数据模型（待生成）
├── quickstart.md                    # 阶段 1 快速开始（待生成）
├── contracts/                       # 阶段 1 API 契约（待生成）
│   ├── config-api.openapi.yaml
│   ├── rules-api.openapi.yaml
│   └── nacos-api.openapi.yaml
├── checklists/
│   └── requirements.md              # 规范质量检查（已完成）
└── tasks.md                         # 阶段 2 可执行任务（待生成）
```

### 源代码(仓库根目录)

本功能涉及后端和前端两个部分：

```
── processor/
│   ├── config/
│   │   ├── types.go                 # [P1] 删除 RoutingRules 字段，保留 APIServer/NacosServer
│   │   └── ...
│   ├── routing/                     # [P1] 新增路由引擎模块
│   │   ├── decision_engine.go       # 核心路由决策函数
│   │   ├── matcher.go               # 规则匹配器（domain/ip/geoip）
│   │   └── cache.go                 # GeoIP 缓存机制
│   ├── nacos/                       # [P2] Nacos 集成模块
│   │   ├── manager.go               # 配置监听器管理
│   │   └── listener.go              # 监听回调实现
│   └── api/
│       ├── config_handler.go        # [P1/P2] 配置 API 端点
│       └── rules_handler.go         # 规则引擎 API 端点
│
── common/
│   └── model/
│       ├── routing_config.go        # [P1] 新的 RoutingRulesConfig 模型
│       ├── config.go                # 现有 Config 模型（简化）
│       └── ...
│
── app/website/
│   ├── index.html                   # [P2 完成] UI 规则编辑界面
│   └── assets/
│       ├── app.js                   # [P2 完成] 前端规则管理逻辑（2978 行）
│       └── styles.css               # 样式文件

── cmd/
│   └── main.go                      # [P2] 启动流程修改：初始化 Nacos 监听
```

**结构决策**: 采用选项 1（单一后端项目）加前端资源的组织方式。路由引擎与 Nacos 集成分离为独立模块，便于独立测试和维护。前端已在 Phase 2 完成，本阶段重点聚焦后端实现。

## 复杂度跟踪

*仅在章程检查有必须证明的违规时填写*

| 违规 | 为什么需要 | 拒绝更简单替代方案的原因 |
|-----------|------------|-------------------------------------|
| 直接替换配置格式（无向后兼容性） | 旧的 RoutingRules 结构已过时，存在格式不一致、功能重复、代码难以维护的问题。新模型引入了 auto_update、全局开关、优先级路由等核心特性 | **1. 兼容层维护成本**: 需要同时支持两套配置格式（旧 RoutingRules + 新 RoutingRulesConfig），包括解析、验证、转换逻辑，代码复杂度至少翻倍<br>**2. 功能不对称性**: 新特性（auto_update、none_lane_enabled、geoip_enabled）无法映射到旧结构，兼容层无法提供完整功能<br>**3. 长期技术债务**: 兼容层一旦引入，将长期存在，影响未来所有配置相关开发<br>**4. 用户迁移清晰度**: 明确的版本切换强制用户正确迁移，避免隐蔽的配置不一致问题<br>**补偿措施**: 提供迁移脚本和详细文档，在升级指南中明确说明配置格式变更 |

**复杂度决策总结**: 虽然直接替换违反向后兼容性原则，但维持兼容性的成本远高于一次性迁移的代价。新架构简化了配置逻辑，通过 auto_update 单一标志统一管理状态，降低了运行时复杂度。
