# 数据模型: ShadowTLS 协议支持

**功能**: 004-shadowtls-support | **日期**: 2025-12-19

---

## 核心数据模型

### 1. ShadowTLS 插件配置对象

**实体**: `ShadowTLSPluginOpts`

**用途**: 存储 Shadowsocks 代理中的 ShadowTLS 插件特定参数

**字段定义**:

| 字段名 | 类型 | 必需 | 约束 | 说明 |
|--------|------|------|------|------|
| `host` | string | ✅ | 非空，有效域名 | TLS 伪装目标域名（如 www.bing.com） |
| `password` | string | ✅ | 非空，> 8 字符 | ShadowTLS 认证密码 |
| `version` | int | ✅ | 值为 1、2 或 3 | ShadowTLS 协议版本（当前推荐 3） |

**验证规则**:
- `host`: 必须是有效的 FQDN（完整域名），不能为空
- `password`: 不能为空或仅空格，长度 > 8 字符（安全要求）
- `version`: 仅接受 1、2、3，其他值返回错误

**Go 定义**:
```go
type ShadowTLSPluginOpts struct {
    Host     string `json:"host"`     // 伪装域名
    Password string `json:"password"` // 认证密码
    Version  int    `json:"version"`  // 协议版本
}

func (o *ShadowTLSPluginOpts) Validate() error {
    if o.Host == "" {
        return fmt.Errorf("plugin_opts.host is required")
    }
    if o.Password == "" || len(strings.TrimSpace(o.Password)) == 0 {
        return fmt.Errorf("plugin_opts.password is required and cannot be empty")
    }
    if len(o.Password) < 8 {
        return fmt.Errorf("plugin_opts.password must be at least 8 characters")
    }
    if o.Version != 1 && o.Version != 2 && o.Version != 3 {
        return fmt.Errorf("plugin_opts.version must be 1, 2, or 3")
    }
    return nil
}
```

---

### 2. 扩展的 Shadowsocks 配置

**实体**: `ShadowsocksConfig` (扩展)

**位置**: `processor/config/types.go`

**现有字段** (不变):
```go
type ShadowsocksConfig struct {
    Server     string `json:"server_host"`
    ServerPort uint16 `json:"server_port"`
    Method     string `json:"method"`        // 加密方式
    Password   string `json:"password"`
    Username   string `json:"username,omitempty"`
    ObfsMode   string `json:"obfs_mode,omitempty"`
    ObfsHost   string `json:"obfs_host,omitempty"`
    // ... 其他现有字段
}
```

**新增字段** (ShadowTLS 支持):
```go
type ShadowsocksConfig struct {
    // ... 现有字段 ...

    // 插件支持
    Plugin     string                 `json:"plugin,omitempty"`      // 插件名称，如 "shadow-tls"
    PluginOpts *ShadowTLSPluginOpts   `json:"plugin_opts,omitempty"` // 插件配置
}

func (c *ShadowsocksConfig) Validate() error {
    // 现有验证逻辑 ...

    // 新增验证：如果有 plugin，必须验证 plugin_opts
    if c.Plugin != "" {
        if c.Plugin == "shadow-tls" {
            if c.PluginOpts == nil {
                return fmt.Errorf("plugin_opts is required when plugin='shadow-tls'")
            }
            if err := c.PluginOpts.Validate(); err != nil {
                return err
            }
        } else {
            return fmt.Errorf("unsupported plugin: %s", c.Plugin)
        }
    }

    return nil
}
```

**关键约束**:
1. `Server` 和 `ServerPort`: 必须指向 ShadowTLS 服务器（代理层会处理）
2. `Method`: 任何 Shadowsocks 支持的加密方式都有效
3. `Password`: 用于 Shadowsocks 层加密，与 `Plugin.Password` 不同
4. `Plugin`: 当值为 "shadow-tls" 时，必须有对应的 `PluginOpts`

---

### 3. ShadowTLS 代理实例

**实体**: `ShadowTLS` 代理对象

**位置**: `outbound/proxy/shadowtls/shadowtls.go`

**职责**:
- 管理到 ShadowTLS 服务器的连接
- 处理 TLS 握手和 ShadowTLS 认证
- 转发 Shadowsocks 加密流量
- 实现 `proxy.Proxy` 接口

**Go 定义** (概要):
```go
type ShadowTLS struct {
    *proxy.Base

    // 基础信息
    server   string
    port     uint16

    // Shadowsocks 配置（用于加密层）
    ssConfig *ShadowsocksConfig

    // ShadowTLS 特定参数
    tlsHost  string  // 伪装域名
    tlsPass  string  // ShadowTLS 密码
    version  int     // 协议版本

    // 连接管理
    tlsConn  net.Conn // 底层 TLS 连接（如果需要复用）
    mu       sync.RWMutex
}

// 实现 proxy.Proxy 接口
func (s *ShadowTLS) DialContext(ctx context.Context, metadata *metadata.Metadata) (net.Conn, error) {
    // 执行 ShadowTLS 握手并返回应用层连接
}

func (s *ShadowTLS) DialUDP(metadata *metadata.Metadata) (net.PacketConn, error) {
    // 返回 UDP 连接（或 unsupported error）
}

func (s *ShadowTLS) Addr() string {
    return fmt.Sprintf("%s:%d", s.server, s.port)
}

func (s *ShadowTLS) Proto() proto.Proto {
    return proto.ShadowTLS
}
```

---

### 4. 配置 JSON Schema

**文件**: `contracts/shadowtls-config.json`

**格式**: JSON Schema 定义，用于验证 ShadowTLS 配置

```json
{
  "$schema": "http://json-schema.org/draft-07/schema#",
  "title": "Shadowsocks with ShadowTLS Configuration",
  "type": "object",
  "required": ["type", "server_host", "server_port", "method", "password"],
  "properties": {
    "type": {
      "type": "string",
      "enum": ["ss", "shadowsocks"],
      "description": "代理类型"
    },
    "name": {
      "type": "string",
      "description": "代理显示名称"
    },
    "server_host": {
      "type": "string",
      "format": "ipv4",
      "description": "ShadowTLS 服务器地址"
    },
    "server_port": {
      "type": "integer",
      "minimum": 1,
      "maximum": 65535,
      "description": "ShadowTLS 服务器端口"
    },
    "method": {
      "type": "string",
      "enum": ["aes-256-gcm", "chacha20-ietf-poly1305", "aes-128-gcm"],
      "description": "Shadowsocks 加密方式"
    },
    "password": {
      "type": "string",
      "minLength": 8,
      "description": "Shadowsocks 加密密码"
    },
    "username": {
      "type": "string",
      "description": "可选用户名"
    },
    "plugin": {
      "type": "string",
      "enum": ["shadow-tls"],
      "description": "Shadowsocks 插件类型"
    },
    "plugin_opts": {
      "type": "object",
      "required": ["host", "password", "version"],
      "properties": {
        "host": {
          "type": "string",
          "format": "hostname",
          "description": "TLS 伪装域名（如 www.bing.com）"
        },
        "password": {
          "type": "string",
          "minLength": 8,
          "description": "ShadowTLS 认证密码"
        },
        "version": {
          "type": "integer",
          "enum": [1, 2, 3],
          "description": "ShadowTLS 协议版本"
        }
      },
      "description": "ShadowTLS 插件参数"
    }
  },
  "additionalProperties": false
}
```

---

### 5. 配置示例

**完整 JSON 配置示例** (符合以上 Schema):

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

**标准 Shadowsocks 配置示例** (无插件):

```json
{
  "type": "ss",
  "name": "standard-ss",
  "server_host": "192.168.1.100",
  "server_port": 8388,
  "method": "aes-256-gcm",
  "password": "MySecurePassword123456"
}
```

---

### 6. 数据关系图

```
┌─────────────────────────────────────┐
│   Shadowsocks 配置对象               │
│  (processor/config/types.go)         │
├─────────────────────────────────────┤
│  - server_host: string              │
│  - server_port: int                 │
│  - method: string (加密)             │
│  - password: string                 │
│  - username?: string                │
│  - plugin?: string                  │─────────────────┐
│  - plugin_opts?: {...}              │                 │
└─────────────────────────────────────┘                 │
                                                        │
                                                        ▼
                                    ┌─────────────────────────────┐
                                    │   ShadowTLS 插件配置         │
                                    │  (outbound/proxy/shadowtls) │
                                    ├─────────────────────────────┤
                                    │  - host: string (伪装域名)   │
                                    │  - password: string         │
                                    │  - version: int             │
                                    └─────────────────────────────┘
```

---

### 7. 状态转换

**ShadowTLS 连接生命周期**:

```
┌─────────────────┐
│   未连接状态      │
│  (disconnected) │
└────────┬────────┘
         │
         ▼ DialContext() 调用
┌─────────────────────────────┐
│  TCP 连接建立中              │
│ (tcp_connecting)            │
└────────┬────────────────────┘
         │ TCP 连接成功
         ▼
┌─────────────────────────────┐
│  TLS 握手中                  │
│ (tls_handshaking)          │
└────────┬────────────────────┘
         │ TLS 握手成功
         ▼
┌─────────────────────────────┐
│  ShadowTLS 认证中            │
│ (shadowtls_authenticating) │
└────────┬────────────────────┘
         │ 认证成功
         ▼
┌─────────────────────────────┐
│  Shadowsocks 连接中          │
│ (ss_connecting)            │
└────────┬────────────────────┘
         │ Shadowsocks 连接建立
         ▼
┌─────────────────────────────┐
│  已连接，就绪转发             │
│ (connected)                │
└────────┬────────────────────┘
         │ 连接关闭
         ▼
┌─────────────────────────────┐
│  已断开连接                  │
│ (disconnected)             │
└─────────────────────────────┘
```

---

## 验证规则

### 配置验证

**必需字段验证** (验收标准 SC-004: 100% 覆盖):

1. **Shadowsocks 基础配置**:
   - `server_host`: 非空，有效 IP 或域名
   - `server_port`: 范围 1-65535
   - `method`: 属于支持的加密方式
   - `password`: 非空

2. **ShadowTLS 插件配置** (当 `plugin: "shadow-tls"`):
   - `host`: 非空，有效 FQDN
   - `password`: 非空，> 8 字符
   - `version`: 1、2 或 3

3. **不一致性检查**:
   - 如果 `plugin` 非空但值不是 "shadow-tls" → 错误
   - 如果 `plugin: "shadow-tls"` 但 `plugin_opts` 缺失 → 错误
   - 如果 `plugin_opts` 存在但 `plugin` 不是 "shadow-tls" → 警告

### 连接验证

**TLS 握手验证** (验收标准 SC-005):
- 证书链验证（必须与伪装域名匹配）
- TLS 版本检查（1.2+）
- 密码套件支持

**ShadowTLS 认证验证**:
- 密码 HMAC 验证
- 版本匹配检查

---

## 扩展性考虑

### 未来支持的插件

当前实现支持 `shadow-tls`。未来可扩展支持其他 Shadowsocks 插件：

- `v2ray-plugin`: V2Ray 混淆协议
- `simple-obfs`: 简单混淆协议
- 自定义插件

**扩展方式**: 在 `PluginOpts` 中使用 `map[string]interface{}` 或使用特定的插件结构体。

---

## 总结

**关键数据模型**:
1. `ShadowTLSPluginOpts`: ShadowTLS 特定参数
2. `ShadowsocksConfig` (扩展): 添加 `Plugin` 和 `PluginOpts` 字段
3. `ShadowTLS` 代理实例: 实现 `proxy.Proxy` 接口
4. JSON Schema: 配置验证标准

**关键约束**:
- 所有必需字段必须非空（SC-004）
- 配置验证在加载时执行
- TLS 握手必须成功（SC-005）
- 支持 Shadowsocks 所有加密方式

**设计目标**:
- ✅ 向后兼容
- ✅ 易于扩展
- ✅ 清晰的错误消息
- ✅ 完整的验证覆盖
