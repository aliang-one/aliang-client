# 研究文档: ShadowTLS 协议支持

**功能**: 004-shadowtls-support | **日期**: 2025-12-19

---

## 研究任务概览

本文档记录了 ShadowTLS 协议支持功能的技术研究和决策过程。

---

## 1. ShadowTLS 协议原理

### Decision: ShadowTLS 作为 Shadowsocks 插件实现

**选择的技术**: Shadow TLS v3（作为 Shadowsocks 的 plugin）

**原理**:
- ShadowTLS 是一种 TLS 伪装协议，将 Shadowsocks 流量伪装为标准的 TLS 1.3 连接
- 通过与目标伪装域名（如 www.bing.com）建立真实的 TLS 握手来规避检测
- 在 TLS 应用数据层中嵌入 Shadowsocks 加密流量
- 使用三层安全机制：
  1. TLS 握手伪装（外层）
  2. ShadowTLS 认证密码（中层）
  3. Shadowsocks 加密（内层）

**Rationale**:
- 比纯 Shadowsocks 更难被识别和阻断
- 利用真实 TLS 握手，流量特征与正常 HTTPS 流量几乎相同
- 支持 Shadowsocks 的所有加密方式，无需重新实现
- 插件架构灵活，易于维护和升级

**Alternatives considered**:
- 独立实现 ShadowTLS：复杂度高，维护成本大
- 使用其他混淆协议（如 v2ray-plugin）：功能不如 ShadowTLS 先进
- 直接修改 Shadowsocks 核心：耦合度高，不易扩展

---

## 2. 配置结构设计

### Decision: 扩展 ShadowsocksConfig 结构

**选择的方案**: 在 `processor/config/types.go` 中的 `ShadowsocksConfig` 添加可选字段

**配置格式** (JSON):
```json
{
  "type": "ss",
  "name": "myhk3",
  "server_host": "151.242.165.151",
  "server_port": 443,
  "method": "chacha20-ietf-poly1305",
  "password": "I8U3GD4pziEyIeQwTqd52CGLisU5boCwg6FBU9KpARs=",
  "username": "6TVbtuTh",
  "plugin": "shadow-tls",
  "plugin_opts": {
    "host": "www.bing.com",
    "password": "I8U3GD4pziEyIeQwTqd52CGLisU5boCwg6FBU9KpARs=",
    "version": 3
  }
}
```

**Go 结构定义**:
```go
type ShadowsocksConfig struct {
    Server     string              `json:"server_host"`
    ServerPort uint16              `json:"server_port"`
    Method     string              `json:"method"`
    Password   string              `json:"password"`
    Username   string              `json:"username,omitempty"`
    ObfsMode   string              `json:"obfs_mode,omitempty"`
    ObfsHost   string              `json:"obfs_host,omitempty"`

    // ShadowTLS 插件支持
    Plugin     string              `json:"plugin,omitempty"`
    PluginOpts *ShadowTLSPluginOpts `json:"plugin_opts,omitempty"`
}

type ShadowTLSPluginOpts struct {
    Host     string `json:"host"`      // TLS 伪装域名
    Password string `json:"password"`  // ShadowTLS 认证密码
    Version  int    `json:"version"`   // 协议版本（通常为 3）
}
```

**Rationale**:
- 保持向后兼容：不影响现有 Shadowsocks 配置
- 清晰的插件标识：`plugin: shadow-tls` 明确指示使用哪种插件
- 结构化参数：`plugin_opts` 包含所有 ShadowTLS 特定参数
- 易于扩展：未来可支持其他 Shadowsocks 插件（如 v2ray-plugin）

**Alternatives considered**:
- 创建独立的 ShadowTLSConfig：会导致配置重复，不符合插件模式
- 使用字符串配置：不够结构化，难以验证和维护
- 扁平化 plugin_opts：字段命名会冲突，结构不清晰

---

## 3. 协议选择逻辑

### Decision: 基于 plugin 字段的工厂模式

**实现方案**:

在代理初始化时（如 `cmd/start.go` 或代理工厂方法）检查 `ShadowsocksConfig.Plugin` 字段：

```go
func CreateShadowsocksProxy(config *ShadowsocksConfig) (proxy.Proxy, error) {
    if config.Plugin == "shadow-tls" {
        // 创建 ShadowTLS 代理
        if config.PluginOpts == nil {
            return nil, fmt.Errorf("shadow-tls plugin requires plugin_opts")
        }
        return shadowtls.New(config)
    }

    // 创建标准 Shadowsocks 代理
    return shadowsocks.New(config)
}
```

**Rationale**:
- 简单明了：单一判断点，易于理解和维护
- 向后兼容：不影响现有 Shadowsocks 代理
- 错误处理：在创建时验证必需参数

**Alternatives considered**:
- 在 Shadowsocks 内部判断：耦合度高，违反单一职责原则
- 使用独立的配置类型：会导致配置重复
- 自动检测：不够明确，可能导致意外行为

---

## 4. ShadowTLS 实现参考

### Decision: 参考 sing-box 的 shadow-tls 实现

**参考项目**:
- **sing-box**: https://github.com/SagerNet/sing-box
  - 包含成熟的 ShadowTLS 实现
  - 与 Go 生态系统兼容
  - 经过实际生产环境验证

**关键组件**:
1. **TLS 握手**:
   - 使用 Go 的 `crypto/tls` 包与伪装域名建立真实 TLS 连接
   - 验证 TLS 证书以确保连接的真实性

2. **ShadowTLS 认证**:
   - 在 TLS ApplicationData 阶段发送 ShadowTLS 认证标识
   - 使用 HMAC 或类似机制验证密码

3. **数据转发**:
   - 将 Shadowsocks 加密数据嵌入 TLS 应用层数据
   - 实现双向流转发（客户端 ↔ ShadowTLS 服务器）

**Rationale**:
- sing-box 是 Go 编写的成熟代理项目，代码质量高
- 已被广泛使用，兼容性和稳定性有保障
- 与项目现有的 sing-box 依赖一致（如 VLESS 实现也参考 sing-box）

**Alternatives considered**:
- shadowtls-rust: 使用 Rust 实现，不便于 Go 项目集成
- 自行实现：需要深入理解 TLS 协议，开发周期长，风险高

---

## 5. 代理接口适配

### Decision: 实现 proxy.Proxy 接口

**接口定义** (已存在于 `outbound/proxy/interfaces.go`):
```go
type Proxy interface {
    Dialer
    Addr() string
    Proto() proto.Proto
}

type Dialer interface {
    DialContext(context.Context, *metadata.Metadata) (net.Conn, error)
    DialUDP(*metadata.Metadata) (net.PacketConn, error)
}
```

**实现策略**:
```go
type ShadowTLS struct {
    *proxy.Base
    server   string
    port     uint16
    ssConfig *ShadowsocksConfig  // Shadowsocks 配置
    tlsHost  string              // TLS 伪装域名
    tlsPass  string              // ShadowTLS 密码
    version  int                 // 协议版本
}

func (s *ShadowTLS) DialContext(ctx context.Context, metadata *metadata.Metadata) (net.Conn, error) {
    // 1. 建立到 ShadowTLS 服务器的 TCP 连接
    // 2. 进行 TLS 握手（伪装为访问 tlsHost）
    // 3. ShadowTLS 认证（发送密码 HMAC）
    // 4. 建立 Shadowsocks 连接（使用 ssConfig）
    // 5. 返回包装后的连接
}

func (s *ShadowTLS) Addr() string {
    return fmt.Sprintf("%s:%d", s.server, s.port)
}

func (s *ShadowTLS) Proto() proto.Proto {
    return proto.ShadowTLS  // 需要在 proto 包中添加新常量
}
```

**Rationale**:
- 与现有代理（VLESS、Shadowsocks、NoneLane）保持一致
- 使用 `proxy.Base` 提供公共功能
- 实现 `DialContext` 处理 ShadowTLS 特定握手流程

**Alternatives considered**:
- 继承 Shadowsocks 代理：会导致逻辑混乱，ShadowTLS 有独特的握手流程
- 使用适配器模式：增加额外抽象层，不必要的复杂性

---

## 6. 测试策略

### Decision: 单元测试 + 集成测试

**单元测试** (`outbound/proxy/shadowtls/shadowtls_test.go`):
- 配置解析和验证测试
- TLS 握手逻辑测试（使用 mock TLS 服务器）
- ShadowTLS 认证测试
- 连接建立和数据转发测试

**集成测试**:
- 与真实 ShadowTLS 服务器的兼容性测试
- 多并发连接测试
- 错误处理和恢复测试

**测试覆盖目标**:
- 配置验证：100%（所有必需参数检查）
- 协议流程：> 80%（TLS 握手、认证、数据转发）
- 错误处理：> 90%（各种失败场景）

**Rationale**:
- 符合项目的测试优先原则
- 单元测试确保代码正确性
- 集成测试确保与实际服务器兼容

**Alternatives considered**:
- 仅单元测试：无法验证真实场景下的兼容性
- 仅集成测试：难以定位具体问题，测试速度慢

---

## 7. 启动时配置解析

### Decision: 在 cmd/start.go 中集成 ShadowTLS 解析

**实现位置**: `cmd/start.go`（代理初始化逻辑）

**伪代码**:
```go
// 在启动时，从配置加载 Door 代理成员
for _, member := range doorProxyMembers {
    if member.Type == "shadowsocks" || member.Type == "ss" {
        ssConfig, err := member.GetShadowsocksConfig()
        if err != nil {
            // 错误处理
        }

        // 检查是否有 ShadowTLS 插件
        var proxyInstance proxy.Proxy
        if ssConfig.Plugin == "shadow-tls" {
            proxyInstance, err = shadowtls.New(ssConfig)
        } else {
            proxyInstance, err = shadowsocks.New(ssConfig)
        }

        if err != nil {
            // 错误处理
        }

        // 注册代理实例
        registerProxy(member.ShowName, proxyInstance)
    }
}
```

**Rationale**:
- 在应用启动时统一处理所有代理配置
- 与现有的 VLESS、Shadowsocks 解析流程一致
- 错误可以在启动时被捕获，而非运行时

**Alternatives considered**:
- 延迟初始化（首次使用时）：会增加首次连接延迟，错误处理复杂
- 独立的配置加载器：增加代码复杂性，不必要

---

## 8. 性能考虑

### Decision: 连接复用和缓存

**优化策略**:
1. **TLS 会话复用**: 使用 Go 的 `tls.Config` 的会话缓存
2. **连接池**: 复用已建立的 TCP 连接（如果协议允许）
3. **缓冲优化**: 使用合理的读写缓冲区大小

**预期性能**:
- 初始化延迟: < 1 秒（符合 SC-001）
- 连接建立: < 3 秒（符合 SC-002）
- 并发连接: 100+（符合 SC-003）

**Rationale**:
- TLS 握手是性能瓶颈，会话复用可显著减少延迟
- 连接池减少频繁的 TCP 和 TLS 握手开销

**Alternatives considered**:
- 无优化：首次连接延迟可能超过 3 秒，不符合性能要求
- 预建立连接：资源浪费，可能不被使用

---

## 9. 错误处理

### Decision: 分层错误处理

**错误类型分类**:
1. **配置错误**: 在初始化时检测并返回（如缺少 host、password）
2. **网络错误**: TCP 连接失败、超时
3. **TLS 错误**: 证书验证失败、握手失败
4. **认证错误**: ShadowTLS 密码错误
5. **协议错误**: 数据格式不正确

**错误消息格式**:
```go
// 示例
return nil, fmt.Errorf("ShadowTLS: TLS handshake failed to %s: %w", host, err)
return nil, fmt.Errorf("ShadowTLS: authentication failed (check password)")
return nil, fmt.Errorf("ShadowTLS: missing required plugin_opts field: %s", field)
```

**Rationale**:
- 清晰的错误消息帮助用户快速定位问题（符合 SC-006）
- 分层处理避免混淆不同类型的错误

**Alternatives considered**:
- 通用错误：用户难以定位问题
- 错误代码：Go 惯例是使用描述性错误消息

---

## 总结

### 关键决策列表

| 决策点 | 选择 | 原因 |
|--------|------|------|
| 架构模式 | Shadowsocks 插件 | 向后兼容，易于扩展 |
| 配置结构 | 扩展 ShadowsocksConfig | 结构化，避免重复 |
| 协议选择 | 基于 plugin 字段的工厂模式 | 简单明了，易于维护 |
| 实现参考 | sing-box shadow-tls | 成熟稳定，生产验证 |
| 接口适配 | 实现 proxy.Proxy | 与现有代理一致 |
| 测试策略 | 单元 + 集成测试 | 确保正确性和兼容性 |
| 启动解析 | cmd/start.go 统一处理 | 错误早期捕获 |
| 性能优化 | TLS 会话复用 | 满足性能要求 |
| 错误处理 | 分层错误消息 | 易于调试 |

### 风险评估

**低风险**:
- 配置解析和验证（简单逻辑，易于测试）
- 接口适配（遵循现有模式）

**中风险**:
- TLS 握手实现（需要正确处理证书验证）
- ShadowTLS 认证（需要与服务器协议兼容）

**缓解措施**:
- 参考 sing-box 成熟实现
- 编写充分的集成测试
- 逐步实现，先保证基础功能，再优化

### 下一步: 阶段 1

进入阶段 1（设计与合同），生成：
1. `data-model.md`: ShadowTLS 配置数据模型
2. `contracts/shadowtls-config.json`: JSON Schema
3. `quickstart.md`: ShadowTLS 配置和使用指南
