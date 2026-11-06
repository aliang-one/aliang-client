<template>
  <!-- Session settings bar: directory / model / effort -->
  <div v-if="conversation" class="relative border-b border-slate-200 dark:border-slate-800">
    <!-- 内层滚动行：overflow-x-auto 会连 y 轴一起裁剪（CSS 规范行为），
         absolute 浮层若放行内会被整个裁掉，必须挂到外层包裹容器上。 -->
    <div class="flex items-center gap-2 overflow-x-auto px-4 py-1.5 text-xs">
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
        class="flex min-h-[40px] max-w-[16rem] shrink-0 items-center gap-1 rounded-lg border border-slate-200 bg-white px-2 text-slate-600 transition hover:border-slate-300 hover:bg-slate-100 focus-visible:ring-2 focus-visible:ring-primary/30 disabled:cursor-not-allowed disabled:opacity-60 dark:border-slate-700 dark:bg-slate-800 dark:text-slate-300 dark:hover:bg-slate-800 md:min-h-0"
        :disabled="activeRunning"
        :title="directoryLabel"
        :aria-label="t('chat_settingsDirectory')"
        @click="toggleDirectoryPanel"
      >
        <span class="material-symbols-outlined text-sm">folder</span>
        <span class="truncate">{{ directoryLabel }}</span>
      </button>
      <!-- 模型/effort 选择器迁 composer 工具行(2026-11-05,学 Cursor;
           单入口纪律——本条只留 目录/更多/预算面板,状态源唯一) -->
      <!-- 「更多」输出预算/会话级 MCP·技能开关（master 并行线 ChatPage 迁入）：
           面板数据与动作收在 useAliangChat，本组件只承载展开态与递交。 -->
      <button
        type="button"
        data-testid="chat-settings-more"
        class="ml-1 flex min-h-[40px] shrink-0 items-center gap-0.5 rounded-lg border border-slate-200 px-2 text-slate-500 transition hover:bg-slate-100 focus-visible:ring-2 focus-visible:ring-primary/30 dark:border-slate-700 dark:text-slate-400 dark:hover:bg-slate-800 md:min-h-0"
        :aria-expanded="moreSettingsOpen ? 'true' : 'false'"
        :title="t('chat_settingsMore')"
        @click="toggleMoreSettings"
      >
        <span class="material-symbols-outlined text-sm">tune</span>
        <span class="hidden md:inline">{{ t('chat_settingsMore') }}</span>
      </button>
      <!-- 「下轮生效」角标随模型/effort 选择器迁 composer 工具行（2026-11-05）。 -->
      <div
        v-if="createError"
        data-testid="chat-settings-error"
        class="ml-2 shrink-0 truncate text-rose-500"
        :title="createError"
      >{{ createError }}</div>
    </div>

    <!-- 输出预算（默认折叠）：max_tokens / context_window 都只在用户显式
         填写时下发；留空 = 不下发 = 内核默认（输出 64000、窗口 200k）。 -->
    <div
      v-if="moreSettingsOpen"
      data-testid="chat-settings-more-panel"
      class="flex flex-wrap items-center gap-x-4 gap-y-2 border-t border-slate-100 px-4 py-2 text-xs dark:border-slate-800"
    >
      <label class="flex items-center gap-1.5 text-slate-500 dark:text-slate-400">
        <span>{{ t('chat_settingsMaxTokens') }}</span>
        <input
          v-model="maxTokensModel"
          type="number"
          min="1024"
          max="128000"
          step="1024"
          data-testid="chat-settings-max-tokens"
          class="h-8 w-24 rounded-lg border border-slate-200 bg-white px-2 font-mono text-xs text-slate-700 outline-none transition focus:border-primary focus:ring-2 focus:ring-primary/30 disabled:opacity-60 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-200"
          :placeholder="t('chat_settingsMaxTokensPlaceholder')"
          :disabled="activeRunning"
          @change="emit('commit-max-tokens')"
        />
      </label>
      <label class="flex items-center gap-1.5 text-slate-500 dark:text-slate-400">
        <span>{{ t('chat_settingsContextWindow') }}</span>
        <input
          v-model="contextWindowModel"
          type="number"
          min="100000"
          max="1000000"
          step="10000"
          data-testid="chat-settings-context-window"
          class="h-8 w-28 rounded-lg border border-slate-200 bg-white px-2 font-mono text-xs text-slate-700 outline-none transition focus:border-primary focus:ring-2 focus:ring-primary/30 disabled:opacity-60 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-200"
          :placeholder="t('chat_settingsContextWindowPlaceholder')"
          :disabled="activeRunning"
          @change="emit('commit-context-window')"
        />
      </label>
      <span class="text-[11px] text-slate-400 dark:text-slate-500">{{ t('chat_settingsBudgetHint') }}</span>
    </div>

    <!-- 会话级 MCP 开关：只影响本会话（服务端按整列表替换 disabled_mcp_servers）。 -->
    <div
      v-if="moreSettingsOpen"
      data-testid="chat-settings-mcp-panel"
      class="border-t border-slate-100 px-4 py-2 text-xs dark:border-slate-800"
    >
      <div class="flex items-center gap-1.5 text-slate-500 dark:text-slate-400">
        <span class="material-symbols-outlined text-sm">hub</span>
        <span class="font-semibold">{{ t('chat_settingsMcpTitle') }}</span>
        <span class="text-[11px] text-slate-400">{{ t('chat_settingsMcpHint') }}</span>
      </div>
      <p v-if="mcpLoadError" class="mt-1 text-[11px] text-rose-500">{{ mcpLoadError }}</p>
      <p v-else-if="mcpLoaded && !mcpServers.length" class="mt-1 text-[11px] text-slate-400">
        {{ t('chat_settingsMcpEmpty') }}
      </p>
      <div v-else-if="mcpServers.length" class="mt-1.5 flex flex-wrap gap-x-4 gap-y-1.5">
        <label
          v-for="server in mcpServers"
          :key="server.name"
          class="flex min-h-[32px] items-center gap-1.5 text-slate-600 dark:text-slate-300"
        >
          <input
            type="checkbox"
            class="h-3.5 w-3.5 rounded border-slate-300 text-primary focus:ring-primary/20 dark:border-slate-600 dark:bg-slate-900"
            :checked="!disabledMcpSet.has(server.name)"
            :disabled="activeRunning"
            :data-testid="'chat-mcp-' + server.name"
            @change="emit('toggle-mcp', server.name)"
          />
          <span class="truncate">{{ server.name }}</span>
        </label>
      </div>
    </div>

    <!-- 会话级技能开关（与 MCP 同构）：取消勾选后该技能不再进 system prompt 清单。 -->
    <div
      v-if="moreSettingsOpen"
      data-testid="chat-settings-skills-panel"
      class="border-t border-slate-100 px-4 py-2 text-xs dark:border-slate-800"
    >
      <div class="flex items-center gap-1.5 text-slate-500 dark:text-slate-400">
        <span class="material-symbols-outlined text-sm">school</span>
        <span class="font-semibold">{{ t('chat_settingsSkillsTitle') }}</span>
        <span class="text-[11px] text-slate-400">{{ t('chat_settingsSkillsHint') }}</span>
      </div>
      <p v-if="mcpLoadError" class="mt-1 text-[11px] text-rose-500">{{ mcpLoadError }}</p>
      <p v-else-if="mcpLoaded && !skills.length" class="mt-1 text-[11px] text-slate-400">
        {{ t('chat_settingsSkillsEmpty') }}
      </p>
      <div v-else-if="skills.length" class="mt-1.5 flex flex-wrap gap-x-4 gap-y-1.5">
        <label
          v-for="skill in skills"
          :key="skill.name"
          class="flex min-h-[32px] items-center gap-1.5 text-slate-600 dark:text-slate-300"
          :title="skill.description || ''"
        >
          <input
            type="checkbox"
            class="h-3.5 w-3.5 rounded border-slate-300 text-primary focus:ring-primary/20 dark:border-slate-600 dark:bg-slate-900"
            :checked="!disabledSkillSet.has(skill.name)"
            :disabled="activeRunning"
            :data-testid="'chat-skill-' + skill.name"
            @change="emit('toggle-skill', skill.name)"
          />
          <span class="truncate">{{ skill.name }}</span>
        </label>
      </div>
    </div>

    <!-- 遮罩与浮层是滚动行的兄弟节点（外层容器的直接子级）：不再被裁剪；
         Esc 在浮层上拦截（.stop），不冒泡到文档级 handleKeydown 的 close()。 -->
    <div v-if="directoryPanelOpen" class="fixed inset-0 z-20" @click="directoryPanelOpen = false"></div>
    <div
      v-if="directoryPanelOpen"
      class="absolute left-2 top-full z-30 mt-1 w-80 rounded-lg border border-slate-200 bg-white p-3 shadow-lg dark:border-slate-600 dark:bg-slate-800"
      @keydown.esc.stop="directoryPanelOpen = false"
    >
      <div class="mb-1 text-xs font-semibold text-slate-400">{{ t('chat_recentProjects') }}</div>
      <div v-if="recentProjects.length === 0" class="px-1 py-1 text-xs text-slate-400">{{ t('chat_noRecentProjects') }}</div>
      <button
        v-for="p in recentProjects"
        :key="p"
        type="button"
        class="flex min-h-[40px] w-full items-center rounded-lg px-1 text-left text-xs text-slate-700 transition hover:bg-primary/10 dark:text-slate-200"
        :title="p"
        @click="pickDirectory(p)"
      ><span class="truncate">{{ p }}</span></button>
      <div class="mt-2 flex gap-1">
        <input
          v-model="customPathDraft"
          type="text"
          :placeholder="t('chat_customPathPlaceholder')"
          class="h-10 min-w-0 flex-1 rounded-lg border border-slate-200 bg-white px-2 text-xs outline-none transition focus:border-primary focus:ring-2 focus:ring-primary/30 dark:border-slate-600 dark:bg-slate-800"
          @keydown.enter="onCustomPathEnter"
          @keydown.esc.stop="directoryPanelOpen = false"
        />
        <button
          type="button"
          class="h-10 shrink-0 rounded-lg bg-primary px-3 text-xs font-semibold text-white transition hover:bg-primary/90"
          @click="commitCustomPath"
        >{{ t('chat_pathConfirm') }}</button>
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref, computed, watch } from 'vue';
import { useI18n } from '../../i18n';
import { isComposingEvent } from '../../composables/useAliangChat';
import {
  isDirectoryLocked,
  recentProjectsFromSessions,
} from '../../utils/chatSettings';

const props = defineProps({
  conversation: { type: Object, default: null },
  activeRunning: { type: Boolean, default: false },
  createError: { type: String, default: '' },
  // 最近项目数据通道：面板首开时拉一次（与侧栏清单共享同一次
  // /api/agent/sessions 请求的 promise 去重），返回设备清单原值。
  loadRecentProjects: { type: Function, required: true },
  // 输出预算/会话级 MCP·技能开关（master 并行线迁入）：数据在 useAliangChat，
  // 经 props 进来，动作经事件递交回 composable（commit/toggle 全走
  // applySessionSettings 的 running 守卫/持久化/清错/服务端同步）。
  maxTokensDraft: { type: String, default: '' },
  contextWindowDraft: { type: String, default: '' },
  mcpServers: { type: Array, default: () => [] },
  skills: { type: Array, default: () => [] },
  mcpLoaded: { type: Boolean, default: false },
  mcpLoadError: { type: String, default: '' },
  ensureMcpLoaded: { type: Function, default: null },
});

// apply-settings(patch)：统一写入口在 useAliangChat.applySessionSettings
// （running 守卫/持久化/清错/服务端原位同步），本组件只递交补丁。
const emit = defineEmits([
  'apply-settings',
  'update:max-tokens-draft',
  'update:context-window-draft',
  'commit-max-tokens',
  'commit-context-window',
  'toggle-mcp',
  'toggle-skill',
]);

const { t } = useI18n();

// --- 设置条 UI 状态（自 ChatPage 原位迁入） ---
const directoryPanelOpen = ref(false);
const customPathDraft = ref('');
const recentProjects = ref([]);
const recentProjectsLoaded = ref(false);
// 「更多」面板展开态（纯 UI）：数据与动作在 useAliangChat，这里只管开关。
const moreSettingsOpen = ref(false);

// 预算草稿双向绑定：值真身在 composable（commit 函数读同源草稿），
// 经 props 进 / 事件出的 computed 桥（与 activeModel/activeEffort 同构）。
const maxTokensModel = computed({
  get: () => props.maxTokensDraft,
  set: (v) => emit('update:max-tokens-draft', v),
});
const contextWindowModel = computed({
  get: () => props.contextWindowDraft,
  set: (v) => emit('update:context-window-draft', v),
});

// 本会话已关闭的 MCP server / 技能名集合（渲染复选框勾选态用，纯派生视图态）。
const disabledMcpSet = computed(() => {
  const list = props.conversation?.disabledMcpServers;
  return new Set(Array.isArray(list) ? list : []);
});
const disabledSkillSet = computed(() => {
  const list = props.conversation?.disabledSkills;
  return new Set(Array.isArray(list) ? list : []);
});

function toggleMoreSettings() {
  moreSettingsOpen.value = !moreSettingsOpen.value;
  if (moreSettingsOpen.value && props.ensureMcpLoaded) void props.ensureMcpLoaded();
}

const directoryLocked = computed(() => isDirectoryLocked(props.conversation));

const directoryLabel = computed(() => props.conversation?.projectPath || t('chat_pathDefault'));

// 模型/effort 选择逻辑已迁 ChatComposerTools(2026-11-05 单入口收编)。

async function toggleDirectoryPanel() {
  const conv = props.conversation;
  if (!conv || directoryLocked.value || props.activeRunning) return;
  directoryPanelOpen.value = !directoryPanelOpen.value;
  if (directoryPanelOpen.value && !recentProjectsLoaded.value) {
    recentProjectsLoaded.value = true;
    // 与侧栏设备清单共享同一次 /api/agent/sessions 请求（promise 去重）；
    // 失败静默（available=false、清单为空），手输路径不受影响。
    // 拆分前清单值在 await 后即时读取，这里保持同一时序（返回值即清单）。
    recentProjects.value = recentProjectsFromSessions(await props.loadRecentProjects());
  }
}

function pickDirectory(path) {
  const conv = props.conversation;
  if (!conv) return;
  customPathDraft.value = '';
  directoryPanelOpen.value = false;
  emit('apply-settings', { projectPath: path });
}

function commitCustomPath() {
  const conv = props.conversation;
  if (!conv) return;
  const path = customPathDraft.value.trim();
  if (!path) return;
  pickDirectory(path);
}

// IME 组合期的 Enter 是「确认候选词」，不是提交。preventDefault 必须放在
// 组合判定之后（模板 .prevent 会先于 handler 执行，把输入法自己的按键处理
// 一起吃掉）——与重命名输入的 onRenameEnter 同一套路。
function onCustomPathEnter(e) {
  if (isComposingEvent(e)) return;
  e.preventDefault();
  commitCustomPath();
}

// 切会话收起面板：原 ChatPage watch(activeId) 里的 UI 复位迁入本组件
// （预算草稿的重同步在数据侧，随 composable 的 watch(activeId) 走）。
// conversation prop 的对象身份只在 activeId 变化时改变（activeConversation 在
// 会话池里按同引用命中），触发集合与原 watch 等价。
watch(() => props.conversation, () => {
  directoryPanelOpen.value = false;
  moreSettingsOpen.value = false;
});
</script>
