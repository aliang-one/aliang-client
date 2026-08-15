# AliangGate

[English](./README.md) | **简体中文**

> AliangGate（模块名 `aliang.one/nursorgate`）是一个跨平台、高性能的 Go 网关。
> 它将 TUN/HTTP 代理引擎与可挂载的 AI Agent 客户端合二为一，并自带实时 Web 管理面板。

## 项目概述

AliangGate 在操作系统网络层（TUN）拦截路由流量，也可以作为标准 HTTP/SOCKS5
代理运行。命中 AI 服务域名的流量会被透明拦截（MITM）并转发到 Aliang 上游；
其余流量走 SOCKS5/VLESS/Shadowsocks 或直连。在代理之上，Agent 子系统向远程
Phone Server 注册设备，并可通过 Claude Code / Codex / OpenCode 执行 AI 驱动的
「goal」任务。

### 功能特性

- **双入站模式**
  - TUN：操作系统网络层透明拦截（内核/用户态 TUN）
  - HTTP 代理：标准 HTTP/SOCKS5 监听
- **智能路由**
  - SNI/域名白名单 → MITM 转发到 Aliang 上游
  - 其余：SOCKS5 / VLESS / Shadowsocks / 直连
  - 基于 GeoIP 的规则，缓存优先优化
- **DNS/IP 绑定缓存**
  - 多来源绑定（SNI、HTTP Host、CONNECT、DNS）
  - 双向映射（域名→IP、IP→域名），命中统计、TTL、LRU 淘汰
- **HTTPS MITM** — 可选的透明拦截，自定义 CA
- **链路自愈** — 后台健康监视器 + mTLS 拨号重试；relay 瞬断被吸收，
  不会把链路状态翻成离线
- **Agent 客户端**
  - JWT 设备认证与自动重连
  - 远程 AI 审批、slash 命令能力发现
  - Goal 规划/执行循环（多轮规划器、只读探索、验收检查），
    规划思考过程实时透传
  - 会话隧道（基于 piko）
- **流量镜像** — 命中域名的 TCP 明文流旁路镜像到 HTTP 端点
  （见[流量镜像](#流量镜像)）
- **Web 管理面板** — 实时统计、DNS 缓存、流量与规则管理
  （Vue 3 + Vite + TailwindCSS）
- **跨平台** — Windows（服务/托盘）、macOS、Linux；系统服务安装卸载、
  桌面托盘模式

## 架构

```
┌─────────────┐   ┌──────────────┐   ┌──────────────┐
│  TUN/HTTP   │→→│ 元数据/规则    │→→│   出站        │
│   入站      │   │ 引擎/缓存     │   │  代理/直连    │
└─────────────┘   └──────────────┘   └──────────────┘
```

| 模块 | 职责 |
| --- | --- |
| `cmd/` | CLI：start、tray、service、config、version（入口：`cmd/aliang`） |
| `inbound/` | TUN / HTTP 流量捕获 |
| `processor/` | 规则、缓存、DNS、GeoIP、配置、统计 |
| `outbound/` | 代理协议实现（含 `proxy/aliang` 链路与健康监视器） |
| `app/http/` | REST API、面板服务、agent/goal 服务 |
| `app/website/` | Web 管理面板（Vue 3、Vite） |
| `app/tunnel/` | 会话隧道客户端 |
| `common/` | 日志、版本、公共工具 |

## 快速开始

### 前置要求

- Go 1.25+
- **必须开启 CGO**：构建会引入 `go-sqlite3`（经 `gorm.io/driver/sqlite`）
  和 GTK/AppIndicator（经 `beeep`/`systray`）。用 `CGO_ENABLED=0` 构建的二进制
  **能编译通过但启动即崩**（打开 SQLite 存储时）。交叉编译时 Go 会自动关闭
  CGO，必须显式设置 `CGO_ENABLED=1` 并使用目标平台的原生工具链。
- Linux 另需：`build-essential pkg-config libgtk-3-dev libayatana-appindicator3-dev`
  （TUN 模式还需要 root —— 仅设 capability 无效）

```bash
# Debian/Ubuntu
sudo apt-get install -y build-essential pkg-config libgtk-3-dev libayatana-appindicator3-dev
```

### 构建

```bash
# 标准构建
go build -o aliang ./cmd/aliang

# 体积优化
go build -trimpath -ldflags="-s -w" -o aliang ./cmd/aliang
```

> **本地 `goproxy` replace 说明：** `go.mod` 中带有
> `replace github.com/elazarl/goproxy v1.7.2 => ../goproxy`。如果你的机器上
> 不存在 `../goproxy`，构建前先移除：
>
> ```bash
> go mod edit -dropreplace=github.com/elazarl/goproxy
> ```

### 运行

```bash
# TUN 模式启动（默认）
./aliang start --config ./config.json

# HTTP 代理模式启动
./aliang start --config ./config.json --mode http

# 桌面托盘模式（macOS / Windows）
./aliang tray --config ./config.json

# 安装为系统服务（管理员/root）
sudo ./aliang service install --system-wide --config /etc/aliang/config.json
```

### 管理面板

启动后打开 <http://localhost:56431>。

## 主要命令

| 命令 | 用途 |
| --- | --- |
| `aliang start` | 启动核心代理引擎 |
| `aliang tray` | 系统托盘模式启动 |
| `aliang service` | 系统服务管理（install/uninstall/start/stop） |
| `aliang config` | 配置管理 / 加载 / 校验 |
| `aliang version` | 打印版本信息 |

## 配置

完整示例见 [`config.new.json`](./config.new.json)。主要配置段：

- `core.engine` — TUN/HTTP 模式、设备、日志级别等
- `core.aliangServer` — Aliang 上游（mTLS relay）地址
- `customer.proxy` — 可选出站代理（开关、类型、服务器、凭据）
- `customer.ai_rules` — AI 服务域名白名单（走 MITM 路由）
- `customer.proxy_rules` — 自定义域名/IP 路由规则
- `customer.traffic_mirror` — TCP 流量镜像（见下）

> `config.json` 是机器本地配置，已被 git 忽略；切勿提交真实凭据。

### 流量镜像

启用后，域名命中配置列表的 TCP 流量会在流层面旁路镜像到 HTTP 端点。
无论最终路由是 Aliang、直连还是其他，命中即镜像；镜像失败不影响正常流量。

```json
{
    "customer": {
        "traffic_mirror": {
            "enabled": true,
            "target": "http://172.16.159.219:443/mirror",
            "domains": ["api.openai.com", "api.anthropic.com", "*.cursor.sh"]
        }
    }
}
```

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `enabled` | bool | 开关，`false` 时不产生任何开销 |
| `target` | string | 接收 StreamChunk 的 HTTP POST 端点 |
| `domains` | string[] | 精确匹配、`*.wildcard`、后缀匹配 |

**StreamChunk（HTTP POST JSON body）：**

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

- `direction`：`request`（客户端→上游）或 `response`（上游→客户端）
- `offset` / `seq`：当前方向内的字节偏移与 chunk 序号，从 0 递增
- `payload`：本次读到的明文字节片段
- `protocol_hint`：`http1` / `http2` / `unknown`

**Flow 生命周期事件** — 除数据片段外，镜像还发送生命周期事件，让服务端
知道每个 flow 的开始与结束。通过 `event_type` 区分：无/空 = 数据片段，
`flow_start` / `flow_end` = 生命周期事件。

```
flow_start  →  StreamChunk × N  →  flow_end
```

`flow_end` 额外携带 `client_to_server_bytes`、`server_to_client_bytes`、
`duration_ms` 和 `error_class`（`clean` / `timeout` / `reset` / `tls_error` /
`context_cancel` / `unknown`）；`flow_start` / `flow_end` 均携带 `host_name`。

**服务端重组字节流：**

1. 按 `flow_id` 分组，得到一条连接的所有 chunk
2. 按 `direction` 拆分为 request / response 两个独立流
3. 按 `offset` 排序（chunk 可能乱序到达）
4. 按顺序拼接 `payload`，即还原完整明文字节流
5. offset 不连续说明有 chunk 丢失（channel 满时丢弃）

## 开发

```bash
# 构建
go build ./cmd/aliang

# 核心包测试
go test ./processor/cache ./processor/rules

# 出站链路 + 服务测试
go test ./outbound/proxy/aliang/... ./app/http/services/

# 调试运行中的实例
curl http://localhost:56431/api/dns/stats | jq
curl http://localhost:56431/api/dns/hotspots | jq
curl "http://localhost:56431/api/dns/cache/query?domain=example.com" | jq
curl -X DELETE http://localhost:56431/api/dns/cache
```

更多文档（API 参考、服务管理、证书信任等）见 [`docs/`](./docs/)。

### 实现备忘

- **HTTP/2 帧**：解析 header 前先从 payload 中取出 `priority` 字段，
  修改 header 后再放回 —— Envoy 可能强制 HTTP→H2 升级，priority 处理
  不当会破坏部分站点（如 Cursor）的加载。
- **MITM CA**：拦截需要系统显式信任 `mitm-ca.pem`；证书固定的应用可能
  无法拦截。
- **GeoIP**：路由使用 GeoLite2（`data/GeoLite2-Country.mmdb`）；需定期
  更新数据库。

## 安全

- HTTPS MITM 需要在系统上信任自定义 CA
- SNI 提取发生在 TCP 层（明文 TLS ClientHello）
- Aliang relay 链路使用 mTLS；agent 设备使用 JWT 设备令牌认证

## 许可

私有项目 —— aliang.one 内部使用，保留所有权利。

---

**维护者：** aliang.one
