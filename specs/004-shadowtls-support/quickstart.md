# 快速开始: ShadowTLS 协议支持

**功能**: 004-shadowtls-support | **日期**: 2025-12-19

---

## 概述

ShadowTLS 是 Shadowsocks 的一个插件，通过 TLS 伪装技术增强流量隐蔽性。本指南将帮助你快速配置和使用 ShadowTLS 代理。

**核心特性**:
- 将 Shadowsocks 流量伪装为标准 TLS 1.3 连接
- 三层安全机制：TLS 握手 + ShadowTLS 认证 + Shadowsocks 加密
- 兼容所有 Shadowsocks 加密方式
- 简单的插件式配置

---

## 快速配置

### 最小配置示例

```json
{
  "type": "ss",
  "name": "my-shadowtls-proxy",
  "server_host": "151.242.165.151",
  "server_port": 443,
  "method": "chacha20-ietf-poly1305",
  "password": "your-shadowsocks-password",
  "plugin": "shadow-tls",
  "plugin_opts": {
    "host": "www.bing.com",
    "password": "your-shadowtls-password",
    "version": 3
  }
}
```

### 配置字段说明

#### Shadowsocks 基础配置

| 字段 | 必需 | 说明 | 示例 |
|------|------|------|------|
| `type` | ✅ | 代理类型 | `"ss"` 或 `"shadowsocks"` |
| `name` | ❌ | 代理显示名称 | `"HK-Node-1"` |
| `server_host` | ✅ | 服务器地址 | `"151.242.165.151"` |
| `server_port` | ✅ | 服务器端口 | `443` |
| `method` | ✅ | 加密方式 | `"chacha20-ietf-poly1305"` |
| `password` | ✅ | Shadowsocks 密码 | `"your-ss-password"` |
| `username` | ❌ | 可选用户名 | `"user123"` |

#### ShadowTLS 插件配置

| 字段 | 必需 | 说明 | 示例 |
|------|------|------|------|
| `plugin` | ✅ | 插件类型 | `"shadow-tls"` |
| `plugin_opts.host` | ✅ | TLS 伪装域名 | `"www.bing.com"` |
| `plugin_opts.password` | ✅ | ShadowTLS 密码（≥8字符） | `"your-stls-password"` |
| `plugin_opts.version` | ✅ | 协议版本（1/2/3） | `3` |

---

## 支持的加密方式

ShadowTLS 支持 Shadowsocks 的所有主流加密方式：

**推荐**:
- `chacha20-ietf-poly1305` - 最佳性能与安全性平衡
- `aes-256-gcm` - AES 硬件加速支持

**完整列表**:
- `aes-128-gcm`
- `aes-192-gcm`
- `aes-256-gcm`
- `chacha20-poly1305`
- `chacha20-ietf-poly1305`
- `xchacha20-poly1305`
- `aes-128-ctr`
- `aes-192-ctr`
- `aes-256-ctr`

---

## 完整配置示例

### 示例 1: 生产环境配置

```json
{
  "type": "ss",
  "name": "HK-ShadowTLS-Production",
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

### 示例 2: 标准 Shadowsocks（无插件）

```json
{
  "type": "ss",
  "name": "Standard-SS",
  "server_host": "192.168.1.100",
  "server_port": 8388,
  "method": "aes-256-gcm",
  "password": "MySecurePassword123456"
}
```

**注意**: 如果配置中没有 `plugin: shadow-tls` 字段，系统会自动使用标准 Shadowsocks 协议。

---

## 如何使用

### 1. 配置代理

将 ShadowTLS 配置添加到你的代理配置文件中（具体位置取决于你的部署方式）。

### 2. 启动应用

```bash
./nursor [your-start-command]
```

应用会在启动时自动识别 `plugin: shadow-tls` 字段并初始化 ShadowTLS 代理。

### 3. 验证连接

你可以通过以下方式验证连接是否成功：

1. **查看日志**: 检查是否有 ShadowTLS 初始化成功的日志
2. **测试连接**: 尝试通过代理访问网络资源
3. **性能检查**: 连接建立应该在 3 秒以内完成

---

## 常见问题排查

### 1. 配置验证失败

**错误**: `plugin_opts.host is required`

**解决**: 确保 `plugin_opts` 包含所有必需字段（host、password、version）

```json
"plugin_opts": {
  "host": "www.bing.com",       // 必需
  "password": "12345678",       // 必需，≥8 字符
  "version": 3                  // 必需，1/2/3
}
```

### 2. TLS 握手失败

**错误**: `TLS handshake failed to www.bing.com`

**可能原因**:
- 伪装域名不可访问
- TLS 证书验证失败
- 网络连接问题

**解决**:
1. 检查 `plugin_opts.host` 是否是有效的可访问域名
2. 确保你的网络可以访问该域名
3. 尝试更换其他可靠的伪装域名（如 `www.google.com`、`www.cloudflare.com`）

### 3. ShadowTLS 认证失败

**错误**: `ShadowTLS: authentication failed (check password)`

**解决**: 确保 `plugin_opts.password` 与服务器端配置一致

### 4. 不支持的加密方式

**错误**: `unsupported cipher method: xxx`

**解决**: 使用支持的加密方式（参考"支持的加密方式"章节）

### 5. 连接超时

**可能原因**:
- 服务器地址或端口错误
- 防火墙阻止连接
- 服务器离线

**解决**:
1. 验证 `server_host` 和 `server_port` 配置正确
2. 检查服务器状态
3. 使用 `telnet` 或 `nc` 测试端口连通性

---

## 配置最佳实践

### 1. 密码安全

- Shadowsocks 密码和 ShadowTLS 密码应该不同
- 使用强密码（建议 16+ 字符，包含大小写字母、数字、特殊字符）
- 定期更换密码

### 2. 伪装域名选择

**推荐的伪装域名特征**:
- 高可用性（如 CDN 域名）
- HTTPS 支持
- 常见的公共服务域名

**推荐域名**:
- `www.bing.com`
- `www.cloudflare.com`
- `www.microsoft.com`
- `www.apple.com`

**避免使用**:
- 不稳定的小网站域名
- 已被屏蔽的域名
- 自签名证书的域名

### 3. 版本选择

- **推荐**: 使用 `version: 3`（最新版本，兼容性最好）
- 除非服务器明确要求，否则不使用旧版本（1 或 2）

### 4. 加密方式选择

- **CPU 有 AES 加速**: 使用 `aes-256-gcm`
- **CPU 无 AES 加速**: 使用 `chacha20-ietf-poly1305`
- **不确定**: 使用 `chacha20-ietf-poly1305`（通用性好）

---

## 性能指标

**预期性能**（在正常网络环境下）:
- 代理初始化时间: < 1 秒
- TCP 连接建立时间: < 3 秒
- 并发连接数: 100+ 个
- 数据传输延迟: 与标准 Shadowsocks 相当

---

## 技术细节

### 三层安全机制

1. **TLS 握手层（外层）**:
   - 与伪装域名建立真实的 TLS 1.3 连接
   - 流量特征与正常 HTTPS 几乎相同

2. **ShadowTLS 认证层（中层）**:
   - 使用 `plugin_opts.password` 进行 HMAC 认证
   - 验证客户端身份

3. **Shadowsocks 加密层（内层）**:
   - 使用 `method` 和 `password` 加密实际数据
   - 保护数据内容安全

### 连接建立流程

```
客户端 → TCP 连接 → ShadowTLS 服务器
       → TLS 握手（伪装为访问 host）
       → ShadowTLS 认证（plugin_opts.password）
       → Shadowsocks 加密通道建立
       → 数据转发
```

---

## 参考资源

- **规范文档**: [spec.md](./spec.md)
- **数据模型**: [data-model.md](./data-model.md)
- **实施计划**: [plan.md](./plan.md)
- **配置 Schema**: [contracts/shadowtls-config.json](./contracts/shadowtls-config.json)

---

## 下一步

配置完成后，你可以：

1. **测试连接**: 验证代理是否正常工作
2. **监控性能**: 观察连接延迟和稳定性
3. **调整配置**: 根据实际情况优化参数
4. **添加更多代理**: 配置多个 ShadowTLS 节点实现负载均衡

如有问题，请参考"常见问题排查"章节或查阅完整的规范文档。
