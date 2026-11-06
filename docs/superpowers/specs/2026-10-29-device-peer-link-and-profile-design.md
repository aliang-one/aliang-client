# 设备互访（peer-link）与设备画像底座 设计

- 日期：2026-10-29
- 状态：草案（待评审）
- 仓：alianggate（客户端/agent）+ AliangPhoneServer（服务端，`~/MyProgram/AiProgram/vibe_on_phone/AliangPhoneServer`，main@89ad5ef 时点侦察）
- 前置调研：2026-10-25 设备文件互传 spec（P2/P3 占位本文兑现）、2026-10-27~29 三轮代码侦察与对抗复核（30 断言 0 refuted）

---

## 1. 目标与非目标

### 1.1 目标

1. **意图面/传输面分离**：LLM 与上层客户端只表达「在设备 B 执行命令 / 取文件 / 访问服务」，中间网络路径（云 WS 中继、对象存储中继、piko 中继、LAN 直连）由服务端协商决定，不进入 LLM 上下文。
2. **设备画像底座**：服务端集中维护每台设备的三层数据——冷层（慢变清单）、热层（快变遥测+网络身份）、灵活层（LLM 可读可写元数据），为后续扩展（跨设备协作、选路决策、设备推荐）提供地基。
3. **多路径协商**：文件传输按「LAN 直连优先、COS 兜底」协商选路；命令走云 WS 中继（KB 级载荷下即终态）；服务访问走 piko 端口映射（A 以访问者身份拨号）。
4. **不代理全局网络**：只打通设备间指定通道；手机永久只做控制面，不进数据面。

### 1.2 非目标

- P2P 打洞（STUN/UDP hole punching）——触发式后续项（见 §12），本期中继+LAN 直连已覆盖目标场景。
- IP 层 mesh / TUN / WireGuard overlay——明确不做（移动端 NetworkExtension 成本黑洞 + 与自家代理引擎双 TUN 冲突）。
- 目录级持续同步（rsync 语义）——transfer 保持一次性传输语义。
- 设备间 AI 会话转发（ai.session.* 跨设备）——后续评估，v1 命令面用一次性 exec。

### 1.3 现状基线（侦察已钉死的事实，设计依据）

| 积木 | 状态 | 关键证据 |
|---|---|---|
| B 端命令执行引擎 | ✅ 完整 | `terminal.*` 真 PTY + quota（agent_terminal.go）、agentcore shell 工具（tools_shell.go） |
| 文件互传数据面 | ✅ 完整 | transfer.* COS 分块直传 + Range 续传 + sha256（agent_transfer_*.go）；服务端全状态机编排（server/src/modules/agent/handler.ts:296-530） |
| 中继基座 | ✅ 完整 | 客户端仓 app/tunnel/ 基于 piko v0.10.0：`<deviceID>.tcp` 裸 TCP 桥（app/tunnel/bridge.go）+ Ed25519 路由票（app/tunnel/ticket.go）+ RFC1918 目标围栏（app/tunnel/target.go）；piko 依赖自带访问端 API（manager.go:310 client.Upstream） |
| 设备画像冷层 | ✅ ~80% | hello 全量携带 projects/vibe_sessions/history/authorized_directories 且服务端全落库（PhoneServer handler.ts:296-530） |
| 设备画像热层 | ◐ ~50% | load 快照 13 字段随 10s 心跳（agent_load.go:47-63）+ 服务端指纹去抖；无时序、无网络身份 |
| 设备画像灵活层 | ❌ 全缺 | 无设备级 metadata 存储（PhoneServer devices 表无列；agent 端无字段） |
| 设备间寻址 | ❌ 缺 | WS 帧族 112 个消息类型零设备寻址（agent_protocol.go:10-132）；REST 层 transfer 已有 target_device_id 先例 |
| LLM 路径泄漏 | ✅ 零泄漏 | 注册表 5 工具全只读全本地锚定；tunnel/token 全走 WS 帧不进上下文（五层核实） |
| 服务端来源 IP 观测 | ❌ 零 | `req.socket.remoteAddress` 从未读取（server/src/modules/agent/connections.ts:98-140 只读鉴权头） |

---

## 2. 总体架构

```
LLM / 上层客户端（意图面，永远只见意图）
    │  peer_exec(device, cmd) / peer_fetch_file(device, path)
    │  peer_access(device, port) / device_info / device_note
    ▼
意图工具门面（注册表工具 + agentcore 自定义工具，参数无路径细节）
    ▼
agent 本地服务（鉴权、脱敏、审计） ──callAgentServer（用户 JWT + X-Aliang-Device-ID）──▶ PhoneServer
                                                                    │
                                              ┌── ① ACL/在线/能力裁决 + 候选组装
                                              ▼
                    ┌──── peer.* 帧族（云 WS 中继，KB 级）────┐
                    ▼                                        ▼
                设备 B agent                              设备 A agent
                    │                                        │
        ┌───────────┼───────────┐          ┌────────────────┘
        ▼           ▼           ▼          ▼
   peer.exec    transfer.*   peer.access   协商后数据面（阶段 2）：
   (一次性命令)  (COS/直连)   (piko 票据)   LAN 直连 TLS / piko `<B>.tcp` / COS
```

三条既定分工不变：**命令 KB 级走云 WS；文件大流量走两端↔COS（或协商直连）；服务端口走 piko 中继**。本期新增的只是其上的「设备寻址 + 协商 + 意图门面 + 画像底座」。

---

## 3. 阶段 0：前置护栏（先于一切功能）

规模侦察确认三处裸奔，画像与 peer 功能让上报变大之前必须先立：

1. **设备数上限**：PhoneServer 注册函数加 per-user 设备数上限（建议 50，超限拒绝并明确错误码）。
2. **数组条数封顶**：`projects[]`（现无上限）、`history[]`（现无上限）在服务端 normalize 处截断（建议 500/64）并在响应标注 truncated；agent 侧采集同步封顶。
3. **WS 消息限流（通用洪泛护栏）**：阶段 0 先落按设备的基础消息限频（服务端兜底，防任意帧族洪泛）；peer.* 专属限频随阶段 1 落地，超限走 `peer.exec.rejected{reason:rate_limited}`——不设独立 rate_limited 帧，避免同一条件两套机制。
4. **幽灵项目采集标记**：`collectAgentProjects`（agent_inventory.go:357-385）加一次性 `os.Stat(projectPath)` 存在性检查，**必须经 tccguard.StatAllowed 包裹**——不能用 GuardPath，后者对受保护+在场+无授权路径会走 probe 触发 macOS TCC 弹窗；StatAllowed 只放行非保护/已有授权路径，其余静默跳过保持未知，延续「后台采集零探盘」纪律（agent_inventory.go:450-456 注释）。实现为**标记不剔除**（`AgentProject.dir_missing`，omitempty）：手机侧把幽灵卡片当缓存展示，剔除与否由服务端/UI 决定（`project_path_missing` 语义码 4e705f8 仍管按需访问路径）。
5. **独立小修**：`/api/agent/auth-recover` 补注册进 56433 `registerAgentRoutes`（现只在 routes.go:264，HandleAuthRecover 的 proxyIfNeeded 与 core 恢复推送 agent_service.go:2386-2392 均 404——双路由表现行活 bug）。

---

## 4. 设备画像底座

### 4.1 数据模型（三层）

| 层 | 内容 | 变化频率 | 传输通道 | 服务端存储 |
|---|---|---|---|---|
| 冷层 | OS/架构/内核/CPU 型号/内存总量、工具链版本、项目清单、授权目录、能力位 | 天级/变更驱动 | agent.hello（现有） | devices 表 JSON 列 + projects 表（现有，扩展字段） |
| 热层 | load 快照、在线状态、**网络身份**（接口 IP/前缀 + 出口 IP）、最近活跃项目 | 10s~60s | heartbeat（现有 10s 通道，扩展子对象） | devices 表列（单值快照，现有）+ 出口 IP 新列 |
| 灵活层 | 设备备注/标签/角色（"构建机""有 GPU"），LLM 与用户可写 | 事件驱动 | REST（新增） | **独立 `device_metadata` 表**（见 4.4 陷阱） |

### 4.2 冷层扩展

- hello 新增 `system` 子对象：`{os_name, os_version, kernel_version, arch, cpu_model, mem_total_bytes}`，采集用 gopsutil `host.Info()` + `mem.VirtualMemory()`（依赖已在，load 采集同族），全部 best-effort 逐项 omitempty（沿用 agent_load.go:103-147 的降级语义）。
- **profile_hash（304 式去重）**：对冷层规范化序列化算 sha256，hello 携带 `profile_hash`；服务端比对命中则跳过 projects/history/tools 的重复落库（load 已有同款指纹先例：server/src/modules/device/load.ts:54-73，实测省 2 万 PG 事务/天）。v1 可仅做服务端跳过、hello 仍全量发（不破坏现有 upsert 链），帧级瘦身留作优化。
- 工具链注释（agent_service.go:1504-1560「供能力清单先行的设备画像」）表明该扩展是既定方向的兑现，无架构冲突。

### 4.3 热层扩展（与协商选路共用采集）

- heartbeat 新增 `net` 子对象：`{interfaces: [{name, ipv4, prefix_len}]}`（仅非回环 IPv4；复用 tun_conflict_scan.go:194-217 的 `net.Interfaces` 样板 + 补 `iface.Addrs()` + 2s TTL 缓存模式）。服务端照 applyAgentLoad 的指纹去抖模式，网络身份未变不落库。
- **出口 IP 服务端自观测**：`registerAgentSocket`（connections.ts:98-140）读取 `req.socket.remoteAddress`；反代部署下读 `X-Forwarded-For` 并配套 trusted-proxy 配置（**必须**显式配置信任跳数，防伪造）。存 devices 表新列 `last_egress_ip`，连接级更新。
- 「最近活跃项目」：**零新增**——`AgentProject.last_active_at` 已是 reader 推导的最后真实消息时间（刻意弃用 mtime 防污染，agent_inventory.go:1658-1661），排序即得。git 最近提交时间留触发式（探针里加 `git log -1 --format=%cI`）。

### 4.4 灵活层（全新）

**存储**（PhoneServer）：独立表，**禁止**放 devices 表内列——`DeviceRepository.upsert` 是整对象覆盖（server/src/db/devices.ts:71-91，SQLite/PG 两份实现同语义），列内存储会被每次 hello 的全量 upsert 抹掉。

```sql
CREATE TABLE device_metadata (
  id TEXT PRIMARY KEY,
  device_id TEXT NOT NULL,
  key TEXT NOT NULL,          -- ≤64 字符
  value TEXT NOT NULL,        -- ≤4KB
  updated_by TEXT NOT NULL,   -- 'user' | 'llm' | 'agent'
  updated_at TIMESTAMP NOT NULL,
  UNIQUE(device_id, key)      -- ≤32 键/设备，应用层封顶
);
```

**API**：挂现有 deviceRouter（鉴权直接复用 `requireUserId` + `getAccessibleDeviceOrThrow`）：
- `GET /api/devices/:deviceId/metadata` → 全量 KV
- `PATCH /api/devices/:deviceId/metadata` → 按 key upsert（body 带 `entries: [{key, value}]`），设备 JWT 调用时 `updated_by='llm'`（由 `x-aliang-device-id` + 调用上下文判定），用户仪表盘调用时 `'user'`
- `DELETE /api/devices/:deviceId/metadata/:key`

**变更推送**：写操作后经 RealtimeBroker 向该用户在线设备广播 `device.metadata.updated {device_id, key, updated_by}`（载荷不含 value，避免无关设备收到写入内容；需要值的设备按 GET 拉）。

**属主变更清理（不变量）**：设备 rebind 到新账号或 unbind+purgeData 时，必须级联删除该 device_id 的全部 metadata 行（前任属主与其 LLM 写入的值对新属主不可见——沿用本仓「deviceId-only 数据跨属主清除」不变量），且 `peer_exec_enabled` 强制回落 false（危险开关未经新属主同意不得存活）。

**客户端镜像缓存**（agent，可选 v2）：照 approval_policy.json 的 hash+cache 模式本地镜像，服务端 hash 变了才拉。

---

## 5. 协议帧族（peer.*）

### 5.1 寻址约定

- 全部 peer.* 帧带 `source_device_id` + `target_device_id`；服务端为唯一路由与裁决点（复用 publishToAgent 定向推送 + requestAgentPayload 的 request_id RPC 骨架，server/src/modules/agent/request.ts:78-139）。
- 新帧全部登记进 agent_protocol.go 契约表（required/optional 逐帧），未注册事件的 `unsupported` 错误帧兜底（agent_remote_ws.go:906-917）即老版本兼容语义。
- **新帧处理路径必须 `SetReadLimit`**（对抗复核确认：现有 WS 双方均无帧上限，gorilla 默认不限；512KiB 是 scrollback/文件读取上限而非帧限）。建议 peer 帧读限 1MiB。
- 能力位：agent.hello capabilities 新增 `peer_v1`（阶段 1）/`peer_path_v1`（阶段 2 协商）/`peer_access_v1`（阶段 2 piko 访问者）。服务端按位门控下发，老设备自动落「仅中继」兼容路径。

### 5.2 命令执行（阶段 1）

选择**一次性 exec**而非 PTY 会话：LLM 消费者要的是「给命令、收回显」，无会话状态、天然幂等、免 PTY 生命周期跨中继管理。交互式终端共享（peer.terminal.*，PTY 透传）列为后续评估。

```
peer.exec.request   (server→B)  {request_id, source_device_id, command, timeout_ms≤600_000, workdir?}
peer.exec.result    (B→server→A){request_id, exit_code, stdout(≤256KiB,截断标记), stderr(≤256KiB), duration_ms, fail_reason?}
peer.exec.rejected  (server→A)  {request_id, reason: device_offline|not_allowed|rate_limited|busy|timeout|target_missing_capability}
```

- REST：`POST /api/peer/exec`（设备身份调用）→ 200 {exit_code,stdout,stderr,truncated,duration_ms}；校验拒绝 403 not_allowed / 409 busy·rate_limited·fail_reason / 404 掩蔽 / 400 validation。fence=min(timeout_ms+10s, 620s)；**注意反代 60s proxy_read_timeout：timeout_ms>~55s 的调用会被反代 504 掐断（命令照常执行），部署侧需放宽或客户端侧避免超长 timeout**。result 帧 fail_reason 取代初版 audit_id（审计落 audit_logs 不进帧）。
- rejected 完整枚举：timeout 与 target_missing_capability 为实现期补充（fence 到点与目标能力位缺失）。
- B 端执行：`exec.CommandContext` + **进程组（Setpgid）+ 组杀 + WaitDelay**（防 `nohup x &` 类后台子进程占住 stdout 管道把串行槽永久楔死；Windows 走 newBackgroundCommandContext 的 HideWindow 封装）+ 可信 shell 路径解析（复用 isTrustedAgentShellPath 白名单语义）；workdir 可选、默认 $HOME、须存在；输出双封顶 256KiB×2 + 截断标记 + 总量二级熔断；**env 最小集 PATH/HOME/TERM/LANG/TMPDIR，严禁继承 agent 进程全量 env（内含 LLM 凭据）**；**每次执行写审计**（journal + 服务端 audit_logs，早退路径同样落痕）。
- 门禁：设备级新开关 `peer_exec_enabled`（**默认关**，用户显式开启；与现有 remote_terminal_enabled/ai_control_enabled 同族），加服务端 peer ACL（§8）。该族开关必须写后 scheduleStateSave（防重启复活），rebind 换属主时强制回落 false。
- 串行：同设备同时只跑 1 个 peer.exec（复用 busy 语义，超限回 rejected(busy)）；目标设备失联（真下线/socket 被替换）时在途 pending 立即 fail(device_offline)，不挂满 fence。

### 5.3 文件获取（阶段 1，走既有 transfer 编排）

- A（LLM 所在设备）发起「从 B 取文件」：新增服务端编排入口——REST `POST /api/transfers` 扩展调用方语义：调用设备 = 目标（target），body 带 `source_device_id` + `source_path`；服务端校验同账号 + B 能力位（file_transfer_v1）+ 在线（离线走既有 pending/inbox 补投，PhoneServer 已有退避重推+hello 重推）。响应 201 {transfer:{transfer_id,...}}（嵌套壳，客户端按此解析）。
- **设备身份调用的边界**：带 x-aliang-device-id 的请求只能使用 fetch 语义（body 禁止 target_device_id，出现即 400）——设备不得借普通分支为本账号任意两台设备建单；fetch 的 source 查找用严格同 user_id（无 admin 旁路，对齐 peer/exec 口径）。
- 数据面帧不变（transfer.upload→COS→transfer.receive），B 上传、A 下载——「协商与凭据走信令、字节走旁路」完全沿用现架构。
- 设备主动发起的既有入口（POST /api/agent/transfers，源设备视角）保留不动，两者在服务端汇合同一编排。
- 阶段 2 的直连候选按 spec §11 预留挂点落在 `transfer.receive` 的 `direct_candidates` 字段（§6.3）。

### 5.4 服务访问（阶段 2）

```
peer.access.open    (A→server)  {request_id, target_device_id, target_host, target_port, ttl_seconds≤600}
peer.access.granted (server→A)  {request_id, local_endpoint: "127.0.0.1:<ephemeral>", expires_at, ticket}
peer.access.closed  (A→server)  {request_id, reason}
```

- 服务端裁决后签发 Ed25519 路由票（claims 绑 target_host/target_port/device，30s 时钟宽容，复用 ticket.go 全套）；A 的 agent 作为 **piko 访问者**拨 `<B>.tcp` 端点（首行 JSON {ticket, target_host, target_port}，bridge.go 协议），在 A 本地起 `127.0.0.1:<ephemeral>` 反向转发，TTL 到期自动关。
- LLM 从工具拿到的是 `local_endpoint`——`curl http://127.0.0.1:54321` 即等价访问 B:8080，路径细节零暴露。
- 目标围栏沿用 validateTarget：loopback 恒可、RFC1918 需 B 侧 `allow_private_targets`（per-mapping 显式开关）；注意复核修正——TCP 桥路径走系统 DNS 解析（localhost 钉死仅 HTTP 反代路径），目标仍限 IPv4 字面量与 `::1`。

---

## 6. LAN 检测与协商选路（阶段 2）

### 6.1 同内网判定（三层递进）

1. **启发式**（服务端，零成本）：两设备 `last_egress_ip` 相同 **且** 双方上报接口子网重叠 → 候选「疑似同 LAN」。重叠比对由服务端用 TypeScript 实现最小 IPv4 CIDR 重叠判定（两端地址集都汇聚在服务端，仅 v4 参与）；Go 侧 netip 与 app/tunnel/target.go:14-18 的 RFC1918 常量只用于 agent 自身目标围栏，不跨语言复用。
2. **实证**（权威裁决）：A 持服务端签发的短时 token（≤30s，单用途）直连 B 按需拉起的临时 TLS listener，双向校验账号派生凭据——握手成功才发 `lan_direct` 候选。**不改动 56433**（loopbackOnly+零 token 鉴权是刻意红线，agentruntime/server.go:144），listener 是独立 ephemeral 端口。
3. **mDNS 兜底发现**（可选，P2 后期）：`_aliang-transfer._tcp` 实例携带 device_id+账号指纹（spec §11 原文），用于服务端地址过期时的本地补发现。

### 6.2 协商帧族

```
peer.negotiate.request (A→server) {request_id, target_device_id, intent: exec|fetch_file|access, params}
peer.path.offer        (server→A/B) {request_id, candidates: [...], expires_at}
peer.path.selected     (A→server)   {request_id, candidate_type}
peer.path.failed       (A/B→server) {request_id, candidate_type, stage: probe|connect|mid_transfer}
```

候选结构（`direct_candidates` 字段族，兑现 spec §11 预留）：

```json
{ "type": "lan_direct", "addr": "192.168.1.23:0", "token": "<短时单向token>", "expires_at": "...", "fingerprint": "<BTLS指纹>" }
{ "type": "relay_piko", "mapping_hint": "..." }
{ "type": "relay_cos" }   // 仅文件意图，恒可用兜底
```

### 6.3 降级语义（v1 明确从简）

- 文件意图：`lan_direct` 探测失败（probe/connect 阶段）→ 直接落 COS（现链路零改动）；**传输中途失败 v1 不做无缝换路**——整批转 COS 重传（COS 链路自身有断点续传）。中途续接对齐（直连分块↔COS 分块）列为开放问题（§12）。
- 命令意图：仅 ws_relay 一条腿（终态，无需协商参与数据面）。
- 服务意图：仅 relay_piko 一条腿（阶段 2 内无第二腿）。
- 全部协商结果服务端留痕（选了哪条、失败在哪阶段）进审计。

---

## 7. LLM 工具面（意图门面）

### 7.1 工具清单

| 工具 | 类型 | 参数 | 返回 |
|---|---|---|---|
| `peer_exec` | 写（设备级） | device_id, command, timeout_ms?, workdir? | exit_code/stdout/stderr（脱敏后） |
| `peer_fetch_file` | 写（设备级） | device_id, remote_path, local_dir? | transfer_id/进度引用（异步完成） |
| `peer_access` | 写（设备级） | device_id, port, protocol?, ttl_seconds? | local_endpoint + expires_at |
| `device_info` | 读 | device_id?（缺省=本机） | 冷层+热层快照（含 net 身份）——「除非 LLM 自己要看」的内省位 |
| `device_note` | 写 | key, value（本机或 device_id?） | upsert 结果 |

**分批交付**：阶段 1 交付 `peer_exec`/`peer_fetch_file`/`device_note`/`device_info`（本机冷层+load 范围）；`peer_access` 与 `device_info` 的链路内省字段随阶段 2 能力位点亮。未具备后端能力的工具按能力位门控返回明确 unsupported 语义，不做空实现。

### 7.2 接入点（两条，handler 逻辑共享）

- **注册表工具**（服务端编排的 AI 会话，手机/仪表盘驱动）：新增 5 个描述符进 agent_tool_registry.go（rev 3→4，服务端靠心跳 rev 漂移自动重拉、**零服务端点亮代码**——注册表自述先例）。事件名避开 agent_remote_ws.go 显式 switch 臂（file.list 等四旧臂）。
- **agentcore 自定义工具**（本机 AI 会话）：`registry.Register` 宿主注入（子 agent 桥同款），handler 内调同一套本地服务逻辑；审批挂 `agentCoreApprove` 自定义工具 require 档。
- 设备级工具不调 `resolveEnvToolCwd`（无项目 cwd 语义），但 device_id≠本机时必须走 §5 帧；写工具护栏落在**审批层**（ShellPolicy 是「空白名单=全放行」，与审批策略方向相反，不可依赖）。

### 7.3 纪律（延续现架构零泄漏状态）

- 参数/描述/返回永不含 piko URL、token、票据、候选地址、端口映射细节；错误文本脱敏照抄 `sanitizeTransferError`（剥预签名 URL，agent_transfer_common.go:143-170）扩展为 `sanitizePeerError`。
- `device_info` 是唯一允许返回链路基础信息的口（用户明确要求「除非 LLM 自己要看」），返回 `via: lan|relay|cos` 一行式摘要而非拓扑。

---

## 8. 安全模型

1. **peer ACL（服务端集中）**：v1 同账号隐式允许 + 全量审计（谁、对哪台、执行了什么、走了哪条路）；`peer_exec_enabled` 设备级开关默认关；后续可加 pair 级白名单（TerminalWebGrant 的 scoped 凭据是模板）。
2. **可选目标机确认档**：B 侧「peer 命令需本机确认」开关（默认关），开启后 peer.exec 先推 tool_ask 式确认帧（复用 interaction_id 幂等/SLA/取消三态骨架，agent_ai_tool_ask.go:264-320）。
3. **56433 红线**：永不改绑、永不放开 loopbackOnly；设备间一切访问走隧道+票据。
4. **帧上限**：新 peer 帧处理路径 SetReadLimit（§5.1）。
5. **双路由表强制检查**：任何新增本地 HTTP 端点必须同时在 routes.go(56431) 与 registerAgentRoutes(56433) 注册（auth-recover 404 活案例，阶段 0 修复项 5）。
6. **updated_by 审计**：device_metadata 每次写记录来源（user/llm/agent）。
7. **出口 IP 观测的 XFF 信任链**必须显式配置，未配置直连模式才读 remoteAddress。
8. **PhoneServer 爆炸半径声明**：peer 上线后服务端被攻破 = 全账号设备命令执行权；审计 + 默认关的设备开关 + 可选确认档是三层缓解，评审时须显式接受该风险。

---

## 9. 服务端改动清单（PhoneServer）

| 项 | 类型 | 说明 |
|---|---|---|
| device_metadata 表 + 3 API + 变更推送 | 新建 | §4.4 |
| peer.* 帧路由（exec/result/rejected 转发）| 新建 | 复用 publishToAgent + requestAgentPayload 骨架 |
| peer ACL + peer_exec_enabled 设备开关 + 审计 | 新建 | §8 |
| fetch 编排扩展（调用设备=目标的 POST /api/transfers） | 小改 | 现编排校验链全复用 |
| 出口 IP 观测（remoteAddress/XFF）+ last_egress_ip 列 | 小改 | §4.3 |
| capabilities 门控 peer_v1/peer_path_v1/peer_access_v1 | 小改 | requireDeviceCapability 先例 |
| piko 访问票据签发 API（Ed25519 路由票） | 新建 | §5.4 |
| LAN 探测短时 token 签发与校验（≤30s、单用途） | 新建 | §6.1 实证腿；与 piko 路由票是两套凭据 |
| 规模护栏（设备数/数组/限流） | 小改 | §3 |
| 传输编排状态机 route 字段 + lan 候选决策点 | 阶段 2 | §6 |

## 10. 客户端改动清单（alianggate）

| 项 | 类型 | 说明 |
|---|---|---|
| peer.exec.request 处理分支（有界执行+审计）| 新建 | §5.2，注册协议契约表 |
| 意图工具分两批（注册表 + agentcore 双接入）：阶段 1 = peer_exec/peer_fetch_file/device_note/device_info(本机)；阶段 2 = peer_access/device_info(链路内省) | 新建 | §7，rev 3→4 |
| callAgentServer POST 变体（带 ctx 超时）| 小改 | 照 doAgentServerGET 模式 |
| 阶段 0 各护栏项 + auth-recover 修复 | 小改 | §3 |
| hello `system` 子对象 + profile_hash | 小改 | §4.2 |
| heartbeat `net` 子对象 + Addrs 采集 | 小改 | §4.3 |
| peer.negotiate 客户端 + LAN 探测器（token TLS + 临时 listener）| 阶段 2 | §6 |
| piko 访问者拨号器 + 本地反向转发 | 阶段 2 | §5.4 |
| transfer.receive `direct_candidates` 消费 + 直连腿 | 阶段 2 | §6.3 |

---

## 11. 交付计划

| 阶段 | 内容 | 估时 | 交付判据 |
|---|---|---|---|
| 0 | 规模护栏 + 幽灵过滤 + auth-recover 修复 | 2-3 人日 | 护栏生效；本机 A→A 冒烟 auth-recover 恢复链通 |
| 1 | peer 寻址帧 + peer.exec + fetch 编排 + 意图工具门面（第一批 4 工具）+ 灵活层（表/API/工具）+ ACL/审计 | 9-12 人日 | LLM 在设备 A 上 `peer_exec(B, ...)` 取回显；`device_note` 写读闭环且 hello 重连不丢；审计全落 |
| 2 | 画像冷/热扩展 + 出口 IP + 协商帧族 + LAN 检测 + piko 访问者 + 票据签发 + 探测 token + direct_candidates + 第二批工具（peer_access/链路内省） | 7-10 人日 | 同 LAN 两机文件走直连（审计可见 via=lan）；跨网 fallback COS；peer_access 打通 B:port |
| 触发式 | 负载时序表、git 提交时间、中途无缝换路、mDNS、P2P 打洞 | 另计 | 见 §12 |

合计 ≈ 18-25 人日。每阶段独立可交付、独立可回退（能力位门控）。

## 12. 开放问题

1. `device_note` 审批默认档：免审批+审计（当前倾向）vs 每写必确认。
2. peer.exec 目标机确认档的默认值（建议生产默认关、企业提供配置开）。
3. 中途无缝换路的分块对齐语义（直连分块↔COS 分块续接）——spec §14 遗留，需单独设计。
4. WS 限流数值与突发策略（建议 60 帧/分 + 10 突发）。
5. profile_hash 粒度：整包 vs 分节（projects/tools/system 各自 hash）。
6. egress IP 双出口设备（Wi-Fi+以太网）的候选生成策略。
7. LAN 临时 listener 的端口族与防火墙提示（macOS 本地网络权限弹窗预期）。
8. pair 级 ACL 引入时机（多用户共享账号场景出现时）。
