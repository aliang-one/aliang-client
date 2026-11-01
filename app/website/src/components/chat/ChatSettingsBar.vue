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
      <span class="ml-2 hidden shrink-0 text-slate-400 md:inline">{{ t('chat_settingsModel') }}</span>
      <select
        v-model="activeModel"
        data-testid="chat-settings-model"
        class="min-h-[40px] shrink-0 rounded-lg border border-slate-200 bg-white px-1 text-slate-600 outline-none transition focus:border-primary focus:ring-2 focus:ring-primary/30 disabled:cursor-not-allowed disabled:opacity-60 dark:border-slate-700 dark:bg-slate-800 dark:text-slate-300 md:min-h-0"
        :disabled="activeRunning"
        :aria-label="t('chat_settingsModel')"
      >
        <option v-for="opt in modelOptions" :key="opt.value" :value="opt.value">{{ opt.label }}</option>
      </select>
      <input
        v-if="modelCustomActive"
        v-model="modelCustomDraft"
        type="text"
        class="h-9 w-36 shrink-0 rounded-lg border border-slate-200 bg-white px-2 text-xs outline-none transition focus:border-primary focus:ring-2 focus:ring-primary/30 dark:border-slate-600 dark:bg-slate-800"
        :placeholder="t('chat_customModel')"
        @keydown.enter="onCustomModelEnter"
        @blur="commitCustomModel"
      />
      <span class="ml-2 hidden shrink-0 text-slate-400 md:inline">{{ t('chat_settingsEffort') }}</span>
      <select
        v-model="activeEffort"
        data-testid="chat-settings-effort"
        class="min-h-[40px] shrink-0 rounded-lg border border-slate-200 bg-white px-1 text-slate-600 outline-none transition focus:border-primary focus:ring-2 focus:ring-primary/30 disabled:cursor-not-allowed disabled:opacity-60 dark:border-slate-700 dark:bg-slate-800 dark:text-slate-300 md:min-h-0"
        :disabled="activeRunning"
        :aria-label="t('chat_settingsEffort')"
      >
        <option v-for="opt in EFFORT_OPTIONS" :key="opt.value" :value="opt.value">{{ opt.value || t('chat_modelDefault') }}</option>
      </select>
      <!-- 「下轮生效」角标（Task 12）：effort/model 改动落值即挂（conv.pendingConfig，
           随 applySessionSettings 置位），ai.run.started 路由回本会话即清——新回合
           已按新配置跑。瞬态不落盘，重载后自然退场。 -->
      <span
        v-if="conversation?.pendingConfig"
        data-testid="chat-effort-pending"
        class="shrink-0 rounded bg-amber-100 px-1.5 py-0.5 text-[10px] text-amber-700 dark:bg-amber-900/40 dark:text-amber-300"
      >{{ t('chat_effortPending') }}</span>
      <div
        v-if="createError"
        data-testid="chat-settings-error"
        class="ml-2 shrink-0 truncate text-rose-500"
        :title="createError"
      >{{ createError }}</div>
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
  AGENTCORE_MODEL_OPTIONS,
  EFFORT_OPTIONS,
  MODEL_CUSTOM_VALUE,
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
});

// apply-settings(patch)：统一写入口在 useAliangChat.applySessionSettings
// （running 守卫/持久化/清错/服务端原位同步），本组件只递交补丁。
const emit = defineEmits(['apply-settings']);

const { t } = useI18n();

// --- 设置条 UI 状态（自 ChatPage 原位迁入） ---
const directoryPanelOpen = ref(false);
const customPathDraft = ref('');
const recentProjects = ref([]);
const recentProjectsLoaded = ref(false);
const modelCustomActive = ref(false);
const modelCustomDraft = ref('');

const directoryLocked = computed(() => isDirectoryLocked(props.conversation));

const directoryLabel = computed(() => props.conversation?.projectPath || t('chat_pathDefault'));

const modelOptions = computed(() => AGENTCORE_MODEL_OPTIONS.map(o => ({
  ...o,
  label: o.value === '' ? t('chat_modelDefault')
    : o.value === MODEL_CUSTOM_VALUE ? t('chat_customModel')
    : o.label,
})));

// select 当前值：预设模型→自身；非预设自定义值→哨兵（输入框承接显示）。
const modelSelectValue = computed(() => {
  const m = props.conversation?.model || '';
  if (!m) return '';
  return isPresetModel(m) ? m : MODEL_CUSTOM_VALUE;
});

function isPresetModel(m) {
  return AGENTCORE_MODEL_OPTIONS.some(o => o.value === m && o.value !== MODEL_CUSTOM_VALUE);
}

function onModelSelect(value) {
  const conv = props.conversation;
  if (!conv || props.activeRunning) return;
  if (value === MODEL_CUSTOM_VALUE) {
    modelCustomDraft.value = conv.model && !isPresetModel(conv.model) ? conv.model : '';
    modelCustomActive.value = true;
    return;
  }
  modelCustomActive.value = false;
  emit('apply-settings', { model: value });
}

function commitCustomModel() {
  const conv = props.conversation;
  if (!conv) return;
  const model = modelCustomDraft.value.trim();
  modelCustomActive.value = false;
  if (!model) return; // 空输入视作取消，保持原值
  emit('apply-settings', { model });
}

function onCustomModelEnter(e) {
  if (isComposingEvent(e)) return;
  e.preventDefault();
  commitCustomModel();
}

// v-model 写法让 select 每次渲染都回写 el.value：取消自定义输入后选择框
// 立即弹回当前值，不会停留在已被丢弃的哨兵选项上（与 activeEffort 同构）。
const activeModel = computed({
  get: () => modelSelectValue.value,
  set: (v) => onModelSelect(v),
});

const activeEffort = computed({
  get: () => props.conversation?.effort || '',
  set(value) {
    const conv = props.conversation;
    if (!conv || props.activeRunning) return;
    emit('apply-settings', { effort: value });
  },
});

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

// 切会话收起面板：原 ChatPage watch(activeId) 里的两项 UI 复位迁入本组件。
// conversation prop 的对象身份只在 activeId 变化时改变（activeConversation 在
// 会话池里按同引用命中），触发集合与原 watch 等价。
watch(() => props.conversation, () => {
  directoryPanelOpen.value = false;
  modelCustomActive.value = false;
});
</script>
