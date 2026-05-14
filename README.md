
# Aliang-Core (Nursorgate) - Intelligent TUN/HTTP Proxy System

English | [中文](./README.zh.md)

> Aliang-Core (Nursorgate) is a cross-platform, high-performance proxy system supporting TUN-based transparent proxy and HTTP proxy modes, with intelligent routing, DNS caching, and a real-time management dashboard.

## 🚀 Project Overview

Aliang-Core (Nursorgate) is a next-generation proxy engine for Windows, macOS, and Linux. It intercepts and routes network traffic using TUN virtual network interfaces or HTTP proxy, with advanced rule engines, DNS/IP caching, and a modern web dashboard.

### ✨ Features

- **Dual Mode:**
  - TUN Mode: Transparent interception at the OS network layer (kernel/user-space TUN)
  - HTTP Proxy Mode: Standard HTTP/SOCKS5 proxy
- **Intelligent Routing:**
  - SNI/domain allowlist → MITM to Aliang
  - Otherwise: SOCKS5/VLESS/SS/Direct
  - GeoIP-based rules, cache-first optimization
- **DNS/IP Cache:**
  - Multi-source domain-IP binding (SNI, HTTP Host, CONNECT)
  - Bidirectional mapping (domain→IP, IP→domain)
  - Real-time cache stats, hit/miss, TTL, source tracking
- **HTTPS MITM:**
  - Optional transparent HTTPS interception with custom CA
- **Web Dashboard:**
  - Real-time stats, DNS cache, traffic, and rule management
  - Built with Vue 3, TailwindCSS, Vite
- **Cross-Platform:**
  - Windows (service/tray), macOS, Linux
- **Extensible Protocols:**
  - SOCKS5, VLESS, Shadowsocks, custom outbound
- **Service/Tray Integration:**
  - System service install/uninstall/start/stop
  - Tray mode for desktop control

## 🏗️ Architecture

```
┌─────────────┐   ┌──────────────┐   ┌──────────────┐
│  TUN/HTTP   │→→│ Metadata/Rules│→→│ Outbound     │
│  Inbound    │   │ Engine/Cache │   │ Proxy/Direct│
└─────────────┘   └──────────────┘   └──────────────┘
```

Key modules:
- `cmd/`         - CLI, service, tray, start, config commands
- `inbound/`     - TUN/HTTP traffic capture
- `processor/`   - Rules, cache, DNS, geoip, config, statistics
- `outbound/`    - Proxy protocol implementations
- `app/http/`    - REST API, dashboard server
- `app/website/` - Web dashboard (Vue 3, Vite)
- `common/`      - Logger, version, shared utils

## ⚡ Quick Start

### Build

```bash
# Standard build
go build -o aliang ./cmd/aliang

# Cross-compile (example: Windows)
GOOS=windows GOARCH=amd64 go build -o aliang.exe ./cmd/aliang
```

### Run

```bash
# Start in TUN mode (default)
./aliang start --config ./config.json

# Start HTTP proxy mode
./aliang start --config ./config.json --mode http

# Start as system tray (desktop)
./aliang tray --config ./config.json

# Install as system service (admin/root)
sudo ./aliang service install --system-wide --config /etc/aliang/config.json
```

### Dashboard

Open browser: [http://localhost:56431](http://localhost:56431)

## 🔑 Configuration

See `config.new.json` for a full example. Key sections:

- `core.engine`: TUN/HTTP mode, device, loglevel, etc.
- `customer.proxy`: Enable/disable outbound proxy, type, server
- `customer.ai_rules`: Domain allowlists for AI services
- `customer.proxy_rules`: Custom domain/IP routing rules
- `customer.traffic_mirror`: TCP-level traffic mirroring (see below)

### Traffic Mirror (TCP Stream Mirroring)

在配置文件中启用后，对匹配指定域名的流量在 TCP 层面进行旁路镜像转发。无论最终走的是 Aliang、直连还是其他路由，只要该连接命中配置的域名，就会尝试镜像；镜像失败不影响正常流量。

**配置格式：**

```json
{
    "customer": {
        "traffic_mirror": {
            "enabled": true,
            "target": "http://127.0.0.1:9090/mirror",
            "domains": ["api.openai.com", "api.anthropic.com", "*.cursor.sh"]
        }
    }
}
```

| 字段 | 类型 | 说明 |
|------|------|------|
| `enabled` | bool | 开关，`false` 时不产生任何开销 |
| `target` | string | 接收 StreamChunk 的 HTTP POST 端点 |
| `domains` | string[] | 匹配域名列表，支持精确匹配、`*.wildcard` 和后缀匹配 |

**域名匹配规则：**

| 模式 | 示例 | 匹配 |
|------|------|------|
| 精确匹配 | `api.openai.com` | `api.openai.com` |
| 通配符 | `*.cursor.sh` | `api2.cursor.sh`，不匹配 `cursor.sh` 本身 |
| 后缀匹配 | `openai.com` | `api.openai.com`、`cdn.openai.com` |

**StreamChunk 数据格式（HTTP POST JSON body）：**

```json
{
    "flow_id": "a1b2c3d4e5f6...",
    "conn_id": "tcp-42",
    "direction": "request",
    "offset": 0,
    "seq": 0,
    "payload": "<base64 bytes>",
    "timestamp": 1715673600123,
    "src_addr": "192.168.1.5:54321",
    "dst_addr": "104.18.6.192:443",
    "client_addr": "192.168.1.5:54321",
    "upstream_addr": "104.18.6.192:443",
    "protocol_hint": "http1"
}
```

| 字段 | 说明 |
|------|------|
| `flow_id` | 唯一标识一条完整的代理连接 |
| `conn_id` | TCP handler 内部连接 ID |
| `direction` | `request`（客户端→上游）或 `response`（上游→客户端） |
| `offset` | 当前方向内的字节偏移，从 0 递增 |
| `seq` | 当前方向内的 chunk 序号，从 0 递增 |
| `payload` | 本次读到的明文字节片段 |
| `timestamp` | 收集侧读到数据的时间（unix ms） |
| `src_addr` | 当前 chunk 原始 src 地址 |
| `dst_addr` | 当前 chunk 原始 dst 地址 |
| `client_addr` | 客户端地址 |
| `upstream_addr` | 上游地址 |
| `protocol_hint` | 应用层协议提示（`http1` / `http2` / `unknown`） |

**服务端如何重组字节流：**

1. 按 `flow_id` 分组，得到一条连接的所有 chunk
2. 按 `direction` 分为 `request` 和 `response` 两个独立的流
3. 按 `offset` 排序（chunk 可能乱序到达）
4. 按顺序拼接每个 chunk 的 `payload`，即还原出完整的明文请求/响应字节流
5. 如果检测到 offset 不连续，说明有 chunk 丢失（channel 满时丢弃）

### Flow Lifecycle Events（流量生命周期事件）

除数据片段外，镜像还发送生命周期事件，让服务端知道每个 flow 的开始与结束。完整的事件序列：

```
flow_start  →  StreamChunk × N  →  flow_end
```

服务端通过 `event_type` 字段区分：无/空 = 数据片段，`flow_start` / `flow_end` = 生命周期事件。

**flow_start 示例：**

```json
{
    "event_type": "flow_start",
    "flow_id": "a1b2c3d4e5f6...",
    "conn_id": "tcp-42",
    "timestamp": 1715612345678,
    "client_addr": "192.168.1.100:54321",
    "upstream_addr": "93.184.216.34:443",
    "protocol_hint": "http1",
    "host_name": "api.openai.com"
}
```

**flow_end 示例（正常关闭）：**

```json
{
    "event_type": "flow_end",
    "flow_id": "a1b2c3d4e5f6...",
    "conn_id": "tcp-42",
    "timestamp": 1715612347890,
    "client_addr": "192.168.1.100:54321",
    "upstream_addr": "93.184.216.34:443",
    "protocol_hint": "http1",
    "host_name": "api.openai.com",
    "client_to_server_bytes": 1024,
    "server_to_client_bytes": 8192,
    "duration_ms": 2212,
    "error_class": "clean"
}
```

**FlowEvent 字段说明：**

| 字段 | 说明 |
|------|------|
| `event_type` | `flow_start` 或 `flow_end` |
| `flow_id` | 唯一标识一条代理连接 |
| `conn_id` | TCP handler 内部连接 ID |
| `timestamp` | 事件时间（unix ms） |
| `client_addr` | 客户端地址 |
| `upstream_addr` | 上游地址 |
| `protocol_hint` | 应用层协议提示（`http1` / `http2` / `unknown`） |
| `host_name` | 目标域名 |
| `client_to_server_bytes` | 客户端→上游总字节数（仅 flow_end） |
| `server_to_client_bytes` | 上游→客户端总字节数（仅 flow_end） |
| `duration_ms` | flow 持续时间（仅 flow_end） |
| `error` | 错误描述（仅 flow_end 异常时） |
| `error_class` | 错误分类（`clean` / `timeout` / `reset` / `tls_error` / `context_cancel` / `unknown`） |

**服务端 Flow 状态机：**

```
[无状态] --flow_start--> ACTIVE
ACTIVE --data chunk--> ACTIVE（按 offset 追加 payload）
ACTIVE --flow_end--> COMPLETE（校验完整性，触发后处理）
```

```
request  流: offset=0 payload="GET /v1/chat..." + offset=1425 payload="..."  → 完整 HTTP 请求
response 流: offset=0 payload="HTTP/1.1 200..." + offset=876  payload="..."  → 完整 HTTP 响应
```

**架构位置：**

```
客户端 → proxy (MITM 解密)
  → mirrorConn.Wrap(clientConn, DirectionRequest)    ← wrapper
  → mirrorConn.Wrap(remoteConn, DirectionResponse)    ← wrapper
  → relay(wrappedClient, wrappedRemote)
      → wrappedClient.Read()  → 底层 Read + 捕获 StreamChunk → 异步 HTTP POST
      → wrappedRemote.Read()  → 底层 Read + 捕获 StreamChunk → 异步 HTTP POST
```

- wrapper 位于 `statistic.TCPTracker` 之外（更外层），统计数据不受影响
- 仅对 `RouteToALiang` 路由生效，直连流量不经过镜像
- 未配置时，开销仅一次 `nil` 判断，不创建任何 goroutine 或 channel

## 🧩 Key Commands

- `aliang start`      - Start core proxy engine
- `aliang tray`       - Start system tray app
- `aliang service`    - Manage as system service (install/uninstall/start/stop)
- `aliang config`     - Manage/load/validate config
- `aliang version`    - Print version info

## 📦 Dependencies

- Go 1.25+
- sing-box, gVisor, tun2socks, gorilla/websocket, miekg/dns, GORM, SQLite/MySQL, Vue 3, Vite, TailwindCSS

## 🛡️ Security

- HTTPS MITM requires trusting custom CA
- SNI/domain extraction at TCP handshake
- GeoIP database (GeoLite2) for region-based rules

## 🤝 Contributing

See [docs/](docs/) for API, config, and development notes.

---

**Last Updated:** April 2026
**Maintainers:** aliang.one

**Data Structure Enhancement:**
```go
type DNSInfo struct {
    BindingSource BindingSource  // Source: SNI, HTTP, DNS, CONNECT
    BindingTime   time.Time      // When binding was captured
    CacheTTL      time.Duration  // How long to keep this binding
    ShouldCache   bool           // Whether to persist this binding
}
```

---

### Dashboard Display Fixes ✅ (December 2024)

Fixed three critical dashboard display issues:

| Issue | Root Cause | Fix |
|-------|-----------|-----|
| **Hit Count = 0** | Get() method wasn't updating individual entry HitCount | Added `entry.HitCount++` in Get() method |
| **Hit Rate = 0** | Stats() calculated correctly but missing data from cache usage | Fixed data flow with StoreBinding() implementation |
| **Wrong Unique Counts** | Stats() returned maxEntries (capacity) instead of uniqueDomains; JS mapped hits instead of uniqueIPs | Added uniqueDomains and uniqueIPs calculation; Fixed JS mapping |

**Files Modified:**
- `processor/cache/ipdomain.go:Get()` - Update HitCount on cache hit
- `processor/cache/ipdomain.go:Stats()` - Calculate and return uniqueDomains and uniqueIPs
- `app/website/assets/app.js` - Correct field mapping for dashboard display

---

## 🏗️ Architecture Overview

```
┌─────────────────────────────────────────────────────────────────┐
│                       TUN Device Layer                           │
│         Intercepts all TCP/UDP packets at kernel level           │
└────────────────┬────────────────────────────────────────────────┘
                 │
┌────────────────▼────────────────────────────────────────────────┐
│                    Protocol Detection                            │
│   Determines: TLS (443) | HTTP (80) | Other (custom handling)   │
└────────────────┬────────────────────────────────────────────────┘
                 │
         ┌───────┴─────────┬──────────────┐
         │                 │              │
    ┌────▼─────┐      ┌────▼────┐   ┌───▼──┐
    │  TLS/443 │      │ HTTP/80 │   │ Other│
    └────┬─────┘      └────┬────┘   └───┬──┘
         │                 │            │
    ┌────▼──────────────────▼────────────▼─────┐
    │        Metadata Extraction & Caching     │
    │  • SNI domain extraction from TLS        │
    │  • HTTP Host header extraction           │
    │  • CONNECT request parsing               │
    │  • Automatic DNS binding storage         │
    └────┬────────────────────────────────────┘
         │
    ┌────▼──────────────────────────────────────┐
    │       Routing Decision Engine             │
    │  Priority: Bypass → Cache → Rules → GeoIP│
    │  Returns: Route decision + domain info    │
    └────┬──────────────────────────────────────┘
         │
    ┌────▼──────────────────────────────────────┐
    │          Route Execution                  │
    │  RouteToALiang (MITM) → Aliang Proxy   │
    │  RouteToDoor (Forward) → VLESS/SS Proxy  │
    │  RouteDirect → Direct TCP Connection     │
    └────┬──────────────────────────────────────┘
         │
    ┌────▼──────────────────────────────────────┐
    │      Data Relay & Statistics              │
    │  • Bidirectional data forwarding           │
    │  • Connection tracking & stats collection │
    │  • DNS binding persistence                │
    └──────────────────────────────────────────┘
```

### DNS Caching System

```
HTTP Metadata         TCP Handler           DNS Cache
  Extraction          (port 443)            Storage
     │                    │                    │
     ├─ CONNECT request   ├─ SNI extraction   │
     │  → DNSInfo (10m)   │  → DNSInfo (5m)  │
     │                    │                    │
     └─ HTTP Host header  └─ Create Route     │
        → DNSInfo (10m)      Decision         │
                                 │
                                 ▼
                          ┌─────────────┐
                          │StoreBinding()
                          │   (New!)    │
                          └─────┬───────┘
                                │
                    ┌───────────▼───────────┐
                    │  IPDomainCache        │
                    │  ├─ Forward: Domain→IP
                    │  ├─ Reverse: IP→Domain
                    │  ├─ HitCount tracking
                    │  └─ LRU eviction
                    └───────────┬───────────┘
                                │
                        ┌───────▼────────┐
                        │  Next Request  │
                        │ (Same IP?)     │
                        │  → Cache HIT!  │
                        │ Skip SNI extract
                        └────────────────┘
```

---

## 🚀 Quick Start

### Build

```bash
# Standard binary build
go build -o nursorgate ./cmd/nursor

# Optimized for size (with symbol stripping)
go build -ldflags="-s -w" -o nursorgate ./cmd/nursor

# Cross-compile for different platforms
./build.sh  # See build scripts below
```

### Cross-Platform Build Scripts

**macOS (arm64 - Apple Silicon):**
```bash
export CGO_ENABLED=1
export GOOS=darwin
export GOARCH=arm64
go build -ldflags="-s -w" -tags=with_utls -o nursorgate-darwin-arm64 ./cmd/nursor
```

**macOS (amd64 - Intel):**
```bash
export CGO_ENABLED=1
export GOOS=darwin
export GOARCH=amd64
go build -ldflags="-s -w" -o nursorgate-darwin-amd64 ./cmd/nursor
```

**Linux (amd64):**
```bash
export CGO_ENABLED=1
export GOOS=linux
export GOARCH=amd64
go build -ldflags="-s -w" -o nursorgate-linux-amd64 ./cmd/nursor
```

**Linux (arm64):**
```bash
export GOOS=linux
export GOARCH=arm64
go build -ldflags="-s -w" -o nursorgate-linux-arm64 ./cmd/nursor
```

**Windows (amd64):**
```bash
set CGO_ENABLED=1
set GOOS=windows
set GOARCH=amd64
go build -ldflags="-s -w" -o nursorgate-win-amd64.exe ./cmd/nursor
```

### Run

```bash
# Start the proxy
./nursorgate --config config.json

# View management dashboard
# Open browser to: http://localhost:56431
```

---

## 📚 Development Documentation

### DNS Caching System Design

The DNS caching system solves a fundamental architectural challenge: **TUN devices capture only TCP/UDP layer traffic, never seeing DNS queries at the application layer**.

This creates a "hostname metadata vacuum" where domain resolution context available at query time is completely lost by the time TCP connections are captured.

**Solution:** Multi-source domain binding through:

1. **SNI Extraction** (HTTPS): Auto-extract domain from TLS ClientHello
2. **HTTP Headers**: Capture domain from Host header
3. **CONNECT Requests**: Extract domain from CONNECT method
4. **System DNS Interception** (Optional): Capture full DNS queries at network layer

Each binding is automatically stored to cache with:
- Domain name and destination IP
- Binding source (SNI/HTTP/CONNECT/DNS)
- Route decision used
- Expiration time (TTL varies by source)

**Cache Usage:**
- First connection: Expensive SNI extraction or header parsing
- Subsequent connections: Cache hit → skip extraction → faster routing

### HTTP CONNECT Handling

Important implementation detail for HTTP tunneling:

```
Client → Proxy:  CONNECT example.com:443
         HTTP/1.1 200 Connection Established
         (metadata extraction happens here)

         ↓ (metadata + route decision)

Proxy → Remote: Transparent TCP connection
```

The proxy must:
1. Return `HTTP/1.1 200 Connection Established` before routing
2. Extract domain from CONNECT request for cache
3. Switch to transparent TCP relay mode

---

## 📝 Development Notes

### HTTP/2 Frame Handling

When processing HTTP/2 traffic:

1. **Header Frames**: Must extract `priority` field from payload before parsing headers
2. **Header Assembly**: After modifying headers, `priority` must be placed back in payload
3. **Important**: Envoy may force HTTP→H2 conversion, requiring proper priority handling
4. **Cursor Compatibility**: Improper priority handling breaks Cursor website loading

### Certificate Authority Setup

For HTTPS interception:
- Cannot use system CA certificates
- Must explicitly trust `mitm-ca.pem` certificate
- Certificate pinning in some applications may prevent interception

### GeoIP Routing

The system can route traffic based on IP geolocation:

```
IP Address → GeoIP Lookup → Country/City → Rule Evaluation → Route
```

This enables country-based routing rules without application involvement.

---

## 📊 Development Journal

### December 10, 2024

**DNS Cache Storage Implementation**
- ✅ Added Route field to Metadata struct
- ✅ Implemented StoreBinding() in RuleEngine
- ✅ Integrated storage into TCP handler
- ✅ Fixed dashboard display issues (HitCount, Hit Rate, unique counts)

**Achievement**: Complete end-to-end DNS caching system now operational. DNS bindings are automatically captured, stored, and reused for cache-first routing optimization.

### December 8-9, 2024

**Dashboard Display Bug Fixes**
- 🐛 Issue: Hit count always showing 0
  - Root Cause: Get() method not updating individual entry HitCount
  - Fix: Added `entry.HitCount++` in Get() method

- 🐛 Issue: Hit rate always 0%
  - Root Cause: Cache wasn't being queried, so hits=0, misses=0
  - Context: Not a bug but reflection of cache usage pattern

- 🐛 Issue: Wrong unique IP/domain display
  - Root Cause: Backend missing uniqueDomains and uniqueIPs calculation
  - Root Cause: Frontend incorrectly mapped stats.maxEntries and stats.hits
  - Fix: Added calculation to Stats(); corrected JS field mapping

**Achievement**: Dashboard now accurately reflects cache statistics and performance metrics.

### December 2-7, 2024

**Real-Time DNS Cache Dashboard**
- ✅ Created 7 REST API endpoints for DNS cache operations
- ✅ Integrated DNS cache panel into main web dashboard
- ✅ Implemented live statistics with 5-second refresh
- ✅ Added hot domains/IPs tables with color-coded source badges
- ✅ Implemented search, delete, and clear functions

**Achievement**: Complete visibility into DNS cache operations with real-time statistics and management capabilities.

### August 4, 2024

**HTTP/2 Frame Processing**
1. Header frame priority field must be extracted from payload before header parsing
2. After header modification, priority must be restored to payload
3. Envoy may convert HTTP to H2, requiring robust priority handling
4. Missing priority restoration breaks Cursor website loading

---

## 🛠️ Development Commands

### Build & Test

```bash
# Build individual packages
go build ./processor/tcp
go build ./processor/cache
go build ./processor/rules
go build ./app/http/handlers

# Run tests
go test -v ./processor/cache
go test -v ./processor/rules

# Clean build
go clean -cache
go build -o nursorgate ./cmd/nursor
```

### Debugging

```bash
# Check DNS cache API response
curl http://localhost:56431/api/dns/stats | jq

# View hotspots
curl http://localhost:56431/api/dns/hotspots | jq

# Query specific domain
curl "http://localhost:56431/api/dns/cache/query?domain=example.com" | jq

# Clear cache
curl -X DELETE http://localhost:56431/api/dns/cache
```

---

## 📦 Key Dependencies

- **gVisor** (github.com/sagernet/gvisor) - User-space network stack
- **sing-box** (github.com/sagernet/sing-box) - Protocol implementations
- **SNI Allowlist** - Local domain list for MITM routing to Aliang
- **GeoIP2** (oschwald/geoip2-golang) - IP geolocation
- **tun2socks** (xjasonlyu/tun2socks/v2) - TUN device integration
- **miekg/dns** - DNS protocol support

---

## 🔒 Security Considerations

- HTTPS MITM requires system CA trust
- SNI extraction operates at TCP layer (TLS plaintext handshake)
- GeoIP database updates recommended quarterly
- System DNS reconfiguration for full DNS interception

---

## 📄 License & Attribution

See LICENSE file for project licensing information.

---

## 🤝 Contributing

Development focuses on:
1. Cache performance optimization
2. Protocol compatibility improvements
3. Dashboard UX/UX enhancements
4. Cross-platform stability

---

**Last Updated**: December 10, 2024
**Latest Version**: Phase 4 - Complete DNS Caching System
**Module**: aliang.one/nursorgate
