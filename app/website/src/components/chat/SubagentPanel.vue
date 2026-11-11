<template>
  <!--
    子agent面板（Task 8, spec 2026-11-07-subagent-panel §三）：入口条点击后的
    列表+详情双栏视图。桌面（≥768px）右侧 drawer（36rem），<768px 全屏遮罩
    （选中条目后列表让位详情，返回键回列表）。移动端约定：flex-col+min-h-0
    内滚 / h-dvh / 触控目标 ≥40px。空数组不渲染（父层挂载守卫的双保险）。
    选中态经 emit('select', taskId|null) 上抛 ChatPage 写
    subagentWatchingTaskId（M1 终态静默重拉钩子的 watching 判据）。
  -->
  <div v-if="open && subagents.length" data-testid="chat-subagent-panel">
    <!-- 遮罩：移动全屏态打底，桌面 drawer 点击空白处关闭 -->
    <div
      data-testid="chat-subagent-panel-backdrop"
      class="fixed inset-0 z-[940] bg-black/50"
      @click="emit('close')"
    ></div>

    <section
      class="fixed inset-0 z-[950] flex h-dvh flex-col border-l border-slate-200 bg-white shadow-xl md:left-auto md:w-[36rem] dark:border-slate-700 dark:bg-slate-900"
      role="dialog"
      :aria-label="t('chat_subagentEntry')"
    >
      <!-- 头：标题+运行计数+关闭（触控≥40px） -->
      <header class="flex shrink-0 items-center gap-2 border-b border-slate-200 px-4 py-2 dark:border-slate-700">
        <h2 class="text-sm font-bold text-slate-900 dark:text-white">{{ t('chat_subagentEntry') }}</h2>
        <span v-if="runningCount > 0" class="text-xs text-primary">{{ t('chat_subagentEntryRunning', { n: runningCount }) }}</span>
        <button
          type="button"
          data-testid="chat-subagent-panel-close"
          class="ml-auto flex min-h-[40px] min-w-[40px] items-center justify-center rounded text-slate-500 hover:bg-slate-100 dark:text-slate-400 dark:hover:bg-slate-800"
          :aria-label="t('chat_subagentPanelClose')"
          @click="emit('close')"
        >
          <span class="material-symbols-outlined text-lg leading-none">close</span>
        </button>
      </header>

      <!-- 主体：移动端列表/详情互斥（全屏切换），桌面双栏并排 -->
      <div class="relative flex min-h-0 flex-1 flex-col md:flex-row">
        <!-- 左列表 -->
        <div
          data-testid="chat-subagent-panel-list"
          class="min-h-0 flex-1 overflow-y-auto md:w-60 md:flex-none md:border-r md:border-slate-200 md:dark:border-slate-700"
          :class="selected ? 'hidden md:block' : ''"
        >
          <button
            v-for="s in subagents"
            :key="s.taskId"
            type="button"
            data-testid="chat-subagent-panel-item"
            class="flex min-h-[44px] w-full items-center gap-2 border-b border-slate-100 px-3 py-2 text-left transition hover:bg-slate-100 dark:border-slate-800 dark:hover:bg-slate-800"
            :class="s.taskId === selectedTaskId
              ? 'bg-primary/10'
              : 'bg-transparent'"
            @click="select(s.taskId)"
          >
            <span
              class="material-symbols-outlined shrink-0 text-base leading-none"
              :class="rowToneClass(s)"
            >
              {{ subagentPhaseMeta(s.phase).icon }}
            </span>
            <span
              v-if="subagentPhaseMeta(s.phase).tone === 'running'"
              data-testid="chat-subagent-item-spinner"
              class="material-symbols-outlined shrink-0 animate-spin text-sm leading-none text-primary"
            >progress_activity</span>
            <span class="min-w-0 flex-1">
              <span class="block truncate text-sm font-medium text-slate-900 dark:text-white">{{ s.name || s.taskId }}</span>
              <span v-if="rowDescription(s)" class="block truncate text-xs text-slate-500 dark:text-slate-400">{{ rowDescription(s) }}</span>
            </span>
            <span class="shrink-0 text-right text-[11px] leading-tight text-slate-400 dark:text-slate-500">
              <span v-if="formatSubagentDuration(s.durationMs)" class="block">{{ formatSubagentDuration(s.durationMs) }}</span>
              <span v-if="formatSubagentTokens(s.totalTokens)" class="block">{{ formatSubagentTokens(s.totalTokens) }}</span>
            </span>
          </button>
        </div>

        <!-- 右详情：移动端绝对定位盖过列表（返回键回列表），桌面 flex-1 双栏 -->
        <div
          v-if="selected"
          class="absolute inset-0 z-10 flex min-h-0 flex-col bg-white md:static md:z-auto md:flex-1 md:border-l md:border-slate-200 md:dark:border-slate-700 dark:bg-slate-900"
        >
          <button
            type="button"
            data-testid="chat-subagent-panel-back"
            class="flex min-h-[40px] shrink-0 items-center gap-1 border-b border-slate-200 px-3 text-xs text-slate-500 hover:bg-slate-100 md:hidden dark:border-slate-700 dark:text-slate-400 dark:hover:bg-slate-800"
            @click="select('')"
          >
            <span class="material-symbols-outlined text-base leading-none">arrow_back</span>
            {{ t('chat_subagentPanelBack') }}
          </button>
          <SubagentDetail :subagent="selected" />
        </div>
      </div>
    </section>
  </div>
</template>

<script setup>
// 面板（列表+详情壳）。选中真值存本组件 selectedTaskId，详情对象按 taskId
// 从 props.subagents 派生（computed）——详情重拉整体换数组引用后同 taskId
// 自动跟住新对象（报告/时间线补齐即显），选中不丢、不重复发 select。
import { computed, onMounted, ref, watch } from 'vue';
import { useI18n } from '../../i18n';
import {
  subagentPhaseMeta,
  formatSubagentDuration,
  formatSubagentTokens,
} from '../../utils/subagentPanel';
import SubagentDetail from './SubagentDetail.vue';

const props = defineProps({
  open: { type: Boolean, default: false },
  subagents: { type: Array, default: () => [] },
});
const emit = defineEmits(['close', 'select']);
const { t } = useI18n();

const RUNNING_TONE_CLASSES = {
  running: 'text-primary',
  queued: 'text-sky-500',
  done: 'text-primary',
  failed: 'text-red-500',
  interrupted: 'text-amber-500',
};

function rowToneClass(s) {
  return RUNNING_TONE_CLASSES[subagentPhaseMeta(s?.phase).tone] || 'text-slate-400';
}

function rowDescription(s) {
  const d = s?.description;
  return typeof d === 'string' && d ? d : '';
}

const runningCount = computed(() =>
  (props.subagents || []).filter((s) => s && subagentPhaseMeta(s.phase).tone === 'running').length
);

const selectedTaskId = ref('');
const selected = computed(() =>
  (props.subagents || []).find((s) => s && s.taskId === selectedTaskId.value) || null
);

function select(taskId) {
  const next = taskId || '';
  if (selectedTaskId.value === next) return;
  selectedTaskId.value = next;
  // 取消选中/列表空 → 上抛 null（ChatPage 清 watchingTaskId，M1 钩子失活）
  emit('select', next || null);
}

// 打开即选中首条：桌面双栏右侧不空白；选中即写 watching，M1 completed 静默
// 重拉从打开面板这一刻起生效（spec §三「面板打开时拉」与「正在看时重拉」衔接）。
function selectFirst(list) {
  const first = (Array.isArray(list) ? list : []).find((s) => s && s.taskId) || null;
  select(first ? first.taskId : '');
}

onMounted(() => {
  if (props.open) selectFirst(props.subagents);
});

// 详情重拉/WS 聚合换数组后维护选中：仍在则不动（computed 已跟新对象），
// 丢失（条目被删/会话切换清空）则归位首条或清空。
watch(() => props.subagents, (list) => {
  if (selected.value) return;
  if (props.open) selectFirst(list);
});
</script>
