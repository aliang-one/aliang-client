# AliangGate

**English** | [简体中文](./README.zh-CN.md)

> AliangGate (module `aliang.one/nursorgate`) is a cross-platform, high-performance
> network gateway written in Go. It combines a TUN/HTTP proxy engine with an
> attachable AI agent client, and ships with a real-time web dashboard.

## Overview

AliangGate intercepts and routes traffic at the OS network layer (TUN) or as a
standard HTTP/SOCKS5 proxy. Matching AI-service traffic is transparently
intercepted (MITM) and forwarded to the Aliang upstream; everything else goes
through SOCKS5/VLESS/Shadowsocks or direct. On top of the proxy, the agent
subsystem registers the device with a remote phone server and can execute
AI-driven "goals" via Claude Code / Codex / OpenCode providers.

### Features

- **Dual inbound mode**
  - TUN: transparent interception at the OS network layer (kernel/user-space TUN)
  - HTTP proxy: standard HTTP/SOCKS5 listening
- **Intelligent routing**
  - SNI/domain allowlist → MITM to Aliang upstream
  - Otherwise: SOCKS5 / VLESS / Shadowsocks / direct
  - GeoIP-based rules with cache-first optimization
- **DNS/IP binding cache**
  - Multi-source bindings (SNI, HTTP Host, CONNECT, DNS)
  - Bidirectional mapping (domain→IP, IP→domain), hit stats, TTL, LRU eviction
- **HTTPS MITM** — optional transparent interception with a custom CA
- **Link self-healing** — background health monitor with mTLS dial retries;
  transient relay blips are absorbed instead of flipping the link offline
- **Agent client**
  - JWT device authentication and auto-reconnect
  - Remote AI approval, slash-command capability discovery
  - Goal planning/execution loop (multi-turn planner, read-only exploration,
    acceptance checks) with streaming thinking
  - Session tunneling (piko-based)
- **Traffic mirroring** — TCP-level stream mirroring of matched domains to an
  HTTP endpoint (see [Traffic Mirror](#traffic-mirror))
- **Web dashboard** — real-time stats, DNS cache, traffic, rule management
  (Vue 3 + Vite + TailwindCSS)
- **Cross-platform** — Windows (service/tray), macOS, Linux; system service
  install/uninstall and desktop tray mode

## Architecture

```
┌─────────────┐   ┌──────────────┐   ┌──────────────┐
│  TUN/HTTP   │→→│ Metadata/Rules│→→│  Outbound    │
│  Inbound    │   │ Engine/Cache │   │ Proxy/Direct │
└─────────────┘   └──────────────┘   └──────────────┘
```

| Module | Purpose |
| --- | --- |
| `cmd/` | CLI: start, tray, service, config, version (entry: `cmd/aliang`) |
| `inbound/` | TUN / HTTP traffic capture |
| `processor/` | Rules, cache, DNS, GeoIP, config, statistics |
| `outbound/` | Proxy protocol implementations (incl. `proxy/aliang` link + health monitor) |
| `app/http/` | REST API, dashboard server, agent/goal services |
| `app/website/` | Web dashboard (Vue 3, Vite) |
| `app/tunnel/` | Session tunnel client |
| `common/` | Logger, version, shared utils |

## Quick Start

### Prerequisites

- Go 1.25+
- **CGO is required**: the build pulls in `go-sqlite3` (via `gorm.io/driver/sqlite`)
  and GTK/AppIndicator (via `beeep`/`systray`). A binary built with
  `CGO_ENABLED=0` **compiles but crashes at startup** when opening the SQLite
  store. Go auto-disables CGO when cross-compiling, so set `CGO_ENABLED=1`
  explicitly and use a native toolchain for the target platform.
- Linux additionally: `build-essential pkg-config libgtk-3-dev libayatana-appindicator3-dev`
  (TUN mode also requires root — capability flags alone are not enough)

```bash
# Debian/Ubuntu
sudo apt-get install -y build-essential pkg-config libgtk-3-dev libayatana-appindicator3-dev
```

### Build

```bash
# Standard build
go build -o aliang ./cmd/aliang

# Optimized for size
go build -trimpath -ldflags="-s -w" -o aliang ./cmd/aliang
```

> **Note on the local `goproxy` replace:** `go.mod` carries
> `replace github.com/elazarl/goproxy v1.7.2 => ../goproxy`. If `../goproxy`
> is not present on your machine, drop it before building:
>
> ```bash
> go mod edit -dropreplace=github.com/elazarl/goproxy
> ```

### Run

```bash
# Start in TUN mode (default)
./aliang start --config ./config.json

# Start HTTP proxy mode
./aliang start --config ./config.json --mode http

# Desktop tray (macOS / Windows)
./aliang tray --config ./config.json

# Install as system service (admin/root)
sudo ./aliang service install --system-wide --config /etc/aliang/config.json
```

### Dashboard

Open <http://localhost:56431> after start.

## Key Commands

| Command | Purpose |
| --- | --- |
| `aliang start` | Start the core proxy engine |
| `aliang tray` | Start in system-tray mode |
| `aliang service` | Manage as system service (install/uninstall/start/stop) |
| `aliang config` | Manage / load / validate configuration |
| `aliang version` | Print version info |

## Configuration

See [`config.new.json`](./config.new.json) for a full example. Key sections:

- `core.engine` — TUN/HTTP mode, device, log level, …
- `core.aliangServer` — Aliang upstream (mTLS relay) address
- `customer.proxy` — optional outbound proxy (enable, type, server, credentials)
- `customer.ai_rules` — domain allowlists for AI services (routed via MITM)
- `customer.proxy_rules` — custom domain/IP routing rules
- `customer.traffic_mirror` — TCP stream mirroring (below)

> `config.json` is machine-local and git-ignored; never commit real credentials.

### Traffic Mirror

When enabled, TCP traffic whose domain matches the configured list is mirrored
(bypassed, in parallel) to an HTTP endpoint at the stream level. Mirroring
applies regardless of the final route (Aliang / direct / other), and mirror
failures never affect the live flow.

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

| Field | Type | Description |
| --- | --- | --- |
| `enabled` | bool | Off switch — `false` costs nothing |
| `target` | string | HTTP POST endpoint receiving StreamChunks |
| `domains` | string[] | Exact, `*.wildcard`, and suffix matching |

**StreamChunk (HTTP POST JSON body):**

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

- `direction`: `request` (client→upstream) or `response` (upstream→client)
- `offset` / `seq`: byte offset and chunk index within one direction, from 0
- `payload`: plaintext bytes read in this chunk
- `protocol_hint`: `http1` / `http2` / `unknown`

**Flow lifecycle events** — besides data chunks, the mirror emits lifecycle
events so the server knows each flow's start and end. Distinguish by
`event_type`: empty/absent = data chunk, `flow_start` / `flow_end` = lifecycle.

```
flow_start  →  StreamChunk × N  →  flow_end
```

`flow_end` additionally carries `client_to_server_bytes`,
`server_to_client_bytes`, `duration_ms`, and `error_class`
(`clean` / `timeout` / `reset` / `tls_error` / `context_cancel` / `unknown`);
`flow_start` / `flow_end` both carry `host_name`.

**Reassembling the byte stream server-side:**

1. Group chunks by `flow_id` — one connection
2. Split by `direction` into independent request/response streams
3. Sort by `offset` (chunks may arrive out of order)
4. Concatenate `payload` in order → full plaintext stream
5. Gaps in `offset` mean chunks were dropped (channel full)

## Development

```bash
# Build
go build ./cmd/aliang

# Run tests for core packages
go test ./processor/cache ./processor/rules

# Test the outbound link + services
go test ./outbound/proxy/aliang/... ./app/http/services/

# Debug the running instance
curl http://localhost:56431/api/dns/stats | jq
curl http://localhost:56431/api/dns/hotspots | jq
curl "http://localhost:56431/api/dns/cache/query?domain=example.com" | jq
curl -X DELETE http://localhost:56431/api/dns/cache
```

More docs (API references, service management, certificate trust, …) live in
[`docs/`](./docs/).

### Implementation notes

- **HTTP/2 frames**: extract the `priority` field from the payload before
  parsing headers and restore it after modification — Envoy may force
  HTTP→H2 upgrade, and broken priority handling breaks some sites (e.g. Cursor).
- **MITM CA**: interception requires explicitly trusting `mitm-ca.pem`;
  certificate-pinned apps may resist interception.
- **GeoIP**: routing uses GeoLite2 (`data/GeoLite2-Country.mmdb`); refresh the
  database periodically.

## Security

- HTTPS MITM requires trusting the custom CA on the system
- SNI extraction happens at the TCP layer (plaintext TLS ClientHello)
- mTLS is used on the Aliang relay link; agent devices authenticate with JWT
  device tokens

## License

Proprietary — internal project by aliang.one. All rights reserved.

---

**Maintainer:** aliang.one
