# 设计：Agent 设置新增「设备互访」第 4 子 tab + 子 tab 样式改 navitem 下划线式

日期：2026-11-04
状态：已与用户对齐（收编范围=全家收编、命名=设备互访、方案=A、样式=navitem 下划线式；OtherSettings 处置=瘦身保留）
批次性质：纯前端 UI（app/website），不动 Go 后端
评审记录：第 1 轮评审发现 OtherSettings.vue 实际挂 5 项（另有 SystemSettings / AgentFileAccessSettings 两个活跃设置面，全仓无其他挂载点），「文件删除」原设计会静默砍掉两个功能；已按用户拍板改为「瘦身保留」。

## 1. 背景与问题

2026-11-04 的 9-agent 审计（见记忆 `peer-link-support-audit-2026-11-04`）确认：跨设备 peer 能力（peer_exec / peer_fetch_file / device_* / 总开关 customer.peer_mcp）客户端已真实落地，但「设备互访」相关设置 UI 分散且埋没：

- `PeerMCPSettings.vue`（总开关 + 双内核注册状态 + 两段式保存）挂在**状态栏页尾「其他」区块**（`OtherSettings.vue:5`），不在 Agent 栏；
- 设备门禁占位卡（一张卡内含两行开关：跨设备执行 `peer_exec_enabled` / 文件传输 `peer_fetch_enabled`，disabled + coming soon，写通道待跨仓）同在 OtherSettings（:17-58）；
- Agent 栏现有三子 tab（记录/用户态agent/aliangcode）为 pill 圆角胶囊实底风格，与设置页顶导航的下划线式（navitem）风格不一致。

用户诉求：① 在 Agent 栏新增第 4 子 tab（与记录/用户态agent/aliangcode 并列），收编全部设备互访设置；② 子 tab 样式改走 navitem 标签风格。

## 2. 目标

1. Agent 栏新增第 4 子 tab「设备互访」（键 `peer-access`），收编 PeerMCPSettings 卡 + 门禁占位卡（一张卡内含两行开关），成为跨设备设置的唯一入口。
2. 四个子 tab 全部改为 navitem 下划线式（对齐顶导航先例 `SettingsPage.vue:26-28`）。
3. `OtherSettings.vue` **瘦身保留**：仅迁走 PeerMCPSettings 挂载与门禁占位卡；SystemSettings（运行模式 TUN/系统代理）与 AgentFileAccessSettings（macOS 隐私目录预授权）两卡**原地不动**（全仓无其他挂载点，删除会静默砍功能）。
4. 行为零改动：门禁占位卡仍 disabled + coming soon；PeerMCPSettings 两段式保存链路不变；SystemSettings / AgentFileAccessSettings 挂载与行为不变。

## 3. 非目标（另立批次）

- 三个后端缺口：D1 桥/注册表不受 customer.peer_mcp 门控、`peer.exec.result`/`transfer.progress` 消费臂、peer_fetch 设备开关写通道。
- 文件收件箱/传输列表视图。
- 门禁占位卡的启用（依赖 PhoneServer 写通道）。
- Go 后端任何改动。

## 4. 结构设计（方案 A）

新建 `app/website/src/components/settings/agent/PeerAccessSettings.vue` 收编两卡：

- 复用 `settings/PeerMCPSettings.vue`（组件文件不动），props 透传形状保持 `config/loading/saving/error/successMessage + @save`；
- 门禁占位卡（**一张卡内含两行开关**：跨设备执行 / 文件传输）结构原样从 `OtherSettings.vue:17-58` 迁入（i18n 键复用现有 `settings_peerExecPendingTitle/Desc`、`settings_peerTransferPendingTitle/Desc`，zh.js:578-585）。

挂载链变为：`SettingsPage.vue`（customerConfig + saveCustomerConfig）→ `AgentPage.vue` → `PeerAccessSettings.vue` → `PeerMCPSettings.vue` + 门禁占位卡。AgentPage 若尚无相应 props，需在迁移时把 `config` 与 `@save` 透传进来（保持 OtherSettings 原有的透传形状，见 OtherSettings.vue:4-14 现状锚点）。

`OtherSettings.vue` 瘦身：删除 PeerMCPSettings 挂载（:5）与门禁占位卡（:17-58）及其相关 import；保留 SectionHeader（「其他」标题）、SystemSettings（:13）、AgentFileAccessSettings（:14）三个挂载与其 import；`SettingsPage.vue` 的挂载（:203-210）、import（:250-251）与 components 注册（:444）**均不动**。`settings_otherTitle`/`settings_otherDesc` 键继续使用，不清。

## 5. 样式设计（navitem 下划线式）

改动点仅 `AgentPage.vue:5-20` 一处（全仓唯一 tablist）：

| 部分 | 现状（pill） | 目标（navitem） |
|---|---|---|
| 容器（:5） | `flex flex-wrap gap-2` | `flex flex-wrap items-center gap-4 border-b border-slate-200 dark:border-slate-700` |
| 按钮基类（:12） | `min-h-10 rounded-full px-4 text-xs font-bold transition` | `min-h-10 px-1 py-2 text-sm font-medium transition` |
| 激活（:14） | `bg-primary text-white shadow-sm` | `border-b-2 border-primary text-primary` |
| 非激活（:15） | `bg-slate-100 text-slate-600 hover:bg-slate-200 dark:…` | `border-b-2 border-transparent text-slate-500 hover:text-slate-700 dark:text-slate-300 dark:hover:text-slate-100` |

要点：
- **保留 `min-h-10`** 触控目标（移动端适配约定 + e2e 哨兵依赖）。
- **保留 `flex flex-wrap`**（390px 溢出哨兵 `app/website/tests/e2e/mobile-responsive.spec.js:77-93` 靠它兜底）。
- 非激活垫 `border-b-2 border-transparent`，避免切换时 2px 高度跳动（顶导航先例未垫，但它有容器底线掩护；子 tab 自带容器底线后同样成立）。
- 结构保留：`role=tablist` / `role=tab` + `:aria-selected` / `switchTab` 直写导航层 ref（setup 解包陷阱注释）/ `active` computed 未知键兜底回 records，全部不动。

## 6. 第 4 子 tab 的四处同步面

漏改任一词表会被 `normalizeAgentSubTab`（useNavigation.js:17-19）**静默归一回 records 不报错**——这是首要回归风险，四处必须一次改齐：

1. `app/website/src/composables/useNavigation.js:14`：`AGENT_SUB_TABS = ['records','user-agent','aliangcode','peer-access']`（唯一真值源）。
2. `app/website/src/composables/useNavigation.test.js:86-87`：钉死断言改为四键；并为 `agentSubTab='peer-access'` 补归一/深链用例。
3. `app/website/src/components/settings/agent/AgentPage.vue:104-108`：词表副本加 `{ key:'peer-access', labelKey:'agent_subPeerAccess' }`；新增 `v-else-if="active==='peer-access'"` 渲染块挂 PeerAccessSettings。
4. i18n 两语：`zh.js` / `en.js` 加 `agent_subPeerAccess: '设备互访' / 'Device Access'`。

命名纪律：新键走 `agent_sub*` 前缀（子 tab 命名空间），**勿与顶导航 `settings_tab*` 混址**（tabAgent 键不同址是已知教训）。

## 7. 测试与验收

测试：
- `npx vitest run` 全绿：含更新后的 useNavigation 断言、新增 `PeerAccessSettings.test.js`（最小组件测试：渲染 PeerMCPSettings 卡 + 门禁占位卡，占位卡两行开关均 disabled + comingSoon 徽章）。
- `npx playwright test tests/e2e/mobile-responsive.spec.js`（路径 `app/website/tests/e2e/mobile-responsive.spec.js`，设置子页溢出哨兵 :77-93，390/768/1280）绿。

验收清单：
1. Agent 栏出现第 4 子 tab「设备互访」，四 tab 并列且可切换；深链 `showPage('agent',{sub:'peer-access'})` 可用；非法 sub 仍归一 records。
2. 新 tab 内卡片齐全：总开关可切换、两段式保存可用（持久化 + POST /api/agent/peer/mcp 两步独立反馈）；门禁占位卡两行开关 disabled + comingSoon，行为与迁移前一致。
3. 状态栏页尾「其他」区块保留且只含 SystemSettings + AgentFileAccessSettings 两卡；OtherSettings.vue 内不再有 PeerMCPSettings/门禁卡挂载与 import（grep 零残留）。
4. 四个子 tab 均为下划线式；390px 无横向溢出；触控目标 ≥40px。
5. i18n 两语齐全且键名符合第 6 节纪律；`settings_peer*Pending*` 键迁移后仍被引用，不清键。

## 8. 风险与教训锚点

- 词表漏改 → 静默归回 records（见第 6 节）。
- `PeerMCPSettings` 两段式保存第二步失败不掩盖第一步（红显 applyError）——迁移时不得改变该行为。
- 构建验证注意 4174 旧 dev server 复用陷阱（旧 server 会供应旧构建，冒烟前先确认端口）。
- 开发流程：worktree 切分支，不直接改 master 工作区（项目约定）。
- AgentPage 对 props 的现有接口以实现时实读为准（本 spec 只锁定透传形状，不锁定 AgentPage 现有 props 细节）。
