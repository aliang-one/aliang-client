# 设计:设置页四栏重组——代理设置/Agent/状态/用户 + Agent 三子 tab

- 日期:2026-11-03
- 状态:设计定稿(brainstorming 产出,用户已批 §1 骨架与 §2 技术设计,"直接做完")
- 仓:alianggate(app/website 纯前端)
- 基线:master@e42661f

## 1. 背景与动机

用户反馈当前设置页(5 tab:模型与推理/账户与界面/诊断/能力接入/设备与网络)太乱:aliangcode 的 MCP/技能在「能力接入」,用户态 Agent 的设备网络在「设备与网络」,会话记录在「诊断」,找东西要来回跳。要求恢复「设置/用户中心/状态」三栏心智,并把 Agent 相关内容收拢进一个 Agent 栏目。

## 2. 目标

1. 顶部导航收敛为四栏,顺序固定:**代理设置 → Agent → 状态 → 用户**。
2. Agent 为独立页,含三个子 tab:**记录**(各 coding agent 会话 + 远程终端,分页)/**用户态 agent**(注册与设备网络)/ **aliangcode**(LLM/MCP/技能/环境变量)。
3. 设备级杂项(系统设置/文件访问授权/跨设备执行/文件传输)集中到「状态」页尾「其他」区块。
4. 旧「能力接入」tab 解散,组件全部重新归位,无孤儿组件。

## 3. 非目标

- 后端改动:跨设备执行(`peer_exec_enabled`)/文件传输(`peer_fetch_enabled`)开关的写通道属跨仓工作(本仓 agent + PhoneServer),**下一轮做**;本轮前端置灰占位(决策 B)。
- 终端历史持久化(决策 A:终端「记录」= 实时在连列表,零后端改动)。
- 全面 hash 路由化(方案 3 被否,YAGNI)。
- 移动端 App / 手机端 UI(本设计只覆盖仪表盘 website)。
- 基线说明:peer-mcp-switch 线已于 master@09acab4/e42661f 合入「设备互访 (Aliang Peer)」平台托管卡(PeerMCPSettings,customer.peer_mcp 总开关,管三内核工具注入层)。本设计的置灰占位开关是**设备门禁层**(服务端设备注册表 `peer_exec_enabled`/`peer_fetch_enabled`)。两层真值源不同、互不依赖;本设计只决定 PeerMCPSettings 卡片的**展示落位**(→ 状态›其他,见 §4.2),不改其行为。

## 4. 新信息架构(IA)

### 4.1 四栏(显示文案中/英,内部 key 不变)

| 顺序 | 显示(zh/en) | 内部 key | 原归属 |
|---|---|---|---|
| 1 | 代理设置 / Proxy | `settings` | 原「模型与推理」改名 |
| 2 | Agent / Agent | `agent` | 原「设备与网络」重组为子 tab 页 |
| 3 | 状态 / Status | `log` | 原「诊断」改名 |
| 4 | 用户 / User | `user` | 原「账户与界面」改名 |
| — | (删除) | `capability` | 「能力接入」解散,组件迁移 |

### 4.2 各栏内容

**① 代理设置**
- ModelMappingSettings(模型映射,自原「模型与推理」)
- RulesSettings(代理开关 + AI 规则 + 分流规则,自原「设备与网络」)

**② Agent(新独立组件 AgentPage,三子 tab)**

| 子 tab | key | 组件 | 迁移来源 |
|---|---|---|---|
| 记录 | `records`(默认) | AgentActivitySettings + AgentRuntimeSessions | 原「诊断」 |
| 用户态 agent | `user-agent` | AgentSettings(卡片+设置弹窗) + AgentScanDirectoriesSettings + AgentIntegrationsSettings | 原「设备与网络」×2 + 原「能力接入」 |
| aliangcode | `aliangcode` | AgentCoreSettings(LLM) + AgentMCPGlobalSettings + AgentSkillsGlobalSettings + CustomEnvSettings | 原「模型与推理」×1 + 原「能力接入」×3 |

**③ 状态**
- LogsSettings(实时日志,原样)
- 「其他」页尾区块(新 OtherSettings.vue):
  - PeerMCPSettings(设备互访 Aliang Peer 总开关,自原「能力接入」;行为零改动,仅落位)
  - SystemSettings(运行模式/系统服务/证书,自原「设备与网络」)
  - AgentFileAccessSettings(文件访问授权,自原「设备与网络」)
  - 🆕 跨设备执行开关(置灰占位)+ 🆕 文件传输开关(置灰占位)——与 PeerMCP 卡片构成设备互访家族集中区

**④ 用户**
- UserInfoSettings + AppearanceSettings(不变)

### 4.3 记录子 tab 的 agent 类型筛选

AgentActivitySettings 现有「仅 aliangcode」单一开关升级为类型下拉筛选(**全部/claude/codex/aliangcode/opencode**),按 `tool||provider` 名匹配;aliangcode 项走 `isAliangCodeSession` 同源判据,未知来源仅在「全部」可见(有意不留「其他」档)。默认「全部」(记录页定位为各 coding agent 全量视角;aliangcode 已有独立子 tab)。分页复用现有客户端分页;`scopedSessions/scopedRuntimeAi` 计算属性名保持不变,消费方 decoratedSessions 零改动。远程终端区块保持实时语义。

## 5. 技术设计

### 5.1 组件结构

- 新增 `app/website/src/components/settings/agent/AgentPage.vue`:
  - props 转传 customerConfig 数据流(沿用 SettingsPage 统一装载/保存、子组件纯受控的现有模式,子组件零改动)
  - 子 tab 药丸样式沿用现有 badge 风格;移动端可换行
- 新增 `app/website/src/components/settings/OtherSettings.vue`:状态页尾区块容器,内含 PeerMCPSettings/SystemSettings/AgentFileAccessSettings 原样迁入 + 两个置灰开关(纯前端,disabled + 「即将上线」徽标,零请求);customerConfig 数据流经状态 section 透传进 PeerMCPSettings
- SettingsPage.vue 瘦身为四个薄 section;`isVisible` 去掉 `capability`

### 5.2 导航层(useNavigation.js)

- 新增 `agentSubTab` ref(默认 `records`);`showPage(page, { sub })` 选填参数写入,未知值 fallback `records`
- SettingsPage watch/深链:`showPage('agent', { sub: 'aliangcode' })` 可定向
- 仪表盘引导跳转更新:`handleGuideOpenSettings`(DashboardPage.vue:1705-1713)从「showAgent()+找 run-mode 锚点」改为「showPage('log')+找 run-mode 锚点」;`data-guide="run-mode"` 随 SystemSettings 迁至状态页,迁移后 grep 断言全局唯一
- compat anchors(rules/logs/userinfo/system/config-sync)保留;watcher 本就无 capability 分支,无需处理(capability 引用实际在 isVisible/pageTitle/两处 tab 按钮/section v-else-if,见 §5.1 与实现清单)

### 5.3 i18n(zh/en)

- 改名:`settings_tabSettings`→代理设置/Proxy、`settings_tabUserCenter`→用户/User、`settings_tabLogs`→状态/Status、`settings_tabAgent`→Agent/Agent
- 新增:子 tab 三键(agent_subRecords/agent_subUserAgent/agent_subAliangCode)、其他区块键(settings_otherTitle 等)、置灰开关「即将上线」键
- 删除(已 grep 确认零引用):`settings_tabCapability`、`settings_sectionCapabilityDesc`、`settings_pageUser`、`agent_activity_onlyAliangCode(+Hint)`、旧三分区描述键
- **复用不删**:`settings_loginRequiredCapability(+Desc)` → aliangcode 子 tab 登录占位(AgentPage 内)
- `pageTitle` 计算同步四栏
- ⚠️ `settings_tabAgent` 与 562-564 区块**不同址**(631 行附近),改名勿漏(e2e 曾抓到此漏改)

### 5.4 登录占位语义

- 代理设置、状态页:未登录卡片(现状同款)
- Agent 页:**不设区级占位**,占位收在 aliangcode 子 tab 内(AgentPage 判 isAuthenticated);记录与用户态 agent 子 tab 未登录可直接看(用户态 agent 的注册状态展示、记录的 offline/空态逻辑各自兜底)

## 6. 错误处理/边界

- 子 tab 深链未知值 → fallback `records`
- runtime-sessions 拉取失败 → 仅缺「进行中」徽章(现有语义)
- sessions 空 → 运行时离线态(现有)
- `run-mode` 引导锚点唯一性:实现时 grep `[data-guide="run-mode"]` 断言仅 SystemSettings 一处

## 7. 测试

- vitest:AgentPage 子 tab 切换 + 未知 sub fallback;agent 类型筛选过滤逻辑
- e2e:mobile-responsive.spec.js 哨兵(390/768/1280)**必须同步改断言**——改名后 Agent 页 h1 变为「Agent」,以下四处 `/设备与网络|Device & Network/` 断言改为 `/Agent/`:行 104/113(mobile Agent 入口用例)、行 177/182(desktop 用例);行 86 网格计数断言 `toBeGreaterThanOrEqual(5)` 改为 `toBe(4)`
- 手动冒烟:四栏切换、子 tab 深链、仪表盘引导跳转落点(状态页 run-mode)、未登录占位、记录筛选

## 8. 交付物与验收

1. 新 IA 四栏 + Agent 三子 tab 全量可用,组件零功能回归
2. 仪表盘引导跳转落点正确
3. vitest + e2e mobile-responsive 绿
4. 不含任何后端改动;两个置灰开关零请求
