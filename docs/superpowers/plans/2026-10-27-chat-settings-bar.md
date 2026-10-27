# ChatPage 会话设置条（目录 / 模型 / effort）实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** web ChatPage 头部下方新增常驻设置条，让工作目录 / 模型 / effort 三项可见可配，后端零行为改动。

**Architecture:** 纯逻辑抽到新文件 `utils/chatSettings.js`（vitest 直测）；ChatPage 只做模板装配与协议接线（create 载荷统一走 `sessionCreatePayload`，每次发送前都发 create——自愈 socket 掉线期间的设置漂移）；最近项目来自既有 `GET /api/agent/sessions`；create 阶段失败首次可见（内联错误 + 复位重试）。

**Tech Stack:** Vue 3 `<script setup>`（无路由 SPA）、vitest、Playwright（chromium + `channel:'chrome'`）、Go（仅一行协议文档表）。

**Spec:** `docs/superpowers/specs/2026-10-27-chat-settings-bar-design.md`

**工作区约定（本仓库规则）：**
- 全程在 git worktree 分支开发（`git worktree add .worktrees/chat-settings-bar -b feature/chat-settings-bar master`）。新 worktree 若需 `go build` 全仓，先从主仓 `cp -r app/website/dist app/website/`（embed 占位坑）；本计划 Go 侧只 `go build ./app/http/models/`，不触发 embed。
- **每步都不自动 commit**。所有任务完成、全量验证通过后，向用户展示变更摘要，**经用户明确同意才提交**（提交信息建议：`新增:ChatPage 会话设置条(工作目录/模型/effort),目录锁定模型随时改,create 失败可见`）。

**关键既有事实（写代码前必读）：**
- `ChatPage.vue` 约在 :218 有 `<!-- Messages -->` 注释，设置条模板插在它**之前**（同属 `<div class="flex flex-1 flex-col min-w-0">` 容器，顶栏 `</div>` 之后）。行号会漂移，以注释锚点为准。
- `ensureAISocket()` / `sendAI(payload)` 是模块内既有函数（`sendAI` 返回 boolean）；socket 就绪判定 = `aiSocket && aiSocket.readyState === WebSocket.OPEN`（项目里**没有** `socketOpen` 标志量）。
- `ai.session.created` 回显含 `project_path/model/effort`（服务端 `agent_ai.go:2754-2764`）；create 失败回 `ai.error`（**无** `message_id` 字段）。
- 现状 `ai.error` handler 只找 running 气泡（`findRunningAssistant`），create 阶段错误今天被静默丢弃——本单要补。
- 现状 sendMessage 只在 `!conv.aiSessionCreated` 时发 create（ChatPage.vue:1115 一带）。本计划改为「create 确认门」：**每次发送都先发 create、等回执成功才发 ai.message**（后端 existing 分支 :1507-1529 原位更新，幂等于本页自管会话；确认门防目录无效时消息经 lazy-create 落 home；掉线期间的设置漂移自动补同步）。注意 `sending.value = false` 在 catch 块尾部而非 finally——try 内提前 return 须自带复位。**这是对 spec §4.3「onChange 重发」的等价超集简化**，Task 4 里同步 spec。
- 后端 glm-only 是回环网关的环境约束，代码只挡空值/`<synthetic>`——前端不做模型名白名单强校验，自定义值原样透传。
- i18n：`app/website/src/i18n/zh.js` / `en.js`，key 形如 `chat_xxx: '文本',`（zh :211-240 一带）。
- e2e 认证 stub 范式见 `app/website/tests/e2e/auth-consistency.spec.js`（`page.route('**/api/**')` + `/api/auth/session` 返回 active 快照）；onboarding 跳过 = `localStorage.setItem('alianggate.onboarding.v2', 'completed')`；聊天入口按钮 `data-testid="aliang-chat-entry-sidebar"`。
- vitest 运行目录：`app/website`，命令 `npm test` / `npx vitest run src/utils/chatSettings.test.js`。

---

### Task 1: `utils/chatSettings.js` 纯逻辑 + 单测（TDD）

**Files:**
- Create: `app/website/src/utils/chatSettings.js`
- Create: `app/website/src/utils/chatSettings.test.js`

- [ ] **Step 1: 写失败测试**

`app/website/src/utils/chatSettings.test.js`（风格仿 `chatTitle.test.js`，中文用例名）：

```js
import { describe, expect, it } from 'vitest';
import {
  AGENTCORE_MODEL_OPTIONS,
  EFFORT_OPTIONS,
  MODEL_CUSTOM_VALUE,
  isDirectoryLocked,
  recentProjectsFromSessions,
  sessionCreatePayload,
} from './chatSettings';

describe('EFFORT_OPTIONS', () => {
  it('与内核 NormalizeEffortLevel 白名单同源', () => {
    expect(EFFORT_OPTIONS.map(o => o.value)).toEqual(['', 'low', 'medium', 'high', 'xhigh', 'max']);
  });
});

describe('AGENTCORE_MODEL_OPTIONS', () => {
  it('含默认/常用档/[1m] 变体/自定义哨兵，且不含重复值', () => {
    const values = AGENTCORE_MODEL_OPTIONS.map(o => o.value);
    expect(values[0]).toBe('');
    expect(values).toContain('glm-5.3');
    expect(values).toContain('glm-5.3[1m]');
    expect(values).toContain('glm-5.3-flash[1m]');
    expect(values[values.length - 1]).toBe(MODEL_CUSTOM_VALUE);
    expect(new Set(values).size).toBe(values.length);
  });
});

describe('recentProjectsFromSessions', () => {
  it('按输入顺序去重并截断 limit（输入已是 updatedAt 倒序）', () => {
    const sessions = [
      { project_path: '/a' }, { project_path: '/b' }, { project_path: '/a' }, { project_path: '/c' },
    ];
    expect(recentProjectsFromSessions(sessions)).toEqual(['/a', '/b', '/c']);
    expect(recentProjectsFromSessions(sessions, { limit: 2 })).toEqual(['/a', '/b']);
  });

  it('过滤空/非字符串路径，容错非数组输入', () => {
    expect(recentProjectsFromSessions([{ project_path: '  ' }, {}, null, { project_path: '/ok' }])).toEqual(['/ok']);
    expect(recentProjectsFromSessions(null)).toEqual([]);
    expect(recentProjectsFromSessions('x')).toEqual([]);
    expect(recentProjectsFromSessions([{ project_path: ' /p ' }])).toEqual(['/p']);
  });
});

describe('isDirectoryLocked', () => {
  it('服务端确认建会话（dirLocked，由 ai.session.created 回显置位）后锁定', () => {
    expect(isDirectoryLocked({ dirLocked: true })).toBe(true);
  });

  it('未确认不锁定：新会话、create 失败的会话、legacy 会话（无该字段）', () => {
    expect(isDirectoryLocked({ dirLocked: false })).toBe(false);
    expect(isDirectoryLocked({})).toBe(false);
    // legacy：本功能上线前的会话只有 aiSessionCreated/messages，不锁定（可修）
    expect(isDirectoryLocked({ aiSessionCreated: true, messages: [{ role: 'user', content: 'hi' }] })).toBe(false);
    expect(isDirectoryLocked(null)).toBe(false);
  });
});

describe('sessionCreatePayload', () => {
  it('空值归空串，provider 缺省 claudecode，字段齐全', () => {
    expect(sessionCreatePayload({ aiSessionId: 's1' })).toEqual({
      type: 'ai.session.create',
      session_id: 's1',
      project_path: '',
      provider: 'claudecode',
      mode: 'vibe',
      model: '',
      effort: '',
    });
  });

  it('透传非空值与自定义模型/effort', () => {
    const conv = { aiSessionId: 's2', projectPath: '/w', provider: 'agentcore', model: 'my-model', effort: 'xhigh' };
    expect(sessionCreatePayload(conv)).toMatchObject({ project_path: '/w', provider: 'agentcore', model: 'my-model', effort: 'xhigh' });
  });
});
```

- [ ] **Step 2: 跑测试确认失败**

Run: `cd app/website && npx vitest run src/utils/chatSettings.test.js`
Expected: FAIL（`Cannot find module './chatSettings'` 或导出未定义）

- [ ] **Step 3: 写实现**

`app/website/src/utils/chatSettings.js`：

```js
// 会话设置条（工作目录/模型/effort）的纯逻辑：选项常量、最近项目整理、
// create 载荷组装、目录锁定判定。UI 装配在 ChatPage.vue；本文件保持零依赖
// 纯函数，vitest 直接覆盖（chatSettings.test.js）。

// 自定义模型的 select 哨兵值。真实模型名直接用自身作 value。
export const MODEL_CUSTOM_VALUE = '__custom__';

// 模型清单：前端常量维护（后端无清单接口）。glm-only 是回环网关的环境约束，
// 代码层不强制——自定义值原样透传，合法性与网关裁决。default('') 表示跟随
// 本机配置（agentCoreModel：ANTHROPIC_MODEL/ANTHROPIC_DEFAULT_SONNET_MODEL
// env 兜底，最终回落 glm-5.3）。
export const AGENTCORE_MODEL_OPTIONS = [
  { value: '', label: 'default' },
  { value: 'glm-5.3', label: 'glm-5.3' },
  { value: 'glm-5.3[1m]', label: 'glm-5.3[1m]' },
  { value: 'glm-5.3-flash', label: 'glm-5.3-flash' },
  { value: 'glm-5.3-flash[1m]', label: 'glm-5.3-flash[1m]' },
  { value: 'glm-5.2', label: 'glm-5.2' },
  { value: 'glm-5.1', label: 'glm-5.1' },
  { value: MODEL_CUSTOM_VALUE, label: 'custom' },
];

// effort 档位与内核 NormalizeEffortLevel（app/agentcore/loop.go）白名单同源：
// 超集值会被内核静默丢弃，清单必须收敛。'' = 不发送。
export const EFFORT_OPTIONS = [
  { value: '', label: 'default' },
  { value: 'low', label: 'low' },
  { value: 'medium', label: 'medium' },
  { value: 'high', label: 'high' },
  { value: 'xhigh', label: 'xhigh' },
  { value: 'max', label: 'max' },
];

// 最近项目：取 sessions[].project_path 非空值，按输入顺序（调用方保证
// updatedAt 倒序）去重，截断 limit。任何形态的脏输入都安全返回数组。
export function recentProjectsFromSessions(sessions, { limit = 8 } = {}) {
  if (!Array.isArray(sessions)) return [];
  const seen = new Set();
  const out = [];
  for (const session of sessions) {
    const raw = session && typeof session === 'object' ? session.project_path : null;
    const path = typeof raw === 'string' ? raw.trim() : '';
    if (!path || seen.has(path)) continue;
    seen.add(path);
    out.push(path);
    if (out.length >= limit) break;
  }
  return out;
}

// 目录锁定：以服务端确认为准——ai.session.created 回显到达时由 ChatPage 置
// conv.dirLocked = true。create 被拒的会话（目录不存在等）保持可改，用户纠正
// 路径后下一条消息的 create 即落正确目录。legacy 会话（无该字段）不锁定。
export function isDirectoryLocked(conv) {
  return !!(conv && typeof conv === 'object' && conv.dirLocked);
}

// ai.session.create 载荷统一出口：ChatPage 首发与重发共用，保证字段一致。
export function sessionCreatePayload(conv) {
  return {
    type: 'ai.session.create',
    session_id: (conv && conv.aiSessionId) || '',
    project_path: (conv && conv.projectPath) || '',
    provider: (conv && conv.provider) || 'claudecode',
    mode: 'vibe',
    model: (conv && conv.model) || '',
    effort: (conv && conv.effort) || '',
  };
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `cd app/website && npx vitest run src/utils/chatSettings.test.js`
Expected: PASS（全部用例绿）

---

### Task 2: i18n key

**Files:**
- Modify: `app/website/src/i18n/zh.js`（`chat_` 键区末尾，约 :240）
- Modify: `app/website/src/i18n/en.js`（同位置）

- [ ] **Step 1: zh.js 追加（紧随 `chat_approvalSendFailed` 之后）**

```js
  chat_settingsDirectory: '工作目录',
  chat_settingsModel: '模型',
  chat_settingsEffort: '推理力度',
  chat_modelDefault: '默认',
  chat_pathDefault: '默认（home）',
  chat_recentProjects: '最近项目',
  chat_noRecentProjects: '暂无最近项目',
  chat_customModel: '自定义…',
  chat_customPathPlaceholder: '输入 agent 机器上的绝对路径',
  chat_pathConfirm: '确定',
  chat_createFailed: '会话创建失败',
```

- [ ] **Step 2: en.js 追加（同键序）**

```js
  chat_settingsDirectory: 'Directory',
  chat_settingsModel: 'Model',
  chat_settingsEffort: 'Effort',
  chat_modelDefault: 'Default',
  chat_pathDefault: 'Default (home)',
  chat_recentProjects: 'Recent projects',
  chat_noRecentProjects: 'No recent projects',
  chat_customModel: 'Custom…',
  chat_customPathPlaceholder: 'Absolute path on the agent machine',
  chat_pathConfirm: 'Confirm',
  chat_createFailed: 'Failed to create session',
```

- [ ] **Step 3: 验证构建不破**

Run: `cd app/website && npx vitest run src/i18n 2>/dev/null | tail -3; npm run build 2>&1 | tail -3`
Expected: 无 i18n 相关测试或已有测试通过；build 成功（~2s）

---

### Task 3: ChatPage 集成（模板 + 状态 + 协议接线）

**Files:**
- Modify: `app/website/src/components/ChatPage.vue`

锚点（行号会漂移，全用文本锚点）：
- 模板插入点：`<!-- Messages -->` 注释**之前**
- 脚本 import 区：`import { useAuthStore } from '../stores/auth';` 之后
- 协议接线：`sendMessage()` 内 `await ensureAISocket();` 一带；`case 'ai.session.created':` 与 `case 'ai.error':` 两分支

- [ ] **Step 1: 追加 import 与模块状态**

import 区追加：

```js
import { watch } from 'vue';   // 合并进既有 vue import 行亦可
import { getAgentSessions } from '../services/agentApi';
import {
  AGENTCORE_MODEL_OPTIONS,
  EFFORT_OPTIONS,
  MODEL_CUSTOM_VALUE,
  isDirectoryLocked,
  recentProjectsFromSessions,
  sessionCreatePayload,
} from '../utils/chatSettings';
```

状态区（`const showMobileSidebar = ref(false);` 之后）追加：

```js
// --- 会话设置条（工作目录/模型/effort）状态 ---
const directoryPanelOpen = ref(false);
const customPathDraft = ref('');
const recentProjects = ref([]);
const recentProjectsLoaded = ref(false);
const modelCustomActive = ref(false);
const modelCustomDraft = ref('');
const createError = ref('');
```

- [ ] **Step 2: 追加 computed 与处理函数**

放在 `slashProviderOf` 定义之前的任意相邻位置（与设置条状态同区块）：

```js
// --- 会话设置条逻辑 ---

const directoryLocked = computed(() => isDirectoryLocked(activeConversation.value));

const directoryLabel = computed(() => activeConversation.value?.projectPath || t('chat_pathDefault'));

const modelOptions = computed(() => AGENTCORE_MODEL_OPTIONS.map(o => ({
  ...o,
  label: o.value === '' ? t('chat_modelDefault')
    : o.value === MODEL_CUSTOM_VALUE ? t('chat_customModel')
    : o.label,
})));

// select 当前值：预设模型→自身；非预设自定义值→哨兵（输入框承接显示）。
const modelSelectValue = computed(() => {
  const m = activeConversation.value?.model || '';
  if (!m) return '';
  return isPresetModel(m) ? m : MODEL_CUSTOM_VALUE;
});

function isPresetModel(m) {
  return AGENTCORE_MODEL_OPTIONS.some(o => o.value === m && o.value !== MODEL_CUSTOM_VALUE);
}

// 统一写入口：落字段 → 持久化 → 清错误 → 服务端原位同步（掉线时下条消息兜底）。
function applySessionSettings(conv, patch) {
  if (activeRunning.value) return;
  Object.assign(conv, patch);
  conv.updatedAt = now();
  saveToStorage();
  createError.value = '';
  resendCreate(conv);
}

// 已建会话的设置变更要立即让服务端知道；socket 不在线就放弃本轮——
// sendMessage 现在每条消息前都重发 create，天然兜底，无需脏标记。
function resendCreate(conv) {
  if (!conv.aiSessionCreated) return;
  if (!aiSocket || aiSocket.readyState !== WebSocket.OPEN) return;
  sendAI(sessionCreatePayload(conv));
}

async function toggleDirectoryPanel() {
  const conv = activeConversation.value;
  if (!conv || directoryLocked.value || activeRunning.value) return;
  directoryPanelOpen.value = !directoryPanelOpen.value;
  if (directoryPanelOpen.value && !recentProjectsLoaded.value) {
    recentProjectsLoaded.value = true;
    try {
      const data = await getAgentSessions();
      recentProjects.value = recentProjectsFromSessions(data?.sessions);
    } catch (_) {
      recentProjects.value = []; // 拉取失败静默，手输路径不受影响
    }
  }
}

function pickDirectory(path) {
  const conv = activeConversation.value;
  if (!conv) return;
  customPathDraft.value = '';
  directoryPanelOpen.value = false;
  applySessionSettings(conv, { projectPath: path });
}

function commitCustomPath() {
  const conv = activeConversation.value;
  if (!conv) return;
  const path = customPathDraft.value.trim();
  if (!path) return;
  pickDirectory(path);
}

function onModelSelect(value) {
  const conv = activeConversation.value;
  if (!conv || activeRunning.value) return;
  if (value === MODEL_CUSTOM_VALUE) {
    modelCustomDraft.value = conv.model && !isPresetModel(conv.model) ? conv.model : '';
    modelCustomActive.value = true;
    return;
  }
  modelCustomActive.value = false;
  applySessionSettings(conv, { model: value });
}

function commitCustomModel() {
  const conv = activeConversation.value;
  if (!conv) return;
  const model = modelCustomDraft.value.trim();
  modelCustomActive.value = false;
  if (!model) return; // 空输入视作取消，保持原值
  applySessionSettings(conv, { model });
}

const activeEffort = computed({
  get: () => activeConversation.value?.effort || '',
  set(value) {
    const conv = activeConversation.value;
    if (!conv || activeRunning.value) return;
    applySessionSettings(conv, { effort: value });
  },
});

// 切会话收起面板、清一次性错误。
watch(activeId, () => {
  directoryPanelOpen.value = false;
  modelCustomActive.value = false;
  createError.value = '';
});

// create 确认门：sendMessage 发出 create 后等回执再发 ai.message——目录无效
// 时服务端拒绝建会话，绝不能让消息经 ai.message 的 lazy-create 落到 home 跑。
// 回执来源：ai.session.created → 'created'；create 期 ai.error（无 message_id）
// → 'error'（错误文本已由该分支渲染）；超时 → 'timeout'（socket 死亡等）。
const pendingCreates = new Map(); // session_id -> settle(status)

function waitForCreateAck(conv, timeoutMs = 5000) {
  return new Promise((resolve) => {
    const sid = conv.aiSessionId;
    let done = false;
    const settle = (status) => {
      if (done) return;
      done = true;
      pendingCreates.delete(sid);
      clearTimeout(timer);
      resolve(status);
    };
    const timer = setTimeout(() => settle('timeout'), timeoutMs);
    pendingCreates.set(sid, settle);
    sendAI(sessionCreatePayload(conv));
    conv.aiSessionCreated = true;
  });
}
```

- [ ] **Step 3: 模板插入设置条**

在 `<!-- Messages -->` 注释**之前**插入（注意：外层容器已是 `flex flex-col`，本行自占一行）：

```html
      <!-- Session settings bar: directory / model / effort -->
      <div
        v-if="activeConversation"
        class="relative flex items-center gap-2 overflow-x-auto border-b border-slate-200 px-4 py-1.5 text-xs dark:border-slate-800"
      >
        <span class="hidden shrink-0 text-slate-400 md:inline">{{ t('chat_settingsDirectory') }}</span>
        <span
          v-if="directoryLocked"
          data-testid="chat-settings-directory"
          class="flex min-h-[40px] max-w-[16rem] shrink-0 items-center gap-1 text-slate-400 md:min-h-0"
          :title="directoryLabel"
        >
          <span class="material-symbols-outlined text-sm">folder</span>
          <span class="truncate">{{ directoryLabel }}</span>
        </span>
        <button
          v-else
          type="button"
          data-testid="chat-settings-directory"
          class="flex min-h-[40px] max-w-[16rem] shrink-0 items-center gap-1 rounded border border-slate-200 px-2 text-slate-600 transition hover:bg-slate-100 disabled:cursor-not-allowed disabled:opacity-60 dark:border-slate-700 dark:text-slate-300 dark:hover:bg-slate-800 md:min-h-0"
          :disabled="activeRunning"
          :title="directoryLabel"
          :aria-label="t('chat_settingsDirectory')"
          @click="toggleDirectoryPanel"
        >
          <span class="material-symbols-outlined text-sm">folder</span>
          <span class="truncate">{{ directoryLabel }}</span>
        </button>
        <div v-if="directoryPanelOpen" class="absolute inset-0 z-20" @click="directoryPanelOpen = false"></div>
        <div
          v-if="directoryPanelOpen"
          class="absolute left-2 top-full z-30 mt-1 w-80 rounded-lg border border-slate-200 bg-white p-3 shadow-lg dark:border-slate-600 dark:bg-slate-800"
        >
          <div class="mb-1 text-xs font-semibold text-slate-400">{{ t('chat_recentProjects') }}</div>
          <div v-if="recentProjects.length === 0" class="px-1 py-1 text-xs text-slate-400">{{ t('chat_noRecentProjects') }}</div>
          <button
            v-for="p in recentProjects"
            :key="p"
            type="button"
            class="block w-full truncate rounded px-1 py-1.5 text-left text-xs text-slate-700 transition hover:bg-primary/10 dark:text-slate-200"
            :title="p"
            @click="pickDirectory(p)"
          >{{ p }}</button>
          <div class="mt-2 flex gap-1">
            <input
              v-model="customPathDraft"
              type="text"
              :placeholder="t('chat_customPathPlaceholder')"
              class="h-9 min-w-0 flex-1 rounded border border-slate-200 bg-white px-2 text-xs outline-none focus:border-primary dark:border-slate-600 dark:bg-slate-800"
              @keydown.enter.prevent="commitCustomPath"
              @keydown.esc="directoryPanelOpen = false"
            />
            <button
              type="button"
              class="h-9 shrink-0 rounded bg-primary px-3 text-xs font-semibold text-white transition hover:bg-primary/90"
              @click="commitCustomPath"
            >{{ t('chat_pathConfirm') }}</button>
          </div>
        </div>
        <span class="ml-2 hidden shrink-0 text-slate-400 md:inline">{{ t('chat_settingsModel') }}</span>
        <select
          data-testid="chat-settings-model"
          class="min-h-[40px] shrink-0 rounded border border-slate-200 bg-white px-1 text-slate-600 outline-none disabled:cursor-not-allowed disabled:opacity-60 dark:border-slate-700 dark:bg-slate-800 dark:text-slate-300 md:min-h-0"
          :value="modelSelectValue"
          :disabled="activeRunning"
          :aria-label="t('chat_settingsModel')"
          @change="onModelSelect($event.target.value)"
        >
          <option v-for="opt in modelOptions" :key="opt.value" :value="opt.value">{{ opt.label }}</option>
        </select>
        <input
          v-if="modelCustomActive"
          v-model="modelCustomDraft"
          type="text"
          class="h-9 w-36 shrink-0 rounded border border-slate-200 bg-white px-2 text-xs outline-none focus:border-primary dark:border-slate-600 dark:bg-slate-800"
          :placeholder="t('chat_customModel')"
          @keydown.enter.prevent="commitCustomModel"
          @blur="commitCustomModel"
        />
        <span class="ml-2 hidden shrink-0 text-slate-400 md:inline">{{ t('chat_settingsEffort') }}</span>
        <select
          v-model="activeEffort"
          data-testid="chat-settings-effort"
          class="min-h-[40px] shrink-0 rounded border border-slate-200 bg-white px-1 text-slate-600 outline-none disabled:cursor-not-allowed disabled:opacity-60 dark:border-slate-700 dark:bg-slate-800 dark:text-slate-300 md:min-h-0"
          :disabled="activeRunning"
          :aria-label="t('chat_settingsEffort')"
        >
          <option v-for="opt in EFFORT_OPTIONS" :key="opt.value" :value="opt.value">{{ opt.value || t('chat_modelDefault') }}</option>
        </select>
        <div
          v-if="createError"
          data-testid="chat-settings-error"
          class="ml-2 shrink-0 truncate text-rose-500"
          :title="createError"
        >{{ createError }}</div>
      </div>
```

- [ ] **Step 4: 协议接线**

4a. `sendMessage()` 内，把现有条件 create 改为「create 确认门」：先发 create 等
回执，确认成功才发 `ai.message`（防目录无效时消息经 lazy-create 落 home 跑）；
每次发送都重发 create 同时自愈掉线期间的设置漂移（后端 existing 分支幂等）：

```js
  try {
    await ensureAISocket();
    createError.value = '';
    const ack = await waitForCreateAck(conv);
    if (ack !== 'created') {
      // create 被拒或无回执：内联错误已由 ai.error 分支渲染（timeout 无帧，
      // 这里兜底提示）；撤掉空占位气泡防空转；不发 ai.message。
      const asst = findRunningAssistant(conv);
      if (asst && !asst.content) conv.messages = conv.messages.filter(m => m !== asst);
      if (!createError.value) createError.value = t('chat_serviceUnavailable');
      sending.value = false; // 本函数的 sending=false 在 catch 尾部，早退须自理
      saveToStorage();
      return;
    }
    const ok = sendAI({
      type: 'ai.message',
      session_id: conv.aiSessionId,
      message_id: messageId,
      content: text,
    });
```

（即删除 `if (!conv.aiSessionCreated) { ... }` 包裹，换成 `waitForCreateAck`；
`ai.message` 帧原样保留。注意现有 `sending.value = false; saveToStorage();` 是
catch 块的尾部语句、不在 finally 里——try 内任何提前 return 都要自带这两行。）

4b. `case 'ai.session.created':` 追加回显同步 + create 确认 + 目录锁定：

```js
    case 'ai.session.created': {
      conv.aiReady = true;
      // 目录锁定以服务端确认为准：create 成功后不可再改（create 失败保持可改）。
      conv.dirLocked = true;
      // 回显是服务端清洗后的权威值：project_path 已是解析后的绝对路径。
      if (typeof e.project_path === 'string' && e.project_path) conv.projectPath = e.project_path;
      if (typeof e.model === 'string') conv.model = e.model;
      if (typeof e.effort === 'string') conv.effort = e.effort;
      const pending = pendingCreates.get(e.session_id);
      if (pending) pending('created');
      persist = true;
      break;
    }
```

4c. `case 'ai.error':` 前置 create 阶段失败分支。**判定只用单条件
`!e.message_id`**——运行期所有 `ai.error` 出口都带 message_id（agent_ai.go
:1605/:1650/:1658/:1725/:2080 等），create 期的不带且 `agentAIErrorPayload`
对空 message_id 省略该字段；不能用「无 running 气泡」做条件（sendMessage 在发
create 前已推入占位气泡，create 失败时气泡存在，双条件永不命中）：

```js
    case 'ai.error': {
      const asst = findRunningAssistant(conv);
      if (!e.message_id) {
        // create 阶段失败（典型：手输目录不存在）。亮在设置条上、结算确认门；
        // 不进消息流。目录未锁定（dirLocked 只在回显成功时置位），纠正路径后
        // 下一条消息重发 create 即成功。
        if (conv.id === activeId.value) createError.value = e.error || t('chat_createFailed');
        const pending = pendingCreates.get(e.session_id);
        if (pending) pending('error');
        saveToStorage();
        return;
      }
      if (asst) {
        asst.status = 'error';
        if (!asst.content) asst.content = e.error || t('chat_aiError');
      }
      clearApprovalsForSession(conv, e.session_id);
      finishRun(conv);
      return;
    }
```

- [ ] **Step 5: 验证**

Run: `cd app/website && npm test 2>&1 | tail -5 && npm run build 2>&1 | tail -3`
Expected: 全部既有测试通过（112+），build 成功

Run: 手动 `npm run dev` 打开仪表盘 → 点 AliangCode 聊天入口 → 新建会话，确认设置条渲染、目录面板开合、模型/effort 可选。

---

### Task 4: spec 文档同步（评审修正落档）

**Files:**
- Modify: `docs/superpowers/specs/2026-10-27-chat-settings-bar-design.md`

实现与 spec 初稿有六处偏差（§3 计两处：确认门数据流 + ai.error 判定行），逐条**原位改写**（不留自相矛盾的旧文本）：

- [ ] **Step 1: §3 数据流图两行改写**：

原文：

```markdown
ai.error（无 message_id、无 running 气泡 = create 阶段失败）
  └─ 设置条下方一次性内联错误 + 复位 conv.aiSessionCreated=false（下次发送重试）
```

改为：

```markdown
sendMessage 走「create 确认门」：先发 create 等 ai.session.created 回执
（或 create 期 ai.error / 5s 超时），确认成功才发 ai.message——目录无效时
消息绝不经 ai.message 的 lazy-create 落 home 跑；socket 掉线期间的设置漂移
由每次发送前重发 create 自动补同步。
ai.error（无 message_id = create 阶段失败；运行期错误帧全带 message_id）
  └─ 设置条下方一次性内联错误；目录保持可改（dirLocked 未置位），纠正路径后
     下一条消息重发 create 即成功
```

- [ ] **Step 2: §4.1 `isDirectoryLocked` 语义原位改写** — 锁定改为「服务端确认制」：

```markdown
export function isDirectoryLocked(conv);
// conv.dirLocked === true → 锁定。dirLocked 由 ChatPage 在收到
// ai.session.created 回显时置位（锁定 = 服务端确认建会话成功）；create 被拒的
// 会话保持可改，纠正路径后下一条消息即落正确目录；legacy 会话（无该字段）
// 不锁定。
```

（同一节去重措辞澄清：输入按 updatedAt 倒序时，某路径的**首次出现**即其最新
会话——实现保留首次出现顺序，与「最新优先」语义一致。）

- [ ] **Step 3: §4.3「ai.error 分支」段落原位改写**（原「双条件…复位
aiSessionCreated」描述替换）：

```markdown
- 新增 create 阶段错误分支：`ai.error` 到达时若**无 `message_id`**（单条件：
  运行期所有错误出口都带 message_id，create 期全不带；「无 running 气泡」不可
  作条件——sendMessage 在发 create 前已推入占位气泡。该判定只覆盖 create 与
  ai.message 流量；`ai.approval.response` 出错理论上 message_id 可为空，实践
  不可达）→ 设置条下方渲染一次性错误（`e.error` 原文），确认门拦住
  `ai.message`；目录未锁定，纠正路径后下次发送自动重试；该错误不进消息流。
```

- [ ] **Step 4: §5 错误表「手输路径不存在」行原位改写**：

```markdown
| 手输路径不存在/是文件/是 symlink 悬挂 | create 回 `ai.error`（`resolveAgentAICWD` → `cleanExistingAgentDirectory` 报错），设置条下方一次性内联提示原文；确认门拦住 ai.message（不落 home 跑）；目录保持可改（dirLocked 未置位），纠正路径后下一条消息重发 create 成功 |
```

- [ ] **Step 5: §7 风险表后补一行「实现注记」**（两处外观偏差，不改设计正文）：

```markdown
实现注记：目录控件用 CSS 端截断（truncate，max-w-[16rem]）而非中段省略；
目录面板 Esc 仅绑手输框、点击遮罩只盖设置条行。
```

---

### Task 5: 协议文档补录（Go 侧唯一改动）

**Files:**
- Modify: `app/http/models/agent_protocol.go:284`

- [ ] **Step 1: `ai.session.create` 行的 Optional 清单追加 `effort`**

现值（:284）：

```go
{Type: AgentEventAISessionCreate, Required: []string{"type", "session_id"}, Optional: []string{"project_path", "mode", "provider", "tool", "model", "source_session_id", "resume_session_id", "initial_context", "transcript", "claude_remote_policy"}, Emits: []string{AgentEventAISessionCreated, AgentEventAIError}},
```

Optional 改为（仅插入 `"effort", `，置于 `"model", ` 之后）：

```go
Optional: []string{"project_path", "mode", "provider", "tool", "model", "effort", "source_session_id", "resume_session_id", "initial_context", "transcript", "claude_remote_policy"}
```

- [ ] **Step 2: 编译验证**

Run: `go build ./app/http/models/ && go vet ./app/http/models/`
Expected: 无输出（成功）

---

### Task 6: E2E 冒烟 spec

**Files:**
- Create: `app/website/tests/e2e/chat-settings.spec.js`

- [ ] **Step 1: 写 spec（WS fake 是新写的：捕获 send 帧 + 注入服务端帧）**

```js
import { expect, test } from '@playwright/test';

// 注入假 WebSocket：捕获客户端发出的帧到 window.__aiFrames，并对
// ai.session.create 回 ai.session.created（可经 window.__failCreate 翻转为
// ai.error 以模拟目录不存在）。
const WS_STUB = `
  window.__aiFrames = [];
  window.__failCreate = false;
  window.EventSource = class { constructor() { setTimeout(() => this.onopen?.(), 0); } close() {} };
  class FakeWebSocket {
    // 应用代码以 WebSocket.OPEN 等静态常量做就绪判定（readyState === WebSocket.OPEN），
    // stub 替换全局后若缺这些常量判定恒假、sendAI 永不发帧——必须补齐。
    static OPEN = 1;
    static CONNECTING = 0;
    static CLOSING = 2;
    static CLOSED = 3;
    constructor(url) {
      this.url = url;
      this.readyState = 0;
      FakeWebSocket.instances.push(this);
      setTimeout(() => { this.readyState = 1; this.onopen?.(); }, 0);
    }
    send(data) {
      const frame = JSON.parse(data);
      window.__aiFrames.push(frame);
      if (frame.type === 'ai.session.create') {
        setTimeout(() => {
          if (!this.onmessage) return;
          const payload = window.__failCreate
            ? { type: 'ai.error', session_id: frame.session_id, error: 'stat /no/such/dir: no such file or directory' }
            : { type: 'ai.session.created', session_id: frame.session_id, mode: frame.mode, project_path: frame.project_path || '/home/e2e', provider: frame.provider, model: frame.model || '', effort: frame.effort || '', state: 'idle' };
          this.onmessage({ data: JSON.stringify(payload) });
        }, 0);
      }
      if (frame.type === 'ai.message') {
        setTimeout(() => {
          if (!this.onmessage) return;
          this.onmessage({ data: JSON.stringify({ type: 'ai.done', session_id: frame.session_id }) });
        }, 0);
      }
    }
    close() {}
  }
  FakeWebSocket.instances = [];
  window.WebSocket = FakeWebSocket;
`;

async function openChat(page) {
  await page.addInitScript(WS_STUB);
  await page.addInitScript(() => localStorage.setItem('alianggate.onboarding.v2', 'completed'));
  await page.route('**/api/**', async (route) => {
    const url = new URL(route.request().url());
    if (url.pathname === '/api/auth/session') {
      await route.fulfill({
        contentType: 'application/json',
        body: JSON.stringify({
          code: 0,
          data: { type: 'session_snapshot', instance_id: 'e2e', revision: 1, state: 'active', user: { id: 1, username: 'alice', email: 'a@b.c' } },
        }),
      });
      return;
    }
    if (url.pathname === '/api/agent/sessions') {
      await route.fulfill({
        contentType: 'application/json',
        body: JSON.stringify({ code: 0, data: { sessions: [
          { project_path: '/home/alice/proj-b' }, { project_path: '/home/alice/proj-a' }, { project_path: '/home/alice/proj-a' },
        ] } }),
      });
      return;
    }
    await route.fulfill({ contentType: 'application/json', body: JSON.stringify({ code: 0, data: {} }) });
  });
  await page.goto('/');
  await expect(page.getByTestId('aliang-chat-entry-sidebar')).toBeVisible();
  await page.getByTestId('aliang-chat-entry-sidebar').click();
  await page.getByRole('button').filter({ hasText: 'add' }).first().click(); // 侧栏「+」新建会话
  await expect(page.getByTestId('chat-settings-directory')).toBeVisible();
}

test('设置条渲染 + 最近项目去重 + 改模型后 create 帧带 model', async ({ page }) => {
  await openChat(page);
  // 打开目录面板：最近项目去重为 2 项
  await page.getByTestId('chat-settings-directory').click();
  await expect(page.getByText('/home/alice/proj-b')).toBeVisible();
  const projectButtons = page.locator('button', { hasText: '/home/alice/proj-' });
  await expect(projectButtons).toHaveCount(2);
  await page.getByText('/home/alice/proj-b').click();
  // 改模型为 glm-5.2
  await page.getByTestId('chat-settings-model').selectOption('glm-5.2');
  // 发消息 → 捕获的 create 帧含目录与 model
  await page.getByPlaceholder(/输入消息|Type a message/).fill('你好');
  await page.keyboard.press('Enter');
  // 帧在 Enter 时同步入列，直接取即可
  await expect.poll(async () => page.evaluate(() => window.__aiFrames.filter(f => f.type === 'ai.session.create').length)).toBeGreaterThan(0);
  const creates = await page.evaluate(() => window.__aiFrames.filter(f => f.type === 'ai.session.create'));
  const last = creates[creates.length - 1];
  expect(last.project_path).toBe('/home/alice/proj-b');
  expect(last.model).toBe('glm-5.2');
  expect(last.effort).toBe('');
});

test('已建会话改 effort 触发第二次 create；目录锁定为只读', async ({ page }) => {
  await openChat(page);
  await page.getByPlaceholder(/输入消息|Type a message/).fill('第一句');
  await page.keyboard.press('Enter');
  await expect.poll(async () => page.evaluate(() => window.__aiFrames.some(f => f.type === 'ai.session.create'))).toBe(true);
  // 发送过 → 目录锁定（只读 span，非 button）
  await expect(page.locator('span[data-testid="chat-settings-directory"]')).toBeVisible();
  // 改 effort → 追加一次 create
  await page.getByTestId('chat-settings-effort').selectOption('high');
  await expect.poll(async () => page.evaluate(() => window.__aiFrames.filter(f => f.type === 'ai.session.create' && f.effort === 'high').length)).toBeGreaterThan(0);
});

test('create 失败内联可见且下次发送重试', async ({ page }) => {
  await openChat(page);
  await page.evaluate(() => { window.__failCreate = true; });
  // 先手输一个坏目录
  await page.getByTestId('chat-settings-directory').click();
  await page.getByPlaceholder(/agent 机器|agent machine/).fill('/no/such/dir');
  await page.getByRole('button', { name: /确定|Confirm/ }).click();
  await page.getByPlaceholder(/输入消息|Type a message/).fill('你好');
  await page.keyboard.press('Enter');
  await expect(page.getByTestId('chat-settings-error')).toContainText('no such file or directory');
  // 翻转 stub 标志后重发（路径本身没改，retry create 仍带 /no/such/dir——
  // 本用例断言的是重发机制与确认门，不是路径修正）：第二次 create 前不应有
  // ai.message（确认门拦住了失败那轮），且错误消失
  await page.evaluate(() => { window.__failCreate = false; });
  await page.getByPlaceholder(/输入消息|Type a message/).fill('再来');
  await page.keyboard.press('Enter');
  await expect.poll(async () => {
    const frames = await page.evaluate(() => window.__aiFrames);
    const types = frames.map(f => f.type);
    const creates = frames.filter(f => f.type === 'ai.session.create');
    const firstCreateIdx = types.indexOf('ai.session.create');
    const retryIdx = types.lastIndexOf('ai.session.create');
    // 右边界必须钉在 retry create：重试自己的 ai.message 会在毫秒级入列，
    // 无边界的 slice 到数组尾会让断言恒假。
    const messageBeforeRetry = types.slice(firstCreateIdx, retryIdx).includes('ai.message');
    return creates.length >= 2 && creates[creates.length - 1].project_path === '/no/such/dir' && !messageBeforeRetry;
  }).toBe(true);
  await expect(page.getByTestId('chat-settings-error')).toHaveCount(0);
});
```

- [ ] **Step 2: 运行**

Run: `cd app/website && npx playwright test tests/e2e/chat-settings.spec.js`
Expected: 3 passed。若选择器因 i18n 语言失配失败，把 `/输入消息|Type a message/` 等正则放宽为 `[placeholder]` 属性选择器；若「+」按钮定位脆弱，改用侧栏头部按钮的 aria/图标类定位（`aside button` 中含 `material-symbols-outlined` 文本 `add` 的那个）。

---

### Task 7: 全量验证 + 人工验收 + 提交门

- [ ] **Step 1: 全量回归**

Run: `cd app/website && npm test 2>&1 | tail -3 && npm run build 2>&1 | tail -2 && npx playwright test 2>&1 | tail -5`
Run: `go build ./app/http/models/ && go vet ./app/http/models/`
Expected: 全绿（既有 112+ vitest、全部 e2e、Go 编译干净）

- [ ] **Step 2: 人工验收清单（部署 liang-dev 后逐项勾）**

1. 新建 agentcore 会话，设置条三控件可见；目录面板列出本机历史项目（去重）；
2. 手输不存在路径发消息 → 设置条内联报错（非静默），纠正路径后发送成功；
3. 选中有效目录发消息 → 回答体现项目上下文；
4. 模型切 `glm-5.2` 再发消息 → `.aliang/ai-sessions` journal session 行 model 变化；
5. effort 切 high/xhigh 各发一条 → 无 effort 被拒错误、回答有差异；
6. 发送过消息的会话：目录只读、模型/effort 可改且下一条生效；
7. 刷新页面 → 三字段从 localStorage 恢复；
8. 390px 宽度：设置条横向滚动不溢出、控件可触控；claudecode 旧会话行为不变。

- [ ] **Step 3: 提交（须用户明确同意后执行）**

变更集：`chatSettings.js`(+test)、`i18n/zh.js`/`en.js`、`ChatPage.vue`、`agent_protocol.go`、spec(+Task 4 增量)、plan、`chat-settings.spec.js`。

```bash
git add app/website/src/utils/chatSettings.js app/website/src/utils/chatSettings.test.js \
  app/website/src/i18n/zh.js app/website/src/i18n/en.js \
  app/website/src/components/ChatPage.vue app/http/models/agent_protocol.go \
  app/website/tests/e2e/chat-settings.spec.js \
  docs/superpowers/specs/2026-10-27-chat-settings-bar-design.md docs/superpowers/plans/2026-10-27-chat-settings-bar.md
git commit  # 信息：新增:ChatPage 会话设置条(工作目录/模型/effort),目录锁定模型随时改,create 失败可见
```

**未经用户明确说「提交」，此步不得执行。**
