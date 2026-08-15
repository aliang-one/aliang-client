# 功能规范: ShadowTLS 协议支持

**功能分支**: `004-shadowtls-support`
**创建时间**: 2025-12-19
**状态**: 草稿
**输入**: 用户描述: "在 outbound/proxy 下实现对 shadowtls 协议的支持，参考 vless.go 的实现，借鉴 singbox"

## 用户场景与测试 *(必填)*

<!--
  重要说明: 用户故事应按重要性排序, 作为用户旅程进行优先级划分.
  每个用户故事/旅程必须能够独立测试——这意味着即使只实现其中一个, 
  你仍然应该有一个可行的 MVP(最小可行产品)来交付价值.

  为每个故事分配优先级(P1、P2、P3 等), 其中 P1 是最关键的.
  将每个故事视为独立的功能切片, 可以: 
  - 独立开发
  - 独立测试
  - 独立部署
  - 独立向用户演示
-->

### 用户故事 1 - 配置 Shadowsocks + ShadowTLS 插件代理 (优先级: P1)

用户希望能够配置一个带有 ShadowTLS 插件的 Shadowsocks 代理服务器作为出站代理。配置中包含 Shadowsocks 的标准字段（服务器地址、端口、加密方式、密码）和 ShadowTLS 插件的特定字段（plugin: shadow-tls, plugin-opts: {host, password, version}）。系统应该能够正确解析这些配置，识别 ShadowTLS 插件，并创建相应的代理实例。

**优先级原因**: 这是 ShadowTLS 支持的基础功能，没有这个功能，用户无法使用 ShadowTLS 增强的 Shadowsocks 代理。这是必要的 MVP 功能。

**独立测试**: 用户可以通过加载包含 ShadowTLS 插件的 Shadowsocks 代理配置，验证系统是否正确识别 `plugin: shadow-tls` 字段，并创建 ShadowTLS 代理实例而非标准 Shadowsocks 实例。

**验收场景**:

1. **给定** 用户提供了有效的 Shadowsocks 配置（包含 server、port、cipher、password）和 ShadowTLS 插件配置（plugin: shadow-tls, plugin-opts 包含 host、password、version），**当** 系统加载此配置时，**那么** 系统应该成功创建一个 ShadowTLS 代理实例，而不是标准 Shadowsocks 实例。

2. **给定** 配置中包含完整的 ShadowTLS 插件参数，**当** 代理被初始化时，**那么** 所有 Shadowsocks 和 ShadowTLS 插件的配置参数都应该被正确存储和保留。

3. **给定** 用户提供了 Shadowsocks 配置但不包含 `plugin: shadow-tls` 字段，**当** 系统加载此配置时，**那么** 系统应该创建标准 Shadowsocks 代理实例。

4. **给定** 用户提供了无效的 ShadowTLS 插件配置（如 plugin-opts 缺少 host 字段），**当** 系统尝试加载此配置时，**那么** 系统应该返回清晰的错误消息，指出缺少的必要字段。

---

### 用户故事 2 - 通过 ShadowTLS 代理转发 TCP 连接 (优先级: P1)

用户希望通过已配置的 ShadowTLS 代理建立到远程服务器的 TCP 连接。当应用发起一个连接请求时，系统应该通过 ShadowTLS 代理进行转发，建立端到端的通信通道。

**优先级原因**: 这是 ShadowTLS 代理的核心功能，使用户能够实际通过代理进行网络通信。没有这个功能，配置的代理无法被使用。

**独立测试**: 可以通过配置一个 ShadowTLS 代理，向其发送 TCP 连接请求，验证连接是否成功建立以及数据是否能正确通过代理转发。

**验收场景**:

1. **给定** 一个已配置的 ShadowTLS 代理和目标远程服务器地址，**当** 应用发起 TCP 连接请求时，**那么** 连接应该通过 ShadowTLS 代理建立。

2. **给定** 已建立的 ShadowTLS 代理连接，**当** 用户向目标服务器发送数据时，**那么** 数据应该通过代理正确转发。

3. **给定** 目标服务器不可达的情况，**当** 通过 ShadowTLS 代理尝试连接时，**那么** 系统应该返回适当的错误信息。

---

### 用户故事 3 - 支持多种加密方式和协议组合 (优先级: P2)

用户希望 ShadowTLS 代理支持多种加密方式（如 chacha20-poly1305、aes-256-gcm 等）和不同的 TLS 伪装配置。这样用户可以根据安全需求选择合适的加密方式。

**优先级原因**: 这提高了协议的灵活性和安全选项，但不是必需的基础功能。系统可以先支持常见的加密方式，后续可扩展。

**独立测试**: 可以分别配置使用不同加密方式的 ShadowTLS 代理，验证每种配置是否都能正确初始化和工作。

**验收场景**:

1. **给定** 使用不同加密方式的 ShadowTLS 配置，**当** 系统加载此配置时，**那么** 代理应该能够正确识别和支持这种加密方式。

2. **给定** 配置了 TLS 伪装参数的 ShadowTLS 代理，**当** 建立连接时，**那么** 流量应该被正确伪装为 TLS 连接。

---

### 用户故事 4 - 错误处理和连接失败 (优先级: P2)

用户希望当 ShadowTLS 连接失败时，系统能够提供有意义的错误信息。这样用户可以快速定位问题并进行故障排除。

**优先级原因**: 这改进了用户体验和系统的可靠性，但不影响基础功能的可用性。

**独立测试**: 可以通过配置错误的 ShadowTLS 参数或模拟连接失败的场景来测试错误处理机制。

**验收场景**:

1. **给定** ShadowTLS 代理配置错误或服务器无法访问，**当** 用户尝试建立连接时，**那么** 系统应该返回清晰的错误消息。

### 边界情况

- 当 ShadowTLS 服务器突然离线时，已建立的连接应该如何处理？系统应该能够优雅地关闭连接并通知应用。

- 当用户更新代理配置时，如何处理已有的活跃连接？系统应该继续使用旧配置完成现有连接，新连接使用新配置。

- 当加密方式不被支持时，系统应该如何处理？应该在配置加载时立即报错，而不是在运行时失败。

- 当网络带宽很低或丢包率很高时，ShadowTLS 连接是否能稳定工作？系统应该支持合理的超时和重试参数。

- 当同时有多个 ShadowTLS 代理配置时，系统应该如何管理它们？每个代理应该是独立的实例，互不干扰。

## 需求 *(必填)*

<!--
  需要操作: 本节内容表示占位符.
  请用正确的功能需求填写.
-->

### 功能需求

- **FR-001**: 系统必须能够从配置文件加载包含 ShadowTLS 插件的 Shadowsocks 代理配置，识别 `plugin: shadow-tls` 字段并正确解析嵌套的 `plugin-opts`。

- **FR-002**: 系统必须在 `processor/config/types.go` 中的 `ShadowsocksConfig` 结构中添加 `Plugin` 和 `PluginOpts` 字段，用于存储 ShadowTLS 插件配置。

- **FR-003**: 系统必须实现协议选择逻辑：当 Shadowsocks 配置中存在 `plugin: shadow-tls` 时，创建并使用 ShadowTLS 代理实例；否则使用标准 Shadowsocks 代理。

- **FR-004**: 系统必须在 `outbound/proxy/shadowtls/` 目录下实现 ShadowTLS 代理，支持标准 Shadowsocks 的所有加密方式（chacha20-poly1305、aes-256-gcm 等）。

- **FR-005**: 系统必须正确处理 ShadowTLS 握手过程，包括 TLS 伪装、密码验证和数据加密，与底层 Shadowsocks 加密协议配合。

- **FR-006**: 系统必须验证 Shadowsocks + ShadowTLS 配置的有效性，对于缺少必要字段（如 plugin-opts 中的 host 或 password）应该返回有意义的错误消息。

- **FR-007**: 系统必须能够在建立 ShadowTLS 连接失败时提供详细的错误信息（如超时、TLS 握手失败、认证失败等）。

- **FR-008**: 系统必须在启动时（cmd 命令执行时）正确解析包含 ShadowTLS 插件的 Shadowsocks 配置，并初始化相应的代理实例。

- **FR-009**: 系统必须正确关闭 ShadowTLS 连接，释放相关资源，确保不会出现资源泄漏。

- **FR-010**: 系统必须能够同时管理多个不同的 Shadowsocks + ShadowTLS 代理配置实例，它们应该相互独立。

### 关键实体 *(如果功能涉及数据则包含)*

- **Shadowsocks 配置对象（带 ShadowTLS 插件）**: 扩展的 Shadowsocks 配置，包含标准字段（server、port、cipher、password）和可选的 `plugin` 和 `plugin-opts` 字段。当 `plugin: shadow-tls` 时，系统应该使用 ShadowTLS 代理实现；否则使用标准 Shadowsocks。

- **ShadowTLS PluginOpts 对象**: 嵌套在 Shadowsocks 配置中的 ShadowTLS 特定参数，包含：
  - `host`: TLS 伪装域名（如 www.bing.com）
  - `password`: ShadowTLS 认证密码
  - `version`: 协议版本（如 3）

- **ShadowTLS 代理实例**: 当 Shadowsocks 配置包含 `plugin: shadow-tls` 时创建的代理实例，负责通过 ShadowTLS 协议建立和维护连接。

- **ShadowTLS 连接**: 代表通过 ShadowTLS 插件建立的一个具体网络连接，负责加密、解密和数据转发。与底层 TCP 连接和远程目标都有关联。

## 成功标准 *(必填)*

<!--
  需要操作: 定义可衡量的成功标准.
  这些标准必须与技术无关且可衡量.
-->

### 可衡量的结果

- **SC-001**: 系统能够在 1 秒内完成 ShadowTLS 代理的初始化和加载。

- **SC-002**: 通过 ShadowTLS 代理的 TCP 连接建立时间应该在 3 秒以内（在网络正常的情况下）。

- **SC-003**: 系统应该支持至少 100 个并发的 ShadowTLS 代理连接而不显著降低性能。

- **SC-004**: 无效的 ShadowTLS 配置应该在配置加载阶段被检测出来，100% 的必需参数缺失都应该被捕获。

- **SC-005**: ShadowTLS 协议实现应该通过与实际 ShadowTLS 服务器的兼容性测试，能够正确建立和维护连接。

- **SC-006**: 连接失败时，错误信息应该能帮助用户定位问题，在调查中 95% 的情况下用户应该能通过错误信息快速找到根本原因。

- **SC-007**: 已建立的 ShadowTLS 连接应该能够正确处理从 1KB 到 1MB 的数据传输，数据完整性 100% 正确。

- **SC-008**: 系统应该能够在接收到连接关闭信号后的 100ms 内完全释放所有 ShadowTLS 相关资源。

---

## Clarifications

### Session 2025-12-19

- Q: ShadowTLS 的架构模式是什么？是独立协议还是 Shadowsocks 的插件？ → A: ShadowTLS 是作为 Shadowsocks 的插件（plugin）实现的，不是独立协议。在 Shadowsocks 配置中通过 `plugin: shadow-tls` 和 `plugin-opts` 字段启用。

- Q: 配置数据来源是什么？ → A: 配置信息从后端以 JSON 格式获取，ShadowTLS 的 plugin-opts 嵌套在 Shadowsocks 配置内部。

- Q: 协议选择逻辑是什么？ → A: 如果 Shadowsocks 配置中存在 `plugin: shadow-tls` 字段，则使用 ShadowTLS 协议；否则使用标准 Shadowsocks 协议。

- Q: ShadowTLS plugin-opts 包含哪些字段？ → A: 包含 `host`（伪装域名，如 www.bing.com）、`password`（插件密码）、`version`（协议版本，如 3）。

---

## 假设

- ShadowTLS 作为 Shadowsocks 的插件实现，而非独立协议。配置通过 Shadowsocks 的 `plugin` 和 `plugin-opts` 字段提供。

- 配置从后端以 JSON 格式获取，ShadowTLS 的配置嵌套在 Shadowsocks 配置对象中。

- 系统环境支持标准的 TLS 库和加密函数库（如 Go 内置的 crypto 包）。

- 用户希望 ShadowTLS/Shadowsocks 代理与其他代理（如 VLESS）共存，所以应该采用相同的代理接口模式。

- ShadowTLS 协议的实现应该参考 singbox 的 shadow-tls 插件实现，确保协议兼容性和安全性。

- 命令行启动时需要能够正确解析包含 ShadowTLS 插件的 Shadowsocks 配置。

---

## 验收标准

功能完成时，应该满足以下条件：

1. ✅ 在 `processor/config/types.go` 中的 `ShadowsocksConfig` 结构添加了 `Plugin` 和 `PluginOpts` 字段
2. ✅ 可以正确解析和加载包含 `plugin: shadow-tls` 的 Shadowsocks 配置
3. ✅ 协议选择逻辑正确：存在 ShadowTLS 插件时使用 ShadowTLS 代理，否则使用标准 Shadowsocks
4. ✅ 在 `outbound/proxy/shadowtls/` 目录实现了 ShadowTLS 代理模块
5. ✅ 可以通过 ShadowTLS 代理建立 TCP 连接
6. ✅ 支持 Shadowsocks 的所有常见加密方式
7. ✅ 在启动时（cmd 命令）正确解析 ShadowTLS 插件配置
8. ✅ 提供有意义的错误处理（缺少插件参数、握手失败等）
9. ✅ 通过与真实 ShadowTLS 服务器的兼容性测试
10. ✅ 代码遵循项目的代理实现模式（参考 VLESS 和现有 Shadowsocks）
11. ✅ 包含单元测试和集成测试
12. ✅ 文档清晰说明配置方法和使用示例（包括 plugin-opts 的 JSON 格式）
