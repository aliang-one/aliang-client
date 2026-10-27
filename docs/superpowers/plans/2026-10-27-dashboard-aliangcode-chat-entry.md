# 仪表盘 AliangCode 聊天入口 — 实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 复活仪表盘侧栏/移动面板的聊天入口，使其进入现有 ChatPage 并以 `provider=agentcore`（AliangCode 内置内核）新建会话；同时补上会话重命名。

**Architecture:** 纯前端改动，零后端改动。导航层模块级 ref `chatProviderPreset` 承载「入口 → 聊天页」的 provider 预置，ChatPage 在 `createConversation()` 读取它；ChatPage 挂载点（`App.vue`）当前被注释，必须先恢复。两段可测纯逻辑（标题归一化、斜杠 provider 映射）抽成 `src/utils/` 下的小工具模块，用 vitest 覆盖。

**Tech Stack:** Vue 3 `<script setup>`、Tailwind、vitest 2、Playwright（既有 `mobile-responsive.spec.js`）。

**规格来源：** `docs/superpowers/specs/2026-10-27-dashboard-aliangcode-chat-entry-design.md`（已评审通过）

**提交策略（覆盖模板默认）：** 项目 `CLAUDE.md` 明令「**NEVER automatically use `git commit`**」。本计划**不包含任何自动 commit 步骤**；每个任务末尾改为「暂存/自查」，全部验证通过后由用户明确许可再提交。

---

## 文件结构（先锁定分解）

| 文件 | 职责 | 动作 |
|---|---|---|
| `app/website/src/utils/chatTitle.js` | 会话标题归一化（trim/折叠空白/60 字上限/空串语义） | 新建 |
| `app/website/src/utils/chatTitle.test.js` | 上者单测 | 新建 |
| `app/website/src/utils/chatProvider.js` | provider → 斜杠命令面映射（claude/codex/空） | 新建 |
| `app/website/src/utils/chatProvider.test.js` | 上者单测 | 新建 |
| `app/website/src/composables/useNavigation.js` | 页面导航 + 聊天 provider 预置 | 修改 |
| `app/website/src/composables/useNavigation.test.js` | 预置读写单测 | 新建 |
| `app/website/src/i18n/zh.js` / `en.js` | 新增 3 个 key、删除 1 个死 key | 修改 |
| `app/website/src/App.vue` | 恢复 `<ChatPage>` 挂载（硬前置） | 修改 |
| `app/website/src/components/DashboardPage.vue` | 桌面侧栏 + 移动面板入口 | 修改 |
| `app/website/src/components/ChatPage.vue` | provider 预置、斜杠屏蔽、会话重命名 | 修改 |

工作目录约定：所有改动在独立 worktree 中进行（仓库既有惯例 `.worktrees/<name>`），**不直接改 master 工作区**。

---

### Task 0: 建 worktree 并跑通基线

**Files:** 无代码改动

- [ ] **Step 1: 创建 worktree 与新分支**

```bash
cd /Users/mac/MyProgram/GoProgram/nursor/alianggate
git worktree add .worktrees/aliangcode-chat-entry -b feature/dashboard-aliangcode-chat-entry
```

Expected: `Preparing worktree (new branch 'feature/dashboard-aliangcode-chat-entry')`

- [ ] **Step 2: 把规格与计划文档带进 worktree**

```bash
cd /Users/mac/MyProgram/GoProgram/nursor/alianggate
mkdir -p .worktrees/aliangcode-chat-entry/docs/superpowers/specs \
         .worktrees/aliangcode-chat-entry/docs/superpowers/plans
cp docs/superpowers/specs/2026-10-27-dashboard-aliangcode-chat-entry-design.md \
   .worktrees/aliangcode-chat-entry/docs/superpowers/specs/
cp docs/superpowers/plans/2026-10-27-dashboard-aliangcode-chat-entry.md \
   .worktrees/aliangcode-chat-entry/docs/superpowers/plans/
```

- [ ] **Step 3: 复用主仓 node_modules（worktree 没有自己的依赖目录）**

```bash
cd /Users/mac/MyProgram/GoProgram/nursor/alianggate/.worktrees/aliangcode-chat-entry/app/website
ln -s /Users/mac/MyProgram/GoProgram/nursor/alianggate/app/website/node_modules node_modules
ls node_modules/.bin/vitest
```

Expected: 打印 `node_modules/.bin/vitest` 路径（若符号链接不被接受，改用 `npm install`，耗时更长但等价）

> 注意：本单只跑前端 vitest / Playwright，**不需要** `cp app/website/dist`（Playwright 自带 `webServer` 起 dev server，见 `playwright.config.js`）。

- [ ] **Step 4: 跑基线单测**

```bash
cd /Users/mac/MyProgram/GoProgram/nursor/alianggate/.worktrees/aliangcode-chat-entry/app/website
npx vitest run
```

Expected: 全部 PASS。若有失败，先记录原始失败名——那是既有基线，不是本单引入。

- [ ] **Step 5: 记下基准提交**

```bash
git -C /Users/mac/MyProgram/GoProgram/nursor/alianggate/.worktrees/aliangcode-chat-entry rev-parse --short HEAD
```

后续每步 diff 都相对这个基准。

---

### Task 1: 标题归一化工具（TDD）

**Files:**
- Create: `app/website/src/utils/chatTitle.js`
- Test: `app/website/src/utils/chatTitle.test.js`

- [ ] **Step 1: 写失败测试**

```javascript
// app/website/src/utils/chatTitle.test.js
import { describe, expect, it } from 'vitest';
import { CHAT_TITLE_MAX_LENGTH, normalizeConversationTitle } from './chatTitle';

describe('normalizeConversationTitle', () => {
  it('空值/纯空白返回空串（由渲染期兜底显示「新对话」）', () => {
    expect(normalizeConversationTitle('')).toBe('');
    expect(normalizeConversationTitle('   ')).toBe('');
    expect(normalizeConversationTitle('\n\t ')).toBe('');
    expect(normalizeConversationTitle(null)).toBe('');
    expect(normalizeConversationTitle(undefined)).toBe('');
  });

  it('剥离首尾空白', () => {
    expect(normalizeConversationTitle('  修复登录  ')).toBe('修复登录');
  });

  it('折叠内部换行/连续空白为单个空格', () => {
    expect(normalizeConversationTitle('第一行\n第二行')).toBe('第一行 第二行');
    expect(normalizeConversationTitle('a    b')).toBe('a b');
  });

  it('超过上限截断到 60 字符', () => {
    const raw = 'x'.repeat(80);
    const out = normalizeConversationTitle(raw);
    expect(out).toHaveLength(CHAT_TITLE_MAX_LENGTH);
    expect(CHAT_TITLE_MAX_LENGTH).toBe(60);
  });

  it('非字符串输入按字符串处理', () => {
    expect(normalizeConversationTitle(2026)).toBe('2026');
  });
});
```

- [ ] **Step 2: 运行测试确认失败**

```bash
cd /Users/mac/MyProgram/GoProgram/nursor/alianggate/.worktrees/aliangcode-chat-entry/app/website
npx vitest run src/utils/chatTitle.test.js
```

Expected: FAIL — `Failed to resolve import "./chatTitle"`

- [ ] **Step 3: 写最小实现**

```javascript
// app/website/src/utils/chatTitle.js
export const CHAT_TITLE_MAX_LENGTH = 60;

// 会话标题归一化。空串是合法结果：渲染期用 `c.title || t('chat_emptyTitle')`
// 兜底，绝不把某个语言的译文写进 localStorage（否则切语言后标题不跟随）。
export function normalizeConversationTitle(raw) {
  const text = String(raw ?? '').replace(/\s+/g, ' ').trim();
  if (!text) return '';
  return text.slice(0, CHAT_TITLE_MAX_LENGTH);
}
```

- [ ] **Step 4: 运行测试确认通过**

```bash
npx vitest run src/utils/chatTitle.test.js
```

Expected: 5 passed

- [ ] **Step 5: 暂存（不提交）**

```bash
git add src/utils/chatTitle.js src/utils/chatTitle.test.js
```

---

### Task 2: 斜杠命令面 provider 映射（TDD）

**Files:**
- Create: `app/website/src/utils/chatProvider.js`
- Test: `app/website/src/utils/chatProvider.test.js`

背景：ChatPage 的 `/` 补全按 provider 拉命令面。agentcore（内置内核）没有 claude/codex 的插件/项目级命令面，必须映射为空串 → 客户端**不发请求、不显示面板**。后端 `normalizeSlashProvider` 反而把空值当「未指定」返回全量命令，所以**不能**靠后端兜底。

- [ ] **Step 1: 写失败测试**

```javascript
// app/website/src/utils/chatProvider.test.js
import { describe, expect, it } from 'vitest';
import { slashProviderFor } from './chatProvider';

describe('slashProviderFor', () => {
  it('agentcore 三种写法都映射为空串（不展示斜杠命令面）', () => {
    expect(slashProviderFor('agentcore')).toBe('');
    expect(slashProviderFor('agent-core')).toBe('');
    expect(slashProviderFor('agent_core')).toBe('');
    expect(slashProviderFor('AgentCore')).toBe('');
  });

  it('codex 映射为 codex', () => {
    expect(slashProviderFor('codex')).toBe('codex');
    expect(slashProviderFor('CODEX')).toBe('codex');
  });

  it('其余（含缺省）映射为 claude', () => {
    expect(slashProviderFor('claudecode')).toBe('claude');
    expect(slashProviderFor('claude')).toBe('claude');
    expect(slashProviderFor('')).toBe('claude');
    expect(slashProviderFor(undefined)).toBe('claude');
    expect(slashProviderFor(null)).toBe('claude');
  });
});
```

- [ ] **Step 2: 运行确认失败**

```bash
npx vitest run src/utils/chatProvider.test.js
```

Expected: FAIL — `Failed to resolve import "./chatProvider"`

- [ ] **Step 3: 写最小实现**

```javascript
// app/website/src/utils/chatProvider.js
// 返回该会话可用的斜杠命令面：'claude' | 'codex' | ''（空串 = 无命令面）。
//
// agentcore 归一成空串是**纯客户端抑制**：调用方见到空串就不发
// slash.commands.list、不打开补全面板。不要据此放宽后端 provider 判定——
// 后端只把字面量 'agentcore' 路由到内置内核（agent_ai.go:3900），
// agent-core / agent_core 只是防御性宽松匹配。
export function slashProviderFor(provider) {
  const p = String(provider || 'claudecode').toLowerCase();
  if (p === 'agentcore' || p === 'agent-core' || p === 'agent_core') return '';
  return p === 'codex' ? 'codex' : 'claude';
}
```

- [ ] **Step 4: 运行确认通过**

```bash
npx vitest run src/utils/chatProvider.test.js
```

Expected: 3 passed

- [ ] **Step 5: 暂存（不提交）**

```bash
git add src/utils/chatProvider.js src/utils/chatProvider.test.js
```

---

### Task 3: 导航层 provider 预置（TDD）

**Files:**
- Modify: `app/website/src/composables/useNavigation.js`
- Test: `app/website/src/composables/useNavigation.test.js`

- [ ] **Step 1: 写失败测试**

```javascript
// app/website/src/composables/useNavigation.test.js
import { beforeEach, describe, expect, it } from 'vitest';
import { useNavigation } from './useNavigation';

describe('useNavigation chat provider preset', () => {
  beforeEach(() => {
    const nav = useNavigation();
    nav.showChat();      // 清空预置
    nav.showDashboard();
  });

  it('showChat({ provider }) 写入预置并切到 chat 页', () => {
    const nav = useNavigation();
    nav.showChat({ provider: 'agentcore' });
    expect(nav.chatProviderPreset.value).toBe('agentcore');
    expect(nav.currentPage.value).toBe('chat');
  });

  it('无参调用清空预置（回到 ChatPage 自身的 claudecode 默认）', () => {
    const nav = useNavigation();
    nav.showChat({ provider: 'agentcore' });
    nav.showChat();
    expect(nav.chatProviderPreset.value).toBe('');
    expect(nav.currentPage.value).toBe('chat');
  });

  it('空白/非法 options 归一为空串而不是留下脏值', () => {
    const nav = useNavigation();
    nav.showChat({ provider: '  AgentCore  ' });
    expect(nav.chatProviderPreset.value).toBe('agentcore');
    nav.showChat(null);
    expect(nav.chatProviderPreset.value).toBe('');
  });

  it('预置对多个 consumer 是同一份模块级状态', () => {
    const a = useNavigation();
    const b = useNavigation();
    a.showChat({ provider: 'agentcore' });
    expect(b.chatProviderPreset.value).toBe('agentcore');
  });
});
```

- [ ] **Step 2: 运行确认失败**

```bash
npx vitest run src/composables/useNavigation.test.js
```

Expected: FAIL — `chatProviderPreset` 为 undefined / `showChat` 不写入

- [ ] **Step 3: 改实现**

把 `app/website/src/composables/useNavigation.js` 整文件替换为：

```javascript
import { ref } from 'vue';

const currentPage = ref('dashboard');

// 聊天页的 provider 预置：仪表盘「AliangCode 聊天」入口写入 'agentcore'，
// ChatPage 新建会话时读取。模块级 = 跨组件共享；浏览器刷新自然重置为空，
// 不引入陈旧状态（不持久化）。
const chatProviderPreset = ref('');

export function useNavigation() {
  function showPage(page) {
    currentPage.value = page;
  }

  function showSettings() {
    showPage('settings');
  }

  function showAgent() {
    showPage('agent');
  }

  function showUserCenter() {
    showPage('user');
  }

  function showLogs() {
    showPage('log');
  }

  function showDashboard() {
    showPage('dashboard');
  }

  function showChat(options = {}) {
    chatProviderPreset.value = String(options?.provider || '').trim().toLowerCase();
    showPage('chat');
  }

  return {
    currentPage,
    chatProviderPreset,
    showPage,
    showSettings,
    showAgent,
    showUserCenter,
    showLogs,
    showDashboard,
    showChat
  };
}
```

- [ ] **Step 4: 运行确认通过**

```bash
npx vitest run src/composables/useNavigation.test.js
```

Expected: 4 passed

- [ ] **Step 5: 暂存（不提交）**

```bash
git add src/composables/useNavigation.js src/composables/useNavigation.test.js
```

---

### Task 4: i18n 新增 3 个 key、删除死 key

**Files:**
- Modify: `app/website/src/i18n/zh.js`
- Modify: `app/website/src/i18n/en.js`

- [ ] **Step 1: zh.js 新增 `dash_aliangChat`**

在 `dash_moreSettings: '更多设置',` 之后插入一行：

```javascript
  dash_aliangChat: 'AliangCode 聊天',
```

- [ ] **Step 2: zh.js 新增两个 chat key**

在 `chat_emptyTitle: '新对话',` 之后插入：

```javascript
  chat_rename: '重命名',
  chat_renamePlaceholder: '输入会话名称',
```

- [ ] **Step 3: zh.js 删除死 key**

删除整行 `dash_quickChat: '快捷聊天',`（原 :403）。该 key 的唯一引用是本次要删掉的注释死块，删除后无引用。

> 不要动 `dash_loginModalDesc` 里的「快捷聊天」**文案**——那是自然语言句子，不是 key 引用，属非目标范围。

- [ ] **Step 4: en.js 同步三处**

在 `dash_moreSettings: 'More Settings',` 之后插入：

```javascript
  dash_aliangChat: 'AliangCode Chat',
```

在 `app/website/src/i18n/en.js:213` 的 `chat_emptyTitle: 'New Chat',` 之后插入：

```javascript
  chat_rename: 'Rename',
  chat_renamePlaceholder: 'Enter a conversation name',
```

删除整行 `dash_quickChat: 'Quick Chat',`。

- [ ] **Step 5: 跑 i18n 键对齐测试**

```bash
npx vitest run src/i18n/i18n.test.js
```

Expected: PASS（zh/en key 集合一致）

- [ ] **Step 6: 暂存（不提交）**

```bash
git add src/i18n/zh.js src/i18n/en.js
```

---

### Task 5: 恢复 ChatPage 挂载点（硬前置）

**Files:**
- Modify: `app/website/src/App.vue:29-31`

- [ ] **Step 1: 解注释**

把 `App.vue:29-31`：

```vue
    <!-- Chat feature hidden temporarily
    <ChatPage v-if="currentPage === 'chat'" />
    -->
```

替换为：

```vue
    <ChatPage v-if="currentPage === 'chat'" />
```

`ChatPage` 的 import（`:41`）与 `const { currentPage } = useNavigation()`（`:57`）均已存在，无需其它改动。

- [ ] **Step 2: 确认没有残留注释标记**

```bash
grep -rn "Chat feature hidden temporarily" src/ || echo "clean"
```

Expected: `clean`

- [ ] **Step 3: 构建确认模板可编译**

```bash
npm run build
```

Expected: `vite build` 成功。

> 注：主工作区的 `app/website/package.json` 被改成了 `vite build && node ../../scripts/check-dist.mjs`，但该改动与 `scripts/check-dist.mjs` **都还没提交**，所以从 HEAD 拉出的 worktree 里 build 脚本仍是裸 `vite build`。这是预期行为——本单不顺手把那笔护栏改动卷进来。

- [ ] **Step 4: 暂存（不提交）**

```bash
git add src/App.vue
```

---

### Task 6: 仪表盘入口（桌面 + 移动）

**Files:**
- Modify: `app/website/src/components/DashboardPage.vue:164-175`（桌面死块）
- Modify: `app/website/src/components/DashboardPage.vue:349-350` 之后（移动面板）
- Modify: `app/website/src/components/DashboardPage.vue:1027`（解构）

- [ ] **Step 1: 桌面侧栏——注释块替换为真按钮**

把 `DashboardPage.vue:164-175` 整段注释块：

```vue
        <!-- Chat feature hidden temporarily
        <button
          type="button"
          :disabled="!isAuthenticated"
          @click="openQuickChat"
          class="w-full flex items-center gap-3 px-4 py-2.5 bg-white dark:bg-slate-900 border border-slate-200 dark:border-slate-700 rounded text-sm font-medium hover:border-primary transition-colors"
          :class="!isAuthenticated ? 'cursor-not-allowed opacity-60 hover:border-slate-200 dark:hover:border-slate-700' : ''"
        >
          <span class="material-symbols-outlined text-slate-400 text-lg">chat_bubble</span>
          {{ t('dash_quickChat') }}
        </button>
        -->
```

替换为：

```vue
        <button
          type="button"
          data-testid="aliang-chat-entry-sidebar"
          :disabled="!isAuthenticated"
          @click="showChat({ provider: 'agentcore' })"
          class="w-full flex items-center gap-3 px-4 py-2.5 bg-white dark:bg-slate-900 border border-slate-200 dark:border-slate-700 rounded text-sm font-medium hover:border-primary transition-colors"
          :class="!isAuthenticated ? 'cursor-not-allowed opacity-60 hover:border-slate-200 dark:hover:border-slate-700' : ''"
        >
          <span class="material-symbols-outlined text-slate-400 text-lg">smart_toy</span>
          {{ t('dash_aliangChat') }}
        </button>
```

样式与「快速配置」「更多设置」逐字同构（仅 icon/文案/处理器不同），`data-testid` 沿用既有 `agent-entry-sidebar` 命名惯例。

- [ ] **Step 2: 移动面板——快速配置按钮之后插入同款入口**

在 `DashboardPage.vue` 的移动快捷工具块里，`{{ t('dash_quickSetup') }}</button>` 之后、`更多设置`按钮之前插入：

```vue
            <button
              type="button"
              data-testid="aliang-chat-entry-panel"
              :disabled="!isAuthenticated"
              class="flex w-full items-center gap-3 rounded border bg-white px-4 py-3 text-sm font-medium dark:bg-slate-900"
              :class="!isAuthenticated ? 'cursor-not-allowed opacity-60 border-slate-200 dark:border-slate-700' : 'border-slate-200 hover:border-primary dark:border-slate-700'"
              @click="showChat({ provider: 'agentcore' })"
            >
              <span class="material-symbols-outlined text-lg text-slate-400">smart_toy</span>
              {{ t('dash_aliangChat') }}
            </button>
```

`py-3` 保证触控高度 ≥40px（移动端约定）；禁用样式与同块「快速配置」一致。

- [ ] **Step 3: 解构 `showChat`**

`DashboardPage.vue:1027`：

```javascript
const { currentPage, showSettings, showAgent } = useNavigation();
```

改为：

```javascript
const { currentPage, showSettings, showAgent, showChat } = useNavigation();
```

- [ ] **Step 4: 确认没有遗留的未定义处理器**

```bash
grep -rn "openQuickChat\|dash_quickChat" src/ || echo "clean"
```

Expected: `clean`

- [ ] **Step 5: 确认 `showChat` 真的进了 setup 作用域（**必做**）**

```bash
grep -n "showChat" src/components/DashboardPage.vue
```

Expected: 恰好 **3** 行 —— Step 3 的解构 1 行 + 桌面按钮 `@click` 1 行 + 移动按钮 `@click` 1 行。
若只有 2 行（两个 `@click`、没有解构），说明 Step 3 漏做或拼错，按钮点下去才会抛错。

⚠️ **本步不能靠 `npm run build` 兜底**（已实测）：`<script setup>` 里模板引用了 setup
作用域中不存在的标识符**不会**编译失败，Vue 会把它编译成
`onClick: $event => (_ctx.showChat({...}))`，构建全绿、只在点击时抛
`TypeError: _ctx.showChat is not a function`。本单的 e2e 也不覆盖这个新按钮，
所以 grep 是唯一自动化拦截点，跳过它等于把风险全部压到人工验收第 1 项。

- [ ] **Step 6: 构建确认可编译**

```bash
npm run build
```

Expected: 成功。注意这条只证明**语法/模板可编译**，不证明 `showChat` 绑定正确（见 Step 5）。

- [ ] **Step 7: 暂存（不提交）**

```bash
git add src/components/DashboardPage.vue
```

---

### Task 7: ChatPage 读取 provider 预置

**Files:**
- Modify: `app/website/src/components/ChatPage.vue:272`（既有解构）
- Modify: `app/website/src/components/ChatPage.vue:410-424`（`createConversation`）

- [ ] **Step 1: 追加解构，不新增 import**

`ChatPage.vue:272` 现为：

```javascript
const { showDashboard } = useNavigation();
```

改为：

```javascript
const { showDashboard, chatProviderPreset } = useNavigation();
```

（`import { useNavigation } from '../composables/useNavigation';` 已在 `:266`，不要重复 import。）

- [ ] **Step 2: 新建会话带 provider**

`createConversation()` 的对象字面量中，`createdAt: now(),` 之前插入：

```javascript
    provider: chatProviderPreset.value || 'claudecode',
```

改完函数体应为：

```javascript
function createConversation() {
  const conv = {
    id: generateId(),
    title: '',
    messages: [],
    approvals: [],
    provider: chatProviderPreset.value || 'claudecode',
    createdAt: now(),
    updatedAt: now()
  };
  conversations.value.unshift(conv);
  activeId.value = conv.id;
  showMobileSidebar.value = false;
  saveToStorage();
  nextTick(() => inputRef.value?.focus());
}
```

说明：`sendMessage()` 早已用 `conv.provider || 'claudecode'` 发 `ai.session.create`，无需改动。

- [ ] **Step 3: 确认 preset 已进入作用域 + 构建**

```bash
grep -n "chatProviderPreset" src/components/ChatPage.vue
npm run build
```

Expected: grep 恰好 **2** 行（Step 1 的解构 + Step 2 的读取）；`npm run build` 成功。

⚠️ 与 Task 6 Step 5 同理，`npm run build` **不校验** setup 作用域里的标识符：
漏做 Step 1 时 `chatProviderPreset` 在脚本里是未声明变量，构建照样全绿，
直到用户点「+ 新建对话」才抛 `ReferenceError`。grep 是本步唯一的拦截点。

- [ ] **Step 4: 暂存（不提交）**

```bash
git add src/components/ChatPage.vue
```

---

### Task 8: ChatPage 屏蔽 agentcore 的斜杠命令面

**Files:**
- Modify: `app/website/src/components/ChatPage.vue:319-322`（`slashProviderOf`）
- Modify: `app/website/src/components/ChatPage.vue:513-529`（`ensureSlashCommands`）
- Modify: `app/website/src/components/ChatPage.vue:531-540`（`onSlashInput`）
- Modify: `app/website/src/components/ChatPage.vue` 顶部 import 区（新增一行 import）

- [ ] **Step 1: 引入工具函数**

在 `ChatPage.vue` 的 import 区（`import { useNavigation } from '../composables/useNavigation';` 附近）加入：

```javascript
import { slashProviderFor } from '../utils/chatProvider';
```

- [ ] **Step 2: `slashProviderOf` 委托给工具函数**

```javascript
function slashProviderOf(conv) {
  return slashProviderFor(conv?.provider);
}
```

空串 = 无命令面（agentcore）。

- [ ] **Step 3: `ensureSlashCommands` 遇空 provider 直接返回（不发请求）**

```javascript
async function ensureSlashCommands() {
  const provider = slashActiveProvider.value;
  // agentcore 内核没有 claude/codex 的插件命令面。绝不发空 provider 请求：
  // 后端 normalizeSlashProvider 把空值当「未指定」并返回全部 provider 的命令，
  // 正是要避免的误导。
  if (!provider) return;
  if (slashCommandsByProvider.value[provider] || slashPending[provider]) return;
  slashPending[provider] = true;
  try {
    await ensureAISocket();
    sendAI({
      type: 'slash.commands.list',
      request_id: 'slash-' + provider,
      project_path: activeConversation.value?.projectPath || '',
      provider,
    });
  } catch (_) {
    // socket unavailable: leave uncached so a later retry can fill it in
    slashPending[provider] = false;
  }
}
```

- [ ] **Step 4: `onSlashInput` 遇空 provider 不打开面板**

```javascript
function onSlashInput() {
  const token = slashToken(inputText.value);
  // 无命令面（agentcore）：`/` 按普通文本发送，不弹补全。
  if (token === null || !slashActiveProvider.value) {
    slashOpen.value = false;
    return;
  }
  ensureSlashCommands();
  slashOpen.value = true;
  slashIndex.value = 0;
}
```

`slashMatches` 无需改动：`slashCommandsByProvider['']` 为 `undefined` → 列表天然为空。

- [ ] **Step 5: 构建 + 全量单测**

```bash
npm run build && npx vitest run
```

Expected: build 成功，vitest 全绿

- [ ] **Step 6: 暂存（不提交）**

```bash
git add src/components/ChatPage.vue
```

---

### Task 9: 会话重命名（列表项 + 顶栏）

**Files:**
- Modify: `app/website/src/components/ChatPage.vue:29-64`（三组列表行结构改造）
- Modify: `app/website/src/components/ChatPage.vue:75-107`（顶栏标题/铅笔）
- Modify: `app/website/src/components/ChatPage.vue` 脚本区（新增重命名状态与函数 + import）

背景（必须遵守）：现有列表行是 `<button>` 包 `<div class="truncate">`。把 `<input>` 或铅笔 `<button>` 嵌进 `<button>` 违反 HTML 内容模型，`@click.stop` 挡不住非法的 DOM 嵌套与焦点异常。**必须先改容器**为非按钮元素。

- [ ] **Step 1: 新增 import**

在 `import { slashProviderFor } from '../utils/chatProvider';` 之后加：

```javascript
import { normalizeConversationTitle } from '../utils/chatTitle';
```

- [ ] **Step 2: 新增重命名状态与函数**

放在 `function deleteConversation() {` 之前：

```javascript
// --- 会话重命名（仅本地标题，不同步 native / 服务端） ---
const renamingId = ref('');
// 'list' | 'header'：同一会话在列表与顶栏各有一个可编辑位点，
// 必须靠它保证**同一时刻只渲染一个输入框**，否则键盘焦点会落到 DOM 里靠后的那个。
const renameSurface = ref('');
const renameDraft = ref('');
// v-for 内的 ref 会收集成数组，故用函数 ref 只记住当前那一个输入框。
let renameInputEl = null;

function startRename(conv, surface = 'list') {
  if (!conv?.id) return;
  renamingId.value = conv.id;
  renameSurface.value = surface;
  renameDraft.value = conv.title || '';
  nextTick(() => {
    renameInputEl?.focus();
    renameInputEl?.select();
  });
}

function commitRename() {
  if (!renamingId.value) return;
  const conv = conversations.value.find(c => c.id === renamingId.value);
  if (conv) {
    conv.title = normalizeConversationTitle(renameDraft.value);
    saveToStorage(); // 刻意不动 updatedAt：改名不是内容更新，不该把会话顶到分组首位
  }
  cancelRename();
}

function cancelRename() {
  renamingId.value = '';
  renameSurface.value = '';
  renameDraft.value = '';
  renameInputEl = null;
}
```

- [ ] **Step 3: 改造「今天」分组的列表行**

把 `ChatPage.vue:29-38` 的 `<button ...>...</button>` 整块替换为：

```vue
          <div
            v-for="c in groupedConversations.today"
            :key="c.id"
            role="button"
            tabindex="0"
            class="group w-full cursor-pointer px-4 py-2.5 text-left text-sm transition-colors focus:outline-none focus-visible:ring-2 focus-visible:ring-primary/40"
            :class="c.id === activeId ? 'bg-primary/10 text-primary font-medium' : 'text-slate-700 hover:bg-slate-100 dark:text-slate-300 dark:hover:bg-slate-800'"
            @click="selectConversation(c.id)"
            @keydown.enter.prevent="selectConversation(c.id)"
            @keydown.space.prevent="selectConversation(c.id)"
          >
            <input
              v-if="renamingId === c.id && renameSurface === 'list'"
              v-model="renameDraft"
              :ref="(el) => { if (el) renameInputEl = el; }"
              type="text"
              maxlength="60"
              :placeholder="t('chat_renamePlaceholder')"
              class="w-full rounded border border-primary/40 bg-white px-2 py-1 text-sm outline-none dark:bg-slate-800"
              @click.stop
              @keydown.enter.prevent.stop="commitRename"
              @keydown.esc.prevent.stop="cancelRename"
              @blur="commitRename"
            />
            <div v-else class="flex items-center gap-2">
              <div class="min-w-0 flex-1 truncate">{{ c.title || t('chat_emptyTitle') }}</div>
              <button
                type="button"
                class="shrink-0 rounded p-1 text-slate-400 opacity-100 transition-opacity hover:bg-slate-200 hover:text-slate-600 md:opacity-0 md:group-hover:opacity-100 dark:hover:bg-slate-700"
                :aria-label="t('chat_rename')"
                @click.stop="startRename(c, 'list')"
                @keydown.enter.stop
                @keydown.space.stop
              >
                <span class="material-symbols-outlined text-base">edit</span>
              </button>
            </div>
          </div>
```

- [ ] **Step 4: 同样改造「昨天」「更早」两组**

把 `ChatPage.vue:42-51`（`groupedConversations.yesterday`）与 `:55-64`（`groupedConversations.older`）的两块 `<button>` 按 Step 3 **逐字套用**，只改 `v-for` 的数据源。即：

- 「昨天」组：与 Step 3 完全相同，仅 `v-for="c in groupedConversations.yesterday"`
- 「更早」组：与 Step 3 完全相同，仅 `v-for="c in groupedConversations.older"`

三组必须全部改造——只改一组会造成同样的非法嵌套残留。注意三组的输入框条件都必须是
`v-if="renamingId === c.id && renameSurface === 'list'"`（顶栏也有一份编辑器，缺了
`renameSurface` 判断会让同一会话同时渲染两个输入框）。

- [ ] **Step 5: 顶栏标题支持行内重命名**

把 `ChatPage.vue:83-85`：

```vue
        <h3 class="flex-1 truncate text-sm font-semibold text-slate-900 dark:text-white">
          {{ activeConversation?.title || t('chat_emptyTitle') }}
        </h3>
```

替换为：

```vue
        <input
          v-if="activeId && renamingId === activeId && renameSurface === 'header'"
          v-model="renameDraft"
          :ref="(el) => { if (el) renameInputEl = el; }"
          type="text"
          maxlength="60"
          :placeholder="t('chat_renamePlaceholder')"
          class="min-w-0 flex-1 rounded border border-primary/40 bg-white px-2 py-1 text-sm outline-none dark:bg-slate-800"
          @click.stop
          @keydown.enter.prevent.stop="commitRename"
          @keydown.esc.prevent.stop="cancelRename"
          @blur="commitRename"
        />
        <h3 v-else class="flex-1 truncate text-sm font-semibold text-slate-900 dark:text-white">
          {{ activeConversation?.title || t('chat_emptyTitle') }}
        </h3>
        <button
          v-if="activeId && renamingId !== activeId"
          type="button"
          class="rounded p-1 text-slate-400 hover:bg-slate-100 hover:text-slate-600 dark:hover:bg-slate-800"
          :aria-label="t('chat_rename')"
          @click="startRename(activeConversation, 'header')"
        >
          <span class="material-symbols-outlined text-lg">edit</span>
        </button>
```

- [ ] **Step 6: 构建 + 全量单测**

```bash
npm run build && npx vitest run
```

Expected: 成功 + 全绿

- [ ] **Step 7: 暂存（不提交）**

```bash
git add src/components/ChatPage.vue
```

---

### Task 10: 全量验证

**Files:** 无

- [ ] **Step 1: 全量单测**

```bash
cd /Users/mac/MyProgram/GoProgram/nursor/alianggate/.worktrees/aliangcode-chat-entry/app/website
npx vitest run
```

Expected: 全绿；与本单新增的 3 个测试文件（chatTitle / chatProvider / useNavigation）共 12 个新用例全部 PASS。

- [ ] **Step 2: 生产构建**

```bash
npm run build
```

Expected: `vite build` 成功（worktree 里不含未提交的 `check-dist.mjs` 护栏，见 Task 5 Step 3 的说明）。

- [ ] **Step 3: 移动端回归（必跑）**

```bash
npx playwright test tests/e2e/mobile-responsive.spec.js
```

Expected: 全绿。该文件覆盖仪表盘布局、折叠面板、设置子页、Agent 入口、登录弹窗、快速配置弹窗（390 / 768 / 1280 三档），正是本次改动区域。

- [ ] **Step 4: 自查 diff 范围**

```bash
git status --short
git diff --stat
```

Expected: 只出现本计划表格里的 **11** 个文件 —— 表格有 10 行，其中 `i18n/zh.js` 与
`i18n/en.js` 共用一行，故实际是 11 个路径；另加 plan/spec 两份文档。`dist/` 未进 diff
（已 gitignore）。若多出其它路径，说明改动越界，回退后再继续。

- [ ] **Step 5: 人工验收清单（需真实登录态，逐条过）**

1. 桌面侧栏「快速配置」与「更多设置」之间出现「AliangCode 聊天」，点击进入聊天页；
2. 移动端（390px）展开控制面板，快捷工具区出现同款按钮，触控高度足够；
3. 未登录时两处按钮置灰不可点；
4. 进入后**主区域正常渲染不是空白**；新建会话说一句话 → `~/.aliang/ai-sessions/<sessionId>` 出现 header/user/assistant 行（证明走的是内置内核）；
5. agentcore 会话输入 `/` 不弹斜杠补全面板，按回车按普通文本发送；
6. 列表项与顶栏均可重命名：Enter 生效、Esc 取消、失焦提交；改当前会话时**只出现一个编辑框**
   （点列表铅笔 → 焦点在列表那行；点顶栏铅笔 → 焦点在顶栏）；清空标题后显示「新对话」
   （不是固化的旧译文）；刷新页面后标题保持；
7. 旧会话（provider 缺省=claudecode）点开继续用 claude，`/` 补全正常；
8. 刷新页面后再从入口进入，新会话仍预置 agentcore；不经过入口直接刷新，新建会话回落到 claudecode。

- [ ] **Step 6: 向用户申请提交许可**

按项目 `CLAUDE.md`，**不得自动 commit**。整理 `git status` / `git diff` 摘要后向用户请示，获明确同意后再按仓库规范提交（中文标题「新增：/修复：」前缀）。

---

## 附：容易被做错的三个点（给实现者）

1. **App.vue 挂载点**（Task 5）是硬前置。漏做的话，点入口只会得到空白主区域，其它改动全部「看起来没生效」。
2. **空标题必须存空串**（Task 1/9），绝不能让 `t('chat_emptyTitle')` 的译文落进 localStorage——否则切语言后旧标题不跟随。
3. **斜杠屏蔽是客户端行为**（Task 8）：空 provider 时**不发请求**。后端把空 provider 当「全量返回」，方向相反，不能以后端为据。
