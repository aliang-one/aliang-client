# 仪表盘移动端适配 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** ALiang Gateway Dashboard（`app/website`，Vue 3 + Tailwind 3）在 ≥360px 手机视口完整可用，≥768px（md）桌面布局零变化。

**Architecture:** 全部以 Tailwind 响应式类实现（base 类适配移动、`md:` 恢复桌面），仅新增一个 `mobilePanelOpen` ref 控制折叠面板。DashboardPage 侧栏在移动端 display:none，新增移动专属「紧凑顶栏 + 折叠面板」（复用页面级 computed/方法，不抽共享模板）。依据 2026-10-08 全量审计（77 项发现）逐文件修复。

**Tech Stack:** Vue 3 `<script setup>` / Tailwind 3.4（默认 screens，`max-md:` 可用）/ Vitest / Playwright（chrome channel, webServer=vite dev :4174）

**Spec:** `docs/superpowers/specs/2026-10-08-mobile-responsive-design.md`

**工作目录:** git worktree `.claude/worktrees/feat-mobile-responsive`（分支 `feat/mobile-responsive`），所有命令在 `app/website/` 下执行。

---

## 全局红线（每个任务都适用）

1. **桌面零变化**：桌面值永远通过 `md:` 前缀恢复，不删/不改任何现有桌面类。允许 `sm:`（640-767px）仅当审计已给出且 640-767px 行为变化是预期改进。
2. **`dvh` 单位**：max-h/高度约束统一用 `dvh`（本产品 2026 年目标浏览器均支持；旧浏览器声明失效=维持现状，可接受）。不用 `100vh`（iOS 地址栏问题）。
3. **表格 th/td padding**：th 是 `py-3`、td 是 `py-4`，两套 `md:` 还原值不同，批量替换禁止统一。
4. **不动的东西**：`compat-anchors`、`.settings-tab/.settings-content` 遗留类、`styles.css` 中非目标规则、QuickSetupModal 的 z-[130]/z-[140] 层级与 Esc 逻辑、OnboardingGuide 面板宽度公式、UserInfoSettings.vue:3 的死类 `sm:p-4.5`（保留不动）。
5. **本仓库不自动 commit**：执行过程不产生任何 commit；全部改动留在工作区，最后由用户决定提交（提交信息规范：`新增：…`/`修复：…` 全中文）。
6. **TDD 适配**：本任务无单元测试可写（纯模板/类变更），以 Task 1 的 Playwright 移动视口断言作为回归网：先写失败 → 每任务结束跑对应断言转绿。
7. 审计报告全文在 `/tmp/mobile-audit-full.md`（77 项，含 OK/风险清单）；每个任务里已摘录所需条目，无需重新审计。
8. **锚点消歧**：两处锚点串在目标文件中不唯一——DashboardPage.vue 的 `flex items-center gap-4`（4 处，Task 2 只改 header logo 组那处）与 QuickSetupModal.vue 的 `px-6 py-5`（2 处，L16 侧栏头与 L107 内容区）——Edit 时用行号附近上下文加长 old_string 消歧。

---

### Task 1: 移动端 e2e 回归网（先写，确认失败）

**Files:**
- Create: `tests/e2e/mobile-responsive.spec.js`

- [ ] **Step 1: 写测试文件**

```js
import { expect, test } from '@playwright/test';

// 与 auth-consistency.spec.js 相同的 API mock 模式；dashboardData.js 对空对象有防御性提取
async function mockApi(page) {
  await page.addInitScript(() => {
    window.EventSource = class {
      constructor() { setTimeout(() => this.onopen?.(), 0); }
      close() {}
    };
  });
  await page.route('**/api/**', async (route) => {
    const url = new URL(route.request().url());
    if (url.pathname === '/api/auth/session') {
      await route.fulfill({
        contentType: 'application/json',
        body: JSON.stringify({
          code: 0,
          data: {
            type: 'session_snapshot', instance_id: 'e2e-mobile', revision: 1, state: 'active',
            user: { id: 7, username: 'alice', email: 'alice@example.com' }
          }
        })
      });
      return;
    }
    await route.fulfill({ contentType: 'application/json', body: JSON.stringify({ code: 0, data: {} }) });
  });
}

async function horizontalOverflowPx(page) {
  return page.evaluate(() =>
    Math.max(
      document.documentElement.scrollWidth - document.documentElement.clientWidth,
      document.body.scrollWidth - document.body.clientWidth
    )
  );
}

test.describe('mobile 390x844', () => {
  test.use({ viewport: { width: 390, height: 844 } });
  test.beforeEach(async ({ page }) => { await mockApi(page); });

  test('dashboard: aside 隐藏、移动顶栏可见、无横向溢出、无 pageerror', async ({ page }) => {
    const pageErrors = [];
    page.on('pageerror', (e) => pageErrors.push(e));
    await page.goto('/');
    await expect(page.locator('h1').first()).toContainText('ALiang');
    await expect(page.locator('aside').first()).toBeHidden();
    await expect(page.locator('[data-mobile-topbar]')).toBeVisible();
    expect(await horizontalOverflowPx(page)).toBeLessThanOrEqual(1);
    expect(pageErrors).toEqual([]);
  });

  test('dashboard: 折叠面板可开合', async ({ page }) => {
    await page.goto('/');
    await expect(page.locator('h1').first()).toContainText('ALiang');
    const panel = page.locator('[data-mobile-panel]');
    await expect(panel).toBeHidden();
    await page.locator('[data-mobile-panel-toggle]').click();
    await expect(panel).toBeVisible();
    await page.locator('[data-mobile-panel-toggle]').click();
    await expect(panel).toBeHidden();
  });

  test('dashboard: 各主内容区块无横向溢出', async ({ page }) => {
    await page.goto('/');
    await expect(page.locator('h1').first()).toContainText('ALiang');
    // 展开折叠面板到最大内容状态再量
    await page.locator('[data-mobile-panel-toggle]').click();
    await expect(page.locator('[data-mobile-panel]')).toBeVisible();
    expect(await horizontalOverflowPx(page)).toBeLessThanOrEqual(1);
  });

  test('settings: 各子页无横向溢出', async ({ page }) => {
    await page.goto('/');
    await expect(page.locator('h1').first()).toContainText('ALiang');
    await page.locator('[data-mobile-panel-toggle]').click();
    await page.getByRole('button', { name: /更多设置|More Settings/ }).click();
    await expect(page.locator('aside').first()).toBeVisible(); // settings 页 aside 是导航栏
    const tabs = page.locator('div.grid.grid-cols-2.md\\:hidden > button');
    const count = await tabs.count();
    expect(count).toBeGreaterThanOrEqual(4);
    for (let i = 0; i < count; i++) {
      await tabs.nth(i).click();
      await expect(tabs.nth(i)).toHaveClass(/border-primary/);
      expect(await horizontalOverflowPx(page)).toBeLessThanOrEqual(1);
    }
  });

  test('登录弹窗在视口内且可滚动到底部按钮', async ({ page }) => {
    // 未登录态：拦掉 session 快照
    await page.route('**/api/auth/session', (route) =>
      route.fulfill({
        contentType: 'application/json',
        body: JSON.stringify({ code: 0, data: { type: 'session_snapshot', instance_id: 'e2e-mobile', revision: 1, state: 'unauthenticated' } })
      })
    );
    await page.goto('/');
    await page.locator('[data-mobile-topbar] button').first().waitFor();
    // 未登录时顶栏头像入口点击 → 登录弹窗（data-mobile-account 在 Task 2 的顶栏头像按钮上）
    await page.locator('[data-mobile-account]').click();
    const dialog = page.locator('.fixed.inset-0 .max-w-md');
    await expect(dialog).toBeVisible();
    const box = await dialog.boundingBox();
    expect(box).not.toBeNull();
    expect(box.y).toBeGreaterThanOrEqual(0);
    expect(box.y + box.height).toBeLessThanOrEqual(844);
    await dialog.locator('button[type="submit"], form button').last().scrollIntoViewIfNeeded();
  });
});

test.describe('desktop 1280x800 控制组（桌面零变化）', () => {
  test.use({ viewport: { width: 1280, height: 800 } });
  test.beforeEach(async ({ page }) => { await mockApi(page); });

  test('桌面: aside 与服务器链路卡可见、无移动顶栏', async ({ page }) => {
    await page.goto('/');
    await expect(page.locator('aside').first()).toBeVisible();
    await expect(page.getByText(/服务器连接|服务器链路|Server Link/i).first()).toBeVisible();
    await expect(page.locator('[data-mobile-topbar]')).toBeHidden(); // md:hidden 下元素仍在 DOM，仅 display:none
  });
});
```

- [ ] **Step 2: 跑测试确认按预期失败**

Run: `npx playwright test tests/e2e/mobile-responsive.spec.js`
Expected: mobile describe 全部 5 个用例 FAIL（`[data-mobile-topbar]` 尚不存在 / aside 可见），desktop 控制组 PASS（`toBeHidden` 对不存在的元素同样满足；服务器连接文案 zh/en 均能命中）。这是后续任务的基线。

（若 webServer 启动失败，先单独跑 `npx playwright test tests/e2e/auth-consistency.spec.js` 确认环境正常。）

---

### Task 2: DashboardPage 移动骨架（顶栏 + 折叠面板 + header 瘦身）

**Files:**
- Modify: `src/components/DashboardPage.vue`（L8 aside、L189-251 header、`<script setup>` 加 ref、template 插入移动骨架）
- Modify: `src/i18n/zh.js`、`src/i18n/en.js`（各加 1 个 key）

审计依据：break L5-9（采纳「顶栏+折叠面板」方案，**不采纳**根容器 flex-col 堆叠——aside 直接 display:none，根容器/滚动结构零改动）、break L189-251、squeeze L235-250。

- [ ] **Step 1: i18n 加 key**

`src/i18n/zh.js` 在 `dash_quickTools: '快捷工具',` 行后加：
```js
  dash_controlPanel: '控制面板',
```
`src/i18n/en.js` 在 `dash_quickTools: 'Quick Tools',` 行后加：
```js
  dash_controlPanel: 'Controls',
```

- [ ] **Step 2: aside 移动端隐藏**

`src/components/DashboardPage.vue` L8 aside 的 class 开头 `w-80 lg:w-96 bg-white` 前插入 `hidden md:flex`（原有 `flex flex-col` 保留——`hidden md:flex` 负责显示切换，`flex-col` 是方向）：

```
class="hidden md:flex w-80 lg:w-96 bg-white dark:bg-slate-900 border-r ... flex flex-col h-full overflow-y-auto custom-scrollbar"
```
≥768px 与现状逐类一致（仅显示方式多一层等价切换）。

- [ ] **Step 3: 插入移动顶栏 + 折叠面板**

在 `<main ...>`（L188）开标签之后、`<header`（L189）之前插入完整块。全部复用页面既有 computed/方法（powerButtonClass/toggleProxyPower/runActionLoading/proxyStatusTitle/serverStateLabel/userAvatarText/userDisplayName/planLabel/accountSubtitle/authNotice/isAuthenticated/openLoginModal/runModeLabel/certLoading/networkStatusIcon/networkStatusIconClass/certBadgeText/certBadgeClass/openCertModal/handleReinstall/openQuickSetup/handleShowSettings/handleTopUp/accountBalanceText/accountBalanceHint/accountBalanceHintClass/powerButtonDisabled/powerButtonTitle）：

```html
      <!-- 移动端：紧凑顶栏 + 可折叠控制面板（≥md 隐藏，桌面结构不受影响） -->
      <div
        data-mobile-topbar
        class="md:hidden sticky top-0 z-40 shrink-0 border-b border-slate-200 bg-white/95 backdrop-blur dark:border-slate-800 dark:bg-slate-900/95"
      >
        <div class="flex items-center gap-3 px-4 py-2.5">
          <button
            type="button"
            :disabled="powerButtonDisabled"
            :title="powerButtonTitle"
            class="relative flex size-11 shrink-0 items-center justify-center rounded-full border transition-all disabled:cursor-not-allowed"
            :class="powerButtonClass"
            @click="toggleProxyPower"
          >
            <span v-if="runActionLoading" class="inline-block size-5 border-2 border-white/30 border-t-white rounded-full animate-spin"></span>
            <span v-else class="material-symbols-outlined text-xl font-bold">power_settings_new</span>
          </button>
          <div class="min-w-0 flex-1">
            <p class="truncate text-sm font-semibold text-slate-600 dark:text-slate-400">{{ proxyStatusTitle }}</p>
            <p class="truncate text-xs text-slate-400">{{ runActionLoading ? powerButtonBusyText : serverStateLabel }}</p>
          </div>
          <button
            type="button"
            data-mobile-account
            class="flex size-11 shrink-0 items-center justify-center rounded-xl bg-gradient-to-br from-primary to-emerald-500 text-white"
            :title="userDisplayName"
            @click="handleAccountCardClick"
          >
            <span class="text-sm font-bold uppercase tracking-wide">{{ userAvatarText }}</span>
          </button>
        </div>
        <button
          type="button"
          data-mobile-panel-toggle
          class="flex w-full items-center gap-1.5 border-t border-slate-100 px-4 py-2.5 text-xs font-bold uppercase tracking-wider text-slate-400 dark:border-slate-800"
          @click="mobilePanelOpen = !mobilePanelOpen"
        >
          <span class="material-symbols-outlined text-base transition-transform" :class="mobilePanelOpen ? 'rotate-180' : ''">expand_more</span>
          {{ t('dash_controlPanel') }}
        </button>
        <div
          v-show="mobilePanelOpen"
          data-mobile-panel
          class="max-h-[70dvh] space-y-4 overflow-y-auto border-t border-slate-100 px-4 py-4 dark:border-slate-800"
        >
          <!-- 账户块 -->
          <button
            type="button"
            class="flex w-full items-center gap-3 rounded-lg border border-slate-200 bg-slate-50 p-3 text-left dark:border-slate-700 dark:bg-slate-800/50"
            @click="handleAccountCardClick"
          >
            <span class="flex size-10 shrink-0 items-center justify-center rounded-lg bg-gradient-to-br from-primary to-emerald-500 text-white">
              <span class="text-xs font-bold uppercase">{{ userAvatarText }}</span>
            </span>
            <span class="min-w-0 flex-1">
              <span class="block truncate text-sm font-bold text-slate-900 dark:text-white">{{ userDisplayName }}</span>
              <span class="mt-0.5 block truncate text-[11px] text-slate-400">{{ accountSubtitle }}</span>
            </span>
            <span class="shrink-0 rounded bg-primary/10 px-2 py-0.5 text-[10px] font-bold uppercase tracking-wider text-primary">{{ planLabel }}</span>
          </button>
          <div
            v-if="!isAuthenticated"
            class="rounded-lg border border-amber-200 bg-amber-50 px-3 py-2 text-xs text-amber-700 dark:border-amber-900 dark:bg-amber-950/40 dark:text-amber-300"
          >
            <div class="flex items-center justify-between gap-3">
              <span class="min-w-0">{{ authNotice }}</span>
              <button
                type="button"
                class="shrink-0 rounded-md bg-white/80 px-3 py-2 text-[11px] font-semibold text-amber-700 dark:bg-slate-900/70 dark:text-amber-200"
                @click="openLoginModal"
              >
                {{ t('dash_loginNow') }}
              </button>
            </div>
          </div>
          <!-- 网络状态块 -->
          <div class="rounded border border-slate-100 bg-slate-50 p-4 dark:border-slate-700 dark:bg-slate-800/50">
            <div class="mb-3 flex items-center justify-between">
              <p class="text-xs font-bold uppercase text-slate-500">{{ t('dash_networkStatus') }}</p>
              <span v-if="certLoading" class="inline-block size-4 border-2 border-slate-200 border-t-slate-400 rounded-full animate-spin"></span>
              <span v-else class="material-symbols-outlined text-sm" :class="networkStatusIconClass">{{ networkStatusIcon }}</span>
            </div>
            <div class="space-y-2">
              <div class="flex justify-between text-sm">
                <span class="text-slate-500">{{ t('dash_mode') }}</span>
                <span class="font-medium text-slate-700 dark:text-slate-200">{{ runModeLabel }}</span>
              </div>
              <div class="flex justify-between text-sm">
                <span class="text-slate-500">{{ t('dash_protocol') }}</span>
                <span class="font-medium text-slate-700 dark:text-slate-200">SOCKS5</span>
              </div>
              <div class="flex justify-between text-sm">
                <span class="text-slate-500">{{ t('dash_certificate') }}</span>
                <span class="font-medium" :class="certBadgeClass">{{ certBadgeText }}</span>
              </div>
            </div>
            <div class="mt-4 grid grid-cols-2 gap-2">
              <button type="button" class="px-3 py-2.5 text-xs font-bold border border-slate-200 dark:border-slate-600 rounded" @click="openCertModal">
                {{ t('dash_details') }}
              </button>
              <button type="button" class="px-3 py-2.5 text-xs font-bold bg-primary/10 text-primary rounded" @click="handleReinstall">
                {{ t('dash_reinstall') }}
              </button>
            </div>
          </div>
          <!-- 快捷工具块 -->
          <div class="space-y-3">
            <p class="px-1 text-[10px] font-bold uppercase tracking-widest text-slate-400">{{ t('dash_quickTools') }}</p>
            <button
              type="button"
              :disabled="!isAuthenticated"
              class="flex w-full items-center gap-3 rounded border bg-white px-4 py-3 text-sm font-medium dark:bg-slate-900"
              :class="!isAuthenticated ? 'cursor-not-allowed opacity-60 border-slate-200 dark:border-slate-700' : 'border-slate-200 hover:border-primary dark:border-slate-700'"
              @click="openQuickSetup"
            >
              <span class="material-symbols-outlined text-lg text-slate-400">bolt</span>
              {{ t('dash_quickSetup') }}
            </button>
            <button
              type="button"
              class="flex w-full items-center gap-3 rounded border border-slate-200 bg-white px-4 py-3 text-sm font-medium hover:border-primary dark:border-slate-700 dark:bg-slate-900"
              @click="handleShowSettings"
            >
              <span class="material-symbols-outlined text-lg text-slate-400">settings</span>
              {{ t('dash_moreSettings') }}
            </button>
          </div>
          <!-- 余额块 -->
          <div class="border-t border-slate-100 pt-4 dark:border-slate-800">
            <div class="mb-3 flex items-center justify-between">
              <span class="text-xs font-bold uppercase tracking-wider text-slate-500">{{ t('dash_accountBalance') }}</span>
              <span class="text-lg font-bold text-slate-900 dark:text-white">{{ accountBalanceText }}</span>
            </div>
            <button
              type="button"
              class="w-full rounded bg-slate-900 py-2.5 text-xs font-bold text-white hover:opacity-90 dark:bg-primary"
              @click="handleTopUp"
            >
              {{ t('dash_topUpFunds') }}
            </button>
            <p class="mt-2 text-center text-[10px] italic" :class="accountBalanceHintClass">{{ accountBalanceHint }}</p>
          </div>
        </div>
      </div>
```

- [ ] **Step 4: script 加状态**

`<script setup>` 中已有 `import { computed, ref, ... } from 'vue'`（L798）。在已有 ref 声明区（如 `const isLoginModalOpen = ...` 附近）加：

```js
const mobilePanelOpen = ref(false);
```

- [ ] **Step 5: header 瘦身（逐处精确 Edit）**

| 位置 | 旧类（唯一锚点片段） | 新类 |
|---|---|---|
| L190 header 容器 | `justify-between px-8 shrink-0` | `justify-between px-4 shrink-0 md:px-8` |
| L194 logo 组 | `flex items-center gap-4` | `flex items-center gap-2 md:gap-4` |
| L196 h1 | `font-bold text-xl tracking-tight` | `font-bold text-lg tracking-tight md:text-xl` |
| L202 server 卡外层 | `rounded-xl border border-slate-200 bg-slate-50/90 px-3 py-2 dark:border-slate-700 dark:bg-slate-800/70` | 同串前加 `hidden md:block ` |
| L234 分隔线 | `h-8 w-px bg-slate-200 dark:bg-slate-800 mx-2` | 同串前加 `hidden md:block ` |
| L246 刷新按钮 | `px-4 py-1.5 bg-primary/10 text-primary text-xs font-bold rounded-lg` | `px-3 py-2 md:px-4 md:py-1.5 bg-primary/10 text-primary text-xs font-bold rounded-lg` |

- [ ] **Step 6: 主内容容器与图表卡 padding**

| 位置 | 旧 | 新 |
|---|---|---|
| L253 滚动容器 | `flex-1 overflow-y-auto p-8 custom-scrollbar` | `flex-1 overflow-y-auto p-4 custom-scrollbar md:p-8` |
| L254 图表网格 | `gap-8 mb-8` | `gap-4 mb-8 md:gap-8` |
| L256、L312 两张图表卡 | `p-6 rounded border` | `p-4 rounded border md:p-6` |

- [ ] **Step 7: 跑 e2e 移动骨架断言转绿**

Run: `npx playwright test tests/e2e/mobile-responsive.spec.js`
Expected: 前两个用例（topbar/panel）PASS；settings/弹窗用例可能仍 FAIL（属 Task 3/4）；desktop 控制组必须 PASS。

---

### Task 3: DashboardPage 主内容 + 内嵌弹窗

**Files:** Modify: `src/components/DashboardPage.vue`

审计依据：break L427/L449-484/L523-633/L635-785、squeeze L273-295/L388-409、polish L256-262/L378-383/L396-408/L411/L415-511/L538-577。

- [ ] **Step 1: 模型用量分布卡（环形图堆叠）**

| 位置 | 旧 | 新 |
|---|---|---|
| L273 图例行 | `flex items-center gap-8` | `flex flex-col items-center gap-4 md:flex-row md:gap-8` |
| L274 环形容器 | `relative size-40 flex items-center justify-center` | `relative flex size-32 items-center justify-center md:size-40` |
| L260-262 more_horiz | `text-slate-400 hover:text-primary` | `rounded p-2 -m-2 text-slate-400 hover:text-primary` |

- [ ] **Step 2: 用量趋势图小修**

| 位置 | 旧 | 新（追加） |
|---|---|---|
| L378 X 轴容器 | `absolute bottom-0 w-full flex justify-between text-[10px] text-slate-400 pt-2` | 追加 `overflow-hidden` |
| L379 每个日期 span | `<span ...>` | 追加 `min-w-0 truncate`（该 v-for span） |
| L381 摘要浮层 | `absolute left-0 top-0 rounded bg-white/80 px-2 py-1 text-[10px]` | 追加 `max-w-[75%] truncate` |

- [ ] **Step 3: 近期用量记录卡**

| 位置 | 旧 | 新 |
|---|---|---|
| L388 卡头 | `p-6 border-b border-slate-100 dark:border-slate-800 flex justify-between items-center` | `flex flex-col items-start gap-3 border-b border-slate-100 p-4 dark:border-slate-800 md:flex-row md:items-center md:justify-between md:p-6` |
| L396-408 刷新/导出小按钮（两处） | 各自 class 末尾追加 | `h-10 md:h-auto` |
| L411 筛选栏 | `px-6 py-3 border-b` | `px-4 py-3 border-b md:px-6` |
| L427 搜索输入 | `h-9 min-w-[260px] flex-1 rounded border` | `h-10 min-w-0 flex-1 rounded border md:h-9 md:min-w-[260px]` |
| L415/432/440/500/511 下拉与应用/重置/分页按钮 | `h-9 rounded` | `h-10 rounded md:h-9`（五处逐个改） |
| L449 table | `w-full text-left` | `w-full max-md:min-w-[880px] text-left`（收尾修正：原 `min-w-[880px]` 会泄漏到 ≥768 桌面，改 max-md: 门控后 ≥768 与 master 逐类一致） |
| L454-464 各 th | `px-6 py-3` | `px-3 py-2.5 md:px-6 md:py-3` |
| L473-483 各 td | `px-6 py-4` | `px-3 py-3 md:px-6 md:py-4` |

注意：th/td 的 md: 还原值不同（py-3 vs py-4），逐个替换禁止全局查找替换 `px-6 py-`。

- [ ] **Step 4: 登录弹窗（L523-633）**

| 位置 | 旧 | 新 |
|---|---|---|
| L528 面板 | `w-full max-w-md overflow-hidden rounded-2xl` | `flex max-h-[calc(100dvh-2rem)] w-full max-w-md flex-col overflow-hidden rounded-2xl` |
| L549 内容体 | `space-y-4 p-5` | `min-h-0 flex-1 space-y-4 overflow-y-auto p-5` |
| L538-545 关闭钮 | `rounded-lg p-1.5 text-slate-500 transition` | `rounded-lg p-2.5 text-slate-500 transition md:p-1.5` |
| L561-577 两个 tab | `rounded-md px-3 py-1.5 text-xs font-semibold transition` | `rounded-md px-3 py-2.5 text-xs font-semibold transition md:py-1.5`（两处） |

- [ ] **Step 5: Deep Mode 弹窗（L635-785）**

| 位置 | 旧 | 新 |
|---|---|---|
| L640 面板 | `w-full max-w-3xl overflow-hidden rounded-2xl` | `flex max-h-[calc(100dvh-2rem)] w-full max-w-3xl flex-col overflow-hidden rounded-2xl` |
| L658 内容体 | `space-y-5 p-5` | `min-h-0 flex-1 space-y-5 overflow-y-auto p-5` |
| L648-654 关闭钮 | 同登录弹窗关闭钮改法 | 同上（p-2.5 md:p-1.5） |

- [ ] **Step 6: 跑相关断言**

Run: `npx playwright test tests/e2e/mobile-responsive.spec.js && npx playwright test tests/e2e/auth-consistency.spec.js`
Expected: dashboard 无溢出用例 PASS（含表格区块）；desktop 控制组 PASS；auth-consistency 不回归。

---

### Task 4: styles.css 全局规则 + SettingsPage/SystemSettings/UserInfoSettings

**Files:**
- Modify: `assets/styles.css:418-432`
- Modify: `src/components/SettingsPage.vue`、`src/components/settings/SystemSettings.vue`、`src/components/settings/UserInfoSettings.vue`

审计依据：squeeze styles.css:155/67、polish SettingsPage L70-77/L85-118/L219-227、break/squeeze/polish SystemSettings 多条、polish UserInfoSettings L31-48/L360-368。

- [ ] **Step 1: 删除全局 aside/main 移动规则**

`assets/styles.css` L418-432 的 `/* Responsive adjustments */ @media (max-width: 768px) { .page-container.active {...} aside {...!important} main {...} }` 块中，删除 `aside { ... }` 与 `main { ... }` 两条规则，保留 `.page-container.active` 那条（若 grep 全仓无 `.page-container` 使用者，则整块删除）。Settings 页移动端布局由其自身 Tailwind 类（`grid-cols-1 lg:grid-cols-12`）承担。

先跑 `grep -rn "page-container" src/ assets/` 确认使用情况再定删除范围。

**收尾修正（整体评审 Critical）**：该 `@media (max-width: 768px)` 的 768px 与 Tailwind `md`（min-width:768px）在恰好 768px（iPad 竖屏）撞车——桌面布局渲染 + 强制纵向堆叠 = 主区高度 0。保留的媒体查询值改为 `max-width: 767px`。

- [ ] **Step 2: SettingsPage**

| 位置 | 旧 | 新 |
|---|---|---|
| L70-77 返回按钮 | `flex h-8 w-8 items-center justify-center rounded-lg` | `flex h-10 w-10 items-center justify-center rounded-lg md:h-8 md:w-8` |
| L85-118 移动 tab（4 个按钮） | `rounded-lg border px-3 py-2 text-sm font-semibold transition` | 加 `min-h-10`（此块仅 <768 渲染，无需 md: 还原） |
| L219-227 footer | `mx-auto flex max-w-7xl items-center justify-between px-4` | `mx-auto flex max-w-7xl flex-wrap items-center justify-between gap-x-4 gap-y-1.5 px-4` |

- [ ] **Step 3: SystemSettings**

| 位置 | 旧 | 新 |
|---|---|---|
| L3 卡片容器 | `p-5` | `p-4 md:p-5` |
| L10/L45/L68 三行设置行 | `flex items-center justify-between`（L68 为 `flex items-center justify-between gap-4`） | `flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between sm:gap-4`（L68 在原串基础上加 flex-col gap-3 sm: 前半段） |
| L16-87 分段按钮（4 处 :class 数组首项） | `rounded px-3 py-1 text-[10px] font-bold transition` | `rounded px-3 py-2 md:py-1 text-[10px] font-bold transition` |
| L126-131 TUN 确认弹窗卡片 | `w-full max-w-md overflow-hidden rounded-2xl` | `max-h-[85dvh] w-full max-w-md overflow-y-auto overflow-x-hidden rounded-2xl` |
| L141/L276 两弹窗关闭钮 | `rounded-lg p-1.5` / `rounded-xl p-2` | `rounded-lg p-2.5 md:p-1.5` / `rounded-xl p-3 md:p-2` |
| L231-238 服务确认弹窗卡片 | `w-full max-w-lg overflow-hidden rounded-3xl` | `max-h-[85dvh] w-full max-w-lg overflow-y-auto overflow-x-hidden rounded-3xl` |
| L376-393 注册/卸载按钮 | `py-1.5 text-[11px] font-bold` | `py-2.5 md:py-1.5 text-[11px] font-bold`（两处） |
| L395-402 刷新按钮（实际串含 `mt-2 w-full rounded border border-slate-200 py-1.5 text-[11px] font-bold`） | `py-1.5` | `py-2.5 md:py-1.5` |

- [ ] **Step 4: UserInfoSettings**

| 位置 | 旧 | 新 |
|---|---|---|
| L31-48 登录方式切换（两处） | `rounded-md px-3 py-1.5 text-xs font-semibold transition` | `rounded-md px-3 py-1.5 text-xs font-semibold transition min-h-10 md:min-h-0` |
| L360-368 复制钮 | `inline-flex h-8 w-8 shrink-0 items-center justify-center rounded-lg` | `inline-flex h-10 w-10 shrink-0 items-center justify-center rounded-lg md:h-8 md:w-8` |

- [ ] **Step 5: 验证**

Run: `npx playwright test tests/e2e/mobile-responsive.spec.js`
Expected: settings 各子页无溢出用例 PASS；desktop 控制组 PASS。

---

### Task 5: 独立弹窗组件

**Files:**
- Modify: `src/components/QuickSetupModal.vue`、`src/components/ConfigDiffEditors.vue`、`src/components/QuickSetupResultPanel.vue`、`src/components/CertManagementModal.vue`、`src/components/TutorialDocsModal.vue`、`src/components/SoftwareUpdateNotice.vue`、`src/components/OnboardingGuide.vue`

审计依据：break QuickSetupModal L12-24、break ConfigDiffEditors、squeeze QuickSetupModal L12/L461-607、polish 各文件触控目标。**两条 break（壳层堆叠 + 高度约束）必须一起落地**。

- [ ] **Step 1: QuickSetupModal 壳层堆叠**

| 位置 | 旧 | 新 |
|---|---|---|
| L12 壳层 | `flex h-[720px] w-full max-w-6xl` | `flex h-[720px] max-h-[92dvh] w-full max-w-6xl flex-col md:flex-row` |
| L15 aside | `flex w-72 shrink-0 flex-col border-r border-slate-100` | `flex w-full shrink-0 flex-col border-b border-slate-100 md:w-72 md:border-b-0 md:border-r`（其余类保留） |
| L24 组合列表 | `flex-1 space-y-2 overflow-y-auto px-4 py-4` | `max-h-44 md:max-h-none flex-1 space-y-2 overflow-y-auto px-4 py-4` |
| L16 侧栏头部 | `px-6 py-5` | `px-4 py-4 md:px-6 md:py-5` |
| L70 右侧 header | `px-6` | `px-4 md:px-6` |
| L107 内容滚动区 | `px-6 py-5` | `px-4 py-4 md:px-6 md:py-5` |

- [ ] **Step 2: QuickSetupModal 嵌套弹窗与触控**

| 位置 | 旧 | 新 |
|---|---|---|
| L461/L540/L575 三个嵌套面板 | `relative z-10 w-full max-w-md rounded-2xl border border-slate-200 bg-white p-5 shadow-2xl` | 同串中 `w-full` 后插 `max-h-[88dvh] overflow-y-auto` |
| 全部主操作 `min-h-9` 按钮（L82、L90、L257、L317、L324、L514、L521、L549、L556、L599、L607 共 11 处，含嵌套弹窗底部取消/确认钮） | `min-h-9` | `min-h-10 md:min-h-9` |
| L150-427 各工具小按钮 `min-h-8`（约 11 处） | `min-h-8` | `min-h-9 md:min-h-8` |
| L427 备份恢复 `min-h-7` | `min-h-7 shrink-0` | `min-h-9 md:min-h-7 shrink-0` |

- [ ] **Step 3: ConfigDiffEditors 移动端堆叠**

| 位置 | 旧 | 新 |
|---|---|---|
| L1 根 grid | `grid min-h-0 flex-1 grid-cols-[minmax(0,1fr)_minmax(0,1fr)] overflow-hidden` | `grid min-h-0 flex-1 grid-cols-1 overflow-hidden md:grid-cols-[minmax(0,1fr)_minmax(0,1fr)]` |
| L4 左列 | `flex min-h-0 min-w-0 flex-col border-r border-slate-200` | `flex min-h-0 min-w-0 flex-col border-b border-slate-200 md:border-b-0 md:border-r`（dark:border 类保留） |
| QuickSetupModal L401 宿主高度 | `h-[440px]` | `h-[75dvh] md:h-[440px]` |

改完手动验证：390px 视口打开快速设置→配置 diff，两编辑器上下堆叠、CodeMirror 正确重测量、无横向溢出（CodeMirror resize 依赖容器宽度测量，需目检）。

- [ ] **Step 4: QuickSetupResultPanel / CertManagementModal / TutorialDocsModal**

| 位置 | 旧 | 新 |
|---|---|---|
| ResultPanel L31 复制钮 | `size-7 shrink-0` | `size-9 md:size-7 shrink-0` |
| Cert L90/99/112/121 四按钮 | `min-h-9 flex items-center` | `min-h-10 md:min-h-9 flex items-center` |
| Cert L261 重装行 | `flex items-center justify-between gap-3` | `flex flex-col items-start gap-3 sm:flex-row sm:items-center sm:justify-between` |
| Cert L72 详情容器 | `text-xs text-slate-500 dark:text-slate-400 space-y-0.5 mt-2` | 同串前加 `break-words ` |
| Cert L10/L25 | `px-6 py-4` / `p-6` | `px-4 py-4 md:px-6` / `p-4 md:p-6` |
| Tutorial L13 宽 | `w-[min(96vw,900px)]` | `w-full sm:w-[min(96vw,900px)]` |
| Tutorial L13 高 | `h-[min(88vh,760px)]` | `h-[min(88vh,760px)] max-h-[88dvh]` |
| Tutorial L22 关闭钮 | `size-8 shrink-0` | `size-10 md:size-8 shrink-0` |
| Tutorial L35 页签 | `rounded-full px-3 py-1.5 text-xs` | `rounded-full px-3 py-2.5 md:py-1.5 text-xs` |

- [ ] **Step 5: SoftwareUpdateNotice / OnboardingGuide**

| 位置 | 旧 | 新 |
|---|---|---|
| Update L2 浮标容器 | `pointer-events-none fixed right-5 top-5 z-[1050] flex flex-col items-end gap-3` | `pointer-events-none fixed right-3 top-3 z-[1050] flex max-w-[calc(100vw-1.5rem)] flex-col items-end gap-3 sm:right-5 sm:top-5` |
| Update L26 内容框 | `w-full max-w-2xl overflow-hidden rounded-[28px] border` | `flex max-h-[90dvh] w-full max-w-2xl flex-col overflow-hidden rounded-[28px] border`（border 余类保留） |
| Update L65 正文 | `space-y-5 px-6 py-6 sm:px-7` | `min-h-0 flex-1 space-y-5 overflow-y-auto px-6 py-6 sm:px-7` |
| Onboarding L20 面板 | `absolute z-10 w-[min(92vw,420px)] rounded-2xl border` | 同串 `z-10` 后插 `max-h-[calc(100dvh-24px)] overflow-y-auto`（宽度公式不动） |
| Onboarding L30 关闭钮 | `size-8` | `size-10 md:size-8` |
| Onboarding L80/88/95 底部按钮 | `h-9` | `h-10 md:h-9` |

- [ ] **Step 6: 验证**

Run: `npm run build && npx playwright test tests/e2e/mobile-responsive.spec.js`
Expected: build 成功；既有用例不回归。弹窗细节靠 Task 7 截图走查覆盖（登录弹窗用例应持续 PASS）。

---

### Task 6: Settings 其余子页

**Files:** Modify: `src/components/settings/ModelMappingSettings.vue`、`ConfigSyncSettings.vue`、`RulesSettings.vue`、`LogsSettings.vue`、`AgentSettings.vue`

- [ ] **Step 1: ModelMappingSettings（break：4 列硬轨道）**

| 位置 | 旧 | 新 |
|---|---|---|
| L34 规则行容器 | `grid grid-cols-[1fr_auto_1fr_auto] items-center gap-2` | `grid grid-cols-1 items-center gap-2 sm:grid-cols-[minmax(0,1fr)_auto_minmax(0,1fr)_auto]` |
| L43 箭头 | `text-slate-400` | `hidden text-slate-400 sm:inline` |
| L24 表头行 | `grid grid-cols-[1fr_auto_1fr_auto] items-center gap-2 text-[11px]` | `hidden text-[11px] sm:grid sm:grid-cols-[minmax(0,1fr)_auto_minmax(0,1fr)_auto] sm:items-center sm:gap-2`（余类保留） |
| L9-15 复选框 | `h-4 w-4 rounded` | `h-5 w-5 rounded md:h-4 md:w-4` |
| L53 删除钮 | 加 | `min-h-9 min-w-9 md:min-h-0 md:min-w-0` |
| L65 添加钮 | 加 | `min-h-10 md:min-h-0` |
| L77 保存钮 | 加 | `min-h-10 md:min-h-0` |

- [ ] **Step 2: ConfigSyncSettings**

| 位置 | 旧 | 新 |
|---|---|---|
| L45 table | `w-full text-sm` | `w-full max-md:min-w-[720px] text-sm` |
| L61-65 勾选框 | 加 | `h-5 w-5 md:h-4 md:w-4` |
| L82-84 操作按钮 | `!py-1 !px-2` | `!py-1.5 !px-2.5 md:!py-1 md:!px-2` |
| L9 头部按钮行 | `flex items-center gap-2` | `flex flex-wrap items-center gap-2` |
| L115 底部按钮行 | `mt-3 flex justify-end gap-2` | `mt-3 flex flex-wrap justify-end gap-2` |

- [ ] **Step 3: RulesSettings**

| 位置 | 旧 | 新 |
|---|---|---|
| L162 域名编辑弹层 | `absolute inset-x-0 top-full z-20 mt-2 rounded-2xl border border-slate-200 bg-white p-4 shadow-2xl` | `mt-2 rounded-2xl border border-slate-200 bg-white p-4 shadow-2xl md:absolute md:inset-x-0 md:top-full md:z-20`（余类保留） |
| L140 启用开关 label | `inline-flex cursor-pointer items-center gap-2 text-xs font-semibold` | 同串加 `min-h-9`，末尾加 ` md:min-h-0` |
| L188/L195 取消/保存 | `px-2.5 py-1.5 text-xs` | `px-3 py-2 text-xs md:px-2.5 md:py-1.5` |

- [ ] **Step 4: LogsSettings**

| 位置 | 旧 | 新 |
|---|---|---|
| L122-125 meta 行 | `mt-4 flex items-center justify-between text-xs text-slate-500 dark:text-slate-400` | `mt-4 flex flex-col gap-0.5 text-xs text-slate-500 dark:text-slate-400 sm:flex-row sm:items-center sm:justify-between` |
| L175 select | `rounded-md border-0 bg-white px-2.5 py-1.5 text-xs` | 同串前加 `min-h-10 `，末尾加 ` md:min-h-0` |

- [ ] **Step 5: AgentSettings**

| 位置 | 旧 | 新 |
|---|---|---|
| L60 dl 指标格 | `grid grid-cols-2 border-t` | `grid grid-cols-1 border-t sm:grid-cols-2`（`lg:grid-cols-4` 保留） |
| L70/L84 第 2/4 格 | `border-l` | `sm:border-l` |
| L191 弹窗刷新钮 | `rounded border border-slate-200 px-2 py-1 text-[10px]` | `inline-flex min-h-9 items-center rounded border border-slate-200 px-2.5 py-1 text-[10px] md:min-h-0 md:px-2` |

- [ ] **Step 6: 验证**

Run: `npx playwright test tests/e2e/mobile-responsive.spec.js && npm run build`
Expected: settings 无溢出用例全 PASS；build 成功。

---

### Task 7: 全量验证 + 中英双语走查 + 提交（用户门控）

- [ ] **Step 1: 单元测试与构建**

Run: `npm test && npm run build`
Expected: vitest 全绿；build 成功且 `dist/` 更新。

- [ ] **Step 2: e2e 全量**

Run: `npx playwright test`
Expected: mobile-responsive 全 PASS；auth-consistency 不回归。

- [ ] **Step 3: 中英双语人工走查（Playwright 截图）**

用 `npx playwright test --ui` 或临时脚本在 390×844 下对 en/zh 两种 locale（localStorage 持久化 i18n）截图：仪表盘（面板展开/收起）、设置 5 个子页、快速设置弹窗、证书弹窗、登录弹窗、Deep Mode 弹窗。核对：无横向溢出、按钮可达、文案不溢出（en 比 zh 宽 40-60%，以 en 为准校验）。再抽 dark 模式一组（仪表盘 + 登录弹窗 + 快速设置）核对深色样式；console 无新增错误（mobile 用例已断言 pageerror 为空）。

- [ ] **Step 4: 桌面回归目检**

1280×800 截图仪表盘与设置页，与 master 版本对比：布局零变化（aside 宽度、header server 卡、表格密度、图表尺寸）。

- [ ] **Step 5: 向用户汇报 diff 摘要，等用户决定提交**

### 收尾修正记录（整体评审 + 截图走查发现，均已修复）

1. **Critical**：styles.css 保留的 `@media (max-width: 768px)` 与 Tailwind `md` 在恰好 768px 撞车（iPad 竖屏主区高度 0）→ 改 `max-width: 767px`。
2. **阻断**：QuickSetupModal 右列缺 `min-h-0`，移动堆叠下内容 905px 不收缩被裁、底部按钮不可达 → 右列加 `min-h-0`。
3. **低危**：DashboardPage 记录表 `min-w-[880px]` 泄漏桌面（1280-1330 视口出横向滚动）→ `max-md:min-w-[880px]`。
4. e2e 补 768×1024 平板哨兵用例 + 390px QuickSetup 视口内/可滚动用例（堵住 768px 逃逸口）。

**接受的遗留润色项**（记录不修）：QuickSetupModal 横屏矮视口（<425px 高）壳层压缩（超出手机竖屏范围）；SoftwareUpdateNotice 关闭钮 36px；刷新仪表板按钮 ~32px 高（次级操作）；EN h1 折两行；弹窗高度约束三模式并存（flex-col+内滚 / 整卡滚 / vh 旧式 Cert）——未来新弹窗优先 flex-col+min-h-0 内滚模式；ConfigSyncSettings 组件当前未挂载（既有状况）。

Run: `git status && git diff --stat`
不自动 commit。用户明确要求后按规范提交（全中文标题，`新增：`/`修复：` 前缀，正文说明桌面零变化约束与验证结果，页脚带 Claude 署名行）。

---

## 风险与回退

- 每个任务结束跑一次 `npm run build`（2s）快速兜底；e2e 控制组（desktop 1280）是桌面零变化的自动化哨兵。
- CodeMirror 容器变化（Task 5 Step 3）是唯一可能影响功能行为的改动：真机/视口目检两编辑器重测量与「采用此块」浮钮定位；异常则回退该步并改用横向滚动方案（外层 `overflow-x-auto` + `min-w-[640px]`）。
- styles.css 全局规则删除（Task 4 Step 1）影响所有命中 `aside`/`main` 的页面：删除后必须完整走一遍设置页 5 个子页 + 仪表盘。
- 若 dvh 在目标环境异常（表现=约束失效），统一回退为 `vh` 版本（仅改类名后缀）。
