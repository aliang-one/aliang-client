# 设计:仪表盘侧栏收纳「系统状态/文件访问授权」+ 运行模式迁代理设置 + 设备互访问径统一

- 日期:2026-11-05
- 状态:设计定稿(brainstorming 产出,用户已批整体骨架,「好,继续」)
- 仓:alianggate(纯前端 app/website;口径统一仅动 i18n 键值与两行源码注释,零 Go 逻辑改动)
- 基线:master@d4ed54a
- 术语审计:多 agent 全仓审计(4 路并行扫描 + 逐条对抗复核,59 条确认/5 条驳回),结论沉淀于 §5

## 1. 背景与动机

2026-11-03 四栏重组(spec 2026-11-03-agent-settings-restructure-design.md)把设备级杂项(系统设置/文件访问授权)集中到状态页尾「其他」区块(OtherSettings);2026-11-04(spec 2026-11-04-agent-peer-access-tab-design.md)把设备互访家族(PeerMCPSettings + 门禁占位卡)收编到 Agent 栏第 4 子 tab。用户复盘后拍板三步:

1. 「系统状态」「文件访问授权」回归仪表盘侧栏——设备本地状态就该首屏可见,埋在设置页尾不利于日常观察;侧栏形态选**紧凑状态卡 + 弹窗承载完整卡片**(用户在三个备选中拍板方案 A);
2. 「代理运行的模式」(tun/http)迁到代理设置 tab——它本质是代理运行方式,与代理开关同族;
3. 设备互访家族用户可见文案统一口径:跨设备执行/文件传输/设备互访(既定命名,2026-11-03 spec §4.2 与 2026-11-04 spec §4 一致,用户在需求里点名即口径锚点);英文族名三分裂归一 Device Access。

## 2. 目标

1. 仪表盘侧栏(桌面 aside + 移动端 data-mobile-panel)新增两张紧凑状态卡:**系统状态**/**文件访问授权**;点击按钮打开弹窗承载完整卡片,功能零裁剪。
2. 运行模式(tun/http 切换 + 状态条 + TUN 冲突弹窗 + wintun 安装流)迁到设置页›代理设置 tab 第一张卡,**移出登录门**(设备本地操作,与电源按钮同级)。
3. OtherSettings.vue(状态页尾「其他」区块)解散删除;状态页只剩 LogsSettings(实时日志)。
4. 设备互访问径统一:7 个 i18n 键改写(zh+en 同步)+ 2 行源码注释对齐(§5)。
5. SystemSettings.vue 拆一为二(SystemRunModeSettings / SystemStatusSettings),逻辑照搬零行为改动(§4.1)。

## 3. 非目标

- 门禁占位开关(peer_exec_enabled / peer_fetch_enabled)写通道启用——跨仓工作,另立批次(2026-11-03 spec §3 既有非目标)。
- Go 后端任何逻辑改动(口径「判定不改」清单见 §5.3,逐条记录理由)。
- 手机 App 端 UI。
- 侧栏「网络状态」卡与系统状态紧凑卡的证书状态重叠——有意保留两卡(定位不同:网络状态=连接观测,系统状态=服务与证书管理),不做合并。
- agent_fileAccess_desc「手机远程调用工具」措辞调整——手机 AI 会话工具调用场景,非设备互访门禁家族,不改(§5.3)。
- compat-anchors 清理——本次只随拆分按域搬移,不做删除(零消费者已核实,删除留待独立小修)。

## 4. 结构设计

### 4.1 SystemSettings.vue 拆一为二(旧文件删除)

现文件 1155 行,单卡承载四块:运行模式切换(:10-43)/模式状态条(:45-79)/软件状态·系统服务(:284-363,含确认弹窗 :186-282)/证书 CA(:365-392),外加 TUN 冲突弹窗(:81-184)与 compat-anchors(:394-425)。按卡片粒度拆分(2026-11-03 spec 组件=卡片先例):

**A. `app/website/src/components/settings/SystemRunModeSettings.vue`(新建,迁代理设置)**

- 承载:运行模式 tun/http 切换 + 模式状态条(后端/所选/运行中+刷新)+ TUN 冲突扫描弹窗(含分页)+ wintun 依赖安装轮询流 + run-mode 族 compat-anchors(runModeTun/runModeHttp/runStartBtn/runStopBtn/runModeBtn/runCurrentMode/runServiceStatus/runAvailableModes/runStatusInfo)
- `data-guide="run-mode"` 锚点保持在切换行(OnboardingGuide.vue:218 target 依赖)
- 方法照搬清单:selectMode / cancelTunSwitchConfirm / confirmTunSwitch / continueTunSwitchAfterDependencyReady / switchMode / refreshModeState / installWintunDependency / startWintunInstallPolling / stopWintunInstallPolling / pollWintunInstallState / fetchWintunDependencyStatus / fetchTunConflictScan / ensureWintunInstallProgressModal / updateWintunInstallProgressModal / formatWintunInstallError / dispatchTunProgressEvent / goToTunConflictPage / modeLabel / normalizeMode / requestJSON / clearMessages
- computed 照搬:backendMode / isRunning / modeStatus / backendModeLabel / selectedModeLabel / tunSwitchDialogTitle / tunSwitchDialogDescription / tunSwitchDialogWarning / tunConflictTotalPages / paginatedTunConflictInterfaces / tunConflictPaginationSummary / wintunDependency / showMissingWintunBanner / showInstallingWintunBanner
- watch 照搬:backendMode / tunConflictScan.interfaces / showTunSwitchConfirm
- 依赖:useRunStatus 模块级单例(模块 ref + refCount,多实例共享一个定时器,已核实)——照用,勿另起轮询
- 生命周期钩子分区(原 SystemSettings.vue:711-726 混管两路轮询,拆分后各归其主):mounted 只留 startRunStatePolling + refreshModeState(+wintun 安装中续接轮询);beforeUnmount 只留 stopRunStatePolling + stopWintunInstallPolling
- tun 进度事件 `aliang:tun-progress-*` 为 window 级(DashboardPage.vue:2217-2241 全局监听),dispatch 源换位置不受影响

**B. `app/website/src/components/settings/SystemStatusSettings.vue`(新建,进侧栏弹窗)**

- 承载:「软件状态」区块(系统服务注册/卸载 + 确认弹窗 + 信息栅格)+「证书 CA」区块(信任态/明细/导出/安装/管理)+ cert 族 compat-anchors(openCertManagementModalBtn / btn-install-cert / btn-export-cert / btn-remove-cert / btn-download-cert / btn-check-cert / cert-status 等 :404-424 全部)
- 方法照搬:refreshSystemServiceStatus / registerSystemService / uninstallSystemService / openSystemServiceConfirm / closeSystemServiceConfirm / confirmSystemServiceAction / clearSystemServiceMessages / downloadCurrentCertificate / openCertificateModal
- computed 照搬:showServicePrivilegeHint / systemScopeServiceBlocked / systemServiceScopeLabel / systemServiceTitle / systemServiceDescription / systemServiceBadge / friendlyPlatformLabel / friendlyServiceLabel / serviceBadgeClass / serviceCardClass / systemServiceConfirmTitle / systemServiceConfirmDescription / systemServiceConfirmHighlight / systemServiceConfirmChecklist / systemServiceConfirmButtonLabel
- 依赖:useCertStatus 模块级单例(3s 轮询至 trusted+installed,refCount 共享,已核实)——照用
- 生命周期钩子分区:mounted 只留 startCertStatusPolling + refreshSystemServiceStatus;beforeUnmount 只留 stopCertStatusPolling
- cert「安装/管理」按钮继续走 `aliang:open-cert-modal` window 事件(App.vue:61 全局监听 CertManagementModal,零改动)

**拆分安全性**:两卡唯一的概念交互(TUN 模式↔系统服务)只是各自调本地 API,组件层无互相引用,拆分无耦合断裂。

### 4.2 侧栏两张紧凑卡(新建 `app/website/src/components/dashboard/`)

**A. `SystemStatusSidebarCard.vue`**

- 展示:服务状态点四态(运行中/已注册未运行/未注册/不支持,复用 sys_running / sys_badgeRegistered / sys_badgeNotRegistered / sys_badgeNotSupported)+ 证书信任态(复用 sys_trusted / cert_notTrusted)
- 数据:服务状态 GET /api/system/service/status(mount 取一次 + 弹窗关闭刷新一次);证书态走 useCertStatus 单例
- 按钮「管理…」(新键 dash_systemManage)→ 打开内嵌弹窗(max-w 约 560px;CertManagementModal 同款壳:fixed overlay + backdrop 点击关 + max-h 滚动体),内容=SystemStatusSettings 原卡
- 无登录门(设备本地 API)

**B. `FileAccessSidebarCard.vue`**

- 标题复用既有 agent_fileAccess_title(「文件访问授权」),零新键
- 展示:授权进度「x/y 类别已授权」(复用 agent_fileAccess_summary 参数化文案 granted/denied/absent/total)+ denied>0 时琥珀警示点
- 数据:getAgentFileAccess()(mount 取一次 + 弹窗关闭刷新;本地 agent 接口无登录)
- 按钮「查看 / 申请…」(新键 dash_fileAccessManage)→ 同款弹窗承载 AgentFileAccessSettings 原卡(该卡自取数据自轮询零 props,AgentFileAccessSettings.test.js 零改动)

**挂载位置**:
- 桌面:DashboardPage.vue aside 内,网络状态块(data-guide="cert",:102-148)之后、快捷工具区(:149)之前
- 移动:DashboardPage.vue data-mobile-panel 网络状态块之后
- 两实例同屏共存(桌面实例在 hidden md:flex 父容器、移动实例在 md:hidden 父容器)——useCertStatus 单例共享轮询无双重开销;服务/文件访问各自 mount 一次本地 GET,成本低可接受

**弹窗壳不抽公共组件**:本仓先例(CertManagementModal / QuickSetup 系列)各自内联 overlay 标记,两卡各自内联约 30 行弹窗壳,不引入新抽象(YAGNI)。

### 4.3 代理设置 tab 挂载

SettingsPage.vue ①代理设置 section(:130-163):

- SystemRunModeSettings 挂在**登录占位之前**(section 顶部,所有用户可见)
- 登录占位与 ModelMappingSettings / RulesSettings 结构不动
- 理由:/api/run/swift 与电源按钮同级是设备本地操作,不该被登录挡

### 4.4 OtherSettings 解散

- 删 `app/website/src/components/settings/OtherSettings.vue`
- SettingsPage.vue:状态 section(:188-212)删 OtherSettings 挂载(:203-210)与 import/components 注册;该 section 的 @save 透传随之消失(两卡均为自足式 API 调用,不经 save 链;PeerMCPSettings 已于 2026-11-04 迁走);:244 附近「设备级杂项收拢在 OtherSettings.vue」注释同步更新(组件将不存在)
- 状态 section 收敛为 SectionHeader + 登录占位 + LogsSettings
- `settings_otherTitle` / `settings_otherDesc` 键:删组件后 grep 确认零引用再删(键清理纪律)

### 4.5 引导跳转落点

DashboardPage.vue handleGuideOpenSettings(:1706-1713):`showPage('log')` → `showPage('settings')`;滚动锚点 `[data-guide="run-mode"]` 不变(随卡迁移,代理设置页内有锚点)。注释同步改写(记录第二次随迁)。

## 5. 口径统一清单(审计驱动)

### 5.1 改(7 键,zh+en 同步;只改键值不改键名,零引用断裂)

| # | 键(锚点) | 现值 | 改为 |
|---|---|---|---|
| 1 | peerMcp_description(zh:603 / en:602) | 「跨设备工具(列出设备、**拉取文件**、**远程执行**)」/ "cross-device tools (list devices, **fetch files**, **run remotely**)" | 「设备互访工具(列出设备、**文件传输**、**跨设备执行**)」/ "device access tools (list devices, **file transfer**, **cross-device execution**)" |
| 2 | peerMcp_enable(zh:605 / en:604) | 「**跨设备工具面**」/ "Enable the **cross-device tool surface**" | 「**设备互访**」/ "**Enable device access**" |
| 3 | peerMcp_subAgentcore(zh:615 / en:613) | 「aliangcode **工具面**」/ "aliangcode **tool face**" | 「aliangcode **工具注入**」/ "aliangcode **tool injection**" |
| 4 | peerMcp_subAgentcoreDesc(zh:616 / en:614) | 「**跨设备工具**注入(设备互访五件套)」/ "**Cross-device tool** injection (peer five-piece set)" | 「**设备互访工具**注入(五件套)」/ "**Device access tool** injection (peer five-piece set)" |
| 5 | peerMcp_title(en:601) | "**Device Mesh** (Aliang Peer)" | "**Device Access** (Aliang Peer)"(zh:602「设备互访 (Aliang Peer)」不动) |
| 6 | settings_sectionAgentDesc(en:574) | "**device mesh** settings" | "**device access** settings" |
| 7 | settings_peerTransferPendingDesc(zh:589 / en:592) | 「允许跨设备**拉取**本机文件的设备级门禁」/ "letting other devices **fetch files** here" | 「允许其他设备发起**文件传输**、拉取本机文件的设备级门禁」/ "gate letting other devices **start file transfers** to pull files from this machine" |

英文族名三分裂(agent_subPeerAccess="Device Access" / peerMcp_title="Device Mesh" / settings_sectionAgentDesc="device mesh")统一为 **Device Access**(与 zh「设备互访」、组件名 PeerAccessSettings、键 peer-access 对齐);agent_subPeerAccess en:583 本身已正确不动。

### 5.2 顺带卫生(源码注释,非用户可见)

- zh.js:601 `// 设备互访(aliang-peer 跨设备工具面,能力接入)` → `// 设备互访(aliang-peer,能力接入)`
- en.js:600 同构注释同改
- PeerMCPSettings.vue:2-8 组件头注释「跨设备工具面」同口径对齐(可选)

### 5.3 判定不改(审计确认后逐条记录理由)

| 项 | 理由 |
|---|---|
| agent_fileAccess_desc「手机远程调用工具」(zh:1273 / en:1270) | 手机 AI 会话工具调用场景,非设备互访门禁家族 |
| agent_service.go:1520-1521「远程执行尚未支持」 | 指平台远程驱动 CLI(AI run 通道),与跨设备执行异族 |
| agent_channel_health.go:226「文件传输等功能暂不可用」 | 已是口径,不改 |
| agent_tool_registry.go / agent_peer_tools.go:985 / agent_ai_tool_ask_peer.go:60 英文工具描述 | 面向内核的英文文案,不受中文口径约束,且用词已正确(peer exec / peer fetch) |
| tray/companion_watchdog.go:117「远程设备」/ auth_runtime_hooks.go:124「远程访问」 | 通用远程概念,非家族术语 |
| agent_ai.go:8904 "remote execution" | 审批校验错误消息,与 peer 家族异族 |
| docs/superpowers/ 历史文档与计划 | 历史记录不改写 |
| chat-device-sessions.spec.js「设备会话/设备壳」 | 聊天设备会话特性,与 peer 家族无关(审计驳回项) |

## 6. i18n 键增删(zh/en 两语同步)

新增:
- dash_systemStatusTitle: '系统状态' / 'System Status'(紧凑卡标题 + 弹窗标题)
- dash_systemManage: '管理…' / 'Manage…'
- dash_fileAccessManage: '查看 / 申请…' / 'View / Request…'

复用不新增:
- 服务态:sys_running / sys_badgeRegistered / sys_badgeNotRegistered / sys_badgeNotSupported
- 证书态:sys_trusted / cert_notTrusted
- 授权进度:agent_fileAccess_summary(参数化 granted/denied/absent/total)

删除(grep 零引用后):settings_otherTitle / settings_otherDesc

键名纪律:仪表盘新键走 dash_ 前缀(仪表盘命名空间);口径改动全部只改键值不改键名(零引用断裂风险)。

## 7. 测试与验收

测试:
- `npx vitest run` 全绿(AgentFileAccessSettings.test.js / PeerAccessSettings.test.js / useNavigation.test.js 组件与词表均不动,应保持绿)
- `npx playwright test tests/e2e/mobile-responsive.spec.js`(390/768/1280)绿——新紧凑卡 flex 布局过 390px 溢出哨兵
- 手动冒烟清单:
  1. 侧栏两张紧凑卡出现,状态点与设置页原卡数据一致
  2. 「管理…」弹窗:服务注册/卸载全流程 + 证书导出/安装跳 CertManagementModal 正常;弹窗关闭后紧凑卡状态刷新
  3. 「查看 / 申请…」弹窗:申请授权→轮询→类别状态更新全链正常(与原状态页行为一致)
  4. 代理设置 tab:运行模式切换 tun/http 全链(TUN 冲突弹窗 / wintun 流按平台)+ 未登录可见可切换
  5. 状态页:只剩实时日志,无「其他」区块
  6. 仪表盘引导 run-mode 步骤:跳转落代理设置页并滚动到切换行
  7. 设备互访子 tab:PeerMCP 卡新文案(设备互访工具 / 启用设备互访 / 工具注入)渲染正确,中英两语切换
  8. 移动端面板:两张紧凑卡出现且可用

验收红线:
- 拆分零行为改动:运行模式全链与服务/证书全链逐项对照原 SystemSettings 行为
- 口径改动仅 i18n 键值,无键名变更,无组件引用断裂
- grep 断言:OtherSettings.vue 删除后全仓零引用;settings_other* 键零引用后删

## 8. 风险与教训锚点

- **拆分漏搬**:两个新卡按 §4.1 逐方法清单照搬,搬完 grep 原文件已删无残留;两卡唯一交互只是同调本地 API,无组件耦合
- **i18n 键误删**:删键先 grep(2026-11-04 spec 键清理纪律);peerMcp_state_error「防御性保留」先例——settings_other* 若发现防御性用途则注释保留不删
- **useRunStatus / useCertStatus 单例**:拆分后多组件共用,refCount 机制已核实共享单定时器,勿各自另起轮询
- **390px 溢出哨兵**:紧凑卡内行布局用 min-w-0 + truncate(侧栏既有先例)
- **4174 旧 dev server 复用陷阱**:冒烟前确认端口(2026-11-04 spec 教训)
- **worktree 流程**:开发一律 worktree 切分支,不直接改 master 工作区(项目约定)
- **提交纪律**:spec 文档与实现均不自动 commit,待用户明示(项目 CLAUDE.md)
