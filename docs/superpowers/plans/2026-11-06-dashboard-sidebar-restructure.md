# 仪表盘侧栏收纳+运行模式迁移+设备互访问径统一 实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 按 spec `docs/superpowers/specs/2026-11-05-dashboard-sidebar-restructure-design.md` 完成:①系统状态/文件访问授权收纳仪表盘侧栏(紧凑卡+弹窗);②运行模式迁代理设置 tab 并移出登录门;③OtherSettings 解散;④设备互访 7 键口径统一。

**Architecture:** SystemSettings.vue 拆一为二(RunMode/Status,逻辑照搬零行为改动);两张新紧凑卡自取数据、内联弹窗承载完整卡片;口径统一仅改 i18n 键值。

**Tech Stack:** Vue 3(options API)+ Tailwind + vitest + Playwright;纯前端 app/website。

**⚠️ 提交纪律(项目 CLAUDE.md 覆盖技能模板):** 全程 **不执行 `git commit`** ——所有改动留在 worktree 工作区,完成后向用户汇报 diff 概要,commit/push 均待用户明示。计划中因此没有 commit 步骤,以「grep 断言 + 测试绿」替代每任务的落盘检查点。

**基线:** master@d4ed54a;worktree 分支 `feat/dashboard-sidebar-restructure`。

---

## 文件结构总览

| 文件 | 动作 | 职责 |
|---|---|---|
| `app/website/src/components/settings/SystemRunModeSettings.vue` | 新建 | 运行模式卡(tun/http+状态条+TUN 冲突弹窗+wintun 流) |
| `app/website/src/components/settings/SystemStatusSettings.vue` | 新建 | 系统状态卡(系统服务+证书 CA) |
| `app/website/src/components/dashboard/SystemStatusSidebarCard.vue` | 新建 | 侧栏紧凑卡+弹窗承载 SystemStatusSettings |
| `app/website/src/components/dashboard/FileAccessSidebarCard.vue` | 新建 | 侧栏紧凑卡+弹窗承载 AgentFileAccessSettings |
| `app/website/src/components/dashboard/SystemStatusSidebarCard.test.js` | 新建 | 紧凑卡渲染+弹窗行为最小测试 |
| `app/website/src/components/dashboard/FileAccessSidebarCard.test.js` | 新建 | 同上 |
| `app/website/src/components/settings/SystemSettings.vue` | 删除 | 拆分后旧单体 |
| `app/website/src/components/settings/OtherSettings.vue` | 删除 | 状态页尾「其他」容器解散 |
| `app/website/src/components/SettingsPage.vue` | 修改 | 挂载运行模式卡/删 OtherSettings/注释更新 |
| `app/website/src/components/DashboardPage.vue` | 修改 | 桌面 aside+移动面板挂两张紧凑卡/引导跳转落点 |
| `app/website/src/i18n/zh.js` / `en.js` | 修改 | 3 新键+7 口径键改写+2 注释对齐+2 键删除 |

---

### Task 0: worktree 与基线

- [x] **Step 0.1: EnterWorktree** 建 `dashboard-sidebar-restructure`(harness EnterWorktree,项目记忆 worktree-development-workflow 约定所有开发不直接改 master 工作区)
- [x] **Step 0.2:** `cd app/website && npm ci`(新 worktree 无 node_modules;耗时几分钟属正常)
- [x] **Step 0.3: 基线绿** `npx vitest run` ——记录失败集合(预期全绿;若基线已有挂,记录并区分「预存失败」与后续回归判定)
- [x] **Step 0.4: 基线绿** `npx playwright test tests/e2e/mobile-responsive.spec.js`(预期绿;若端口 4174 被旧 server 占用先 `lsof -ti :4174 | xargs kill` ——记忆 4174 旧 dev server 复用陷阱)

### Task 1: 拆出 SystemRunModeSettings.vue(照搬清单制)

**Files:** Create `app/website/src/components/settings/SystemRunModeSettings.vue`;参考源 `SystemSettings.vue`(在 worktree 中行号与 spec 锚定一致)

- [x] **Step 1.1: 骨架** 新建文件,name=`SystemRunModeSettings`,无 props/emits(自足式)
- [x] **Step 1.2: template 照搬** 源 :10-79(运行模式切换行含 `data-guide="run-mode"` + 状态条)、:81-184(TUN 冲突弹窗)整体搬入;外层包 `<div class="settings-pane" data-pane="run-mode"><div class="rounded-xl border border-slate-200 bg-white p-4 md:p-5 dark:border-slate-800 dark:bg-background-dark">` + 卡头(照搬源 :4-7 的 h3,标题键改用新卡自有键 `sys_runModeTitle`(新)=「运行模式」/ 'Run Mode',icon `swap_horiz`)+ compat-anchors 仅搬 run-mode 族(:395-403 runModeTun/runModeHttp/runStartBtn/runStopBtn/runModeBtn/runCurrentMode/runServiceStatus/runAvailableModes/runStatusInfo)
- [x] **Step 1.3: script 照搬** 源 imports(useNavigation/useRunStatus/useI18n——cert 组合式不搬);setup 返回 sharedRun*/refreshSharedRunState/startRunStatePolling/stopRunStatePolling(照源 :444-465 命名映射);data 只搬 run-mode 域字段(selectedMode/modeError/modeSuccess/loadingMode/pendingTunSwitchAfterInstall/tunConflictPageSize/currentTunConflictPage/tunConflictScan/authoritativeWintunDependency/showTunSwitchConfirm/switchingMode/wintunInstallPollTimer/wintunInstallPollInFlight);computed 14 个(backendMode/isRunning/modeStatus/backendModeLabel/selectedModeLabel/tunSwitchDialog×3/tunConflict×3/wintunDependency/showMissingWintunBanner/showInstallingWintunBanner——逐个照搬);methods 21 个(selectMode/cancelTunSwitchConfirm/confirmTunSwitch/continueTunSwitchAfterDependencyReady/switchMode/refreshModeState/installWintunDependency/startWintunInstallPolling/stopWintunInstallPolling/pollWintunInstallState/fetchWintunDependencyStatus/fetchTunConflictScan/ensureWintunInstallProgressModal/updateWintunInstallProgressModal/formatWintunInstallError/dispatchTunProgressEvent/goToTunConflictPage/modeLabel/normalizeMode/requestJSON/clearMessages——逐个照搬);watch 3 个(backendMode/'tunConflictScan.interfaces'/showTunSwitchConfirm);mounted 只留 startRunStatePolling+refreshModeState+wintun 续接逻辑(照源 :714-719),beforeUnmount 只留 stopRunStatePolling+stopWintunInstallPolling
- [x] **Step 1.4:** `npx vitest run` 绿(新组件无人引用,编译通过即可)

### Task 2: 拆出 SystemStatusSettings.vue

**Files:** Create `app/website/src/components/settings/SystemStatusSettings.vue`

- [x] **Step 2.1: 骨架** name=`SystemStatusSettings`,无 props/emits
- [x] **Step 2.2: template 照搬** 源 :284-363(软件状态·系统服务区块)、:186-282(服务确认弹窗)、:365-392(证书 CA 区块);外层同款卡壳+卡头(标题复用既有键 `sys_systemSettings`=「系统设置」?否——spec §4.2 卡头对齐侧栏弹窗标题,直接用新键 `dash_systemStatusTitle` 由宿主弹窗承担,SystemStatusSettings 卡头仍照搬源 :4-7 h3 用 `sys_systemSettings` 保持原卡观感);compat-anchors 仅搬 cert 族(:404-424 openCertManagementModalBtn/btn-*-cert/cert-*)
- [x] **Step 2.3: script 照搬** imports(nextTick 不需要则去/useI18n/useCertStatus);setup 返回 sharedCertStatus/startCertStatusPolling/stopCertStatusPolling(照源 :443/466-468);data 只搬 service 域字段(systemServiceInfo/serviceActionLoading/serviceAction/systemServiceError/systemServiceMessage/showSystemServiceConfirm/systemServiceConfirmAction);computed 15 个(service 域全部,照源 :598-709);methods 9 个(refreshSystemServiceStatus/clearSystemServiceMessages/openSystemServiceConfirm/closeSystemServiceConfirm/confirmSystemServiceAction/registerSystemService/uninstallSystemService/downloadCurrentCertificate/openCertificateModal/requestJSON);mounted 只留 startCertStatusPolling+refreshSystemServiceStatus,beforeUnmount 只留 stopCertStatusPolling
- [x] **Step 2.4:** `npx vitest run` 绿

### Task 3: 挂载运行模式卡进代理设置 + 删 SystemSettings

**Files:** Modify `SettingsPage.vue`;Delete `SystemSettings.vue`

- [x] **Step 3.1:** SettingsPage.vue 加 import `SystemRunModeSettings`(import 区,按字母序插在 ModelMappingSettings 之后)+ components 注册
- [x] **Step 3.2:** ①代理设置 section(:130):SectionHeader 之后、登录占位 `v-if="!isAuthenticated"` 卡片**之前**插入 `<SystemRunModeSettings />`(移出登录门,spec §4.3)
- [x] **Step 3.3:** 删 `settings/SystemSettings.vue`(此时已无挂载点;grep 确认全仓零 import)
- [x] **Step 3.4:** `npx vitest run` 绿;`grep -rn "SystemSettings" app/website/src/ --include="*.vue" --include="*.js"` 应零命中(注释提及可容忍,import 零容忍)

### Task 4: 两张侧栏紧凑卡(TDD)

**Files:** Create `dashboard/SystemStatusSidebarCard.vue` + `FileAccessSidebarCard.vue` + 两份 .test.js

- [x] **Step 4.1: 先写测试**(⚠️ 仓内无 @vue/test-utils,用 PeerAccessSettings.test.js 的 createApp+h+jsdom 手法;i18n 无需 mock 也可,但为断言稳定建议 mock 成返回键名——断言打在键名与 data-testid 上) `app/website/src/components/dashboard/SystemStatusSidebarCard.test.js`:

```js
import { describe, expect, it, vi } from 'vitest';

// 服务状态/证书状态全局 fetch 打桩(紧凑卡 mount 即取服务状态;证书走 useCertStatus 单例)
const serviceStatus = { supported: true, installed: true, running: true, status: 'active', pid: 1, platform: 'darwin', service_kind: 'launchd' };
vi.stubGlobal('fetch', vi.fn(async (url) => {
  if (String(url).includes('/api/system/service/status')) {
    return { ok: true, json: async () => ({ code: 0, data: serviceStatus }) };
  }
  if (String(url).includes('/api/cert/status')) {
    return { ok: true, json: async () => ({ code: 0, data: { is_installed: true, is_trusted: true, is_exported: true, trust_status: 'trusted', fingerprint: 'f', source: 's', activated_at: '', can_rollback: false } }) };
  }
  return { ok: true, json: async () => ({ code: 0, data: {} }) };
}));

import { createApp, h, nextTick } from 'vue';
import SystemStatusSidebarCard from './SystemStatusSidebarCard.vue';

async function mountComp() {
  const el = document.createElement('div');
  const app = createApp({ render: () => h(SystemStatusSidebarCard) });
  app.mount(el);
  await Promise.resolve(); await Promise.resolve(); await nextTick();
  return el;
}

describe('SystemStatusSidebarCard', () => {
  it('渲染标题与服务状态摘要(键名透出)', async () => {
    const el = await mountComp();
    expect(el.textContent).toContain('dash_systemStatusTitle');
    expect(el.textContent).toContain('sys_running');
  });

  it('点击管理按钮打开弹窗并承载完整卡', async () => {
    const el = await mountComp();
    el.querySelector('[data-testid="system-status-manage"]').click();
    await Promise.resolve(); await nextTick();
    expect(el.querySelector('[data-testid="system-status-modal"]')).toBeTruthy();
    expect(el.textContent).toContain('sys_softwareStatus'); // SystemStatusSettings 原卡已承载
  });

  it('关闭弹窗后触发一次服务状态刷新', async () => {
    const el = await mountComp();
    const before = vi.mocked(fetch).mock.calls.filter(([u]) => String(u).includes('/api/system/service/status')).length;
    el.querySelector('[data-testid="system-status-manage"]').click();
    await Promise.resolve(); await nextTick();
    el.querySelector('[data-testid="system-status-modal-close"]').click();
    await Promise.resolve(); await Promise.resolve(); await nextTick();
    const after = vi.mocked(fetch).mock.calls.filter(([u]) => String(u).includes('/api/system/service/status')).length;
    expect(after).toBeGreaterThan(before);
  });
});
```

- [x] **Step 4.2:** `FileAccessSidebarCard.test.js` 同构 createApp 手法;`vi.mock` 路径与组件内 import 串一致(`../../services/agentApi`)——mock `getAgentFileAccess` 返回三类 granted 快照;断言:标题键 `agent_fileAccess_title` 透出、进度含 granted 计数、点击打开弹窗且 `data-testid="file-access-status"`(原卡清单)出现

- [x] **Step 4.3: 跑测试确认失败** `npx vitest run src/components/dashboard` → FAIL(组件不存在)

- [x] **Step 4.4: 实现 SystemStatusSidebarCard.vue**(完整结构):
  - 紧凑卡:卡头(`dash_systemStatusTitle` + 状态点)+ 两行摘要(服务态复用 `sys_running`/`sys_badgeRegistered`/`sys_badgeNotRegistered`/`sys_badgeNotSupported`;证书态复用 `sys_trusted`/`cert_notTrusted`)+ 按钮「管理…」(`dash_systemManage`,`data-testid="system-status-manage"`,min-h-10 触控)
  - 内联弹窗:`v-if="modalOpen"` fixed overlay(z-[120]) + backdrop 点击关 + `data-testid="system-status-modal"` + 标题栏(`dash_systemStatusTitle` + 关闭钮 `data-testid="system-status-modal-close"`) + 滚动体承载 `<SystemStatusSettings />`
  - script:data `serviceInfo`(默认 supported:false/installed:false/running:false)、`modalOpen`;methods `loadServiceStatus()`(fetch `/api/system/service/status`,requestJSON 式解析 code===0 取 data)、`refreshOnClose()`(关弹窗后重取);证书态走 `useCertStatus()` 单例(certStatus/loading 直接读,不 startPolling——侧栏宿主环境已有网络卡轮询者则共享,无则 mount 时 startPolling+unmount stopPolling)
  - 样式:外层 `rounded-xl border border-slate-200 bg-white p-4 dark:border-slate-800 dark:bg-slate-900`,行布局 `flex min-w-0 items-center gap-2` + truncate(390px 哨兵)

- [x] **Step 4.5: 实现 FileAccessSidebarCard.vue** 同构:
  - 紧凑卡:卡头(`agent_fileAccess_title` 既有键)+ 进度行(`agent_fileAccess_summary` 参数化 + denied>0 时琥珀点 `bg-amber-400`)+ 按钮「查看 / 申请…」(`dash_fileAccessManage`,`data-testid="file-access-manage"`)
  - 数据:`getAgentFileAccess()`(`../../services/agentApi`);弹窗关闭后重取
  - 弹窗承载 `<AgentFileAccessSettings />` 原卡(零 props)

- [x] **Step 4.6:** `npx vitest run src/components/dashboard` → PASS

### Task 5: 挂载紧凑卡进 DashboardPage(桌面+移动)

**Files:** Modify `DashboardPage.vue`

- [x] **Step 5.1:** import 两张紧凑卡(components/dashboard/ 路径)+ 注册(该文件若为 script setup 则 import 即注册;实读 :1060 附近确认风格)
- [x] **Step 5.2: 桌面 aside** 网络状态块(:102-148,`data-guide="cert"`)闭合标签之后、快捷工具(:149)之前插入:
```html
      <div class="mt-6 space-y-3 px-6">
        <SystemStatusSidebarCard />
        <FileAccessSidebarCard />
      </div>
```
- [x] **Step 5.3: 移动面板** 网络状态块闭合(:336 附近)与 `<!-- 快捷工具块 -->`(:337)之间插入同款两卡容器
- [x] **Step 5.4:** `npx vitest run` 绿;`npx playwright test tests/e2e/mobile-responsive.spec.js` 绿(390/768/1280)

### Task 6: OtherSettings 解散 + i18n 键清理

**Files:** Delete `settings/OtherSettings.vue`;Modify `SettingsPage.vue`;Modify i18n 两语

- [x] **Step 6.1:** SettingsPage.vue:删 OtherSettings import(:251)/components 注册(:443)/状态 section 挂载(:203-210)及其 @save 透传;:244 注释「设备级杂项收拢在 OtherSettings.vue」改为「设备级杂项已迁仪表盘侧栏与代理设置(2026-11-05 spec)」
- [x] **Step 6.2:** 状态 section 收敛为 SectionHeader + 登录占位 + `<LogsSettings />`;该 section 不再有 @save
- [x] **Step 6.3:** 删 `settings/OtherSettings.vue`;`grep -rn "OtherSettings" app/website/src/ --include="*.vue" --include="*.js"` 断言零命中(历史注释提及可容忍,import/挂载零容忍)
- [x] **Step 6.4:** i18n 删键:`grep -rn "settings_otherTitle\|settings_otherDesc" app/website/src/` 确认仅 zh/en 定义处后删两键(两语同步)
- [x] **Step 6.5:** `npx vitest run` 绿

### Task 7: 引导跳转落点

**Files:** Modify `DashboardPage.vue:1706-1713`

- [x] **Step 7.1:** `showPage('log')` → `showPage('settings')`;注释改写:
```js
function handleGuideOpenSettings() {
  // 运行模式第二次随迁(2026-11-05 spec):状态页尾「其他」解散,落位代理设置
  // tab 首卡,引导跳代理设置页,滚动锚点 data-guide="run-mode" 不变。
  showPage('settings');
  void nextTick(() => {
    document.querySelector('[data-guide="run-mode"]')?.scrollIntoView({ block: 'center', behavior: 'smooth' });
  });
}
```
- [x] **Step 7.2:** `grep -c 'data-guide="run-mode"' app/website/src/components/settings/SystemRunModeSettings.vue` = 1(全局唯一断言:`grep -rn 'data-guide="run-mode"' app/website/src/` 仅此一处)

### Task 8: 口径统一(7 键 + 2 注释)

**Files:** Modify `i18n/zh.js`、`i18n/en.js`、`PeerMCPSettings.vue` 头注释

- [x] **Step 8.1:** zh.js 六键值改写(spec §5.1 表逐字;peerMcp_description/peerMcp_enable/peerMcp_subAgentcore/peerMcp_subAgentcoreDesc/settings_peerTransferPendingDesc 五键 + zh:601 注释对齐)
- [x] **Step 8.2:** en.js 六键值改写(peerMcp_description/peerMcp_enable/peerMcp_subAgentcore/peerMcp_subAgentcoreDesc/peerMcp_title Device Mesh→Device Access/settings_sectionAgentDesc device mesh→device access/settings_peerTransferPendingDesc + en:600 注释)
- [x] **Step 8.3:** PeerMCPSettings.vue:2-8 头注释「跨设备工具面」→「设备互访」
- [x] **Step 8.4:** `npx vitest run` 绿(PeerAccessSettings.test.js 断言的是键名 peerMcp_title 非键值,不受键值改写影响——若断言键值需同步);`grep -rn "远程执行\|拉取文件\|跨设备工具" app/website/src/i18n/` 断言仅剩判定不改项(agent_fileAccess_desc zh:1273)

### Task 9: 全量验证

- [x] **Step 9.1:** `npx vitest run` 全绿(对照 Task 0 基线集合,零新增失败)
- [x] **Step 9.2:** `npx playwright test tests/e2e/mobile-responsive.spec.js` 绿
- [x] **Step 9.3:** `npm run build` 成功(vite 生产构建,catch 模板/引用错)
- [x] **Step 9.4: grep 红线断言**:`SystemSettings` 零 import / `OtherSettings` 零 import / `settings_other` 零引用 / `data-guide="run-mode"` 全局唯一 / i18n 改写键与 spec §5.1 逐字一致
- [x] **Step 9.5: 冒烟(无头可验部分)**:`npm run preview` 或 dev server 起 4174,Playwright 截屏侧栏两卡+代理设置首卡+弹窗开合,人工清单(spec §7 冒烟 1-8)无法无头验证项(真机 TCC/服务注册)留给用户醒后核
- [x] **Step 9.6:** 汇总改动文件清单 + git status + git diff --stat,向用户汇报;**不 commit**(待用户明示)

---

## 风险回看(spec §8)

拆分漏搬→照搬清单制逐项勾对;i18n 键误删→删前 grep;useRunStatus/useCertStatus 单例→勿另起轮询;390px→min-w-0+truncate;4174→先查端口;worktree→不碰 master 工作区;提交→全程不 commit。

---

## 执行实录(2026-11-06 通宵自主执行,与计划的偏差记录)

1. **Task 1.2 卡头标题**:复用既有键 `sys_runMode`(「运行模式」),未新增计划草案里的 `sys_runModeTitle` ——与 spec §6「仅 3 新键」一致,零键增量(计划评审 Issue 2 的采纳解)。
2. **Task 3+6 合并执行**:SystemSettings 删除后 OtherSettings 仍 import 它(树会红),故 OtherSettings 解散与 i18n 孤儿键删除提前至 Task 3 同批,保每步可编译;grep 断言全部通过。
3. **Task 4 测试手法**:计划评审 Issue 1(@vue/test-utils 不存在)已在执行前修正为仓内 createApp+h+jsdom 手法;两卡测试 6/6 绿。
4. **证书态键**:紧凑卡证书行复用既有 `dash_certTrusted`/`dash_certNotTrusted`(spec §6 原写 sys_trusted/cert_notTrusted)——dash_ 命名空间更贴,键均为既有,零新增。
5. **顺带卫生追加**:en.js Agent 子 tab 区注释 device mesh→device access(§5.2 清单外的一处同口径注释)。
6. **冒烟**:一次性 spec(4 用例,跑完已删)覆盖侧栏两卡渲染/双弹窗开合/代理设置运行模式首卡/设备互访新口径文案/移动面板两卡;截图人工复核 4 张均正确。真机项(TCC 授权/服务注册/wintun)留待用户。
7. **终验**:vitest 275/275(基线 269+新增 6)、e2e mobile-responsive 10/10、`npm run build` 通过、§9.4 五条 grep 红线全过。
8. **提交**:按项目 CLAUDE.md 纪律,全程未 commit;全部改动留在 worktree `feat/dashboard-sidebar-restructure` 工作区待用户验收后明示提交。
