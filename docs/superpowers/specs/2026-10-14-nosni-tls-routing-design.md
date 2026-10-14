# 无 SNI TLS 放行规则引擎设计（行为直接放行）

- 日期：2026-10-14
- 状态：设计已与用户对齐，待实现
- 前置：规则语法特性（glob+IP）与 GeoIP 接通（并行分支 `feat/geoip-enable`）提供的 IP/geoip 规则能力是本设计的价值前提；本设计不依赖其合并顺序，但文档生效范围表述需与其协调
- 三子项目序列中的第 2 个；第 3 个（服务端协同）已证实收敛为 agent 仓内交付，另行处理

---

## 1. 背景与现状（2026-10-13/14 深度侦察结论）

### 1.1 现状行为

- `processor/tcp/handler.go:518-521` 函数头注释（逐字）：「Transparent TLS interception only trusts identifiers observed on the current connection. If we can't read an explicit hostname or SNI, we bypass MITM and route direct to avoid certificate and HTTP/2 mismatches on shared IPs.」
- **提前直连条件链**（`handler.go:584-637`）：`HostName==""` → DNS 缓存查询（`DstIP` 有效时）→ `selectUniqueCachedDomainEntry`（`handler.go:239`，唯一域名数恰为 1 才补名）→ 补名失败仍 `HostName==""` → **`:636` 无条件 `RouteDirect` 返回**。规则引擎（含 IP/geoip 规则）从不被咨询。
- DoH 特判（`:577`）在更早处，仅当 `HostName!=""` 触发——与本设计无交集。
- 唯一绑定补名成功的连接（`:608`）已有名字，走正常路由路径——本设计不动。

### 1.2 放行后的出口安全性（逐行核实）

| target | 无 SNI 下游行为 | 结论 |
|---|---|---|
| RouteDirect | `dialDirect` 只拨 `DstIP:port`（`:671`），全程不读 HostName | 安全 |
| RouteToSocks | socks5 空 HostName → `useDomain=false` → IP CONNECT（`outbound/proxy/socks5/socks5.go:134`）；http 上游 `DestinationAddress()`=DstIP:port（`metadata.go:59`）；客户代理禁用时降级直连（`handler.go:915`） | 安全 |
| RouteToALiang | `resolveTLSRoute:699` → `PerformMITM(mitmedSNI)`；无 SNI 会话 `mitmedSNI` 必为空串——`:657-661` 两个补名条件均不可达（第一条件的关键守卫是 `HostName != ""` 恒假，与 DNSInfo 是否为 nil 无关；第二条件 `cacheFallbackUsed` 只在唯一绑定补名路径 :606 置位）→ 给空名签 MITM 证书，**必断链** | 必须硬排除 |
| mirror/MITM | `shouldMITMForMirror` → `MatchesAnyDomain(HostName="")` 恒 false——名称硬门天然保护 | 不会误触 |

- 引擎匹配面：`MatchContext{Domain: HostName(空), IP: DstIP}`（`tls.go:312-317` 已填）——无 SNI 时**只有 IP/geoip 规则可命中**，域名规则天然不命中落默认 direct。
- deny 规则在 TCP 路径退化为 direct/socks fallback（`tls.go:325-332` 无 RouteDeny case）——预存行为。

### 1.3 放行成本

无 SNI 连接从「跳过引擎」变「咨询引擎」：每连接一次快照编译 + 规则匹配（与有 SNI 连接同成本；规则量几十条，µs-ms 级）。可接受。

---

## 2. 目标与非目标

### 目标

1. 无 SNI TLS 连接携带 IP 上下文咨询规则引擎：**IP/geoip 规则真实生效**（命中 socks 则按 IP 走客户代理，命中 geoip 按归属国分流），无规则命中维持现状 direct。
2. `RouteToALiang` 命中时**硬排除**（按未命中处理降级 direct）——空名 MITM 断链不可达。
3. MITM/镜像零误触（名称硬门保持），MITM 语义与安全注释同步改写。
4. 文档生效范围从「三盲区」改为「两盲区」（UDP、本地 HTTP 代理入站）。

### 非目标（明确不做）

- 不加任何配置开关（用户已拍板「行为直接放行」；与 geoip 特性「规则即开关」哲学一致——写 IP/geoip 规则即启用）。
- 不新增 RouteDeny 语义（deny 在 TCP 路径退化为 fallback 是预存行为，保持）。
- 不动 DoH 特判、唯一绑定继续路径、HTTP 非 TLS 路径、UDP 恒直连。
- 不做 MatchContext/编译器的引擎侧过滤（toAliang 排除在 handler 侧降级实现，零引擎侵入）。

---

## 3. 行为规范

### 3.1 放行点语义

替换 `handler.go:636` 的提前直连为：

```
if metadata.HostName == "" {
    route := 咨询规则引擎(metadata)            // 复用 DetermineRouteWithContext 同链路
    if route == RouteToALiang {
        route = RouteDirect                    // 硬排除：空名 MITM 必断链
        // Debug 日志（每连接级，不可 Info 刷屏）：no-SNI hit toAliang rule, downgraded to direct
    }
    return h.resolveTLSRoute(sessionCtx, connectCtx, originConn, metadata, route, wrapped, "")
}
```

- 咨询实现复用 `tls.go` 既有入口（`DetermineRouteWithContext` → `decideRouteWithRoutingEngine` → `DecideRouteFromSnapshot`），`MatchContext` 的 IP 已在 `tls.go:315-317` 填充，无新代码路径。
- 失败/引擎不可用时的回退与正常路径一致（`defaultFallbackRoute` → direct）。

### 3.2 行为矩阵

| 配置状态 | 无 SNI TLS 行为 |
|---|---|
| 无任何规则 | direct（现状不变） |
| 有 IP 规则命中（如 `ip-cidr,x,proxy`） | 按规则 target 走 socks/direct |
| 有 geoip 规则命中 | 按 target 分流 |
| 命中 toAliang 规则（如 AI 域名规则误配宽 IP 条件） | 降级 direct + Debug 日志 |
| deny 规则（改造前不作用于无 SNI 流量——引擎未被咨询；现开始命中） | 退化为 direct——`EffectiveDefaultProxy` 硬编码 `"direct"`（`config/types.go:1035-1040`），与改造前行为重合；**不存在 socks fallback** |
| `shouldForceAliangRoute` 特判命中（DstPort=默认代理端口 + 回环 DstIP，`route_override.go:11`，引擎前置） | 同样被 toAliang 硬排除降级 direct（与现状一致）；此类非规则来源的降级也会打 Debug 日志 |
| 有域名规则但无 IP/geoip 命中 | direct（域名规则对空 Domain 天然不命中） |

### 3.3 MITM 不变量

无 SNI 连接在任何配置下都不会进入 `PerformMITM` 或镜像流（toAliang 硬排除 + mirror 名称硬门双保险）。`:518-521` 注释改写为准确描述：「无 SNI 时绕过 MITM（证书错配防护不变），但携带 IP 咨询规则引擎做分流」。

---

## 4. 架构设计

### 4.1 改动文件

| 文件 | 改动 |
|---|---|
| `processor/tcp/handler.go` | `:636` 提前直连段替换（§3.1）；建议把咨询+降级抽成小方法（如 `resolveNoSNIRoute(metadata) Route`）便于测试；`:518-521` 注释改写 |
| `processor/tcp` 测试 | 新增 handler 级/方法级测试（§5） |
| README 中英、`docs/tutorials/{zh_CN,en}/usage-guide.md`（+ zh mdx/import.json 重生成） | 生效范围「三盲区→两盲区」；无 SNI TLS 行为说明 |

### 4.2 可测性

咨询+降级逻辑抽为独立小函数（输入 metadata/依赖注入引擎结果），测试四例：IP 规则命中 socks / geoip 命中 / toAliang 降级 direct / 无规则现状 direct。handler 集成视 processor/tcp 既有测试基建酌情（TestMain 隔离已存在）。

---

## 5. 测试策略

1. 单元：`resolveNoSNIRoute`（或等效）四例矩阵 + toAliang 降级日志不刷屏（Debug 级断言可选）。
2. 集成：既有 processor/tcp 全量回归（TestMain 隔离）；routing/match 零改动故不重复。
3. 前端：无改动。
4. 全量：`go test ./...`（预存 flake 守则同前）。

---

## 6. 兼容性与风险

| 风险 | 评估 | 缓解 |
|---|---|---|
| 写了 IP/geoip 规则的用户，无 SNI 流量开始分流 | **这是特性本身**；未写规则用户零变化 | 发布说明明确写出 |
| toAliang 断链 | 硬排除后不可达 | 单元测试锁定降级行为 |
| 每连接编译成本 | 无 SNI 连接新增一次快照编译（与有 SNI 同级） | 已在 §1.3 评估可接受 |
| 性能敏感场景（大量无 SNI 扫描流量） | 扫描器连接多为此类；新增 µs-ms 级编译 | 与有 SNI 连接同成本；如后续 profile 显示问题，快照缓存是独立优化项（非本设计） |
| deny 退化语义困惑 | 预存行为，非本设计引入 | 非目标记录 |

---

## 7. 验收标准

- [ ] 无 SNI TLS + `ip-cidr,x,proxy` 规则：连接按 socks 出口建连（IP CONNECT）。
- [ ] 无 SNI TLS + geoip 规则：按归属国命中分流。
- [ ] 无 SNI TLS + 命中 toAliang 规则：连接直连成功（**不断链**）+ Debug 日志。
- [ ] 无任何规则：行为与改造前完全一致（direct）。
- [ ] 全程无 MITM/镜像触发（断言 MITM 代码路径不可达）。
- [ ] 域名/IP/geoip 既有测试全绿；文档两处生效范围更新。
