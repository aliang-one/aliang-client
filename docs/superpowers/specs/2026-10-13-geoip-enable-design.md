# GeoIP 接通设计：条目语法扩展 + 数据分发 + 匹配真实现

- 日期：2026-10-13（v2：吸收静态审阅 5 项 P0/P1 与补充项）
- 状态：设计已与用户对齐（A 方案），待实现
- 前置：上一特性「规则语法扩展（glob+IP）」已合并 master（`2dffa5d`），本设计复用其全部链路（`processor/match` 内核、快照编译器、校验面、UI 校验）
- 三个子项目序列中的第 1 个（geoip → 无 SNI TLS 放行 → 服务端协同）

---

## 1. 背景与现状（侦察 + 静态审阅结论）

### 1.1 已就绪

- `processor/geoip` 包功能完整（`service.go` 239 行）：`GetService()` 单例、`DefaultDatabasePath()`（经 `cache.GetCacheSubdir` → `runtimepath.ResolveStateDir`，daemon 模式下为 `<CoreDataDir>/geoip/GeoLite2-Country.mmdb`，支持 `~` 展开）、`LoadDatabase(path)`（缺文件自动下载：5 分钟超时、`.tmp`→rename 原子落盘）、`LookupCountry(net.IP)`、`Enable/Disable`。读取库 `oschwald/geoip2-golang v1.9.0` 已在 go.mod。
- 数据目录已被 `processor/setup/data_dir.go:56` 预创建。**路径说明**：实际落点由 `DefaultDatabasePath()` 决定，实现一律委托该函数取路径，不硬编码 `CoreDataDir`（user-agent 模式解析到用户态目录，属预期）。
- 运行时分发就位：`processor/routing/decision_engine.go:173`（`checkRuleBySnapshot`）与 `:196`（legacy `checkRule`）都把 `RuleTypeGeoIP` 路由给 `matchGeoIP`，入参含 `ctx.IP`。
- 模型层：`common/model/routing_config.go:19` `RuleTypeGeoIP = "geoip"`；legacy 校验 `isValidCountryCode`（`^[A-Z]{2}$`，`:87`）。

### 1.2 缺口（含静态审阅 5 项）

1. **【P0】daemon 启动路径不初始化 GeoIP**：三条启动入口中 `cmd/start.go:113`（`InitializeGlobalRuleEngine`，start 用）与 `cmd/tray.go:67`（tray 用）会经过 GeoIP 编排点，但 **daemon 走 `cmd/core.go:157` → `initializeCoreRuleEngine()`（:173）→ `rules.GetEngine().Initialize()`，完全不经过 GeoIP**——只改 `cmd/start.go` 会使 core 模式永远不可用。
2. **【P0】UI 逗号 tokenizer 破坏多国语法**：AI include 输入经 `sanitizeList`（`RulesSettings.vue:324`）按 `[\n,;]+` 拆分，`geoip:CN,HK,US` 会被拆成三个坏条目。→ **多国分隔符定为 `+`**（见 §3.1 修订说明）。
3. **【P1】快照层把 condition 小写化**：`CompileRuntimeSnapshot`（`snapshot.go:185`）`condition: strings.ToLower(...)`——geoip 条件 `CN+HK` 会变 `cn+hk`；`matchGeoIP` 若大小写敏感比较则全部失效。
4. **【P1】锁内下载**：`LoadDatabase`（`service.go:60-63`）在持有 `s.mu.Lock()` 期间执行最长 5 分钟的网络下载；直接拆 `LoadLocal` 不解决，下载必须移出锁外，完成后原子替换 reader。
5. **【P1】死路径旧 GeoIP 全局路由**：`processor/rules/engine.go:109 → checkGeoIP(:206)`（`chinaDirect` 字段 :22）在 `EvaluateRoute` 内对 IP 按国家直接路由——该路径虽已证实死代码（`EvaluateRoute` 无生产调用者），但启用 geoip 服务后一旦复活即与新语义冲突，须删除或显式废弃。
6. **启动显式禁用**：`cmd/start.go:222` `geoip.GetService().Disable()`（注释 `:112` 与代码相反）；正确加载函数 `initializeGeoIPDatabase()`（`:233-248`）零调用者。
7. **匹配恒 false**：`matchGeoIP`（`decision_engine.go:235-243`）无条件 false stub。
8. **语法不可达 + 误编译**：`ClassifyEntry` 无 geoip 关键字；`GEOIP,CN,proxy` 被兜底编译成 domain `cn`（后缀命中所有 `*.cn`，错误行为）；`geoip:CN` 被 `SplitHostPort` 剥成 domain `geoip`；裸 `geoip` 关键字会成为 `*.geoip` 后缀规则（同类误编译）。
9. **下载源已死**：`DefaultGeoIPDownloadURL = "https://git.io/GeoLite2-Country.mmdb"`（`service.go:28`）。
10. **死开关**：`GeoIPEnabled` 字段与 `SwitchManager.geoIPEnabled`——引用点为 `rules_handler.go` 的 API 状态回显（:121/:124/:177/:231/:246 等）与内存字段，无持久化、无行为联动；legacy 写路径已封死（400）。
11. **文档残留**：`processor/geoip/download_example.md`（6 处 git.io）与 `docs/api-reference.md`（`:11` 依赖表行、`:396` 附近 geoip 段）写死旧 URL/路径。

### 1.3 数据文件事实

- 源镜像：`https://github.com/P3TERX/GeoLite.mmdb/releases/latest/download/GeoLite2-Country.mmdb`（官方 license key 每周构建；2026-10-13 实测 HTTP 200、3.3MB、geoip2-golang 读取验证 `114.114.114.114→CN`/`8.8.8.8→US` 正确）。
- 用户 COS 分发（**已就位**）：`https://aliang-1305838434.cos.ap-nanjing.myqcloud.com/static/GeoLite2-Country.mmdb`（2026-10-13 实测 HTTP 200，Content-Length 3410978）。

---

## 2. 目标与非目标

### 目标

1. 用户在 `ai_rules.*.include` / `proxy_rules` 写 geoip 条目（`geoip:CN`、`geoip:CN+HK+US`、`GEOIP,CN,proxy`），编译产出 `Type=geoip` 并按 IP 归属国真实命中（**编译快照后匹配的端到端集成测试**覆盖）。
2. mmdb 从用户 COS 分发：本地缺失后台异步下载（**锁外**），不阻塞启动；就绪后 geoip 规则自动生效；**start / tray / core 三条启动路径全部接入**。
3. 修掉 geoip 条目误编译为域名规则的全类问题（`GEOIP,CN,proxy`→`cn`、`geoip:CN`→`geoip`、裸 `geoip`→`*.geoip`）。
4. UI 校验器（含 tokenizer 兼容）、统计归一化、文档全链路同步。

### 非目标（明确不做）

- 不做定期自动更新/刷新；不接 `GeoIPCache`（YAGNI）；不做否定语法（`!CN`）。
- 不补 canonical `/api/config/routing` 对 rule.Type/Condition 的通用校验（预存空洞；结构化 `type:"geoip"` 多国 condition 透传合法，见 §3.3）。
- 不动 UDP 与无 SNI TLS 盲区（子项目 2）。
- 不删 `IsChina` 等包内其他死代码。

---

## 3. 语法规范（唯一权威定义）

### 3.1 geoip 条目写法（v2 修订：多国分隔符 `+`）

```
// 冒号风格（先于逗号切分；多国用 + 分隔——避开 AI include 逗号 tokenizer）
geoip:CN              单国
geoip:CN+HK+US        多国任一命中
GEOIP:cn              大小写不敏感 → 归一化 "CN"

// Clash 风格（单国；仅用于按行/数组存储的 proxy_rules 与 config 文件/API 面，
// AI include 输入按逗号拆分不支持该形态）
GEOIP,CN,proxy        尾段 target 照旧剥离
geoip,JP              无尾段照旧
```

**v2 修订理由**：原定多国逗号分隔（`geoip:CN,HK,US`）与 AI include 的逗号 tokenizer（`sanitizeList` 按 `[\n,;]+` 拆分）根本冲突；改用 `+` 后 UI/后端零启发式、无误并风险。`+` 在域名结构校验中本为非法字符，geoip 分支内合法不产生歧义。

- **归一化 condition**：大写国家码 `+` 串（如 `"CN+HK+US"`），去空白；Clash 风格取第二段（单国）。
- **校验**：每段必须 `^[A-Z]{2}$`（ISO 3166-1 alpha-2）。显式拒绝（错误含条目原文与原因）：`geoip:CHN`、`geoip:`、`geoip:CN+`、`geoip:CN++HK`（空段）、`geoip,`（空段）、**裸 `geoip`**（无冒号无逗号——防 `*.geoip` 域名误编译）、**`geoip:CN,HK`**（逗号多国——AI include 会拆坏，校验层直接拒绝并提示「多国用 + 分隔」，防止坏 payload 进入存储）。
- **边界定义**：`geoip,CN,proxy,extra` → Clash 风格取第二段 `CN`，其余段按噪声忽略（与既有尾段剥离语义一致）；结构化 `RoutingRule.Type=geoip` 的 condition 允许多国（`+` 或 `,` 串——**匹配侧统一把 `,` 归一为 `+` 再拆分**，见 §4.3；此形态只能经 canonical 注入路径到达，legacy `RoutingRule.Validate`（`routing_config.go:55-60`，死路径）仍强制单国 `^[A-Z]{2}$`），canonical schema 仍不校验（预存空洞，非目标）。
- **误编译修复**：geoip 分支在逗号兜底与域名兜底之前生效；`GEOIP,CN,proxy` 产出 `Type=geoip, Condition="CN"`。裸域名 `cn` 条目不受影响。

### 3.2 匹配语义

- 运行时对 `ctx.IP` 查 mmdb 得 ISO 国家码，与 condition 拆分后的任一国码**大小写不敏感**比较（`EqualFold` 或双侧 `ToUpper`）——**必须适配快照层的小写化**（`snapshot.go:185`），由「编译快照后匹配」集成测试锁定。
- mmdb 未就绪 / IP 未收录 / IP 空 → 不命中（静默，热路径零日志）。
- 生效范围与 IP 条件一致（三盲区不生效，文档写明）。

### 3.3 数据获取

- 默认下载地址：`https://aliang-1305838434.cos.ap-nanjing.myqcloud.com/static/GeoLite2-Country.mmdb`；覆盖：环境变量 `ALIANG_GEOIP_DOWNLOAD_URL`。不新增 config schema 字段。
- **加载编排（锁纪律）**：`processor/geoip` 拆分为两步——`LoadLocal(path)`（持锁：打开+原子替换 reader）与 `EnsureDownloaded(path)`（**无锁**：stat+下载，复用 tmp→rename 与 5 分钟超时）。编排方先 `EnsureDownloaded`（文件已存在时零网络）再 `LoadLocal`；**下载绝不持锁**。就绪前 `LookupCountry` 立即返回未命中。
- 启动行为：本地已有 mmdb → 同步加载；缺失 → Info「后台下载中」+ goroutine 异步 `EnsureDownloaded→LoadLocal→Enable`，失败仅日志、不重试本次。配置含 geoip 条目且库未就绪 → Info 提醒一次。
- 旧 `LoadDatabase`（锁内下载语义）删除，唯一调用方是本设计改造的编排函数（grep 已证实零其他调用者）。

---

## 4. 架构设计

### 4.1 `processor/match`：`EntryGeoIP` + 关键字识别

- `EntryKind` 新增 `EntryGeoIP`，`String()` → `"geoip"`。
- `ClassifyEntry` 显式分支（IP 关键字之后、逗号兜底之前）：冒号风格 `geoip:`（`+` 多国）与 Clash 首段 `geoip`（单国）；解析/归一化/校验；裸 `geoip`、逗号多国、非法码均显式报错。
- 归属原则不变：校验严格在分类器，运行时匹配宽松兜底。

### 4.2 快照编译：`Type=geoip` 分支

- `appendRules` ruleType 三分支（domain/ip/geoip），去重键 `kind|cond` 不变。快照层 condition 小写化**保持不动**（domain/ip 路径零扰动），geoip 大小写适配在匹配侧（§4.3）。

### 4.3 匹配真实现（可注入查找 + 大小写适配）

```go
// geoipLookup 可被测试替换；默认委托 processor/geoip 单例。
var geoipLookup = func(ip net.IP) (string, error) { ... LookupCountry().ISOCode ... }

func matchGeoIP(condition, ip string) bool {
    code, err := geoipLookup(net.ParseIP(ip)); if err != nil || code == "" { return false }
    // `,` 与 `+` 均为合法多国分隔（`,` 仅来自结构化注入路径），统一归一后拆分；
    // 快照层 condition 已被小写化，用 EqualFold 适配
    norm := strings.ToUpper(strings.ReplaceAll(condition, ",", "+"))
    for _, c := range strings.Split(norm, "+") {
        if c != "" && strings.EqualFold(c, code) { return true }
    }
    return false
}
```

### 4.4 启动编排（**三路径全覆盖**）

- 抽共享函数（建议 `cmd` 包内 `initializeGeoIPLoading() error` 或等效）：本地有文件 → `EnsureDownloaded`(零网络)→`LoadLocal`→Enable；缺失 → Info + goroutine 异步（锁外下载→LoadLocal→Enable→Info 就绪）。
- 接入点：`cmd/start.go` `InitializeGlobalRuleEngine`（替换 `:222` `Disable()`，`:112` 注释改真）、`cmd/tray.go:67` 同函数自然覆盖、**`cmd/core.go` `initializeCoreRuleEngine()`(:173) 追加调用**（daemon 路径）。
- geoip 条目存在性检测（一次性、非热路径）：启动时扫 `Customer.ProxyRules` 与 `AIRules include` 的 `geoip` 前缀（大小写不敏感 + Clash 首段）。

### 4.5 `GeoIPEnabled` 处置：**保留兼容字段，停止一切行为读取**

- `model.RulesSettings.GeoIPEnabled`、`SwitchManager.geoIPEnabled`、`rules_handler` 的 API 回显字段**全部保留**（避免破坏 dashboard 状态契约）；写路径维持已封死状态。任何行为分支不得依赖该字段——geoip 生效 = 「规则存在 + 库已加载」。grep 清单：`rules_handler.go:121/124/177/231/246/253-275`、`switch_manager.go:15/31` 及 `:68-75/:93-97/:130-142/:152/:163/:174/:185`（全部保留不动）。

### 4.6 周边面同步（静态审阅补充项）

- **traffic_mirror**：`TrafficMirrorConfig.Validate`（`types.go:311`）对 `ClassifyEntry` 判定为 `EntryGeoIP` 的条目**显式拒绝**（错误注明「镜像列表不支持 geoip 条目」）——镜像按域名匹配，geoip 条目零命中可能，拒绝优于静默失效（与 IP 条目「接受+文档」不同处理的理由：裸单 IP 尚可字面命中，geoip 条目命中概率为零）。
- **统计归一化**：`ai_domains.go` normalize 的 IP 守卫扩展为 `EntryIP || EntryGeoIP` → 返回归一化 condition（展示形态与编译侧一致，防逗号截断/字符集变形）。
- **死路径清理**：`processor/rules/engine.go` 删除 `checkGeoIP`（:206）与 `chinaDirect`（:22）及 `:109` 调用点（`EvaluateRoute` 已证实死代码；防复活语义冲突）。
- **文档残留**：`processor/geoip/download_example.md` 重写（COS 地址 + env 覆盖 + 新编排日志）；`docs/api-reference.md` geoip 段更新（`:11` 依赖表行与 `:396` 附近一并更新）。

### 4.7 UI

- `RulesSettings.vue`：`validateRuleEntry` 加 geoip 分支（冒号 `+` 多国 + Clash 单国）；**专用检测**：include 面出现 `geoip:CN,HK` 逗号多国 → 明确错误提示「多国请用 + 分隔」。**该检测必须作用于 tokenizer 拆分前的原始草稿文本**（拆分后的碎片 `geoip:CN` 与 `HK` 各自合法，拆后检测永不触发）；i18n 键 `rules_geoipInvalid`（zh/en）；占位符/提示文案。proxy_rules 文本域按行拆分不受影响。
- UI 校验与后端语义对齐红线不变：前端从不拒绝后端会接受的输入。

### 4.8 文档

README 中英 + usage-guide zh/en：geoip 语法段（写法、`+` 多国、生效范围三盲区、数据获取说明——启动自动从 COS 下载、env 覆盖、COS 副本建议每月刷新）；zh_CN 走 `md_to_mdx.py` 重生成。

---

## 5. 改动清单（文件级）

| # | 文件 | 改动 |
|---|---|---|
| 1 | `processor/match/entry.go` + `entry_test.go` | `EntryGeoIP` + geoip 分支（`+` 多国/Clash 单国/边界拒绝）；矩阵含误编译回归 |
| 2 | `processor/routing/snapshot.go` + `snapshot_test.go` | 三分支编译；geoip 条目 → Type=geoip 测试；误编译回归 |
| 3 | `processor/routing/decision_engine.go` + 新测试 | matchGeoIP 真实现 + seam + 大小写适配；注入查找矩阵 |
| 4 | **`processor/routing` 集成测试** | **编译快照后匹配**（compile→DecideRouteFromSnapshot 注入查找，小写化路径实证） |
| 5 | `processor/geoip/service.go` | `LoadLocal`/`EnsureDownloaded` 拆分（下载移出锁）；COS URL + env；删旧 `LoadDatabase`；`download_example.md` 重写 |
| 6 | `cmd/start.go`、`cmd/core.go`、`cmd/tray.go`（如需） | 共享编排函数 + 三路径接入；core 路径测试 |
| 7 | `processor/rules/engine.go` | 删 `checkGeoIP`/`chinaDirect`/调用点（死路径） |
| 8 | `processor/config/types.go` + 测试 | mirror 拒绝 geoip 条目；customer 校验 geoip 正反例 |
| 9 | `processor/statistic/ai_domains.go` + 测试 | normalize 守卫扩展 EntryGeoIP |
| 10 | `common/model` + `switch_manager` + `rules_handler` | `GeoIPEnabled` 保留停读（含注释说明） |
| 11 | `app/website`（RulesSettings.vue + i18n） | geoip 分支 + 逗号多国专用提示 + i18n |
| 12 | README ×2、usage-guide ×2、mdx/import.json、`docs/api-reference.md` | geoip 语法/生效范围/数据获取文档 |

开发流程：git worktree 分支（`feat/geoip-enable`），全中文提交 + Co-Authored-By 页脚。

---

## 6. 兼容性与风险

| 风险 | 评估 | 缓解 |
|---|---|---|
| 存量 geoip 风格条目语义变化 | `GEOIP,x` 从「`*.x` 域名误编译」变为真实 geoip 规则或显式拒绝——修复而非破坏 | 回归测试 + 发布说明 |
| 下载持锁阻塞（审阅 P1#4） | 已设计移出锁 | 并发测试：下载期间 lookup 立即不命中 |
| 快照小写化使规则失效（审阅 P1#3） | 已设计匹配侧适配 | 编译快照后匹配的集成测试 |
| core 路径漏接（审阅 P0#1） | 已设计三路径接入 | core 路径测试 |
| UI tokenizer 拆坏多国（审阅 P0#2） | `+` 分隔符 + 逗号多国显式拒绝提示 | UI payload 级测试（最终提交内容断言） |
| COS 不可达/副本过时 | geoip 规则不命中，其余零影响；失败仅日志下次再试 | 文档写明刷新建议（每月） |
| mmdb 查询热路径成本 | µs 级内存映射查找，规则量几十条 | 不引入缓存（YAGNI） |
| 死开关残留误读 | 保留兼容字段停读，注释说明 | grep 清单（§4.5） |
| API 注入垃圾 geoip 规则 | 预存 schema 空洞，不扩大不闭合 | 非目标记录 |

---

## 7. 测试策略

1. `processor/match`：geoip 语法矩阵（`+` 多国/大小写/Clash 单国带尾段/`geoip,CN,proxy,extra` 噪声段；拒绝集：`CHN`/空段/`CN++HK`/裸 `geoip`/`geoip,CN,HK` 逗号多国/`geoip:CN,HK`）+ 误编译回归 + domain/ip 零回归。
2. `processor/routing`：编译 `Type=geoip`；matchGeoIP 注入查找矩阵（多国任一命中/未收录/大小写：condition `"cn+hk"` 与 `"cn,hk"`（结构化逗号串归一）vs code `"CN"` 均命中）；**集成：CompileRuntimeSnapshotFromRuntimeInputs → DecideRouteFromSnapshot 注入查找命中**（实证小写化路径）。
3. `processor/geoip`：`LoadLocal`（存在/缺失/损坏）；`EnsureDownloaded`（URL 常量/env 覆盖/失败日志）；**并发测试**：EnsureDownloaded 进行中 LookupCountry 立即不命中、LoadLocal 后立即可查。
4. `cmd`：三路径编排接入测试（core 路径必测；异步路径注入化）。
5. `processor/rules`：checkGeoIP/chinaDirect 删除后包编译与既有测试绿。
6. `processor/config`：customer geoip 正反例；mirror 拒绝 geoip 条目。
7. `processor/statistic`：geoip 条目归一化不变形（对照编译侧 condition）。
8. 前端：geoip 分支探针（含 `geoip:CN+HK+US` 过、`geoip:CN,HK` 专用提示、`geoip:CHN` 拒）+ **提交 payload 断言**（include 最终数组含完整 `geoip:CN+HK+US` 单条目）+ i18n 键集 + 构建。
9. 全量：`go test ./...`（预存 flake 守则同前）。

---

## 8. 验收标准

- [ ] `geoip:CN+HK+US` 与 `GEOIP,CN,proxy` 编译为 `Type=geoip`；后者不再产出 domain `cn`；`geoip:CN,HK` 被校验拒绝并提示 `+` 分隔。
- [ ] **core daemon 路径** geoip 初始化生效（三路径测试齐全）。
- [ ] 本地无 mmdb 启动：不阻塞、下载期间查询立即不命中、下载完成自动生效；含 geoip 条目时一次性日志提醒。
- [ ] 快照小写化条件下 geoip 规则仍命中（集成测试实证）。
- [ ] `geoip:CHN` / 裸 `geoip` / `geoip,` 保存被拒（含原文）；合法条目通过；mirror 列表 geoip 条目被拒（明确提示）。
- [ ] 前端 geoip 正反例与后端一致，最终提交 payload 完整（多国单条目）；误拒清单为空。
- [ ] `checkGeoIP`/`chinaDirect` 已删除；`GeoIPEnabled` 无任何行为分支依赖。
- [ ] 存量无 geoip 条目配置零行为变化；domain/ip 既有测试全绿；文档五处同步（README×2、guide×2、api-reference/download_example）。
