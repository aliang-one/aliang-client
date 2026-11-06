# 设置页四栏重组 Implementation Plan

> **执行状态(2026-11-03):全部任务已执行完毕,改动留在 worktree `feature/agent-settings-restructure` 未提交(等用户指示)。**
> 验证:vitest 258/258 绿;playwright mobile-responsive 10/10 绿;全量 e2e 54 过 1 挂——`chat-device-sessions › ai.run.notice tool_timeout` **master 基线同样复现(预存失败,非回归)**;`vite build` 成功。
> 执行偏差记录:
> 1. plan reviewer 五处修正已消化(AgentPage 全 `../` import/登录占位收进 aliangcode 子 tab/`scopedSessions` 实名保留/逐 prop 绑定/run-mode 锚点 grep 预期 3 处)
> 2. `settings_tabAgent` 首轮漏改(该键不在 562-564 区块,e2e h1 断言抓到),已补
> 3. master 执行期间前进 e42661f→4a978f3(纯后端 peer-defect-batch),与本改动零交集,无冲突

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 设置页从 5 tab 重组为四栏「代理设置 → Agent(三子 tab) → 状态(含页尾其他) → 用户」,纯前端,零后端改动。

**Architecture:** SettingsPage 瘦身为四个薄 section;Agent 三子 tab 抽独立组件 AgentPage.vue(子 tab 状态入导航层 useNavigation,支持深链定向跳转);状态页尾新增 OtherSettings.vue 收拢设备级杂项(含 PeerMCPSettings 迁入与两个置灰占位开关);记录页的「仅 aliangcode」开关升级为 agent 类型筛选。

**Tech Stack:** Vue 3 Options API(SFC)、Tailwind、vitest(无 VTU,组件测试=SFC 导出纯函数 + vi.mock i18n)、Playwright(chrome channel,webServer=vite dev :4174)。

**Spec:** `docs/superpowers/specs/2026-11-03-agent-settings-restructure-design.md`

**Worktree:** `/Users/mac/MyProgram/GoProgram/nursor/alianggate/.worktrees/agent-settings-restructure`(branch `feature/agent-settings-restructure`,基线 master@e42661f)。所有命令在 `app/website/` 下执行。

**提交纪律(CLAUDE.md):** 全程**不自动 commit**;所有改动留在 worktree,完成后向用户报告并等明确指示再提交。

**基线事实(实现者必读):**
- 现有 5 tab:settings(模型与推理)/capability(能力接入)/agent(设备与网络)/user(账户与界面)/log(诊断),内部 key 保持不变,只改显示文案。
- `capability` 区现含:AgentMCPGlobalSettings、AgentSkillsGlobalSettings、**PeerMCPSettings**(09acab4 新入,在 2 列网格里)、AgentIntegrationsSettings、CustomEnvSettings。
- 组件测试范式见 `src/components/settings/RulesSettings.test.js`:顶部 `vi.mock('../../i18n', ...)` 后从 SFC 导入纯函数。
- e2e 哨兵 `tests/e2e/mobile-responsive.spec.js` 有 **5 处**断言会因本改动而断(行 86 计数、行 104/113/177/182 标题),Task 8 必须同步改。

---

### Task 1: 导航层 agentSubTab(深链子 tab)

**Files:**
- Modify: `app/website/src/composables/useNavigation.js`
- Test: `app/website/src/composables/useNavigation.test.js`

- [ ] **Step 1: 写失败测试**

在 `useNavigation.test.js` 追加(文件顶部 import 已有 `useNavigation`,补 `AGENT_SUB_TABS`):

```js
import { AGENT_SUB_TABS, useNavigation } from './useNavigation';

describe('useNavigation agentSubTab', () => {
  beforeEach(() => {
    const nav = useNavigation();
    nav.showPage('agent', { sub: 'records' });
    nav.showDashboard();
  });

  it('showPage("agent", { sub }) 写入合法子 tab', () => {
    const nav = useNavigation();
    nav.showPage('agent', { sub: 'aliangcode' });
    expect(nav.currentPage.value).toBe('agent');
    expect(nav.agentSubTab.value).toBe('aliangcode');
  });

  it('未知 sub 归一为 records(深链 fallback)', () => {
    const nav = useNavigation();
    nav.showPage('agent', { sub: 'bogus' });
    expect(nav.agentSubTab.value).toBe('records');
  });

  it('无 sub 参数保留现状(仪表盘跳转不打断用户所在子 tab)', () => {
    const nav = useNavigation();
    nav.showPage('agent', { sub: 'aliangcode' });
    nav.showDashboard();
    nav.showPage('agent');
    expect(nav.agentSubTab.value).toBe('aliangcode');
  });

  it('非 agent 页不改写子 tab', () => {
    const nav = useNavigation();
    nav.showPage('agent', { sub: 'user-agent' });
    nav.showPage('settings', { sub: 'records' });
    expect(nav.agentSubTab.value).toBe('user-agent');
  });

  it('AGENT_SUB_TABS 词表与 AgentPage 子 tab 一一对应', () => {
    expect(AGENT_SUB_TABS).toEqual(['records', 'user-agent', 'aliangcode']);
  });
});
```

- [ ] **Step 2: 跑测试确认失败**

Run: `cd /Users/mac/MyProgram/GoProgram/nursor/alianggate/.worktrees/agent-settings-restructure/app/website && npx vitest run src/composables/useNavigation.test.js`
Expected: FAIL(`AGENT_SUB_TABS is not exported` / `agentSubTab undefined`)

- [ ] **Step 3: 实现 useNavigation.js**

在 `const chatProviderPreset = ref('');` 之后加:

```js
// Agent 页子 tab(模块级,与 currentPage 同生命周期,浏览器刷新自然重置)。
// 词表与 settings/agent/AgentPage.vue 的子 tab 一一对应;非法值写入时归一
// 为默认,深链安全。仅 showPage('agent', { sub }) 显式携带时才改写——仪表盘
// showAgent() 一类无参跳转不打断用户当前所在子 tab。
export const AGENT_SUB_TABS = ['records', 'user-agent', 'aliangcode'];
const agentSubTab = ref('records');

function normalizeAgentSubTab(sub) {
  return AGENT_SUB_TABS.includes(sub) ? sub : 'records';
}
```

`showPage` 改为:

```js
function showPage(page, options = {}) {
  currentPage.value = page;
  if (page === 'agent' && options && options.sub !== undefined) {
    agentSubTab.value = normalizeAgentSubTab(options.sub);
  }
}
```

return 对象加 `agentSubTab`(放在 `chatProviderPreset` 之后)。其余 show* 函数不动(它们最终都走 showPage)。

- [ ] **Step 4: 跑测试确认通过**

Run: `npx vitest run src/composables/useNavigation.test.js`
Expected: PASS(新旧全部)

---

### Task 2: i18n 改名与新增(死键删除推迟到 Task 6)

**Files:**
- Modify: `app/website/src/i18n/zh.js`
- Modify: `app/website/src/i18n/en.js`

- [ ] **Step 1: zh.js 改名四键**(行 562-564、601)

```js
settings_tabSettings: '代理设置',      // 原 '模型与推理'
settings_tabUserCenter: '用户',        // 原 '账户与界面'
settings_tabLogs: '状态',              // 原 '诊断'
settings_tabAgent: 'Agent',            // 原 '设备与网络'
```

- [ ] **Step 2: zh.js 分区描述改造**

原 `settings_sectionModelDesc`/`settings_sectionDeviceDesc`/`settings_sectionDiagnosticsDesc` 语义已漂移,改为:

```js
settings_sectionProxyDesc: '出站代理、AI 规则、分流规则与模型映射。仅影响新会话与代理层。',
settings_sectionAgentDesc: '会话记录、用户态 Agent 与 aliangcode 内核设置集中地。',
settings_sectionStatusDesc: '只读观测:实时日志;页尾「其他」为设备级开关杂项。',
```

(删旧三键;`settings_sectionCapabilityDesc`/`settings_sectionAccountDesc` 本轮不动,前者 Task 6 删。)

- [ ] **Step 3: zh.js 新增键**(放在 `settings_tabAgent` 附近与 agent_ 区、status 区就近处)

```js
// Agent 子 tab(SettingsPage agent 区新导航)
agent_subRecords: '记录',
agent_subUserAgent: '用户态 agent',
agent_subAliangCode: 'aliangcode',
// 记录页 agent 类型筛选(替代 仅 AliangCode 开关)
agent_activity_filterLabel: 'Agent 类型',
agent_activity_filterAll: '全部',
// 状态页尾「其他」区块
settings_otherTitle: '其他',
settings_otherDesc: '设备级开关与系统杂项:设备互访、运行模式、文件访问授权。',
// 置灰占位开关(设备门禁层,写通道下一轮跨仓落地)
settings_peerExecPendingTitle: '跨设备执行',
settings_peerExecPendingDesc: '允许其他设备在本机执行命令的设备级门禁。',
settings_peerTransferPendingTitle: '文件传输',
settings_peerTransferPendingDesc: '允许跨设备拉取本机文件的设备级门禁。',
settings_comingSoon: '即将上线',
```

- [ ] **Step 4: en.js 对应改**(同键位)

```js
settings_tabSettings: 'Proxy',
settings_tabUserCenter: 'User',
settings_tabLogs: 'Status',
settings_tabAgent: 'Agent',
settings_sectionProxyDesc: 'Outbound proxy, AI rules, routing rules and model mapping. Applies to new sessions and the proxy layer.',
settings_sectionAgentDesc: 'Session records, the user-mode agent, and the aliangcode kernel settings in one place.',
settings_sectionStatusDesc: 'Read-only observation: live logs. Device-level switches live in Other at the bottom.',
agent_subRecords: 'Records',
agent_subUserAgent: 'User-mode agent',
agent_subAliangCode: 'aliangcode',
agent_activity_filterLabel: 'Agent type',
agent_activity_filterAll: 'All',
settings_otherTitle: 'Other',
settings_otherDesc: 'Device-level switches and system misc: peer access, run mode, file access authorization.',
settings_peerExecPendingTitle: 'Cross-device execution',
settings_peerExecPendingDesc: 'Device-level gate for letting other devices run commands here.',
settings_peerTransferPendingTitle: 'File transfer',
settings_peerTransferPendingDesc: 'Device-level gate for letting other devices fetch files from this machine.',
settings_comingSoon: 'Coming soon',
```

- [ ] **Step 5: i18n 测试与全量 vitest**

Run: `npx vitest run src/i18n/i18n.test.js`
Expected: PASS(若该测试断言 zh/en 键集合一致,新增键两侧都必须加)

---

### Task 3: 记录页 agent 类型筛选(替代「仅 AliangCode」)

**Files:**
- Modify: `app/website/src/components/settings/AgentActivitySettings.vue`
- Test(create): `app/website/src/components/settings/AgentActivitySettings.test.js`

- [ ] **Step 1: 写失败测试**(RulesSettings.test.js 范式)

```js
import { describe, expect, it, vi } from 'vitest';

// SFC 模块求值会执行 i18n loadLocale()(读 localStorage,node 环境无 DOM),
// mock 掉后只测 matchesAgentFilter 纯函数。
vi.mock('../../i18n', () => ({ useI18n: () => ({ t: (k) => k }) }));

import { matchesAgentFilter } from './AgentActivitySettings.vue';

describe('matchesAgentFilter 记录页 agent 类型筛选', () => {
  it('all 恒真', () => {
    expect(matchesAgentFilter({ provider: 'claudecode' }, 'all')).toBe(true);
    expect(matchesAgentFilter({ provider: 'anything-unknown' }, 'all')).toBe(true);
  });

  it('aliangcode 走 isAliangCodeSession 同源判据', () => {
    expect(matchesAgentFilter({ provider: 'agentcore' }, 'aliangcode')).toBe(true);
    expect(matchesAgentFilter({ provider: 'claudecode' }, 'aliangcode')).toBe(false);
  });

  it('claude 包含 claudecode 别名', () => {
    expect(matchesAgentFilter({ provider: 'claudecode' }, 'claude')).toBe(true);
    expect(matchesAgentFilter({ provider: 'agentcore' }, 'claude')).toBe(false);
  });

  it('codex / opencode 按名匹配', () => {
    expect(matchesAgentFilter({ provider: 'codex' }, 'codex')).toBe(true);
    expect(matchesAgentFilter({ provider: 'opencode' }, 'opencode')).toBe(true);
    expect(matchesAgentFilter({ provider: 'codex' }, 'claude')).toBe(false);
  });

  it('tool 字段优先于 provider(session.tool 存在时)', () => {
    expect(matchesAgentFilter({ tool: 'claude', provider: 'agentcore' }, 'claude')).toBe(true);
  });

  it('空会话恒 false', () => {
    expect(matchesAgentFilter(null, 'all')).toBe(false);
  });
});
```

- [ ] **Step 2: 跑测试确认失败**

Run: `npx vitest run src/components/settings/AgentActivitySettings.test.js`
Expected: FAIL(`matchesAgentFilter` not exported)

- [ ] **Step 3: 实现**

`AgentActivitySettings.vue` `<script>`:在现有 import `isAliangCodeSession` 之后导出纯函数(放 import 块与组件定义之间):

```js
// 记录页 agent 类型筛选(2026-11-03 设置页四栏重组):aliangcode 判据与
// 聊天窗同源(isAliangCodeSession),其余按 tool/provider 名包含匹配;
// 未知来源仅在「全部」可见。导出纯函数供单测(RulesSettings 范式)。
export function matchesAgentFilter(session, filter) {
  if (!session) return false;
  if (filter === 'all') return true;
  if (filter === 'aliangcode') return isAliangCodeSession(session);
  const name = String(session.tool || session.provider || '').toLowerCase();
  if (filter === 'claude') return name.includes('claude');
  if (filter === 'codex') return name.includes('codex');
  if (filter === 'opencode') return name.includes('opencode');
  return false;
}
```

`data()`:`onlyAliangCode: true` → `agentFilter: 'all'`(默认全量,与「记录各种 coding agent」的新定位一致)。

两个 computed(行 ~231-238):

```js
filteredSessions() {
  return this.sessions.filter((session) => matchesAgentFilter(session, this.agentFilter));
},
filteredRuntimeAi() {
  return this.runtimeAi.filter((session) => matchesAgentFilter(session, this.agentFilter));
},
```

模板:把行 14-25 的 checkbox `<label>`(仅 AliangCode)整体替换为:

```html
<label
  class="inline-flex min-h-11 items-center gap-1.5 rounded-lg border border-slate-200 px-3 text-xs font-semibold text-slate-600 dark:border-slate-700 dark:text-slate-300"
>
  <span>{{ t('agent_activity_filterLabel') }}</span>
  <select
    v-model="agentFilter"
    class="h-8 rounded border border-slate-200 bg-transparent px-1 text-xs dark:border-slate-700"
    :aria-label="t('agent_activity_filterLabel')"
  >
    <option value="all">{{ t('agent_activity_filterAll') }}</option>
    <option value="claude">Claude</option>
    <option value="codex">Codex</option>
    <option value="aliangcode">AliangCode</option>
    <option value="opencode">OpenCode</option>
  </select>
</label>
```

(消费 `filteredSessions`/`filteredRuntimeAi` 的地方若原名引用 `onlyAliangCodeSessions` 等旧 computed 名,以实际代码为准改名,保持引用一致。)

- [ ] **Step 4: 跑测试确认通过**

Run: `npx vitest run src/components/settings/AgentActivitySettings.test.js`
Expected: PASS

---

### Task 4: AgentPage.vue 三子 tab 容器

**Files:**
- Create: `app/website/src/components/settings/agent/AgentPage.vue`

无独立单测(无 VTU;子 tab 词表已在 Task 1 钉死),以 Task 8 全量测试+e2e 兜底。

- [ ] **Step 1: 创建组件**

```vue
<template>
  <div class="flex flex-col gap-5">
    <!-- 子 tab:词表与 useNavigation.AGENT_SUB_TABS 一一对应,状态在导航层,
         支持 showPage('agent', { sub }) 深链定向(2026-11-03 四栏重组)。 -->
    <div class="flex flex-wrap gap-2" role="tablist" :aria-label="t('settings_tabAgent')">
      <button
        v-for="tab in agentSubTabs"
        :key="tab.key"
        type="button"
        role="tab"
        :aria-selected="active === tab.key"
        class="min-h-10 rounded-full px-4 text-xs font-bold transition"
        :class="active === tab.key
          ? 'bg-primary text-white shadow-sm'
          : 'bg-slate-100 text-slate-600 hover:bg-slate-200 dark:bg-slate-800 dark:text-slate-300 dark:hover:bg-slate-700/70'"
        @click="active = tab.key"
      >
        {{ t(tab.labelKey) }}
      </button>
    </div>

    <template v-if="active === 'records'">
      <AgentActivitySettings />
      <AgentRuntimeSessions />
    </template>

    <template v-else-if="active === 'user-agent'">
      <AgentSettings />
      <AgentScanDirectoriesSettings />
      <AgentIntegrationsSettings />
    </template>

    <template v-else>
      <AgentCoreSettings
        :config="config"
        :loading="loading"
        :saving="saving"
        :error="error"
        :success-message="successMessage"
        @save="$emit('save', $event)"
      />
      <div class="grid grid-cols-1 items-stretch gap-5 lg:grid-cols-2">
        <AgentMCPGlobalSettings
          :config="config"
          :loading="loading"
          :saving="saving"
          :error="error"
          :success-message="successMessage"
          @save="$emit('save', $event)"
        />
        <AgentSkillsGlobalSettings
          :config="config"
          :loading="loading"
          :saving="saving"
          :error="error"
          :success-message="successMessage"
          @save="$emit('save', $event)"
        />
      </div>
      <CustomEnvSettings
        :config="config"
        :loading="loading"
        :saving="saving"
        :error="error"
        :success-message="successMessage"
        @save="$emit('save', $event)"
      />
    </template>
  </div>
</template>

<script>
import AgentActivitySettings from './AgentActivitySettings.vue';
import AgentRuntimeSessions from '../AgentRuntimeSessions.vue';
import AgentSettings from '../AgentSettings.vue';
import AgentScanDirectoriesSettings from '../AgentScanDirectoriesSettings.vue';
import AgentIntegrationsSettings from '../AgentIntegrationsSettings.vue';
import AgentCoreSettings from '../AgentCoreSettings.vue';
import AgentMCPGlobalSettings from '../AgentMCPGlobalSettings.vue';
import AgentSkillsGlobalSettings from '../AgentSkillsGlobalSettings.vue';
import CustomEnvSettings from '../CustomEnvSettings.vue';
import { AGENT_SUB_TABS } from '../../../composables/useNavigation';
import { useI18n } from '../../../i18n';

// 子 tab 词表(labels),key 来源唯一:useNavigation.AGENT_SUB_TABS。
const agentSubTabs = [
  { key: 'records', labelKey: 'agent_subRecords' },
  { key: 'user-agent', labelKey: 'agent_subUserAgent' },
  { key: 'aliangcode', labelKey: 'agent_subAliangCode' }
];

export default {
  name: 'AgentPage',
  components: {
    AgentActivitySettings,
    AgentRuntimeSessions,
    AgentSettings,
    AgentScanDirectoriesSettings,
    AgentIntegrationsSettings,
    AgentCoreSettings,
    AgentMCPGlobalSettings,
    AgentSkillsGlobalSettings,
    CustomEnvSettings
  },
  props: {
    config: { type: Object, default: () => ({}) },
    loading: { type: Boolean, default: false },
    saving: { type: Boolean, default: false },
    error: { type: String, default: '' },
    successMessage: { type: String, default: '' }
  },
  emits: ['save'],
  setup() {
    const { agentSubTab } = useNavigation();
    const { t } = useI18n();
    return { agentSubTab, t, agentSubTabs };
  },
  computed: {
    active() {
      // 词表防御:导航层已归一,这里兜底未知值回默认(records)。
      return agentSubTabs.some((tab) => tab.key === this.agentSubTab) ? this.agentSubTab : 'records';
    }
  }
};
</script>
```

注意:同目录组件(AgentActivitySettings)用 `./`,跨到 settings/ 下的用 `../`;`AGENT_SUB_TABS` 导入仅为对齐文档性引用(computed 未直接用),若 lint 报 unused 则删掉该 import 并在注释保留词表出处说明。

- [ ] **Step 2: 语法冒烟**

Run: `npx vite build 2>&1 | tail -3`
Expected: 构建成功(此时 AgentPage 尚未被引用,仅验证 SFC 语法)

---

### Task 5: OtherSettings.vue(状态页尾「其他」区块)

**Files:**
- Create: `app/website/src/components/settings/OtherSettings.vue`

- [ ] **Step 1: 创建组件**

```vue
<template>
  <div class="flex flex-col gap-5">
    <SectionHeader :title="t('settings_otherTitle')" :desc="t('settings_otherDesc')" scope="device" />
    <!-- PeerMCPSettings 行为零改动,仅从原「能力接入」落位至此(设备互访家族集中)。 -->
    <PeerMCPSettings
      :config="config"
      :loading="loading"
      :saving="saving"
      :error="error"
      :success-message="successMessage"
      @save="$emit('save', $event)"
    />
    <SystemSettings />
    <AgentFileAccessSettings />
    <!-- 设备门禁开关(peer_exec_enabled / peer_fetch_enabled):真值源在服务端
         设备注册表,写通道待跨仓(agent + PhoneServer)落地;先置灰占位零请求。 -->
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
          <input type="checkbox" disabled :aria-label="t('settings_peerExecPendingTitle')" class="h-4 w-4 shrink-0 rounded border-slate-300 text-primary dark:border-slate-600 dark:bg-slate-900" />
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
          <input type="checkbox" disabled :aria-label="t('settings_peerTransferPendingTitle')" class="h-4 w-4 shrink-0 rounded border-slate-300 text-primary dark:border-slate-600 dark:bg-slate-900" />
        </div>
      </div>
    </div>
  </div>
</template>

<script>
import PeerMCPSettings from './PeerMCPSettings.vue';
import SystemSettings from './SystemSettings.vue';
import AgentFileAccessSettings from './AgentFileAccessSettings.vue';
import SectionHeader from './SectionHeader.vue';
import { useI18n } from '../../i18n';

export default {
  name: 'OtherSettings',
  components: { PeerMCPSettings, SystemSettings, AgentFileAccessSettings, SectionHeader },
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
  }
};
</script>
```

---

### Task 6: SettingsPage 四栏重组(主改)

**Files:**
- Modify: `app/website/src/components/SettingsPage.vue`

- [ ] **Step 1: 桌面导航(行 16-57)改四项**

删除 capability `<a>`(行 41-48);其余四项文字绑定不变(key 未变,Task 2 已改文案)。**顺序核对:settings → agent → log → user**(即 代理设置→Agent→状态→用户;现状桌面顺序是 settings,user,log,capability,agent,要重排成上述顺序)。

- [ ] **Step 2: 移动网格(行 93-134)同步**

`grid-cols-2` 网格改 4 个按钮,顺序同上;第 2 个按钮文案用 `t('settings_tabAgent')`(现状是 `settings_pageUser`,新结构里第二个是 Agent,用户在第四位用 `settings_pageUser` 或 `settings_tabUserCenter` 统一取后者)。现有 5 个按钮删 capability。

- [ ] **Step 3: section 重组**

- `settings` section(行 146-177):保留登录占位;`<template v-else>` 内改为 `ModelMappingSettings` + `RulesSettings`(带 `:preset-providers` 等现有 props,从 agent section 原样搬);SectionHeader desc 键改 `settings_sectionProxyDesc`。
- **删除** `capability` section(行 179-230)。
- `agent` section(行 232-264):保留登录占位;`<template v-else>` 内只留 `<AgentPage v-bind="agentPageProps" @save="saveCustomerConfig" />`(或逐 prop 绑定与 Task 4 props 对齐);SectionHeader desc 键改 `settings_sectionAgentDesc`。
- `log` section(行 273-292):保留登录占位;`<template v-else>` 内改为 `LogsSettings` + `<OtherSettings v-bind=... @save="saveCustomerConfig" />`;SectionHeader desc 键改 `settings_sectionStatusDesc`。
- `user` section 不动。

- [ ] **Step 4: script 清理**

- imports:删 AgentCoreSettings/AgentMCPGlobalSettings/AgentSkillsGlobalSettings/CustomEnvSettings/PeerMCPSettings/AgentSettings/AgentRuntimeSessions/AgentActivitySettings/AgentScanDirectoriesSettings/AgentIntegrationsSettings/AgentFileAccessSettings/SystemSettings;新增 `import AgentPage from './settings/agent/AgentPage.vue';` 与 `import OtherSettings from './settings/OtherSettings.vue';`。保留:RulesSettings/UserInfoSettings/LogsSettings/AppearanceSettings/ModelMappingSettings/SectionHeader 与 normalize 工具(`normalizePeerMCPConfig` 仍在 SettingsPage 的归一层,勿删)。
- `components` 注册表同步。
- `isVisible`(行 ~575):`['settings', 'capability', 'user', 'log', 'agent']` → `['settings', 'agent', 'log', 'user']`。
- `pageTitle`(行 ~563-578):删 capability 分支;其余四分支保留(agent 分支已在)。
- `presetProviders`/`loadPresetProviders`/customerConfig 全套逻辑不动。

- [ ] **Step 5: 死键清扫**(改完模板后 grep 逐个确认零引用再删)

Run: `grep -rn "settings_tabCapability\|settings_sectionCapabilityDesc\|settings_loginRequiredCapability\|settings_sectionModelDesc\|settings_sectionDeviceDesc\|settings_sectionDiagnosticsDesc\|agent_activity_onlyAliangCode\|settings_pageUser" app/website/src`
- 4 个 capability 登录占位键 + `agent_activity_onlyAliangCode(Hint)` + `settings_sectionModelDesc/DeviceDesc/DiagnosticsDesc` 应零引用 → 从 zh/en 删除。
- `settings_pageUser`:grep 若仍有引用(如 DashboardPage)则保留,否则删。
- `settings_loginRequiredAgent`(agent 区占位仍在用)保留。

- [ ] **Step 6: 全量 vitest + build**

Run: `npx vitest run && npx vite build 2>&1 | tail -3`
Expected: 全部 PASS + 构建成功

---

### Task 7: 仪表盘引导跳转改道

**Files:**
- Modify: `app/website/src/components/DashboardPage.vue`(行 ~1037、1705-1713)

- [ ] **Step 1: 更新 handleGuideOpenSettings**

```js
function handleGuideOpenSettings() {
  // 运行模式随 2026-11-03 四栏重组落位「状态」页页尾「其他」(SystemSettings
  // 随迁),引导跳状态页,滚动锚点 data-guide="run-mode" 不变。
  showPage('log');
  void nextTick(() => {
    document.querySelector('[data-guide="run-mode"]')?.scrollIntoView({ block: 'center', behavior: 'smooth' });
  });
}
```

`useNavigation()` 解构(行 1037)加 `showPage`(保留 showSettings/showAgent/showChat——`handleShowAgent` 顶栏入口仍跳 agent 页,不动)。

- [ ] **Step 2: 锚点唯一性断言**

Run: `grep -rn 'data-guide="run-mode"' app/website/src`
Expected: 仅 `SystemSettings.vue` 一处

---

### Task 8: e2e 断言同步

**Files:**
- Modify: `app/website/tests/e2e/mobile-responsive.spec.js`

- [ ] **Step 1: 改 5 处断言**

- 行 ~86-88:`expect(count).toBeGreaterThanOrEqual(5);` → `expect(count).toBe(4);`(四栏 2×2 网格;注释同步为「四栏重组」)
- 行 ~104、113(mobile Agent 入口用例):`toContainText(/设备与网络|Device & Network/)` → `toContainText(/Agent/)`(两处)
- 行 ~177、182(desktop 用例):同上改 `/Agent/`(两处)

- [ ] **Step 2: 跑 mobile e2e**

Run: `npx playwright test tests/e2e/mobile-responsive.spec.js`
Expected: 全部 PASS(webServer 自动起 vite dev :4174;若 chrome channel 缺失按报错处理,勿改 channel)

---

### Task 9: 全量验证与收尾

- [ ] **Step 1: 全量 vitest**

Run: `npx vitest run`
Expected: 全部 PASS(基线 flake 见 repo 惯例:单包复跑即绿,勿当回归)

- [ ] **Step 2: 全量 e2e(可选但推荐)**

Run: `npx playwright test`
Expected: 与 master 基线同等绿(聊天系用例不涉本改动,若挂先对照基线)

- [ ] **Step 3: 生产构建冒烟**

Run: `npx vite build 2>&1 | tail -3 && ls -la dist/index.html`
Expected: 构建成功,产物存在

- [ ] **Step 4: 手动冒烟清单**(报告给用户,由用户或后续会话在真机验证)

1. 四栏切换:代理设置/Agent/状态/用户 往返,zh/en 文案正确
2. Agent 三子 tab:记录(默认)/用户态 agent/aliangcode 切换,内容各自正确
3. 深链:`showPage('agent', { sub: 'aliangcode' })` 直达 aliangcode 子 tab;未知 sub 落记录
4. 仪表盘引导:点引导「打开设置」→ 落状态页并滚动到运行模式开关
5. 状态页尾「其他」:PeerMCP 卡可开关保存;两个置灰开关不可点、有「即将上线」徽标
6. 记录筛选:全部/Claude/Codex/AliangCode/OpenCode 过滤生效,分页正常
7. 未登录:代理设置/Agent/状态显示登录占位卡
8. 移动端 390px:导航 2×2 无溢出,子 tab 换行不破格

- [ ] **Step 5: 报告用户**(不 commit)

汇总改动文件清单 + 测试结果,请求用户验收;commit 仅在用户明确指示后执行(CLAUDE.md 纪律)。
