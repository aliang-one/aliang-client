# Agent 设置「设备互访」第 4 子 tab + navitem 样式改版 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在设置页 Agent 栏新增第 4 子 tab「设备互访」（键 `peer-access`）收编 PeerMCPSettings 卡 + 门禁占位卡，四个子 tab 样式改 navitem 下划线式；OtherSettings 瘦身保留（迁走设备互访两卡，留下 SystemSettings/AgentFileAccessSettings 原地不动）。

**Architecture:** 纯前端批次（app/website，Vue3 options API + Tailwind）。子 tab 状态真值源在导航层 `useNavigation.AGENT_SUB_TABS`（模块级 ref + 归一），AgentPage 只做词表副本 + 渲染分发；新内容组件 `PeerAccessSettings.vue` 收编两卡并透传 props/事件；样式只动 AgentPage 全仓唯一 tablist 的类名。

**Tech Stack:** Vue 3（options API，仓内风格）、Tailwind、vitest（jsdom，`createApp` 挂载手法，仓内无 @vue/test-utils）、Playwright（移动端哨兵）。

**Spec:** `docs/superpowers/specs/2026-11-04-agent-peer-access-tab-design.md`（已评审通过）

**提交纪律（项目 CLAUDE.md 覆盖技能默认）：** 全程**不自动 `git commit`**——实现与验证全部完成后向用户汇报，由用户决定是否提交。以下各任务的「Commit」步骤一律改为「向用户展示改动摘要」。

---

### Task 0: worktree 准备

**Files:** 无代码文件（环境准备）

- [x] **Step 1: 创建 worktree 分支**（项目约定：所有开发一律 worktree；`.worktrees/` 是本项目既有 worktree 家，已验证被 gitignore）

```bash
cd /Users/mac/MyProgram/GoProgram/nursor/alianggate
git check-ignore -q .worktrees   # IGNORED_OK
git worktree add .worktrees/peer-access-tab -b feature/agent-peer-access-tab master
```
已完成（2026-11-04，HEAD=eec587b）。

- [x] **Step 2: 安装前端依赖**（worktree 无 node_modules，主仓 cp 过去）

```bash
cp -R app/website/node_modules .worktrees/peer-access-tab/app/website/node_modules
```
已完成。

- [x] **Step 3: 基线验证**（改动前先证明现状是绿的，防止把预存失败当回归）

```bash
cd .worktrees/peer-access-tab/app/website && npx vitest run src/composables/useNavigation.test.js
```
已完成：10/10 PASS。

---

### Task 1: 导航层词表加 `peer-access`（TDD：先红后绿）

**Files:**
- Modify: `app/website/src/composables/useNavigation.test.js:86-88`
- Modify: `app/website/src/composables/useNavigation.js:14`

- [ ] **Step 1: 改测试（先红）**

`useNavigation.test.js` 第 86-88 行的钉死断言改为四键，并新增 peer-access 写入用例：

```js
  it('AGENT_SUB_TABS 词表与 AgentPage 子 tab 一一对应', () => {
    expect(AGENT_SUB_TABS).toEqual(['records', 'user-agent', 'aliangcode', 'peer-access']);
  });

  it('peer-access 是合法子 tab（深链写入不被归一）', () => {
    const nav = useNavigation();
    nav.showPage('agent', { sub: 'peer-access' });
    expect(nav.agentSubTab.value).toBe('peer-access');
  });
```

- [ ] **Step 2: 跑测试确认失败**

```bash
cd /Users/mac/MyProgram/GoProgram/nursor/alianggate/.worktrees/peer-access-tab/app/website
npx vitest run src/composables/useNavigation.test.js
```
Expected: FAIL（词表断言不匹配 + peer-access 被归一成 records）

- [ ] **Step 3: 改实现（最小改动）**

`useNavigation.js:14`：

```js
export const AGENT_SUB_TABS = ['records', 'user-agent', 'aliangcode', 'peer-access'];
```

（注释第 10-13 行「词表与 settings/agent/AgentPage.vue 的子 tab 一一对应」语义不变，不动。）

- [ ] **Step 4: 跑测试确认通过**

```bash
npx vitest run src/composables/useNavigation.test.js
```
Expected: PASS（含新用例）

---

### Task 2: i18n 两语加 `agent_subPeerAccess` + 三处描述文案校正

**Files:**
- Modify: `app/website/src/i18n/zh.js`（`agent_subAliangCode` 行后插入；`settings_otherDesc`、`settings_sectionAgentDesc` 文案更新）
- Modify: `app/website/src/i18n/en.js`（同位）

- [ ] **Step 1: zh.js 改动**

`agent_subAliangCode: 'aliangcode',` 行后插入：

```js
  agent_subPeerAccess: '设备互访',
```

`settings_sectionAgentDesc` 更新（设备互访迁入 Agent 栏后描述要如实）：

```js
  settings_sectionAgentDesc: '会话记录、用户态 Agent、aliangcode 内核与设备互访设置集中地。',
```

`settings_otherDesc` 更新（设备互访迁走后不再罗列）：

```js
  settings_otherDesc: '设备级开关与系统杂项：运行模式、文件访问授权。',
```

（`settings_sectionStatusDesc` 与 `settings_otherTitle/Desc` 键继续使用，不清键。）

- [ ] **Step 2: en.js 对应改动**

```js
  agent_subPeerAccess: 'Device Access',
```

```js
  settings_sectionAgentDesc: 'Session records, the user-mode agent, the aliangcode kernel, and device mesh settings in one place.',
```

```js
  settings_otherDesc: 'Device-level switches and system misc: run mode and file access authorization.',
```

- [ ] **Step 3: 快速验证**（i18n 无独立测试，由 Task 7 全量 vitest 兜底；本步只确认语法）

```bash
node -e "import('./src/i18n/zh.js').then(m=>console.log('zh ok')); import('./src/i18n/en.js').then(m=>console.log('en ok'))" 2>&1 | head -5
```
Expected: 输出 `zh ok` / `en ok`（或无语法报错；若 ESM 直导入受构建链限制，跳过本步由 vitest 兜底）

---

### Task 3: 新组件 PeerAccessSettings.vue + 挂载测试（TDD：先红后绿）

**Files:**
- Create: `app/website/src/components/settings/agent/PeerAccessSettings.test.js`
- Create: `app/website/src/components/settings/agent/PeerAccessSettings.vue`

- [ ] **Step 1: 写失败测试**

```js
// 设备互访子 tab 内容卡(2026-11-04 收编)。挂载用 createApp + jsdom 手法
// (仓内无 @vue/test-utils,vite.config 已配 jsdom);i18n 与 agentApi 必须mock:
// 前者避免 locale 依赖,后者阻止 PeerMCPSettings.mounted 的真实网络请求。
import { describe, expect, it, vi } from 'vitest';

vi.mock('../../../i18n', () => ({ useI18n: () => ({ t: (k) => k }) }));
vi.mock('../../../services/agentApi', () => ({
  getPeerMCPStatus: vi.fn(async () => ({ registration: [] })),
  setPeerMCPRegistration: vi.fn(async () => ({}))
}));

import { createApp, h } from 'vue';
import PeerAccessSettings from './PeerAccessSettings.vue';

const baseProps = { config: { peer_mcp: { enable: true } } };

function mountComp(props = {}, listeners = {}) {
  const el = document.createElement('div');
  const app = createApp({ render: () => h(PeerAccessSettings, { ...baseProps, ...props, ...listeners }) });
  app.mount(el);
  return el;
}

describe('PeerAccessSettings 收编卡', () => {
  it('渲染 PeerMCPSettings 卡(标题经 i18n 键透出)', () => {
    const el = mountComp();
    expect(el.textContent).toContain('peerMcp_title');
  });

  it('渲染门禁占位卡:两行开关均 disabled + comingSoon 徽章', () => {
    const el = mountComp();
    const boxes = el.querySelectorAll('input[type="checkbox"][disabled]');
    expect(boxes.length).toBeGreaterThanOrEqual(2);
    expect(el.querySelector('input[aria-label="settings_peerExecPendingTitle"]')).toBeTruthy();
    expect(el.querySelector('input[aria-label="settings_peerTransferPendingTitle"]')).toBeTruthy();
    expect(el.textContent.match(/settings_comingSoon/g).length).toBe(2);
  });

  it('save 事件两层透传:子卡 → 本卡 → 父页(payload 带 peer_mcp)', async () => {
    const onSave = vi.fn();
    const el = mountComp({}, { onSave });
    // PeerMCPSettings 的保存按钮是子卡内最后一个 button(两段式保存的①段入口)
    const buttons = el.querySelectorAll('button');
    const saveBtn = buttons[buttons.length - 1];
    saveBtn.click();
    await Promise.resolve();
    await Promise.resolve();
    expect(onSave).toHaveBeenCalledTimes(1);
    expect(onSave.mock.calls[0][0]).toEqual({ peer_mcp: { enable: true } });
  });
});
```

- [ ] **Step 2: 跑测试确认失败**

```bash
npx vitest run src/components/settings/agent/PeerAccessSettings.test.js
```
Expected: FAIL（`PeerAccessSettings.vue` 不存在，导入失败）

- [ ] **Step 3: 实现组件**（门禁占位卡模板从 `OtherSettings.vue:17-58` **原样**迁入，注释更新落位说明）

```vue
<template>
  <div class="flex flex-col gap-5">
    <!-- PeerMCPSettings 行为零改动，从状态栏页尾「其他」收编至此（设备互访家族
         集中，2026-11-04）。两段式保存链路不变：emit save → AgentPage → SettingsPage。 -->
    <PeerMCPSettings
      :config="config"
      :loading="loading"
      :saving="saving"
      :error="error"
      :success-message="successMessage"
      @save="emitSave"
    />
    <!-- 设备门禁开关（peer_exec_enabled / peer_fetch_enabled）：真值源在服务端
         设备注册表，写通道待跨仓（agent + PhoneServer）落地；先置灰占位零请求。 -->
    <div class="rounded-xl border border-slate-200 bg-white p-5 dark:border-slate-800 dark:bg-background-dark">
      <div class="space-y-5">
        <div class="flex items-center justify-between gap-3">
          <div class="min-w-0">
            <p class="flex flex-wrap items-center gap-2 text-sm font-semibold text-slate-900 dark:text-white">
              {{ t('settings_peerExecPendingTitle') }}
              <span class="rounded bg-slate-200 px-1.5 py-0.5 text-[10px] font-bold text-slate-500 dark:bg-slate-700 dark:text-slate-300">
                {{ t('settings_comingSoon') }}
              </span>
            </p>
            <p class="mt-0.5 text-[10px] leading-4 text-slate-500 dark:text-slate-400">
              {{ t('settings_peerExecPendingDesc') }}
            </p>
          </div>
          <input
            type="checkbox"
            disabled
            :aria-label="t('settings_peerExecPendingTitle')"
            class="h-4 w-4 shrink-0 cursor-not-allowed rounded border-slate-300 text-primary dark:border-slate-600 dark:bg-slate-900"
          />
        </div>
        <div class="flex items-center justify-between gap-3">
          <div class="min-w-0">
            <p class="flex flex-wrap items-center gap-2 text-sm font-semibold text-slate-900 dark:text-white">
              {{ t('settings_peerTransferPendingTitle') }}
              <span class="rounded bg-slate-200 px-1.5 py-0.5 text-[10px] font-bold text-slate-500 dark:bg-slate-700 dark:text-slate-300">
                {{ t('settings_comingSoon') }}
              </span>
            </p>
            <p class="mt-0.5 text-[10px] leading-4 text-slate-500 dark:text-slate-400">
              {{ t('settings_peerTransferPendingDesc') }}
            </p>
          </div>
          <input
            type="checkbox"
            disabled
            :aria-label="t('settings_peerTransferPendingTitle')"
            class="h-4 w-4 shrink-0 cursor-not-allowed rounded border-slate-300 text-primary dark:border-slate-600 dark:bg-slate-900"
          />
        </div>
      </div>
    </div>
  </div>
</template>

<script>
// 注意路径：本组件在 settings/agent/ 下，PeerMCPSettings 在 settings/ 上一级。
import PeerMCPSettings from '../PeerMCPSettings.vue';
import { useI18n } from '../../../i18n';

export default {
  name: 'PeerAccessSettings',
  components: { PeerMCPSettings },
  props: {
    config: { type: Object, default: () => ({}) },
    loading: { type: Boolean, default: false },
    saving: { type: Boolean, default: false },
    error: { type: String, default: '' },
    successMessage: { type: String, default: '' }
  },
  emits: ['save'],
  setup() {
    const { t } = useI18n();
    return { t };
  },
  methods: {
    emitSave(payload) {
      this.$emit('save', payload);
    }
  }
};
</script>
```

- [ ] **Step 4: 跑测试确认通过**

```bash
npx vitest run src/components/settings/agent/PeerAccessSettings.test.js
```
Expected: PASS（3 用例；若 save 透传用例因 jsdom 定时问题抖动，允许给断言前补一次 `await new Promise(r=>setTimeout(r,0))`）

---

### Task 4: AgentPage 接入第 4 子 tab + navitem 样式改版

**Files:**
- Modify: `app/website/src/components/settings/agent/AgentPage.vue`（tablist :5-20、词表 :104-108、新渲染块、import、components 注册）

- [ ] **Step 1: tablist 模板改 navitem 下划线式**（:5-20 整块替换；结构 role/aria/switchTab 不动，只换类名）

```vue
    <div class="flex flex-wrap items-center gap-x-5 gap-y-2 border-b border-slate-200 dark:border-slate-700" role="tablist" :aria-label="t('settings_tabAgent')">
      <button
        v-for="tab in agentSubTabs"
        :key="tab.key"
        type="button"
        role="tab"
        :aria-selected="active === tab.key"
        class="min-h-10 border-b-2 px-1 py-2 text-sm font-medium transition"
        :class="active === tab.key
          ? 'border-primary text-primary'
          : 'border-transparent text-slate-500 hover:text-slate-700 dark:text-slate-300 dark:hover:text-slate-100'"
        @click="switchTab(tab.key)"
      >
        {{ t(tab.labelKey) }}
      </button>
    </div>
```

（要点：`min-h-10` 触控目标保留；非激活垫 `border-transparent` 防切换跳动；`flex-wrap` 保留供 390px 哨兵兜底。）

- [ ] **Step 2: 渲染分发加第 4 块**（在 `v-else-if="active === 'user-agent'"` 块之后、aliangcode 注释块之前插入；aliangcode 仍为 `v-else` 兜底尾块）

```vue
    <!-- 设备互访：customerConfig 依赖登录态的卡片（PeerMCPSettings 两段式保存
         需父页 config/save 透传；门禁占位卡为纯展示）。 -->
    <template v-else-if="active === 'peer-access'">
      <PeerAccessSettings
        :config="config"
        :loading="loading"
        :saving="saving"
        :error="error"
        :success-message="successMessage"
        @save="emitSave"
      />
    </template>
```

- [ ] **Step 3: script 三处接线**

import 区（`CustomEnvSettings` 行后）加：

```js
import PeerAccessSettings from './PeerAccessSettings.vue';
```

`agentSubTabs` 词表（:104-108）加第 4 项：

```js
const agentSubTabs = [
  { key: 'records', labelKey: 'agent_subRecords' },
  { key: 'user-agent', labelKey: 'agent_subUserAgent' },
  { key: 'aliangcode', labelKey: 'agent_subAliangCode' },
  { key: 'peer-access', labelKey: 'agent_subPeerAccess' }
];
```

`components: {}` 注册加 `PeerAccessSettings`。

- [ ] **Step 4: 快速验证**

```bash
npx vitest run src/composables/useNavigation.test.js src/components/settings/agent/PeerAccessSettings.test.js
```
Expected: PASS

---

### Task 5: OtherSettings 瘦身

**Files:**
- Modify: `app/website/src/components/settings/OtherSettings.vue`

- [ ] **Step 1: 删设备互访两卡**——删除：PeerMCPSettings 挂载块（:4-12，含注释）、门禁占位卡整块（:15-58，含注释）、`import PeerMCPSettings`（:63）、components 注册中的 `PeerMCPSettings`；保留：SectionHeader（:3）、SystemSettings（:13）、AgentFileAccessSettings（:14）及其 import/注册（**原地不动，全仓无其他挂载点**）。props 中 `config/loading/saving/error/successMessage` 与 `emits:['save']` 保留（SystemSettings/AgentFileAccessSettings 不用它们，但 SettingsPage 传参形状不动最安全）。

瘦身后模板骨架：

```vue
  <div class="flex flex-col gap-5">
    <SectionHeader :title="t('settings_otherTitle')" :desc="t('settings_otherDesc')" scope="device" />
    <SystemSettings />
    <AgentFileAccessSettings />
  </div>
```

- [ ] **Step 2: 残留检查**

```bash
grep -n "PeerMCP\|peerExec\|peerTransfer" src/components/settings/OtherSettings.vue
```
Expected: 零输出

- [ ] **Step 3: 全量 vitest**

```bash
npx vitest run
```
Expected: PASS（含既有全部用例 + 新增用例）

---

### Task 6: 终验（vitest 全绿 + 移动端哨兵 + 结构断言）

- [ ] **Step 1: 全量 vitest 复跑**

```bash
cd /Users/mac/MyProgram/GoProgram/nursor/alianggate/.worktrees/peer-access-tab/app/website
npx vitest run
```
Expected: PASS

- [ ] **Step 2: 移动端哨兵**（390/768/1280 溢出 + 四栏 tab 结构）

```bash
npx playwright test tests/e2e/mobile-responsive.spec.js
```
Expected: PASS（:87 断言顶层 4 tab 不变；子 tab 未被该 spec 直点，靠 390px 溢出检查兜底）

- [ ] **Step 3: 结构自检**（不依赖 e2e 的静态确认）

```bash
grep -c "peer-access" src/composables/useNavigation.js src/components/settings/agent/AgentPage.vue src/components/settings/agent/PeerAccessSettings.test.js
grep -c "agent_subPeerAccess" src/i18n/zh.js src/i18n/en.js src/components/settings/agent/AgentPage.vue
```
Expected: 三文件各 ≥1；三文件各 ≥1

- [ ] **Step 4: 向用户汇报改动摘要，等用户决定是否提交**（项目 CLAUDE.md：不自动 commit）

---

## 风险锚点（实现者必读）

1. **词表四处同步**：useNavigation.js（Task 1）+ 词表副本/渲染块（Task 4）+ i18n（Task 2）+ 钉死断言（Task 1）——漏任一处会被 `normalizeAgentSubTab` **静默归回 records 不报错**。
2. **两段式保存不得变形**：PeerMCPSettings 的 emit save → 父页持久化、POST `/api/agent/peer/mcp` 应用注册、applyError 独立红显——本批次只改挂载位置，`PeerMCPSettings.vue` 文件**零改动**。
3. **OtherSettings 只瘦身不删除**：SystemSettings / AgentFileAccessSettings 全仓无其他挂载点。
4. **构建验证陷阱**：4174 旧 dev server 会供应旧构建，如需起 dev server 冒烟先确认端口干净。
5. **i18n 键纪律**：新键走 `agent_subPeerAccess`（子 tab 命名空间），勿与顶导航 `settings_tab*` 混址。
