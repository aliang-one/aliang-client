<template>
  <!-- 子agent状态入口条（Task 7, spec 2026-11-07-subagent-panel §三）：会话页
       常驻入口条。挂载由父层控制（agentcore 会话且 activeSubagents.length>0），
       组件内再对空数组渲染为空（双保险）；整条可点击 emit('open') 由 ChatPage
       置面板打开标志（面板本体 Task 8）。 -->
  <button
    v-if="subagents.length"
    type="button"
    data-testid="chat-subagent-entry"
    class="mx-4 mb-2 flex min-h-[40px] shrink-0 items-center gap-2 rounded-lg border border-slate-200 bg-white px-3 py-1 text-xs text-slate-600 text-left transition hover:bg-slate-100 focus-visible:ring-2 focus-visible:ring-primary/30 md:min-h-0 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-300 dark:hover:bg-slate-800"
    :aria-label="entryAriaLabel"
    @click="emit('open')"
  >
    <span
      v-if="runningCount > 0"
      class="inline-block h-1.5 w-1.5 shrink-0 animate-pulse rounded-full bg-primary"
    ></span>
    <span class="font-semibold shrink-0">{{ t('chat_subagentEntry') }}</span>
    <span v-if="runningCount > 0" class="shrink-0 text-primary">
      {{ t('chat_subagentEntryRunning', { n: runningCount }) }}
    </span>
    <span v-if="queuedCount > 0" class="shrink-0 text-sky-500">
      {{ t('chat_subagentEntryQueued', { n: queuedCount }) }}
    </span>
    <span v-if="doneCount > 0" class="shrink-0 text-slate-400 dark:text-slate-500">
      {{ t('chat_subagentEntryDone', { n: doneCount }) }}
    </span>
    <span class="ml-auto flex shrink-0 items-center gap-1 text-slate-400 dark:text-slate-500">
      {{ t('chat_subagentEntryView') }}
      <span class="material-symbols-outlined text-sm">chevron_right</span>
    </span>
  </button>
</template>

<script setup>
import { computed } from 'vue';
import { useI18n } from '../../i18n';

// 纯渲染：子agent状态入口条。subagents 数组由 useAliangChat.activeSubagents
// 下传（reducer applySubagentEvent 的不可变更新结果），phase 词表与
// utils/subagentPanel.js 同源：started/progress=运行中，
// completed/failed/interrupted=终态（计数侧不重复词表，与 ACTIVE/TERMINAL
// 集合保持语义一致——组件只读，不维护第二份真相）。
const props = defineProps({
  subagents: { type: Array, default: () => [] },
});
const emit = defineEmits(['open']);
const { t } = useI18n();

// 运行中数：started/progress；排队数：queued（2026-11-10 ③ 排队可见化——
// 帧态相位，不算 running 不参与 pulse，但入口条必须有计数，否则「2 运行 +
// 1 排队」与只派两个不可区分）；完成数：completed/failed/interrupted。
// 未知 phase 两侧都不计（reducer 未知帧原样返回，这里同口径容错）。
const RUNNING_PHASES = ['started', 'progress'];
const QUEUED_PHASES = ['queued'];
const DONE_PHASES = ['completed', 'failed', 'interrupted'];
const countByPhases = (phases) => (props.subagents || []).filter(
  s => s && phases.includes(s.phase)
).length;

const runningCount = computed(() => countByPhases(RUNNING_PHASES));
const queuedCount = computed(() => countByPhases(QUEUED_PHASES));
const doneCount = computed(() => countByPhases(DONE_PHASES));

const entryAriaLabel = computed(() => {
  const parts = [t('chat_subagentEntry')];
  if (runningCount.value > 0) parts.push(t('chat_subagentEntryRunning', { n: runningCount.value }));
  if (queuedCount.value > 0) parts.push(t('chat_subagentEntryQueued', { n: queuedCount.value }));
  if (doneCount.value > 0) parts.push(t('chat_subagentEntryDone', { n: doneCount.value }));
  return parts.join('，');
});
</script>
