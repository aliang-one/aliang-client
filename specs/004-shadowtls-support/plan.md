# 实施计划: ShadowTLS 协议支持

**分支**: `004-shadowtls-support` | **日期**: 2025-12-19 | **规范**: [spec.md](./spec.md)
**输入**: 来自 `/specs/004-shadowtls-support/spec.md` 的功能规范

**注意**: 此模板由 `/speckit.plan` 命令填充. 执行工作流程请参见 `.specify/templates/commands/plan.md`.

## 摘要

实现 ShadowTLS 作为 Shadowsocks 的插件支持，而非独立协议。当 Shadowsocks 配置中包含 `plugin: shadow-tls` 字段时，系统应使用 ShadowTLS 代理实现；否则使用标准 Shadowsocks。

**核心需求**:
1. 扩展 `processor/config/types.go` 中的 `ShadowsocksConfig`，添加 `Plugin` 和 `PluginOpts` 字段
2. 在 `outbound/proxy/shadowtls/` 实现 ShadowTLS 代理模块
3. 实现协议选择逻辑：根据 `plugin` 字段选择 ShadowTLS 或标准 Shadowsocks
4. 支持 ShadowTLS 的 TLS 伪装功能（host、password、version 参数）
5. 在 cmd 启动时正确解析包含 ShadowTLS 插件的配置

**技术方法**: 参考 singbox 的 shadow-tls 实现，采用与现有 VLESS 代理相同的接口模式，确保与其他代理（NoneLane、Door、Direct）共存。

## 技术背景

**语言/版本**: Go 1.19+ (项目使用 Go 1.25.1)
**主要依赖**:
  - singbox 的 shadow-tls 实现（参考）
  - Go 内置 crypto 包（TLS 握手、加密）
  - 现有 Shadowsocks 代理实现 (`outbound/proxy/shadowsocks/`)
  - 现有 VLESS 代理实现（接口模式参考）

**存储**: N/A （纯网络协议实现，无持久化）
**测试**: Go testing 包 (`go test`)，单元测试和集成测试
**目标平台**: 与项目相同（macOS、Linux、可能的 Windows）
**项目类型**: 单一 Go 模块，属于 nursorgate2 代理系统
**性能目标**:
  - 代理初始化 < 1 秒
  - TCP 连接建立 < 3 秒
  - 支持 100+ 并发连接
  - 数据转发延迟与标准 Shadowsocks 相当

**约束条件**:
  - 配置验证 100% 覆盖必需参数
  - 无资源泄漏（连接正确关闭）
  - 与现有代理系统兼容
  - 命令行启动时正确解析

**规模/范围**:
  - 单一功能模块（ShadowTLS 代理）
  - 支持 plugin-opts 中的 3 个参数（host、password、version）
  - 支持 Shadowsocks 的所有加密方式

## 章程检查

*门控: 必须在阶段 0 研究前通过. 阶段 1 设计后重新检查. *

基于项目的 CLAUDE.md 配置和开发实践：

- ✅ **Git 提交政策**: 无自动提交。所有更改需显式用户同意才能提交（符合 CLAUDE.md）。
- ✅ **代码质量**: 遵循 Go 最佳实践，使用 gofmt、go vet、go test。
- ✅ **测试优先**: 在实现代码之前编写测试（用户故事对应的验收场景）。
- ✅ **接口标准**: 遵循现有的 proxy 接口（参考 VLESS、Shadowsocks），实现 `Dial`、`DialUDP`、`Addr`、`Proto` 方法。
- ✅ **依赖管理**: 仅使用现有依赖（singbox 的相关包作为参考，不引入新的外部依赖）。
- ✅ **文档**: 提供配置示例和 API 文档。

**可能的复杂性**:
- ShadowTLS 的 TLS 握手与 Shadowsocks 加密的双层通信（但这是必要的功能）
- 与现有 Shadowsocks 代理的兼容性和协议选择逻辑（基于 plugin 字段）

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
# 选项: 单一 Go 模块 - 代理功能包
outbound/proxy/
├── shadowtls/
│   ├── shadowtls.go           # ShadowTLS 代理主实现
│   ├── config.go              # 配置类型定义（嵌套在 processor/config 中）
│   ├── protocol.go            # ShadowTLS 协议握手与通信
│   ├── connection.go          # 连接管理和数据转发
│   └── shadowtls_test.go      # 单元测试

processor/
├── config/
│   ├── types.go               # 扩展 ShadowsocksConfig，添加 Plugin 和 PluginOpts
│   └── types_test.go          # 配置解析测试

cmd/
├── start.go                   # 修改以支持 ShadowTLS 代理初始化和解析
└── [相关初始化代码]

# 文档和规范
specs/004-shadowtls-support/
├── spec.md                    # 功能规范（已存在）
├── plan.md                    # 此文件
├── research.md                # 阶段 0：研究和技术决策（待生成）
├── data-model.md              # 阶段 1：数据模型和配置结构（待生成）
├── quickstart.md              # 阶段 1：快速开始指南（待生成）
└── contracts/
    └── shadowtls-config.json  # 配置示例 JSON Schema（待生成）
```

**结构决策**:
- 选择单一 Go 模块结构，在 `outbound/proxy/shadowtls/` 实现 ShadowTLS 代理。
- 扩展现有的 `processor/config/types.go` 添加 ShadowTLS 配置支持。
- 修改 `cmd/start.go` 在启动时正确解析和初始化 ShadowTLS 代理。
- 参考 VLESS（`outbound/proxy/vless/`）和 Shadowsocks（`outbound/proxy/shadowsocks/`）的实现模式。

## 复杂度跟踪

*仅在章程检查有必须证明的违规时填写*

无违规。此功能遵循现有的代理实现模式，不引入不必要的复杂性。
