# 规则匹配语法扩展设计：glob 域名通配符 + IP 条件

- 日期：2026-10-11
- 状态：已与用户对齐设计方向，待实现
- 范围：alianggate 客户端（本仓库）。服务端（官网 / PhoneServer）为跨仓协同事项，见「非目标」

---

## 1. 背景与现状

域名加速的规则匹配现状（2026-10-11 代码审计结论）：

### 1.1 匹配内核

生产运行时的域名匹配只有三种纯字符串语义：**精确相等 + 后缀匹配 + `*.前缀通配`**，无正则。

| 实现位置 | 语义差异 | 状态 |
|---|---|---|
| `processor/routing/decision_engine.go:213` `matchDomain` | 精确+后缀+`*.`（`*.` 命中多级子域、不含裸域） | **生产唯一实现**（AI 加速 ToAliang 与 proxy_rules 共用） |
| `processor/mirror/mirror/matcher.go:46` `hostMatches`（即 `processor/mirror/matcher.go`） | 同上，`*.` 分支显式排除裸域 | 生产（mirror MITM） |
| `processor/watcher/aliang_http_host_rewrite.go:120` `aliangHTTPHostMatches` | 同上，`*.` 分支不显式排除裸域 | 生产（AI 加速 MITM 内 Host 重写） |
| `processor/statistic/ai_domains.go:262` `aiDomainMatches` | 同上 | 生产（统计归类，不分流） |
| `processor/rules/allowlist.go:10` `MatchDomain` | 同上 | **死路径**（入口 `EvaluateRoute` 无生产调用者） |

运行时调用链（IP 上下文来源已打通）：
`processor/tcp/handler.go:522 handleTLS` / `:460 handleNonTLS` → `processor/tcp/tls.go:277 DetermineRouteWithContext` → `:295 decideRouteWithRoutingEngine`（`:312-317` 构造 `MatchContext{Domain: HostName小写, IP: metadata.DstIP}`）→ `processor/routing/decision_engine.go:55 DecideRouteFromSnapshot` → `:168 checkRuleBySnapshot`（按 `RuleType` 分发 domain/ip/geoip）。

### 1.2 已确认的坑

1. **编译通道缺失**：`processor/routing/snapshot.go:283-338` `compileCanonicalRoutingFromRuntimeInputs` 硬编码只产 `Type=domain` 规则（`:311`、`:321`）。`RuleTypeIP` 与 `matchIP`（`netip.ParsePrefix` CIDR 匹配，`decision_engine.go:245`）在内核中就绪，但没有任何路径编译出 IP 规则 → IP 规则今天不可用。
2. **静默误编译**：`snapshot.go:387` `parseProxyRuleDomain` 对 `ip-cidr,121.32.12.0/24,proxy` 这类条目会把尾段当域名提取，产出 `Type=domain, Condition="121.32.12.0/24"` 的规则——永不命中且无报错。
3. **统计侧变形**：`processor/statistic/ai_domains.go:240` 在第一个 `/` 处截断，CIDR 条目会被变形为裸 IP 参与统计。
4. **静默失效陷阱**：域名字段校验全程放行任意字符串（前端 `RulesSettings.vue` 域名输入零格式校验；`processor/config/types.go:800-813` `validateCustomerEditableSurface` 仅 TrimSpace+非空；`routing_schema.go:140` 不校验 condition）。用户写正则等不被支持的语法会静默永不匹配。
5. **四套实现漂移**：1.1 表中四套生产实现语义近似但独立演化（如 `*.` 裸域排除行为不一致）。

### 1.3 IP 匹配的生效范围（诚实边界）

引擎被咨询的 TCP 路径上 `ctx.IP` 已填充（TUN 五元组真实目的 IP，`inbound/tun/tunnel/tcp.go:34`）。三个盲区**本设计不改动**：

- 无 SNI 且无 DNS 缓存绑定的 TLS 连接在 `handler.go:636` 提前 RouteDirect，不查引擎（该提前返回承担防共享 IP MITM 错配的安全职责）；
- UDP 硬编码直连（`inbound/tun/tunnel/udp.go:41`）；
- 本地 HTTP 代理入站 `DstIP` 为监听地址非真实目标（`inbound/http/handler.go:86`）。

IP 规则的核心价值场景：**无 SNI 的非 TLS 流量**（`handler.go:511-512` 会带 IP 查引擎，今天只能默认直连）。

### 1.4 其他事实

- `RuleType` 枚举 `domain/ip/geoip`（`common/model/routing_config.go:16-20`）；`matchGeoIP` 恒 false stub；`processor/geoip` 有 mmdb 加载代码（`cmd/start.go:233` `initializeGeoIPDatabase`），但该函数本身无调用者——加载链同为死代码——接通属后续机会，不在本设计内。
- `go.mod` go 1.25.5，`netip` 全 API 可用；`go4.org/netipx` 为 indirect 依赖。
- 前端 i18n：`app/website/src/i18n/{zh,en}.js`，`i18n.test.js` 强制 zh/en 键集一致。
- processor 包测试约定：标准库 `testing` + `t.Run` 表驱动（范例 `processor/watcher/aliang_http_host_rewrite_test.go:99`）；`processor/mirror` 现无任何测试文件。
- 文档位置：`README.md:168` / `README.zh-CN.md:163`（domains 匹配说明）、`docs/tutorials/{zh_CN,en}/usage-guide.md:648` 附近（规则格式定义）+ 同构 `usage-guide.import.json`。
- 配置入口仅两条：`POST /api/config/customer`（`customer_config_service.go:108` Validate）与启动 config 文件，均过 `validateCustomerEditableSurface`。本仓未发现服务端直接拉取/写配置的代码路径。

---

## 2. 目标与非目标

### 目标

1. 域名规则支持 glob 风格通配符：`*` 出现在任意位置、可跨点号（用户已拍板「可跨级」语义）。
2. 路由层（AI 加速列表与 proxy_rules）支持 IP 条件：CIDR、单 IP、区间（IP-FROM/IP-TO），v4/v6，自动识别 + 显式关键字 + Clash 风格前缀（用户已拍板「自动+显式都支持」）。
3. 四套生产匹配实现收敛为单一共享内核，语义一处定义。
4. 新语法非法时**显式报错**（终结静默失效），存量合法配置零迁移。

### 非目标（明确不做）

- 不改三个 IP 盲区（无 SNI TLS 提前直连 / UDP 直连 / 本地 HTTP 代理 DstIP）——各自独立的产品决策。
- 不接通 geoip（mmdb 已就绪但属独立 feature）。
- 不引入正则域名匹配、不支持 `?` 单字符通配、不做结构化 rules 数组 schema 升级。
- 不动服务端：本仓两条配置入口均放行新语法；服务端 UI/接口若未来加格式校验，跨仓另行协同。

---

## 3. 语法规范（唯一权威定义）

### 3.1 域名 glob 语义

匹配输入统一先做小写化与端口剥离（沿用各调用点现状）；`*` 匹配 **≥1 个任意字符、可包含点号**；整体小写字符串比较，不使用 regexp。

| 规则写法 | 命中 | 不命中 |
|---|---|---|
| `example.com`（不含 `*`，现状不变） | `example.com`、`www.example.com`、`a.b.example.com` | `badexample.com` |
| `*.epic.com` | `cdn.epic.com`、`a.b.epic.com` | `epic.com`（裸域） |
| `aliang.*` | `aliang.xxx`、`aliang.co.uk`、`aliang.com.cn` | `aliang` |
| `*epic.com` | `cdnepic.com`、`cdn.epic.com` | `epic.com`（`*` ≥1 字符） |
| `cdn*.com` | `cdnabc.com`、`cdn.epic.com` | `ab.com` |

- 只支持 `*`，不支持 `?`（YAGNI，留作后续扩展位）。
- 兼容性锚点：现状「不含 `*` = 精确+后缀」「`*.` = 子域通配不含裸域」两条语义**不变**——glob `*`≥1 字符的性质使 `*.epic.com` 天然不命中裸域，与现状一致。

### 3.2 IP 条件语义

匹配对象为 `MatchContext.IP`（TUN 真实目的 IP）。v4/v6 均支持。以下写法全部等价进入 `Type=ip` 规则：

```
// 裸形态（自动识别）
121.32.12.0/24          CIDR
1.2.3.4                 单 IP（等价 /32 或 /128）
1.2.3.1-1.2.3.254       闭区间（from ≤ to，`-` 分隔两端均为合法 IP）
2001:db8::/32           IPv6 CIDR
2001:db8::1-2001:db8::ff  IPv6 区间

// 显式关键字（大小写不敏感）
ip-cidr:121.32.12.0/24
ip-range:1.2.3.1-1.2.3.254
ip-from:1.2.3.1,ip-to:1.2.3.254    ← 专用解析分支（条目内含逗号）

// proxy_rules 的 Clash 风格前缀（复用现有尾段 target 提取逻辑）
ip-cidr,121.32.12.0/24,proxy
ip-range,1.2.3.1-1.2.3.254,proxy
```

解析顺序：**关键字前缀识别先于逗号切分**。`ip-from:…,ip-to:…` 整体作为一个条目解析（避免被 Clash 风格逗号切分误拆）；冒号风格条目支持可选尾段 target（如 `ip-from:1.2.3.1,ip-to:1.2.3.254,proxy`，与既有尾段提取约定一致），省略时走该列表的默认 target。

判定规则：
- `ip-cidr:` / 裸 CIDR → `netip.ParsePrefix` + `Contains`；`/0` 合法（匹配全部，视为有意为之）。
- `ip-range:` / 裸 `a-b` / `ip-from:…,ip-to:…` → 两端 `netip.ParseAddr`，`Addr.Compare` 闭区间判定；**from > to 视为非法**（校验报错）；跨协议族（v4-v6）非法；单点区间 `a-a` 合法（等价单 IP）。
- 单 IP → 等价 `/32`（v6 为 `/128`）。
- 域名与 IP 的归类互斥：条目若为合法 IP 形态则按 IP 处理，否则按域名（glob）处理；两者都不是 → 校验报错（见 3.3）。

### 3.3 校验策略

- **IP 形态**：严格校验。非法 IP 条件在配置保存时（`validateCustomerEditableSurface`）明确报错，前端实时提示。不允许静默。
- **域名形态（glob 与纯域名统一）**：对所有域名条目做结构校验——不允许空标签（如 `a..b`）、不允许以 `.` 开头或结尾、通配符仅允许 `*` 字符。「纯域名条目保持现状宽松」仅指不强制完整 DNS 合法性（长度、TLD、连字符规则），**并非豁免上述结构检查**（验收标准要求拒绝无 `*` 的 `a..b`）。
- 存量兼容：现有默认配置（`domains,google.com` 等）与合法用户配置全部通过，无迁移脚本。**旧版兜底语义完整保留**：未知逗号条目取尾段、尾段为 `proxy` 时取倒数第二段（与旧 `parseProxyRuleDomain` 一致），因此 `DOMAIN-SUFFIX,x,proxy`、`a.com,b.com,proxy` 等存量形态继续按旧值通过。**host:port 提取后若 host 为 IP 形态则归类为 IP 条件**（`1.2.3.4:99` → IP `1.2.3.4`、`[2001:db8::1]:443` → IP；显式域名前缀里的 IP 值同样归类为 IP（`domains,1.2.3.4` → IP）），避免产生永不命中的死规则域名。

### 3.4 应用范围

新语法统一应用于：`ai_rules.*.include`、`proxy_rules`、`traffic_mirror.domains`、AI 加速 Host 重写的 include 列表——即所有经共享内核匹配的输入。统计侧（`aiDomainMatches`）同样委托共享内核，保证归类一致。

---

## 4. 架构设计

### 4.1 共享匹配内核：新包 `processor/match`

语义唯一定义点，纯函数、零状态、不使用 regexp（保持热路径性能特征）：

```go
package match

// MatchDomain 判定 host 是否命中 pattern。
// 语义：见本设计 3.1（精确+后缀 为无 * 兜底；含 * 走 glob，* ≥1 字符可跨点）。
func MatchDomain(pattern, host string) bool

// MatchIPCond 判定 ip 是否命中 cond（3.2 全部形态）。
func MatchIPCond(cond, ip string) bool

// ClassifyEntry 将原始条目分类为 domain/ip，并归一化出 condition。
// 供 snapshot 编译器与校验器共用，避免分类逻辑二次实现。
type EntryKind int // EntryDomain / EntryIP
func ClassifyEntry(raw string) (kind EntryKind, condition string, ok bool)
```

实现要点：
- `MatchDomain`：`strings` 分段/后缀判断；glob 无 `*` 时走旧三件套；有 `*` 时按 `.` 分段做通配匹配（`*` 段允许跨点：实现上以「首段前缀匹配 + 尾段后缀匹配 + 中间 `*` 吞并任意标签序列」或等价的单遍扫描实现，具体以测试矩阵锁定为准）。
- `MatchIPCond`：per-call `netip` 解析（与现状 `matchIP` 同级开销，规则量为几十条级别，不做缓存；若后续规则上千再加编译期预解析优化）。
- `ClassifyEntry`：识别 3.2 全部 IP 写法与 Clash 前缀，归一化 condition（小写、去关键字前缀）；IP 条目规范化输出保留原始大小写无关形式。

### 4.2 委托关系（消灭四套实现漂移）

| 调用点 | 改造 |
|---|---|
| `processor/routing/decision_engine.go:213` `matchDomain` | 委托 `match.MatchDomain`（保持函数名以免大面积改调用点） |
| `processor/routing/decision_engine.go:245` `matchIP` | 委托 `match.MatchIPCond`（获得区间/单 IP 能力） |
| `processor/mirror/matcher.go:46` `hostMatches` | 委托 `match.MatchDomain`（`*.` 不命中裸域语义由 glob 性质自然满足，回归测试锁定） |
| `processor/watcher/aliang_http_host_rewrite.go:120` | 委托 `match.MatchDomain` |
| `processor/statistic/ai_domains.go:262` `aiDomainMatches` | 委托 `match.MatchDomain`；`:240` 的首个 `/` 截断逻辑对 IP 条目改为整体归类，消除变形 |
| `processor/rules/allowlist.go:10`（死路径） | 最小同步委托，防止将来复活时语义分裂 |

### 4.3 编译通道：snapshot 产出 IP 规则

`processor/routing/snapshot.go`：

1. `parseProxyRuleDomain(:387)` 扩展显式分支：
   - 首段 `ip-cidr` / `ip-range`（Clash 风格）→ 按 3.2 提取 condition 与尾段 target；
   - 条目以 `ip-cidr:` / `ip-range:` / `ip-from:` 开头（冒号风格）→ 提取 condition；
   - 其余维持现状域名提取逻辑（含 `domains,` / `domains` 前缀）。
2. 新增条目分类：经 `match.ClassifyEntry` 判定，`compileCanonicalRoutingFromRuntimeInputs(:283-338)` 按分类产出 `Type=domain` 或 `Type=ip` 的 `CanonicalRoutingRule`（ID 前缀沿用 `ai_allowlist_` / `proxy_rule_`）。
3. `collectAIDomains` 同样走分类；AI 列表中的 IP 条目同样编译为 `Type=ip`（target=ToAliang），语义一致、无害。
4. `SnapshotRule` 结构不变（仍为字符串 condition），运行时 `checkRuleBySnapshot` 现有 domain/ip 分发即可命中，无需改判定引擎结构。

### 4.4 校验面

- `processor/config/types.go` `validateCustomerEditableSurface(:800-813)`：对 `ai_rules.*.include`、`proxy_rules`、`traffic_mirror.domains` 逐条调用 `match.ClassifyEntry` + 结构校验（3.3 策略），错误信息含条目原文与原因，走现有 400 返回路径。
- 前端 `app/website/src/components/settings/RulesSettings.vue`：proxy_rules 与 AI include 输入增加与后端一致的实时校验（IP 形态 + glob 结构），错误文案新增 i18n 键（`app/website/src/i18n/{zh,en}.js`，键集一致性由 `i18n.test.js` 保障）。代理服务器地址的既有 `DOMAIN_RE` 校验不动。
- `common/model/routing_config.go:79` `isValidDomain` 及 `RoutingRule.Validate` 属死路径（零生产调用者）：**不改动**（与 §5 第 9 行一致）；将来复活时应直接改用 `processor/match`。

---

## 5. 改动清单（文件级）

| # | 文件 | 改动 |
|---|---|---|
| 1 | `processor/match/domain.go`、`processor/match/ipcond.go`、`processor/match/entry.go`（新建） | 共享内核三函数 + 全语义矩阵测试 |
| 2 | `processor/routing/decision_engine.go` | matchDomain / matchIP 委托 |
| 3 | `processor/routing/snapshot.go` | parseProxyRuleDomain 新分支；编译按 ClassifyEntry 产 domain/ip；collectAIDomains 分类 |
| 4 | `processor/mirror/matcher.go` | hostMatches 委托（+ 新建 `matcher_test.go`，mirror 包首个测试） |
| 5 | `processor/watcher/aliang_http_host_rewrite.go` | aliangHTTPHostMatches 委托 |
| 6 | `processor/statistic/ai_domains.go` | aiDomainMatches 委托；修 `:240` CIDR 截断变形 |
| 7 | `processor/rules/allowlist.go` | 死路径最小同步 |
| 8 | `processor/config/types.go` | validateCustomerEditableSurface 挂 3.3 校验 |
| 9 | `common/model/routing_config.go` | 不改动（死路径零调用者，扩展死校验只增维护面；将来复活时直接改用 `processor/match.MatchIPCond`） |
| 10 | `app/website/src/components/settings/RulesSettings.vue` + `app/website/src/i18n/{zh,en}.js` | 实时校验 + zh/en 文案 |
| 11 | `README.md` / `README.zh-CN.md`、`docs/tutorials/{zh_CN,en}/usage-guide.md` + `usage-guide.import.json` | 语法文档（含 IP 生效范围边界说明） |

开发流程约束（仓库惯例）：实现一律在 git worktree 分支进行，不直接改 master 工作区；提交信息全中文「新增：/修复：」风格。

---

## 6. 兼容性与风险

| 风险 | 评估 | 缓解 |
|---|---|---|
| 历史静默条目「复活」 | 用户配置里以前写过 `aliang.*` 等串，升级后开始真实匹配——这正是特性，但属行为变化 | 发布说明明确写出；回归矩阵锁定旧语义不变 |
| 四套实现收敛的语义微差 | mirror `*.` 显式排除裸域 vs host_rewrite 不排除；glob `*`≥1 字符后两者天然一致 | 委托前先为各调用点补行为基线测试，委托后回归 |
| 校验变严拒绝存量配置 | IP 新语法严格报错；纯域名保持宽松 | 3.3 分层策略；存量合法配置全通过（默认配置逐条验证） |
| IP 规则命中面与用户预期不符 | 盲区（无 SNI TLS/UDP/本地代理）不生效 | 文档写明生效范围（1.3） |
| 性能 | glob 不用 regexp；IP per-call netip 解析与现状 matchIP 同级 | 规则量级几十条；预解析缓存留作后续优化位 |
| 跨仓协同 | 服务端若加配置 UI 校验会挡新语法 | 本仓入口全放行；跨仓备忘跟踪 |

---

## 7. 测试策略

1. `processor/match`：表驱动全语义矩阵——glob 命中/不命中（含 3.1 表全部行 + 边界：裸域、尾点、空标签、大小写、多级跨点、`*` 首尾中间位置）；IP 全形态命中/不命中（v4/v6、区间边界、from>to 拒绝、跨族拒绝、`/0`、单 IP、`a-a`）；`ClassifyEntry` 全前缀分类矩阵 + **现状行为不变回归用例**（`domains,x,proxy`、裸域名、尾段 target 提取等）。
2. `processor/routing`：编译集成测试——含 `*` 与 IP 条目的输入产出对应 Type 的 CanonicalRoutingRule；DecideRouteFromSnapshot 端到端命中 IP 规则（ctx.IP 路径）。
3. `processor/mirror`：新建首个测试文件，hostMatches 基线 + 委托后回归。
4. `processor/statistic`：IP/CIDR 条目归类不再变形。
5. `processor/config`：校验器对合法/非法条目的接受与拒绝路径。
6. 前端：i18n 键集一致性（现有 `i18n.test.js` 自动覆盖新键）。
7. 全量：`go test ./...`（注意既有预存 flake：`agent_ai.go` 子进程超时类测试偶败，单包复跑即绿，勿当回归）。

---

## 8. 验收标准

- [ ] `aliang.*` 命中 `aliang.co.uk`；`*.epic.com` 不命中裸域 `epic.com`；`*epic.com` 同时命中 `cdnepic.com` 与 `cdn.epic.com`。
- [ ] proxy_rules 写 `ip-cidr,121.32.12.0/24,proxy`，无 SNI 的 TCP 流量目的 IP 落在网段内时路由至 socks。
- [ ] `ip-from:1.2.3.1,ip-to:1.2.3.254` 保存成功并命中区间内 IP；`from>to` 保存被拒且错误信息可读。
- [ ] 写非法 IP 条目（如 `ip-cidr:abc`）保存被拒；写非法 glob（如 `a..b`）保存被拒；写普通域名/正则样字符串不再静默——前者正常工作，后者被明确拒绝。
- [ ] mirror / host 重写 / 统计归类与路由对同一 pattern 判定一致（共享内核）。
- [ ] 存量默认配置零改动通过校验与编译。
