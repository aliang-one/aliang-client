# 仪表盘移动端适配设计

日期：2026-10-08
状态：已确认（用户批准紧凑顶栏+可折叠方案）

## 背景与目标

ALiang Gateway Dashboard（`app/website`，Vue 3 + Tailwind 3，构建产物经 `app/embed.go` 嵌入 Go 二进制）目前是纯桌面布局：DashboardPage 固定 `w-80 lg:w-96` 控制面板侧栏 + 主内容区，全站 ~14.6k 行组件中响应式断点类只有个位数。

**目标**：手机（≥360px 宽）上完整可用；**≥768px（Tailwind `md` 断点）桌面布局零变化**。

**非目标**：平板专属布局、触摸手势、PWA、ChatPage（已临时隐藏，不顺手改动）。

## 技术策略

- 以 Tailwind `md:` 前缀为唯一切换点；移动端样式写在 base，桌面样式靠追加 `md:` 恢复，**不删除已有桌面类**。
- 纯 CSS 响应式（`hidden md:flex` / `md:hidden`）为主；JS 只承担折叠面板开合状态（一个 ref），不做 viewport 检测、不复制组件。
- 弹窗统一 `mx-4 max-h-[90vh] overflow-y-auto` 约束。

## 改动明细

### 1. DashboardPage.vue（主要工作量）

移动端新增结构（`md:hidden`），桌面结构（`hidden md:flex` 的 aside + 原 header）不动：

```
┌──────────────────────────┐
│ ⏻ 运行中·SOCKS5   [张三] │ ← 紧凑顶栏：迷你电源钮+状态文字+账户入口
│ ▾ 网络状态 · 快捷工具 · 余额 │ ← 折叠面板头（v-show + mobilePanelOpen ref，默认收起）
├──────────────────────────┤
│ 主内容区（立即可见）        │
└──────────────────────────┘
```

- 原 `<aside>` 内容（账户卡、电源、网络状态/证书、快捷工具、余额充值）**复用**进可折叠面板：抽出共享的模板片段或直接在移动端结构里复用同一批 computed/方法，行为（点击登录、安装证书、充值）与桌面完全一致。
- 账户卡在移动端的点击登录逻辑保留。
- 原 header（h-16）移动端瘦身：server link 组件 `min-w-[210px]` 三列信息改紧凑展示（隐藏次要字段/明细行），`px-8` → `px-4 md:px-8`（追加式，非替换），刷新与帮助按钮保留且触控目标 ≥40px。
- 侧栏内容复用方式：**不抽取共享模板片段**，移动端结构直接复用页面级 computed/方法（登录弹窗状态 `isLoginModalOpen` 已在页面级，无需重构）。

**主内容区**：
- 容器 `p-8` → `px-4 py-5 md:p-8`；图表卡 `p-6` → `p-4 md:p-6`。
- 模型用量分布 / 用量趋势卡（已是 `grid-cols-1 lg:grid-cols-2`）：检查内部条形图/SVG 无固定像素宽度导致横向溢出，必要时改百分比宽度。
- 近期用量记录：行内多列在移动端改为堆叠卡片式（或最低成本：允许横向滚动容器），防撑破视口。

### 2. 弹窗统一适配

QuickSetupModal、CertManagementModal、TutorialDocsModal、登录弹窗（DashboardPage 内）：`max-w-*` 追加 `mx-4`、`max-h-[90vh]`、内容区 `overflow-y-auto`；底部按钮区在移动端可换行。同属全屏覆盖层的还有 OnboardingGuide（`fixed inset-0 z-[200]`）与 SoftwareUpdateNotice（`fixed inset-0`，内层 `max-w-2xl`，外层已有 `p-4` 约束），一并纳入走查清单。

### 3. SettingsPage 及子页微调

已有基础适配（`md:hidden` 双列标签格、`grid-cols-1 lg:grid-cols-12`、`p-4 sm:p-6 lg:p-8`），本次只补查残留问题：SystemSettings、UserInfoSettings、LogsSettings、AgentSettings 等子页中的固定宽度容器、多列统计格、CodeMirror 编辑器容器、表格。

### 4. 全局

- `index.html` 已有 `viewport` meta，不动。
- 排查全站 `overflow-x` 溢出源（长 token、长 URL、CodeMirror）。
- 主操作按钮触控目标 ≥40px（不破坏现有桌面尺寸）。

## 验证方式

1. `npm run build` 成功；`vitest run` 全绿（本仓已知 3 个 go test 子进程超时 flake 与本工作无关）。
2. Playwright（项目已有配置）以 **390×844** viewport 对仪表盘、设置各子页、全部弹窗截图走查 + dark mode 抽查；核对：无横向滚动、折叠面板展开/收起正常、登录/电源/充值等行为可用、console 无新增错误。
3. **1280px 桌面截图对比改动前**：布局零回归。

## 风险与对策

- DashboardPage.vue 2398 行单文件，改动全部用精确 Edit，追加式修改；每完成一个区块跑一次 build 快速验证。
- 共享 computed/方法已在 setup/computed 区域集中，移动端结构直接复用，不复制业务逻辑。
- e2e（Playwright tests/）若依赖桌面 DOM 结构，改动后需跑 `test:e2e` 确认；仅移动端新增节点用 `md:hidden` 不影响桌面选择器。
